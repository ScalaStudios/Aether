package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	maxQuarantineMessage   = 25 << 20
	maxQuarantineExpansion = 5000
)

var (
	quarantineCategories    = []string{"spam", "high_confidence_spam", "phishing", "malware"}
	quarantineAccessLevels  = []string{"release", "request", "none"}
	defaultQuarantineAccess = map[string]string{"spam": "release", "high_confidence_spam": "request", "phishing": "request", "malware": "none"}
	dmarcFailures           = []string{"DMARC_POLICY_REJECT", "DMARC_POLICY_QUARANTINE", "DMARC_POLICY_SOFTFAIL"}
)

func quarantineAccess(category string) string {
	defaults, _ := json.Marshal(defaultQuarantineAccess)
	return `(('` + string(defaults) + `'::jsonb || coalesce(p.access, '{}')) ->> ` + category + `)`
}

type quarantineSymbol struct {
	Name    string   `json:"name"`
	Score   float64  `json:"score"`
	Options []string `json:"options,omitempty"`
}

type quarantineScan struct {
	Action  string             `json:"action"`
	Score   float64            `json:"score"`
	From    string             `json:"from"`
	Rcpt    []string           `json:"rcpt"`
	Symbols []quarantineSymbol `json:"symbols"`
}

type quarantineTarget struct {
	MailboxID, OrgID, Address, Recipient string
}

func cleanText(s string, n int) string {
	return strings.ToValidUTF8(strings.ReplaceAll(truncate(s, n), "\x00", ""), "�")
}

func quarantineCategory(action string, symbols []quarantineSymbol) string {
	category := "spam"
	if action == "reject" {
		category = "high_confidence_spam"
	}
	for _, s := range symbols {
		switch {
		case s.Name == "CLAM_VIRUS":
			return "malware"
		case strings.HasPrefix(s.Name, "PHISH"):
			category = "phishing"
		}
	}
	return category
}

func cleanSymbols(in []quarantineSymbol) []quarantineSymbol {
	out := []quarantineSymbol{}
	for _, s := range in {
		if len(out) == 100 {
			break
		}
		c := quarantineSymbol{Name: cleanText(s.Name, 100), Score: s.Score}
		for _, o := range s.Options {
			if len(c.Options) == 3 {
				break
			}
			c.Options = append(c.Options, cleanText(o, 200))
		}
		out = append(out, c)
	}
	return out
}

func aliasKeys(address string) []string {
	local, domain, ok := strings.Cut(address, "@")
	if !ok || domain == "" {
		return nil
	}
	keys := []string{address}
	if base, _, tagged := strings.Cut(local, "+"); tagged && base != "" {
		keys = append(keys, base+"@"+domain)
	}
	return append(keys, "@"+domain)
}

func expandRecipients(rcpts []string, lookup func(keys []string) (map[string][]string, error)) (map[string]string, error) {
	type pending struct{ address, rcpt string }
	var queue []pending
	for _, r := range rcpts {
		r = strings.ToLower(strings.TrimSpace(r))
		queue = append(queue, pending{r, r})
	}
	targets := map[string]string{}
	seen := map[string]bool{}
	for depth := 0; len(queue) > 0 && depth < 20 && len(seen) < maxQuarantineExpansion; depth++ {
		var keys []string
		for _, p := range queue {
			keys = append(keys, aliasKeys(p.address)...)
		}
		found, err := lookup(keys)
		if err != nil {
			return nil, err
		}
		var next []pending
		for _, p := range queue {
			if seen[p.address] || len(seen) >= maxQuarantineExpansion {
				continue
			}
			seen[p.address] = true
			for _, key := range aliasKeys(p.address) {
				dests, ok := found[key]
				if !ok {
					continue
				}
				for _, d := range dests {
					d = strings.ToLower(d)
					if d != key {
						next = append(next, pending{d, p.rcpt})
					} else if _, dup := targets[d]; !dup {
						targets[d] = p.rcpt
					}
				}
				break
			}
		}
		queue = next
	}
	return targets, nil
}

func (s *Server) quarantineTargets(ctx context.Context, rcpts []string) ([]quarantineTarget, error) {
	addresses, err := expandRecipients(rcpts, func(keys []string) (map[string][]string, error) {
		rows, err := s.db.Query(ctx, `SELECT address, destination FROM mail_aliases WHERE address = ANY($1)`, keys)
		if err != nil {
			return nil, err
		}
		found := map[string][]string{}
		var address, destination string
		_, err = pgx.ForEachRow(rows, []any{&address, &destination}, func() error {
			found[address] = append(found[address], destination)
			return nil
		})
		return found, err
	})
	if err != nil || len(addresses) == 0 {
		return nil, err
	}
	list := make([]string, 0, len(addresses))
	for a := range addresses {
		list = append(list, a)
	}
	rows, err := s.db.Query(ctx, `SELECT m.id, m.org_id, m.address FROM mail_users mu JOIN mailboxes m ON m.id = mu.id WHERE mu.address = ANY($1)`, list)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (quarantineTarget, error) {
		var t quarantineTarget
		err := row.Scan(&t.MailboxID, &t.OrgID, &t.Address)
		t.Recipient = addresses[t.Address]
		return t, err
	})
}

func (s *Server) deliverToAllowed(ctx context.Context, targets []quarantineTarget, category, sender string, symbols []quarantineSymbol, raw []byte) ([]quarantineTarget, error) {
	if sender == "" || (category != "spam" && category != "high_confidence_spam") {
		return targets, nil
	}
	for _, sym := range symbols {
		if slices.Contains(dmarcFailures, sym.Name) {
			return targets, nil
		}
	}
	ids := make([]string, len(targets))
	for i, t := range targets {
		ids[i] = t.MailboxID
	}
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT a.mailbox_id FROM sender_list_entries a JOIN mailboxes m ON m.id = a.mailbox_id LEFT JOIN quarantine_policies p ON p.org_id = m.org_id
		WHERE a.mailbox_id = ANY($1) AND a.action = 'allow'
		  AND ((a.kind = 'address' AND a.value = $2) OR (a.kind = 'domain' AND a.value = split_part($2, '@', 2)))
		  AND `+quarantineAccess("$3::text")+` = 'release'`, ids, strings.ToLower(sender), category)
	if err != nil {
		return nil, err
	}
	allowed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	held := targets[:0:0]
	for _, t := range targets {
		if !slices.Contains(allowed, t.MailboxID) {
			held = append(held, t)
			continue
		}
		if err := s.mail.Deliver(t.Address, raw); err != nil {
			s.log.Warn("delivering mail from an allowed sender failed; holding it instead", "mailbox", t.MailboxID, "err", err)
			held = append(held, t)
		}
	}
	return held, nil
}

func (s *Server) quarantineIngest(w http.ResponseWriter, r *http.Request) {
	if !internalCaller(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var scan quarantineScan
	if err := json.Unmarshal([]byte(r.Header.Get("X-Aether-Scan")), &scan); err != nil || len(scan.Rcpt) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_scan", "X-Aether-Scan must describe the scan as JSON with at least one recipient.")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxQuarantineMessage))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Messages over 25 MB are not quarantined.")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_message", "The message could not be read.")
		return
	}
	msg, err := mailpkg.ParseRaw(raw)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message", "The message could not be parsed.")
		return
	}
	ctx := r.Context()
	targets, err := s.quarantineTargets(ctx, scan.Rcpt)
	if err != nil {
		s.log.Error("resolving quarantine recipients failed", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if len(targets) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var from mailpkg.Address
	if len(msg.From) > 0 {
		from = msg.From[0]
	}
	category := quarantineCategory(scan.Action, scan.Symbols)
	symbols := cleanSymbols(scan.Symbols)
	held, err := s.deliverToAllowed(ctx, targets, category, from.Address, symbols, raw)
	if err != nil {
		s.log.Error("checking allowed senders failed", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if len(held) > 0 {
		err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			var id string
			err := tx.QueryRow(ctx, `
				INSERT INTO quarantine_messages (raw, size, category, action, score, symbols, envelope_from, from_address, from_name, subject)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
				raw, len(raw), category, cleanText(scan.Action, 40), scan.Score, symbols, cleanText(strings.ToLower(scan.From), 320),
				cleanText(from.Address, 320), cleanText(from.Name, 200), cleanText(msg.Subject, 998)).Scan(&id)
			if err != nil {
				return err
			}
			rows := make([][]any, len(held))
			for i, t := range held {
				rows[i] = []any{id, t.MailboxID, t.OrgID, cleanText(t.Recipient, 320)}
			}
			_, err = tx.CopyFrom(ctx, pgx.Identifier{"quarantine_items"}, []string{"message_id", "mailbox_id", "org_id", "recipient"}, pgx.CopyFromRows(rows))
			return err
		})
		if err != nil {
			s.log.Error("storing a quarantined message failed", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	s.log.Info("message quarantined", "category", category, "score", scan.Score, "held", len(held), "delivered", len(targets)-len(held))
	w.WriteHeader(http.StatusCreated)
}
