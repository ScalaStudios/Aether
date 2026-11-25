package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	imageProxyTTL       = 6 * time.Hour
	imageProxyContext   = "image-proxy"
	maxProxyImageBytes  = 5 << 20
	maxImageRedirects   = 3
	imageProxyUserAgent = "Aether-ImageProxy/1"
)

var errImageRejected = errors.New("image rejected")

var imageFetchSlots = make(chan struct{}, 32)

func newImageClient() *http.Client {
	dialer := publicDialer(false)
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second, MaxIdleConns: 32, IdleConnTimeout: 30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type imageTicket struct {
	U string `json:"u"`
	E int64  `json:"e"`
}

type remoteImageInfo struct {
	Status         string `json:"status"`
	Remote         int    `json:"remote"`
	Trackers       int    `json:"trackers"`
	Sender         string `json:"sender,omitempty"`
	SenderAllowed  bool   `json:"senderAllowed"`
	MessageAllowed bool   `json:"messageAllowed"`
	Policy         string `json:"policy"`
}

func (s *Server) signImageProxy(raw string) string {
	normalized, ok := mailpkg.NormalizeImageURL(raw)
	if !ok || normalized != raw || mailpkg.IsTrackingImageURL(normalized) || len(s.cfg.SecretKey) == 0 {
		return ""
	}
	body, err := json.Marshal(imageTicket{U: normalized, E: time.Now().Add(imageProxyTTL).Unix()})
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, s.cfg.SecretKey)
	mac.Write([]byte(imageProxyContext))
	mac.Write(body)
	return "/api/v1/image-proxy/" + base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) openImageProxy(token string) (string, error) {
	encBody, encSig, ok := strings.Cut(token, ".")
	if !ok || encBody == "" || encSig == "" || strings.Contains(encSig, ".") || len(s.cfg.SecretKey) == 0 {
		return "", errImageRejected
	}
	body, err := base64.RawURLEncoding.DecodeString(encBody)
	sig, err2 := base64.RawURLEncoding.DecodeString(encSig)
	if err != nil || err2 != nil || len(sig) != sha256.Size {
		return "", errImageRejected
	}
	mac := hmac.New(sha256.New, s.cfg.SecretKey)
	mac.Write([]byte(imageProxyContext))
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", errImageRejected
	}
	var ticket imageTicket
	if json.Unmarshal(body, &ticket) != nil || ticket.E < time.Now().Unix() {
		return "", errImageRejected
	}
	normalized, ok := mailpkg.NormalizeImageURL(ticket.U)
	if !ok || normalized != ticket.U || mailpkg.IsTrackingImageURL(normalized) {
		return "", errImageRejected
	}
	return normalized, nil
}

func (s *Server) proxyImage(w http.ResponseWriter, r *http.Request) {
	if !s.allow(r.Context(), "imageproxy:"+s.clientIP(r), 240) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many image requests. Wait a minute and open the message again.")
		return
	}
	raw, err := s.openImageProxy(r.PathValue("token"))
	if err != nil {
		notFound(w)
		return
	}
	body, ctype, err := s.fetchProxiedImage(r.Context(), raw)
	if err != nil {
		host := ""
		if u, parseErr := url.Parse(raw); parseErr == nil {
			host = u.Hostname()
		}
		s.log.Warn("image proxy failed", "host", host, "err", err)
		notFound(w)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Content-Disposition", "inline")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func (s *Server) fetchProxiedImage(ctx context.Context, raw string) ([]byte, string, error) {
	select {
	case imageFetchSlots <- struct{}{}:
		defer func() { <-imageFetchSlots }()
	default:
		return nil, "", errImageRejected
	}
	current := raw
	client := s.imageClient
	if client == nil {
		client = newImageClient()
	}
	for hop := 0; hop <= maxImageRedirects; hop++ {
		normalized, ok := mailpkg.NormalizeImageURL(current)
		if !ok || mailpkg.IsTrackingImageURL(normalized) {
			return nil, "", errImageRejected
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized, nil)
		if err != nil {
			return nil, "", errImageRejected
		}
		req.Header.Set("User-Agent", imageProxyUserAgent)
		req.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/gif,image/bmp")
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", errImageRejected
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 && hop < maxImageRedirects {
			loc := resp.Header.Get("Location")
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			next, err := req.URL.Parse(loc)
			if err != nil || loc == "" {
				return nil, "", errImageRejected
			}
			current = next.String()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			return nil, "", errImageRejected
		}
		if resp.ContentLength > maxProxyImageBytes {
			resp.Body.Close()
			return nil, "", errImageRejected
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxProxyImageBytes+1))
		resp.Body.Close()
		if err != nil || len(body) == 0 || len(body) > maxProxyImageBytes {
			return nil, "", errImageRejected
		}
		ctype := sniffImage(body)
		if ctype == "" {
			return nil, "", errImageRejected
		}
		return body, ctype, nil
	}
	return nil, "", errImageRejected
}

func sniffImage(b []byte) string {
	switch {
	case len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return "image/png"
	case len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff:
		return "image/jpeg"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif"
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	case len(b) >= 12 && string(b[4:8]) == "ftyp" && (string(b[8:12]) == "avif" || string(b[8:12]) == "avis"):
		return "image/avif"
	case len(b) >= 2 && b[0] == 'B' && b[1] == 'M':
		return "image/bmp"
	default:
		return ""
	}
}

func normalizeSender(from []mailpkg.Address) string {
	if len(from) == 0 {
		return ""
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(from[0].Address))
	if err != nil {
		return ""
	}
	addr := strings.ToLower(parsed.Address)
	if len(addr) > 320 || strings.Count(addr, "@") != 1 || strings.ContainsAny(addr, " \r\n\x00") {
		return ""
	}
	return addr
}

func remoteImageKey(mailboxID, folder string, msg *mailpkg.Message) string {
	id := strings.TrimSpace(msg.MessageID)
	if id != "" && len(id) <= 900 && !strings.ContainsAny(id, "\r\n\x00") {
		return "mid:" + id
	}
	sum := sha256.Sum256([]byte(mailboxID + "\n" + folder + "\n" + strconv.FormatUint(uint64(msg.UID), 10)))
	return "uid:" + hex.EncodeToString(sum[:])
}

func (s *Server) remoteImageChoice(ctx context.Context, orgID, userID, sender, messageKey string) (policy string, senderAllowed, messageAllowed bool, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT remote_image_policy FROM organizations WHERE id = $1), 'block'),
			EXISTS (SELECT 1 FROM remote_image_senders WHERE user_id = $2 AND address = $3),
			EXISTS (SELECT 1 FROM remote_image_messages WHERE user_id = $2 AND message_key = $4)`,
		orgID, userID, sender, messageKey).Scan(&policy, &senderAllowed, &messageAllowed)
	if policy != "proxy" {
		policy = "block"
	}
	return policy, senderAllowed, messageAllowed, err
}

func (s *Server) applyRemoteImages(ctx context.Context, userID, orgID, folder string, mailboxID string, msg *mailpkg.Message) (remoteImageInfo, error) {
	sender := normalizeSender(msg.From)
	key := remoteImageKey(mailboxID, folder, msg)
	policy, senderAllowed, messageAllowed, err := s.remoteImageChoice(ctx, orgID, userID, sender, key)
	if err != nil {
		return remoteImageInfo{}, err
	}
	show := policy == "proxy" || senderAllowed || messageAllowed
	html, stats := mailpkg.PresentRemoteImages(msg.HTML, show, func(raw string) (string, bool) {
		if _, ok := mailpkg.NormalizeImageURL(raw); !ok {
			return "", false
		}
		if !show {
			return "", true
		}
		signed := s.signImageProxy(raw)
		if signed == "" {
			return "", false
		}
		return signed, true
	})
	msg.HTML = html
	status := "none"
	switch {
	case stats.Remote == 0 && stats.Trackers == 0:
		status = "none"
	case show && stats.Remote > 0:
		status = "proxied"
	default:
		status = "hidden"
	}
	return remoteImageInfo{
		Status: status, Remote: stats.Remote, Trackers: stats.Trackers, Sender: sender,
		SenderAllowed: senderAllowed, MessageAllowed: messageAllowed, Policy: policy,
	}, nil
}

func (s *Server) writeMessage(w http.ResponseWriter, r *http.Request, p *principal, m *ownedMailbox, msg *mailpkg.Message) {
	var inv *invitation
	if msg.Calendar != "" && len(msg.From) > 0 {
		if on, err := s.userFeaturesOn(r.Context(), p.User.ID, featureCalendar); err == nil && on {
			if ctx, err := s.pimContext(r, p); err == nil {
				inv = s.describeInvitation(ctx, p.User, m, strings.ToLower(msg.From[0].Address), msg.Calendar)
			}
		}
	}
	related, err := s.mail.Related(m.Address, folderParam(r), msg)
	if err != nil {
		s.log.Warn("finding related messages failed", "mailbox", m.ID, "err", err)
	}
	sender, err := s.assessSender(r.Context(), m, folderParam(r), msg)
	if err != nil {
		s.log.Warn("assessing sender failed", "mailbox", m.ID, "err", err)
	}
	msg.Unsubscribe = unsubscribeOffer(msg.ParsedUnsubscribe(), s.cfg.WebhookAllowPrivate)
	links, err := s.messageFileLinks(r.Context(), m.ID, msg.FileLinkIDs)
	if err != nil {
		s.log.Warn("loading download links failed", "mailbox", m.ID, "err", err)
	}
	images, err := s.applyRemoteImages(r.Context(), p.User.ID, m.OrgID, folderParam(r), m.ID, msg)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		*mailpkg.Message
		Invitation   *invitation       `json:"invitation,omitempty"`
		Related      []mailpkg.Related `json:"related"`
		Sender       *senderInsight    `json:"sender"`
		RemoteImages remoteImageInfo   `json:"remoteImages"`
		FileLinks    []fileLinkSummary `json:"fileLinks,omitempty"`
	}{msg, inv, nonNil(related), sender, images, links})
}

func (s *Server) setRemoteImages(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.apiKey != nil || p.User.ID == "" {
		writeError(w, http.StatusForbidden, "api_key_not_allowed", "Sign in to choose which images to show.")
		return
	}
	if !s.allow(r.Context(), "remote-images:"+p.User.ID, 60) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many image choices. Wait a minute and try again.")
		return
	}
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
	var in struct {
		Action string `json:"action"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	switch in.Action {
	case "show", "hide", "allowSender", "forgetSender":
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid_action", "Choose show, hide, allowSender, or forgetSender.")
		return
	}
	msg, err := s.mail.Get(m.Address, folderParam(r), uid)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	sender := normalizeSender(msg.From)
	key := remoteImageKey(m.ID, folderParam(r), msg)
	switch in.Action {
	case "show":
		_, err = s.db.Exec(r.Context(), `INSERT INTO remote_image_messages (user_id, message_key) VALUES ($1, $2) ON CONFLICT DO NOTHING`, p.User.ID, key)
	case "hide":
		_, err = s.db.Exec(r.Context(), `DELETE FROM remote_image_messages WHERE user_id = $1 AND message_key = $2`, p.User.ID, key)
	case "allowSender":
		if sender == "" {
			writeError(w, http.StatusUnprocessableEntity, "no_sender", "This message has no sender address to remember.")
			return
		}
		_, err = s.db.Exec(r.Context(), `INSERT INTO remote_image_senders (user_id, address) VALUES ($1, $2) ON CONFLICT DO NOTHING`, p.User.ID, sender)
	case "forgetSender":
		if sender == "" {
			writeError(w, http.StatusUnprocessableEntity, "no_sender", "This message has no sender address to forget.")
			return
		}
		_, err = s.db.Exec(r.Context(), `DELETE FROM remote_image_senders WHERE user_id = $1 AND address = $2`, p.User.ID, sender)
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeMessage(w, r, p, m, msg)
}

func (s *Server) getMailPrivacy(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var policy string
	err := s.db.QueryRow(r.Context(), `SELECT remote_image_policy FROM organizations WHERE id = $1`, orgID).Scan(&policy)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if policy != "proxy" {
		policy = "block"
	}
	writeJSON(w, http.StatusOK, map[string]string{"remoteImages": policy})
}

func (s *Server) saveMailPrivacy(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		RemoteImages string `json:"remoteImages"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	if in.RemoteImages != "block" && in.RemoteImages != "proxy" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_policy", "Choose block or proxy.")
		return
	}
	var previous string
	err := s.db.QueryRow(r.Context(), `
		UPDATE organizations AS o SET remote_image_policy = $2
		FROM (SELECT id, remote_image_policy FROM organizations WHERE id = $1) AS prev
		WHERE o.id = prev.id
		RETURNING prev.remote_image_policy`, orgID, in.RemoteImages).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "organization.remote_images", "organization", orgID, map[string]any{"remoteImages": in.RemoteImages, "previous": previous})
	writeJSON(w, http.StatusOK, map[string]string{"remoteImages": in.RemoteImages})
}

type remoteImageSender struct {
	Address   string    `json:"address"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) listRemoteImageSenders(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.apiKey != nil || p.User.ID == "" {
		writeError(w, http.StatusForbidden, "api_key_not_allowed", "Sign in to manage senders whose images you always show.")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT address, created_at FROM remote_image_senders WHERE user_id = $1 ORDER BY address`, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	senders, err := pgx.CollectRows(rows, pgx.RowToStructByPos[remoteImageSender])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(senders))
}

func (s *Server) forgetRemoteImageSender(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.apiKey != nil || p.User.ID == "" {
		writeError(w, http.StatusForbidden, "api_key_not_allowed", "Sign in to manage senders whose images you always show.")
		return
	}
	addr := strings.ToLower(strings.TrimSpace(r.PathValue("address")))
	parsed, err := mail.ParseAddress(addr)
	if err != nil || strings.ToLower(parsed.Address) != addr || strings.Count(addr, "@") != 1 {
		notFound(w)
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM remote_image_senders WHERE user_id = $1 AND address = $2`, p.User.ID, addr)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
