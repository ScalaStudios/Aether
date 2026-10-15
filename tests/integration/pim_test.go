//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

func TestContactsDirectoryAndCalendarAPI(t *testing.T) {
	w := setupWorld(t)
	bob := "bob@" + w.domain

	var created struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Emails []string `json:"emails"`
	}
	if got := w.alice.do("POST", "/contacts", map[string]any{"name": "", "emails": []string{"x@y.z"}}, nil); got != 422 {
		t.Fatalf("nameless contact returned %d", got)
	}
	w.alice.must("POST", "/contacts", map[string]any{"name": "Ada Lovelace", "emails": []string{"Ada@Example.org"}, "organization": "Analytical Engines"}, &created, 201)
	if len(created.Emails) != 1 || created.Emails[0] != "ada@example.org" {
		t.Fatalf("created contact: %+v", created)
	}
	w.alice.must("PUT", "/contacts/"+created.ID, map[string]any{"name": "Ada King", "emails": []string{"ada@example.org"}}, &created, 200)

	httpClient := webdav.HTTPClientWithBasicAuth(http.DefaultClient, "alice@"+w.domain, w.aliceAppPassword)
	cards, _ := carddav.NewClient(httpClient, env(t, "AETHER_TEST_API", "http://api:8080")+"/.well-known/carddav")
	ctx := context.Background()
	principal, _ := cards.FindCurrentUserPrincipal(ctx)
	home, _ := cards.FindAddressBookHomeSet(ctx, principal)
	books, _ := cards.FindAddressBooks(ctx, home)
	if len(books) == 0 {
		t.Fatal("no address book over CardDAV")
	}
	obj, err := cards.GetAddressObject(ctx, books[0].Path+created.ID)
	if err != nil || obj.Card.PreferredValue(vcard.FieldFormattedName) != "Ada King" {
		t.Fatalf("API contact not visible over CardDAV: %v %v", obj, err)
	}

	var suggestions []struct {
		Address string `json:"address"`
		Source  string `json:"source"`
	}
	w.alice.must("GET", "/recipients?q=ada", nil, &suggestions, 200)
	if len(suggestions) == 0 || suggestions[0].Address != "ada@example.org" || suggestions[0].Source != "contact" {
		t.Fatalf("contact suggestion: %+v", suggestions)
	}
	w.alice.must("GET", "/recipients?q=bob", nil, &suggestions, 200)
	if len(suggestions) == 0 || suggestions[0].Address != bob || suggestions[0].Source != "directory" {
		t.Fatalf("directory suggestion: %+v", suggestions)
	}
	w.alice.must("DELETE", "/contacts/"+created.ID, nil, nil, 204)

	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1).Add(15 * time.Hour)
	var event struct {
		ID     string `json:"id"`
		Repeat string `json:"repeat"`
	}
	w.alice.must("POST", "/calendar/events", map[string]any{"title": "Standup", "start": start, "end": start.Add(15 * time.Minute), "repeat": "weekly"}, &event, 201)
	var occurrences []struct {
		ID          string    `json:"id"`
		Title       string    `json:"title"`
		Start       time.Time `json:"start"`
		SeriesStart time.Time `json:"seriesStart"`
	}
	q := url.Values{"start": {start.Add(-time.Hour).Format(time.RFC3339)}, "end": {start.AddDate(0, 0, 20).Format(time.RFC3339)}}
	w.alice.must("GET", "/calendar/events?"+q.Encode(), nil, &occurrences, 200)
	count := 0
	for _, o := range occurrences {
		if o.ID == event.ID {
			count++
			if !o.SeriesStart.Equal(start) {
				t.Fatalf("occurrence on %s reports series start %s, want %s", o.Start, o.SeriesStart, start)
			}
		}
	}
	if count != 3 {
		t.Fatalf("weekly event over 20 days expanded to %d occurrences, want 3: %+v", count, occurrences)
	}
	w.alice.must("PUT", "/calendar/events/"+event.ID, map[string]any{"title": "Standup (moved)", "start": start.Add(time.Hour), "end": start.Add(75 * time.Minute)}, nil, 200)
	w.alice.must("GET", "/calendar/events?"+q.Encode(), nil, &occurrences, 200)
	if len(occurrences) != 1 || occurrences[0].Title != "Standup (moved)" {
		t.Fatalf("edited event: %+v", occurrences)
	}
	if got := w.bob.do("DELETE", "/calendar/events/"+event.ID, nil, nil); got != 404 {
		t.Fatalf("bob deleted alice's event: %d", got)
	}
	w.alice.must("DELETE", "/calendar/events/"+event.ID, nil, nil, 204)
}
