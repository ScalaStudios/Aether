package api

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func TestJMAPWeeklyEventKeepsLocalTimeAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	cal := newCalendar()
	comp := ical.NewEvent().Component
	comp.Props.SetText(ical.PropUID, "weekly")
	comp.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	cal.Children = append(cal.Children, comp)
	if uerr := applyEventSchedule(comp, map[string]any{"start": "2026-10-19T09:00:00", "timeZone": "Europe/Amsterdam", "duration": "PT30M", "recurrenceRules": []any{map[string]any{"frequency": "weekly"}}}); uerr != nil {
		t.Fatal(uerr)
	}
	check := func(stage string) {
		from := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
		got := expandObject("weekly.ics", cal, from, from.Add(23*24*time.Hour))
		if len(got) != 4 {
			t.Fatalf("%s: got %d occurrences, want 4", stage, len(got))
		}
		for _, o := range got {
			if local := o.Start.In(loc); local.Hour() != 9 || local.Minute() != 0 || o.End.Sub(o.Start) != 30*time.Minute {
				t.Errorf("%s: occurrence %s to %s, want 09:00 local for 30m", stage, local, o.End.In(loc))
			}
		}
	}
	check("created")
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		t.Fatal(err)
	}
	obj := eventObject(jmapStoredEvent{Data: buf.String()})
	if obj["start"] != "2026-10-19T09:00:00" || obj["timeZone"] != "Europe/Amsterdam" || obj["duration"] != "PT30M" || obj["recurrenceRules"] == nil {
		t.Fatalf("event object start %v zone %v duration %v rules %v", obj["start"], obj["timeZone"], obj["duration"], obj["recurrenceRules"])
	}
	if uerr := applyEventSchedule(comp, map[string]any{"start": "2026-10-19T09:00:00"}); uerr != nil {
		t.Fatal(uerr)
	}
	check("restarted")
}

func TestEditedEventExportsTimezone(t *testing.T) {
	data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VEVENT\r\nUID:tz\r\nDTSTAMP:20260101T000000Z\r\nDTSTART;TZID=America/New_York:20261019T090000\r\nDTEND;TZID=America/New_York:20261019T100000\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:Standup\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if uerr := applyEventSchedule(masterEvent(cal), map[string]any{"start": "2026-10-20T09:00:00"}); uerr != nil {
		t.Fatal(uerr)
	}
	addTimezones(cal)
	if _, err := calendarFields(cal); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		t.Fatal(err)
	}
	out, err := compileCalendar([]string{buf.String()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\n",
		"DTSTART;TZID=America/New_York:20261020T090000",
		"BEGIN:DAYLIGHT\r\nDTSTART:20260308T020000\r\nTZNAME:EDT\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\n",
		"BEGIN:STANDARD\r\nDTSTART:20261101T020000\r\nTZNAME:EST\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\n",
		"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\nTZNAME:EDT\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\n",
		"RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\nTZNAME:EST\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("export missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "BEGIN:VTIMEZONE") != 1 {
		t.Errorf("want one VTIMEZONE:\n%s", out)
	}
}

func TestExportTimezoneCoversOldAndCurrentEvents(t *testing.T) {
	weekly := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VEVENT\r\nUID:tz\r\nDTSTAMP:20260101T000000Z\r\nDTSTART;TZID=America/New_York:20261019T090000\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:Standup\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	birthday := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VEVENT\r\nUID:old\r\nDTSTAMP:20260101T000000Z\r\nDTSTART;TZID=America/New_York:19900615T120000\r\nRRULE:FREQ=YEARLY\r\nSUMMARY:Birthday\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	out, err := compileCalendar([]string{weekly, birthday})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"BEGIN:DAYLIGHT\r\nDTSTART:19890402T020000\r\nTZNAME:EDT\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\n",
		"BEGIN:DAYLIGHT\r\nDTSTART:20260308T020000\r\nTZNAME:EDT\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\n",
		"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\nTZNAME:EDT\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("export missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "BYMONTH=4;BYDAY=1SU") {
		t.Errorf("current rules extrapolated from 1989:\n%s", out)
	}
}
