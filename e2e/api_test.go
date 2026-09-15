package e2e

import (
	"fmt"
	"os"
	"testing"
)

// TestSetupAPI verifies that the setup API works correctly.
func TestSetupAPI(t *testing.T) {
	// This test requires a running panel and collector.
	// It's skipped in unit tests and run in the E2E environment.
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	baseURL := os.Getenv("DEMO_URL")
	if baseURL == "" {
		t.Skip("skipping: DEMO_URL not set")
	}

	apiKey := os.Getenv("DEMO_API_KEY")
	if apiKey == "" {
		t.Skip("skipping: DEMO_API_KEY not set")
	}

	client := NewSetupClient(baseURL, apiKey)

	config := SetupConfig{
		Apps: []AppSpec{
			{
				Name:      "demo-api",
				Parser:    &ParserSpec{Type: "json"},
				Source:    &SourceSpec{Type: "journalctl", Params: map[string]string{"unit": "demo-api.service"}},
				Lifecycle: &LifecycleSpec{Type: "systemd"},
			},
		},
	}

	err := client.PutCollectorConfig("demo", config)
	if err != nil {
		t.Fatalf("PutCollectorConfig failed: %v", err)
	}

	fmt.Println("Setup API test passed")
}
