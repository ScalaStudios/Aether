package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
)

var testRP = RelyingParty{ID: "mail.example.com", Origin: "https://mail.example.com"}

type softKey struct {
	id     []byte
	signer func([]byte) []byte
	spki   []byte
	count  uint32
}

func newECKey(t *testing.T) *softKey {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	spki, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	return &softKey{id: []byte("credential-1"), spki: spki, signer: func(data []byte) []byte {
		digest := sha256.Sum256(data)
		sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
		return sig
	}}
}

func clientData(kind, origin string, challenge []byte) []byte {
	raw, _ := json.Marshal(map[string]any{"type": kind, "challenge": base64.RawURLEncoding.EncodeToString(challenge), "origin": origin, "crossOrigin": false})
	return raw
}

func authData(rpID string, flags byte, count uint32, attested []byte) []byte {
	hash := sha256.Sum256([]byte(rpID))
	out := append(hash[:], flags)
	out = binary.BigEndian.AppendUint32(out, count)
	return append(out, attested...)
}

func (k *softKey) attested() []byte {
	out := make([]byte, 16)
	out = binary.BigEndian.AppendUint16(out, uint16(len(k.id)))
	return append(out, k.id...)
}

func (k *softKey) assert(rpID, origin string, challenge []byte) (client, data, sig []byte) {
	k.count++
	client = clientData("webauthn.get", origin, challenge)
	data = authData(rpID, flagUserPresent, k.count, nil)
	hash := sha256.Sum256(client)
	return client, data, k.signer(append(append([]byte{}, data...), hash[:]...))
}

func TestSecurityKeyRegistration(t *testing.T) {
	key := newECKey(t)
	challenge := []byte("registration-challenge")
	data := authData(testRP.ID, flagUserPresent|flagAttestedData, 0, key.attested())
	if _, err := testRP.VerifyRegistration(clientData("webauthn.create", testRP.Origin, challenge), data, key.spki, key.id, challenge); err != nil {
		t.Fatalf("valid registration rejected: %v", err)
	}
	cases := map[string]func() error{
		"wrong origin": func() error {
			_, err := testRP.VerifyRegistration(clientData("webauthn.create", "https://evil.example", challenge), data, key.spki, key.id, challenge)
			return err
		},
		"wrong challenge": func() error {
			_, err := testRP.VerifyRegistration(clientData("webauthn.create", testRP.Origin, []byte("other")), data, key.spki, key.id, challenge)
			return err
		},
		"assertion instead of registration": func() error {
			_, err := testRP.VerifyRegistration(clientData("webauthn.get", testRP.Origin, challenge), data, key.spki, key.id, challenge)
			return err
		},
		"other relying party": func() error {
			_, err := testRP.VerifyRegistration(clientData("webauthn.create", testRP.Origin, challenge), authData("evil.example", flagUserPresent|flagAttestedData, 0, key.attested()), key.spki, key.id, challenge)
			return err
		},
		"no user presence": func() error {
			_, err := testRP.VerifyRegistration(clientData("webauthn.create", testRP.Origin, challenge), authData(testRP.ID, flagAttestedData, 0, key.attested()), key.spki, key.id, challenge)
			return err
		},
		"credential mismatch": func() error {
			_, err := testRP.VerifyRegistration(clientData("webauthn.create", testRP.Origin, challenge), data, key.spki, []byte("credential-2"), challenge)
			return err
		},
		"unusable key": func() error {
			_, err := testRP.VerifyRegistration(clientData("webauthn.create", testRP.Origin, challenge), data, []byte("not a key"), key.id, challenge)
			return err
		},
	}
	for name, run := range cases {
		if run() == nil {
			t.Errorf("%s: registration accepted", name)
		}
	}
}

func TestSecurityKeyAssertion(t *testing.T) {
	key := newECKey(t)
	challenge := []byte("login-challenge")
	client, data, sig := key.assert(testRP.ID, testRP.Origin, challenge)
	count, err := testRP.VerifyAssertion(client, data, sig, key.spki, challenge, 0)
	if err != nil || count != 1 {
		t.Fatalf("valid assertion rejected: count %d, %v", count, err)
	}
	if _, err := testRP.VerifyAssertion(client, data, sig, key.spki, challenge, count); err == nil {
		t.Error("replayed assertion with the same counter accepted")
	}
	if _, err := testRP.VerifyAssertion(client, data, sig, key.spki, []byte("other"), 0); err == nil {
		t.Error("assertion for another challenge accepted")
	}
	tampered := append([]byte{}, sig...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := testRP.VerifyAssertion(client, data, tampered, key.spki, challenge, 0); err == nil {
		t.Error("tampered signature accepted")
	}
	other := newECKey(t)
	if _, err := testRP.VerifyAssertion(client, data, sig, other.spki, challenge, 0); err == nil {
		t.Error("signature checked against another key accepted")
	}
	client, data, sig = key.assert("evil.example", testRP.Origin, challenge)
	if _, err := testRP.VerifyAssertion(client, data, sig, key.spki, challenge, 0); err == nil {
		t.Error("assertion for another relying party accepted")
	}
}

func TestSecurityKeyWithoutCounterAndEd25519(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	spki, _ := x509.MarshalPKIXPublicKey(pub)
	challenge := []byte("passkey-challenge")
	client := clientData("webauthn.get", testRP.Origin, challenge)
	data := authData(testRP.ID, flagUserPresent, 0, nil)
	hash := sha256.Sum256(client)
	sig := ed25519.Sign(priv, append(append([]byte{}, data...), hash[:]...))
	for range 2 {
		if _, err := testRP.VerifyAssertion(client, data, sig, spki, challenge, 0); err != nil {
			t.Fatalf("passkey without a signature counter rejected: %v", err)
		}
	}
}
