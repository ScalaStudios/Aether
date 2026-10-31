package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

func fakeIMAP(t *testing.T, answer bool) (*Client, <-chan []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	afterLogin := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			afterLogin <- nil
			return
		}
		defer conn.Close()
		io.WriteString(conn, "* OK [CAPABILITY IMAP4rev1 STARTTLS ACL] ready\r\n")
		r := bufio.NewReader(conn)
		var recorded bytes.Buffer
		loggedIn := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				afterLogin <- recorded.Bytes()
				return
			}
			if loggedIn {
				recorded.WriteString(line)
			}
			tag, rest, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
			verb, _, _ := strings.Cut(rest, " ")
			switch strings.ToUpper(verb) {
			case "STARTTLS":
				io.WriteString(conn, tag+" OK [CAPABILITY IMAP4rev1 ACL] begin TLS\r\n")
				tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
				if err := tc.Handshake(); err != nil {
					afterLogin <- nil
					return
				}
				conn = tc
				r = bufio.NewReader(conn)
			case "CAPABILITY":
				io.WriteString(conn, "* CAPABILITY IMAP4rev1 ACL\r\n"+tag+" OK\r\n")
			case "LOGIN":
				loggedIn = true
				io.WriteString(conn, tag+" OK [CAPABILITY IMAP4rev1 ACL] logged in\r\n")
			default:
				if answer {
					io.WriteString(conn, tag+" OK\r\n")
				}
			}
		}
	}()
	c := &Client{imapAddr: ln.Addr().String(), tls: &tls.Config{ServerName: "localhost", RootCAs: roots, MinVersion: tls.VersionTLS12}, masterUser: "aether", masterPassword: "pw"}
	return c, afterLogin
}

func TestSetACLSendsNothingAfterAPauseThatOutlivedItsDeadline(t *testing.T) {
	c, afterLogin := fakeIMAP(t, true)
	err := c.withBounded(context.Background(), "team@example.test", func(ic *imapclient.Client, conn *boundedConn) error {
		paused := func(bool) error {
			conn.until.Store(time.Now().Add(-time.Second).UnixNano())
			return nil
		}
		return setACL(ic, conn, "INBOX", "bob@example.test", FullRights, paused)
	})
	if !errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("setACL after a pause that outlived the deadline returned %v, want the refused write's error", err)
	}
	if got := <-afterLogin; bytes.Contains(got, []byte("SETACL")) {
		t.Fatalf("the stale SETACL reached the server: %q", got)
	}
}

func TestBoundedConnClampsBothDeadlines(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	held := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			held <- c
		}
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	defer func() { (<-held).Close() }()
	conn := &boundedConn{Conn: raw}
	conn.until.Store(time.Now().Add(-time.Second).UnixNano())
	conn.SetWriteDeadline(time.Now().Add(time.Minute))
	conn.SetReadDeadline(time.Time{})
	if _, err := conn.Write([]byte("x")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write past the limit: %v, want a deadline error", err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read past the limit: %v, want a deadline error", err)
	}
}

func TestSyncACLStopsAtTheContextDeadline(t *testing.T) {
	c, afterLogin := fakeIMAP(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	err := c.SyncACL(ctx, "team@example.test", nil, func() error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SyncACL against a silent server returned %v after %s, want the context deadline", err, time.Since(start))
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("SyncACL took %s to observe a 1 s context deadline", took)
	}
	<-afterLogin
}
