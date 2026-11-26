package mail

import (
	"bytes"
	"encoding/base64"
	"io"
	"strings"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

func lenient(err error) bool {
	return err == nil || message.IsUnknownCharset(err) || message.IsUnknownEncoding(err)
}

func headerAddresses(h mail.Header, key string) []Address {
	out := []Address{}
	list, err := h.AddressList(key)
	if err != nil {
		if v, _ := h.Text(key); strings.TrimSpace(v) != "" {
			out = append(out, Address{Address: strings.TrimSpace(v)})
		}
		return out
	}
	for _, a := range list {
		out = append(out, Address{Name: a.Name, Address: strings.ToLower(a.Address)})
	}
	return out
}

func ParseRaw(raw []byte) (*Message, error) {
	e, err := message.Read(bytes.NewReader(raw))
	if !lenient(err) {
		return nil, err
	}
	h := mail.Header{Header: e.Header}
	out := &Message{
		Summary: Summary{Size: int64(len(raw)), From: headerAddresses(h, "From"), To: headerAddresses(h, "To")},
		Cc:      headerAddresses(h, "Cc"), Bcc: []Address{}, ReplyTo: headerAddresses(h, "Reply-To"), References: []string{}, Attachments: []Attachment{},
	}
	if out.Subject, err = h.Subject(); err != nil {
		out.Subject = h.Get("Subject")
	}
	out.Date, _ = h.Date()
	if id, err := h.MessageID(); err == nil && id != "" {
		out.MessageID = angleID(id)
	}
	type inlineImage struct {
		cid, mediaType string
		body           []byte
		attachment     Attachment
	}
	var inline []inlineImage
	walkErr := e.Walk(func(path []int, part *message.Entity, err error) error {
		if !lenient(err) {
			return err
		}
		mediaType, _, _ := part.Header.ContentType()
		if strings.HasPrefix(mediaType, "multipart/") {
			return nil
		}
		if mediaType == "" {
			mediaType = "text/plain"
		}
		disposition, _, _ := part.Header.ContentDisposition()
		ah := mail.AttachmentHeader{Header: part.Header}
		filename, _ := ah.Filename()
		attached := strings.EqualFold(disposition, "attachment") || (filename != "" && !strings.HasPrefix(mediaType, "text/"))
		body, err := io.ReadAll(io.LimitReader(part.Body, maxInlineBody+1))
		if err != nil {
			return err
		}
		size, _ := io.Copy(io.Discard, part.Body)
		size += int64(len(body))
		number := []int{1}
		if len(path) > 0 {
			number = make([]int, len(path))
			for i, p := range path {
				number[i] = p + 1
			}
		}
		a := Attachment{Part: partString(number), Filename: firstNonEmpty(filename, "attachment-"+partString(number)), ContentType: mediaType, Size: size}
		cid := strings.Trim(part.Header.Get("Content-Id"), "<> ")
		switch {
		case !attached && mediaType == "text/plain" && out.Text == "" && size <= maxInlineBody:
			out.Text = strings.ToValidUTF8(string(body), "�")
		case !attached && mediaType == "text/html" && out.HTML == "" && size <= maxInlineBody:
			out.HTML = strings.ToValidUTF8(string(body), "�")
		case !strings.EqualFold(disposition, "attachment") && cid != "" && strings.HasPrefix(mediaType, "image/") && size <= maxInlineImage:
			inline = append(inline, inlineImage{cid: cid, mediaType: mediaType, body: body, attachment: a})
		default:
			out.Attachments = append(out.Attachments, a)
		}
		return nil
	})
	if walkErr != nil && out.Text == "" && out.HTML == "" {
		return nil, walkErr
	}
	budget := maxInlinedImageBytes
	for _, img := range inline {
		if !inlineCID(&out.HTML, img.cid, img.mediaType, img.body, &budget) {
			out.Attachments = append(out.Attachments, img.attachment)
		}
	}
	if out.HTML != "" {
		out.HTML = SanitizePreviewHTML(out.HTML)
	}
	out.HasAttachments = len(out.Attachments) > 0
	return out, nil
}

const maxInlinedImageBytes = 10 << 20

func inlineCID(html *string, cid, mediaType string, body []byte, budget *int) bool {
	ref := "cid:" + cid
	n := strings.Count(*html, ref)
	uri := "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(body)
	if n == 0 || n*len(uri) > *budget {
		return false
	}
	*budget -= n * len(uri)
	*html = strings.ReplaceAll(*html, ref, uri)
	return true
}
