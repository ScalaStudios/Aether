//go:build integration

package integration

import (
	"strconv"
	"testing"
	"time"
)

type invitationState struct {
	Method  string `json:"method"`
	Status  string `json:"status"`
	EventID string `json:"eventId"`
	Applied bool   `json:"applied"`
	Event   struct {
		Title     string `json:"title"`
		Organizer string `json:"organizer"`
	} `json:"event"`
}

type invitationView struct {
	Invitation *invitationState `json:"invitation"`
}

type eventView struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Organizer string `json:"organizer"`
	Attendees []struct {
		Address string `json:"address"`
		Status  string `json:"status"`
	} `json:"attendees"`
}

func TestMeetingInvitations(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	title := "Planning " + randomSuffix()
	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour).UTC()
	messagePath := func(mailbox string, uid uint32) string {
		return "/mailboxes/" + mailbox + "/messages/" + strconv.FormatUint(uint64(uid), 10)
	}
	rangeQuery := "/calendar/events?start=" + start.Add(-time.Hour).Format(time.RFC3339) + "&end=" + start.Add(3*time.Hour).Format(time.RFC3339)

	var created eventView
	w.alice.must("POST", "/calendar/events", map[string]any{
		"title": title, "start": start, "end": start.Add(time.Hour), "attendees": []string{"Bob <" + bob + ">"},
	}, &created, 201)
	if created.Organizer != alice || len(created.Attendees) != 1 || created.Attendees[0].Status != "needs-action" {
		t.Fatalf("organizer and attendee not recorded: %+v", created)
	}

	var inviteUID uint32
	waitFor(t, "invitation in bob's inbox", func() bool {
		inviteUID = findInAPI(w.bob, w.bobMailbox, "INBOX", "Invitation: "+title)
		return inviteUID != 0
	})
	var invite invitationView
	w.bob.must("GET", messagePath(w.bobMailbox, inviteUID), nil, &invite, 200)
	if invite.Invitation == nil || invite.Invitation.Method != "request" || invite.Invitation.Status != "needs-action" || invite.Invitation.Event.Organizer != alice {
		t.Fatalf("invitation not parsed: %+v", invite.Invitation)
	}

	var accepted invitationState
	w.bob.must("POST", messagePath(w.bobMailbox, inviteUID)+"/invitation", map[string]string{"response": "accepted"}, &accepted, 200)
	if accepted.Status != "accepted" || accepted.EventID == "" {
		t.Fatalf("accepting did not add the event: %+v", accepted)
	}
	var bobEvents []eventView
	w.bob.must("GET", rangeQuery, nil, &bobEvents, 200)
	if len(bobEvents) != 1 || bobEvents[0].Title != title {
		t.Fatalf("accepted event missing from bob's calendar: %+v", bobEvents)
	}

	var replyUID uint32
	waitFor(t, "reply in alice's inbox", func() bool {
		replyUID = findInAPI(w.alice, w.aliceMailbox, "INBOX", "Accepted: "+title)
		return replyUID != 0
	})
	var reply invitationView
	w.alice.must("GET", messagePath(w.aliceMailbox, replyUID), nil, &reply, 200)
	if reply.Invitation == nil || reply.Invitation.Method != "reply" || reply.Invitation.Status != "accepted" || reply.Invitation.Applied {
		t.Fatalf("reply not parsed: %+v", reply.Invitation)
	}
	var applied invitationState
	w.alice.must("POST", messagePath(w.aliceMailbox, replyUID)+"/invitation", map[string]string{"response": "apply"}, &applied, 200)
	if !applied.Applied {
		t.Fatalf("reply not applied: %+v", applied)
	}
	var aliceEvents []eventView
	w.alice.must("GET", rangeQuery, nil, &aliceEvents, 200)
	if len(aliceEvents) != 1 || len(aliceEvents[0].Attendees) != 1 || aliceEvents[0].Attendees[0].Status != "accepted" {
		t.Fatalf("attendee status not updated for the organizer: %+v", aliceEvents)
	}

	if code := w.bob.do("POST", messagePath(w.bobMailbox, inviteUID)+"/invitation", map[string]string{"response": "apply"}, nil); code != 422 {
		t.Fatalf("applying to a request should be rejected, got %d", code)
	}

	w.alice.must("DELETE", "/calendar/events/"+created.ID, nil, nil, 204)
	var cancelUID uint32
	waitFor(t, "cancellation in bob's inbox", func() bool {
		cancelUID = findInAPI(w.bob, w.bobMailbox, "INBOX", "Cancelled: "+title)
		return cancelUID != 0
	})
	var cancel invitationView
	w.bob.must("GET", messagePath(w.bobMailbox, cancelUID), nil, &cancel, 200)
	if cancel.Invitation == nil || cancel.Invitation.Method != "cancel" || cancel.Invitation.EventID == "" {
		t.Fatalf("cancellation not parsed: %+v", cancel.Invitation)
	}
	w.bob.must("POST", messagePath(w.bobMailbox, cancelUID)+"/invitation", map[string]string{"response": "remove"}, nil, 200)
	w.bob.must("GET", rangeQuery, nil, &bobEvents, 200)
	if len(bobEvents) != 0 {
		t.Fatalf("cancelled event still in bob's calendar: %+v", bobEvents)
	}
}
