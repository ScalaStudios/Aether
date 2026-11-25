//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"
)

type attachmentRules struct {
	Action     string   `json:"action"`
	Extensions []string `json:"extensions"`
}

type attachmentPolicy struct {
	Inbound       attachmentRules `json:"inbound"`
	Outbound      attachmentRules `json:"outbound"`
	ExemptSenders []string        `json:"exemptSenders"`
}

func messageWithAttachment(from, to, subject, filename string, data []byte) string {
	return "From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject + "\r\nDate: " + time.Now().Format(time.RFC1123Z) +
		"\r\nMessage-ID: <" + randomSuffix() + "@integration.test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"" + filename + "\"\r\n\r\n" +
		base64.StdEncoding.EncodeToString(data) + "\r\n--b--\r\n"
}

func TestAttachmentFiltering(t *testing.T) {
	w := setupWorld(t)
	path := "/organizations/" + w.orgID + "/attachment-policy"
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	alice := "alice@" + w.domain
	outside := "sender@external.example"

	var policy attachmentPolicy
	w.admin.must("GET", path, nil, &policy, 200)
	if policy.Inbound.Action != "reject" || !slices.Contains(policy.Inbound.Extensions, "exe") || policy.Outbound.Action != "off" {
		t.Fatalf("unexpected default policy %+v", policy)
	}
	if code := w.bob.do("GET", path, nil, nil); code != 404 && code != 403 {
		t.Fatalf("member read the attachment policy: %d", code)
	}
	if code := w.admin.do("PUT", path, attachmentPolicy{Inbound: attachmentRules{"quarantine", nil}, Outbound: attachmentRules{"off", nil}, ExemptSenders: []string{}}, nil); code != 422 {
		t.Fatalf("unknown action accepted: %d", code)
	}

	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, _ := zw.Create("payload.exe")
	f.Write([]byte("x"))
	zw.Close()
	for name, data := range map[string][]byte{"invoice.pdf.exe": []byte("x"), "docs.zip": archive.Bytes()} {
		err := submit(t, smtpAddr, "", "", outside, []string{alice}, messageWithAttachment(outside, alice, "blocked "+name, name, data), false)
		if err == nil || !strings.Contains(err.Error(), "blocked file type") {
			t.Fatalf("%s was not rejected: %v", name, err)
		}
	}
	if err := submit(t, smtpAddr, "", "", outside, []string{alice}, messageWithAttachment(outside, alice, "allowed", "notes.txt", []byte("x")), false); err != nil {
		t.Fatalf("allowed attachment rejected: %v", err)
	}

	policy.Inbound.Action = "remove"
	w.admin.must("PUT", path, policy, &policy, 200)
	if err := submit(t, smtpAddr, "", "", outside, []string{alice}, messageWithAttachment(outside, alice, "stripped", "setup.exe", []byte("x")), false); err != nil {
		t.Fatalf("message with removable attachment rejected: %v", err)
	}
	policy.ExemptSenders = []string{"@External.example"}
	w.admin.must("PUT", path, policy, &policy, 200)
	if !slices.Equal(policy.ExemptSenders, []string{"@external.example"}) {
		t.Fatalf("exempt senders not normalized: %v", policy.ExemptSenders)
	}
	if err := submit(t, smtpAddr, "", "", outside, []string{alice}, messageWithAttachment(outside, alice, "exempt", "setup.exe", []byte("x")), false); err != nil {
		t.Fatalf("exempt sender rejected: %v", err)
	}

	c, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var stripped, exempt []byte
	waitFor(t, "filtered messages", func() bool {
		stripped, exempt = imapFindSubject(t, c, "INBOX", "stripped"), imapFindSubject(t, c, "INBOX", "exempt")
		return stripped != nil && exempt != nil
	})
	if strings.Contains(string(stripped), `filename="setup.exe"`) || !strings.Contains(string(stripped), `The attachment "setup.exe" was removed`) {
		t.Fatalf("attachment was not replaced by a note:\n%s", stripped)
	}
	if !strings.Contains(string(exempt), `filename="setup.exe"`) {
		t.Fatalf("exempt sender's attachment was removed:\n%s", exempt)
	}

	policy.Outbound = attachmentRules{"reject", []string{".EXE"}}
	w.admin.must("PUT", path, policy, &policy, 200)
	err = submit(t, env(t, "AETHER_TEST_SUBMISSION", "postfix:587"), alice, w.aliceAppPassword, alice, []string{"someone@external.example"},
		messageWithAttachment(alice, "someone@external.example", "outbound", "tool.exe", []byte("x")), false)
	if err == nil || !strings.Contains(err.Error(), "blocked file type") {
		t.Fatalf("outbound blocked attachment was not rejected: %v", err)
	}
}
