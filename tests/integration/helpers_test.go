//go:build integration

package integration

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

func env(t *testing.T, key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if fallback == "" {
		t.Skipf("%s is not set", key)
	}
	return fallback
}

func randomSuffix() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func mailTLS(t *testing.T) *tls.Config {
	pem, err := os.ReadFile(env(t, "AETHER_TEST_CA_FILE", "/tls/fullchain.pem"))
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	return &tls.Config{RootCAs: pool, ServerName: env(t, "AETHER_TEST_MAIL_HOSTNAME", "mail.aether.test")}
}

type apiClient struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func newClient(t *testing.T) *apiClient {
	jar, _ := cookiejar.New(nil)
	return &apiClient{t: t, base: env(t, "AETHER_TEST_API", "http://api:8080") + "/api/v1", http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (c *apiClient) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		r = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode
}

func (c *apiClient) raw(method, path string) []byte {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body
}

func (c *apiClient) must(method, path string, body any, out any, want int) {
	c.t.Helper()
	if got := c.do(method, path, body, out); got != want {
		c.t.Fatalf("%s %s: status %d, want %d", method, path, got, want)
	}
}

var adminSession struct {
	jar  http.CookieJar
	csrf string
}

func adminClient(t *testing.T) *apiClient {
	c := newClient(t)
	if adminSession.jar == nil {
		c.login(env(t, "AETHER_TEST_ADMIN_EMAIL", ""), env(t, "AETHER_TEST_ADMIN_PASSWORD", ""))
		adminSession.jar, adminSession.csrf = c.http.Jar, c.csrf
	}
	c.http.Jar, c.csrf = adminSession.jar, adminSession.csrf
	return c
}

func (c *apiClient) login(email, password string) {
	c.t.Helper()
	var s struct {
		CSRFToken string `json:"csrfToken"`
	}
	c.must("POST", "/auth/login", map[string]string{"email": email, "password": password}, &s, 200)
	c.csrf = s.CSRFToken
}

type idOnly struct {
	ID string `json:"id"`
}

func submit(t *testing.T, addr, user, pass, from string, to []string, msg string, implicitTLS bool) error {
	t.Helper()
	var c *smtp.Client
	var err error
	if user == "" {
		c, err = smtp.Dial(addr)
	} else {
		c, err = smtp.DialStartTLS(addr, mailTLS(t))
	}
	if err != nil {
		return err
	}
	defer c.Close()
	if user != "" {
		if err := c.Auth(sasl.NewPlainClient("", user, pass)); err != nil {
			return err
		}
	}
	return c.SendMail(from, to, strings.NewReader(msg))
}

func rfc822(from, to, subject, body string) string {
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@integration.test>\r\n\r\n%s\r\n",
		from, to, subject, time.Now().Format(time.RFC1123Z), randomSuffix(), body)
}

func imapLogin(t *testing.T, user, pass string) (*imapclient.Client, error) {
	t.Helper()
	c, err := imapclient.DialStartTLS(env(t, "AETHER_TEST_IMAP", "dovecot:143"), &imapclient.Options{TLSConfig: mailTLS(t)})
	if err != nil {
		t.Fatalf("imap dial: %v", err)
	}
	if err := c.Login(user, pass).Wait(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func imapFindSubject(t *testing.T, c *imapclient.Client, folder, subject string) []byte {
	t.Helper()
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("select %s: %v", folder, err)
	}
	data, err := c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: subject}}}, nil).Wait()
	if err != nil || len(data.AllUIDs()) == 0 {
		return nil
	}
	section := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(imap.UIDSetNum(data.AllUIDs()[0]), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil || len(msgs) == 0 {
		return nil
	}
	return msgs[0].FindBodySection(section)
}
