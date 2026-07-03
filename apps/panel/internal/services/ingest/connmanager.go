package ingest

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/types"
)

// ConnManager tracks WebSocket connections per collector name,
// allowing the panel to send messages to connected collectors.
type ConnManager struct {
	mu    sync.RWMutex
	conns map[string]*websocket.Conn // collector name -> conn
}

// NewConnManager creates a new connection manager.
func NewConnManager() *ConnManager {
	return &ConnManager{
		conns: make(map[string]*websocket.Conn),
	}
}

// Register associates a WebSocket connection with a collector name.
// If a previous connection exists for the same collector, it is closed.
func (cm *ConnManager) Register(collector string, conn *websocket.Conn) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if existing, ok := cm.conns[collector]; ok {
		slog.Debug("Closing previous connection for collector", "collector", collector)
		existing.Close()
	}
	cm.conns[collector] = conn
	slog.Debug("Registered WebSocket connection", "collector", collector)
}

// Unregister removes a WebSocket connection for a collector.
func (cm *ConnManager) Unregister(collector string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	delete(cm.conns, collector)
	slog.Debug("Unregistered WebSocket connection", "collector", collector)
}

// SendUpdate sends an update message to a specific collector.
// Returns true if the message was sent successfully.
func (cm *ConnManager) SendUpdate(collector string) bool {
	cm.mu.RLock()
	conn, ok := cm.conns[collector]
	cm.mu.RUnlock()

	if !ok {
		return false
	}

	msg := types.UpdateMessage{
		Type: types.EventTypeUpdate,
	}

	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(msg); err != nil {
		slog.Error("Failed to send update message to collector",
			"collector", collector, "error", err)
		return false
	}

	slog.Info("Sent update message to collector", "collector", collector)
	return true
}

// SendUpdateAll sends an update message to all connected collectors.
// Returns the number of collectors the message was sent to.
func (cm *ConnManager) SendUpdateAll() int {
	cm.mu.RLock()
	collectors := make([]string, 0, len(cm.conns))
	for name := range cm.conns {
		collectors = append(collectors, name)
	}
	cm.mu.RUnlock()

	var sent int
	for _, name := range collectors {
		if cm.SendUpdate(name) {
			sent++
		}
	}
	return sent
}

// IsConnected returns true if a collector has an active WebSocket connection.
func (cm *ConnManager) IsConnected(collector string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	_, ok := cm.conns[collector]
	return ok
}

// ListConnected returns the names of all connected collectors.
func (cm *ConnManager) ListConnected() []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	names := make([]string, 0, len(cm.conns))
	for name := range cm.conns {
		names = append(names, name)
	}
	return names
}

// EncodeUpdateJSON returns a JSON-encoded update message for use with
// conn.WriteMessage(websocket.TextMessage, data).
func EncodeUpdateJSON() ([]byte, error) {
	return json.Marshal(types.UpdateMessage{
		Type: types.EventTypeUpdate,
	})
}
