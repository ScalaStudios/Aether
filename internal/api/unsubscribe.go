package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	netmail "net/mail"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/scalastudios/aether/internal/mail"
)

var errUnsafeUnsubscribe = errors.New("unsafe unsubscribe url")

type unsubscribeResponse struct {
	Method string `json:"method"`
	URL    string `json:"url,omitempty"`
}

type newsletterSender struct {
	Address   string    `json:"address"`
	Name      string    `json:"name"`
	Subject   string    `json:"subject"`
	Count     int       `json:"count"`
	LatestUID uint32    `json:"latestUid"`
	Folder    string    `json:"folder"`
	Date      time.Time `json:"date"`
	OneClick  bool      `json:"oneClick"`
	Link      bool      `json:"link"`
	Mailto    bool      `json:"mailto"`
	Blocked   bool      `json:"blocked"`
}

func safeUnsubscribeURL(raw string, allowPrivate bool) (*url.URL, error) {
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00 ") {
		return nil, errUnsafeUnsubscribe
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return nil, errUnsafeUnsubscribe
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !allowPrivate {
			return nil, errUnsafeUnsubscribe
		}
	default:
		return nil, errUnsafeUnsubscribe
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !allowPrivate && !publicAddress(ip) {
		return nil, errUnsafeUnsubscribe
	}
	return u, nil
}

func chooseUnsubscribe(l mail.ListUnsubscribe, allowPrivate bool) (method, target string) {
	links := append([]string{}, l.HTTPS...)
	if allowPrivate {
		links = append(links, l.HTTP...)
	}
	for _, raw := range links {
		if _, err := safeUnsubscribeURL(raw, allowPrivate); err != nil {
			continue
		}
		if l.OneClick {
			return "one-click", raw
		}
		return "link", raw
	}
	for _, raw := range l.Mailto {
		if _, _, _, err := mail.ParseMailto(raw); err == nil {
			return "mailto", raw
		}
	}
	return "", ""
}

func unsubscribeOffer(l mail.ListUnsubscribe, allowPrivate bool) *mail.Unsubscribe {
	switch method, _ := chooseUnsubscribe(l, allowPrivate); method {
	case "one-click":
		return &mail.Unsubscribe{OneClick: true}
	case "link":
		return &mail.Unsubscribe{Link: true}
	case "mailto":
		return &mail.Unsubscribe{Mailto: true}
	default:
		return nil
	}
}

func unsubscribeHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (s *Server) postOneClick(ctx context.Context, raw string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		return errUnsafeUnsubscribe
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Aether")
	resp, err := s.webhookClient.Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddress) {
			return errUnsafeUnsubscribe
		}
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unsubscribe endpoint returned %d", resp.StatusCode)
	}
	return nil
}

func (s *Server) unsubscribeMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return
	}
	m, _, err := s.accessibleMailbox(r.Context(), id, p.User.ID)
	if err != nil {
		notFound(w)
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	if !s.allow(r.Context(), "unsubscribe:"+m.ID, 20) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many unsubscribe requests. Wait a minute and try again.")
		return
	}
	folder := folderParam(r)
	list, err := s.mail.ListUnsubscribe(m.Address, folder, uid)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	method, target := chooseUnsubscribe(list, s.cfg.WebhookAllowPrivate)
	switch method {
	case "one-click":
		if err := s.postOneClick(r.Context(), target); err != nil {
			if errors.Is(err, errUnsafeUnsubscribe) {
				writeError(w, http.StatusUnprocessableEntity, "unsafe_url", "This unsubscribe link points at a private or reserved address.")
				return
			}
			s.log.Warn("one-click unsubscribe failed", "mailbox", m.ID, "host", unsubscribeHost(target), "err", err)
			writeError(w, http.StatusBadGateway, "unsubscribe_failed", "The sender did not confirm the unsubscribe. Try again in a moment.")
			return
		}
		s.audit(r.Context(), r, p, &m.OrgID, "mailbox.unsubscribe", "mailbox", m.ID, map[string]any{"method": "one-click", "host": unsubscribeHost(target), "uid": uid, "folder": folder})
		writeJSON(w, http.StatusOK, unsubscribeResponse{Method: "one-click"})
	case "link":
		s.audit(r.Context(), r, p, &m.OrgID, "mailbox.unsubscribe", "mailbox", m.ID, map[string]any{"method": "link", "host": unsubscribeHost(target), "uid": uid, "folder": folder})
		writeJSON(w, http.StatusOK, unsubscribeResponse{Method: "link", URL: target})
	case "mailto":
		if !m.SendAs && !m.SendOnBehalf {
			writeError(w, http.StatusForbidden, "cannot_send", "Unsubscribing sends a message from this mailbox. Ask an administrator for Send as or Send on behalf.")
			return
		}
		to, subject, body, err := mail.ParseMailto(target)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_unsubscribe", "This message's unsubscribe address cannot be used.")
			return
		}
		out := &mail.Outgoing{
			From:    mail.Address{Name: m.DisplayName, Address: m.Address},
			To:      []mail.Address{{Address: to}},
			Subject: subject,
			Text:    body,
		}
		if !m.SendAs {
			out.Sender = &mail.Address{Name: p.User.DisplayName, Address: p.User.Email}
		}
		raw, _, err := out.Build(s.cfg.MailHostname)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if err := s.mail.Send(m.Address, out.From.Address, out.Recipients(), raw); err != nil {
			s.log.Warn("unsubscribe message rejected", "mailbox", m.ID, "err", err)
			writeError(w, http.StatusBadGateway, "send_failed", "The mail server did not accept the unsubscribe message. Try again in a moment.")
			return
		}
		if err := s.mail.SaveSent(m.Address, raw); err != nil {
			s.log.Error("saving unsubscribe message to Sent failed", "mailbox", m.ID, "err", err)
		}
		s.audit(r.Context(), r, p, &m.OrgID, "mailbox.unsubscribe", "mailbox", m.ID, map[string]any{"method": "mailto", "recipient": to, "uid": uid, "folder": folder})
		writeJSON(w, http.StatusOK, unsubscribeResponse{Method: "mailto"})
	default:
		writeError(w, http.StatusUnprocessableEntity, "no_unsubscribe", "This message has no unsubscribe link.")
	}
}

func (s *Server) listNewsletters(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	if !s.allow(r.Context(), "newsletters:"+m.ID, 30) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "This list was requested too often. Wait a minute and try again.")
		return
	}
	rules, _, err := s.loadSieveSettings(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	found, err := s.mail.Newsletters(m.Address, func(l mail.ListUnsubscribe) bool {
		method, _ := chooseUnsubscribe(l, s.cfg.WebhookAllowPrivate)
		return method != ""
	})
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	out := make([]newsletterSender, 0, len(found))
	for _, n := range found {
		offer := unsubscribeOffer(n.List, s.cfg.WebhookAllowPrivate)
		if offer == nil {
			continue
		}
		out = append(out, newsletterSender{
			Address: n.Address, Name: n.Name, Subject: n.Subject, Count: n.Count, LatestUID: n.LatestUID, Folder: n.Folder, Date: n.Date,
			OneClick: offer.OneClick, Link: offer.Link, Mailto: offer.Mailto, Blocked: newsletterBlocked(rules, n.Address),
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Senders []newsletterSender `json:"senders"`
	}{nonNil(out)})
}

func newsletterBlocked(rules []mail.Rule, address string) bool {
	for _, rule := range rules {
		if !rule.Enabled || !rule.Actions.Delete {
			continue
		}
		for _, c := range rule.Conditions {
			if c.Field != "from" || c.Negate || !strings.EqualFold(strings.TrimSpace(c.Contains), address) {
				continue
			}
			if rule.Match == "all" && len(rule.Conditions) > 1 {
				continue
			}
			return true
		}
	}
	return false
}

func normalizeSenderAddress(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	a, err := netmail.ParseAddress(raw)
	if err != nil || !strings.EqualFold(a.Address, raw) || strings.ContainsAny(a.Address, "\r\n ") || len(a.Address) > 254 {
		return "", false
	}
	return strings.ToLower(a.Address), true
}

func blockRuleName(address string) string {
	name := "Block " + address
	if len(name) > 100 {
		return name[:100]
	}
	return name
}

func (s *Server) blockNewsletter(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Address string `json:"address"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	address, ok := normalizeSenderAddress(in.Address)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_recipient", "Enter the sender's email address.")
		return
	}
	if strings.EqualFold(address, m.Address) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_recipient", "You cannot block this mailbox's own address.")
		return
	}
	rules, reply, err := s.loadSieveSettings(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if newsletterBlocked(rules, address) {
		writeJSON(w, http.StatusOK, map[string]bool{"blocked": true})
		return
	}
	if len(rules) >= 100 {
		writeError(w, http.StatusUnprocessableEntity, "too_many_rules", "A mailbox can have up to 100 rules.")
		return
	}
	rule := mail.Rule{
		Name: blockRuleName(address), Enabled: true, Match: "all",
		Conditions: []mail.RuleCondition{{Field: "from", Contains: address}},
		Actions:    mail.RuleActions{Delete: true, Stop: true},
	}
	if err := rule.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_rule", err.Error())
		return
	}
	s.installSieve(w, r, p, m, append([]mail.Rule{rule}, rules...), reply, map[string]bool{"blocked": true})
}
