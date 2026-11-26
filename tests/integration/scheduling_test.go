//go:build integration

package integration

import (
	"strconv"
	"strings"
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
		"sendAt": time.Now().Add(20 * time.Second).UTC().Format(time.RFC3339),
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

	waitUpTo(t, time.Minute, "bob to receive the scheduled message", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 })
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
		"until": time.Now().Add(20 * time.Second).UTC().Format(time.RFC3339),
	}, &snoozed, 200)
	if findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 || findInAPI(w.bob, w.bobMailbox, "Snoozed", subject) != snoozed.UID {
		t.Fatal("snoozed message did not move to Snoozed")
	}
	waitUpTo(t, time.Minute, "the snoozed message to return to the inbox", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 })
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

func TestUndoSend(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	sendPath := "/mailboxes/" + w.aliceMailbox + "/send"
	var session struct {
		User struct {
			Title           string `json:"title"`
			Phone           string `json:"phone"`
			UndoSendSeconds int    `json:"undoSendSeconds"`
		} `json:"user"`
	}
	w.alice.must("GET", "/session", nil, &session, 200)
	if session.User.UndoSendSeconds != 0 {
		t.Fatalf("undo delay default = %d", session.User.UndoSendSeconds)
	}
	if code := w.alice.do("PUT", "/me/profile", map[string]any{"title": "Engineer", "phone": "", "undoSendSeconds": 7}, nil); code != 422 {
		t.Fatalf("invalid undo delay returned %d", code)
	}
	w.alice.must("PUT", "/me/profile", map[string]any{"title": "Engineer", "phone": "+1 555 0101", "undoSendSeconds": 10}, &session, 200)
	if session.User.UndoSendSeconds != 10 || session.User.Title != "Engineer" || session.User.Phone != "+1 555 0101" {
		t.Fatalf("profile = %+v", session.User)
	}
	w.alice.must("PUT", "/me/profile", map[string]any{"title": "Engineer", "phone": "+1 555 0101"}, &session, 200)
	if session.User.UndoSendSeconds != 10 {
		t.Fatalf("omitted delay reset the setting: %+v", session.User)
	}

	type held struct {
		ID      string    `json:"id"`
		SendAt  time.Time `json:"sendAt"`
		Undo    bool      `json:"undo"`
		Subject string    `json:"subject"`
	}
	subject := "undo me " + randomSuffix()
	var first held
	sentAt := time.Now()
	w.alice.must("POST", sendPath, map[string]any{
		"to": []map[string]string{{"address": bob}}, "bcc": []map[string]string{{"address": alice}},
		"subject": subject, "text": "take this back",
	}, &first, 202)
	if !first.Undo || first.ID == "" || first.Subject != subject {
		t.Fatalf("undo hold = %+v", first)
	}
	if first.SendAt.Before(sentAt.Add(5*time.Second)) || first.SendAt.After(sentAt.Add(20*time.Second)) {
		t.Fatalf("undo sendAt = %s, request at %s", first.SendAt, sentAt)
	}
	var list []held
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/scheduled", nil, &list, 200)
	if len(list) != 1 || list[0].ID != first.ID || !list[0].Undo {
		t.Fatalf("scheduled list = %+v", list)
	}
	if code := w.bob.do("DELETE", "/mailboxes/"+w.aliceMailbox+"/scheduled/"+first.ID, nil, nil); code != 404 {
		t.Fatalf("bob cancelled alice's hold: %d", code)
	}
	if findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 {
		t.Fatal("undo hold was delivered before it was cancelled")
	}

	var restored struct {
		UID    uint32 `json:"uid"`
		Folder string `json:"folder"`
	}
	w.alice.must("DELETE", "/mailboxes/"+w.aliceMailbox+"/scheduled/"+first.ID, nil, &restored, 200)
	var draft struct {
		Subject string `json:"subject"`
		Text    string `json:"text"`
		Bcc     []struct {
			Address string `json:"address"`
		} `json:"bcc"`
	}
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/messages/"+strconv.FormatUint(uint64(restored.UID), 10)+"?folder="+restored.Folder, nil, &draft, 200)
	if draft.Subject != subject || !strings.Contains(draft.Text, "take this back") || len(draft.Bcc) != 1 || draft.Bcc[0].Address != alice {
		t.Fatalf("restored draft = %+v", draft)
	}
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/scheduled", nil, &list, 200)
	if len(list) != 0 {
		t.Fatalf("scheduled list after undo = %+v", list)
	}

	if code := w.alice.do("POST", sendPath, map[string]any{
		"to": []map[string]string{{"address": bob}}, "subject": "too soon", "text": "x",
		"sendAt": time.Now().Add(time.Second).UTC().Format(time.RFC3339),
	}, nil); code != 422 {
		t.Fatalf("explicit send in one second returned %d", code)
	}
	var chosen held
	w.alice.must("POST", sendPath, map[string]any{
		"to": []map[string]string{{"address": bob}}, "subject": "later " + randomSuffix(), "text": "scheduled",
		"sendAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}, &chosen, 202)
	if chosen.Undo {
		t.Fatal("a chosen send time was held for undo")
	}
	w.alice.must("DELETE", "/mailboxes/"+w.aliceMailbox+"/scheduled/"+chosen.ID, nil, nil, 200)

	w.alice.must("PUT", "/me/profile", map[string]any{"title": "Engineer", "phone": "+1 555 0101", "undoSendSeconds": 5}, nil, 200)
	delayed := "delayed " + randomSuffix()
	started := time.Now()
	var second held
	w.alice.must("POST", sendPath, map[string]any{
		"to": []map[string]string{{"address": bob}}, "subject": delayed, "text": "goes out",
	}, &second, 202)
	if !second.Undo {
		t.Fatal("five second delay did not hold the message")
	}
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/scheduled", nil, &list, 200)
	if len(list) != 1 || list[0].ID != second.ID || !list[0].Undo {
		t.Fatalf("held message missing from the schedule: %+v", list)
	}
	waitUpTo(t, 20*time.Second, "the undo hold to be sent", func() bool {
		return findInAPI(w.bob, w.bobMailbox, "INBOX", delayed) != 0
	})
	if elapsed := time.Since(started); elapsed < 3*time.Second || elapsed > 15*time.Second {
		t.Fatalf("undo hold was sent after %s", elapsed)
	}
	waitFor(t, "the delayed copy in alice's Sent folder", func() bool {
		return findInAPI(w.alice, w.aliceMailbox, "Sent", delayed) != 0
	})
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/scheduled", nil, &list, 200)
	if len(list) != 0 {
		t.Fatalf("scheduled list after the hold was sent = %+v", list)
	}

	w.alice.must("PUT", "/me/profile", map[string]any{"title": "Engineer", "phone": "+1 555 0101", "undoSendSeconds": 0}, nil, 200)
	immediate := "immediate " + randomSuffix()
	var sent struct {
		MessageID string `json:"messageId"`
		ID        string `json:"id"`
		Undo      bool   `json:"undo"`
	}
	w.alice.must("POST", sendPath, map[string]any{
		"to": []map[string]string{{"address": bob}}, "subject": immediate, "text": "now",
	}, &sent, 202)
	if sent.MessageID == "" || sent.Undo || sent.ID != "" {
		t.Fatalf("immediate send = %+v", sent)
	}
	waitFor(t, "the immediate message", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", immediate) != 0 })
}
