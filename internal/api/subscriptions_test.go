package api

import "testing"

func TestNormalizeFeedURL(t *testing.T) {
	got, err := normalizeFeedURL("webcal://calendar.example/holidays.ics", false)
	if err != nil || got != "https://calendar.example/holidays.ics" {
		t.Fatalf("webcal: %s %v", got, err)
	}
	if _, err := normalizeFeedURL("http://calendar.example/a.ics", false); err == nil {
		t.Fatal("http should be refused")
	}
	got, err = normalizeFeedURL("http://calendar.example/a.ics", true)
	if err != nil || got != "http://calendar.example/a.ics" {
		t.Fatalf("http allowed: %s %v", got, err)
	}
	for _, raw := range []string{"", "https://user:pw@calendar.example/a.ics", "javascript:alert(1)", "ftp://calendar.example/a.ics"} {
		if _, err := normalizeFeedURL(raw, true); err == nil {
			t.Fatalf("%s should be refused", raw)
		}
	}
}

func TestRouteRegistration(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatal(recovered)
		}
	}()
	_ = (&Server{}).Handler()
}

func TestStableObjectName(t *testing.T) {
	if stableObjectName("planning") != "planning.ics" {
		t.Fatal(stableObjectName("planning"))
	}
	if stableObjectName("has a space") == "has a space.ics" || len(stableObjectName("has a space")) < 8 {
		t.Fatal(stableObjectName("has a space"))
	}
}

func TestObjectsFromFeed(t *testing.T) {
	feed := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//Test//Feed//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:holiday-1\r\n" +
		"DTSTART;VALUE=DATE:20261225\r\n" +
		"DTEND;VALUE=DATE:20261226\r\n" +
		"SUMMARY:Winter Holiday\r\n" +
		"END:VEVENT\r\n" +
		"BEGIN:VTODO\r\n" +
		"UID:todo-1\r\n" +
		"SUMMARY:Prepare release\r\n" +
		"DUE:20261201T120000Z\r\n" +
		"END:VTODO\r\n" +
		"END:VCALENDAR\r\n"
	objs, err := objectsFromFeed(feed)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("objects = %d, want 2", len(objs))
	}
	if objs[0].Fields.UID != "holiday-1" || objs[0].Fields.Component != "VEVENT" {
		t.Fatalf("first = %+v", objs[0].Fields)
	}
	if objs[1].Fields.UID != "todo-1" || objs[1].Fields.Component != "VTODO" || objs[1].Fields.EndsAt == nil {
		t.Fatalf("second = %+v", objs[1].Fields)
	}
	if _, err := objectsFromFeed("not a calendar"); err == nil {
		t.Fatal("invalid feed should return error")
	}
}

func TestValidRefresh(t *testing.T) {
	if !validRefresh(15) || !validRefresh(360) || !validRefresh(10080) {
		t.Fatal("valid intervals rejected")
	}
	for _, bad := range []int{0, 10, 17, 10085} {
		if validRefresh(bad) {
			t.Fatalf("%d should be rejected", bad)
		}
	}
}
