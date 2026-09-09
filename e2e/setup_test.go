package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetupClient_PutCollectorConfig(t *testing.T) {
	// Mock server
	var receivedConfig SetupConfig
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.Header.Get("X-API-Key") != "test-api-key" {
			t.Errorf("expected X-API-Key test-api-key, got %s", r.Header.Get("X-API-Key"))
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&receivedConfig); err != nil {
			t.Fatalf("could not decode request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewSetupClient(server.URL, "test-api-key")

	config := SetupConfig{
		Parsers: map[string]ParserSpec{
			"json-parser": {Type: "json"},
		},
		Apps: []AppSpec{
			{
				Name:      "demo-api",
				ParserID:  "json-parser",
				Source:    &SourceSpec{Type: "journalctl", Params: map[string]string{"unit": "demo-api.service"}},
				Lifecycle: &LifecycleSpec{Type: "systemd"},
			},
		},
	}

	if err := client.PutCollectorConfig("demo", config); err != nil {
		t.Fatalf("PutCollectorConfig failed: %v", err)
	}

	if len(receivedConfig.Apps) != 1 {
		t.Errorf("expected 1 app, got %d", len(receivedConfig.Apps))
	}
	if receivedConfig.Apps[0].Name != "demo-api" {
		t.Errorf("expected app name demo-api, got %s", receivedConfig.Apps[0].Name)
	}
}
