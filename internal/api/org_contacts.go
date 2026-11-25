package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"
)

type orgContact struct {
	ID      string `json:"id"`
	OrgID   string `json:"orgId"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Company string `json:"company"`
	Phone   string `json:"phone"`
}

const orgContactColumns = `id, org_id, name, email, company, phone`

func (s *Server) listOrgContacts(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+orgContactColumns+` FROM organization_contacts WHERE org_id = $1 ORDER BY lower(name), email`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[orgContact])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) saveOrgContact(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var id string
	create := r.PathValue("id") == ""
	if !create {
		if id, ok = pathID(w, r, "id"); !ok {
			return
		}
	}
	var in struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Company string `json:"company"`
		Phone   string `json:"phone"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	c := orgContact{ID: id, OrgID: orgID, Name: strings.TrimSpace(in.Name), Company: strings.TrimSpace(in.Company), Phone: strings.TrimSpace(in.Phone)}
	addr, err := mail.ParseAddress(strings.TrimSpace(in.Email))
	switch {
	case c.Name == "" || len(c.Name) > 200:
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", "Enter a name of up to 200 characters.")
		return
	case err != nil || addr.Name != "" || len(addr.Address) > 254:
		writeError(w, http.StatusUnprocessableEntity, "invalid_email", "Enter a valid email address.")
		return
	case len(c.Company) > 200:
		writeError(w, http.StatusUnprocessableEntity, "invalid_company", "Keep the company under 200 characters.")
		return
	case len(c.Phone) > 50:
		writeError(w, http.StatusUnprocessableEntity, "invalid_phone", "Keep the phone number under 50 characters.")
		return
	}
	c.Email = strings.ToLower(addr.Address)
	if create {
		err = s.db.QueryRow(r.Context(), `INSERT INTO organization_contacts (org_id, name, email, company, phone) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			orgID, c.Name, c.Email, c.Company, c.Phone).Scan(&c.ID)
	} else {
		err = s.db.QueryRow(r.Context(), `UPDATE organization_contacts SET name = $3, email = $4, company = $5, phone = $6, updated_at = now() WHERE id = $1 AND org_id = $2 RETURNING id`,
			id, orgID, c.Name, c.Email, c.Company, c.Phone).Scan(&c.ID)
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		notFound(w)
		return
	case isUniqueViolation(err):
		writeError(w, http.StatusConflict, "contact_exists", c.Email+" is already an organization contact.")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	action, status := "contact.update", http.StatusOK
	if create {
		action, status = "contact.create", http.StatusCreated
	}
	s.audit(r.Context(), r, p, &orgID, action, "contact", c.ID, map[string]any{"name": c.Name, "email": c.Email, "company": c.Company, "phone": c.Phone})
	writeJSON(w, status, c)
}

func (s *Server) deleteOrgContact(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var email string
	err := s.db.QueryRow(r.Context(), `DELETE FROM organization_contacts WHERE id = $1 AND org_id = $2 RETURNING email`, id, orgID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "contact.delete", "contact", id, map[string]any{"email": email})
	w.WriteHeader(http.StatusNoContent)
}
