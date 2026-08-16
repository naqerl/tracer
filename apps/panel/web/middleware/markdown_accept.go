package middleware

import (
	"bufio"
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown"
)

// mdConverter converts HTML documents to markdown. It is stateless, so it is
// shared across requests.
var mdConverter = md.NewConverter("", true, nil)

// BuildMarkdownAcceptMiddleware converts HTML responses to markdown on the fly
// when the client requests it via an explicit Accept: text/markdown header.
//
// The conversion happens through a proxied response writer that captures the
// downstream handler's output; once the handler returns, an HTML response is
// re-emitted as markdown. Streaming responses (SSE) and anything that is not
// HTML are passed through untouched.
func BuildMarkdownAcceptMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Every response through this middleware is keyed on Accept: the
			// representation differs (HTML vs markdown) even when the handler
			// output is identical, so caches must store per-Accept variants.
			w.Header().Add("Vary", "Accept")

			if !acceptsMarkdown(r.Header.Get("Accept")) {
				next.ServeHTTP(w, r)
				return
			}

			bw := &bufferedResponseWriter{header: make(http.Header), underlying: w}
			next.ServeHTTP(bw, r)

			if !bw.shouldConvert() {
				bw.replay(w)
				return
			}

			// Turn hx-get placeholders (apps grid, stats, log groups, entries)
			// into anchors so the markdown keeps links to the dynamic
			// fragments instead of dropping the empty placeholders.
			html, err := anchorsFromHXGet(bw.body.Bytes())
			if err != nil {
				slog.Warn("could not convert hx-get blocks to anchors", "path", r.URL.Path, "error", err)
				html = bw.body.Bytes()
			}

			mdBody, err := convertHTMLToMarkdown(html)
			if err != nil {
				slog.Warn("could not convert HTML response to markdown", "path", r.URL.Path, "error", err)
				bw.replay(w)
				return
			}

			// Carry the captured headers (cookies, cache control, HX-*) onto
			// the converted response; only the content type and length change.
			h := w.Header()
			for k, vv := range bw.header {
				if strings.EqualFold(k, "Content-Type") || strings.EqualFold(k, "Content-Length") {
					continue
				}
				for _, v := range vv {
					h.Add(k, v)
				}
			}
			h.Set("Content-Type", "text/markdown; charset=utf-8")
			h.Del("Content-Length")
			if bw.status != 0 {
				w.WriteHeader(bw.status)
			}
			_, _ = w.Write(mdBody)
		})
	}
}

// acceptsMarkdown reports whether the Accept header explicitly requests
// markdown with a q-value at least as high as any HTML preference. A bare
// wildcard (*/*) or a browser's default does not trigger conversion.
func acceptsMarkdown(accept string) bool {
	accept = strings.TrimSpace(accept)
	if accept == "" || accept == "*/*" {
		return false
	}

	mdQ, mdOK := parseAcceptQ(accept, "text/markdown")
	if !mdOK || mdQ <= 0 {
		return false
	}

	htmlQ, htmlOK := parseAcceptQ(accept, "text/html")
	if !htmlOK || mdQ >= htmlQ {
		return true
	}
	return false
}

// parseAcceptQ returns the q-value (defaulting to 1) for a media type in an
// Accept header, and whether it is present. A malformed q-value (per RFC 9110
// it must be 0 to 1 with at most 3 decimals) excludes the media type.
func parseAcceptQ(accept, mediaType string) (float64, bool) {
	for _, part := range strings.Split(accept, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segments := strings.Split(part, ";")
		if !strings.EqualFold(strings.TrimSpace(segments[0]), mediaType) {
			continue
		}
		q := 1.0
		for _, param := range segments[1:] {
			param = strings.TrimSpace(param)
			kv := strings.SplitN(param, "=", 2)
			if !strings.EqualFold(kv[0], "q") {
				continue
			}
			if len(kv) != 2 || !qValueRegex.MatchString(kv[1]) {
				return 0, false
			}
			var v float64
			if _, err := fmt.Sscanf(kv[1], "%f", &v); err != nil {
				return 0, false
			}
			q = v
		}
		return q, true
	}
	return 0, false
}

// qValueRegex matches an RFC 9110 q-value: 0 to 1 with at most 3 decimals.
var qValueRegex = regexp.MustCompile(`^(0(\.\d{1,3})?|1(\.0{1,3})?)$`)

// bufferedResponseWriter captures a handler's response so the middleware can
// decide whether to convert it. It implements http.ResponseWriter plus
// http.Flusher (streaming responses commit on flush and pass through) and
// http.Hijacker.
type bufferedResponseWriter struct {
	header     http.Header
	status     int
	body       bytes.Buffer
	committed  bool
	underlying http.ResponseWriter
}

func (b *bufferedResponseWriter) Header() http.Header {
	if b.committed {
		return b.underlying.Header()
	}
	return b.header
}

func (b *bufferedResponseWriter) WriteHeader(status int) {
	if b.committed {
		b.underlying.WriteHeader(status)
		return
	}
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponseWriter) Write(p []byte) (int, error) {
	if b.committed {
		return b.underlying.Write(p)
	}
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

// Flush forwards the flush only once the capture is committed (streaming
// responses set text/event-stream before flushing, so those commit here and
// pass through). Templ renders also flush at the end of every render, but
// their Content-Type is HTML or unset, so the flush is swallowed and the
// capture stays buffered for conversion. Forwarding an uncommitted flush to
// the real writer would commit an implicit empty 200 before the middleware
// can act.
func (b *bufferedResponseWriter) Flush() {
	if b.committed {
		if f, ok := b.underlying.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	if strings.HasPrefix(b.header.Get("Content-Type"), "text/event-stream") {
		b.commit()
		if f, ok := b.underlying.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// Hijack delegates to the underlying writer (for websocket upgrades) and
// marks the capture committed: after a hijack the handler owns the
// connection, so the post-return convert/replay must not write to it.
func (b *bufferedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := b.underlying.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	b.committed = true
	return h.Hijack()
}

// commit copies the captured status/headers/body to the real writer and marks
// the proxy as pass-through for all subsequent writes.
func (b *bufferedResponseWriter) commit() {
	if b.committed {
		return
	}
	b.committed = true
	h := b.underlying.Header()
	for k, vv := range b.header {
		for _, v := range vv {
			h.Add(k, v)
		}
	}
	if b.status != 0 {
		b.underlying.WriteHeader(b.status)
	}
	if b.body.Len() > 0 {
		_, _ = b.underlying.Write(b.body.Bytes())
	}
	b.body.Reset()
}

// shouldConvert reports whether the captured response is a successful HTML
// response worth converting. Non-2xx, streaming, and non-HTML responses are
// excluded.
func (b *bufferedResponseWriter) shouldConvert() bool {
	if b.committed {
		return false
	}
	if b.status != 0 && (b.status < 200 || b.status >= 300) {
		return false
	}
	ct := b.header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(b.body.Bytes())
	}
	return strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/xhtml+xml")
}

// replay copies the captured response to the real writer as-is.
func (b *bufferedResponseWriter) replay(w http.ResponseWriter) {
	if b.committed {
		return
	}
	for k, vv := range b.header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	if b.status != 0 {
		w.WriteHeader(b.status)
	}
	if b.body.Len() > 0 {
		_, _ = w.Write(b.body.Bytes())
	}
}

// convertHTMLToMarkdown converts an HTML document/body to markdown.
func convertHTMLToMarkdown(html []byte) ([]byte, error) {
	out, err := mdConverter.ConvertString(string(html))
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}
