//go:build integration

package integration

import (
	"encoding/base64"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"
)

func TestFullTextSearch(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	ic, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("imap login: %v", err)
	}
	defer ic.Close()
	pdf, err := os.ReadFile("testdata/searchable.pdf")
	if err != nil {
		t.Fatal(err)
	}
	report := "From: Amy <amy@external.example>\r\nTo: " + alice + "\r\nSubject: Report\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n--b\r\nContent-Type: application/pdf; name=q3-report.pdf\r\nContent-Disposition: attachment; filename=q3-report.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString(pdf) + "\r\n--b--\r\n"
	day := func(d int) time.Time { return time.Date(2026, 2, d, 12, 0, 0, 0, time.UTC) }
	imapAppend(t, ic, "INBOX", rfc822("Ben <ben@external.example>", alice, "Budget", "the pelicanmarmot budget is attached later"), day(1))
	imapAppend(t, ic, "INBOX", report, day(2))
	imapAppend(t, ic, "INBOX", rfc822("Cy <cy@external.example>", alice, "Lunch", "tacos on friday"), day(3))

	search := func(q string) []string {
		var page struct {
			Messages []struct {
				Subject string `json:"subject"`
			} `json:"messages"`
		}
		w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/messages?folder=INBOX&q="+url.QueryEscape(q), nil, &page, 200)
		out := []string{}
		for _, m := range page.Messages {
			out = append(out, m.Subject)
		}
		return out
	}
	cases := []struct {
		q    string
		want []string
	}{
		{"pelicanmarmot", []string{"Budget"}},
		{"pelicanmar", []string{"Budget"}},
		{"from:amy", []string{"Report"}},
		{"to:" + alice + " tacos", []string{"Lunch"}},
		{"has:attachment", []string{"Report"}},
		{"filename:REPORT", []string{"Report"}},
		{"filename:invoice", []string{}},
		{"nosuchwordanywhere", []string{}},
	}
	if env(t, "AETHER_TEST_TIKA", "") == "true" {
		cases = append(cases, struct {
			q    string
			want []string
		}{"zephyrquokka", []string{"Report"}})
	}
	for _, c := range cases {
		if got := search(c.q); !slices.Equal(got, c.want) {
			t.Errorf("q=%q: got %v, want %v", c.q, got, c.want)
		}
	}
}
