//go:build integration

package integration

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

func ownAddress(t *testing.T) string {
	t.Helper()
	conn, err := net.Dial("tcp", env(t, "AETHER_TEST_IMAP", "dovecot:143"))
	if err != nil {
		t.Fatalf("dial dovecot: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.TCPAddr).IP.String()
}

func smtpAuth(t *testing.T, user, pass string) error {
	t.Helper()
	c, err := smtp.DialStartTLS(env(t, "AETHER_TEST_SUBMISSION", "postfix:587"), mailTLS(t))
	if err != nil {
		t.Fatalf("dial submission: %v", err)
	}
	defer c.Close()
	return c.Auth(sasl.NewPlainClient("", user, pass))
}

func TestFailedSignInsDoNotStallTrustedNetwork(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	for i := range 12 {
		if c, err := imapLogin(t, alice, "wrong-"+strconv.Itoa(i)); err == nil {
			c.Logout()
			t.Fatal("IMAP accepted a wrong password")
		}
	}
	start := time.Now()
	c, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("IMAP login from an allow-listed address after failures: %v", err)
	}
	c.Logout()
	if err := smtpAuth(t, alice, w.aliceAppPassword); err != nil {
		t.Fatalf("SMTP AUTH from an allow-listed address after failures: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("logins after failed attempts took %s; the per-address penalty is back", d)
	}
	var blocks []struct {
		Address string `json:"address"`
	}
	w.admin.must("GET", "/admin/blocked-addresses", nil, &blocks, 200)
	for _, b := range blocks {
		if b.Address == ownAddress(t) {
			t.Fatalf("allow-listed address %s was blocked", b.Address)
		}
	}
}

func TestBlockedAddressIsRefusedUntilUnblocked(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	ip := ownAddress(t)
	db := testDB(t)
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO blocked_addresses (address, protocol, failures, blocked_until) VALUES ($1, 'imap', 10, now() + interval '10 minutes')
		ON CONFLICT (address) DO UPDATE SET blocked_until = excluded.blocked_until RETURNING id`, ip).Scan(&id); err != nil {
		t.Fatalf("insert block: %v", err)
	}
	t.Cleanup(func() { db.Exec(context.Background(), `DELETE FROM blocked_addresses WHERE id = $1`, id) })

	if c, err := imapLogin(t, alice, w.aliceAppPassword); err == nil {
		c.Logout()
		t.Fatal("IMAP accepted a login from a blocked address")
	}
	if err := smtpAuth(t, alice, w.aliceAppPassword); err == nil {
		t.Fatal("SMTP AUTH accepted a login from a blocked address")
	}

	var blocks []struct {
		ID      string `json:"id"`
		Address string `json:"address"`
	}
	w.admin.must("GET", "/admin/blocked-addresses", nil, &blocks, 200)
	found := false
	for _, b := range blocks {
		found = found || (b.ID == id && b.Address == ip)
	}
	if !found {
		t.Fatalf("blocked address %s missing from %v", ip, blocks)
	}
	if w.alice.do("GET", "/admin/blocked-addresses", nil, nil) != 403 {
		t.Fatal("a non-administrator could list blocked addresses")
	}
	w.admin.must("DELETE", "/admin/blocked-addresses/"+id, nil, nil, 204)
	w.admin.must("DELETE", "/admin/blocked-addresses/"+id, nil, nil, 404)

	c, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("IMAP login after unblocking: %v", err)
	}
	c.Logout()
	if err := smtpAuth(t, alice, w.aliceAppPassword); err != nil {
		t.Fatalf("SMTP AUTH after unblocking: %v", err)
	}
}
