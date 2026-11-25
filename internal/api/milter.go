package api

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-milter"
	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/mailflow"
)

const (
	relayUser      = "aether-sieve-relay"
	stopHeader     = "X-Aether-Flow-Stop"
	scoreHeader    = "X-Aether-Flow-Score"
	tlsHeader      = "X-Aether-Require-TLS"
	maxFlowMessage = 64 << 20
)

var spamScorePattern = regexp.MustCompile(`score=(-?[0-9]+(?:\.[0-9]+)?)`)

func (s *Server) ServeMilter(ln net.Listener, afterSpam bool) error {
	srv := &milter.Server{
		NewMilter: func() milter.Milter { return &flowMilter{s: s, after: afterSpam} },
		Actions:   milter.OptAddHeader | milter.OptChangeHeader | milter.OptChangeBody | milter.OptAddRcpt | milter.OptRemoveRcpt,
		Protocol:  milter.OptNoConnect | milter.OptNoHelo,
	}
	return srv.Serve(ln)
}

type flowMilter struct {
	milter.NoOpMilter
	s       *Server
	after   bool
	relay   bool
	rcpts   []string
	msg     mailflow.Message
	body    bytes.Buffer
	tooLong bool
}

func (f *flowMilter) MailFrom(from string, m *milter.Modifier) (milter.Response, error) {
	user := m.Macros["{auth_authen}"]
	f.msg = mailflow.Message{Sender: strings.ToLower(from), Authenticated: user != ""}
	f.rcpts = nil
	f.body.Reset()
	f.tooLong = false
	f.relay = user == relayUser
	if f.relay && f.after {
		return milter.RespAccept, nil
	}
	return milter.RespContinue, nil
}

func (f *flowMilter) RcptTo(rcpt string, m *milter.Modifier) (milter.Response, error) {
	f.rcpts = append(f.rcpts, rcpt)
	f.msg.Recipients = append(f.msg.Recipients, strings.ToLower(rcpt))
	return milter.RespContinue, nil
}

func (f *flowMilter) Header(name, value string, m *milter.Modifier) (milter.Response, error) {
	f.msg.Header = append(f.msg.Header, mailflow.Field{Name: name, Value: value})
	return milter.RespContinue, nil
}

func (f *flowMilter) BodyChunk(chunk []byte, m *milter.Modifier) (milter.Response, error) {
	if f.body.Len()+len(chunk) > maxFlowMessage {
		f.tooLong = true
	} else {
		f.body.Write(chunk)
	}
	return milter.RespContinue, nil
}

func (f *flowMilter) Abort(m *milter.Modifier) error {
	f.msg = mailflow.Message{}
	f.rcpts = nil
	f.body.Reset()
	return nil
}

func (f *flowMilter) Body(m *milter.Modifier) (milter.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := f.s.runFlow(ctx, f, m)
	if err != nil {
		f.s.log.Error("mail flow rules failed", "sender", f.msg.Sender, "err", err)
		return smtpReply("451 4.3.0 Mail flow rules are unavailable; try again later"), nil
	}
	return resp, nil
}

func smtpReply(text string) milter.Response {
	return milter.NewResponseStr('y', strings.ReplaceAll(text, "%", "%%"))
}

type headerEdit struct {
	index       int
	name, value string
	add         bool
}

type flowEdits struct {
	header []mailflow.Field
	ops    []headerEdit
}

func (e *flowEdits) count(name string) int {
	n := 0
	for _, f := range e.header {
		if strings.EqualFold(f.Name, name) {
			n++
		}
	}
	return n
}

func (e *flowEdits) remove(name string) {
	for i := e.count(name); i >= 1; i-- {
		e.ops = append(e.ops, headerEdit{index: i, name: name})
	}
	e.header = slices.DeleteFunc(e.header, func(f mailflow.Field) bool { return strings.EqualFold(f.Name, name) })
}

func (e *flowEdits) add(name, value string) {
	e.ops = append(e.ops, headerEdit{name: name, value: value, add: true})
	e.header = append(e.header, mailflow.Field{Name: name, Value: value})
}

func (e *flowEdits) set(name, value string) {
	if e.count(name) == 0 {
		if value != "" {
			e.add(name, value)
		}
		return
	}
	e.ops = append(e.ops, headerEdit{index: 1, name: name, value: value})
	for i, f := range e.header {
		if strings.EqualFold(f.Name, name) {
			if value == "" {
				e.header = slices.Delete(e.header, i, i+1)
			} else {
				e.header[i].Value = value
			}
			break
		}
	}
}

func (e *flowEdits) value(name string) string {
	for _, f := range slices.Backward(e.header) {
		if strings.EqualFold(f.Name, name) {
			return f.Value
		}
	}
	return ""
}

func (e *flowEdits) apply(m *milter.Modifier) error {
	for _, op := range e.ops {
		var err error
		if op.add {
			err = m.AddHeader(op.name, op.value)
		} else {
			err = m.ChangeHeader(op.index, op.name, op.value)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) runFlow(ctx context.Context, f *flowMilter, m *milter.Modifier) (milter.Response, error) {
	e := &flowEdits{header: slices.Clone(f.msg.Header)}
	if f.relay {
		if e.count(tlsHeader) > 0 {
			e.remove(tlsHeader)
			e.add(tlsHeader, "yes")
		}
		return milter.RespAccept, e.apply(m)
	}
	var stopped []string
	if f.after {
		if match := spamScorePattern.FindStringSubmatch(e.value("X-Spam-Status")); match != nil {
			f.msg.SpamScore, _ = strconv.ParseFloat(match[1], 64)
		}
		stopped = strings.Fields(e.value(stopHeader))
		e.remove(stopHeader)
		e.remove(scoreHeader)
	} else {
		for _, name := range []string{stopHeader, scoreHeader, tlsHeader, "X-Spam-Status"} {
			e.remove(name)
		}
	}
	if f.tooLong {
		return milter.RespAccept, e.apply(m)
	}
	f.msg.Header = e.header
	f.msg.Body = f.body.Bytes()
	f.msg.Size = f.body.Len()
	for _, h := range e.header {
		f.msg.Size += len(h.Name) + len(h.Value) + 4
	}
	rules, dir, err := s.loadFlow(ctx, &f.msg)
	if err != nil {
		return nil, err
	}
	out := mailflow.Evaluate(&f.msg, rules, dir, f.after, stopped)
	s.logFlowMatches(ctx, &f.msg, out.Matches)
	if out.Reject != "" {
		return smtpReply("550 5.7.1 " + out.Reject), nil
	}
	body := f.msg.Body
	if len(out.Disclaimers) > 0 {
		h := e.header
		for _, d := range out.Disclaimers {
			var ok bool
			if h, body, ok = mailflow.AppendDisclaimer(h, body, d); !ok {
				return smtpReply("550 5.7.1 Organization policy adds a disclaimer to this message, which cannot be done because it is signed or encrypted"), nil
			}
		}
		for _, name := range []string{"Content-Type", "Content-Transfer-Encoding", "MIME-Version"} {
			if v := fieldValue(h, name); v != fieldValue(e.header, name) {
				e.set(name, v)
			}
		}
	}
	for _, name := range out.RemoveHeaders {
		e.remove(name)
	}
	for _, h := range out.AddHeaders {
		e.add(h.Name, h.Value)
	}
	recipients := out.Recipients(f.msg.Recipients)
	if out.RequireTLS {
		hosted, err := s.anyHosted(ctx, recipients)
		if err != nil {
			return nil, err
		}
		if hosted {
			return smtpReply("550 5.7.1 Organization policy requires TLS for this message, which cannot be combined with recipients on this server; send it to them separately"), nil
		}
	}
	if len(out.Approvers) > 0 {
		if err := s.holdMessage(ctx, &f.msg, out, e.header, body, recipients); err != nil {
			return nil, err
		}
		return milter.RespDiscard, nil
	}
	if out.RequireTLS {
		e.add(tlsHeader, "yes")
	}
	if out.SpamScore != 0 && !f.after {
		e.add(scoreHeader, strconv.FormatFloat(out.SpamScore, 'f', 2, 64))
	}
	if len(out.Stopped) > 0 && !f.after {
		e.add(stopHeader, strings.Join(out.Stopped, " "))
	}
	if err := e.apply(m); err != nil {
		return nil, err
	}
	if len(out.Disclaimers) > 0 {
		if err := m.ReplaceBody(body); err != nil {
			return nil, err
		}
	}
	for i, r := range f.msg.Recipients {
		if !slices.Contains(recipients, r) {
			if err := m.DeleteRecipient(f.rcpts[i]); err != nil {
				return nil, err
			}
		}
	}
	for _, r := range recipients {
		if !slices.Contains(f.msg.Recipients, r) {
			if err := m.AddRecipient(r); err != nil {
				return nil, err
			}
		}
	}
	return milter.RespAccept, nil
}

func fieldValue(h []mailflow.Field, name string) string {
	for _, f := range h {
		if strings.EqualFold(f.Name, name) {
			return f.Value
		}
	}
	return ""
}

func domainsOf(addresses ...[]string) []string {
	var out []string
	for _, list := range addresses {
		for _, a := range list {
			if _, d, ok := strings.Cut(a, "@"); ok && !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

func (s *Server) loadFlow(ctx context.Context, msg *mailflow.Message) (map[string][]mailflow.Rule, *mailflow.Directory, error) {
	dir := &mailflow.Directory{Domains: map[string]string{}, Lists: map[string][]string{}}
	rows, err := s.db.Query(ctx, `SELECT name, org_id FROM domains WHERE active AND verified_at IS NOT NULL AND name = ANY($1)`, domainsOf([]string{msg.Sender}, msg.Recipients))
	if err != nil {
		return nil, nil, err
	}
	var name, org string
	if _, err := pgx.ForEachRow(rows, []any{&name, &org}, func() error { dir.Domains[name] = org; return nil }); err != nil {
		return nil, nil, err
	}
	orgs := msg.Orgs(dir)
	rules := map[string][]mailflow.Rule{}
	if len(orgs) == 0 {
		return rules, dir, nil
	}
	rows, err = s.db.Query(ctx, `
		SELECT org_id, id, name, mode, conditions, exceptions, actions, stop FROM mail_flow_rules
		WHERE org_id = ANY($1) AND mode <> 'off' ORDER BY position, created_at`, orgs)
	if err != nil {
		return nil, nil, err
	}
	var r mailflow.Rule
	var lists []string
	_, err = pgx.ForEachRow(rows, []any{&org, &r.ID, &r.Name, &r.Mode, &r.Conditions, &r.Exceptions, &r.Actions, &r.Stop}, func() error {
		rules[org] = append(rules[org], r)
		for _, c := range slices.Concat(r.Conditions, r.Exceptions) {
			if c.Scope == "memberOf" {
				lists = append(lists, c.Values...)
			}
		}
		r = mailflow.Rule{}
		return nil
	})
	if err != nil || len(lists) == 0 {
		return rules, dir, err
	}
	rows, err = s.db.Query(ctx, `SELECT address, destinations FROM aliases WHERE kind = 'list' AND active AND lower(address) = ANY($1)`, lists)
	if err != nil {
		return nil, nil, err
	}
	var members []string
	_, err = pgx.ForEachRow(rows, []any{&name, &members}, func() error {
		for i := range members {
			members[i] = strings.ToLower(members[i])
		}
		dir.Lists[strings.ToLower(name)] = members
		members = nil
		return nil
	})
	return rules, dir, err
}

func (s *Server) anyHosted(ctx context.Context, recipients []string) (bool, error) {
	var hosted bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_domains WHERE name = ANY($1))`, domainsOf(recipients)).Scan(&hosted)
	return hosted, err
}

func storable(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
}

func subjectOf(h []mailflow.Field) string {
	v := mailflow.DecodeHeader(fieldValue(h, "Subject"))
	if len(v) > 300 {
		v = v[:300]
	}
	return storable(v)
}

func (s *Server) logFlowMatches(ctx context.Context, msg *mailflow.Message, matches []mailflow.Match) {
	if len(matches) == 0 {
		return
	}
	subject := subjectOf(msg.Header)
	batch := &pgx.Batch{}
	for _, m := range matches {
		batch.Queue(`INSERT INTO mail_flow_matches (org_id, rule_id, rule_name, test, direction, sender, recipients, subject, actions) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			m.OrgID, m.Rule.ID, m.Rule.Name, m.Test, m.Direction, msg.Sender, msg.Recipients, subject, nonNil(m.Rule.Actions.Summary()))
		s.log.Info("mail flow rule matched", "org", m.OrgID, "rule", m.Rule.Name, "test", m.Test, "direction", m.Direction, "sender", msg.Sender, "actions", m.Rule.Actions.Summary())
	}
	if err := s.db.SendBatch(ctx, batch).Close(); err != nil {
		s.log.Error("recording mail flow matches failed", "err", err)
	}
}

func (s *Server) holdMessage(ctx context.Context, msg *mailflow.Message, out mailflow.Outcome, header []mailflow.Field, body []byte, recipients []string) error {
	var raw bytes.Buffer
	for _, h := range header {
		raw.WriteString(h.Name + ": " + h.Value + "\r\n")
	}
	raw.WriteString("\r\n")
	raw.Write(body)
	var rule *mailflow.Match
	for i := range out.Matches {
		if m := &out.Matches[i]; !m.Test && len(m.Rule.Actions.Approvers) > 0 {
			rule = m
		}
	}
	subject := subjectOf(header)
	preview := storable(mailflow.Preview(&mailflow.Message{Header: header, Body: body}, 2000))
	var id string
	err := s.db.QueryRow(ctx, `
		INSERT INTO mail_flow_held (org_id, rule_id, rule_name, approvers, sender, recipients, subject, preview, require_tls, raw)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		rule.OrgID, rule.Rule.ID, rule.Rule.Name, out.Approvers, msg.Sender, recipients, subject, preview, out.RequireTLS, raw.Bytes()).Scan(&id)
	if err != nil {
		return err
	}
	s.log.Info("mail flow held a message for approval", "id", id, "rule", rule.Rule.Name, "sender", msg.Sender)
	go s.notifyApprovers(id, rule.Rule.Name, msg.Sender, recipients, subject, preview, out.Approvers)
	return nil
}

func (s *Server) notifyApprovers(id, rule, sender string, recipients []string, subject, preview string, approvers []string) {
	text := fmt.Sprintf("%s sent a message to %s that the mail flow rule \"%s\" holds for approval.\n\nSubject: %s\n\n%s\n\nApprove or reject it in Aether under Settings, Messages held by mail flow rules:\n%s/settings\n\nMessages that are not approved within 7 days are discarded.\n",
		sender, strings.Join(recipients, ", "), rule, subject, preview, strings.TrimRight(s.cfg.PublicURL.String(), "/"))
	note := mailpkg.Outgoing{
		From:    mailpkg.Address{Name: "Aether", Address: "postmaster@" + s.cfg.MailHostname},
		Subject: "Approval needed: " + subject,
		Text:    text,
	}
	for _, a := range approvers {
		note.To = append(note.To, mailpkg.Address{Address: a})
	}
	raw, _, err := note.Build(s.cfg.MailHostname)
	if err == nil {
		err = s.mail.Relay(note.From.Address, approvers, raw, nil)
	}
	if err != nil {
		s.log.Error("notifying approvers failed", "held", id, "err", err)
	}
}
