//go:build integration

package integration

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

func sieveLogin(t *testing.T, user, pass string) error {
	t.Helper()
	raw, err := net.DialTimeout("tcp", env(t, "AETHER_TEST_SIEVE", "dovecot:4190"), 10*time.Second)
	if err != nil {
		t.Fatalf("managesieve dial: %v", err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(20 * time.Second))
	var conn net.Conn = raw
	r := bufio.NewReader(conn)
	reply := func() string {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("managesieve read: %v", err)
			}
			if strings.HasPrefix(line, "OK") || strings.HasPrefix(line, "NO") || strings.HasPrefix(line, "BYE") {
				return line
			}
		}
	}
	reply()
	fmt.Fprint(conn, "STARTTLS\r\n")
	if line := reply(); !strings.HasPrefix(line, "OK") {
		t.Fatalf("managesieve STARTTLS: %s", line)
	}
	conn = tls.Client(raw, mailTLS(t))
	r = bufio.NewReader(conn)
	reply()
	fmt.Fprintf(conn, "AUTHENTICATE \"PLAIN\" \"%s\"\r\n", base64.StdEncoding.EncodeToString([]byte("\x00"+user+"\x00"+pass)))
	if line := reply(); !strings.HasPrefix(line, "OK") {
		return fmt.Errorf("managesieve: %s", strings.TrimSpace(line))
	}
	return nil
}

func davStatus(t *testing.T, user, pass string) int {
	t.Helper()
	req, _ := http.NewRequest("PROPFIND", env(t, "AETHER_TEST_API", "http://api:8080")+"/dav/", nil)
	req.SetBasicAuth(user, pass)
	req.Header.Set("Depth", "0")
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatalf("dav: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestMailboxProtocolsAndScopedAppPasswords(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	submission := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	mailbox := "/mailboxes/" + w.aliceMailbox
	var mail, dav struct {
		Password string `json:"password"`
		Scope    string `json:"scope"`
	}
	w.alice.must("POST", "/me/app-passwords", map[string]string{"name": "phone mail", "mailboxId": w.aliceMailbox, "scope": "mail"}, &mail, 201)
	w.alice.must("POST", "/me/app-passwords", map[string]string{"name": "calendar", "mailboxId": w.aliceMailbox, "scope": "dav"}, &dav, 201)
	if mail.Scope != "mail" || dav.Scope != "dav" {
		t.Fatalf("scopes = %q, %q", mail.Scope, dav.Scope)
	}
	if code := errorCode(t, w.alice, "POST", "/me/app-passwords", map[string]string{"name": "x", "mailboxId": w.aliceMailbox, "scope": "imap"}, 422); code != "invalid_scope" {
		t.Fatalf("unknown scope: %q, want invalid_scope", code)
	}
	var listed []map[string]any
	w.alice.must("GET", "/me/app-passwords", nil, &listed, 200)
	if !slices.ContainsFunc(listed, func(p map[string]any) bool { return p["name"] == "integration" && p["scope"] == "all" }) {
		t.Fatalf("an app password created without a scope is not listed as all: %+v", listed)
	}

	imapWorks := func(pass string) bool {
		c, err := imapLogin(t, alice, pass)
		if err == nil {
			c.Close()
		}
		return err == nil
	}
	smtpWorks := func(pass string) bool {
		return submit(t, submission, alice, pass, alice, []string{bob}, rfc822(alice, bob, "protocols "+randomSuffix(), "x"), false) == nil
	}
	sieveWorks := func(pass string) bool { return sieveLogin(t, alice, pass) == nil }
	davWorks := func(pass string) bool { return davStatus(t, alice, pass) == http.StatusPermanentRedirect }
	expect := func(when string, all, scopedMail, scopedDAV [4]bool) {
		t.Helper()
		for name, c := range map[string]struct {
			pass string
			want [4]bool
		}{"all": {w.aliceAppPassword, all}, "mail": {mail.Password, scopedMail}, "dav": {dav.Password, scopedDAV}} {
			got := [4]bool{imapWorks(c.pass), smtpWorks(c.pass), sieveWorks(c.pass), davWorks(c.pass)}
			if got != c.want {
				t.Errorf("%s: %s app password [imap smtp sieve dav] = %v, want %v", when, name, got, c.want)
			}
		}
	}
	expect("every protocol on", [4]bool{true, true, true, true}, [4]bool{true, true, false, false}, [4]bool{false, false, false, true})

	ic, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("imap login: %v", err)
	}
	defer ic.Close()
	if _, err := ic.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("select: %v", err)
	}
	setProtocols := func(protocols ...string) {
		t.Helper()
		var m struct {
			Protocols []string `json:"protocols"`
		}
		w.admin.must("PATCH", mailbox, map[string]any{"protocols": append([]string{}, protocols...)}, &m, 200)
		if !slices.Equal(m.Protocols, protocols) {
			t.Fatalf("protocols = %v, want %v", m.Protocols, protocols)
		}
	}
	start := time.Now()
	setProtocols("smtp", "sieve", "dav", "webmail")
	waitKicked(t, ic, start)
	expect("imap off", [4]bool{false, true, true, true}, [4]bool{false, true, false, false}, [4]bool{false, false, false, true})
	w.alice.must("GET", mailbox+"/folders", nil, nil, 200)

	setProtocols("imap", "webmail")
	expect("smtp, sieve and dav off", [4]bool{true, false, false, false}, [4]bool{true, false, false, false}, [4]bool{false, false, false, false})
	w.alice.must("POST", mailbox+"/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": "web send " + randomSuffix(), "text": "x"}, nil, 202)
	w.alice.must("PUT", mailbox+"/rules", []map[string]any{}, nil, 200)

	setProtocols("imap", "smtp", "sieve", "dav")
	if code := w.alice.do("GET", mailbox+"/folders", nil, nil); code != 404 {
		t.Fatalf("Aether Mail with webmail off returned %d, want 404", code)
	}
	if code := w.alice.do("POST", mailbox+"/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": "x", "text": "x"}, nil); code != 404 {
		t.Fatalf("sending in Aether Mail with webmail off returned %d, want 404", code)
	}
	w.admin.must("GET", mailbox+"/rules", nil, nil, 200)
	expect("webmail off", [4]bool{true, true, true, true}, [4]bool{true, true, false, false}, [4]bool{false, false, false, true})

	if code := errorCode(t, w.admin, "PATCH", mailbox, map[string]any{"protocols": []string{"pop3"}}, 422); code != "invalid_protocols" {
		t.Fatalf("unknown protocol: %q, want invalid_protocols", code)
	}
	if code := w.alice.do("PATCH", mailbox, map[string]any{"protocols": []string{}}, nil); code != 403 {
		t.Fatalf("a member changed protocols: %d, want 403", code)
	}

	setProtocols()
	expect("everything off", [4]bool{false, false, false, false}, [4]bool{false, false, false, false}, [4]bool{false, false, false, false})
	setProtocols("imap", "smtp", "sieve", "dav", "webmail")
	expect("everything back on", [4]bool{true, true, true, true}, [4]bool{true, true, false, false}, [4]bool{false, false, false, true})
}

func TestProtocolDefaultsForNewMailboxes(t *testing.T) {
	w := setupWorld(t)
	org := "/organizations/" + w.orgID
	var got struct {
		MailboxProtocols []string `json:"mailboxProtocols"`
	}
	w.admin.must("GET", org, nil, &got, 200)
	if !slices.Equal(got.MailboxProtocols, []string{"imap", "smtp", "sieve", "dav", "webmail"}) {
		t.Fatalf("default mailbox protocols = %v", got.MailboxProtocols)
	}
	if code := w.alice.do("PATCH", org, map[string]any{"mailboxProtocols": []string{"webmail"}}, nil); code != 403 {
		t.Fatalf("a member changed the organization defaults: %d, want 403", code)
	}
	if code := errorCode(t, w.admin, "PATCH", org, map[string]any{"mailboxProtocols": []string{"imap", "imap"}}, 422); code != "invalid_protocols" {
		t.Fatalf("duplicate protocol: %q, want invalid_protocols", code)
	}
	w.admin.must("PATCH", org, map[string]any{"mailboxProtocols": []string{"dav", "webmail"}}, &got, 200)
	if !slices.Equal(got.MailboxProtocols, []string{"dav", "webmail"}) {
		t.Fatalf("saved defaults = %v", got.MailboxProtocols)
	}

	var carol struct {
		ID        string   `json:"id"`
		Protocols []string `json:"protocols"`
	}
	w.admin.must("POST", org+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "carol", "password": "carol-integration-pw"}, &carol, 201)
	if !slices.Equal(carol.Protocols, []string{"dav", "webmail"}) {
		t.Fatalf("new mailbox protocols = %v, want the organization defaults", carol.Protocols)
	}
	var mailboxes []struct {
		ID        string   `json:"id"`
		Protocols []string `json:"protocols"`
	}
	w.admin.must("GET", org+"/mailboxes", nil, &mailboxes, 200)
	for _, m := range mailboxes {
		if m.ID == w.aliceMailbox && len(m.Protocols) != 5 {
			t.Fatalf("an existing mailbox changed with the defaults: %v", m.Protocols)
		}
	}
	c := newClient(t)
	c.login("carol@"+w.domain, "carol-integration-pw")
	pass := createAppPassword(t, c, carol.ID)
	if ic, err := imapLogin(t, "carol@"+w.domain, pass); err == nil {
		ic.Close()
		t.Fatal("IMAP works on a mailbox created without it")
	}
	if code := davStatus(t, "carol@"+w.domain, pass); code != http.StatusPermanentRedirect {
		t.Fatalf("DAV on a mailbox created with it returned %d", code)
	}
}

func TestProtocolsLimitAccessTokensAndSharedMembers(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	f, _, _ := linkedIssuer(t, w, alice)
	token := f.mint(f.subject, f.clientID)
	smtpToken := func() error {
		c, err := smtp.DialStartTLS(env(t, "AETHER_TEST_SUBMISSION", "postfix:587"), mailTLS(t))
		if err != nil {
			t.Fatalf("smtp dial: %v", err)
		}
		defer c.Close()
		return c.Auth(sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{Username: alice, Token: token}))
	}
	if c, err := imapTokenLogin(t, alice, token); err != nil {
		t.Fatalf("IMAP with an access token: %v", err)
	} else {
		c.Close()
	}
	if err := smtpToken(); err != nil {
		t.Fatalf("SMTP with an access token: %v", err)
	}
	w.admin.must("PATCH", "/mailboxes/"+w.aliceMailbox, map[string]any{"protocols": []string{"smtp", "webmail"}}, nil, 200)
	if c, err := imapTokenLogin(t, alice, token); err == nil {
		c.Close()
		t.Fatal("IMAP accepted an access token with IMAP off")
	}
	if err := smtpToken(); err != nil {
		t.Fatalf("SMTP with an access token and SMTP on: %v", err)
	}
	w.admin.must("PATCH", "/mailboxes/"+w.aliceMailbox, map[string]any{"protocols": []string{"imap", "webmail"}}, nil, 200)
	if err := smtpToken(); err == nil {
		t.Fatal("SMTP accepted an access token with SMTP off")
	}

	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": bob, "access": "full", "sendAs": true})
	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	ic, err := imapLogin(t, bob, bobPass)
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer ic.Close()
	if !slices.Contains(imapNames(t, ic), inbox) {
		t.Fatalf("a member does not see %s", inbox)
	}
	if _, err := ic.Select(inbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", inbox, err)
	}
	submission := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	if err := submit(t, submission, bob, bobPass, shared.Address, []string{alice}, rfc822(shared.Address, alice, "as support", "x"), false); err != nil {
		t.Fatalf("member send as over SMTP: %v", err)
	}

	start := time.Now()
	w.admin.must("PATCH", "/mailboxes/"+shared.ID, map[string]any{"protocols": []string{"webmail"}}, nil, 200)
	waitKicked(t, ic, start)
	again, err := imapLogin(t, bob, bobPass)
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer again.Close()
	if slices.Contains(imapNames(t, again), inbox) {
		t.Fatalf("a member still sees %s with IMAP off", inbox)
	}
	if err := submit(t, submission, bob, bobPass, shared.Address, []string{alice}, rfc822(shared.Address, alice, "as support", "x"), false); err == nil {
		t.Fatal("a member sent as the shared mailbox over SMTP with SMTP off")
	}
	w.bob.must("GET", "/mailboxes/"+shared.ID+"/folders", nil, nil, 200)

	w.admin.must("PATCH", "/mailboxes/"+shared.ID, map[string]any{"protocols": []string{"imap", "smtp", "webmail"}}, nil, 200)
	waitFor(t, "the member to see the shared mailbox again", func() bool {
		c, err := imapLogin(t, bob, bobPass)
		if err != nil {
			return false
		}
		defer c.Close()
		return slices.Contains(imapNames(t, c), inbox)
	})
}
