package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/i18n"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

var forwardingModes = []string{"allowed", "domains", "blocked"}

type forwarder struct {
	MailboxID string `json:"mailboxId"`
	Address   string `json:"address"`
	Rule      string `json:"rule"`
	Enabled   bool   `json:"enabled"`
	To        string `json:"to"`
	Allowed   bool   `json:"allowed"`
	live      bool
}

func (s *Server) forwardPolicy(ctx context.Context, orgID string) (mailpkg.ForwardPolicy, error) {
	var p mailpkg.ForwardPolicy
	err := s.db.QueryRow(ctx, `
		SELECT coalesce(f.mode, 'allowed'), coalesce(f.domains, '{}'), coalesce((SELECT array_agg(d.name) FROM domains d WHERE d.org_id = o.id), '{}')
		FROM organizations o LEFT JOIN organization_forwarding f ON f.org_id = o.id WHERE o.id = $1`, orgID).
		Scan(&p.Mode, &p.Domains, &p.OrgDomains)
	return p, err
}

func (s *Server) externalForwarders(ctx context.Context, orgID string, policy mailpkg.ForwardPolicy) ([]forwarder, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.id, m.address, m.active AND d.active AND d.verified_at IS NOT NULL, r.rules
		FROM mailbox_rules r JOIN mailboxes m ON m.id = r.mailbox_id JOIN domains d ON d.id = m.domain_id
		WHERE m.org_id = $1 AND jsonb_path_exists(r.rules, '$[*].actions.forwardTo[*]') ORDER BY m.address`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []forwarder{}
	for rows.Next() {
		var f forwarder
		var raw []byte
		if err := rows.Scan(&f.MailboxID, &f.Address, &f.live, &raw); err != nil {
			return nil, err
		}
		var rules []mailpkg.Rule
		if err := json.Unmarshal(raw, &rules); err != nil {
			return nil, err
		}
		for _, rule := range rules {
			for _, to := range rule.Actions.ForwardTo {
				if policy.External(to) {
					f.Rule, f.Enabled, f.To, f.Allowed = rule.Name, rule.Enabled, to, policy.Allows(to)
					out = append(out, f)
				}
			}
		}
	}
	return out, rows.Err()
}

func (s *Server) writeForwarding(w http.ResponseWriter, r *http.Request, orgID string, policy mailpkg.ForwardPolicy) {
	forwarders, err := s.externalForwarders(r.Context(), orgID, policy)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": policy.Mode, "domains": policy.Domains, "forwarders": forwarders})
}

func (s *Server) getForwarding(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	policy, err := s.forwardPolicy(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeForwarding(w, r, orgID, policy)
}

func (s *Server) saveForwarding(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		Mode    string   `json:"mode"`
		Domains []string `json:"domains"`
	}
	if !decode(w, r, 32<<10, &in) {
		return
	}
	if !slices.Contains(forwardingModes, in.Mode) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_forwarding", "Choose allowed, domains or blocked.")
		return
	}
	domains := []string{}
	for _, d := range in.Domains {
		d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "@")
		if !domainPattern.MatchString(d) || len(d) > 253 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_forwarding", d+" is not a valid domain name.")
			return
		}
		if !slices.Contains(domains, d) {
			domains = append(domains, d)
		}
	}
	switch {
	case len(domains) > 100:
		writeError(w, http.StatusUnprocessableEntity, "invalid_forwarding", "List up to 100 domains.")
		return
	case in.Mode == "domains" && len(domains) == 0:
		writeError(w, http.StatusUnprocessableEntity, "invalid_forwarding", "Add at least one domain that mail can be forwarded to.")
		return
	}
	before, err := s.forwardPolicy(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO organization_forwarding (org_id, mode, domains) VALUES ($1, $2, $3)
		ON CONFLICT (org_id) DO UPDATE SET mode = excluded.mode, domains = excluded.domains, updated_at = now()`, orgID, in.Mode, domains)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	policy := before
	policy.Mode, policy.Domains = in.Mode, domains
	if before.Mode != policy.Mode || !slices.Equal(before.Domains, policy.Domains) {
		s.audit(r.Context(), r, p, &orgID, "forwarding.policy.update", "organization", orgID, map[string]any{"mode": policy.Mode, "domains": policy.Domains})
	}
	forwarders, err := s.externalForwarders(r.Context(), orgID, policy)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var failed []string
	installed := map[string]bool{}
	for _, f := range forwarders {
		if !f.live || installed[f.MailboxID] {
			continue
		}
		installed[f.MailboxID] = true
		if err := s.reinstallSieve(r.Context(), orgID, f.MailboxID, f.Address, policy); err != nil {
			mailBackendErrs.Inc()
			s.log.Error("reinstalling mail rules after a forwarding policy change failed", "mailbox", f.MailboxID, "err", err)
			failed = append(failed, f.Address)
		}
	}
	if len(failed) > 0 {
		writeError(w, http.StatusBadGateway, "rules_not_updated", "The policy is saved, but the rules of "+strings.Join(failed, ", ")+" could not be updated on the mail server. The mail server still refuses forwarding the policy blocks. Save again to retry.")
		return
	}
	s.writeForwarding(w, r, orgID, policy)
}

func (s *Server) reinstallSieve(ctx context.Context, orgID, mailboxID, address string, policy mailpkg.ForwardPolicy) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM mailbox_rules WHERE mailbox_id = $1 FOR UPDATE`, mailboxID); err != nil {
			return err
		}
		rules, reply, err := s.loadSieveSettings(ctx, mailboxID)
		if err != nil {
			return err
		}
		catalog, err := s.labelRefs(ctx, mailboxID)
		if err != nil {
			return err
		}
		defaultSubject := i18n.T(s.mailboxLocale(ctx, mailboxID, orgID), "email.autoReply.defaultSubject")
		return s.mail.PutSieve(address, mailpkg.CompileSieve(mailpkg.WithLabelColors(rules, catalog), reply, address, policy, defaultSubject))
	})
}
