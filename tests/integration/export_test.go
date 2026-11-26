//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type mailboxExport struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"`
	Error    string  `json:"error"`
	Messages int     `json:"messages"`
	Active   bool    `json:"active"`
	Purge    *string `json:"purgeAfter"`
	OnHold   bool    `json:"onHold"`
	Address  string  `json:"address"`
}

func TestMailboxExportAndDeletion(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	subject := "export me " + randomSuffix()
	w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/send", map[string]any{
		"to": []map[string]string{{"address": alice}}, "subject": subject, "text": "please archive this",
	}, nil, 202)
	waitFor(t, "alice to receive the message", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", subject) != 0 })
	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	w.alice.must("POST", "/calendar/events", map[string]any{"title": "Export rehearsal", "start": start, "end": end}, nil, 201)
	w.alice.must("POST", "/contacts", map[string]any{"name": "Exported Person", "emails": []string{"person@example.com"}}, nil, 201)

	if got := w.bob.do("POST", "/mailboxes/"+w.aliceMailbox+"/exports", map[string]any{"format": "zip"}, nil); got != 403 {
		t.Fatalf("bob exported alice's mailbox: %d", got)
	}
	w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/exports", map[string]any{"format": "zip", "folder": "INBOX"}, nil, 202)
	exp := waitExport(t, w.alice, w.aliceMailbox)
	if exp.Messages < 1 {
		t.Fatalf("export has %d messages", exp.Messages)
	}
	body := downloadExport(t, w.alice, w.aliceMailbox, exp.ID)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	var sawMail, sawCalendar, sawContact bool
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(rc)
		rc.Close()
		switch {
		case strings.HasPrefix(f.Name, "mail/INBOX/") && bytes.Contains(raw, []byte(subject)):
			sawMail = true
		case strings.HasPrefix(f.Name, "calendars/") && bytes.Contains(raw, []byte("Export rehearsal")):
			sawCalendar = true
		case strings.HasPrefix(f.Name, "contacts/") && bytes.Contains(raw, []byte("Exported Person")):
			sawContact = true
		}
	}
	if !sawMail || !sawCalendar || !sawContact {
		names := make([]string, len(zr.File))
		for i, f := range zr.File {
			names[i] = f.Name
		}
		t.Fatalf("archive missing mail=%v calendar=%v contacts=%v files=%v", sawMail, sawCalendar, sawContact, names)
	}

	mbox := map[string]any{"format": "mbox", "includeCalendar": false, "includeContacts": false}
	w.admin.must("POST", "/mailboxes/"+w.bobMailbox+"/exports", mbox, nil, 403)
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": alice, "role": "admin"}, nil, 204)
	w.alice.must("POST", "/mailboxes/"+w.bobMailbox+"/exports", mbox, nil, 202)
	waitExport(t, w.alice, w.bobMailbox)
	var events []struct {
		Action string `json:"action"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &events, 200)
	var saw bool
	for _, e := range events {
		if e.Action == "mailbox.export" {
			saw = true
		}
	}
	if !saw {
		t.Fatal("audit log has no mailbox.export event")
	}

	var carol struct {
		ID string `json:"id"`
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{
		"domainId": w.domainID, "localPart": "carol", "password": "carol-integration-pw",
	}, &carol, 201)
	w.admin.must("PATCH", "/mailboxes/"+carol.ID, map[string]bool{"onHold": true}, nil, 200)
	var held mailboxExport
	w.admin.must("DELETE", "/mailboxes/"+carol.ID, nil, &held, 200)
	if !held.OnHold || held.Purge != nil || held.Active {
		t.Fatalf("held delete: active=%v hold=%v purge=%v", held.Active, held.OnHold, held.Purge)
	}
	w.admin.must("PATCH", "/mailboxes/"+carol.ID, map[string]any{"onHold": false, "active": true}, nil, 200)
	var scheduled mailboxExport
	w.admin.must("DELETE", "/mailboxes/"+carol.ID+"?graceDays=30", nil, &scheduled, 200)
	if scheduled.Active || scheduled.Purge == nil {
		t.Fatalf("grace delete: active=%v purge=%v", scheduled.Active, scheduled.Purge)
	}
	var restored mailboxExport
	w.admin.must("POST", "/mailboxes/"+carol.ID+"/restore", nil, &restored, 200)
	if !restored.Active || restored.Purge != nil {
		t.Fatalf("restore: active=%v purge=%v", restored.Active, restored.Purge)
	}
	w.admin.must("DELETE", "/mailboxes/"+carol.ID+"?graceDays=0", nil, nil, 204)
	if got := w.admin.do("GET", "/organizations/"+w.orgID+"/mailboxes", nil, nil); got != 200 {
		t.Fatalf("list after delete: %d", got)
	}
	var left []struct {
		ID string `json:"id"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/mailboxes", nil, &left, 200)
	for _, m := range left {
		if m.ID == carol.ID {
			t.Fatal("carol's mailbox row survived an immediate delete")
		}
	}
}

func waitExport(t *testing.T, c *apiClient, mailbox string) mailboxExport {
	t.Helper()
	var list []mailboxExport
	waitUpTo(t, time.Minute, "the export to finish", func() bool {
		c.must("GET", "/mailboxes/"+mailbox+"/exports", nil, &list, 200)
		return len(list) > 0 && (list[0].Status == "ready" || list[0].Status == "failed")
	})
	if list[0].Status != "ready" {
		t.Fatalf("export %s: %s", list[0].Status, list[0].Error)
	}
	return list[0]
}

func downloadExport(t *testing.T, c *apiClient, mailbox, id string) []byte {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.base+"/mailboxes/"+mailbox+"/exports/"+id+"/download", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("download %d: %s", resp.StatusCode, body)
	}
	return body
}
