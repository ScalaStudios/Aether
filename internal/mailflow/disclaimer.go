package mailflow

import (
	"html"
	"mime"
	"strings"
)

func protectedType(t string) bool {
	switch t {
	case "multipart/signed", "multipart/encrypted", "application/pkcs7-mime", "application/x-pkcs7-mime":
		return true
	}
	return false
}

func AppendDisclaimer(h []Field, body []byte, d Disclaimer) ([]Field, []byte, bool) {
	body = crlf(body)
	if t, _ := mediaType(h); protectedType(t) {
		switch d.Fallback {
		case "skip":
			return h, body, true
		case "reject":
			return h, body, false
		}
	} else if nh, nb, ok := footer(h, body, d, 0); ok {
		return withMIMEVersion(nh), nb, true
	}
	h, body = wrap(h, body, d)
	return withMIMEVersion(h), body, true
}

func withMIMEVersion(h []Field) []Field {
	if get(h, "MIME-Version") == "" {
		return append(h, Field{Name: "MIME-Version", Value: "1.0"})
	}
	return h
}

func footer(h []Field, body []byte, d Disclaimer, depth int) ([]Field, []byte, bool) {
	t, params := mediaType(h)
	switch t {
	case "text/plain":
		text, ok := decodedText(h, body, 64<<20)
		if !ok {
			return h, body, false
		}
		return textPart(h, t, params, strings.TrimRight(text, "\r\n")+"\r\n\r\n"+d.Text+"\r\n")
	case "text/html":
		doc, ok := decodedText(h, body, 64<<20)
		if !ok {
			return h, body, false
		}
		snippet := d.HTML
		if snippet == "" {
			snippet = "<p>" + strings.ReplaceAll(html.EscapeString(d.Text), "\n", "<br>") + "</p>"
		}
		snippet = "<div class=\"aether-disclaimer\">" + snippet + "</div>"
		if i := strings.LastIndex(strings.ToLower(doc), "</body"); i >= 0 {
			doc = doc[:i] + snippet + doc[i:]
		} else {
			doc += snippet
		}
		return textPart(h, t, params, doc)
	case "multipart/alternative", "multipart/mixed", "multipart/related":
		if depth > 10 {
			return h, body, false
		}
		mp, ok := splitMultipart(body, params["boundary"])
		if !ok {
			return h, body, false
		}
		changed := false
		for i, raw := range mp.parts {
			if t != "multipart/alternative" && i > 0 {
				break
			}
			child := parseEntity(raw)
			if ct, _ := mediaType(child.header); t == "multipart/alternative" && ct != "text/plain" && ct != "text/html" && !strings.HasPrefix(ct, "multipart/") {
				continue
			}
			nh, nb, ok := footer(child.header, child.body, d, depth+1)
			if !ok {
				return h, body, false
			}
			mp.parts[i] = entity{header: nh, body: nb}.bytes()
			changed = true
		}
		return h, mp.join(params["boundary"]), changed
	}
	return h, body, false
}

func textPart(h []Field, t string, params map[string]string, text string) ([]Field, []byte, bool) {
	params["charset"] = "utf-8"
	h = setField(h, "Content-Type", mime.FormatMediaType(t, params))
	h = setField(h, "Content-Transfer-Encoding", "quoted-printable")
	return h, quotedPrintable(text), true
}

func wrap(h []Field, body []byte, d Disclaimer) ([]Field, []byte) {
	inner := []Field{{Name: "Content-Type", Value: get(h, "Content-Type")}}
	if inner[0].Value == "" {
		inner[0].Value = "text/plain; charset=us-ascii"
	}
	if cte := get(h, "Content-Transfer-Encoding"); cte != "" {
		inner = append(inner, Field{Name: "Content-Transfer-Encoding", Value: cte})
	}
	note := entity{
		header: []Field{{Name: "Content-Type", Value: "text/plain; charset=utf-8"}, {Name: "Content-Transfer-Encoding", Value: "quoted-printable"}},
		body:   quotedPrintable(d.Text + "\r\n"),
	}
	b := boundary()
	out := multipartBody{parts: [][]byte{entity{header: inner, body: body}.bytes(), note.bytes()}, epilogue: []byte("\r\n")}.join(b)
	h = setField(h, "Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": b}))
	return setField(h, "Content-Transfer-Encoding", ""), out
}
