package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func (s *Server) listRules(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	var raw []byte
	err := s.db.QueryRow(r.Context(), `SELECT rules FROM mailbox_rules WHERE mailbox_id = $1`, m.ID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		raw = []byte("[]")
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (s *Server) saveRules(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	var rules []mailpkg.Rule
	if !decode(w, r, 256<<10, &rules) {
		return
	}
	if len(rules) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "too_many_rules", "A mailbox can have up to 100 rules.")
		return
	}
	folders, err := s.mail.Folders(m.Address)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	known := map[string]bool{}
	for _, f := range folders {
		known[f.Name] = true
	}
	for _, rule := range rules {
		if err := rule.Validate(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_rule", rule.Name+": "+err.Error())
			return
		}
		if rule.Actions.MoveTo != "" && !known[rule.Actions.MoveTo] {
			writeError(w, http.StatusUnprocessableEntity, "invalid_rule", rule.Name+": the folder "+rule.Actions.MoveTo+" does not exist.")
			return
		}
	}
	raw, _ := json.Marshal(nonNil(rules))
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `INSERT INTO mailbox_rules (mailbox_id, rules) VALUES ($1, $2) ON CONFLICT (mailbox_id) DO UPDATE SET rules = EXCLUDED.rules, updated_at = now()`, m.ID, raw); err != nil {
			return err
		}
		return s.mail.PutSieve(m.Address, mailpkg.CompileSieve(rules))
	})
	if err != nil {
		s.log.Error("saving mail rules failed", "mailbox", m.ID, "err", err)
		writeError(w, http.StatusBadGateway, "rules_not_saved", "The rules could not be installed on the mail server. Nothing was changed; try again.")
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.rules.update", "mailbox", m.ID, map[string]any{"count": len(rules)})
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}
