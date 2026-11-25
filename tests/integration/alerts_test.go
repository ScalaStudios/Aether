//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type alertRuleView struct {
	Key             string     `json:"key"`
	Enabled         bool       `json:"enabled"`
	Threshold       float64    `json:"threshold"`
	CooldownSeconds int        `json:"cooldownSeconds"`
	NotifyEmail     bool       `json:"notifyEmail"`
	NotifyWebhook   bool       `json:"notifyWebhook"`
	EmailRecipients []string   `json:"emailRecipients"`
	LastEvaluatedAt *time.Time `json:"lastEvaluatedAt"`
}

type alertView struct {
	ID      string `json:"id"`
	RuleKey string `json:"ruleKey"`
	Status  string `json:"status"`
	Title   string `json:"title"`
}

func waitAlertWebhook(t *testing.T, got <-chan receivedWebhook, event, ruleKey string) {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for {
		select {
		case r := <-got:
			var payload struct {
				Type string
				Data struct{ RuleKey string }
			}
			json.Unmarshal(r.body, &payload)
			if r.event == event && payload.Type == event && payload.Data.RuleKey == ruleKey {
				return
			}
		case <-deadline:
			t.Fatalf("no %s webhook for %s within 60 seconds", event, ruleKey)
		}
	}
}

func TestAdminAlerts(t *testing.T) {
	from := os.Getenv("AETHER_TEST_ALERT_FROM")
	if from == "" || env(t, "AETHER_TEST_WEBHOOKS", "false") != "true" {
		t.Skip("set AETHER_ALERT_FROM and AETHER_WEBHOOK_ALLOW_PRIVATE=true to test alerts")
	}
	w := setupWorld(t)
	if got := w.alice.do("GET", "/admin/alerts/rules", nil, nil); got != 403 {
		t.Fatalf("a mailbox user listed alert rules: %d", got)
	}

	local, senderDomain, _ := strings.Cut(from, "@")
	var org idOnly
	if w.admin.do("POST", "/organizations", map[string]string{"name": "Alert sender", "slug": "alert-sender"}, &org) == 201 {
		var dom idOnly
		w.admin.must("POST", "/organizations/"+org.ID+"/domains", map[string]string{"name": senderDomain}, &dom, 201)
		w.admin.must("POST", "/organizations/"+org.ID+"/mailboxes", map[string]any{
			"domainId": dom.ID, "localPart": local, "displayName": "Aether Alerts", "password": "alerts-integration-pw",
		}, nil, 201)
	}

	target, got := webhookReceiver(t, 100)
	w.admin.must("POST", "/admin/webhooks", map[string]any{
		"url": target, "events": []string{"alert.test", "alert.firing", "alert.acknowledged", "alert.resolved"},
	}, nil, 201)

	recipient := "alice@" + w.domain
	var test struct {
		EmailSent       bool     `json:"emailSent"`
		EmailRecipients []string `json:"emailRecipients"`
		EmailError      *string  `json:"emailError"`
		WebhooksQueued  int      `json:"webhooksQueued"`
	}
	w.admin.must("POST", "/admin/alerts/test", map[string]any{"emailRecipients": []string{recipient}}, &test, 200)
	if !test.EmailSent || test.EmailError != nil || len(test.EmailRecipients) != 1 || test.WebhooksQueued < 1 {
		t.Fatalf("test alert result %+v", test)
	}
	waitAlertWebhook(t, got, "alert.test", "test")
	waitUpTo(t, 60*time.Second, "the test alert email", func() bool {
		return findInAPI(w.alice, w.aliceMailbox, "INBOX", "[Aether Alert][TEST] Test alert notification ("+env(t, "AETHER_TEST_MAIL_HOSTNAME", "")+")") != 0
	})

	const key = "dovecot_acl_backlog"
	var rules []alertRuleView
	w.admin.must("GET", "/admin/alerts/rules", nil, &rules, 200)
	var original alertRuleView
	for _, r := range rules {
		if r.Key == key {
			original = r
		}
	}
	if original.Key == "" {
		t.Fatalf("rule %s is missing from %+v", key, rules)
	}
	t.Cleanup(func() {
		w.admin.must("PATCH", "/admin/alerts/rules/"+key, map[string]any{
			"enabled": original.Enabled, "threshold": original.Threshold, "cooldownSeconds": original.CooldownSeconds,
			"notifyEmail": original.NotifyEmail, "notifyWebhook": original.NotifyWebhook, "emailRecipients": original.EmailRecipients,
		}, nil, 200)
	})
	if code := errorCode(t, w.admin, "PATCH", "/admin/alerts/rules/"+key, map[string]any{"threshold": -1}, 422); code != "invalid_threshold" {
		t.Fatalf("negative threshold: %s", code)
	}
	var updated alertRuleView
	w.admin.must("PATCH", "/admin/alerts/rules/"+key, map[string]any{
		"enabled": true, "threshold": 0, "cooldownSeconds": 60, "notifyEmail": true, "notifyWebhook": true, "emailRecipients": []string{recipient},
	}, &updated, 200)
	if updated.Threshold != 0 || len(updated.EmailRecipients) != 1 || updated.EmailRecipients[0] != recipient {
		t.Fatalf("updated rule %+v", updated)
	}

	var firing alertView
	waitUpTo(t, 60*time.Second, "the ACL backlog alert to fire", func() bool {
		var list struct{ Alerts []alertView }
		w.admin.must("GET", "/admin/alerts?status=active&rule="+key, nil, &list, 200)
		if len(list.Alerts) == 1 {
			firing = list.Alerts[0]
		}
		return firing.ID != ""
	})
	if firing.Status != "firing" {
		t.Fatalf("new alert %+v", firing)
	}
	waitAlertWebhook(t, got, "alert.firing", key)
	waitUpTo(t, 60*time.Second, "the firing alert email", func() bool {
		return findInAPI(w.alice, w.aliceMailbox, "INBOX", "[Aether Alert][WARNING] "+firing.Title) != 0
	})

	var acked alertView
	w.admin.must("POST", "/admin/alerts/"+firing.ID+"/acknowledge", nil, &acked, 200)
	if acked.Status != "acknowledged" {
		t.Fatalf("acknowledged alert %+v", acked)
	}
	waitAlertWebhook(t, got, "alert.acknowledged", key)

	w.admin.must("PATCH", "/admin/alerts/rules/"+key, map[string]any{"threshold": 300}, nil, 200)
	waitUpTo(t, 60*time.Second, "the ACL backlog alert to resolve", func() bool {
		var a alertView
		w.admin.must("GET", "/admin/alerts/"+firing.ID, nil, &a, 200)
		return a.Status == "resolved"
	})
	waitAlertWebhook(t, got, "alert.resolved", key)
	if code := errorCode(t, w.admin, "POST", "/admin/alerts/"+firing.ID+"/resolve", nil, 409); code != "alert_already_resolved" {
		t.Fatalf("resolving a resolved alert: %s", code)
	}

	w.admin.must("GET", "/admin/alerts/rules", nil, &rules, 200)
	for _, r := range rules {
		if r.Key == key && r.LastEvaluatedAt == nil {
			t.Fatalf("rule %s has no lastEvaluatedAt after firing", key)
		}
	}
}
