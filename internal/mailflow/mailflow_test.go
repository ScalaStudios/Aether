package mailflow

import (
	"bytes"
	"strings"
	"testing"
)

var dir = &Directory{
	Domains: map[string]string{"acme.test": "acme", "beta.test": "beta"},
	Lists:   map[string][]string{"sales@acme.test": {"ann@acme.test"}},
}

func newMessage(sender string, auth bool, rcpts []string, header []Field, body string) *Message {
	return &Message{Sender: sender, Authenticated: auth, Recipients: rcpts, Header: header, Body: []byte(body), Size: len(body)}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		rule Rule
		ok   bool
	}{
		{Rule{Name: "copy", Mode: "enforce", Actions: Actions{Bcc: []string{"Audit@Acme.test"}}}, true},
		{Rule{Name: "none", Mode: "enforce"}, false},
		{Rule{Name: "x", Mode: "maybe", Actions: Actions{RequireTLS: true}}, false},
		{Rule{Name: "x", Mode: "enforce", Actions: Actions{Reject: "No", Bcc: []string{"a@acme.test"}}}, false},
		{Rule{Name: "x", Mode: "enforce", Actions: Actions{AddHeaders: []HeaderValue{{Name: "From", Value: "x"}}}}, false},
		{Rule{Name: "x", Mode: "enforce", Actions: Actions{RemoveHeaders: []string{"Content-Type"}}}, false},
		{Rule{Name: "x", Mode: "enforce", Conditions: []Condition{{Field: "spamScoreOver", Number: 5}}, Actions: Actions{Disclaimer: &Disclaimer{Text: "x", Fallback: "skip"}}}, false},
		{Rule{Name: "x", Mode: "enforce", Conditions: []Condition{{Field: "sender", Scope: "domain", Values: []string{"@Example.COM"}}}, Actions: Actions{RequireTLS: true}}, true},
		{Rule{Name: "x", Mode: "enforce", Conditions: []Condition{{Field: "sender", Scope: "address", Values: []string{"nope"}}}, Actions: Actions{RequireTLS: true}}, false},
		{Rule{Name: "x", Mode: "enforce", Conditions: []Condition{{Field: "subject"}}, Actions: Actions{RequireTLS: true}}, false},
		{Rule{Name: "x", Mode: "enforce", Conditions: []Condition{{Field: "direction", Values: []string{"sideways"}}}, Actions: Actions{RequireTLS: true}}, false},
	}
	for i, c := range cases {
		if err := c.rule.Validate(); (err == nil) != c.ok {
			t.Errorf("case %d: got %v", i, err)
		}
	}
	r := Rule{Name: "x", Mode: "enforce", Conditions: []Condition{{Field: "sender", Scope: "domain", Values: []string{"@Example.COM"}}}, Actions: Actions{Bcc: []string{"Audit@Acme.test"}}}
	r.Validate()
	if r.Conditions[0].Values[0] != "example.com" || r.Actions.Bcc[0] != "audit@acme.test" {
		t.Fatalf("not normalized: %+v", r)
	}
}

func TestDirectionAndConditions(t *testing.T) {
	out := newMessage("bob@acme.test", true, []string{"x@else.test", "ann@acme.test"}, nil, "")
	if got := out.Direction("acme", dir); got != "outgoing" {
		t.Fatalf("direction %s", got)
	}
	if got := out.Direction("beta", dir); got != "incoming" {
		t.Fatalf("direction %s", got)
	}
	spoofed := newMessage("bob@acme.test", false, []string{"ann@acme.test"}, nil, "")
	if got := spoofed.Direction("acme", dir); got != "incoming" {
		t.Fatalf("unauthenticated hosted sender must count as incoming, got %s", got)
	}
	internal := newMessage("bob@acme.test", true, []string{"ann@acme.test"}, nil, "")
	if got := internal.Direction("acme", dir); got != "internal" {
		t.Fatalf("direction %s", got)
	}

	rules := map[string][]Rule{"acme": {
		{Name: "list", Mode: "enforce", Conditions: []Condition{{Field: "recipient", Scope: "memberOf", Values: []string{"sales@acme.test"}}}, Actions: Actions{Bcc: []string{"audit@acme.test"}}},
		{Name: "test", Mode: "test", Conditions: []Condition{{Field: "subject", Values: []string{"secret"}}}, Actions: Actions{Reject: "no"}},
		{Name: "stop", Mode: "enforce", Stop: true, Actions: Actions{AddHeaders: []HeaderValue{{Name: "X-Tag", Value: "1"}}}},
		{Name: "after stop", Mode: "enforce", Actions: Actions{RequireTLS: true}},
	}}
	m := newMessage("x@else.test", false, []string{"ann@acme.test"}, []Field{{Name: "Subject", Value: "=?utf-8?q?Top_SECRET?="}}, "")
	o := Evaluate(m, rules, dir, false, nil)
	if len(o.Matches) != 3 || !o.Matches[1].Test || o.Reject != "" {
		t.Fatalf("matches %+v reject %q", o.Matches, o.Reject)
	}
	if o.RequireTLS || len(o.AddHeaders) != 1 || len(o.Stopped) != 1 {
		t.Fatalf("stop not honoured: %+v", o)
	}
	if got := o.Recipients(m.Recipients); strings.Join(got, ",") != "ann@acme.test,audit@acme.test" {
		t.Fatalf("recipients %v", got)
	}
	if again := Evaluate(m, rules, dir, false, []string{"acme"}); len(again.Matches) != 0 {
		t.Fatalf("stopped org still evaluated")
	}
}

func TestRedirectOnlyTouchesOwnRecipientsForIncoming(t *testing.T) {
	rules := map[string][]Rule{"beta": {{Name: "r", Mode: "enforce", Actions: Actions{RedirectTo: []string{"desk@beta.test"}}}}}
	m := newMessage("x@else.test", false, []string{"ann@acme.test", "joe@beta.test"}, nil, "")
	o := Evaluate(m, rules, dir, false, nil)
	if got := o.Recipients(m.Recipients); strings.Join(got, ",") != "ann@acme.test,desk@beta.test" {
		t.Fatalf("recipients %v", got)
	}
}

func TestAttachmentsAndBody(t *testing.T) {
	body := "--b1\r\nContent-Type: text/plain\r\n\r\nPlease find the invoice\r\n--b1\r\nContent-Type: application/zip\r\nContent-Disposition: attachment; filename=\"files.ZIP\"\r\n\r\nUEsDBA==\r\n--b1--\r\n"
	m := newMessage("x@else.test", false, []string{"ann@acme.test"}, []Field{{Name: "Content-Type", Value: "multipart/mixed; boundary=b1"}}, body)
	for _, c := range []Condition{
		{Field: "attachmentName", Values: []string{".zip"}},
		{Field: "attachmentType", Values: []string{"application/zip"}},
		{Field: "body", Values: []string{"INVOICE"}},
		{Field: "sizeOver", Number: 0.1},
	} {
		if !c.matches(m, "acme", "incoming", dir) {
			t.Errorf("%s did not match", c.Field)
		}
	}
	if (&Condition{Field: "body", Values: []string{"UEsDBA"}}).matches(m, "acme", "incoming", dir) {
		t.Error("attachment content counted as body text")
	}
}

func TestDisclaimerPlainText(t *testing.T) {
	h := []Field{{Name: "Subject", Value: "Hi"}, {Name: "Content-Type", Value: "text/plain; charset=iso-8859-1"}, {Name: "Content-Transfer-Encoding", Value: "8bit"}}
	nh, nb, ok := AppendDisclaimer(h, []byte("Gr\xfc\xdfe\n"), Disclaimer{Text: "Confidential – do not forward", Fallback: "wrap"})
	if !ok || get(nh, "Content-Transfer-Encoding") != "quoted-printable" || !strings.Contains(get(nh, "Content-Type"), "utf-8") {
		t.Fatalf("headers %+v", nh)
	}
	text, _ := decodedText(nh, nb, 1<<20)
	if !strings.HasPrefix(text, "Grüße\r\n\r\nConfidential – do not forward") {
		t.Fatalf("text %q", text)
	}
	if get(nh, "MIME-Version") != "1.0" || get(nh, "Subject") != "Hi" {
		t.Fatalf("headers %+v", nh)
	}
}

func TestDisclaimerAlternative(t *testing.T) {
	body := "preamble\r\n--a\r\nContent-Type: text/plain\r\n\r\nhello\r\n--a\r\nContent-Type: text/html\r\n\r\n<html><body><p>hello</p></BODY></html>\r\n--a\r\nContent-Type: text/calendar\r\n\r\nBEGIN:VCALENDAR\r\n--a--\r\n"
	h := []Field{{Name: "Content-Type", Value: "multipart/alternative;\r\n boundary=\"a\""}, {Name: "MIME-Version", Value: "1.0"}}
	nh, nb, ok := AppendDisclaimer(h, []byte(body), Disclaimer{Text: "Legal", HTML: "<b>Legal</b>", Fallback: "skip"})
	if !ok || get(nh, "Content-Type") != get(h, "Content-Type") {
		t.Fatalf("headers %+v", nh)
	}
	mp, ok := splitMultipart(nb, "a")
	if !ok || len(mp.parts) != 3 || string(mp.preamble) != "preamble" {
		t.Fatalf("parts %q", nb)
	}
	plain, html, cal := parseEntity(mp.parts[0]), parseEntity(mp.parts[1]), parseEntity(mp.parts[2])
	if txt, _ := decodedText(plain.header, plain.body, 1<<20); txt != "hello\r\n\r\nLegal\r\n" {
		t.Fatalf("plain %q", txt)
	}
	if doc, _ := decodedText(html.header, html.body, 1<<20); !strings.Contains(doc, `<p>hello</p><div class="aether-disclaimer"><b>Legal</b></div></BODY>`) {
		t.Fatalf("html %q", doc)
	}
	if string(cal.body) != "BEGIN:VCALENDAR" {
		t.Fatalf("calendar part changed: %q", cal.body)
	}
}

func TestDisclaimerSignedFallbacks(t *testing.T) {
	h := []Field{{Name: "Content-Type", Value: `multipart/signed; protocol="application/pkcs7-signature"; boundary=s`}}
	body := []byte("--s\r\nContent-Type: text/plain\r\n\r\nsigned text\r\n--s\r\nContent-Type: application/pkcs7-signature\r\n\r\nSIG\r\n--s--\r\n")
	if nh, nb, ok := AppendDisclaimer(h, body, Disclaimer{Text: "L", Fallback: "skip"}); !ok || !bytes.Equal(nb, body) || len(nh) != 1 {
		t.Fatal("skip changed the message")
	}
	if _, _, ok := AppendDisclaimer(h, body, Disclaimer{Text: "L", Fallback: "reject"}); ok {
		t.Fatal("reject fallback accepted")
	}
	nh, nb, ok := AppendDisclaimer(h, body, Disclaimer{Text: "L", Fallback: "wrap"})
	t1, params := mediaType(nh)
	if !ok || t1 != "multipart/mixed" {
		t.Fatalf("headers %+v", nh)
	}
	mp, ok := splitMultipart(nb, params["boundary"])
	if !ok || len(mp.parts) != 2 {
		t.Fatalf("body %q", nb)
	}
	inner := parseEntity(mp.parts[0])
	if !bytes.Equal(inner.body, body) || !strings.HasPrefix(get(inner.header, "Content-Type"), "multipart/signed") {
		t.Fatalf("signed part not preserved: %q", mp.parts[0])
	}
}

func TestSplitMultipartRoundTrip(t *testing.T) {
	body := []byte("--x\r\nContent-Type: text/plain\r\n\r\none\r\n--x\r\n\r\ntwo\r\n--x--\r\nepilogue\r\n")
	mp, ok := splitMultipart(body, "x")
	if !ok || len(mp.parts) != 2 {
		t.Fatalf("parts %+v", mp)
	}
	if got := mp.join("x"); !bytes.Equal(got, body) {
		t.Fatalf("round trip %q", got)
	}
	if e := parseEntity(mp.parts[1]); len(e.header) != 0 || string(e.body) != "two" {
		t.Fatalf("headerless part %+v", e)
	}
}
