package mail

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWriteMboxEscapesFromLines(t *testing.T) {
	var buf bytes.Buffer
	raw := []byte("From: a@b\r\nSubject: hi\r\n\r\nFrom the start\r\n")
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := WriteMbox(&buf, when, raw); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "From MAILER-DAEMON Fri Jan  2 03:04:05 2026\n") {
		t.Fatalf("header: %q", got)
	}
	if !strings.Contains(got, "\n>From the start\n") {
		t.Fatalf("from line was not escaped: %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("mbox message was not terminated: %q", got)
	}
}

func TestMailExportPath(t *testing.T) {
	got, err := MailExportPath("INBOX", 4)
	if err != nil || got != "mail/INBOX/4.eml" {
		t.Fatalf("path %q err %v", got, err)
	}
	got, err = MailExportPath("Projects/2026", 9)
	if err != nil || got != "mail/Projects/2026/9.eml" {
		t.Fatalf("nested path %q err %v", got, err)
	}
	for _, folder := range []string{"", "../etc", "INBOX/../x", `INBOX\x`, "Shared", "Shared/team"} {
		if _, err := MailExportPath(folder, 1); err == nil {
			t.Fatalf("accepted %q", folder)
		}
	}
}
