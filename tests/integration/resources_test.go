//go:build integration

package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type bookingView struct {
	ID        string `json:"id"`
	Attendees []struct {
		Address string `json:"address"`
		Status  string `json:"status"`
		Kind    string `json:"kind"`
	} `json:"attendees"`
	Bookings []struct {
		Address string `json:"address"`
		Status  string `json:"status"`
		Reason  string `json:"reason"`
	} `json:"bookings"`
}

type resourceChoiceView struct {
	Address      string `json:"address"`
	Bookable     bool   `json:"bookable"`
	Availability string `json:"availability"`
	Reason       string `json:"reason"`
}

func roomBusy(t *testing.T, c *apiClient, address string, start, end time.Time) int {
	t.Helper()
	var out []struct {
		Known bool       `json:"known"`
		Busy  []struct{} `json:"busy"`
	}
	c.must("GET", "/calendar/freebusy?"+url.Values{"start": {start.Format(time.RFC3339)}, "end": {end.Format(time.RFC3339)}, "attendee": {address}}.Encode(), nil, &out, 200)
	if len(out) != 1 || !out[0].Known {
		t.Fatalf("room free/busy unknown: %+v", out)
	}
	return len(out[0].Busy)
}

func TestRoomsAndEquipment(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	for _, email := range []string{alice, bob} {
		w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": email, "role": "member"}, nil, 204)
	}
	var bobSession struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	w.bob.must("GET", "/session", nil, &bobSession, 200)

	rules := map[string]any{"type": "room", "capacity": 8, "location": "3rd floor", "windowDays": 30, "maxMinutes": 120, "timeZone": "UTC"}
	if code := w.admin.do("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"kind": "resource", "domainId": w.domainID, "localPart": "board", "resource": rules}, nil); code != 422 {
		t.Fatalf("a resource without a name: %d, want 422", code)
	}
	var room, projector idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"kind": "resource", "domainId": w.domainID, "localPart": "board", "displayName": "Board Room", "resource": rules}, &room, 201)
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{
		"kind": "resource", "domainId": w.domainID, "localPart": "projector", "displayName": "Projector",
		"resource": map[string]any{"type": "equipment", "policy": "approval", "approvers": []string{bobSession.User.ID}, "timeZone": "UTC"},
	}, &projector, 201)
	roomAddress, projectorAddress := "board@"+w.domain, "projector@"+w.domain
	if code := w.admin.do("POST", "/mailboxes/"+room.ID+"/delegates", map[string]any{"email": alice}, nil); code != 422 {
		t.Fatalf("delegates on a room: %d, want 422", code)
	}
	if code := w.admin.do("POST", "/mailboxes/"+room.ID+"/convert", map[string]any{"kind": "shared"}, nil); code != 422 {
		t.Fatalf("converting a room: %d, want 422", code)
	}
	var listed []struct {
		Address   string   `json:"address"`
		Type      string   `json:"type"`
		Approvers []string `json:"approvers"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/resources", nil, &listed, 200)
	if len(listed) != 2 || listed[0].Address != roomAddress || listed[1].Type != "equipment" || len(listed[1].Approvers) != 1 {
		t.Fatalf("resources listed %+v", listed)
	}
	var dir []struct {
		Address string `json:"address"`
		Kind    string `json:"kind"`
	}
	w.alice.must("GET", "/directory?q=board", nil, &dir, 200)
	if len(dir) != 1 || dir[0].Kind != "room" {
		t.Fatalf("room missing from the directory: %+v", dir)
	}

	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour).UTC()
	end := start.Add(time.Hour)
	var aliceEvent bookingView
	w.alice.must("POST", "/calendar/events", map[string]any{"title": "Quarterly review", "start": start, "end": end, "attendees": []string{bob, roomAddress}}, &aliceEvent, 201)
	if len(aliceEvent.Bookings) != 1 || aliceEvent.Bookings[0].Status != "accepted" {
		t.Fatalf("free room should be booked right away: %+v", aliceEvent.Bookings)
	}
	for _, a := range aliceEvent.Attendees {
		if a.Address == roomAddress && (a.Status != "accepted" || a.Kind != "room") {
			t.Fatalf("room attendee %+v", a)
		}
	}
	if roomBusy(t, w.bob, roomAddress, start.Add(-time.Hour), end.Add(time.Hour)) != 1 {
		t.Fatal("the booking should make the room busy")
	}

	var choices []resourceChoiceView
	query := "/calendar/resources?" + url.Values{"start": {start.Add(30 * time.Minute).Format(time.RFC3339)}, "end": {end.Add(30 * time.Minute).Format(time.RFC3339)}}.Encode()
	w.bob.must("GET", query, nil, &choices, 200)
	if len(choices) != 2 || choices[0].Availability != "busy" || !strings.Contains(choices[0].Reason, "already booked") || choices[1].Availability != "free" {
		t.Fatalf("finder for bob %+v", choices)
	}
	w.alice.must("GET", query+"&exclude="+url.QueryEscape(aliceEvent.ID), nil, &choices, 200)
	if choices[0].Availability != "free" {
		t.Fatalf("the event being edited must not block its own room: %+v", choices[0])
	}

	var clash bookingView
	w.bob.must("POST", "/calendar/events", map[string]any{"title": "Overlapping", "start": start.Add(30 * time.Minute), "end": end.Add(30 * time.Minute), "attendees": []string{roomAddress}}, &clash, 201)
	if len(clash.Bookings) != 1 || clash.Bookings[0].Status != "declined" || !strings.Contains(clash.Bookings[0].Reason, "already booked") {
		t.Fatalf("double booking must be declined: %+v", clash.Bookings)
	}
	waitFor(t, "decline in bob's inbox", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", "Declined: Overlapping") != 0 })
	var long bookingView
	w.bob.must("POST", "/calendar/events", map[string]any{"title": "Workshop", "start": start.Add(48 * time.Hour), "end": start.Add(51 * time.Hour), "attendees": []string{roomAddress}}, &long, 201)
	if long.Bookings[0].Status != "declined" || !strings.Contains(long.Bookings[0].Reason, "at most 2 hours") {
		t.Fatalf("rule violation must be declined: %+v", long.Bookings)
	}

	var pending bookingView
	w.alice.must("POST", "/calendar/events", map[string]any{"title": "Demo", "start": start.Add(24 * time.Hour), "end": end.Add(24 * time.Hour), "attendees": []string{projectorAddress}}, &pending, 201)
	if pending.Bookings[0].Status != "tentative" {
		t.Fatalf("approval policy should hold the request: %+v", pending.Bookings)
	}
	var requests []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Conflict string `json:"conflict"`
	}
	w.alice.must("GET", "/calendar/booking-requests", nil, &requests, 200)
	if len(requests) != 0 {
		t.Fatalf("only approvers see requests: %+v", requests)
	}
	w.bob.must("GET", "/calendar/booking-requests", nil, &requests, 200)
	if len(requests) != 1 || requests[0].Title != "Demo" || requests[0].Conflict != "" {
		t.Fatalf("approver queue %+v", requests)
	}
	if code := w.alice.do("POST", "/calendar/booking-requests/"+requests[0].ID, map[string]string{"decision": "approve"}, nil); code != 404 {
		t.Fatalf("non-approver approved a request: %d", code)
	}
	w.bob.must("POST", "/calendar/booking-requests/"+requests[0].ID, map[string]string{"decision": "approve"}, nil, 204)
	if code := w.bob.do("POST", "/calendar/booking-requests/"+requests[0].ID, map[string]string{"decision": "approve"}, nil); code != 404 {
		t.Fatalf("a handled request can be approved twice: %d", code)
	}
	var events []bookingView
	w.alice.must("GET", "/calendar/events?start="+url.QueryEscape(start.Add(23*time.Hour).Format(time.RFC3339))+"&end="+url.QueryEscape(end.Add(25*time.Hour).Format(time.RFC3339)), nil, &events, 200)
	if len(events) != 1 || len(events[0].Attendees) != 1 || events[0].Attendees[0].Status != "accepted" || events[0].Attendees[0].Kind != "equipment" {
		t.Fatalf("approval not applied to the organizer's event: %+v", events)
	}
	waitFor(t, "approval email", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", "Accepted: Demo") != 0 })

	mailStart := start.Add(5 * 24 * time.Hour)
	invite := func(uid, title, from string) string {
		return strings.Join([]string{
			"From: " + from, "To: " + roomAddress, "Subject: Invitation: " + title, "Date: " + time.Now().Format(time.RFC1123Z),
			"Message-ID: <" + randomSuffix() + "@integration.test>", "MIME-Version: 1.0", "Content-Type: text/calendar; charset=utf-8; method=REQUEST", "",
			"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Integration//EN", "METHOD:REQUEST", "BEGIN:VEVENT", "UID:" + uid,
			"DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z"), "DTSTART:" + mailStart.Format("20060102T150405Z"), "DTEND:" + mailStart.Add(time.Hour).Format("20060102T150405Z"),
			"SUMMARY:" + title, "ORGANIZER:mailto:" + from, "ATTENDEE;CUTYPE=ROOM;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:" + roomAddress, "END:VEVENT", "END:VCALENDAR", "",
		}, "\r\n")
	}
	if err := submit(t, smtpAddr, "", "", alice, []string{roomAddress}, invite("spoof-"+randomSuffix(), "Spoofed", alice), false); err == nil || !strings.Contains(err.Error(), "does not accept mail") {
		t.Fatalf("unauthenticated mail claiming a hosted organizer must be refused: %v", err)
	}
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{roomAddress}, invite("imip-"+randomSuffix(), "Mailed review", alice), false); err != nil {
		t.Fatalf("submitting an invitation from a calendar app: %v", err)
	}
	waitUpTo(t, 90*time.Second, "room reply to an emailed invitation", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", "Accepted: Mailed review") != 0 })
	if roomBusy(t, w.bob, roomAddress, mailStart, mailStart.Add(time.Hour)) != 1 {
		t.Fatal("an emailed invitation should book the room")
	}

	base := env(t, "AETHER_TEST_API", "http://api:8080")
	principal := ""
	{
		req, _ := http.NewRequest("PROPFIND", base+"/.well-known/caldav", strings.NewReader(`<propfind xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><prop><c:schedule-outbox-URL/><c:calendar-user-address-set/></prop></propfind>`))
		req.SetBasicAuth(alice, w.aliceAppPassword)
		req.Header.Set("Depth", "0")
		req.Header.Set("Content-Type", "application/xml")
		client := &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
			principal = r.URL.Path
			r.SetBasicAuth(alice, w.aliceAppPassword)
			return nil
		}}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), principal+"outbox/") || !strings.Contains(string(body), "mailto:"+alice) {
			t.Fatalf("principal does not advertise the scheduling outbox: %s", body)
		}
		opts, _ := http.NewRequest("OPTIONS", base+principal+"calendars/", nil)
		opts.SetBasicAuth(alice, w.aliceAppPassword)
		resp, err = http.DefaultClient.Do(opts)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if !strings.Contains(strings.Join(resp.Header.Values("DAV"), ","), "calendar-schedule") {
			t.Fatalf("DAV header %q", resp.Header.Values("DAV"))
		}
	}
	freebusy := fmt.Sprintf("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Integration//EN\r\nMETHOD:REQUEST\r\nBEGIN:VFREEBUSY\r\nUID:fb-%s\r\nDTSTAMP:%s\r\nDTSTART:%s\r\nDTEND:%s\r\nORGANIZER:mailto:%s\r\nATTENDEE:mailto:%s\r\nATTENDEE:mailto:nobody@external.example\r\nEND:VFREEBUSY\r\nEND:VCALENDAR\r\n",
		randomSuffix(), time.Now().UTC().Format("20060102T150405Z"), start.Add(-time.Hour).Format("20060102T150405Z"), end.Add(time.Hour).Format("20060102T150405Z"), alice, roomAddress)
	req, _ := http.NewRequest("POST", base+principal+"outbox/", strings.NewReader(freebusy))
	req.SetBasicAuth(alice, w.aliceAppPassword)
	req.Header.Set("Content-Type", "text/calendar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	period := "FREEBUSY;FBTYPE=BUSY:" + start.Format("20060102T150405Z") + "/" + end.Format("20060102T150405Z")
	if resp.StatusCode != 200 || !strings.Contains(string(body), "2.0;Success") || !strings.Contains(string(body), period) || !strings.Contains(string(body), "3.7;Invalid calendar user") {
		t.Fatalf("outbox free/busy %d: %s", resp.StatusCode, body)
	}

	w.alice.must("DELETE", "/calendar/events/"+url.PathEscape(aliceEvent.ID), nil, nil, 204)
	if roomBusy(t, w.bob, roomAddress, start.Add(-time.Hour), end.Add(time.Hour)) != 0 {
		t.Fatal("cancelling the event must free the room")
	}
}
