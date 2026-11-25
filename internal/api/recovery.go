package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func (s *Server) listRecoverable(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	var days int
	if err := s.db.QueryRow(r.Context(), `SELECT deleted_item_retention_days FROM organizations WHERE id = $1`, m.OrgID).Scan(&days); err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := s.mail.Recoverable(r.Context(), m.Address)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	if m.Access == "" {
		s.audit(r.Context(), r, p, &m.OrgID, "mailbox.recoverable.view", "mailbox", m.ID, map[string]any{"address": m.Address, "messages": len(items)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"retentionDays": days, "items": nonNil(items)})
}

func (s *Server) restoreRecoverable(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Items []mailpkg.RecoverableRef `json:"items"`
	}
	if !decode(w, r, 64<<10, &in) {
		return
	}
	if len(in.Items) == 0 || len(in.Items) > 500 || slices.ContainsFunc(in.Items, func(i mailpkg.RecoverableRef) bool {
		return i.UID == 0 || i.Folder == "" || mailpkg.SharedFolder(i.Folder) || strings.HasPrefix(i.Folder, mailpkg.ExpungedPrefix)
	}) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_messages", "Choose between 1 and 500 messages.")
		return
	}
	var held bool
	if err := s.db.QueryRow(r.Context(), `SELECT `+mailboxHeld("$1"), m.ID).Scan(&held); err != nil {
		s.internalError(w, r, err)
		return
	}
	n, err := s.mail.Restore(m.Address, in.Items, held)
	if n > 0 {
		var folders []string
		for _, i := range in.Items {
			if !slices.Contains(folders, i.Folder) {
				folders = append(folders, i.Folder)
			}
		}
		s.audit(r.Context(), r, p, &m.OrgID, "mailbox.messages.restore", "mailbox", m.ID, map[string]any{
			"address": m.Address, "folders": folders, "messages": n, "byAdministrator": m.Access == "",
		})
	}
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"restored": n})
}

func (s *Server) purgeRecoverable(ctx context.Context) {
	if ok, err := s.kv.SetNX(ctx, "aether:recoverable-purge", "1", time.Hour).Result(); err != nil || !ok {
		return
	}
	rows, err := s.db.Query(ctx, `
		SELECT mu.id, mu.address, o.deleted_item_retention_days,
			coalesce((SELECT max(r.days) FROM retention_policies r WHERE r.action = 'keep' AND r.org_id = m.org_id AND (r.mailbox_id IS NULL OR r.mailbox_id = m.id)), 0)
		FROM mail_users mu
		JOIN mailboxes m ON m.id = mu.id JOIN organizations o ON o.id = m.org_id`)
	if err != nil {
		s.log.Error("loading mailboxes for deleted item purge failed", "err", err)
		return
	}
	type target struct {
		id, address string
		days, keep  int
	}
	targets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (target, error) {
		var t target
		return t, row.Scan(&t.id, &t.address, &t.days, &t.keep)
	})
	if err != nil {
		s.log.Error("loading mailboxes for deleted item purge failed", "err", err)
		return
	}
	for _, t := range targets {
		hold, err := s.holdScope(ctx, t.id)
		if err == nil {
			err = s.mail.PurgeRecoverable(ctx, t.address, t.days, t.keep, hold)
		}
		if err != nil {
			s.log.Warn("purging expired deleted items failed; retrying in an hour", "address", t.address, "err", err)
		}
	}
}
