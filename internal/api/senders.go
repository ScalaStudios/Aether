package api

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

type senderSettings struct {
	ExternalTag   bool `json:"externalTag"`
	FirstContact  bool `json:"firstContact"`
	Impersonation bool `json:"impersonation"`
}

type senderWarning struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Address   string `json:"address,omitempty"`
	Domain    string `json:"domain,omitempty"`
	Resembles string `json:"resembles,omitempty"`
}

type senderInsight struct {
	Organization   string                  `json:"organization"`
	External       bool                    `json:"external"`
	FirstContact   bool                    `json:"firstContact"`
	Warnings       []senderWarning         `json:"warnings"`
	Authentication *mailpkg.Authentication `json:"authentication"`
}

var (
	confusables = map[rune]rune{
		'а': 'a', 'е': 'e', 'о': 'o', 'р': 'p', 'с': 'c', 'у': 'y', 'х': 'x', 'к': 'k', 'і': 'i', 'ј': 'j', 'ѕ': 's',
		'ԁ': 'd', 'һ': 'h', 'ӏ': 'l', 'ԛ': 'q', 'ԝ': 'w', 'ь': 'b', 'ɡ': 'g', 'α': 'a', 'ο': 'o', 'ρ': 'p', 'ν': 'v',
		'ι': 'i', 'κ': 'k', 'χ': 'x', 'υ': 'u', 'ı': 'i', 'ɩ': 'i', 'ł': 'l', '0': 'o', '1': 'l', '|': 'l',
	}
	confusableSequences = strings.NewReplacer("rn", "m", "vv", "w", "cl", "d")
	addressInName       = regexp.MustCompile(`[^\s<>"'(),;:\[\]]+@[^\s<>"'(),;:\[\]]+`)
)

func skeleton(s string) string {
	s = strings.ToLower(norm.NFKD.String(s))
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		if c, ok := confusables[r]; ok {
			return c
		}
		return r
	}, s)
	return confusableSequences.Replace(s)
}

func nameKey(name string) string {
	words := strings.FieldsFunc(skeleton(strings.ReplaceAll(name, "I", "l")), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	if utf8.RuneCountInString(strings.Join(words, "")) < 3 {
		return ""
	}
	slices.Sort(words)
	return strings.Join(words, " ")
}

func editDistance(a, b []rune) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

func lookalikeDomain(domain, own string) bool {
	if domain == own || strings.HasSuffix(domain, "."+own) || strings.HasSuffix(own, "."+domain) {
		return false
	}
	if strings.Contains("."+domain+".", "."+own+".") {
		return true
	}
	if u, err := idna.ToUnicode(domain); err == nil {
		domain = u
	}
	a, b := skeleton(domain), skeleton(own)
	if labels := strings.Split(a, "."); len(labels) > strings.Count(b, ".")+1 {
		a = strings.Join(labels[len(labels)-strings.Count(b, ".")-1:], ".")
	}
	return a == b || (utf8.RuneCountInString(b[:max(strings.LastIndex(b, "."), 0)]) >= 5 && editDistance([]rune(a), []rune(b)) == 1)
}

func senderDomain(address string) string {
	return strings.ToLower(address[strings.LastIndex(address, "@")+1:])
}

func senderOrigin(address string, auth *mailpkg.Authentication, domains []string) (external, spoofed bool) {
	switch {
	case !slices.Contains(domains, senderDomain(address)):
		return true, false
	case auth == nil || auth.Submission != "none" || auth.DMARC.Result == "pass":
		return false, false
	}
	return true, true
}

func impersonationWarnings(from mailpkg.Address, domains []string, people []directoryEntry) []senderWarning {
	var out []senderWarning
	if key := nameKey(from.Name); key != "" {
		if i := slices.IndexFunc(people, func(c directoryEntry) bool {
			return nameKey(c.Name) == key && !strings.EqualFold(c.Address, from.Address)
		}); i >= 0 {
			out = append(out, senderWarning{Kind: "name", Name: people[i].Name, Address: people[i].Address})
		}
	}
	for _, shown := range addressInName.FindAllString(from.Name, -1) {
		shown = strings.ToLower(strings.TrimRight(shown, "."))
		if !strings.EqualFold(shown, from.Address) && slices.Contains(domains, senderDomain(shown)) {
			out = append(out, senderWarning{Kind: "address-in-name", Address: shown})
			break
		}
	}
	domain := senderDomain(from.Address)
	if i := slices.IndexFunc(domains, func(own string) bool { return lookalikeDomain(domain, own) }); i >= 0 && !slices.Contains(domains, domain) {
		out = append(out, senderWarning{Kind: "lookalike", Domain: domain, Resembles: domains[i]})
	}
	return out
}

func (s *Server) senderContext(ctx context.Context, orgID string) (name string, domains []string, settings senderSettings, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT o.name, coalesce(w.external_tag, true), coalesce(w.first_contact, true), coalesce(w.impersonation, true),
		       array(SELECT d.name FROM domains d WHERE d.org_id = o.id AND d.active AND d.verified_at IS NOT NULL ORDER BY d.name)
		FROM organizations o LEFT JOIN organization_sender_warnings w ON w.org_id = o.id WHERE o.id = $1`, orgID).
		Scan(&name, &settings.ExternalTag, &settings.FirstContact, &settings.Impersonation, &domains)
	return
}

func (s *Server) assessSender(ctx context.Context, m *ownedMailbox, folder string, msg *mailpkg.Message) (*senderInsight, error) {
	if len(msg.From) == 0 {
		return nil, nil
	}
	org, domains, settings, err := s.senderContext(ctx, m.OrgID)
	if err != nil {
		return nil, err
	}
	out := &senderInsight{Organization: org, Warnings: []senderWarning{}, Authentication: msg.Authentication}
	from := msg.From[0]
	external, spoofed := senderOrigin(from.Address, msg.Authentication, domains)
	if !external {
		return out, nil
	}
	out.External = settings.ExternalTag
	if settings.Impersonation {
		if spoofed {
			out.Warnings = append(out.Warnings, senderWarning{Kind: "spoofed", Domain: senderDomain(from.Address)})
		}
		rows, err := s.db.Query(ctx, `
			SELECT coalesce(nullif(m.display_name, ''), u.display_name, ''), m.address, 'directory'
			FROM mailboxes m LEFT JOIN users u ON u.id = m.owner_user_id
			WHERE m.org_id = $1 AND m.kind = 'user' AND m.active
			UNION
			SELECT u.display_name, u.email, 'directory' FROM organization_members om JOIN users u ON u.id = om.user_id
			WHERE om.org_id = $1 AND u.status = 'active' AND u.display_name <> ''`, m.OrgID)
		if err != nil {
			return nil, err
		}
		people, err := pgx.CollectRows(rows, pgx.RowToStructByPos[directoryEntry])
		if err != nil {
			return nil, err
		}
		out.Warnings = append(out.Warnings, impersonationWarnings(from, domains, people)...)
	}
	if settings.FirstContact && !spoofed {
		known, err := s.mail.KnownSender(m.Address, folder, msg.UID, from.Address)
		if err != nil {
			s.log.Warn("checking earlier mail from sender failed", "mailbox", m.ID, "err", err)
		}
		out.FirstContact = err == nil && !known
	}
	return out, nil
}

func (s *Server) getSenderWarnings(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	_, _, settings, err := s.senderContext(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) saveSenderWarnings(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in senderSettings
	if !decode(w, r, 1<<10, &in) {
		return
	}
	_, _, before, err := s.senderContext(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO organization_sender_warnings (org_id, external_tag, first_contact, impersonation) VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id) DO UPDATE SET external_tag = excluded.external_tag, first_contact = excluded.first_contact, impersonation = excluded.impersonation, updated_at = now()`,
		orgID, in.ExternalTag, in.FirstContact, in.Impersonation)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	changed := map[string]any{}
	for field, values := range map[string][2]bool{
		"externalTag":   {before.ExternalTag, in.ExternalTag},
		"firstContact":  {before.FirstContact, in.FirstContact},
		"impersonation": {before.Impersonation, in.Impersonation},
	} {
		if values[0] != values[1] {
			changed[field] = values[1]
		}
	}
	if len(changed) > 0 {
		s.audit(r.Context(), r, p, &orgID, "sender_warnings.update", "organization", orgID, changed)
	}
	writeJSON(w, http.StatusOK, in)
}
