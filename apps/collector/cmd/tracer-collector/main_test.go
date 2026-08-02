package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/formatguesser"
	"tracer/apps/collector/internal/kubeconfig"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
)

// TestWireKubeconfig verifies kubectl-setup points every kubectl source at the
// installed kubeconfig, so a source can never silently fall back to an
// unreadable system default.
func TestWireKubeconfig(t *testing.T) {
	cfg := config.Default()
	cfg.Apps = []config.AppSpec{
		{Name: "file-app", Source: source.Spec{Type: "file", Params: map[string]string{"path": "/x.log"}}},
		{Name: "kubectl-empty", Source: source.Spec{Type: "kubectl", Params: map[string]string{"resource": "deployment/a"}}},
		{Name: "kubectl-set", Source: source.Spec{Type: "kubectl", Params: map[string]string{"resource": "deployment/b", "kubeconfig": "/custom/kc"}}},
		{Name: "kubectl-nil", Source: source.Spec{Type: "kubectl"}},
	}

	changed := wireKubeconfig(&cfg)
	if !changed {
		t.Fatal("expected changed=true when kubectl sources lack kubeconfig")
	}

	// Existing value preserved.
	if got := cfg.Apps[2].Source.Params["kubeconfig"]; got != "/custom/kc" {
		t.Fatalf("existing kubeconfig should be preserved, got %q", got)
	}
	// Empty and nil params now point at the installed file.
	if got := cfg.Apps[1].Source.Params["kubeconfig"]; got != kubeconfig.Path {
		t.Fatalf("empty kubeconfig should be set to %q, got %q", kubeconfig.Path, got)
	}
	if got := cfg.Apps[3].Source.Params["kubeconfig"]; got != kubeconfig.Path {
		t.Fatalf("nil params kubeconfig should be set to %q, got %q", kubeconfig.Path, got)
	}
	// Non-kubectl apps untouched.
	if _, ok := cfg.Apps[0].Source.Params["kubeconfig"]; ok {
		t.Fatal("file source should not get a kubeconfig")
	}

	// Second run changes nothing.
	if changed := wireKubeconfig(&cfg); changed {
		t.Fatal("expected changed=false when all kubectl sources already have kubeconfig")
	}
}

func TestWantsHelp(t *testing.T) {
	fs := newAddFlagSet(&addOptions{})
	tests := []struct {
		name    string
		args    []string
		reorder bool
		want    bool
	}{
		// add (reorder=true) scans every token: flags may follow the positional.
		{"empty", nil, true, false},
		{"no help flags", []string{"myapp", "--no-confirm"}, true, false},
		{"-h", []string{"-h"}, true, true},
		{"-help", []string{"-help"}, true, true},
		{"--help", []string{"--help"}, true, true},
		{"help after positional", []string{"myapp", "-h"}, true, true},
		{"help as value of value-taking flag is not help", []string{"--favour", "-h"}, true, false},
		{"equals-form value flag value is not help", []string{"--favour=-h"}, true, false},
		{"plain word help is not a flag", []string{"help"}, true, false},
		{"-help=true edge", []string{"--help=true"}, true, true},
		{"h equals form", []string{"-h=true"}, true, true},
		{"inline equals value flag does not eat next token", []string{"--favour=x", "-h"}, true, true},
		{"inline equals value flag then positional then help", []string{"--favour=x", "myapp", "-h"}, true, true},
		// set/verify/update/install/kubectl-setup (reorder=false) parse in
		// place: the first positional ends flag parsing, so a later -h is a
		// value or an argument, not help.
		{"in-place -h first", []string{"-h"}, false, true},
		{"in-place positional stops scan", []string{"api-key", "-h"}, false, false},
		{"in-place help after two positionals", []string{"api-key", "value", "-h"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wantsHelp(tt.args, fs, tt.reorder); got != tt.want {
				t.Fatalf("wantsHelp(%v, reorder=%v) = %v, want %v", tt.args, tt.reorder, got, tt.want)
			}
		})
	}
}

// TestPrintUsageComplete verifies that -help renders the complete CLI: global
// flags, every command, and per-command flags.
func TestPrintUsageComplete(t *testing.T) {
	global := flag.NewFlagSet("tracer-collector", flag.ExitOnError)
	global.String("config", "", "path to config file")
	global.Bool("print-defaults", false, "print default configuration and exit")
	global.Bool("version", false, "print version and exit")

	opts := &addOptions{}
	addCmd := newAddFlagSet(opts)
	setupCmd := flag.NewFlagSet("kubectl-setup", flag.ExitOnError)
	setupCmd.String("kubeconfig", "", "kubeconfig to install")
	commands := []commandHelp{
		{name: "add", args: "<source>", summary: "Add a log source", flags: addCmd},
		{name: "set", args: "<key> <value>", summary: "Set a config value"},
		{name: "verify", summary: "Verify panel connectivity"},
		{name: "update", summary: "Update the binary"},
		{name: "install", summary: "Install the service"},
		{name: "kubectl-setup", summary: "Install a kubeconfig", flags: setupCmd},
	}

	var buf bytes.Buffer
	printUsage(&buf, global, commands)
	text := buf.String()

	// Global flags appear.
	for _, want := range []string{"-config", "-print-defaults", "-version"} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage missing global flag %q:\n%s", want, text)
		}
	}
	// Every command appears with its summary.
	for _, want := range []string{"add <source>", "set <key> <value>", "verify", "update", "install", "kubectl-setup"} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage missing command %q:\n%s", want, text)
		}
	}
	// Per-command flags appear.
	for _, want := range []string{"-override", "-no-confirm", "-kubeconfig"} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage missing command flag %q:\n%s", want, text)
		}
	}
}

// TestPrintUsageToWriter verifies the writer is honored and nothing writes to
// os.Stdout implicitly.
func TestPrintUsageToWriter(t *testing.T) {
	global := flag.NewFlagSet("tracer-collector", flag.ExitOnError)
	global.String("config", "", "path to config file")
	var buf bytes.Buffer
	printUsage(&buf, global, nil)
	if !strings.Contains(buf.String(), "-config") {
		t.Fatalf("usage missing global flag:\n%s", buf.String())
	}
}

// TestFlagDefaultsRestoresOutput verifies flagDefaults does not leave a
// FlagSet's output redirected (used by parse-error printing).
func TestFlagDefaultsRestoresOutput(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	fs.Bool("x", false, "some flag")

	// Create a scratch file as the "real" output, then restore to it.
	scratch := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(scratch)
	if err != nil {
		t.Fatal(err)
	}
	fs.SetOutput(f)
	_ = flagDefaults(fs)
	if got := fs.Output(); got != f {
		t.Fatalf("flagDefaults changed FlagSet output: got %v, want %v", got, f)
	}
	_ = f.Close()
}

func TestReorderFlags(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.Bool("no-confirm", false, "")
	fs.String("n", "default", "")
	fs.String("favour", "", "")

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "flags before positional stay put",
			args: []string{"--no-confirm", "myapp"},
			want: []string{"--no-confirm", "myapp"},
		},
		{
			name: "flags after positional move to front",
			args: []string{"myapp", "--no-confirm"},
			want: []string{"--no-confirm", "myapp"},
		},
		{
			name: "value flag after positional keeps its value",
			args: []string{"myapp", "--favour", `timestamp_field:"time"`},
			want: []string{"--favour", `timestamp_field:"time"`, "myapp"},
		},
		{
			name: "mixed order",
			args: []string{"myapp", "-n", "prod", "--no-confirm"},
			want: []string{"-n", "prod", "--no-confirm", "myapp"},
		},
		{
			name: "equals form value flag",
			args: []string{"myapp", "--favour=type:json"},
			want: []string{"--favour=type:json", "myapp"},
		},
		{
			// An inline `--flag=value` must not consume the next token.
			name: "inline equals value flag does not eat the positional",
			args: []string{"--favour=type:json", "myapp"},
			want: []string{"--favour=type:json", "myapp"},
		},
		{
			name: "inline equals value flag with trailing flag",
			args: []string{"--favour=type:json", "myapp", "--no-confirm"},
			want: []string{"--favour=type:json", "--no-confirm", "myapp"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderFlags(tt.args, fs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("reorderFlags(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

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

func TestFormatFavourSpec(t *testing.T) {
	spec := parser.Spec{Params: map[string]string{"timestamp_field": "time", "level_field": "level"}}
	got := formatFavourSpec(spec)
	want := "--favour 'level_field:\"level\",timestamp_field:\"time\"'"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func errFromString(s string) error {
	return &plainErr{s}
}

type plainErr struct{ s string }

func (e *plainErr) Error() string { return e.s }
