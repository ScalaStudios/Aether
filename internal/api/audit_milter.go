package api

import (
	"context"
	"errors"
	"mime"
	"net"
	"net/textproto"
	"strings"
	"time"

	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-milter"
	"github.com/jackc/pgx/v5"
)

var headerDecoder = &mime.WordDecoder{CharsetReader: charset.Reader}

func (s *Server) ServeAuditMilter(ln net.Listener) error {
	srv := &milter.Server{
		NewMilter: func() milter.Milter { return &auditMilter{s: s} },
		Protocol:  milter.OptNoConnect | milter.OptNoHelo | milter.OptNoBody,
	}
	return srv.Serve(ln)
}

type smtpSend struct {
	Login, From, IP    string
	Recipients         int
	Subject, MessageID string
}

type auditMilter struct {
	milter.NoOpMilter
	s    *Server
	send smtpSend
}

func (a *auditMilter) MailFrom(from string, m *milter.Modifier) (milter.Response, error) {
	a.send = smtpSend{Login: strings.ToLower(m.Macros["{auth_authen}"]), From: strings.ToLower(strings.Trim(from, "<>")), IP: m.Macros["{client_addr}"]}
	if a.send.Login == "" || strings.Contains(a.send.Login, "*") || a.send.From == "" {
		return milter.RespAccept, nil
	}
	return milter.RespContinue, nil
}

func (a *auditMilter) RcptTo(string, *milter.Modifier) (milter.Response, error) {
	a.send.Recipients++
	return milter.RespContinue, nil
}

func (a *auditMilter) Headers(h textproto.MIMEHeader, m *milter.Modifier) (milter.Response, error) {
	subject := h.Get("Subject")
	if decoded, err := headerDecoder.DecodeHeader(subject); err == nil {
		subject = decoded
	}
	a.send.Subject, a.send.MessageID = strings.TrimSpace(subject), strings.TrimSpace(h.Get("Message-Id"))
	return milter.RespContinue, nil
}

func (a *auditMilter) Body(m *milter.Modifier) (milter.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.s.recordSMTPSend(ctx, a.send)
	return milter.RespAccept, nil
}

func (s *Server) recordSMTPSend(ctx context.Context, send smtpSend) {
	var actorID, actorEmail, mailboxID, orgID, kind string
	var owner *string
	err := s.db.QueryRow(ctx, `
		SELECT u.id, u.email, t.id, t.org_id, t.kind, t.owner_user_id
		FROM mailboxes l JOIN users u ON u.id = l.owner_user_id JOIN mailboxes t ON t.address = $2
		WHERE l.address = $1`, send.Login, send.From).Scan(&actorID, &actorEmail, &mailboxID, &orgID, &kind, &owner)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("resolving an SMTP sender for the mailbox access audit failed", "err", err)
		}
		return
	}
	role := "delegate"
	switch {
	case owner != nil && *owner == actorID:
		role = "owner"
	case kind == "shared":
		role = "member"
	}
	st, err := s.loadMailboxAudit(ctx, orgID)
	if err != nil || !st.covers(role, "message.send_as") {
		return
	}
	s.writeAccess(ctx, accessRecord{
		OrgID: orgID, MailboxID: mailboxID, Address: send.From, ActorID: &actorID, ActorEmail: actorEmail, Role: role,
		Client: "smtp", IP: send.IP, Action: "message.send_as",
		Items: []accessItem{{Subject: send.Subject, MessageID: send.MessageID}}, Meta: map[string]any{"recipients": send.Recipients},
	})
}
