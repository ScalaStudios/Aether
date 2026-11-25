package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

func (a *alias) personalOwner(p *principal) bool {
	return a.PersonalOwnerID != nil && *a.PersonalOwnerID == p.User.ID
}

func (s *Server) getPersonalAliasPolicy(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, false)
	if !ok {
		return
	}
	var limit int
	var domains json.RawMessage
	err := s.db.QueryRow(r.Context(), `SELECT personal_alias_limit,
		(SELECT coalesce(jsonb_agg(jsonb_build_object('id', id, 'name', name, 'available', active AND verified_at IS NOT NULL) ORDER BY name), '[]')
		FROM domains WHERE org_id = o.id AND allow_personal_aliases) FROM organizations o WHERE id = $1`, orgID).Scan(&limit, &domains)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"limitPerUser": limit, "domains": domains})
}

func (s *Server) setPersonalAliasPolicy(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		Limit     *int32   `json:"limitPerUser"`
		DomainIDs []string `json:"domainIds"`
	}
	if !decode(w, r, 64<<10, &in) {
		return
	}
	if in.Limit == nil || *in.Limit < 0 || in.DomainIDs == nil {
		writeError(w, 422, "invalid_policy", "Set a nonnegative limit and choose the allowed domains.")
		return
	}
	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE organizations SET personal_alias_limit=$2 WHERE id=$1`, orgID, *in.Limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT FROM unnest($2::text[]) x WHERE NOT EXISTS (SELECT FROM domains WHERE org_id=$1 AND id::text=x))`, orgID, in.DomainIDs).Scan(&valid)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !valid {
		writeError(w, 422, "invalid_domain", "Choose domains from this organization.")
		return
	}
	_, err = tx.Exec(ctx, `UPDATE domains SET allow_personal_aliases=(id::text=ANY($2)) WHERE org_id=$1 AND allow_personal_aliases<>(id::text=ANY($2))`, orgID, in.DomainIDs)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(ctx, r, p, &orgID, "alias.policy.update", "organization", orgID, map[string]any{"limitPerUser": *in.Limit, "domainIds": in.DomainIDs})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listPersonalAliases(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, false)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+aliasColumns+` FROM aliases a WHERE a.org_id=$1 AND a.personal_owner_id=$2 ORDER BY a.created_at DESC, a.address`, orgID, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[alias])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var keyMailboxID *string
	err = s.db.QueryRow(r.Context(), `SELECT (SELECT mailbox_id FROM personal_alias_keys WHERE org_id=$1 AND user_id=$2)`, orgID, p.User.ID).Scan(&keyMailboxID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"aliases": nonNil(items), "keyMailboxId": keyMailboxID})
}

type personalAliasTarget struct {
	MailboxID string `json:"mailboxId"`
	DomainID  string `json:"domainId"`
}

const personalAliasTargetSQL = ` FROM organizations o
	JOIN domains d ON d.org_id=o.id AND d.allow_personal_aliases AND d.active AND d.verified_at IS NOT NULL
	JOIN mailboxes m ON m.org_id=o.id AND m.kind='user' AND m.active
	JOIN mail_users mu ON mu.id=m.id
	JOIN organization_members om ON om.org_id=o.id AND om.user_id=m.owner_user_id
	JOIN users u ON u.id=om.user_id AND u.status='active'
	WHERE o.id=$1 AND m.owner_user_id=$2 AND m.id=$3 AND d.id=$4 AND o.personal_alias_limit>0`

func (target personalAliasTarget) valid(w http.ResponseWriter) bool {
	if uuidPattern.MatchString(target.MailboxID) && uuidPattern.MatchString(target.DomainID) {
		return true
	}
	writeError(w, 422, "invalid_target", "Choose an owned mailbox and an allowed domain.")
	return false
}

type personalAliasInput struct {
	personalAliasTarget
	LocalPart string     `json:"localPart"`
	Note      string     `json:"note"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

func validAliasDetails(w http.ResponseWriter, note string, expires *time.Time) bool {
	if len(note) > 1000 {
		writeError(w, 422, "invalid_note", "Keep the note within 1000 bytes.")
		return false
	}
	if expires != nil && !expires.After(time.Now()) {
		writeError(w, 422, "invalid_expiry", "Choose a future expiry.")
		return false
	}
	return true
}

func (s *Server) createPersonalAlias(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, false)
	if !ok {
		return
	}
	var in personalAliasInput
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if a := s.insertPersonalAlias(w, r, p, orgID, in); a != nil {
		writeJSON(w, http.StatusCreated, a)
	}
}

func (s *Server) insertPersonalAlias(w http.ResponseWriter, r *http.Request, p *principal, orgID string, in personalAliasInput) *alias {
	if !in.valid(w) || !validAliasDetails(w, in.Note, in.ExpiresAt) {
		return nil
	}
	local := strings.ToLower(strings.TrimSpace(in.LocalPart))
	if local == "" {
		local = secrets.Token(8)
	}
	if !localPartPattern.MatchString(local) || strings.Contains(local, "+") || strings.Contains(local, "..") ||
		slices.Contains([]string{"abuse", "admin", "administrator", "dmarc-reports", "hostmaster", "postmaster", "security", "webmaster"}, local) {
		writeError(w, 422, "invalid_local_part", "Choose an available address without a plus tag or consecutive dots.")
		return nil
	}
	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return nil
	}
	defer tx.Rollback(ctx)
	var domain, destination string
	var limit, count int
	err = tx.QueryRow(ctx, `SELECT d.name,m.address,o.personal_alias_limit`+personalAliasTargetSQL+` FOR NO KEY UPDATE OF o FOR SHARE OF d,m,om,u`, orgID, p.User.ID, in.MailboxID, in.DomainID).Scan(&domain, &destination, &limit)
	if errors.Is(err, pgx.ErrNoRows) {
		forbidden(w)
		return nil
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FROM aliases WHERE org_id=$1 AND personal_owner_id=$2`, orgID, p.User.ID).Scan(&count)
	if err != nil {
		s.internalError(w, r, err)
		return nil
	}
	if count >= limit {
		writeError(w, 409, "alias_limit", "Your organization’s personal alias limit has been reached.")
		return nil
	}
	rows, err := tx.Query(ctx, `INSERT INTO aliases AS a (org_id,domain_id,local_part,address,destinations,personal_owner_id,personal_mailbox_id,note,expires_at)
		VALUES ($1,$2,$3,$4,ARRAY[$5::text],$6,$7,$8,$9) RETURNING `+aliasColumns, orgID, in.DomainID, local, local+"@"+domain, destination, p.User.ID, in.MailboxID, strings.TrimSpace(in.Note), in.ExpiresAt)
	var a *alias
	if err == nil {
		a, err = pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByPos[alias])
	}
	if isUniqueViolation(err) {
		writeError(w, 409, "address_taken", "This address is already in use.")
		return nil
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil
	}
	s.audit(ctx, r, p, &orgID, "alias.create", "alias", a.ID, map[string]any{"address": a.Address, "personalOwnerId": p.User.ID, "expiresAt": a.ExpiresAt})
	return a
}

func (s *Server) updatePersonalAlias(w http.ResponseWriter, r *http.Request, p *principal, a *alias) {
	var in struct {
		Active    *bool           `json:"active"`
		Note      *string         `json:"note"`
		ExpiresAt json.RawMessage `json:"expiresAt"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	var expiry *time.Time
	if in.ExpiresAt != nil && json.Unmarshal(in.ExpiresAt, &expiry) != nil {
		writeError(w, 422, "invalid_expiry", "Use an RFC 3339 timestamp or null to clear expiry.")
		return
	}
	note := ""
	if in.Note != nil {
		note = strings.TrimSpace(*in.Note)
		in.Note = &note
	}
	if !validAliasDetails(w, note, expiry) {
		return
	}
	rows, err := s.db.Query(r.Context(), `UPDATE aliases AS a SET active=coalesce($2,active),note=coalesce($3,note),
		expires_at=CASE WHEN $4 THEN $5 ELSE expires_at END,updated_at=now() WHERE id=$1 RETURNING `+aliasColumns, a.ID, in.Active, in.Note, in.ExpiresAt != nil, expiry)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	saved, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByPos[alias])
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &a.OrgID, "alias.update", "alias", a.ID, map[string]any{"address": a.Address, "active": saved.Active, "expiresAt": saved.ExpiresAt, "noteChanged": in.Note != nil})
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) setPersonalAliasKey(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, false)
	if !ok {
		return
	}
	var target personalAliasTarget
	if !decode(w, r, 4<<10, &target) || !target.valid(w) {
		return
	}
	token := secrets.Token(30)
	tag, err := s.db.Exec(r.Context(), `INSERT INTO personal_alias_keys (org_id,user_id,mailbox_id,domain_id,token_hash)
		SELECT o.id,u.id,m.id,d.id,$5`+personalAliasTargetSQL+`
		ON CONFLICT (org_id,user_id) DO UPDATE SET mailbox_id=excluded.mailbox_id,domain_id=excluded.domain_id,token_hash=excluded.token_hash`, orgID, p.User.ID, target.MailboxID, target.DomainID, secrets.SHA256(token))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		forbidden(w)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "alias.key.issue", "mailbox", target.MailboxID, map[string]any{"domainId": target.DomainID})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *Server) revokePersonalAliasKey(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, false)
	if !ok {
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM personal_alias_keys WHERE org_id=$1 AND user_id=$2`, orgID, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() > 0 {
		s.audit(r.Context(), r, p, &orgID, "alias.key.revoke", "user", p.User.ID, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) generateMaskedAlias(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Authentication, Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	token := r.Header.Get("Authentication")
	if token == "" {
		writeError(w, 401, "unauthenticated", "A personal alias key is required.")
		return
	}
	var in personalAliasInput
	var orgID string
	p := &principal{}
	err := s.db.QueryRow(r.Context(), `SELECT org_id,user_id,mailbox_id,domain_id FROM personal_alias_keys WHERE token_hash=$1`, secrets.SHA256(token)).Scan(&orgID, &p.User.ID, &in.MailboxID, &in.DomainID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 401, "unauthenticated", "Invalid personal alias key.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !s.allow(r.Context(), "personal-alias:"+p.User.ID, 60) {
		writeError(w, 429, "rate_limited", "Wait a minute before generating another address.")
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decode(w, r, 8<<10, &body) {
		return
	}
	in.Note = body.Note
	if in.Note == "" {
		in.Note = r.URL.Query().Get("hostname")
	}
	if a := s.insertPersonalAlias(w, r, p, orgID, in); a != nil {
		writeJSON(w, http.StatusCreated, map[string]any{"alias": a.Address, "email": a.Address, "id": a.ID, "enabled": a.Active, "note": a.Note})
	}
}
