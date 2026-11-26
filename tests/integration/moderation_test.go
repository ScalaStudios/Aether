//go:build integration

package integration

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type heldBody struct {
	ID          string `json:"id"`
	ListAddress string `json:"listAddress"`
	Sender      string `json:"sender"`
	From        string `json:"from"`
	Subject     string `json:"subject"`
	Preview     string `json:"preview"`
}

func listCopy(t *testing.T, c *imapclient.Client, subject, list string) bool {
	t.Helper()
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("select INBOX: %v", err)
	}
	data, err := c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: subject}, {Key: "X-Aether-List", Value: list}}}, nil).Wait()
	return err == nil && len(data.AllUIDs()) > 0
}

func TestListModeration(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	bobID := userID(t, w.bob)
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": bob, "role": "member"}, nil, 204)
	var ap struct {
		Password string `json:"password"`
	}
	w.bob.must("POST", "/me/app-passwords", map[string]string{"name": "integration", "mailboxId": w.bobMailbox}, &ap, 201)

	if code := w.admin.do("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{
		"domainId": w.domainID, "localPart": "nobody-" + randomSuffix(), "kind": "list", "destinations": []string{alice}, "moderated": true,
	}, nil); code != 422 {
		t.Fatalf("moderated list without moderators or owners: %d, want 422", code)
	}
	var list aliasBody
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{
		"domainId": w.domainID, "localPart": "all", "kind": "list", "name": "Everyone",
		"destinations": []string{alice}, "acceptFrom": "anyone", "owners": []string{bobID},
		"moderated": true, "moderationExempt": []string{"@partner.example"},
	}, &list, 201)

	c, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()

	held := func() []heldBody {
		var out []heldBody
		w.bob.must("GET", "/me/held-messages", nil, &out, 200)
		return out
	}
	waitHeld := func(subject string) heldBody {
		var found heldBody
		waitFor(t, "held "+subject, func() bool {
			for _, h := range held() {
				if h.Subject == subject {
					found = h
					return true
				}
			}
			return false
		})
		return found
	}

	approved := "approve me " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "outsider@external.example", []string{list.Address}, rfc822("outsider@external.example", list.Address, approved, "please share"), false); err != nil {
		t.Fatalf("outside sender to a moderated list: %v", err)
	}
	h := waitHeld(approved)
	if h.ListAddress != list.Address || !strings.Contains(h.Preview, "please share") || !strings.Contains(h.From, "outsider@external.example") {
		t.Fatalf("held message %+v", h)
	}
	var mine []heldBody
	w.alice.must("GET", "/me/held-messages", nil, &mine, 200)
	if len(mine) != 0 {
		t.Fatalf("a member who does not moderate sees held messages: %+v", mine)
	}
	if code := w.alice.do("POST", "/held-messages/"+h.ID+"/approve", nil, nil); code != 404 {
		t.Fatalf("non-moderator approved: %d, want 404", code)
	}
	if imapFindSubject(t, c, "INBOX", approved) != nil {
		t.Fatal("a held message reached the members before approval")
	}
	if raw := w.bob.raw("GET", "/held-messages/"+h.ID+"/raw"); !strings.Contains(string(raw), "please share") {
		t.Fatalf("raw download %q", raw)
	}
	w.bob.must("POST", "/held-messages/"+h.ID+"/approve", nil, nil, 204)
	waitFor(t, "approved delivery", func() bool { return imapFindSubject(t, c, "INBOX", approved) != nil })
	if code := w.bob.do("POST", "/held-messages/"+h.ID+"/approve", nil, nil); code != 404 {
		t.Fatalf("second approval: %d, want 404", code)
	}

	rejected := "reject me " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "outsider@external.example", []string{list.Address}, rfc822("outsider@external.example", list.Address, rejected, "x"), false); err != nil {
		t.Fatal(err)
	}
	h = waitHeld(rejected)
	w.bob.must("POST", "/held-messages/"+h.ID+"/reject", map[string]string{"reason": "Off topic"}, nil, 204)
	for _, m := range held() {
		if m.ID == h.ID {
			t.Fatal("rejected message is still held")
		}
	}

	direct := "owner " + randomSuffix()
	if err := submit(t, submissionAddr, bob, ap.Password, bob, []string{list.Address}, rfc822(bob, list.Address, direct, "x"), false); err != nil {
		t.Fatalf("owner sending to the list: %v", err)
	}
	waitFor(t, "owner delivery", func() bool { return imapFindSubject(t, c, "INBOX", direct) != nil })

	partner := "partner " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "ceo@partner.example", []string{list.Address}, rfc822("ceo@partner.example", list.Address, partner, "x"), false); err != nil {
		t.Fatalf("exempt outside sender: %v", err)
	}
	waitFor(t, "exempt delivery", func() bool { return imapFindSubject(t, c, "INBOX", partner) != nil })

	err = submit(t, smtpAddr, "", "", bob, []string{list.Address}, rfc822(bob, list.Address, "spoof", "x"), false)
	if err == nil || !strings.Contains(err.Error(), "does not accept mail") {
		t.Fatalf("unauthenticated sender claiming the owner's address: %v", err)
	}

	var audit []struct {
		Action string `json:"action"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &audit, 200)
	var actions []string
	for _, e := range audit {
		actions = append(actions, e.Action)
	}
	if !strings.Contains(strings.Join(actions, " "), "alias.message_approve") || !strings.Contains(strings.Join(actions, " "), "alias.message_reject") {
		t.Fatalf("audit actions %v", actions)
	}

	var off aliasBody
	w.admin.must("PATCH", "/aliases/"+list.ID, map[string]any{"moderated": false}, &off, 200)
	after := "unmoderated " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "outsider@external.example", []string{list.Address}, rfc822("outsider@external.example", list.Address, after, "x"), false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "unmoderated delivery", func() bool { return imapFindSubject(t, c, "INBOX", after) != nil })
}

func TestModeratedListFromSCIMGroup(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	aliceID, bobID := userID(t, w.alice), userID(t, w.bob)
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": bob, "role": "member"}, nil, 204)

	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider struct {
		ID string `json:"id"`
	}
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret,
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)
	var enabled struct {
		Token string `json:"token"`
	}
	w.admin.must("POST", "/admin/identity-providers/"+provider.ID+"/scim-token", nil, &enabled, 200)
	halo := newClient(t)
	halo.bearer = enabled.Token
	linkToProvider(t, f, slug, "alice@"+w.domain, "bob@"+w.domain)
	var group scimGroupBody
	halo.must("POST", "/scim/"+slug+"/Groups", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": "Everyone " + randomSuffix(), "members": []map[string]string{{"value": aliceID}},
	}, &group, 201)

	var list aliasBody
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{
		"domainId": w.domainID, "localPart": "everyone", "kind": "list", "groups": []string{group.ID}, "owners": []string{bobID}, "moderated": true,
	}, &list, 201)

	held := func() []heldBody {
		var out []heldBody
		w.bob.must("GET", "/me/held-messages", nil, &out, 200)
		return out
	}
	waitHeld := func(subject string) heldBody {
		var found heldBody
		waitFor(t, "held "+subject, func() bool {
			all := held()
			i := slices.IndexFunc(all, func(h heldBody) bool { return h.Subject == subject })
			if i >= 0 {
				found = all[i]
			}
			return i >= 0
		})
		return found
	}
	gone := func(id string) bool { return !slices.ContainsFunc(held(), func(h heldBody) bool { return h.ID == id }) }

	c, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()

	approved := "group approve " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "outsider@external.example", []string{list.Address}, rfc822("outsider@external.example", list.Address, approved, "x"), false); err != nil {
		t.Fatalf("outside sender to a moderated group list: %v", err)
	}
	h := waitHeld(approved)
	if listCopy(t, c, approved, list.Address) {
		t.Fatal("a group member received a held message")
	}
	halo.must("PATCH", "/scim/"+slug+"/Groups/"+group.ID, map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "Add", "path": "members", "value": []map[string]string{{"value": bobID}}}},
	}, nil, 200)
	w.bob.must("POST", "/held-messages/"+h.ID+"/approve", nil, nil, 204)
	waitFor(t, "approved delivery to the group member", func() bool { return listCopy(t, c, approved, list.Address) })
	var ap struct {
		Password string `json:"password"`
	}
	w.bob.must("POST", "/me/app-passwords", map[string]string{"name": "integration", "mailboxId": w.bobMailbox}, &ap, 201)
	bc, err := imapLogin(t, bob, ap.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Logout()
	waitFor(t, "approved delivery to the member added while it was held", func() bool { return listCopy(t, bc, approved, list.Address) })

	rejected := "group reject " + randomSuffix()
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{list.Address}, rfc822(alice, list.Address, rejected, "x"), false); err != nil {
		t.Fatalf("group member sending to a moderated list: %v", err)
	}
	h = waitHeld(rejected)
	w.bob.must("POST", "/held-messages/"+h.ID+"/reject", map[string]string{"reason": "Use the team channel"}, nil, 204)
	waitFor(t, "rejection notice", func() bool {
		body := imapFindSubject(t, c, "INBOX", "Not delivered: "+rejected)
		return body != nil && strings.Contains(string(body), "Use the team channel")
	})
	if listCopy(t, c, rejected, list.Address) || listCopy(t, bc, rejected, list.Address) {
		t.Fatal("a rejected message reached the members")
	}

	expired := "group expire " + randomSuffix()
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{list.Address}, rfc822(alice, list.Address, expired, "x"), false); err != nil {
		t.Fatal(err)
	}
	h = waitHeld(expired)
	if _, err := testDB(t).Exec(context.Background(), `UPDATE held_messages SET expires_at = now() - interval '1 second' WHERE id = $1`, h.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "expiry", func() bool { return gone(h.ID) })
	waitFor(t, "expiry notice", func() bool {
		body := imapFindSubject(t, c, "INBOX", "Not delivered: "+expired)
		return body != nil && strings.Contains(string(body), "within 5 days")
	})
	if listCopy(t, c, expired, list.Address) || listCopy(t, bc, expired, list.Address) {
		t.Fatal("an expired message reached the members")
	}
}
