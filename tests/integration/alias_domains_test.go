//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAliasDomains(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	aliasName := "alias" + randomSuffix() + ".test"

	type domainBody struct {
		ID          string  `json:"id"`
		Verified    bool    `json:"verified"`
		AliasOf     *string `json:"aliasOf"`
		AliasSendAs bool    `json:"aliasSendAs"`
	}
	var ad domainBody
	w.admin.must("POST", "/organizations/"+w.orgID+"/domains", map[string]any{"name": aliasName, "aliasOf": w.domainID}, &ad, 201)
	if !ad.Verified || ad.AliasOf == nil || *ad.AliasOf != w.domainID || ad.AliasSendAs {
		t.Fatalf("created alias domain %+v", ad)
	}

	if code := w.admin.do("POST", "/organizations/"+w.orgID+"/domains", map[string]any{"name": "chain" + randomSuffix() + ".test", "aliasOf": ad.ID}, nil); code != 422 {
		t.Fatalf("alias domain of an alias domain: %d, want 422", code)
	}
	if code := w.admin.do("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": ad.ID, "localPart": "carol"}, nil); code != 422 {
		t.Fatalf("mailbox on an alias domain: %d, want 422", code)
	}
	if code := w.admin.do("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": ad.ID, "localPart": "", "destinations": []string{alice}}, nil); code != 422 {
		t.Fatalf("catch-all on an alias domain: %d, want 422", code)
	}
	if code := w.admin.do("PATCH", "/domains/"+w.domainID, map[string]any{"aliasSendAs": true}, nil); code != 422 {
		t.Fatalf("send-as on a regular domain: %d, want 422", code)
	}
	if code := w.admin.do("DELETE", "/domains/"+w.domainID, nil, nil); code != 409 {
		t.Fatalf("deleting a domain with alias domains: %d, want 409", code)
	}

	var team, old, staff aliasBody
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "team", "destinations": []string{alice}}, &team, 201)
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "old", "destinations": []string{alice}}, &old, 201)
	w.admin.must("PATCH", "/aliases/"+old.ID, map[string]any{"active": false}, nil, 200)
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{
		"domainId": w.domainID, "localPart": "staff", "kind": "list", "name": "Staff", "destinations": []string{alice}, "acceptFrom": "organization",
	}, &staff, 201)
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "", "destinations": []string{alice}}, nil, 201)

	toBob := "to bob " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "sender@external.example", []string{"bob@" + aliasName}, rfc822("sender@external.example", "bob@"+aliasName, toBob, "x"), false); err != nil {
		t.Fatalf("mail to a mailbox on the alias domain: %v", err)
	}
	toTeam := "to team " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "sender@external.example", []string{"team@" + aliasName}, rfc822("sender@external.example", "team@"+aliasName, toTeam, "x"), false); err != nil {
		t.Fatalf("mail to an alias on the alias domain: %v", err)
	}
	for _, rcpt := range []string{"nobody@" + aliasName, "old@" + aliasName} {
		if err := submit(t, smtpAddr, "", "", "sender@external.example", []string{rcpt}, rfc822("sender@external.example", rcpt, "unknown", "x"), false); err == nil {
			t.Fatalf("%s was accepted although the target address is unknown or disabled", rcpt)
		}
	}
	err := submit(t, smtpAddr, "", "", "outsider@external.example", []string{"staff@" + aliasName}, rfc822("outsider@external.example", "staff@"+aliasName, "outside", "x"), false)
	if err == nil || !strings.Contains(err.Error(), "does not accept mail") {
		t.Fatalf("outside sender to an organization-only list through the alias domain: %v", err)
	}
	waitFor(t, "delivery to bob through the alias domain", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", toBob) != 0 })
	waitFor(t, "delivery to the alias through the alias domain", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", toTeam) != 0 })

	aliasAlice := "alice@" + aliasName
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, aliasAlice, []string{bob}, rfc822(aliasAlice, bob, "blocked", "x"), false); err == nil {
		t.Fatal("sent as the alias domain address before send-as was allowed")
	}
	w.admin.must("PATCH", "/domains/"+ad.ID, map[string]any{"aliasSendAs": true}, &ad, 200)
	if !ad.AliasSendAs {
		t.Fatalf("PATCH aliasSendAs: %+v", ad)
	}
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, "bob@"+aliasName, []string{alice}, rfc822("bob@"+aliasName, alice, "spoof", "x"), false); err == nil {
		t.Fatal("alice sent as bob's alias domain address")
	}
	sent := "as alias " + randomSuffix()
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, aliasAlice, []string{bob}, rfc822(aliasAlice, bob, sent, "x"), false); err != nil {
		t.Fatalf("send as the alias domain address: %v", err)
	}
	waitFor(t, "bob to receive mail sent from the alias domain", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", sent) != 0 })
	ic, err := imapLogin(t, bob, createAppPassword(t, w.bob, w.bobMailbox))
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	if raw := imapFindSubject(t, ic, "INBOX", sent); !bytes.Contains(raw, []byte("d="+aliasName)) {
		t.Fatalf("mail from the alias domain is not DKIM signed for %s:\n%s", aliasName, raw)
	}

	w.admin.must("DELETE", "/domains/"+ad.ID, nil, nil, 204)
	if err := submit(t, smtpAddr, "", "", "sender@external.example", []string{"bob@" + aliasName}, rfc822("sender@external.example", "bob@"+aliasName, "gone", "x"), false); err == nil {
		t.Fatal("mail to a deleted alias domain was accepted")
	}
}

func TestAliasDomainModeratedGroupListAndDMARC(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	aliceID, bobID := userID(t, w.alice), userID(t, w.bob)
	aliasName := "alias" + randomSuffix() + ".test"
	var ad idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/domains", map[string]any{"name": aliasName, "aliasOf": w.domainID}, &ad, 201)

	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider idOnly
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
	var group scimGroupBody
	halo.must("POST", "/scim/"+slug+"/Groups", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": "Everyone " + randomSuffix(), "members": []map[string]string{{"value": aliceID}},
	}, &group, 201)

	var list aliasBody
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{
		"domainId": w.domainID, "localPart": "everyone", "kind": "list", "groups": []string{group.ID}, "owners": []string{bobID}, "moderated": true,
		"acceptFrom": "allowlist", "allowedSenders": []string{"@partner.example"},
	}, &list, 201)
	aliasList := "everyone@" + aliasName

	for _, rcpt := range []string{aliasList, list.Address} {
		err := submit(t, smtpAddr, "", "", "outsider@external.example", []string{rcpt}, rfc822("outsider@external.example", rcpt, "outside", "x"), false)
		if err == nil || !strings.Contains(err.Error(), "does not accept mail") {
			t.Fatalf("sender outside the allow-list to %s: %v", rcpt, err)
		}
	}
	if err := submit(t, smtpAddr, "", "", "alice@"+aliasName, []string{aliasList}, rfc822("alice@"+aliasName, aliasList, "spoof", "x"), false); err == nil {
		t.Fatal("unauthenticated sender claiming an alias domain address reached a moderated list")
	}

	subject := "alias moderated " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "ceo@partner.example", []string{aliasList}, rfc822("ceo@partner.example", aliasList, subject, "x"), false); err != nil {
		t.Fatalf("allow-listed sender to the moderated list through the alias domain: %v", err)
	}
	var h heldBody
	waitFor(t, "the message to be held", func() bool {
		var all []heldBody
		w.bob.must("GET", "/me/held-messages", nil, &all, 200)
		i := slices.IndexFunc(all, func(x heldBody) bool { return x.Subject == subject })
		if i >= 0 {
			h = all[i]
		}
		return i >= 0
	})
	c, err := imapLogin(t, "alice@"+w.domain, w.aliceAppPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	if listCopy(t, c, subject, list.Address) {
		t.Fatal("a held message reached a member")
	}
	halo.must("PATCH", "/scim/"+slug+"/Groups/"+group.ID, map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "Add", "path": "members", "value": []map[string]string{{"value": bobID}}}},
	}, nil, 200)
	w.bob.must("POST", "/held-messages/"+h.ID+"/approve", nil, nil, 204)
	waitFor(t, "approved delivery to the original member", func() bool { return listCopy(t, c, subject, list.Address) })
	bc, err := imapLogin(t, "bob@"+w.domain, createAppPassword(t, w.bob, w.bobMailbox))
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Logout()
	waitFor(t, "approved delivery to the member added while held", func() bool { return listCopy(t, bc, subject, list.Address) })

	var reports idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "dmarc-reports", "password": "dmarc-integration-pw"}, &reports, 201)
	now := time.Now().Unix()
	xml := fmt.Sprintf(`<?xml version="1.0"?><feedback><report_metadata><org_name>receiver.example</org_name><report_id>a-%s</report_id>
<date_range><begin>%d</begin><end>%d</end></date_range></report_metadata><policy_published><domain>%s</domain><p>quarantine</p></policy_published>
<record><row><source_ip>198.51.100.8</source_ip><count>3</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>pass</spf></policy_evaluated></row><identifiers><header_from>%s</header_from></identifiers></record>
</feedback>`, randomSuffix(), now-86400, now-60, aliasName, aliasName)
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	gw.Write([]byte(xml))
	gw.Close()
	rcpt := "dmarc-reports@" + aliasName
	reportSubject := "Report domain: " + aliasName + " " + randomSuffix()
	msg := "From: reports@receiver.example\r\nTo: " + rcpt + "\r\nSubject: " + reportSubject + "\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: application/gzip; name=report.xml.gz\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(gz.Bytes()) + "\r\n"
	if err := submit(t, smtpAddr, "", "", "reports@receiver.example", []string{rcpt}, msg, false); err != nil {
		t.Fatalf("delivering a DMARC report to the alias domain: %v", err)
	}
	var summary struct {
		Reports int
	}
	waitFor(t, "the alias domain report to be stored", func() bool {
		w.admin.must("GET", "/domains/"+ad.ID+"/dmarc", nil, &summary, 200)
		return summary.Reports == 1
	})
	reportsUser := newClient(t)
	reportsUser.login("dmarc-reports@"+w.domain, "dmarc-integration-pw")
	sentinel := "after report " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "sender@external.example", []string{"dmarc-reports@" + w.domain}, rfc822("sender@external.example", "dmarc-reports@"+w.domain, sentinel, "x"), false); err != nil {
		t.Fatalf("mail to the target domain's dmarc-reports mailbox: %v", err)
	}
	waitFor(t, "the dmarc-reports mailbox to receive direct mail", func() bool { return findInAPI(reportsUser, reports.ID, "INBOX", sentinel) != 0 })
	if findInAPI(reportsUser, reports.ID, "INBOX", reportSubject) != 0 {
		t.Fatal("the alias domain DMARC report also reached the target domain's dmarc-reports mailbox")
	}
}
