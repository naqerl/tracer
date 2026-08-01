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
	mux.Handle("/", http.FileServer(http.FS(fs)))
	return mux
}
