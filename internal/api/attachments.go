package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

const attachmentPolicyHash = "AETHER_ATTACHMENT_POLICY"

var (
	attachmentActions        = []string{"off", "reject", "remove"}
	extensionPattern         = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	defaultBlockedExtensions = []string{
		"ace", "ani", "apk", "app", "appx", "arj", "bat", "cab", "chm", "cmd", "com", "cpl", "dll", "docm", "dotm",
		"exe", "hta", "img", "inf", "ins", "iso", "jar", "jnlp", "js", "jse", "lnk", "msc", "msi", "msix", "msp",
		"mst", "pif", "potm", "ppam", "ppsm", "pptm", "ps1", "psm1", "reg", "scf", "scr", "sct", "sldm", "sys",
		"vb", "vbe", "vbs", "vhd", "vhdx", "vxd", "wsc", "wsf", "wsh", "xlam", "xll", "xlsm", "xltm",
	}
)

type attachmentRules struct {
	Action     string   `json:"action"`
	Extensions []string `json:"extensions"`
}

type attachmentPolicy struct {
	Inbound       attachmentRules `json:"inbound"`
	Outbound      attachmentRules `json:"outbound"`
	ExemptSenders []string        `json:"exemptSenders"`
}

func (s *Server) loadAttachmentPolicy(ctx context.Context, orgID string) (attachmentPolicy, error) {
	p := attachmentPolicy{
		Inbound:       attachmentRules{"reject", slices.Clone(defaultBlockedExtensions)},
		Outbound:      attachmentRules{"off", slices.Clone(defaultBlockedExtensions)},
		ExemptSenders: []string{},
	}
	err := s.db.QueryRow(ctx, `SELECT inbound_action, inbound_extensions, outbound_action, outbound_extensions, exempt_senders FROM organization_attachment_policies WHERE org_id = $1`, orgID).
		Scan(&p.Inbound.Action, &p.Inbound.Extensions, &p.Outbound.Action, &p.Outbound.Extensions, &p.ExemptSenders)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	return p, err
}

func (s *Server) publishAttachmentPolicy(ctx context.Context, orgID string) error {
	policy, err := s.loadAttachmentPolicy(ctx, orgID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `SELECT name FROM domains WHERE org_id = $1`, orgID)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(names) == 0 {
		return err
	}
	fields := make([]any, 0, 2*len(names))
	for _, name := range names {
		fields = append(fields, name, raw)
	}
	return s.kv.HSet(ctx, attachmentPolicyHash, fields...).Err()
}

func (s *Server) SyncAttachmentPolicies(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT org_id FROM domains`)
	if err != nil {
		return err
	}
	orgIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	var errs []error
	for _, orgID := range orgIDs {
		errs = append(errs, s.publishAttachmentPolicy(ctx, orgID))
	}
	return errors.Join(errs...)
}

func (s *Server) writeAttachmentPolicy(w http.ResponseWriter, r *http.Request, orgID string) {
	policy, err := s.loadAttachmentPolicy(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		attachmentPolicy
		DefaultExtensions []string `json:"defaultExtensions"`
	}{policy, defaultBlockedExtensions})
}

func (s *Server) getAttachmentPolicy(w http.ResponseWriter, r *http.Request, p *principal) {
	if orgID, ok := s.orgFromPath(w, r, p, true); ok {
		s.writeAttachmentPolicy(w, r, orgID)
	}
}

func (s *Server) saveAttachmentPolicy(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in attachmentPolicy
	if !decode(w, r, 64<<10, &in) {
		return
	}
	for _, rules := range []*attachmentRules{&in.Inbound, &in.Outbound} {
		if msg := normalizeAttachmentRules(rules); msg != "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_attachment_policy", msg)
			return
		}
	}
	if len(in.ExemptSenders) > 500 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_attachment_policy", "Keep the list of exempt senders to 500 entries.")
		return
	}
	exempt, fe := parseAddresses(in.ExemptSenders, true)
	if fe != nil {
		writeError(w, fe.Status, fe.Code, fe.Message)
		return
	}
	in.ExemptSenders = exempt
	before, err := s.loadAttachmentPolicy(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO organization_attachment_policies (org_id, inbound_action, inbound_extensions, outbound_action, outbound_extensions, exempt_senders) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (org_id) DO UPDATE SET inbound_action = excluded.inbound_action, inbound_extensions = excluded.inbound_extensions, outbound_action = excluded.outbound_action,
			outbound_extensions = excluded.outbound_extensions, exempt_senders = excluded.exempt_senders, updated_at = now()`,
		orgID, in.Inbound.Action, in.Inbound.Extensions, in.Outbound.Action, in.Outbound.Extensions, in.ExemptSenders)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.publishAttachmentPolicy(r.Context(), orgID); err != nil {
		s.internalError(w, r, err)
		return
	}
	var changed []string
	for field, same := range map[string]bool{
		"inbound":       before.Inbound.Action == in.Inbound.Action && slices.Equal(before.Inbound.Extensions, in.Inbound.Extensions),
		"outbound":      before.Outbound.Action == in.Outbound.Action && slices.Equal(before.Outbound.Extensions, in.Outbound.Extensions),
		"exemptSenders": slices.Equal(before.ExemptSenders, in.ExemptSenders),
	} {
		if !same {
			changed = append(changed, field)
		}
	}
	if len(changed) > 0 {
		slices.Sort(changed)
		s.audit(r.Context(), r, p, &orgID, "attachment_policy.update", "organization", orgID, map[string]any{"changed": changed, "inboundAction": in.Inbound.Action, "outboundAction": in.Outbound.Action})
	}
	s.writeAttachmentPolicy(w, r, orgID)
}

func normalizeAttachmentRules(rules *attachmentRules) string {
	if !slices.Contains(attachmentActions, rules.Action) {
		return "Choose off, reject, or remove as the action."
	}
	if len(rules.Extensions) > 300 {
		return "Keep each list to 300 file types."
	}
	out := make([]string, 0, len(rules.Extensions))
	for _, ext := range rules.Extensions {
		ext = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), ".")
		if !extensionPattern.MatchString(ext) {
			return "Enter file types as extensions made of letters and digits, such as exe or docm."
		}
		out = append(out, ext)
	}
	slices.Sort(out)
	rules.Extensions = slices.Compact(out)
	return ""
}
