package api

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func testRoom() *resource {
	limit := 120
	return &resource{Name: "Board Room", WindowDays: 30, MaxMinutes: &limit, WorkingHours: true, WorkDays: []int{1, 2, 3, 4, 5}, WorkStart: 8 * 60, WorkEnd: 18 * 60, TimeZone: "Europe/Berlin"}
}

func TestResourceViolations(t *testing.T) {
	room := testRoom()
	berlin := room.location()
	now := time.Date(2026, 3, 23, 9, 0, 0, 0, berlin)
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 3, day, hour, minute, 0, 0, berlin) }
	cases := []struct {
		name string
		occ  []span
		want string
	}{
		{"in hours", []span{{at(24, 10, 0), at(24, 11, 0)}}, ""},
		{"past", nil, "in the past"},
		{"beyond window", []span{{at(24, 10, 0).AddDate(0, 0, 31), at(24, 11, 0).AddDate(0, 0, 31)}}, "at most 30 days ahead"},
		{"too long", []span{{at(24, 9, 0), at(24, 11, 30)}}, "at most 2 hours at a time"},
		{"before opening", []span{{at(24, 7, 30), at(24, 8, 30)}}, "Monday to Friday, 08:00 to 18:00 (Europe/Berlin)"},
		{"weekend", []span{{at(28, 10, 0), at(28, 11, 0)}}, "Monday to Friday"},
		{"after DST change", []span{{at(30, 17, 0), at(30, 18, 0)}}, ""},
		{"past closing after DST change", []span{{at(30, 17, 30), at(30, 18, 30)}}, "08:00 to 18:00"},
	}
	for _, c := range cases {
		got := room.violation(c.occ, now)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: violation = %q, want %q", c.name, got, c.want)
		}
	}
	room.WorkingHours, room.MaxMinutes = false, nil
	if got := room.violation([]span{{at(28, 22, 0), at(29, 6, 0)}}, now); got != "" {
		t.Errorf("without working hours or a length limit, overnight bookings are fine: %q", got)
	}
}

func TestResourceLabels(t *testing.T) {
	days := map[string][]int{
		"Monday to Friday":                {5, 1, 3, 2, 4},
		"every day":                       {0, 1, 2, 3, 4, 5, 6},
		"on Saturday and Sunday":          {6, 0},
		"on Monday, Wednesday and Friday": {1, 3, 5},
		"on Sunday":                       {0},
		"Friday to Sunday":                {0, 5, 6},
	}
	for want, in := range days {
		if got := weekdaysLabel(in); got != want {
			t.Errorf("weekdaysLabel(%v) = %q, want %q", in, got, want)
		}
	}
	durations := map[int]string{15: "15 minutes", 60: "1 hour", 90: "1 hour 30 minutes", 240: "4 hours", 1440: "1 day", 20160: "14 days", 61: "1 hour 1 minute"}
	for in, want := range durations {
		if got := durationLabel(in); got != want {
			t.Errorf("durationLabel(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstConflict(t *testing.T) {
	base := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	booked := []span{{base, base.Add(time.Hour)}}
	if c := firstConflict([]span{{base.Add(time.Hour), base.Add(2 * time.Hour)}}, booked); c != nil {
		t.Fatalf("back-to-back bookings must not conflict: %+v", c)
	}
	if c := firstConflict([]span{{base.Add(30 * time.Minute), base.Add(90 * time.Minute)}}, booked); c == nil || !c.Start.Equal(base) {
		t.Fatalf("overlap not found: %+v", c)
	}
}

func TestResourceInputNormalize(t *testing.T) {
	in := resourceInput{Type: "room", WorkDays: []int{5, 1, 1}}
	if code, msg := in.normalize(); code != "" {
		t.Fatalf("defaults should be valid: %s %s", code, msg)
	}
	if in.WindowDays != 180 || in.Policy != "auto" || in.BookableBy != "organization" || in.TimeZone != "UTC" || in.WorkStart != 480 || in.WorkEnd != 1080 || len(in.WorkDays) != 2 {
		t.Fatalf("defaults not applied: %+v", in)
	}
	bad := map[string]resourceInput{
		"invalid_type":      {Type: "car"},
		"invalid_approvers": {Type: "room", Policy: "approval"},
		"invalid_window":    {Type: "room", WindowDays: 2000},
		"invalid_hours":     {Type: "room", WorkStart: 600, WorkEnd: 540},
		"invalid_time_zone": {Type: "room", TimeZone: "Mars/Olympus"},
		"invalid_bookers":   {Type: "room", Bookers: []string{"alice@example.com"}},
	}
	for want, in := range bad {
		if code, _ := in.normalize(); code != want {
			t.Errorf("normalize(%+v) = %q, want %q", in, code, want)
		}
	}
}

func decodeTestCalendar(t *testing.T, lines ...string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(strings.Join(append(append([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN"}, lines...), "END:VCALENDAR"), "\r\n") + "\r\n")).Decode()
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func TestBookingDocumentMergesOverrides(t *testing.T) {
	stored := decodeTestCalendar(t,
		"BEGIN:VEVENT", "UID:weekly", "DTSTAMP:20260101T000000Z", "DTSTART:20260105T090000Z", "DTEND:20260105T100000Z", "RRULE:FREQ=WEEKLY", "SUMMARY:Standup", "END:VEVENT")
	moved := decodeTestCalendar(t,
		"BEGIN:VEVENT", "UID:weekly", "DTSTAMP:20260101T000000Z", "RECURRENCE-ID:20260112T090000Z", "DTSTART:20260112T140000Z", "DTEND:20260112T150000Z", "SUMMARY:Standup", "END:VEVENT")
	doc, err := bookingDocument(moved, &storedBooking{Cal: stored})
	if err != nil {
		t.Fatal(err)
	}
	room := &resource{TimeZone: "UTC"}
	occ := room.occurrences(doc, time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	if len(occ) != 3 || occ[1].Start.Hour() != 14 {
		t.Fatalf("the moved occurrence should replace the regular one: %+v", occ)
	}
	cancel := decodeTestCalendar(t, "BEGIN:VEVENT", "UID:weekly", "DTSTAMP:20260101T000000Z", "RECURRENCE-ID:20260119T090000Z", "DTSTART:20260119T090000Z", "SUMMARY:Standup", "END:VEVENT")
	cancelInstance(doc, masterEvent(cancel))
	if occ := room.occurrences(doc, time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)); len(occ) != 2 {
		t.Fatalf("a cancelled occurrence must free the room: %+v", occ)
	}
}

func TestAllDayBookingsUseResourceTimeZone(t *testing.T) {
	cal := decodeTestCalendar(t, "BEGIN:VEVENT", "UID:offsite", "DTSTAMP:20260101T000000Z", "DTSTART;VALUE=DATE:20260610", "DTEND;VALUE=DATE:20260612", "SUMMARY:Offsite", "END:VEVENT")
	room := &resource{TimeZone: "Europe/Berlin"}
	occ := room.occurrences(cal, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	want := time.Date(2026, 6, 10, 0, 0, 0, 0, room.location())
	if len(occ) != 1 || !occ[0].Start.Equal(want) || !occ[0].End.Equal(want.AddDate(0, 0, 2)) {
		t.Fatalf("all-day booking should cover two local days: %+v", occ)
	}
}
