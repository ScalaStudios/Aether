//go:build integration

package integration

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAPIKeysAndWebhooks(t *testing.T) {
	admin := adminClient(t)
	suffix := randomSuffix()
	var org, other idOnly
	admin.must("POST", "/organizations", map[string]string{"name": "Keys " + suffix, "slug": "keys-" + suffix}, &org, 201)
	admin.must("POST", "/organizations", map[string]string{"name": "Other " + suffix, "slug": "other-" + suffix}, &other, 201)
	domain := "keys" + suffix + ".test"
	var dom idOnly
	admin.must("POST", "/organizations/"+org.ID+"/domains", map[string]string{"name": domain}, &dom, 201)

	if code := admin.do("POST", "/organizations/"+org.ID+"/api-keys", map[string]any{"name": "bad", "scopes": []string{"users:write"}}, nil); code != 422 {
		t.Fatalf("unknown scope returned %d, want 422", code)
	}
	var created struct {
		Key   struct{ ID, Prefix string }
		Token string
	}
	admin.must("POST", "/organizations/"+org.ID+"/api-keys", map[string]any{
		"name": "automation", "scopes": []string{"domains:read", "mailboxes:write", "webhooks:write"},
	}, &created, 201)
	if !strings.HasPrefix(created.Token, created.Key.Prefix) {
		t.Fatalf("token %q does not start with prefix %q", created.Token[:10], created.Key.Prefix)
	}
	key := &apiClient{t: t, base: admin.base, http: &http.Client{Timeout: 30 * time.Second}, bearer: created.Token}

	key.must("GET", "/organizations/"+org.ID+"/domains", nil, nil, 200)
	checks := []struct {
		method, path string
		want         int
	}{
		{"POST", "/organizations/" + org.ID + "/domains", 403},
		{"GET", "/organizations/" + org.ID + "/audit", 403},
		{"GET", "/organizations/" + other.ID + "/domains", 404},
		{"GET", "/me/mailboxes", 403},
		{"GET", "/session", 403},
		{"POST", "/organizations/" + org.ID + "/api-keys", 403},
	}
	for _, c := range checks {
		if code := key.do(c.method, c.path, map[string]string{"name": "x.test"}, nil); code != c.want {
			t.Errorf("API key %s %s = %d, want %d", c.method, c.path, code, c.want)
		}
	}

	if env(t, "AETHER_TEST_WEBHOOKS", "") == "true" {
		testWebhookDelivery(t, key, org.ID, dom.ID, domain)
	}

	admin.must("DELETE", "/api-keys/"+created.Key.ID, nil, nil, 204)
	if code := key.do("GET", "/organizations/"+org.ID+"/domains", nil, nil); code != 401 {
		t.Fatalf("revoked key returned %d, want 401", code)
	}
}

func testWebhookDelivery(t *testing.T, key *apiClient, orgID, domainID, domain string) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type received struct {
		event, signature string
		body             []byte
	}
	got := make(chan received, 10)
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- received{r.Header.Get("Aether-Event"), r.Header.Get("Aether-Signature"), body}
	}))
	var ip string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			ip = n.IP.String()
			break
		}
	}
	target := "http://" + net.JoinHostPort(ip, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)) + "/hook"

	var hook struct {
		Webhook struct{ ID string }
		Secret  string
	}
	key.must("POST", "/organizations/"+orgID+"/webhooks", map[string]any{"url": target, "events": []string{"mailbox.create"}}, &hook, 201)
	key.must("POST", "/organizations/"+orgID+"/mailboxes", map[string]any{
		"domainId": domainID, "localPart": "hooked", "displayName": "Hooked", "password": "hooked-integration-pw",
	}, nil, 201)

	var r received
	select {
	case r = <-got:
	case <-time.After(40 * time.Second):
		t.Fatal("no webhook delivery within 40 seconds")
	}
	if r.event != "mailbox.create" {
		t.Fatalf("delivered event %q", r.event)
	}
	parts := map[string]string{}
	for _, kv := range strings.Split(r.signature, ",") {
		k, v, _ := strings.Cut(kv, "=")
		parts[k] = v
	}
	mac := hmac.New(sha256.New, []byte(hook.Secret))
	mac.Write([]byte(parts["t"] + "."))
	mac.Write(r.body)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts["v1"])) {
		t.Fatalf("signature %q does not match the body", r.signature)
	}
	var payload struct {
		Type  string
		Actor struct{ APIKeyID *string }
		Data  map[string]any
	}
	json.Unmarshal(r.body, &payload)
	if payload.Type != "mailbox.create" || payload.Data["address"] != "hooked@"+domain || payload.Actor.APIKeyID == nil {
		t.Fatalf("unexpected payload %s", r.body)
	}

	var deliveries []struct{ Status string }
	waitFor(t, "the delivery to be marked delivered", func() bool {
		key.must("GET", "/webhooks/"+hook.Webhook.ID+"/deliveries", nil, &deliveries, 200)
		return len(deliveries) == 1 && deliveries[0].Status == "delivered"
	})
}
