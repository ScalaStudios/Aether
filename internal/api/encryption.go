package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/net/idna"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

type encryptionPolicy struct {
	SMIME    string   `json:"smime"`
	OpenPGP  string   `json:"openpgp"`
	Password string   `json:"password"`
	Domains  []string `json:"domains,omitempty"`
}

func validPolicy(v string) bool {
	return v == "off" || v == "allow" || v == "require"
}

func policyLevel(p encryptionPolicy, method string) string {
	switch method {
	case "smime":
		return p.SMIME
	case "openpgp":
		return p.OpenPGP
	case "password":
		return p.Password
	default:
		return ""
	}
}

func satisfiesKeyPolicy(p encryptionPolicy, method string, encrypted bool) bool {
	needSMIME := p.SMIME == "require"
	needPGP := p.OpenPGP == "require"
	if !needSMIME && !needPGP {
		return true
	}
	if !encrypted {
		return false
	}
	if needSMIME && needPGP {
		return method == "smime" || method == "openpgp"
	}
	if needSMIME {
		return method == "smime"
	}
	return method == "openpgp"
}

func keyRequiredMessage(p encryptionPolicy) string {
	switch {
	case p.SMIME == "require" && p.OpenPGP == "require":
		return "Your organization requires outgoing mail to be encrypted with S/MIME or OpenPGP."
	case p.SMIME == "require":
		return "Your organization requires outgoing mail to be encrypted with S/MIME."
	default:
		return "Your organization requires outgoing mail to be encrypted with OpenPGP."
	}
}

func encryptionDenial(p encryptionPolicy, method string, encrypted, external bool) (code, message string) {
	if method != "" && policyLevel(p, method) == "off" {
		switch method {
		case "smime":
			return "encryption_disabled", "Your organization has turned off S/MIME."
		case "openpgp":
			return "encryption_disabled", "Your organization has turned off OpenPGP."
		default:
			return "protected_mail_disabled", "Your organization has turned off password-protected mail."
		}
	}
	if !satisfiesKeyPolicy(p, method, encrypted) {
		return "encryption_required", keyRequiredMessage(p)
	}
	if p.Password == "require" && external && method != "password" && !(encrypted && (method == "smime" || method == "openpgp")) {
		return "password_required", "Recipients outside your domains need a password-protected message, or mail encrypted to their key."
	}
	return "", ""
}

func (s *Server) loadEncryptionPolicy(ctx context.Context, orgID string) (encryptionPolicy, error) {
	var p encryptionPolicy
	err := s.db.QueryRow(ctx, `SELECT smime_policy, openpgp_policy, password_policy FROM organizations WHERE id = $1`, orgID).
		Scan(&p.SMIME, &p.OpenPGP, &p.Password)
	return p, err
}

func (s *Server) orgDomainNames(ctx context.Context, orgID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT name FROM domains WHERE org_id = $1 AND active AND verified_at IS NOT NULL ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if names == nil {
		names = []string{}
	}
	return names, nil
}

func (s *Server) recipientOutside(ctx context.Context, orgID string, recipients []string) (bool, error) {
	names, err := s.orgDomainNames(ctx, orgID)
	if err != nil {
		return false, err
	}
	own := map[string]bool{}
	for _, name := range names {
		own[strings.ToLower(name)] = true
	}
	for _, recipient := range recipients {
		_, domain, ok := strings.Cut(strings.ToLower(recipient), "@")
		if !ok || !own[domain] {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) allowEncryption(w http.ResponseWriter, r *http.Request, orgID, method string, encrypted, autocrypt bool, recipients []string) bool {
	policy, err := s.loadEncryptionPolicy(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	if autocrypt && policy.OpenPGP == "off" {
		writeError(w, http.StatusUnprocessableEntity, "encryption_disabled", "Your organization has turned off OpenPGP.")
		return false
	}
	external, err := s.recipientOutside(r.Context(), orgID, recipients)
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	if code, message := encryptionDenial(policy, method, encrypted, external); code != "" {
		writeError(w, http.StatusUnprocessableEntity, code, message)
		return false
	}
	return true
}

func (s *Server) encryptionBlock(ctx context.Context, orgID, method string, encrypted bool, recipients []string) (string, error) {
	policy, err := s.loadEncryptionPolicy(ctx, orgID)
	if err != nil {
		return "", err
	}
	external, err := s.recipientOutside(ctx, orgID, recipients)
	if err != nil {
		return "", err
	}
	_, message := encryptionDenial(policy, method, encrypted, external)
	return message, nil
}

func (s *Server) encryptionSettings(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, r.Method == http.MethodPut)
	if !ok {
		return
	}
	if r.Method == http.MethodPut {
		var in struct {
			SMIME    *string `json:"smime"`
			OpenPGP  *string `json:"openpgp"`
			Password *string `json:"password"`
		}
		if !decode(w, r, 1024, &in) || in.SMIME == nil || in.OpenPGP == nil || in.Password == nil ||
			!validPolicy(*in.SMIME) || !validPolicy(*in.OpenPGP) || !validPolicy(*in.Password) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_policy", "Choose off, allow, or require for S/MIME, OpenPGP, and password-protected mail.")
			return
		}
		_, err := s.db.Exec(r.Context(), `UPDATE organizations SET smime_policy = $2, openpgp_policy = $3, password_policy = $4 WHERE id = $1`,
			orgID, *in.SMIME, *in.OpenPGP, *in.Password)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		s.audit(r.Context(), r, p, &orgID, "organization.encryption", "organization", orgID, map[string]any{
			"smime": *in.SMIME, "openpgp": *in.OpenPGP, "password": *in.Password,
		})
	}
	policy, err := s.loadEncryptionPolicy(r.Context(), orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	policy.Domains, err = s.orgDomainNames(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

type storedKey struct {
	ID          string    `json:"id"`
	Address     string    `json:"address"`
	Protocol    string    `json:"protocol"`
	Fingerprint string    `json:"fingerprint"`
	PublicKey   string    `json:"publicKey"`
	PrivateKey  []byte    `json:"privateKey,omitempty"`
	Salt        []byte    `json:"salt,omitempty"`
	Iterations  int       `json:"iterations,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (s *Server) listEncryptionKeys(w http.ResponseWriter, r *http.Request, p *principal) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id, address, protocol, fingerprint, convert_from(public_key, 'UTF8'), private_key, salt, iterations, created_at
		FROM encryption_keys WHERE user_id = $1 ORDER BY address, protocol`, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (storedKey, error) {
		var item storedKey
		return item, row.Scan(&item.ID, &item.Address, &item.Protocol, &item.Fingerprint, &item.PublicKey, &item.PrivateKey, &item.Salt, &item.Iterations, &item.CreatedAt)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(items))
}

func parsePublicMaterial(protocol, data, address string) (fingerprint string, err error) {
	var emails []string
	switch protocol {
	case "smime":
		fingerprint, emails, err = mailpkg.ParseCertificate([]byte(data))
	case "openpgp":
		fingerprint, emails, err = mailpkg.ParseOpenPGPPublic([]byte(data))
	default:
		return "", mailpkg.ErrNotPublicKey
	}
	if err != nil {
		return "", err
	}
	if !mailpkg.ContainsAddress(emails, address) {
		return "", mailpkg.ErrNotPublicKey
	}
	return fingerprint, nil
}

func (s *Server) saveEncryptionKey(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Address    string `json:"address"`
		Protocol   string `json:"protocol"`
		PublicKey  string `json:"publicKey"`
		PrivateKey []byte `json:"privateKey"`
		Salt       []byte `json:"salt"`
		Iterations int    `json:"iterations"`
	}
	if !decode(w, r, 256<<10, &in) {
		return
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(in.Address))
	if err != nil || parsed.Name != "" || (in.Protocol != "smime" && in.Protocol != "openpgp") ||
		len(in.PublicKey) < 32 || len(in.PublicKey) > 64<<10 || !mailpkg.ValidWrappedPrivateKey(in.PrivateKey) ||
		len(in.Salt) != 16 || in.Iterations < 200000 || in.Iterations > 600000 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_key", "The certificate or key could not be stored. Check the address and upload the encrypted private key.")
		return
	}
	address := strings.ToLower(parsed.Address)
	fingerprint, err := parsePublicMaterial(in.Protocol, in.PublicKey, address)
	if err != nil {
		message := "The certificate or public key does not match this address."
		if errors.Is(err, mailpkg.ErrPrivateKey) {
			message = "Upload a certificate or public key. The private key has to be encrypted in the browser first."
		}
		writeError(w, http.StatusUnprocessableEntity, "invalid_key", message)
		return
	}
	var orgID string
	err = s.db.QueryRow(r.Context(), `
		SELECT m.org_id FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE lower(m.address) = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL AND `+mailboxSendAs("$2"),
		address, p.User.ID).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "cannot_send", "You can only add a key for an address you can send as.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	policy, err := s.loadEncryptionPolicy(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if policyLevel(policy, in.Protocol) == "off" {
		writeError(w, http.StatusUnprocessableEntity, "encryption_disabled", "Your organization has turned off this kind of encryption.")
		return
	}
	var item storedKey
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO encryption_keys (id, user_id, org_id, address, protocol, fingerprint, public_key, private_key, salt, iterations)
		VALUES ($1, $2, $3, $4, $5, $6, convert_to($7, 'UTF8'), $8, $9, $10)
		ON CONFLICT (user_id, address, protocol) DO UPDATE
		SET fingerprint = EXCLUDED.fingerprint, public_key = EXCLUDED.public_key, private_key = EXCLUDED.private_key,
		    salt = EXCLUDED.salt, iterations = EXCLUDED.iterations, created_at = now()
		RETURNING id, address, protocol, fingerprint, convert_from(public_key, 'UTF8'), private_key, salt, iterations, created_at`,
		uuid.NewString(), p.User.ID, orgID, address, in.Protocol, fingerprint, in.PublicKey, in.PrivateKey, in.Salt, in.Iterations).
		Scan(&item.ID, &item.Address, &item.Protocol, &item.Fingerprint, &item.PublicKey, &item.PrivateKey, &item.Salt, &item.Iterations, &item.CreatedAt)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "encryption_key.save", "encryption_key", item.ID, map[string]any{
		"address": address, "protocol": in.Protocol, "fingerprint": fingerprint,
	})
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteEncryptionKey(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "keyId")
	if !ok {
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM encryption_keys WHERE id = $1 AND user_id = $2`, id, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type recipientKey struct {
	Protocol    string `json:"protocol"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
	Source      string `json:"source"`
}

func cleanEmail(value string) (string, bool) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil || parsed.Name != "" || !strings.Contains(parsed.Address, "@") {
		return "", false
	}
	return strings.ToLower(parsed.Address), true
}

func (s *Server) recipientKeys(w http.ResponseWriter, r *http.Request, p *principal) {
	address, ok := cleanEmail(r.URL.Query().Get("address"))
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_recipient", "Enter an email address to look up.")
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT protocol, fingerprint, convert_from(public_key, 'UTF8'), 'directory' FROM encryption_keys
		WHERE address = $1 AND org_id IN (SELECT org_id FROM organization_members WHERE user_id = $2)
		UNION ALL
		SELECT protocol, fingerprint, convert_from(public_key, 'UTF8'), source FROM peer_keys
		WHERE user_id = $2 AND lower(address) = $1
		ORDER BY 1, 2`, address, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[recipientKey])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(items))
}

func (s *Server) savePeerKey(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.allow(r.Context(), "peer-key:"+p.User.ID, 60) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Wait a minute before saving another key.")
		return
	}
	var in struct {
		Address   string `json:"address"`
		Protocol  string `json:"protocol"`
		PublicKey string `json:"publicKey"`
		Source    string `json:"source"`
	}
	if !decode(w, r, 128<<10, &in) {
		return
	}
	address, ok := cleanEmail(in.Address)
	if !ok || (in.Protocol != "smime" && in.Protocol != "openpgp") || (in.Source != "message" && in.Source != "autocrypt" && in.Source != "wkd") {
		writeError(w, http.StatusUnprocessableEntity, "invalid_key", "This public key could not be saved.")
		return
	}
	fingerprint, err := parsePublicMaterial(in.Protocol, in.PublicKey, address)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_key", "This public key does not belong to that address.")
		return
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO peer_keys (id, user_id, address, protocol, fingerprint, public_key, source)
		VALUES ($1, $2, $3, $4, $5, convert_to($6, 'UTF8'), $7)
		ON CONFLICT (user_id, lower(address), protocol, fingerprint) DO UPDATE SET source = EXCLUDED.source`,
		uuid.NewString(), p.User.ID, address, in.Protocol, fingerprint, in.PublicKey, in.Source)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) webKeyDirectory(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.allow(r.Context(), "wkd:"+p.User.ID, 30) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Wait a minute before looking up another key.")
		return
	}
	address, ok := cleanEmail(r.URL.Query().Get("address"))
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_recipient", "Enter an email address to look up.")
		return
	}
	body, err := s.fetchWKD(r.Context(), address)
	if err != nil {
		writeError(w, http.StatusNotFound, "key_not_found", "No OpenPGP key was published for this address.")
		return
	}
	fingerprint, emails, err := mailpkg.ParseOpenPGPPublic(body)
	if err != nil || (len(emails) > 0 && !mailpkg.ContainsAddress(emails, address)) {
		writeError(w, http.StatusNotFound, "key_not_found", "No OpenPGP key was published for this address.")
		return
	}
	writeJSON(w, http.StatusOK, recipientKey{Protocol: "openpgp", Fingerprint: fingerprint, PublicKey: mailpkg.ArmorOpenPGPPublic(body), Source: "wkd"})
}

func (s *Server) fetchWKD(ctx context.Context, address string) ([]byte, error) {
	local, domain, _ := strings.Cut(address, "@")
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil || ascii == "" || strings.Contains(ascii, "/") || netHostBlocked(ascii) {
		return nil, mailpkg.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	hash := mailpkg.WKDLocal(local)
	query := url.Values{"l": {local}}.Encode()
	advancedHost := "openpgpkey." + ascii
	urls := []string{"https://" + advancedHost + "/.well-known/openpgpkey/" + ascii + "/hu/" + hash + "?" + query}
	if s.wkdHostMissing(ctx, advancedHost) {
		urls = append(urls, "https://"+ascii+"/.well-known/openpgpkey/hu/"+hash+"?"+query)
	}
	for _, rawURL := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/octet-stream")
		resp, err := s.webhookClient.Do(req)
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK || len(body) == 0 || len(body) > 64<<10 {
			continue
		}
		if _, _, err := mailpkg.ParseOpenPGPPublic(body); err != nil {
			continue
		}
		return body, nil
	}
	return nil, mailpkg.ErrNotFound
}

func (s *Server) wkdHostMissing(ctx context.Context, host string) bool {
	_, err := s.resolve.LookupHost(ctx, host)
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

func netHostBlocked(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil
}
