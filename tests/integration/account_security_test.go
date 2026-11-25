//go:build integration

package integration

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func (c *apiClient) failure(method, path string, body any) (int, string) {
	c.t.Helper()
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, c.base+path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&e)
	return resp.StatusCode, e.Error.Code
}

type loginResult struct {
	CSRFToken string `json:"csrfToken"`
	TwoFactor *struct {
		TOTP        bool `json:"totp"`
		SecurityKey *struct {
			Challenge        []byte `json:"challenge"`
			RPID             string `json:"rpId"`
			AllowCredentials []struct {
				ID []byte `json:"id"`
			} `json:"allowCredentials"`
		} `json:"securityKey"`
	} `json:"twoFactor"`
}

func (c *apiClient) passwordStep(email, password string) loginResult {
	c.t.Helper()
	var r loginResult
	c.must("POST", "/auth/login", map[string]string{"email": email, "password": password}, &r, 200)
	if r.TwoFactor == nil {
		c.t.Fatalf("sign-in of %s did not ask for a second factor", email)
	}
	return r
}

func (c *apiClient) secondStep(body any) {
	c.t.Helper()
	var s struct {
		CSRFToken string `json:"csrfToken"`
	}
	c.must("POST", "/auth/login/two-factor", body, &s, 200)
	c.csrf = s.CSRFToken
}

func totpAt(t *testing.T, secret string, at time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatalf("decode TOTP secret: %v", err)
	}
	mac := hmac.New(sha1.New, key)
	binary.Write(mac, binary.BigEndian, uint64(at.Unix()/30))
	sum := mac.Sum(nil)
	o := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[o:o+4])&0x7fffffff)%1_000_000)
}

func enableTOTP(t *testing.T, c *apiClient) (string, []string) {
	t.Helper()
	var setup struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
		QRCode string `json:"qrCode"`
	}
	c.must("POST", "/me/totp", nil, &setup, 200)
	if setup.Secret == "" || len(setup.QRCode) < 100 {
		t.Fatalf("TOTP setup is missing the secret or QR code: %+v", setup)
	}
	if code, _ := c.failure("PUT", "/me/totp", map[string]string{"code": "000000"}); code != 422 {
		t.Fatalf("wrong TOTP confirmation code returned %d, want 422", code)
	}
	var out struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	c.must("PUT", "/me/totp", map[string]string{"code": totpAt(t, setup.Secret, time.Now())}, &out, 200)
	return setup.Secret, out.RecoveryCodes
}

func memberID(t *testing.T, w *world, email string) string {
	t.Helper()
	var members []struct {
		UserID string `json:"userId"`
		Email  string `json:"email"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/members", nil, &members, 200)
	for _, m := range members {
		if m.Email == email {
			return m.UserID
		}
	}
	t.Fatalf("%s is not a member", email)
	return ""
}

type sessionRow struct {
	ID      string `json:"id"`
	IP      string `json:"ip"`
	Current bool   `json:"current"`
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	other := newClient(t)
	other.login(alice, "alice-integration-pw")
	third := newClient(t)
	third.login(alice, "alice-integration-pw")

	var sessions []sessionRow
	w.alice.must("GET", "/me/sessions", nil, &sessions, 200)
	current := 0
	for _, s := range sessions {
		if s.Current {
			current++
		}
	}
	if len(sessions) < 3 || current != 1 {
		t.Fatalf("expected at least 3 sessions with one current, got %+v", sessions)
	}

	var thirdSessions []sessionRow
	third.must("GET", "/me/sessions", nil, &thirdSessions, 200)
	var thirdID string
	for _, s := range thirdSessions {
		if s.Current {
			thirdID = s.ID
		}
	}
	var bobSessions []sessionRow
	w.bob.must("GET", "/me/sessions", nil, &bobSessions, 200)
	if got := w.alice.do("DELETE", "/me/sessions/"+bobSessions[0].ID, nil, nil); got != 404 {
		t.Fatalf("ending another person's session returned %d, want 404", got)
	}
	w.alice.must("DELETE", "/me/sessions/"+thirdID, nil, nil, 204)
	if got := third.do("GET", "/session", nil, nil); got != 401 {
		t.Fatalf("signed-out session still works: %d", got)
	}
	w.bob.must("GET", "/session", nil, nil, 200)

	if code, reason := w.alice.failure("PUT", "/me/password", map[string]string{"currentPassword": "wrong-password", "newPassword": "a-new-long-password"}); code != 422 || reason != "wrong_password" {
		t.Fatalf("wrong current password returned %d %s", code, reason)
	}
	if code, reason := w.alice.failure("PUT", "/me/password", map[string]string{"currentPassword": "alice-integration-pw", "newPassword": "short"}); code != 422 || reason != "weak_password" {
		t.Fatalf("short new password returned %d %s", code, reason)
	}
	w.alice.must("PUT", "/me/password", map[string]string{"currentPassword": "alice-integration-pw", "newPassword": "a-new-long-password"}, nil, 204)
	if got := other.do("GET", "/session", nil, nil); got != 401 {
		t.Fatalf("other session survived a password change: %d", got)
	}
	w.alice.must("GET", "/session", nil, nil, 200)
	if got := newClient(t).do("POST", "/auth/login", map[string]string{"email": alice, "password": "alice-integration-pw"}, nil); got != 401 {
		t.Fatalf("old password still signs in: %d", got)
	}
	again := newClient(t)
	again.login(alice, "a-new-long-password")
	w.alice.must("DELETE", "/me/sessions", nil, nil, 204)
	if got := again.do("GET", "/session", nil, nil); got != 401 {
		t.Fatalf("sign out of other sessions left one signed in: %d", got)
	}
	w.alice.must("GET", "/me/sessions", nil, &sessions, 200)
	if len(sessions) != 1 || !sessions[0].Current {
		t.Fatalf("after signing out others, sessions are %+v", sessions)
	}
}

func TestTOTPSignInAndRecoveryCodes(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	secret, codes := enableTOTP(t, w.alice)
	if len(codes) != 10 {
		t.Fatalf("enabling the first method returned %d recovery codes, want 10", len(codes))
	}
	if code, reason := w.alice.failure("POST", "/me/totp", nil); code != 409 || reason != "totp_enabled" {
		t.Fatalf("starting a second TOTP setup returned %d %s", code, reason)
	}

	c := newClient(t)
	prompt := c.passwordStep(alice, "alice-integration-pw")
	if !prompt.TwoFactor.TOTP || prompt.TwoFactor.SecurityKey != nil || prompt.CSRFToken != "" {
		t.Fatalf("unexpected second factor prompt %+v", prompt)
	}
	if got := c.do("GET", "/session", nil, nil); got != 401 {
		t.Fatalf("password alone created a session: %d", got)
	}
	if code, reason := c.failure("POST", "/auth/login/two-factor", map[string]string{"code": "000000"}); code != 401 || reason != "invalid_code" {
		t.Fatalf("wrong code returned %d %s", code, reason)
	}
	next := totpAt(t, secret, time.Now().Add(30*time.Second))
	c.secondStep(map[string]string{"code": next})
	c.must("GET", "/session", nil, nil, 200)

	replay := newClient(t)
	replay.passwordStep(alice, "alice-integration-pw")
	if code, _ := replay.failure("POST", "/auth/login/two-factor", map[string]string{"code": next}); code != 401 {
		t.Fatalf("a used TOTP code was accepted again: %d", code)
	}
	replay.secondStep(map[string]string{"recoveryCode": " " + codes[0] + " "})
	replay.must("GET", "/session", nil, nil, 200)

	reuse := newClient(t)
	reuse.passwordStep(alice, "alice-integration-pw")
	if code, _ := reuse.failure("POST", "/auth/login/two-factor", map[string]string{"recoveryCode": codes[0]}); code != 401 {
		t.Fatalf("a used recovery code was accepted again: %d", code)
	}

	var status struct {
		TOTP          bool `json:"totp"`
		RecoveryCodes int  `json:"recoveryCodes"`
	}
	w.alice.must("GET", "/me/security", nil, &status, 200)
	if !status.TOTP || status.RecoveryCodes != 9 {
		t.Fatalf("security status %+v, want TOTP on and 9 codes", status)
	}
	if code, _ := w.alice.failure("POST", "/me/recovery-codes", map[string]string{"password": "wrong-password"}); code != 422 {
		t.Fatalf("regenerating codes with a wrong password returned %d", code)
	}
	var fresh struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	w.alice.must("POST", "/me/recovery-codes", map[string]string{"password": "alice-integration-pw"}, &fresh, 200)
	if len(fresh.RecoveryCodes) != 10 {
		t.Fatalf("regenerated codes %v", fresh.RecoveryCodes)
	}

	if code, _ := w.alice.failure("DELETE", "/me/totp", map[string]string{"password": "wrong-password"}); code != 422 {
		t.Fatalf("turning off TOTP with a wrong password returned %d", code)
	}
	w.alice.must("DELETE", "/me/totp", map[string]string{"password": "alice-integration-pw"}, nil, 204)
	w.alice.must("GET", "/me/security", nil, &status, 200)
	if status.TOTP || status.RecoveryCodes != 0 {
		t.Fatalf("after turning off the only method, status is %+v", status)
	}
	plain := newClient(t)
	plain.login(alice, "alice-integration-pw")
	if plain.csrf == "" {
		t.Fatal("sign-in without two-factor did not return a session")
	}
}

func TestTwoFactorAttemptsAndExpiry(t *testing.T) {
	w := setupWorld(t)
	enableTOTP(t, w.bob)
	if code, reason := newClient(t).failure("POST", "/auth/login/two-factor", map[string]string{"code": "123456"}); code != 401 || reason != "two_factor_expired" {
		t.Fatalf("second step without a pending sign-in returned %d %s", code, reason)
	}
	c := newClient(t)
	c.passwordStep("bob@"+w.domain, "bob-integration-pw")
	for i := range 5 {
		if code, reason := c.failure("POST", "/auth/login/two-factor", map[string]string{"code": "000000"}); code != 401 || reason != "invalid_code" {
			t.Fatalf("wrong code %d returned %d %s", i+1, code, reason)
		}
	}
	if code, reason := c.failure("POST", "/auth/login/two-factor", map[string]string{"code": "000000"}); code != 401 || reason != "two_factor_expired" {
		t.Fatalf("sixth attempt returned %d %s, want the sign-in to end", code, reason)
	}
}

type softKey struct {
	t      *testing.T
	key    *ecdsa.PrivateKey
	id     []byte
	count  uint32
	origin string
}

func newSoftKey(t *testing.T) *softKey {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	id := make([]byte, 32)
	rand.Read(id)
	return &softKey{t: t, key: key, id: id, origin: env(t, "AETHER_TEST_PUBLIC_URL", "")}
}

func (k *softKey) clientData(kind string, challenge []byte, origin string) []byte {
	raw, _ := json.Marshal(map[string]any{"type": kind, "challenge": base64.RawURLEncoding.EncodeToString(challenge), "origin": origin})
	return raw
}

func (k *softKey) authData(rpID string, flags byte, attested bool) []byte {
	hash := sha256.Sum256([]byte(rpID))
	out := binary.BigEndian.AppendUint32(append(hash[:], flags), k.count)
	if attested {
		out = append(out, make([]byte, 16)...)
		out = binary.BigEndian.AppendUint16(out, uint16(len(k.id)))
		out = append(out, k.id...)
	}
	return out
}

func (k *softKey) register(c *apiClient, origin string) (int, string) {
	var options struct {
		Challenge []byte `json:"challenge"`
		RP        struct {
			ID string `json:"id"`
		} `json:"rp"`
	}
	c.must("POST", "/me/security-keys/challenge", nil, &options, 200)
	spki, _ := x509.MarshalPKIXPublicKey(&k.key.PublicKey)
	return c.failure("POST", "/me/security-keys", map[string]any{"name": "Soft key", "credential": map[string]any{
		"id": k.id, "clientDataJSON": k.clientData("webauthn.create", options.Challenge, origin), "authenticatorData": k.authData(options.RP.ID, 0x41, true),
		"publicKey": spki, "transports": []string{"usb", "bogus"},
	}})
}

func (k *softKey) assert(rpID string, challenge []byte) map[string]any {
	k.count++
	client := k.clientData("webauthn.get", challenge, k.origin)
	data := k.authData(rpID, 0x01, false)
	clientHash := sha256.Sum256(client)
	digest := sha256.Sum256(append(append([]byte{}, data...), clientHash[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, k.key, digest[:])
	return map[string]any{"id": k.id, "clientDataJSON": client, "authenticatorData": data, "signature": sig}
}

func TestSecurityKeySignIn(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	key := newSoftKey(t)
	if code, reason := key.register(w.alice, "https://evil.example"); code != 422 || reason != "invalid_security_key" {
		t.Fatalf("registration from another origin returned %d %s", code, reason)
	}
	if code, reason := w.alice.failure("POST", "/me/security-keys", map[string]any{"name": "Again", "credential": map[string]any{"id": key.id}}); code != 422 || reason != "challenge_expired" {
		t.Fatalf("registration without a fresh challenge returned %d %s", code, reason)
	}
	if code, reason := key.register(w.alice, key.origin); code != 200 {
		t.Fatalf("registering a security key returned %d %s", code, reason)
	}

	var status struct {
		RecoveryCodes int `json:"recoveryCodes"`
		SecurityKeys  []struct {
			ID         string     `json:"id"`
			LastUsedAt *time.Time `json:"lastUsedAt"`
		} `json:"securityKeys"`
	}
	w.alice.must("GET", "/me/security", nil, &status, 200)
	if len(status.SecurityKeys) != 1 || status.RecoveryCodes != 10 {
		t.Fatalf("after adding a key, status is %+v", status)
	}

	c := newClient(t)
	prompt := c.passwordStep(alice, "alice-integration-pw")
	options := prompt.TwoFactor.SecurityKey
	if prompt.TwoFactor.TOTP || options == nil || len(options.AllowCredentials) != 1 || !bytes.Equal(options.AllowCredentials[0].ID, key.id) {
		t.Fatalf("unexpected prompt %+v", prompt.TwoFactor)
	}
	if code, _ := c.failure("POST", "/auth/login/two-factor", map[string]any{"securityKey": key.assert(options.RPID, []byte("not the challenge"))}); code != 401 {
		t.Fatalf("assertion over the wrong challenge returned %d", code)
	}
	if code, _ := c.failure("POST", "/auth/login/two-factor", map[string]any{"securityKey": key.assert("evil.example", options.Challenge)}); code != 401 {
		t.Fatalf("assertion for another relying party returned %d", code)
	}
	c.secondStep(map[string]any{"securityKey": key.assert(options.RPID, options.Challenge)})
	c.must("GET", "/session", nil, nil, 200)

	w.alice.must("GET", "/me/security", nil, &status, 200)
	if status.SecurityKeys[0].LastUsedAt == nil {
		t.Fatal("the security key's last use was not recorded")
	}
	if got := w.bob.do("DELETE", "/me/security-keys/"+status.SecurityKeys[0].ID, map[string]string{"password": "bob-integration-pw"}, nil); got != 404 {
		t.Fatalf("removing another person's security key returned %d", got)
	}
	w.alice.must("DELETE", "/me/security-keys/"+status.SecurityKeys[0].ID, map[string]string{"password": "alice-integration-pw"}, nil, 204)
	w.alice.must("GET", "/me/security", nil, &status, 200)
	if len(status.SecurityKeys) != 0 || status.RecoveryCodes != 0 {
		t.Fatalf("after removing the only key, status is %+v", status)
	}
}

func TestTwoFactorPolicyAndAdminControls(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	aliceID, bobID := memberID(t, w, alice), memberID(t, w, bob)
	org := "/organizations/" + w.orgID

	if code, _ := w.admin.failure("PUT", org+"/security", map[string]string{"twoFactorPolicy": "sometimes"}); code != 422 {
		t.Fatalf("invalid policy returned %d", code)
	}
	w.admin.must("POST", org+"/members", map[string]string{"email": alice, "role": "admin"}, nil, 204)
	w.admin.must("PUT", org+"/security", map[string]string{"twoFactorPolicy": "admins"}, nil, 200)

	var session struct {
		TwoFactorSetupRequired bool `json:"twoFactorSetupRequired"`
	}
	w.alice.must("GET", "/session", nil, &session, 200)
	if !session.TwoFactorSetupRequired {
		t.Fatal("an administrator without two-factor is not asked to set it up")
	}
	if code, reason := w.alice.failure("GET", "/me/mailboxes", nil); code != 403 || reason != "two_factor_setup_required" {
		t.Fatalf("an administrator without two-factor used the API: %d %s", code, reason)
	}
	w.bob.must("GET", "/me/mailboxes", nil, nil, 200)
	var policy struct {
		TwoFactorPolicy string `json:"twoFactorPolicy"`
	}
	w.admin.must("GET", org+"/security", nil, &policy, 200)
	if policy.TwoFactorPolicy != "admins" {
		t.Fatalf("policy is %q", policy.TwoFactorPolicy)
	}

	w.admin.must("PUT", org+"/security", map[string]string{"twoFactorPolicy": "everyone"}, nil, 200)
	if code, _ := w.bob.failure("GET", "/me/mailboxes", nil); code != 403 {
		t.Fatalf("a member without two-factor used the API under the everyone policy: %d", code)
	}
	fresh := newClient(t)
	fresh.login(bob, "bob-integration-pw")
	fresh.must("GET", "/session", nil, &session, 200)
	if !session.TwoFactorSetupRequired {
		t.Fatal("a fresh password sign-in skipped the required setup")
	}
	enableTOTP(t, w.alice)
	w.alice.must("GET", "/me/mailboxes", nil, nil, 200)
	w.alice.must("GET", "/session", nil, &session, 200)
	if session.TwoFactorSetupRequired {
		t.Fatal("setup is still required after enabling TOTP")
	}

	var members []struct {
		Email          string `json:"email"`
		PasswordSignIn bool   `json:"passwordSignIn"`
		TwoFactor      bool   `json:"twoFactor"`
	}
	w.admin.must("GET", org+"/members", nil, &members, 200)
	for _, m := range members {
		if m.Email == alice && (!m.TwoFactor || !m.PasswordSignIn) || m.Email == bob && m.TwoFactor {
			t.Fatalf("member list reports %+v", m)
		}
	}

	var bobSessions []sessionRow
	w.alice.must("GET", org+"/members/"+bobID+"/sessions", nil, &bobSessions, 200)
	if len(bobSessions) < 2 {
		t.Fatalf("an organization administrator sees %d of bob's sessions", len(bobSessions))
	}
	w.alice.must("DELETE", org+"/members/"+bobID+"/sessions/"+bobSessions[0].ID, nil, nil, 204)
	w.alice.must("DELETE", org+"/members/"+bobID+"/sessions", nil, nil, 204)
	if w.bob.do("GET", "/session", nil, nil) != 401 || fresh.do("GET", "/session", nil, nil) != 401 {
		t.Fatal("signing a member out everywhere left a session")
	}
	w.admin.must("POST", org+"/members", map[string]string{"email": bob, "role": "owner"}, nil, 204)
	if got := w.alice.do("GET", org+"/members/"+bobID+"/sessions", nil, nil); got != 403 {
		t.Fatalf("an administrator listed an owner's sessions: %d", got)
	}
	w.admin.must("GET", org+"/members/"+bobID+"/sessions", nil, nil, 200)

	if got := w.alice.do("PUT", "/admin/users/"+bobID+"/password", map[string]string{"password": "an-admin-chosen-password"}, nil); got != 403 {
		t.Fatalf("an organization administrator reset a password: %d", got)
	}
	if got := w.alice.do("DELETE", "/admin/users/"+aliceID+"/two-factor", nil, nil); got != 403 {
		t.Fatalf("an organization administrator reset two-factor: %d", got)
	}
	if code, reason := w.admin.failure("PUT", "/admin/users/"+bobID+"/password", map[string]string{"password": "short"}); code != 422 || reason != "weak_password" {
		t.Fatalf("short admin password returned %d %s", code, reason)
	}
	again := newClient(t)
	again.login(bob, "bob-integration-pw")
	w.admin.must("PUT", "/admin/users/"+bobID+"/password", map[string]string{"password": "an-admin-chosen-password"}, nil, 204)
	if got := again.do("GET", "/session", nil, nil); got != 401 {
		t.Fatalf("a session survived an administrator password reset: %d", got)
	}
	newClient(t).login(bob, "an-admin-chosen-password")

	aliceAgain := newClient(t)
	aliceAgain.passwordStep(alice, "alice-integration-pw")
	w.admin.must("DELETE", "/admin/users/"+aliceID+"/two-factor", nil, nil, 204)
	var status struct {
		TOTP bool `json:"totp"`
	}
	w.alice.must("GET", "/me/security", nil, &status, 200)
	if status.TOTP {
		t.Fatal("two-factor reset left the authenticator app")
	}
	w.alice.must("GET", "/session", nil, &session, 200)
	if !session.TwoFactorSetupRequired {
		t.Fatal("after a two-factor reset under a requirement, setup is not required again")
	}
	w.admin.must("PUT", org+"/security", map[string]string{"twoFactorPolicy": "off"}, nil, 200)
	w.alice.must("GET", "/me/mailboxes", nil, nil, 200)
}

func TestSecurityEndpointsRefuseAPIKeys(t *testing.T) {
	w := setupWorld(t)
	var key struct {
		Token string `json:"token"`
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/api-keys", map[string]any{"name": "security", "scopes": []string{"mailboxes:write"}}, &key, 201)
	c := newClient(t)
	c.bearer = key.Token
	for _, path := range []string{"/me/security", "/me/sessions", "/organizations/" + w.orgID + "/security"} {
		if got := c.do("GET", path, nil, nil); got != 403 {
			t.Errorf("API key reached %s: %d", path, got)
		}
	}
}
