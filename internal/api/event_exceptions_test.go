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
