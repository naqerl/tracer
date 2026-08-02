package sender

import "testing"

func TestPanelWSURL(t *testing.T) {
	tests := []struct {
		name      string
		panelHost string
		want      string
		wantErr   bool
	}{
		{"bare host defaults to wss", "tracer.naqerl.com", "wss://tracer.naqerl.com/ingest/ws", false},
		{"bare host with port defaults to ws", "localhost:8080", "ws://localhost:8080/ingest/ws", false},
		{"bare host with trailing whitespace", "  tracer.naqerl.com  ", "wss://tracer.naqerl.com/ingest/ws", false},
		{"explicit ws", "ws://panel.local:9000", "ws://panel.local:9000/ingest/ws", false},
		{"explicit wss", "wss://tracer.naqerl.com", "wss://tracer.naqerl.com/ingest/ws", false},
		{"http maps to ws", "http://panel.local:8080", "ws://panel.local:8080/ingest/ws", false},
		{"https maps to wss", "https://tracer.naqerl.com", "wss://tracer.naqerl.com/ingest/ws", false},
		{"empty host", "", "", true},
		{"missing host after scheme", "wss://", "", true},
		{"unsupported scheme", "ftp://tracer.naqerl.com", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := panelWSURL(tt.panelHost)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("panelWSURL(%q) error = nil, want error", tt.panelHost)
				}
				return
			}
			if err != nil {
				t.Fatalf("panelWSURL(%q) unexpected error: %v", tt.panelHost, err)
			}
			if got != tt.want {
				t.Errorf("panelWSURL(%q) = %q, want %q", tt.panelHost, got, tt.want)
			}
		})
	}
}
