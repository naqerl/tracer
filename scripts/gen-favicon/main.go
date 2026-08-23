// Command gen-favicon renders the panel's favicon SVG from the topology graph
// motif. The committed asset is regenerated output, not hand-edited SVG: change
// the geometry constants in this program, run `make favicon`, and commit the
// regenerated files. The apple-touch-icon PNG is rasterized from the SVG with
// librsvg by the Makefile pipeline.
//
// Every coordinate in the output is derived from the constants below plus the
// leaf angles: leaf centers sit on a shared reference circle so they line up
// perfectly, and each bezier edge connects a leaf's perimeter to the panel's.
package main

import (
	"bytes"
	"fmt"
	"log"
	"math"
	"os"
)

const (
	// canvas
	boxSize  = 1120.0
	bgSize   = 1040.0
	bgRadius = 120.0

	// node geometry
	refRadius   = 400.0 // circle the leaf centers sit on
	panelRadius = 75.0
	leafRadius  = 60.0

	// stroke widths
	strokePanel = 4.0
	strokeLeaf  = 3.0
	strokeCurve = 2.0

	// styling
	curveOpacity    = 0.55
	leafStrokeAlpha = 0.60
	leafTextOpacity = 0.90
	collectorSize   = 30.0
	leafSize        = 20.0

	// bezier bend, shared with the live topology graph
	bendFrac = 0.12
	bendMax  = 40.0
)

// leaf is one of the surrounding nodes: its angle in degrees from +x with y-down
// (matching SVG) and the label rendered on it.
type leaf struct {
	angle float64
	label string
}

// leaves returns the six leaf nodes. The top five are fanned evenly across a
// 120° arc centred on +y so they are symmetric and uniformly spaced; a sixth sits
// directly below the panel and is labelled "panel".
func leaves() []leaf {
	const topArc, topLeaves = 120.0, 5
	out := make([]leaf, 0, 6)
	for i := 0; i < topLeaves; i++ {
		angle := 90.0 + topArc/2 - float64(i)*topArc/float64(topLeaves-1)
		out = append(out, leaf{angle: angle, label: "app"})
	}
	return append(out, leaf{angle: 270.0, label: "panel"})
}

// pt is a point in the SVG coordinate space.
type pt struct{ x, y float64 }

// polar places a point at radius on the circle at angle degrees from +x, y-down.
func polar(angle, radius float64) pt {
	r := angle * math.Pi / 180
	return pt{radius * math.Cos(r), -radius * math.Sin(r)}
}

// fmtCoord renders a coordinate to one decimal, folding a negative zero to 0.0.
func fmtCoord(v float64) string {
	if math.Abs(v) < 0.05 {
		return "0.0"
	}
	return fmt.Sprintf("%.1f", v)
}

// edge is a quadratic bezier from the panel perimeter to a leaf perimeter,
// offset perpendicular to the segment midpoint (the topology graph's curve).
type edge struct {
	start, control, end pt
}

// bezierEdge builds the curved edge that joins a leaf to the central panel.
func bezierEdge(leafAngle float64) edge {
	dir := polar(leafAngle, 1) // unit vector pointing at the leaf
	start := pt{dir.x * panelRadius, dir.y * panelRadius}
	leaf := polar(leafAngle, refRadius)
	end := pt{leaf.x - dir.x*leafRadius, leaf.y - dir.y*leafRadius}

	mid := pt{(start.x + end.x) / 2, (start.y + end.y) / 2}
	vx, vy := end.x-start.x, end.y-start.y
	norm := math.Hypot(vx, vy)
	bend := math.Min(bendMax, norm*bendFrac)
	const direction = -1.0
	control := pt{mid.x + direction*(vy/norm)*bend, mid.y - direction*(vx/norm)*bend}
	return edge{start: start, control: control, end: end}
}

func main() {
	leafNodes := leaves()
	var edges []edge
	var centers []pt
	labels := make([]string, 0, len(leafNodes))
	for _, l := range leafNodes {
		edges = append(edges, bezierEdge(l.angle))
		centers = append(centers, polar(l.angle, refRadius))
		labels = append(labels, l.label)
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="%d %d %d %d">`+"\n",
		int(-boxSize/2), int(-boxSize/2), int(boxSize), int(boxSize))
	fmt.Fprint(&b, `  <defs><style>`)
	fmt.Fprint(&b, `.bg{fill:#1E1E1E}`)
	fmt.Fprintf(&b, `.central{fill:#0A0A0A;stroke:#808080;stroke-width:%.1f}`, strokePanel)
	fmt.Fprintf(&b, `.outer{fill:#0A0A0A;stroke:#808080;stroke-width:%.1f;stroke-opacity:%.2f}`, strokeLeaf, leafStrokeAlpha)
	fmt.Fprintf(&b, `.curve{fill:none;stroke:#808080;stroke-width:%.1f;stroke-opacity:%.2f}`, strokeCurve, curveOpacity)
	fmt.Fprint(&b, `.label{fill:#EEEEEE;font-family:sans-serif;text-anchor:middle;dominant-baseline:middle}`)
	fmt.Fprintf(&b, `.app{fill:#808080;font-family:sans-serif;text-anchor:middle;dominant-baseline:middle;font-size:%.0fpx;font-weight:400;opacity:%.2f}`, leafSize, leafTextOpacity)
	fmt.Fprint(&b, `</style></defs>`+"\n\n")
	fmt.Fprintf(&b, `  <rect class="bg" x="%d" y="%d" width="%d" height="%d" rx="%d"/>`+"\n\n",
		int(-bgSize/2), int(-bgSize/2), int(bgSize), int(bgSize), int(bgRadius))

	fmt.Fprint(&b, "  <!-- bezier edges from the panel to each leaf -->\n")
	for _, e := range edges {
		fmt.Fprintf(&b, `  <path class="curve" d="M %s %s Q %s %s %s %s"/>`+"\n",
			fmtCoord(e.start.x), fmtCoord(e.start.y),
			fmtCoord(e.control.x), fmtCoord(e.control.y),
			fmtCoord(e.end.x), fmtCoord(e.end.y))
	}

	fmt.Fprint(&b, "\n  <!-- central collector node -->\n")
	fmt.Fprintf(&b, "  <g>\n    <circle class=\"central\" r=\"%d\"/>\n", int(panelRadius))
	fmt.Fprintf(&b, `    <text class="label" style="font-size:%d;font-weight:700">collector</text>`+"\n", int(collectorSize))
	fmt.Fprint(&b, "  </g>\n\n")

	fmt.Fprint(&b, "  <!-- surrounding app leaves -->\n")
	for i, c := range centers {
		fmt.Fprintf(&b, "  <g>\n    <circle class=\"outer\" cx=\"%s\" cy=\"%s\" r=\"%d\"/>\n",
			fmtCoord(c.x), fmtCoord(c.y), int(leafRadius))
		fmt.Fprintf(&b, `    <text class="app" x="%s" y="%s">%s</text>`+"\n", fmtCoord(c.x), fmtCoord(c.y), labels[i])
		fmt.Fprint(&b, "  </g>\n")
	}
	fmt.Fprint(&b, "</svg>\n")

	if err := os.WriteFile("apps/panel/web/static/static/img/favicon.svg", b.Bytes(), 0o644); err != nil {
		log.Fatalf("could not write favicon.svg: %v", err)
	}
}
