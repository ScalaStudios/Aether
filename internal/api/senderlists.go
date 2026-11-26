package api

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

type senderEntry struct {
	ID        string     `json:"id"`
	DomainID  *string    `json:"domainId"`
	Action    string     `json:"action"`
	Kind      string     `json:"kind"`
	Value     string     `json:"value"`
	Note      string     `json:"note"`
	ExpiresAt *time.Time `json:"expiresAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

type senderInput struct {
	Action    string     `json:"action"`
	Value     string     `json:"value"`
	Note      string     `json:"note"`
	ExpiresAt *time.Time `json:"expiresAt"`
	DomainID  *string    `json:"domainId"`
}

const senderEntryColumns = `id, domain_id, action, kind, value, note, expires_at, created_at`

const (
	maxOrgSenderEntries     = 10000
	maxMailboxSenderEntries = 1000
)

func parseSender(raw string) (kind, value string, ok bool) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if p, err := parsePrefix(v); err == nil {
		if p.IsSingleIP() {
			return "ip", p.Addr().String(), true
		}
		return "ip", p.String(), true
	}
	if d := strings.TrimPrefix(v, "@"); domainPattern.MatchString(d) && len(d) <= 253 {
		return "domain", d, true
	}
	if a, err := mail.ParseAddress(v); err == nil && a.Name == "" && a.Address == v && len(v) <= 320 {
		return "address", v, true
	}
	return "", "", false
}

func senderDomains(addresses []string) []string {
	var out []string
	for _, a := range addresses {
		_, d, ok := strings.Cut(a, "@")
		for ok && strings.Contains(d, ".") {
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
			_, d, ok = strings.Cut(d, ".")
		}
	}
	return out
}

type senderMatch struct {
	Action, Kind, Value string
}

func decideSender(matches []senderMatch, verified []string, dmarcReject bool) string {
	trusted := append(senderDomains(verified), verified...)
	verdict := ""
	for _, m := range matches {
		switch {
		case m.Action == "block":
			return "block"
		case dmarcReject:
		case m.Kind == "ip" || slices.Contains(trusted, m.Value):
			verdict = "allow"
		}
	}
	return verdict
}

func senderAddresses(values []string) []string {
	var out []string
	for _, a := range values {
		if a = strings.ToLower(strings.TrimSpace(a)); strings.Contains(a, "@") && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func (s *Server) senderVerdict(ctx context.Context, recipientDomain string, senders, verified []string, ip string, dmarcReject bool) (string, error) {
	addresses := senderAddresses(senders)
	var client *string
	if addr, err := netip.ParseAddr(ip); err == nil {
		v := addr.Unmap().String()
		client = &v
	}
	rows, err := s.db.Query(ctx, `
		SELECT e.action, e.kind, e.value FROM domains d
		JOIN sender_list_entries e ON e.org_id = d.org_id AND e.mailbox_id IS NULL AND (e.domain_id IS NULL OR e.domain_id = d.id)
		WHERE d.name = $1 AND (e.expires_at IS NULL OR e.expires_at > now())
		  AND ((e.kind = 'address' AND e.value = ANY ($2)) OR (e.kind = 'domain' AND e.value = ANY ($3)) OR (e.kind = 'ip' AND $4::inet IS NOT NULL AND e.value::inet >>= $4::inet))`,
		strings.ToLower(recipientDomain), nonNil(addresses), nonNil(senderDomains(addresses)), client)
	if err != nil {
		return "", err
	}
	matches, err := pgx.CollectRows(rows, pgx.RowToStructByPos[senderMatch])
	if err != nil {
		return "", err
	}
	return decideSender(matches, senderAddresses(verified), dmarcReject), nil
}

func (s *Server) senderPolicy(ctx context.Context, attrs map[string]string) (string, error) {
	if attrs["sasl_username"] != "" {
		return "DUNNO", nil
	}
	_, domain, ok := strings.Cut(attrs["recipient"], "@")
	if !ok {
		return "DUNNO", nil
	}
	verdict, err := s.senderVerdict(ctx, domain, []string{attrs["sender"]}, nil, attrs["client_address"], false)
	if err != nil || verdict != "block" {
		return "DUNNO", err
	}
	return "REJECT 5.7.1 Mail from this sender is blocked by the recipient's organization", nil
}

func (s *Server) checkSenderLists(w http.ResponseWriter, r *http.Request) {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err != nil || publicAddress(ap.Addr()) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var in struct {
		From        string   `json:"from"`
		HeaderFrom  string   `json:"headerFrom"`
		IP          string   `json:"ip"`
		Recipients  []string `json:"recipients"`
		SPFPass     bool     `json:"spfPass"`
		DMARCPass   bool     `json:"dmarcPass"`
		DMARCReject bool     `json:"dmarcReject"`
	}
	if !decode(w, r, 256<<10, &in) {
		return
	}
	var domains []string
	for _, rcpt := range in.Recipients {
		if _, d, ok := strings.Cut(strings.ToLower(rcpt), "@"); ok && !slices.Contains(domains, d) {
			domains = append(domains, d)
		}
	}
	var verified []string
	if in.SPFPass {
		verified = append(verified, in.From)
	}
	if in.DMARCPass {
		verified = append(verified, in.HeaderFrom)
	}
	action := ""
	for i, d := range domains {
		verdict, err := s.senderVerdict(r.Context(), d, []string{in.From, in.HeaderFrom}, verified, in.IP, in.DMARCReject)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if i == 0 {
			action = verdict
		} else if verdict != action {
			action = ""
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"action": action})
}

func (s *Server) checkHostedSender(w http.ResponseWriter, r *http.Request) {
	if !internalCaller(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var in struct {
		From string `json:"from"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	var hosted bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM mail_domains WHERE name = ANY ($1))`, nonNil(senderDomains(senderAddresses([]string{in.From})))).Scan(&hosted); err != nil {
		s.internalError(w, r, err)
		return
	}
	if !hosted {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validSenderAction(w http.ResponseWriter, action string) bool {
	if action != "allow" && action != "block" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_action", "action must be allow or block.")
		return false
	}
	return true
}

func invalidSender(w http.ResponseWriter, value string, ip bool) {
	if ip {
		writeError(w, http.StatusUnprocessableEntity, "invalid_sender", value+" is not an email address, domain or IP address.")
		return
	}
	writeError(w, http.StatusUnprocessableEntity, "invalid_sender", value+" is not an email address or domain.")
}

func creator(p *principal) *string {
	if p.apiKey != nil {
		return nil
	}
	return &p.User.ID
}

func (s *Server) listOrgSenderLists(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+senderEntryColumns+` FROM sender_list_entries WHERE org_id = $1 AND mailbox_id IS NULL ORDER BY created_at DESC`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	entries, err := pgx.CollectRows(rows, pgx.RowToStructByPos[senderEntry])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(entries))
}

func (s *Server) addOrgSenderEntry(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in senderInput
	if !decode(w, r, 16<<10, &in) || !validSenderAction(w, in.Action) {
		return
	}
	kind, value, ok := parseSender(in.Value)
	if !ok {
		invalidSender(w, in.Value, true)
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(in.Note) > 500 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_note", "Keep the note under 500 characters.")
		return
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_expiry", "The expiry date must be in the future.")
		return
	}
	if in.DomainID != nil {
		var exists bool
		if uuidPattern.MatchString(*in.DomainID) {
			if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM domains WHERE id = $1 AND org_id = $2)`, *in.DomainID, orgID).Scan(&exists); err != nil {
				s.internalError(w, r, err)
				return
			}
		}
		if !exists {
			writeError(w, http.StatusUnprocessableEntity, "invalid_domain", "Choose one of this organization's domains.")
			return
		}
	}
	var count int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM sender_list_entries WHERE org_id = $1 AND mailbox_id IS NULL`, orgID).Scan(&count); err != nil {
		s.internalError(w, r, err)
		return
	}
	if count >= maxOrgSenderEntries {
		writeError(w, http.StatusUnprocessableEntity, "too_many_entries", "An organization can have up to 10,000 allowed and blocked senders.")
		return
	}
	var e senderEntry
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO sender_list_entries (org_id, domain_id, action, kind, value, note, expires_at, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (org_id, domain_id, mailbox_id, value) DO UPDATE SET action = excluded.action, kind = excluded.kind, note = excluded.note,
			expires_at = excluded.expires_at, created_by = excluded.created_by, created_at = now()
		RETURNING `+senderEntryColumns, orgID, in.DomainID, in.Action, kind, value, in.Note, in.ExpiresAt, creator(p)).
		Scan(&e.ID, &e.DomainID, &e.Action, &e.Kind, &e.Value, &e.Note, &e.ExpiresAt, &e.CreatedAt)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "sender_list.add", "organization", orgID, map[string]any{"entryId": e.ID, "domainId": e.DomainID, "action": e.Action, "value": e.Value, "note": e.Note, "expiresAt": e.ExpiresAt})
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) removeOrgSenderEntry(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "entryId")
	if !ok {
		return
	}
	var e senderEntry
	err := s.db.QueryRow(r.Context(), `DELETE FROM sender_list_entries WHERE id = $1 AND org_id = $2 AND mailbox_id IS NULL RETURNING domain_id, action, value`, id, orgID).
		Scan(&e.DomainID, &e.Action, &e.Value)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "sender_list.remove", "organization", orgID, map[string]any{"entryId": id, "domainId": e.DomainID, "action": e.Action, "value": e.Value})
	w.WriteHeader(http.StatusNoContent)
}

func mailboxSenderEntries(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, mailboxID string) ([]senderEntry, error) {
	rows, err := q.Query(ctx, `SELECT `+senderEntryColumns+` FROM sender_list_entries WHERE mailbox_id = $1 ORDER BY created_at DESC`, mailboxID)
	if err != nil {
		return nil, err
	}
	entries, err := pgx.CollectRows(rows, pgx.RowToStructByPos[senderEntry])
	return nonNil(entries), err
}

func (s *Server) listMailboxSenderLists(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	entries, err := mailboxSenderEntries(r.Context(), s.db, m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) changeMailboxSenderList(w http.ResponseWriter, r *http.Request, m *ownedMailbox, change func(tx pgx.Tx) error) ([]senderEntry, bool) {
	var entries []senderEntry
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT 1 FROM mailboxes WHERE id = $1 FOR UPDATE`, m.ID); err != nil {
			return err
		}
		if err := change(tx); err != nil {
			return err
		}
		var err error
		if entries, err = mailboxSenderEntries(r.Context(), tx, m.ID); err != nil {
			return err
		}
		if len(entries) > maxMailboxSenderEntries {
			return errTooManySenders
		}
		var blocked, trusted []string
		for _, e := range entries {
			if e.Action == "block" {
				blocked = append(blocked, e.Value)
			} else {
				trusted = append(trusted, e.Value)
			}
		}
		if err := s.mail.PutSenderSieve(m.Address, mailpkg.CompileSenderSieve(blocked, trusted)); err != nil {
			return errors.Join(errSieveInstall, err)
		}
		return nil
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		notFound(w)
	case errors.Is(err, errTooManySenders):
		writeError(w, http.StatusUnprocessableEntity, "too_many_entries", "A mailbox can have up to 1,000 blocked and trusted senders.")
	case errors.Is(err, errSieveInstall):
		mailBackendErrs.Inc()
		s.log.Error("installing sender lists failed", "mailbox", m.ID, "err", err)
		writeError(w, http.StatusBadGateway, "senders_not_saved", "The change could not be installed on the mail server. Nothing was changed; try again.")
	case err != nil:
		s.internalError(w, r, err)
	default:
		return entries, true
	}
	return nil, false
}

var (
	errTooManySenders = errors.New("too many sender list entries")
	errSieveInstall   = errors.New("sieve install failed")
)

func (s *Server) addMailboxSenderEntry(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Action string `json:"action"`
		Value  string `json:"value"`
	}
	if !decode(w, r, 4<<10, &in) || !validSenderAction(w, in.Action) {
		return
	}
	kind, value, ok := parseSender(in.Value)
	if !ok || kind == "ip" {
		invalidSender(w, in.Value, false)
		return
	}
	var e senderEntry
	entries, ok := s.changeMailboxSenderList(w, r, m, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			INSERT INTO sender_list_entries (org_id, mailbox_id, action, kind, value, created_by) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (org_id, domain_id, mailbox_id, value) DO UPDATE SET action = excluded.action, created_by = excluded.created_by, created_at = now()
			RETURNING id`, m.OrgID, m.ID, in.Action, kind, value, creator(p)).Scan(&e.ID)
	})
	if !ok {
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "sender_list.add", "mailbox", m.ID, map[string]any{"entryId": e.ID, "address": m.Address, "action": in.Action, "value": value})
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) removeMailboxSenderEntry(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "entryId")
	if !ok {
		return
	}
	var e senderEntry
	entries, ok := s.changeMailboxSenderList(w, r, m, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `DELETE FROM sender_list_entries WHERE id = $1 AND mailbox_id = $2 RETURNING action, value`, id, m.ID).Scan(&e.Action, &e.Value)
	})
	if !ok {
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "sender_list.remove", "mailbox", m.ID, map[string]any{"entryId": id, "address": m.Address, "action": e.Action, "value": e.Value})
	writeJSON(w, http.StatusOK, entries)
}
