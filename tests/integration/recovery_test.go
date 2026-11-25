//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

type recoverableList struct {
	RetentionDays int `json:"retentionDays"`
	Items         []struct {
		Folder    string    `json:"folder"`
		UID       uint32    `json:"uid"`
		Subject   string    `json:"subject"`
		DeletedAt time.Time `json:"deletedAt"`
	} `json:"items"`
}

func TestRecoverDeletedItems(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	base := "/mailboxes/" + w.aliceMailbox
	recoverable := base + "/recoverable"
	ic, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("imap login: %v", err)
	}
	defer ic.Close()
	suffix := randomSuffix()
	trashed, phone, archived := "trashed "+suffix, "phone "+suffix, "archived "+suffix
	for _, s := range []string{trashed, archived} {
		imapAppend(t, ic, "INBOX", rfc822("sender@external.example", alice, s, "body"), time.Now())
	}
	imapAppend(t, ic, "Archive", rfc822("sender@external.example", alice, phone, "body"), time.Now())

	list := func(c *apiClient) recoverableList {
		var out recoverableList
		c.must("GET", recoverable, nil, &out, 200)
		return out
	}

	w.alice.must("POST", base+"/messages/"+itoaUID(findInAPI(w.alice, w.aliceMailbox, "INBOX", archived))+"/move", map[string]any{"to": "Archive"}, nil, 204)
	w.alice.must("DELETE", base+"/messages/"+itoaUID(findInAPI(w.alice, w.aliceMailbox, "INBOX", trashed))+"?folder=INBOX", nil, nil, 204)
	var draft struct{ UID uint32 }
	w.alice.must("PUT", base+"/drafts", map[string]any{"subject": "draft " + suffix}, &draft, 200)
	w.alice.must("PUT", base+"/drafts", map[string]any{"subject": "draft " + suffix, "draftUid": draft.UID}, &draft, 200)
	if got := list(w.alice); len(got.Items) != 0 || got.RetentionDays != 14 {
		t.Fatalf("moving, deleting to Trash and saving drafts must not create recoverable items: %+v", got)
	}

	w.alice.must("POST", base+"/folders/empty?folder=Trash", nil, nil, 204)
	if _, err := ic.Select("Archive", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := ic.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: phone}}}, nil).Wait()
	if err != nil || len(data.AllUIDs()) != 1 {
		t.Fatalf("finding %q: %v", phone, err)
	}
	uids := imap.UIDSetNum(data.AllUIDs()...)
	ic.Store(uids, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close()
	if err := ic.UIDExpunge(uids).Close(); err != nil {
		t.Fatal(err)
	}
	sneak := ic.Append(".EXPUNGED/Archive", 3, nil)
	sneak.Write([]byte("x\r\n"))
	sneak.Close()
	if _, err := sneak.Wait(); err == nil {
		t.Fatal("a mail app could store messages in the quota-free deleted items folders")
	}

	got := list(w.alice)
	folders := map[string]string{}
	for _, it := range got.Items {
		folders[it.Subject] = it.Folder
		if time.Since(it.DeletedAt) > time.Hour {
			t.Errorf("%q deletedAt = %v, want the time it was purged", it.Subject, it.DeletedAt)
		}
	}
	if len(got.Items) != 2 || folders[trashed] != "Trash" || folders[phone] != "Archive" {
		t.Fatalf("recoverable items = %+v", got.Items)
	}
	if code := w.bob.do("GET", recoverable, nil, nil); code != 404 {
		t.Errorf("a colleague listed alice's deleted items: %d", code)
	}
	for _, body := range []any{
		map[string]any{"items": []any{}},
		map[string]any{"items": []map[string]any{{"folder": "Shared/x/INBOX", "uid": 1}}},
		map[string]any{"items": []map[string]any{{"folder": ".EXPUNGED/INBOX", "uid": 1}}},
	} {
		if code := w.alice.do("POST", recoverable+"/restore", body, nil); code != 422 {
			t.Errorf("restoring %v returned %d, want 422", body, code)
		}
	}

	var restored struct{ Restored int }
	for _, it := range got.Items {
		if it.Subject == trashed {
			w.alice.must("POST", recoverable+"/restore", map[string]any{"items": []map[string]any{{"folder": it.Folder, "uid": it.UID}}}, &restored, 200)
		}
	}
	if restored.Restored != 1 || findInAPI(w.alice, w.aliceMailbox, "Trash", trashed) == 0 {
		t.Fatalf("restoring %q by its owner: %+v", trashed, restored)
	}

	byAdmin := list(w.admin)
	if len(byAdmin.Items) != 1 || byAdmin.Items[0].Subject != phone {
		t.Fatalf("administrator sees %+v", byAdmin.Items)
	}
	item := map[string]any{"folder": byAdmin.Items[0].Folder, "uid": byAdmin.Items[0].UID}
	w.admin.must("POST", recoverable+"/restore", map[string]any{"items": []any{item}}, &restored, 200)
	if restored.Restored != 1 || findInAPI(w.alice, w.aliceMailbox, "Archive", phone) == 0 {
		t.Fatalf("restoring %q by an administrator: %+v", phone, restored)
	}
	if code := w.admin.do("POST", recoverable+"/restore", map[string]any{"items": []any{item}}, nil); code != 404 {
		t.Errorf("restoring an already restored message returned %d, want 404", code)
	}
	if got := list(w.alice); len(got.Items) != 0 {
		t.Fatalf("restored messages are still listed: %+v", got.Items)
	}

	var audit []struct {
		Action     string  `json:"action"`
		ActorEmail *string `json:"actorEmail"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &audit, 200)
	seen := map[string]int{}
	for _, e := range audit {
		if e.Action == "mailbox.messages.restore" && e.ActorEmail != nil && *e.ActorEmail != alice {
			seen["admin restore"]++
		}
		seen[e.Action]++
	}
	if seen["mailbox.messages.restore"] != 2 || seen["admin restore"] != 1 || seen["mailbox.recoverable.view"] == 0 {
		t.Fatalf("recovery is missing from the audit log: %v", seen)
	}

	org := "/organizations/" + w.orgID
	for _, days := range []int{0, 31} {
		if code := w.admin.do("PATCH", org, map[string]int{"deletedItemRetentionDays": days}, nil); code != 422 {
			t.Errorf("a retention of %d days returned %d, want 422", days, code)
		}
	}
	if code := w.alice.do("PATCH", org, map[string]int{"deletedItemRetentionDays": 7}, nil); code != 403 {
		t.Errorf("a member changed the retention: %d", code)
	}
	var updated struct {
		DeletedItemRetentionDays int `json:"deletedItemRetentionDays"`
	}
	w.admin.must("PATCH", org, map[string]int{"deletedItemRetentionDays": 7}, &updated, 200)
	if updated.DeletedItemRetentionDays != 7 || list(w.alice).RetentionDays != 7 {
		t.Fatalf("retention after the update = %+v", updated)
	}
}
