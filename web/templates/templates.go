package templates

import (
	"database/sql"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

//go:embed shared/*.html
var sharedFS embed.FS

type Templates struct {
	views *template.Template
}

func New(fsys fs.FS) Templates {
	funcMap := template.FuncMap{
		"formatDate":     formatDate,
		"formatDateTime": formatDateTime,
		"levelClass":     levelClass,
		"truncate":       truncate,
		"nullStr":        nullStr,
		"hasContext":     hasContext,
		"urlEncode":      url.QueryEscape,
		"toFloat":        toFloat,
		"divFloat":       divFloat,
		"mulFloat":       mulFloat,
	}

	// Parse shared templates first
	views := template.Must(template.New("").Funcs(funcMap).ParseFS(sharedFS, "shared/*.html"))
	// Then parse feature-specific views
	views = template.Must(views.ParseFS(fsys, "views/*.html"))

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

// NavData holds data for the shared navbar template
type NavData struct {
	Title      string
	ActivePage string
	Username   string
}

// NewNavData creates navbar data
func NewNavData(title, activePage, username string) NavData {
	return NavData{
		Title:      title,
		ActivePage: activePage,
		Username:   username,
	}
}

func formatDate(t any) string {
	switch v := t.(type) {
	case time.Time:
		return v.Format("2006-01-02")
	case sql.NullTime:
		if v.Valid {
			return v.Time.Format("2006-01-02")
		}
		return ""
	default:
		return ""
	}
}

func formatDateTime(t any) string {
	switch v := t.(type) {
	case time.Time:
		return v.Format("2006-01-02 15:04:05")
	case sql.NullTime:
		if v.Valid {
			return v.Time.Format("2006-01-02 15:04:05")
		}
		return ""
	default:
		return ""
	}
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

func nullStr(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case sql.NullString:
		if s.Valid {
			return s.String
		}
		return ""
	default:
		return ""
	}
}

func hasContext(v any) bool {
	switch s := v.(type) {
	case string:
		return s != "" && s != "{}"
	case sql.NullString:
		return s.Valid && s.String != "" && s.String != "{}"
	default:
		return false
	}
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	case uint64:
		return float64(n)
	default:
		return 0
	}
}

func divFloat(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func mulFloat(a, b float64) float64 {
	return a * b
}
