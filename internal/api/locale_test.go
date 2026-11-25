package api

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func TestDescribeEventLocalized(t *testing.T) {
	comp := ical.NewComponent(ical.CompEvent)
	comp.Props.SetText(ical.PropSummary, "Team Sync")
	comp.Props.SetDateTime(ical.PropDateTimeStart, time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC))
	comp.Props.SetDateTime(ical.PropDateTimeEnd, time.Date(2026, 6, 15, 15, 0, 0, 0, time.UTC))
	comp.Props.SetText(ical.PropLocation, "Room A")
	comp.Props.SetText(ical.PropDescription, "Weekly sync agenda")
	comp.Props.Set(organizerProp(mailpkg.Address{Name: "Alice", Address: "alice@example.com"}))

	cases := []struct {
		locale string
		wants  []string
	}{
		{
			locale: "en",
			wants:  []string{"Team Sync", "When: Monday, 15 June 2026 14:00 – 15:00 UTC", "Where: Room A", "Organizer: Alice", "Weekly sync agenda"},
		},
		{
			locale: "pt",
			wants:  []string{"Team Sync", "Quando: segunda-feira, 15 de junho de 2026, 14:00 – 15:00 UTC", "Onde: Room A", "Organizador: Alice", "Weekly sync agenda"},
		},
		{
			locale: "es",
			wants:  []string{"Team Sync", "Cuándo: lunes, 15 de junio de 2026, 14:00 – 15:00 UTC", "Dónde: Room A", "Organizador: Alice", "Weekly sync agenda"},
		},
		{
			locale: "fr",
			wants:  []string{"Team Sync", "When: Monday, 15 June 2026 14:00 – 15:00 UTC", "Where: Room A", "Organizer: Alice"},
		},
	}

	for _, tc := range cases {
		got := describeEvent(comp, tc.locale)
		for _, want := range tc.wants {
			if !strings.Contains(got, want) {
				t.Errorf("describeEvent(%q) = %q, want substring %q", tc.locale, got, want)
			}
		}
	}
}

func TestDescribeEventAllDayLocalized(t *testing.T) {
	comp := ical.NewComponent(ical.CompEvent)
	comp.Props.SetText(ical.PropSummary, "Offsite")
	comp.Props.SetDate(ical.PropDateTimeStart, time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC))
	comp.Props.SetDate(ical.PropDateTimeEnd, time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC))

	if got := describeEvent(comp, "pt"); !strings.Contains(got, "Quando: segunda-feira, 15 de junho de 2026") {
		t.Errorf("describeEvent(all-day, pt) = %q", got)
	}
	if got := describeEvent(comp, "es"); !strings.Contains(got, "Cuándo: lunes, 15 de junio de 2026") {
		t.Errorf("describeEvent(all-day, es) = %q", got)
	}
}
