package view

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	panelsvc "tracer/apps/panel/internal/services/panel"
)

func TestAppCardSkeletonRendersHealthBucketCountBars(t *testing.T) {
	doc := renderComponent(t, CollectorAppsSkeleton(1))
	if got := doc.Find(".rounded-full").Length(); got != panelsvc.HealthBucketCount {
		t.Fatalf("skeleton card rendered %d bars, want %d", got, panelsvc.HealthBucketCount)
	}
}

func TestAppCardSkeletonKeepsLoadedCardBoxes(t *testing.T) {
	skeleton := renderComponent(t, AppCard("demo", AppView{}, true))
	loaded := renderComponent(t, AppCard("demo", AppView{
		Name:       "demo-api",
		HasUptime:  true,
		Bars:       placeholderUptimeBars(),
		ErrorCount: "3",
	}, false))

	skeletonBoxes := cardBoxes(t, skeleton)
	loadedBoxes := cardBoxes(t, loaded)
	for i, want := range loadedBoxes {
		if skeletonBoxes[i] != want {
			t.Fatalf("skeleton box %d is %q, loaded box is %q", i, skeletonBoxes[i], want)
		}
	}
}

func TestAppCardSkeletonCentresTextPlaceholders(t *testing.T) {
	skeleton := renderComponent(t, AppCard("demo", AppView{}, true))
	card := skeleton.Find("body").Children().First()
	lines := card.Children().First()
	if lines.Children().Length() != 3 {
		t.Fatalf("card text block holds %d lines, want the name, stats and storage lines", lines.Children().Length())
	}
	for i := range lines.Children().Length() {
		line := lines.Children().Eq(i)
		wantRects := 1
		if i == 2 {
			wantRects = 3
		}
		if got := line.Find("[class*='bg-fg/10']").Length(); got != wantRects {
			t.Fatalf("line %d holds %d placeholder rectangles, want %d", i, got, wantRects)
		}
		line.Find("[class*='bg-fg/10']").Each(func(_ int, rect *goquery.Selection) {
			holder := rect.Parent()
			if !hasClass(holder, "items-center") || heightClass(holder.AttrOr("class", "")) == "" {
				t.Fatalf("line %d: placeholder %q is not centred in a box holding the line height, so it sticks to the line below",
					i, rect.AttrOr("class", ""))
			}
		})
	}
}

func renderComponent(t *testing.T, component interface {
	Render(ctx context.Context, w io.Writer) error
},
) *goquery.Document {
	t.Helper()
	var buf bytes.Buffer
	if err := component.Render(t.Context(), &buf); err != nil {
		t.Fatalf("could not render component: %v", err)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("could not parse rendered component: %v", err)
	}
	return doc
}

// cardBoxes lists the classes of the boxes that decide the card's size: the
// card itself, its two text lines and its uptime bars. Hover and loading
// effects do not change the geometry and are left out.
func cardBoxes(t *testing.T, doc *goquery.Document) []string {
	t.Helper()
	card := doc.Find("body").Children().First()
	if card.Length() != 1 || !card.Is("div, a") {
		t.Fatalf("expected the card to be the only rendered element, got %d", card.Length())
	}
	lines := card.Children().First()
	bars := card.Children().Last()
	if card.Children().Length() != 2 {
		t.Fatalf("card holds %d boxes, want its text block and its bars", card.Children().Length())
	}
	boxes := []string{
		layoutClasses(card),
		layoutClasses(lines),
	}
	for i := range lines.Children().Length() {
		boxes = append(boxes, layoutClasses(lines.Children().Eq(i)))
	}
	return append(boxes, layoutClasses(bars))
}

// layoutClasses keeps the classes that size a box and drops the ones that only
// paint or animate it.
func layoutClasses(sel *goquery.Selection) string {
	keep := map[string]bool{
		"p-3": true, "flex": true, "flex-col": true, "min-w-0": true, "relative": true,
		"gap-2": true, "items-center": true, "font-medium": true, "truncate": true,
		"text-xs": true,
	}
	classes := strings.Fields(sel.AttrOr("class", ""))
	kept := make([]string, 0, len(classes))
	for _, class := range classes {
		if keep[class] {
			kept = append(kept, class)
		}
	}
	return strings.Join(kept, " ")
}

// hasClass reports whether the box carries the class.
func hasClass(sel *goquery.Selection, class string) bool {
	return slices.Contains(strings.Fields(sel.AttrOr("class", "")), class)
}

// heightClass returns the Tailwind height class a box is sized with.
func heightClass(classes string) string {
	for _, class := range strings.Fields(classes) {
		if strings.HasPrefix(class, "h-") {
			return class
		}
	}
	return ""
}
