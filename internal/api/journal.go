package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type journalRule struct {
	ID             string    `json:"id"`
	Match          string    `json:"match"`
	ArchiveAddress string    `json:"archiveAddress"`
	CreatedBy      *string   `json:"createdBy"`
	CreatedAt      time.Time `json:"createdAt"`
}

func (s *Server) listJournalRules(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.complianceOrg(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT j.id, j.match, j.archive_address, u.email, j.created_at FROM journal_rules j LEFT JOIN users u ON u.id = j.created_by WHERE j.org_id = $1 ORDER BY j.match`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[journalRule])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func validArchiveAddress(address string) bool {
	parsed, err := mail.ParseAddress(address)
	return err == nil && parsed.Address == address && parsed.Name == "" && strings.Count(address, "@") == 1 && !strings.ContainsAny(address, "*?%'\"\\")
}

func (s *Server) createJournalRule(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.complianceOrg(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Match          string `json:"match"`
		ArchiveAddress string `json:"archiveAddress"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	in.Match = strings.ToLower(strings.TrimSpace(in.Match))
	in.ArchiveAddress = strings.ToLower(strings.TrimSpace(in.ArchiveAddress))
	local, domain, _ := strings.Cut(in.Match, "@")
	if local != "" && !localPartPattern.MatchString(local) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_match", "Journal an address such as person@example.com, or a whole domain as @example.com.")
		return
	}
	if !validArchiveAddress(in.ArchiveAddress) || in.ArchiveAddress == in.Match {
		writeError(w, http.StatusUnprocessableEntity, "invalid_archive", "Enter the archive's email address. It cannot be the journaled address itself.")
		return
	}
	var domainID string
	err := s.db.QueryRow(r.Context(), `SELECT id FROM domains WHERE org_id = $1 AND name = $2`, orgID, domain).Scan(&domainID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_match", "Journal an address or domain that belongs to this organization.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var out journalRule
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO journal_rules (org_id, domain_id, match, archive_address, created_by) VALUES ($1, $2, $3, $4, $5)
		RETURNING id, match, archive_address, (SELECT email FROM users WHERE id = $5), created_at`,
		orgID, domainID, in.Match, in.ArchiveAddress, p.User.ID).Scan(&out.ID, &out.Match, &out.ArchiveAddress, &out.CreatedBy, &out.CreatedAt)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "journal_rule_exists", in.Match+" is already journaled. Delete that rule first to change its archive.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "compliance.journal_rule_create", "journal_rule", out.ID, map[string]any{"match": out.Match, "archiveAddress": out.ArchiveAddress})
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) deleteJournalRule(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "ruleId")
	if !ok {
		return
	}
	var orgID, match string
	err := s.db.QueryRow(r.Context(), `SELECT org_id, match FROM journal_rules WHERE id = $1`, id).Scan(&orgID, &match)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !p.canCompliance(orgID)) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM journal_rules WHERE id = $1`, id); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "compliance.journal_rule_delete", "journal_rule", id, map[string]any{"match": match})
	w.WriteHeader(http.StatusNoContent)
}
