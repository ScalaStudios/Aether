//go:build integration

package integration

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFreeBusy(t *testing.T) {
	w := setupWorld(t)
	bob := "bob@" + w.domain
	start := time.Now().Add(120 * time.Hour).Truncate(time.Hour).UTC()
	title := "Private review " + randomSuffix()
	w.bob.must("POST", "/calendar/events", map[string]any{"title": title, "start": start, "end": start.Add(time.Hour)}, nil, 201)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	w.bob.must("POST", "/calendar/events", map[string]any{"title": "Holiday", "start": day, "end": day.AddDate(0, 0, 1), "allDay": true}, nil, 201)

	query := url.Values{
		"start":    {start.Add(-2 * time.Hour).Format(time.RFC3339)},
		"end":      {start.Add(3 * time.Hour).Format(time.RFC3339)},
		"attendee": {strings.ToUpper(bob), "stranger@example.com"},
	}
	raw := string(w.alice.raw("GET", "/calendar/freebusy?"+query.Encode()))
	if strings.Contains(raw, title) || strings.Contains(raw, "Holiday") {
		t.Fatalf("free/busy must not reveal event details: %s", raw)
	}
	var out []struct {
		Email string `json:"email"`
		Known bool   `json:"known"`
		Busy  []struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"busy"`
	}
	w.alice.must("GET", "/calendar/freebusy?"+query.Encode(), nil, &out, 200)
	if len(out) != 2 || out[0].Email != bob || !out[0].Known || len(out[0].Busy) != 1 || !out[0].Busy[0].Start.Equal(start) {
		t.Fatalf("bob should be busy for exactly the timed event: %+v", out)
	}
	if out[1].Known || len(out[1].Busy) != 0 {
		t.Fatalf("addresses outside the organization are unknown: %+v", out[1])
	}
	if code := w.alice.do("GET", "/calendar/freebusy?start="+url.QueryEscape(start.Format(time.RFC3339))+"&end="+url.QueryEscape(start.AddDate(0, 2, 0).Format(time.RFC3339))+"&attendee="+bob, nil, nil); code != 422 {
		t.Fatalf("ranges over 31 days should be refused, got %d", code)
	}
}
