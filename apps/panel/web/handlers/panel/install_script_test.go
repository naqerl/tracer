package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRenderCollectorInstallScript(t *testing.T) {
	script := renderCollectorInstallScript(
		"prod-a",
		"deadbeef",
		"https://panel.example.com",
		installScriptURL,
	)

	wantParts := []string{
		"#!/bin/bash",
		"set -euo pipefail",
		"'prod-a'",
		installScriptURL,
		"TRACER_COLLECTOR_ONLY=1",
		"TRACER_COLLECTOR_API_KEY='deadbeef'",
		"TRACER_COLLECTOR_PANEL_HOST='https://panel.example.com'",
		"tracer-collector set api-key 'deadbeef'",
		"tracer-collector kubectl-setup",
		"systemctl enable --now 'tracer-collector.service'",
		"systemctl daemon-reload",
		"tracer-collector verify",
	}
	for _, part := range wantParts {
		if !strings.Contains(script, part) {
			t.Errorf("script missing %q", part)
		}
	}
}

// TestRenderCollectorInstallScriptQuotesValues proves a hostile collector name
// or key cannot break out of its quoted argument and run commands.
func TestRenderCollectorInstallScriptQuotesValues(t *testing.T) {
	script := renderCollectorInstallScript(
		`evil'; touch /tmp/pwned; echo '`,
		`k'; touch /tmp/pwned2; echo '`,
		"https://panel.example.com",
		installScriptURL,
	)

	for _, forbidden := range []string{
		"'evil'; touch /tmp/pwned; echo ''",
		"'k'; touch /tmp/pwned2; echo ''",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("value was not quoted, found %q", forbidden)
		}
	}
	if !strings.Contains(script, `'evil'\''; touch /tmp/pwned; echo '\''`) {
		t.Errorf("collector name not escaped as expected:\n%s", script)
	}
}

// TestRenderCollectorInstallScriptNamesCollectorInClosingMessage pins the two
// places the name reaches the end of the script: not in a comment, and in a
// closing message that names the collector.
func TestRenderCollectorInstallScriptNamesCollectorInClosingMessage(t *testing.T) {
	script := renderCollectorInstallScript(
		"prod-a",
		"deadbeef",
		"https://panel.example.com",
		installScriptURL,
	)

	if !strings.Contains(script, `log 'Collector prod-a is installed and running.'`) {
		t.Errorf("closing message does not name the collector:\n%s", script)
	}
	if strings.Contains(script, "# Installs or upgrades the tracer collector '") {
		t.Errorf("collector name is interpolated into a comment:\n%s", script)
	}
}

// TestRenderCollectorInstallScriptLogsCollectorName runs the generated script
// and proves the closing message prints what it says: the collector name is in
// it, and no format specifier is left standing in for a value.
func TestRenderCollectorInstallScriptLogsCollectorName(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	dir := t.TempDir()
	script := renderCollectorInstallScript("prod-a", "deadbeef", "https://panel.example.com", installScriptURL)
	out := runInstallScript(t, script, dir, filepath.Join(dir, "collector-args"))

	if !strings.Contains(out, "Collector prod-a is installed and running.") {
		t.Errorf("closing message did not print the collector name:\n%s", out)
	}
	if strings.Contains(out, "%s") {
		t.Errorf("closing message printed an unexpanded format specifier:\n%s", out)
	}
}

// TestRenderCollectorInstallScriptRunsHostileNameLiterally runs the generated
// script with stubbed commands and proves the collector name never reaches the
// shell as code: no marker file appears and the name arrives as one argument.
func TestRenderCollectorInstallScriptRunsHostileNameLiterally(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	cases := []struct {
		label     string
		collector func(marker string) string
	}{
		{label: "command substitution", collector: func(m string) string { return "a$(touch " + m + ")b" }},
		{label: "backticks", collector: func(m string) string { return "a`touch " + m + "`b" }},
		{label: "double quote", collector: func(m string) string { return `a"; touch ` + m + `; echo "b` }},
		{label: "newline", collector: func(m string) string { return "a\ntouch " + m + "\nb" }},
		{label: "single quote", collector: func(m string) string { return `a'; touch ` + m + `; echo 'b` }},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "pwned")
			collector := tc.collector(marker)

			script := renderCollectorInstallScript(collector, "deadbeef", "https://panel.example.com", installScriptURL)
			argsFile := filepath.Join(dir, "collector-args")
			runInstallScript(t, script, dir, argsFile)

			if _, err := os.Stat(marker); err == nil {
				t.Errorf("collector name executed: %s was created", marker)
			}
			if got := readInstallScriptArgs(t, argsFile); !slices.Contains(got, collector) {
				t.Errorf("collector name was not passed as a single argument, got %q", got)
			}
		})
	}
}

// TestRenderCollectorInstallScriptIsValidShell parses the generated script so a
// quoting mistake cannot ship a script that fails on the collector host.
func TestRenderCollectorInstallScriptIsValidShell(t *testing.T) {
	script := renderCollectorInstallScript(
		`na'me with spaces`,
		`key with 'quote`,
		"https://panel.example.com:8080",
		installScriptURL,
	)

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated script is not valid shell: %v\n%s\nscript:\n%s", err, out, script)
	}
}

// runInstallScript executes a generated install script against stub commands —
// id, curl, sudo, systemctl and tracer-collector — so nothing on the machine is
// touched and the script runs as root regardless of the test user. The stub
// tracer-collector records each argument it receives, NUL separated, into args.
// It returns everything the script printed.
func runInstallScript(t *testing.T, script, dir, args string) string {
	t.Helper()

	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create stub bin dir: %v", err)
	}
	stubs := map[string]string{
		"id":               "#!/bin/bash\necho 0\n",
		"curl":             "#!/bin/bash\nexit 0\n",
		"systemctl":        "#!/bin/bash\nexit 0\n",
		"sudo":             "#!/bin/bash\nwhile [[ \"$1\" == *=* ]]; do shift; done\nexec \"$@\"\n",
		"tracer-collector": "#!/bin/bash\nfor arg in \"$@\"; do printf '%s\\0' \"$arg\"; done >> \"$TRACER_STUB_ARGS\"\nexit 0\n",
	}
	for name, body := range stubs {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}

	scriptPath := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"TRACER_STUB_ARGS="+args,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated script failed: %v\n%s\nscript:\n%s", err, out, script)
	}
	return string(out)
}

// readInstallScriptArgs returns the arguments the stub tracer-collector saw.
func readInstallScriptArgs(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stubbed collector args: %v", err)
	}
	var args []string
	for _, arg := range strings.Split(string(data), "\x00") {
		if arg == "" {
			continue
		}
		args = append(args, arg)
	}
	return args
}
