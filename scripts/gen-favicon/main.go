// Command gen-favicon writes the panel favicon SVG and apple-touch PNG: a 16×16
// crisp-edge letter T on a dark field with a blue accent square.
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

const (
	grid    = 16
	pngSize = 180
	svgPath = "apps/panel/web/static/static/img/favicon.svg"
	pngPath = "apps/panel/web/static/static/img/favicon.png"
)

var (
	bg    = color.RGBA{0x0A, 0x0A, 0x0A, 0xFF}
	white = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	blue  = color.RGBA{0x25, 0x63, 0xEB, 0xFF}
)

const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" shape-rendering="crispEdges" role="img" aria-label="tracer">
  <rect width="16" height="16" fill="#0a0a0a"/>
  <rect x="2" y="2" width="10" height="3" fill="#ffffff"/>
  <rect x="6" y="2" width="3" height="12" fill="#ffffff"/>
  <rect x="13" y="11" width="3" height="3" fill="#2563eb"/>
</svg>
`

func fill(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			img.SetRGBA(xx, yy, c)
		}
	}
}

func render(size int) *image.RGBA {
	src := image.NewRGBA(image.Rect(0, 0, grid, grid))
	fill(src, 0, 0, grid, grid, bg)
	fill(src, 2, 2, 10, 3, white) // top bar
	fill(src, 6, 2, 3, 12, white) // stem
	fill(src, 13, 11, 3, 3, blue) // accent

	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dst.SetRGBA(x, y, src.RGBAAt(x*grid/size, y*grid/size))
		}
	}
	return dst
}

func main() {
	if err := os.WriteFile(svgPath, []byte(svg), 0o644); err != nil {
		log.Fatalf("could not write favicon.svg: %v", err)
	}

	f, err := os.Create(pngPath)
	if err != nil {
		log.Fatalf("could not create favicon.png: %v", err)
	}
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(f, render(pngSize)); err != nil {
		log.Fatalf("could not encode favicon.png: %v", err)
	}
}
