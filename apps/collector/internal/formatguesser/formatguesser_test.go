package formatguesser

import (
	"context"
	"io"
	"testing"

	"github.com/go-test/deep"

	"tracer/apps/collector/internal/parser"
)

func TestGuessFormat(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantSpec parser.Spec
		wantErr  bool
	}{
		// JSON format tests
		{
			name: "JSON with standard fields",
			line: `{"timestamp":"2026-03-10T19:05:18Z","level":"INFO","message":"Test message","trace_id":"abc123"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "timestamp",
					"level_field":      "level",
					"message_field":    "message",
					"trace_id_field":   "trace_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'ts' timestamp synonym",
			line: `{"ts":"2026-03-10T19:05:18.123Z","level":"ERROR","message":"Error occurred"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "ts",
					"level_field":      "level",
					"message_field":    "message",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'time' timestamp synonym",
			line: `{"time":"2026-03-10 19:05:18","severity":"WARN","msg":"Warning message"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "severity",
					"message_field":    "msg",
					"timestamp_format": "2006-01-02 15:04:05",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with '@timestamp' field",
			line: `{"@timestamp":"2026-03-10T19:05:18.123456Z","lvl":"DEBUG","body":"Debug info"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "@timestamp",
					"level_field":      "lvl",
					"message_field":    "body",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'created_at' timestamp",
			line: `{"created_at":"2026-03-10T19:05:18Z","status":"INFO","text":"Info message"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "created_at",
					"level_field":      "status",
					"message_field":    "text",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'log_level' synonym",
			line: `{"timestamp":"2026-03-10T19:05:18Z","log_level":"CRITICAL","content":"Critical error"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "timestamp",
					"level_field":      "log_level",
					"message_field":    "content",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'loglevel' synonym",
			line: `{"date":"2026-03-10T19:05:18Z","loglevel":"FATAL","description":"Fatal error"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "date",
					"level_field":      "loglevel",
					"message_field":    "description",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'severity' and 'priority'",
			line: `{"time_stamp":"2026-03-10T19:05:18Z","severity":"ERROR","summary":"Error summary"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "time_stamp",
					"level_field":      "severity",
					"message_field":    "summary",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'traceid' synonym",
			line: `{"timestamp":"2026-03-10T19:05:18Z","level":"INFO","message":"Test","traceid":"xyz789"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "timestamp",
					"level_field":      "level",
					"message_field":    "message",
					"trace_id_field":   "traceid",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'request_id' synonym",
			line: `{"event_time":"2026-03-10T19:05:18Z","prio":"NOTICE","payload":"Notice message","request_id":"req-123"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "event_time",
					"level_field":      "prio",
					"message_field":    "payload",
					"trace_id_field":   "request_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'correlation_id'",
			line: `{"published_at":"2026-03-10T19:05:18Z","alert_level":"WARNING","log_msg":"Warning log","correlation_id":"corr-456"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "published_at",
					"level_field":      "alert_level",
					"message_field":    "log_msg",
					"trace_id_field":   "correlation_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'span_id' synonym",
			line: `{"start_time":"2026-03-10T19:05:18Z","s":"TRACE","event_message":"Trace event","span_id":"span-001"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "start_time",
					"level_field":      "s",
					"message_field":    "event_message",
					"trace_id_field":   "span_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'err_msg' synonym",
			line: `{"recorded_at":"2026-03-10T19:05:18Z","syslog_priority":"ERR","err_msg":"Error message content"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "recorded_at",
					"level_field":      "syslog_priority",
					"message_field":    "err_msg",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'detail' synonym",
			line: `{"log_time":"2026-03-10T19:05:18Z","sev":"ALERT","detail":"Alert details here","x-request-id":"xreq-789"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "log_time",
					"level_field":      "sev",
					"message_field":    "detail",
					"trace_id_field":   "x-request-id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'info' synonym",
			line: `{"asctime":"2026-03-10T19:05:18Z","log.level":"INFO","info":"Information message"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "asctime",
					"level_field":      "log.level",
					"message_field":    "info",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name:    "JSON with 'statement' synonym - numeric timestamp (error case)",
			line:    `{"millis":1710177918123,"loglevel":"DEBUG","statement":"Debug statement"}`,
			wantErr: true,
		},
		{
			name:    "JSON with 'error_message' synonym - numeric timestamp (error case)",
			line:    `{"unix_timestamp":1710177918,"priority":"ERROR","error_message":"Error occurred","guid":"guid-123"}`,
			wantErr: true,
		},
		{
			name: "JSON with 'raw' synonym",
			line: `{"timestamp":"2026-03-10T19:05:18Z","lvl":"INFO","raw":"Raw log message"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "timestamp",
					"level_field":      "lvl",
					"message_field":    "raw",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'data' synonym",
			line: `{"timestamp":"2026-03-10T19:05:18Z","severity":"WARN","data":"Warning data","operation_id":"op-789"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "timestamp",
					"level_field":      "severity",
					"message_field":    "data",
					"trace_id_field":   "operation_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "JSON with 'tid' synonym",
			line: `{"timestamp":"2026-03-10T19:05:18Z","level":"INFO","message":"Test","tid":"tid-789"}`,
			wantSpec: parser.Spec{
				Type: "json",
				Params: map[string]string{
					"timestamp_field":  "timestamp",
					"level_field":      "level",
					"message_field":    "message",
					"trace_id_field":   "tid",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name:    "JSON without level field is rejected (level is required)",
			line:    `{"message": "104.28.196.105:0 - \"GET /api/v1/mobile/home HTTP/1.1\" 200", "timestamp": "2026-08-01T11:52:19.611285+00:00"}`,
			wantErr: true,
		},

		// Slogtext format tests
		{
			name: "Slogtext with standard fields",
			line: `time=2026-03-10T19:05:18.123Z level=INFO msg="Test message" trace_id=abc123`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "trace_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with ERROR level",
			line: `time=2026-03-10T19:05:18Z level=ERROR msg="Error occurred" request_id=req-123`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "request_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name:    "Slogtext with WARN level - quoted timestamp (error case)",
			line:    `time="2026-03-10 19:05:18" level=WARN msg="Warning message" correlation_id=corr-456`,
			wantErr: true,
		},
		{
			name: "Slogtext with DEBUG level",
			line: `time=2026-03-10T19:05:18.123456Z level=DEBUG msg="Debug info" span_id=span-001`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "span_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with TRACE level",
			line: `time=2026-03-10T19:05:18Z level=TRACE msg="Trace event" traceid=trace-123`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "traceid",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with FATAL level",
			line: `time=2026-03-10T19:05:18Z level=FATAL msg="Fatal error" operation_id=op-789`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "operation_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with different field order",
			line: `level=INFO msg="Message content" time=2026-03-10T19:05:18Z req_id=req-001`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "req_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with RFC3339Nano timestamp",
			line: `time=2026-03-10T19:05:18.123456789Z level=NOTICE msg="Notice message" x-trace-id=xtrace-123`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "x-trace-id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with message in quotes",
			line: `time=2026-03-10T19:05:18Z level=CRITICAL msg="Critical error with details" uid=user-123`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "uid",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with extra fields",
			line: `time=2026-03-10T19:05:18Z level=INFO msg="Request processed" service=api user=admin requestId=req-456`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "requestId",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with short timestamp",
			line: `time=2026-03-10T19:05:18Z level=ALERT msg="Alert message" tid=t-001`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "tid",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with body instead of msg",
			line: `time=2026-03-10T19:05:18Z level=EMERGENCY body="Emergency log" x-b3-traceid=b3-123`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "body",
					"trace_id_field":   "x-b3-traceid",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with text instead of msg",
			line: `time=2026-03-10T19:05:18Z level=WARNING text="Warning text" otel.trace_id=otel-123`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "text",
					"trace_id_field":   "otel.trace_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with log_msg instead of msg",
			line: `time=2026-03-10T19:05:18Z level=ERR log_msg="Error log message" traceId=tid-789`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "log_msg",
					"trace_id_field":   "traceId",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with summary instead of msg",
			line: `time=2026-03-10T19:05:18Z level=INFO summary="Summary message" correlationId=corr-abc`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "summary",
					"trace_id_field":   "correlationId",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name:    "Slogtext with timestamp in different format - quoted time (error case)",
			line:    `time="2026/03/10 19:05:18" level=INFO msg="Message" spanid=s-001`,
			wantErr: true,
		},
		{
			name: "Slogtext with description instead of msg",
			line: `time=2026-03-10T19:05:18Z level=INFO description="Description message" reqid=r-001`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "description",
					"trace_id_field":   "reqid",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with payload instead of msg",
			line: `time=2026-03-10T19:05:18Z level=INFO payload="Payload message" req_id=rq-001`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "payload",
					"trace_id_field":   "req_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with multiple key-value pairs",
			line: `time=2026-03-10T19:05:18Z level=INFO msg="Multi field message" service=api user=admin trace_id=t-999`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "trace_id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with quoted values containing spaces",
			line: `time=2026-03-10T19:05:18Z level=INFO msg="Message with multiple words" duration=123ms trace.id=t.id.001`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "msg",
					"trace_id_field":   "trace.id",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},
		{
			name: "Slogtext with content instead of msg",
			line: `time=2026-03-10T19:05:18Z level=INFO content="Content message" guid=g-001`,
			wantSpec: parser.Spec{
				Type: "slogtext",
				Params: map[string]string{
					"timestamp_field":  "time",
					"level_field":      "level",
					"message_field":    "content",
					"trace_id_field":   "guid",
					"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
				},
			},
			wantErr: false,
		},

		// Error cases - should return error
		{
			name:    "Plain text log",
			line:    "This is just a plain text log message without any structure",
			wantErr: true,
		},
		{
			name:    "Malformed JSON - missing closing brace",
			line:    `{"timestamp":"2026-03-10T19:05:18Z","level":"INFO"`,
			wantErr: true,
		},
		{
			name:    "Malformed JSON - invalid syntax",
			line:    `{"timestamp": "2026-03-10T19:05:18Z", "level":}`,
			wantErr: true,
		},
		{
			name:    "Syslog format",
			line:    "Mar 10 19:05:18 server application[1234]: Log message here",
			wantErr: true,
		},
		{
			name:    "Apache access log",
			line:    `127.0.0.1 - - [10/Mar/2026:19:05:18 +0000] "GET /api/test HTTP/1.1" 200 123`,
			wantErr: true,
		},
		{
			name:    "Simple comma-separated values",
			line:    "2026-03-10,INFO,Test message,abc123",
			wantErr: true,
		},
		{
			name:    "Logfmt without timestamp",
			line:    `level=INFO msg="Message without timestamp"`,
			wantErr: true,
		},
		{
			name:    "Logfmt without level is rejected (level is required)",
			line:    `time=2026-03-10T19:05:18Z msg="Message without level"`,
			wantErr: true,
		},
		{
			name:    "Logfmt without message",
			line:    `time=2026-03-10T19:05:18Z level=INFO`,
			wantErr: true,
		},
		{
			name:    "Empty string",
			line:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			curr, err := guessParserSpec(tt.line)

			if tt.wantErr != (err != nil) {
				t.Fatalf("guessFormat() error = %v, wantErr = %v", err, tt.wantErr)
			}

			if err == nil {
				if diff := deep.Equal(tt.wantSpec, curr); diff != nil {
					t.Errorf("diff = %v, want = %#v, got = %#v", diff, tt.wantSpec, curr)
				}
			}
		})
	}
}

func TestBuildParserSpecRejectsMixedFormatsByDefault(t *testing.T) {
	_, err := buildParserSpec(context.Background(), &stubSourceSeeker{
		lines: []string{
			`{"ts":"2026-03-10T19:05:18Z","level":"I","msg":"a"}`,
			`time=2026-03-10T19:06:18Z level=I msg=a`,
		},
	}, parser.Spec{})
	if err == nil {
		t.Fatal("buildParserSpec() error = nil, want mixed-format error")
	}
}

func TestBuildParserSpecResolvesFieldConflictWithFavour(t *testing.T) {
	// When favour spec provides a field value, use it to resolve conflicts
	result, err := buildParserSpec(context.Background(), &stubSourceSeeker{
		lines: []string{
			`{"ts":"2026-03-10T19:05:18Z","level":"I","msg":"a"}`,
			`{"ts":"2026-03-10T19:06:18Z","status":"E","msg":"b"}`,
		},
	}, parser.Spec{
		Params: map[string]string{
			"level_field": "level",
		},
	})
	if err != nil {
		t.Fatalf("buildParserSpec() error = %v", err)
	}

	// The favoured level_field should be used
	if result.Spec.Params["level_field"] != "level" {
		t.Fatalf("expected level_field='level', got %q", result.Spec.Params["level_field"])
	}
	// Other fields should still be detected
	if result.Spec.Params["message_field"] != "msg" {
		t.Fatalf("expected message_field='msg', got %q", result.Spec.Params["message_field"])
	}
}

func TestBuildParserSpecResolvesTimestampConflictWithFavour(t *testing.T) {
	// When favour spec provides timestamp field, use it to resolve conflicts
	result, err := buildParserSpec(context.Background(), &stubSourceSeeker{
		lines: []string{
			`{"ts":"2026-03-10T19:05:18Z","level":"I","msg":"a"}`,
			`{"time":"2026-03-10T19:06:18Z","level":"E","msg":"b"}`,
		},
	}, parser.Spec{
		Params: map[string]string{
			"timestamp_field": "ts",
		},
	})
	if err != nil {
		t.Fatalf("buildParserSpec() error = %v", err)
	}

	// The favoured timestamp_field should be used
	if result.Spec.Params["timestamp_field"] != "ts" {
		t.Fatalf("expected timestamp_field='ts', got %q", result.Spec.Params["timestamp_field"])
	}
}

type stubSourceSeeker struct {
	lines      []string
	idx        int
	seekCalled bool
}

func (s *stubSourceSeeker) Open(context.Context) error { return nil }

func (s *stubSourceSeeker) Next(context.Context) (string, error) {
	if s.seekCalled {
		s.seekCalled = false
		if len(s.lines) == 0 {
			return "", io.EOF
		}
		return s.lines[0], nil
	}
	if s.idx >= len(s.lines) {
		return "", io.EOF
	}
	line := s.lines[s.idx]
	s.idx++
	return line, nil
}

func (s *stubSourceSeeker) Close() error { return nil }

func (s *stubSourceSeeker) Seek(offset int64, whence int) (int64, error) {
	s.idx = 0
	s.seekCalled = true
	return 0, nil
}
