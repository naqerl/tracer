package middleware

import (
	"context"
	"net/http"
	"strings"
)

type markdownContextKey string

const formatContextKey markdownContextKey = "format"

// GetFormat returns the format from request context ("markdown" or "")
func GetFormat(r *http.Request) string {
	format, _ := r.Context().Value(formatContextKey).(string)
	return format
}

// IsMarkdownFormat returns true if markdown format is requested
func IsMarkdownFormat(r *http.Request) bool {
	return GetFormat(r) == "markdown"
}

// BuildMarkdownMiddleware creates middleware that intercepts .md suffix
// Must run BEFORE auth middleware to modify the path before routing
func BuildMarkdownMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Check if path ends with .md
			if strings.HasSuffix(path, ".md") {
				// Remove .md suffix from path and update request
				trimmed := strings.TrimSuffix(path, ".md")

				// Avoid ServeMux trailing slash redirects for section roots.
				if trimmed == "/panel" {
					trimmed = "/panel/"
				}
				if trimmed == "/topology" {
					trimmed = "/topology/"
				}
				if trimmed == "/settings" {
					trimmed = "/settings/"
				}

				r.URL.Path = trimmed

				// Store format flag in context
				ctx := context.WithValue(r.Context(), formatContextKey, "markdown")
				r = r.WithContext(ctx)
			}

			next.ServeHTTP(w, r)
		})
	}
}
