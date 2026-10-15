//go:build integration

package integration

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type fakeIssuer struct {
	t        *testing.T
	issuer   string
	clientID string
	secret   string
	key      *rsa.PrivateKey
	mu       sync.Mutex
	codes    map[string]url.Values
	subject  string
	email    string
	groups   []string
	picture  []byte
}

func startFakeIssuer(t *testing.T) *fakeIssuer {
	host, _ := os.Hostname()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIssuer{t: t, key: key, clientID: "aether-it", secret: "s3cret-" + randomSuffix(), codes: map[string]url.Values{}}
	f.issuer = "http://" + host + ":" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.issuer, "authorization_endpoint": f.issuer + "/authorize", "token_endpoint": f.issuer + "/token",
			"jwks_uri": f.issuer + "/keys", "end_session_endpoint": f.issuer + "/logout", "response_types_supported": []string{"code"},
			"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"},
			"code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"openid", "profile", "email", "groups"},
			"claims_supported": []string{"sub", "email", "email_verified", "name", "groups"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/picture", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(f.picture)
	})
	go http.Serve(ln, mux)
	return f
}

func (f *fakeIssuer) authorize(authURL string) (code string, q url.Values) {
	u, _ := url.Parse(authURL)
	q = u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		f.t.Fatalf("authorization request is missing PKCE S256: %s", authURL)
	}
	if q.Get("client_id") != f.clientID || !strings.Contains(q.Get("scope"), "openid") {
		f.t.Fatalf("unexpected authorization request: %s", authURL)
	}
	code = "code-" + randomSuffix()
	f.mu.Lock()
	f.codes[code] = q
	f.mu.Unlock()
	return code, q
}

func (f *fakeIssuer) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	r.ParseForm()
	user, pass, ok := r.BasicAuth()
	if !ok {
		user, pass = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	f.mu.Lock()
	q, found := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || user != f.clientID || pass != f.secret || base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") || r.PostForm.Get("redirect_uri") != q.Get("redirect_uri") {
		f.t.Logf("token request rejected: found=%v client=%v secret=%v pkce=%v redirect=%q vs %q", found, user == f.clientID, pass == f.secret, base64.RawURLEncoding.EncodeToString(sum[:]) == q.Get("code_challenge"), r.PostForm.Get("redirect_uri"), q.Get("redirect_uri"))
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		return
	}
	signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	now := time.Now()
	idToken, _ := jwt.Signed(signer).Claims(jwt.Claims{Issuer: f.issuer, Subject: f.subject, Audience: jwt.Audience{f.clientID}, IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute))}).
		Claims(map[string]any{"nonce": q.Get("nonce"), "email": f.email, "email_verified": true, "name": "Halo User", "groups": f.groups, "picture": f.issuer + "/picture"}).Serialize()
	json.NewEncoder(w).Encode(map[string]any{"access_token": "at-" + randomSuffix(), "token_type": "Bearer", "expires_in": 300, "id_token": idToken})
}

func oidcAuthorize(t *testing.T, f *fakeIssuer, slug string) (*apiClient, string, url.Values) {
	t.Helper()
	browser := newClient(t)
	browser.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	start, err := browser.http.Get(browser.base + "/auth/oidc/" + slug + "/start?return_to=/mail")
	if err != nil || start.StatusCode != http.StatusFound {
		t.Fatalf("start: %v %v", err, start)
	}
	code, q := f.authorize(start.Header.Get("Location"))
	return browser, browser.base + "/auth/oidc/" + slug + "/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(q.Get("state")), q
}

func oidcLogin(t *testing.T, f *fakeIssuer, slug string) *apiClient {
	t.Helper()
	browser, callback, _ := oidcAuthorize(t, f, slug)
	resp, err := browser.http.Get(callback)
	if err != nil || resp.Header.Get("Location") != "/mail" {
		t.Fatalf("oidc login failed: %v %v", err, resp)
	}
	var s struct {
		CSRFToken string `json:"csrfToken"`
	}
	browser.must("GET", "/session", nil, &s, 200)
	browser.csrf = s.CSRFToken
	return browser
}

func TestOIDCLoginWithPKCE(t *testing.T) {
	w := setupWorld(t)
	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider struct {
		ID          string `json:"id"`
		RedirectURI string `json:"redirectUri"`
	}
	var discovered map[string]any
	if got := w.admin.do("POST", "/admin/identity-providers/discover", map[string]string{"issuer": f.issuer}, &discovered); got != 200 || discovered["pkceS256"] != true || discovered["groupsClaim"] != true {
		t.Fatalf("discovery returned %d %v", got, discovered)
	}
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret,
		"autoProvision": true, "groupMappings": []map[string]string{{"group": "mail-admins", "orgId": w.orgID, "role": "admin"}},
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)

	f.subject, f.email, f.groups, f.picture = "halo-"+randomSuffix(), "carol@"+w.domain, []string{"mail-admins"}, picture(t, 1200)
	browser, callback, q := oidcAuthorize(t, f, slug)
	if q.Get("redirect_uri") != provider.RedirectURI {
		t.Fatalf("redirect_uri %q, want %q", q.Get("redirect_uri"), provider.RedirectURI)
	}

	attacker := newClient(t)
	attacker.http.CheckRedirect = browser.http.CheckRedirect
	if resp, _ := attacker.http.Get(callback); resp == nil || !strings.Contains(resp.Header.Get("Location"), "error=state_mismatch") {
		t.Fatalf("callback without the browser's state cookie was not rejected: %v", resp)
	}

	resp, err := browser.http.Get(callback)
	if err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/mail" {
		t.Fatalf("callback: %v status=%d location=%q", err, resp.StatusCode, resp.Header.Get("Location"))
	}
	var session struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
		Memberships []struct {
			OrgID string `json:"orgId"`
			Role  string `json:"role"`
		} `json:"memberships"`
	}
	browser.must("GET", "/session", nil, &session, 200)
	if session.User.Email != f.email || len(session.Memberships) != 1 || session.Memberships[0].Role != "admin" {
		t.Fatalf("unexpected session after OIDC login: %+v", session)
	}
	var mailboxes []struct {
		Address string `json:"address"`
	}
	browser.must("GET", "/me/mailboxes", nil, &mailboxes, 200)
	if len(mailboxes) != 1 || mailboxes[0].Address != f.email {
		t.Fatalf("auto-provisioned mailbox missing: %+v", mailboxes)
	}

	if env(t, "AETHER_TEST_WEBHOOKS", "") == "true" {
		avatar := func(c *apiClient) []byte { return c.raw("GET", "/avatars/"+f.email) }
		waitFor(t, "the provider's picture to be imported", func() bool { return bytes.HasPrefix(avatar(browser), []byte("\xff\xd8")) })
		var csrf struct {
			CSRFToken string `json:"csrfToken"`
		}
		browser.must("GET", "/session", nil, &csrf, 200)
		browser.csrf = csrf.CSRFToken
		if code := uploadAvatar(t, browser, picture(t, 64)); code != 204 {
			t.Fatalf("upload: %d", code)
		}
		uploaded := avatar(browser)
		oidcLogin(t, f, slug)
		time.Sleep(2 * time.Second)
		if !bytes.Equal(avatar(browser), uploaded) {
			t.Fatal("signing in again replaced the picture the user uploaded")
		}
	}

	replay, _ := browser.http.Get(callback)
	if replay == nil || !strings.Contains(replay.Header.Get("Location"), "error=") {
		t.Fatal("authorization code/state replay was accepted")
	}
}
