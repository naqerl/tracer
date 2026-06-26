package static

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed htmx.min.js anime.umd.min.js uPlot.iife.min.js uPlot.min.css
var assets embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(assets, ".")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
