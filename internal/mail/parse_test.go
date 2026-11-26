package mail

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseRawPreviewHasNoLinksOrRemoteContent(t *testing.T) {
	raw := "From: =?utf-8?q?Payroll_Team?= <Payroll@Example.test>\r\nTo: bob@example.test\r\nSubject: =?utf-8?b?WW91ciBwYXlzbGlw?=\r\nMessage-ID: <abc@example.test>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: multipart/related; boundary=rel\r\n\r\n" +
		"--rel\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		`<p>Sign in <a href="https://evil.example/login">here</a> or <a href="mailto:x@evil.example">mail</a></p><img src="https://tracker.example/p.gif"><img src="cid:logo@x">` + "\r\n" +
		"--rel\r\nContent-Type: image/png\r\nContent-ID: <logo@x>\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo=\r\n" +
		"--rel--\r\n" +
		"--outer\r\nContent-Type: application/octet-stream\r\n\r\nMZ-no-name\r\n" +
		"--outer\r\nContent-Type: application/zip\r\nContent-Disposition: attachment; filename=\"invoice.zip\"\r\n\r\nPK\r\n" +
		"--outer--\r\n"
	m, err := ParseRaw([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Your payslip" || len(m.From) != 1 || m.From[0].Address != "payroll@example.test" || m.From[0].Name != "Payroll Team" || m.MessageID != "<abc@example.test>" {
		t.Fatalf("headers not decoded: %+v", m.Summary)
	}
	for _, banned := range []string{"evil.example", "tracker.example", "href", "cid:"} {
		if strings.Contains(m.HTML, banned) {
			t.Errorf("preview still contains %q: %s", banned, m.HTML)
		}
	}
	for _, kept := range []string{"here", "data:image/png;base64,iVBORw0KGgo="} {
		if !strings.Contains(m.HTML, kept) {
			t.Errorf("preview lost %q: %s", kept, m.HTML)
		}
	}
	var names []string
	for _, a := range m.Attachments {
		names = append(names, a.Filename+" "+a.ContentType)
	}
	if got := strings.Join(names, ", "); got != "attachment-2 application/octet-stream, invoice.zip application/zip" || !m.HasAttachments {
		t.Errorf("attachments = %q", got)
	}
}

func TestParseRawKeepsUnparseableFromReadable(t *testing.T) {
	m, err := ParseRaw([]byte("From: not an address\r\nSubject: hi\r\n\r\nplain body\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.From) != 1 || m.From[0].Address != "not an address" || m.Text != "plain body\r\n" {
		t.Fatalf("got from %+v text %q", m.From, m.Text)
	}
}

func TestParseRawBoundsRepeatedInlineImages(t *testing.T) {
	img := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, 8<<10))
	raw := "From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/html\r\n\r\n<p>" + strings.Repeat(`<img src="cid:a">`, 2000) + "</p>\r\n" +
		"--b\r\nContent-Type: image/png\r\nContent-Id: <a>\r\nContent-Transfer-Encoding: base64\r\n\r\n" + img + "\r\n--b--\r\n"
	m, err := ParseRaw([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.HTML) > 10<<20+len(raw) {
		t.Fatalf("html grew to %d bytes", len(m.HTML))
	}
	if len(m.Attachments) != 1 || m.Attachments[0].ContentType != "image/png" {
		t.Fatalf("image not listed as attachment: %+v", m.Attachments)
	}
}
