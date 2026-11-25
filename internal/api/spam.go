package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxLearnBytes = 50 << 20

var spamClasses = map[string]string{"spam": "/learnspam", "ham": "/learnham"}

func (s *Server) recipientMailbox(ctx context.Context, address string) (owner *string, mailbox string, learning bool, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT m.owner_user_id, m.address, o.spam_learning FROM mailboxes m JOIN organizations o ON o.id = m.org_id
		WHERE m.address = $1 OR m.address = regexp_replace($1, '\+[^@]*@', '@') ORDER BY m.address = $1 DESC LIMIT 1`,
		strings.ToLower(strings.TrimSpace(address))).Scan(&owner, &mailbox, &learning)
	return
}

func (s *Server) learnSpam(w http.ResponseWriter, r *http.Request) {
	if !internalCaller(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	class := r.URL.Query().Get("class")
	path, ok := spamClasses[class]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_, _, enabled, err := s.recipientMailbox(r.Context(), r.URL.Query().Get("user"))
	if errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		s.log.Warn("spam learning lookup failed", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if !enabled {
		spamLearned.WithLabelValues(class, "disabled").Inc()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.cfg.RspamdPassword == "" {
		spamLearned.WithLabelValues(class, "failed").Inc()
		s.log.Warn("spam learning skipped: AETHER_RSPAMD_CONTROLLER_PASSWORD is not set")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.RspamdControllerURL, "/")+path, http.MaxBytesReader(w, r.Body, maxLearnBytes))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	req.Header.Set("Password", s.cfg.RspamdPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		spamLearned.WithLabelValues(class, "failed").Inc()
		s.log.Warn("spam learning failed", "class", class, "err", err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		spamLearned.WithLabelValues(class, "learned").Inc()
	case http.StatusAlreadyReported:
		spamLearned.WithLabelValues(class, "duplicate").Inc()
	default:
		spamLearned.WithLabelValues(class, "failed").Inc()
		s.log.Warn("spam learning rejected", "class", class, "status", resp.StatusCode)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) trustedSender(w http.ResponseWriter, r *http.Request) {
	if !internalCaller(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	owner, mailbox, _, err := s.recipientMailbox(r.Context(), r.PostFormValue("user"))
	if errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		s.log.Warn("trusted sender lookup failed", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	trusted := false
	if sender := strings.ToLower(strings.TrimSpace(r.PostFormValue("from"))); owner != nil && sender != "" {
		err = s.db.QueryRow(r.Context(), `
			SELECT EXISTS (SELECT 1 FROM dav_objects o JOIN dav_collections c ON c.id = o.collection_id
			WHERE c.user_id = $1 AND c.kind = 'addressbook' AND o.emails @> ARRAY[$2])`, *owner, sender).Scan(&trusted)
		if err != nil {
			s.log.Warn("trusted sender lookup failed", "err", err)
		}
	}
	if reply := strings.TrimSpace(r.PostFormValue("inReplyTo")); !trusted && reply != "" && len(reply) <= 998 {
		if trusted, err = s.mail.Sent(mailbox, reply); err != nil {
			s.log.Warn("trusted sender sent search failed", "err", err)
		}
	}
	if !trusted {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
