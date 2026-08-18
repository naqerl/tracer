package middleware

import (
	"strings"
	"testing"
)

func TestAnchorLabel(t *testing.T) {
	cases := []struct {
		href string
		want string
	}{
		{href: "/panel/collectors/demo/apps", want: "collectors/demo/apps"},
		{href: "/panel/collectors/demo/stats", want: "collectors/demo/stats"},
		{href: "/panel/collectors/demo/apps/myapp/logs/groups?level=error", want: "collectors/demo/apps/myapp/logs/groups?level=error"},
		{href: "/panel", want: "/panel"},
		{href: "/", want: "/"},
	}
	for _, tc := range cases {
		if got := anchorLabel(tc.href); got != tc.want {
			t.Fatalf("anchorLabel(%q) = %q, want %q", tc.href, got, tc.want)
		}
	}
}

func TestAnchorsFromHXGet(t *testing.T) {
	htmlBody := `<html><body>
		<div hx-get="/panel/collectors/demo/apps" hx-trigger="load, every 15s"></div>
		<div hx-get="/panel/collectors/demo/stats" hx-trigger="every 15s"><p>CPU 12%</p></div>
		<a href="/panel/collectors/demo/resources">Metrics &rarr;</a>
		<a href="/panel/collectors/demo" class="x">real link</a>
	</body></html>`

	out, err := anchorsFromHXGet([]byte(htmlBody))
	if err != nil {
		t.Fatalf("anchorsFromHXGet: %v", err)
	}
	s := string(out)

	// Empty div becomes a link labelled with its route.
	if !strings.Contains(s, `<a href="/panel/collectors/demo/apps">collectors/demo/apps</a>`) {
		t.Fatalf("expected empty hx-get div to become a labelled link, got:\n%s", s)
	}
	// Div with content is wrapped in a link, content preserved.
	if !strings.Contains(s, `<a href="/panel/collectors/demo/stats">`) || !strings.Contains(s, "CPU 12%") {
		t.Fatalf("expected hx-get div with content wrapped in a link, got:\n%s", s)
	}
	// Anchor with a real href stays untouched (the Metrics link now
	// carries href directly; the hx-get anchors are the placeholder ones).
	if !strings.Contains(s, `<a href="/panel/collectors/demo/resources">Metrics →</a>`) {
		t.Fatalf("expected the real Metrics anchor to stay untouched, got:\n%s", s)
	}
	// An anchor with a real href stays untouched (real link, or the
	// Metrics link that now carries href directly instead of hx-get).
	if !strings.Contains(s, `<a href="/panel/collectors/demo" class="x">real link</a>`) {
		t.Fatalf("expected real href anchor untouched, got:\n%s", s)
	}
}

func TestAnchorsFromHXGetEmpty(t *testing.T) {
	// No hx-get elements: the document passes through unchanged.
	out, err := anchorsFromHXGet([]byte(`<html><body><h1>Hi</h1></body></html>`))
	if err != nil {
		t.Fatalf("anchorsFromHXGet: %v", err)
	}
	if !strings.Contains(string(out), "<h1>Hi</h1>") {
		t.Fatalf("expected document unchanged, got:\n%s", string(out))
	}
}

func TestAnchorsFromHXGetInvalidHTML(t *testing.T) {
	// Malformed HTML must not panic; goquery normalizes it.
	out, err := anchorsFromHXGet([]byte(`<div hx-get="/x"`))
	if err != nil {
		t.Fatalf("anchorsFromHXGet: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty output for malformed input")
	}
}
