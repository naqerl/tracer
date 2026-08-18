package templates

import (
	"bytes"
	"strings"
	"testing"
)

// TestMarkdownCheckboxUncheckedSubmitsNothing verifies the hidden input carries
// an empty value when the checkbox is unchecked, so "disabled by default" holds
// and a form save does not enable apps the user never toggled.
func TestMarkdownCheckboxUncheckedSubmitsNothing(t *testing.T) {
	var buf bytes.Buffer
	if err := MarkdownCheckbox("cb1", "apps", "myapp", "prod:myapp", false, 0).Render(t.Context(), &buf); err != nil {
		t.Fatalf("render unchecked checkbox: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `name="apps"`) {
		t.Fatalf("expected name=apps hidden input, got: %s", out)
	}
	if !strings.Contains(out, `data-checked-value="prod:myapp"`) {
		t.Fatalf("expected checked-value preserved, got: %s", out)
	}
	if !strings.Contains(out, `value="" data-checked-value="prod:myapp"`) {
		t.Fatalf("unchecked checkbox must carry an empty value, got: %s", out)
	}
	if !strings.Contains(out, "[ ] myapp") {
		t.Fatalf("unchecked checkbox must render [ ] label, got: %s", out)
	}

	// The checked variant keeps its value so it submits.
	buf.Reset()
	if err := MarkdownCheckbox("cb2", "apps", "myapp", "prod:myapp", true, 0).Render(t.Context(), &buf); err != nil {
		t.Fatalf("render checked checkbox: %v", err)
	}
	out = buf.String()
	if !strings.Contains(out, `value="prod:myapp" data-checked-value="prod:myapp"`) {
		t.Fatalf("checked checkbox must carry its value, got: %s", out)
	}
	if !strings.Contains(out, "[x] myapp") {
		t.Fatalf("checked checkbox must render [x] label, got: %s", out)
	}
}

// TestMarkdownCheckboxToggleScript references the external toggle script.
func TestMarkdownCheckboxToggleScript(t *testing.T) {
	var buf bytes.Buffer
	if err := MarkdownCheckboxScript().Render(t.Context(), &buf); err != nil {
		t.Fatalf("render script: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `<script src="/static/markdown-checkbox.js"></script>`) {
		t.Fatalf("expected external toggle script reference, got: %s", out)
	}
}
