package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
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

var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

type resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, name string) ([]string, error)
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
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
	DKIMRotationDue   bool       `json:"dkimRotationDue"`
	Active            bool       `json:"active"`
	AliasOf           *string    `json:"aliasOf"`
	AliasSendAs       bool       `json:"aliasSendAs"`
	CreatedAt         time.Time  `json:"createdAt"`
	CoexistenceHost   string     `json:"coexistenceHost"`
	DualDeliveryIDs   []string   `json:"dualDeliveryMailboxIds"`
}

const domainColumns = `id, org_id, name, verified_at, verification_token,
	coalesce((SELECT selector FROM dkim_keys WHERE domain_id = domains.id AND algorithm = 'rsa' AND state = 'active'), ''),
	coalesce((SELECT public_key FROM dkim_keys WHERE domain_id = domains.id AND algorithm = 'rsa' AND state = 'active'), ''),
	EXISTS (SELECT 1 FROM dkim_keys WHERE domain_id = domains.id AND state = 'active' AND activated_at < now() - interval '365 days'),
	active, alias_of, alias_send_as, created_at, coexistence_host,
	ARRAY(SELECT m.id::text FROM mailboxes m WHERE m.domain_id = domains.id AND m.coexistence_dual AND m.active ORDER BY m.id)`

func scanDomain(row pgx.Row) (*domain, error) {
	d := &domain{}
	var pub string
	if err := row.Scan(&d.ID, &d.OrgID, &d.Name, &d.VerifiedAt, &d.VerificationToken, &d.DKIMSelector, &pub, &d.DKIMRotationDue, &d.Active, &d.AliasOf, &d.AliasSendAs, &d.CreatedAt, &d.CoexistenceHost, &d.DualDeliveryIDs); err != nil {
		return nil, err
	}
	d.Verified = d.VerifiedAt != nil
	d.DKIMRecord = dkimRecord("rsa", pub)
	return d, nil
}

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permDomainsRead, permMailboxesWrite, permAliasesWrite, permListsWrite, permSignaturesRead, permMembersWrite)
	if !ok {
		return
	}
	var rows pgx.Rows
	var err error
	if scoped, isScoped := p.scopedDomains(orgID); isScoped {
		rows, err = s.db.Query(r.Context(), `SELECT `+domainColumns+` FROM domains WHERE org_id = $1 AND id::text = ANY($2) ORDER BY name`, orgID, scoped)
	} else {
		rows, err = s.db.Query(r.Context(), `SELECT `+domainColumns+` FROM domains WHERE org_id = $1 ORDER BY name`, orgID)
	}
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
	orgID, ok := s.orgPerm(w, r, p, permDomainsWrite)
	if !ok {
		return
	}
	if _, isScoped := p.scopedDomains(orgID); isScoped {
		forbidden(w)
		return
	}
	var in struct {
		Name        string  `json:"name"`
		AliasOf     *string `json:"aliasOf"`
		AliasSendAs bool    `json:"aliasSendAs"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.Name)), ".")
	if !domainPattern.MatchString(name) || len(name) > 253 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_domain", "Enter a fully qualified domain name such as example.com.")
		return
	}
	if in.AliasOf != nil {
		var target bool
		if uuidPattern.MatchString(*in.AliasOf) {
			if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM domains WHERE id = $1 AND org_id = $2 AND alias_of IS NULL)`, *in.AliasOf, orgID).Scan(&target); err != nil {
				s.internalError(w, r, err)
				return
			}
		}
		if !target {
			writeError(w, http.StatusUnprocessableEntity, "invalid_alias_target", "Choose a domain of this organization that is not itself an alias domain.")
			return
		}
	}
	in.AliasSendAs = in.AliasSendAs && in.AliasOf != nil
	key, err := generateDKIMKey("rsa", 2048)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	selector := newDKIMSelector("rsa")
	var verifiedAt *time.Time
	if p.User.IsPlatformAdmin {
		now := time.Now()
		verifiedAt = &now
	}
	var id string
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `INSERT INTO domains (org_id, name, verification_token, verified_at, alias_of, alias_send_as) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			orgID, name, secrets.Token(20), verifiedAt, in.AliasOf, in.AliasSendAs).Scan(&id); err != nil {
			return err
		}
		_, err := s.insertDKIMKey(r.Context(), tx, id, selector, "active", key)
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
	if err := s.syncDomainDKIM(r.Context(), id, name); err != nil {
		s.log.Error("dkim publish failed; outgoing mail for this domain will be unsigned until the API restarts", "domain", name, "err", err)
	}
	if err := s.publishAttachmentPolicy(r.Context(), orgID); err != nil {
		s.log.Error("attachment policy publish failed; attachments for this domain are not filtered until the API restarts", "domain", name, "err", err)
	}
	s.audit(r.Context(), r, p, &orgID, "domain.create", "domain", id, map[string]any{"name": name, "aliasOf": in.AliasOf, "aliasSendAs": in.AliasSendAs})
	d, err := scanDomain(s.db.QueryRow(r.Context(), `SELECT `+domainColumns+` FROM domains WHERE id = $1`, id))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) domainFromPath(w http.ResponseWriter, r *http.Request, p *principal, perm string) (*domain, bool) {
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
	if !p.canOnDomain(d.OrgID, d.ID, perm) {
		forbidden(w)
		return nil, false
	}
	return d, true
}

func (s *Server) getDomain(w http.ResponseWriter, r *http.Request, p *principal) {
	if d, ok := s.domainFromPath(w, r, p, permDomainsRead); ok {
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p, permDomainsWrite)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockDKIMDomain(ctx, tx, d.ID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT selector FROM dkim_keys WHERE domain_id = $1`, d.ID)
		if err != nil {
			return err
		}
		selectors, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM domains WHERE id = $1`, d.ID); err != nil {
			return err
		}
		_, err = s.kv.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HDel(ctx, dkimSelectorsHash, d.Name)
			pipe.HDel(ctx, dkimEd25519SelectorsHash, d.Name)
			if len(selectors) > 0 {
				for i := range selectors {
					selectors[i] += "." + d.Name
				}
				pipe.HDel(ctx, dkimKeysHash, selectors...)
			}
			return nil
		})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "domains_alias_of_fkey" {
		writeError(w, http.StatusConflict, "domain_has_alias_domains", "Alias domains still point to this domain. Delete them first; the domain is unchanged.")
		return
	}
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		writeError(w, http.StatusConflict, "domain_not_empty", "This domain still has mailboxes. Delete them first; the domain and its aliases are unchanged.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.kv.HDel(r.Context(), attachmentPolicyHash, d.Name)
	s.audit(r.Context(), r, p, &d.OrgID, "domain.delete", "domain", d.ID, map[string]any{"name": d.Name})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateDomain(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p, permDomainsWrite)
	if !ok {
		return
	}
	var in struct {
		AliasSendAs *bool `json:"aliasSendAs"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if in.AliasSendAs != nil {
		if d.AliasOf == nil {
			writeError(w, http.StatusUnprocessableEntity, "not_alias_domain", "Only alias domains have a send-as setting.")
			return
		}
		if _, err := s.db.Exec(r.Context(), `UPDATE domains SET alias_send_as = $2 WHERE id = $1`, d.ID, *in.AliasSendAs); err != nil {
			s.internalError(w, r, err)
			return
		}
		s.audit(r.Context(), r, p, &d.OrgID, "domain.update", "domain", d.ID, map[string]any{"name": d.Name, "aliasSendAs": *in.AliasSendAs})
		d.AliasSendAs = *in.AliasSendAs
	}
	writeJSON(w, http.StatusOK, d)
}

type dnsRecord struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Value   string   `json:"value"`
	Purpose string   `json:"purpose"`
	Status  string   `json:"status"`
	Found   []string `json:"found"`
}

func (s *Server) expectedRecords(d *domain, keys []dkimKey) []dnsRecord {
	records := []dnsRecord{
		{Type: "TXT", Name: "_aether." + d.Name, Value: "aether-verification=" + d.VerificationToken, Purpose: "verification"},
		{Type: "MX", Name: d.Name, Value: "10 " + s.cfg.MailHostname + ".", Purpose: "mx"},
		{Type: "TXT", Name: d.Name, Value: "v=spf1 mx -all", Purpose: "spf"},
	}
	for _, k := range keys {
		if k.RemoveAfter == nil || time.Now().Before(*k.RemoveAfter) {
			records = append(records, dnsRecord{Type: "TXT", Name: k.DNSName, Value: k.Record, Purpose: "dkim"})
		}
	}
	return append(records, []dnsRecord{
		{Type: "TXT", Name: "_dmarc." + d.Name, Value: "v=DMARC1; p=quarantine; adkim=s; aspf=s; rua=mailto:dmarc-reports@" + d.Name, Purpose: "dmarc"},
		{Type: "CNAME", Name: "mta-sts." + d.Name, Value: s.cfg.PublicURL.Hostname() + ".", Purpose: "mta-sts-host"},
		{Type: "TXT", Name: "_mta-sts." + d.Name, Value: "v=STSv1; id=" + s.mtaSTSPolicyID(), Purpose: "mta-sts"},
		{Type: "TXT", Name: "_smtp._tls." + d.Name, Value: "v=TLSRPTv1; rua=mailto:postmaster@" + d.Name, Purpose: "tls-rpt"},
		{Type: "CNAME", Name: "autoconfig." + d.Name, Value: s.cfg.PublicURL.Hostname() + ".", Purpose: "autoconfig"},
		{Type: "CNAME", Name: "autodiscover." + d.Name, Value: s.cfg.PublicURL.Hostname() + ".", Purpose: "autodiscover"},
		{Type: "SRV", Name: "_imaps._tcp." + d.Name, Value: "0 1 993 " + s.cfg.MailHostname + ".", Purpose: "srv-imaps"},
		{Type: "SRV", Name: "_submissions._tcp." + d.Name, Value: "0 1 465 " + s.cfg.MailHostname + ".", Purpose: "srv-submissions"},
	}...)
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
	name, ok := hostDomain(host, "mta-sts.")
	return ok && s.verifiedDomain(ctx, name)
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
	name, ok := hostDomain(r.URL.Query().Get("domain"), "mta-sts.", "autoconfig.", "autodiscover.")
	if !ok || !s.verifiedDomain(r.Context(), name) {
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
	if rec.Type == "SRV" {
		parts := strings.SplitN(rec.Name, ".", 3)
		var srvs []*net.SRV
		_, srvs, err = s.resolve.LookupSRV(ctx, strings.TrimPrefix(parts[0], "_"), strings.TrimPrefix(parts[1], "_"), parts[2])
		for _, srv := range srvs {
			rec.Found = append(rec.Found, fmt.Sprintf("%d %d %d %s", srv.Priority, srv.Weight, srv.Port, strings.ToLower(srv.Target)))
		}
	} else if rec.Type == "MX" {
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
	if rec.Purpose == "spf" {
		rec.Status = s.spfStatus(ctx, rec.Name)
		return
	}
	rec.Status = "mismatch"
	if rec.Purpose == "dkim" && len(rec.Found) != 1 {
		return
	}
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
	case "dmarc":
		return strings.HasPrefix(normalized, "v=dmarc1;")
	case "mta-sts":
		return normalized == strings.ToLower(strings.Join(strings.Fields(rec.Value), ""))
	case "srv-imaps", "srv-submissions":
		f, want := strings.Fields(found), strings.Fields(rec.Value)
		return len(f) == 4 && f[2] == want[2] && strings.TrimSuffix(f[3], ".") == mailHost
	case "tls-rpt":
		return strings.HasPrefix(normalized, "v=tlsrptv1;")
	case "dkim":
		return dkimRecordMatches(rec.Value, found)
	default:
		return strings.TrimSpace(found) == rec.Value
	}
}

func (s *Server) domainDNS(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p, permDomainsRead)
	if !ok {
		return
	}
	keys, err := s.dkimKeys(r.Context(), d)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	records := s.expectedRecords(d, keys)
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
	d, ok := s.domainFromPath(w, r, p, permDomainsWrite)
	if !ok {
		return
	}
	if !d.Verified {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		rec := s.expectedRecords(d, nil)[0]
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
