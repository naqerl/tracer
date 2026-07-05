package ingest

import (
	"log/slog"
	"sync"

	"tracer/libs/shared/wsconn"
	"tracer/libs/types"
)

// ConnManager tracks collector WebSocket connections so the panel can
// send messages back to collectors (e.g., update commands).
type ConnManager struct {
	mu    sync.RWMutex
	conns map[string]*wsconn.Conn
}

// NewConnManager creates a new ConnManager.
func NewConnManager() *ConnManager {
	return &ConnManager{
		conns: make(map[string]*wsconn.Conn),
	}
}

// Register stores a connection keyed by connID. If a connection
// for this connID already exists, it is closed.
func (cm *ConnManager) Register(connID string, c *wsconn.Conn) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if existing, ok := cm.conns[connID]; ok {
		slog.Debug("Replacing existing connection", "conn_id", connID)
		existing.Close()
	}

	cm.conns[connID] = c
	slog.Debug("Connection registered", "conn_id", connID)
}

// Unregister removes a connection by connID and closes it.
// It returns true if a registered connection was found and closed.
// It is safe to call multiple times.
func (cm *ConnManager) Unregister(connID string) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if c, ok := cm.conns[connID]; ok {
		c.Close()
		delete(cm.conns, connID)
		slog.Debug("Connection unregistered", "conn_id", connID)
		return true
	}
	return false
}

// SendUpdate sends an update command to all connected collectors.
// Returns the number of collectors the message was sent to.
// The read lock is held for the entire iteration to prevent concurrent
// modification of the connection map by Register/Unregister.
func (cm *ConnManager) SendUpdate() int {
	msg := types.MessageHeader{Type: types.EventTypeUpdate}

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if len(cm.conns) == 0 {
		slog.Debug("No connected collectors to send update")
		return 0
	}

	slog.Info("Sending update command to collectors", "count", len(cm.conns))

	sent := 0
	for _, c := range cm.conns {
		select {
		case c.Outgoing() <- msg:
			sent++
		default:
			slog.Warn("Write buffer full, dropping update for collector")
		}
	}

	return sent
}

// ConnectedCount returns the number of currently connected collectors.
func (cm *ConnManager) ConnectedCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.conns)
}
