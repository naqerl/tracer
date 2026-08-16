package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcceptsMarkdown(t *testing.T) {
	tests := []struct {
		name   string
		accept string
		want   bool
	}{
		{name: "absent", accept: "", want: false},
		{name: "wildcard", accept: "*/*", want: false},
		{name: "browser default", accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", want: false},
		{name: "plain markdown", accept: "text/markdown", want: true},
		{name: "markdown with charset", accept: "text/markdown; charset=utf-8", want: true},
		{name: "markdown preferred over html", accept: "text/markdown;q=1,text/html;q=0.9", want: true},
		{name: "html preferred over markdown", accept: "text/html;q=1,text/markdown;q=0.5", want: false},
		{name: "equal q", accept: "text/html;q=0.8,text/markdown;q=0.8", want: true},
		{name: "markdown zero q", accept: "text/markdown;q=0,text/html", want: false},
		{name: "text/plain only", accept: "text/plain", want: false},
		{name: "markdown plus plain", accept: "text/markdown,text/plain", want: true},
		{name: "case insensitive media type", accept: "text/Markdown", want: true},
		{name: "malformed q excluded", accept: "text/markdown;q=abc", want: false},
		{name: "q above 1 excluded", accept: "text/markdown;q=1.5", want: false},
		{name: "q too many decimals excluded", accept: "text/markdown;q=0.1234", want: false},
		{name: "uppercase Q", accept: "text/markdown;Q=0.9", want: true},
		{name: "q with trailing dot excluded", accept: "text/markdown;q=0.", want: false},
		{name: "q one decimal", accept: "text/markdown;q=0.9,text/html;q=0.8", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := acceptsMarkdown(tt.accept); got != tt.want {
				t.Fatalf("acceptsMarkdown(%q) = %v, want %v", tt.accept, got, tt.want)
			}
		})
	}
}

func TestBufferedResponseWriterCapturesAndReplays(t *testing.T) {
	rec := httptest.NewRecorder()
	bw := &bufferedResponseWriter{header: make(http.Header), underlying: rec}

	bw.Header().Set("Content-Type", "text/html")
	bw.WriteHeader(http.StatusOK)
	_, _ = bw.Write([]byte("<p>hello</p>"))

	if bw.status != http.StatusOK {
		t.Fatalf("expected status 200, got %d", bw.status)
	}
	if !strings.Contains(bw.body.String(), "<p>hello</p>") {
		t.Fatalf("expected body to be captured, got %q", bw.body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("underlying recorder must not be written before commit, got %q", rec.Body.String())
	}

	bw.commit()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected commit to write 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<p>hello</p>") {
		t.Fatalf("expected committed body, got %q", rec.Body.String())
	}

	// After commit, writes pass through.
	_, _ = bw.Write([]byte(" extra"))
	if !strings.Contains(rec.Body.String(), " extra") {
		t.Fatalf("expected post-commit write to pass through, got %q", rec.Body.String())
	}
}

func TestBufferedResponseWriterFirstFlushDoesNotCommitHTML(t *testing.T) {
	// templ flushes at the end of every render; an HTML response must stay
	// captured so the middleware can convert it.
	rec := httptest.NewRecorder()
	bw := &bufferedResponseWriter{header: make(http.Header), underlying: rec}

	_, _ = bw.Write([]byte("<h1>hi</h1>"))
	bw.Flush() // templ's end-of-render flush

	if bw.committed {
		t.Fatal("expected HTML response to stay captured after templ's flush")
	}
	if bw.body.Len() == 0 {
		t.Fatal("expected body to remain in the capture buffer")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected nothing written to the underlying writer, got %q", rec.Body.String())
	}
}

func TestBufferedResponseWriterSSEFlushCommits(t *testing.T) {
	// An SSE stream sets text/event-stream before flushing; the first flush
	// must commit so the stream passes through.
	rec := httptest.NewRecorder()
	bw := &bufferedResponseWriter{header: make(http.Header), underlying: rec}
	bw.header.Set("Content-Type", "text/event-stream")

	_, _ = bw.Write([]byte("data: first\n\n"))
	bw.Flush() // first flush commits

	if !bw.committed {
		t.Fatal("expected first Flush to commit the SSE capture")
	}
	if !strings.Contains(rec.Body.String(), "data: first") {
		t.Fatalf("expected committed body to reach the recorder, got %q", rec.Body.String())
	}

	// Subsequent writes stream directly.
	_, _ = bw.Write([]byte("data: second\n\n"))
	bw.Flush()
	if !strings.Contains(rec.Body.String(), "data: second") {
		t.Fatalf("expected post-commit stream to pass through, got %q", rec.Body.String())
	}
}

func TestShouldConvert(t *testing.T) {
	tests := []struct {
		name   string
		status int
		ct     string
		body   string
		want   bool
	}{
		{name: "html explicit", status: 200, ct: "text/html", body: "<p>hi</p>", want: true},
		{name: "html sniffed", status: 200, ct: "", body: "<!DOCTYPE html><html><p>hi</p></html>", want: true},
		{name: "xhtml", status: 200, ct: "application/xhtml+xml", body: "<p>hi</p>", want: true},
		{name: "json", status: 200, ct: "application/json", body: `{"a":1}`, want: false},
		{name: "sse", status: 200, ct: "text/event-stream", body: "data: x\n", want: false},
		{name: "error status", status: 404, ct: "text/html", body: "<p>nope</p>", want: false},
		{name: "plain text", status: 200, ct: "text/plain", body: "hello", want: false},
		{name: "css", status: 200, ct: "text/css", body: "body{}", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bw := &bufferedResponseWriter{
				header: make(http.Header),
				status: tt.status,
			}
			if tt.ct != "" {
				bw.header.Set("Content-Type", tt.ct)
			}
			if tt.body != "" {
				_, _ = bw.body.WriteString(tt.body)
			}
			if got := bw.shouldConvert(); got != tt.want {
				t.Fatalf("shouldConvert() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConvertHTMLToMarkdown(t *testing.T) {
	out, err := convertHTMLToMarkdown([]byte(`
		<html><body>
		<h1>Title</h1>
		<p>Some <strong>bold</strong> text.</p>
		<a href="/panel/">Link</a>
		<ul><li>One</li><li>Two</li></ul>
		</body></html>
	`))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "# Title") {
		t.Fatalf("expected heading converted to # Title, got:\n%s", s)
	}
	if !strings.Contains(s, "**bold**") {
		t.Fatalf("expected bold converted, got:\n%s", s)
	}
	if !strings.Contains(s, "[Link](/panel/)") {
		t.Fatalf("expected link converted, got:\n%s", s)
	}
	if !strings.Contains(s, "- One") {
		t.Fatalf("expected list converted, got:\n%s", s)
	}
	if strings.Contains(s, "<div") || strings.Contains(s, "<html") {
		t.Fatalf("expected no raw HTML tags in markdown, got:\n%s", s)
	}
}

func TestMarkdownAcceptMiddlewareConverts(t *testing.T) {
	mw := BuildMarkdownAcceptMiddleware()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<h1>Hello</h1><p>World</p>"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	req.Header.Set("Accept", "text/markdown")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("expected text/markdown content type, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "# Hello") {
		t.Fatalf("expected converted markdown heading, got:\n%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<h1>") {
		t.Fatalf("expected raw HTML gone, got:\n%s", rec.Body.String())
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatal("expected Content-Length removed after conversion")
	}
}

func TestMarkdownAcceptMiddlewarePassesThrough(t *testing.T) {
	mw := BuildMarkdownAcceptMiddleware()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<h1>Keep</h1>"))
	}))

	// No Accept header → pass through unchanged.
	req := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "<h1>Keep</h1>") {
		t.Fatalf("expected HTML preserved without Accept, got %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html" {
		t.Fatalf("expected text/html, got %q", ct)
	}

	// */* → pass through.
	req2 := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	req2.Header.Set("Accept", "*/*")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "<h1>Keep</h1>") {
		t.Fatalf("expected HTML preserved for */*, got %q", rec2.Body.String())
	}
}

func TestMarkdownAcceptMiddlewareSkipsNonHTML(t *testing.T) {
	mw := BuildMarkdownAcceptMiddleware()

	// JSON response should pass through even with Accept: text/markdown.
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Accept", "text/markdown")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `{"a":1}`) {
		t.Fatalf("expected JSON preserved, got %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}
}

func TestMarkdownAcceptMiddlewareSkipsErrorStatus(t *testing.T) {
	mw := BuildMarkdownAcceptMiddleware()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Header.Set("Accept", "text/markdown")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("expected error body preserved, got %q", rec.Body.String())
	}
}

func TestMarkdownAcceptMiddlewareStreamingFlush(t *testing.T) {
	// A handler that flushes (like SSE) must commit on the first Flush and
	// stream subsequent data through, never being buffered forever.
	var buf bytes.Buffer
	flusher := &testFlusher{ResponseWriter: httptest.NewRecorder(), buf: &buf}
	handler := BuildMarkdownAcceptMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: first\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: second\n\n"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/topology", nil)
	req.Header.Set("Accept", "text/markdown")
	handler.ServeHTTP(flusher, req)

	if !strings.Contains(buf.String(), "data: first") {
		t.Fatalf("expected first SSE chunk streamed, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "data: second") {
		t.Fatalf("expected second SSE chunk streamed, got %q", buf.String())
	}
}

func TestMarkdownAcceptMiddlewareCarriesHeaders(t *testing.T) {
	// Captured headers (cookies, cache control) must survive conversion.
	mw := BuildMarkdownAcceptMiddleware()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Control", "no-store")
		http.SetCookie(w, &http.Cookie{Name: "tracer_session", Value: "abc", Path: "/"})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<h1>Hi</h1>"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	req.Header.Set("Accept", "text/markdown")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("expected Cache-Control carried over, got %q", cc)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "tracer_session" {
		t.Fatalf("expected Set-Cookie carried over, got %v", cookies)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("expected text/markdown content type, got %q", ct)
	}
}

func TestMarkdownAcceptMiddlewareSetsVaryOnEveryResponse(t *testing.T) {
	// The middleware produces two representations keyed on Accept, so every
	// response through it must declare Vary: Accept — even pass-throughs.
	mw := BuildMarkdownAcceptMiddleware()

	for _, tt := range []struct {
		name   string
		accept string
	}{
		{name: "markdown accept", accept: "text/markdown"},
		{name: "no markdown accept", accept: "text/html"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("<h1>Hi</h1>"))
			}))

			req := httptest.NewRequest(http.MethodGet, "/panel/", nil)
			req.Header.Set("Accept", tt.accept)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Accept" {
				t.Fatalf("expected single Vary: Accept header, got %v", got)
			}
		})
	}
}

type testFlusher struct {
	http.ResponseWriter
	buf *bytes.Buffer
}

func (t *testFlusher) Write(p []byte) (int, error) {
	return t.buf.Write(p)
}

func (t *testFlusher) WriteHeader(status int) {}

func (t *testFlusher) Header() http.Header { return make(http.Header) }

func (t *testFlusher) Flush() {}
