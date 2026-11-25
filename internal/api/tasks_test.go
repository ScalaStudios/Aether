package api

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func TestTaskRoundTrip(t *testing.T) {
	due := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	comp := taskComponent("uid-1", "Write the notes", "Bring the draft", "in-process", 1, 2, &due, nil)
	got := taskFromComponent("uid-1.ics", comp)
	if got.Title != "Write the notes" || got.Notes != "Bring the draft" || got.Status != "in-process" || got.Priority != 1 || got.Due == nil || !got.Due.Equal(due) || got.CompletedAt != nil {
		t.Fatalf("task = %+v", got)
	}
	done := due.Add(time.Hour)
	comp = taskComponent("uid-1", "Write the notes", "", "completed", 0, 3, &due, &done)
	got = taskFromComponent("uid-1.ics", comp)
	if got.Status != "completed" || got.CompletedAt == nil || !got.CompletedAt.Equal(done) || got.Priority != 0 {
		t.Fatalf("completed task = %+v", got)
	}
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Scala Studios//Aether//EN")
	cal.Children = append(cal.Children, comp)
	var buf strings.Builder
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		t.Fatal(err)
	}
	parsed, ok := taskFromCalendar("uid-1.ics", buf.String())
	if !ok || parsed.Status != "completed" || parsed.Title != "Write the notes" {
		t.Fatalf("parsed = %+v ok=%v", parsed, ok)
	}
}

func TestSortTasks(t *testing.T) {
	soon := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	later := soon.Add(24 * time.Hour)
	items := []taskItem{
		{Title: "zeta", Status: "needs-action"},
		{Title: "done", Status: "completed", Due: &soon},
		{Title: "beta", Status: "in-process", Due: &later},
		{Title: "alpha", Status: "needs-action", Due: &soon},
	}
	sortTasks(items)
	var titles []string
	for _, item := range items {
		titles = append(titles, item.Title)
	}
	want := "alpha beta zeta done"
	if strings.Join(titles, " ") != want {
		t.Fatalf("order = %s, want %s", strings.Join(titles, " "), want)
	}
}
