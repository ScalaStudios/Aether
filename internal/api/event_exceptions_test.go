package api

import (
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

const seriesWithExceptions = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VEVENT
UID:planning
DTSTAMP:20260101T000000Z
DTSTART;TZID=Europe/Amsterdam:20261019T090000
DTEND;TZID=Europe/Amsterdam:20261019T100000
RRULE:FREQ=WEEKLY
EXDATE;TZID=Europe/Amsterdam:20261102T090000
SUMMARY:Planning
END:VEVENT
BEGIN:VEVENT
UID:planning
DTSTAMP:20260101T000000Z
RECURRENCE-ID;TZID=Europe/Amsterdam:20261026T090000
DTSTART;TZID=Europe/Amsterdam:20261026T140000
DTEND;TZID=Europe/Amsterdam:20261026T150000
SUMMARY:Planning (moved)
END:VEVENT
END:VCALENDAR
`

func TestJMAPMovingSeriesReanchorsExceptions(t *testing.T) {
	for name, tc := range map[string]struct {
		patch map[string]any
		kept  bool
	}{
		"title only":      {map[string]any{"title": "Renamed"}, true},
		"same start":      {map[string]any{"start": "2026-10-19T09:00:00"}, true},
		"new duration":    {map[string]any{"duration": "PT2H"}, true},
		"start next day":  {map[string]any{"start": "2026-10-20T09:00:00"}, false},
		"other time zone": {map[string]any{"timeZone": "America/New_York"}, false},
		"different rule":  {map[string]any{"recurrenceRules": []any{map[string]any{"frequency": "daily"}}}, false},
		"rule removed":    {map[string]any{"recurrenceRules": nil}, false},
		"becomes all day": {map[string]any{"start": "2026-10-19", "showWithoutTime": true, "duration": "P1D"}, false},
	} {
		cal, err := ical.NewDecoder(strings.NewReader(strings.ReplaceAll(seriesWithExceptions, "\n", "\r\n"))).Decode()
		if err != nil {
			t.Fatal(err)
		}
		master := masterEvent(cal)
		if uerr := rescheduleEvent(cal, master, tc.patch); uerr != nil {
			t.Fatalf("%s: %v", name, uerr)
		}
		hasExdate := master.Props.Get(ical.PropExceptionDates) != nil
		overrides := len(cal.Events()) - 1
		if tc.kept && (!hasExdate || overrides != 1) || !tc.kept && (hasExdate || overrides != 0) {
			t.Errorf("%s: EXDATE %v, %d overrides, want kept=%v", name, hasExdate, overrides, tc.kept)
		}
	}
}

func TestResourceBookingsStoreTimezones(t *testing.T) {
	request, err := ical.NewDecoder(strings.NewReader(strings.ReplaceAll(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
METHOD:REQUEST
BEGIN:VEVENT
UID:room
DTSTAMP:20260101T000000Z
DTSTART;TZID=America/Chicago:20261019T090000
DTEND;TZID=America/Chicago:20261019T100000
RRULE:FREQ=WEEKLY
SUMMARY:Room booking
END:VEVENT
END:VCALENDAR
`, "\n", "\r\n"))).Decode()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := bookingDocument(request, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := encodeCalendar(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "BEGIN:VTIMEZONE\r\nTZID:America/Chicago\r\n") || !strings.Contains(data, "TZOFFSETTO:-0600") {
		t.Fatalf("booking stored without its time zone:\n%s", data)
	}
	stored, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	cancelled, _ := cloneCalendar(request)
	instance := masterEvent(cancelled)
	instance.Props.Set(&ical.Prop{Name: ical.PropRecurrenceID, Value: "20261026T090000", Params: ical.Params{ical.ParamTimezoneID: {"America/Chicago"}}})
	cancelInstance(stored, instance)
	again, _, err := encodeCalendar(stored)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(again, "BEGIN:VTIMEZONE") != 1 {
		t.Fatalf("cancelled booking has %d time zones:\n%s", strings.Count(again, "BEGIN:VTIMEZONE"), again)
	}
}
