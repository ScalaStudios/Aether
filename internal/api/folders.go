package api

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func folderPath(parent, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, "./*%") || strings.HasPrefix(name, "~") || strings.ContainsFunc(name, unicode.IsControl) || mailpkg.SharedFolder(cmp.Or(parent, name)) {
		return "", false
	}
	if parent == "" {
		return name, true
	}
	return parent + "/" + name, true
}

func invalidFolderName(w http.ResponseWriter) {
	writeError(w, http.StatusUnprocessableEntity, "invalid_folder_name", "Use up to 100 characters. Folder names cannot contain . / * or %, or start with ~.")
}

func folderQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_folder", "Choose a folder.")
	}
	return folder, folder != ""
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Name   string `json:"name"`
		Parent string `json:"parent"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	name, ok := folderPath(in.Parent, in.Name)
	if !ok {
		invalidFolderName(w)
		return
	}
	if err := s.mail.CreateFolder(m.Address, name); err != nil {
		s.mailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *Server) updateFolder(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	folder, ok := folderQuery(w, r)
	if !ok {
		return
	}
	var in struct {
		Name       *string `json:"name"`
		Parent     *string `json:"parent"`
		Subscribed *bool   `json:"subscribed"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	name := folder
	if in.Name != nil || in.Parent != nil {
		parent, leaf := "", folder
		if i := strings.LastIndex(folder, "/"); i >= 0 {
			parent, leaf = folder[:i], folder[i+1:]
		}
		if in.Parent != nil {
			parent = *in.Parent
		}
		if in.Name != nil {
			leaf = *in.Name
		}
		if parent == folder || strings.HasPrefix(parent, folder+"/") {
			writeError(w, http.StatusUnprocessableEntity, "invalid_parent", "A folder cannot be moved into itself or one of its subfolders.")
			return
		}
		if name, ok = folderPath(parent, leaf); !ok {
			invalidFolderName(w)
			return
		}
	}
	if name != folder {
		if err := s.mail.RenameFolder(m.Address, folder, name); err != nil {
			s.mailError(w, r, err)
			return
		}
		if err := s.renameRuleFolders(r.Context(), m, folder, name); err != nil {
			mailBackendErrs.Inc()
			s.log.Error("updating mail rules after a folder rename failed", "mailbox", m.ID, "err", err)
			writeError(w, http.StatusBadGateway, "rules_not_updated", "The folder was renamed, but the mail rules that file into it could not be updated. Check them in Settings.")
			return
		}
	}
	if in.Subscribed != nil {
		if err := s.mail.SetSubscribed(m.Address, name, *in.Subscribed); err != nil {
			s.mailError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	folder, ok := folderQuery(w, r)
	if !ok {
		return
	}
	moveTo := r.URL.Query().Get("moveTo")
	if moveTo == folder {
		writeError(w, http.StatusUnprocessableEntity, "invalid_folder", "Choose a different folder to move the messages to.")
		return
	}
	if err := s.mail.DeleteFolder(m.Address, folder, moveTo); err != nil {
		s.mailError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) emptyFolder(w http.ResponseWriter, r *http.Request, p *principal) {
	s.folderAction(w, r, p, s.mail.EmptyFolder)
}

func (s *Server) markFolderRead(w http.ResponseWriter, r *http.Request, p *principal) {
	s.folderAction(w, r, p, s.mail.MarkFolderRead)
}

func (s *Server) folderAction(w http.ResponseWriter, r *http.Request, p *principal, action func(address, folder string) error) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	folder, ok := folderQuery(w, r)
	if !ok {
		return
	}
	if err := action(m.Address, folder); err != nil {
		s.mailError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) renameRuleFolders(ctx context.Context, m *ownedMailbox, from, to string) error {
	rules, reply, err := s.loadSieveSettings(ctx, m.ID)
	if err != nil {
		return err
	}
	changed := false
	for i, rule := range rules {
		if rule.Actions.MoveTo == from || strings.HasPrefix(rule.Actions.MoveTo, from+"/") {
			rules[i].Actions.MoveTo = to + strings.TrimPrefix(rule.Actions.MoveTo, from)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE mailbox_rules SET rules = $2, updated_at = now() WHERE mailbox_id = $1`, m.ID, raw); err != nil {
			return err
		}
		return s.mail.PutSieve(m.Address, mailpkg.CompileSieve(rules, reply, m.Address))
	})
}
