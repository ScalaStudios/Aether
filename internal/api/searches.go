package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const savedSearchColumns = `id, name, query, unread, flagged, has_attachments, from_text, to_text, since_date, until_date, label_id::text, folder, all_folders, position`

func scanSavedSearch(row pgx.Row) (savedSearch, error) {
	var search savedSearch
	var since, until *time.Time
	var labelID *string
	err := row.Scan(&search.ID, &search.Name, &search.Query, &search.Unread, &search.Flagged, &search.HasAttachments, &search.From, &search.To, &since, &until, &labelID, &search.Folder, &search.AllFolders, &search.Position)
	if since != nil {
		search.Since = since.Format(time.DateOnly)
	}
	if until != nil {
		search.Until = until.Format(time.DateOnly)
	}
	if labelID != nil {
		search.LabelID = *labelID
	}
	return search, err
}

func (s *Server) loadSavedSearches(r *http.Request, mailboxID string) ([]savedSearch, error) {
	rows, err := s.db.Query(r.Context(), `SELECT `+savedSearchColumns+` FROM saved_searches WHERE mailbox_id = $1 ORDER BY position, lower(name)`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []savedSearch
	for rows.Next() {
		search, err := scanSavedSearch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, search)
	}
	return out, rows.Err()
}

func (s *Server) savedSearchByID(r *http.Request, mailboxID, id string) (savedSearch, error) {
	return scanSavedSearch(s.db.QueryRow(r.Context(), `SELECT `+savedSearchColumns+` FROM saved_searches WHERE mailbox_id = $1 AND id = $2`, mailboxID, id))
}

type savedSearchInput struct {
	Name           string `json:"name"`
	Query          string `json:"query"`
	Unread         bool   `json:"unread"`
	Flagged        bool   `json:"flagged"`
	HasAttachments bool   `json:"hasAttachments"`
	From           string `json:"from"`
	To             string `json:"to"`
	Since          string `json:"since"`
	Until          string `json:"until"`
	LabelID        string `json:"labelId"`
	Folder         string `json:"folder"`
	AllFolders     bool   `json:"allFolders"`
}

func cleanSearchText(value string, limit int) (string, bool) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > limit || strings.ContainsFunc(value, unicode.IsControl) {
		return "", false
	}
	return value, true
}

func (s *Server) normalizeSavedSearch(w http.ResponseWriter, r *http.Request, m *ownedMailbox, in savedSearchInput) (savedSearchInput, bool) {
	name, ok := cleanSearchText(in.Name, 80)
	if !ok || name == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_search", "Use a search name of up to 80 characters.")
		return in, false
	}
	query, ok := cleanSearchText(in.Query, 500)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_search", "The search text can be up to 500 characters on one line.")
		return in, false
	}
	from, ok := cleanSearchText(in.From, 200)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_search", "The sender filter can be up to 200 characters.")
		return in, false
	}
	to, ok := cleanSearchText(in.To, 200)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_search", "The recipient filter can be up to 200 characters.")
		return in, false
	}
	since, sinceErr := parseDate(in.Since)
	until, untilErr := parseDate(in.Until)
	if sinceErr != nil || untilErr != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_date", "Write since and until as dates like 2026-10-31.")
		return in, false
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_date", "The end date is before the start date.")
		return in, false
	}
	in.Name, in.Query, in.From, in.To = name, query, from, to
	if in.LabelID != "" {
		if !uuidPattern.MatchString(in.LabelID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_label", "That label does not exist.")
			return in, false
		}
		var keyword string
		err := s.db.QueryRow(r.Context(), `SELECT keyword FROM mailbox_labels WHERE mailbox_id = $1 AND id = $2`, m.ID, in.LabelID).Scan(&keyword)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_label", "That label does not exist.")
			return in, false
		}
		if err != nil {
			s.internalError(w, r, err)
			return in, false
		}
	}
	if in.AllFolders {
		in.Folder = ""
		return in, true
	}
	in.Folder = strings.TrimSpace(in.Folder)
	if in.Folder == "" || utf8.RuneCountInString(in.Folder) > 400 || strings.ContainsFunc(in.Folder, unicode.IsControl) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_folder", "Choose a folder for this search, or search every folder.")
		return in, false
	}
	folders, err := s.mail.Folders(m.Address)
	if err != nil {
		s.mailError(w, r, err)
		return in, false
	}
	for _, folder := range folders {
		if folder.Name == in.Folder {
			return in, true
		}
	}
	writeError(w, http.StatusUnprocessableEntity, "invalid_folder", "That folder does not exist.")
	return in, false
}

func (s *Server) listSavedSearches(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	searches, err := s.loadSavedSearches(r, m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(searches))
}

func (s *Server) createSavedSearch(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	var in savedSearchInput
	if !decode(w, r, 16<<10, &in) {
		return
	}
	in, ok = s.normalizeSavedSearch(w, r, m, in)
	if !ok {
		return
	}
	var count int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM saved_searches WHERE mailbox_id = $1`, m.ID).Scan(&count); err != nil {
		s.internalError(w, r, err)
		return
	}
	if count >= maxSavedSearches {
		writeError(w, http.StatusUnprocessableEntity, "too_many_searches", "A mailbox can have up to 50 saved searches.")
		return
	}
	search, err := scanSavedSearch(s.db.QueryRow(r.Context(), `
		INSERT INTO saved_searches (mailbox_id, name, query, unread, flagged, has_attachments, from_text, to_text, since_date, until_date, label_id, folder, all_folders, position)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, '')::date, NULLIF($10, '')::date, NULLIF($11, '')::uuid, $12, $13,
			(SELECT COALESCE(MAX(position), -1) + 1 FROM saved_searches WHERE mailbox_id = $1))
		RETURNING `+savedSearchColumns, m.ID, in.Name, in.Query, in.Unread, in.Flagged, in.HasAttachments, in.From, in.To, in.Since, in.Until, in.LabelID, in.Folder, in.AllFolders))
	if constraint, ok := uniqueViolation(err); ok && strings.Contains(constraint, "name") {
		writeError(w, http.StatusConflict, "search_exists", "A saved search named "+in.Name+" already exists.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.saved_search.create", "mailbox", m.ID, map[string]any{"searchId": search.ID, "name": search.Name})
	writeJSON(w, http.StatusCreated, search)
}

func (s *Server) updateSavedSearch(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "searchId")
	if !ok {
		return
	}
	if _, err := s.savedSearchByID(r, m.ID, id); errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	var in savedSearchInput
	if !decode(w, r, 16<<10, &in) {
		return
	}
	in, ok = s.normalizeSavedSearch(w, r, m, in)
	if !ok {
		return
	}
	search, err := scanSavedSearch(s.db.QueryRow(r.Context(), `
		UPDATE saved_searches SET name = $3, query = $4, unread = $5, flagged = $6, has_attachments = $7, from_text = $8, to_text = $9,
			since_date = NULLIF($10, '')::date, until_date = NULLIF($11, '')::date, label_id = NULLIF($12, '')::uuid, folder = $13, all_folders = $14
		WHERE mailbox_id = $1 AND id = $2
		RETURNING `+savedSearchColumns, m.ID, id, in.Name, in.Query, in.Unread, in.Flagged, in.HasAttachments, in.From, in.To, in.Since, in.Until, in.LabelID, in.Folder, in.AllFolders))
	if constraint, ok := uniqueViolation(err); ok && strings.Contains(constraint, "name") {
		writeError(w, http.StatusConflict, "search_exists", "A saved search named "+in.Name+" already exists.")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.saved_search.update", "mailbox", m.ID, map[string]any{"searchId": search.ID, "name": search.Name})
	writeJSON(w, http.StatusOK, search)
}

func (s *Server) deleteSavedSearch(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "searchId")
	if !ok {
		return
	}
	var name string
	err := s.db.QueryRow(r.Context(), `DELETE FROM saved_searches WHERE mailbox_id = $1 AND id = $2 RETURNING name`, m.ID, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.saved_search.delete", "mailbox", m.ID, map[string]any{"searchId": id, "name": name})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resolveList(w http.ResponseWriter, r *http.Request, m *ownedMailbox, o *mailpkg.ListOptions, folder *string, all *bool) bool {
	q := r.URL.Query()
	switch q.Get("folders") {
	case "":
	case "all":
		*all = true
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid_folder", "Search one folder, or every folder with folders=all.")
		return false
	}
	labelID := strings.TrimSpace(q.Get("label"))
	if savedID := strings.TrimSpace(q.Get("saved")); savedID != "" {
		if !uuidPattern.MatchString(savedID) {
			writeError(w, http.StatusNotFound, "not_found", "That saved search does not exist.")
			return false
		}
		search, err := s.savedSearchByID(r, m.ID, savedID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "That saved search does not exist.")
			return false
		}
		if err != nil {
			s.internalError(w, r, err)
			return false
		}
		since, sinceErr := parseDate(search.Since)
		until, untilErr := parseDate(search.Until)
		if sinceErr != nil || untilErr != nil {
			s.internalError(w, r, errors.Join(sinceErr, untilErr))
			return false
		}
		o.Query, o.Unread, o.Flagged, o.HasAttachments = search.Query, search.Unread, search.Flagged, search.HasAttachments
		o.From, o.To, o.Since, o.Until = search.From, search.To, since, until
		o.Keywords = nil
		labelID = search.LabelID
		if search.AllFolders {
			*all = true
		} else {
			*all = false
			*folder = search.Folder
		}
	}
	if labelID != "" {
		if !uuidPattern.MatchString(labelID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_label", "That label does not exist.")
			return false
		}
		var keyword string
		err := s.db.QueryRow(r.Context(), `SELECT keyword FROM mailbox_labels WHERE mailbox_id = $1 AND id = $2`, m.ID, labelID).Scan(&keyword)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_label", "That label does not exist.")
			return false
		}
		if err != nil {
			s.internalError(w, r, err)
			return false
		}
		o.Keywords = []string{keyword}
	}
	if *all {
		o.Threads = false
	}
	return true
}
