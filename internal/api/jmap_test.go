package api

import (
	"testing"
	"time"
)

func TestJSONPointer(t *testing.T) {
	doc := map[string]any{"ids": []any{"a", "b"}, "a/b": map[string]any{"~": "tilde"}}
	got, err := jsonPointer(doc, "/ids/1")
	if err != nil || got != "b" {
		t.Fatalf("index: %v %v", got, err)
	}
	got, err = jsonPointer(doc, "/a~1b/~0")
	if err != nil || got != "tilde" {
		t.Fatalf("escaped: %v %v", got, err)
	}
	if _, err := jsonPointer(doc, "/missing"); err == nil {
		t.Fatal("missing path succeeded")
	}
}

func TestRewriteCreatedIDsAndResultReference(t *testing.T) {
	created := map[string]string{"draft": "e.created"}
	calls := []jmapCall{{
		Name: "Email/set", ID: "c1",
		Result: map[string]any{"created": map[string]any{"draft": map[string]any{"id": "e.created"}}},
	}}
	in := map[string]any{
		"emailId": "#draft",
		"keep":    "#unknown",
		"#id":     map[string]any{"resultOf": "c1", "name": "Email/set", "path": "/created/draft/id"},
	}
	out, err := rewriteJMAP(in, created, calls)
	if err != nil {
		t.Fatal(err)
	}
	obj := out.(map[string]any)
	if obj["emailId"] != "e.created" || obj["keep"] != "#unknown" || obj["id"] != "e.created" {
		t.Fatalf("rewrite: %#v", obj)
	}
	if _, err := rewriteJMAP(map[string]any{"#x": map[string]any{"resultOf": "missing", "name": "Email/set", "path": "/id"}}, created, calls); err == nil {
		t.Fatal("missing result reference succeeded")
	}
}

func TestKeywordMap(t *testing.T) {
	kw, uerr := keywordMap(map[string]any{"$seen": true, "$Draft": true, "$custom_1": true, "dropped": nil})
	if uerr != nil || !kw["$seen"] || !kw["$draft"] || !kw["$custom_1"] {
		t.Fatalf("keywords %#v %v", kw, uerr)
	}
	if _, uerr := keywordMap(map[string]any{"seen": true}); uerr == nil {
		t.Fatal("bare keyword accepted")
	}
	if _, uerr := keywordMap(map[string]any{"$seen": false}); uerr == nil {
		t.Fatal("false keyword accepted")
	}
}

func TestParseDuration(t *testing.T) {
	d, err := parseDuration("PT1H30M")
	if err != nil || d != 90*time.Minute {
		t.Fatalf("hours: %v %v", d, err)
	}
	d, err = parseDuration("P2D")
	if err != nil || d != 48*time.Hour {
		t.Fatalf("days: %v %v", d, err)
	}
	if _, err := parseDuration("1H"); err == nil {
		t.Fatal("bare duration accepted")
	}
}

func TestStablePIMIDs(t *testing.T) {
	id := eventID("meeting-1")
	uid, ok := eventUID(id)
	if !ok || uid != "meeting-1" || eventID(uid) != id {
		t.Fatalf("event id %s %s %v", id, uid, ok)
	}
	cid := cardID("card-1")
	uid, ok = cardUID(cid)
	if !ok || uid != "card-1" {
		t.Fatalf("card id %s %s %v", cid, uid, ok)
	}
	if _, ok := eventUID("e.not-an-event"); ok {
		t.Fatal("email id decoded as an event")
	}
}

func TestDateOnlyAndStripTags(t *testing.T) {
	if dateOnly("2026-12-01T00:00:00Z") != "2026-12-01" {
		t.Fatal(dateOnly("2026-12-01T00:00:00Z"))
	}
	if stripTags("<p>Hello <b>there</b></p>") != "Hello there" {
		t.Fatal(stripTags("<p>Hello <b>there</b></p>"))
	}
}
