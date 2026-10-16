package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

type OutgoingAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

type Outgoing struct {
	From        Address
	To          []Address
	Cc          []Address
	Bcc         []Address
	Subject     string
	Text        string
	HTML        string
	InReplyTo   string
	References  []string
	Attachments []OutgoingAttachment
	IncludeBcc  bool
	Date        time.Time
	Calendar    *CalendarPart
}

type CalendarPart struct {
	Method string
	Data   string
}

func mailAddresses(in []Address) []*mail.Address {
	out := make([]*mail.Address, len(in))
	for i, a := range in {
		out[i] = &mail.Address{Name: a.Name, Address: a.Address}
	}
	return out
}

func (o *Outgoing) Recipients() []string {
	var out []string
	for _, list := range [][]Address{o.To, o.Cc, o.Bcc} {
		for _, a := range list {
			out = append(out, a.Address)
		}
	}
	return out
}

func (o *Outgoing) Build(hostname string) (raw []byte, messageID string, err error) {
	id := make([]byte, 16)
	rand.Read(id)
	messageID = "<" + hex.EncodeToString(id) + "@" + hostname + ">"
	var h mail.Header
	date := o.Date
	if date.IsZero() {
		date = time.Now()
	}
	h.SetDate(date)
	h.SetAddressList("From", mailAddresses([]Address{o.From}))
	if len(o.To) > 0 {
		h.SetAddressList("To", mailAddresses(o.To))
	}
	if len(o.Cc) > 0 {
		h.SetAddressList("Cc", mailAddresses(o.Cc))
	}
	if o.IncludeBcc && len(o.Bcc) > 0 {
		h.SetAddressList("Bcc", mailAddresses(o.Bcc))
	}
	h.SetSubject(o.Subject)
	h.Set("Message-ID", messageID)
	if o.InReplyTo != "" {
		h.Set("In-Reply-To", o.InReplyTo)
		refs := o.References
		if !slices.Contains(refs, o.InReplyTo) {
			refs = append(refs, o.InReplyTo)
		}
		h.Set("References", strings.Join(refs, " "))
	}
	h.Set("User-Agent", "Aether")
	var buf bytes.Buffer
	if len(o.Attachments) == 0 && o.HTML == "" && o.Calendar == nil {
		h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		w, err := mail.CreateSingleInlineWriter(&buf, h)
		if err != nil {
			return nil, "", err
		}
		w.Write([]byte(o.Text))
		if err := w.Close(); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), messageID, nil
	}
	html, images := inlineImages(o.HTML)
	top := "multipart/alternative"
	if len(images) > 0 {
		top = "multipart/related"
	}
	if len(o.Attachments) > 0 {
		top = "multipart/mixed"
	}
	h.SetContentType(top, nil)
	w, err := message.CreateWriter(&buf, h.Header)
	if err != nil {
		return nil, "", err
	}
	body := w
	if len(images) > 0 && len(o.Attachments) > 0 {
		if body, err = multipart(w, "multipart/related"); err != nil {
			return nil, "", err
		}
	}
	alt := body
	if top != "multipart/alternative" {
		if alt, err = multipart(body, "multipart/alternative"); err != nil {
			return nil, "", err
		}
	}
	if err := o.writeBody(alt, html); err != nil {
		return nil, "", err
	}
	if alt != body {
		if err := alt.Close(); err != nil {
			return nil, "", err
		}
	}
	for _, img := range images {
		if err := writePart(body, img.ContentType, "inline", img.Filename, img.ID, img.Data); err != nil {
			return nil, "", err
		}
	}
	if body != w {
		if err := body.Close(); err != nil {
			return nil, "", err
		}
	}
	for _, a := range o.Attachments {
		if err := writePart(w, a.ContentType, "attachment", a.Filename, "", a.Data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), messageID, nil
}

func (o *Outgoing) writeBody(w *message.Writer, html string) error {
	type bodyPart struct {
		typ, body string
		params    map[string]string
	}
	parts := []bodyPart{{"text/plain", o.Text, nil}}
	if html != "" {
		parts = append(parts, bodyPart{"text/html", html, nil})
	}
	if o.Calendar != nil {
		parts = append(parts, bodyPart{"text/calendar", o.Calendar.Data, map[string]string{"method": o.Calendar.Method}})
	}
	for _, part := range parts {
		params := map[string]string{"charset": "utf-8"}
		maps.Copy(params, part.params)
		var ph message.Header
		ph.SetContentType(part.typ, params)
		ph.Set("Content-Transfer-Encoding", "quoted-printable")
		pw, err := w.CreatePart(ph)
		if err != nil {
			return err
		}
		pw.Write([]byte(part.body))
		if err := pw.Close(); err != nil {
			return err
		}
	}
	return nil
}

func multipart(parent *message.Writer, typ string) (*message.Writer, error) {
	var h message.Header
	h.SetContentType(typ, nil)
	return parent.CreatePart(h)
}

func writePart(parent *message.Writer, contentType, disposition, filename, contentID string, data []byte) error {
	var h message.Header
	h.SetContentType(contentType, nil)
	h.SetContentDisposition(disposition, map[string]string{"filename": filename})
	h.Set("Content-Transfer-Encoding", "base64")
	if contentID != "" {
		h.Set("Content-ID", "<"+contentID+">")
	}
	pw, err := parent.CreatePart(h)
	if err != nil {
		return err
	}
	pw.Write(data)
	return pw.Close()
}

type inlineImage struct {
	ID, ContentType, Filename string
	Data                      []byte
}

var dataImage = regexp.MustCompile(`src="data:(image/(?:png|jpeg|gif|webp));base64,([A-Za-z0-9+/=]+)"`)

func inlineImages(html string) (string, []inlineImage) {
	var images []inlineImage
	html = dataImage.ReplaceAllStringFunc(html, func(m string) string {
		sub := dataImage.FindStringSubmatch(m)
		data, err := base64.StdEncoding.DecodeString(sub[2])
		if err != nil {
			return m
		}
		id := make([]byte, 8)
		rand.Read(id)
		img := inlineImage{ID: hex.EncodeToString(id) + "@aether", ContentType: sub[1], Filename: fmt.Sprintf("image%d.%s", len(images)+1, strings.TrimPrefix(sub[1], "image/")), Data: data}
		images = append(images, img)
		return `src="cid:` + img.ID + `"`
	})
	return html, images
}
