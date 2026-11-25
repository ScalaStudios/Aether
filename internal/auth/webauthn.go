package auth

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
)

const (
	flagUserPresent  = 0x01
	flagAttestedData = 0x40
)

var ErrSecurityKey = errors.New("the security key response does not verify")

type RelyingParty struct {
	ID     string
	Origin string
}

func (rp RelyingParty) checkClientData(raw []byte, kind string, challenge []byte) error {
	var c struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Type != kind || c.Origin != rp.Origin || c.CrossOrigin || len(challenge) == 0 ||
		subtle.ConstantTimeCompare([]byte(c.Challenge), []byte(base64.RawURLEncoding.EncodeToString(challenge))) != 1 {
		return ErrSecurityKey
	}
	return nil
}

func (rp RelyingParty) checkAuthenticatorData(raw []byte, flags byte) (uint32, error) {
	rpHash := sha256.Sum256([]byte(rp.ID))
	if len(raw) < 37 || !bytes.Equal(raw[:32], rpHash[:]) || raw[32]&flags != flags {
		return 0, ErrSecurityKey
	}
	return binary.BigEndian.Uint32(raw[33:37]), nil
}

func parseSecurityKey(spki []byte) (crypto.PublicKey, error) {
	key, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, ErrSecurityKey
	}
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		if k.Curve == elliptic.P256() {
			return k, nil
		}
	case ed25519.PublicKey:
		return k, nil
	case *rsa.PublicKey:
		if k.N.BitLen() >= 2048 {
			return k, nil
		}
	}
	return nil, ErrSecurityKey
}

func (rp RelyingParty) VerifyRegistration(clientDataJSON, authenticatorData, publicKey, credentialID, challenge []byte) (uint32, error) {
	if err := rp.checkClientData(clientDataJSON, "webauthn.create", challenge); err != nil {
		return 0, err
	}
	count, err := rp.checkAuthenticatorData(authenticatorData, flagUserPresent|flagAttestedData)
	if err != nil {
		return 0, err
	}
	attested := authenticatorData[37:]
	if len(attested) < 18 {
		return 0, ErrSecurityKey
	}
	n := int(binary.BigEndian.Uint16(attested[16:18]))
	if n == 0 || len(attested) < 18+n || !bytes.Equal(attested[18:18+n], credentialID) {
		return 0, ErrSecurityKey
	}
	if _, err := parseSecurityKey(publicKey); err != nil {
		return 0, err
	}
	return count, nil
}

func (rp RelyingParty) VerifyAssertion(clientDataJSON, authenticatorData, signature, publicKey, challenge []byte, storedCount uint32) (uint32, error) {
	if err := rp.checkClientData(clientDataJSON, "webauthn.get", challenge); err != nil {
		return 0, err
	}
	count, err := rp.checkAuthenticatorData(authenticatorData, flagUserPresent)
	if err != nil {
		return 0, err
	}
	key, err := parseSecurityKey(publicKey)
	if err != nil {
		return 0, err
	}
	clientHash := sha256.Sum256(clientDataJSON)
	signed := append(bytes.Clone(authenticatorData), clientHash[:]...)
	digest := sha256.Sum256(signed)
	var valid bool
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		valid = ecdsa.VerifyASN1(k, digest[:], signature)
	case ed25519.PublicKey:
		valid = ed25519.Verify(k, signed, signature)
	case *rsa.PublicKey:
		valid = rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], signature) == nil
	}
	if !valid || ((count != 0 || storedCount != 0) && count <= storedCount) {
		return 0, ErrSecurityKey
	}
	return count, nil
}
