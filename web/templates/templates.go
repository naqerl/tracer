package templates

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"
)

type Templates struct {
	views *template.Template
}

func New(fsys fs.FS) Templates {
	funcMap := template.FuncMap{
		"formatDate":     formatDate,
		"formatDateTime": formatDateTime,
		"levelClass":     levelClass,
		"truncate":       truncate,
	}

	views := template.Must(template.New("").Funcs(funcMap).ParseFS(fsys, "views/*.html"))

	return Templates{views: views}
}

func (t Templates) RenderHTTP(w http.ResponseWriter, r *http.Request, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.views.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("Template error", "template", name, "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

func (t Templates) RenderError(w http.ResponseWriter, r *http.Request, title, message string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	data := map[string]any{
		"Title":   title,
		"Message": message,
		"Status":  status,
	}
	if err := t.views.ExecuteTemplate(w, "error", data); err != nil {
		slog.Error("Template error", "template", "error", "error", err)
		http.Error(w, message, status)
	}
}

func formatDate(t time.Time) string {
	return t.Format("2006-01-02")
}

func formatDateTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

func levelClass(level string) string {
	switch level {
	case "error", "ERROR":
		return "level-error"
	case "warn", "WARN", "warning", "WARNING":
		return "level-warn"
	case "info", "INFO":
		return "level-info"
	case "debug", "DEBUG":
		return "level-debug"
	default:
		return "level-default"
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
