package logparser

import (
	"errors"
	"testing"
	"time"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/libs/types"
)

func TestParserIntegration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     collectorconfig.ResolvedApp
		line    string
		wantErr bool
		check   func(t *testing.T, got types.LogEntry)
	}{
		{
			name: "json parser parses mapped fields",
			cfg: collectorconfig.ResolvedApp{
				Source:          "/var/log/app.log",
				ParserKind:      "json",
				TimestampField:  "timestamp",
				TimestampFormat: time.RFC3339,
				LevelField:      "level",
				TraceIDField:    "trace_id",
				MessageField:    "message",
			},
			line: `{"timestamp":"2026-02-23T10:11:12Z","level":"warning","trace_id":"tr-1","message":"hello","user":"u1"}`,
			check: func(t *testing.T, got types.LogEntry) {
				t.Helper()
				if got.Source != "/var/log/app.log" {
					t.Fatalf("unexpected source: %q", got.Source)
				}
				if got.Level != types.LevelWarn {
					t.Fatalf("unexpected level: %q", got.Level)
				}
				if got.TraceID != "tr-1" {
					t.Fatalf("unexpected trace id: %q", got.TraceID)
				}
				if got.Message != "hello" {
					t.Fatalf("unexpected message: %q", got.Message)
				}
				if got.Extra["user"] != "u1" {
					t.Fatalf("expected user in extra, got: %#v", got.Extra)
				}
			},
		},
		{
			name: "json parser fails on bad timestamp",
			cfg: collectorconfig.ResolvedApp{
				Source:          "/var/log/app.log",
				ParserKind:      "json",
				TimestampField:  "timestamp",
				TimestampFormat: time.RFC3339,
				LevelField:      "level",
				TraceIDField:    "trace_id",
				MessageField:    "message",
			},
			line:    `{"timestamp":"not-a-time","level":"info","trace_id":"tr-1","message":"hello"}`,
			wantErr: true,
		},
		{
			name: "regex parser parses named groups",
			cfg: collectorconfig.ResolvedApp{
				Source:          "/var/log/regex.log",
				ParserKind:      "regex",
				Pattern:         `^(?P<timestamp>\S+) \[(?P<level>[A-Z]+)\] trace=(?P<trace_id>\S+) msg="(?P<message>[^"]+)"$`,
				TimestampField:  "timestamp",
				TimestampFormat: time.RFC3339,
				LevelField:      "level",
				TraceIDField:    "trace_id",
				MessageField:    "message",
			},
			line: `2026-02-23T10:11:12Z [ERROR] trace=tr-2 msg="boom"`,
			check: func(t *testing.T, got types.LogEntry) {
				t.Helper()
				if got.Level != types.LevelError {
					t.Fatalf("unexpected level: %q", got.Level)
				}
				if got.TraceID != "tr-2" {
					t.Fatalf("unexpected trace id: %q", got.TraceID)
				}
				if got.Message != "boom" {
					t.Fatalf("unexpected message: %q", got.Message)
				}
			},
		},
		{
			name: "text parser parses timestamp level and trace id",
			cfg: collectorconfig.ResolvedApp{
				Source:          "/var/log/text.log",
				ParserKind:      "text",
				TimestampRegex:  `^(\S+)`,
				TimestampFormat: time.RFC3339,
				LevelRegex:      `\b(INFO|WARN|ERROR|DEBUG)\b`,
				TraceIDRegex:    `trace=([^\s]+)`,
			},
			line: `2026-02-23T10:11:12Z INFO trace=tr-3 payment failed`,
			check: func(t *testing.T, got types.LogEntry) {
				t.Helper()
				if got.Level != types.LevelInfo {
					t.Fatalf("unexpected level: %q", got.Level)
				}
				if got.TraceID != "tr-3" {
					t.Fatalf("unexpected trace id: %q", got.TraceID)
				}
				if got.Message == "" {
					t.Fatalf("expected message")
				}
			},
		},
		{
			name: "text parser fails when timestamp missing",
			cfg: collectorconfig.ResolvedApp{
				Source:          "/var/log/text.log",
				ParserKind:      "text",
				TimestampRegex:  `^(\S+)`,
				TimestampFormat: time.RFC3339,
			},
			line:    `no timestamp here`,
			wantErr: true,
		},
		{
			name: "journalctl parser parses envelope",
			cfg: collectorconfig.ResolvedApp{
				Source:     "journalctl:api.service",
				ParserKind: "journalctl",
				Unit:       "api.service",
			},
			line: `{"__REALTIME_TIMESTAMP":"1708683072000000","PRIORITY":"4","MESSAGE":"service warning","_SYSTEMD_UNIT":"api.service"}`,
			check: func(t *testing.T, got types.LogEntry) {
				t.Helper()
				if got.Level != types.LevelWarn {
					t.Fatalf("unexpected level: %q", got.Level)
				}
				if got.Message != "service warning" {
					t.Fatalf("unexpected message: %q", got.Message)
				}
				if got.Extra["_SYSTEMD_UNIT"] != "api.service" {
					t.Fatalf("expected _SYSTEMD_UNIT in extra, got %#v", got.Extra)
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, err := New(tc.cfg)
			if err != nil {
				t.Fatalf("new parser: %v", err)
			}

			got, err := p.Parse(tc.line)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil and entry=%+v", got)
				}
				if !errors.Is(err, ErrParseFailed) {
					t.Fatalf("expected ErrParseFailed, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
			if got.Timestamp.IsZero() {
				t.Fatalf("timestamp should be set")
			}
			if got.Raw == "" {
				t.Fatalf("raw should be set")
			}
		})
	}

	t.Run("unknown parser kind", func(t *testing.T) {
		t.Parallel()
		_, err := New(collectorconfig.ResolvedApp{ParserKind: "unknown"})
		if !errors.Is(err, ErrUnknownFormat) {
			t.Fatalf("expected ErrUnknownFormat, got: %v", err)
		}
	})

}
