package mail

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

func TestWKDLocalUsesThePublishedExample(t *testing.T) {
	if got := WKDLocal("Joe.Doe"); got != "iy9q119eutrkn8s1mk4r39qejnbu3n5q" {
		t.Fatalf("WKDLocal = %s", got)
	}
}

func TestCertificateFingerprintAndEmails(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:   big.NewInt(1),
		NotBefore:      time.Now().Add(-time.Hour),
		NotAfter:       time.Now().Add(time.Hour),
		EmailAddresses: []string{"Ada@Example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	want := hex.EncodeToString(sum[:])
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	fp, emails, err := ParseCertificate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if fp != want || len(emails) != 1 || emails[0] != "ada@example.com" {
		t.Fatalf("fingerprint %s emails %v", fp, emails)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseCertificate(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})); err != ErrPrivateKey {
		t.Fatalf("private key error = %v", err)
	}
}

func newFormatPacket(tag byte, body []byte) []byte {
	return append([]byte{0xC0 | tag, byte(len(body))}, body...)
}

func TestOpenPGPPublicKeyRoundTrip(t *testing.T) {
	body := []byte{4, 0, 0, 0, 1, 22, 1, 2, 3, 4}
	want, err := publicKeyFingerprint(body)
	if err != nil {
		t.Fatal(err)
	}
	raw := append(newFormatPacket(6, body), newFormatPacket(13, []byte("Ada Lovelace <Ada@Example.com>"))...)
	fp, emails, err := ParseOpenPGPPublic(raw)
	if err != nil {
		t.Fatal(err)
	}
	if fp != want || len(emails) != 1 || emails[0] != "ada@example.com" {
		t.Fatalf("binary key %s %v", fp, emails)
	}
	again, emails, err := ParseOpenPGPPublic([]byte(ArmorOpenPGPPublic(raw)))
	if err != nil || again != fp || len(emails) != 1 {
		t.Fatalf("armor %s %v %v", again, emails, err)
	}
	if _, _, err := ParseOpenPGPPublic(append(raw, newFormatPacket(5, []byte{4, 0})...)); err != ErrPrivateKey {
		t.Fatalf("secret packet error = %v", err)
	}
}

func TestOpenPGPRejectsPrivateArmor(t *testing.T) {
	_, _, err := ParseOpenPGPPublic([]byte("-----BEGIN PGP PRIVATE KEY BLOCK-----\n\nAAAA\n-----END PGP PRIVATE KEY BLOCK-----\n"))
	if err != ErrPrivateKey && err != ErrNotPublicKey {
		t.Fatal(err)
	}
}

func TestWrappedPrivateKeyMagic(t *testing.T) {
	if ValidWrappedPrivateKey([]byte("-----BEGIN PRIVATE KEY-----")) || ValidWrappedPrivateKey(bytes.Repeat([]byte{0x30}, 40)) {
		t.Fatal("accepted an unencrypted key")
	}
	if !ValidWrappedPrivateKey(append([]byte{0xA1}, bytes.Repeat([]byte{7}, 40)...)) {
		t.Fatal("rejected a wrapped key")
	}
}

func TestClassifySealed(t *testing.T) {
	pgp := &Sealed{ContentType: `multipart/encrypted; protocol="application/pgp-encrypted"; boundary="b"`, Body: []byte("--b\r\n\r\n-----BEGIN PGP MESSAGE-----\r\n-----END PGP MESSAGE-----\r\n--b--")}
	got, err := ClassifySealed(pgp.ContentType, pgp.Body)
	if err != nil || got.Protocol != "openpgp" || !got.Encrypted {
		t.Fatalf("pgp = %+v %v", got, err)
	}
	if _, err := ClassifySealed("text/plain", []byte("hello")); err == nil {
		t.Fatal("accepted plaintext")
	}
}

func TestDescribeProtection(t *testing.T) {
	part := &imap.BodyStructureMultiPart{Subtype: "encrypted", Extended: &imap.BodyStructureMultiPartExt{Params: map[string]string{"protocol": "application/pgp-encrypted"}}}
	got := DescribeProtection(part)
	if got == nil || got.Protocol != "openpgp" || !got.Encrypted {
		t.Fatalf("%+v", got)
	}
}

func TestBuildSealedOmitsPlaintext(t *testing.T) {
	o := Outgoing{
		From: Address{Name: "Ada", Address: "ada@example.test"}, To: []Address{{Address: "bea@example.test"}}, Subject: "secret",
		Text: "not this", HTML: "<p>nor this</p>",
		Sealed:    &Sealed{ContentType: `multipart/encrypted; protocol="application/pgp-encrypted"; boundary="bound"`, Body: []byte("--bound\r\nContent-Type: application/pgp-encrypted\r\n\r\nVersion: 1\r\n--bound\r\nContent-Type: application/octet-stream\r\n\r\n-----BEGIN PGP MESSAGE-----\r\nY2lwaGVydGV4dA==\r\n-----END PGP MESSAGE-----\r\n--bound--\r\n"), Protocol: "openpgp", Encrypted: true},
		Autocrypt: "addr=ada@example.test; keydata=QQ==",
	}
	raw, _, err := o.Build("mail.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("not this")) || bytes.Contains(raw, []byte("nor this")) {
		t.Fatalf("plaintext leaked:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("BEGIN PGP MESSAGE")) || !bytes.Contains(raw, []byte("Autocrypt:")) || !bytes.Contains(raw, []byte("multipart/encrypted")) {
		t.Fatalf("sealed message missing:\n%s", raw)
	}
	protocol, encrypted := ClassifyRaw(raw)
	if protocol != "openpgp" || !encrypted {
		t.Fatalf("classified %s %v", protocol, encrypted)
	}
}

func TestNormalizeAutocryptRejectsAKeyForSomeoneElse(t *testing.T) {
	if _, err := NormalizeAutocrypt("addr=ada@example.test; keydata=%%%", "ada@example.test"); err == nil {
		t.Fatal("accepted a broken header")
	}
	if strings.Contains("addr=ada@example.test\nkeydata=QQ==", "\n") {
		if _, err := NormalizeAutocrypt("addr=ada@example.test\nkeydata=QQ==", "ada@example.test"); err == nil {
			t.Fatal("accepted a folded header")
		}
	}
}
