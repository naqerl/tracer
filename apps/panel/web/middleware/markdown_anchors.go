package middleware

import (
	"bytes"
	"fmt"
	"html"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// anchorsFromHXGet prepares an HTML document for markdown conversion in one
// DOM pass: hx-get placeholders are rewritten into anchors, markdown-exclude
// elements are dropped, and link titles (hover hints) are stripped. A div with
// content gets its content wrapped in a link to the hx-get URL; an empty div
// becomes a link labelled with the path; an anchor without an href (nav/filter
// links) gets one.
func anchorsFromHXGet(htmlBody []byte) ([]byte, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(htmlBody))
	if err != nil {
		return nil, err
	}
	doc.Find("[markdown-exclude]").Remove()
	// Link titles are browser-only hover hints; they would leak into the
	// exported markdown as quoted link titles.
	doc.Find("a[title]").RemoveAttr("title")

	doc.Find("[hx-get]").Each(func(_ int, el *goquery.Selection) {
		// A nested hx-get inside an anchor (e.g. log-group filter links
		// inside the hx-get root div) is left as-is: wrapping an anchor in
		// an anchor would produce invalid HTML.
		if el.ParentsFiltered("a").Length() > 0 {
			return
		}

		href, ok := el.Attr("hx-get")
		if !ok || href == "" {
			return
		}
		escaped := html.EscapeString(href)

		if el.Is("a") {
			if _, has := el.Attr("href"); !has {
				el.SetAttr("href", href)
			}
			return
		}

		if strings.TrimSpace(el.Text()) != "" {
			el.WrapInnerHtml(fmt.Sprintf(`<a href="%s"></a>`, escaped))
			return
		}
		el.ReplaceWithHtml(fmt.Sprintf(`<a href="%s">%s</a>`, escaped, html.EscapeString(anchorLabel(href))))
	})

	out, err := doc.Html()
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// anchorLabel derives a readable label for an empty hx-get placeholder from
// its path, dropping the /panel prefix so dashboard apps/stats placeholders
// read as their route (e.g. "collectors/demo/apps").
func anchorLabel(href string) string {
	p := strings.TrimPrefix(href, "/panel")
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return href
	}
	return p
}
