package main

import (
	"embed"
	"flag"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"tracer/libs/logx"
)

//go:embed views/*.html shared/*.html
var viewsFS embed.FS

type templates struct {
	views *template.Template
}

func newTemplates() templates {
	funcMap := template.FuncMap{
		"truncate": func(s string, maxLen int) string {
			if len(s) <= maxLen {
				return s
			}
			return s[:maxLen] + "..."
		},
	}

	views := template.Must(template.New("").Funcs(funcMap).ParseFS(viewsFS, "shared/*.html", "views/*.html"))
	return templates{views: views}
}

func (t templates) renderHTML(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.views.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("template error", "template", name, "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

func (t templates) renderMarkdown(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if err := t.views.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("template error", "template", name, "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

func isMarkdownFormat(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, ".md") || r.Header.Get("Accept") == "text/markdown"
}

func main() {
	port := flag.String("port", "8080", "port to listen on")
	flag.Parse()

	logx.SetDefault()

	tmpl := newTemplates()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		data := map[string]any{
			"Title": "Tracer - Lightweight Log Aggregation",
		}

		if isMarkdownFormat(r) {
			tmpl.renderMarkdown(w, "landing-markdown", data)
		} else {
			tmpl.renderHTML(w, "landing", data)
		}
	})

	slog.Info("Landing server starting", "addr", ":"+*port)
	if err := http.ListenAndServe(":"+*port, mux); err != nil {
		slog.Error("Server error", "error", err)
		os.Exit(1)
	}
}
