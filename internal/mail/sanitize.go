package mail

import (
	"net/url"

	"github.com/microcosm-cc/bluemonday"
)

var htmlPolicy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowDataURIImages()
	p.AllowTables()
	p.AllowElements("center", "font", "span", "div", "section", "header", "footer")
	p.AllowAttrs("color", "face", "size").OnElements("font")
	p.AllowAttrs("align", "valign", "width", "height", "bgcolor", "border", "cellpadding", "cellspacing").Globally()
	p.AllowStyles(
		"color", "background-color", "font-family", "font-size", "font-weight", "font-style", "line-height",
		"text-align", "text-decoration", "text-transform", "letter-spacing", "vertical-align", "white-space",
		"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
		"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
		"border", "border-top", "border-right", "border-bottom", "border-left", "border-color", "border-width", "border-style", "border-radius", "border-collapse",
		"width", "max-width", "min-width", "height", "max-height", "display",
	).Globally()
	p.AllowURLSchemes("mailto", "http", "https")
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.RewriteSrc(func(u *url.URL) {
		if u.Scheme != "data" {
			*u = url.URL{}
		}
	})
	return p
}()

func SanitizeHTML(html string) string {
	return htmlPolicy.Sanitize(html)
}
