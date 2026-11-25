//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type securityNotificationItem struct {
	ID             string         `json:"id"`
	EventType      string         `json:"eventType"`
	Title          string         `json:"title"`
	Summary        string         `json:"summary"`
	MailboxAddress string         `json:"mailboxAddress"`
	Details        map[string]any `json:"details"`
	ActionURL      string         `json:"actionUrl"`
	CanUndo        bool           `json:"canUndo"`
	UndoLabel      string         `json:"undoLabel"`
	ReadAt         *string        `json:"readAt"`
	UndoneAt       *string        `json:"undoneAt"`
	ReportedAt     *string        `json:"reportedAt"`
}

type securityNotificationsPage struct {
	UnreadCount   int                        `json:"unreadCount"`
	Notifications []securityNotificationItem `json:"notifications"`
}

func findSecurityNotification(c *apiClient, eventType string) *securityNotificationItem {
	var page securityNotificationsPage
	c.must("GET", "/me/security-notifications", nil, &page, 200)
	for i := range page.Notifications {
		if page.Notifications[i].EventType == eventType {
			return &page.Notifications[i]
		}
	}
	return nil
}

func findInboxSubjectContaining(c *apiClient, mailbox, needle string) string {
	var page messagePage
	c.must("GET", "/mailboxes/"+mailbox+"/messages?folder=INBOX", nil, &page, 200)
	for _, m := range page.Messages {
		if strings.Contains(m.Subject, needle) {
			return m.Subject
		}
	}
	return ""
}

func loginWithHeaders(t *testing.T, email, password, userAgent, country string) (*apiClient, int) {
	t.Helper()
	c := newClient(t)
	buf, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, _ := http.NewRequest("POST", c.base+"/auth/login", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if country != "" {
		req.Header.Set("CF-IPCountry", country)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		var s struct {
			CSRFToken string `json:"csrfToken"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			t.Fatalf("decode login: %v", err)
		}
		c.csrf = s.CSRFToken
	}
	return c, resp.StatusCode
}

func TestSecurityNotifications(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	bob := "bob@" + w.domain
	secAdminAddr := "secadmin@" + w.domain

	var secAdminMailbox idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{
		"domainId": w.domainID, "localPart": "secadmin", "displayName": "Security Admin", "password": "secadmin-integration-pw",
	}, &secAdminMailbox, 201)
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": secAdminAddr, "role": "admin"}, nil, 204)
	secAdmin := newClient(t)
	secAdmin.login(secAdminAddr, "secadmin-integration-pw")

	settingsPath := "/organizations/" + w.orgID + "/security-notifications"
	var settings struct {
		AppPasswordCreated     bool `json:"appPasswordCreated"`
		ExternalForwardingRule bool `json:"externalForwardingRule"`
		DelegateAdded          bool `json:"delegateAdded"`
		NewDeviceSignIn        bool `json:"newDeviceSignIn"`
		FailedSignIns          bool `json:"failedSignIns"`
		NotifyAdmins           bool `json:"notifyAdmins"`
		FailedSignInThreshold  int  `json:"failedSignInThreshold"`
	}
	w.admin.must("GET", settingsPath, nil, &settings, 200)
	if !settings.AppPasswordCreated || !settings.ExternalForwardingRule || !settings.DelegateAdded || !settings.NewDeviceSignIn || !settings.FailedSignIns || settings.NotifyAdmins || settings.FailedSignInThreshold != 5 {
		t.Fatalf("unexpected default org security settings: %+v", settings)
	}
	if code := w.alice.do("GET", settingsPath, nil, nil); code != 403 {
		t.Fatalf("non-admin GET org security settings returned %d, want 403", code)
	}
	if code := w.admin.do("PUT", settingsPath, map[string]any{
		"appPasswordCreated": true, "externalForwardingRule": true, "delegateAdded": true,
		"newDeviceSignIn": true, "failedSignIns": true, "failedSignInThreshold": 2, "notifyAdmins": false,
	}, nil); code != 422 {
		t.Fatalf("invalid threshold returned %d, want 422", code)
	}

	var ap struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	w.alice.must("POST", "/me/app-passwords", map[string]string{"name": "Suspicious Phone", "mailboxId": w.aliceMailbox}, &ap, 201)
	var apNotif *securityNotificationItem
	waitFor(t, "app password security notification", func() bool {
		apNotif = findSecurityNotification(w.alice, "app_password_created")
		return apNotif != nil
	})
	if !apNotif.CanUndo {
		t.Fatalf("app password alert should be undoable: %+v", apNotif)
	}
	waitFor(t, "app password alert email in alice INBOX", func() bool {
		return findInboxSubjectContaining(w.alice, w.aliceMailbox, "App password created") != ""
	})
	w.alice.must("POST", "/me/security-notifications/"+apNotif.ID+"/undo", nil, nil, 200)
	if code := w.alice.do("POST", "/me/security-notifications/"+apNotif.ID+"/undo", nil, nil); code != 409 {
		t.Fatalf("second undo returned %d, want 409", code)
	}
	if ic, err := imapLogin(t, alice, ap.Password); err == nil {
		ic.Close()
		t.Fatal("undone app password still works over IMAP")
	}

	w.alice.must("PUT", "/mailboxes/"+w.aliceMailbox+"/rules", []map[string]any{
		{
			"name": "Internal copy", "enabled": true, "match": "all",
			"conditions": []map[string]string{{"field": "subject", "contains": "internal"}},
			"actions":    map[string]any{"forwardTo": []string{bob}, "keepCopy": true},
		},
	}, nil, 200)
	if n := findSecurityNotification(w.alice, "external_forwarding_rule"); n != nil {
		t.Fatalf("internal forwarding triggered external forwarding alert: %+v", n)
	}

	w.alice.must("PUT", "/mailboxes/"+w.aliceMailbox+"/rules", []map[string]any{
		{
			"name": "Internal copy", "enabled": true, "match": "all",
			"conditions": []map[string]string{{"field": "subject", "contains": "internal"}},
			"actions":    map[string]any{"forwardTo": []string{bob}, "keepCopy": true},
		},
		{
			"name": "Exfil", "enabled": true, "match": "all",
			"conditions": []map[string]string{{"field": "subject", "contains": "invoice"}},
			"actions":    map[string]any{"forwardTo": []string{"outsider@external.example"}, "keepCopy": true},
		},
	}, nil, 200)
	var fwdNotif *securityNotificationItem
	waitFor(t, "external forwarding notification", func() bool {
		fwdNotif = findSecurityNotification(w.alice, "external_forwarding_rule")
		return fwdNotif != nil
	})
	if !fwdNotif.CanUndo {
		t.Fatalf("external forwarding alert should be undoable: %+v", fwdNotif)
	}
	waitFor(t, "external forwarding alert email in alice INBOX", func() bool {
		return findInboxSubjectContaining(w.alice, w.aliceMailbox, "External forwarding rule added") != ""
	})
	w.alice.must("POST", "/me/security-notifications/"+fwdNotif.ID+"/undo", nil, nil, 200)
	var rulesAfter []struct {
		Name    string `json:"name"`
		Actions struct {
			ForwardTo []string `json:"forwardTo"`
		} `json:"actions"`
	}
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/rules", nil, &rulesAfter, 200)
	for _, r := range rulesAfter {
		if slices.Contains(r.Actions.ForwardTo, "outsider@external.example") {
			t.Fatalf("external forwarding rule still present after undo: %+v", rulesAfter)
		}
	}

	w.admin.must("POST", "/mailboxes/"+w.aliceMailbox+"/delegates", map[string]any{"email": bob, "access": "full", "sendAs": true}, nil, 200)
	var delNotif *securityNotificationItem
	waitFor(t, "delegate added notification", func() bool {
		delNotif = findSecurityNotification(w.alice, "delegate_added")
		return delNotif != nil
	})
	if !delNotif.CanUndo {
		t.Fatalf("delegate added alert should be undoable: %+v", delNotif)
	}
	waitFor(t, "delegate added alert email in alice INBOX", func() bool {
		return findInboxSubjectContaining(w.alice, w.aliceMailbox, "Delegate access granted") != ""
	})
	w.alice.must("POST", "/me/security-notifications/"+delNotif.ID+"/undo", nil, nil, 200)
	var delegates []struct {
		Email string `json:"email"`
	}
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/delegates", nil, &delegates, 200)
	if len(delegates) != 0 {
		t.Fatalf("delegates remain after undo: %+v", delegates)
	}

	otherDevice, code := loginWithHeaders(t, alice, "alice-integration-pw",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "DE")
	if code != 200 {
		t.Fatalf("new device login returned %d, want 200", code)
	}
	var signInNotif *securityNotificationItem
	waitFor(t, "new device sign-in notification", func() bool {
		signInNotif = findSecurityNotification(w.alice, "new_device_sign_in")
		return signInNotif != nil
	})
	if !signInNotif.CanUndo {
		t.Fatalf("new device sign-in alert should be undoable: %+v", signInNotif)
	}
	waitFor(t, "new device sign-in email in alice INBOX", func() bool {
		return findInboxSubjectContaining(w.alice, w.aliceMailbox, "Sign-in from") != ""
	})
	w.alice.must("POST", "/me/security-notifications/"+signInNotif.ID+"/undo", nil, nil, 200)
	if status := otherDevice.do("GET", "/me", nil, nil); status != 401 {
		t.Fatalf("undone session still authenticated with status %d, want 401", status)
	}

	w.admin.must("PUT", settingsPath, map[string]any{
		"appPasswordCreated": false, "externalForwardingRule": true, "delegateAdded": true,
		"newDeviceSignIn": true, "failedSignIns": true, "failedSignInThreshold": 3, "notifyAdmins": true,
	}, nil, 200)

	var beforePage securityNotificationsPage
	w.alice.must("GET", "/me/security-notifications", nil, &beforePage, 200)
	w.alice.must("POST", "/me/app-passwords", map[string]string{"name": "Muted Password", "mailboxId": w.aliceMailbox}, nil, 201)
	var afterMuted securityNotificationsPage
	w.alice.must("GET", "/me/security-notifications", nil, &afterMuted, 200)
	if len(afterMuted.Notifications) != len(beforePage.Notifications) {
		t.Fatalf("disabled appPasswordCreated still emitted a notification")
	}

	for range 3 {
		if _, status := loginWithHeaders(t, alice, "wrong-password-123", "BadBot/1.0", "US"); status != 401 {
			t.Fatalf("failed login returned %d, want 401", status)
		}
	}
	var failedNotif *securityNotificationItem
	waitFor(t, "failed sign-ins notification", func() bool {
		failedNotif = findSecurityNotification(w.alice, "failed_sign_ins")
		return failedNotif != nil
	})
	waitFor(t, "failed sign-ins email in alice INBOX", func() bool {
		return findInboxSubjectContaining(w.alice, w.aliceMailbox, "Repeated failed sign-in attempts") != ""
	})
	waitFor(t, "admin copy email in secadmin INBOX", func() bool {
		return findInboxSubjectContaining(secAdmin, secAdminMailbox.ID, "Security Alert] Repeated failed sign-in attempts") != ""
	})

	w.alice.must("POST", "/me/security-notifications/"+failedNotif.ID+"/report", map[string]string{"note": "Not me, please investigate"}, nil, 200)
	var auditEvents []struct {
		Action   string `json:"action"`
		TargetID string `json:"targetId"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &auditEvents, 200)
	foundAudit := false
	for _, e := range auditEvents {
		if e.Action == "security.notification.report" && e.TargetID == failedNotif.ID {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("security.notification.report missing from audit log: %+v", auditEvents)
	}
	waitFor(t, "suspicious activity report email in secadmin INBOX", func() bool {
		return findInboxSubjectContaining(secAdmin, secAdminMailbox.ID, "Security Report]") != ""
	})

	w.alice.must("POST", "/me/security-notifications/read-all", nil, nil, 204)
	var finalPage securityNotificationsPage
	w.alice.must("GET", "/me/security-notifications", nil, &finalPage, 200)
	if finalPage.UnreadCount != 0 {
		t.Fatalf("unreadCount = %d after read-all, want 0", finalPage.UnreadCount)
	}
}
