package htmx

import "net/http"

// IsAjax returns true if the request was made by HTMX
func IsAjax(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
