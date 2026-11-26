package mail

import (
	"bytes"
	"html"
	"io"
	"net/url"
	"strings"

	"github.com/emersion/go-message/mail"
	"github.com/microcosm-cc/bluemonday"
	xhtml "golang.org/x/net/html"
)

func newHTMLPolicy(links, keepRemoteImages bool) *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowDataURIImages()
	p.AllowTables()
	p.AllowElements("center", "font", "span", "div", "section", "header", "footer")
	p.AllowAttrs("data-aether-signature").OnElements("div")
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
	p.AllowRelativeURLs(false)
	if !links {
		for _, scheme := range []string{"mailto", "http", "https"} {
			p.AllowURLSchemeWithCustomPolicy(scheme, func(*url.URL) bool { return false })
		}
	}
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	if !keepRemoteImages {
		p.RewriteSrc(func(u *url.URL) {
			if u.Scheme != "data" {
				*u = url.URL{}
			}
		})
	}
	return p
}

var (
	htmlPolicy        = newHTMLPolicy(true, false)
	previewPolicy     = newHTMLPolicy(false, false)
	messageHTMLPolicy = newHTMLPolicy(true, true)
)

func SanitizeHTML(html string) string {
	return htmlPolicy.Sanitize(html)
}

func SanitizePreviewHTML(html string) string {
	return previewPolicy.Sanitize(html)
}

func TextPreview(raw []byte, limit int) string {
	mr, _ := mail.CreateReader(bytes.NewReader(raw))
	if mr == nil {
		return ""
	}
	var htmlText string
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		h, ok := part.Header.(*mail.InlineHeader)
		if !ok {
			continue
		}
		t, _, _ := h.ContentType()
		if t != "text/plain" && t != "text/html" {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(part.Body, 256<<10))
		if t == "text/plain" {
			return clip(strings.TrimSpace(string(body)), limit)
		}
		if htmlText == "" {
			htmlText = strings.Join(strings.Fields(html.UnescapeString(bluemonday.StrictPolicy().Sanitize(string(body)))), " ")
		}
	}
	return clip(htmlText, limit)
}

func clip(s string, limit int) string {
	if r := []rune(s); len(r) > limit {
		return strings.TrimSpace(string(r[:limit])) + "…"
	}
	return s
}

func SanitizeMessageHTML(html string) string {
	return messageHTMLPolicy.Sanitize(upgradeProtocolRelativeImages(html))
}

func upgradeProtocolRelativeImages(in string) string {
	z := xhtml.NewTokenizer(strings.NewReader(in))
	var out strings.Builder
	for z.Next() != xhtml.ErrorToken {
		raw := string(z.Raw())
		t := z.Token()
		changed := false
		if (t.Type == xhtml.StartTagToken || t.Type == xhtml.SelfClosingTagToken) && t.Data == "img" {
			for i, a := range t.Attr {
				if v := strings.TrimSpace(a.Val); a.Key == "src" && strings.HasPrefix(v, "//") {
					t.Attr[i].Val, changed = "https:"+v, true
				}
			}
		}
		if changed {
			out.WriteString(t.String())
		} else {
			out.WriteString(raw)
		}
	}
	return out.String()
}
