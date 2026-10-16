//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

type xoauth2 struct{ user, token string }

func (x xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

func (x xoauth2) Next([]byte) ([]byte, error) { return nil, nil }

func imapAuthenticate(t *testing.T, client sasl.Client) error {
	t.Helper()
	c, err := imapclient.DialStartTLS(env(t, "AETHER_TEST_IMAP", "dovecot:143"), &imapclient.Options{TLSConfig: mailTLS(t)})
	if err != nil {
		t.Fatalf("imap dial: %v", err)
	}
	defer c.Close()
	if err := c.Authenticate(client); err != nil {
		return err
	}
	_, err = c.Select("INBOX", nil).Wait()
	return err
}

func TestMailLoginWithAccessToken(t *testing.T) {
	w := setupWorld(t)
	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider struct {
		ID string `json:"id"`
	}
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret, "autoProvision": true,
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)

	f.subject, f.email = "halo-"+randomSuffix(), "dave@"+w.domain
	oidcLogin(t, f, slug)
	token := f.mint(f.subject, f.clientID)

	if err := imapAuthenticate(t, sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{Username: f.email, Token: token})); err != nil {
		t.Fatalf("OAUTHBEARER with a valid token: %v", err)
	}
	if err := imapAuthenticate(t, xoauth2{f.email, token}); err != nil {
		t.Fatalf("XOAUTH2 with a valid token: %v", err)
	}
	for name, client := range map[string]sasl.Client{
		"an unknown token":                 xoauth2{f.email, "at-forged"},
		"another person's mailbox":         xoauth2{"alice@" + w.domain, token},
		"a token issued to another client": xoauth2{f.email, f.mint(f.subject, "some-other-app")},
	} {
		if err := imapAuthenticate(t, client); err == nil {
			t.Errorf("IMAP accepted %s", name)
		}
	}

	c, err := smtp.DialStartTLS(env(t, "AETHER_TEST_SUBMISSION", "postfix:587"), mailTLS(t))
	if err != nil {
		t.Fatalf("smtp dial: %v", err)
	}
	defer c.Close()
	if err := c.Auth(sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{Username: f.email, Token: token})); err != nil {
		t.Fatalf("SMTP OAUTHBEARER with a valid token: %v", err)
	}
	if err := c.SendMail(f.email, []string{f.email}, strings.NewReader(rfc822(f.email, f.email, "token "+randomSuffix(), "Sent with an access token."))); err != nil {
		t.Fatalf("send after token login: %v", err)
	}
}
