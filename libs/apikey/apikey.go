// Package apikey reads the API key a request carries.
package apikey

import (
	"net/http"
	"strings"
)

const bearerScheme = "bearer "

// FromHeader returns the API key carried by h.
//
// The X-API-Key header wins, and a standard Authorization: Bearer credential is
// accepted as well so that clients which can only set an authorization header
// reach the same endpoints.
func FromHeader(h http.Header) string {
	if h == nil {
		return ""
	}
	if key := strings.TrimSpace(h.Get("X-API-Key")); key != "" {
		return key
	}
	auth := strings.TrimSpace(h.Get("Authorization"))
	if len(auth) < len(bearerScheme) {
		return ""
	}
	if !strings.EqualFold(auth[:len(bearerScheme)], bearerScheme) {
		return ""
	}
	return strings.TrimSpace(auth[len(bearerScheme):])
}
