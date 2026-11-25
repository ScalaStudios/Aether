package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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

func (s *Server) installSieve(w http.ResponseWriter, r *http.Request, p *principal, m *ownedMailbox, rules []mailpkg.Rule, reply mailpkg.AutoReply, response any) bool {
	rawRules, _ := json.Marshal(nonNil(rules))
	rawReply, _ := json.Marshal(reply)
	forward, err := s.forwardPolicy(r.Context(), m.OrgID)
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO mailbox_rules (mailbox_id, rules, auto_reply) VALUES ($1, $2, $3)
			ON CONFLICT (mailbox_id) DO UPDATE SET rules = EXCLUDED.rules, auto_reply = EXCLUDED.auto_reply, updated_at = now()`, m.ID, rawRules, rawReply)
		if err != nil {
			return err
		}
		return s.mail.PutSieve(m.Address, mailpkg.CompileSieve(rules, reply, m.Address, forward))
	})
	if err != nil {
		mailBackendErrs.Inc()
		s.log.Error("installing mail rules failed", "mailbox", m.ID, "err", err)
		writeError(w, http.StatusBadGateway, "rules_not_saved", "The settings could not be installed on the mail server. Nothing was changed; try again.")
		return false
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.rules.update", "mailbox", m.ID, map[string]any{"rules": len(rules), "autoReply": reply.Enabled})
	if _, autoReply := response.(mailpkg.AutoReply); autoReply {
		s.recordAccess(r, p, m, "auto_reply.update", "", nil, map[string]any{"enabled": reply.Enabled})
	} else {
		s.recordAccess(r, p, m, "rules.update", "", nil, map[string]any{"rules": len(rules)})
	}
	writeJSON(w, http.StatusOK, response)
	return true
}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
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
	m, ok := s.managedMailbox(w, r, p)
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
	forward, err := s.forwardPolicy(r.Context(), m.OrgID)
	if err != nil {
		s.internalError(w, r, err)
		return
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
		for _, addr := range rule.Actions.ForwardTo {
			if !forward.Allows(addr) {
				writeError(w, http.StatusUnprocessableEntity, "forwarding_blocked", rule.Name+": your organization does not allow forwarding to "+addr+".")
				return
			}
		}
	}
	before, reply, err := s.loadSieveSettings(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !s.installSieve(w, r, p, m, rules, reply, nonNil(rules)) {
		return
	}
	old, next := forwardDestinations(before), forwardDestinations(rules)
	for addr, rule := range next {
		if _, ok := old[addr]; !ok {
			s.audit(r.Context(), r, p, &m.OrgID, "rule.forwarding.add", "mailbox", m.ID, map[string]any{"address": addr, "rule": rule, "external": forward.External(addr)})
		}
	}
	for addr, rule := range old {
		if _, ok := next[addr]; !ok {
			s.audit(r.Context(), r, p, &m.OrgID, "rule.forwarding.remove", "mailbox", m.ID, map[string]any{"address": addr, "rule": rule, "external": forward.External(addr)})
		}
	}
}

func forwardDestinations(rules []mailpkg.Rule) map[string]string {
	out := map[string]string{}
	for _, rule := range rules {
		for _, addr := range rule.Actions.ForwardTo {
			if _, ok := out[strings.ToLower(addr)]; !ok {
				out[strings.ToLower(addr)] = rule.Name
			}
		}
	}
	return out
}

func (s *Server) autoReplyMailbox(w http.ResponseWriter, r *http.Request, p *principal) (*ownedMailbox, bool) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return nil, false
	}
	if !m.SendAs && !p.canAdminOrg(m.OrgID) {
		writeError(w, http.StatusForbidden, "cannot_send", "Automatic replies are sent as this mailbox's address. Ask an administrator for Send as.")
		return nil, false
	}
	return m, true
}

func (s *Server) getAutoReply(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.autoReplyMailbox(w, r, p)
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
	m, ok := s.autoReplyMailbox(w, r, p)
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
