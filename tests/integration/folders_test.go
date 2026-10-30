//go:build integration

package integration

import (
	"net/url"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type folderState struct {
	Name       string `json:"name"`
	Total      int    `json:"total"`
	Unread     int    `json:"unread"`
	Subscribed bool   `json:"subscribed"`
}

func folderStates(c *apiClient, mailbox string) map[string]folderState {
	var list []folderState
	c.must("GET", "/mailboxes/"+mailbox+"/folders", nil, &list, 200)
	out := map[string]folderState{}
	for _, f := range list {
		out[f.Name] = f
	}
	return out
}

func imapAppend(t *testing.T, c *imapclient.Client, folder, raw string, date time.Time, flags ...imap.Flag) {
	t.Helper()
	cmd := c.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: date})
	cmd.Write([]byte(raw))
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatalf("append to %s: %v", folder, err)
	}
}

func TestFolderManagement(t *testing.T) {
	w := setupWorld(t)
	base := "/mailboxes/" + w.aliceMailbox
	folders := base + "/folders"
	at := func(name string) string { return folders + "?folder=" + url.QueryEscape(name) }
	ic, err := imapLogin(t, "alice@"+w.domain, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("imap login: %v", err)
	}
	defer ic.Close()
	message := func(subject string) string {
		return rfc822("sender@external.example", "alice@"+w.domain, subject, "body")
	}

	var got struct{ Name string }
	w.alice.must("POST", folders, map[string]string{"name": "Clients"}, &got, 201)
	w.alice.must("POST", folders, map[string]string{"name": " Acme ", "parent": "Clients"}, &got, 201)
	if got.Name != "Clients/Acme" {
		t.Fatalf("created %q, want Clients/Acme", got.Name)
	}
	for _, name := range []string{"", "a/b", "v1.2", "~home", "50%"} {
		if code := w.alice.do("POST", folders, map[string]string{"name": name}, nil); code != 422 {
			t.Errorf("creating %q returned %d, want 422", name, code)
		}
	}
	if code := w.alice.do("POST", folders, map[string]string{"name": "Clients"}, nil); code != 409 {
		t.Errorf("a duplicate folder returned %d, want 409", code)
	}
	if code := w.alice.do("POST", folders, map[string]string{"name": "x", "parent": "Missing"}, nil); code != 404 {
		t.Errorf("a folder under a missing parent returned %d, want 404", code)
	}

	w.alice.must("PUT", base+"/rules", []map[string]any{{
		"name": "Acme", "enabled": true, "match": "any",
		"conditions": []map[string]string{{"field": "from", "contains": "acme.example"}}, "actions": map[string]any{"moveTo": "Clients/Acme"},
	}}, nil, 200)
	w.alice.must("PATCH", at("Clients"), map[string]string{"name": "Customers"}, &got, 200)
	states := folderStates(w.alice, w.aliceMailbox)
	if _, ok := states["Clients"]; ok || !states["Customers"].Subscribed || !states["Customers/Acme"].Subscribed {
		t.Fatalf("after renaming Clients to Customers: %+v", states)
	}
	var rules []struct {
		Actions struct {
			MoveTo string `json:"moveTo"`
		} `json:"actions"`
	}
	w.alice.must("GET", base+"/rules", nil, &rules, 200)
	if len(rules) != 1 || rules[0].Actions.MoveTo != "Customers/Acme" {
		t.Fatalf("rule after the rename: %+v", rules)
	}
	if code := w.alice.do("PATCH", at("Customers"), map[string]string{"parent": "Customers/Acme"}, nil); code != 422 {
		t.Errorf("moving a folder into its own subfolder returned %d, want 422", code)
	}
	w.alice.must("PATCH", at("Customers/Acme"), map[string]string{"parent": ""}, &got, 200)
	if got.Name != "Acme" {
		t.Fatalf("moved to %q, want Acme", got.Name)
	}
	for _, special := range []string{"INBOX", "Sent", "Trash"} {
		if code := w.alice.do("PATCH", at(special), map[string]string{"name": "Renamed"}, nil); code != 422 {
			t.Errorf("renaming %s returned %d, want 422", special, code)
		}
		if code := w.alice.do("DELETE", at(special), nil, nil); code != 422 {
			t.Errorf("deleting %s returned %d, want 422", special, code)
		}
	}

	imapAppend(t, ic, "Acme", message("filed "+randomSuffix()), time.Now())
	if code := w.alice.do("DELETE", at("Acme"), nil, nil); code != 409 {
		t.Errorf("deleting a folder with messages returned %d, want 409", code)
	}
	w.alice.must("DELETE", at("Acme")+"&moveTo=INBOX", nil, nil, 204)
	states = folderStates(w.alice, w.aliceMailbox)
	if _, ok := states["Acme"]; ok || states["INBOX"].Total != 1 {
		t.Fatalf("after deleting Acme into INBOX: %+v", states)
	}
	w.alice.must("POST", folders, map[string]string{"name": "Sub", "parent": "Customers"}, nil, 201)
	if code := w.alice.do("DELETE", at("Customers"), nil, nil); code != 409 {
		t.Errorf("deleting a folder with subfolders returned %d, want 409", code)
	}

	imapAppend(t, ic, "Customers", message("one"), time.Now())
	imapAppend(t, ic, "Customers", message("two"), time.Now())
	w.alice.must("POST", folders+"/mark-read?folder=Customers", nil, nil, 204)
	if s := folderStates(w.alice, w.aliceMailbox)["Customers"]; s.Total != 2 || s.Unread != 0 {
		t.Fatalf("after mark-read: %+v", s)
	}
	w.alice.must("POST", folders+"/empty?folder=Customers", nil, nil, 204)
	states = folderStates(w.alice, w.aliceMailbox)
	if states["Customers"].Total != 0 || states["Trash"].Total != 2 {
		t.Fatalf("emptying Customers should move its messages to Trash: %+v", states)
	}
	w.alice.must("POST", folders+"/empty?folder=Trash", nil, nil, 204)
	if s := folderStates(w.alice, w.aliceMailbox)["Trash"]; s.Total != 0 {
		t.Fatalf("Trash still has %d messages", s.Total)
	}

	w.alice.must("PATCH", at("Customers"), map[string]bool{"subscribed": false}, &got, 200)
	if folderStates(w.alice, w.aliceMailbox)["Customers"].Subscribed {
		t.Fatal("Customers is still subscribed")
	}
}
