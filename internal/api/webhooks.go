package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

const (
	webhookSecretContext = "webhook-secret"
	maxWebhookAttempts   = 8
)

var webhookRetryDelays = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour}

var eventNamePattern = regexp.MustCompile(`^[a-z_]+(\.[a-z_]+)+$`)

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

func publicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

var errBlockedAddress = errors.New("webhook destination resolves to a private or reserved address")

func newWebhookClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !publicAddress(ap.Addr()) {
			return errBlockedAddress
		}
		return nil
	}}
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second, MaxIdleConns: 10, IdleConnTimeout: 30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func signWebhook(secret, body []byte, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) enqueueWebhooks(ctx context.Context, orgID, event string, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		s.log.Error("encoding webhook payload failed", "event", event, "err", err)
		return
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO webhook_deliveries (webhook_id, event, payload)
		SELECT id, $2, $3 FROM webhooks WHERE org_id = $1 AND active AND ($2 = ANY (events) OR '*' = ANY (events))`, orgID, event, body)
	if err != nil {
		s.log.Error("queueing webhooks failed", "event", event, "err", err)
	}
}

func (s *Server) deliverWebhooks(ctx context.Context) {
	rows, err := s.db.Query(ctx, `
		UPDATE webhook_deliveries d SET next_attempt_at = now() + interval '2 minutes'
		FROM webhooks w
		WHERE d.id IN (SELECT id FROM webhook_deliveries WHERE status = 'pending' AND next_attempt_at <= now() ORDER BY next_attempt_at LIMIT 20 FOR UPDATE SKIP LOCKED)
		  AND w.id = d.webhook_id
		RETURNING d.id, d.event, d.payload, d.attempts, w.url, w.secret`)
	if err != nil {
		s.log.Error("loading webhook deliveries failed", "err", err)
		return
	}
	type delivery struct {
		id, event, url string
		payload        []byte
		attempts       int
		secret         []byte
	}
	due, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (delivery, error) {
		var d delivery
		return d, row.Scan(&d.id, &d.event, &d.payload, &d.attempts, &d.url, &d.secret)
	})
	if err != nil {
		s.log.Error("loading webhook deliveries failed", "err", err)
		return
	}
	for _, d := range due {
		code, err := s.postWebhook(ctx, d.id, d.event, d.url, d.secret, d.payload)
		switch {
		case err == nil:
			s.db.Exec(ctx, `UPDATE webhook_deliveries SET status = 'delivered', attempts = attempts + 1, response_code = $2, last_error = NULL, delivered_at = now() WHERE id = $1`, d.id, code)
		case d.attempts+1 >= maxWebhookAttempts:
			s.db.Exec(ctx, `UPDATE webhook_deliveries SET status = 'failed', attempts = attempts + 1, response_code = $2, last_error = $3 WHERE id = $1`, d.id, code, truncate(err.Error(), 500))
		default:
			s.db.Exec(ctx, `UPDATE webhook_deliveries SET attempts = attempts + 1, response_code = $2, last_error = $3, next_attempt_at = now() + make_interval(secs => $4) WHERE id = $1`,
				d.id, code, truncate(err.Error(), 500), webhookRetryDelays[min(d.attempts, len(webhookRetryDelays)-1)].Seconds())
		}
	}
}

func (s *Server) postWebhook(ctx context.Context, deliveryID, event, target string, sealed, body []byte) (*int, error) {
	secret, err := s.box.Open(sealed, webhookSecretContext)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Aether-Webhooks/1")
	req.Header.Set("Aether-Event", event)
	req.Header.Set("Aether-Delivery", deliveryID)
	req.Header.Set("Aether-Signature", signWebhook(secret, body, time.Now()))
	resp, err := s.webhookClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	code := resp.StatusCode
	if code < 200 || code > 299 {
		return &code, errors.New("the endpoint responded with HTTP " + strconv.Itoa(code))
	}
	return &code, nil
}

type webhook struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) webhookOrg(w http.ResponseWriter, r *http.Request, p *principal) (string, bool) {
	orgID, ok := pathID(w, r, "orgId")
	if !ok {
		return "", false
	}
	if !p.canAdminOrg(orgID) {
		notFound(w)
		return "", false
	}
	return orgID, true
}

func (s *Server) ownedWebhook(w http.ResponseWriter, r *http.Request, p *principal) (id, orgID string, ok bool) {
	if id, ok = pathID(w, r, "webhookId"); !ok {
		return "", "", false
	}
	if err := s.db.QueryRow(r.Context(), `SELECT org_id FROM webhooks WHERE id = $1`, id).Scan(&orgID); err != nil || !p.canAdminOrg(orgID) {
		notFound(w)
		return "", "", false
	}
	return id, orgID, true
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.webhookOrg(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, url, events, active, created_at FROM webhooks WHERE org_id = $1 ORDER BY created_at`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	hooks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (webhook, error) {
		var h webhook
		return h, row.Scan(&h.ID, &h.URL, &h.Events, &h.Active, &h.CreatedAt)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hooks)
}

func (s *Server) validWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || len(raw) > 2048 {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && s.cfg.WebhookAllowPrivate)
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.webhookOrg(w, r, p)
	if !ok {
		return
	}
	var in struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	in.URL = strings.TrimSpace(in.URL)
	if !s.validWebhookURL(in.URL) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_url", "Enter an https:// URL without a username or password.")
		return
	}
	if len(in.Events) == 0 || len(in.Events) > 50 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_events", "Choose between 1 and 50 events, or * for all events.")
		return
	}
	for _, e := range in.Events {
		if e != "*" && !eventNamePattern.MatchString(e) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_events", e+" is not a valid event name.")
			return
		}
	}
	var count int
	s.db.QueryRow(r.Context(), `SELECT count(*) FROM webhooks WHERE org_id = $1`, orgID).Scan(&count)
	if count >= 20 {
		writeError(w, http.StatusUnprocessableEntity, "too_many_webhooks", "An organization can have up to 20 webhooks.")
		return
	}
	secret := "whsec_" + secrets.Token(30)
	var createdBy *string
	if p.apiKey == nil {
		createdBy = &p.User.ID
	}
	var h webhook
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO webhooks (org_id, url, secret, events, created_by) VALUES ($1, $2, $3, $4, $5)
		RETURNING id, url, events, active, created_at`, orgID, in.URL, s.box.Seal([]byte(secret), webhookSecretContext), in.Events, createdBy).
		Scan(&h.ID, &h.URL, &h.Events, &h.Active, &h.CreatedAt)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "webhook.create", "webhook", h.ID, map[string]any{"url": h.URL, "events": h.Events})
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": h, "secret": secret})
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request, p *principal) {
	id, orgID, ok := s.ownedWebhook(w, r, p)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM webhooks WHERE id = $1`, id); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "webhook.delete", "webhook", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request, p *principal) {
	id, _, ok := s.ownedWebhook(w, r, p)
	if !ok {
		return
	}
	type delivery struct {
		ID           string     `json:"id"`
		Event        string     `json:"event"`
		Status       string     `json:"status"`
		Attempts     int        `json:"attempts"`
		ResponseCode *int       `json:"responseCode"`
		LastError    *string    `json:"lastError"`
		CreatedAt    time.Time  `json:"createdAt"`
		DeliveredAt  *time.Time `json:"deliveredAt"`
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, event, status, attempts, response_code, last_error, created_at, delivered_at FROM webhook_deliveries WHERE webhook_id = $1 ORDER BY created_at DESC LIMIT 50`, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (delivery, error) {
		var d delivery
		return d, row.Scan(&d.ID, &d.Event, &d.Status, &d.Attempts, &d.ResponseCode, &d.LastError, &d.CreatedAt, &d.DeliveredAt)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request, p *principal) {
	id, orgID, ok := s.ownedWebhook(w, r, p)
	if !ok {
		return
	}
	body, _ := json.Marshal(map[string]any{"type": "webhook.test", "createdAt": time.Now().UTC(), "organizationId": orgID, "data": map[string]any{}})
	var deliveryID string
	if err := s.db.QueryRow(r.Context(), `INSERT INTO webhook_deliveries (webhook_id, event, payload) VALUES ($1, 'webhook.test', $2) RETURNING id`, id, body).Scan(&deliveryID); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"deliveryId": deliveryID})
}
