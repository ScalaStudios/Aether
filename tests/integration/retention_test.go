//go:build integration

package integration

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type retentionRule struct {
	Folder string `json:"folder"`
	Days   int    `json:"days"`
}

type mailboxRetention struct {
	KeepDays int             `json:"keepDays"`
	Folders  []retentionRule `json:"folders"`
}

func TestRetentionPolicies(t *testing.T) {
	w := setupWorld(t)
	db := testDB(t)
	policies := "/organizations/" + w.orgID + "/retention-policies"
	ic, err := imapLogin(t, "alice@"+w.domain, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("imap login: %v", err)
	}
	defer ic.Close()
	now := time.Now()
	for subject, age := range map[string]int{"retention-old": 100, "retention-middle": 70, "retention-new": 0} {
		imapAppend(t, ic, "INBOX", rfc822("sender@external.example", "alice@"+w.domain, subject, "body"), now.AddDate(0, 0, -age))
	}
	imapAppend(t, ic, "Trash", rfc822("sender@external.example", "alice@"+w.domain, "retention-trash", "body"), now.AddDate(0, 0, -100))

	var effective mailboxRetention
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/retention", nil, &effective, 200)
	if want := (mailboxRetention{Folders: []retentionRule{{"Junk", 30}, {"Trash", 30}}}); !reflect.DeepEqual(effective, want) {
		t.Fatalf("default retention = %+v, want %+v", effective, want)
	}
	if code := w.alice.do("GET", policies, nil, nil); code != 403 {
		t.Fatalf("a member listing retention policies got %d, want 403", code)
	}
	if code := w.alice.do("POST", policies, map[string]any{"action": "keep", "days": 10}, nil); code != 403 {
		t.Fatalf("a member creating a retention policy got %d, want 403", code)
	}
	for _, bad := range []map[string]any{
		{"action": "keep"},
		{"action": "delete", "folder": "Shared/bob@" + w.domain + "/INBOX", "days": 10},
		{"action": "delete", "folder": "INBOX", "days": 0},
		{"action": "delete", "folder": "INBOX", "days": 36501},
		{"action": "archive", "folder": "INBOX", "days": 10},
		{"action": "keep", "days": 10, "mailboxId": "00000000-0000-0000-0000-000000000000"},
	} {
		if code := w.admin.do("POST", policies, bad, nil); code != 422 {
			t.Errorf("creating %v returned %d, want 422", bad, code)
		}
	}

	var inbox, keep, trash struct {
		ID string `json:"id"`
	}
	w.admin.must("POST", policies, map[string]any{"action": "keep", "days": 90, "mailboxId": w.aliceMailbox}, &keep, 201)
	w.admin.must("POST", policies, map[string]any{"action": "delete", "folder": "inbox", "days": 60}, &inbox, 201)
	if code := w.admin.do("POST", policies, map[string]any{"action": "delete", "folder": "INBOX", "days": 30}, nil); code != 409 {
		t.Fatalf("a second INBOX policy for all mailboxes returned %d, want 409", code)
	}
	w.admin.must("POST", policies, map[string]any{"action": "delete", "folder": "Missing/Folder", "days": 10}, nil, 201)
	w.admin.must("POST", policies, map[string]any{"action": "delete", "folder": "Trash", "mailboxId": w.aliceMailbox}, &trash, 201)

	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/retention", nil, &effective, 200)
	if want := (mailboxRetention{KeepDays: 90, Folders: []retentionRule{{"INBOX", 60}, {"Junk", 30}, {"Missing/Folder", 10}}}); !reflect.DeepEqual(effective, want) {
		t.Fatalf("alice's retention = %+v, want %+v", effective, want)
	}
	w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/retention", nil, &effective, 200)
	if want := (mailboxRetention{Folders: []retentionRule{{"INBOX", 60}, {"Junk", 30}, {"Missing/Folder", 10}, {"Trash", 30}}}); !reflect.DeepEqual(effective, want) {
		t.Fatalf("bob's retention = %+v, want %+v", effective, want)
	}

	ctx := context.Background()
	waitUpTo(t, 2*time.Minute, "retention to run for alice's mailbox", func() bool {
		var done bool
		return db.QueryRow(ctx, `SELECT next_run_at > now() + interval '23 hours' FROM retention_runs WHERE mailbox_id = $1`, w.aliceMailbox).Scan(&done) == nil && done
	})
	for subject, kept := range map[string]bool{"retention-old": false, "retention-middle": true, "retention-new": true} {
		if got := imapFindSubject(t, ic, "INBOX", subject) != nil; got != kept {
			t.Errorf("%s still in INBOX = %v, want %v", subject, got, kept)
		}
	}
	if imapFindSubject(t, ic, "Trash", "retention-trash") == nil {
		t.Error("a message moved to Trash just now was deleted")
	}

	w.admin.must("PATCH", "/retention-policies/"+keep.ID, map[string]any{"days": 30}, nil, 200)
	if code := w.admin.do("PATCH", "/retention-policies/"+keep.ID, map[string]any{"days": nil}, nil); code != 422 {
		t.Fatalf("clearing the days of a keep policy returned %d, want 422", code)
	}
	w.admin.must("PATCH", "/retention-policies/"+inbox.ID, map[string]any{"days": nil}, nil, 200)
	w.admin.must("DELETE", "/retention-policies/"+trash.ID, nil, nil, 204)
	if code := w.alice.do("DELETE", "/retention-policies/"+keep.ID, nil, nil); code != 403 {
		t.Fatalf("a member deleting a retention policy got %d, want 403", code)
	}
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/retention", nil, &effective, 200)
	if want := (mailboxRetention{KeepDays: 30, Folders: []retentionRule{{"Junk", 30}, {"Missing/Folder", 10}, {"Trash", 30}}}); !reflect.DeepEqual(effective, want) {
		t.Fatalf("alice's retention after the changes = %+v, want %+v", effective, want)
	}

	var audit []struct {
		Action string `json:"action"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &audit, 200)
	seen := map[string]int{}
	for _, e := range audit {
		seen[e.Action]++
	}
	if seen["retention_policy.create"] != 4 || seen["retention_policy.update"] != 2 || seen["retention_policy.remove"] != 1 {
		t.Fatalf("retention changes in the audit log: %v", seen)
	}
}
