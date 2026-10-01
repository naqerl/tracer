package format

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"tracer/libs/types"
)

// slogOutput generates log output using actual slog.TextHandler
func slogOutput(fn func(*slog.Logger)) string {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, nil)
	logger := slog.New(handler)
	fn(logger)
	return buf.String()
}

// slogOutputDebug generates log output with debug level enabled
func slogOutputDebug(fn func(*slog.Logger)) string {
	var buf bytes.Buffer
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	handler := slog.NewTextHandler(&buf, opts)
	logger := slog.New(handler)
	fn(logger)
	return buf.String()
}

// slogOutputWithOptions generates log output with custom handler options
func slogOutputWithOptions(opts *slog.HandlerOptions, fn func(*slog.Logger)) string {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, opts)
	logger := slog.New(handler)
	fn(logger)
	return buf.String()
}

func TestSlogText_Parse_RealSlogOutput(t *testing.T) {
	defaultSchema := Schema{
		TimestampField:  "time",
		TimestampFormat: time.RFC3339Nano, // Use Nano because slog outputs fractional seconds
		LevelField:      "level",
		TraceIDField:    "trace_id",
		MessageField:    "msg",
	}

	tests := []struct {
		name         string
		schema       Schema
		generateLine func() string
		validate     func(t *testing.T, entry types.LogEntry)
		wantErr      bool
	}{
		{
			name:   "basic_info_message",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("hello world")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Level != types.LevelInfo {
					t.Errorf("Level = %v, want Info", e.Level)
				}
				if e.Message != "hello world" {
					t.Errorf("Message = %q, want %q", e.Message, "hello world")
				}
			},
			wantErr: false,
		},
		{
			name:   "debug_level",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutputDebug(func(l *slog.Logger) {
					l.Debug("debug message")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Level != types.LevelDebug {
					t.Errorf("Level = %v, want Debug", e.Level)
				}
				if e.Message != "debug message" {
					t.Errorf("Message = %q, want %q", e.Message, "debug message")
				}
			},
			wantErr: false,
		},
		{
			name:   "warn_level",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Warn("warning message")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Level != types.LevelWarn {
					t.Errorf("Level = %v, want Warn", e.Level)
				}
			},
			wantErr: false,
		},
		{
			name:   "error_level",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Error("error message")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Level != types.LevelError {
					t.Errorf("Level = %v, want Error", e.Level)
				}
			},
			wantErr: false,
		},
		{
			name:   "value_with_equals_sign",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "config", "key=value")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["config"] != "key=value" {
					t.Errorf("Extra['config'] = %q, want %q", e.Extra["config"], "key=value")
				}
			},
			wantErr: false,
		},
		{
			name:   "value_with_multiple_equals",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "data", "a=b=c=d")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["data"] != "a=b=c=d" {
					t.Errorf("Extra['data'] = %q, want %q", e.Extra["data"], "a=b=c=d")
				}
			},
			wantErr: false,
		},
		{
			name:   "value_with_spaces",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "query", "SELECT * FROM users")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["query"] != "SELECT * FROM users" {
					t.Errorf("Extra['query'] = %q, want %q", e.Extra["query"], "SELECT * FROM users")
				}
			},
			wantErr: false,
		},
		{
			name:   "value_with_multiple_spaces",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "key", "hello   world")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["key"] != "hello   world" {
					t.Errorf("Extra['key'] = %q, want %q", e.Extra["key"], "hello   world")
				}
			},
			wantErr: false,
		},
		{
			name:   "value_with_quotes",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "key", `say "hello"`)
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				want := `say "hello"`
				if e.Extra["key"] != want {
					t.Errorf("Extra['key'] = %q, want %q", e.Extra["key"], want)
				}
			},
			wantErr: false,
		},
		{
			name:   "json_value",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "json", `{"key":"value"}`)
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				want := `{"key":"value"}`
				if e.Extra["json"] != want {
					t.Errorf("Extra['json'] = %q, want %q", e.Extra["json"], want)
				}
			},
			wantErr: false,
		},
		{
			name:   "windows_path_with_spaces",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "path", `C:\Program Files\App\config.ini`)
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				want := `C:\Program Files\App\config.ini`
				if e.Extra["path"] != want {
					t.Errorf("Extra['path'] = %q, want %q", e.Extra["path"], want)
				}
			},
			wantErr: false,
		},
		{
			name:   "windows_path_without_spaces_unquoted",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "path", `C:\Users\test`)
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				want := `C:\Users\test`
				if e.Extra["path"] != want {
					t.Errorf("Extra['path'] = %q, want %q", e.Extra["path"], want)
				}
			},
			wantErr: false,
		},
		{
			name:   "empty_string_value",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "key", "")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["key"] != "" {
					t.Errorf("Extra['key'] = %q, want empty string", e.Extra["key"])
				}
			},
			wantErr: false,
		},
		{
			name:   "integer_value",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "count", 42)
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["count"] != "42" {
					t.Errorf("Extra['count'] = %q, want %q", e.Extra["count"], "42")
				}
			},
			wantErr: false,
		},
		{
			name:   "float_value",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "pi", 3.14159)
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["pi"] != "3.14159" {
					t.Errorf("Extra['pi'] = %q, want %q", e.Extra["pi"], "3.14159")
				}
			},
			wantErr: false,
		},
		{
			name:   "boolean_true_value",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "active", true)
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["active"] != "true" {
					t.Errorf("Extra['active'] = %q, want %q", e.Extra["active"], "true")
				}
			},
			wantErr: false,
		},
		{
			name:   "boolean_false_value",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "valid", false)
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["valid"] != "false" {
					t.Errorf("Extra['valid'] = %q, want %q", e.Extra["valid"], "false")
				}
			},
			wantErr: false,
		},
		{
			name:   "grouped_attributes",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.WithGroup("req").Info("test", "method", "GET", "url", "/api")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["req.method"] != "GET" {
					t.Errorf("Extra['req.method'] = %q, want %q", e.Extra["req.method"], "GET")
				}
				if e.Extra["req.url"] != "/api" {
					t.Errorf("Extra['req.url'] = %q, want %q", e.Extra["req.url"], "/api")
				}
			},
			wantErr: false,
		},
		{
			name:   "nested_groups",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.WithGroup("outer").WithGroup("inner").Info("test", "key", "value")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["outer.inner.key"] != "value" {
					t.Errorf("Extra['outer.inner.key'] = %q, want %q", e.Extra["outer.inner.key"], "value")
				}
			},
			wantErr: false,
		},
		{
			name:   "group_with_equals_in_value",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.WithGroup("g").Info("test", "key", "a=b=c")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["g.key"] != "a=b=c" {
					t.Errorf("Extra['g.key'] = %q, want %q", e.Extra["g.key"], "a=b=c")
				}
			},
			wantErr: false,
		},
		{
			name:   "message_with_equals",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("key=value")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Message != "key=value" {
					t.Errorf("Message = %q, want %q", e.Message, "key=value")
				}
			},
			wantErr: false,
		},
		{
			name:   "message_with_quotes",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info(`say "hello world"`)
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				want := `say "hello world"`
				if e.Message != want {
					t.Errorf("Message = %q, want %q", e.Message, want)
				}
			},
			wantErr: false,
		},
		{
			name:   "multiple_attributes",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("request", "method", "GET", "path", "/api/users", "status", 200, "duration", "45ms")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["method"] != "GET" {
					t.Errorf("Extra['method'] = %q, want GET", e.Extra["method"])
				}
				if e.Extra["path"] != "/api/users" {
					t.Errorf("Extra['path'] = %q, want /api/users", e.Extra["path"])
				}
				if e.Extra["status"] != "200" {
					t.Errorf("Extra['status'] = %q, want 200", e.Extra["status"])
				}
				if e.Extra["duration"] != "45ms" {
					t.Errorf("Extra['duration'] = %q, want 45ms", e.Extra["duration"])
				}
			},
			wantErr: false,
		},
		{
			name:   "with_predefined_attributes",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.With("trace_id", "abc-123").Info("request completed")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				// trace_id is mapped to TraceID field in schema
				if e.TraceID != "abc-123" {
					t.Errorf("TraceID = %q, want abc-123", e.TraceID)
				}
			},
			wantErr: false,
		},
		{
			name:   "with_source_file",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutputWithOptions(&slog.HandlerOptions{AddSource: true}, func(l *slog.Logger) {
					l.Info("test with source")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				// Source should be in Extra and contain file path with line number
				if e.Extra["source"] == "" {
					t.Error("Expected source in Extra, but was empty")
				}
				if e.Message != "test with source" {
					t.Errorf("Message = %q, want %q", e.Message, "test with source")
				}
			},
			wantErr: false,
		},
		{
			name:   "unicode_in_message",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("Hello 世界 🌍")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				want := "Hello 世界 🌍"
				if e.Message != want {
					t.Errorf("Message = %q, want %q", e.Message, want)
				}
			},
			wantErr: false,
		},
		{
			name:   "unicode_in_value",
			schema: defaultSchema,
			generateLine: func() string {
				return slogOutput(func(l *slog.Logger) {
					l.Info("test", "user", "用户", "emoji", "🎉")
				})
			},
			validate: func(t *testing.T, e types.LogEntry) {
				if e.Extra["user"] != "用户" {
					t.Errorf("Extra['user'] = %q, want 用户", e.Extra["user"])
				}
				if e.Extra["emoji"] != "🎉" {
					t.Errorf("Extra['emoji'] = %q, want 🎉", e.Extra["emoji"])
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := tt.generateLine()
			t.Logf("Generated line: %q", line)

			parser := SlogText{Schema: tt.schema}
			entry, err := parser.Parse(line)

			if (err != nil) != tt.wantErr {
				t.Errorf("SlogText.Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			// Always check timestamp is parsed
			if entry.Timestamp.IsZero() {
				t.Error("Timestamp should not be zero")
			}

			// Run test-specific validation
			tt.validate(t, entry)
		})
	}
}

// Test for error cases with hardcoded input
func TestSlogText_Parse_ErrorCases(t *testing.T) {
	defaultSchema := Schema{
		TimestampField:  "time",
		TimestampFormat: time.RFC3339,
		LevelField:      "level",
		TraceIDField:    "trace_id",
		MessageField:    "msg",
	}

	tests := []struct {
		name   string
		schema Schema
		input  string
	}{
		{
			name:   "missing_timestamp",
			schema: defaultSchema,
			input:  "level=INFO msg=test",
		},
		{
			name:   "invalid_timestamp_format",
			schema: defaultSchema,
			input:  "time=not-a-date level=INFO msg=test",
		},
		{
			name:   "empty_line",
			schema: defaultSchema,
			input:  "",
		},
		{
			name:   "whitespace_only_line",
			schema: defaultSchema,
			input:  "   \t\n",
		},
		{
			name:   "line_without_equals",
			schema: defaultSchema,
			input:  "this is not a valid slog line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := SlogText{Schema: tt.schema}
			_, err := parser.Parse(tt.input)
			if err == nil {
				t.Error("Expected error but got none")
			}
		})
	}
}

// Test for custom schema with hardcoded input (since we need specific field names)
func TestSlogText_Parse_CustomSchema(t *testing.T) {
	customSchema := Schema{
		TimestampField:  "ts",
		TimestampFormat: "2006-01-02 15:04:05",
		LevelField:      "lvl",
		TraceIDField:    "trace",
		MessageField:    "message",
	}

	// Note: This test uses a quoted timestamp because custom format includes space
	input := `ts="2024-06-01 14:30:00" lvl=DEBUG trace=xyz789 message="custom format"`
	parser := SlogText{Schema: customSchema}
	entry, err := parser.Parse(input)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	wantTime := time.Date(2024, 6, 1, 14, 30, 0, 0, time.UTC)
	if !entry.Timestamp.Equal(wantTime) {
		t.Errorf("Timestamp = %v, want %v", entry.Timestamp, wantTime)
	}
	if entry.Level != types.LevelDebug {
		t.Errorf("Level = %v, want Debug", entry.Level)
	}
	if entry.TraceID != "xyz789" {
		t.Errorf("TraceID = %q, want xyz789", entry.TraceID)
	}
	if entry.Message != "custom format" {
		t.Errorf("Message = %q, want custom format", entry.Message)
	}
}

// Test tokenize function directly
func TestTokenize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []token
		wantErr bool
	}{
		{
			name:  "simple_key_value_pairs",
			input: "time=2024-01-15T10:30:00Z level=INFO msg=test",
			want: []token{
				{key: "time", value: "2024-01-15T10:30:00Z"},
				{key: "level", value: "INFO"},
				{key: "msg", value: "test"},
			},
		},
		{
			name:  "quoted_value_with_spaces",
			input: `msg="hello world" level=INFO`,
			want: []token{
				{key: "msg", value: `"hello world"`},
				{key: "level", value: "INFO"},
			},
		},
		{
			name:  "quoted_value_with_escaped_quotes",
			input: `json="{\"key\":\"value\"}" level=INFO`,
			want: []token{
				{key: "json", value: `"{\"key\":\"value\"}"`},
				{key: "level", value: "INFO"},
			},
		},
		{
			name:  "empty_value",
			input: "key=",
			want: []token{
				{key: "key", value: ""},
			},
		},
		{
			name:  "empty_quoted_value",
			input: `key=""`,
			want: []token{
				{key: "key", value: `""`},
			},
		},
		{
			name:  "value_with_equals_quoted",
			input: `config="key=value"`,
			want: []token{
				{key: "config", value: `"key=value"`},
			},
		},
		{
			name:  "grouped_keys",
			input: "req.method=GET req.url=/api req.status=200",
			want: []token{
				{key: "req.method", value: "GET"},
				{key: "req.url", value: "/api"},
				{key: "req.status", value: "200"},
			},
		},
		{
			name:  "leading_trailing_whitespace",
			input: "  time=now  level=INFO  ",
			want: []token{
				{key: "time", value: "now"},
				{key: "level", value: "INFO"},
			},
		},
		{
			name:  "multiple_spaces_between_tokens",
			input: "time=now    level=INFO   msg=test",
			want: []token{
				{key: "time", value: "now"},
				{key: "level", value: "INFO"},
				{key: "msg", value: "test"},
			},
		},
		{
			name:  "windows_path_quoted",
			input: `path="C:\\Program Files\\App\\file.txt"`,
			want: []token{
				{key: "path", value: `"C:\\Program Files\\App\\file.txt"`},
			},
		},
		{
			name:  "backslash_at_end_unquoted",
			input: `path=C:\\ extra=value`,
			want: []token{
				{key: "path", value: `C:\\`},
				{key: "extra", value: "value"},
			},
		},
		{
			name:  "escape_sequences",
			input: `key="line1\\nline2\\tcol"`,
			want: []token{
				{key: "key", value: `"line1\\nline2\\tcol"`},
			},
		},
		{
			name:  "nested_groups",
			input: `outer.inner.key=value`,
			want: []token{
				{key: "outer.inner.key", value: "value"},
			},
		},
		{
			name:  "quoted_key_with_spaces",
			input: `"my key"=v`,
			want: []token{
				{key: "my key", value: "v"},
			},
		},
		{
			name:  "quoted_key_empty",
			input: `""=v`,
			want: []token{
				{key: "", value: "v"},
			},
		},
		{
			name:  "quoted_key_then_pairs",
			input: `time=now "my key"=v level=INFO`,
			want: []token{
				{key: "time", value: "now"},
				{key: "my key", value: "v"},
				{key: "level", value: "INFO"},
			},
		},
		{
			// Go's default slog handler prefixes a stdlib log timestamp,
			// which cannot appear unquoted in a TextHandler key.
			name:    "stdlib_log_prefix",
			input:   `2026/10/01 07:40:22 INFO HTTP method=GET status=200`,
			wantErr: true,
		},
		{
			name:    "unquoted_space_in_key",
			input:   `level=INFO bad key=value`,
			wantErr: true,
		},
		{
			name:    "no_pairs",
			input:   `this is not a valid slog line`,
			wantErr: true,
		},
		{
			name:    "unterminated_quoted_key",
			input:   `"my key=v`,
			wantErr: true,
		},
		{
			name:    "quoted_key_without_equals",
			input:   `"my key" v`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tokenize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("tokenize() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			if len(got) != len(tt.want) {
				t.Errorf("tokenize() got %d tokens, want %d", len(got), len(tt.want))
				for i, tok := range got {
					t.Logf("  got[%d]: key=%q value=%q", i, tok.key, tok.value)
				}
				return
			}

			for i := range got {
				if got[i].key != tt.want[i].key || got[i].value != tt.want[i].value {
					t.Errorf("tokenize() token[%d] = {key:%q, value:%q}, want {key:%q, value:%q}",
						i, got[i].key, got[i].value, tt.want[i].key, tt.want[i].value)
				}
			}
		})
	}
}

func TestUnquoteValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"unquoted", "hello", "hello"},
		{"quoted_simple", `"hello"`, "hello"},
		{"quoted_with_spaces", `"hello world"`, "hello world"},
		{"quoted_with_escaped_quotes", `"say \"hello\""`, `say "hello"`},
		{"quoted_with_newline", `"line1\nline2"`, "line1\nline2"},
		{"quoted_with_tab", `"col1\tcol2"`, "col1\tcol2"},
		{"quoted_with_backslash", `"C:\\Program Files"`, `C:\Program Files`},
		{"invalid_quoted", `"unclosed`, `"unclosed`},
		{"empty", "", ""},
		{"empty_quoted", `""`, ""},
		{"just_quotes", `"`, `"`},
		{"unquoted_with_equals", "key=value", "key=value"},
		{"unquoted_with_backslash", `C:\\Users`, `C:\\Users`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unquoteValue(tt.input)
			if got != tt.want {
				t.Errorf("unquoteValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantVal  string
		wantRest string
	}{
		{"unquoted_simple", "hello world", "hello", " world"},
		{"unquoted_end", "value", "value", ""},
		{"quoted_simple", `"hello" there`, `"hello"`, " there"},
		{"quoted_with_space", `"hello world" rest`, `"hello world"`, " rest"},
		{"quoted_with_escaped_quote", `"say \"hi\"" next`, `"say \"hi\""`, " next"},
		{"quoted_at_end", `"value"`, `"value"`, ""},
		{"empty", "", "", ""},
		{"quoted_empty", `"" rest`, `""`, " rest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVal, gotRest := parseValue(tt.input)
			if gotVal != tt.wantVal || gotRest != tt.wantRest {
				t.Errorf("parseValue(%q) = (%q, %q), want (%q, %q)",
					tt.input, gotVal, gotRest, tt.wantVal, tt.wantRest)
			}
		})
	}
}

// Benchmark tests
func BenchmarkSlogText_Parse(b *testing.B) {
	// Generate a realistic log line using slog
	line := slogOutput(func(l *slog.Logger) {
		l.Info("HTTP request completed",
			"trace_id", "req-123",
			"method", "GET",
			"path", "/api/v1/users",
			"status", 200,
			"duration", "45.2ms",
			"user_agent", "Mozilla/5.0",
		)
	})

	schema := Schema{
		TimestampField:  "time",
		TimestampFormat: time.RFC3339Nano,
		LevelField:      "level",
		TraceIDField:    "trace_id",
		MessageField:    "msg",
	}
	parser := SlogText{Schema: schema}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := parser.Parse(line)
		if err != nil {
			b.Fatal(err)
		}
	}
}
