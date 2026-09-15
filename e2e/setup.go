package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SetupConfig represents the collector configuration sent to the panel.
type SetupConfig struct {
	Apps []AppSpec `json:"apps"`
}

type ParserSpec struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params,omitempty"`
}

type AppSpec struct {
	Name      string         `json:"name"`
	Parser    *ParserSpec    `json:"parser,omitempty"`
	Source    *SourceSpec    `json:"source,omitempty"`
	Lifecycle *LifecycleSpec `json:"lifecycle,omitempty"`
	Health    *HealthSpec    `json:"health,omitempty"`
}

type SourceSpec struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

type LifecycleSpec struct {
	Type string `json:"type"`
}

type HealthSpec struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

// SetupClient provides helpers for configuring the tracer via the panel API.
type SetupClient struct {
	BaseURL string
	APIKey  string
}

// NewSetupClient creates a new SetupClient.
func NewSetupClient(baseURL, apiKey string) *SetupClient {
	return &SetupClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
	}
}

// PutCollectorConfig sends the configuration to the panel.
func (c *SetupClient) PutCollectorConfig(collectorName string, config SetupConfig) error {
	b, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}

	url := fmt.Sprintf("%s/ingest/config/%s", c.BaseURL, collectorName)
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(b)))
	if err != nil {
		return fmt.Errorf("could not create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
