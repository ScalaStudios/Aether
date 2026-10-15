package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/scalastudios/aether/internal/secrets"
)

const (
	dkimKeysHash      = "DKIM_KEYS"
	dkimSelectorsHash = "DKIM_SELECTORS"
)

var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

type resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, name string) ([]string, error)
}

type domain struct {
	ID                string     `json:"id"`
	OrgID             string     `json:"orgId"`
	Name              string     `json:"name"`
	Verified          bool       `json:"verified"`
	VerifiedAt        *time.Time `json:"verifiedAt"`
	VerificationToken string     `json:"verificationToken"`
	DKIMSelector      string     `json:"dkimSelector"`
	DKIMRecord        string     `json:"dkimRecord"`
	Active            bool       `json:"active"`
	CreatedAt         time.Time  `json:"createdAt"`
}

const domainColumns = `id, org_id, name, verified_at, verification_token, dkim_selector, dkim_public_key, active, created_at`

func scanDomain(row pgx.Row) (*domain, error) {
	d := &domain{}
	var pub string
	if err := row.Scan(&d.ID, &d.OrgID, &d.Name, &d.VerifiedAt, &d.VerificationToken, &d.DKIMSelector, &pub, &d.Active, &d.CreatedAt); err != nil {
		return nil, err
	}
	d.Verified = d.VerifiedAt != nil
	d.DKIMRecord = "v=DKIM1; k=rsa; p=" + pub
	return d, nil
}

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+domainColumns+` FROM domains WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()
	out := []*domain{}
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.Name)), ".")
	if !domainPattern.MatchString(name) || len(name) > 253 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_domain", "Enter a fully qualified domain name such as example.com.")
		return
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	selector := "aether" + time.Now().UTC().Format("200601")
	var verifiedAt *time.Time
	if p.User.IsPlatformAdmin {
		now := time.Now()
		verifiedAt = &now
	}
	var id string
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `INSERT INTO domains (org_id, name, verification_token, verified_at, dkim_selector, dkim_private_key, dkim_public_key) VALUES ($1, $2, $3, $4, $5, '', $6) RETURNING id`,
			orgID, name, secrets.Token(20), verifiedAt, selector, base64.StdEncoding.EncodeToString(pub)).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `UPDATE domains SET dkim_private_key = $2 WHERE id = $1`, id, s.box.Seal(privPEM, "dkim:"+id))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, http.StatusConflict, "domain_taken", "This domain is already registered on this server.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.publishDKIM(r.Context(), name, selector, privPEM); err != nil {
		s.log.Error("dkim publish failed; outgoing mail for this domain will be unsigned until the API restarts", "domain", name, "err", err)
	}
	s.audit(r.Context(), r, p, &orgID, "domain.create", "domain", id, map[string]any{"name": name})
	d, err := scanDomain(s.db.QueryRow(r.Context(), `SELECT `+domainColumns+` FROM domains WHERE id = $1`, id))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) domainFromPath(w http.ResponseWriter, r *http.Request, p *principal) (*domain, bool) {
	id, ok := pathID(w, r, "domainId")
	if !ok {
		return nil, false
	}
	d, err := scanDomain(s.db.QueryRow(r.Context(), `SELECT `+domainColumns+` FROM domains WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.orgRole(d.OrgID) == "") {
		notFound(w)
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	if !p.canAdminOrg(d.OrgID) {
		forbidden(w)
		return nil, false
	}
	return d, true
}

func (s *Server) getDomain(w http.ResponseWriter, r *http.Request, p *principal) {
	if d, ok := s.domainFromPath(w, r, p); ok {
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM domains WHERE id = $1`, d.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.kv.HDel(r.Context(), dkimKeysHash, d.DKIMSelector+"."+d.Name)
	s.kv.HDel(r.Context(), dkimSelectorsHash, d.Name)
	s.audit(r.Context(), r, p, &d.OrgID, "domain.delete", "domain", d.ID, map[string]any{"name": d.Name})
	w.WriteHeader(http.StatusNoContent)
}

type dnsRecord struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Value   string   `json:"value"`
	Purpose string   `json:"purpose"`
	Status  string   `json:"status"`
	Found   []string `json:"found"`
}

func (s *Server) expectedRecords(d *domain) []dnsRecord {
	return []dnsRecord{
		{Type: "TXT", Name: "_aether." + d.Name, Value: "aether-verification=" + d.VerificationToken, Purpose: "verification"},
		{Type: "MX", Name: d.Name, Value: "10 " + s.cfg.MailHostname + ".", Purpose: "mx"},
		{Type: "TXT", Name: d.Name, Value: "v=spf1 mx -all", Purpose: "spf"},
		{Type: "TXT", Name: d.DKIMSelector + "._domainkey." + d.Name, Value: d.DKIMRecord, Purpose: "dkim"},
		{Type: "TXT", Name: "_dmarc." + d.Name, Value: "v=DMARC1; p=quarantine; adkim=s; aspf=s; rua=mailto:dmarc-reports@" + d.Name, Purpose: "dmarc"},
		{Type: "CNAME", Name: "mta-sts." + d.Name, Value: s.cfg.PublicURL.Hostname() + ".", Purpose: "mta-sts-host"},
		{Type: "TXT", Name: "_mta-sts." + d.Name, Value: "v=STSv1; id=" + s.mtaSTSPolicyID(), Purpose: "mta-sts"},
		{Type: "TXT", Name: "_smtp._tls." + d.Name, Value: "v=TLSRPTv1; rua=mailto:postmaster@" + d.Name, Purpose: "tls-rpt"},
	}
}

func (s *Server) mtaSTSPolicy() string {
	maxAge := "86400"
	if s.cfg.MTASTSMode == "enforce" {
		maxAge = "604800"
	}
	return "version: STSv1\r\nmode: " + s.cfg.MTASTSMode + "\r\nmx: " + s.cfg.MailHostname + "\r\nmax_age: " + maxAge + "\r\n"
}

func (s *Server) mtaSTSPolicyID() string {
	sum := sha256.Sum256([]byte(s.mtaSTSPolicy()))
	return hex.EncodeToString(sum[:8])
}

func (s *Server) servesMTASTS(ctx context.Context, host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	name, ok := strings.CutPrefix(strings.ToLower(host), "mta-sts.")
	if !ok {
		return false
	}
	var found bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM domains WHERE name = $1 AND verified_at IS NOT NULL AND active)`, name).Scan(&found)
	return err == nil && found
}

func (s *Server) mtaSTSPolicyFile(w http.ResponseWriter, r *http.Request) {
	if !s.servesMTASTS(r.Context(), r.Host) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=300")
	w.Write([]byte(s.mtaSTSPolicy()))
}

func (s *Server) tlsAsk(w http.ResponseWriter, r *http.Request) {
	if !s.servesMTASTS(r.Context(), r.URL.Query().Get("domain")) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) checkRecord(ctx context.Context, rec *dnsRecord) {
	rec.Found = []string{}
	var err error
	if rec.Type == "CNAME" {
		s.checkHost(ctx, rec)
		return
	}
	if rec.Type == "MX" {
		var mxs []*net.MX
		mxs, err = s.resolve.LookupMX(ctx, rec.Name)
		for _, mx := range mxs {
			rec.Found = append(rec.Found, strings.TrimSuffix(strings.ToLower(mx.Host), "."))
		}
	} else {
		var txts []string
		txts, err = s.resolve.LookupTXT(ctx, rec.Name)
		for _, t := range txts {
			if rec.Purpose != "spf" || strings.HasPrefix(strings.ToLower(t), "v=spf1") {
				rec.Found = append(rec.Found, t)
			}
		}
	}
	var dnsErr *net.DNSError
	if err != nil && !(errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
		rec.Status = "error"
		return
	}
	if len(rec.Found) == 0 {
		rec.Status = "missing"
		return
	}
	rec.Status = "mismatch"
	for _, f := range rec.Found {
		if recordMatches(rec, f, s.cfg.MailHostname) {
			rec.Status = "ok"
		}
	}
}

func (s *Server) checkHost(ctx context.Context, rec *dnsRecord) {
	got, err := s.resolve.LookupHost(ctx, rec.Name)
	rec.Found = got
	if rec.Found == nil {
		rec.Found = []string{}
	}
	var dnsErr *net.DNSError
	switch {
	case err != nil && errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		rec.Status = "missing"
		return
	case err != nil:
		rec.Status = "error"
		return
	}
	want, err := s.resolve.LookupHost(ctx, strings.TrimSuffix(rec.Value, "."))
	if err != nil {
		rec.Status = "error"
		return
	}
	rec.Status = "mismatch"
	for _, g := range got {
		if slices.Contains(want, g) {
			rec.Status = "ok"
		}
	}
}

func recordMatches(rec *dnsRecord, found, mailHost string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(found), ""))
	switch rec.Purpose {
	case "mx":
		return found == mailHost
	case "spf":
		fields := strings.Fields(strings.ToLower(found))
		for _, f := range fields {
			if f == "mx" || f == "+mx" || f == "a:"+mailHost || f == "+a:"+mailHost {
				return true
			}
		}
		return false
	case "dmarc":
		return strings.HasPrefix(normalized, "v=dmarc1;")
	case "mta-sts":
		return normalized == strings.ToLower(strings.Join(strings.Fields(rec.Value), ""))
	case "tls-rpt":
		return strings.HasPrefix(normalized, "v=tlsrptv1;")
	case "dkim":
		_, pub, _ := strings.Cut(rec.Value, "p=")
		return strings.Contains(strings.ReplaceAll(found, " ", ""), "p="+pub)
	default:
		return strings.TrimSpace(found) == rec.Value
	}
}

func (s *Server) domainDNS(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	records := s.expectedRecords(d)
	done := make(chan struct{}, len(records))
	for i := range records {
		go func() {
			s.checkRecord(ctx, &records[i])
			done <- struct{}{}
		}()
	}
	for range records {
		<-done
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records})
}

func (s *Server) verifyDomain(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p)
	if !ok {
		return
	}
	if !d.Verified {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		rec := s.expectedRecords(d)[0]
		s.checkRecord(ctx, &rec)
		if rec.Status != "ok" {
			writeError(w, http.StatusUnprocessableEntity, "verification_failed", "The TXT record "+rec.Name+" does not contain "+rec.Value+" yet. DNS changes can take time to propagate; try again later.")
			return
		}
		if _, err := s.db.Exec(r.Context(), `UPDATE domains SET verified_at = now() WHERE id = $1 AND verified_at IS NULL`, d.ID); err != nil {
			s.internalError(w, r, err)
			return
		}
		s.audit(r.Context(), r, p, &d.OrgID, "domain.verify", "domain", d.ID, map[string]any{"name": d.Name})
	}
	d, err := scanDomain(s.db.QueryRow(r.Context(), `SELECT `+domainColumns+` FROM domains WHERE id = $1`, d.ID))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) publishDKIM(ctx context.Context, name, selector string, privPEM []byte) error {
	if err := s.kv.HSet(ctx, dkimKeysHash, selector+"."+name, string(privPEM)).Err(); err != nil {
		return err
	}
	return s.kv.HSet(ctx, dkimSelectorsHash, name, selector).Err()
}

func (s *Server) SyncDKIM(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id, name, dkim_selector, dkim_private_key FROM domains WHERE active`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var errs []error
	for rows.Next() {
		var id, name, selector string
		var sealed []byte
		if err := rows.Scan(&id, &name, &selector, &sealed); err != nil {
			return err
		}
		privPEM, err := s.box.Open(sealed, "dkim:"+id)
		if err != nil {
			errs = append(errs, errors.New(name+": cannot decrypt DKIM key; was AETHER_SECRET_KEY changed?"))
			continue
		}
		if err := s.publishDKIM(ctx, name, selector, privPEM); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(append(errs, rows.Err())...)
}
