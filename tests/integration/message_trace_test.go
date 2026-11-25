//go:build integration

package integration

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type tracePage struct {
	Messages []struct {
		ID         int64    `json:"id"`
		Status     string   `json:"status"`
		Sender     string   `json:"sender"`
		Recipients []string `json:"recipients"`
		Subject    string   `json:"subject"`
		TLS        *string  `json:"tls"`
		Detail     *string  `json:"detail"`
	} `json:"messages"`
	RetentionDays *int `json:"retentionDays"`
}

type traceDetail struct {
	Status          string            `json:"status"`
	Recipients      []string          `json:"recipients"`
	RecipientStatus map[string]string `json:"recipientStatus"`
	Events          []struct {
		Kind      string `json:"kind"`
		Recipient string `json:"recipient"`
	} `json:"events"`
}

func traceSearch(c *apiClient, orgID string, q url.Values) tracePage {
	var page tracePage
	c.must("GET", "/organizations/"+orgID+"/message-trace?"+q.Encode(), nil, &page, 200)
	return page
}

func TestMessageTrace(t *testing.T) {
	a := setupWorld(t)
	b := setupWorld(t)
	for _, w := range []*world{a, b} {
		w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": "alice@" + w.domain, "role": "admin"}, nil, 204)
	}
	adminA, adminB := newClient(t), newClient(t)
	adminA.login("alice@"+a.domain, "alice-integration-pw")
	adminB.login("alice@"+b.domain, "alice-integration-pw")
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	sender := "billing-" + randomSuffix() + "@example.org"

	shared := "Shared invoice " + randomSuffix()
	to := []string{"alice@" + a.domain, "bob@" + b.domain}
	if err := submit(t, smtpAddr, "", "", sender, to, rfc822(sender, strings.Join(to, ", "), shared, "hello"), false); err != nil {
		t.Fatalf("send shared: %v", err)
	}
	private := "Only for B " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", sender, []string{"bob@" + b.domain}, rfc822(sender, "bob@"+b.domain, private, "hello"), false); err != nil {
		t.Fatalf("send private: %v", err)
	}
	if err := submit(t, smtpAddr, "", "", sender, []string{"ghost@" + a.domain}, rfc822(sender, "ghost@"+a.domain, "typo", "hello"), false); err == nil {
		t.Fatal("mail to an unknown mailbox was accepted")
	}

	var page tracePage
	waitUpTo(t, 60*time.Second, "the shared message to be traced as delivered", func() bool {
		page = traceSearch(adminA, a.orgID, url.Values{"subject": {shared}})
		return len(page.Messages) == 1 && page.Messages[0].Status == "delivered"
	})
	m := page.Messages[0]
	if m.Sender != sender || len(m.Recipients) != 1 || m.Recipients[0] != "alice@"+a.domain {
		t.Fatalf("organization A sees %+v, want only its own recipient", m)
	}
	if page.RetentionDays == nil || *page.RetentionDays < 1 {
		t.Fatalf("retention not reported: %+v", page.RetentionDays)
	}
	var detail traceDetail
	adminA.must("GET", "/organizations/"+a.orgID+"/message-trace/"+strconv.FormatInt(m.ID, 10), nil, &detail, 200)
	if len(detail.RecipientStatus) != 1 || detail.RecipientStatus["alice@"+a.domain] != "delivered" {
		t.Fatalf("recipient status %v", detail.RecipientStatus)
	}
	for _, e := range detail.Events {
		if e.Recipient != "" && e.Recipient != "alice@"+a.domain {
			t.Fatalf("organization A sees an event for %s", e.Recipient)
		}
	}

	pageB := traceSearch(adminB, b.orgID, url.Values{"sender": {sender}})
	if len(pageB.Messages) != 2 {
		t.Fatalf("organization B sees %d messages from the sender, want 2", len(pageB.Messages))
	}
	for _, mb := range pageB.Messages {
		if len(mb.Recipients) != 1 || mb.Recipients[0] != "bob@"+b.domain {
			t.Fatalf("organization B sees recipients %v", mb.Recipients)
		}
		if mb.Subject == private && adminA.do("GET", "/organizations/"+a.orgID+"/message-trace/"+strconv.FormatInt(mb.ID, 10), nil, nil) != 404 {
			t.Fatal("organization A opened a message only sent to organization B")
		}
	}
	if got := traceSearch(adminA, a.orgID, url.Values{"subject": {private}}); len(got.Messages) != 0 {
		t.Fatalf("organization A found organization B's message: %+v", got.Messages)
	}

	waitUpTo(t, 60*time.Second, "the rejected recipient to be traced", func() bool {
		page = traceSearch(adminA, a.orgID, url.Values{"recipient": {"ghost@" + a.domain}, "status": {"rejected"}})
		return len(page.Messages) == 1
	})
	if d := page.Messages[0].Detail; d == nil || !strings.Contains(*d, "User unknown") {
		t.Fatalf("rejection reason missing: %v", d)
	}

	csv := string(adminA.raw("GET", "/organizations/"+a.orgID+"/message-trace/export?subject="+url.QueryEscape(shared)))
	if !strings.HasPrefix(csv, "Received (UTC),Status,Sender,Recipients") || !strings.Contains(csv, shared) || strings.Contains(csv, "bob@"+b.domain) {
		t.Fatalf("export:\n%s", csv)
	}

	var key struct {
		Token string `json:"token"`
	}
	adminA.must("POST", "/organizations/"+a.orgID+"/api-keys", map[string]any{"name": "trace", "scopes": []string{"message-trace:read"}}, &key, 201)
	bot := newClient(t)
	bot.bearer = key.Token
	if got := traceSearch(bot, a.orgID, url.Values{"subject": {shared}}); len(got.Messages) != 1 {
		t.Fatalf("API key search returned %d messages", len(got.Messages))
	}
	if got := a.bob.do("GET", "/organizations/"+a.orgID+"/message-trace", nil, nil); got != 403 {
		t.Errorf("ordinary member searched the message trace: %d", got)
	}
	var all tracePage
	a.admin.must("GET", "/admin/message-trace?sender="+url.QueryEscape(sender), nil, &all, 200)
	if len(all.Messages) != 3 {
		t.Fatalf("platform administrator sees %d messages, want 3", len(all.Messages))
	}
}
