package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/mailflow"
)

type flowRule struct {
	mailflow.Rule
	UpdatedAt time.Time `json:"updatedAt"`
}

const flowRuleColumns = `id, name, mode, conditions, exceptions, actions, stop, updated_at`

func scanFlowRule(row pgx.Row) (flowRule, error) {
	var r flowRule
	err := row.Scan(&r.ID, &r.Name, &r.Mode, &r.Conditions, &r.Exceptions, &r.Actions, &r.Stop, &r.UpdatedAt)
	return r, err
}

func (s *Server) listFlowRules(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+flowRuleColumns+` FROM mail_flow_rules WHERE org_id = $1 ORDER BY position, created_at`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (flowRule, error) { return scanFlowRule(row) })
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(rules))
}

func (s *Server) decodeFlowRule(w http.ResponseWriter, r *http.Request, orgID string) (*mailflow.Rule, bool) {
	var in mailflow.Rule
	if !decode(w, r, 256<<10, &in) {
		return nil, false
	}
	fail := func(msg string) (*mailflow.Rule, bool) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_rule", msg)
		return nil, false
	}
	if err := in.Validate(); err != nil {
		return fail(err.Error())
	}
	if d := in.Actions.Disclaimer; d != nil && d.HTML != "" {
		d.HTML = mailpkg.SanitizeHTML(d.HTML)
	}
	if len(in.Actions.Approvers) > 0 {
		if s.cfg.RelayPassword == "" {
			return fail("Approval needs AETHER_SIEVE_RELAY_PASSWORD to be set for the API, so approved messages can be sent on.")
		}
		var known []string
		err := s.db.QueryRow(r.Context(), `
			SELECT coalesce(array_agg(lower(u.email)), '{}') FROM organization_members om JOIN users u ON u.id = om.user_id
			WHERE om.org_id = $1 AND u.status = 'active' AND lower(u.email) = ANY($2)`, orgID, in.Actions.Approvers).Scan(&known)
		if err != nil {
			s.internalError(w, r, err)
			return nil, false
		}
		for _, a := range in.Actions.Approvers {
			if !slices.Contains(known, a) {
				return fail(a + " is not an active member of this organization, so they cannot approve messages.")
			}
		}
	}
	var lists []string
	for _, c := range slices.Concat(in.Conditions, in.Exceptions) {
		if c.Scope == "memberOf" {
			lists = append(lists, c.Values...)
		}
	}
	if len(lists) > 0 {
		var known []string
		err := s.db.QueryRow(r.Context(), `SELECT coalesce(array_agg(lower(address)), '{}') FROM aliases WHERE org_id = $1 AND kind = 'list' AND lower(address) = ANY($2)`, orgID, lists).Scan(&known)
		if err != nil {
			s.internalError(w, r, err)
			return nil, false
		}
		for _, l := range lists {
			if !slices.Contains(known, l) {
				return fail(l + " is not a distribution list in this organization.")
			}
		}
	}
	return &in, true
}

func (s *Server) createFlowRule(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	in, ok := s.decodeFlowRule(w, r, orgID)
	if !ok {
		return
	}
	var count int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM mail_flow_rules WHERE org_id = $1`, orgID).Scan(&count); err != nil {
		s.internalError(w, r, err)
		return
	}
	if count >= 200 {
		writeError(w, http.StatusUnprocessableEntity, "too_many_rules", "An organization can have up to 200 mail flow rules.")
		return
	}
	rule, err := scanFlowRule(s.db.QueryRow(r.Context(), `
		INSERT INTO mail_flow_rules (org_id, position, name, mode, conditions, exceptions, actions, stop)
		VALUES ($1, (SELECT coalesce(max(position) + 1, 0) FROM mail_flow_rules WHERE org_id = $1), $2, $3, $4, $5, $6, $7)
		RETURNING `+flowRuleColumns, orgID, in.Name, in.Mode, in.Conditions, in.Exceptions, in.Actions, in.Stop))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "mail_flow_rule.create", "mail_flow_rule", rule.ID, map[string]any{"after": rule.Rule})
	writeJSON(w, http.StatusCreated, rule)
}

func (s *Server) updateFlowRule(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	ruleID, ok := pathID(w, r, "ruleId")
	if !ok {
		return
	}
	in, ok := s.decodeFlowRule(w, r, orgID)
	if !ok {
		return
	}
	var before, after flowRule
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		var err error
		if before, err = scanFlowRule(tx.QueryRow(r.Context(), `SELECT `+flowRuleColumns+` FROM mail_flow_rules WHERE id = $1 AND org_id = $2 FOR UPDATE`, ruleID, orgID)); err != nil {
			return err
		}
		after, err = scanFlowRule(tx.QueryRow(r.Context(), `
			UPDATE mail_flow_rules SET name = $2, mode = $3, conditions = $4, exceptions = $5, actions = $6, stop = $7, updated_at = now()
			WHERE id = $1 RETURNING `+flowRuleColumns, ruleID, in.Name, in.Mode, in.Conditions, in.Exceptions, in.Actions, in.Stop))
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
	s.audit(r.Context(), r, p, &orgID, "mail_flow_rule.update", "mail_flow_rule", ruleID, map[string]any{"before": before.Rule, "after": after.Rule})
	writeJSON(w, http.StatusOK, after)
}

func (s *Server) deleteFlowRule(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	ruleID, ok := pathID(w, r, "ruleId")
	if !ok {
		return
	}
	before, err := scanFlowRule(s.db.QueryRow(r.Context(), `DELETE FROM mail_flow_rules WHERE id = $1 AND org_id = $2 RETURNING `+flowRuleColumns, ruleID, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "mail_flow_rule.delete", "mail_flow_rule", ruleID, map[string]any{"before": before.Rule})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) orderFlowRules(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, 64<<10, &in) {
		return
	}
	var before []string
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT id::text FROM mail_flow_rules WHERE org_id = $1 ORDER BY position, created_at FOR UPDATE`, orgID)
		if err != nil {
			return err
		}
		if before, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		sorted := slices.Sorted(slices.Values(in.IDs))
		if len(in.IDs) != len(before) || !slices.Equal(slices.Compact(sorted), slices.Sorted(slices.Values(before))) {
			return errInvalidOrder
		}
		_, err = tx.Exec(r.Context(), `UPDATE mail_flow_rules r SET position = o.position - 1 FROM unnest($2::uuid[]) WITH ORDINALITY AS o(id, position) WHERE r.id = o.id AND r.org_id = $1`, orgID, in.IDs)
		return err
	})
	if errors.Is(err, errInvalidOrder) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_order", "List every rule of this organization exactly once.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "mail_flow_rule.reorder", "organization", orgID, map[string]any{"before": before, "after": in.IDs})
	w.WriteHeader(http.StatusNoContent)
}

var errInvalidOrder = errors.New("invalid order")

type flowMatch struct {
	ID         int64     `json:"id"`
	RuleID     *string   `json:"ruleId"`
	RuleName   string    `json:"ruleName"`
	Test       bool      `json:"test"`
	Direction  string    `json:"direction"`
	Sender     string    `json:"sender"`
	Recipients []string  `json:"recipients"`
	Subject    string    `json:"subject"`
	Actions    []string  `json:"actions"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (s *Server) listFlowMatches(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT id, rule_id, rule_name, test, direction, sender, recipients, subject, actions, created_at
		FROM mail_flow_matches WHERE org_id = $1 ORDER BY created_at DESC, id DESC LIMIT 200`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	matches, err := pgx.CollectRows(rows, pgx.RowToStructByPos[flowMatch])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(matches))
}

type heldMessage struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"orgId"`
	RuleName   string    `json:"ruleName"`
	Approvers  []string  `json:"approvers"`
	Sender     string    `json:"sender"`
	Recipients []string  `json:"recipients"`
	Subject    string    `json:"subject"`
	Preview    string    `json:"preview"`
	CreatedAt  time.Time `json:"createdAt"`
}

const heldColumns = `id, org_id, rule_name, approvers, sender, recipients, subject, preview, created_at`

func (s *Server) listHeld(w http.ResponseWriter, r *http.Request, where string, args ...any) {
	rows, err := s.db.Query(r.Context(), `SELECT `+heldColumns+` FROM mail_flow_held WHERE status = 'pending' AND `+where+` ORDER BY created_at LIMIT 500`, args...)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	held, err := pgx.CollectRows(rows, pgx.RowToStructByPos[heldMessage])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(held))
}

func (s *Server) listOrgHeld(w http.ResponseWriter, r *http.Request, p *principal) {
	if orgID, ok := s.orgFromPath(w, r, p, true); ok {
		s.listHeld(w, r, `org_id = $1`, orgID)
	}
}

func (s *Server) myHeld(w http.ResponseWriter, r *http.Request, p *principal) {
	s.listHeld(w, r, `approvers @> ARRAY[lower($1)] AND org_id IN (SELECT org_id FROM organization_members WHERE user_id = $2)`, p.User.Email, p.User.ID)
}

func (s *Server) approveHeld(w http.ResponseWriter, r *http.Request, p *principal) {
	s.decideHeld(w, r, p, true)
}

func (s *Server) rejectHeld(w http.ResponseWriter, r *http.Request, p *principal) {
	s.decideHeld(w, r, p, false)
}

func (s *Server) decideHeld(w http.ResponseWriter, r *http.Request, p *principal, approve bool) {
	id, ok := pathID(w, r, "heldId")
	if !ok {
		return
	}
	var orgID, sender, ruleName string
	var approvers, recipients []string
	var raw []byte
	var requireTLS bool
	failed := false
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `SELECT org_id, rule_name, approvers, sender, recipients, raw, require_tls FROM mail_flow_held WHERE id = $1 AND status = 'pending' FOR UPDATE`, id).
			Scan(&orgID, &ruleName, &approvers, &sender, &recipients, &raw, &requireTLS)
		if err != nil {
			return err
		}
		if !p.canAdminOrg(orgID) && (p.apiKey != nil || p.orgRole(orgID) == "" || !slices.Contains(approvers, strings.ToLower(p.User.Email))) {
			return pgx.ErrNoRows
		}
		status := "rejected"
		if approve {
			status = "approved"
			if requireTLS {
				raw = append([]byte(tlsHeader+": yes\r\n"), raw...)
			}
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			if err := s.relayHeld(ctx, sender, recipients, raw); err != nil {
				failed = true
				return err
			}
		}
		var decidedBy *string
		if p.apiKey == nil {
			decidedBy = &p.User.ID
		}
		_, err = tx.Exec(r.Context(), `UPDATE mail_flow_held SET status = $2, decided_by = $3, decided_at = now(), raw = '' WHERE id = $1`, id, status, decidedBy)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if failed {
		mailBackendErrs.Inc()
		s.log.Error("sending an approved message failed", "held", id, "err", err)
		writeError(w, http.StatusBadGateway, "send_failed", "The message could not be handed to the mail server. It is still waiting; try again.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	action := "mail_flow.held.reject"
	if approve {
		action = "mail_flow.held.approve"
	}
	s.audit(r.Context(), r, p, &orgID, action, "mail_flow_held", id, map[string]any{"rule": ruleName, "sender": sender, "recipients": recipients})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) relayHeld(ctx context.Context, sender string, recipients []string, raw []byte) error {
	done := make(chan error, 1)
	go func() { done <- s.mail.Relay(s.cfg.RelayAddr, s.cfg.RelayPassword, sender, recipients, raw) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) cleanMailFlow(ctx context.Context) {
	for _, q := range []string{
		`UPDATE mail_flow_held SET status = 'expired', decided_at = now(), raw = '' WHERE status = 'pending' AND created_at < now() - interval '7 days'`,
		`DELETE FROM mail_flow_held WHERE status <> 'pending' AND decided_at < now() - interval '30 days'`,
		`DELETE FROM mail_flow_matches WHERE created_at < now() - interval '30 days'`,
	} {
		if _, err := s.db.Exec(ctx, q); err != nil {
			s.log.Error("cleaning up mail flow records failed", "err", err)
		}
	}
}
