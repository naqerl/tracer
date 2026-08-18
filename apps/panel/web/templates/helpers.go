package templates

import (
	"encoding/json"
	"fmt"
	"html"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// idSanitizer replaces any character that is unsafe in an HTML element id.
var idSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// SanitizeID makes a collector or app name safe to embed in an element id.
func SanitizeID(raw string) string {
	safe := idSanitizer.ReplaceAllString(raw, "-")
	if safe == "" {
		return "node"
	}
	return safe
}

// jsonKV represents a key-value pair for JSON rendering
type jsonKV struct {
	Key   string
	Value any
}

// NavData holds data for the navigation bar
type NavData struct {
	Since string
}

// LevelFilter represents a log level filter option
type LevelFilter struct {
	Val   string
	Label string
	Color string
}

// parseJSON parses a JSON string into a slice of key-value pairs
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
	keys := slices.Sorted(maps.Keys(obj))
	var result []jsonKV
	for _, k := range keys {
		fullKey := k
		if prefix != "" {
			fullKey = prefix + "." + k
		}

		switch val := obj[k].(type) {
		case map[string]any:
			result = append(result, flattenJSONMap(val, fullKey)...)
		default:
			result = append(result, jsonKV{Key: fullKey, Value: val})
		}
	}
	return result
}

// secretKeySet are JSON keys whose values must never be rendered verbatim
// (API tokens, passwords, secrets).
var secretKeySet = map[string]bool{
	"token": true, "api_key": true, "apikey": true, "key": true,
	"secret": true, "password": true, "passwd": true, "auth": true,
}

// isSecretKey reports whether a flattened JSON key (e.g. "forgejo.token")
// refers to a secret value.
func isSecretKey(key string) bool {
	parts := strings.Split(key, ".")
	return secretKeySet[strings.ToLower(parts[len(parts)-1])]
}

// renderJSONKV renders one key-value pair, masking secret values.
func renderJSONKV(kv jsonKV) string {
	if isSecretKey(kv.Key) {
		return renderJSONValue(maskSecret(kv.Value))
	}
	return renderJSONValue(kv.Value)
}

// maskSecret replaces a secret string with "••••" plus its last four
// characters, keeping non-string secrets fully hidden.
func maskSecret(v any) string {
	s, ok := v.(string)
	if !ok || s == "" {
		return "••••"
	}
	if len(s) <= 4 {
		return "••••"
	}
	return "••••" + s[len(s)-4:]
}

// jsonViewerHTML generates HTML for a JSON viewer
func jsonViewerHTML(data string, valid bool) string {
	if !valid {
		return `<span class="text-sm text-fg-secondary/60">-</span>`
	}
	items := parseJSON(data)
	if len(items) == 0 {
		return `<span class="text-fg-secondary italic">empty object</span>`
	}
	var b strings.Builder
	b.WriteString(`<div class="json-viewer text-sm"><div class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 min-w-max">`)
	for _, kv := range items {
		b.WriteString(`<span class="text-fg-secondary whitespace-nowrap">`)
		b.WriteString(kv.Key)
		b.WriteString(`</span><span>`)
		b.WriteString(renderJSONKV(kv))
		b.WriteString(`</span>`)
	}
	b.WriteString(`</div></div>`)
	return b.String()
}

// jsonViewerFullHTML generates HTML for a full JSON viewer
func jsonViewerFullHTML(data string, valid bool) string {
	if !valid {
		return `<span class="text-sm text-fg-secondary/60">No context data available</span>`
	}
	items := parseJSON(data)
	if len(items) == 0 {
		return `<span class="text-fg-secondary italic">empty object</span>`
	}
	var b strings.Builder
	b.WriteString(`<div class="json-viewer text-sm"><div class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">`)
	for _, kv := range items {
		b.WriteString(`<span class="text-fg-secondary whitespace-nowrap">`)
		b.WriteString(kv.Key)
		b.WriteString(`:</span><span>`)
		b.WriteString(renderJSONKV(kv))
		b.WriteString(`</span>`)
	}
	b.WriteString(`</div></div>`)
	return b.String()
}

func renderJSONValue(v any) string {
	switch val := v.(type) {
	case nil:
		return `<span class="text-fg-secondary">null</span>`
	case bool:
		return fmt.Sprintf(`<span class="text-accent">%t</span>`, val)
	case float64:
		return fmt.Sprintf(`<span class="text-secondary">%v</span>`, val)
	case string:
		if strings.HasPrefix(val, "http://") || strings.HasPrefix(val, "https://") {
			return fmt.Sprintf(`<a href="%s" class="text-accent hover:underline break-all" target="_blank">%s</a>`, val, html.EscapeString(val))
		}
		return fmt.Sprintf(`<span class="break-words">%s</span>`, html.EscapeString(val))
	case []any:
		return fmt.Sprintf(`<span class="text-fg-secondary">[%d items]</span>`, len(val))
	default:
		return fmt.Sprintf(`<span class="text-fg-secondary">%v</span>`, val)
	}
}

// tern provides a ternary operator for use in templates
func tern(condition bool, a, b string) string {
	if condition {
		return a
	}
	return b
}
