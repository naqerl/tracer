package middleware

import (
	"context"
	"net/http"
	"strings"
)

const formatContextKey contextKey = "format"

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
				// Remove .md suffix from path
				newPath := strings.TrimSuffix(path, ".md")

				// For top-level section paths, add trailing slash to match router registration
				// This prevents redirects that would lose the markdown context
				if newPath == "/analytics" || newPath == "/logs" || newPath == "/settings" {
					newPath += "/"
				}

				r.URL.Path = newPath

				// Store format flag in context
				ctx := context.WithValue(r.Context(), formatContextKey, "markdown")
				r = r.WithContext(ctx)
			}

			next.ServeHTTP(w, r)
		})
	}
}
