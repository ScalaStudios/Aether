package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

type signature struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	HTML string `json:"html"`
}

type signatureSettings struct {
	Signatures []signature `json:"signatures"`
	NewMessage string      `json:"newMessage"`
	Replies    string      `json:"replies"`
}

var signatureIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

func (s *Server) getSignatures(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	out := signatureSettings{Signatures: []signature{}}
	err := s.db.QueryRow(r.Context(), `SELECT settings FROM mailbox_signatures WHERE mailbox_id = $1`, m.ID).Scan(&out)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveSignatures(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	var in signatureSettings
	if !decode(w, r, 512<<10, &in) {
		return
	}
	if len(in.Signatures) > 20 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_signature", "A mailbox can have at most 20 signatures.")
		return
	}
	ids := map[string]bool{"": true}
	for i := range in.Signatures {
		sig := &in.Signatures[i]
		sig.Name = strings.TrimSpace(sig.Name)
		sig.HTML = mailpkg.SanitizeHTML(sig.HTML)
		switch {
		case !signatureIDPattern.MatchString(sig.ID) || ids[sig.ID]:
			writeError(w, http.StatusUnprocessableEntity, "invalid_signature", "Each signature needs its own id.")
			return
		case sig.Name == "" || len(sig.Name) > 100:
			writeError(w, http.StatusUnprocessableEntity, "invalid_signature", "Give each signature a name of up to 100 characters.")
			return
		case len(sig.HTML) > 64<<10:
			writeError(w, http.StatusUnprocessableEntity, "invalid_signature", "Keep each signature under 64 KB.")
			return
		}
		ids[sig.ID] = true
	}
	if !ids[in.NewMessage] || !ids[in.Replies] {
		writeError(w, http.StatusUnprocessableEntity, "invalid_signature", "Choose default signatures from this mailbox's list.")
		return
	}
	if in.Signatures == nil {
		in.Signatures = []signature{}
	}
	if _, err := s.db.Exec(r.Context(), `
		INSERT INTO mailbox_signatures (mailbox_id, settings) VALUES ($1, $2)
		ON CONFLICT (mailbox_id) DO UPDATE SET settings = excluded.settings, updated_at = now()`, m.ID, in); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}
