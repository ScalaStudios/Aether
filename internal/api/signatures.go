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
	DisplayName string      `json:"displayName,omitempty"`
	Signatures  []signature `json:"signatures"`
	NewMessage  string      `json:"newMessage"`
	Replies     string      `json:"replies"`
}

var signatureIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

func (s *Server) signatureAddress(w http.ResponseWriter, r *http.Request, p *principal, m *ownedMailbox) (string, bool) {
	address := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("fromAddress")))
	if address == "" || address == m.Address {
		return "", true
	}
	from, _, ok := s.resolveSender(w, r, p, m, address)
	return from.Address, ok
}

func (s *Server) getSignatures(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	address, ok := s.signatureAddress(w, r, p, m)
	if !ok {
		return
	}
	out := signatureSettings{Signatures: []signature{}}
	err := s.db.QueryRow(r.Context(), `SELECT settings FROM mailbox_signatures WHERE mailbox_id = $1 AND from_address = $2`, m.ID, address).Scan(&out)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveSignatures(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	address, ok := s.signatureAddress(w, r, p, m)
	if !ok {
		return
	}
	var in signatureSettings
	if !decode(w, r, 512<<10, &in) {
		return
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if len(in.DisplayName) > 200 || strings.ContainsAny(in.DisplayName, "\r\n") {
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", "Use a display name of up to 200 characters on one line.")
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
		INSERT INTO mailbox_signatures (mailbox_id, from_address, settings) VALUES ($1, $2, $3)
		ON CONFLICT (mailbox_id, from_address) DO UPDATE SET settings = excluded.settings, updated_at = now()`, m.ID, address, in); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.signatures.update", "mailbox", m.ID, map[string]any{"fromAddress": firstNonEmpty(address, m.Address), "signatures": len(in.Signatures)})
	s.recordAccess(r, p, m, "signatures.update", "", nil, map[string]any{"signatures": len(in.Signatures)})
	writeJSON(w, http.StatusOK, in)
}
