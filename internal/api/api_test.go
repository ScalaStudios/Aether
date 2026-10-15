package api

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-webdav"

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
	sts := &dnsRecord{Purpose: "mta-sts", Value: "v=STSv1; id=abc123"}
	tlsrpt := &dnsRecord{Purpose: "tls-rpt"}
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
		{sts, "v=STSv1;id=abc123", true},
		{sts, "v=STSv1; id=stale", false},
		{tlsrpt, "v=TLSRPTv1; rua=mailto:tls@example.com", true},
		{tlsrpt, "v=DMARC1; p=none", false},
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

func TestDAVStatusCodeReadsLibraryErrors(t *testing.T) {
	if got := davStatusCode(webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("changed"))); got != http.StatusPreconditionFailed {
		t.Fatalf("status %d, want 412; go-webdav may have renamed HTTPError.Code", got)
	}
	if got := davStatusCode(errors.New("plain")); got != 0 {
		t.Fatalf("plain error gave status %d", got)
	}
}

func TestPublicAddressBlocksInternalTargets(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "172.18.0.5", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fd00::1", "fe80::1", "::ffff:10.0.0.1", "64:ff9b::a00:1", "240.0.0.1"}
	for _, ip := range blocked {
		if publicAddress(netip.MustParseAddr(ip)) {
			t.Errorf("%s should be blocked", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "31.77.212.35", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(ip)) {
			t.Errorf("%s should be allowed", ip)
		}
	}
}

func TestAPIKeyScopes(t *testing.T) {
	k := &apiKeyIdentity{Scopes: []string{"domains:read", "mailboxes:write"}}
	cases := []struct {
		method, resource string
		want             bool
	}{
		{"GET", "domains", true},
		{"POST", "domains", false},
		{"DELETE", "mailboxes", true},
		{"GET", "mailboxes", true},
		{"GET", "audit", false},
		{"GET", "", true},
	}
	for _, c := range cases {
		if got := k.permits(c.method, c.resource); got != c.want {
			t.Errorf("%s %s = %v, want %v", c.method, c.resource, got, c.want)
		}
	}
}

func TestWebhookSignatureMatchesVerifier(t *testing.T) {
	secret, body, now := []byte("whsec_x"), []byte(`{"type":"webhook.test"}`), time.Now()
	if !verifySignature(signWebhook(secret, body, now), body, secret, now) {
		t.Fatal("webhook signature does not verify")
	}
}

const sampleDMARC = `<?xml version="1.0" encoding="UTF-8" ?>
<feedback>
  <report_metadata><org_name>google.com</org_name><report_id>123456789</report_id>
    <date_range><begin>1792022400</begin><end>1792108799</end></date_range></report_metadata>
  <policy_published><domain>example.com</domain><p>quarantine</p></policy_published>
  <record><row><source_ip>31.77.212.35</source_ip><count>12</count>
    <policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>pass</spf></policy_evaluated></row>
    <identifiers><header_from>example.com</header_from></identifiers></record>
  <record><row><source_ip>203.0.113.9</source_ip><count>3</count>
    <policy_evaluated><disposition>quarantine</disposition><dkim>fail</dkim><spf>fail</spf></policy_evaluated></row>
    <identifiers><header_from>example.com</header_from></identifiers></record>
</feedback>`

func TestParseDMARCReportAttachments(t *testing.T) {
	var gz, zipped bytes.Buffer
	gw := gzip.NewWriter(&gz)
	gw.Write([]byte(sampleDMARC))
	gw.Close()
	zw := zip.NewWriter(&zipped)
	f, _ := zw.Create("google.com!example.com!1792022400!1792108799.xml")
	f.Write([]byte(sampleDMARC))
	zw.Close()
	for name, attachment := range map[string][]byte{"gzip": gz.Bytes(), "zip": zipped.Bytes(), "plain": []byte(sampleDMARC)} {
		msg := "From: noreply-dmarc-support@google.com\r\nTo: dmarc-reports@example.com\r\nSubject: Report domain: example.com\r\nMIME-Version: 1.0\r\n" +
			"Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nThis is a report.\r\n" +
			"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(attachment) + "\r\n--b--\r\n"
		reports, err := parseDMARCMessage(strings.NewReader(msg))
		if err != nil || len(reports) != 1 {
			t.Fatalf("%s: %v %d", name, err, len(reports))
		}
		fb := reports[0]
		if fb.Metadata.OrgName != "google.com" || fb.Policy.Domain != "example.com" || len(fb.Records) != 2 || fb.Records[1].Row.Count != 3 || fb.Records[0].Row.Evaluated.DKIM != "pass" {
			t.Fatalf("%s: unexpected report %+v", name, fb)
		}
	}
	if _, err := parseDMARCMessage(strings.NewReader("From: a@b.c\r\nSubject: hi\r\n\r\nhello\r\n")); err == nil {
		t.Fatal("a plain message was accepted as a report")
	}
}
