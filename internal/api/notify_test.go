package api

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func TestFoldersForUsesInboxUnlessTurnedOff(t *testing.T) {
	got := foldersFor(map[string]bool{"Projects": true, "INBOX": false}, "Archive")
	if strings.Join(got, ",") != "Projects,Archive" {
		t.Fatalf("folders = %v", got)
	}
	got = foldersFor(nil, "")
	if strings.Join(got, ",") != "INBOX" {
		t.Fatalf("default folders = %v", got)
	}
}

func TestLimitWatchesKeepsInboxAndFocus(t *testing.T) {
	var specs []watchSpec
	specs = append(specs, watchSpec{mailboxID: "box", address: "a@b.test", folder: "INBOX"})
	specs = append(specs, watchSpec{mailboxID: "box", address: "a@b.test", folder: "Focus", focus: true})
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		specs = append(specs, watchSpec{mailboxID: "box", address: "a@b.test", folder: name})
	}
	got := limitWatches(specs)
	if len(got) != maxWatchedFolders {
		t.Fatalf("watched %d folders", len(got))
	}
	if _, ok := got["box\nINBOX"]; !ok {
		t.Fatal("inbox was dropped")
	}
	if _, ok := got["box\nFocus"]; !ok {
		t.Fatal("the open folder was dropped")
	}
}

func TestWantsFolder(t *testing.T) {
	if !wantsFolder(nil, "INBOX") || wantsFolder(nil, "Archive") {
		t.Fatal("defaults")
	}
	if wantsFolder(map[string]bool{"INBOX": false}, "Inbox") {
		t.Fatal("inbox was disabled")
	}
	if !wantsFolder(map[string]bool{"Projects": true}, "Projects") {
		t.Fatal("enabled folder")
	}
}

func TestReminderDueWindow(t *testing.T) {
	now := time.Date(2026, 5, 2, 15, 0, 0, 0, time.UTC)
	start := now.Add(15 * time.Minute)
	if !reminderDue(start, 15, now) {
		t.Fatal("a reminder at its alarm time should fire")
	}
	if reminderDue(start, 15, now.Add(-2*time.Minute)) {
		t.Fatal("a reminder should not fire early")
	}
	if reminderDue(start, 15, now.Add(2*time.Minute)) {
		t.Fatal("a reminder should not fire again after the window")
	}
}

func TestReminderRoundTrip(t *testing.T) {
	event := ical.NewEvent()
	event.Props.SetText(ical.PropSummary, "Standup")
	event.Props.SetDateTime(ical.PropDateTimeStart, time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC))
	event.Props.SetDateTime(ical.PropDateTimeEnd, time.Date(2026, 5, 1, 16, 0, 0, 0, time.UTC))
	setReminder(event.Component, 15, "Standup")
	out, _, err := eventFromComponent("standup.ics", event.Component)
	if err != nil {
		t.Fatal(err)
	}
	if out.Reminder == nil || *out.Reminder != 15 {
		t.Fatalf("reminder = %v", out.Reminder)
	}
	plain := ical.NewEvent()
	plain.Props.SetDateTime(ical.PropDateTimeStart, time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC))
	plain.Props.SetDateTime(ical.PropDateTimeEnd, time.Date(2026, 5, 1, 16, 0, 0, 0, time.UTC))
	out, _, err = eventFromComponent("plain.ics", plain.Component)
	if err != nil || out.Reminder != nil {
		t.Fatalf("reminder = %v err %v", out.Reminder, err)
	}
}

func TestLiveHubDeliversMailToTheRightSubscriber(t *testing.T) {
	h := newLiveHub()
	alice := h.add("alice", map[string]struct{}{"box": {}})
	bob := h.add("bob", map[string]struct{}{"other": {}})
	h.deliver("", "box", sseFrame("mail", liveEvent{MailboxID: "box", Kind: "new"}))
	select {
	case frame := <-alice.ch:
		if !strings.Contains(string(frame), "event: mail") || !strings.Contains(string(frame), `"mailboxId":"box"`) {
			t.Fatalf("frame = %s", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("alice got no event")
	}
	select {
	case frame := <-bob.ch:
		t.Fatalf("bob got %s", frame)
	default:
	}
	h.deliver("bob", "", sseFrame("calendar", liveEvent{Kind: "calendar", Title: "Standup"}))
	select {
	case frame := <-bob.ch:
		if !strings.Contains(string(frame), "Standup") {
			t.Fatalf("frame = %s", frame)
		}
	default:
		t.Fatal("bob missed the reminder")
	}
}

func TestPushAndIOSValidation(t *testing.T) {
	if err := validatePushEndpoint("https://updates.push.services.mozilla.com/wpush/v2/abc"); err != nil {
		t.Fatal(err)
	}
	if validatePushEndpoint("http://127.0.0.1/push") == nil || validatePushEndpoint("https://user:pass@push.example/w") == nil {
		t.Fatal("accepted an unsafe push endpoint")
	}
	token := strings.Repeat("ab", 32)
	if !validIOSRegistration("ada@example.com", "account-1", token, "com.apple.mobilemail") {
		t.Fatal("expected a valid registration")
	}
	if validIOSRegistration("ada@example.com", "account-1", token, "com.example.other") {
		t.Fatal("accepted an arbitrary APNs topic")
	}
	if validIOSRegistration("ada@example.com", "account-1", "zzzz", "com.apple.mobilemail") {
		t.Fatal("accepted a short token")
	}
}

func TestClipKeepsWholeCharacters(t *testing.T) {
	if got := clip("héllo", 4); got != "hél…" {
		t.Fatalf("clip = %q", got)
	}
}
