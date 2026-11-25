package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type orgLimits struct {
	MaxMailboxes    int64 `json:"maxMailboxes"`
	MaxAliases      int64 `json:"maxAliases"`
	MaxLists        int64 `json:"maxLists"`
	MaxDomains      int64 `json:"maxDomains"`
	MaxStorageBytes int64 `json:"maxStorageBytes"`
}

type orgUsage struct {
	Mailboxes    int64 `json:"mailboxes"`
	Aliases      int64 `json:"aliases"`
	Lists        int64 `json:"lists"`
	Domains      int64 `json:"domains"`
	StorageBytes int64 `json:"storageBytes"`
	NoQuota      int64 `json:"mailboxesWithoutQuota"`
}

type quotaDefault struct {
	QuotaBytes          *int64 `json:"quotaBytes"`
	EffectiveQuotaBytes int64  `json:"effectiveQuotaBytes"`
}

type limitError struct{ code, message string }

func (e *limitError) Error() string { return e.message }

func writeLimitError(w http.ResponseWriter, err error) bool {
	var e *limitError
	if !errors.As(err, &e) {
		return false
	}
	writeError(w, http.StatusConflict, e.code, e.message)
	return true
}

func ignoreLimit(err error) error {
	var e *limitError
	if errors.As(err, &e) {
		return nil
	}
	return err
}

func checkCount(code, noun string, used, limit int64) error {
	if limit > 0 && used >= limit {
		return &limitError{code, fmt.Sprintf("This organization has reached its limit of %d %s. Remove one, or ask a platform administrator to raise the limit.", limit, noun)}
	}
	return nil
}

func checkStorage(allocated, quota, limit int64) error {
	switch {
	case limit == 0:
		return nil
	case quota == 0:
		return &limitError{"quota_required", fmt.Sprintf("This organization has a storage limit of %s, so every mailbox needs a quota. Set a quota, or set a default quota for the organization or domain.", formatBytes(limit))}
	case quota > limit-allocated:
		return &limitError{"storage_limit_reached", fmt.Sprintf("A quota of %s does not fit in this organization's storage limit: %s of %s is already given to other mailboxes.", formatBytes(quota), formatBytes(allocated), formatBytes(limit))}
	}
	return nil
}

func (s *Server) lockOrgLimits(ctx context.Context, tx pgx.Tx, orgID string) (orgLimits, orgUsage, error) {
	var l orgLimits
	var u orgUsage
	err := tx.QueryRow(ctx, `SELECT max_mailboxes, max_aliases, max_lists, max_domains, max_storage_bytes FROM organizations WHERE id = $1 FOR NO KEY UPDATE`, orgID).
		Scan(&l.MaxMailboxes, &l.MaxAliases, &l.MaxLists, &l.MaxDomains, &l.MaxStorageBytes)
	if err != nil {
		return l, u, err
	}
	err = tx.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM mailboxes WHERE org_id = $1),
			(SELECT count(*) FROM aliases WHERE org_id = $1 AND kind = 'alias' AND personal_owner_id IS NULL),
			(SELECT count(*) FROM aliases WHERE org_id = $1 AND kind = 'list'),
			(SELECT count(*) FROM domains WHERE org_id = $1),
			(SELECT least(coalesce(sum(quota_bytes), 0), 9223372036854775807)::bigint FROM mailboxes WHERE org_id = $1),
			(SELECT count(*) FROM mailboxes WHERE org_id = $1 AND quota_bytes = 0)`, orgID).
		Scan(&u.Mailboxes, &u.Aliases, &u.Lists, &u.Domains, &u.StorageBytes, &u.NoQuota)
	return l, u, err
}

func (s *Server) reserveMailbox(ctx context.Context, tx pgx.Tx, orgID, domainID string, quota *int64) (int64, error) {
	l, u, err := s.lockOrgLimits(ctx, tx, orgID)
	if err != nil {
		return 0, err
	}
	if quota == nil {
		quota = new(int64)
		if err := tx.QueryRow(ctx, `SELECT coalesce(d.default_quota_bytes, o.default_quota_bytes, 0) FROM domains d JOIN organizations o ON o.id = d.org_id WHERE d.id = $1`, domainID).Scan(quota); err != nil {
			return 0, err
		}
	}
	if err := checkCount("mailbox_limit_reached", "mailboxes", u.Mailboxes, l.MaxMailboxes); err != nil {
		return 0, err
	}
	return *quota, checkStorage(u.StorageBytes, *quota, l.MaxStorageBytes)
}

func (s *Server) checkQuotaChange(ctx context.Context, tx pgx.Tx, orgID, mailboxID string, quota int64) error {
	l, u, err := s.lockOrgLimits(ctx, tx, orgID)
	if err != nil {
		return err
	}
	var current int64
	if err := tx.QueryRow(ctx, `SELECT quota_bytes FROM mailboxes WHERE id = $1`, mailboxID).Scan(&current); err != nil {
		return err
	}
	if quota == current || (quota != 0 && quota < current) {
		return nil
	}
	return checkStorage(u.StorageBytes-current, quota, l.MaxStorageBytes)
}

func (s *Server) checkAliasLimit(ctx context.Context, tx pgx.Tx, orgID, kind string) error {
	l, u, err := s.lockOrgLimits(ctx, tx, orgID)
	if err != nil {
		return err
	}
	if kind == "list" {
		return checkCount("list_limit_reached", "distribution lists", u.Lists, l.MaxLists)
	}
	return checkCount("alias_limit_reached", "aliases", u.Aliases, l.MaxAliases)
}

func (s *Server) checkDomainLimit(ctx context.Context, tx pgx.Tx, orgID string) error {
	l, u, err := s.lockOrgLimits(ctx, tx, orgID)
	if err != nil {
		return err
	}
	return checkCount("domain_limit_reached", "domains", u.Domains, l.MaxDomains)
}

func (s *Server) getOrganizationLimits(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var out struct {
		orgLimits
		Usage orgUsage `json:"usage"`
	}
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		var err error
		out.orgLimits, out.Usage, err = s.lockOrgLimits(r.Context(), tx, orgID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveOrganizationLimits(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	orgID, ok := pathID(w, r, "orgId")
	if !ok {
		return
	}
	var in orgLimits
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if min(in.MaxMailboxes, in.MaxAliases, in.MaxLists, in.MaxDomains, in.MaxStorageBytes) < 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_limit", "Limits cannot be negative. Use 0 for no limit.")
		return
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE organizations SET max_mailboxes = $2, max_aliases = $3, max_lists = $4, max_domains = $5, max_storage_bytes = $6 WHERE id = $1`,
		orgID, in.MaxMailboxes, in.MaxAliases, in.MaxLists, in.MaxDomains, in.MaxStorageBytes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "organization.limits.update", "organization", orgID, map[string]any{"limits": in})
	s.getOrganizationLimits(w, r, p)
}

func (s *Server) getOrganizationDefaults(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var out quotaDefault
	err := s.db.QueryRow(r.Context(), `SELECT default_quota_bytes, coalesce(default_quota_bytes, 0) FROM organizations WHERE id = $1`, orgID).Scan(&out.QuotaBytes, &out.EffectiveQuotaBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func decodeQuotaDefault(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	var in struct {
		QuotaBytes *int64 `json:"quotaBytes"`
	}
	if !decode(w, r, 8<<10, &in) {
		return nil, false
	}
	if in.QuotaBytes != nil && *in.QuotaBytes < 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_quota", "The quota cannot be negative.")
		return nil, false
	}
	return in.QuotaBytes, true
}

func (s *Server) saveOrganizationDefaults(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	quota, ok := decodeQuotaDefault(w, r)
	if !ok {
		return
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE organizations SET default_quota_bytes = $2 WHERE id = $1`, orgID, quota)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "organization.defaults.update", "organization", orgID, map[string]any{"quotaBytes": quota})
	s.getOrganizationDefaults(w, r, p)
}

func (s *Server) getDomainDefaults(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p, permDomainsRead)
	if !ok {
		return
	}
	var out quotaDefault
	err := s.db.QueryRow(r.Context(), `SELECT d.default_quota_bytes, coalesce(d.default_quota_bytes, o.default_quota_bytes, 0) FROM domains d JOIN organizations o ON o.id = d.org_id WHERE d.id = $1`, d.ID).
		Scan(&out.QuotaBytes, &out.EffectiveQuotaBytes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveDomainDefaults(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p, permDomainsWrite)
	if !ok {
		return
	}
	quota, ok := decodeQuotaDefault(w, r)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE domains SET default_quota_bytes = $2 WHERE id = $1`, d.ID, quota); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &d.OrgID, "domain.defaults.update", "domain", d.ID, map[string]any{"name": d.Name, "quotaBytes": quota})
	s.getDomainDefaults(w, r, p)
}
