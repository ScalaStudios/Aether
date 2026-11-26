package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	secEventAppPasswordCreated     = "app_password_created"
	secEventExternalForwardingRule = "external_forwarding_rule"
	secEventDelegateAdded          = "delegate_added"
	secEventNewDeviceSignIn        = "new_device_sign_in"
	secEventFailedSignIns          = "failed_sign_ins"

	defaultFailedSignInThreshold = 5
	minFailedSignInThreshold     = 3
	maxFailedSignInThreshold     = 50
	failedSignInWindow           = 15 * time.Minute
	maxSecurityEmailAttempts     = 5
)

var securityEmailRetryDelays = []time.Duration{
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	time.Hour,
}

type orgSecuritySettings struct {
	AppPasswordCreated     bool `json:"appPasswordCreated"`
	ExternalForwardingRule bool `json:"externalForwardingRule"`
	DelegateAdded          bool `json:"delegateAdded"`
	NewDeviceSignIn        bool `json:"newDeviceSignIn"`
	FailedSignIns          bool `json:"failedSignIns"`
	NotifyAdmins           bool `json:"notifyAdmins"`
	FailedSignInThreshold  int  `json:"failedSignInThreshold"`
}

func defaultOrgSecuritySettings() orgSecuritySettings {
	return orgSecuritySettings{
		AppPasswordCreated:     true,
		ExternalForwardingRule: true,
		DelegateAdded:          true,
		NewDeviceSignIn:        true,
		FailedSignIns:          true,
		NotifyAdmins:           false,
		FailedSignInThreshold:  defaultFailedSignInThreshold,
	}
}

func (s orgSecuritySettings) enabled(eventType string) bool {
	switch eventType {
	case secEventAppPasswordCreated:
		return s.AppPasswordCreated
	case secEventExternalForwardingRule:
		return s.ExternalForwardingRule
	case secEventDelegateAdded:
		return s.DelegateAdded
	case secEventNewDeviceSignIn:
		return s.NewDeviceSignIn
	case secEventFailedSignIns:
		return s.FailedSignIns
	default:
		return false
	}
}

type securityNotification struct {
	ID             string         `json:"id"`
	EventType      string         `json:"eventType"`
	Title          string         `json:"title"`
	Summary        string         `json:"summary"`
	MailboxID      *string        `json:"mailboxId"`
	MailboxAddress string         `json:"mailboxAddress"`
	Details        map[string]any `json:"details"`
	ActionURL      string         `json:"actionUrl"`
	CanUndo        bool           `json:"canUndo"`
	UndoLabel      string         `json:"undoLabel"`
	ReadAt         *time.Time     `json:"readAt"`
	UndoneAt       *time.Time     `json:"undoneAt"`
	ReportedAt     *time.Time     `json:"reportedAt"`
	CreatedAt      time.Time      `json:"createdAt"`
}

func undoLabelFor(eventType string) string {
	switch eventType {
	case secEventAppPasswordCreated:
		return "Revoke app password"
	case secEventExternalForwardingRule:
		return "Remove forwarding"
	case secEventDelegateAdded:
		return "Remove delegate"
	case secEventNewDeviceSignIn:
		return "Sign out session"
	default:
		return ""
	}
}

func canUndoNotification(eventType string, undoneAt *time.Time, targetSessionHash, currentSessionHash []byte) bool {
	if undoneAt != nil {
		return false
	}
	switch eventType {
	case secEventAppPasswordCreated, secEventExternalForwardingRule, secEventDelegateAdded:
		return true
	case secEventNewDeviceSignIn:
		return len(targetSessionHash) > 0 && !bytes.Equal(targetSessionHash, currentSessionHash)
	default:
		return false
	}
}

func parseDeviceLabel(ua string) (string, string) {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return "Unknown device", "unknown"
	}
	lower := strings.ToLower(ua)
	browser := ""
	switch {
	case strings.Contains(lower, "edg/") || strings.Contains(lower, "edge/"):
		browser = "Edge"
	case strings.Contains(lower, "opr/") || strings.Contains(lower, "opera"):
		browser = "Opera"
	case strings.Contains(lower, "firefox/") || strings.Contains(lower, "fxios/"):
		browser = "Firefox"
	case strings.Contains(lower, "chrome/") || strings.Contains(lower, "crios/") || strings.Contains(lower, "chromium/"):
		browser = "Chrome"
	case strings.Contains(lower, "safari/"):
		browser = "Safari"
	case strings.HasPrefix(lower, "curl/"):
		browser = "curl"
	}
	osName := ""
	switch {
	case strings.Contains(lower, "iphone"):
		osName = "iPhone"
	case strings.Contains(lower, "ipad"):
		osName = "iPad"
	case strings.Contains(lower, "android"):
		osName = "Android"
	case strings.Contains(lower, "windows"):
		osName = "Windows"
	case strings.Contains(lower, "cros"):
		osName = "ChromeOS"
	case strings.Contains(lower, "macintosh") || strings.Contains(lower, "mac os x"):
		osName = "macOS"
	case strings.Contains(lower, "linux"):
		osName = "Linux"
	}
	switch {
	case browser != "" && osName != "":
		label := browser + " on " + osName
		return label, strings.ToLower(browser + "|" + osName)
	case browser != "":
		return browser, strings.ToLower(browser)
	case osName != "":
		return osName, strings.ToLower(osName)
	default:
		clean := truncate(ua, 80)
		return clean, strings.ToLower(clean)
	}
}

func deviceFingerprint(ua string) (string, string) {
	label, key := parseDeviceLabel(ua)
	sum := sha256.Sum256([]byte(key))
	return label, hex.EncodeToString(sum[:])
}

func (s *Server) clientCountry(r *http.Request) string {
	if r == nil || s.cfg == nil || !s.cfg.TrustProxy {
		return ""
	}
	for _, h := range []string{"CF-IPCountry", "X-Country-Code", "X-GeoIP-Country-Code", "CloudFront-Viewer-Country", "Fastly-Client-Country"} {
		v := strings.ToUpper(strings.TrimSpace(r.Header.Get(h)))
		if len(v) == 2 && v[0] >= 'A' && v[0] <= 'Z' && v[1] >= 'A' && v[1] <= 'Z' && v != "XX" && v != "T1" {
			return v
		}
	}
	return ""
}

func externalDomain(addr string, orgDomains map[string]bool) bool {
	_, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(addr)), "@")
	if !ok || domain == "" {
		return false
	}
	return !orgDomains[domain]
}

func newlyAddedExternalForwards(oldRules, newRules []mailpkg.Rule, orgDomains map[string]bool) ([]string, []string) {
	oldCounts := map[string]int{}
	for _, rule := range oldRules {
		if !rule.Enabled {
			continue
		}
		seenInRule := map[string]bool{}
		for _, addr := range rule.Actions.ForwardTo {
			norm := strings.ToLower(strings.TrimSpace(addr))
			if norm == "" || seenInRule[norm] || !externalDomain(norm, orgDomains) {
				continue
			}
			seenInRule[norm] = true
			oldCounts[norm]++
		}
	}
	newCounts := map[string]int{}
	newByAddr := map[string][]string{}
	for _, rule := range newRules {
		if !rule.Enabled {
			continue
		}
		seenInRule := map[string]bool{}
		for _, addr := range rule.Actions.ForwardTo {
			norm := strings.ToLower(strings.TrimSpace(addr))
			if norm == "" || seenInRule[norm] || !externalDomain(norm, orgDomains) {
				continue
			}
			seenInRule[norm] = true
			newCounts[norm]++
			newByAddr[norm] = append(newByAddr[norm], rule.Name)
		}
	}
	var added []string
	ruleSet := map[string]bool{}
	var ruleNames []string
	for addr, cnt := range newCounts {
		if cnt > oldCounts[addr] {
			added = append(added, addr)
			for _, name := range newByAddr[addr] {
				if !ruleSet[name] {
					ruleSet[name] = true
					ruleNames = append(ruleNames, name)
				}
			}
		}
	}
	slices.Sort(added)
	slices.Sort(ruleNames)
	return added, ruleNames
}

func removeExternalForwards(rules []mailpkg.Rule, targets []string) ([]mailpkg.Rule, bool) {
	drop := map[string]bool{}
	for _, t := range targets {
		norm := strings.ToLower(strings.TrimSpace(t))
		if norm != "" {
			drop[norm] = true
		}
	}
	if len(drop) == 0 {
		return rules, false
	}
	changed := false
	out := make([]mailpkg.Rule, 0, len(rules))
	for _, rule := range rules {
		if len(rule.Actions.ForwardTo) == 0 {
			out = append(out, rule)
			continue
		}
		kept := make([]string, 0, len(rule.Actions.ForwardTo))
		for _, addr := range rule.Actions.ForwardTo {
			if drop[strings.ToLower(strings.TrimSpace(addr))] {
				changed = true
				continue
			}
			kept = append(kept, addr)
		}
		rule.Actions.ForwardTo = kept
		if len(kept) == 0 {
			rule.Actions.KeepCopy = false
		}
		if rule.Actions.MoveTo == "" && !rule.Actions.Delete && !rule.Actions.MarkRead && !rule.Actions.Flag && len(rule.Actions.ForwardTo) == 0 {
			continue
		}
		out = append(out, rule)
	}
	return out, changed
}

func (s *Server) loadOrgSecuritySettings(ctx context.Context, orgID string) (orgSecuritySettings, error) {
	cfg := defaultOrgSecuritySettings()
	err := s.db.QueryRow(ctx, `
		SELECT notify_app_password_created, notify_external_forwarding_rule, notify_delegate_added,
		       notify_new_device_sign_in, notify_failed_sign_ins, notify_admins, failed_sign_in_threshold
		FROM organization_security_settings WHERE org_id = $1`, orgID).
		Scan(&cfg.AppPasswordCreated, &cfg.ExternalForwardingRule, &cfg.DelegateAdded,
			&cfg.NewDeviceSignIn, &cfg.FailedSignIns, &cfg.NotifyAdmins, &cfg.FailedSignInThreshold)
	if errors.Is(err, pgx.ErrNoRows) {
		return cfg, nil
	}
	return cfg, err
}

func (s *Server) getOrgSecurityNotifications(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	cfg, err := s.loadOrgSecuritySettings(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) saveOrgSecurityNotifications(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in orgSecuritySettings
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if in.FailedSignInThreshold == 0 {
		in.FailedSignInThreshold = defaultFailedSignInThreshold
	}
	if in.FailedSignInThreshold < minFailedSignInThreshold || in.FailedSignInThreshold > maxFailedSignInThreshold {
		writeError(w, http.StatusUnprocessableEntity, "invalid_threshold", "Set the failed sign-in threshold between 3 and 50 attempts.")
		return
	}
	_, err := s.db.Exec(r.Context(), `
		INSERT INTO organization_security_settings (
			org_id, notify_app_password_created, notify_external_forwarding_rule, notify_delegate_added,
			notify_new_device_sign_in, notify_failed_sign_ins, notify_admins, failed_sign_in_threshold, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (org_id) DO UPDATE SET
			notify_app_password_created = EXCLUDED.notify_app_password_created,
			notify_external_forwarding_rule = EXCLUDED.notify_external_forwarding_rule,
			notify_delegate_added = EXCLUDED.notify_delegate_added,
			notify_new_device_sign_in = EXCLUDED.notify_new_device_sign_in,
			notify_failed_sign_ins = EXCLUDED.notify_failed_sign_ins,
			notify_admins = EXCLUDED.notify_admins,
			failed_sign_in_threshold = EXCLUDED.failed_sign_in_threshold,
			updated_at = now()`,
		orgID, in.AppPasswordCreated, in.ExternalForwardingRule, in.DelegateAdded,
		in.NewDeviceSignIn, in.FailedSignIns, in.NotifyAdmins, in.FailedSignInThreshold)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "organization.security_notifications.update", "organization", orgID, map[string]any{
		"appPasswordCreated":     in.AppPasswordCreated,
		"externalForwardingRule": in.ExternalForwardingRule,
		"delegateAdded":          in.DelegateAdded,
		"newDeviceSignIn":        in.NewDeviceSignIn,
		"failedSignIns":          in.FailedSignIns,
		"notifyAdmins":           in.NotifyAdmins,
		"failedSignInThreshold":  in.FailedSignInThreshold,
	})
	writeJSON(w, http.StatusOK, in)
}

type orgSecurityEntry struct {
	OrgID    string
	Settings orgSecuritySettings
}

func (s *Server) userOrgSecuritySettings(ctx context.Context, userID string) ([]orgSecurityEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT o.id,
		       coalesce(s.notify_app_password_created, true),
		       coalesce(s.notify_external_forwarding_rule, true),
		       coalesce(s.notify_delegate_added, true),
		       coalesce(s.notify_new_device_sign_in, true),
		       coalesce(s.notify_failed_sign_ins, true),
		       coalesce(s.notify_admins, false),
		       coalesce(s.failed_sign_in_threshold, $2)
		FROM organizations o
		LEFT JOIN organization_security_settings s ON s.org_id = o.id
		WHERE EXISTS (SELECT 1 FROM organization_members om WHERE om.org_id = o.id AND om.user_id = $1)
		   OR EXISTS (SELECT 1 FROM mailboxes m WHERE m.org_id = o.id AND m.owner_user_id = $1)
		ORDER BY o.created_at`, userID, defaultFailedSignInThreshold)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (orgSecurityEntry, error) {
		var e orgSecurityEntry
		return e, row.Scan(&e.OrgID, &e.Settings.AppPasswordCreated, &e.Settings.ExternalForwardingRule,
			&e.Settings.DelegateAdded, &e.Settings.NewDeviceSignIn, &e.Settings.FailedSignIns,
			&e.Settings.NotifyAdmins, &e.Settings.FailedSignInThreshold)
	})
}

func (s *Server) brandNameForOrg(ctx context.Context, orgID *string) string {
	if orgID == nil || *orgID == "" {
		return "Aether"
	}
	var brandName, orgName string
	err := s.db.QueryRow(ctx, `SELECT coalesce(b.name, ''), o.name FROM organizations o LEFT JOIN organization_branding b ON b.org_id = o.id WHERE o.id = $1`, *orgID).
		Scan(&brandName, &orgName)
	if err != nil {
		return "Aether"
	}
	if brandName != "" {
		return brandName
	}
	if orgName != "" {
		return orgName
	}
	return "Aether"
}

func (s *Server) publicBaseURL() string {
	if s.cfg == nil || s.cfg.PublicURL == nil {
		return ""
	}
	return strings.TrimRight(s.cfg.PublicURL.String(), "/")
}

type securityEmailSpec struct {
	Subject  string
	TextBody string
	HTMLBody string
}

func formatDetailRows(details map[string]any, mailboxAddress string, createdAt time.Time) [][2]string {
	var rows [][2]string
	if mailboxAddress != "" {
		rows = append(rows, [2]string{"Mailbox", mailboxAddress})
	}
	if v, ok := details["appPasswordName"].(string); ok && v != "" {
		rows = append(rows, [2]string{"App password", v})
	}
	if v, ok := details["delegateEmail"].(string); ok && v != "" {
		access, _ := details["access"].(string)
		val := v
		if access != "" {
			val += " (" + access + " access)"
		}
		rows = append(rows, [2]string{"Delegate", val})
	}
	if list := stringSliceDetail(details["addresses"]); len(list) > 0 {
		rows = append(rows, [2]string{"Forwarding to", strings.Join(list, ", ")})
	}
	if list := stringSliceDetail(details["ruleNames"]); len(list) > 0 {
		rows = append(rows, [2]string{"Rule", strings.Join(list, ", ")})
	}
	if v, ok := details["actorLabel"].(string); ok && v != "" {
		rows = append(rows, [2]string{"Initiated by", v})
	}
	if v, ok := details["device"].(string); ok && v != "" {
		rows = append(rows, [2]string{"Device", v})
	}
	if v, ok := details["country"].(string); ok && v != "" {
		rows = append(rows, [2]string{"Country", v})
	}
	if v, ok := details["ip"].(string); ok && v != "" {
		rows = append(rows, [2]string{"IP address", v})
	}
	if v, ok := intDetail(details["attempts"]); ok && v > 0 {
		rows = append(rows, [2]string{"Failed attempts", strconv.Itoa(v) + " in 15 minutes"})
	}
	rows = append(rows, [2]string{"Time (UTC)", createdAt.UTC().Format("2006-01-02 15:04:05 UTC")})
	return rows
}

func stringSliceDetail(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func intDetail(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}

func buildSecurityEmail(baseURL, brandName, role, userEmail string, n *securityNotification, reportNote string) securityEmailSpec {
	if brandName == "" {
		brandName = "Aether"
	}
	reviewURL := baseURL + "/settings?alert=" + n.ID + "#security"
	secondaryURL := ""
	secondaryLabel := ""
	if n.ActionURL != "" && n.ActionURL != "/settings#security" {
		secondaryURL = baseURL + n.ActionURL
		switch n.EventType {
		case secEventAppPasswordCreated:
			secondaryLabel = "Open App passwords"
		case secEventExternalForwardingRule:
			secondaryLabel = "Open Mail rules"
		}
	}
	adminURL := baseURL + "/admin"
	rows := formatDetailRows(n.Details, n.MailboxAddress, n.CreatedAt)

	var subject, heading, intro, guidance, primaryURL, primaryLabel string
	switch role {
	case "admin-report":
		subject = fmt.Sprintf("[%s Security Report] %s reported: %s", brandName, userEmail, n.Title)
		heading = "Suspicious activity reported"
		intro = fmt.Sprintf("%s reported the following security event on their account as unrecognized or suspicious: %s", userEmail, n.Summary)
		if reportNote != "" {
			rows = append([][2]string{{"User note", reportNote}}, rows...)
		}
		guidance = "Review the organization audit log and check the affected account, active sessions, mail rules, delegates, and app passwords."
		primaryURL = adminURL
		primaryLabel = "Open Administration"
	case "admin":
		subject = fmt.Sprintf("[%s Security Alert] %s for %s", brandName, n.Title, userEmail)
		heading = n.Title
		intro = fmt.Sprintf("A security event occurred on %s's account: %s", userEmail, n.Summary)
		guidance = "You received this alert because administrator security notifications are enabled for your organization."
		primaryURL = adminURL
		primaryLabel = "Open Administration"
	default:
		subject = fmt.Sprintf("[%s Security] %s", brandName, n.Title)
		heading = n.Title
		intro = n.Summary
		guidance = "If you recognize this activity, no action is needed. If you did not do this, review the alert to undo the change or report it to your organization's administrators."
		primaryURL = reviewURL
		primaryLabel = "Review, undo, or report"
	}

	var tb strings.Builder
	tb.WriteString(heading + "\n")
	tb.WriteString(strings.Repeat("=", len(heading)) + "\n\n")
	tb.WriteString(intro + "\n\n")
	for _, r := range rows {
		tb.WriteString(r[0] + ": " + r[1] + "\n")
	}
	tb.WriteString("\n" + guidance + "\n")
	if primaryURL != "" {
		tb.WriteString("\n" + primaryLabel + ": " + primaryURL + "\n")
	}
	if role == "user" && secondaryURL != "" {
		tb.WriteString(secondaryLabel + ": " + secondaryURL + "\n")
	}

	var hb strings.Builder
	hb.WriteString(`<!doctype html><html><body style="margin:0;padding:24px;background:#f5f4f2;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;color:#2a2724;">`)
	hb.WriteString(`<div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e7e5e2;border-radius:12px;padding:24px;">`)
	hb.WriteString(`<p style="margin:0 0 8px;font-size:12px;font-weight:600;letter-spacing:0.04em;text-transform:uppercase;color:#b85110;">` + html.EscapeString(brandName) + ` Security</p>`)
	hb.WriteString(`<h1 style="margin:0 0 12px;font-size:20px;line-height:1.3;color:#111111;">` + html.EscapeString(heading) + `</h1>`)
	hb.WriteString(`<p style="margin:0 0 20px;font-size:15px;line-height:1.6;color:#2a2724;">` + html.EscapeString(intro) + `</p>`)
	hb.WriteString(`<table role="presentation" style="width:100%;border-collapse:collapse;margin:0 0 20px;border:1px solid #e7e5e2;border-radius:8px;">`)
	for i, r := range rows {
		border := ""
		if i > 0 {
			border = "border-top:1px solid #e7e5e2;"
		}
		hb.WriteString(`<tr><td style="padding:10px 12px;font-size:13px;color:#78716a;width:140px;vertical-align:top;` + border + `">` + html.EscapeString(r[0]) + `</td>`)
		hb.WriteString(`<td style="padding:10px 12px;font-size:13px;color:#2a2724;word-break:break-word;` + border + `">` + html.EscapeString(r[1]) + `</td></tr>`)
	}
	hb.WriteString(`</table>`)
	hb.WriteString(`<p style="margin:0 0 20px;font-size:14px;line-height:1.6;color:#57534d;">` + html.EscapeString(guidance) + `</p>`)
	if primaryURL != "" {
		hb.WriteString(`<p style="margin:0 0 12px;"><a href="` + html.EscapeString(primaryURL) + `" style="display:inline-block;background:#b85110;color:#ffffff;text-decoration:none;font-weight:600;font-size:14px;padding:10px 18px;border-radius:8px;">` + html.EscapeString(primaryLabel) + `</a></p>`)
	}
	if role == "user" && secondaryURL != "" {
		hb.WriteString(`<p style="margin:0;"><a href="` + html.EscapeString(secondaryURL) + `" style="color:#b85110;font-size:14px;">` + html.EscapeString(secondaryLabel) + `</a></p>`)
	}
	hb.WriteString(`</div></body></html>`)

	return securityEmailSpec{Subject: subject, TextBody: tb.String(), HTMLBody: hb.String()}
}

type emitSecurityInput struct {
	UserID            string
	OrgID             *string
	AdminOrgIDs       []string
	MailboxID         *string
	MailboxAddress    string
	EventType         string
	Title             string
	Summary           string
	Details           map[string]any
	ActionURL         string
	TargetSessionHash []byte
}

func (s *Server) emitSecurityNotification(ctx context.Context, in emitSecurityInput) (*securityNotification, error) {
	if in.Details == nil {
		in.Details = map[string]any{}
	}
	rawDetails, err := json.Marshal(in.Details)
	if err != nil {
		return nil, err
	}
	var userEmail string
	if err := s.db.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, in.UserID).Scan(&userEmail); err != nil {
		return nil, err
	}
	n := &securityNotification{
		EventType:      in.EventType,
		Title:          in.Title,
		Summary:        in.Summary,
		MailboxID:      in.MailboxID,
		MailboxAddress: in.MailboxAddress,
		Details:        in.Details,
		ActionURL:      in.ActionURL,
		UndoLabel:      undoLabelFor(in.EventType),
	}
	err = s.db.QueryRow(ctx, `
		INSERT INTO security_notifications (user_id, org_id, mailbox_id, event_type, title, summary, details, action_url, target_session_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at`,
		in.UserID, in.OrgID, in.MailboxID, in.EventType, in.Title, in.Summary, rawDetails, in.ActionURL, in.TargetSessionHash).
		Scan(&n.ID, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	n.CanUndo = canUndoNotification(n.EventType, nil, in.TargetSessionHash, nil)
	recipientAddr := in.MailboxAddress
	if recipientAddr == "" {
		recipientAddr = userEmail
	}
	brandName := s.brandNameForOrg(ctx, in.OrgID)
	baseURL := s.publicBaseURL()
	userEmailSpec := buildSecurityEmail(baseURL, brandName, "user", userEmail, n, "")
	if _, err := s.db.Exec(ctx, `
		INSERT INTO security_notification_emails (notification_id, recipient_user_id, recipient_address, recipient_role, org_id, subject, text_body, html_body)
		VALUES ($1, $2, $3, 'user', $4, $5, $6, $7)`,
		n.ID, in.UserID, recipientAddr, in.OrgID, userEmailSpec.Subject, userEmailSpec.TextBody, userEmailSpec.HTMLBody); err != nil {
		s.log.Error("queueing user security email failed", "notification", n.ID, "err", err)
	}
	if len(in.AdminOrgIDs) > 0 {
		s.enqueueAdminSecurityEmails(ctx, in.AdminOrgIDs, n, userEmail, in.UserID, "admin", "")
	}
	wake(s.securityNotifyWake)
	return n, nil
}

func (s *Server) enqueueAdminSecurityEmails(ctx context.Context, orgIDs []string, n *securityNotification, userEmail, excludeUserID, role, reportNote string) {
	seen := map[string]bool{}
	if excludeUserID != "" {
		seen[excludeUserID] = true
	}
	baseURL := s.publicBaseURL()
	for _, orgID := range orgIDs {
		brandName := s.brandNameForOrg(ctx, &orgID)
		spec := buildSecurityEmail(baseURL, brandName, role, userEmail, n, reportNote)
		rows, err := s.db.Query(ctx, `
			SELECT u.id, u.email
			FROM organization_members om
			JOIN users u ON u.id = om.user_id
			WHERE om.org_id = $1 AND om.role IN ('owner', 'admin') AND u.status = 'active'`, orgID)
		if err != nil {
			s.log.Error("loading organization admins for security email failed", "org", orgID, "err", err)
			continue
		}
		type adminRecipient struct {
			id, email string
		}
		admins, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adminRecipient, error) {
			var a adminRecipient
			return a, row.Scan(&a.id, &a.email)
		})
		if err != nil {
			s.log.Error("reading organization admins for security email failed", "org", orgID, "err", err)
			continue
		}
		for _, a := range admins {
			if seen[a.id] {
				continue
			}
			seen[a.id] = true
			if _, err := s.db.Exec(ctx, `
				INSERT INTO security_notification_emails (notification_id, recipient_user_id, recipient_address, recipient_role, org_id, subject, text_body, html_body)
				VALUES ($1, $2, $3, 'admin', $4, $5, $6, $7)`,
				n.ID, a.id, a.email, orgID, spec.Subject, spec.TextBody, spec.HTMLBody); err != nil {
				s.log.Error("queueing admin security email failed", "org", orgID, "admin", a.id, "err", err)
			}
		}
	}
	wake(s.securityNotifyWake)
}

func actorDetails(p *principal, r *http.Request, country string) (string, map[string]any) {
	details := map[string]any{}
	if r != nil {
		if ua := truncate(r.UserAgent(), 300); ua != "" {
			label, _ := deviceFingerprint(ua)
			details["device"] = label
			details["userAgent"] = ua
		}
	}
	if country != "" {
		details["country"] = country
	}
	label := ""
	switch {
	case p != nil && p.apiKey != nil:
		label = "API key " + p.apiKey.Name
		details["apiKeyId"] = p.apiKey.ID
		details["apiKeyName"] = p.apiKey.Name
		details["actorLabel"] = label
	case p != nil:
		label = p.User.Email
		details["actorUserId"] = p.User.ID
		details["actorEmail"] = p.User.Email
		details["actorLabel"] = label
	}
	return label, details
}

func (s *Server) notifyAppPasswordCreated(ctx context.Context, r *http.Request, p *principal, orgID, ownerUserID, mailboxID, mailboxAddress, appPasswordID, appPasswordName string) {
	settings, err := s.loadOrgSecuritySettings(ctx, orgID)
	if err != nil {
		s.log.Error("loading org security settings failed", "org", orgID, "err", err)
		return
	}
	if !settings.enabled(secEventAppPasswordCreated) {
		return
	}
	country := s.clientCountry(r)
	actorLabel, details := actorDetails(p, r, country)
	if r != nil {
		details["ip"] = s.clientIP(r)
	}
	details["appPasswordId"] = appPasswordID
	details["appPasswordName"] = appPasswordName
	details["mailboxId"] = mailboxID
	details["mailboxAddress"] = mailboxAddress
	summary := fmt.Sprintf("An app password named %q was created for %s.", appPasswordName, mailboxAddress)
	if p != nil && p.apiKey == nil && p.User.ID != ownerUserID && actorLabel != "" {
		summary = fmt.Sprintf("%s created an app password named %q for %s.", actorLabel, appPasswordName, mailboxAddress)
	}
	var adminOrgs []string
	if settings.NotifyAdmins {
		adminOrgs = []string{orgID}
	}
	if _, err := s.emitSecurityNotification(ctx, emitSecurityInput{
		UserID:         ownerUserID,
		OrgID:          &orgID,
		AdminOrgIDs:    adminOrgs,
		MailboxID:      &mailboxID,
		MailboxAddress: mailboxAddress,
		EventType:      secEventAppPasswordCreated,
		Title:          "App password created",
		Summary:        summary,
		Details:        details,
		ActionURL:      "/settings#app-passwords",
	}); err != nil {
		s.log.Error("creating app password security notification failed", "mailbox", mailboxID, "err", err)
	}
}

func (s *Server) notifyExternalForwardingRules(ctx context.Context, r *http.Request, p *principal, m *ownedMailbox, oldRules, newRules []mailpkg.Rule) {
	var ownerUserID *string
	if err := s.db.QueryRow(ctx, `SELECT owner_user_id FROM mailboxes WHERE id = $1`, m.ID).Scan(&ownerUserID); err != nil || ownerUserID == nil {
		return
	}
	settings, err := s.loadOrgSecuritySettings(ctx, m.OrgID)
	if err != nil {
		s.log.Error("loading org security settings failed", "org", m.OrgID, "err", err)
		return
	}
	if !settings.enabled(secEventExternalForwardingRule) {
		return
	}
	rows, err := s.db.Query(ctx, `SELECT name FROM domains WHERE org_id = $1 AND active AND verified_at IS NOT NULL`, m.OrgID)
	if err != nil {
		s.log.Error("loading org domains for forwarding check failed", "org", m.OrgID, "err", err)
		return
	}
	domains, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.log.Error("reading org domains for forwarding check failed", "org", m.OrgID, "err", err)
		return
	}
	orgDomains := map[string]bool{}
	for _, d := range domains {
		orgDomains[strings.ToLower(d)] = true
	}
	added, ruleNames := newlyAddedExternalForwards(oldRules, newRules, orgDomains)
	if len(added) == 0 {
		return
	}
	country := s.clientCountry(r)
	actorLabel, details := actorDetails(p, r, country)
	if r != nil {
		details["ip"] = s.clientIP(r)
	}
	details["mailboxId"] = m.ID
	details["mailboxAddress"] = m.Address
	details["addresses"] = added
	details["ruleNames"] = ruleNames
	summary := fmt.Sprintf("A mail rule on %s now forwards messages externally to %s.", m.Address, strings.Join(added, ", "))
	if actorLabel != "" && (p.apiKey != nil || p.User.ID != *ownerUserID) {
		summary = fmt.Sprintf("%s added an external forwarding rule on %s to %s.", actorLabel, m.Address, strings.Join(added, ", "))
	}
	var adminOrgs []string
	if settings.NotifyAdmins {
		adminOrgs = []string{m.OrgID}
	}
	if _, err := s.emitSecurityNotification(ctx, emitSecurityInput{
		UserID:         *ownerUserID,
		OrgID:          &m.OrgID,
		AdminOrgIDs:    adminOrgs,
		MailboxID:      &m.ID,
		MailboxAddress: m.Address,
		EventType:      secEventExternalForwardingRule,
		Title:          "External forwarding rule added",
		Summary:        summary,
		Details:        details,
		ActionURL:      "/settings#mail-rules",
	}); err != nil {
		s.log.Error("creating forwarding rule security notification failed", "mailbox", m.ID, "err", err)
	}
}

func (s *Server) notifyDelegateAdded(ctx context.Context, r *http.Request, p *principal, m *mailbox, d delegate, existed bool) {
	if m.OwnerUserID == nil {
		return
	}
	settings, err := s.loadOrgSecuritySettings(ctx, m.OrgID)
	if err != nil {
		s.log.Error("loading org security settings failed", "org", m.OrgID, "err", err)
		return
	}
	if !settings.enabled(secEventDelegateAdded) {
		return
	}
	country := s.clientCountry(r)
	actorLabel, details := actorDetails(p, r, country)
	if r != nil {
		details["ip"] = s.clientIP(r)
	}
	details["mailboxId"] = m.ID
	details["mailboxAddress"] = m.Address
	details["delegateUserId"] = d.UserID
	details["delegateEmail"] = d.Email
	details["delegateName"] = d.DisplayName
	details["access"] = d.Access
	details["sendAs"] = d.SendAs
	details["sendOnBehalf"] = d.SendOnBehalf
	accessDesc := "read-only"
	if d.Access == "full" {
		accessDesc = "full"
	}
	verb := "was given"
	if existed {
		verb = "was granted increased"
	}
	summary := fmt.Sprintf("%s %s %s delegate access to %s.", d.Email, verb, accessDesc, m.Address)
	if actorLabel != "" {
		summary = fmt.Sprintf("%s gave %s %s delegate access to %s.", actorLabel, d.Email, accessDesc, m.Address)
	}
	var adminOrgs []string
	if settings.NotifyAdmins {
		adminOrgs = []string{m.OrgID}
	}
	if _, err := s.emitSecurityNotification(ctx, emitSecurityInput{
		UserID:         *m.OwnerUserID,
		OrgID:          &m.OrgID,
		AdminOrgIDs:    adminOrgs,
		MailboxID:      &m.ID,
		MailboxAddress: m.Address,
		EventType:      secEventDelegateAdded,
		Title:          "Delegate access granted",
		Summary:        summary,
		Details:        details,
		ActionURL:      "/settings#security",
	}); err != nil {
		s.log.Error("creating delegate security notification failed", "mailbox", m.ID, "err", err)
	}
}

func (s *Server) checkSignInNotification(ctx context.Context, r *http.Request, userID, method, provider string, sessionHash []byte) {
	ua := truncate(r.UserAgent(), 300)
	deviceLabel, deviceHash := deviceFingerprint(ua)
	country := s.clientCountry(r)
	ip := s.clientIP(r)

	var hasPrior, knownDevice, knownCountry bool
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('known_device:' || $1))`, userID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_known_devices WHERE user_id = $1)`, userID).Scan(&hasPrior); err != nil {
			return err
		}
		if hasPrior {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_known_devices WHERE user_id = $1 AND device_hash = $2)`, userID, deviceHash).Scan(&knownDevice); err != nil {
				return err
			}
			if country == "" {
				knownCountry = true
			} else {
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_known_devices WHERE user_id = $1 AND country = $2)`, userID, country).Scan(&knownCountry); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO user_known_devices (user_id, device_hash, country, device_label, user_agent, last_ip, first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, now(), now())
			ON CONFLICT (user_id, device_hash, country) DO UPDATE SET
				device_label = EXCLUDED.device_label,
				user_agent = EXCLUDED.user_agent,
				last_ip = EXCLUDED.last_ip,
				last_seen_at = now()`,
			userID, deviceHash, country, deviceLabel, ua, ip)
		return err
	})
	if err != nil {
		s.log.Error("updating known devices failed", "user", userID, "err", err)
		return
	}
	if !hasPrior || (knownDevice && knownCountry) {
		return
	}

	orgs, err := s.userOrgSecuritySettings(ctx, userID)
	if err != nil {
		s.log.Error("loading user org security settings failed", "user", userID, "err", err)
		return
	}
	notifyUser := len(orgs) == 0
	var primaryOrgID *string
	var adminOrgs []string
	for _, o := range orgs {
		if o.Settings.enabled(secEventNewDeviceSignIn) {
			notifyUser = true
			if primaryOrgID == nil {
				id := o.OrgID
				primaryOrgID = &id
			}
			if o.Settings.NotifyAdmins {
				adminOrgs = append(adminOrgs, o.OrgID)
			}
		}
	}
	if !notifyUser {
		return
	}

	newDevice := !knownDevice
	newCountry := !knownCountry && country != ""
	var title, summary string
	switch {
	case newDevice && newCountry:
		title = "Sign-in from a new device and country"
		summary = fmt.Sprintf("Your account signed in from %s in %s.", deviceLabel, country)
	case newCountry:
		title = "Sign-in from a new country"
		summary = fmt.Sprintf("Your account signed in from a new country (%s) using %s.", country, deviceLabel)
	default:
		title = "Sign-in from a new device"
		summary = fmt.Sprintf("Your account signed in from %s.", deviceLabel)
	}
	details := map[string]any{
		"method":     method,
		"device":     deviceLabel,
		"userAgent":  ua,
		"ip":         ip,
		"newDevice":  newDevice,
		"newCountry": newCountry,
	}
	if provider != "" {
		details["provider"] = provider
	}
	if country != "" {
		details["country"] = country
	}
	if _, err := s.emitSecurityNotification(ctx, emitSecurityInput{
		UserID:            userID,
		OrgID:             primaryOrgID,
		AdminOrgIDs:       adminOrgs,
		EventType:         secEventNewDeviceSignIn,
		Title:             title,
		Summary:           summary,
		Details:           details,
		ActionURL:         "/settings#security",
		TargetSessionHash: sessionHash,
	}); err != nil {
		s.log.Error("creating new device sign-in notification failed", "user", userID, "err", err)
	}
}

func (s *Server) recordFailedSignIn(ctx context.Context, r *http.Request, userID string) {
	orgs, err := s.userOrgSecuritySettings(ctx, userID)
	if err != nil {
		s.log.Error("loading user org security settings failed", "user", userID, "err", err)
		return
	}
	notifyUser := len(orgs) == 0
	threshold := defaultFailedSignInThreshold
	if len(orgs) > 0 {
		threshold = maxFailedSignInThreshold + 1
	}
	var primaryOrgID *string
	var adminOrgs []string
	for _, o := range orgs {
		if o.Settings.enabled(secEventFailedSignIns) {
			notifyUser = true
			if o.Settings.FailedSignInThreshold < threshold {
				threshold = o.Settings.FailedSignInThreshold
			}
			if primaryOrgID == nil {
				id := o.OrgID
				primaryOrgID = &id
			}
			if o.Settings.NotifyAdmins {
				adminOrgs = append(adminOrgs, o.OrgID)
			}
		}
	}
	if !notifyUser {
		return
	}
	if threshold < minFailedSignInThreshold || threshold > maxFailedSignInThreshold {
		threshold = defaultFailedSignInThreshold
	}

	ip := s.clientIP(r)
	country := s.clientCountry(r)
	ua := truncate(r.UserAgent(), 300)
	deviceLabel, _ := deviceFingerprint(ua)

	var attempts int
	var trigger bool
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('failed_login:' || $1))`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_failed_sign_ins WHERE user_id = $1 AND created_at < now() - interval '24 hours'`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_failed_sign_ins (user_id, ip, country, user_agent) VALUES ($1, $2, $3, $4)`, userID, ip, country, ua); err != nil {
			return err
		}
		var lastAlert *time.Time
		if err := tx.QueryRow(ctx, `SELECT max(created_at) FROM security_notifications WHERE user_id = $1 AND event_type = $2`, userID, secEventFailedSignIns).Scan(&lastAlert); err != nil {
			return err
		}
		windowStart := time.Now().Add(-failedSignInWindow)
		if lastAlert != nil && lastAlert.After(windowStart) {
			windowStart = *lastAlert
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_failed_sign_ins WHERE user_id = $1 AND created_at > $2`, userID, windowStart).Scan(&attempts); err != nil {
			return err
		}
		trigger = attempts >= threshold
		return nil
	})
	if err != nil {
		s.log.Error("recording failed sign-in failed", "user", userID, "err", err)
		return
	}
	if !trigger {
		return
	}
	details := map[string]any{
		"attempts":      attempts,
		"windowMinutes": int(failedSignInWindow.Minutes()),
		"ip":            ip,
		"device":        deviceLabel,
		"userAgent":     ua,
	}
	if country != "" {
		details["country"] = country
	}
	summary := fmt.Sprintf("There were %d failed sign-in attempts on your account in the last 15 minutes.", attempts)
	if _, err := s.emitSecurityNotification(ctx, emitSecurityInput{
		UserID:      userID,
		OrgID:       primaryOrgID,
		AdminOrgIDs: adminOrgs,
		EventType:   secEventFailedSignIns,
		Title:       "Repeated failed sign-in attempts",
		Summary:     summary,
		Details:     details,
		ActionURL:   "/settings#security",
	}); err != nil {
		s.log.Error("creating failed sign-in notification failed", "user", userID, "err", err)
	}
}

func (s *Server) scanSecurityNotification(row pgx.Row, currentSessionHash []byte) (*securityNotification, error) {
	var n securityNotification
	var rawDetails []byte
	var targetSessionHash []byte
	err := row.Scan(
		&n.ID, &n.EventType, &n.Title, &n.Summary, &n.MailboxID, &n.MailboxAddress,
		&rawDetails, &n.ActionURL, &targetSessionHash, &n.ReadAt, &n.UndoneAt, &n.ReportedAt, &n.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(rawDetails, &n.Details); err != nil || n.Details == nil {
		n.Details = map[string]any{}
	}
	n.UndoLabel = undoLabelFor(n.EventType)
	n.CanUndo = canUndoNotification(n.EventType, n.UndoneAt, targetSessionHash, currentSessionHash)
	return &n, nil
}

const securityNotificationSelect = `
	SELECT n.id, n.event_type, n.title, n.summary, n.mailbox_id, coalesce(m.address, ''),
	       n.details, n.action_url, n.target_session_hash, n.read_at, n.undone_at, n.reported_at, n.created_at
	FROM security_notifications n
	LEFT JOIN mailboxes m ON m.id = n.mailbox_id`

func (s *Server) listSecurityNotifications(w http.ResponseWriter, r *http.Request, p *principal) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 100 {
			limit = v
		}
	}
	unreadOnly := r.URL.Query().Get("unread") == "true"
	query := securityNotificationSelect + ` WHERE n.user_id = $1`
	if unreadOnly {
		query += ` AND n.read_at IS NULL`
	}
	query += ` ORDER BY n.created_at DESC LIMIT $2`
	rows, err := s.db.Query(r.Context(), query, p.User.ID, limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()
	items := []securityNotification{}
	for rows.Next() {
		item, err := s.scanSecurityNotification(rows, p.sessionHash)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	var unreadCount int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM security_notifications WHERE user_id = $1 AND read_at IS NULL`, p.User.ID).Scan(&unreadCount); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"notifications": items,
		"unreadCount":   unreadCount,
	})
}

func (s *Server) markSecurityNotificationRead(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE security_notifications SET read_at = coalesce(read_at, now()) WHERE id = $1 AND user_id = $2`, id, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	n, err := s.scanSecurityNotification(s.db.QueryRow(r.Context(), securityNotificationSelect+` WHERE n.id = $1 AND n.user_id = $2`, id, p.User.ID), p.sessionHash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) markAllSecurityNotificationsRead(w http.ResponseWriter, r *http.Request, p *principal) {
	if _, err := s.db.Exec(r.Context(), `UPDATE security_notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, p.User.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) undoSecurityNotification(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var orgID, mailboxID *string
	var eventType string
	var rawDetails, targetSessionHash []byte
	var undoneAt *time.Time
	err := s.db.QueryRow(r.Context(), `
		SELECT org_id, mailbox_id, event_type, details, target_session_hash, undone_at
		FROM security_notifications WHERE id = $1 AND user_id = $2`, id, p.User.ID).
		Scan(&orgID, &mailboxID, &eventType, &rawDetails, &targetSessionHash, &undoneAt)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if undoneAt != nil {
		writeError(w, http.StatusConflict, "already_undone", "This security event has already been undone.")
		return
	}
	details := map[string]any{}
	json.Unmarshal(rawDetails, &details)

	var message string
	switch eventType {
	case secEventAppPasswordCreated:
		appPasswordID, _ := details["appPasswordId"].(string)
		if !uuidPattern.MatchString(appPasswordID) {
			writeError(w, http.StatusUnprocessableEntity, "not_undoable", "This app password can no longer be revoked from here.")
			return
		}
		var revokedOrg, revokedAddr string
		err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
			err := tx.QueryRow(r.Context(), `
				UPDATE app_passwords a SET revoked_at = now()
				FROM mailboxes m
				WHERE a.id = $1 AND a.revoked_at IS NULL AND m.id = a.mailbox_id
				  AND (a.user_id = $2 OR m.owner_user_id = $2)
				RETURNING m.org_id, m.address`, appPasswordID, p.User.ID).Scan(&revokedOrg, &revokedAddr)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			return queueKicks(r.Context(), tx, []string{revokedAddr})
		})
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if revokedOrg != "" {
			s.audit(r.Context(), r, p, &revokedOrg, "app_password.revoke", "app_password", appPasswordID, map[string]any{"source": "security_notification"})
			wake(s.kickWake)
			message = "The app password has been revoked."
		} else {
			message = "That app password was already revoked."
		}

	case secEventExternalForwardingRule:
		if mailboxID == nil {
			writeError(w, http.StatusUnprocessableEntity, "not_undoable", "The mailbox no longer exists.")
			return
		}
		var mOrgID, mAddr string
		err := s.db.QueryRow(r.Context(), `
			SELECT m.org_id, m.address FROM mailboxes m JOIN domains d ON d.id = m.domain_id
			WHERE m.id = $1 AND m.owner_user_id = $2 AND m.active AND d.active AND d.verified_at IS NOT NULL`, *mailboxID, p.User.ID).
			Scan(&mOrgID, &mAddr)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnprocessableEntity, "not_undoable", "That mailbox is no longer active or owned by you.")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		targets := stringSliceDetail(details["addresses"])
		rules, reply, err := s.loadSieveSettings(r.Context(), *mailboxID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		updated, changed := removeExternalForwards(rules, targets)
		if changed {
			err = s.persistSieve(r.Context(), &ownedMailbox{ID: *mailboxID, OrgID: mOrgID, Address: mAddr}, updated, reply)
			if err != nil {
				mailBackendErrs.Inc()
				s.log.Error("undoing external forwarding rule failed", "mailbox", *mailboxID, "err", err)
				writeError(w, http.StatusBadGateway, "rules_not_saved", "The updated rules could not be installed on the mail server. Try again.")
				return
			}
			s.audit(r.Context(), r, p, &mOrgID, "mailbox.rules.update", "mailbox", *mailboxID, map[string]any{"rules": len(updated), "autoReply": reply.Enabled, "source": "security_notification"})
			message = "The external forwarding rule has been removed."
		} else {
			message = "That external forwarding address was already removed."
		}

	case secEventDelegateAdded:
		delegateUserID, _ := details["delegateUserId"].(string)
		if mailboxID == nil || !uuidPattern.MatchString(delegateUserID) {
			writeError(w, http.StatusUnprocessableEntity, "not_undoable", "This delegate can no longer be removed from here.")
			return
		}
		var mOrgID, mAddr, mKind string
		err := s.db.QueryRow(r.Context(), `SELECT org_id, address, kind FROM mailboxes WHERE id = $1 AND owner_user_id = $2`, *mailboxID, p.User.ID).
			Scan(&mOrgID, &mAddr, &mKind)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnprocessableEntity, "not_undoable", "That mailbox is no longer owned by you.")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		var removedEmail string
		err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
			err := tx.QueryRow(r.Context(), `
				DELETE FROM mailbox_delegates md USING users u
				WHERE md.mailbox_id = $1 AND md.user_id = $2 AND u.id = md.user_id
				RETURNING u.email`, *mailboxID, delegateUserID).Scan(&removedEmail)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			return s.revokeMailAccess(r.Context(), tx, delegateUserID, []string{*mailboxID})
		})
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if removedEmail != "" {
			s.audit(r.Context(), r, p, &mOrgID, "mailbox.delegate_remove", "mailbox", *mailboxID, map[string]any{"address": mAddr, "delegate": removedEmail, "source": "security_notification"})
			wake(s.kickWake)
			message = "Delegate access for " + removedEmail + " has been removed."
		} else {
			message = "That delegate no longer had access."
		}

	case secEventNewDeviceSignIn:
		if len(targetSessionHash) == 0 {
			writeError(w, http.StatusUnprocessableEntity, "not_undoable", "That session can no longer be signed out from here.")
			return
		}
		if bytes.Equal(targetSessionHash, p.sessionHash) {
			writeError(w, http.StatusConflict, "current_session", "This alert is for your current session. Use Sign out in the account menu to end it.")
			return
		}
		tag, err := s.db.Exec(r.Context(), `DELETE FROM sessions WHERE id_hash = $1 AND user_id = $2`, targetSessionHash, p.User.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if tag.RowsAffected() > 0 {
			s.audit(r.Context(), r, p, orgID, "auth.session.revoke", "user", p.User.ID, map[string]any{"source": "security_notification"})
			message = "That session has been signed out."
		} else {
			message = "That session had already ended."
		}

	default:
		writeError(w, http.StatusUnprocessableEntity, "not_undoable", "This event cannot be undone automatically. Report it to your administrator if it looks suspicious.")
		return
	}

	if _, err := s.db.Exec(r.Context(), `UPDATE security_notifications SET undone_at = now(), read_at = coalesce(read_at, now()) WHERE id = $1 AND user_id = $2`, id, p.User.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, orgID, "security.notification.undo", "security_notification", id, map[string]any{"eventType": eventType})
	n, err := s.scanSecurityNotification(s.db.QueryRow(r.Context(), securityNotificationSelect+` WHERE n.id = $1 AND n.user_id = $2`, id, p.User.ID), p.sessionHash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"notification": n,
		"message":      message,
	})
}

func (s *Server) reportSecurityNotification(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		if !decode(w, r, 8<<10, &in) {
			return
		}
	}
	in.Note = strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(in.Note) > 1000 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_note", "Keep your note to 1,000 characters or fewer.")
		return
	}
	var orgID *string
	var reportedAt *time.Time
	err := s.db.QueryRow(r.Context(), `SELECT org_id, reported_at FROM security_notifications WHERE id = $1 AND user_id = $2`, id, p.User.ID).Scan(&orgID, &reportedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if reportedAt != nil {
		writeError(w, http.StatusConflict, "already_reported", "This security event has already been reported to your administrators.")
		return
	}
	if in.Note != "" {
		patch, _ := json.Marshal(map[string]string{"reportNote": in.Note})
		if _, err := s.db.Exec(r.Context(), `UPDATE security_notifications SET reported_at = now(), read_at = coalesce(read_at, now()), details = details || $3::jsonb WHERE id = $1 AND user_id = $2`, id, p.User.ID, patch); err != nil {
			s.internalError(w, r, err)
			return
		}
	} else {
		if _, err := s.db.Exec(r.Context(), `UPDATE security_notifications SET reported_at = now(), read_at = coalesce(read_at, now()) WHERE id = $1 AND user_id = $2`, id, p.User.ID); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	n, err := s.scanSecurityNotification(s.db.QueryRow(r.Context(), securityNotificationSelect+` WHERE n.id = $1 AND n.user_id = $2`, id, p.User.ID), p.sessionHash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var targetOrgs []string
	if orgID != nil {
		targetOrgs = []string{*orgID}
	} else {
		orgs, err := s.userOrgSecuritySettings(r.Context(), p.User.ID)
		if err == nil {
			for _, o := range orgs {
				targetOrgs = append(targetOrgs, o.OrgID)
			}
		}
	}
	meta := map[string]any{"eventType": n.EventType, "title": n.Title, "mailbox": n.MailboxAddress}
	if in.Note != "" {
		meta["note"] = in.Note
	}
	if len(targetOrgs) == 0 {
		s.audit(r.Context(), r, p, nil, "security.notification.report", "security_notification", id, meta)
	} else {
		for _, oid := range targetOrgs {
			oidCopy := oid
			s.audit(r.Context(), r, p, &oidCopy, "security.notification.report", "security_notification", id, meta)
		}
		s.enqueueAdminSecurityEmails(r.Context(), targetOrgs, n, p.User.Email, "", "admin-report", in.Note)
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) resolveDeliveryMailbox(ctx context.Context, recipientUserID *string, recipientAddress string, orgID *string) (string, error) {
	var addr string
	err := s.db.QueryRow(ctx, `
		SELECT m.address FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.address = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL`, strings.ToLower(strings.TrimSpace(recipientAddress))).Scan(&addr)
	if err == nil {
		return addr, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if recipientUserID == nil {
		return "", nil
	}
	if orgID != nil {
		err = s.db.QueryRow(ctx, `
			SELECT m.address FROM mailboxes m JOIN domains d ON d.id = m.domain_id
			WHERE m.owner_user_id = $1 AND m.org_id = $2 AND m.active AND d.active AND d.verified_at IS NOT NULL
			ORDER BY m.created_at LIMIT 1`, *recipientUserID, *orgID).Scan(&addr)
		if err == nil {
			return addr, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	err = s.db.QueryRow(ctx, `
		SELECT m.address FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.owner_user_id = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL
		ORDER BY m.created_at LIMIT 1`, *recipientUserID).Scan(&addr)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return addr, err
}

func (s *Server) deliverSecurityEmails(ctx context.Context) {
	if s.mail == nil || s.db == nil {
		return
	}
	rows, err := s.db.Query(ctx, `
		UPDATE security_notification_emails SET next_attempt_at = now() + interval '2 minutes'
		WHERE id IN (
			SELECT id FROM security_notification_emails
			WHERE status = 'pending' AND next_attempt_at <= now()
			ORDER BY next_attempt_at LIMIT 20 FOR UPDATE SKIP LOCKED
		)
		RETURNING id, recipient_user_id, recipient_address, org_id, subject, text_body, html_body, attempts`)
	if err != nil {
		s.log.Error("loading security notification emails failed", "err", err)
		return
	}
	type job struct {
		id              string
		recipientUserID *string
		recipientAddr   string
		orgID           *string
		subject         string
		textBody        string
		htmlBody        string
		attempts        int
	}
	due, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (job, error) {
		var j job
		return j, row.Scan(&j.id, &j.recipientUserID, &j.recipientAddr, &j.orgID, &j.subject, &j.textBody, &j.htmlBody, &j.attempts)
	})
	if err != nil {
		s.log.Error("reading security notification emails failed", "err", err)
		return
	}
	mailHost := "aether.local"
	if s.cfg != nil && s.cfg.MailHostname != "" {
		mailHost = s.cfg.MailHostname
	}
	for _, j := range due {
		targetMailbox, err := s.resolveDeliveryMailbox(ctx, j.recipientUserID, j.recipientAddr, j.orgID)
		if err != nil {
			s.log.Warn("resolving mailbox for security email failed", "emailId", j.id, "err", err)
			s.retrySecurityEmail(ctx, j.id, j.attempts, err)
			continue
		}
		if targetMailbox == "" {
			s.db.Exec(ctx, `UPDATE security_notification_emails SET status = 'skipped', attempts = attempts + 1, last_error = 'Recipient has no active hosted mailbox.' WHERE id = $1`, j.id)
			continue
		}
		brandName := s.brandNameForOrg(ctx, j.orgID)
		out := &mailpkg.Outgoing{
			From:          mailpkg.Address{Name: brandName + " Security", Address: "security@" + mailHost},
			To:            []mailpkg.Address{{Address: targetMailbox}},
			Subject:       j.subject,
			Text:          j.textBody,
			HTML:          j.htmlBody,
			AutoSubmitted: "auto-generated",
		}
		raw, _, err := out.Build(mailHost)
		if err != nil {
			s.db.Exec(ctx, `UPDATE security_notification_emails SET status = 'failed', attempts = attempts + 1, last_error = $2 WHERE id = $1`, j.id, truncate(err.Error(), 500))
			continue
		}
		if err := s.mail.DeliverInbox(ctx, targetMailbox, raw); err != nil {
			s.log.Warn("delivering security notification email failed", "emailId", j.id, "mailbox", targetMailbox, "err", err)
			s.retrySecurityEmail(ctx, j.id, j.attempts, err)
			continue
		}
		s.db.Exec(ctx, `UPDATE security_notification_emails SET status = 'sent', attempts = attempts + 1, last_error = NULL, sent_at = now() WHERE id = $1`, j.id)
	}
}

func (s *Server) retrySecurityEmail(ctx context.Context, id string, attempts int, err error) {
	if attempts+1 >= maxSecurityEmailAttempts {
		s.db.Exec(ctx, `UPDATE security_notification_emails SET status = 'failed', attempts = attempts + 1, last_error = $2 WHERE id = $1`, id, truncate(err.Error(), 500))
		return
	}
	delay := securityEmailRetryDelays[min(attempts, len(securityEmailRetryDelays)-1)].Seconds()
	s.db.Exec(ctx, `UPDATE security_notification_emails SET attempts = attempts + 1, last_error = $2, next_attempt_at = now() + make_interval(secs => $3) WHERE id = $1`, id, truncate(err.Error(), 500), delay)
}
