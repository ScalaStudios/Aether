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

func TestBuildKeepsBodiesAndReferences(t *testing.T) {
	for _, o := range []Outgoing{
		{Text: "plain only body", InReplyTo: "<a@x>", References: []string{"<a@x>"}},
		{Text: "plain only body", HTML: "<p>html body</p>"},
		{Text: "plain only body", Attachments: []OutgoingAttachment{{Filename: "f.txt", ContentType: "text/plain", Data: []byte("x")}}},
	} {
		o.From, o.To, o.Subject = Address{Address: "a@example.test"}, []Address{{Address: "b@example.test"}}, "s"
		raw, _, err := o.Build("mail.example.test")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte("plain only body")) {
			t.Fatalf("body missing from built message:\n%s", raw)
		}
		if o.HTML != "" && !bytes.Contains(raw, []byte("html body")) {
			t.Fatalf("html part missing:\n%s", raw)
		}
		if bytes.Count(raw, []byte("<a@x>")) > 2 {
			t.Fatalf("duplicate reference:\n%s", raw)
		}
	}
}

func TestCompileSieveEscapesAndSkipsDisabled(t *testing.T) {
	script := CompileSieve([]Rule{
		{Name: "invoices", Enabled: true, Match: "any", Conditions: []RuleCondition{{Field: "subject", Contains: `say "hi" \ now`}, {Field: "from", Contains: "billing@"}},
			Actions: RuleActions{MoveTo: "Archive", MarkRead: true, Stop: true}},
		{Name: "off", Enabled: false, Match: "all", Conditions: []RuleCondition{{Field: "to", Contains: "x"}}, Actions: RuleActions{Flag: true}},
		{Name: "fwd", Enabled: true, Match: "all", Conditions: []RuleCondition{{Field: "from", Contains: "boss"}}, Actions: RuleActions{ForwardTo: []string{"me@else.example"}, KeepCopy: true}},
	}, AutoReply{Enabled: true, Subject: "Away", Message: "Back on \"Monday\"", Start: "2026-10-14", End: "2026-10-20"}, "me@example.test")
	for _, want := range []string{`redirect :copy "me@else.example";`, `currentdate :value "ge" "date" "2026-10-14"`, `vacation :days 1 :subject "Away" :from "me@example.test" "Back on \"Monday\"";`, `anyof (header :contains "Subject" "say \"hi\" \\ now", header :contains "From" "billing@")`, `addflag "\\Seen";`, `fileinto :create "Archive";`, "stop;"} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %s:\n%s", want, script)
		}
	}
	if strings.Contains(script, `"To"`) {
		t.Errorf("disabled rule compiled:\n%s", script)
	}
	if (Rule{Name: "x", Match: "all", Conditions: []RuleCondition{{Field: "subject", Contains: "a\r\nb"}}, Actions: RuleActions{Flag: true}}).Validate() == nil {
		t.Error("multi-line condition accepted")
	}
	if (Rule{Name: "x", Match: "all", Conditions: []RuleCondition{{Field: "subject", Contains: "a"}}, Actions: RuleActions{ForwardTo: []string{"Evil <x@y.z>"}}}).Validate() == nil {
		t.Error("display-name forwarding address accepted")
	}
	if (AutoReply{Enabled: true, Message: "x", Start: "2026-10-20", End: "2026-10-01"}).Validate() == nil {
		t.Error("inverted date range accepted")
	}
}
