package api

import (
	"testing"
	"time"
)

func TestBookingSlots(t *testing.T) {
	loc := time.UTC
	monday := time.Date(2026, 11, 23, 0, 0, 0, 0, loc)
	windows := []availabilityWindow{{Weekday: int(time.Monday), StartMinute: 9 * 60, EndMinute: 10 * 60}}
	now := monday.Add(-24 * time.Hour)
	from := monday
	to := monday.Add(24 * time.Hour)
	slots := bookingSlots(now, from, to, loc, 30, 0, 0, 0, 60, windows, nil)
	want := []time.Time{
		monday.Add(9 * time.Hour),
		monday.Add(9*time.Hour + 15*time.Minute),
		monday.Add(9*time.Hour + 30*time.Minute),
	}
	if len(slots) != len(want) {
		t.Fatalf("slots = %v, want %v", slots, want)
	}
	for i := range want {
		if !slots[i].Equal(want[i]) {
			t.Fatalf("slot %d = %s, want %s", i, slots[i], want[i])
		}
	}

	busy := []timeInterval{{Start: monday.Add(9 * time.Hour), End: monday.Add(9*time.Hour + 30*time.Minute)}}
	slots = bookingSlots(now, from, to, loc, 30, 0, 0, 0, 60, windows, busy)
	if len(slots) != 1 || !slots[0].Equal(monday.Add(9*time.Hour+30*time.Minute)) {
		t.Fatalf("busy slots = %v", slots)
	}

	slots = bookingSlots(now, from, to, loc, 30, 15, 0, 0, 60, windows, busy)
	if len(slots) != 0 {
		t.Fatalf("buffer before should clear the hour, got %v", slots)
	}

	slots = bookingSlots(monday.Add(9*time.Hour+20*time.Minute), from, to, loc, 30, 0, 0, 0, 60, windows, nil)
	if len(slots) != 1 || !slots[0].Equal(monday.Add(9*time.Hour+30*time.Minute)) {
		t.Fatalf("past slots = %v", slots)
	}

	slots = bookingSlots(now, from, to, loc, 30, 0, 0, 0, 1, windows, nil)
	if len(slots) != 0 {
		t.Fatalf("horizon should hide next week's monday when now is the day before and horizon is 1 day, got %v", slots)
	}
}

func TestBookingSlotsTimezone(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 23, 0, 0, 0, 0, loc)
	windows := []availabilityWindow{{Weekday: int(time.Monday), StartMinute: 9 * 60, EndMinute: 9*60 + 30}}
	slots := bookingSlots(day.Add(-time.Hour), day, day.Add(24*time.Hour), loc, 30, 0, 0, 0, 30, windows, nil)
	if len(slots) != 1 {
		t.Fatalf("slots = %v", slots)
	}
	wall := slots[0].In(loc)
	if wall.Hour() != 9 || wall.Minute() != 0 || wall.Day() != 23 {
		t.Fatalf("wall time = %s", wall)
	}
	if !slots[0].Equal(time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("utc = %s", slots[0])
	}
}

func TestSlotStep(t *testing.T) {
	if slotStep(30) != 15 || slotStep(25) != 5 || slotStep(7) != 7 || slotStep(10) != 5 {
		t.Fatalf("steps = %d %d %d %d", slotStep(30), slotStep(25), slotStep(7), slotStep(10))
	}
}

func TestNormalizeWindows(t *testing.T) {
	got, msg := normalizeWindows([]bookingWindow{
		{Weekday: 3, StartMinute: 600, EndMinute: 720},
		{Weekday: 1, StartMinute: 540, EndMinute: 600},
		{Weekday: 1, StartMinute: 600, EndMinute: 660},
	}, 30)
	if msg != "" || len(got) != 3 || got[0].Weekday != 1 || got[0].StartMinute != 540 || got[2].Weekday != 3 {
		t.Fatalf("sorted = %+v msg=%q", got, msg)
	}
	if _, msg := normalizeWindows([]bookingWindow{
		{Weekday: 1, StartMinute: 540, EndMinute: 630},
		{Weekday: 1, StartMinute: 600, EndMinute: 660},
	}, 30); msg == "" {
		t.Fatal("overlapping windows should be refused")
	}
	if _, msg := normalizeWindows([]bookingWindow{{Weekday: 1, StartMinute: 540, EndMinute: 555}}, 30); msg == "" {
		t.Fatal("too-short window should be refused")
	}
	if _, msg := normalizeWindows(nil, 30); msg == "" {
		t.Fatal("empty windows should be refused")
	}
}

func TestBookingValidationHelpers(t *testing.T) {
	if !validVideoURL("") || !validVideoURL("https://meet.example/room") {
		t.Fatal("valid video URLs refused")
	}
	for _, bad := range []string{"http://meet.example/room", "https://u:p@meet.example/r", "javascript:alert(1)"} {
		if validVideoURL(bad) {
			t.Fatalf("%s should be refused", bad)
		}
	}
	if _, ok := validTimezone("Europe/London"); !ok {
		t.Fatal("Europe/London should be valid")
	}
	for _, bad := range []string{"", "Local", "../UTC", "Mars/Olympus"} {
		if _, ok := validTimezone(bad); ok {
			t.Fatalf("%s should be invalid", bad)
		}
	}
	if slugify("30-Minute Intro!") != "30-minute-intro" || slugify("!!!") != "meet" {
		t.Fatalf("slugify unexpected: %q %q", slugify("30-Minute Intro!"), slugify("!!!"))
	}
	ev := calendarEvent{Attendees: []eventAttendee{{Address: "host@example.com", Status: "declined"}}}
	if !declinedBy(ev, map[string]bool{"host@example.com": true}) {
		t.Fatal("declinedBy should match host address")
	}
	if declinedBy(ev, map[string]bool{"other@example.com": true}) {
		t.Fatal("declinedBy should not match other address")
	}
}
