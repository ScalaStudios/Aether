package mail

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message"
)

var ErrMalformedProtection = errors.New("malformed protected message")

const zbase32Alphabet = "ybndrfg8ejkmcpqxot1uwisza345h769"

func WKDLocal(local string) string {
	sum := sha1.Sum([]byte(strings.ToLower(local)))
	return zbase32(sum[:])
}

func zbase32(src []byte) string {
	dst := make([]byte, 0, (len(src)*8+4)/5)
	var acc uint
	var nbits uint
	for _, b := range src {
		acc = (acc << 8) | uint(b)
		nbits += 8
		for nbits >= 5 {
			nbits -= 5
			dst = append(dst, zbase32Alphabet[(acc>>nbits)&31])
		}
	}
	if nbits > 0 {
		dst = append(dst, zbase32Alphabet[(acc<<(5-nbits))&31])
	}
	return string(dst)
}

type MessageProtection struct {
	Protocol  string `json:"protocol,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`
	Signed    bool   `json:"signed,omitempty"`
	Autocrypt string `json:"autocrypt,omitempty"`
}

func DescribeProtection(bs imap.BodyStructure) *MessageProtection {
	if bs == nil {
		return nil
	}
	mt := strings.ToLower(bs.MediaType())
	params := structureParams(bs)
	protocol := strings.ToLower(params["protocol"])
	smimeType := strings.ToLower(params["smime-type"])
	switch {
	case mt == "multipart/encrypted" && strings.Contains(protocol, "pgp-encrypted"):
		return &MessageProtection{Protocol: "openpgp", Encrypted: true}
	case mt == "multipart/signed" && strings.Contains(protocol, "pgp-signature"):
		return &MessageProtection{Protocol: "openpgp", Signed: true}
	case mt == "multipart/signed" && strings.Contains(protocol, "pkcs7-signature"):
		return &MessageProtection{Protocol: "smime", Signed: true}
	case mt == "application/pkcs7-mime" || mt == "application/x-pkcs7-mime":
		if strings.Contains(smimeType, "signed") && !strings.Contains(smimeType, "enveloped") {
			return &MessageProtection{Protocol: "smime", Signed: true}
		}
		return &MessageProtection{Protocol: "smime", Encrypted: true}
	default:
		return nil
	}
}

func structureParams(bs imap.BodyStructure) map[string]string {
	switch p := bs.(type) {
	case *imap.BodyStructureMultiPart:
		if p.Extended != nil && p.Extended.Params != nil {
			return p.Extended.Params
		}
	case *imap.BodyStructureSinglePart:
		if p.Params != nil {
			return p.Params
		}
	}
	return map[string]string{}
}

func WithAutocrypt(p *MessageProtection, value string) *MessageProtection {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 16<<10 || strings.ContainsAny(value, "\r\n") {
		return p
	}
	if p == nil {
		p = &MessageProtection{}
	}
	p.Autocrypt = value
	return p
}

type Sealed struct {
	ContentType string
	Body        []byte
	Protocol    string
	Encrypted   bool
	Signed      bool
}

func ClassifySealed(contentType string, body []byte) (*Sealed, error) {
	if len(body) == 0 || len(body) > 20<<20 || len(contentType) > 500 {
		return nil, ErrMalformedProtection
	}
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, ErrMalformedProtection
	}
	mt = strings.ToLower(mt)
	protocol := strings.ToLower(params["protocol"])
	smimeType := strings.ToLower(params["smime-type"])
	sealed := &Sealed{ContentType: mime.FormatMediaType(mt, params), Body: body}
	switch {
	case mt == "application/pkcs7-mime" || mt == "application/x-pkcs7-mime":
		if body[0] != 0x30 {
			return nil, ErrMalformedProtection
		}
		sealed.Protocol = "smime"
		if smimeType == "signed-data" {
			sealed.Signed = true
		} else {
			sealed.Encrypted = true
		}
	case mt == "multipart/encrypted" && protocol == "application/pgp-encrypted":
		if !bytes.Contains(body, []byte("BEGIN PGP MESSAGE")) {
			return nil, ErrMalformedProtection
		}
		sealed.Protocol = "openpgp"
		sealed.Encrypted = true
	case mt == "multipart/signed" && protocol == "application/pgp-signature":
		if !bytes.Contains(body, []byte("BEGIN PGP SIGNATURE")) {
			return nil, ErrMalformedProtection
		}
		sealed.Protocol = "openpgp"
		sealed.Signed = true
	case mt == "multipart/signed" && (protocol == "application/pkcs7-signature" || protocol == "application/x-pkcs7-signature"):
		lower := bytes.ToLower(body)
		if !bytes.Contains(lower, []byte("pkcs7-signature")) {
			return nil, ErrMalformedProtection
		}
		sealed.Protocol = "smime"
		sealed.Signed = true
	default:
		return nil, ErrMalformedProtection
	}
	return sealed, nil
}

func ClassifyRaw(raw []byte) (protocol string, encrypted bool) {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		return "", false
	}
	mt, params, err := entity.Header.ContentType()
	if err != nil {
		return "", false
	}
	body, _ := io.ReadAll(io.LimitReader(entity.Body, 64<<10))
	sealed, err := ClassifySealed(mime.FormatMediaType(mt, params), body)
	if err != nil {
		return "", false
	}
	return sealed.Protocol, sealed.Encrypted
}

func NormalizeAutocrypt(header, from string) (string, error) {
	header = strings.TrimSpace(header)
	if header == "" || len(header) > 16<<10 || strings.ContainsAny(header, "\r\n") {
		return "", ErrMalformedProtection
	}
	parts := strings.Split(header, ";")
	var addr, prefer, keydata string
	for _, part := range parts {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return "", ErrMalformedProtection
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "addr":
			addr = value
		case "prefer-encrypt":
			prefer = strings.ToLower(value)
		case "keydata":
			keydata = value
		default:
			return "", ErrMalformedProtection
		}
	}
	if !strings.EqualFold(addr, from) || (prefer != "" && prefer != "mutual" && prefer != "nopreference") || keydata == "" {
		return "", ErrMalformedProtection
	}
	raw, err := base64.StdEncoding.DecodeString(keydata)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(keydata)
	}
	if err != nil {
		return "", ErrMalformedProtection
	}
	_, emails, err := ParseOpenPGPPublic(raw)
	if err != nil || !ContainsAddress(emails, from) {
		return "", ErrMalformedProtection
	}
	out := "addr=" + strings.ToLower(from) + ";"
	if prefer != "" {
		out += " prefer-encrypt=" + prefer + ";"
	}
	out += " keydata=" + keydata
	return out, nil
}

func ValidWrappedPrivateKey(b []byte) bool {
	return len(b) >= 29 && len(b) <= 64<<10 && b[0] == 0xA1
}
