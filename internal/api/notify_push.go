package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5"
)

func (s *Server) createPushSubscription(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if err := validatePushEndpoint(in.Endpoint); err != nil || !validPushKey(in.Keys.P256dh) || !validPushKey(in.Keys.Auth) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_subscription", "The browser did not send a complete https push subscription.")
		return
	}
	if _, _, err := s.vapid(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "push_unavailable", "Background notifications are not available on this server yet.")
		return
	}
	agent := r.UserAgent()
	if len(agent) > 300 {
		agent = agent[:300]
	}
	if _, err := s.db.Exec(r.Context(), `
		INSERT INTO web_push_subscriptions (user_id, endpoint, p256dh, auth, user_agent) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, user_agent = excluded.user_agent`,
		p.User.ID, in.Endpoint, in.Keys.P256dh, in.Keys.Auth, agent); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.wakeNotify()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deletePushSubscription(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM web_push_subscriptions WHERE user_id = $1 AND endpoint = $2`, p.User.ID, in.Endpoint); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.wakeNotify()
	w.WriteHeader(http.StatusNoContent)
}

func validatePushEndpoint(endpoint string) error {
	u, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil || u.URL.Scheme != "https" || u.URL.Host == "" || u.URL.User != nil || len(endpoint) > 2000 {
		return errors.New("invalid endpoint")
	}
	return nil
}

func validPushKey(key string) bool {
	if key == "" || len(key) > 200 || strings.ContainsAny(key, "\r\n ") {
		return false
	}
	return true
}

func (s *Server) vapid(ctx context.Context) (string, string, error) {
	if s.cfg.VAPIDPublicKey != "" || s.cfg.VAPIDPrivateKey != "" {
		if s.cfg.VAPIDPublicKey == "" || s.cfg.VAPIDPrivateKey == "" {
			return "", "", errors.New("AETHER_VAPID_PUBLIC_KEY and AETHER_VAPID_PRIVATE_KEY must both be set")
		}
		return s.cfg.VAPIDPublicKey, s.cfg.VAPIDPrivateKey, nil
	}
	s.vapidMu.Lock()
	defer s.vapidMu.Unlock()
	if s.vapidPublic != "" {
		return s.vapidPublic, s.vapidPrivate, nil
	}
	var public, private string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7316249)`); err != nil {
			return err
		}
		var sealed []byte
		err := tx.QueryRow(ctx, `SELECT public_key, private_key FROM push_credentials WHERE id`).Scan(&public, &sealed)
		if err == nil {
			opened, err := s.box.Open(sealed, "push-vapid")
			if err != nil {
				return err
			}
			private = string(opened)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		private, public, err = webpush.GenerateVAPIDKeys()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO push_credentials (public_key, private_key) VALUES ($1, $2)`, public, s.box.Seal([]byte(private), "push-vapid"))
		return err
	})
	if err != nil {
		return "", "", err
	}
	s.vapidPublic, s.vapidPrivate = public, private
	return public, private, nil
}

func (s *Server) vapidSubject() string {
	host := s.cfg.MailHostname
	if host == "" {
		host = "localhost"
	}
	return "mailto:postmaster@" + host
}

type pushSub struct {
	id, endpoint, p256dh, auth string
}

type mailRecipient struct {
	id   string
	subs []pushSub
}

func (s *Server) pushMail(ctx context.Context, spec watchSpec, validity uint32, unseen []liveMessage) {
	public, private, err := s.vapid(ctx)
	if err != nil || public == "" {
		notifySent.WithLabelValues("web", "skipped").Inc()
		return
	}
	recipients, err := s.mailRecipients(ctx, spec.mailboxID)
	if err != nil {
		s.log.Error("loading push subscriptions failed", "err", err)
		return
	}
	prefs, err := s.folderPrefs(ctx, spec.mailboxID)
	if err != nil {
		s.log.Error("loading folder notification settings failed", "err", err)
		return
	}
	first := unseen[0]
	who := first.From
	if who == "" {
		who = "Someone"
	}
	title := "New mail"
	if len(unseen) > 1 {
		title = fmt.Sprintf("%d new messages", len(unseen))
	}
	body := who
	if first.Subject != "" {
		body += "\n" + clip(first.Subject, 140)
	}
	payload, _ := json.Marshal(liveEvent{
		Kind: "mail", Title: title, Body: body, URL: mailURL(spec.mailboxID, spec.folder, first.UID),
		Tag: "mail:" + spec.mailboxID + ":" + spec.folder, MailboxID: spec.mailboxID, Folder: spec.folder,
	})
	for _, user := range recipients {
		if !wantsFolder(prefs[user.id], spec.folder) {
			continue
		}
		var claimed []string
		for _, m := range unseen {
			key := fmt.Sprintf("web:%s:%s:%d:%d", user.id, spec.mailboxID, validity, m.UID)
			if s.claimDelivery(ctx, key) {
				claimed = append(claimed, key)
			}
		}
		if len(claimed) == 0 {
			continue
		}
		if s.sendWebPush(ctx, user.subs, payload, public, private) {
			notifySent.WithLabelValues("web", "sent").Inc()
			continue
		}
		notifySent.WithLabelValues("web", "failed").Inc()
		s.releaseDelivery(ctx, claimed)
	}
}

func wantsFolder(explicit map[string]bool, folder string) bool {
	if on, ok := explicit[folder]; ok {
		return on
	}
	for name, on := range explicit {
		if strings.EqualFold(name, folder) {
			return on
		}
	}
	return strings.EqualFold(folder, "INBOX")
}

func (s *Server) mailRecipients(ctx context.Context, mailboxID string) ([]mailRecipient, error) {
	rows, err := s.db.Query(ctx, `
		SELECT u.id, s.id, s.endpoint, s.p256dh, s.auth
		FROM web_push_subscriptions s
		JOIN users u ON u.id = s.user_id AND u.status = 'active'
		JOIN mailboxes m ON m.id = $1
		WHERE coalesce((SELECT mail_enabled FROM notification_settings n WHERE n.user_id = u.id), true)
		  AND `+mailboxAccess("u.id")+`
		ORDER BY u.id`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mailRecipient
	for rows.Next() {
		var userID string
		var sub pushSub
		if err := rows.Scan(&userID, &sub.id, &sub.endpoint, &sub.p256dh, &sub.auth); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].id != userID {
			out = append(out, mailRecipient{id: userID})
		}
		out[len(out)-1].subs = append(out[len(out)-1].subs, sub)
	}
	return out, rows.Err()
}

func (s *Server) folderPrefs(ctx context.Context, mailboxID string) (map[string]map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT user_id, folder, enabled FROM mail_folder_notifications WHERE mailbox_id = $1`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var userID, folder string
		var enabled bool
		if err := rows.Scan(&userID, &folder, &enabled); err != nil {
			return nil, err
		}
		if out[userID] == nil {
			out[userID] = map[string]bool{}
		}
		out[userID][folder] = enabled
	}
	return out, rows.Err()
}

func (s *Server) sendWebPush(ctx context.Context, subs []pushSub, payload []byte, public, private string) bool {
	ok := false
	for _, sub := range subs {
		resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
			Endpoint: sub.endpoint,
			Keys:     webpush.Keys{P256dh: sub.p256dh, Auth: sub.auth},
		}, &webpush.Options{
			Subscriber:      s.vapidSubject(),
			VAPIDPublicKey:  public,
			VAPIDPrivateKey: private,
			TTL:             60,
			Urgency:         webpush.UrgencyHigh,
			HTTPClient:      s.webhookClient,
		})
		if err != nil {
			s.log.Warn("web push failed", "err", err)
			continue
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusNotFound, http.StatusGone:
			s.db.Exec(ctx, `DELETE FROM web_push_subscriptions WHERE id = $1`, sub.id)
		case http.StatusOK, http.StatusCreated, http.StatusAccepted:
			s.db.Exec(ctx, `UPDATE web_push_subscriptions SET last_success_at = now() WHERE id = $1`, sub.id)
			ok = true
		default:
			s.log.Warn("web push was rejected", "status", resp.StatusCode)
		}
	}
	return ok
}

func (s *Server) registerIOSPush(w http.ResponseWriter, r *http.Request) {
	if !s.dovecotAuthorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "The Dovecot API key is missing or wrong.")
		return
	}
	var in struct {
		Address     string `json:"address"`
		AccountID   string `json:"accountId"`
		DeviceToken string `json:"deviceToken"`
		Subtopic    string `json:"subtopic"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	in.Address = strings.ToLower(strings.TrimSpace(in.Address))
	in.DeviceToken = strings.ToLower(strings.TrimSpace(in.DeviceToken))
	in.AccountID = strings.TrimSpace(in.AccountID)
	in.Subtopic = strings.TrimSpace(in.Subtopic)
	if !validIOSRegistration(in.Address, in.AccountID, in.DeviceToken, in.Subtopic) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_device", "Send the mailbox address, the Apple account id, a hex device token, and the Mail subtopic.")
		return
	}
	var mailboxID string
	err := s.db.QueryRow(r.Context(), `
		SELECT m.id FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE lower(m.address) = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL`, in.Address).Scan(&mailboxID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if _, err := s.db.Exec(r.Context(), `
		INSERT INTO ios_push_devices (mailbox_id, account_id, device_token, subtopic) VALUES ($1, $2, $3, $4)
		ON CONFLICT (mailbox_id, device_token) DO UPDATE SET account_id = excluded.account_id, subtopic = excluded.subtopic, updated_at = now()`,
		mailboxID, in.AccountID, in.DeviceToken, in.Subtopic); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.wakeNotify()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteIOSPush(w http.ResponseWriter, r *http.Request) {
	if !s.dovecotAuthorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "The Dovecot API key is missing or wrong.")
		return
	}
	var in struct {
		Address     string `json:"address"`
		DeviceToken string `json:"deviceToken"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	in.Address = strings.ToLower(strings.TrimSpace(in.Address))
	in.DeviceToken = strings.ToLower(strings.TrimSpace(in.DeviceToken))
	if _, err := s.db.Exec(r.Context(), `
		DELETE FROM ios_push_devices i USING mailboxes m
		WHERE i.mailbox_id = m.id AND lower(m.address) = $1 AND i.device_token = $2`, in.Address, in.DeviceToken); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) dovecotAuthorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && constantTimeEqual(token, s.cfg.DovecotAPIKey)
}

func validIOSRegistration(address, accountID, token, subtopic string) bool {
	if len(address) < 3 || len(address) > 320 || !strings.Contains(address, "@") || strings.ContainsAny(address, " \r\n") {
		return false
	}
	if accountID == "" || len(accountID) > 128 || strings.ContainsAny(accountID, "\r\n") {
		return false
	}
	if len(token) < 64 || len(token) > 200 {
		return false
	}
	for _, c := range token {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return subtopic == "com.apple.mobilemail" || subtopic == "com.apple.mail.XServer"
}

func (s *Server) apnsReady() bool {
	_, ok := s.apnsClient()
	return ok
}

type apnsClient struct {
	http    *http.Client
	sandbox bool
}

func (s *Server) apnsClient() (*apnsClient, bool) {
	if s.cfg.APNSCertFile == "" || s.cfg.APNSKeyFile == "" {
		return nil, false
	}
	key := s.cfg.APNSCertFile + "\n" + s.cfg.APNSKeyFile
	s.apnsMu.Lock()
	defer s.apnsMu.Unlock()
	if s.apns != nil && s.apnsKey == key {
		return s.apns, true
	}
	certPEM, err := os.ReadFile(s.cfg.APNSCertFile)
	if err != nil {
		return nil, false
	}
	keyPEM, err := os.ReadFile(s.cfg.APNSKeyFile)
	if err != nil {
		return nil, false
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		s.log.Warn("Apple Push certificate could not be read", "err", err)
		return nil, false
	}
	client := &apnsClient{
		sandbox: s.cfg.APNSSandbox,
		http: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2: true,
		}},
	}
	s.apns, s.apnsKey = client, key
	return client, true
}

func (s *Server) pushIOS(ctx context.Context, spec watchSpec, validity uint32, unseen []liveMessage) {
	client, ok := s.apnsClient()
	if !ok {
		return
	}
	rows, err := s.db.Query(ctx, `SELECT account_id, device_token, subtopic FROM ios_push_devices WHERE mailbox_id = $1`, spec.mailboxID)
	if err != nil {
		s.log.Error("loading iOS push devices failed", "err", err)
		return
	}
	defer rows.Close()
	type device struct{ account, token, topic string }
	var devices []device
	for rows.Next() {
		var d device
		if err := rows.Scan(&d.account, &d.token, &d.topic); err != nil {
			s.log.Error("loading iOS push devices failed", "err", err)
			return
		}
		devices = append(devices, d)
	}
	if err := rows.Err(); err != nil || len(devices) == 0 {
		return
	}
	for _, d := range devices {
		var claimed []string
		for _, m := range unseen {
			key := fmt.Sprintf("ios:%s:%d:%d:%s", spec.mailboxID, validity, m.UID, d.token)
			if s.claimDelivery(ctx, key) {
				claimed = append(claimed, key)
			}
		}
		if len(claimed) == 0 {
			continue
		}
		if err := client.send(ctx, d.token, d.topic, d.account); err != nil {
			if errors.Is(err, errAPNSGone) {
				s.db.Exec(ctx, `DELETE FROM ios_push_devices WHERE mailbox_id = $1 AND device_token = $2`, spec.mailboxID, d.token)
			} else {
				s.log.Warn("Apple push failed", "mailbox", spec.mailboxID, "err", err)
				s.releaseDelivery(ctx, claimed)
			}
			notifySent.WithLabelValues("ios", "failed").Inc()
			continue
		}
		notifySent.WithLabelValues("ios", "sent").Inc()
	}
}

var errAPNSGone = errors.New("apple push device is gone")

func (c *apnsClient) send(ctx context.Context, token, topic, accountID string) error {
	host := "https://api.push.apple.com"
	if c.sandbox {
		host = "https://api.sandbox.push.apple.com"
	}
	body, _ := json.Marshal(map[string]any{"aps": map[string]string{"account-id": accountID}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/3/device/"+token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("apns-topic", topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusGone:
		return errAPNSGone
	default:
		return fmt.Errorf("APNs status %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
	}
}
