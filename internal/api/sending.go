package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

type sendingCaps struct {
	MessagesPerHour      int `json:"messagesPerHour"`
	MessagesPerDay       int `json:"messagesPerDay"`
	RecipientsPerMessage int `json:"recipientsPerMessage"`
	RecipientsPerDay     int `json:"recipientsPerDay"`
	NewRecipientsPerDay  int `json:"newRecipientsPerDay"`
	SpamPercent          int `json:"spamPercent"`
}

var defaultSendingCaps = sendingCaps{MessagesPerHour: 200, MessagesPerDay: 1000, RecipientsPerMessage: 100, RecipientsPerDay: 2000, SpamPercent: 50}

var sendingLimitMax = map[string]int{
	"messagesPerHour": 100000, "messagesPerDay": 1000000, "recipientsPerMessage": 1000,
	"recipientsPerDay": 1000000, "newRecipientsPerDay": 1000000, "spamPercent": 100,
}

var sendingLimitNames = map[string]string{
	"messagesPerHour": "messages an hour", "messagesPerDay": "messages a day",
	"recipientsPerDay": "recipients a day", "newRecipientsPerDay": "first-time recipients a day",
}

const spamSampleMessages = 10

func sendingLimitsProblem(limits map[string]int) string {
	for k, v := range limits {
		max, ok := sendingLimitMax[k]
		if !ok {
			return "Unknown limit " + strconv.Quote(k) + "."
		}
		if v < 0 || v > max {
			return fmt.Sprintf("Set %s between 0 and %d, or leave it empty to inherit.", k, max)
		}
	}
	return ""
}

func effectiveSendingCaps(merged []byte) sendingCaps {
	caps := defaultSendingCaps
	if err := json.Unmarshal(merged, &caps); err != nil {
		return defaultSendingCaps
	}
	return caps
}

type sendingCount struct {
	limit string
	count int64
	max   int
	key   string
	ttl   time.Duration
}

func overLimit(counts []sendingCount) (exceeded *sendingCount, near []sendingCount) {
	for _, c := range counts {
		switch {
		case c.max <= 0:
		case c.count > int64(c.max):
			return &c, nil
		case c.count*5 >= int64(c.max)*4:
			near = append(near, c)
		}
	}
	return nil, near
}

func spamExceeded(percent int, messages, spam int64) bool {
	return percent > 0 && messages >= spamSampleMessages && spam*100 >= int64(percent)*messages
}

func sendingDay(now time.Time) string {
	return strconv.FormatInt(now.Unix()/86400, 10)
}

func sendingKeys(mailboxID, login string, now time.Time) []string {
	prefix := "aether:sending:" + mailboxID + ":"
	day := sendingDay(now)
	return []string{prefix + "mh:" + strconv.FormatInt(now.Unix()/3600, 10), prefix + "md:" + day, prefix + "rd:" + day, prefix + "nd:" + day, "aether:sending:spam:" + login + ":" + day}
}

func describeSendingLimit(c sendingCount, messages int64) string {
	if c.limit == "spamPercent" {
		return fmt.Sprintf("%d of its %d messages today were scored as spam, more than the %d%% allowed", c.count, messages, c.max)
	}
	return fmt.Sprintf("it sent %d of its %d %s", c.count, c.max, sendingLimitNames[c.limit])
}

func (s *Server) sendingPolicy(ctx context.Context, login, instance, recipient string) (string, error) {
	login = strings.ToLower(login)
	if login == "" {
		return "DUNNO", nil
	}
	var mailboxID, orgID string
	var restricted bool
	var merged []byte
	err := s.db.QueryRow(ctx, `
		SELECT m.id, m.org_id, m.sending_restricted_at IS NOT NULL, o.sending_limits || d.sending_limits || m.sending_limits
		FROM mailboxes m JOIN domains d ON d.id = m.domain_id JOIN organizations o ON o.id = m.org_id
		WHERE m.address = $1`, login).Scan(&mailboxID, &orgID, &restricted, &merged)
	if errors.Is(err, pgx.ErrNoRows) {
		return "DUNNO", nil
	}
	if err != nil {
		return "", err
	}
	if restricted {
		return "REJECT 5.7.1 Sending from this account is restricted. Contact your administrator.", nil
	}
	caps := effectiveSendingCaps(merged)
	msgKey := "aether:sending:" + mailboxID + ":msg:" + instance
	pipe := s.kv.TxPipeline()
	inMessage := pipe.Incr(ctx, msgKey)
	pipe.Expire(ctx, msgKey, time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", err
	}
	if caps.RecipientsPerMessage > 0 && inMessage.Val() > int64(caps.RecipientsPerMessage) {
		return fmt.Sprintf("452 4.5.3 Send to %d recipients or fewer per message", caps.RecipientsPerMessage), nil
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO sending_recipients (mailbox_id, recipient) VALUES ($1, $2) ON CONFLICT DO NOTHING`, mailboxID, strings.ToLower(recipient))
	if err != nil {
		return "", err
	}
	keys := sendingKeys(mailboxID, login, time.Now())
	counts := []sendingCount{
		{"messagesPerHour", 0, caps.MessagesPerHour, keys[0], 2 * time.Hour},
		{"messagesPerDay", 0, caps.MessagesPerDay, keys[1], 48 * time.Hour},
		{"recipientsPerDay", 0, caps.RecipientsPerDay, keys[2], 48 * time.Hour},
		{"newRecipientsPerDay", 0, caps.NewRecipientsPerDay, keys[3], 48 * time.Hour},
	}
	if inMessage.Val() > 1 {
		counts = counts[2:]
	}
	if tag.RowsAffected() == 0 {
		counts = counts[:len(counts)-1]
	}
	pipe = s.kv.TxPipeline()
	cmds := make([]*redis.IntCmd, len(counts))
	for i, c := range counts {
		cmds[i] = pipe.Incr(ctx, c.key)
		pipe.Expire(ctx, c.key, c.ttl)
	}
	spamCmd := pipe.Get(ctx, keys[4])
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return "", err
	}
	for i := range counts {
		counts[i].count = cmds[i].Val()
	}
	exceeded, near := overLimit(counts)
	var messages int64
	if inMessage.Val() == 1 {
		messages = counts[1].count
		spam, _ := spamCmd.Int64()
		if exceeded == nil && spamExceeded(caps.SpamPercent, messages, spam) {
			exceeded = &sendingCount{limit: "spamPercent", count: spam, max: caps.SpamPercent}
		}
	}
	if exceeded != nil {
		s.restrictSender(ctx, mailboxID, orgID, login, *exceeded, messages)
		return "REJECT 5.7.1 This account exceeded its sending limit and is restricted. Contact your administrator.", nil
	}
	for _, c := range near {
		if ok, err := s.kv.SetNX(ctx, c.key+":near", 1, c.ttl).Result(); err == nil && ok {
			s.audit(ctx, nil, nil, &orgID, "mailbox.sending_limit_near", "mailbox", mailboxID, map[string]any{"address": login, "limit": c.limit, "count": c.count, "max": c.max})
			s.noticeAdmins(orgID, "Sending limit nearly reached by "+login,
				fmt.Sprintf("%s is close to a sending limit: %s.\r\n\r\nAether restricts the account and stops its outgoing mail if it goes over the limit.", login, describeSendingLimit(c, 0)))
		}
	}
	return "DUNNO", nil
}

func (s *Server) restrictSender(ctx context.Context, mailboxID, orgID, login string, c sendingCount, messages int64) {
	tag, err := s.db.Exec(ctx, `UPDATE mailboxes SET sending_restricted_at = now(), sending_restriction = $2 WHERE id = $1 AND sending_restricted_at IS NULL`, mailboxID, c.limit)
	if err != nil {
		s.log.Error("restricting sender failed", "mailbox", mailboxID, "err", err)
		return
	}
	if tag.RowsAffected() == 0 {
		return
	}
	s.log.Warn("sender restricted", "mailbox", mailboxID, "limit", c.limit, "count", c.count, "max", c.max)
	s.audit(ctx, nil, nil, &orgID, "mailbox.sending_restricted", "mailbox", mailboxID, map[string]any{"address": login, "limit": c.limit, "count": c.count, "max": c.max})
	s.noticeAdmins(orgID, "Sending restricted for "+login,
		fmt.Sprintf("Aether stopped outgoing mail from %s because %s.\r\n\r\nIf the account may be compromised, reset its password and revoke its app passwords. Then release it under Administration, Sending.", login, describeSendingLimit(c, messages)))
}

func (s *Server) noticeAdmins(orgID, subject, text string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		rows, err := s.db.Query(ctx, `
			SELECT DISTINCT ON (om.user_id) m.address, m.display_name
			FROM organization_members om
			JOIN users u ON u.id = om.user_id AND u.status = 'active'
			JOIN mailboxes m ON m.owner_user_id = om.user_id AND m.kind = 'user' AND m.active
			WHERE om.org_id = $1 AND om.role IN ('owner', 'admin')
			ORDER BY om.user_id, m.org_id = $1 DESC, m.created_at`, orgID)
		if err != nil {
			s.log.Error("loading administrators to notify failed", "org", orgID, "err", err)
			return
		}
		admins, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mailpkg.Address, error) {
			var a mailpkg.Address
			return a, row.Scan(&a.Address, &a.Name)
		})
		if err != nil {
			s.log.Error("loading administrators to notify failed", "org", orgID, "err", err)
			return
		}
		for _, a := range admins {
			out := &mailpkg.Outgoing{From: mailpkg.Address{Name: "Aether", Address: "postmaster@" + s.cfg.MailHostname}, To: []mailpkg.Address{a}, Subject: subject, Text: text + "\r\n"}
			raw, _, err := out.Build(s.cfg.MailHostname)
			if err == nil {
				err = s.mail.Deliver(a.Address, raw)
			}
			if err != nil {
				s.log.Warn("delivering administrator notice failed", "org", orgID, "to", a.Address, "err", err)
			}
		}
	}()
}

type sendingOverrides struct {
	Defaults     sendingCaps               `json:"defaults"`
	Organization map[string]int            `json:"organization"`
	Domains      map[string]map[string]int `json:"domains"`
	Mailboxes    map[string]map[string]int `json:"mailboxes"`
}

func (s *Server) getSendingLimits(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	out := sendingOverrides{Defaults: defaultSendingCaps, Domains: map[string]map[string]int{}, Mailboxes: map[string]map[string]int{}}
	if err := s.db.QueryRow(r.Context(), `SELECT sending_limits FROM organizations WHERE id = $1`, orgID).Scan(&out.Organization); err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT 'domain', id, sending_limits FROM domains WHERE org_id = $1 AND sending_limits <> '{}'
		UNION ALL
		SELECT 'mailbox', id, sending_limits FROM mailboxes WHERE org_id = $1 AND sending_limits <> '{}'`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var scope, id string
		var limits map[string]int
		if err := rows.Scan(&scope, &id, &limits); err != nil {
			s.internalError(w, r, err)
			return
		}
		if scope == "domain" {
			out.Domains[id] = limits
		} else {
			out.Mailboxes[id] = limits
		}
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveSendingLimits(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		DomainID  *string        `json:"domainId"`
		MailboxID *string        `json:"mailboxId"`
		Limits    map[string]int `json:"limits"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if msg := sendingLimitsProblem(in.Limits); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_limits", msg)
		return
	}
	if in.Limits == nil {
		in.Limits = map[string]int{}
	}
	table, id := "organizations", orgID
	switch {
	case in.DomainID != nil && in.MailboxID != nil:
		writeError(w, http.StatusUnprocessableEntity, "invalid_scope", "Set limits for a domain or a mailbox, not both.")
		return
	case in.DomainID != nil:
		table, id = "domains", *in.DomainID
	case in.MailboxID != nil:
		table, id = "mailboxes", *in.MailboxID
	}
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_scope", "Choose a domain or mailbox in this organization.")
		return
	}
	scopeOrg := "org_id"
	if table == "organizations" {
		scopeOrg = "id"
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE `+table+` SET sending_limits = $3 WHERE id = $1 AND `+scopeOrg+` = $2`, id, orgID, in.Limits)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_scope", "Choose a domain or mailbox in this organization.")
		return
	}
	s.audit(r.Context(), r, p, &orgID, "sending_limits.update", strings.TrimSuffix(table, "s"), id, map[string]any{"limits": in.Limits})
	writeJSON(w, http.StatusOK, in.Limits)
}

type restrictedSender struct {
	MailboxID    string    `json:"mailboxId"`
	Address      string    `json:"address"`
	DisplayName  string    `json:"displayName"`
	RestrictedAt time.Time `json:"restrictedAt"`
	Reason       string    `json:"reason"`
}

func (s *Server) listRestrictedSenders(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT id, address, display_name, sending_restricted_at, coalesce(sending_restriction, '')
		FROM mailboxes WHERE org_id = $1 AND sending_restricted_at IS NOT NULL ORDER BY sending_restricted_at DESC`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[restrictedSender])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) releaseSender(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForPerm(w, r, p, permMailboxesWrite)
	if !ok {
		return
	}
	var reason string
	err := s.db.QueryRow(r.Context(), `
		UPDATE mailboxes n SET sending_restricted_at = NULL, sending_restriction = NULL
		FROM mailboxes o WHERE n.id = $1 AND o.id = n.id AND n.sending_restricted_at IS NOT NULL
		RETURNING coalesce(o.sending_restriction, '')`, m.ID).Scan(&reason)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.kv.Del(r.Context(), sendingKeys(m.ID, strings.ToLower(m.Address), time.Now())...).Err(); err != nil {
		s.log.Warn("clearing sending counters failed", "mailbox", m.ID, "err", err)
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.sending_released", "mailbox", m.ID, map[string]any{"address": m.Address, "limit": reason})
	w.WriteHeader(http.StatusNoContent)
}
