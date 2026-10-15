package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/scalastudios/aether/internal/config"
	"github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/secrets"
)

const sessionCookie = "aether_session"

type Server struct {
	cfg           *config.Config
	db            *pgxpool.Pool
	box           *secrets.Box
	kv            *redis.Client
	mail          *mail.Client
	log           *slog.Logger
	oidc          *oidcCache
	resolve       resolver
	webhookClient *http.Client
}

func New(cfg *config.Config, db *pgxpool.Pool, box *secrets.Box, kv *redis.Client, mc *mail.Client, log *slog.Logger) *Server {
	return &Server{cfg: cfg, db: db, box: box, kv: kv, mail: mc, log: log, oidc: newOIDCCache(), resolve: net.DefaultResolver, webhookClient: newWebhookClient(cfg.WebhookAllowPrivate)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", s.ready)

	mux.HandleFunc("GET /api/v1/auth/providers", s.authProviders)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/oidc/{slug}/start", s.oidcStart)
	mux.HandleFunc("GET /api/v1/auth/oidc/{slug}/callback", s.oidcCallback)
	mux.HandleFunc("POST /api/v1/hooks/identity-providers/{slug}", s.providerWebhook)
	mux.HandleFunc("/dav/", s.serveDAV)
	mux.HandleFunc("/.well-known/caldav", s.serveDAV)
	mux.HandleFunc("/.well-known/carddav", s.serveDAV)
	mux.HandleFunc("GET /.well-known/mta-sts.txt", s.mtaSTSPolicyFile)
	mux.HandleFunc("GET /internal/tls-ask", s.tlsAsk)

	authed := func(pattern string, h func(http.ResponseWriter, *http.Request, *principal)) {
		mux.HandleFunc(pattern, s.withPrincipal(h))
	}
	authed("POST /api/v1/auth/logout", s.logout)
	authed("GET /api/v1/session", s.session)

	authed("GET /api/v1/organizations", s.listOrganizations)
	authed("POST /api/v1/organizations", s.createOrganization)
	authed("GET /api/v1/organizations/{orgId}", s.getOrganization)
	authed("GET /api/v1/organizations/{orgId}/members", s.listMembers)
	authed("POST /api/v1/organizations/{orgId}/members", s.setMember)
	authed("GET /api/v1/organizations/{orgId}/audit", s.listAudit)
	authed("GET /api/v1/organizations/{orgId}/api-keys", s.listAPIKeys)
	authed("POST /api/v1/organizations/{orgId}/api-keys", s.createAPIKey)
	authed("DELETE /api/v1/api-keys/{keyId}", s.revokeAPIKey)
	authed("GET /api/v1/organizations/{orgId}/webhooks", s.listWebhooks)
	authed("POST /api/v1/organizations/{orgId}/webhooks", s.createWebhook)
	authed("DELETE /api/v1/webhooks/{webhookId}", s.deleteWebhook)
	authed("GET /api/v1/webhooks/{webhookId}/deliveries", s.listDeliveries)
	authed("POST /api/v1/webhooks/{webhookId}/test", s.testWebhook)

	authed("GET /api/v1/organizations/{orgId}/domains", s.listDomains)
	authed("POST /api/v1/organizations/{orgId}/domains", s.createDomain)
	authed("GET /api/v1/domains/{domainId}", s.getDomain)
	authed("DELETE /api/v1/domains/{domainId}", s.deleteDomain)
	authed("GET /api/v1/domains/{domainId}/dns", s.domainDNS)
	authed("POST /api/v1/domains/{domainId}/verify", s.verifyDomain)
	authed("GET /api/v1/domains/{domainId}/dmarc", s.domainDMARC)

	authed("GET /api/v1/organizations/{orgId}/mailboxes", s.listMailboxes)
	authed("POST /api/v1/organizations/{orgId}/mailboxes", s.createMailbox)
	authed("PATCH /api/v1/mailboxes/{mailboxId}", s.updateMailbox)
	authed("DELETE /api/v1/mailboxes/{mailboxId}", s.deleteMailbox)
	authed("GET /api/v1/mailboxes/{mailboxId}/delegates", s.listDelegates)
	authed("POST /api/v1/mailboxes/{mailboxId}/delegates", s.addDelegate)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/delegates/{userId}", s.removeDelegate)

	authed("GET /api/v1/organizations/{orgId}/aliases", s.listAliases)
	authed("POST /api/v1/organizations/{orgId}/aliases", s.createAlias)
	authed("DELETE /api/v1/aliases/{aliasId}", s.deleteAlias)

	authed("GET /api/v1/me/mailboxes", s.myMailboxes)
	authed("GET /api/v1/me/app-passwords", s.listAppPasswords)
	authed("POST /api/v1/me/app-passwords", s.createAppPassword)
	authed("DELETE /api/v1/me/app-passwords/{id}", s.revokeAppPassword)

	authed("GET /api/v1/mailboxes/{mailboxId}/folders", s.listFolders)
	authed("GET /api/v1/mailboxes/{mailboxId}/messages", s.listMessages)
	authed("GET /api/v1/mailboxes/{mailboxId}/messages/{uid}", s.getMessage)
	authed("PATCH /api/v1/mailboxes/{mailboxId}/messages/{uid}", s.updateMessage)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/messages/{uid}", s.deleteMessage)
	authed("POST /api/v1/mailboxes/{mailboxId}/messages/{uid}/move", s.moveMessage)
	authed("GET /api/v1/mailboxes/{mailboxId}/messages/{uid}/attachments/{part}", s.getAttachment)
	authed("POST /api/v1/mailboxes/{mailboxId}/send", s.sendMessage)
	authed("PUT /api/v1/mailboxes/{mailboxId}/drafts", s.saveDraft)
	authed("GET /api/v1/mailboxes/{mailboxId}/scheduled", s.listScheduled)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/scheduled/{scheduledId}", s.cancelScheduled)
	authed("POST /api/v1/mailboxes/{mailboxId}/messages/{uid}/snooze", s.snoozeMessage)
	authed("GET /api/v1/mailboxes/{mailboxId}/snoozed", s.listSnoozed)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/snoozed/{uid}", s.unsnoozeMessage)
	authed("GET /api/v1/mailboxes/{mailboxId}/rules", s.listRules)
	authed("PUT /api/v1/mailboxes/{mailboxId}/rules", s.saveRules)
	authed("GET /api/v1/mailboxes/{mailboxId}/auto-reply", s.getAutoReply)
	authed("PUT /api/v1/mailboxes/{mailboxId}/auto-reply", s.saveAutoReply)
	authed("DELETE /api/v1/mailboxes/{mailboxId}/drafts/{uid}", s.deleteDraft)

	authed("GET /api/v1/contacts", s.listContacts)
	authed("POST /api/v1/contacts", s.saveContact)
	authed("PUT /api/v1/contacts/{id}", s.saveContact)
	authed("DELETE /api/v1/contacts/{id}", s.deleteContact)
	authed("GET /api/v1/directory", s.listDirectory)
	authed("GET /api/v1/recipients", s.suggestRecipients)
	authed("GET /api/v1/calendar/events", s.listEvents)
	authed("POST /api/v1/calendar/events", s.saveEvent)
	authed("PUT /api/v1/calendar/events/{id}", s.saveEvent)
	authed("DELETE /api/v1/calendar/events/{id}", s.deleteEvent)

	authed("GET /api/v1/admin/identity-providers", s.listIdentityProviders)
	authed("POST /api/v1/admin/identity-providers", s.createIdentityProvider)
	authed("POST /api/v1/admin/identity-providers/discover", s.discoverIdentityProvider)
	authed("PUT /api/v1/admin/identity-providers/{id}", s.updateIdentityProvider)
	authed("DELETE /api/v1/admin/identity-providers/{id}", s.deleteIdentityProvider)
	authed("PATCH /api/v1/admin/users/{id}", s.updateUserStatus)

	return instrument(securityHeaders(mux))
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.kv.Ping(ctx).Err(); err != nil {
		http.Error(w, "valkey unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type principal struct {
	User        User
	sessionHash []byte
	csrf        string
	providerID  *string
	idToken     *string
	roles       map[string]string
	apiKey      *apiKeyIdentity
}

func (p *principal) orgRole(orgID string) string {
	if p.User.IsPlatformAdmin {
		return "owner"
	}
	return p.roles[orgID]
}

func (p *principal) canAdminOrg(orgID string) bool {
	role := p.orgRole(orgID)
	return role == "owner" || role == "admin"
}

func (s *Server) withPrincipal(h func(http.ResponseWriter, *http.Request, *principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && strings.HasPrefix(token, apiKeyPrefix) {
			if p, ok := s.apiKeyPrincipal(w, r, token); ok {
				h(w, r, p)
			}
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		hash := secrets.SHA256(c.Value)
		p := &principal{sessionHash: hash, roles: map[string]string{}}
		err = s.db.QueryRow(r.Context(), `
			UPDATE sessions s SET last_seen_at = now()
			FROM users u
			WHERE s.id_hash = $1 AND s.expires_at > now() AND u.id = s.user_id AND u.status = 'active'
			RETURNING u.id, u.email, u.display_name, u.is_platform_admin, u.status, s.csrf_token, s.provider_id, s.id_token`, hash).
			Scan(&p.User.ID, &p.User.Email, &p.User.DisplayName, &p.User.IsPlatformAdmin, &p.User.Status, &p.csrf, &p.providerID, &p.idToken)
		if errors.Is(err, pgx.ErrNoRows) {
			clearCookie(w, sessionCookie, s.cfg.SecureCookies())
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Your session has ended. Sign in again.")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !constantTimeEqual(r.Header.Get("X-CSRF-Token"), p.csrf) {
				writeError(w, http.StatusForbidden, "csrf", "The request is missing a valid CSRF token. Reload the page and try again.")
				return
			}
		}
		rows, err := s.db.Query(r.Context(), `SELECT org_id, role FROM organization_members WHERE user_id = $1`, p.User.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		for rows.Next() {
			var org, role string
			if err := rows.Scan(&org, &role); err != nil {
				rows.Close()
				s.internalError(w, r, err)
				return
			}
			p.roles[org] = role
		}
		rows.Close()
		h(w, r, p)
	}
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var e apiError
	e.Error.Code, e.Error.Message = code, message
	writeJSON(w, status, e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong on the server. Try again, and check the server logs if it keeps happening.")
}

func notFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "The requested resource does not exist or you do not have access to it.")
}

func forbidden(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "forbidden", "You do not have permission to do this.")
}

func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "The request body is too large.")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_json", "The request body is not valid JSON for this endpoint: "+err.Error())
		}
		return false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "The request body must contain a single JSON object.")
		return false
	}
	return true
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func pathID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	id := strings.ToLower(r.PathValue(name))
	if !uuidPattern.MatchString(id) {
		notFound(w)
		return "", false
	}
	return id, true
}

func (s *Server) clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && s.cfg.TrustProxy {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) audit(ctx context.Context, r *http.Request, p *principal, orgID *string, action, targetType, targetID string, meta map[string]any) {
	var actor *string
	if meta == nil {
		meta = map[string]any{}
	}
	switch {
	case p != nil && p.apiKey != nil:
		meta["apiKeyId"], meta["apiKeyName"] = p.apiKey.ID, p.apiKey.Name
	case p != nil:
		actor = &p.User.ID
	}
	var eventID int64
	var at time.Time
	err := s.db.QueryRow(ctx, `INSERT INTO audit_events (actor_user_id, org_id, action, target_type, target_id, ip, metadata) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, created_at`,
		actor, orgID, action, targetType, targetID, s.clientIP(r), meta).Scan(&eventID, &at)
	if err != nil {
		s.log.Error("audit write failed", "action", action, "err", err)
		return
	}
	if orgID != nil {
		s.enqueueWebhooks(ctx, *orgID, action, map[string]any{
			"id": strconv.FormatInt(eventID, 10), "type": action, "createdAt": at, "organizationId": *orgID,
			"actor":  map[string]any{"userId": actor, "apiKeyId": meta["apiKeyId"]},
			"target": map[string]string{"type": targetType, "id": targetID},
			"data":   meta,
		})
	}
}

func (s *Server) allow(ctx context.Context, key string, perMinute int64) bool {
	window := time.Now().Unix() / 60
	k := "aether:ratelimit:" + key + ":" + strconv.FormatInt(window, 10)
	pipe := s.kv.TxPipeline()
	count := pipe.Incr(ctx, k)
	pipe.Expire(ctx, k, 2*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		s.log.Error("rate limit check failed; allowing request", "err", err)
		return true
	}
	return count.Val() <= perMinute
}
