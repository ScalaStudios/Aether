package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Server) setDomainCoexistence(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Host       *string  `json:"host"`
		MailboxIDs []string `json:"dualDeliveryMailboxIds"`
	}
	if !decode(w, r, 256<<10, &in) {
		return
	}
	if in.Host == nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_coexistence_host", "Enter the legacy SMTP hostname or an empty string to disable coexistence.")
		return
	}
	rawHost := strings.TrimSpace(*in.Host)
	host := strings.TrimSuffix(strings.ToLower(rawHost), ".")
	if rawHost != "" && (!domainPattern.MatchString(host) || len(host) > 253 ||
		host == d.Name || strings.EqualFold(host, strings.TrimSuffix(s.cfg.MailHostname, "."))) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_coexistence_host", "Enter the legacy server's fully qualified SMTP hostname.")
		return
	}
	if host != "" && (!d.Verified || !d.Active) {
		writeError(w, http.StatusUnprocessableEntity, "domain_not_ready", "Coexistence requires an active, verified domain.")
		return
	}
	invalidMailboxes := errors.New("invalid dual delivery mailboxes")
	ctx := r.Context()
	var previousHost string
	var updated *domain
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT coexistence_host FROM domains WHERE id = $1 FOR NO KEY UPDATE`, d.ID).Scan(&previousHost); err != nil {
			return err
		}
		if host == "" && len(in.MailboxIDs) != 0 {
			return invalidMailboxes
		}
		for i, id := range in.MailboxIDs {
			in.MailboxIDs[i] = strings.ToLower(id)
			if !uuidPattern.MatchString(in.MailboxIDs[i]) {
				return invalidMailboxes
			}
		}
		if in.MailboxIDs == nil {
			in.MailboxIDs = []string{}
		}
		if _, err := tx.Exec(ctx, `UPDATE mailboxes SET coexistence_dual = false WHERE domain_id = $1 AND coexistence_dual AND NOT (id = ANY($2::text[]::uuid[]))`, d.ID, in.MailboxIDs); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE mailboxes SET coexistence_dual = true WHERE domain_id = $1 AND active AND id = ANY($2::text[]::uuid[])`, d.ID, in.MailboxIDs)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != int64(len(in.MailboxIDs)) {
			return invalidMailboxes
		}
		updated, err = scanDomain(tx.QueryRow(ctx, `UPDATE domains SET coexistence_host = $2 WHERE id = $1 RETURNING `+domainColumns, d.ID, host))
		return err
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		notFound(w)
	case errors.Is(err, invalidMailboxes):
		writeError(w, http.StatusUnprocessableEntity, "invalid_dual_delivery_mailboxes", "Select distinct active mailboxes on this domain while coexistence is enabled.")
	case err != nil:
		s.internalError(w, r, err)
	default:
		switch {
		case previousHost == "" && host != "":
			s.audit(ctx, r, p, &d.OrgID, "domain.coexistence.enable", "domain", d.ID, map[string]any{"host": host})
		case previousHost != "" && host == "":
			s.audit(ctx, r, p, &d.OrgID, "domain.coexistence.disable", "domain", d.ID, map[string]any{"host": previousHost})
		}
		writeJSON(w, http.StatusOK, updated)
	}
}
