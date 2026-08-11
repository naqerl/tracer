package ntfy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tracer/apps/panel/internal/services/notify"
)

// Config represents ntfy notifier configuration.
type Config struct {
	URL   string `json:"url"`
	Topic string `json:"topic"`
}

// Notifier sends notifications via ntfy.
type Notifier struct {
	name   string
	url    string
	topic  string
	client *http.Client
}

// New creates a new ntfy notifier.
func New(name, url, topic string) (*Notifier, error) {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	topic = strings.TrimSpace(topic)
	if url == "" || topic == "" {
		return nil, fmt.Errorf("url and topic are required")
	}

	return &Notifier{
		name:  name,
		url:   url,
		topic: topic,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

// Type returns the notifier type.
func (n *Notifier) Type() string {
	return "ntfy"
}

// Name returns the user-defined name for this notifier instance.
func (n *Notifier) Name() string {
	return n.name
}

// Notify sends a notification rendered from the structured data.
func (n *Notifier) Notify(notification notify.Notification) error {
	req, err := http.NewRequest(http.MethodPost, n.url+"/"+n.topic, bytes.NewBufferString(notify.FormatMessage(notification)))
	if err != nil {
		return fmt.Errorf("build ntfy request: %w", err)
	}

	req.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	req.Header.Set("Markdown", "yes")
	req.Header.Set("Tags", string(notification.Type))

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("send ntfy notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ntfy returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return nil
}

// FromRawJSON creates an ntfy notifier from config string (JSON).
func FromRawJSON(name, config string) (notify.Notifier, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(config), &cfg); err != nil {
		return nil, fmt.Errorf("parse ntfy config: %w", err)
	}

	return New(name, cfg.URL, cfg.Topic)
}
