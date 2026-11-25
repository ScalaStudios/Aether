package mailflow

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"mime"
	"mime/quotedprintable"
	"strings"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/textproto"
)

type Field struct {
	Name  string
	Value string
}

type entity struct {
	header []Field
	body   []byte
}

var wordDecoder = &mime.WordDecoder{CharsetReader: charset.Reader}

func unfold(v string) string {
	return strings.NewReplacer("\r\n", "", "\n", "").Replace(v)
}

func DecodeHeader(v string) string {
	v = unfold(v)
	if out, err := wordDecoder.DecodeHeader(v); err == nil {
		return out
	}
	return v
}

func get(h []Field, name string) string {
	for _, f := range h {
		if strings.EqualFold(f.Name, name) {
			return unfold(f.Value)
		}
	}
	return ""
}

func mediaType(h []Field) (string, map[string]string) {
	t, params, err := mime.ParseMediaType(get(h, "Content-Type"))
	if err != nil || t == "" {
		return "text/plain", map[string]string{}
	}
	return t, params
}

func crlf(b []byte) []byte {
	return bytes.ReplaceAll(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
}

func parseEntity(raw []byte) entity {
	if body, ok := bytes.CutPrefix(raw, []byte("\r\n")); ok {
		return entity{body: body}
	}
	head, body, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
	return entity{header: parseFields(head), body: body}
}

func parseFields(head []byte) []Field {
	var out []Field
	for _, line := range strings.Split(string(head), "\r\n") {
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(out) > 0 {
			out[len(out)-1].Value += "\r\n" + line
			continue
		}
		if name, value, ok := strings.Cut(line, ":"); ok {
			out = append(out, Field{Name: name, Value: strings.TrimLeft(value, " \t")})
		}
	}
	return out
}

func (e entity) bytes() []byte {
	var b bytes.Buffer
	for _, f := range e.header {
		b.WriteString(f.Name + ": " + f.Value + "\r\n")
	}
	b.WriteString("\r\n")
	b.Write(e.body)
	return b.Bytes()
}

type multipartBody struct {
	preamble []byte
	parts    [][]byte
	epilogue []byte
}

func splitMultipart(body []byte, boundary string) (multipartBody, bool) {
	var out multipartBody
	if boundary == "" {
		return out, false
	}
	delim := []byte("--" + boundary)
	start, open := -1, false
	pos := 0
	for pos <= len(body) {
		end := bytes.Index(body[pos:], []byte("\r\n"))
		lineEnd := len(body)
		if end >= 0 {
			lineEnd = pos + end
		}
		line := bytes.TrimRight(body[pos:lineEnd], " \t")
		if bytes.HasPrefix(line, delim) {
			rest := line[len(delim):]
			closing := bytes.Equal(rest, []byte("--"))
			if closing || len(rest) == 0 {
				cut := max(pos-2, 0)
				if !open {
					out.preamble = body[:cut]
				} else {
					out.parts = append(out.parts, body[start:cut])
				}
				if closing {
					out.epilogue = body[lineEnd:]
					return out, len(out.parts) > 0
				}
				open, start = true, min(lineEnd+2, len(body))
			}
		}
		if end < 0 {
			break
		}
		pos = lineEnd + 2
	}
	return out, false
}

func (m multipartBody) join(boundary string) []byte {
	var b bytes.Buffer
	b.Write(m.preamble)
	for i, p := range m.parts {
		if i > 0 || len(m.preamble) > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString("--" + boundary + "\r\n")
		b.Write(p)
	}
	b.WriteString("\r\n--" + boundary + "--")
	b.Write(m.epilogue)
	return b.Bytes()
}

func decodedText(h []Field, body []byte, limit int64) (string, bool) {
	var th textproto.Header
	for _, name := range []string{"Content-Type", "Content-Transfer-Encoding"} {
		if v := get(h, name); v != "" {
			th.Add(name, v)
		}
	}
	e, err := message.New(message.Header{Header: th}, bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	out, err := io.ReadAll(io.LimitReader(e.Body, limit))
	return string(out), err == nil
}

type part struct {
	contentType string
	filename    string
	attachment  bool
	text        string
}

func walk(h []Field, body []byte, depth int, visit func(part)) {
	t, params := mediaType(h)
	if strings.HasPrefix(t, "multipart/") {
		mp, ok := splitMultipart(body, params["boundary"])
		if !ok || depth > 20 {
			return
		}
		for _, raw := range mp.parts {
			child := parseEntity(raw)
			walk(child.header, child.body, depth+1, visit)
		}
		return
	}
	p := part{contentType: t}
	disposition, dparams, _ := mime.ParseMediaType(get(h, "Content-Disposition"))
	p.filename = DecodeHeader(dparams["filename"])
	if p.filename == "" {
		p.filename = DecodeHeader(params["name"])
	}
	p.attachment = disposition == "attachment" || p.filename != ""
	if !p.attachment && (t == "text/plain" || t == "text/html") {
		p.text, _ = decodedText(h, body, 1<<20)
	}
	visit(p)
}

func boundary() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "aether-" + hex.EncodeToString(b)
}

func quotedPrintable(s string) []byte {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

func setField(h []Field, name, value string) []Field {
	out := make([]Field, 0, len(h)+1)
	done := false
	for _, f := range h {
		if strings.EqualFold(f.Name, name) {
			if !done && value != "" {
				out = append(out, Field{Name: name, Value: value})
			}
			done = true
			continue
		}
		out = append(out, f)
	}
	if !done && value != "" {
		out = append(out, Field{Name: name, Value: value})
	}
	return out
}
