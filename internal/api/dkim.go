package api

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"

	"github.com/scalastudios/aether/internal/secrets"
)

const (
	dkimKeysHash             = "DKIM_KEYS"
	dkimSelectorsHash        = "DKIM_SELECTORS"
	dkimEd25519SelectorsHash = "DKIM_SELECTORS_ED25519"
	dkimRetireGrace          = 7 * 24 * time.Hour
	dkimRotateAfter          = 365 * 24 * time.Hour
)

var errDKIMPublish = errors.New("DKIM signer unavailable")

var selectorPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type dkimMaterial struct {
	algorithm string
	bits      int
	privPEM   []byte
	pub       string
}

type dkimKey struct {
	ID          string     `json:"id"`
	Selector    string     `json:"selector"`
	Algorithm   string     `json:"algorithm"`
	Bits        int        `json:"bits"`
	State       string     `json:"state"`
	DNSName     string     `json:"dnsName"`
	Record      string     `json:"record"`
	CreatedAt   time.Time  `json:"createdAt"`
	ActivatedAt *time.Time `json:"activatedAt"`
	RetiredAt   *time.Time `json:"retiredAt"`
	RemoveAfter *time.Time `json:"removeAfter"`
	RotationDue bool       `json:"rotationDue"`
}

func dkimRecord(algorithm, pub string) string {
	return "v=DKIM1; k=" + algorithm + "; p=" + pub
}

func newDKIMSelector(algorithm string) string {
	prefix := "aether"
	if algorithm == "ed25519" {
		prefix = "aethered"
	}
	return prefix + time.Now().UTC().Format("200601") + secrets.Token(6)
}

func dkimKeyMaterial(key crypto.Signer) (*dkimMaterial, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	m := &dkimMaterial{privPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})}
	switch k := key.(type) {
	case *rsa.PrivateKey:
		pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
		if err != nil {
			return nil, err
		}
		m.algorithm, m.bits, m.pub = "rsa", k.N.BitLen(), base64.StdEncoding.EncodeToString(pub)
	case ed25519.PrivateKey:
		m.algorithm, m.bits, m.pub = "ed25519", 256, base64.StdEncoding.EncodeToString(k.Public().(ed25519.PublicKey))
	default:
		return nil, errors.New("unsupported key type")
	}
	return m, nil
}

func generateDKIMKey(algorithm string, bits int) (*dkimMaterial, error) {
	if algorithm == "ed25519" {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		return dkimKeyMaterial(key)
	}
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, err
	}
	return dkimKeyMaterial(key)
}

func parseDKIMKey(text string) (*dkimMaterial, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(text)))
	if block == nil {
		return nil, errors.New("not a PEM private key")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, errors.New("unsupported PEM block " + block.Type)
	}
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("unsupported key type")
	}
	m, err := dkimKeyMaterial(signer)
	if err != nil {
		return nil, err
	}
	if m.algorithm == "rsa" && (m.bits < 1024 || m.bits > 4096) {
		return nil, errors.New("RSA keys must be between 1024 and 4096 bits")
	}
	return m, nil
}

func (s *Server) insertDKIMKey(ctx context.Context, tx pgx.Tx, domainID, selector, state string, m *dkimMaterial) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO dkim_keys (domain_id, selector, algorithm, bits, private_key, public_key, state, activated_at)
		VALUES ($1, $2, $3, $4, '', $5, $6::text, CASE WHEN $6::text = 'active' THEN now() END) RETURNING id`,
		domainID, selector, m.algorithm, m.bits, m.pub, state).Scan(&id); err != nil {
		return "", err
	}
	_, err := tx.Exec(ctx, `UPDATE dkim_keys SET private_key = $2 WHERE id = $1`, id, s.box.Seal(m.privPEM, "dkim:"+id))
	return id, err
}

func (s *Server) dkimKeys(ctx context.Context, d *domain) ([]dkimKey, error) {
	rows, err := s.db.Query(ctx, `SELECT id, selector, algorithm, bits, state, public_key, created_at, activated_at, retired_at
		FROM dkim_keys WHERE domain_id = $1 ORDER BY algorithm DESC, created_at`, d.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dkimKey{}
	for rows.Next() {
		var k dkimKey
		var pub string
		if err := rows.Scan(&k.ID, &k.Selector, &k.Algorithm, &k.Bits, &k.State, &pub, &k.CreatedAt, &k.ActivatedAt, &k.RetiredAt); err != nil {
			return nil, err
		}
		k.DNSName = k.Selector + "._domainkey." + d.Name
		k.Record = dkimRecord(k.Algorithm, pub)
		if k.RetiredAt != nil {
			after := k.RetiredAt.Add(dkimRetireGrace)
			k.RemoveAfter = &after
		}
		k.RotationDue = k.State == "active" && k.ActivatedAt != nil && time.Since(*k.ActivatedAt) > dkimRotateAfter
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Server) listDKIMKeys(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p)
	if !ok {
		return
	}
	keys, err := s.dkimKeys(r.Context(), d)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) createDKIMKey(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Algorithm  string `json:"algorithm"`
		Bits       int    `json:"bits"`
		Selector   string `json:"selector"`
		PrivateKey string `json:"privateKey"`
	}
	if !decode(w, r, 64<<10, &in) {
		return
	}
	var m *dkimMaterial
	var err error
	selector := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.Selector)), ".")
	if in.PrivateKey != "" {
		if m, err = parseDKIMKey(in.PrivateKey); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_key", "The private key could not be read: "+err.Error()+". Paste an unencrypted PEM RSA or Ed25519 private key.")
			return
		}
		if !selectorPattern.MatchString(selector) || len(selector) > 63 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_selector", "Enter a single DNS label for the selector, such as default or s1.")
			return
		}
	} else {
		if in.Selector != "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_selector", "A selector can only be chosen when importing a key.")
			return
		}
		switch {
		case in.Algorithm == "rsa" && in.Bits == 0:
			in.Bits = 2048
		case in.Algorithm == "rsa" && (in.Bits == 2048 || in.Bits == 4096), in.Algorithm == "ed25519" && in.Bits == 0:
		default:
			writeError(w, http.StatusUnprocessableEntity, "invalid_algorithm", "Choose an RSA key of 2048 or 4096 bits, or an Ed25519 key.")
			return
		}
		if m, err = generateDKIMKey(in.Algorithm, in.Bits); err != nil {
			s.internalError(w, r, err)
			return
		}
		selector = newDKIMSelector(in.Algorithm)
	}
	var id string
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		id, err = s.insertDKIMKey(r.Context(), tx, d.ID, selector, "pending", m)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if pgErr.ConstraintName == "dkim_keys_pending_idx" {
			writeError(w, http.StatusConflict, "dkim_key_pending", "This domain already has a new "+m.algorithm+" key waiting for its DNS record. Activate or cancel that key first.")
		} else {
			writeError(w, http.StatusConflict, "selector_taken", "This domain already has a key with the selector "+selector+".")
		}
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &d.OrgID, "dkim_key.create", "dkim_key", id, map[string]any{"domain": d.Name, "selector": selector, "algorithm": m.algorithm, "bits": m.bits, "imported": in.PrivateKey != ""})
	s.writeDKIMKey(w, r, d, id, http.StatusCreated)
}

func (s *Server) writeDKIMKey(w http.ResponseWriter, r *http.Request, d *domain, id string, status int) {
	keys, err := s.dkimKeys(r.Context(), d)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, k := range keys {
		if k.ID == id {
			writeJSON(w, status, k)
			return
		}
	}
	notFound(w)
}

func (s *Server) dkimKeyFromPath(w http.ResponseWriter, r *http.Request, p *principal) (*domain, *dkimKey, bool) {
	d, ok := s.domainFromPath(w, r, p)
	if !ok {
		return nil, nil, false
	}
	id, ok := pathID(w, r, "keyId")
	if !ok {
		return nil, nil, false
	}
	keys, err := s.dkimKeys(r.Context(), d)
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	for _, k := range keys {
		if k.ID == id {
			return d, &k, true
		}
	}
	notFound(w)
	return nil, nil, false
}

func (s *Server) activateDKIMKey(w http.ResponseWriter, r *http.Request, p *principal) {
	d, k, ok := s.dkimKeyFromPath(w, r, p)
	if !ok {
		return
	}
	if k.State == "active" {
		writeJSON(w, http.StatusOK, k)
		return
	}
	if k.State != "pending" {
		writeError(w, http.StatusConflict, "dkim_key_not_pending", "Only a new key that is not signing yet can be activated.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	rec := dnsRecord{Type: "TXT", Name: k.DNSName, Value: k.Record, Purpose: "dkim"}
	s.checkRecord(ctx, &rec)
	cancel()
	if rec.Status == "error" {
		writeError(w, http.StatusUnprocessableEntity, "dkim_lookup_failed", "The DNS lookup for "+rec.Name+" failed. Try again when DNS is reachable; mail stays signed with the current key until then.")
		return
	}
	if rec.Status != "ok" {
		writeError(w, http.StatusUnprocessableEntity, "dkim_record_missing", "The TXT record "+rec.Name+" does not contain this key yet. Publish it, wait for DNS to update, then try again; mail stays signed with the current key until then.")
		return
	}
	var old string
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if err := lockDKIMDomain(r.Context(), tx, d.ID); err != nil {
			return err
		}
		var sealed []byte
		var algorithm, pub string
		if err := tx.QueryRow(r.Context(), `SELECT private_key, algorithm, public_key FROM dkim_keys WHERE id = $1 AND state = 'pending' FOR UPDATE`, k.ID).Scan(&sealed, &algorithm, &pub); err != nil {
			return err
		}
		privPEM, err := s.box.Open(sealed, "dkim:"+k.ID)
		if err != nil {
			return err
		}
		parsed, err := parseDKIMKey(string(privPEM))
		if err != nil || parsed.algorithm != algorithm || parsed.pub != pub {
			return errors.New("stored DKIM key does not match its public key")
		}
		if err := tx.QueryRow(r.Context(), `UPDATE dkim_keys SET state = 'retired', retired_at = now(), private_key = '' WHERE domain_id = $1 AND algorithm = $2 AND state = 'active' RETURNING selector`, d.ID, algorithm).Scan(&old); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE dkim_keys SET state = 'active', activated_at = now() WHERE id = $1`, k.ID); err != nil {
			return err
		}
		if err := s.publishDKIM(r.Context(), d.Name, k.Selector, algorithm, privPEM); err != nil {
			return fmt.Errorf("%w: %w", errDKIMPublish, err)
		}
		if old != "" && old != k.Selector {
			if err := s.kv.HDel(r.Context(), dkimKeysHash, old+"."+d.Name).Err(); err != nil {
				return fmt.Errorf("%w: %w", errDKIMPublish, err)
			}
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "dkim_key_not_pending", "Only a new key that is not signing yet can be activated.")
		return
	}
	if errors.Is(err, errDKIMPublish) {
		s.log.Error("dkim switch failed", "domain", d.Name, "err", err)
		writeError(w, http.StatusServiceUnavailable, "dkim_signer_unavailable", "The DKIM signer is temporarily unavailable. Try activating the key again.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &d.OrgID, "dkim_key.activate", "dkim_key", k.ID, map[string]any{"domain": d.Name, "selector": k.Selector, "algorithm": k.Algorithm, "retired": old})
	s.writeDKIMKey(w, r, d, k.ID, http.StatusOK)
}

func (s *Server) deleteDKIMKey(w http.ResponseWriter, r *http.Request, p *principal) {
	d, k, ok := s.dkimKeyFromPath(w, r, p)
	if !ok {
		return
	}
	if k.State == "active" && k.Algorithm == "rsa" {
		writeError(w, http.StatusConflict, "dkim_key_in_use", "The RSA key signs all mail for this domain. Rotate it to replace it instead.")
		return
	}
	if k.RemoveAfter != nil && time.Now().Before(*k.RemoveAfter) {
		writeError(w, http.StatusConflict, "dkim_key_retiring", "Keep the retired key's DNS record until removeAfter.")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockDKIMDomain(ctx, tx, d.ID); err != nil {
			return err
		}
		if k.State == "active" {
			tag, err := tx.Exec(ctx, `UPDATE dkim_keys SET state = 'retired', retired_at = now(), private_key = '' WHERE id = $1 AND state = 'active'`, k.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return pgx.ErrNoRows
			}
			if err := s.kv.HDel(ctx, dkimEd25519SelectorsHash, d.Name).Err(); err != nil {
				return fmt.Errorf("%w: %w", errDKIMPublish, err)
			}
			if err := s.kv.HDel(ctx, dkimKeysHash, k.Selector+"."+d.Name).Err(); err != nil {
				return fmt.Errorf("%w: %w", errDKIMPublish, err)
			}
			return nil
		}
		tag, err := tx.Exec(ctx, `DELETE FROM dkim_keys WHERE id = $1 AND state = $2`, k.ID, k.State)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if k.State == "pending" {
			selected, err := s.kv.HGet(ctx, dkimSelectorHash(k.Algorithm), d.Name).Result()
			if err != nil && !errors.Is(err, redis.Nil) {
				return fmt.Errorf("%w: %w", errDKIMPublish, err)
			}
			if selected == k.Selector {
				return pgx.ErrNoRows
			}
		}
		if err := s.kv.HDel(ctx, dkimKeysHash, k.Selector+"."+d.Name).Err(); err != nil {
			return fmt.Errorf("%w: %w", errDKIMPublish, err)
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "dkim_key_changed", "The key changed. Reload the keys and try again.")
		return
	}
	if errors.Is(err, errDKIMPublish) {
		writeError(w, http.StatusServiceUnavailable, "dkim_signer_unavailable", "The DKIM signer is temporarily unavailable. Try again.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if k.State == "active" {
		s.audit(ctx, r, p, &d.OrgID, "dkim_key.retire", "dkim_key", k.ID, map[string]any{"domain": d.Name, "selector": k.Selector, "algorithm": k.Algorithm})
		s.writeDKIMKey(w, r, d, k.ID, http.StatusOK)
		return
	}
	s.audit(r.Context(), r, p, &d.OrgID, "dkim_key.delete", "dkim_key", k.ID, map[string]any{"domain": d.Name, "selector": k.Selector, "algorithm": k.Algorithm, "state": k.State})
	w.WriteHeader(http.StatusNoContent)
}

func lockDKIMDomain(ctx context.Context, tx pgx.Tx, id string) error {
	return tx.QueryRow(ctx, `SELECT id FROM domains WHERE id = $1 FOR UPDATE`, id).Scan(&id)
}

func dkimSelectorHash(algorithm string) string {
	if algorithm == "ed25519" {
		return dkimEd25519SelectorsHash
	}
	return dkimSelectorsHash
}

func (s *Server) publishDKIM(ctx context.Context, name, selector, algorithm string, privPEM []byte) error {
	if err := s.kv.HSet(ctx, dkimKeysHash, selector+"."+name, privPEM).Err(); err != nil {
		return err
	}
	return s.kv.HSet(ctx, dkimSelectorHash(algorithm), name, selector).Err()
}

func (s *Server) syncDomainDKIM(ctx context.Context, id, name string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockDKIMDomain(ctx, tx, id); err != nil {
			return err
		}
		type activeKey struct {
			ID, Selector, Algorithm string
			PrivateKey              []byte
		}
		rows, err := tx.Query(ctx, `SELECT k.id, k.selector, k.algorithm, k.private_key FROM dkim_keys k
			JOIN domains d ON d.id = k.domain_id WHERE d.id = $1 AND d.active AND k.state = 'active'`, id)
		if err != nil {
			return err
		}
		keys, err := pgx.CollectRows(rows, pgx.RowToStructByPos[activeKey])
		if err != nil {
			return err
		}
		dual := false
		for _, key := range keys {
			dual = dual || key.Algorithm == "ed25519"
			privPEM, err := s.box.Open(key.PrivateKey, "dkim:"+key.ID)
			if err != nil {
				return errors.New("cannot decrypt DKIM key; was AETHER_SECRET_KEY changed?")
			}
			if err := s.publishDKIM(ctx, name, key.Selector, key.Algorithm, privPEM); err != nil {
				return err
			}
		}
		if !dual {
			if err := s.kv.HDel(ctx, dkimEd25519SelectorsHash, name).Err(); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Server) SyncDKIM(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id, name FROM domains WHERE active`)
	if err != nil {
		return err
	}
	domains, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, Name string }])
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range domains {
		if err := s.syncDomainDKIM(ctx, d.ID, d.Name); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			errs = append(errs, fmt.Errorf("%s: %w", d.Name, err))
		}
	}
	return errors.Join(errs...)
}

func dkimRecordMatches(expected, found string) bool {
	_, params, _ := strings.Cut(expected, "k=")
	algorithm, publicKey, ok := strings.Cut(params, "; p=")
	if !ok {
		return false
	}
	tags := map[string]string{"k": "rsa"}
	seen := map[string]bool{}
	for i, tag := range strings.Split(strings.TrimSpace(found), ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(tag), "=")
		name = strings.TrimSpace(name)
		if name == "" && !ok && strings.TrimSpace(tag) == "" {
			continue
		}
		if !ok || seen[name] || name == "v" && i != 0 {
			return false
		}
		seen[name] = true
		tags[name] = strings.TrimSpace(value)
	}
	if seen["v"] && tags["v"] != "DKIM1" || tags["k"] != algorithm {
		return false
	}
	if seen["h"] && !slices.Contains(strings.Split(tags["h"], ":"), "sha256") {
		return false
	}
	if seen["s"] && !slices.Contains(strings.Split(tags["s"], ":"), "email") && !slices.Contains(strings.Split(tags["s"], ":"), "*") {
		return false
	}
	got := strings.Join(strings.Fields(tags["p"]), "")
	if got == publicKey || got == strings.TrimRight(publicKey, "=") {
		return true
	}
	if algorithm != "rsa" {
		return false
	}
	der, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		der, err = base64.RawStdEncoding.DecodeString(got)
	}
	if err != nil {
		return false
	}
	key, err := x509.ParsePKCS1PublicKey(der)
	if err != nil {
		return false
	}
	spki, err := x509.MarshalPKIXPublicKey(key)
	return err == nil && base64.StdEncoding.EncodeToString(spki) == publicKey
}
