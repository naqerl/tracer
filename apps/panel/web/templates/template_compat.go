package templates

import (
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
)

//go:embed shared.html
var sharedFS embed.FS

// Templates provides backward compatibility for handlers still using template name rendering.
// New code should call templ components directly.
type Templates struct {
	views *template.Template
}

// New creates a Templates instance from an embed.FS containing views/*.html files.
// Deprecated: Use templ components directly instead.
func New(fsys embed.FS) Templates {
	funcMap := template.FuncMap{
		"default": func(v, defaultVal any) any {
			if v == nil || v == "" || v == 0 {
				return defaultVal
			}
			return v
		},
		"mul": func(a, b int) int {
			return a * b
		},
		"parseJSON":       parseJSON,
		"flattenJSON":     flattenJSON,
		"renderJSONValue": renderJSONValue,
		"toJSON": func(v any) string {
			b, err := json.Marshal(v)
			if err != nil {
				return "{}"
			}
			return string(b)
		},
		"tern": tern,
	}
	// Start with shared.html from this package
	views := template.Must(template.New("").Funcs(funcMap).ParseFS(sharedFS, "shared.html"))
	// Parse handler-specific views
	views = template.Must(views.ParseFS(fsys, "views/*.html"))
	return Templates{views: views}
}

// RenderHTTP renders a template by name with the given data.
// Deprecated: Use templ components directly.
func (t *Templates) RenderHTTP(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if t.views != nil {
		if tmpl := t.views.Lookup(name); tmpl != nil {
			if err := tmpl.Execute(w, data); err != nil {
				ErrorPage("Error", "Failed to render template", 500, NavData{Since: "24h"}).Render(r.Context(), w)
			}
			return
		}
	}
	ErrorPage("Not Found", "Template not found: "+name, 404, NavData{Since: "24h"}).Render(r.Context(), w)
}

// RenderError renders an error page.
// Deprecated: Use templates.ErrorPage directly.
func (t *Templates) RenderError(w http.ResponseWriter, r *http.Request, title, message string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	ErrorPage(title, message, status, NavData{Since: "24h"}).Render(r.Context(), w)
}

// RenderMarkdown renders a template as markdown.
// Deprecated: This is a no-op for now. Handle markdown rendering separately.
func (t *Templates) RenderMarkdown(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if t.views != nil {
		if tmpl := t.views.Lookup(name); tmpl != nil {
			if err := tmpl.Execute(w, data); err != nil {
				w.Write([]byte("Error rendering markdown"))
			}
			return
		}
	}
	w.Write([]byte("Markdown content not available"))
}
