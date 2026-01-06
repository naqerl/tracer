package wsclient

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ai-shift/tracer/core/services/filewatcher"
	"github.com/ai-shift/tracer/core/services/logparser"
)

const (
	writeTimeout     = 10 * time.Second
	reconnectDelay   = 5 * time.Second
	pingInterval     = 30 * time.Second
	maxReconnectWait = 5 * time.Minute
)

type Message struct {
	Type          string             `json:"type"` // "register" or "log"
	AppName       string             `json:"app_name"`
	Apps          []string           `json:"apps,omitempty"`           // for registration
	CollectorName string             `json:"collector_name,omitempty"` // for registration
	Entry         logparser.LogEntry `json:"entry,omitempty"`
}

type Service struct {
	endpoint      string
	collectorName string
	appNames      []string
	logChan       <-chan filewatcher.AppLogEntry

	conn   *websocket.Conn
	connMu sync.Mutex
}

func New(endpoint, collectorName string, appNames []string, logChan <-chan filewatcher.AppLogEntry) *Service {
	return &Service{
		endpoint:      endpoint,
		collectorName: collectorName,
		appNames:      appNames,
		logChan:       logChan,
	}
}

func (s *Service) Start(ctx context.Context) {
	go s.maintainConnection(ctx)
	s.processLogs(ctx)
}

func (s *Service) maintainConnection(ctx context.Context) {
	reconnectWait := reconnectDelay

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := s.connect(); err != nil {
			slog.Error("Failed to connect to panel", "endpoint", s.endpoint, "error", err)
			time.Sleep(reconnectWait)
			// Exponential backoff
			reconnectWait = min(reconnectWait*2, maxReconnectWait)
			continue
		}

		// Reset backoff on successful connection
		reconnectWait = reconnectDelay
		slog.Info("Connected to panel", "endpoint", s.endpoint)

		// Send registration message
		s.sendRegistration()

		// Keep connection alive with pings
		s.pingLoop(ctx)
	}
}

func (s *Service) connect() error {
	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.conn != nil {
		s.conn.Close()
	}

	conn, _, err := websocket.DefaultDialer.Dial(s.endpoint, nil)
	if err != nil {
		return err
	}

	s.conn = conn
	return nil
}

func (s *Service) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.connMu.Lock()
			if s.conn == nil {
				s.connMu.Unlock()
				return
			}
			err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout))
			s.connMu.Unlock()

			if err != nil {
				slog.Warn("Ping failed, reconnecting", "error", err)
				return
			}
		}
	}
}

func (s *Service) processLogs(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.close()
			return
		case entry, ok := <-s.logChan:
			if !ok {
				s.close()
				return
			}
			s.sendLog(entry)
		}
	}
}

func (s *Service) sendRegistration() {
	msg := Message{
		Type:          "register",
		Apps:          s.appNames,
		CollectorName: s.collectorName,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Failed to marshal registration", "error", err)
		return
	}

	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.conn == nil {
		return
	}

	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		slog.Warn("Failed to send registration", "error", err)
	}
}

func (s *Service) sendLog(appLog filewatcher.AppLogEntry) {
	msg := Message{
		Type:    "log",
		AppName: appLog.AppName,
		Entry:   appLog.Entry,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Failed to marshal log entry", "error", err)
		return
	}

	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.conn == nil {
		slog.Warn("No connection, dropping log entry")
		return
	}

	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		slog.Warn("Failed to send log entry", "error", err)
		s.conn.Close()
		s.conn = nil
	}
}

func (s *Service) close() {
	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.conn != nil {
		s.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(writeTimeout),
		)
		s.conn.Close()
		s.conn = nil
	}
}
