package api

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseMailEventAndAddressNormalization(t *testing.T) {
	ev, err := parseMailEvent(`{"u":"Alice@Example.COM*aether-webmail","f":"  Alice@Example.com ","r":["bob+tag@example.com"],"s":-12,"t":1763942400}`)
	if err != nil {
		t.Fatalf("parseMailEvent: %v", err)
	}
	if ev.User != "alice@example.com" || ev.From != "alice@example.com" || ev.Size != 0 {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if base, ok := stripDetailAddress("bob+news@example.com"); !ok || base != "bob@example.com" {
		t.Errorf("stripDetailAddress = %q, %v", base, ok)
	}
	if _, ok := stripDetailAddress("bob@example.com"); ok {
		t.Error("stripDetailAddress should return false when no + detail is present")
	}
	if _, err := parseMailEvent("not-json"); err == nil {
		t.Error("invalid JSON should fail")
	}
}

func TestFillVolumeSeriesZeroFillsMissingDays(t *testing.T) {
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	got := fillVolumeSeries(start, 4, []volumePoint{
		{Day: "2026-11-21", SentCount: 3, ReceivedCount: 5, SentBytes: 1200, ReceivedBytes: 3400},
		{Day: "2026-11-23", SentCount: 1, ReceivedCount: 0, SentBytes: 400, ReceivedBytes: 0},
	})
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	if got[0].Day != "2026-11-20" || got[0].SentCount != 0 || got[0].ReceivedCount != 0 {
		t.Errorf("day 0 = %+v", got[0])
	}
	if got[1].Day != "2026-11-21" || got[1].SentCount != 3 || got[1].ReceivedCount != 5 {
		t.Errorf("day 1 = %+v", got[1])
	}
	if got[2].Day != "2026-11-22" || got[2].SentCount != 0 || got[2].ReceivedCount != 0 {
		t.Errorf("day 2 = %+v", got[2])
	}
	if got[3].Day != "2026-11-23" || got[3].SentCount != 1 {
		t.Errorf("day 3 = %+v", got[3])
	}
}

func TestWantsCSVAndParamParsing(t *testing.T) {
	cases := []struct {
		url, accept string
		wantCSV     bool
		wantValid   bool
	}{
		{"/reports/mailboxes", "", false, true},
		{"/reports/mailboxes?format=csv", "", true, true},
		{"/reports/mailboxes?format=json", "text/csv", false, true},
		{"/reports/mailboxes", "text/csv, */*", true, true},
		{"/reports/mailboxes?format=xml", "", false, false},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.url, nil)
		if c.accept != "" {
			req.Header.Set("Accept", c.accept)
		}
		csvOut, ok := wantsCSV(req)
		if csvOut != c.wantCSV || ok != c.wantValid {
			t.Errorf("%s (Accept=%q) = (%v, %v), want (%v, %v)", c.url, c.accept, csvOut, ok, c.wantCSV, c.wantValid)
		}
	}
	if got := parseIntParam("90", 30, 1, 366); got != 90 {
		t.Errorf("parseIntParam valid = %d", got)
	}
	if got := parseIntParam("9999", 30, 1, 366); got != 30 {
		t.Errorf("parseIntParam overflow = %d", got)
	}
	if got := parseIntParam("-5", 30, 1, 366); got != 30 {
		t.Errorf("parseIntParam negative = %d", got)
	}
}

func TestQuotaPercentAndCSVSafety(t *testing.T) {
	if quotaPercent(500, 0) != nil {
		t.Error("unlimited quota should have nil percent")
	}
	pct := quotaPercent(256, 1024)
	if pct == nil || *pct != 25.0 {
		t.Fatalf("quotaPercent = %v, want 25.0", pct)
	}
	if got := csvSafe("=CMD()"); got != "'=CMD()" {
		t.Errorf("csvSafe formula = %q", got)
	}
	if got := csvSafe("alice@example.com"); got != "alice@example.com" {
		t.Errorf("csvSafe normal email = %q", got)
	}
}

func TestWriteMailboxCSVOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	pct := 50.0
	writeMailboxCSV(rec, "acme", "mailboxes", now, []reportMailbox{{
		Address:        "alice@acme.test",
		DisplayName:    "=Alice, Inc.",
		Kind:           "user",
		Active:         true,
		StorageBytes:   512,
		QuotaBytes:     1024,
		QuotaPercent:   &pct,
		MessageCount:   4,
		LastWebLoginAt: &now,
		LastSignInAt:   &now,
		SentCount:      7,
		ReceivedCount:  11,
		SentBytes:      7000,
		ReceivedBytes:  11000,
		CreatedAt:      now,
	}})
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "acme-mailboxes-2026-11-24.csv") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) != 2 || len(rows[0]) != 18 {
		t.Fatalf("rows = %d x %d", len(rows), len(rows[0]))
	}
	if rows[1][0] != "alice@acme.test" || rows[1][1] != "'=Alice, Inc." || rows[1][6] != "50.0" || rows[1][13] != "7" || rows[1][14] != "11" {
		t.Fatalf("unexpected csv row: %#v", rows[1])
	}
}
