package api

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	searchResultLimit = 500
	searchTimeout     = 90 * time.Second
	searchWorkers     = 4
)

func mailboxHeld(id string) string {
	return `EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = ` + id + `)`
}

func (s *Server) holdScope(ctx context.Context, mailboxID string) (mailpkg.HoldScope, error) {
	rows, err := s.db.Query(ctx, `SELECT criteria FROM mailbox_holds WHERE mailbox_id = $1`, mailboxID)
	if err != nil {
		return mailpkg.HoldScope{Whole: true}, err
	}
	scopes, err := pgx.CollectRows(rows, pgx.RowTo[*caseCriteria])
	if err != nil {
		return mailpkg.HoldScope{Whole: true}, err
	}
	queries := make([]mailpkg.SearchQuery, len(scopes))
	for i, c := range scopes {
		if c == nil {
			continue
		}
		var msg string
		if queries[i], msg = c.normalize(); msg != "" {
			return mailpkg.HoldScope{Whole: true}, errors.New("a hold has invalid criteria: " + msg)
		}
	}
	return mailpkg.NewHoldScope(queries), nil
}

func (c *caseCriteria) holdScope() (*caseCriteria, string) {
	c.Scope = "organization"
	if _, msg := c.normalize(); msg != "" {
		return nil, msg
	}
	if c.From+c.To+c.Subject+c.Body+c.Attachment+c.Since+c.Until == "" {
		return nil, ""
	}
	return c, ""
}

type caseCriteria struct {
	Scope      string   `json:"scope"`
	MailboxIDs []string `json:"mailboxIds"`
	DomainIDs  []string `json:"domainIds"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	Subject    string   `json:"subject"`
	Body       string   `json:"body"`
	Attachment string   `json:"attachment"`
	Since      string   `json:"since"`
	Until      string   `json:"until"`
}

func (c *caseCriteria) normalize() (mailpkg.SearchQuery, string) {
	var q mailpkg.SearchQuery
	c.MailboxIDs, c.DomainIDs = nonNil(c.MailboxIDs), nonNil(c.DomainIDs)
	switch c.Scope {
	case "organization":
		c.MailboxIDs, c.DomainIDs = []string{}, []string{}
	case "mailboxes":
		c.DomainIDs = []string{}
		if len(c.MailboxIDs) == 0 {
			return q, "Choose at least one mailbox to search."
		}
	case "domains":
		c.MailboxIDs = []string{}
		if len(c.DomainIDs) == 0 {
			return q, "Choose at least one domain to search."
		}
	default:
		return q, "Scope must be mailboxes, domains or organization."
	}
	if len(c.MailboxIDs) > 1000 || len(c.DomainIDs) > 100 {
		return q, "A case can name at most 1,000 mailboxes or 100 domains."
	}
	for _, id := range slices.Concat(c.MailboxIDs, c.DomainIDs) {
		if !uuidPattern.MatchString(id) {
			return q, "The scope names a mailbox or domain that does not exist."
		}
	}
	for _, f := range []*string{&c.From, &c.To, &c.Subject, &c.Body, &c.Attachment, &c.Since, &c.Until} {
		*f = strings.TrimSpace(*f)
		if len(*f) > 500 || strings.ContainsAny(*f, "\r\n") {
			return q, "Search terms must be single lines of at most 500 characters."
		}
	}
	q = mailpkg.SearchQuery{From: c.From, To: c.To, Subject: c.Subject, Body: c.Body, Attachment: c.Attachment}
	var err error
	if c.Since != "" {
		if q.Since, err = time.Parse(time.DateOnly, c.Since); err != nil {
			return q, "Dates must look like 2026-01-31."
		}
	}
	if c.Until != "" {
		var until time.Time
		if until, err = time.Parse(time.DateOnly, c.Until); err != nil {
			return q, "Dates must look like 2026-01-31."
		}
		if !q.Since.IsZero() && until.Before(q.Since) {
			return q, "The end date is before the start date."
		}
		q.Before = until.AddDate(0, 0, 1)
	}
	return q, ""
}

type complianceCase struct {
	ID          string       `json:"id"`
	OrgID       string       `json:"orgId"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Criteria    caseCriteria `json:"criteria"`
	CreatedBy   *string      `json:"createdBy"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
	ClosedAt    *time.Time   `json:"closedAt"`
	Holds       int          `json:"holds"`
}

const caseSelect = `SELECT c.id, c.org_id, c.name, c.description, c.criteria, u.email, c.created_at, c.updated_at, c.closed_at, (SELECT count(*) FROM mailbox_holds h WHERE h.case_id = c.id)
	FROM compliance_cases c LEFT JOIN users u ON u.id = c.created_by `

func (s *Server) complianceOrg(w http.ResponseWriter, r *http.Request, p *principal) (string, bool) {
	orgID, ok := s.orgFromPath(w, r, p, false)
	if ok && !p.canCompliance(orgID) {
		forbidden(w)
		return "", false
	}
	return orgID, ok
}

func (s *Server) caseFromPath(w http.ResponseWriter, r *http.Request, p *principal) (*complianceCase, bool) {
	id, ok := pathID(w, r, "caseId")
	if !ok {
		return nil, false
	}
	rows, err := s.db.Query(r.Context(), caseSelect+`WHERE c.id = $1`, id)
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	c, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[complianceCase])
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !p.canCompliance(c.OrgID)) {
		notFound(w)
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return &c, true
}

func (s *Server) listCases(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.complianceOrg(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), caseSelect+`WHERE c.org_id = $1 ORDER BY c.closed_at IS NOT NULL, c.created_at DESC`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[complianceCase])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func validCaseText(name, description string) string {
	switch {
	case name == "" || len(name) > 200:
		return "Name the case in at most 200 characters."
	case len(description) > 2000:
		return "Keep the description under 2,000 characters."
	}
	return ""
}

func (s *Server) createCase(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.complianceOrg(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Name        string       `json:"name"`
		Description string       `json:"description"`
		Criteria    caseCriteria `json:"criteria"`
	}
	if !decode(w, r, 128<<10, &in) {
		return
	}
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if msg := validCaseText(in.Name, in.Description); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_case", msg)
		return
	}
	if _, msg := in.Criteria.normalize(); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_criteria", msg)
		return
	}
	var id string
	if err := s.db.QueryRow(r.Context(), `INSERT INTO compliance_cases (org_id, name, description, criteria, created_by) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		orgID, in.Name, in.Description, in.Criteria, p.User.ID).Scan(&id); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "compliance.case_create", "compliance_case", id, map[string]any{"name": in.Name, "criteria": in.Criteria})
	r.SetPathValue("caseId", id)
	if c, ok := s.caseFromPath(w, r, p); ok {
		writeJSON(w, http.StatusCreated, c)
	}
}

func (s *Server) getCase(w http.ResponseWriter, r *http.Request, p *principal) {
	if c, ok := s.caseFromPath(w, r, p); ok {
		writeJSON(w, http.StatusOK, c)
	}
}

func (s *Server) updateCase(w http.ResponseWriter, r *http.Request, p *principal) {
	c, ok := s.caseFromPath(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Name        *string       `json:"name"`
		Description *string       `json:"description"`
		Criteria    *caseCriteria `json:"criteria"`
		Closed      *bool         `json:"closed"`
	}
	if !decode(w, r, 128<<10, &in) {
		return
	}
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		c.Description = strings.TrimSpace(*in.Description)
	}
	if msg := validCaseText(c.Name, c.Description); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_case", msg)
		return
	}
	if in.Criteria != nil {
		if _, msg := in.Criteria.normalize(); msg != "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_criteria", msg)
			return
		}
		c.Criteria = *in.Criteria
	}
	closing := in.Closed != nil && *in.Closed && c.ClosedAt == nil
	reopening := in.Closed != nil && !*in.Closed && c.ClosedAt != nil
	var released []string
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `
			UPDATE compliance_cases SET name = $2, description = $3, criteria = $4, updated_at = now(),
				closed_at = CASE WHEN $5 THEN now() WHEN $6 THEN NULL ELSE closed_at END
			WHERE id = $1`, c.ID, c.Name, c.Description, c.Criteria, closing, reopening); err != nil {
			return err
		}
		if !closing {
			return nil
		}
		rows, err := tx.Query(r.Context(), `DELETE FROM mailbox_holds h USING mailboxes m WHERE h.case_id = $1 AND m.id = h.mailbox_id RETURNING m.address`, c.ID)
		if err != nil {
			return err
		}
		released, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &c.OrgID, "compliance.case_update", "compliance_case", c.ID, map[string]any{"name": c.Name, "criteria": c.Criteria})
	if closing {
		s.audit(r.Context(), r, p, &c.OrgID, "compliance.case_close", "compliance_case", c.ID, map[string]any{"releasedHolds": nonNil(released)})
	}
	if reopening {
		s.audit(r.Context(), r, p, &c.OrgID, "compliance.case_reopen", "compliance_case", c.ID, nil)
	}
	if out, ok := s.caseFromPath(w, r, p); ok {
		writeJSON(w, http.StatusOK, out)
	}
}

type caseMailbox struct {
	ID, Address string
	Reachable   bool
}

func (s *Server) caseMailboxes(ctx context.Context, c *complianceCase) ([]caseMailbox, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.id, m.address, (m.active AND d.active AND d.verified_at IS NOT NULL) OR `+mailboxHeld("m.id")+`
		FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.org_id = $1 AND ($2 = 'organization' OR m.id = ANY($3::uuid[]) OR m.domain_id = ANY($4::uuid[]))
		ORDER BY m.address`, c.OrgID, c.Criteria.Scope, c.Criteria.MailboxIDs, c.Criteria.DomainIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[caseMailbox])
}

type searchFailure struct {
	Mailbox string `json:"mailbox"`
	Error   string `json:"error"`
}

type searchHit struct {
	MailboxID string `json:"mailboxId"`
	Mailbox   string `json:"mailbox"`
	mailpkg.Hit
}

func (s *Server) prepareCaseRun(w http.ResponseWriter, r *http.Request, p *principal) (*complianceCase, mailpkg.SearchQuery, []caseMailbox, []string, bool) {
	c, ok := s.caseFromPath(w, r, p)
	if !ok {
		return nil, mailpkg.SearchQuery{}, nil, nil, false
	}
	if !s.allow(r.Context(), "compliance:"+p.User.ID, 20) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many searches and exports. Wait a minute and try again.")
		return nil, mailpkg.SearchQuery{}, nil, nil, false
	}
	q, msg := c.Criteria.normalize()
	if msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_criteria", msg)
		return nil, q, nil, nil, false
	}
	all, err := s.caseMailboxes(r.Context(), c)
	if err != nil {
		s.internalError(w, r, err)
		return nil, q, nil, nil, false
	}
	var reachable []caseMailbox
	skipped := []string{}
	for _, m := range all {
		if m.Reachable {
			reachable = append(reachable, m)
		} else {
			skipped = append(skipped, m.Address)
		}
	}
	return c, q, reachable, skipped, true
}

func (s *Server) searchCase(w http.ResponseWriter, r *http.Request, p *principal) {
	c, q, boxes, skipped, ok := s.prepareCaseRun(w, r, p)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()
	var mu sync.Mutex
	hits := []searchHit{}
	failed := []searchFailure{}
	truncated := false
	jobs := make(chan caseMailbox)
	var wg sync.WaitGroup
	for range min(searchWorkers, len(boxes)) {
		wg.Go(func() {
			for m := range jobs {
				err := s.mail.Search(ctx, m.Address, q, false, func(h mailpkg.Hit, _ []byte) error {
					mu.Lock()
					defer mu.Unlock()
					if len(hits) >= searchResultLimit {
						truncated = true
						return mailpkg.ErrStopSearch
					}
					hits = append(hits, searchHit{MailboxID: m.ID, Mailbox: m.Address, Hit: h})
					return nil
				})
				if err != nil && !errors.Is(err, mailpkg.ErrStopSearch) {
					s.log.Warn("compliance search failed for a mailbox", "case", c.ID, "mailbox", m.Address, "err", err)
					mu.Lock()
					failed = append(failed, searchFailure{Mailbox: m.Address, Error: searchErrorText(err)})
					mu.Unlock()
				}
			}
		})
	}
	for _, m := range boxes {
		jobs <- m
	}
	close(jobs)
	wg.Wait()
	slices.SortFunc(hits, func(a, b searchHit) int { return b.Date.Compare(a.Date) })
	slices.SortFunc(failed, func(a, b searchFailure) int { return strings.Compare(a.Mailbox, b.Mailbox) })
	s.audit(r.Context(), r, p, &c.OrgID, "compliance.search", "compliance_case", c.ID, map[string]any{
		"criteria": c.Criteria, "mailboxes": len(boxes), "results": len(hits), "truncated": truncated, "failed": len(failed), "skipped": len(skipped),
	})
	writeJSON(w, http.StatusOK, map[string]any{"results": hits, "truncated": truncated, "searched": len(boxes), "skipped": skipped, "failed": failed})
}

func searchErrorText(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "The search took too long and stopped. Narrow the search or export the case instead."
	}
	return "The mail server could not search this mailbox."
}

func zipPath(parts ...string) string {
	var clean []string
	for _, part := range parts {
		for _, seg := range strings.Split(part, "/") {
			seg = strings.Map(func(r rune) rune {
				if r < 0x20 || r == '\\' || r == ':' {
					return '_'
				}
				return r
			}, seg)
			if seg != "" && seg != "." && seg != ".." {
				clean = append(clean, seg)
			}
		}
	}
	return path.Join(clean...)
}

func (s *Server) exportCase(w http.ResponseWriter, r *http.Request, p *principal) {
	format := r.URL.Query().Get("format")
	if format != "eml" && format != "mbox" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_format", "Format must be eml or mbox.")
		return
	}
	c, q, boxes, skipped, ok := s.prepareCaseRun(w, r, p)
	if !ok {
		return
	}
	s.audit(r.Context(), r, p, &c.OrgID, "compliance.export", "compliance_case", c.ID, map[string]any{"criteria": c.Criteria, "format": format, "mailboxes": len(boxes), "skipped": len(skipped)})
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mailpkg.ContentDisposition(fmt.Sprintf("case-%s-%s.zip", c.ID[:8], time.Now().UTC().Format("20060102-150405"))))
	rc := http.NewResponseController(w)
	zw := zip.NewWriter(w)
	manifest := [][]string{{"mailbox", "folder", "uid", "uid_validity", "message_id", "date", "from", "to", "subject", "size", "sha256", "file"}}
	problems := [][]string{{"mailbox", "problem"}}
	for _, address := range skipped {
		problems = append(problems, []string{address, "Not searched: the mailbox is disabled and not on hold."})
	}
	exported := 0
	for _, m := range boxes {
		var entry io.Writer
		folder := ""
		err := s.mail.Search(r.Context(), m.Address, q, true, func(h mailpkg.Hit, raw []byte) error {
			rc.SetWriteDeadline(time.Now().Add(2 * time.Minute))
			file := zipPath(m.Address, h.Folder, strconv.FormatUint(uint64(h.UID), 10)+".eml")
			if format == "mbox" {
				file = zipPath(m.Address, h.Folder) + ".mbox"
			}
			var err error
			if format == "eml" || entry == nil || folder != h.Folder {
				if entry, err = zw.CreateHeader(&zip.FileHeader{Name: file, Method: zip.Deflate, Modified: h.Date}); err != nil {
					return err
				}
				folder = h.Folder
			}
			if format == "eml" {
				_, err = entry.Write(raw)
			} else {
				err = mailpkg.WriteMbox(entry, h, raw)
			}
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			manifest = append(manifest, []string{m.Address, h.Folder, strconv.FormatUint(uint64(h.UID), 10), strconv.FormatUint(uint64(h.UIDValidity), 10), h.MessageID, h.Date.UTC().Format(time.RFC3339),
				joinAddresses(h.From), joinAddresses(h.To), h.Subject, strconv.FormatInt(h.Size, 10), hex.EncodeToString(sum[:]), file})
			exported++
			return nil
		})
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			s.log.Warn("compliance export failed for a mailbox", "case", c.ID, "mailbox", m.Address, "err", err)
			problems = append(problems, []string{m.Address, "Export incomplete: " + searchErrorText(err)})
		}
	}
	files := []string{"manifest.csv"}
	if len(problems) > 1 {
		files = append(files, "problems.csv")
	}
	for i, name := range files {
		f, err := zw.Create(name)
		if err != nil {
			return
		}
		if err := csv.NewWriter(f).WriteAll([][][]string{manifest, problems}[i]); err != nil {
			return
		}
	}
	if err := zw.Close(); err != nil {
		s.log.Warn("compliance export did not finish", "case", c.ID, "err", err)
		return
	}
	s.log.Info("compliance export finished", "case", c.ID, "messages", exported, "problems", len(problems)-1)
}

func joinAddresses(in []mailpkg.Address) string {
	out := make([]string, len(in))
	for i, a := range in {
		out[i] = a.Address
	}
	return strings.Join(out, ", ")
}

type hold struct {
	MailboxID   string        `json:"mailboxId"`
	Criteria    *caseCriteria `json:"criteria"`
	Address     string        `json:"address"`
	DisplayName string        `json:"displayName"`
	Active      bool          `json:"active"`
	RetainedAt  *time.Time    `json:"retainedAt"`
	CreatedBy   *string       `json:"createdBy"`
	CreatedAt   time.Time     `json:"createdAt"`
}

func (s *Server) listHolds(w http.ResponseWriter, r *http.Request, p *principal) {
	c, ok := s.caseFromPath(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT m.id, h.criteria, m.address, m.display_name, m.active, m.retained_at, u.email, h.created_at
		FROM mailbox_holds h JOIN mailboxes m ON m.id = h.mailbox_id LEFT JOIN users u ON u.id = h.created_by
		WHERE h.case_id = $1 ORDER BY m.address`, c.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[hold])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) addHolds(w http.ResponseWriter, r *http.Request, p *principal) {
	c, ok := s.caseFromPath(w, r, p)
	if !ok {
		return
	}
	var in struct {
		MailboxIDs []string      `json:"mailboxIds"`
		Criteria   *caseCriteria `json:"criteria"`
	}
	if !decode(w, r, 64<<10, &in) {
		return
	}
	var scope *caseCriteria
	if in.Criteria != nil {
		var msg string
		if scope, msg = in.Criteria.holdScope(); msg != "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_criteria", msg)
			return
		}
	}
	if len(in.MailboxIDs) == 0 || len(in.MailboxIDs) > 1000 || slices.ContainsFunc(in.MailboxIDs, func(id string) bool { return !uuidPattern.MatchString(id) }) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_mailboxes", "Choose between 1 and 1,000 mailboxes of this organization.")
		return
	}
	type added struct{ ID, Address string }
	var placed []added
	closed := false
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT closed_at IS NOT NULL FROM compliance_cases WHERE id = $1 FOR UPDATE`, c.ID).Scan(&closed); err != nil || closed {
			return err
		}
		rows, err := tx.Query(r.Context(), `
			WITH ins AS (
				INSERT INTO mailbox_holds (case_id, mailbox_id, created_by, criteria)
				SELECT $1, m.id, $3, $5 FROM mailboxes m WHERE m.org_id = $2 AND m.id = ANY($4::uuid[])
				ON CONFLICT (case_id, mailbox_id) DO UPDATE SET criteria = EXCLUDED.criteria, created_by = EXCLUDED.created_by, created_at = now()
				RETURNING mailbox_id
			)
			SELECT m.id, m.address FROM ins JOIN mailboxes m ON m.id = ins.mailbox_id`, c.ID, c.OrgID, p.User.ID, in.MailboxIDs, scope)
		if err != nil {
			return err
		}
		if placed, err = pgx.CollectRows(rows, pgx.RowToStructByPos[added]); err != nil {
			return err
		}
		addresses := make([]string, len(placed))
		for i, m := range placed {
			addresses[i] = m.Address
		}
		return queueKicks(r.Context(), tx, addresses)
	})
	if closed {
		writeError(w, http.StatusConflict, "case_closed", "This case is closed. Reopen it to place holds.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, m := range placed {
		s.audit(r.Context(), r, p, &c.OrgID, "compliance.hold_add", "mailbox", m.ID, map[string]any{"address": m.Address, "caseId": c.ID, "caseName": c.Name, "criteria": scope})
	}
	wake(s.kickWake)
	writeJSON(w, http.StatusOK, map[string]int{"added": len(placed)})
}

func (s *Server) releaseHold(w http.ResponseWriter, r *http.Request, p *principal) {
	c, ok := s.caseFromPath(w, r, p)
	if !ok {
		return
	}
	mailboxID, ok := pathID(w, r, "mailboxId")
	if !ok {
		return
	}
	var address string
	err := s.db.QueryRow(r.Context(), `DELETE FROM mailbox_holds h USING mailboxes m WHERE h.case_id = $1 AND h.mailbox_id = $2 AND m.id = h.mailbox_id RETURNING m.address`, c.ID, mailboxID).Scan(&address)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &c.OrgID, "compliance.hold_release", "mailbox", mailboxID, map[string]any{"address": address, "caseId": c.ID, "caseName": c.Name})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) complianceMailboxes(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.complianceOrg(w, r, p)
	if !ok {
		return
	}
	type entry struct {
		ID          string     `json:"id"`
		DomainID    string     `json:"domainId"`
		Address     string     `json:"address"`
		DisplayName string     `json:"displayName"`
		Kind        string     `json:"kind"`
		Active      bool       `json:"active"`
		RetainedAt  *time.Time `json:"retainedAt"`
		Held        bool       `json:"held"`
	}
	type domain struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	rows, err := s.db.Query(r.Context(), `SELECT m.id, m.domain_id, m.address, m.display_name, m.kind, m.active, m.retained_at, `+mailboxHeld("m.id")+` FROM mailboxes m WHERE m.org_id = $1 ORDER BY m.address`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	boxes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[entry])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err = s.db.Query(r.Context(), `SELECT id, name FROM domains WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	domains, err := pgx.CollectRows(rows, pgx.RowToStructByPos[domain])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailboxes": nonNil(boxes), "domains": nonNil(domains)})
}
