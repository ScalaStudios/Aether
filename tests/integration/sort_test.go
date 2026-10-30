//go:build integration

package integration

import (
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

func TestSortAndFilterMessages(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	ic, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("imap login: %v", err)
	}
	defer ic.Close()
	day := func(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }
	attachment := "From: Amy <z@external.example>\r\nTo: " + alice + "\r\nSubject: Delta\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n--b\r\nContent-Type: application/pdf; name=report.pdf\r\nContent-Disposition: attachment; filename=report.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n--b--\r\n"
	imapAppend(t, ic, "INBOX", rfc822("Zed <a@external.example>", alice, "Charlie", "short"), day(3))
	imapAppend(t, ic, "INBOX", rfc822("Bea <b@external.example>", alice, "Alpha", strings.Repeat("long body ", 500)), day(1), imap.FlagSeen)
	imapAppend(t, ic, "INBOX", rfc822("Cal <c@external.example>", alice, "Bravo", "short"), day(2), imap.FlagFlagged)
	imapAppend(t, ic, "INBOX", attachment, day(4), imap.FlagSeen)

	base := "/mailboxes/" + w.aliceMailbox + "/messages?folder=INBOX&"
	list := func(query string) ([]string, *string) {
		var page struct {
			Messages []struct {
				Subject string `json:"subject"`
			} `json:"messages"`
			NextCursor *string `json:"nextCursor"`
		}
		w.alice.must("GET", base+query, nil, &page, 200)
		var out []string
		for _, m := range page.Messages {
			out = append(out, m.Subject)
		}
		return out, page.NextCursor
	}
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"", []string{"Delta", "Charlie", "Bravo", "Alpha"}},
		{"sort=date&order=asc", []string{"Alpha", "Bravo", "Charlie", "Delta"}},
		{"sort=subject&order=asc", []string{"Alpha", "Bravo", "Charlie", "Delta"}},
		{"sort=from&order=asc", []string{"Delta", "Alpha", "Bravo", "Charlie"}},
		{"sort=size&order=desc&limit=1", []string{"Alpha"}},
		{"unread=true", []string{"Charlie", "Bravo"}},
		{"flagged=true", []string{"Bravo"}},
		{"hasAttachments=true", []string{"Delta"}},
		{"since=2026-01-02&until=2026-01-03&order=asc", []string{"Bravo", "Charlie"}},
		{"from=zed&threads=true", []string{"Charlie"}},
		{"unread=true&q=short&threads=true&order=asc", []string{"Bravo", "Charlie"}},
	} {
		if got, _ := list(c.query); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %v, want %v", c.query, got, c.want)
		}
	}

	var pages [][]string
	cursor := ""
	for range 4 {
		got, next := list("sort=date&order=asc&limit=3&cursor=" + url.QueryEscape(cursor))
		pages = append(pages, got)
		if next == nil {
			break
		}
		cursor = *next
	}
	if want := [][]string{{"Alpha", "Bravo", "Charlie"}, {"Delta"}}; !reflect.DeepEqual(pages, want) {
		t.Errorf("pages = %v, want %v", pages, want)
	}

	for _, query := range []string{"sort=priority", "order=up", "since=yesterday", "cursor=bogus"} {
		if code := w.alice.do("GET", base+query, nil, nil); code != 422 {
			t.Errorf("%q returned %d, want 422", query, code)
		}
	}
}
