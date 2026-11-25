//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

func TestQuotaWarnings(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain

	var settings struct {
		Percents     []int `json:"percents"`
		NotifyAdmins bool  `json:"notifyAdmins"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/quota-warnings", nil, &settings, 200)
	if len(settings.Percents) != 2 || settings.Percents[0] != 80 || settings.Percents[1] != 95 || settings.NotifyAdmins {
		t.Fatalf("default quota warnings = %+v, want 80 and 95 without administrators", settings)
	}
	if code := w.admin.do("PUT", "/organizations/"+w.orgID+"/quota-warnings", map[string]any{"percents": []int{40}, "notifyAdmins": true}, nil); code != 422 {
		t.Fatalf("a warning level below 50%% returned %d, want 422", code)
	}
	if code := w.alice.do("PUT", "/organizations/"+w.orgID+"/quota-warnings", map[string]any{"percents": []int{80}, "notifyAdmins": true}, nil); code != 403 {
		t.Fatalf("a member changing quota warnings returned %d, want 403", code)
	}
	w.admin.must("PUT", "/organizations/"+w.orgID+"/quota-warnings", map[string]any{"percents": []int{95, 80}, "notifyAdmins": true}, &settings, 200)
	if settings.Percents[0] != 80 || !settings.NotifyAdmins {
		t.Fatalf("saved quota warnings = %+v", settings)
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": bob, "role": "admin"}, nil, 204)
	w.admin.must("PATCH", "/mailboxes/"+w.aliceMailbox, map[string]any{"quotaBytes": 100 << 10}, nil, 200)

	var mine []struct {
		ID     string `json:"id"`
		WarnAt []int  `json:"quotaWarningPercents"`
	}
	w.alice.must("GET", "/me/mailboxes", nil, &mine, 200)
	if len(mine) == 0 || len(mine[0].WarnAt) != 2 {
		t.Fatalf("own mailboxes = %+v, want the organization's warning levels", mine)
	}

	ic, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(rfc822(alice, alice, "Large attachment", strings.Repeat("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\r\n", 1150)))
	cmd := ic.Append("INBOX", int64(len(raw)), &imap.AppendOptions{Time: time.Now()})
	cmd.Write(raw)
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatalf("append: %v", err)
	}
	ic.Logout().Wait()
	ic.Close()

	var folders []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/folders", nil, &folders, 200)
	var inbox int64
	for _, f := range folders {
		if f.Name == "INBOX" {
			inbox = f.Size
		}
	}
	if inbox < int64(len(raw)) {
		t.Fatalf("INBOX size = %d, want at least %d", inbox, len(raw))
	}

	warnings := func(c *apiClient, mailbox string) int {
		var page messagePage
		c.must("GET", "/mailboxes/"+mailbox+"/messages?folder=INBOX", nil, &page, 200)
		n := 0
		for _, m := range page.Messages {
			if strings.HasPrefix(m.Subject, alice+" is ") && strings.HasSuffix(m.Subject, "% full") {
				n++
			}
		}
		return n
	}
	waitUpTo(t, 2*time.Minute, "the storage warning in the mailbox", func() bool { return warnings(w.alice, w.aliceMailbox) == 1 })
	waitFor(t, "the administrator's copy of the storage warning", func() bool { return warnings(w.bob, w.bobMailbox) == 1 })

	var listed []struct {
		ID        string `json:"id"`
		UsedBytes int64  `json:"usedBytes"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/mailboxes", nil, &listed, 200)
	for _, m := range listed {
		if m.ID == w.aliceMailbox && m.UsedBytes < int64(len(raw)) {
			t.Fatalf("console usage for alice = %d, want at least %d", m.UsedBytes, len(raw))
		}
	}

	time.Sleep(25 * time.Second)
	if n := warnings(w.alice, w.aliceMailbox); n != 1 {
		t.Fatalf("the mailbox has %d storage warnings after another check, want 1", n)
	}
}
