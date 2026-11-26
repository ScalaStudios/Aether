//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

const (
	jmapCore = "urn:ietf:params:jmap:core"
	jmapMail = "urn:ietf:params:jmap:mail"
	jmapCal  = "urn:ietf:params:jmap:calendars"
	jmapCard = "urn:ietf:params:jmap:contacts"
)

func TestJMAP(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	bob := "bob@" + w.domain
	origin := env(t, "AETHER_TEST_API", "http://api:8080")
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(alice+":"+w.aliceAppPassword))
	using := []string{jmapCore, jmapMail, jmapCal, jmapCard}

	if status, body := jmapRaw(t, origin+"/.well-known/jmap", http.MethodGet, "", nil); status != http.StatusUnauthorized || !bytes.Contains(body, []byte("urn:ietf:params:jmap:error:unauthorized")) {
		t.Fatalf("anonymous session: %d %s", status, body)
	}
	web := "Basic " + base64.StdEncoding.EncodeToString([]byte(alice+":alice-integration-pw"))
	if status, _ := jmapRaw(t, origin+"/.well-known/jmap", http.MethodGet, web, nil); status != http.StatusUnauthorized {
		t.Fatalf("web password status %d", status)
	}
	cookieReq, _ := http.NewRequest(http.MethodGet, origin+"/.well-known/jmap", nil)
	cookieResp, err := w.alice.http.Do(cookieReq)
	if err != nil || cookieResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session cookie: %v %v", err, cookieResp)
	}
	cookieResp.Body.Close()

	opt, _ := http.NewRequest(http.MethodOptions, origin+"/jmap", nil)
	optResp, err := http.DefaultClient.Do(opt)
	if err != nil || optResp.StatusCode != http.StatusNoContent || optResp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("options: %v %v", err, optResp)
	}
	optResp.Body.Close()

	status, body := jmapRaw(t, origin+"/.well-known/jmap", http.MethodGet, auth, nil)
	if status != http.StatusOK {
		t.Fatalf("session: %d %s", status, body)
	}
	var session map[string]any
	json.Unmarshal(body, &session)
	caps, _ := session["capabilities"].(map[string]any)
	if caps[jmapCore] == nil || caps[jmapMail] == nil || caps[jmapCal] == nil || caps[jmapCard] == nil {
		t.Fatalf("capabilities: %v", caps)
	}
	primary, _ := session["primaryAccounts"].(map[string]any)
	if session["username"] != alice || primary[jmapMail] != w.aliceMailbox || primary[jmapCal] != w.aliceMailbox {
		t.Fatalf("session identity: username=%v primary=%v", session["username"], primary)
	}
	account := w.aliceMailbox

	if name, result := jmapMethod(t, origin, auth, using, "Mailbox/get", map[string]any{"accountId": "00000000-0000-4000-8000-000000000000"}); name != "error" || result["type"] != "accountNotFound" {
		t.Fatalf("unknown account: %s %v", name, result)
	}

	_, boxes := jmapMethod(t, origin, auth, using, "Mailbox/get", map[string]any{"accountId": account})
	var inbox string
	for _, raw := range boxes["list"].([]any) {
		item := raw.(map[string]any)
		if item["role"] == "inbox" {
			inbox = item["id"].(string)
		}
	}
	if inbox == "" {
		t.Fatalf("no inbox: %v", boxes["list"])
	}
	_, createdBox := jmapMethod(t, origin, auth, using, "Mailbox/set", map[string]any{"accountId": account, "create": map[string]any{"a": map[string]any{"name": "JMAP " + randomSuffix()}}})
	archive := createdBox["created"].(map[string]any)["a"].(map[string]any)["id"].(string)

	subject := "jmap " + randomSuffix()
	_, createdMail := jmapMethod(t, origin, auth, using, "Email/set", map[string]any{
		"accountId": account,
		"create": map[string]any{"m": map[string]any{
			"mailboxIds": map[string]bool{inbox: true},
			"from":       []any{map[string]string{"email": alice}},
			"to":         []any{map[string]string{"email": bob}},
			"subject":    subject,
			"textBody":   []any{map[string]string{"partId": "t"}},
			"bodyValues": map[string]any{"t": map[string]string{"value": "hello from jmap"}},
			"keywords":   map[string]bool{"$draft": true},
		}},
	})
	if createdMail["notCreated"] != nil && len(createdMail["notCreated"].(map[string]any)) > 0 {
		t.Fatalf("create: %v", createdMail["notCreated"])
	}
	emailID := createdMail["created"].(map[string]any)["m"].(map[string]any)["id"].(string)

	_, queried := jmapMethod(t, origin, auth, using, "Email/query", map[string]any{"accountId": account, "filter": map[string]any{"inMailbox": inbox, "subject": subject}})
	ids := queried["ids"].([]any)
	if len(ids) != 1 || ids[0] != emailID {
		t.Fatalf("query: %v", ids)
	}
	_, got := jmapMethod(t, origin, auth, using, "Email/get", map[string]any{"accountId": account, "ids": []string{emailID}, "properties": []string{"subject", "keywords", "mailboxIds"}, "fetchAllBodyValues": true})
	msg := got["list"].([]any)[0].(map[string]any)
	if msg["subject"] != subject || msg["keywords"].(map[string]any)["$draft"] != true {
		t.Fatalf("get: %v", msg)
	}

	_, updated := jmapMethod(t, origin, auth, using, "Email/set", map[string]any{"accountId": account, "update": map[string]any{emailID: map[string]any{"keywords/$seen": true, "mailboxIds/" + inbox: nil, "mailboxIds/" + archive: true}}})
	if updated["notUpdated"] != nil && len(updated["notUpdated"].(map[string]any)) > 0 {
		t.Fatalf("update: %v", updated["notUpdated"])
	}
	_, moved := jmapMethod(t, origin, auth, using, "Email/get", map[string]any{"accountId": account, "ids": []string{emailID}, "properties": []string{"mailboxIds", "keywords"}})
	movedMsg := moved["list"].([]any)[0].(map[string]any)
	boxesNow := movedMsg["mailboxIds"].(map[string]any)
	if boxesNow[archive] != true || boxesNow[inbox] != nil || movedMsg["keywords"].(map[string]any)["$seen"] != true || movedMsg["id"] != emailID {
		t.Fatalf("moved: %v", movedMsg)
	}

	sendSubject := "jmap send " + randomSuffix()
	_, draft := jmapMethod(t, origin, auth, using, "Email/set", map[string]any{
		"accountId": account,
		"create": map[string]any{"s": map[string]any{
			"mailboxIds": map[string]bool{inbox: true},
			"to":         []any{map[string]string{"email": bob}},
			"subject":    sendSubject,
			"textBody":   []any{map[string]string{"partId": "t"}},
			"bodyValues": map[string]any{"t": map[string]string{"value": "delivered by jmap"}},
		}},
	})
	sendID := draft["created"].(map[string]any)["s"].(map[string]any)["id"].(string)
	_, submitted := jmapMethod(t, origin, auth, using, "EmailSubmission/set", map[string]any{"accountId": account, "create": map[string]any{"s": map[string]any{"emailId": sendID, "identityId": account}}})
	if submitted["notCreated"] != nil && len(submitted["notCreated"].(map[string]any)) > 0 {
		t.Fatalf("submit: %v", submitted["notCreated"])
	}
	waitFor(t, "bob to receive the JMAP message", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", sendSubject) != 0 })

	_, spoofed := jmapMethod(t, origin, auth, using, "Email/set", map[string]any{
		"accountId": account,
		"create": map[string]any{"bad": map[string]any{
			"mailboxIds": map[string]bool{inbox: true},
			"from":       []any{map[string]string{"email": "other@example.com"}},
			"subject":    "spoof",
			"textBody":   []any{map[string]string{"partId": "t"}},
			"bodyValues": map[string]any{"t": map[string]string{"value": "no"}},
		}},
	})
	if spoofed["notCreated"].(map[string]any)["bad"].(map[string]any)["type"] != "forbiddenFrom" {
		t.Fatalf("spoof: %v", spoofed)
	}

	ctx := context.Background()
	httpClient := webdav.HTTPClientWithBasicAuth(http.DefaultClient, alice, w.aliceAppPassword)
	cal, err := caldav.NewClient(httpClient, origin+"/.well-known/caldav")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cal.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home, err := cal.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	_, cals := jmapMethod(t, origin, auth, using, "Calendar/get", map[string]any{"accountId": account})
	var personal string
	for _, raw := range cals["list"].([]any) {
		item := raw.(map[string]any)
		if item["name"] == "Personal" || personal == "" {
			personal = item["id"].(string)
		}
	}
	if personal == "" {
		t.Fatal("no calendar")
	}
	title := "JMAP review " + randomSuffix()
	_, ev := jmapMethod(t, origin, auth, using, "CalendarEvent/set", map[string]any{"accountId": account, "create": map[string]any{"e": map[string]any{
		"title": title, "start": "2026-12-01T15:00:00", "duration": "PT1H", "calendarIds": map[string]bool{personal: true},
	}}})
	if ev["notCreated"] != nil && len(ev["notCreated"].(map[string]any)) > 0 {
		t.Fatalf("event: %v", ev["notCreated"])
	}
	eventID := ev["created"].(map[string]any)["e"].(map[string]any)["id"].(string)
	_, foundEvents := jmapMethod(t, origin, auth, using, "CalendarEvent/query", map[string]any{"accountId": account, "filter": map[string]any{"text": title}})
	if len(foundEvents["ids"].([]any)) != 1 || foundEvents["ids"].([]any)[0] != eventID {
		t.Fatalf("event query: %v", foundEvents)
	}
	start := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	davCals, err := cal.FindCalendars(ctx, home)
	if err != nil || len(davCals) == 0 {
		t.Fatalf("calendars: %v %v", davCals, err)
	}
	objects, err := cal.QueryCalendar(ctx, davCals[0].Path, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", AllProps: true}}},
		CompFilter:  caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT", Start: start, End: start.Add(48 * time.Hour)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, obj := range objects {
		for _, child := range obj.Data.Children {
			if summary, _ := child.Props.Text(ical.PropSummary); summary == title {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("CalDAV did not return the JMAP event")
	}
	_, _ = jmapMethod(t, origin, auth, using, "CalendarEvent/set", map[string]any{"accountId": account, "destroy": []string{eventID}})

	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	weeklyTitle := "JMAP weekly " + randomSuffix()
	_, weekly := jmapMethod(t, origin, auth, using, "CalendarEvent/set", map[string]any{"accountId": account, "create": map[string]any{"w": map[string]any{
		"title": weeklyTitle, "start": "2026-10-19T09:00:00", "timeZone": "Europe/Amsterdam", "duration": "PT30M", "calendarIds": map[string]bool{personal: true},
		"recurrenceRules": []any{map[string]any{"@type": "RecurrenceRule", "frequency": "weekly"}},
	}}})
	if weekly["notCreated"] != nil && len(weekly["notCreated"].(map[string]any)) > 0 {
		t.Fatalf("weekly event: %v", weekly["notCreated"])
	}
	weeklyID := weekly["created"].(map[string]any)["w"].(map[string]any)["id"].(string)
	weeklyRange := url.Values{"start": {"2026-10-18T00:00:00Z"}, "end": {"2026-11-10T00:00:00Z"}}
	checkWeekly := func(stage, title string) {
		var occurrences []struct {
			Title string    `json:"title"`
			Start time.Time `json:"start"`
		}
		w.alice.must("GET", "/calendar/events?"+weeklyRange.Encode(), nil, &occurrences, 200)
		count := 0
		for _, o := range occurrences {
			if o.Title != title {
				continue
			}
			count++
			if local := o.Start.In(amsterdam); local.Hour() != 9 || local.Minute() != 0 {
				t.Errorf("%s: occurrence at %s, want 09:00 local", stage, local)
			}
		}
		if count != 4 {
			t.Fatalf("%s: got %d occurrences, want 4", stage, count)
		}
	}
	checkWeekly("created", weeklyTitle)
	_, updatedWeekly := jmapMethod(t, origin, auth, using, "CalendarEvent/set", map[string]any{"accountId": account, "update": map[string]any{weeklyID: map[string]any{"title": weeklyTitle + " renamed"}}})
	if updatedWeekly["notUpdated"] != nil && len(updatedWeekly["notUpdated"].(map[string]any)) > 0 {
		t.Fatalf("weekly update: %v", updatedWeekly["notUpdated"])
	}
	checkWeekly("renamed", weeklyTitle+" renamed")
	_, gotWeekly := jmapMethod(t, origin, auth, using, "CalendarEvent/get", map[string]any{"accountId": account, "ids": []string{weeklyID}})
	if item := gotWeekly["list"].([]any)[0].(map[string]any); item["timeZone"] != "Europe/Amsterdam" || item["start"] != "2026-10-19T09:00:00" {
		t.Fatalf("weekly event read back as %v %v", item["start"], item["timeZone"])
	}
	_, _ = jmapMethod(t, origin, auth, using, "CalendarEvent/set", map[string]any{"accountId": account, "destroy": []string{weeklyID}})

	_, books := jmapMethod(t, origin, auth, using, "AddressBook/get", map[string]any{"accountId": account})
	if len(books["list"].([]any)) == 0 {
		t.Fatal("no address book")
	}
	name := "Ada " + randomSuffix()
	_, card := jmapMethod(t, origin, auth, using, "ContactCard/set", map[string]any{"accountId": account, "create": map[string]any{"c": map[string]any{
		"name": map[string]string{"full": name}, "emails": map[string]any{"1": map[string]string{"address": "ada@example.com"}},
	}}})
	if card["notCreated"] != nil && len(card["notCreated"].(map[string]any)) > 0 {
		t.Fatalf("card: %v", card["notCreated"])
	}
	cardID := card["created"].(map[string]any)["c"].(map[string]any)["id"].(string)
	_, cards := jmapMethod(t, origin, auth, using, "ContactCard/query", map[string]any{"accountId": account, "filter": map[string]any{"text": name}})
	if len(cards["ids"].([]any)) != 1 {
		t.Fatalf("card query: %v", cards)
	}
	_, _ = jmapMethod(t, origin, auth, using, "ContactCard/set", map[string]any{"accountId": account, "destroy": []string{cardID}})

	_, vac := jmapMethod(t, origin, auth, using, "VacationResponse/set", map[string]any{"accountId": account, "update": map[string]any{"singleton": map[string]any{"isEnabled": true, "subject": "Away", "textBody": "Back Monday."}}})
	if vac["notUpdated"] != nil && len(vac["notUpdated"].(map[string]any)) > 0 {
		t.Fatalf("vacation: %v", vac["notUpdated"])
	}
	var reply struct {
		Enabled bool   `json:"enabled"`
		Message string `json:"message"`
	}
	w.alice.must("GET", "/mailboxes/"+account+"/auto-reply", nil, &reply, 200)
	if !reply.Enabled || reply.Message != "Back Monday." {
		t.Fatalf("auto-reply: %+v", reply)
	}

	streamReq, _ := http.NewRequest(http.MethodGet, origin+"/jmap/eventsource?types=Email&closeafter=state", nil)
	streamReq.Header.Set("Authorization", auth)
	stream, err := (&http.Client{Timeout: 30 * time.Second}).Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	streamBody, _ := io.ReadAll(stream.Body)
	if stream.StatusCode != http.StatusOK || !bytes.Contains(streamBody, []byte("event: state")) {
		t.Fatalf("eventsource: %d %s", stream.StatusCode, streamBody)
	}

	f, _, _ := linkedIssuer(t, w, alice)
	bearer := "Bearer " + f.mint(f.subject, f.clientID)
	if status, body := jmapRaw(t, origin+"/.well-known/jmap", http.MethodGet, bearer, nil); status != http.StatusOK || !bytes.Contains(body, []byte(alice)) {
		t.Fatalf("bearer session: %d %s", status, body)
	}
	if status, _ := jmapRaw(t, origin+"/.well-known/jmap", http.MethodGet, "Bearer at-not-a-token", nil); status != http.StatusUnauthorized {
		t.Fatalf("bad bearer status %d", status)
	}
}

func jmapRaw(t *testing.T, url, method, auth string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func jmapMethod(t *testing.T, origin, auth string, using []string, name string, args map[string]any) (string, map[string]any) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"using": using, "methodCalls": []any{[]any{name, args, "c1"}}})
	status, raw := jmapRaw(t, origin+"/jmap", http.MethodPost, auth, payload)
	if status != http.StatusOK {
		t.Fatalf("%s: %d %s", name, status, raw)
	}
	var env struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.MethodResponses) != 1 || len(env.MethodResponses[0]) != 3 {
		t.Fatalf("%s decode: %s", name, raw)
	}
	var got string
	var result map[string]any
	json.Unmarshal(env.MethodResponses[0][0], &got)
	json.Unmarshal(env.MethodResponses[0][1], &result)
	if strings.Contains(string(raw), `"serverFail"`) && got != "error" && result["notCreated"] == nil && result["notUpdated"] == nil {
		t.Fatalf("%s serverFail: %s", name, raw)
	}
	return got, result
}
