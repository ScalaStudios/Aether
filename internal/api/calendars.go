package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

var colorPattern = regexp.MustCompile(`^(#[0-9a-fA-F]{6})?$`)

type calendarOwner struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type calendarShare struct {
	ID      string  `json:"id"`
	Access  string  `json:"access"`
	Email   *string `json:"email,omitempty"`
	Name    *string `json:"name,omitempty"`
	OrgID   *string `json:"orgId,omitempty"`
	OrgName *string `json:"orgName,omitempty"`
}

type calendarInfo struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Color  string          `json:"color"`
	Access string          `json:"access"`
	Owner  *calendarOwner  `json:"owner,omitempty"`
	Shares []calendarShare `json:"shares,omitempty"`
}

func (s *Server) listCalendars(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.davCollections(ctx, "calendar")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]calendarInfo, len(rows))
	index := map[string]int{}
	ids := make([]string, len(rows))
	for i, c := range rows {
		out[i] = calendarInfo{ID: c.Slug, Name: c.Name, Color: c.Color, Access: c.Access}
		index[c.ID], ids[i] = i, c.ID
	}
	owners, err := s.db.Query(ctx, `SELECT c.id, u.display_name, u.email FROM dav_collections c JOIN users u ON u.id = c.user_id WHERE c.id = ANY($1) AND c.user_id <> $2`, ids, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for owners.Next() {
		var id string
		var o calendarOwner
		if err := owners.Scan(&id, &o.Name, &o.Email); err != nil {
			s.internalError(w, r, err)
			return
		}
		out[index[id]].Owner = &o
	}
	shares, err := s.db.Query(ctx, `
		SELECT s.collection_id, s.id, s.access, u.email, u.display_name, o.id, o.name FROM calendar_shares s
		JOIN dav_collections c ON c.id = s.collection_id LEFT JOIN users u ON u.id = s.user_id LEFT JOIN organizations o ON o.id = s.org_id
		WHERE c.id = ANY($1) AND c.user_id = $2 ORDER BY s.created_at`, ids, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for shares.Next() {
		var id string
		var sh calendarShare
		if err := shares.Scan(&id, &sh.ID, &sh.Access, &sh.Email, &sh.Name, &sh.OrgID, &sh.OrgName); err != nil {
			s.internalError(w, r, err)
			return
		}
		out[index[id]].Shares = append(out[index[id]].Shares, sh)
	}
	writeJSON(w, http.StatusOK, out)
}

type calendarInput struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func validCalendarInput(w http.ResponseWriter, in *calendarInput) bool {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 || !colorPattern.MatchString(in.Color) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_calendar", "Give the calendar a name of up to 100 characters and a color like #3b82f6.")
		return false
	}
	return true
}

func (s *Server) createCalendar(w http.ResponseWriter, r *http.Request, p *principal) {
	var in calendarInput
	if !decode(w, r, 4<<10, &in) || !validCalendarInput(w, &in) {
		return
	}
	slug := secrets.Token(10)
	if _, err := s.db.Exec(r.Context(), `INSERT INTO dav_collections (user_id, kind, slug, name, color) VALUES ($1, 'calendar', $2, $3, $4)`, p.User.ID, slug, in.Name, in.Color); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, calendarInfo{ID: slug, Name: in.Name, Color: in.Color, Access: "owner"})
}

func (s *Server) ownedCalendar(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal) (string, bool) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM dav_collections WHERE user_id = $1 AND kind = 'calendar' AND slug = $2`, p.User.ID, r.PathValue("calendarId")).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return "", false
	}
	if err != nil {
		s.internalError(w, r, err)
		return "", false
	}
	return id, true
}

func (s *Server) updateCalendar(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.ownedCalendar(r.Context(), w, r, p)
	if !ok {
		return
	}
	var in calendarInput
	if !decode(w, r, 4<<10, &in) || !validCalendarInput(w, &in) {
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE dav_collections SET name = $2, color = $3, updated_at = now() WHERE id = $1`, id, in.Name, in.Color); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteCalendar(w http.ResponseWriter, r *http.Request, p *principal) {
	if r.PathValue("calendarId") == "personal" {
		writeError(w, http.StatusUnprocessableEntity, "personal_calendar", "Your main calendar can’t be deleted.")
		return
	}
	id, ok := s.ownedCalendar(r.Context(), w, r, p)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM dav_collections WHERE id = $1`, id); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) shareCalendar(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.ownedCalendar(r.Context(), w, r, p)
	if !ok {
		return
	}
	var in struct {
		Email  string `json:"email"`
		OrgID  string `json:"orgId"`
		Access string `json:"access"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	if in.Access != "read" && in.Access != "write" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_access", "Access must be read or write.")
		return
	}
	var err error
	switch {
	case in.OrgID != "" && in.Email == "":
		if !uuidPattern.MatchString(in.OrgID) || !p.canAdminOrg(in.OrgID) {
			writeError(w, http.StatusForbidden, "forbidden", "Only organization administrators can share a calendar with everyone in the organization.")
			return
		}
		_, err = s.db.Exec(r.Context(), `
			INSERT INTO calendar_shares (collection_id, org_id, access) VALUES ($1, $2, $3)
			ON CONFLICT (collection_id, org_id) WHERE org_id IS NOT NULL DO UPDATE SET access = EXCLUDED.access`, id, in.OrgID, in.Access)
	case in.Email != "" && in.OrgID == "":
		var userID string
		err = s.db.QueryRow(r.Context(), `
			SELECT u.id FROM users u WHERE u.email = lower(trim($1)) AND u.id <> $2 AND u.status = 'active' AND EXISTS (
				SELECT 1 FROM organization_members a JOIN organization_members b ON a.org_id = b.org_id WHERE a.user_id = u.id AND b.user_id = $2)`, in.Email, p.User.ID).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnprocessableEntity, "unknown_user", "No one else in your organizations signs in with that address.")
			return
		}
		if err == nil {
			_, err = s.db.Exec(r.Context(), `
				INSERT INTO calendar_shares (collection_id, user_id, access) VALUES ($1, $2, $3)
				ON CONFLICT (collection_id, user_id) WHERE user_id IS NOT NULL DO UPDATE SET access = EXCLUDED.access`, id, userID, in.Access)
		}
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid_share", "Share with either a person's email address or an organization.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) unshareCalendar(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.ownedCalendar(r.Context(), w, r, p)
	if !ok {
		return
	}
	shareID, ok := pathID(w, r, "shareId")
	if !ok {
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM calendar_shares WHERE id = $1 AND collection_id = $2`, shareID, id)
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
