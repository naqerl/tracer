package ingest

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeChanCapacity = 32
	writeTimeout      = 10 * time.Second
)

// ConnManager tracks collector WebSocket connections so the panel can
// send messages back to collectors (e.g., update commands).
// All writes are serialized through per-connection write channels
// to satisfy gorilla/websocket's single-writer requirement.
type ConnManager struct {
	mu    sync.RWMutex
	conns map[string]*connWriter
}

// connWriter wraps a WebSocket connection with a write channel that
// serializes all outgoing messages through a single goroutine.
type connWriter struct {
	conn    *websocket.Conn
	writeCh chan []byte
	done    chan struct{}
	once    sync.Once
}

func newConnWriter(conn *websocket.Conn) *connWriter {
	cw := &connWriter{
		conn:    conn,
		writeCh: make(chan []byte, writeChanCapacity),
		done:    make(chan struct{}),
	}
	go cw.writeLoop()
	return cw
}

// writeLoop is the single goroutine that writes to the underlying conn.
func (cw *connWriter) writeLoop() {
	defer close(cw.done)
	for data := range cw.writeCh {
		cw.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := cw.conn.WriteMessage(websocket.TextMessage, data); err != nil {
			slog.Error("WebSocket write failed", "error", err)
			return
		}
	}
}

// WriteJSON marshals v and sends it through the write channel.
func (cw *connWriter) WriteJSON(v any) error {
	msg, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}
	select {
	case cw.writeCh <- msg:
		return nil
	case <-cw.done:
		return fmt.Errorf("connection closed")
	default:
		// Non-blocking: drop if buffer full to avoid blocking the sender.
		slog.Warn("Write buffer full, dropping message")
		return nil
	}
}

// Close gracefully shuts down the writer and closes the underlying conn.
func (cw *connWriter) Close() {
	cw.once.Do(func() {
		close(cw.writeCh)
		<-cw.done // wait for writeLoop to exit
		cw.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(writeTimeout),
		)
		cw.conn.Close()
	})
}

// NewConnManager creates a new ConnManager.
func NewConnManager() *ConnManager {
	return &ConnManager{
		conns: make(map[string]*connWriter),
	}
}

// Register stores or replaces a connection keyed by connID. If a connection
// for this connID already exists, it is closed.
func (cm *ConnManager) Register(connID string, conn *websocket.Conn) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if existing, ok := cm.conns[connID]; ok {
		slog.Debug("Replacing existing connection", "conn_id", connID)
		existing.Close()
	}

	cw := newConnWriter(conn)
	cm.conns[connID] = cw
	slog.Debug("Connection registered", "conn_id", connID)
}

// Unregister removes a connection by connID and closes it.
// It returns true if a registered connection was found and closed.
// It is safe to call multiple times.
func (cm *ConnManager) Unregister(connID string) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cw, ok := cm.conns[connID]; ok {
		cw.Close()
		delete(cm.conns, connID)
		slog.Debug("Connection unregistered", "conn_id", connID)
		return true
	}
	return false
}

// SendUpdate sends an update command to all connected collectors.
// Returns the number of collectors the message was sent to.
func (cm *ConnManager) SendUpdate() int {
	msg := struct {
		Type string `json:"type"`
	}{
		Type: "update",
	}

	cm.mu.RLock()
	names := make([]string, 0, len(cm.conns))
	for name := range cm.conns {
		names = append(names, name)
	}
	cm.mu.RUnlock()

	if len(names) == 0 {
		slog.Debug("No connected collectors to send update")
		return 0
	}

	slog.Info("Sending update command to collectors", "count", len(names))

	sent := 0
	for _, name := range names {
		cm.mu.RLock()
		cw, ok := cm.conns[name]
		cm.mu.RUnlock()

		if !ok {
			continue
		}

		if err := cw.WriteJSON(msg); err != nil {
			slog.Error("Failed to send update to collector", "collector", name, "error", err)
			continue
		}
		sent++
	}

	return sent
}

// ConnectedCount returns the number of currently connected collectors.
func (cm *ConnManager) ConnectedCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.conns)
}
