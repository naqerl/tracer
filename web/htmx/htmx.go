package htmx

import "net/http"

// IsAjax returns true if the request was made by HTMX
func IsAjax(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// TODO: SO DAMN USE IT IN THE PROJECT IF YOU DECLARED IT!!!
// TriggerHeader returns the HX-Trigger header value
func TriggerHeader(r *http.Request) string {
	return r.Header.Get("HX-Trigger")
}

// TODO: SO DAMN USE IT IN THE PROJECT IF YOU DECLARED IT!!!
// SetTrigger sets an event trigger on the response
func SetTrigger(w http.ResponseWriter, event string) {
	w.Header().Set("HX-Trigger", event)
}

// TODO: SO DAMN USE IT IN THE PROJECT IF YOU DECLARED IT!!!
// SetRedirect sets a client-side redirect
func SetRedirect(w http.ResponseWriter, url string) {
	w.Header().Set("HX-Redirect", url)
}

// TODO: SO DAMN USE IT IN THE PROJECT IF YOU DECLARED IT!!!
// SetRefresh triggers a full page refresh
func SetRefresh(w http.ResponseWriter) {
	w.Header().Set("HX-Refresh", "true")
}
