//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

type calendarView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Access string `json:"access"`
	Owner  *struct {
		Email string `json:"email"`
	} `json:"owner"`
	Shares []struct {
		ID     string `json:"id"`
		Access string `json:"access"`
	} `json:"shares"`
}

type sharedEventView struct {
	Title      string `json:"title"`
	CalendarID string `json:"calendarId"`
	ReadOnly   bool   `json:"readOnly"`
}

func TestCalendarSharing(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	start := time.Now().Add(96 * time.Hour).Truncate(time.Hour).UTC()
	rangeQuery := "/calendar/events?start=" + start.Add(-time.Hour).Format(time.RFC3339) + "&end=" + start.Add(4*time.Hour).Format(time.RFC3339)
	event := func(title, calendar string) map[string]any {
		return map[string]any{"title": title, "start": start, "end": start.Add(time.Hour), "calendarId": calendar}
	}
	sharedWith := func(c *apiClient, name string) *calendarView {
		var list []calendarView
		c.must("GET", "/calendar/calendars", nil, &list, 200)
		for i := range list {
			if list[i].Name == name {
				return &list[i]
			}
		}
		return nil
	}

	var team calendarView
	w.alice.must("POST", "/calendar/calendars", map[string]string{"name": "Team " + randomSuffix(), "color": "#3b82f6"}, &team, 201)
	w.alice.must("POST", "/calendar/events", event("Team sync", team.ID), nil, 201)

	if sharedWith(w.bob, team.Name) != nil {
		t.Fatal("bob sees the calendar before it is shared")
	}
	if code := w.alice.do("POST", "/calendar/calendars/"+team.ID+"/shares", map[string]string{"email": "nobody@example.com", "access": "read"}, nil); code != 422 {
		t.Fatalf("sharing with an unknown address should fail, got %d", code)
	}
	if code := w.alice.do("POST", "/calendar/calendars/"+team.ID+"/shares", map[string]string{"orgId": w.orgID, "access": "read"}, nil); code != 403 {
		t.Fatalf("a member sharing with the whole organization should be refused, got %d", code)
	}
	w.alice.must("POST", "/calendar/calendars/"+team.ID+"/shares", map[string]string{"email": bob, "access": "read"}, nil, 204)

	shared := sharedWith(w.bob, team.Name)
	if shared == nil || shared.Access != "read" || shared.Owner == nil || shared.Owner.Email != alice || !strings.HasPrefix(shared.ID, "shared-") {
		t.Fatalf("shared calendar not listed for bob: %+v", shared)
	}
	var bobEvents []sharedEventView
	w.bob.must("GET", rangeQuery, nil, &bobEvents, 200)
	if len(bobEvents) != 1 || bobEvents[0].Title != "Team sync" || bobEvents[0].CalendarID != shared.ID || !bobEvents[0].ReadOnly {
		t.Fatalf("bob does not see the shared event as read-only: %+v", bobEvents)
	}
	if code := w.bob.do("POST", "/calendar/events", event("Bob's idea", shared.ID), nil); code != 403 {
		t.Fatalf("writing to a read-only share should be refused, got %d", code)
	}

	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	ctx := context.Background()
	dav, err := caldav.NewClient(webdav.HTTPClientWithBasicAuth(http.DefaultClient, bob, bobPass), env(t, "AETHER_TEST_API", "http://api:8080")+"/.well-known/caldav")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := dav.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home, err := dav.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	davCalendars, err := dav.FindCalendars(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	var davPath string
	for _, c := range davCalendars {
		if strings.HasSuffix(c.Path, "/"+shared.ID+"/") {
			davPath = c.Path
		}
	}
	if davPath == "" {
		t.Fatalf("shared calendar missing from CalDAV: %+v", davCalendars)
	}
	doc := ical.NewCalendar()
	doc.Props.SetText(ical.PropVersion, "2.0")
	doc.Props.SetText(ical.PropProductID, "-//Aether//integration//EN")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, "dav-"+randomSuffix())
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	ev.Props.SetDateTime(ical.PropDateTimeStart, start)
	ev.Props.SetDateTime(ical.PropDateTimeEnd, start.Add(time.Hour))
	ev.Props.SetText(ical.PropSummary, "From a calendar app")
	doc.Children = append(doc.Children, ev.Component)
	if _, err := dav.PutCalendarObject(ctx, davPath+"from-app.ics", doc); err == nil {
		t.Fatal("CalDAV write to a read-only share should fail")
	}

	w.alice.must("POST", "/calendar/calendars/"+team.ID+"/shares", map[string]string{"email": bob, "access": "write"}, nil, 204)
	if _, err := dav.PutCalendarObject(ctx, davPath+"from-app.ics", doc); err != nil {
		t.Fatalf("CalDAV write with edit access: %v", err)
	}
	w.bob.must("POST", "/calendar/events", event("Bob's idea", shared.ID), nil, 201)
	var aliceEvents []sharedEventView
	w.alice.must("GET", rangeQuery, nil, &aliceEvents, 200)
	if len(aliceEvents) != 3 {
		t.Fatalf("alice should see her event and both of bob's: %+v", aliceEvents)
	}

	var orgWide calendarView
	w.admin.must("POST", "/calendar/calendars", map[string]string{"name": "Holidays " + randomSuffix()}, &orgWide, 201)
	w.admin.must("POST", "/calendar/calendars/"+orgWide.ID+"/shares", map[string]string{"orgId": w.orgID, "access": "read"}, nil, 204)
	if c := sharedWith(w.bob, orgWide.Name); c == nil || c.Access != "read" {
		t.Fatalf("organization calendar not visible to members: %+v", c)
	}

	owned := sharedWith(w.alice, team.Name)
	if owned == nil || len(owned.Shares) != 1 || owned.Shares[0].Access != "write" {
		t.Fatalf("owner should see the share: %+v", owned)
	}
	w.alice.must("DELETE", "/calendar/calendars/"+team.ID+"/shares/"+owned.Shares[0].ID, nil, nil, 204)
	if sharedWith(w.bob, team.Name) != nil {
		t.Fatal("bob still sees the calendar after it was unshared")
	}
	if code := w.alice.do("DELETE", "/calendar/calendars/personal", nil, nil); code != 422 {
		t.Fatalf("deleting the main calendar should be refused, got %d", code)
	}
}
