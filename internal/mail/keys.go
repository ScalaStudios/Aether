package mail

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"regexp"
	"strings"
)

var (
	ErrPrivateKey   = errors.New("private key material")
	ErrNotPublicKey = errors.New("not a public key")
)

var emailInUserID = regexp.MustCompile(`<([^<>@\s]+@[^<>@\s]+)>`)

func ParseCertificate(data []byte) (fingerprint string, emails []string, err error) {
	if bytes.Contains(bytes.ToUpper(data), []byte("PRIVATE")) {
		return "", nil, ErrPrivateKey
	}
	var first *x509.Certificate
	rest := data
	sawPEM := false
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		sawPEM = true
		if strings.Contains(block.Type, "PRIVATE") {
			return "", nil, ErrPrivateKey
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", nil, ErrNotPublicKey
		}
		if first == nil {
			first = cert
		}
	}
	if first == nil && !sawPEM {
		cert, err := x509.ParseCertificate(bytes.TrimSpace(data))
		if err != nil {
			return "", nil, ErrNotPublicKey
		}
		first = cert
	}
	if first == nil {
		return "", nil, ErrNotPublicKey
	}
	sum := sha256.Sum256(first.Raw)
	return hex.EncodeToString(sum[:]), certificateEmails(first), nil
}

func certificateEmails(cert *x509.Certificate) []string {
	seen := map[string]bool{}
	var out []string
	for _, email := range cert.EmailAddresses {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || seen[email] || !strings.Contains(email, "@") {
			continue
		}
		seen[email] = true
		out = append(out, email)
	}
	return out
}

func ParseOpenPGPPublic(data []byte) (fingerprint string, emails []string, err error) {
	if bytes.Contains(bytes.ToUpper(data), []byte("PRIVATE KEY")) {
		return "", nil, ErrPrivateKey
	}
	binaryKey, err := decodeOpenPGP(data)
	if err != nil {
		return "", nil, err
	}
	packets, err := readPackets(binaryKey)
	if err != nil {
		return "", nil, err
	}
	var fp string
	seen := map[string]bool{}
	for _, p := range packets {
		switch p.tag {
		case 5, 7:
			return "", nil, ErrPrivateKey
		case 6:
			if fp != "" {
				continue
			}
			fp, err = publicKeyFingerprint(p.body)
			if err != nil {
				return "", nil, err
			}
		case 13:
			for _, email := range emailsFromUserID(string(p.body)) {
				if !seen[email] {
					seen[email] = true
					emails = append(emails, email)
				}
			}
		}
	}
	if fp == "" {
		return "", nil, ErrNotPublicKey
	}
	return fp, emails, nil
}

func emailsFromUserID(uid string) []string {
	uid = strings.TrimSpace(uid)
	if m := emailInUserID.FindStringSubmatch(uid); m != nil {
		return []string{strings.ToLower(m[1])}
	}
	if strings.Contains(uid, "@") && !strings.ContainsAny(uid, " \t<>") {
		return []string{strings.ToLower(uid)}
	}
	return nil
}

func decodeOpenPGP(data []byte) ([]byte, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	const begin = "-----BEGIN PGP PUBLIC KEY BLOCK-----"
	const end = "-----END PGP PUBLIC KEY BLOCK-----"
	start := strings.Index(text, begin)
	stop := strings.Index(text, end)
	if start < 0 || stop < start {
		if strings.Contains(text, "BEGIN PGP") {
			return nil, ErrNotPublicKey
		}
		return data, nil
	}
	body := text[start+len(begin) : stop]
	if i := strings.Index(body, "\n\n"); i >= 0 {
		body = body[i+2:]
	}
	var b64 strings.Builder
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "=") {
			continue
		}
		b64.WriteString(line)
	}
	decoded, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(b64.String())
	}
	if err != nil {
		return nil, ErrNotPublicKey
	}
	return decoded, nil
}

type pgpPacket struct {
	tag  int
	body []byte
}

func readPackets(data []byte) ([]pgpPacket, error) {
	var out []pgpPacket
	for len(data) > 0 {
		if data[0]&0x80 == 0 {
			return nil, ErrNotPublicKey
		}
		var tag, n int
		var body []byte
		var err error
		if data[0]&0x40 != 0 {
			tag = int(data[0] & 0x3f)
			body, n, err = newFormatBody(data[1:])
			n++
		} else {
			tag = int((data[0] >> 2) & 0x0f)
			body, n, err = oldFormatBody(data)
		}
		if err != nil {
			return nil, err
		}
		if n <= 0 || n > len(data) {
			return nil, ErrNotPublicKey
		}
		out = append(out, pgpPacket{tag: tag, body: body})
		data = data[n:]
	}
	return out, nil
}

func newFormatBody(data []byte) ([]byte, int, error) {
	if len(data) == 0 {
		return nil, 0, ErrNotPublicKey
	}
	first := data[0]
	switch {
	case first < 192:
		if len(data) < 1+int(first) {
			return nil, 0, ErrNotPublicKey
		}
		return data[1 : 1+first], 1 + int(first), nil
	case first < 224:
		if len(data) < 2 {
			return nil, 0, ErrNotPublicKey
		}
		n := (int(first)-192)<<8 + int(data[1]) + 192
		if len(data) < 2+n {
			return nil, 0, ErrNotPublicKey
		}
		return data[2 : 2+n], 2 + n, nil
	case first == 255:
		if len(data) < 5 {
			return nil, 0, ErrNotPublicKey
		}
		n := int(binary.BigEndian.Uint32(data[1:5]))
		if n < 0 || len(data) < 5+n {
			return nil, 0, ErrNotPublicKey
		}
		return data[5 : 5+n], 5 + n, nil
	default:
		return nil, 0, ErrNotPublicKey
	}
}

func oldFormatBody(data []byte) ([]byte, int, error) {
	if len(data) == 0 {
		return nil, 0, ErrNotPublicKey
	}
	switch data[0] & 0x03 {
	case 0:
		if len(data) < 2 {
			return nil, 0, ErrNotPublicKey
		}
		n := int(data[1])
		if len(data) < 2+n {
			return nil, 0, ErrNotPublicKey
		}
		return data[2 : 2+n], 2 + n, nil
	case 1:
		if len(data) < 3 {
			return nil, 0, ErrNotPublicKey
		}
		n := int(binary.BigEndian.Uint16(data[1:3]))
		if len(data) < 3+n {
			return nil, 0, ErrNotPublicKey
		}
		return data[3 : 3+n], 3 + n, nil
	case 2:
		if len(data) < 5 {
			return nil, 0, ErrNotPublicKey
		}
		n := int(binary.BigEndian.Uint32(data[1:5]))
		if n < 0 || len(data) < 5+n {
			return nil, 0, ErrNotPublicKey
		}
		return data[5 : 5+n], 5 + n, nil
	default:
		return data[1:], len(data), nil
	}
}

func publicKeyFingerprint(body []byte) (string, error) {
	if len(body) < 6 {
		return "", ErrNotPublicKey
	}
	switch body[0] {
	case 4:
		h := sha1.New()
		var n [2]byte
		binary.BigEndian.PutUint16(n[:], uint16(len(body)))
		h.Write([]byte{0x99})
		h.Write(n[:])
		h.Write(body)
		return hex.EncodeToString(h.Sum(nil)), nil
	case 6:
		h := sha256.New()
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(body)))
		h.Write([]byte{0x9b})
		h.Write(n[:])
		h.Write(body)
		return hex.EncodeToString(h.Sum(nil)), nil
	default:
		return "", ErrNotPublicKey
	}
}

func ArmorOpenPGPPublic(data []byte) string {
	if bytes.Contains(data, []byte("BEGIN PGP PUBLIC KEY BLOCK")) {
		return string(data)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	var body strings.Builder
	for len(encoded) > 64 {
		body.WriteString(encoded[:64])
		body.WriteByte('\n')
		encoded = encoded[64:]
	}
	body.WriteString(encoded)
	sum := crc24(data)
	checksum := base64.StdEncoding.EncodeToString([]byte{byte(sum >> 16), byte(sum >> 8), byte(sum)})
	return "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n" + body.String() + "\n=" + checksum + "\n-----END PGP PUBLIC KEY BLOCK-----\n"
}

func crc24(data []byte) uint32 {
	crc := uint32(0xB704CE)
	for _, b := range data {
		crc ^= uint32(b) << 16
		for range 8 {
			crc <<= 1
			if crc&0x1000000 != 0 {
				crc ^= 0x1864CFB
			}
		}
	}
	return crc & 0xFFFFFF
}

func ContainsAddress(emails []string, address string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	for _, email := range emails {
		if email == address {
			return true
		}
	}
	return false
}
