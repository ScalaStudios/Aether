//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type listedEvent struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	SeriesStart time.Time `json:"seriesStart"`
	Repeat      string    `json:"repeat"`
	Reminder    *int      `json:"reminderMinutes"`
}

func TestWeeklyEventKeepsLocalTimeAcrossDST(t *testing.T) {
	w := setupWorld(t)
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 19, 9, 0, 0, 0, loc)
	var event listedEvent
	w.alice.must("POST", "/calendar/events", map[string]any{"title": "Weekly sync", "start": start, "end": start.Add(30 * time.Minute), "repeat": "weekly", "timeZone": "Europe/Amsterdam"}, &event, 201)
	q := url.Values{"start": {"2026-10-18T00:00:00Z"}, "end": {"2026-11-10T00:00:00Z"}}
	check := func(stage string) {
		var occurrences []listedEvent
		w.alice.must("GET", "/calendar/events?"+q.Encode(), nil, &occurrences, 200)
		count := 0
		for _, o := range occurrences {
			if o.ID != event.ID {
				continue
			}
			count++
			if local := o.Start.In(loc); local.Hour() != 9 || local.Minute() != 0 {
				t.Errorf("%s: occurrence at %s, want 09:00 local", stage, local)
			}
		}
		if count != 4 {
			t.Fatalf("%s: got %d occurrences, want 4", stage, count)
		}
	}
	check("created")
	w.alice.must("PUT", "/calendar/events/"+event.ID, map[string]any{"title": "Weekly sync (renamed)", "start": start.UTC(), "end": start.Add(30 * time.Minute).UTC(), "repeat": "weekly", "timeZone": "UTC"}, nil, 200)
	check("renamed")
	w.alice.must("DELETE", "/calendar/events/"+event.ID, nil, nil, 204)
}

func TestEditedEventExportsTimezone(t *testing.T) {
	w := setupWorld(t)
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 19, 9, 0, 0, 0, loc)
	var event listedEvent
	w.alice.must("POST", "/calendar/events", map[string]any{"title": "Chicago sync", "start": start, "end": start.Add(time.Hour), "repeat": "weekly", "timeZone": "America/Chicago"}, &event, 201)
	w.alice.must("PUT", "/calendar/events/"+event.ID, map[string]any{"title": "Chicago sync (renamed)", "start": start, "end": start.Add(time.Hour), "repeat": "weekly"}, nil, 200)
	export := string(w.alice.raw("GET", "/calendar/export"))
	for _, want := range []string{"BEGIN:VTIMEZONE\r\nTZID:America/Chicago\r\n", "DTSTART;TZID=America/Chicago:20261019T090000", "TZOFFSETTO:-0600"} {
		if !strings.Contains(export, want) {
			t.Errorf("export is missing %q:\n%s", want, export)
		}
	}
	w.alice.must("DELETE", "/calendar/events/"+event.ID, nil, nil, 204)
}

func TestEditingEventTitleKeepsExceptionsTimezoneAndAlarms(t *testing.T) {
	w := setupWorld(t)
	suffix := randomSuffix()
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
UID:planning-SUFFIX@example.com
DTSTAMP:20260101T000000Z
DTSTART;TZID=Europe/Amsterdam:20261019T090000
DTEND;TZID=Europe/Amsterdam:20261019T100000
RRULE:FREQ=WEEKLY
EXDATE;TZID=Europe/Amsterdam:20261102T090000
SUMMARY:Planning SUFFIX
BEGIN:VALARM
ACTION:AUDIO
TRIGGER:-PT10M
END:VALARM
END:VEVENT
BEGIN:VEVENT
UID:planning-SUFFIX@example.com
DTSTAMP:20260101T000000Z
RECURRENCE-ID;TZID=Europe/Amsterdam:20261026T090000
DTSTART;TZID=Europe/Amsterdam:20261026T140000
DTEND;TZID=Europe/Amsterdam:20261026T150000
SUMMARY:Planning SUFFIX (moved)
END:VEVENT
END:VCALENDAR
`, "SUFFIX", suffix)
	ics = strings.ReplaceAll(ics, "\n", "\r\n")
	req, _ := http.NewRequest("POST", w.alice.base+"/calendar/import", strings.NewReader(ics))
	req.Header.Set("Content-Type", "text/calendar")
	req.Header.Set("X-CSRF-Token", w.alice.csrf)
	resp, err := w.alice.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"imported":1`) {
		t.Fatalf("import: %d %s", resp.StatusCode, body)
	}

	q := url.Values{"start": {"2026-10-18T00:00:00Z"}, "end": {"2026-11-17T00:00:00Z"}}
	var occurrences []listedEvent
	w.alice.must("GET", "/calendar/events?"+q.Encode(), nil, &occurrences, 200)
	var master *listedEvent
	for i, o := range occurrences {
		if o.Title == "Planning "+suffix {
			master = &occurrences[i]
		}
	}
	if master == nil || master.Repeat != "weekly" {
		t.Fatalf("imported series not listed as weekly: %+v", occurrences)
	}
	w.alice.must("PUT", "/calendar/events/"+master.ID, map[string]any{
		"title":           "Planning " + suffix + " (renamed)",
		"start":           master.SeriesStart,
		"end":             master.SeriesStart.Add(master.End.Sub(master.Start)),
		"repeat":          "weekly",
		"reminderMinutes": master.Reminder,
	}, nil, 200)

	export := string(w.alice.raw("GET", "/calendar/export"))
	start := strings.Index(export, "UID:planning-"+suffix)
	if start < 0 {
		t.Fatalf("export lost the series:\n%s", export)
	}
	for _, want := range []string{
		"SUMMARY:Planning " + suffix + " (renamed)",
		"SUMMARY:Planning " + suffix + " (moved)",
		"RECURRENCE-ID;TZID=Europe/Amsterdam:20261026T090000",
		"EXDATE;TZID=Europe/Amsterdam:20261102T090000",
		"DTSTART;TZID=Europe/Amsterdam:20261019T090000",
		"BEGIN:VTIMEZONE",
		"ACTION:AUDIO",
	} {
		if !strings.Contains(export, want) {
			t.Errorf("export is missing %q:\n%s", want, export)
		}
	}

	w.alice.must("GET", "/calendar/events?"+q.Encode(), nil, &occurrences, 200)
	titles := map[string]int{}
	for _, o := range occurrences {
		titles[o.Title]++
	}
	if titles["Planning "+suffix+" (renamed)"] != 3 || titles["Planning "+suffix+" (moved)"] != 1 {
		t.Fatalf("after the edit the series expands to %v", titles)
	}
}
