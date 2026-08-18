package static

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
)

//go:embed static
var StaticFS embed.FS

func InitMux() *http.ServeMux {
	mux := http.NewServeMux()
	fs, err := fs.Sub(StaticFS, "static")
	if err != nil {
		log.Fatalf("could not create sub file system for static with %v", err)
	}
	mux.Handle("/", cacheControl(http.FileServer(http.FS(fs))))
	return mux
}

// cacheControl makes browsers revalidate embedded JS/CSS files on every page
// load; the files are unversioned, so a long max-age would serve stale assets
// after an upgrade.
func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
		next.ServeHTTP(w, r)
	})
}
