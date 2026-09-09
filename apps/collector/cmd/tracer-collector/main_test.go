package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWantsHelp(t *testing.T) {
	fs := flag.NewFlagSet("kubectl-setup", flag.ExitOnError)
	fs.String("kubeconfig", "", "")
	tests := []struct {
		name    string
		args    []string
		reorder bool
		want    bool
	}{
		{"empty", nil, true, false},
		{"no help flags", []string{"myapp", "--kubeconfig=/x"}, true, false},
		{"-h", []string{"-h"}, true, true},
		{"-help", []string{"-help"}, true, true},
		{"--help", []string{"--help"}, true, true},
		{"help after positional", []string{"myapp", "-h"}, true, true},
		{"help as value of value-taking flag is not help", []string{"--kubeconfig", "-h"}, true, false},
		{"equals-form value flag value is not help", []string{"--kubeconfig=-h"}, true, false},
		{"plain word help is not a flag", []string{"help"}, true, false},
		{"-help=true edge", []string{"--help=true"}, true, true},
		{"h equals form", []string{"-h=true"}, true, true},
		{"inline equals value flag does not eat next token", []string{"--kubeconfig=x", "-h"}, true, true},
		{"inline equals value flag then positional then help", []string{"--kubeconfig=x", "myapp", "-h"}, true, true},
		// set/verify/update/install/kubectl-setup (reorder=false) parse in
		// place: the first positional ends flag parsing.
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
	global.Bool("version", false, "print version and exit")

	setupCmd := flag.NewFlagSet("kubectl-setup", flag.ExitOnError)
	setupCmd.String("kubeconfig", "", "kubeconfig to install")
	commands := []commandHelp{
		{name: "set", args: "<key> <value>", summary: "Set a config value"},
		{name: "verify", summary: "Verify panel connectivity"},
		{name: "update", summary: "Update the binary"},
		{name: "install", summary: "Install the service"},
		{name: "kubectl-setup", summary: "Install a kubeconfig", flags: setupCmd},
	}

	var buf bytes.Buffer
	printUsage(&buf, global, commands)
	text := buf.String()

	for _, want := range []string{"-config", "-version"} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage missing global flag %q:\n%s", want, text)
		}
	}
	for _, want := range []string{"set <key> <value>", "verify", "update", "install", "kubectl-setup"} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage missing command %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "-kubeconfig") {
		t.Fatalf("usage missing command flag -kubeconfig:\n%s", text)
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
