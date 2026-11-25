package api

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type outboundRoute struct {
	Kind           string   `json:"kind"`
	Name           string   `json:"name"`
	Target         string   `json:"target"`
	TLSMode        string   `json:"tlsMode"`
	MatchNames     []string `json:"matchNames"`
	HasCredentials bool     `json:"hasCredentials"`
}

func (s *Server) listOutboundRoutes(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT kind, name, target, tls_mode, match_names, credentials IS NOT NULL
		FROM outbound_routes ORDER BY kind, name`)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	routes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[outboundRoute])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(routes))
}

func outboundDomain(name string) bool {
	return len(name) <= 253 && domainPattern.MatchString(name)
}

func outboundEndpoint(endpoint string) bool {
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil || !strings.HasPrefix(endpoint, "[") {
		return false
	}
	port, err := strconv.Atoi(portText)
	return err == nil && port >= 1 && port <= 65535 && strconv.Itoa(port) == portText &&
		(outboundDomain(host) || net.ParseIP(host).To4() != nil)
}

func outboundTLSKey(key string) bool {
	if outboundDomain(key) || outboundEndpoint(key) {
		return true
	}
	if host, ok := strings.CutPrefix(key, "["); ok {
		host, _, ok = strings.Cut(host, "]")
		if ok && (key == "["+host+"]" || key == "["+host+"]:smtp" || key == "["+host+"]:submission") {
			return outboundDomain(host) || net.ParseIP(host).To4() != nil
		}
	}
	host, _, ok := strings.Cut(key, ":")
	return ok && outboundDomain(host) && outboundEndpoint("["+host+"]:"+strings.TrimPrefix(key, host+":"))
}

func outboundCredentialContext(domain string) string {
	return "outbound-relay:" + domain
}

func (s *Server) saveOutboundRoute(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	var in struct {
		outboundRoute
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, 16<<10, &in) {
		return
	}
	in.Name = strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	in.Kind = r.PathValue("kind")
	in.Target = strings.ToLower(strings.TrimSpace(in.Target))
	in.MatchNames = nonNil(in.MatchNames)
	if err := validateOutboundRoute(in.outboundRoute, in.Username, in.Password, s.cfg.MailHostname); err != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_outbound_route", err)
		return
	}
	for i, name := range in.MatchNames {
		in.MatchNames[i] = strings.ToLower(strings.TrimSpace(name))
	}
	var sealed []byte
	if in.Username != "" {
		sealed = s.box.Seal([]byte(in.Username+":"+in.Password), outboundCredentialContext(in.Name))
	}
	tag, err := s.db.Exec(r.Context(), `INSERT INTO outbound_routes (kind, name, target, tls_mode, match_names, credentials)
		SELECT $1, $2, $3, $4, $5, $6
		WHERE $1 <> 'transport' OR NOT EXISTS (SELECT 1 FROM mail_domains WHERE name = $2)
		ON CONFLICT (kind, name) DO UPDATE SET target = EXCLUDED.target, tls_mode = EXCLUDED.tls_mode,
			match_names = EXCLUDED.match_names, credentials = EXCLUDED.credentials`,
		in.Kind, in.Name, in.Target, in.TLSMode, in.MatchNames, sealed)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		writeError(w, http.StatusUnprocessableEntity, "unknown_sender_domain", "Choose a sender domain registered in Aether.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusUnprocessableEntity, "hosted_recipient_domain", "Choose a recipient domain that Aether does not host.")
		return
	}
	in.HasCredentials = sealed != nil
	s.audit(r.Context(), r, p, nil, "outbound_route.save", in.Kind, in.Name, nil)
	writeJSON(w, http.StatusOK, in.outboundRoute)
}

func validateOutboundRoute(route outboundRoute, username, password, mailHost string) string {
	if route.Kind != "relay" && (username != "" || password != "") {
		return "Credentials are only supported for sender relays."
	}
	switch route.Kind {
	case "relay", "transport":
		if !outboundDomain(route.Name) || route.Name == mailHost || route.Name == "aether-reports.internal" {
			return "Enter a valid routing domain."
		}
		if !outboundEndpoint(route.Target) {
			return "Enter the SMTP endpoint as [hostname]:port or [IPv4 address]:port."
		}
		if route.TLSMode != "" || len(route.MatchNames) > 0 {
			return "Configure TLS in a separate TLS policy."
		}
		if (username == "") != (password == "") || len(username) > 512 || len(password) > 2048 ||
			strings.ContainsAny(username, ":\x00\r\n") || strings.ContainsAny(password, "\x00\r\n") {
			return "Enter both a valid SMTP username and password, or leave both blank."
		}
	case "tls":
		if !outboundTLSKey(route.Name) || route.Target != "" {
			return "Enter a destination domain or the exact [hostname]:port of a fixed route."
		}
		switch route.TLSMode {
		case "verify", "secure":
			for _, name := range route.MatchNames {
				if !outboundDomain(strings.ToLower(strings.TrimLeft(strings.TrimSpace(name), "."))) || strings.HasPrefix(strings.TrimSpace(name), "..") {
					return "Enter valid certificate hostnames or dot-prefixed domain suffixes."
				}
			}
		case "none", "may", "encrypt", "dane":
			if len(route.MatchNames) > 0 {
				return "Certificate hostnames require verify or secure mode."
			}
		default:
			return "Choose a supported TLS mode."
		}
	default:
		return "Choose a supported routing rule."
	}
	return ""
}

func (s *Server) deleteOutboundRoute(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	kind, name := r.PathValue("kind"), strings.ToLower(r.PathValue("name"))
	tag, err := s.db.Exec(r.Context(), `DELETE FROM outbound_routes WHERE kind = $1 AND name = $2`, kind, name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, nil, "outbound_route.delete", kind, name, nil)
	w.WriteHeader(http.StatusNoContent)
}
