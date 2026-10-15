//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestSignatures(t *testing.T) {
	w := setupWorld(t)
	path := "/mailboxes/" + w.aliceMailbox + "/signatures"
	type sig struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		HTML string `json:"html"`
	}
	type settings struct {
		Signatures []sig  `json:"signatures"`
		NewMessage string `json:"newMessage"`
		Replies    string `json:"replies"`
	}

	var empty settings
	w.alice.must("GET", path, nil, &empty, 200)
	if empty.Signatures == nil || len(empty.Signatures) != 0 || empty.NewMessage != "" {
		t.Fatalf("a new mailbox should start with no signatures: %+v", empty)
	}
	if code := w.alice.do("PUT", path, settings{Signatures: []sig{{ID: "work", Name: "Work", HTML: "<p>Alice</p>"}}, NewMessage: "missing"}, nil); code != 422 {
		t.Fatalf("a default pointing at no signature should be refused, got %d", code)
	}
	var saved settings
	w.alice.must("PUT", path, settings{
		Signatures: []sig{
			{ID: "work", Name: "Work", HTML: `<p><strong>Alice</strong></p><script>alert(1)</script><p style="color: #dc2626" onclick="x()">Engineering</p>`},
			{ID: "short", Name: "Short", HTML: "<p>— A</p>"},
		},
		NewMessage: "work",
		Replies:    "short",
	}, &saved, 200)
	work := saved.Signatures[0].HTML
	if strings.Contains(work, "script") || strings.Contains(work, "onclick") || !strings.Contains(work, "<strong>Alice</strong>") || !strings.Contains(work, "color: #dc2626") {
		t.Fatalf("signature html was not sanitized as mail is: %q", work)
	}
	var loaded settings
	w.alice.must("GET", path, nil, &loaded, 200)
	if len(loaded.Signatures) != 2 || loaded.NewMessage != "work" || loaded.Replies != "short" {
		t.Fatalf("signatures did not round trip: %+v", loaded)
	}
	if code := w.bob.do("GET", path, nil, nil); code != 404 {
		t.Fatalf("bob should not see alice's signatures, got %d", code)
	}
}
