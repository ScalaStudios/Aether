package api

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const quotaRearmMargin = 5

type quotaWarnings struct {
	Percents     []int16 `json:"percents"`
	NotifyAdmins bool    `json:"notifyAdmins"`
}

func (s *Server) getQuotaWarnings(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var q quotaWarnings
	if err := s.db.QueryRow(r.Context(), `SELECT quota_warning_percents, quota_warning_admins FROM organizations WHERE id = $1`, orgID).Scan(&q.Percents, &q.NotifyAdmins); err != nil {
		s.internalError(w, r, err)
		return
	}
	q.Percents = nonNil(q.Percents)
	writeJSON(w, http.StatusOK, q)
}

func (s *Server) saveQuotaWarnings(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in quotaWarnings
	if !decode(w, r, 1<<10, &in) {
		return
	}
	slices.Sort(in.Percents)
	in.Percents = nonNil(slices.Compact(in.Percents))
	if len(in.Percents) > 2 || slices.ContainsFunc(in.Percents, func(v int16) bool { return v < 50 || v > 99 }) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_quota_warnings", "Choose up to two warning levels between 50% and 99%.")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE organizations SET quota_warning_percents = $2, quota_warning_admins = $3 WHERE id = $1`, orgID, in.Percents, in.NotifyAdmins); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "organization.quota_warnings.update", "organization", orgID, map[string]any{"percents": in.Percents, "notifyAdmins": in.NotifyAdmins})
	writeJSON(w, http.StatusOK, in)
}

func nextQuotaWarning(percent int64, thresholds []int16, warned int16) (int16, bool) {
	var level int16
	for _, t := range thresholds {
		if percent >= int64(t) {
			level = max(level, t)
		}
	}
	switch {
	case level > warned:
		return level, true
	case level < warned && percent < int64(warned)-quotaRearmMargin:
		return level, false
	}
	return warned, false
}

func (s *Server) RunQuotaWatcher(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.QuotaCheckInterval)
	defer ticker.Stop()
	for {
		if err := s.checkQuotas(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("mailbox storage check failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type quotaMailbox struct {
	ID, OrgID, Address, DisplayName, Kind string
	Used, Quota                           int64
	Warned                                int16
	OrgName, Brand                        string
	Thresholds                            []int16
	NotifyAdmins                          bool
}

func (s *Server) checkQuotas(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id::text, address FROM mail_users`)
	if err != nil {
		return err
	}
	ids := map[string]string{}
	addresses, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var id, address string
		err := row.Scan(&id, &address)
		ids[address] = id
		return address, err
	})
	if err != nil {
		return err
	}
	usage, usageErr := s.mail.Usage(ctx, addresses)
	var measured []string
	var used []int64
	for address, n := range usage {
		measured, used = append(measured, ids[address]), append(used, n)
	}
	if len(measured) == 0 {
		return usageErr
	}
	if _, err := s.db.Exec(ctx, `UPDATE mailboxes m SET used_bytes = u.used FROM unnest($1::uuid[], $2::bigint[]) AS u(id, used) WHERE m.id = u.id AND m.used_bytes <> u.used`, measured, used); err != nil {
		return err
	}
	rows, err = s.db.Query(ctx, `
		SELECT m.id, m.org_id, m.address, m.display_name, m.kind, m.used_bytes, m.quota_bytes, m.quota_warned_percent,
		       o.name, coalesce(b.name, ''), o.quota_warning_percents, o.quota_warning_admins
		FROM mailboxes m JOIN organizations o ON o.id = m.org_id LEFT JOIN organization_branding b ON b.org_id = o.id
		WHERE m.id = ANY($1::uuid[])`, measured)
	if err != nil {
		return err
	}
	mailboxes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[quotaMailbox])
	if err != nil {
		return err
	}
	for _, m := range mailboxes {
		var percent int64
		if m.Quota > 0 {
			percent = m.Used * 100 / m.Quota
		}
		next, send := nextQuotaWarning(percent, m.Thresholds, m.Warned)
		if next == m.Warned {
			continue
		}
		tag, err := s.db.Exec(ctx, `UPDATE mailboxes SET quota_warned_percent = $2 WHERE id = $1 AND quota_warned_percent = $3`, m.ID, next, m.Warned)
		if err != nil {
			return err
		}
		if send && tag.RowsAffected() == 1 {
			s.sendQuotaWarning(ctx, m, percent, next)
		}
	}
	return usageErr
}

func (s *Server) sendQuotaWarning(ctx context.Context, m quotaMailbox, percent int64, level int16) {
	critical := percent >= 100 || (len(m.Thresholds) > 1 && level == slices.Max(m.Thresholds))
	n := quotaNotice{quotaMailbox: m, Percent: percent, Level: level, Critical: critical}
	if folders, err := s.mail.Folders(m.Address); err == nil {
		slices.SortFunc(folders, func(a, b mailpkg.Folder) int { return cmp.Compare(b.Size, a.Size) })
		for _, f := range folders[:min(3, len(folders))] {
			name := f.Name
			if f.SpecialUse == "inbox" {
				name = "Inbox"
			}
			if f.Size > 0 {
				n.Folders = append(n.Folders, folderUsage{Name: name, Size: formatBytes(f.Size)})
			}
		}
	}
	if err := s.deliverQuotaNotice(m.Address, m.DisplayName, n.forMailbox(s.cfg.PublicURL.String())); err != nil {
		s.log.Warn("quota warning could not be delivered to the mailbox", "mailbox", m.ID, "err", err)
	} else {
		s.log.Info("quota warning delivered", "mailbox", m.ID, "percent", percent, "level", level)
	}
	if !m.NotifyAdmins {
		return
	}
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT ON (u.id) mb.display_name, mb.address
		FROM organization_members om
		JOIN users u ON u.id = om.user_id AND u.status = 'active'
		JOIN mailboxes mb ON mb.owner_user_id = u.id AND mb.kind = 'user' AND mb.active
		JOIN domains d ON d.id = mb.domain_id AND d.active AND d.verified_at IS NOT NULL
		WHERE om.org_id = $1 AND om.role IN ('owner', 'admin')
		ORDER BY u.id, lower(mb.address) = lower(u.email) DESC, mb.created_at`, m.OrgID)
	if err != nil {
		s.log.Warn("quota warning administrators could not be loaded", "mailbox", m.ID, "err", err)
		return
	}
	admins, err := pgx.CollectRows(rows, pgx.RowToStructByPos[mailpkg.Address])
	if err != nil {
		s.log.Warn("quota warning administrators could not be loaded", "mailbox", m.ID, "err", err)
		return
	}
	copyFor := n.forAdmin(s.cfg.PublicURL.String())
	for _, a := range admins {
		if strings.EqualFold(a.Address, m.Address) {
			continue
		}
		if err := s.deliverQuotaNotice(a.Address, a.Name, copyFor); err != nil {
			s.log.Warn("quota warning could not be delivered to an administrator", "mailbox", m.ID, "admin", a.Address, "err", err)
		}
	}
}

func (s *Server) deliverQuotaNotice(address, name string, c quotaCopy) error {
	var html bytes.Buffer
	if err := quotaMailHTML.Execute(&html, c); err != nil {
		return err
	}
	out := &mailpkg.Outgoing{
		From:    mailpkg.Address{Name: c.Brand, Address: "no-reply@" + s.cfg.MailHostname},
		To:      []mailpkg.Address{{Name: name, Address: address}},
		Subject: c.Subject,
		Text:    c.text(),
		HTML:    html.String(),
	}
	raw, _, err := out.Build(s.cfg.MailHostname)
	if err != nil {
		return err
	}
	return s.mail.Deliver(address, raw)
}

type folderUsage struct {
	Name, Size string
}

type quotaNotice struct {
	quotaMailbox
	Percent  int64
	Level    int16
	Critical bool
	Folders  []folderUsage
}

type quotaCopy struct {
	Brand, Subject, Summary, Heading, Usage, Button, URL, Footer string
	Paragraphs                                                   []string
	Folders                                                      []folderUsage
	Fill                                                         int64
	Color                                                        string
}

func (n quotaNotice) base() quotaCopy {
	c := quotaCopy{
		Brand:   cmp.Or(n.Brand, "Aether"),
		Subject: fmt.Sprintf("%s is %d%% full", n.Address, n.Percent),
		Fill:    max(min(n.Percent, 100), 2),
		Color:   "#e0a21b",
	}
	if n.Percent >= 100 {
		c.Subject = n.Address + " is full"
	}
	if n.Critical {
		c.Color = "#c92438"
	}
	return c
}

func (n quotaNotice) fullness() string {
	if n.Percent >= 100 {
		return "full"
	}
	return strconv.FormatInt(n.Percent, 10) + "% full"
}

func (n quotaNotice) forMailbox(publicURL string) quotaCopy {
	c := n.base()
	c.Heading = n.Address + " is " + n.fullness()
	if n.Kind == "user" {
		c.Heading = "Your mailbox is " + n.fullness()
	}
	c.Usage = fmt.Sprintf("%s is using %s of %s.", n.Address, formatBytes(n.Used), formatBytes(n.Quota))
	if n.Percent >= 100 {
		c.Paragraphs = []string{"New messages are being returned to their senders until you free up space."}
	} else {
		c.Paragraphs = []string{"When it reaches 100%, new messages are returned to their senders. Free up space now so nothing gets lost."}
	}
	c.Paragraphs = append(c.Paragraphs, "Emptying Trash and Junk frees space right away, and sorting a folder by size shows the largest messages first.")
	c.Folders = n.Folders
	c.Button, c.URL = "Review storage", publicURL+"/settings#storage"
	c.Footer = fmt.Sprintf("Sent because %s passed the %d%% storage warning set by %s. If you need more space, ask an administrator of %s.", n.Address, n.Level, n.OrgName, n.OrgName)
	c.Summary = c.Usage + " " + c.Paragraphs[0]
	return c
}

func (n quotaNotice) forAdmin(publicURL string) quotaCopy {
	c := n.base()
	c.Heading = n.Address + " is " + n.fullness()
	c.Usage = fmt.Sprintf("It is using %s of its %s quota.", formatBytes(n.Used), formatBytes(n.Quota))
	if n.Percent >= 100 {
		c.Paragraphs = []string{"New messages to it are being returned to their senders."}
	} else {
		c.Paragraphs = []string{"When it reaches 100%, new messages to it are returned to their senders."}
	}
	c.Paragraphs = append(c.Paragraphs, "Raise its quota under Administration → Mailboxes, or ask the people who use it to free up space.")
	c.Button, c.URL = "Open Administration", publicURL+"/admin"
	c.Footer = fmt.Sprintf("Sent because you administer %s and administrators get storage warnings. Owners and administrators can change this under Administration → Mailboxes → Storage warnings.", n.OrgName)
	c.Summary = c.Usage + " " + c.Paragraphs[0]
	return c
}

func (c quotaCopy) text() string {
	var b strings.Builder
	b.WriteString(c.Heading + "\r\n\r\n" + c.Usage + "\r\n\r\n")
	for _, p := range c.Paragraphs {
		b.WriteString(p + "\r\n\r\n")
	}
	if len(c.Folders) > 0 {
		b.WriteString("Largest folders:\r\n")
		for _, f := range c.Folders {
			b.WriteString("  " + f.Name + ": " + f.Size + "\r\n")
		}
		b.WriteString("\r\n")
	}
	b.WriteString(c.Button + ": " + c.URL + "\r\n\r\n-- \r\n" + c.Footer + "\r\n")
	return b.String()
}

func formatBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value, unit := float64(n)/1024, 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if value < 10 {
		return strconv.FormatFloat(value, 'f', 1, 64) + " " + units[unit]
	}
	return strconv.FormatFloat(value, 'f', 0, 64) + " " + units[unit]
}

const quotaFont = `-apple-system,BlinkMacSystemFont,'Segoe UI',Inter,Helvetica,Arial,sans-serif`

var quotaMailHTML = template.Must(template.New("quota").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>
<body style="margin:0;padding:0;background-color:#f5f4f2">
<div style="display:none;max-height:0;color:#f5f4f2">{{.Summary}}</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#f5f4f2" style="background-color:#f5f4f2">
<tr><td align="center" style="padding:32px 16px">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:560px">
<tr><td style="padding:0 8px 16px;font-family:` + quotaFont + `;font-size:13px;line-height:1.5;font-weight:600;color:#78716a">{{.Brand}}</td></tr>
<tr><td bgcolor="#ffffff" style="background-color:#ffffff;border:1px solid #e7e5e2;border-radius:12px;padding:32px;font-family:` + quotaFont + `;font-size:15px;line-height:1.6;color:#2a2724">
<h1 style="margin:0 0 8px;font-family:Archivo,Helvetica,Arial,sans-serif;font-size:22px;line-height:1.3;font-weight:700;color:#2a2724">{{.Heading}}</h1>
<p style="margin:0 0 24px;color:#57534d">{{.Usage}}</p>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 24px"><tr>
<td width="{{.Fill}}%" height="8" bgcolor="{{.Color}}" style="height:8px;background-color:{{.Color}};border-radius:4px;font-size:0;line-height:0">&nbsp;</td>
{{- if lt .Fill 100}}<td height="8" bgcolor="#ece9e5" style="height:8px;background-color:#ece9e5;border-radius:4px;font-size:0;line-height:0">&nbsp;</td>{{end}}
</tr></table>
{{- range .Paragraphs}}
<p style="margin:0 0 16px">{{.}}</p>
{{- end}}
{{- if .Folders}}
<p style="margin:24px 0 8px;font-size:12px;line-height:1.5;font-weight:600;color:#78716a">Largest folders</p>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 8px;border-top:1px solid #e7e5e2">
{{- range .Folders}}
<tr><td style="padding:8px 0;border-bottom:1px solid #e7e5e2;font-size:14px;color:#2a2724">{{.Name}}</td><td align="right" style="padding:8px 0;border-bottom:1px solid #e7e5e2;font-size:14px;color:#57534d;white-space:nowrap">{{.Size}}</td></tr>
{{- end}}
</table>
{{- end}}
<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:24px 0 0"><tr>
<td bgcolor="#b85110" style="background-color:#b85110;border-radius:8px"><a href="{{.URL}}" style="display:inline-block;padding:12px 20px;font-family:` + quotaFont + `;font-size:15px;line-height:1.2;font-weight:600;color:#ffffff;text-decoration:none">{{.Button}}</a></td>
</tr></table>
</td></tr>
<tr><td style="padding:16px 8px 0;font-family:` + quotaFont + `;font-size:12px;line-height:1.5;color:#78716a">{{.Footer}}</td></tr>
</table>
</td></tr>
</table>
</body>
</html>
`))
