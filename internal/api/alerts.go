package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	minAlertCooldownSeconds = 60
	maxAlertCooldownSeconds = 604800
	maxAlertRecipients      = 50
)

var alertRuleKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

type alertRule struct {
	Key             string     `json:"key"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	Category        string     `json:"category"`
	Scope           string     `json:"scope"`
	Severity        string     `json:"severity"`
	Enabled         bool       `json:"enabled"`
	Threshold       float64    `json:"threshold"`
	Unit            string     `json:"unit"`
	Comparator      string     `json:"comparator"`
	CooldownSeconds int        `json:"cooldownSeconds"`
	NotifyEmail     bool       `json:"notifyEmail"`
	NotifyWebhook   bool       `json:"notifyWebhook"`
	EmailRecipients []string   `json:"emailRecipients"`
	LastEvaluatedAt *time.Time `json:"lastEvaluatedAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (r alertRule) breached(value float64) bool {
	if r.Comparator == "lte" {
		return value <= r.Threshold
	}
	return value >= r.Threshold
}

func (r alertRule) cooldown() time.Duration {
	return time.Duration(r.CooldownSeconds) * time.Second
}

type alertItem struct {
	ID                  string         `json:"id"`
	RuleKey             string         `json:"ruleKey"`
	RuleName            string         `json:"ruleName"`
	Category            string         `json:"category"`
	Scope               string         `json:"scope"`
	Fingerprint         string         `json:"fingerprint"`
	OrgID               *string        `json:"orgId"`
	TargetType          string         `json:"targetType"`
	TargetID            string         `json:"targetId"`
	TargetLabel         string         `json:"targetLabel"`
	Severity            string         `json:"severity"`
	Status              string         `json:"status"`
	Title               string         `json:"title"`
	Message             string         `json:"message"`
	CurrentValue        *float64       `json:"currentValue"`
	ThresholdValue      *float64       `json:"thresholdValue"`
	Unit                string         `json:"unit"`
	Details             map[string]any `json:"details"`
	Occurrences         int            `json:"occurrences"`
	FiredAt             time.Time      `json:"firedAt"`
	LastSeenAt          time.Time      `json:"lastSeenAt"`
	LastNotifiedAt      *time.Time     `json:"lastNotifiedAt"`
	AcknowledgedAt      *time.Time     `json:"acknowledgedAt"`
	AcknowledgedByEmail *string        `json:"acknowledgedByEmail"`
	ResolvedAt          *time.Time     `json:"resolvedAt"`
	ResolvedByEmail     *string        `json:"resolvedByEmail"`
}

type alertSummary struct {
	Firing       int64 `json:"firing"`
	Acknowledged int64 `json:"acknowledged"`
	Resolved     int64 `json:"resolved"`
}

type alertListResponse struct {
	Alerts  []alertItem  `json:"alerts"`
	Summary alertSummary `json:"summary"`
}

type alertRuleUpdateInput struct {
	Enabled         *bool     `json:"enabled"`
	Severity        *string   `json:"severity"`
	Threshold       *float64  `json:"threshold"`
	CooldownSeconds *int      `json:"cooldownSeconds"`
	NotifyEmail     *bool     `json:"notifyEmail"`
	NotifyWebhook   *bool     `json:"notifyWebhook"`
	EmailRecipients *[]string `json:"emailRecipients"`
}

type alertTestInput struct {
	RuleKey         string   `json:"ruleKey"`
	NotifyEmail     *bool    `json:"notifyEmail"`
	NotifyWebhook   *bool    `json:"notifyWebhook"`
	EmailRecipients []string `json:"emailRecipients"`
}

type alertTestResult struct {
	EmailSent       bool     `json:"emailSent"`
	EmailRecipients []string `json:"emailRecipients"`
	EmailError      *string  `json:"emailError"`
	WebhooksQueued  int64    `json:"webhooksQueued"`
}

const alertRuleColumns = `key, name, description, category, scope, severity, enabled, threshold, unit, comparator, cooldown_seconds, notify_email, notify_webhook, email_recipients, last_evaluated_at, updated_at`

const alertSelectQuery = `
	SELECT a.id, a.rule_key, r.name, r.category, r.scope, a.fingerprint, a.org_id, a.target_type, a.target_id, a.target_label,
	       a.severity, a.status, a.title, a.message, a.current_value, a.threshold_value, a.unit, a.details, a.occurrences,
	       a.fired_at, a.last_seen_at, a.last_notified_at, a.acknowledged_at, ack.email, a.resolved_at, res.email
	FROM alerts a
	JOIN alert_rules r ON r.key = a.rule_key
	LEFT JOIN users ack ON ack.id = a.acknowledged_by
	LEFT JOIN users res ON res.id = a.resolved_by`

func scanAlertRule(row pgx.Row) (alertRule, error) {
	var r alertRule
	err := row.Scan(&r.Key, &r.Name, &r.Description, &r.Category, &r.Scope, &r.Severity, &r.Enabled, &r.Threshold, &r.Unit, &r.Comparator, &r.CooldownSeconds, &r.NotifyEmail, &r.NotifyWebhook, &r.EmailRecipients, &r.LastEvaluatedAt, &r.UpdatedAt)
	if r.EmailRecipients == nil {
		r.EmailRecipients = []string{}
	}
	return r, err
}

func scanAlertItem(row pgx.Row) (alertItem, error) {
	var a alertItem
	var rawDetails []byte
	err := row.Scan(&a.ID, &a.RuleKey, &a.RuleName, &a.Category, &a.Scope, &a.Fingerprint, &a.OrgID, &a.TargetType, &a.TargetID, &a.TargetLabel,
		&a.Severity, &a.Status, &a.Title, &a.Message, &a.CurrentValue, &a.ThresholdValue, &a.Unit, &rawDetails, &a.Occurrences,
		&a.FiredAt, &a.LastSeenAt, &a.LastNotifiedAt, &a.AcknowledgedAt, &a.AcknowledgedByEmail, &a.ResolvedAt, &a.ResolvedByEmail)
	if err != nil {
		return a, err
	}
	a.Details = map[string]any{}
	if len(rawDetails) > 0 {
		json.Unmarshal(rawDetails, &a.Details)
	}
	return a, nil
}

func normalizeAlertRecipients(in []string) ([]string, error) {
	if len(in) > maxAlertRecipients {
		return nil, errors.New("Enter at most 50 recipient email addresses.")
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		trimmed := strings.ToLower(strings.TrimSpace(raw))
		if trimmed == "" {
			continue
		}
		parsed, err := mail.ParseAddress(trimmed)
		if err != nil || parsed.Name != "" || parsed.Address != trimmed || strings.ContainsAny(trimmed, "\r\n") {
			return nil, errors.New(raw + " is not a valid email address.")
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	slices.Sort(out)
	return out, nil
}

func validateAlertRuleUpdate(current alertRule, in alertRuleUpdateInput) (alertRule, string, string) {
	next := current
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	if in.Severity != nil {
		sev := strings.ToLower(strings.TrimSpace(*in.Severity))
		if sev != "info" && sev != "warning" && sev != "critical" {
			return next, "invalid_severity", "Severity must be info, warning, or critical."
		}
		next.Severity = sev
	}
	if in.Threshold != nil {
		v := *in.Threshold
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || (current.Unit == "percent" && v > 100) {
			if current.Unit == "percent" {
				return next, "invalid_threshold", "Threshold must be between 0 and 100."
			}
			return next, "invalid_threshold", "Threshold must be 0 or greater."
		}
		next.Threshold = v
	}
	if in.CooldownSeconds != nil {
		c := *in.CooldownSeconds
		if c < minAlertCooldownSeconds || c > maxAlertCooldownSeconds {
			return next, "invalid_cooldown", "Cooldown must be between 60 and 604800 seconds."
		}
		next.CooldownSeconds = c
	}
	if in.NotifyEmail != nil {
		next.NotifyEmail = *in.NotifyEmail
	}
	if in.NotifyWebhook != nil {
		next.NotifyWebhook = *in.NotifyWebhook
	}
	if in.EmailRecipients != nil {
		norm, err := normalizeAlertRecipients(*in.EmailRecipients)
		if err != nil {
			return next, "invalid_recipients", err.Error()
		}
		next.EmailRecipients = norm
	}
	return next, "", ""
}

func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+alertRuleColumns+` FROM alert_rules ORDER BY category, key`)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()
	out := []alertRule{}
	for rows.Next() {
		rule, err := scanAlertRule(rows)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out = append(out, rule)
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateAlertRule(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	key := strings.ToLower(strings.TrimSpace(r.PathValue("ruleKey")))
	if !alertRuleKeyPattern.MatchString(key) {
		notFound(w)
		return
	}
	var in alertRuleUpdateInput
	if !decode(w, r, 16<<10, &in) {
		return
	}
	current, err := scanAlertRule(s.db.QueryRow(r.Context(), `SELECT `+alertRuleColumns+` FROM alert_rules WHERE key = $1`, key))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	next, code, msg := validateAlertRuleUpdate(current, in)
	if code != "" {
		writeError(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	updated, err := scanAlertRule(s.db.QueryRow(r.Context(), `
		UPDATE alert_rules
		SET enabled = $2, severity = $3, threshold = $4, cooldown_seconds = $5,
		    notify_email = $6, notify_webhook = $7, email_recipients = $8, updated_at = now()
		WHERE key = $1
		RETURNING `+alertRuleColumns,
		key, next.Enabled, next.Severity, next.Threshold, next.CooldownSeconds,
		next.NotifyEmail, next.NotifyWebhook, next.EmailRecipients))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, nil, "alert_rule.update", "alert_rule", key, map[string]any{
		"enabled":         updated.Enabled,
		"severity":        updated.Severity,
		"threshold":       updated.Threshold,
		"cooldownSeconds": updated.CooldownSeconds,
		"notifyEmail":     updated.NotifyEmail,
		"notifyWebhook":   updated.NotifyWebhook,
	})
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	q := r.URL.Query()
	statusFilter := strings.ToLower(strings.TrimSpace(q.Get("status")))
	if statusFilter != "" && !slices.Contains([]string{"active", "firing", "acknowledged", "resolved"}, statusFilter) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_status", "Status must be active, firing, acknowledged, or resolved.")
		return
	}
	severityFilter := strings.ToLower(strings.TrimSpace(q.Get("severity")))
	if severityFilter != "" && !slices.Contains([]string{"info", "warning", "critical"}, severityFilter) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_severity", "Severity must be info, warning, or critical.")
		return
	}
	ruleFilter := strings.ToLower(strings.TrimSpace(q.Get("rule")))
	if ruleFilter != "" && !alertRuleKeyPattern.MatchString(ruleFilter) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_rule", "Rule key is not valid.")
		return
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_limit", "Limit must be between 1 and 200.")
			return
		}
		limit = n
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_offset", "Offset must be 0 or greater.")
			return
		}
		offset = n
	}

	var conds []string
	var args []any
	addArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	switch statusFilter {
	case "active":
		conds = append(conds, `a.status IN ('firing', 'acknowledged')`)
	case "firing", "acknowledged", "resolved":
		conds = append(conds, `a.status = `+addArg(statusFilter))
	}
	if severityFilter != "" {
		conds = append(conds, `a.severity = `+addArg(severityFilter))
	}
	if ruleFilter != "" {
		conds = append(conds, `a.rule_key = `+addArg(ruleFilter))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	limitArg := addArg(limit)
	offsetArg := addArg(offset)
	rows, err := s.db.Query(r.Context(), alertSelectQuery+where+` ORDER BY CASE a.status WHEN 'firing' THEN 0 WHEN 'acknowledged' THEN 1 ELSE 2 END, a.fired_at DESC LIMIT `+limitArg+` OFFSET `+offsetArg, args...)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()
	items := []alertItem{}
	for rows.Next() {
		item, err := scanAlertItem(rows)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	var summary alertSummary
	err = s.db.QueryRow(r.Context(), `
		SELECT count(*) FILTER (WHERE status = 'firing'),
		       count(*) FILTER (WHERE status = 'acknowledged'),
		       count(*) FILTER (WHERE status = 'resolved')
		FROM alerts`).Scan(&summary.Firing, &summary.Acknowledged, &summary.Resolved)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, alertListResponse{Alerts: items, Summary: summary})
}

func (s *Server) getAlert(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	id, ok := pathID(w, r, "alertId")
	if !ok {
		return
	}
	item, err := scanAlertItem(s.db.QueryRow(r.Context(), alertSelectQuery+` WHERE a.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) acknowledgeAlert(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	id, ok := pathID(w, r, "alertId")
	if !ok {
		return
	}
	var status, ruleKey string
	err := s.db.QueryRow(r.Context(), `SELECT status, rule_key FROM alerts WHERE id = $1`, id).Scan(&status, &ruleKey)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if status == "resolved" {
		writeError(w, http.StatusConflict, "alert_already_resolved", "This alert is already resolved.")
		return
	}
	if status == "firing" {
		if _, err := s.db.Exec(r.Context(), `UPDATE alerts SET status = 'acknowledged', acknowledged_at = now(), acknowledged_by = $2 WHERE id = $1 AND status = 'firing'`, id, p.User.ID); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	item, err := scanAlertItem(s.db.QueryRow(r.Context(), alertSelectQuery+` WHERE a.id = $1`, id))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if status == "firing" {
		s.audit(r.Context(), r, p, item.OrgID, "alert.acknowledge", "alert", item.ID, map[string]any{"ruleKey": item.RuleKey, "targetLabel": item.TargetLabel})
		if rule, err := scanAlertRule(s.db.QueryRow(r.Context(), `SELECT `+alertRuleColumns+` FROM alert_rules WHERE key = $1`, ruleKey)); err == nil && rule.NotifyWebhook {
			s.enqueueAlertWebhooks(r.Context(), item.OrgID, "alert.acknowledged", buildAlertWebhookPayload("alert.acknowledged", item, s.now()))
		}
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) resolveAlert(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	id, ok := pathID(w, r, "alertId")
	if !ok {
		return
	}
	var status, ruleKey string
	err := s.db.QueryRow(r.Context(), `SELECT status, rule_key FROM alerts WHERE id = $1`, id).Scan(&status, &ruleKey)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if status == "resolved" {
		writeError(w, http.StatusConflict, "alert_already_resolved", "This alert is already resolved.")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE alerts SET status = 'resolved', resolved_at = now(), resolved_by = $2 WHERE id = $1 AND status <> 'resolved'`, id, p.User.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	item, err := scanAlertItem(s.db.QueryRow(r.Context(), alertSelectQuery+` WHERE a.id = $1`, id))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.refreshAlertMetrics(r.Context())
	s.audit(r.Context(), r, p, item.OrgID, "alert.resolve", "alert", item.ID, map[string]any{"ruleKey": item.RuleKey, "targetLabel": item.TargetLabel})
	if rule, err := scanAlertRule(s.db.QueryRow(r.Context(), `SELECT `+alertRuleColumns+` FROM alert_rules WHERE key = $1`, ruleKey)); err == nil && rule.NotifyWebhook {
		s.enqueueAlertWebhooks(r.Context(), item.OrgID, "alert.resolved", buildAlertWebhookPayload("alert.resolved", item, s.now()))
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) testAlert(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	var in alertTestInput
	if r.ContentLength != 0 && !decode(w, r, 16<<10, &in) {
		return
	}
	rule := alertRule{
		Key:           "test",
		Name:          "Test alert",
		Category:      "mail",
		Scope:         "platform",
		Severity:      "info",
		NotifyEmail:   true,
		NotifyWebhook: true,
	}
	if key := strings.ToLower(strings.TrimSpace(in.RuleKey)); key != "" {
		if !alertRuleKeyPattern.MatchString(key) {
			notFound(w)
			return
		}
		loaded, err := scanAlertRule(s.db.QueryRow(r.Context(), `SELECT `+alertRuleColumns+` FROM alert_rules WHERE key = $1`, key))
		if errors.Is(err, pgx.ErrNoRows) {
			notFound(w)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		rule = loaded
	}
	if in.NotifyEmail != nil {
		rule.NotifyEmail = *in.NotifyEmail
	}
	if in.NotifyWebhook != nil {
		rule.NotifyWebhook = *in.NotifyWebhook
	}
	if len(in.EmailRecipients) > 0 {
		norm, err := normalizeAlertRecipients(in.EmailRecipients)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_recipients", err.Error())
			return
		}
		rule.EmailRecipients = norm
	}

	now := s.now()
	sample := alertItem{
		ID:          "test-" + strconv.FormatInt(now.Unix(), 10),
		RuleKey:     rule.Key,
		RuleName:    rule.Name,
		Category:    rule.Category,
		Scope:       rule.Scope,
		Severity:    rule.Severity,
		Status:      "firing",
		TargetType:  "system",
		TargetID:    s.cfg.MailHostname,
		TargetLabel: s.cfg.MailHostname,
		Title:       "Test alert notification",
		Message:     "This is a test alert triggered by " + p.User.Email + " to verify administrator alert delivery.",
		Details:     map[string]any{"triggeredBy": p.User.Email, "test": true},
		FiredAt:     now,
		LastSeenAt:  now,
	}

	res := alertTestResult{EmailRecipients: []string{}}
	if rule.NotifyEmail {
		recipients, err := s.resolveAlertRecipients(r.Context(), rule, nil)
		if err != nil {
			msg := err.Error()
			res.EmailError = &msg
		} else if len(recipients) == 0 {
			msg := "No active administrator email recipients found."
			res.EmailError = &msg
		} else {
			res.EmailRecipients = recipients
			subject, text := formatAlertEmail("alert.test", sample, s.publicURLString())
			if err := s.sendAlertEmail(r.Context(), recipients, subject, text); err != nil {
				msg := err.Error()
				res.EmailError = &msg
			} else {
				res.EmailSent = true
			}
		}
	}
	if rule.NotifyWebhook {
		payload := buildAlertWebhookPayload("alert.test", sample, now)
		res.WebhooksQueued = s.enqueueAlertWebhooks(r.Context(), nil, "alert.test", payload)
	}
	s.audit(r.Context(), r, p, nil, "alert.test", "alert_rule", rule.Key, map[string]any{
		"emailSent":      res.EmailSent,
		"webhooksQueued": res.WebhooksQueued,
	})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) resolveAlertRecipients(ctx context.Context, rule alertRule, orgID *string) ([]string, error) {
	if len(rule.EmailRecipients) > 0 {
		return normalizeAlertRecipients(rule.EmailRecipients)
	}
	var rows pgx.Rows
	var err error
	if orgID != nil && *orgID != "" {
		rows, err = s.db.Query(ctx, `
			SELECT email FROM users WHERE is_platform_admin AND status = 'active'
			UNION
			SELECT u.email FROM organization_members om JOIN users u ON u.id = om.user_id
			WHERE om.org_id = $1 AND om.role IN ('owner', 'admin') AND u.status = 'active'`, *orgID)
	} else {
		rows, err = s.db.Query(ctx, `SELECT email FROM users WHERE is_platform_admin AND status = 'active'`)
	}
	if err != nil {
		return nil, err
	}
	emails, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	return normalizeAlertRecipients(emails)
}

func (s *Server) publicURLString() string {
	if s.cfg.PublicURL == nil {
		return ""
	}
	return s.cfg.PublicURL.String()
}

func (s *Server) resolveAlertSender(ctx context.Context) (mailpkg.Address, error) {
	if s.cfg.AlertFrom == "" {
		return mailpkg.Address{}, errors.New("alert email is not configured: set AETHER_ALERT_FROM to an active mailbox")
	}
	var addr, name string
	err := s.db.QueryRow(ctx, `
		SELECT m.address, m.display_name FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.address = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL`, s.cfg.AlertFrom).Scan(&addr, &name)
	if err != nil {
		return mailpkg.Address{}, errors.New("AETHER_ALERT_FROM is not an active mailbox on a verified domain")
	}
	if name == "" {
		name = "Aether Alerts"
	}
	return mailpkg.Address{Name: name, Address: addr}, nil
}

func (s *Server) sendAlertEmail(ctx context.Context, recipients []string, subject, textBody string) error {
	if s.mail == nil && (s.alerts == nil || s.alerts.sendMail == nil) {
		return errors.New("mail client is not configured")
	}
	sender, err := s.resolveAlertSender(ctx)
	if err != nil {
		return err
	}
	to := make([]mailpkg.Address, len(recipients))
	for i, addr := range recipients {
		to[i] = mailpkg.Address{Address: addr}
	}
	out := &mailpkg.Outgoing{
		From:    sender,
		To:      to,
		Subject: subject,
		Text:    textBody,
		Date:    s.now(),
	}
	raw, _, err := out.Build(s.cfg.MailHostname)
	if err != nil {
		return err
	}
	if s.alerts != nil && s.alerts.sendMail != nil {
		return s.alerts.sendMail(sender, recipients, raw)
	}
	return s.mail.Send(sender.Address, sender.Address, recipients, raw)
}

func formatAlertEmail(event string, item alertItem, publicURL string) (subject, body string) {
	sev := strings.ToUpper(item.Severity)
	prefix := "[Aether Alert][" + sev + "] "
	statusLine := strings.ToUpper(item.Status)
	switch event {
	case "alert.resolved":
		prefix = "[Aether Resolved] "
		statusLine = "RESOLVED"
	case "alert.test":
		prefix = "[Aether Alert][TEST] "
		statusLine = "TEST"
	}
	subject = prefix + item.Title
	if item.TargetLabel != "" && !strings.Contains(item.Title, item.TargetLabel) {
		subject += " (" + item.TargetLabel + ")"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Status: %s\n", statusLine)
	fmt.Fprintf(&b, "Severity: %s\n", sev)
	fmt.Fprintf(&b, "Rule: %s (%s)\n", item.RuleName, item.RuleKey)
	if item.TargetLabel != "" {
		fmt.Fprintf(&b, "Target: %s\n", item.TargetLabel)
	}
	fmt.Fprintf(&b, "\n%s\n", item.Message)
	if item.CurrentValue != nil && item.ThresholdValue != nil {
		fmt.Fprintf(&b, "\nObserved: %s %s\nThreshold: %s %s\n", formatFloat(*item.CurrentValue), item.Unit, formatFloat(*item.ThresholdValue), item.Unit)
	}
	fmt.Fprintf(&b, "\nFired at: %s\n", item.FiredAt.UTC().Format(time.RFC3339))
	if item.ResolvedAt != nil {
		fmt.Fprintf(&b, "Resolved at: %s\n", item.ResolvedAt.UTC().Format(time.RFC3339))
	}
	if publicURL != "" {
		fmt.Fprintf(&b, "\nConsole: %s\n", strings.TrimRight(publicURL, "/"))
	}
	return subject, b.String()
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

func buildAlertWebhookPayload(event string, item alertItem, at time.Time) map[string]any {
	return map[string]any{
		"id":             item.ID,
		"type":           event,
		"createdAt":      at.UTC(),
		"organizationId": item.OrgID,
		"target": map[string]string{
			"type":  item.TargetType,
			"id":    item.TargetID,
			"label": item.TargetLabel,
		},
		"data": map[string]any{
			"alertId":        item.ID,
			"ruleKey":        item.RuleKey,
			"ruleName":       item.RuleName,
			"category":       item.Category,
			"scope":          item.Scope,
			"severity":       item.Severity,
			"status":         item.Status,
			"title":          item.Title,
			"message":        item.Message,
			"currentValue":   item.CurrentValue,
			"thresholdValue": item.ThresholdValue,
			"unit":           item.Unit,
			"details":        item.Details,
			"occurrences":    item.Occurrences,
			"firedAt":        item.FiredAt.UTC(),
			"lastSeenAt":     item.LastSeenAt.UTC(),
			"acknowledgedAt": item.AcknowledgedAt,
			"resolvedAt":     item.ResolvedAt,
		},
	}
}

func (s *Server) enqueueAlertWebhooks(ctx context.Context, orgID *string, event string, payload map[string]any) int64 {
	if orgID != nil && *orgID == "" {
		orgID = nil
	}
	return s.enqueueWebhooks(ctx, orgID, event, payload)
}

func (s *Server) notifyAlert(ctx context.Context, rule alertRule, item alertItem, event string) bool {
	delivered := false
	if rule.NotifyEmail {
		recipients, err := s.resolveAlertRecipients(ctx, rule, item.OrgID)
		if err != nil {
			s.log.Warn("resolving alert recipients failed", "rule", rule.Key, "err", err)
		} else if len(recipients) > 0 {
			subject, text := formatAlertEmail(event, item, s.publicURLString())
			if err := s.sendAlertEmail(ctx, recipients, subject, text); err != nil {
				s.log.Warn("sending alert email failed", "rule", rule.Key, "event", event, "err", err)
			} else {
				delivered = true
			}
		}
	}
	if rule.NotifyWebhook {
		queued := s.enqueueAlertWebhooks(ctx, item.OrgID, event, buildAlertWebhookPayload(event, item, s.now()))
		if queued > 0 {
			delivered = true
		}
	}
	return delivered
}
