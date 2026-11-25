package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/scalastudios/aether/internal/maillog"
)

type suppression struct {
	ID        string    `json:"id"`
	Address   string    `json:"address"`
	Reason    string    `json:"reason"`
	Detail    *string   `json:"detail"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) listSuppressions(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	rows, err := s.db.Query(r.Context(), `
		SELECT id, address, reason, detail, created_at FROM suppressions
		WHERE org_id = $1 AND strpos(address, $2) > 0 ORDER BY created_at DESC LIMIT 200`, orgID, q)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[suppression])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) createSuppression(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		Address string `json:"address"`
		Reason  string `json:"reason"`
		Detail  string `json:"detail"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	list, fe := parseAddresses([]string{in.Address}, false)
	if fe != nil {
		writeError(w, fe.Status, fe.Code, fe.Message)
		return
	}
	if utf8.RuneCountInString(list[0]) > 320 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_address", "Use an email address of up to 320 characters.")
		return
	}
	if in.Reason == "" {
		in.Reason = "manual"
	}
	if in.Reason != "manual" && in.Reason != "complaint" && in.Reason != "bounce" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_reason", "Reason must be manual, complaint, or bounce.")
		return
	}
	var detail *string
	if d := strings.TrimSpace(in.Detail); d != "" {
		if utf8.RuneCountInString(d) > 500 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_detail", "Use a note of up to 500 characters.")
			return
		}
		detail = &d
	}
	var createdBy *string
	if p.apiKey == nil {
		createdBy = &p.User.ID
	}
	var out suppression
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO suppressions (org_id, address, reason, detail, created_by) VALUES ($1, $2, $3, $4, $5)
		RETURNING id, address, reason, detail, created_at`, orgID, list[0], in.Reason, detail, createdBy).
		Scan(&out.ID, &out.Address, &out.Reason, &out.Detail, &out.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, http.StatusConflict, "already_suppressed", list[0]+" is already on the suppression list.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "suppression.create", "suppression", out.ID, map[string]any{"address": out.Address, "reason": out.Reason})
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) deleteSuppression(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "suppressionId")
	if !ok {
		return
	}
	var orgID, address string
	if err := s.db.QueryRow(r.Context(), `SELECT org_id, address FROM suppressions WHERE id = $1`, id).Scan(&orgID, &address); err != nil || !p.canAdminOrg(orgID) {
		notFound(w)
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM suppressions WHERE id = $1`, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "suppression.delete", "suppression", id, map[string]any{"address": address})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) suppressionPolicy(ctx context.Context, recipient, sender string) (string, error) {
	recipient, sender = strings.ToLower(recipient), strings.ToLower(sender)
	at := strings.LastIndexByte(sender, '@')
	if at < 0 || recipient == "" {
		return "DUNNO", nil
	}
	var suppressed bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM suppressions x
			JOIN domains d ON d.org_id = x.org_id
			WHERE d.name = $1 AND d.active AND d.verified_at IS NOT NULL AND x.address = $2
		)`, sender[at+1:], recipient).Scan(&suppressed)
	if err != nil || !suppressed {
		return "DUNNO", err
	}
	return "550 5.7.1 <" + recipient + ">: Recipient is on your organization's suppression list for this sender; remove it from the suppression list to send again", nil
}

func addressOrg(ctx context.Context, tx pgx.Tx, address string) (string, error) {
	at := strings.LastIndexByte(address, '@')
	if at < 0 {
		return "", nil
	}
	var orgID string
	err := tx.QueryRow(ctx, `SELECT org_id FROM domains WHERE name = $1 AND active AND verified_at IS NOT NULL`, address[at+1:]).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return orgID, err
}

func hardBounce(ev maillog.Event) bool {
	if ev.Type != maillog.Bounced {
		return false
	}
	switch ev.DSN {
	case "5.1.1", "5.1.2", "5.1.3", "5.1.6", "5.2.1":
		return true
	default:
		return false
	}
}

func recordMailEvent(ctx context.Context, tx pgx.Tx, ev maillog.Event) error {
	owner := ev.Sender
	if ev.Type == maillog.Received || ev.Type == maillog.Rejected {
		owner = ev.Recipient
	}
	orgID, err := addressOrg(ctx, tx, owner)
	if err != nil || orgID == "" {
		return err
	}
	suppressed := false
	recipient := ev.OriginalRecipient
	if recipient == "" {
		recipient = ev.Recipient
	}
	if local, domain, ok := strings.Cut(recipient, "@"); hardBounce(ev) && ok && local != "" && domain != "" && utf8.RuneCountInString(recipient) <= 320 {
		detail := []rune(strings.TrimSpace(ev.DSN + " " + ev.Reply))
		tag, err := tx.Exec(ctx, `
			INSERT INTO suppressions (org_id, address, reason, detail) VALUES ($1, $2, 'bounce', $3) ON CONFLICT (org_id, address) DO NOTHING`,
			orgID, recipient, string(detail[:min(500, len(detail))]))
		if err != nil {
			return err
		}
		suppressed = tag.RowsAffected() == 1
	}
	sum := sha256.Sum256([]byte(ev.Type + "\n" + ev.Line))
	data := map[string]any{
		"queueId": ev.QueueID, "messageId": ev.MessageID, "from": ev.Sender, "to": ev.Recipient,
		"originalTo": ev.OriginalRecipient, "relay": ev.Relay, "dsn": ev.DSN, "reply": ev.Reply,
	}
	if ev.Type == maillog.Bounced {
		data["suppressed"] = suppressed
	}
	return enqueueWebhooks(ctx, tx, orgID, ev.Type, map[string]any{
		"id": hex.EncodeToString(sum[:16]), "type": ev.Type, "createdAt": ev.Time.UTC(), "organizationId": orgID, "data": data,
	})
}

func (s *Server) FollowMailLog(ctx context.Context, path string) error {
	var saved maillog.Position
	parser := maillog.NewParser()
	err := s.db.QueryRow(ctx, `SELECT inode, "offset", queue FROM mail_log_cursors WHERE path = $1`, path).Scan(&saved.Inode, &saved.Offset, &parser.Queue)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return maillog.Follow(ctx, path, saved, func(lines []string, pos maillog.Position) error {
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			for _, line := range lines {
				for _, ev := range parser.Parse(line) {
					if err := recordMailEvent(ctx, tx, ev); err != nil {
						return err
					}
				}
			}
			queue, err := json.Marshal(parser.Queue)
			if err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO mail_log_cursors (path, inode, "offset", queue) VALUES ($1, $2, $3, $4)
				ON CONFLICT (path) DO UPDATE SET inode = EXCLUDED.inode, "offset" = EXCLUDED."offset", queue = EXCLUDED.queue
				WHERE mail_log_cursors.inode = $5 AND mail_log_cursors."offset" = $6`, path, pos.Inode, pos.Offset, queue, saved.Inode, saved.Offset)
			if err == nil && tag.RowsAffected() != 1 {
				return errors.New("mail log cursor changed while following")
			}
			return err
		})
		saved = pos
		return err
	})
}
