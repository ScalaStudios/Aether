//go:build integration

package integration

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

type forwardingState struct {
	Mode       string   `json:"mode"`
	Domains    []string `json:"domains"`
	Forwarders []struct {
		Address string `json:"address"`
		Rule    string `json:"rule"`
		To      string `json:"to"`
		Allowed bool   `json:"allowed"`
	} `json:"forwarders"`
}

func relayPolicy(t *testing.T, sender, recipient string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", env(t, "AETHER_TEST_POLICY", "api:10040"), 5*time.Second)
	if err != nil {
		t.Fatalf("connect to the policy service: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	conn.Write([]byte("request=smtpd_access_policy\nprotocol_state=RCPT\nsasl_username=aether-sieve-relay\nsender=" + sender + "\nrecipient=" + recipient + "\n\n"))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read policy answer: %v", err)
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "action="))
}

func TestForwardingPolicy(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	tag := "fwdpolicy-" + randomSuffix()
	outside := "someone@" + tag + ".example"
	rule := func(to ...string) []map[string]any {
		return []map[string]any{{
			"name": "Out", "enabled": true, "match": "all", "conditions": []map[string]string{{"field": "subject", "contains": tag}},
			"actions": map[string]any{"forwardTo": to},
		}}
	}
	path := "/organizations/" + w.orgID + "/forwarding"

	w.bob.must("PUT", "/mailboxes/"+w.bobMailbox+"/rules", rule(outside, alice), nil, 200)
	var state forwardingState
	w.admin.must("GET", path, nil, &state, 200)
	if state.Mode != "allowed" || len(state.Forwarders) != 1 || state.Forwarders[0].To != outside || state.Forwarders[0].Address != bob || !state.Forwarders[0].Allowed {
		t.Fatalf("forwarding before a policy: %+v", state)
	}
	var audited string
	if err := testDB(t).QueryRow(context.Background(), `SELECT metadata->>'address' FROM audit_events WHERE org_id = $1 AND action = 'rule.forwarding.add' AND metadata->>'external' = 'true'`, w.orgID).Scan(&audited); err != nil || audited != outside {
		t.Fatalf("rule.forwarding.add audit = %q, %v", audited, err)
	}
	if got := w.bob.do("GET", path, nil, nil); got != 403 && got != 404 {
		t.Fatalf("a mailbox owner reading the forwarding policy got %d, want 403 or 404", got)
	}
	if got := w.admin.do("PUT", path, map[string]any{"mode": "domains", "domains": []string{}}, nil); got != 422 {
		t.Fatalf("an empty domain list returned %d, want 422", got)
	}
	if got := relayPolicy(t, bob, outside); got != "DUNNO" {
		t.Fatalf("relay check while allowed = %q", got)
	}

	w.admin.must("PUT", path, map[string]any{"mode": "domains", "domains": []string{"@Partner.example", "partner.example"}}, &state, 200)
	if len(state.Domains) != 1 || state.Domains[0] != "partner.example" || state.Forwarders[0].Allowed {
		t.Fatalf("forwarding after limiting to domains: %+v", state)
	}
	if got := w.bob.do("PUT", "/mailboxes/"+w.bobMailbox+"/rules", rule("x@"+tag+".example"), nil); got != 422 {
		t.Fatalf("a rule forwarding outside the listed domains returned %d, want 422", got)
	}
	w.bob.must("PUT", "/mailboxes/"+w.bobMailbox+"/rules", rule("x@partner.example", alice), nil, 200)
	if got := relayPolicy(t, bob, "x@partner.example"); got != "DUNNO" {
		t.Fatalf("relay check to a listed domain = %q", got)
	}
	if got := relayPolicy(t, bob, outside); !strings.HasPrefix(got, "REJECT 5.7.1") {
		t.Fatalf("relay check to an unlisted domain = %q", got)
	}

	w.admin.must("PUT", path, map[string]any{"mode": "blocked", "domains": []string{}}, &state, 200)
	if got := relayPolicy(t, bob, "x@partner.example"); !strings.HasPrefix(got, "REJECT 5.7.1") {
		t.Fatalf("relay check while blocked = %q", got)
	}
	for _, internal := range []string{alice, "nobody@" + w.domain} {
		if got := relayPolicy(t, bob, internal); got != "DUNNO" {
			t.Fatalf("relay check inside the organization to %s = %q", internal, got)
		}
	}
	if got := relayPolicy(t, "", outside); got != "DUNNO" {
		t.Fatalf("relay check for a null sender = %q", got)
	}

	w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": "Report " + tag, "text": "inside only"}, nil, 202)
	waitFor(t, "the internal forward to reach alice while outside forwarding is blocked", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", "Report "+tag) != 0 })
}
