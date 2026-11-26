package api

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

func TestExpandObjectIsBounded(t *testing.T) {
	data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VEVENT\r\nUID:fast\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:19000101T000000Z\r\nDTEND:19000101T000001Z\r\nRRULE:FREQ=SECONDLY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	began := time.Now()
	events := expandObject("fast.ics", cal, from, from.Add(400*24*time.Hour))
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("expansion took %s", took)
	}
	if len(events) > maxOccurrences {
		t.Fatalf("got %d occurrences", len(events))
	}
}

func TestCalendarFieldsRejectsSubHourlyRules(t *testing.T) {
	for rule, ok := range map[string]bool{"FREQ=SECONDLY": false, "FREQ=MINUTELY;INTERVAL=30": false, "FREQ=HOURLY": true, "FREQ=WEEKLY": true} {
		data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VEVENT\r\nUID:r\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260101T000000Z\r\nRRULE:" + rule + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
		cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := calendarFields(cal); (err == nil) != ok {
			t.Errorf("%s: err = %v", rule, err)
		}
	}
}

func TestCalendarQueryIsBounded(t *testing.T) {
	object := func(rule string) caldav.CalendarObject {
		data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VEVENT\r\nUID:" + rule + "\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:19000101T000000Z\r\nDTEND:19000101T000001Z\r\nRRULE:" + rule + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
		cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
		if err != nil {
			t.Fatal(err)
		}
		return caldav.CalendarObject{Path: rule, Data: cal}
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	query := &caldav.CalendarQuery{CompFilter: caldav.CompFilter{Name: ical.CompCalendar, Comps: []caldav.CompFilter{{Name: ical.CompEvent, Start: from, End: from.Add(400 * 24 * time.Hour)}}}}
	began := time.Now()
	out, err := filterCalendarObjects(query, []caldav.CalendarObject{object("FREQ=SECONDLY"), object("FREQ=WEEKLY")})
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("query took %s", took)
	}
	if err != nil || len(out) != 1 || out[0].Path != "FREQ=WEEKLY" {
		t.Fatalf("got %+v, %v", out, err)
	}
}
