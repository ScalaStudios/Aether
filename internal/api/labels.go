package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	maxLabels        = 50
	maxSavedSearches = 50
)

type labelRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	Keyword  string `json:"keyword"`
	Position int    `json:"position"`
}

type savedSearch struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Query          string `json:"query"`
	Unread         bool   `json:"unread"`
	Flagged        bool   `json:"flagged"`
	HasAttachments bool   `json:"hasAttachments"`
	From           string `json:"from,omitempty"`
	To             string `json:"to,omitempty"`
	Since          string `json:"since,omitempty"`
	Until          string `json:"until,omitempty"`
	LabelID        string `json:"labelId,omitempty"`
	Folder         string `json:"folder"`
	AllFolders     bool   `json:"allFolders"`
	Position       int    `json:"position"`
}

func (s *Server) labelRefs(ctx context.Context, mailboxID string) ([]mailpkg.LabelRef, error) {
	rows, err := s.db.Query(ctx, `SELECT id, keyword, color, position FROM mailbox_labels WHERE mailbox_id = $1 ORDER BY position, lower(name)`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mailpkg.LabelRef
	for rows.Next() {
		var label mailpkg.LabelRef
		if err := rows.Scan(&label.ID, &label.Keyword, &label.Color, &label.Position); err != nil {
			return nil, err
		}
		out = append(out, label)
	}
	return out, rows.Err()
}

func (s *Server) loadLabels(ctx context.Context, mailboxID string) ([]labelRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, color, keyword, position FROM mailbox_labels WHERE mailbox_id = $1 ORDER BY position, lower(name)`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []labelRecord
	for rows.Next() {
		var label labelRecord
		if err := rows.Scan(&label.ID, &label.Name, &label.Color, &label.Keyword, &label.Position); err != nil {
			return nil, err
		}
		out = append(out, label)
	}
	return out, rows.Err()
}

func cleanLabelName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 40 || strings.ContainsFunc(name, unicode.IsControl) {
		return "", false
	}
	return name, true
}

func uniqueViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName, true
	}
	return "", false
}

func (s *Server) listLabels(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	labels, err := s.loadLabels(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(labels))
}

func (s *Server) createLabel(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	name, ok := cleanLabelName(in.Name)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_label", "Use a label name of up to 40 characters.")
		return
	}
	if !mailpkg.ValidLabelColor(in.Color) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_label", "Choose red, orange, yellow, green, blue, purple, gray, or pink.")
		return
	}
	var count int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM mailbox_labels WHERE mailbox_id = $1`, m.ID).Scan(&count); err != nil {
		s.internalError(w, r, err)
		return
	}
	if count >= maxLabels {
		writeError(w, http.StatusUnprocessableEntity, "too_many_labels", "A mailbox can have up to 50 labels.")
		return
	}
	labels, err := s.loadLabels(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	taken := map[string]bool{}
	for _, label := range labels {
		taken[label.Keyword] = true
		if strings.EqualFold(label.Name, name) {
			writeError(w, http.StatusConflict, "label_exists", "A label named "+name+" already exists.")
			return
		}
	}
	keyword := mailpkg.NextKeyword(name, func(keyword string) bool { return taken[keyword] })
	if keyword == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_label", "Choose a different name. This one is too similar to a label that already exists.")
		return
	}
	var label labelRecord
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO mailbox_labels (mailbox_id, name, color, keyword, position)
		VALUES ($1, $2, $3, $4, (SELECT COALESCE(MAX(position), -1) + 1 FROM mailbox_labels WHERE mailbox_id = $1))
		RETURNING id, name, color, keyword, position`, m.ID, name, in.Color, keyword).Scan(&label.ID, &label.Name, &label.Color, &label.Keyword, &label.Position)
	if constraint, ok := uniqueViolation(err); ok {
		if strings.Contains(constraint, "name") {
			writeError(w, http.StatusConflict, "label_exists", "A label named "+name+" already exists.")
			return
		}
		writeError(w, http.StatusConflict, "label_exists", "Choose a different name. This one is too similar to a label that already exists.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.label.create", "mailbox", m.ID, map[string]any{"labelId": label.ID, "name": label.Name, "keyword": label.Keyword, "color": label.Color})
	writeJSON(w, http.StatusCreated, label)
}

func (s *Server) updateLabel(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "labelId")
	if !ok {
		return
	}
	var in struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	name, ok := cleanLabelName(in.Name)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_label", "Use a label name of up to 40 characters.")
		return
	}
	if !mailpkg.ValidLabelColor(in.Color) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_label", "Choose red, orange, yellow, green, blue, purple, gray, or pink.")
		return
	}
	var current labelRecord
	err := s.db.QueryRow(r.Context(), `SELECT id, name, color, keyword, position FROM mailbox_labels WHERE mailbox_id = $1 AND id = $2`, m.ID, id).
		Scan(&current.ID, &current.Name, &current.Color, &current.Keyword, &current.Position)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var named string
	err = s.db.QueryRow(r.Context(), `SELECT name FROM mailbox_labels WHERE mailbox_id = $1 AND lower(name) = lower($2) AND id <> $3`, m.ID, name, id).Scan(&named)
	if err == nil {
		writeError(w, http.StatusConflict, "label_exists", "A label named "+name+" already exists.")
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, r, err)
		return
	}
	oldName, oldColor := current.Name, current.Color
	if _, err := s.db.Exec(r.Context(), `UPDATE mailbox_labels SET name = $3, color = $4 WHERE mailbox_id = $1 AND id = $2`, m.ID, id, name, in.Color); err != nil {
		if constraint, ok := uniqueViolation(err); ok && strings.Contains(constraint, "name") {
			writeError(w, http.StatusConflict, "label_exists", "A label named "+name+" already exists.")
			return
		}
		s.internalError(w, r, err)
		return
	}
	if in.Color != oldColor {
		if err := s.recolorLabel(r, m, current.Keyword); err != nil {
			s.db.Exec(r.Context(), `UPDATE mailbox_labels SET name = $3, color = $4 WHERE mailbox_id = $1 AND id = $2`, m.ID, id, oldName, oldColor)
			mailBackendErrs.Inc()
			s.log.Error("updating a label color failed", "mailbox", m.ID, "err", err)
			writeError(w, http.StatusBadGateway, "rules_not_saved", "The color could not be saved on the mail server. Nothing was changed. Try again.")
			return
		}
	}
	current.Name, current.Color = name, in.Color
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.label.update", "mailbox", m.ID, map[string]any{"labelId": id, "name": name, "color": in.Color, "keyword": current.Keyword})
	writeJSON(w, http.StatusOK, current)
}

func (s *Server) recolorLabel(r *http.Request, m *ownedMailbox, keyword string) error {
	rules, reply, err := s.loadSieveSettings(r.Context(), m.ID)
	if err != nil {
		return err
	}
	if rulesUseLabel(rules, keyword) {
		if err := s.persistSieve(r.Context(), m, rules, reply); err != nil {
			return err
		}
	}
	catalog, err := s.labelRefs(r.Context(), m.ID)
	if err != nil {
		return err
	}
	folders, err := s.mail.Folders(m.Address)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(folders))
	for _, folder := range folders {
		names = append(names, folder.Name)
	}
	if err := s.mail.AdjustKeyword(m.Address, names, keyword, func(flags []string) ([]string, []string) {
		return mailpkg.DiffLabelFlags(flags, catalog, nil, nil)
	}); err != nil {
		s.log.Error("updating label colors on messages failed", "mailbox", m.ID, "keyword", keyword, "err", err)
	}
	return nil
}

func (s *Server) deleteLabel(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "labelId")
	if !ok {
		return
	}
	var name, keyword string
	err := s.db.QueryRow(r.Context(), `SELECT name, keyword FROM mailbox_labels WHERE mailbox_id = $1 AND id = $2`, m.ID, id).Scan(&name, &keyword)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rules, reply, err := s.loadSieveSettings(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	updated, blocked := rulesWithoutLabel(rules, keyword)
	if len(blocked) > 0 {
		writeError(w, http.StatusConflict, "label_in_use", name+" is the only action on "+quotedList(blocked)+". Add another action to that rule, or delete the rule, before deleting this label.")
		return
	}
	rulesChanged := rulesUseLabel(rules, keyword)
	if rulesChanged {
		catalog, err := s.labelRefs(r.Context(), m.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		var kept []mailpkg.LabelRef
		for _, label := range catalog {
			if label.ID != id {
				kept = append(kept, label)
			}
		}
		if err := s.persistSieveWith(r.Context(), m, updated, reply, kept); err != nil {
			mailBackendErrs.Inc()
			s.log.Error("removing a label from mail rules failed", "mailbox", m.ID, "err", err)
			writeError(w, http.StatusBadGateway, "rules_not_saved", "The label could not be removed from mail rules. Nothing was changed. Try again.")
			return
		}
	}
	catalog, err := s.labelRefs(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var kept []mailpkg.LabelRef
	for _, label := range catalog {
		if label.Keyword != keyword {
			kept = append(kept, label)
		}
	}
	folders, err := s.mail.Folders(m.Address)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	names := make([]string, 0, len(folders))
	for _, folder := range folders {
		names = append(names, folder.Name)
	}
	if err := s.mail.AdjustKeyword(m.Address, names, keyword, func(flags []string) ([]string, []string) {
		return mailpkg.DiffLabelFlags(flags, kept, nil, []string{keyword})
	}); err != nil {
		if rulesChanged {
			writeError(w, http.StatusBadGateway, "mail_unavailable", "The label was removed from mail rules, but it is still on existing messages. Try deleting it again.")
			return
		}
		s.mailError(w, r, err)
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM mailbox_labels WHERE mailbox_id = $1 AND id = $2`, m.ID, id); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.label.delete", "mailbox", m.ID, map[string]any{"labelId": id, "name": name, "keyword": keyword})
	w.WriteHeader(http.StatusNoContent)
}

func rulesUseLabel(rules []mailpkg.Rule, keyword string) bool {
	for _, rule := range rules {
		for _, label := range rule.Actions.Labels {
			if label == keyword {
				return true
			}
		}
	}
	return false
}

func ruleHasAction(rule mailpkg.Rule) bool {
	return rule.Actions.MoveTo != "" || rule.Actions.Delete || rule.Actions.MarkRead || rule.Actions.Flag || len(rule.Actions.ForwardTo) > 0 || len(rule.Actions.Labels) > 0
}

func rulesWithoutLabel(rules []mailpkg.Rule, keyword string) ([]mailpkg.Rule, []string) {
	out := make([]mailpkg.Rule, len(rules))
	var blocked []string
	for i, rule := range rules {
		out[i] = rule
		next := make([]string, 0, len(rule.Actions.Labels))
		for _, label := range rule.Actions.Labels {
			if label != keyword {
				next = append(next, label)
			}
		}
		if len(next) == len(rule.Actions.Labels) {
			continue
		}
		if len(next) == 0 {
			next = nil
		}
		out[i].Actions.Labels = next
		if !ruleHasAction(out[i]) {
			blocked = append(blocked, rule.Name)
		}
	}
	return out, blocked
}

func quotedList(names []string) string {
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = "“" + name + "”"
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}

func (s *Server) applyLabels(w http.ResponseWriter, r *http.Request, m *ownedMailbox, folder string, uids []uint32, addIDs, removeIDs []string) bool {
	if len(addIDs) > maxLabels || len(removeIDs) > maxLabels {
		writeError(w, http.StatusUnprocessableEntity, "invalid_label", "Change up to 50 labels at a time.")
		return false
	}
	if !distinctIDs(addIDs) || !distinctIDs(removeIDs) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_label", "Choose a label from this mailbox.")
		return false
	}
	for _, id := range addIDs {
		for _, other := range removeIDs {
			if id == other {
				writeError(w, http.StatusUnprocessableEntity, "invalid_label", "Add and remove different labels.")
				return false
			}
		}
	}
	catalog, err := s.labelRefs(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	byID := map[string]mailpkg.LabelRef{}
	for _, label := range catalog {
		byID[label.ID] = label
	}
	add := make([]string, 0, len(addIDs))
	for _, id := range addIDs {
		label, ok := byID[id]
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid_label", "That label does not exist.")
			return false
		}
		add = append(add, label.Keyword)
	}
	drop := make([]string, 0, len(removeIDs))
	for _, id := range removeIDs {
		label, ok := byID[id]
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid_label", "That label does not exist.")
			return false
		}
		drop = append(drop, label.Keyword)
	}
	if err := s.mail.AdjustKeywords(m.Address, folder, uids, func(flags []string) ([]string, []string) {
		return mailpkg.DiffLabelFlags(flags, catalog, add, drop)
	}); err != nil {
		s.mailError(w, r, err)
		return false
	}
	return true
}

func distinctIDs(ids []string) bool {
	seen := map[string]bool{}
	for _, id := range ids {
		if !uuidPattern.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
