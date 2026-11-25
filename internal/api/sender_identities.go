package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func sendableAddresses(userParam string) string {
	return `SELECT i.address, coalesce(nullif(sig.settings->>'displayName', ''), i.display_name) AS display_name, i.sent_address
		FROM (
			SELECT m.address, m.display_name, m.address AS sent_address
			UNION ALL
			SELECT a.address, a.name, coalesce(sent.address, m.address)
			FROM mail_senders ms
			JOIN aliases a ON a.address = ms.sender
			LEFT JOIN mail_users sent ON sent.id = a.sent_mailbox_id
			WHERE ms.login = m.address AND ` + signInAsMailbox(userParam) + `
		) i
		LEFT JOIN mailbox_signatures sig ON sig.mailbox_id = m.id AND sig.from_address = CASE WHEN i.address = m.address THEN '' ELSE i.address END`
}

func (s *Server) resolveSender(w http.ResponseWriter, r *http.Request, p *principal, m *ownedMailbox, address string) (from mailpkg.Address, sent string, ok bool) {
	address = strings.ToLower(strings.TrimSpace(address))
	err := s.db.QueryRow(r.Context(), `SELECT i.address, i.display_name, i.sent_address FROM mailboxes m
		CROSS JOIN LATERAL (`+sendableAddresses("nullif($2, '')::uuid")+`) i
		WHERE m.id = $1 AND i.address = coalesce(nullif($3, ''), m.address)`, m.ID, p.User.ID, address).Scan(&from.Address, &from.Name, &sent)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "cannot_send", "Choose an address you have permission to send from.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	return from, sent, true
}
