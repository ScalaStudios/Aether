package api

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"net/url"
	"text/template"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const maxDigestEntries = 50

var quarantineLabels = map[string]string{
	"spam":                 "Spam",
	"high_confidence_spam": "High-confidence spam",
	"phishing":             "Phishing",
	"malware":              "Malware",
}

type digestEntry struct {
	ID, Category, From, Subject, Received, Link string
}

type digest struct {
	Title, Address, Open string
	Retention, More      int
	Entries              []digestEntry
}

var digestHTML = htmltemplate.Must(htmltemplate.New("digest").Parse(`<!doctype html><html><body style="margin:0;padding:24px;background-color:#fafaf9;font:15px/1.6 -apple-system,'Segoe UI',Roboto,sans-serif;color:#2a2724">
<div style="max-width:640px;margin:0 auto;background-color:#ffffff;border:1px solid #e7e5e2;border-radius:12px;padding:32px">
<h1 style="margin:0 0 8px;font-size:22px;line-height:1.3">{{.Title}}</h1>
<p style="margin:0 0 24px;color:#57534d">These messages to {{.Address}} were held because they look like spam, phishing or malware. They are deleted after {{.Retention}} days unless they are released.</p>
<table style="width:100%;border-collapse:collapse;font-size:14px">
{{range .Entries}}<tr style="border-top:1px solid #e7e5e2">
<td style="padding:12px 0;vertical-align:top"><strong>{{.From}}</strong><br><span style="color:#57534d">{{.Subject}}</span><br><span style="color:#78716a;font-size:13px">{{.Category}} · {{.Received}}</span></td>
<td style="padding:12px 0 12px 16px;vertical-align:top;text-align:right;white-space:nowrap"><a href="{{.Link}}" style="color:#b85110">Review</a></td>
</tr>{{end}}
</table>
{{if .More}}<p style="margin:16px 0 0;color:#57534d">And {{.More}} more.</p>{{end}}
<p style="margin:24px 0 0"><a href="{{.Open}}" style="display:inline-block;background-color:#fa7e26;color:#111111;font-weight:600;text-decoration:none;padding:12px 24px;border-radius:8px">Open quarantine</a></p>
<p style="margin:24px 0 0;color:#78716a;font-size:13px">Releasing a message needs you to sign in. Nothing is released from this email.</p>
</div></body></html>`))

var digestText = template.Must(template.New("digest").Parse(`{{.Title}}

These messages to {{.Address}} were held because they look like spam, phishing or malware. They are deleted after {{.Retention}} days unless they are released.
{{range .Entries}}
{{.From}}
{{.Subject}}
{{.Category}}, {{.Received}}
Review: {{.Link}}
{{end}}{{if .More}}
And {{.More}} more.
{{end}}
Open quarantine: {{.Open}}

Releasing a message needs you to sign in. Nothing is released from this email.
`))

func (s *Server) quarantineLink(mailboxID, itemID string) string {
	q := url.Values{"quarantine": {mailboxID}}
	if itemID != "" {
		q.Set("item", itemID)
	}
	u := s.cfg.PublicURL.JoinPath("mail")
	u.RawQuery = q.Encode()
	return u.String()
}

func buildDigest(d digest) (text, html string, err error) {
	var t, h bytes.Buffer
	if err := digestText.Execute(&t, d); err != nil {
		return "", "", err
	}
	if err := digestHTML.Execute(&h, d); err != nil {
		return "", "", err
	}
	return t.String(), h.String(), nil
}

func (s *Server) sweepQuarantine(ctx context.Context) {
	if _, err := s.db.Exec(ctx, `
		DELETE FROM quarantine_items q USING organizations o LEFT JOIN quarantine_policies p ON p.org_id = o.id
		WHERE q.org_id = o.id AND q.created_at < now() - make_interval(days => coalesce(p.retention_days, 30))`); err != nil {
		s.log.Error("expiring quarantined messages failed", "err", err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM quarantine_messages qm WHERE NOT EXISTS (SELECT 1 FROM quarantine_items q WHERE q.message_id = qm.id)`); err != nil {
		s.log.Error("removing released quarantine copies failed", "err", err)
	}
}

const digestPending = `q.notified_at IS NULL AND q.status = 'held' AND coalesce(p.digest, 'daily') <> 'off'`

func (s *Server) sendQuarantineDigests(ctx context.Context) {
	tried := []string{}
	for range 50 {
		var mailboxID string
		err := s.db.QueryRow(ctx, `
			SELECT q.mailbox_id FROM quarantine_items q JOIN quarantine_messages qm ON qm.id = q.message_id
			JOIN mailboxes m ON m.id = q.mailbox_id JOIN domains d ON d.id = m.domain_id LEFT JOIN quarantine_policies p ON p.org_id = q.org_id
			WHERE `+digestPending+` AND `+quarantineAccess("qm.category")+` <> 'none' AND m.active AND d.active AND d.verified_at IS NOT NULL
			  AND NOT (q.mailbox_id = ANY($1::uuid[]))
			  AND NOT EXISTS (SELECT 1 FROM quarantine_items r WHERE r.mailbox_id = q.mailbox_id
			                  AND r.notified_at > now() - CASE p.digest WHEN 'weekly' THEN interval '7 days' ELSE interval '1 day' END)
			LIMIT 1`, tried).Scan(&mailboxID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				s.log.Error("finding quarantine digests to send failed", "err", err)
			}
			return
		}
		tried = append(tried, mailboxID)
		if err := s.sendQuarantineDigest(ctx, mailboxID); err != nil {
			s.log.Warn("sending a quarantine digest failed", "mailbox", mailboxID, "err", err)
		}
	}
}

func (s *Server) sendQuarantineDigest(ctx context.Context, mailboxID string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT q.id, qm.category, qm.from_address, qm.from_name, qm.subject, qm.received_at
			FROM quarantine_items q JOIN quarantine_messages qm ON qm.id = q.message_id LEFT JOIN quarantine_policies p ON p.org_id = q.org_id
			WHERE q.mailbox_id = $1 AND `+digestPending+` AND `+quarantineAccess("qm.category")+` <> 'none'
			ORDER BY q.created_at DESC FOR UPDATE OF q SKIP LOCKED`, mailboxID)
		if err != nil {
			return err
		}
		var ids []string
		var entries []digestEntry
		var e digestEntry
		var name, from string
		var received time.Time
		_, err = pgx.ForEachRow(rows, []any{&e.ID, &e.Category, &from, &name, &e.Subject, &received}, func() error {
			ids = append(ids, e.ID)
			if len(entries) == maxDigestEntries {
				return nil
			}
			entry := digestEntry{ID: e.ID, Category: quarantineLabels[e.Category], Subject: cmp.Or(e.Subject, "(no subject)"), From: from,
				Received: received.UTC().Format("2 Jan 2006, 15:04 UTC"), Link: s.quarantineLink(mailboxID, e.ID)}
			if name != "" {
				entry.From = name + " <" + from + ">"
			}
			entries = append(entries, entry)
			return nil
		})
		if err != nil || len(ids) == 0 {
			return err
		}
		var address, domain, sender string
		var retention int
		err = tx.QueryRow(ctx, `
			SELECT m.address, d.name, coalesce(nullif(b.name, ''), o.name), coalesce(p.retention_days, 30)
			FROM mailboxes m JOIN domains d ON d.id = m.domain_id JOIN organizations o ON o.id = m.org_id
			LEFT JOIN organization_branding b ON b.org_id = o.id LEFT JOIN quarantine_policies p ON p.org_id = o.id
			WHERE m.id = $1`, mailboxID).Scan(&address, &domain, &sender, &retention)
		if err != nil {
			return err
		}
		title := fmt.Sprintf("%d new messages in quarantine", len(ids))
		if len(ids) == 1 {
			title = "1 new message in quarantine"
		}
		text, html, err := buildDigest(digest{Title: title, Address: address, Open: s.quarantineLink(mailboxID, ""), Retention: retention, More: len(ids) - len(entries), Entries: entries})
		if err != nil {
			return err
		}
		out := &mailpkg.Outgoing{From: mailpkg.Address{Name: sender, Address: "postmaster@" + domain}, To: []mailpkg.Address{{Address: address}}, Subject: title, Text: text, HTML: html}
		raw, _, err := out.Build(s.cfg.MailHostname)
		if err != nil {
			return err
		}
		if err := s.mail.Deliver(address, raw); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE quarantine_items SET notified_at = now() WHERE id = ANY($1)`, ids)
		return err
	})
}
