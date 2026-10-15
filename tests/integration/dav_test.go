//go:build integration

package integration

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

func TestCalDAVAndCardDAV(t *testing.T) {
	w := setupWorld(t)
	ctx := context.Background()
	base := env(t, "AETHER_TEST_API", "http://api:8080")
	alice := "alice@" + w.domain
	httpClient := webdav.HTTPClientWithBasicAuth(http.DefaultClient, alice, w.aliceAppPassword)

	cal, err := caldav.NewClient(httpClient, base+"/.well-known/caldav")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cal.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("principal discovery: %v", err)
	}
	home, err := cal.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("calendar home: %v", err)
	}
	calendars, err := cal.FindCalendars(ctx, home)
	if err != nil || len(calendars) == 0 {
		t.Fatalf("calendars: %v %v", calendars, err)
	}

	mkcalendar, _ := http.NewRequest("MKCALENDAR", base+home+"from-app/", strings.NewReader(`<?xml version="1.0"?><C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set><D:prop><D:displayname>Work &amp; travel</D:displayname></D:prop></D:set></C:mkcalendar>`))
	mkcalendar.SetBasicAuth(alice, w.aliceAppPassword)
	mkcalendar.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(mkcalendar)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCALENDAR as calendar apps send it: %v %v", resp, err)
	}
	resp.Body.Close()
	if after, _ := cal.FindCalendars(ctx, home); len(after) != len(calendars)+1 || !slices.ContainsFunc(after, func(c caldav.Calendar) bool { return c.Name == "Work & travel" }) {
		t.Fatalf("calendar created with MKCALENDAR is missing: %+v", after)
	}

	start := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Hour)
	uid := "event-" + randomSuffix()
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, uid)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	event.Props.SetDateTime(ical.PropDateTimeStart, start)
	event.Props.SetDateTime(ical.PropDateTimeEnd, start.Add(time.Hour))
	event.Props.SetText(ical.PropSummary, "Design review")
	doc := ical.NewCalendar()
	doc.Props.SetText(ical.PropVersion, "2.0")
	doc.Props.SetText(ical.PropProductID, "-//Aether//integration//EN")
	doc.Children = append(doc.Children, event.Component)
	objectPath := calendars[0].Path + uid + ".ics"
	if _, err := cal.PutCalendarObject(ctx, objectPath, doc); err != nil {
		t.Fatalf("put event: %v", err)
	}
	found, err := cal.QueryCalendar(ctx, calendars[0].Path, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", AllProps: true}}},
		CompFilter:  caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT", Start: start.Add(-time.Hour), End: start.Add(2 * time.Hour)}}},
	})
	if err != nil || len(found) != 1 {
		t.Fatalf("time-range query returned %d objects: %v", len(found), err)
	}
	if summary, _ := found[0].Data.Children[0].Props.Text(ical.PropSummary); summary != "Design review" {
		t.Fatalf("event round trip lost the summary: %q", summary)
	}
	empty, _ := cal.QueryCalendar(ctx, calendars[0].Path, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR"},
		CompFilter:  caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT", Start: start.Add(48 * time.Hour), End: start.Add(72 * time.Hour)}}},
	})
	if len(empty) != 0 {
		t.Fatalf("time-range query outside the event returned %d objects", len(empty))
	}

	cards, err := carddav.NewClient(httpClient, base+"/.well-known/carddav")
	if err != nil {
		t.Fatal(err)
	}
	cardHome, err := cards.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("address book home: %v", err)
	}
	books, err := cards.FindAddressBooks(ctx, cardHome)
	if err != nil || len(books) == 0 {
		t.Fatalf("address books: %v %v", books, err)
	}
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, "contact-"+randomSuffix())
	card.SetValue(vcard.FieldFormattedName, "Grace Hopper")
	card.SetValue(vcard.FieldEmail, "grace@example.org")
	cardPath := books[0].Path + card.Value(vcard.FieldUID) + ".vcf"
	if _, err := cards.PutAddressObject(ctx, cardPath, card); err != nil {
		t.Fatalf("put contact: %v", err)
	}
	matches, err := cards.QueryAddressBook(ctx, books[0].Path, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{AllProp: true},
		PropFilters: []carddav.PropFilter{{Name: vcard.FieldEmail, TextMatches: []carddav.TextMatch{{Text: "grace@"}}}},
	})
	if err != nil || len(matches) != 1 || matches[0].Card.PreferredValue(vcard.FieldFormattedName) != "Grace Hopper" {
		t.Fatalf("contact query: %v %v", matches, err)
	}

	dav, err := webdav.NewClient(httpClient, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := dav.RemoveAll(ctx, objectPath); err != nil {
		t.Fatalf("delete event: %v", err)
	}
	if err := dav.RemoveAll(ctx, cardPath); err != nil {
		t.Fatalf("delete contact: %v", err)
	}
	if _, err := cal.GetCalendarObject(ctx, objectPath); err == nil {
		t.Fatal("deleted event is still served")
	}

	wrong := webdav.HTTPClientWithBasicAuth(http.DefaultClient, alice, "not-the-password")
	req, _ := http.NewRequest("PROPFIND", base+principal, nil)
	if resp, err := wrong.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %v %v", resp, err)
	}
	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	bobClient := webdav.HTTPClientWithBasicAuth(http.DefaultClient, "bob@"+w.domain, bobPass)
	for _, p := range []string{principal, strings.TrimSuffix(calendars[0].Path, "/") + "/", objectPath} {
		req, _ := http.NewRequest("PROPFIND", base+p, nil)
		req.Header.Set("Depth", "1")
		if resp, err := bobClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("bob reading %s: %v %v", p, resp, err)
		}
	}
}
