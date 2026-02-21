package templates

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ai-shift/tracer/apps/panel/internal/middleware"
)

//go:embed shared/*.html
var sharedFS embed.FS

type Templates struct {
	views *template.Template
}

func New(fsys fs.FS) Templates {
	funcMap := template.FuncMap{
		"formatDate":      formatDate,
		"formatDateTime":  formatDateTime,
		"levelClass":      levelClass,
		"truncate":        truncate,
		"nullStr":         nullStr,
		"hasContext":      hasContext,
		"parseContext":    parseContext,
		"urlEncode":       url.QueryEscape,
		"toFloat":         toFloat,
		"divFloat":        divFloat,
		"mulFloat":        mulFloat,
		"sub":             func(a, b int) int { return a - b },
		"isAnalyticsPath": isAnalyticsPath,
		"isSettingsPath":  isSettingsPath,
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
	if middleware.IsMarkdownFormat(r) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, "# %s\n\n%s\n", title, message)
		return
	}

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

// RenderMarkdown renders data as markdown formatted text
func (t Templates) RenderMarkdown(w http.ResponseWriter, r *http.Request, name string, data any) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if err := t.views.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("Template error", "template", name, "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

// BreadcrumbSegment represents a single segment in the breadcrumb path
type BreadcrumbSegment struct {
	Text string // lowercase text, e.g., "analytics", "aishift", "apps", "mimi"
	URL  string // full URL up to this segment, e.g., "/analytics/aishift"
}

// NavData holds data for the shared navbar template
type NavData struct {
	Breadcrumbs []BreadcrumbSegment
}

// NavBuilder helps construct breadcrumb navigation
type NavBuilder struct {
	segments []BreadcrumbSegment
}

// NewNavBuilder creates a new navigation builder
func NewNavBuilder() *NavBuilder {
	return &NavBuilder{
		segments: make([]BreadcrumbSegment, 0),
	}
}

// Add appends a segment to the breadcrumb
func (b *NavBuilder) Add(text, urlPart string) *NavBuilder {
	var url string
	if len(b.segments) > 0 {
		url = b.segments[len(b.segments)-1].URL + "/" + urlPart
	} else {
		url = "/" + urlPart
	}
	b.segments = append(b.segments, BreadcrumbSegment{
		Text: text,
		URL:  url,
	})
	return b
}

// Build returns the NavData
func (b *NavBuilder) Build() NavData {
	return NavData{
		Breadcrumbs: b.segments,
	}
}

// NewNavData creates navbar data (kept for backwards compatibility during migration)
func NewNavData(title, activePage, username string) NavData {
	// Convert old format to new breadcrumb format
	builder := NewNavBuilder()
	if activePage == "analytics" {
		builder.Add("analytics", "analytics")
	} else if activePage == "settings" {
		builder.Add("settings", "settings")
	}
	return builder.Build()
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
	switch strings.ToLower(level) {
	case "error":
		return "level-error"
	case "warn", "warning":
		return "level-warn"
	case "info":
		return "level-info"
	case "debug":
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

func isAnalyticsPath(breadcrumbs []BreadcrumbSegment) bool {
	if len(breadcrumbs) == 0 {
		return false
	}
	return breadcrumbs[0].Text == "analytics"
}

func isSettingsPath(breadcrumbs []BreadcrumbSegment) bool {
	if len(breadcrumbs) == 0 {
		return false
	}
	return breadcrumbs[0].Text == "settings"
}

func parseContext(v any) map[string]string {
	var contextStr string

	switch ctx := v.(type) {
	case string:
		contextStr = ctx
	case sql.NullString:
		if ctx.Valid {
			contextStr = ctx.String
		}
	default:
		return nil
	}

	if contextStr == "" || contextStr == "{}" {
		return nil
	}

	// Try to parse as JSON
	var data map[string]any
	if err := json.Unmarshal([]byte(contextStr), &data); err != nil {
		// If not JSON, return as single "raw" entry
		return map[string]string{"raw": contextStr}
	}

	// Convert to string map
	result := make(map[string]string)
	for k, v := range data {
		result[k] = fmt.Sprintf("%v", v)
	}

	return result
}
