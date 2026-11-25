package api

import (
	"testing"
	"time"
)

func TestCaseCriteriaNormalize(t *testing.T) {
	id := "6f1c9a52-7d43-4a8e-9b1f-0c2d3e4f5a6b"
	c := caseCriteria{Scope: "mailboxes", MailboxIDs: []string{id}, DomainIDs: []string{id}, From: "  alice  ", Since: "2026-01-01", Until: "2026-01-31"}
	q, msg := c.normalize()
	if msg != "" {
		t.Fatal(msg)
	}
	if len(c.DomainIDs) != 0 || q.From != "alice" || !q.Before.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("normalized to %+v, %+v", c, q)
	}
	for _, bad := range []caseCriteria{
		{Scope: "everything"},
		{Scope: "mailboxes"},
		{Scope: "domains", DomainIDs: []string{"not-a-uuid"}},
		{Scope: "organization", Since: "31/01/2026"},
		{Scope: "organization", Since: "2026-02-01", Until: "2026-01-01"},
		{Scope: "organization", Subject: "a\r\nb"},
	} {
		if _, msg := bad.normalize(); msg == "" {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestZipPathStaysInside(t *testing.T) {
	for _, c := range []struct {
		parts []string
		want  string
	}{
		{[]string{"a@example.com", "INBOX/Work"}, "a@example.com/INBOX/Work"},
		{[]string{"a@example.com", "../../etc"}, "a@example.com/etc"},
		{[]string{"a@example.com", "C:\\x/./y"}, "a@example.com/C__x/y"},
	} {
		if got := zipPath(c.parts...); got != c.want {
			t.Errorf("zipPath(%q) = %q, want %q", c.parts, got, c.want)
		}
	}
}

func TestValidArchiveAddress(t *testing.T) {
	for address, want := range map[string]bool{
		"archive@vault.example": true,
		"Archive <a@b.example>": false,
		"a@b@c":                 false,
		"a'b@example.com":       false,
		"not-an-address":        false,
	} {
		if validArchiveAddress(address) != want {
			t.Errorf("validArchiveAddress(%q) = %v", address, !want)
		}
	}
}
