package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func TestSafeReturnToRejectsOffsiteTargets(t *testing.T) {
	cases := map[string]string{
		"/mail?folder=Sent":    "/mail?folder=Sent",
		"":                     "/mail",
		"//evil.example/x":     "/mail",
		"/\\evil.example":      "/mail",
		"https://evil.example": "/mail",
		"javascript:alert(1)":  "/mail",
		"mail":                 "/mail",
	}
	for in, want := range cases {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRecordMatches(t *testing.T) {
	dkim := &dnsRecord{Purpose: "dkim", Value: "v=DKIM1; k=rsa; p=ABC123"}
	spf := &dnsRecord{Purpose: "spf"}
	dmarc := &dnsRecord{Purpose: "dmarc"}
	checks := []struct {
		rec   *dnsRecord
		found string
		want  bool
	}{
		{dkim, "v=DKIM1; k=rsa; p=ABC123", true},
		{dkim, "v=DKIM1; k=rsa; p=OTHER", false},
		{spf, "v=spf1 mx -all", true},
		{spf, "v=spf1 a:mail.example.com ~all", true},
		{spf, "v=spf1 include:_spf.google.com -all", false},
		{dmarc, "v=DMARC1; p=none", true},
		{dmarc, "v=spf1 -all", false},
	}
	for _, c := range checks {
		if got := recordMatches(c.rec, c.found, "mail.example.com"); got != c.want {
			t.Errorf("%s %q = %v, want %v", c.rec.Purpose, c.found, got, c.want)
		}
	}
}

func TestVerifySignature(t *testing.T) {
	secret, body, now := []byte("whsec"), []byte(`{"id":"evt_1"}`), time.Unix(1_800_000_000, 0)
	sign := func(at time.Time, b []byte) string {
		mac := hmac.New(sha256.New, secret)
		ts := strconv.FormatInt(at.Unix(), 10)
		mac.Write([]byte(ts + "."))
		mac.Write(b)
		return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
	}
	if !verifySignature(sign(now, body), body, secret, now) {
		t.Fatal("valid signature rejected")
	}
	if verifySignature(sign(now, body), []byte(`{"id":"evt_2"}`), secret, now) {
		t.Fatal("tampered body accepted")
	}
	if verifySignature(sign(now.Add(-10*time.Minute), body), body, secret, now) {
		t.Fatal("stale signature accepted")
	}
	if verifySignature("", body, secret, now) || verifySignature("t=1,v1=zz", body, secret, now) {
		t.Fatal("malformed header accepted")
	}
}

func TestValidateAttachments(t *testing.T) {
	list := []mailpkg.OutgoingAttachment{{Filename: `..\..\etc/passwd`, ContentType: "text/plain; charset=x", Data: []byte("x")}, {Filename: "a.bin", ContentType: "nonsense"}}
	if msg := validateAttachments(list); msg != "" {
		t.Fatal(msg)
	}
	if list[0].Filename != "passwd" || list[0].ContentType != "text/plain" || list[1].ContentType != "application/octet-stream" {
		t.Fatalf("not normalized: %+v", list)
	}
	if validateAttachments([]mailpkg.OutgoingAttachment{{Filename: "a\r\nBcc: x", ContentType: "text/plain"}}) == "" {
		t.Fatal("header injection in filename accepted")
	}
	if validateAttachments([]mailpkg.OutgoingAttachment{{Filename: "big", Data: make([]byte, maxAttachmentBytes+1)}}) == "" {
		t.Fatal("oversized attachments accepted")
	}
}
