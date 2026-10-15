//go:build integration

package integration

import (
	"strconv"
	"testing"
	"time"
)

func TestScheduledSendAndSnooze(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	sendPath := "/mailboxes/" + w.aliceMailbox + "/send"
	type scheduled struct {
		ID     string    `json:"id"`
		SendAt time.Time `json:"sendAt"`
	}

	subject := "scheduled " + randomSuffix()
	var first scheduled
	w.alice.must("POST", sendPath, map[string]any{
		"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "sent later",
		"sendAt": time.Now().Add(6 * time.Second).UTC().Format(time.RFC3339),
	}, &first, 202)

	later := "cancelled " + randomSuffix()
	var second scheduled
	w.alice.must("POST", sendPath, map[string]any{
		"to": []map[string]string{{"address": bob}}, "bcc": []map[string]string{{"address": alice}}, "subject": later, "text": "never sent",
		"sendAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}, &second, 202)

	if code := w.alice.do("POST", sendPath, map[string]any{
		"to": []map[string]string{{"address": bob}}, "subject": "past", "sendAt": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}, nil); code != 422 {
		t.Fatalf("scheduling in the past returned %d, want 422", code)
	}

	var list []scheduled
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/scheduled", nil, &list, 200)
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("scheduled list = %+v", list)
	}
	if code := w.bob.do("GET", "/mailboxes/"+w.aliceMailbox+"/scheduled", nil, nil); code != 404 {
		t.Fatalf("another user could list alice's scheduled messages: %d", code)
	}

	var restored struct {
		UID    uint32 `json:"uid"`
		Folder string `json:"folder"`
	}
	w.alice.must("DELETE", "/mailboxes/"+w.aliceMailbox+"/scheduled/"+second.ID, nil, &restored, 200)
	if restored.UID == 0 || findInAPI(w.alice, w.aliceMailbox, restored.Folder, later) != restored.UID {
		t.Fatalf("cancelled message was not restored to Drafts: %+v", restored)
	}

	waitFor(t, "bob to receive the scheduled message", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 })
	waitFor(t, "the scheduled copy in alice's Sent folder", func() bool { return findInAPI(w.alice, w.aliceMailbox, "Sent", subject) != 0 })
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/scheduled", nil, &list, 200)
	if len(list) != 0 {
		t.Fatalf("scheduled list after sending = %+v", list)
	}
	if findInAPI(w.bob, w.bobMailbox, "INBOX", later) != 0 {
		t.Fatal("cancelled message was delivered")
	}

	uid := findInAPI(w.bob, w.bobMailbox, "INBOX", subject)
	var snoozed struct {
		UID uint32 `json:"uid"`
	}
	w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/messages/"+strconv.FormatUint(uint64(uid), 10)+"/snooze?folder=INBOX", map[string]string{
		"until": time.Now().Add(6 * time.Second).UTC().Format(time.RFC3339),
	}, &snoozed, 200)
	if findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 || findInAPI(w.bob, w.bobMailbox, "Snoozed", subject) != snoozed.UID {
		t.Fatal("snoozed message did not move to Snoozed")
	}
	waitFor(t, "the snoozed message to return to the inbox", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 })
	var page messagePage
	w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/messages?folder=INBOX", nil, &page, 200)
	for _, m := range page.Messages {
		if m.Subject == subject && m.Seen {
			t.Fatal("a woken message should be unread")
		}
	}

	uid = findInAPI(w.bob, w.bobMailbox, "INBOX", subject)
	w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/messages/"+strconv.FormatUint(uint64(uid), 10)+"/snooze?folder=INBOX", map[string]string{
		"until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}, &snoozed, 200)
	var snoozedList []struct {
		UID uint32 `json:"uid"`
	}
	w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/snoozed", nil, &snoozedList, 200)
	if len(snoozedList) != 1 || snoozedList[0].UID != snoozed.UID {
		t.Fatalf("snoozed list = %+v", snoozedList)
	}
	w.bob.must("DELETE", "/mailboxes/"+w.bobMailbox+"/snoozed/"+strconv.FormatUint(uint64(snoozed.UID), 10), nil, nil, 204)
	if findInAPI(w.bob, w.bobMailbox, "INBOX", subject) == 0 {
		t.Fatal("unsnoozed message did not return to the inbox")
	}
}
