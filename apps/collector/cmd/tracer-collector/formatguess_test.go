package main

import (
	"errors"
	"fmt"
	"testing"

	"tracer/apps/collector/internal/formatguesser"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
)

func TestSourceNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "typed not found", err: source.NotFound(errors.New("no such unit")), want: true},
		{name: "no logs", err: formatguesser.ErrNoLogs, want: true},
		{name: "guess error", err: formatguesser.GuessError{Line: "line", Err: errors.New("format not recognized")}, want: false},
		{name: "consistency error", err: formatguesser.ConsistencyError{Field: "level_field", Err: errors.New("inconsistent level_field")}, want: false},
		{name: "missing fields", err: &formatguesser.MissingFieldsError{Fields: []string{"level_field"}}, want: false},
		// A source that exists but cannot be read must keep its own error, so
		// the user sees the real reason instead of "not found".
		{
			name: "unreadable kubeconfig",
			err:  fmt.Errorf("could not read from source: %w", errors.New(`kubectl logs (default/deploy/barse) exited: exit status 1: error loading config file "/etc/rancher/k3s/k3s.yaml": permission denied`)),
			want: false,
		},
		{
			name: "permission denied reading logs",
			err:  errors.New(`kubectl logs (default/deploy/barse) exited: exit status 1: pods "barse" is forbidden`),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceNotFound(tc.err); got != tc.want {
				t.Fatalf("sourceNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestConflictFrom(t *testing.T) {
	consistency := formatguesser.ConsistencyError{
		Field:     "level_field",
		PrevValue: "level",
		CurrValue: "status",
		PrevSpec:  parser.Spec{Params: map[string]string{"level_field": "level"}},
		CurrSpec:  parser.Spec{Params: map[string]string{"level_field": "status"}},
		Err:       errors.New("inconsistent level_field"),
	}

	conflict := conflictFrom(consistency)
	if conflict == nil {
		t.Fatal("conflictFrom returned nil for a ConsistencyError")
	}
	if conflict.Field != "level_field" {
		t.Fatalf("field = %q, want level_field", conflict.Field)
	}
	if len(conflict.Options) != 2 || conflict.Options[0] != "level" || conflict.Options[1] != "status" {
		t.Fatalf("options = %v, want [level status]", conflict.Options)
	}

	if c := conflictFrom(errors.New("ordinary failure")); c != nil {
		t.Fatalf("conflictFrom returned %+v for a plain error, want nil", c)
	}
}

func TestConflictFromTypeDedupesOptions(t *testing.T) {
	consistency := formatguesser.ConsistencyError{
		Field:     "type",
		PrevValue: "json",
		CurrValue: "json",
		Err:       errors.New("mixed log format types"),
	}
	conflict := conflictFrom(consistency)
	if conflict == nil {
		t.Fatal("conflictFrom returned nil")
	}
	if len(conflict.Options) != 1 || conflict.Options[0] != "json" {
		t.Fatalf("options = %v, want [json]", conflict.Options)
	}
}
