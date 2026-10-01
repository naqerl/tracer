package settings

import (
	"strings"
	"testing"
)

// TestBuildCollectorInstallCommand pins the copy-paste command shape: one
// pipeline that installs the collector, points it at this panel, wires a
// kubeconfig, and starts or restarts the unit depending on what is present.
func TestBuildCollectorInstallCommand(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		collector string
		apiKey    string
		wantParts []string
		notWant   []string
	}{
		{
			name:      "fresh_host",
			host:      "panel.example.com",
			collector: "prod-a",
			apiKey:    "deadbeef",
			wantParts: []string{
				"curl -fsSL",
				"panel.example.com/panel/collectors/prod-a/install-script",
				`X-API-Key: deadbeef`,
				"sudo bash",
			},
			notWant: []string{"<API_KEY>"},
		},
		{
			name:      "scheme_is_preserved",
			host:      "https://panel.example.com",
			collector: "prod-a",
			apiKey:    "deadbeef",
			wantParts: []string{"https://panel.example.com/panel/collectors/prod-a/install-script"},
		},
		{
			name:      "key_with_shell_metacharacters_is_quoted",
			host:      "panel.example.com",
			collector: "prod-a",
			apiKey:    `abc"$'\;def`,
			// The single quote inside the key is closed, escaped and reopened.
			wantParts: []string{`X-API-Key: abc"$'\''\;def'`},
		},
		{
			name:      "key_cannot_break_out_of_quoting",
			host:      "panel.example.com",
			collector: "prod-a",
			apiKey:    `'; rm -rf /; echo '`,
			wantParts: []string{`X-API-Key: '\''; rm -rf /; echo '\'''`},
			notWant:   []string{"; rm -rf /; echo ' |"},
		},
		{
			name:      "name_with_shell_metacharacters_is_quoted_and_encoded",
			host:      "panel.example.com",
			collector: `prod a'; rm -rf /; echo '`,
			apiKey:    "deadbeef",
			// The name is percent-encoded into the URL and the whole URL is
			// single-quoted, so the shell sees one literal argument.
			wantParts: []string{
				`'panel.example.com/panel/collectors/prod%20a%27%3B%20rm%20-rf%20%2F%3B%20echo%20%27/install-script'`,
			},
			notWant: []string{"/install-script'; rm -rf /", " | sudo bash'"},
		},
		{
			name:      "host_and_url_are_quoted",
			host:      "https://panel.example.com",
			collector: "prod-a",
			apiKey:    "deadbeef",
			wantParts: []string{
				`curl -fsSL 'https://panel.example.com/panel/collectors/prod-a/install-script' -H 'X-API-Key: deadbeef'`,
			},
		},
		{
			name:      "trailing_slash_in_host_does_not_double_up",
			host:      "https://panel.example.com/",
			collector: "prod-a",
			apiKey:    "deadbeef",
			wantParts: []string{`'https://panel.example.com/panel/collectors/prod-a/install-script'`},
			notWant:   []string{"example.com//panel"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCollectorInstallCommand(tt.host, tt.collector, tt.apiKey)

			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("command missing %q:\n%s", part, got)
				}
			}
			for _, part := range tt.notWant {
				if strings.Contains(got, part) {
					t.Errorf("command unexpectedly contains %q:\n%s", part, got)
				}
			}
			if strings.Contains(got, "\n") {
				t.Errorf("command must be a single line:\n%s", got)
			}
			if !strings.HasSuffix(got, " | sudo bash") {
				t.Errorf("command must pipe into sudo bash:\n%s", got)
			}
		})
	}
}

// TestShellQuote covers the quoting helper directly, including the empty and
// already-safe cases.
func TestShellQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: `''`},
		{in: "simple", want: `'simple'`},
		{in: "a-b_c.d", want: `'a-b_c.d'`},
		{in: "with space", want: `'with space'`},
		{in: "semi;colon", want: `'semi;colon'`},
		{in: `quote"inside`, want: `'quote"inside'`},
		{in: "dollar$var", want: `'dollar$var'`},
		{in: `back\slash`, want: `'back\slash'`},
		{in: `it's`, want: `'it'\''s'`},
	}

	for _, tt := range tests {
		if got := shellQuote(tt.in); got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
