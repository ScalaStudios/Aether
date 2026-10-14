package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"
)

type Outgoing struct {
	From       Address
	To         []Address
	Cc         []Address
	Bcc        []Address
	Subject    string
	Text       string
	HTML       string
	InReplyTo  string
	References []string
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
	h.SetAddressList("To", mailAddresses(o.To))
	if len(o.Cc) > 0 {
		h.SetAddressList("Cc", mailAddresses(o.Cc))
	}
	h.SetSubject(o.Subject)
	h.Set("Message-ID", messageID)
	if o.InReplyTo != "" {
		h.Set("In-Reply-To", o.InReplyTo)
		h.Set("References", strings.Join(append(o.References, o.InReplyTo), " "))
	}
	h.Set("User-Agent", "Aether")
	var buf bytes.Buffer
	if o.HTML == "" {
		h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		w, err := mail.CreateSingleInlineWriter(&buf, h)
		if err != nil {
			return nil, "", err
		}
		w.Write([]byte(o.Text))
		return buf.Bytes(), messageID, w.Close()
	}
	w, err := mail.CreateInlineWriter(&buf, h)
	if err != nil {
		return nil, "", err
	}
	for _, part := range []struct{ typ, body string }{{"text/plain", o.Text}, {"text/html", o.HTML}} {
		var ph mail.InlineHeader
		ph.SetContentType(part.typ, map[string]string{"charset": "utf-8"})
		pw, err := w.CreatePart(ph)
		if err != nil {
			return nil, "", err
		}
		pw.Write([]byte(part.body))
		pw.Close()
	}
	return buf.Bytes(), messageID, w.Close()
}
