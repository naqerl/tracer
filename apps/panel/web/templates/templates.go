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

	"tracer/apps/panel/web/middleware"
)

// jsonKV represents a key-value pair for JSON rendering
type jsonKV struct {
	Key   string
	Value any
}

//go:embed shared.html
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
		"parseContext":   parseContext,
		"urlEncode":      url.QueryEscape,
		"toFloat":        toFloat,
		"divFloat":       divFloat,
		"mulFloat":       mulFloat,
		"sub":            func(a, b int) int { return a - b },
		"mul":            func(a, b int) int { return a * b },
		"default": func(def, val any) any {
			if val == nil || val == "" {
				return def
			}
			return val
		},
		"toJSON":          toJSON,
		"dict":            dict,
		"parseJSON":       parseJSON,
		"flattenJSON":     flattenJSON,
		"renderJSONValue": renderJSONValue,
	}

	// Parse shared templates first
	views := template.Must(template.New("").Funcs(funcMap).ParseFS(sharedFS, "shared.html"))
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
	case int64:
		return time.Unix(v, 0).Format("2006-01-02 15:04:05")
	case sql.NullInt64:
		if v.Valid {
			return time.Unix(v.Int64, 0).Format("2006-01-02 15:04:05")
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

func dict(values ...any) map[string]any {
	if len(values)%2 != 0 {
		return nil
	}
	d := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil
		}
		d[key] = values[i+1]
	}
	return d
}

func toJSON(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return template.JS("{}")
	}
	return template.JS(b)
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

// parseJSON parses a JSON string into a map
func parseJSON(data string) []jsonKV {
	if data == "" || data == "{}" {
		return nil
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return nil
	}

	return flattenJSONMap(obj, "")
}

func flattenJSONMap(obj map[string]any, prefix string) []jsonKV {
	var result []jsonKV
	for k, v := range obj {
		fullKey := k
		if prefix != "" {
			fullKey = prefix + "." + k
		}

		switch val := v.(type) {
		case map[string]any:
			result = append(result, flattenJSONMap(val, fullKey)...)
		default:
			result = append(result, jsonKV{Key: fullKey, Value: val})
		}
	}
	return result
}

func flattenJSON(data []jsonKV) []jsonKV {
	return data
}

func renderJSONValue(v any) template.HTML {
	switch val := v.(type) {
	case nil:
		return template.HTML(`<span class="text-fg-secondary">null</span>`)
	case bool:
		return template.HTML(fmt.Sprintf(`<span class="text-accent">%t</span>`, val))
	case float64:
		return template.HTML(fmt.Sprintf(`<span class="text-secondary">%v</span>`, val))
	case string:
		if strings.HasPrefix(val, "http://") || strings.HasPrefix(val, "https://") {
			return template.HTML(fmt.Sprintf(`<a href="%s" class="text-accent hover:underline break-all" target="_blank">%s</a>`, val, val))
		}
		return template.HTML(fmt.Sprintf(`<span class="break-words">%s</span>`, template.HTMLEscapeString(val)))
	case []any:
		return template.HTML(fmt.Sprintf(`<span class="text-fg-secondary">[%d items]</span>`, len(val)))
	default:
		return template.HTML(fmt.Sprintf(`<span class="text-fg-secondary">%v</span>`, val))
	}
}
