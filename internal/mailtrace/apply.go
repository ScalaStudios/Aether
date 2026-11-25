package mailtrace

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (in *Ingester) apply(ctx context.Context, tx pgx.Tx, r record) error {
	in.latest = r.at
	switch r.kind {
	case kindConnect:
		in.sessions[r.process] = session{client: r.client, ip: r.ip, seen: r.at}
		return nil
	case kindDisconnect:
		delete(in.sessions, r.process)
		return nil
	case kindInboundTLS:
		if s, ok := in.sessions[r.process]; ok {
			s.tls = r.tls
			in.sessions[r.process] = s
		}
		return nil
	case kindOutboundTLS:
		in.peers[r.process] = session{relay: r.relay, tls: r.tls, seen: r.at}
		return nil
	case kindClient, kindPickup:
		return in.create(ctx, tx, r)
	}
	if r.queueID == "" {
		if r.kind == kindAction {
			return in.create(ctx, tx, r)
		}
		return nil
	}
	id, err := in.ensure(ctx, tx, r)
	if err != nil {
		return err
	}
	in.dirty[id] = true
	switch r.kind {
	case kindMessageID:
		_, err = tx.Exec(ctx, `UPDATE mail_trace_messages SET message_id = $2 WHERE id = $1`, id, r.value)
	case kindHeader:
		err = in.header(ctx, tx, id, r)
	case kindQueued:
		_, err = tx.Exec(ctx, `UPDATE mail_trace_messages SET sender = $2, size = $3, recipient_count = $4 WHERE id = $1`, id, r.sender, r.size, r.nrcpt)
	case kindExpired:
		err = event(ctx, tx, id, r, "expired", "")
	case kindRemoved:
		_, err = tx.Exec(ctx, `UPDATE mail_trace_messages SET removed = true WHERE id = $1`, id)
		delete(in.open, r.queueID)
	case kindNotified:
		err = event(ctx, tx, id, r, "notified", "")
	case kindAdmin:
		err = event(ctx, tx, id, r, r.status, "")
		if err == nil && r.status == "deleted" {
			_, err = tx.Exec(ctx, `UPDATE mail_trace_messages SET removed = true WHERE id = $1`, id)
			delete(in.open, r.queueID)
		}
	case kindDelivery:
		k := deliveryKind(r)
		if k == "" {
			return nil
		}
		if p, ok := in.peers[r.process]; ok && p.relay == r.relay {
			r.tls = p.tls
		}
		err = event(ctx, tx, id, r, k, r.recipient)
		if err == nil {
			err = addRecipients(ctx, tx, id, r.origRecipient, r.recipient)
		}
	case kindAction:
		err = in.action(ctx, tx, id, r)
	}
	return err
}

func (in *Ingester) create(ctx context.Context, tx pgx.Tx, r record) error {
	var queueID *string
	if r.queueID != "" {
		queueID = &r.queueID
	}
	var tls *string
	if s, ok := in.sessions[r.process]; ok {
		tls = &s.tls
		if r.client == "" {
			r.client, r.ip = s.client, s.ip
		}
	}
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO mail_trace_messages (queue_id, received_at, service, client_name, client_ip, tls, sasl_username, sender)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		queueID, r.at, r.service, r.client, r.ip, tls, r.sasl, r.sender).Scan(&id)
	if err != nil {
		return err
	}
	in.dirty[id] = true
	if r.kind != kindAction {
		in.open[r.queueID] = id
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE mail_trace_messages SET removed = true WHERE id = $1`, id)
	if err == nil {
		err = in.action(ctx, tx, id, r)
	}
	return err
}

func (in *Ingester) ensure(ctx context.Context, tx pgx.Tx, r record) (int64, error) {
	if id, ok := in.open[r.queueID]; ok {
		return id, nil
	}
	var id int64
	err := tx.QueryRow(ctx, `
		SELECT id FROM mail_trace_messages WHERE queue_id = $1 AND NOT removed AND received_at > $2
		ORDER BY id DESC LIMIT 1`, r.queueID, r.at.Add(-7*24*time.Hour)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			INSERT INTO mail_trace_messages (queue_id, received_at, service) VALUES ($1, $2, $3) RETURNING id`,
			r.queueID, r.at, r.service).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	in.open[r.queueID] = id
	return id, nil
}

func (in *Ingester) header(ctx context.Context, tx pgx.Tx, id int64, r record) error {
	var err error
	milter := r.stage == "milter-header-info"
	switch {
	case r.header == "subject":
		_, err = tx.Exec(ctx, `UPDATE mail_trace_messages SET subject = $2 WHERE id = $1`, id, decodeSubject(r.value))
	case milter && r.header == "x-spam-status":
		if spam, score, ok := spamVerdict(r.value); ok {
			_, err = tx.Exec(ctx, `UPDATE mail_trace_messages SET spam_score = $2, spam = spam OR $3 WHERE id = $1`, id, score, spam)
		}
	case milter && r.header == "x-spam":
		if strings.HasPrefix(strings.ToLower(r.value), "yes") {
			_, err = tx.Exec(ctx, `UPDATE mail_trace_messages SET spam = true WHERE id = $1`, id)
		}
	}
	if err == nil {
		err = addRecipients(ctx, tx, id, r.recipient)
	}
	return err
}

func (in *Ingester) action(ctx context.Context, tx pgx.Tx, id int64, r record) error {
	recipient := ""
	if r.stage == "RCPT" {
		recipient = r.recipient
	}
	err := event(ctx, tx, id, r, actionKinds[r.status], recipient)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE mail_trace_messages SET sender = $2 WHERE id = $1 AND sender = ''`, id, r.sender)
	}
	if err == nil {
		err = addRecipients(ctx, tx, id, r.recipient)
	}
	if err == nil && recipient == "" && r.status != "hold" {
		_, err = tx.Exec(ctx, `UPDATE mail_trace_messages SET removed = true WHERE id = $1`, id)
		delete(in.open, r.queueID)
	}
	return err
}

func event(ctx context.Context, tx pgx.Tx, id int64, r record, kind, recipient string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO mail_trace_events (trace_id, at, kind, recipient, orig_recipient, relay, dsn, detail, tls)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, r.at, kind, recipient, r.origRecipient, r.relay, r.dsn, r.detail, r.tls)
	return err
}

func addRecipients(ctx context.Context, tx pgx.Tx, id int64, addresses ...string) error {
	for _, a := range addresses {
		if a == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE mail_trace_messages SET recipients = recipients || $2::text WHERE id = $1 AND NOT $2 = ANY(recipients)`, id, a); err != nil {
			return err
		}
	}
	return nil
}

func refresh(ctx context.Context, tx pgx.Tx, id int64) error {
	rows, err := tx.Query(ctx, `SELECT kind, recipient, orig_recipient FROM mail_trace_events WHERE trace_id = $1 ORDER BY id`, id)
	if err != nil {
		return err
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (traceEvent, error) {
		var e traceEvent
		return e, row.Scan(&e.kind, &e.recipient, &e.orig)
	})
	if err != nil {
		return err
	}
	var recipients []string
	var spam, removed bool
	var expected int
	err = tx.QueryRow(ctx, `SELECT recipients, spam, removed, recipient_count FROM mail_trace_messages WHERE id = $1`, id).Scan(&recipients, &spam, &removed, &expected)
	if err != nil {
		return err
	}
	status, per := summarize(events, recipients, spam, removed, expected)
	_, err = tx.Exec(ctx, `
		UPDATE mail_trace_messages SET status = $2, recipient_status = $3,
			origin_domain = CASE WHEN sasl_username <> '' OR service = 'pickup' THEN coalesce(substring(sender from '@([^@]+)$'), '') ELSE '' END,
			domains = ARRAY(
				SELECT DISTINCT d FROM unnest(recipients || CASE WHEN sasl_username <> '' OR service = 'pickup' THEN ARRAY[sender] ELSE '{}'::text[] END) a,
				substring(a from '@([^@]+)$') d WHERE d IS NOT NULL)
		WHERE id = $1`, id, status, per)
	return err
}
