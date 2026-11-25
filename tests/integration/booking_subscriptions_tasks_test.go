//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

func TestTasksAndCalDAVVTODO(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	due := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Minute)

	type taskView struct {
		ID          string     `json:"id"`
		CalendarID  string     `json:"calendarId"`
		Title       string     `json:"title"`
		Notes       string     `json:"notes"`
		Status      string     `json:"status"`
		Priority    int        `json:"priority"`
		Due         *time.Time `json:"due"`
		CompletedAt *time.Time `json:"completedAt"`
		ReadOnly    bool       `json:"readOnly"`
	}

	if code := w.alice.do("POST", "/calendar/tasks", map[string]any{"title": "   "}, nil); code != 422 {
		t.Fatalf("empty task title returned %d, want 422", code)
	}

	var created taskView
	w.alice.must("POST", "/calendar/tasks", map[string]any{
		"title":    "Prepare quarterly notes",
		"notes":    "Include metrics summary",
		"priority": 1,
		"due":      due,
	}, &created, 201)
	if created.ID == "" || created.Status != "needs-action" || created.Priority != 1 || created.Due == nil {
		t.Fatalf("unexpected created task: %+v", created)
	}

	var tasks []taskView
	w.alice.must("GET", "/calendar/tasks?status=open", nil, &tasks, 200)
	found := false
	for _, item := range tasks {
		if item.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("created task missing from open list: %+v", tasks)
	}

	var events []eventView
	q := url.Values{
		"start": {due.Add(-24 * time.Hour).Format(time.RFC3339)},
		"end":   {due.Add(24 * time.Hour).Format(time.RFC3339)},
	}
	w.alice.must("GET", "/calendar/events?"+q.Encode(), nil, &events, 200)
	for _, ev := range events {
		if ev.Title == "Prepare quarterly notes" {
			t.Fatalf("task leaked into events list: %+v", ev)
		}
	}

	var completed taskView
	w.alice.must("PUT", "/calendar/tasks/"+created.ID, map[string]any{
		"title":    "Prepare quarterly notes",
		"notes":    "Done",
		"priority": 1,
		"status":   "completed",
		"due":      due,
	}, &completed, 200)
	if completed.Status != "completed" || completed.CompletedAt == nil {
		t.Fatalf("task did not complete: %+v", completed)
	}
	firstCompletedAt := *completed.CompletedAt

	time.Sleep(1100 * time.Millisecond)
	var reedited taskView
	w.alice.must("PUT", "/calendar/tasks/"+created.ID, map[string]any{
		"title":    "Prepare quarterly notes (final)",
		"notes":    "Done and filed",
		"priority": 5,
		"status":   "completed",
		"due":      due,
	}, &reedited, 200)
	if reedited.CompletedAt == nil || !reedited.CompletedAt.Equal(firstCompletedAt) {
		t.Fatalf("editing completed task changed completedAt from %v to %v", firstCompletedAt, reedited.CompletedAt)
	}

	ctx := context.Background()
	base := env(t, "AETHER_TEST_API", "http://api:8080")
	davHTTP := webdav.HTTPClientWithBasicAuth(http.DefaultClient, alice, w.aliceAppPassword)
	cal, err := caldav.NewClient(davHTTP, base+"/.well-known/caldav")
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
	calendars, err := cal.FindCalendars(ctx, home)
	if err != nil || len(calendars) == 0 {
		t.Fatalf("calendars: %v %v", calendars, err)
	}
	obj, err := cal.GetCalendarObject(ctx, calendars[0].Path+created.ID)
	if err != nil || len(obj.Data.Children) == 0 || obj.Data.Children[0].Name != ical.CompToDo {
		t.Fatalf("task missing over CalDAV: %v %+v", err, obj)
	}

	davUID := "vtodo-" + randomSuffix()
	todo := ical.NewComponent(ical.CompToDo)
	todo.Props.SetText(ical.PropUID, davUID)
	todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	todo.Props.SetText(ical.PropSummary, "Created via CalDAV")
	todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
	doc := ical.NewCalendar()
	doc.Props.SetText(ical.PropVersion, "2.0")
	doc.Props.SetText(ical.PropProductID, "-//Aether//integration//EN")
	doc.Children = append(doc.Children, todo)
	if _, err := cal.PutCalendarObject(ctx, calendars[0].Path+davUID+".ics", doc); err != nil {
		t.Fatalf("put VTODO via CalDAV: %v", err)
	}

	w.alice.must("GET", "/calendar/tasks?status=open", nil, &tasks, 200)
	var davTaskID string
	for _, item := range tasks {
		if item.Title == "Created via CalDAV" {
			davTaskID = item.ID
		}
	}
	if davTaskID == "" {
		t.Fatalf("CalDAV VTODO not visible in tasks API: %+v", tasks)
	}

	w.alice.must("DELETE", "/calendar/tasks/"+created.ID, nil, nil, 204)
	w.alice.must("DELETE", "/calendar/tasks/"+davTaskID, nil, nil, 204)
}

func TestCalendarSubscriptions(t *testing.T) {
	if env(t, "AETHER_TEST_WEBHOOKS", "false") != "true" {
		t.Skip("AETHER_TEST_WEBHOOKS is not enabled")
	}
	w := setupWorld(t)
	alice := "alice@" + w.domain
	start := time.Now().UTC().Add(36 * time.Hour).Truncate(time.Hour)

	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	etag := `"v1"`
	icsBody := fmt.Sprintf("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//Feed//EN\r\nBEGIN:VEVENT\r\nUID:feed-ev-1\r\nDTSTART:%s\r\nDTEND:%s\r\nSUMMARY:Holiday v1\r\nEND:VEVENT\r\nBEGIN:VTODO\r\nUID:feed-todo-1\r\nSUMMARY:Feed task\r\nSTATUS:NEEDS-ACTION\r\nEND:VTODO\r\nEND:VCALENDAR\r\n",
		start.Format("20060102T150405Z"), start.Add(time.Hour).Format("20060102T150405Z"))

	go http.Serve(ln, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		curTag := etag
		curBody := icsBody
		mu.Unlock()
		if r.Header.Get("If-None-Match") == curTag {
			rw.WriteHeader(http.StatusNotModified)
			return
		}
		rw.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		rw.Header().Set("ETag", curTag)
		rw.WriteHeader(http.StatusOK)
		rw.Write([]byte(curBody))
	}))

	var ip string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			ip = n.IP.String()
			break
		}
	}
	feedURL := "http://" + net.JoinHostPort(ip, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)) + "/feed.ics"

	type subInfo struct {
		URL            string `json:"url"`
		RefreshMinutes int    `json:"refreshMinutes"`
		Events         int    `json:"events"`
		NotModified    bool   `json:"notModified"`
		LastError      string `json:"lastError"`
	}
	type subCalendar struct {
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		Access       string   `json:"access"`
		Subscription *subInfo `json:"subscription"`
	}

	var created subCalendar
	w.alice.must("POST", "/calendar/subscriptions", map[string]any{
		"url":            feedURL,
		"name":           "Team Feed",
		"color":          "#22c55e",
		"refreshMinutes": 60,
	}, &created, 201)
	if created.ID == "" || created.Subscription == nil || created.Subscription.Events != 2 {
		t.Fatalf("unexpected subscription calendar: %+v", created)
	}

	q := url.Values{
		"start": {start.Add(-time.Hour).Format(time.RFC3339)},
		"end":   {start.Add(3 * time.Hour).Format(time.RFC3339)},
	}
	var evs []sharedEventView
	w.alice.must("GET", "/calendar/events?"+q.Encode(), nil, &evs, 200)
	foundEv := false
	for _, e := range evs {
		if e.CalendarID == created.ID && e.Title == "Holiday v1" && e.ReadOnly {
			foundEv = true
		}
	}
	if !foundEv {
		t.Fatalf("subscribed event missing or writable: %+v", evs)
	}

	if code := w.alice.do("POST", "/calendar/events", map[string]any{
		"title": "Blocked", "start": start, "end": start.Add(time.Hour), "calendarId": created.ID,
	}, nil); code != 403 {
		t.Fatalf("writing event to subscribed calendar returned %d, want 403", code)
	}
	if code := w.alice.do("POST", "/calendar/tasks", map[string]any{
		"title": "Blocked task", "calendarId": created.ID,
	}, nil); code != 403 {
		t.Fatalf("writing task to subscribed calendar returned %d, want 403", code)
	}
	if code := w.alice.do("POST", "/calendar/calendars/"+created.ID+"/shares", map[string]string{
		"email": "bob@" + w.domain, "access": "write",
	}, nil); code != 422 {
		t.Fatalf("sharing subscribed calendar with write returned %d, want 422", code)
	}

	ctx := context.Background()
	base := env(t, "AETHER_TEST_API", "http://api:8080")
	davHTTP := webdav.HTTPClientWithBasicAuth(http.DefaultClient, alice, w.aliceAppPassword)
	cal, err := caldav.NewClient(davHTTP, base+"/.well-known/caldav")
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := cal.FindCurrentUserPrincipal(ctx)
	home, _ := cal.FindCalendarHomeSet(ctx, principal)
	davCals, _ := cal.FindCalendars(ctx, home)
	var subPath string
	for _, c := range davCals {
		if strings.HasSuffix(c.Path, "/"+created.ID+"/") {
			subPath = c.Path
		}
	}
	if subPath == "" {
		t.Fatalf("subscribed calendar missing from CalDAV: %+v", davCals)
	}
	roDoc := ical.NewCalendar()
	roDoc.Props.SetText(ical.PropVersion, "2.0")
	roDoc.Props.SetText(ical.PropProductID, "-//Aether//integration//EN")
	roEv := ical.NewEvent()
	roEv.Props.SetText(ical.PropUID, "ro-"+randomSuffix())
	roEv.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	roEv.Props.SetDateTime(ical.PropDateTimeStart, start)
	roEv.Props.SetDateTime(ical.PropDateTimeEnd, start.Add(time.Hour))
	roEv.Props.SetText(ical.PropSummary, "Should fail")
	roDoc.Children = append(roDoc.Children, roEv.Component)
	if _, err := cal.PutCalendarObject(ctx, subPath+"ro.ics", roDoc); err == nil {
		t.Fatal("CalDAV PUT into subscribed calendar succeeded, want error")
	}

	var refreshed subInfo
	w.alice.must("POST", "/calendar/subscriptions/"+created.ID+"/refresh", nil, &refreshed, 200)
	if !refreshed.NotModified || refreshed.Events != 2 {
		t.Fatalf("304 refresh returned %+v, want notModified=true events=2", refreshed)
	}

	mu.Lock()
	etag = `"v2"`
	icsBody = fmt.Sprintf("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//Feed//EN\r\nBEGIN:VEVENT\r\nUID:feed-ev-2\r\nDTSTART:%s\r\nDTEND:%s\r\nSUMMARY:Holiday v2\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		start.Format("20060102T150405Z"), start.Add(time.Hour).Format("20060102T150405Z"))
	mu.Unlock()

	var refreshed2 subInfo
	w.alice.must("POST", "/calendar/subscriptions/"+created.ID+"/refresh", nil, &refreshed2, 200)
	if refreshed2.NotModified || refreshed2.Events != 1 {
		t.Fatalf("updated refresh returned %+v, want notModified=false events=1", refreshed2)
	}

	w.alice.must("PATCH", "/calendar/subscriptions/"+created.ID, map[string]any{
		"refreshMinutes": 15,
	}, nil, 204)

	w.alice.must("DELETE", "/calendar/calendars/"+created.ID, nil, nil, 204)
}

func TestBookingPagesAndInvitation(t *testing.T) {
	w := setupWorld(t)
	bob := "bob@" + w.domain
	slug := "alice-" + randomSuffix()

	w.alice.must("PUT", "/calendar/booking", map[string]any{
		"slug":             slug,
		"active":           true,
		"title":            "Book with Alice",
		"description":      "Pick any open slot.",
		"timezone":         "UTC",
		"minNoticeMinutes": 30,
		"horizonDays":      14,
	}, nil, 201)

	var allDays []map[string]any
	for d := 0; d < 7; d++ {
		allDays = append(allDays, map[string]any{
			"weekday":     d,
			"startMinute": 9 * 60,
			"endMinute":   17 * 60,
		})
	}
	var pageResp struct {
		Types []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"types"`
	}
	w.alice.must("POST", "/calendar/booking/types", map[string]any{
		"name":                "Intro call",
		"slug":                "intro-call",
		"description":         "Quick chat",
		"durationMinutes":     30,
		"bufferBeforeMinutes": 15,
		"bufferAfterMinutes":  15,
		"location":            "Room 4B",
		"videoUrl":            "https://meet.example.com/alice",
		"calendarId":          "personal",
		"active":              true,
		"windows":             allDays,
	}, &pageResp, 201)
	if len(pageResp.Types) == 0 {
		t.Fatal("created meeting type missing from page response")
	}
	mtype := pageResp.Types[len(pageResp.Types)-1]

	anon := newClient(t)
	var pub struct {
		Slug        string `json:"slug"`
		Title       string `json:"title"`
		HorizonDays int    `json:"horizonDays"`
		Types       []struct {
			Slug     string `json:"slug"`
			Location string `json:"location"`
			Video    bool   `json:"video"`
		} `json:"types"`
	}
	anon.must("GET", "/book/"+slug, nil, &pub, 200)
	if pub.Slug != slug || pub.HorizonDays != 14 || len(pub.Types) != 1 || !pub.Types[0].Video {
		t.Fatalf("unexpected public booking page: %+v", pub)
	}

	targetDay := time.Now().UTC().AddDate(0, 0, 2).Truncate(24 * time.Hour)
	busyStart := targetDay.Add(10 * time.Hour)
	var busyEvent eventView
	w.alice.must("POST", "/calendar/events", map[string]any{
		"title": "Busy block", "start": busyStart, "end": busyStart.Add(time.Hour),
	}, &busyEvent, 201)

	var slotsResp struct {
		Slots []time.Time `json:"slots"`
	}
	slotsQuery := url.Values{
		"start": {targetDay.Format(time.RFC3339)},
		"end":   {targetDay.Add(24 * time.Hour).Format(time.RFC3339)},
	}.Encode()
	anon.must("GET", "/book/"+slug+"/intro-call/slots?"+slotsQuery, nil, &slotsResp, 200)
	if len(slotsResp.Slots) == 0 {
		t.Fatal("expected available booking slots")
	}
	var chosen time.Time
	for _, sl := range slotsResp.Slots {
		if sl.Equal(busyStart) || sl.Equal(busyStart.Add(-30*time.Minute)) || sl.Equal(busyStart.Add(time.Hour)) {
			t.Fatalf("slot %s overlaps busy block or buffers (%s..%s)", sl, busyStart, busyStart.Add(time.Hour))
		}
		if sl.Equal(targetDay.Add(13 * time.Hour)) {
			chosen = sl
		}
	}
	if chosen.IsZero() {
		chosen = slotsResp.Slots[0]
	}

	var conf struct {
		ID       string    `json:"id"`
		Title    string    `json:"title"`
		Start    time.Time `json:"start"`
		End      time.Time `json:"end"`
		Location string    `json:"location"`
		VideoURL string    `json:"videoUrl"`
	}
	anon.must("POST", "/book/"+slug+"/intro-call", map[string]any{
		"start": chosen,
		"name":  "Bob Guest",
		"email": bob,
		"note":  "Looking forward to chatting",
	}, &conf, 201)
	if conf.Location != "Room 4B" || conf.VideoURL != "https://meet.example.com/alice" {
		t.Fatalf("unexpected confirmation: %+v", conf)
	}

	if code := anon.do("POST", "/book/"+slug+"/intro-call", map[string]any{
		"start": chosen,
		"name":  "Second Guest",
		"email": "second@example.com",
	}, nil); code != 409 {
		t.Fatalf("double booking returned %d, want 409", code)
	}

	var inviteUID uint32
	waitFor(t, "booking invitation in bob's inbox", func() bool {
		inviteUID = findInAPI(w.bob, w.bobMailbox, "INBOX", "Invitation: "+conf.Title)
		return inviteUID != 0
	})

	q := url.Values{
		"start": {chosen.Add(-time.Hour).Format(time.RFC3339)},
		"end":   {chosen.Add(2 * time.Hour).Format(time.RFC3339)},
	}
	var aliceEvents []eventView
	w.alice.must("GET", "/calendar/events?"+q.Encode(), nil, &aliceEvents, 200)
	var bookedEventID string
	for _, ev := range aliceEvents {
		if ev.Title == conf.Title {
			bookedEventID = ev.ID
		}
	}
	if bookedEventID == "" {
		t.Fatalf("booked event missing from alice's calendar: %+v", aliceEvents)
	}

	w.alice.must("DELETE", "/calendar/events/"+bookedEventID, nil, nil, 204)

	var slotsAfter struct {
		Slots []time.Time `json:"slots"`
	}
	anon.must("GET", "/book/"+slug+"/intro-call/slots?"+slotsQuery, nil, &slotsAfter, 200)
	reopened := false
	for _, sl := range slotsAfter.Slots {
		if sl.Equal(chosen) {
			reopened = true
		}
	}
	if !reopened {
		t.Fatalf("deleting booked event did not reopen slot %s", chosen)
	}

	w.alice.must("DELETE", "/calendar/events/"+busyEvent.ID, nil, nil, 204)
	w.alice.must("DELETE", "/calendar/booking/types/"+mtype.ID, nil, nil, 204)
}
