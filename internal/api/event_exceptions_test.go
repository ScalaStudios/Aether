package api

import (
	"bytes"
	"strings"
	"testing"
	"time"

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
		"title only":              {map[string]any{"title": "Renamed"}, true},
		"same start":              {map[string]any{"start": "2026-10-19T09:00:00"}, true},
		"new duration":            {map[string]any{"duration": "PT2H"}, true},
		"start next day":          {map[string]any{"start": "2026-10-20T09:00:00"}, false},
		"other time zone":         {map[string]any{"timeZone": "America/New_York"}, false},
		"same instant other zone": {map[string]any{"timeZone": "Africa/Johannesburg"}, false},
		"different rule":          {map[string]any{"recurrenceRules": []any{map[string]any{"frequency": "daily"}}}, false},
		"rule removed":            {map[string]any{"recurrenceRules": nil}, false},
		"becomes all day":         {map[string]any{"start": "2026-10-19", "showWithoutTime": true, "duration": "P1D"}, false},
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
	cancelInstance(stored, cancelled, instance)
	again, _, err := encodeCalendar(stored)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(again, "BEGIN:VTIMEZONE") != 1 {
		t.Fatalf("cancelled booking has %d time zones:\n%s", strings.Count(again, "BEGIN:VTIMEZONE"), again)
	}
}

func TestBookingPageEventUsesPageTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	event := ical.NewEvent().Component
	event.Props.SetText(ical.PropUID, "booked")
	event.Props.SetDateTime(ical.PropDateTimeStamp, start)
	doc := bookingCalendar(event, start, start.Add(30*time.Minute), loc)
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(doc); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"DTSTART;TZID=Europe/Lisbon:20260701T100000", "DTEND;TZID=Europe/Lisbon:20260701T103000", "BEGIN:VTIMEZONE\r\nTZID:Europe/Lisbon\r\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("booking missing %q:\n%s", want, out)
		}
	}
	if got, _ := (&ical.Event{Component: event}).DateTimeStart(time.UTC); !got.Equal(start) {
		t.Errorf("start moved to %s", got)
	}
}

func TestCancelledInstanceKeepsItsCustomTimezone(t *testing.T) {
	stored := decodeTestCalendar(t, "BEGIN:VEVENT", "UID:weekly", "DTSTAMP:20260101T000000Z", "DTSTART:20260105T080000Z", "RRULE:FREQ=WEEKLY", "SUMMARY:Standup", "END:VEVENT")
	cancel := decodeTestCalendar(t,
		"BEGIN:VTIMEZONE", "TZID:W. Europe Standard Time", "BEGIN:STANDARD", "DTSTART:16010101T000000", "TZOFFSETFROM:+0100", "TZOFFSETTO:+0100", "END:STANDARD", "END:VTIMEZONE",
		"BEGIN:VTIMEZONE", "TZID:Unused Zone", "BEGIN:STANDARD", "DTSTART:16010101T000000", "TZOFFSETFROM:+0300", "TZOFFSETTO:+0300", "END:STANDARD", "END:VTIMEZONE",
		"BEGIN:VEVENT", "UID:weekly", "DTSTAMP:20260101T000000Z", "RECURRENCE-ID;TZID=W. Europe Standard Time:20260112T090000", "DTSTART;TZID=W. Europe Standard Time:20260112T090000", "SUMMARY:Standup", "END:VEVENT")
	for range 2 {
		cancelInstance(stored, cancel, masterEvent(cancel))
		zones := map[string]int{}
		for _, c := range stored.Children {
			if c.Name == ical.CompTimezone {
				id, _ := c.Props.Text(ical.PropTimezoneID)
				zones[id]++
			}
		}
		if zones["W. Europe Standard Time"] != 1 || zones["Unused Zone"] != 0 {
			t.Fatalf("time zones after cancelling: %v", zones)
		}
	}
}
