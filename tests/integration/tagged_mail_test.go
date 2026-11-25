//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
)

func TestTaggedMail(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	setting := "/mailboxes/" + w.aliceMailbox + "/tagged-mail"
	deliver := func(t *testing.T, to string) (string, error) {
		t.Helper()
		subject := "tagged " + randomSuffix()
		return subject, submit(t, smtpAddr, "", "", "sender@external.example", []string{to}, rfc822("sender@external.example", to, subject, "tagged mail"), false)
	}
	mustDeliver := func(t *testing.T, to string) string {
		t.Helper()
		subject, err := deliver(t, to)
		if err != nil {
			t.Fatalf("delivery to %s: %v", to, err)
		}
		return subject
	}
	setMode := func(t *testing.T, mode string) {
		t.Helper()
		var got struct{ Mode string }
		w.alice.must("PUT", setting, map[string]string{"mode": mode}, &got, 200)
		if got.Mode != mode {
			t.Fatalf("saved mode %q, want %q", got.Mode, mode)
		}
	}

	t.Run("new addresses cannot contain the delimiter", func(t *testing.T) {
		if code := errorCode(t, w.admin, "POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "sales+eu"}, 422); code != "invalid_local_part" {
			t.Fatalf("mailbox with a plus sign: %q", code)
		}
		if code := errorCode(t, w.admin, "POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "sales+eu", "destinations": []string{alice}}, 422); code != "invalid_local_part" {
			t.Fatalf("alias with a plus sign: %q", code)
		}
	})

	t.Run("the setting defaults to the inbox and rejects unknown modes", func(t *testing.T) {
		var got struct{ Mode string }
		w.alice.must("GET", setting, nil, &got, 200)
		if got.Mode != "inbox" {
			t.Fatalf("default mode %q", got.Mode)
		}
		if code := errorCode(t, w.alice, "PUT", setting, map[string]string{"mode": "folder"}, 422); code != "invalid_mode" {
			t.Fatalf("unknown mode: %q", code)
		}
	})

	t.Run("inbox mode keeps tagged mail in the inbox", func(t *testing.T) {
		w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/folders", map[string]string{"name": "news"}, nil, 201)
		subject := mustDeliver(t, "alice+news@"+w.domain)
		waitFor(t, "tagged mail in the inbox", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", subject) != 0 })
		if total := folderStates(w.alice, w.aliceMailbox)["news"].Total; total != 0 {
			t.Fatalf("inbox mode filed %d messages into the tag folder", total)
		}
	})

	t.Run("create folder mode files into a new folder named after the tag", func(t *testing.T) {
		setMode(t, "create_folder")
		subject := mustDeliver(t, "alice+Shop@"+w.domain)
		waitFor(t, "tagged mail in the Shop folder", func() bool {
			f, ok := folderStates(w.alice, w.aliceMailbox)["Shop"]
			return ok && f.Subscribed && findInAPI(w.alice, w.aliceMailbox, "Shop", subject) != 0
		})
		dotted := mustDeliver(t, "alice+a.b@"+w.domain)
		waitFor(t, "a tag with a dot in the inbox", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", dotted) != 0 })
		if _, ok := folderStates(w.alice, w.aliceMailbox)["a.b"]; ok {
			t.Fatal("a tag with a dot created a folder")
		}
	})

	t.Run("existing folder mode files only into folders that exist", func(t *testing.T) {
		setMode(t, "existing_folder")
		w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/folders", map[string]string{"name": "Receipts"}, nil, 201)
		filed := mustDeliver(t, "alice+Receipts@"+w.domain)
		waitFor(t, "tagged mail in Receipts", func() bool { return findInAPI(w.alice, w.aliceMailbox, "Receipts", filed) != 0 })
		missing := mustDeliver(t, "alice+missing@"+w.domain)
		waitFor(t, "mail for a missing folder in the inbox", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", missing) != 0 })
		if _, ok := folderStates(w.alice, w.aliceMailbox)["missing"]; ok {
			t.Fatal("existing folder mode created a folder")
		}
	})

	t.Run("reject mode refuses tagged mail and keeps plain mail and plus aliases", func(t *testing.T) {
		db := testDB(t)
		if _, err := db.Exec(context.Background(), `INSERT INTO aliases (org_id, domain_id, local_part, address, destinations) VALUES ($1, $2, 'alice+eu', $3, $4)`,
			w.orgID, w.domainID, "alice+eu@"+w.domain, []string{bob}); err != nil {
			t.Fatalf("insert a legacy alias: %v", err)
		}
		setMode(t, "reject")
		if _, err := deliver(t, "alice+spam@"+w.domain); err == nil || !strings.Contains(err.Error(), "550") {
			t.Fatalf("tagged mail in reject mode: %v", err)
		}
		tagged := "alice+self@" + w.domain
		if err := submit(t, env(t, "AETHER_TEST_SUBMISSION", "postfix:587"), alice, w.aliceAppPassword, alice, []string{tagged}, rfc822(alice, tagged, "self", "x"), false); err == nil || !strings.Contains(err.Error(), "550") {
			t.Fatalf("submitted tagged mail in reject mode: %v", err)
		}
		plain := mustDeliver(t, alice)
		waitFor(t, "plain mail in reject mode", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", plain) != 0 })
		legacy := mustDeliver(t, "alice+eu@"+w.domain)
		waitFor(t, "the legacy plus alias to reach bob", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", legacy) != 0 })
	})

	t.Run("alias domain follows the destination mailbox setting", func(t *testing.T) {
		aliasName := "alias" + randomSuffix() + ".test"
		w.admin.must("POST", "/organizations/"+w.orgID+"/domains", map[string]any{"name": aliasName, "aliasOf": w.domainID}, nil, 201)
		tagged := "alice+partner@" + aliasName

		setMode(t, "reject")
		if _, err := deliver(t, tagged); err == nil || !strings.Contains(err.Error(), "does not accept tagged addresses") {
			t.Fatalf("tagged alias domain mail in reject mode: %v", err)
		}

		setMode(t, "create_folder")
		filed := mustDeliver(t, tagged)
		waitFor(t, "tagged alias domain mail in the partner folder", func() bool {
			return findInAPI(w.alice, w.aliceMailbox, "partner", filed) != 0
		})

		setMode(t, "inbox")
		inbox := mustDeliver(t, tagged)
		waitFor(t, "tagged alias domain mail in the inbox", func() bool {
			return findInAPI(w.alice, w.aliceMailbox, "INBOX", inbox) != 0
		})
	})

	t.Run("a tag on an alias follows the destination mailbox setting", func(t *testing.T) {
		w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "team", "destinations": []string{alice}}, nil, 201)
		setMode(t, "create_folder")
		subject := mustDeliver(t, "team+project@"+w.domain)
		waitFor(t, "aliased tagged mail in the project folder", func() bool { return findInAPI(w.alice, w.aliceMailbox, "project", subject) != 0 })
		setMode(t, "inbox")
		inbox := mustDeliver(t, "team+project@"+w.domain)
		waitFor(t, "aliased tagged mail in the inbox", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", inbox) != 0 })
	})
}
