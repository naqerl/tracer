package templates

import (
	"net/http"
	"strconv"
	"time"
)

const (
	SinceCookieName   = "tracer_panel_since"
	DefaultSinceLabel = "24h"
)

var validSinceLabels = map[string]bool{
	"1h":  true,
	"6h":  true,
	"24h": true,
	"7d":  true,
	"30d": true,
}

// ReadSince reads and validates the since cookie, returning its label and
// parsed time. It ensures the cookie exists with a valid value.
func ReadSince(w http.ResponseWriter, r *http.Request) (string, time.Time) {
	label := EnsureSinceCookie(w, r)
	return label, ParseSinceLabel(label)
}

// EnsureSinceCookie returns the validated since cookie value, setting a
// default if missing or invalid.
func EnsureSinceCookie(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie(SinceCookieName)
	if err == nil {
		label := SanitizeSinceLabel(cookie.Value)
		if label == cookie.Value {
			return label
		}
	}
	SetSinceCookie(w, r, DefaultSinceLabel)
	return DefaultSinceLabel
}

// SetSinceCookie writes the since cookie with the given label.
func SetSinceCookie(w http.ResponseWriter, r *http.Request, label string) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     SinceCookieName,
		Value:    label,
		Path:     "/",
		MaxAge:   31536000,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ParseSinceLabel converts a since cookie label into an absolute time.
func ParseSinceLabel(label string) time.Time {
	label = SanitizeSinceLabel(label)
	duration, err := time.ParseDuration(label)
	if err != nil {
		if len(label) > 1 && label[len(label)-1] == 'd' {
			days, err := strconv.Atoi(label[:len(label)-1])
			if err == nil {
				return time.Now().Add(-time.Duration(days) * 24 * time.Hour)
			}
		}
		return time.Now().Add(-24 * time.Hour)
	}
	return time.Now().Add(-duration)
}

// SanitizeSinceLabel normalizes a since label to a known value.
func SanitizeSinceLabel(label string) string {
	if validSinceLabels[label] {
		return label
	}
	return DefaultSinceLabel
}
