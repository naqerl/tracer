// Package htmx provides utilities for working with HTMX requests and responses.
package htmx

import "net/http"

// Redirect sends an HTMX-compatible redirect response.
// For HTMX requests, it sets the HX-Redirect header.
// For regular requests, it performs a standard HTTP redirect.
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusOK)
	} else {
		http.Redirect(w, r, url, http.StatusSeeOther)
	}
}
