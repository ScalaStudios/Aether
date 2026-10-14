//go:build integration

package integration

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestAdminSuspendBlocksWebAndMail(t *testing.T) {
	w := setupWorld(t)
	var members []struct {
		UserID string `json:"userId"`
		Email  string `json:"email"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/members", nil, &members, 200)
	var aliceID string
	for _, m := range members {
		if m.Email == "alice@"+w.domain {
			aliceID = m.UserID
		}
	}
	if got := w.alice.do("PATCH", "/admin/users/"+aliceID, map[string]string{"status": "suspended"}, nil); got != 403 {
		t.Fatalf("non-admin changed a user's status: %d", got)
	}
	w.admin.must("PATCH", "/admin/users/"+aliceID, map[string]string{"status": "suspended"}, nil, 204)
	if got := w.alice.do("GET", "/session", nil, nil); got != 401 {
		t.Fatalf("suspended user's session still works: %d", got)
	}
	if _, err := imapLogin(t, "alice@"+w.domain, w.aliceAppPassword); err == nil {
		t.Fatal("suspended user's app password still opens IMAP")
	}
	if got := newClient(t).do("POST", "/auth/login", map[string]string{"email": "alice@" + w.domain, "password": "alice-integration-pw"}, nil); got != 403 {
		t.Fatalf("suspended user signed in: %d", got)
	}
	w.admin.must("PATCH", "/admin/users/"+aliceID, map[string]string{"status": "active"}, nil, 204)
	c, err := imapLogin(t, "alice@"+w.domain, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("restored user's app password rejected: %v", err)
	}
	c.Close()
}

func signedEvent(t *testing.T, url, secret, eventType, subject string) int {
	body, _ := json.Marshal(map[string]any{"id": "evt_" + randomSuffix(), "type": eventType, "time": time.Now(), "data": map[string]string{"targetType": "user", "targetId": subject}})
	return postSigned(t, url, secret, body)
}

func postSigned(t *testing.T, url, secret string, body []byte) int {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Halo-Signature", "t="+ts+",v1="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestProviderWebhookSuspendsAndRestores(t *testing.T) {
	w := setupWorld(t)
	f := startFakeIssuer(t)
	slug, secret := "halo-"+randomSuffix(), "whsec-"+randomSuffix()
	var provider struct {
		ID         string `json:"id"`
		WebhookURL string `json:"webhookUrl"`
		HasWebhook bool   `json:"hasWebhookSecret"`
	}
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret,
		"autoProvision": true, "webhookSecret": secret,
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)
	if !provider.HasWebhook {
		t.Fatal("webhook secret not stored")
	}
	hook := env(t, "AETHER_TEST_API", "http://api:8080") + "/api/v1/hooks/identity-providers/" + slug

	f.subject, f.email = "usr_"+randomSuffix(), "dana@"+w.domain
	dana := oidcLogin(t, f, slug)
	var boxes []idOnly
	dana.must("GET", "/me/mailboxes", nil, &boxes, 200)
	pass := createAppPassword(t, dana, boxes[0].ID)

	if got := postSigned(t, hook, "wrong-secret", []byte(`{"id":"x","type":"user.suspend","data":{"targetType":"user","targetId":"`+f.subject+`"}}`)); got != 401 {
		t.Fatalf("badly signed webhook returned %d", got)
	}
	if got := signedEvent(t, hook, secret, "user.suspend", f.subject); got != 204 {
		t.Fatalf("suspend webhook returned %d", got)
	}
	if got := dana.do("GET", "/session", nil, nil); got != 401 {
		t.Fatalf("session survived provider suspension: %d", got)
	}
	if _, err := imapLogin(t, f.email, pass); err == nil {
		t.Fatal("app password survived provider suspension")
	}

	if got := signedEvent(t, hook, secret, "user.restore", f.subject); got != 204 {
		t.Fatalf("restore webhook returned %d", got)
	}
	c, err := imapLogin(t, f.email, pass)
	if err != nil {
		t.Fatalf("app password still blocked after provider restore: %v", err)
	}
	c.Close()

	var members []struct {
		UserID string `json:"userId"`
		Email  string `json:"email"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/members", nil, &members, 200)
	for _, m := range members {
		if m.Email == f.email {
			w.admin.must("PATCH", "/admin/users/"+m.UserID, map[string]string{"status": "suspended"}, nil, 204)
		}
	}
	signedEvent(t, hook, secret, "user.restore", f.subject)
	if _, err := imapLogin(t, f.email, pass); err == nil {
		t.Fatal("provider restore lifted a suspension an administrator made")
	}
}
