package mail

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/emersion/go-message/mail"
)

func TestSanitizeHTMLRemovesActiveAndRemoteContent(t *testing.T) {
	in := `<p style="color:red;position:fixed">Hi</p><script>alert(1)</script><img src="https://tracker.example/p.gif"><a href="javascript:alert(1)">x</a><a href="https://example.com">ok</a><iframe src="https://evil"></iframe><div onclick="steal()">d</div><img src="data:image/png;base64,iVBORw0KGgo=">`
	out := SanitizeHTML(in)
	for _, banned := range []string{"<script", "tracker.example", "javascript:", "<iframe", "onclick", "position"} {
		if strings.Contains(out, banned) {
			t.Errorf("sanitized output still contains %q: %s", banned, out)
		}
	}
	for _, kept := range []string{"color: red", `href="https://example.com"`, `rel="nofollow noreferrer noopener"`, "data:image/png"} {
		if !strings.Contains(out, kept) {
			t.Errorf("sanitized output lost %q: %s", kept, out)
		}
	}
}

func TestBuildWithAttachmentRoundTrips(t *testing.T) {
	o := Outgoing{
		From: Address{Address: "a@example.test"}, To: []Address{{Address: "b@example.test"}}, Subject: "files",
		Text: "see attached", HTML: "<p>see attached</p>",
		Attachments: []OutgoingAttachment{{Filename: "report.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.7 bytes")}},
	}
	raw, _, err := o.Build("mail.example.test")
	if err != nil {
		t.Fatal(err)
	}
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for {
		p, err := r.NextPart()
		if err != nil {
			break
		}
		if h, ok := p.Header.(*mail.AttachmentHeader); ok {
			name, _ := h.Filename()
			body, _ := io.ReadAll(p.Body)
			found = name == "report.pdf" && string(body) == "%PDF-1.7 bytes"
		}
	}
	if !found {
		t.Fatalf("attachment missing or corrupted:\n%s", raw)
	}
}
