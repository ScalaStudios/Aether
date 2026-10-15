//go:build integration

package integration

import (
	"strconv"
	"testing"
)

type relatedView struct {
	MessageID string `json:"messageId"`
	Related   []struct {
		Folder string `json:"folder"`
		UID    uint32 `json:"uid"`
	} `json:"related"`
}

func TestConversationAcrossSent(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	subject := "Lunch " + randomSuffix()
	open := func(c *apiClient, mailbox, folder string, uid uint32) relatedView {
		var v relatedView
		c.must("GET", "/mailboxes/"+mailbox+"/messages/"+strconv.FormatUint(uint64(uid), 10)+"?folder="+folder, nil, &v, 200)
		return v
	}

	w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "Thursday?"}, nil, 202)
	var questionUID uint32
	waitFor(t, "bob to receive the question", func() bool { questionUID = findInAPI(w.bob, w.bobMailbox, "INBOX", subject); return questionUID != 0 })
	question := open(w.bob, w.bobMailbox, "INBOX", questionUID)
	if len(question.Related) != 0 {
		t.Fatalf("nothing in bob's Sent belongs to this conversation yet: %+v", question.Related)
	}

	w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/send", map[string]any{
		"to": []map[string]string{{"address": alice}}, "subject": "Re: " + subject, "text": "Works for me", "inReplyTo": question.MessageID,
	}, nil, 202)
	var replyUID uint32
	waitFor(t, "alice to receive the reply", func() bool {
		replyUID = findInAPI(w.alice, w.aliceMailbox, "INBOX", "Re: "+subject)
		return replyUID != 0
	})

	reply := open(w.alice, w.aliceMailbox, "INBOX", replyUID)
	if len(reply.Related) != 1 || reply.Related[0].Folder != "Sent" {
		t.Fatalf("alice's original question in Sent should be part of the conversation: %+v", reply.Related)
	}
	question = open(w.bob, w.bobMailbox, "INBOX", questionUID)
	if len(question.Related) != 1 || question.Related[0].Folder != "Sent" {
		t.Fatalf("bob's reply in Sent should be part of the conversation: %+v", question.Related)
	}
	sentReply := open(w.bob, w.bobMailbox, "Sent", question.Related[0].UID)
	if len(sentReply.Related) != 1 || sentReply.Related[0].Folder != "INBOX" || sentReply.Related[0].UID != questionUID {
		t.Fatalf("reading the reply in Sent should link back to the question in the Inbox: %+v", sentReply.Related)
	}
}
