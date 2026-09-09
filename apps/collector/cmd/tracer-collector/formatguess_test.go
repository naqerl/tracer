package main

import (
	"errors"
	"testing"

	"tracer/apps/collector/internal/formatguesser"
	"tracer/apps/collector/internal/parser"
)

func TestSourceNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: true},
		{name: "plain open error", err: errors.New("journalctl -u x.service exited: could not be found"), want: true},
		{name: "no logs", err: errors.New("no logs found in the source"), want: true},
		{name: "guess error", err: formatguesser.GuessError{Line: "line", Err: errors.New("format not recognized")}, want: false},
		{name: "consistency error", err: formatguesser.ConsistencyError{Field: "level_field", Err: errors.New("inconsistent level_field")}, want: false},
		{name: "missing fields", err: &formatguesser.MissingFieldsError{Fields: []string{"level_field"}}, want: false},
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
