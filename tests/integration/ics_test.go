//go:build integration

package integration

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCalendarImportExport(t *testing.T) {
	w := setupWorld(t)
	suffix := randomSuffix()
	day := time.Now().AddDate(0, 0, 3).Format("20060102")
	ics := strings.ReplaceAll(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Import//EN
BEGIN:VTIMEZONE
TZID:Europe/Amsterdam
BEGIN:STANDARD
DTSTART:19701025T030000
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:19700329T020000
TZOFFSETFROM:+0100
TZOFFSETTO:+0200
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU
END:DAYLIGHT
END:VTIMEZONE
BEGIN:VEVENT
UID:standup-SUFFIX@example.com
DTSTAMP:20260101T000000Z
DTSTART;TZID=Europe/Amsterdam:DAYT093000
DTEND;TZID=Europe/Amsterdam:DAYT094500
RRULE:FREQ=DAILY;COUNT=5
SUMMARY:Imported standup SUFFIX
END:VEVENT
BEGIN:VEVENT
UID:standup-SUFFIX@example.com
DTSTAMP:20260101T000000Z
RECURRENCE-ID;TZID=Europe/Amsterdam:DAYT093000
DTSTART;TZID=Europe/Amsterdam:DAYT110000
DTEND;TZID=Europe/Amsterdam:DAYT111500
SUMMARY:Imported standup SUFFIX (moved)
END:VEVENT
BEGIN:VEVENT
UID:review-SUFFIX@example.com
DTSTAMP:20260101T000000Z
DTSTART:DAYT140000Z
DTEND:DAYT150000Z
SUMMARY:Imported review SUFFIX
END:VEVENT
END:VCALENDAR
`, "SUFFIX", suffix)
	ics = strings.ReplaceAll(strings.ReplaceAll(ics, "DAYT", day+"T"), "\n", "\r\n")

	post := func(body string) (int, string) {
		req, _ := http.NewRequest("POST", w.alice.base+"/calendar/import", strings.NewReader(body))
		req.Header.Set("Content-Type", "text/calendar")
		req.Header.Set("X-CSRF-Token", w.alice.csrf)
		resp, err := w.alice.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}
	if code, body := post(ics); code != 200 || !strings.Contains(body, `"imported":2`) {
		t.Fatalf("first import: %d %s", code, body)
	}
	if code, body := post(ics); code != 200 || !strings.Contains(body, `"updated":2`) || !strings.Contains(body, `"imported":0`) {
		t.Fatalf("second import should update in place: %d %s", code, body)
	}
	if code, _ := post("this is not a calendar"); code != 422 {
		t.Fatalf("invalid file returned %d, want 422", code)
	}

	var events []struct{ Title string }
	start, end := time.Now().UTC().Format(time.RFC3339), time.Now().AddDate(0, 0, 10).UTC().Format(time.RFC3339)
	w.alice.must("GET", "/calendar/events?start="+start+"&end="+end, nil, &events, 200)
	titles := map[string]int{}
	for _, e := range events {
		titles[e.Title]++
	}
	if titles["Imported standup "+suffix] != 4 || titles["Imported standup "+suffix+" (moved)"] != 1 || titles["Imported review "+suffix] != 1 {
		t.Fatalf("imported events did not expand as expected: %v", titles)
	}

	export := string(w.alice.raw("GET", "/calendar/export"))
	if strings.Count(export, "BEGIN:VTIMEZONE") != 1 || !strings.Contains(export, "UID:standup-"+suffix+"@example.com") || !strings.Contains(export, "UID:review-"+suffix+"@example.com") || strings.Count(export, "RECURRENCE-ID") != 1 {
		t.Fatalf("export is missing events or duplicates timezones:\n%s", export)
	}
}
