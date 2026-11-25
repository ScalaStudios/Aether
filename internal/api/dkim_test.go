package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

func TestDKIMKeyMaterialRoundTrips(t *testing.T) {
	for _, c := range []struct {
		algorithm string
		bits      int
		pubLen    int
	}{{"rsa", 2048, 392}, {"ed25519", 0, 44}} {
		m, err := generateDKIMKey(c.algorithm, c.bits)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parseDKIMKey(string(m.privPEM))
		if err != nil || parsed.algorithm != c.algorithm || parsed.pub != m.pub || len(m.pub) != c.pubLen {
			t.Fatalf("%s: parsed %+v, generated %+v, err %v", c.algorithm, parsed, m, err)
		}
	}
	key, _ := rsa.GenerateKey(rand.Reader, 1024)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if m, err := parseDKIMKey(string(pkcs1)); err != nil || m.algorithm != "rsa" || m.bits != 1024 {
		t.Fatalf("PKCS#1 import: %+v %v", m, err)
	}
	for _, bad := range []string{"", "not a key", "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----"} {
		if _, err := parseDKIMKey(bad); err == nil {
			t.Fatalf("parseDKIMKey(%q) accepted", bad)
		}
	}
	if r := dkimRecord("ed25519", "abc"); r != "v=DKIM1; k=ed25519; p=abc" {
		t.Fatal(r)
	}
}

func TestDKIMRecordMatches(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.StdEncoding.EncodeToString(spki)
	pkcs1 := base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PublicKey(&key.PublicKey))
	expected := dkimRecord("rsa", pub)
	wrapped := strings.Join(splitEvery(pkcs1, 70), " ")
	for _, found := range []string{
		expected,
		"v=DKIM1; k=rsa; h=sha256; s=email; p=" + pub,
		"k=rsa; p=" + strings.TrimRight(pub, "="),
		"v=DKIM1; k=rsa; p=" + wrapped,
		"v=DKIM1;p=" + pkcs1,
	} {
		if !dkimRecordMatches(expected, found) {
			t.Fatalf("expected match for %q", found)
		}
	}
	for _, found := range []string{
		"v=DKIM1; k=rsa; p=AAAA",
		"v=DKIM1; k=ed25519; p=" + pub,
		"v=SPF1; k=rsa; p=" + pub,
		"v=DKIM1; k=rsa; h=sha1; p=" + pub,
		"v=DKIM1; k=rsa; s=dns; p=" + pub,
		"k=rsa; p=" + pub + "; p=" + pub,
	} {
		if dkimRecordMatches(expected, found) {
			t.Fatalf("expected mismatch for %q", found)
		}
	}
	ed := dkimRecord("ed25519", "abc=")
	if !dkimRecordMatches(ed, "v=DKIM1; k=ed25519; p=abc") || dkimRecordMatches(ed, "v=DKIM1; k=ed25519; p=abd") {
		t.Fatal("ed25519 record match")
	}
}

func splitEvery(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}
