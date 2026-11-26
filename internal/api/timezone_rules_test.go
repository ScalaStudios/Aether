package api

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func decodeCal(t *testing.T, data string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(strings.ReplaceAll(data, "\n", "\r\n"))).Decode()
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func encodeCal(t *testing.T, cal *ical.Calendar) string {
	t.Helper()
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func parseOffset(t *testing.T, s string) int {
	t.Helper()
	off, err := time.Parse("-0700", s)
	if err != nil {
		t.Fatalf("offset %q: %v", s, err)
	}
	_, secs := off.Zone()
	return secs
}

func TestVTimezoneMatchesEveryZone(t *testing.T) {
	root := "/usr/share/zoneinfo"
	if _, err := os.Stat(root); err != nil {
		t.Skip("no zoneinfo directory")
	}
	var ids []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() && (rel == "posix" || rel == "right") {
			return filepath.SkipDir
		}
		if !d.IsDir() && !strings.Contains(rel, ".") && rel != "posixrules" {
			ids = append(ids, rel)
		}
		return nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, id := range ids {
			loc, ok := validTimezone(id)
			if !ok {
				continue
			}
			tz := vtimezone(id, loc, 1970, 2100)
			prev := time.Time{}
			for i, o := range tz.Children {
				start := o.Props.Get(ical.PropDateTimeStart).Value
				fromText := o.Props.Get(ical.PropTimezoneOffsetFrom).Value
				toText := o.Props.Get(ical.PropTimezoneOffsetTo).Value
				from, want := parseOffset(t, fromText), parseOffset(t, toText)
				local, err := time.Parse("20060102T150405", start)
				if err != nil {
					t.Errorf("%s: DTSTART %q", id, start)
					return
				}
				onset := local.Add(-time.Duration(from) * time.Second)
				if i > 0 && !onset.After(prev) {
					t.Errorf("%s: observance %d at %s does not follow %s", id, i, onset, prev)
					return
				}
				prev = onset
				if _, got := onset.In(loc).Zone(); got != want && i > 0 {
					t.Errorf("%s: at %s offset %d, observance says %d", id, onset, got, want)
					return
				}
				if _, got := onset.Add(-time.Second).In(loc).Zone(); got != from && i > 0 {
					t.Errorf("%s: before %s offset %d, observance says %d", id, onset, got, from)
					return
				}
			}
			if len(tz.Children) == 0 || len(tz.Children) > maxObservances {
				t.Errorf("%s: %d observances", id, len(tz.Children))
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("generating time zones did not finish")
	}
	if len(ids) < 300 {
		t.Fatalf("only %d zones found", len(ids))
	}
}

func TestIrregularZoneHasNoRepeatingRule(t *testing.T) {
	for _, id := range []string{"Africa/Casablanca", "Asia/Jerusalem"} {
		checkIrregularZone(t, id)
	}
}

func checkIrregularZone(t *testing.T, id string) {
	cal := decodeCal(t, strings.ReplaceAll(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VEVENT
UID:c
DTSTAMP:20260101T000000Z
DTSTART;TZID=ZONE:20261019T090000
RRULE:FREQ=WEEKLY
END:VEVENT
END:VCALENDAR
`, "ZONE", id))
	addTimezones(cal)
	out := encodeCal(t, cal)
	if strings.Contains(out, "RRULE:FREQ=YEARLY") {
		t.Errorf("%s got a repeating rule:\n%s", id, out)
	}
	loc, _ := time.LoadLocation(id)
	var tz *ical.Component
	for _, c := range cal.Children {
		if c.Name == ical.CompTimezone {
			tz = c
		}
	}
	if tz == nil {
		t.Fatalf("no time zone:\n%s", out)
	}
	for day := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC); day.Year() <= time.Now().Year()+10; day = day.AddDate(0, 0, 1) {
		offset := parseOffset(t, tz.Children[0].Props.Get(ical.PropTimezoneOffsetFrom).Value)
		for _, o := range tz.Children {
			from := parseOffset(t, o.Props.Get(ical.PropTimezoneOffsetFrom).Value)
			local, _ := time.Parse("20060102T150405", o.Props.Get(ical.PropDateTimeStart).Value)
			if !local.Add(-time.Duration(from) * time.Second).After(day) {
				offset = parseOffset(t, o.Props.Get(ical.PropTimezoneOffsetTo).Value)
			}
		}
		if _, want := day.In(loc).Zone(); offset != want {
			t.Fatalf("%s %s: definition gives %d, zone data %d:\n%s", id, day, offset, want, out)
		}
	}
	ams := decodeCal(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VEVENT
UID:a
DTSTAMP:20260101T000000Z
DTSTART;TZID=Europe/Amsterdam:20261019T090000
RRULE:FREQ=WEEKLY
END:VEVENT
END:VCALENDAR
`)
	addTimezones(ams)
	if out := encodeCal(t, ams); !strings.Contains(out, "RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU") {
		t.Errorf("regular zone lost its repeating rule:\n%s", out)
	}
}

func TestEditRebuildsGeneratedTimezone(t *testing.T) {
	cal := decodeCal(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VEVENT
UID:m
DTSTAMP:20260101T000000Z
DTSTART;TZID=America/New_York:20261019T090000
DTEND;TZID=America/New_York:20261019T100000
END:VEVENT
END:VCALENDAR
`)
	addTimezones(cal)
	if uerr := applyEventSchedule(masterEvent(cal), map[string]any{"start": "1990-06-15T12:00:00"}); uerr != nil {
		t.Fatal(uerr)
	}
	addTimezones(cal)
	out := encodeCal(t, cal)
	if strings.Count(out, "BEGIN:VTIMEZONE") != 1 || !strings.Contains(out, "DTSTART:19890402T020000") {
		t.Errorf("moved event not covered:\n%s", out)
	}
	custom := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VTIMEZONE
TZID:America/Chicago
BEGIN:STANDARD
DTSTART:19700101T000000
TZOFFSETFROM:-0600
TZOFFSETTO:-0600
TZNAME:CUSTOM
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:c
DTSTAMP:20260101T000000Z
DTSTART;TZID=America/Chicago:19900615T120000
END:VEVENT
END:VCALENDAR
`
	kept := decodeCal(t, custom)
	addTimezones(kept)
	if out := encodeCal(t, kept); strings.Count(out, "BEGIN:VTIMEZONE") != 1 || !strings.Contains(out, "TZNAME:CUSTOM") {
		t.Errorf("client time zone replaced:\n%s", out)
	}
	recent := decodeCal(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VEVENT
UID:r
DTSTAMP:20260101T000000Z
DTSTART;TZID=America/New_York:20261019T090000
END:VEVENT
END:VCALENDAR
`)
	addTimezones(recent)
	old := decodeCal(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VEVENT
UID:o
DTSTAMP:20260101T000000Z
DTSTART;TZID=America/New_York:19900615T120000
END:VEVENT
END:VCALENDAR
`)
	addTimezones(old)
	export, err := compileCalendar([]string{encodeCal(t, recent), encodeCal(t, old)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(export, "BEGIN:VTIMEZONE") != 1 || !strings.Contains(export, "DTSTART:19890402T020000") || !strings.Contains(export, "DTSTART:20261101T020000") {
		t.Errorf("export does not cover both events:\n%s", export)
	}
}

func viaJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestJMAPRecurrenceRoundTripKeepsRule(t *testing.T) {
	newEvent := func(rule string) *ical.Component {
		cal := decodeCal(t, "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:test\nBEGIN:VEVENT\nUID:r\nDTSTAMP:20260101T000000Z\nDTSTART;TZID=Europe/Amsterdam:20261019T090000\nRRULE:"+rule+"\nEND:VEVENT\nEND:VCALENDAR\n")
		return masterEvent(cal)
	}
	for _, rule := range []string{"FREQ=WEEKLY;UNTIL=20261231T000000Z;BYDAY=MO,WE", "FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU", "FREQ=DAILY;INTERVAL=2;COUNT=5"} {
		comp := newEvent(rule)
		if uerr := applyEventSchedule(comp, map[string]any{"title": "x", "recurrenceRules": viaJSON(t, []any{rruleObject(rule)})}); uerr != nil {
			t.Fatalf("%s: %v", rule, uerr)
		}
		if got := comp.Props.Get(ical.PropRecurrenceRule).Value; got != rule {
			t.Errorf("round trip changed %q to %q", rule, got)
		}
	}
	comp := newEvent("FREQ=WEEKLY")
	if uerr := applyEventSchedule(comp, map[string]any{"recurrenceRules": viaJSON(t, []any{map[string]any{"frequency": "weekly", "byDay": []any{"tu", map[string]any{"@type": "NDay", "day": "fr", "nthOfPeriod": -1}}, "until": "2026-12-31T09:00:00"}})}); uerr != nil {
		t.Fatal(uerr)
	}
	if got := comp.Props.Get(ical.PropRecurrenceRule).Value; got != "FREQ=WEEKLY;UNTIL=20261231T080000Z;BYDAY=TU,-1FR" {
		t.Errorf("written rule %q", got)
	}
	for _, bad := range []map[string]any{
		{"frequency": "weekly", "byMonth": []any{"3"}},
		{"frequency": "weekly", "byDay": []any{"xx"}},
		{"frequency": "weekly", "until": "soon"},
		{"frequency": "weekly", "count": 2, "until": "2026-12-31T09:00:00"},
		{"frequency": "weekly", "interval": 0},
	} {
		if uerr := applyEventSchedule(newEvent("FREQ=WEEKLY"), map[string]any{"recurrenceRules": viaJSON(t, []any{bad})}); uerr == nil || uerr["type"] != "invalidProperties" {
			t.Errorf("%v accepted: %v", bad, uerr)
		}
	}
	if uerr := applyEventSchedule(newEvent("FREQ=WEEKLY"), map[string]any{"recurrenceRules": []any{map[string]any{"frequency": "weekly"}, map[string]any{"frequency": "daily"}}}); uerr == nil {
		t.Error("two rules accepted")
	}
	cleared := newEvent("FREQ=WEEKLY")
	if uerr := applyEventSchedule(cleared, map[string]any{"recurrenceRules": nil}); uerr != nil || cleared.Props.Get(ical.PropRecurrenceRule) != nil {
		t.Errorf("rule not cleared: %v", uerr)
	}
}

func TestJMAPStartAndAllDayDurationValidation(t *testing.T) {
	fresh := func() *ical.Component { return ical.NewEvent().Component }
	for _, patch := range []map[string]any{
		{"start": ""},
		{"start": nil},
		{"title": "no start"},
		{"start": "2026-10-19", "showWithoutTime": true, "duration": "PT12H"},
		{"start": "2026-10-19", "showWithoutTime": true, "duration": "P1DT1H"},
	} {
		if uerr := applyEventSchedule(fresh(), patch); uerr == nil || uerr["type"] != "invalidProperties" {
			t.Errorf("%v accepted: %v", patch, uerr)
		}
	}
	comp := fresh()
	if uerr := applyEventSchedule(comp, map[string]any{"start": "2026-10-19", "showWithoutTime": true, "duration": "P2D"}); uerr != nil {
		t.Fatal(uerr)
	}
	if got := comp.Props.Get(ical.PropDateTimeEnd).Value; got != "20261021" {
		t.Errorf("DTEND %q", got)
	}
	if uerr := applyEventSchedule(comp, map[string]any{"title": "renamed"}); uerr != nil {
		t.Errorf("update without start rejected: %v", uerr)
	}
}
