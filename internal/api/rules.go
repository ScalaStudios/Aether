package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func (s *Server) loadSieveSettings(ctx context.Context, mailboxID string) ([]mailpkg.Rule, mailpkg.AutoReply, error) {
	var rawRules, rawReply []byte
	rules := []mailpkg.Rule{}
	var reply mailpkg.AutoReply
	err := s.db.QueryRow(ctx, `SELECT rules, auto_reply FROM mailbox_rules WHERE mailbox_id = $1`, mailboxID).Scan(&rawRules, &rawReply)
	if errors.Is(err, pgx.ErrNoRows) {
		return rules, reply, nil
	}
	if err != nil {
		return nil, reply, err
	}
	if err := json.Unmarshal(rawRules, &rules); err != nil {
		return nil, reply, err
	}
	return rules, reply, json.Unmarshal(rawReply, &reply)
}

func (s *Server) installSieve(w http.ResponseWriter, r *http.Request, p *principal, m *ownedMailbox, rules []mailpkg.Rule, reply mailpkg.AutoReply, response any) {
	rawRules, _ := json.Marshal(nonNil(rules))
	rawReply, _ := json.Marshal(reply)
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO mailbox_rules (mailbox_id, rules, auto_reply) VALUES ($1, $2, $3)
			ON CONFLICT (mailbox_id) DO UPDATE SET rules = EXCLUDED.rules, auto_reply = EXCLUDED.auto_reply, updated_at = now()`, m.ID, rawRules, rawReply)
		if err != nil {
			return err
		}
		return s.mail.PutSieve(m.Address, mailpkg.CompileSieve(rules, reply, m.Address))
	})
	if err != nil {
		s.log.Error("installing mail rules failed", "mailbox", m.ID, "err", err)
		writeError(w, http.StatusBadGateway, "rules_not_saved", "The settings could not be installed on the mail server. Nothing was changed; try again.")
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.rules.update", "mailbox", m.ID, map[string]any{"rules": len(rules), "autoReply": reply.Enabled})
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	rules, _, err := s.loadSieveSettings(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
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
	_, reply, err := s.loadSieveSettings(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.installSieve(w, r, p, m, rules, reply, nonNil(rules))
}

func (s *Server) getAutoReply(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	_, reply, err := s.loadSieveSettings(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reply)
}

func (s *Server) saveAutoReply(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	var reply mailpkg.AutoReply
	if !decode(w, r, 32<<10, &reply) {
		return
	}
	if err := reply.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_auto_reply", err.Error())
		return
	}
	rules, _, err := s.loadSieveSettings(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.installSieve(w, r, p, m, rules, reply, reply)
}
