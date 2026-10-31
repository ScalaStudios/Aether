package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	markACLPendingBase  = `acl_pending = true, acl_generation = acl_generation + 1, acl_pending_since = coalesce(acl_pending_since, now()), acl_attempts = 0, acl_next_attempt_at = `
	markACLPending      = markACLPendingBase + `CASE WHEN acl_pending THEN least(acl_next_attempt_at, now()) ELSE now() END`
	markACLPendingNow   = markACLPendingBase + `now()`
	claimACLStmt        = `UPDATE mailboxes m SET acl_lease_token = gen_random_uuid(), acl_lease_until = now() + interval '2 minutes' WHERE id = `
	claimACLReturn      = ` RETURNING id, acl_lease_token, address, kind, acl_pending, acl_generation, acl_attempts, active AND EXISTS (SELECT 1 FROM domains d WHERE d.id = m.domain_id AND d.active AND d.verified_at IS NOT NULL)`
	aclOperationTimeout = 60 * time.Second
	aclCleanupTimeout   = 10 * time.Second
)

var (
	accessRetryDelays  = []time.Duration{15 * time.Second, time.Minute, 5 * time.Minute}
	errMailUnavailable = errors.New("the mail server could not be reached")
	errACLLeaseLost    = errors.New("another process took over the mailbox ACL lease")
)

type aclClaim struct {
	id, token, address, kind string
	openable, pending        bool
	generation               int64
	attempts                 int
}

func retryDelay(attempts int) time.Duration {
	return accessRetryDelays[min(attempts, len(accessRetryDelays)-1)]
}

func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), aclCleanupTimeout)
}

func (s *Server) claimACLWhere(ctx context.Context, where string, args ...any) (*aclClaim, error) {
	var c aclClaim
	err := s.db.QueryRow(ctx, claimACLStmt+where+claimACLReturn, args...).
		Scan(&c.id, &c.token, &c.address, &c.kind, &c.pending, &c.generation, &c.attempts, &c.openable)
	return &c, err
}

func (s *Server) claimACL(ctx context.Context, mailboxID string) (*aclClaim, error) {
	for {
		c, err := s.claimACLWhere(ctx, `$1 AND (acl_lease_until IS NULL OR acl_lease_until < now())`, mailboxID)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT true FROM mailboxes WHERE id = $1`, mailboxID).Scan(&exists); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *Server) claimDueACL(ctx context.Context) (*aclClaim, error) {
	return s.claimACLWhere(ctx, `(SELECT p.id FROM mailboxes p JOIN domains d ON d.id = p.domain_id
		WHERE p.acl_pending AND p.active AND d.active AND d.verified_at IS NOT NULL AND p.acl_next_attempt_at <= now() AND (p.acl_lease_until IS NULL OR p.acl_lease_until < now())
		ORDER BY p.acl_next_attempt_at, p.id LIMIT 1 FOR UPDATE OF p SKIP LOCKED)`)
}

func (s *Server) renewACL(ctx context.Context, c *aclClaim) func() error {
	return func() error {
		tag, err := s.db.Exec(ctx, `UPDATE mailboxes SET acl_lease_until = now() + interval '2 minutes' WHERE id = $1 AND acl_lease_token = $2`, c.id, c.token)
		if err == nil && tag.RowsAffected() == 0 {
			return errACLLeaseLost
		}
		return err
	}
}

func (s *Server) releaseACL(ctx context.Context, c *aclClaim, synced bool) error {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	attempted := c.openable && c.pending
	tag, err := s.db.Exec(ctx, `
		UPDATE mailboxes SET acl_lease_token = NULL, acl_lease_until = NULL,
			acl_pending = acl_pending AND NOT ($3 AND acl_generation = $4),
			acl_pending_since = CASE WHEN $3 AND acl_generation = $4 THEN NULL ELSE acl_pending_since END,
			acl_attempts = CASE WHEN $3 THEN 0 WHEN $5 THEN acl_attempts + 1 ELSE acl_attempts END,
			acl_next_attempt_at = CASE WHEN acl_generation <> $4 THEN now() WHEN $5 AND NOT $3 THEN now() + make_interval(secs => $6) ELSE acl_next_attempt_at END
		WHERE id = $1 AND acl_lease_token = $2`, c.id, c.token, synced, c.generation, attempted, retryDelay(c.attempts).Seconds())
	if err == nil && tag.RowsAffected() == 0 {
		return errors.Join(errACLLeaseLost, s.remarkACL(ctx, c.id))
	}
	return err
}

func (s *Server) remarkACL(ctx context.Context, mailboxID string) error {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	_, err := s.db.Exec(ctx, `UPDATE mailboxes SET `+markACLPendingNow+` WHERE id = $1`, mailboxID)
	return err
}

func (s *Server) syncMailboxACL(ctx context.Context, mailboxID string) error {
	ctx, cancel := context.WithTimeout(ctx, aclOperationTimeout)
	defer cancel()
	c, err := s.claimACL(ctx, mailboxID)
	if err != nil {
		return err
	}
	return s.reconcileACL(ctx, c)
}

func (s *Server) reconcileACL(ctx context.Context, c *aclClaim) error {
	var err error
	synced := false
	if c.openable && c.pending {
		err = s.syncClaimedACL(ctx, c)
		synced = err == nil
	}
	return errors.Join(err, s.releaseACL(ctx, c, synced))
}

func (s *Server) syncClaimedACL(ctx context.Context, c *aclClaim) error {
	members := map[string]imap.RightSet{}
	if c.kind == "shared" {
		rows, err := s.db.Query(ctx, `SELECT u.email, md.access FROM mailbox_delegates md JOIN users u ON u.id = md.user_id WHERE md.mailbox_id = $1 AND u.status = 'active'`, c.id)
		if err != nil {
			return err
		}
		var email, access string
		if _, err := pgx.ForEachRow(rows, []any{&email, &access}, func() error {
			members[email] = mailpkg.FullRights
			if access == "read" {
				members[email] = mailpkg.ReadRights
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return s.mail.SyncACL(ctx, c.address, members, s.renewACL(ctx, c))
}

func (s *Server) syncPendingACLs(ctx context.Context) {
	for range 20 {
		c, err := s.syncDueACL(ctx)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return
		case c == nil:
			s.log.Error("claiming a pending mailbox ACL failed", "err", err)
			return
		case err != nil:
			s.log.Warn("mailbox ACL sync failed; retrying", "mailbox", c.id, "err", err)
		}
	}
}

func (s *Server) syncDueACL(ctx context.Context) (*aclClaim, error) {
	ctx, cancel := context.WithTimeout(ctx, aclOperationTimeout)
	defer cancel()
	c, err := s.claimDueACL(ctx)
	if err != nil {
		return nil, err
	}
	return c, s.reconcileACL(ctx, c)
}

func (s *Server) revokeMailAccess(ctx context.Context, tx pgx.Tx, userID string, mailboxIDs []string) error {
	if len(mailboxIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE app_passwords a SET revoked_at = now() FROM mailboxes m
		WHERE m.id = a.mailbox_id AND a.user_id = $1 AND a.mailbox_id = ANY($2) AND a.revoked_at IS NULL AND NOT `+signInAsMailbox("$1"), userID, mailboxIDs); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT m.address FROM mailboxes m JOIN users u ON u.id = $1
		WHERE m.id = ANY($2) AND m.kind = 'user' AND (u.status <> 'active' OR NOT `+signInAsMailbox("$1")+`)
		UNION SELECT email FROM users WHERE id = $1`, userID, mailboxIDs)
	if err != nil {
		return err
	}
	kick, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE mailboxes SET `+markACLPending+` WHERE id = ANY($1) AND kind = 'shared'`, mailboxIDs); err != nil {
		return err
	}
	return queueKicks(ctx, tx, kick)
}

func userMailboxIDs(ctx context.Context, tx pgx.Tx, userID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM mailboxes WHERE owner_user_id = $1 UNION SELECT mailbox_id FROM mailbox_delegates WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func queueKicks(ctx context.Context, tx pgx.Tx, identities []string) error {
	slices.Sort(identities)
	for _, identity := range slices.Compact(identities) {
		if _, err := tx.Exec(ctx, `INSERT INTO mail_kicks (identity) VALUES ($1) ON CONFLICT (identity) DO UPDATE SET generation = mail_kicks.generation + 1, next_attempt_at = now()`, identity); err != nil {
			return err
		}
	}
	return nil
}

func wake(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *Server) kickMailSessions(ctx context.Context) {
	for range 100 {
		var id, identity string
		var generation int64
		var attempts int
		err := s.db.QueryRow(ctx, `
			UPDATE mail_kicks SET next_attempt_at = now() + interval '30 seconds'
			WHERE identity = (SELECT identity FROM mail_kicks WHERE next_attempt_at <= now() ORDER BY next_attempt_at, identity LIMIT 1 FOR UPDATE SKIP LOCKED)
			RETURNING id, identity, generation, attempts`).Scan(&id, &identity, &generation, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			s.log.Error("claiming a pending mail kick failed", "err", err)
			return
		}
		if err := s.mail.KickSessions(ctx, identity); err != nil {
			s.log.Warn("ending mail sessions failed; retrying", "user", identity, "err", err)
			if _, err := s.db.Exec(ctx, `UPDATE mail_kicks SET attempts = attempts + 1, last_error = $2, next_attempt_at = now() + make_interval(secs => $3) WHERE id = $1 AND generation = $4`,
				id, truncate(err.Error(), 500), retryDelay(attempts).Seconds(), generation); err != nil {
				s.log.Error("recording a failed mail kick failed", "user", identity, "err", err)
			}
			continue
		}
		if _, err := s.db.Exec(ctx, `DELETE FROM mail_kicks WHERE id = $1 AND generation = $2`, id, generation); err != nil {
			s.log.Error("removing a completed mail kick failed", "user", identity, "err", err)
		}
	}
}

func (s *Server) retireMailbox(ctx context.Context, m *mailbox, remove bool) error {
	ctx, cancel := context.WithTimeout(ctx, aclOperationTimeout)
	defer cancel()
	c, err := s.claimACL(ctx, m.ID)
	if err != nil {
		return err
	}
	if c.openable && (c.kind == "shared" || c.pending) {
		if err := s.mail.SyncACL(ctx, c.address, nil, s.renewACL(ctx, c)); err != nil {
			return errors.Join(fmt.Errorf("%w: %w", errMailUnavailable, err), s.releaseACL(ctx, c, false))
		}
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		kick := []string{c.address}
		if c.kind == "shared" {
			rows, err := tx.Query(ctx, `SELECT u.email FROM mailbox_delegates md JOIN users u ON u.id = md.user_id WHERE md.mailbox_id = $1`, m.ID)
			if err != nil {
				return err
			}
			members, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			kick = append(kick, members...)
		}
		stmt := `UPDATE mailboxes SET active = false, acl_lease_token = NULL, acl_lease_until = NULL WHERE id = $1 AND acl_lease_token = $2`
		if remove {
			stmt = `DELETE FROM mailboxes WHERE id = $1 AND acl_lease_token = $2`
		}
		tag, err := tx.Exec(ctx, stmt, m.ID, c.token)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errACLLeaseLost
		}
		return queueKicks(ctx, tx, kick)
	})
	if err != nil {
		return errors.Join(err, s.remarkACL(ctx, m.ID), s.releaseACL(ctx, c, false))
	}
	wake(s.kickWake)
	return nil
}

func (s *Server) retireError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errMailUnavailable), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "mail_unavailable", "The mail server could not be reached to remove the members' access. Nothing was changed; try again.")
	case errors.Is(err, pgx.ErrNoRows):
		notFound(w)
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) RunAccessWorkers(ctx context.Context) {
	go s.runAccessWorker(ctx, nil, s.syncPendingACLs)
	s.runAccessWorker(ctx, s.kickWake, s.kickMailSessions)
}

func (s *Server) runAccessWorker(ctx context.Context, signal <-chan struct{}, run func(context.Context)) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		s.refreshAccessBacklog(ctx)
		run(ctx)
		s.refreshAccessBacklog(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-signal:
		}
	}
}

func (s *Server) refreshAccessBacklog(ctx context.Context) {
	var kicks, acls float64
	if err := s.db.QueryRow(ctx, `SELECT
		(SELECT coalesce(extract(epoch FROM now() - min(created_at)), 0)::float8 FROM mail_kicks),
		(SELECT coalesce(extract(epoch FROM now() - min(acl_pending_since)), 0)::float8 FROM mailboxes WHERE acl_pending AND active)`).Scan(&kicks, &acls); err == nil {
		kickBacklog.Set(kicks)
		aclBacklog.Set(acls)
	}
}
