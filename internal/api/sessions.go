package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) writeSessions(w http.ResponseWriter, r *http.Request, p *principal, userID string) {
	type session struct {
		ID         string    `json:"id"`
		IP         string    `json:"ip"`
		UserAgent  string    `json:"userAgent"`
		Provider   *string   `json:"provider"`
		CreatedAt  time.Time `json:"createdAt"`
		LastSeenAt time.Time `json:"lastSeenAt"`
		Current    bool      `json:"current"`
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT s.id, s.ip, s.user_agent, op.name, s.created_at, s.last_seen_at, s.id_hash = $2
		FROM sessions s LEFT JOIN oidc_providers op ON op.id = s.provider_id
		WHERE s.user_id = $1 AND s.expires_at > now()
		ORDER BY s.id_hash = $2 DESC, s.last_seen_at DESC`, userID, p.sessionHash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[session])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) endSessions(w http.ResponseWriter, r *http.Request, p *principal, userID string, orgID *string) {
	var sessionID *string
	if r.PathValue("sessionId") != "" {
		id, ok := pathID(w, r, "sessionId")
		if !ok {
			return
		}
		sessionID = &id
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1 AND id_hash <> $2 AND ($3::uuid IS NULL OR id = $3)`, userID, p.sessionHash, sessionID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if sessionID != nil && tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, orgID, "user.sessions_end", "user", userID, map[string]any{"sessionId": sessionID, "count": tag.RowsAffected()})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMySessions(w http.ResponseWriter, r *http.Request, p *principal) {
	s.writeSessions(w, r, p, p.User.ID)
}

func (s *Server) endMySessions(w http.ResponseWriter, r *http.Request, p *principal) {
	s.endSessions(w, r, p, p.User.ID, nil)
}

func (s *Server) sessionMember(w http.ResponseWriter, r *http.Request, p *principal) (string, string, bool) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return "", "", false
	}
	userID, ok := pathID(w, r, "userId")
	if !ok {
		return "", "", false
	}
	var role string
	var platformAdmin bool
	err := s.db.QueryRow(r.Context(), `SELECT m.role, u.is_platform_admin FROM organization_members m JOIN users u ON u.id = m.user_id WHERE m.org_id = $1 AND m.user_id = $2`, orgID, userID).Scan(&role, &platformAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return "", "", false
	}
	if err != nil {
		s.internalError(w, r, err)
		return "", "", false
	}
	if (role == "owner" && p.orgRole(orgID) != "owner") || (platformAdmin && !p.User.IsPlatformAdmin) {
		forbidden(w)
		return "", "", false
	}
	return orgID, userID, true
}

func (s *Server) listMemberSessions(w http.ResponseWriter, r *http.Request, p *principal) {
	if _, userID, ok := s.sessionMember(w, r, p); ok {
		s.writeSessions(w, r, p, userID)
	}
}

func (s *Server) endMemberSessions(w http.ResponseWriter, r *http.Request, p *principal) {
	if orgID, userID, ok := s.sessionMember(w, r, p); ok {
		s.endSessions(w, r, p, userID, &orgID)
	}
}
