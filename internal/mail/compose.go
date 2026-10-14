package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"time"

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
	h.SetDate(time.Now())
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
	if len(o.Attachments) == 0 && o.HTML == "" {
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
	if len(o.Attachments) == 0 {
		w, err := mail.CreateInlineWriter(&buf, h)
		if err != nil {
			return nil, "", err
		}
		if err := o.writeBody(w); err != nil {
			return nil, "", err
		}
		if err := w.Close(); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), messageID, nil
	}
	w, err := mail.CreateWriter(&buf, h)
	if err != nil {
		return nil, "", err
	}
	iw, err := w.CreateInline()
	if err != nil {
		return nil, "", err
	}
	if err := o.writeBody(iw); err != nil {
		return nil, "", err
	}
	if err := iw.Close(); err != nil {
		return nil, "", err
	}
	for _, a := range o.Attachments {
		var ah mail.AttachmentHeader
		ah.SetContentType(a.ContentType, nil)
		ah.SetFilename(a.Filename)
		aw, err := w.CreateAttachment(ah)
		if err != nil {
			return nil, "", err
		}
		aw.Write(a.Data)
		if err := aw.Close(); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), messageID, nil
}

func (o *Outgoing) writeBody(w *mail.InlineWriter) error {
	parts := []struct{ typ, body string }{{"text/plain", o.Text}}
	if o.HTML != "" {
		parts = append(parts, struct{ typ, body string }{"text/html", o.HTML})
	}
	for _, part := range parts {
		var ph mail.InlineHeader
		ph.SetContentType(part.typ, map[string]string{"charset": "utf-8"})
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
