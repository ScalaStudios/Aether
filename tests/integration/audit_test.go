//go:build integration

package integration

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type auditRow struct {
	ID          int64          `json:"id"`
	CreatedAt   time.Time      `json:"createdAt"`
	OrgID       *string        `json:"orgId"`
	ActorUserID *string        `json:"actorUserId"`
	ActorEmail  *string        `json:"actorEmail"`
	Action      string         `json:"action"`
	TargetType  string         `json:"targetType"`
	TargetID    string         `json:"targetId"`
	IP          string         `json:"ip"`
	Metadata    map[string]any `json:"metadata"`
}

func (c *apiClient) audit(path string, q url.Values) []auditRow {
	c.t.Helper()
	var out []auditRow
	c.must("GET", path+"?"+q.Encode(), nil, &out, 200)
	return out
}

func (c *apiClient) get(path string) (*http.Response, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest("GET", c.base+path, nil)
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func TestAuditSearchExportAndRetention(t *testing.T) {
	w := setupWorld(t)
	base := "/organizations/" + w.orgID + "/audit"
	alice := "alice@" + w.domain

	if code := newClient(t).do("POST", "/auth/login", map[string]string{"email": alice, "password": "wrong-password"}, nil); code != 401 {
		t.Fatalf("wrong password returned %d, want 401", code)
	}

	logins := w.admin.audit(base, url.Values{"action": {"auth.login"}, "actor": {alice}})
	if len(logins) == 0 || logins[0].ActorUserID == nil || logins[0].OrgID == nil || *logins[0].OrgID != w.orgID || logins[0].Metadata["method"] != "password" {
		t.Fatalf("alice's sign-in is not in her organization's audit log: %+v", logins)
	}
	aliceID := *logins[0].ActorUserID
	for _, e := range logins {
		if e.Action != "auth.login" || e.ActorEmail == nil || *e.ActorEmail != alice {
			t.Fatalf("actor filter returned %+v", e)
		}
	}
	if byID := w.admin.audit(base, url.Values{"actor": {aliceID}, "action": {"auth.login"}}); len(byID) != len(logins) {
		t.Fatalf("actor by id found %d events, by email %d", len(byID), len(logins))
	}

	failed := w.admin.audit(base, url.Values{"action": {"auth.login.failed"}, "target": {aliceID}})
	if len(failed) != 1 || failed[0].Metadata["email"] != alice || failed[0].ActorUserID != nil {
		t.Fatalf("failed sign-in = %+v", failed)
	}
	if got := w.admin.audit(base, url.Values{"action": {"auth.*"}}); len(got) < len(logins)+1 {
		t.Fatalf("auth.* matched %d events", len(got))
	}
	if got := w.admin.audit(base, url.Values{"q": {alice}, "action": {"auth.login.failed"}}); len(got) != 1 {
		t.Fatalf("full-text search for the email found %d failed sign-ins", len(got))
	}
	if got := w.admin.audit(base, url.Values{"q": {"no-such-thing-" + randomSuffix()}}); len(got) != 0 {
		t.Fatalf("search for a random string found %+v", got)
	}

	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if got := w.admin.audit(base, url.Values{"since": {tomorrow}}); len(got) != 0 {
		t.Fatalf("since tomorrow returned %d events", len(got))
	}
	if got := w.admin.audit(base, url.Values{"until": {yesterday}}); len(got) != 0 {
		t.Fatalf("until yesterday returned %d events", len(got))
	}
	if got := w.admin.audit(base, url.Values{"since": {yesterday}, "until": {tomorrow}}); len(got) == 0 {
		t.Fatal("today's events are missing from a range around today")
	}
	if code := w.admin.do("GET", base+"?since=yesterday", nil, nil); code != 422 {
		t.Fatalf("invalid date returned %d, want 422", code)
	}

	all := w.admin.audit(base, url.Values{})
	first := w.admin.audit(base, url.Values{"limit": {"1"}})
	next := w.admin.audit(base, url.Values{"limit": {"1"}, "before": {strconv.FormatInt(first[0].ID, 10)}})
	if len(all) < 3 || len(first) != 1 || len(next) != 1 || first[0].ID != all[0].ID || next[0].ID != all[1].ID {
		t.Fatalf("paging with before does not follow the full list")
	}

	resp, body := w.admin.get(base + "/export?format=csv&action=auth.login.failed")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") || !strings.Contains(resp.Header.Get("Content-Disposition"), "audit-log.csv") {
		t.Fatalf("csv export: %d %v", resp.StatusCode, resp.Header)
	}
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil || len(rows) != 2 || rows[0][5] != "action" || rows[1][5] != "auth.login.failed" || rows[1][2] != w.orgID {
		t.Fatalf("csv export rows = %v %v", rows, err)
	}
	resp, body = w.admin.get(base + "/export?actor=" + url.QueryEscape(alice))
	var exported []auditRow
	if resp.StatusCode != 200 || json.Unmarshal(body, &exported) != nil || len(exported) != len(w.admin.audit(base, url.Values{"actor": {alice}})) {
		t.Fatalf("json export: %d %s", resp.StatusCode, body)
	}
	if code := w.admin.do("GET", base+"/export?format=xml", nil, nil); code != 422 {
		t.Fatalf("unknown export format returned %d, want 422", code)
	}
	exports := w.admin.audit(base, url.Values{"action": {"audit.export"}})
	if len(exports) != 2 || exports[0].Metadata["format"] != "json" || exports[1].Metadata["format"] != "csv" || exports[0].Metadata["actor"] != alice {
		t.Fatalf("exports were not audited: %+v", exports)
	}

	var ret struct {
		RetentionDays          *int `json:"retentionDays"`
		DefaultRetentionDays   int  `json:"defaultRetentionDays"`
		EffectiveRetentionDays int  `json:"effectiveRetentionDays"`
	}
	w.admin.must("GET", base+"/retention", nil, &ret, 200)
	if ret.RetentionDays != nil || ret.EffectiveRetentionDays != ret.DefaultRetentionDays {
		t.Fatalf("initial retention = %+v", ret)
	}
	w.admin.must("PUT", base+"/retention", map[string]any{"retentionDays": 30}, &ret, 200)
	if ret.RetentionDays == nil || *ret.RetentionDays != 30 || ret.EffectiveRetentionDays != 30 {
		t.Fatalf("retention after setting 30 = %+v", ret)
	}
	var org struct {
		AuditRetentionDays *int `json:"auditRetentionDays"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID, nil, &org, 200)
	if org.AuditRetentionDays == nil || *org.AuditRetentionDays != 30 {
		t.Fatalf("organization auditRetentionDays = %v", org.AuditRetentionDays)
	}
	for _, bad := range []int{-1, 3651} {
		if code := w.admin.do("PUT", base+"/retention", map[string]any{"retentionDays": bad}, nil); code != 422 {
			t.Fatalf("retention %d returned %d, want 422", bad, code)
		}
	}
	w.admin.must("PUT", base+"/retention", map[string]any{"retentionDays": nil}, &ret, 200)
	if ret.RetentionDays != nil || ret.EffectiveRetentionDays != ret.DefaultRetentionDays {
		t.Fatalf("retention after reset = %+v", ret)
	}
	changes := w.admin.audit(base, url.Values{"action": {"audit.retention.update"}})
	if len(changes) != 2 || changes[0].Metadata["after"] != nil || changes[1].Metadata["after"] != float64(30) {
		t.Fatalf("retention changes = %+v", changes)
	}

	if code := w.bob.do("GET", base, nil, nil); code != 403 {
		t.Fatalf("ordinary member read the audit log: %d", code)
	}

	var created struct{ Token string }
	w.admin.must("POST", "/organizations/"+w.orgID+"/api-keys", map[string]any{"name": "siem", "scopes": []string{"audit:read"}}, &created, 201)
	reader := &apiClient{t: t, base: w.admin.base, http: &http.Client{Timeout: 30 * time.Second}, bearer: created.Token}
	if got := reader.audit(base, url.Values{"action": {"auth.login.failed"}}); len(got) != 1 {
		t.Fatalf("audit:read key listed %d failed sign-ins", len(got))
	}
	if resp, _ := reader.get(base + "/export?format=csv"); resp.StatusCode != 200 {
		t.Fatalf("audit:read key export = %d", resp.StatusCode)
	}
	reader.must("GET", base+"/retention", nil, nil, 200)
	if code := reader.do("PUT", base+"/retention", map[string]any{"retentionDays": 7}, nil); code != 403 {
		t.Fatalf("audit:read key changed retention: %d", code)
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/api-keys", map[string]any{"name": "retention", "scopes": []string{"audit:write"}}, &created, 201)
	writer := &apiClient{t: t, base: w.admin.base, http: &http.Client{Timeout: 30 * time.Second}, bearer: created.Token}
	writer.must("PUT", base+"/retention", map[string]any{"retentionDays": 7}, nil, 200)
	if code := writer.do("GET", "/admin/audit", nil, nil); code != 403 {
		t.Fatalf("API key read the platform audit log: %d", code)
	}

	scoped := w.admin.audit("/admin/audit", url.Values{"orgId": {w.orgID}, "action": {"auth.login.failed"}})
	if len(scoped) != 1 || scoped[0].ID != failed[0].ID {
		t.Fatalf("platform audit for the organization = %+v", scoped)
	}
	if code := w.admin.do("GET", "/admin/audit?orgId=acme", nil, nil); code != 422 {
		t.Fatalf("invalid orgId returned %d, want 422", code)
	}
	resp, body = w.admin.get("/admin/audit/export?format=csv&orgId=none&limit=5")
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "id,created_at,org_id") {
		t.Fatalf("platform export: %d %q", resp.StatusCode, body)
	}
}

func TestPlatformAuditAndWebhooks(t *testing.T) {
	admin := adminClient(t)
	w := setupWorld(t)
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": "alice@" + w.domain, "role": "admin"}, nil, 204)
	orgAdmin := newClient(t)
	orgAdmin.login("alice@"+w.domain, "alice-integration-pw")
	if code := orgAdmin.do("POST", "/admin/webhooks", map[string]any{"url": "https://example.com/hook", "events": []string{"*"}}, nil); code != 403 {
		t.Errorf("organization admin created a platform webhook: %d", code)
	}

	stranger := "nobody-" + randomSuffix() + "@nowhere.test"
	newClient(t).do("POST", "/auth/login", map[string]string{"email": stranger, "password": "x"}, nil)
	waitFor(t, "the org-less failed sign-in", func() bool {
		got := admin.audit("/admin/audit", url.Values{"orgId": {"none"}, "q": {stranger}})
		return len(got) == 1 && got[0].Action == "auth.login.failed" && got[0].OrgID == nil
	})
	if got := admin.audit("/organizations/"+w.orgID+"/audit", url.Values{"q": {stranger}}); len(got) != 0 {
		t.Fatalf("org-less event leaked into an organization: %+v", got)
	}

	if env(t, "AETHER_TEST_WEBHOOKS", "") != "true" {
		return
	}
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 100)
	go http.Serve(ln, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- body
	}))
	target := listenerURL(ln) + "/platform"
	var hook struct {
		Webhook struct{ ID string }
		Secret  string
	}
	admin.must("POST", "/admin/webhooks", map[string]any{"url": target, "events": []string{"auth.login.failed"}}, &hook, 201)
	defer admin.must("DELETE", "/admin/webhooks/"+hook.Webhook.ID, nil, nil, 204)
	var hooks []idOnly
	admin.must("GET", "/admin/webhooks", nil, &hooks, 200)
	found := false
	for _, h := range hooks {
		found = found || h.ID == hook.Webhook.ID
	}
	if !found || hook.Secret == "" {
		t.Fatalf("platform webhook missing from %v", hooks)
	}
	if code := orgAdmin.do("GET", "/webhooks/"+hook.Webhook.ID+"/deliveries", nil, nil); code != 404 {
		t.Fatalf("organization admin read a platform webhook's deliveries: %d", code)
	}

	stranger = "nobody-" + randomSuffix() + "@nowhere.test"
	newClient(t).do("POST", "/auth/login", map[string]string{"email": stranger, "password": "x"}, nil)
	deadline := time.After(40 * time.Second)
	for {
		select {
		case body := <-got:
			var payload struct {
				Type           string
				OrganizationID *string
				Data           map[string]any
			}
			json.Unmarshal(body, &payload)
			if payload.Data["email"] != stranger {
				continue
			}
			if payload.Type != "auth.login.failed" || payload.OrganizationID != nil {
				t.Fatalf("unexpected platform payload %s", body)
			}
			var deliveries []struct{ Status string }
			waitFor(t, "the platform delivery to be marked delivered", func() bool {
				admin.must("GET", "/admin/webhooks/"+hook.Webhook.ID+"/deliveries", nil, &deliveries, 200)
				for _, d := range deliveries {
					if d.Status == "delivered" {
						return true
					}
				}
				return false
			})
			admin.must("POST", "/admin/webhooks/"+hook.Webhook.ID+"/test", nil, nil, 202)
			return
		case <-deadline:
			t.Fatal("no platform webhook delivery within 40 seconds")
		}
	}
}

func listenerURL(ln net.Listener) string {
	var ip string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			ip = n.IP.String()
			break
		}
	}
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
}

func TestSlowWebhookDoesNotDelayOtherTenants(t *testing.T) {
	if env(t, "AETHER_TEST_WEBHOOKS", "") != "true" {
		t.Skip("webhooks to private addresses are disabled")
	}
	serve := func(h http.HandlerFunc) string {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go http.Serve(ln, h)
		return listenerURL(ln)
	}
	slow := serve(func(rw http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	})
	got := make(chan struct{}, 10)
	fast := serve(func(rw http.ResponseWriter, r *http.Request) { got <- struct{}{} })
	hook := func(w *world, target string) string {
		var created struct {
			Webhook struct{ ID string }
		}
		w.admin.must("POST", "/organizations/"+w.orgID+"/webhooks", map[string]any{"url": target, "events": []string{"*"}}, &created, 201)
		t.Cleanup(func() { w.admin.do("DELETE", "/webhooks/"+created.Webhook.ID, nil, nil) })
		return created.Webhook.ID
	}
	a, b := setupWorld(t), setupWorld(t)
	slowHook := hook(a, slow)
	for range 40 {
		a.admin.must("POST", "/webhooks/"+slowHook+"/test", nil, nil, 202)
	}
	fastHook := hook(b, fast)
	time.Sleep(16 * time.Second)
	b.admin.must("POST", "/webhooks/"+fastHook+"/test", nil, nil, 202)
	subject := "scheduled beside webhooks " + randomSuffix()
	b.alice.must("POST", "/mailboxes/"+b.aliceMailbox+"/send", map[string]any{
		"to": []map[string]string{{"address": "bob@" + b.domain}}, "subject": subject, "text": "x",
		"sendAt": time.Now().Add(6 * time.Second).UTC().Format(time.RFC3339),
	}, nil, 202)
	select {
	case <-got:
	case <-time.After(40 * time.Second):
		t.Fatal("another tenant's webhook delivery waited behind a slow endpoint")
	}
	deadline := time.Now().Add(40 * time.Second)
	for findInAPI(b.bob, b.bobMailbox, "INBOX", subject) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("a scheduled message waited behind webhook delivery")
		}
		time.Sleep(time.Second)
	}
}
