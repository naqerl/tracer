package main

import (
	"testing"

	"tracer/apps/collector/internal/formatguesser"
)

func TestParseFavourChoiceTypeConflict(t *testing.T) {
	ce := formatguesser.ConsistencyError{Field: "type", PrevValue: "json", CurrValue: "slogtext"}

	spec, ok := parseFavourChoice(ce, "2")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if spec.Type != "slogtext" {
		t.Fatalf("expected type=slogtext, got %q", spec.Type)
	}

	spec, ok = parseFavourChoice(ce, "1")
	if !ok {
		t.Fatal("expected ok=true for option 1")
	}
	if spec.Type != "json" {
		t.Fatalf("expected type=json, got %q", spec.Type)
	}
}

func TestParseFavourChoiceFieldConflict(t *testing.T) {
	ce := formatguesser.ConsistencyError{Field: "level_field", PrevValue: "level", CurrValue: "severity"}

	spec, ok := parseFavourChoice(ce, "1")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if spec.Params["level_field"] != "level" {
		t.Fatalf("expected level_field=level, got %q", spec.Params["level_field"])
	}

	spec, ok = parseFavourChoice(ce, "2")
	if !ok {
		t.Fatal("expected ok=true for option 2")
	}
	if spec.Params["level_field"] != "severity" {
		t.Fatalf("expected level_field=severity, got %q", spec.Params["level_field"])
	}
}

func TestParseFavourChoiceSkips(t *testing.T) {
	ce := formatguesser.ConsistencyError{Field: "type", PrevValue: "json", CurrValue: "slogtext"}

	if spec, ok := parseFavourChoice(ce, ""); ok {
		t.Fatalf("expected ok=false on empty input, got spec=%+v", spec)
	}
	if spec, ok := parseFavourChoice(ce, "3"); ok {
		t.Fatalf("expected ok=false on out-of-range input, got spec=%+v", spec)
	}
	if spec, ok := parseFavourChoice(ce, "abc"); ok {
		t.Fatalf("expected ok=false on non-numeric input, got spec=%+v", spec)
	}
}

func TestDescribeGuessFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"missing message and timestamp", &formatguesser.MissingFieldsError{Fields: []string{"message_field", "timestamp_field"}}, "missing required field(s): message, timestamp"},
		{"missing level only", &formatguesser.MissingFieldsError{Fields: []string{"level_field"}}, "missing required field(s): level"},
		{"missing all three", &formatguesser.MissingFieldsError{Fields: []string{"level_field", "message_field", "timestamp_field"}}, "missing required field(s): level, message, timestamp"},
		{"unrecognized failure", errFromString("failed to detect the timestamp format"), "failed to detect the timestamp format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeGuessFailure(tt.err); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDescribeGuessFailureWrapped verifies errors.As unwraps through the
// GuessError chain, matching how buildParserSpec wraps fillSpec errors.
func TestDescribeGuessFailureWrapped(t *testing.T) {
	inner := &formatguesser.MissingFieldsError{Fields: []string{"level_field"}}
	wrapped := formatguesser.GuessError{Line: `{"message":"x"}`, Err: inner}

	if got := describeGuessFailure(wrapped); got != "missing required field(s): level" {
		t.Fatalf("got %q, want %q", got, "missing required field(s): level")
	}
}

func errFromString(s string) error {
	return &plainErr{s}
}

type plainErr struct{ s string }

func (e *plainErr) Error() string { return e.s }
