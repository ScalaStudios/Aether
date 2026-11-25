package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/mail"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/secrets"
)

var objectNamePattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,255}$`)

func (s *Server) pimContext(r *http.Request, p *principal) (context.Context, error) {
	return s.pimContextFor(r, p.User.ID)
}

func (s *Server) pimContextFor(r *http.Request, userID string) (context.Context, error) {
	ctx := context.WithValue(r.Context(), davUserKey{}, userID)
	return ctx, s.ensureDAVDefaults(ctx, userID)
}

func (s *Server) mailboxOwnerContext(w http.ResponseWriter, r *http.Request, p *principal) (context.Context, *ownedMailbox, bool) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return nil, nil, false
	}
	m := &ownedMailbox{}
	var owner *string
	err := s.db.QueryRow(r.Context(), `
		SELECT m.id, m.org_id, m.domain_id, m.address, m.display_name, m.kind, m.owner_user_id FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.id = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL`, id).
		Scan(&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.DisplayName, &m.Kind, &owner)
	if err != nil || !p.canOnDomain(m.OrgID, m.DomainID, permMailboxesImport) {
		if p.apiKey != nil {
			notFound(w)
			return nil, nil, false
		}
		m, ok = s.ownedMailbox(w, r, p)
		if !ok {
			return nil, nil, false
		}
		if err := s.db.QueryRow(r.Context(), `SELECT owner_user_id FROM mailboxes WHERE id = $1`, m.ID).Scan(&owner); err != nil {
			s.internalError(w, r, err)
			return nil, nil, false
		}
	}
	if !s.requireFeature(w, r, m.OrgID, featureCalendar) {
		return nil, nil, false
	}
	if owner == nil {
		writeError(w, http.StatusUnprocessableEntity, "no_owner", "This mailbox has no owner whose calendar and contacts could receive the import.")
		return nil, nil, false
	}
	ctx, err := s.pimContextFor(r, *owner)
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	return ctx, m, true
}

func davStatusCode(err error) int {
	v := reflect.ValueOf(err)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return 0
	}
	if code := v.Elem().FieldByName("Code"); code.IsValid() && code.CanInt() {
		return int(code.Int())
	}
	return 0
}

func (s *Server) davError(w http.ResponseWriter, r *http.Request, err error) {
	switch code := davStatusCode(err); {
	case code == http.StatusNotFound:
		notFound(w)
	case code == http.StatusPreconditionFailed:
		writeError(w, http.StatusConflict, "changed", "This item was changed in another app. Reload and try again.")
	case code >= 400 && code < 500:
		writeError(w, code, "invalid", errors.Unwrap(err).Error())
	default:
		s.internalError(w, r, err)
	}
}

type contact struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Emails       []string `json:"emails"`
	Phones       []string `json:"phones"`
	Organization string   `json:"organization"`
	Notes        string   `json:"notes"`
	BookID       string   `json:"bookId,omitempty"`
	ReadOnly     bool     `json:"readOnly,omitempty"`
}

type contactInput struct {
	Name         string   `json:"name"`
	Emails       []string `json:"emails"`
	Phones       []string `json:"phones"`
	Organization string   `json:"organization"`
	Notes        string   `json:"notes"`
	BookID       string   `json:"bookId"`
}

func contactFromCard(id string, card vcard.Card) contact {
	c := contact{ID: id, Name: card.PreferredValue(vcard.FieldFormattedName), Emails: nonNil(card.Values(vcard.FieldEmail)), Phones: nonNil(card.Values(vcard.FieldTelephone)), Notes: card.Value(vcard.FieldNote)}
	if org := card.Get(vcard.FieldOrganization); org != nil {
		c.Organization = strings.ReplaceAll(org.Value, ";", " ")
	}
	return c
}

func (s *Server) listContacts(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	books, err := s.addressBooks(ctx, r.URL.Query().Get("book"))
	if err != nil {
		s.davError(w, r, err)
		return
	}
	out := []contact{}
	if len(books) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	byID := map[string]collectionRow{}
	ids := make([]string, len(books))
	for i, book := range books {
		byID[book.ID] = book
		ids[i] = book.ID
	}
	q := "%" + strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))) + "%"
	rows, err := s.db.Query(ctx, `
		SELECT o.collection_id, o.name, o.data FROM dav_objects o
		WHERE o.collection_id = ANY($1) AND NOT o.is_group
		  AND (lower(o.summary) LIKE $2 OR EXISTS (SELECT 1 FROM unnest(o.emails) e WHERE e LIKE $2))
		ORDER BY lower(o.summary), o.name LIMIT 500`, ids, q)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var collectionID, name, data string
		if err := rows.Scan(&collectionID, &name, &data); err != nil {
			s.internalError(w, r, err)
			return
		}
		card, err := vcard.NewDecoder(strings.NewReader(data)).Decode()
		if err != nil || isGroupCard(card) {
			continue
		}
		item := contactFromCard(name, card)
		item.BookID = byID[collectionID].Slug
		item.ReadOnly = byID[collectionID].Access == "read"
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (in *contactInput) card(uid string, existing vcard.Card) (vcard.Card, string) {
	card := existing
	if card == nil {
		card = vcard.Card{}
		card.SetValue(vcard.FieldVersion, "4.0")
		card.SetValue(vcard.FieldUID, uid)
	}
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "" || len(in.Name) > 200:
		return nil, "Enter a name of up to 200 characters."
	case len(in.Emails) > 10 || len(in.Phones) > 10:
		return nil, "A contact can have up to 10 email addresses and 10 phone numbers."
	case len(in.Notes) > 5000 || len(in.Organization) > 200:
		return nil, "The organization or notes are too long."
	}
	card.SetValue(vcard.FieldFormattedName, in.Name)
	card.SetName(&vcard.Name{GivenName: in.Name})
	delete(card, vcard.FieldEmail)
	delete(card, vcard.FieldTelephone)
	for _, e := range in.Emails {
		a, err := mail.ParseAddress(strings.TrimSpace(e))
		if err != nil || a.Name != "" {
			return nil, e + " is not a valid email address."
		}
		card.Add(vcard.FieldEmail, &vcard.Field{Value: strings.ToLower(a.Address)})
	}
	for _, ph := range in.Phones {
		if ph = strings.TrimSpace(ph); ph != "" {
			card.Add(vcard.FieldTelephone, &vcard.Field{Value: ph})
		}
	}
	setOptional := func(field, value string) {
		if strings.TrimSpace(value) == "" {
			delete(card, field)
		} else {
			card.SetValue(field, strings.TrimSpace(value))
		}
	}
	setOptional(vcard.FieldOrganization, in.Organization)
	setOptional(vcard.FieldNote, in.Notes)
	return card, ""
}

func (s *Server) saveContact(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var in contactInput
	if !decode(w, r, 64<<10, &in) {
		return
	}
	name := r.PathValue("id")
	var book *collectionRow
	var existing vcard.Card
	var ifMatch webdav.ConditionalMatch
	if name == "" {
		slug := in.BookID
		if slug == "" {
			slug = defaultAddressBook
		}
		book, err = s.writableCollection(ctx, "addressbook", slug)
		name = secrets.Token(16) + ".vcf"
	} else {
		book, existing, ifMatch, err = s.findAddressObject(ctx, name, in.BookID, false)
		if errors.Is(err, errAmbiguousObject) {
			writeError(w, http.StatusConflict, "ambiguous", "This contact is in more than one address book. Choose the address book.")
			return
		}
		if err != nil {
			s.davError(w, r, err)
			return
		}
		book, err = s.writableCollection(ctx, "addressbook", book.Slug)
	}
	if err != nil {
		s.davError(w, r, err)
		return
	}
	card, msg := in.card(strings.TrimSuffix(name, ".vcf"), existing)
	if msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_contact", msg)
		return
	}
	f, err := cardFields(card)
	if err != nil {
		s.davError(w, r, err)
		return
	}
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		s.internalError(w, r, err)
		return
	}
	if _, err := s.putDAVObject(ctx, book.ID, name, buf.String(), f, ifMatch, ""); err != nil {
		s.davError(w, r, err)
		return
	}
	status := http.StatusOK
	if existing == nil {
		status = http.StatusCreated
	}
	out := contactFromCard(name, card)
	out.BookID = book.Slug
	writeJSON(w, status, out)
}

func (s *Server) findAddressObject(ctx context.Context, name, bookSlug string, group bool) (*collectionRow, vcard.Card, webdav.ConditionalMatch, error) {
	if !objectNamePattern.MatchString(name) {
		return nil, nil, "", davNotFound()
	}
	books, err := s.addressBooks(ctx, bookSlug)
	if err != nil {
		return nil, nil, "", err
	}
	byID := map[string]collectionRow{}
	ids := make([]string, 0, len(books))
	for _, book := range books {
		byID[book.ID] = book
		ids = append(ids, book.ID)
	}
	if len(ids) == 0 {
		return nil, nil, "", davNotFound()
	}
	rows, err := s.db.Query(ctx, `SELECT collection_id, data, etag FROM dav_objects WHERE collection_id = ANY($1) AND name = $2 AND is_group = $3`, ids, name, group)
	if err != nil {
		return nil, nil, "", err
	}
	defer rows.Close()
	var collectionID, data, etag string
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			return nil, nil, "", errAmbiguousObject
		}
		if err := rows.Scan(&collectionID, &data, &etag); err != nil {
			return nil, nil, "", err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, "", err
	}
	if count == 0 {
		return nil, nil, "", davNotFound()
	}
	card, err := vcard.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		return nil, nil, "", err
	}
	book := byID[collectionID]
	return &book, card, webdav.ConditionalMatch(`"` + etag + `"`), nil
}

func (s *Server) deleteContact(w http.ResponseWriter, r *http.Request, p *principal) {
	name := r.PathValue("id")
	if !objectNamePattern.MatchString(name) {
		notFound(w)
		return
	}
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	book, _, _, err := s.findAddressObject(ctx, name, r.URL.Query().Get("book"), false)
	if errors.Is(err, errAmbiguousObject) {
		writeError(w, http.StatusConflict, "ambiguous", "This contact is in more than one address book. Choose the address book.")
		return
	}
	if err != nil {
		s.davError(w, r, err)
		return
	}
	book, err = s.writableCollection(ctx, "addressbook", book.Slug)
	if err != nil {
		s.davError(w, r, err)
		return
	}
	if err := s.deleteDAVObject(ctx, book.ID, name); err != nil {
		s.davError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type directoryEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Source  string `json:"source"`
	Kind    string `json:"kind,omitempty"`
	Company string `json:"company,omitempty"`
	Phone   string `json:"phone,omitempty"`
}

func (s *Server) directory(ctx context.Context, userID, q string, limit int) ([]directoryEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT ON (address) name, address, 'directory', kind, company, phone FROM (
			SELECT coalesce(nullif(m.display_name, ''), nullif(u.display_name, ''), '') AS name, m.address, coalesce(r.resource_type, '') AS kind, '' AS company, '' AS phone
			FROM mailboxes m LEFT JOIN users u ON u.id = m.owner_user_id LEFT JOIN resources r ON r.mailbox_id = m.id
			WHERE m.active AND NOT m.hidden_from_directory AND m.org_id IN (SELECT org_id FROM organization_members WHERE user_id = $1)
			  AND (m.address LIKE $2 OR lower(m.display_name) LIKE $2 OR lower(coalesce(u.display_name, '')) LIKE $2)
			UNION ALL
			SELECT a.name, a.address, '', '', '' FROM aliases a
			WHERE a.kind = 'list' AND a.active AND NOT a.hidden_from_directory AND a.org_id IN (SELECT org_id FROM organization_members WHERE user_id = $1)
			  AND (a.address LIKE $2 OR lower(a.name) LIKE $2)
			UNION ALL
			SELECT c.name, c.email, '', c.company, c.phone FROM organization_contacts c
			WHERE c.org_id IN (SELECT org_id FROM organization_members WHERE user_id = $1)
			  AND (c.email LIKE $2 OR lower(c.name) LIKE $2 OR lower(c.company) LIKE $2)
		) d
		ORDER BY address LIMIT $3`, userID, q, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[directoryEntry])
}

func (s *Server) listDirectory(w http.ResponseWriter, r *http.Request, p *principal) {
	entries, err := s.directory(r.Context(), p.User.ID, "%"+strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))+"%", 1000)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(entries))
}

func (s *Server) suggestRecipients(w http.ResponseWriter, r *http.Request, p *principal) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		writeJSON(w, http.StatusOK, []recipient{})
		return
	}
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	like := "%" + q + "%"
	books, err := s.addressBooks(ctx, "")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var contacts []directoryEntry
	if len(books) > 0 {
		ids := make([]string, len(books))
		for i, book := range books {
			ids[i] = book.ID
		}
		rows, err := s.db.Query(ctx, `
			SELECT o.summary, e, 'contact', '', '', '' FROM dav_objects o, unnest(o.emails) e
			WHERE o.collection_id = ANY($1) AND NOT o.is_group AND (e LIKE $2 OR lower(o.summary) LIKE $2)
			LIMIT 8`, ids, like)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		contacts, err = pgx.CollectRows(rows, pgx.RowToStructByPos[directoryEntry])
		if err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	groups, err := s.suggestGroups(ctx, books, like)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	dir, err := s.directory(ctx, p.User.ID, like, 8)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]recipient, 0, len(contacts)+len(groups)+len(dir))
	for _, entry := range contacts {
		out = append(out, recipient{Name: entry.Name, Address: entry.Address, Source: entry.Source})
	}
	out = append(out, groups...)
	for _, entry := range dir {
		out = append(out, recipient{Name: entry.Name, Address: entry.Address, Source: entry.Source, Kind: entry.Kind, Company: entry.Company, Phone: entry.Phone})
	}
	seen := map[string]bool{}
	deduped := out[:0]
	for _, entry := range out {
		if seen[entry.Address] {
			continue
		}
		seen[entry.Address] = true
		deduped = append(deduped, entry)
	}
	sort.SliceStable(deduped, func(i, j int) bool {
		iMatch := strings.HasPrefix(deduped[i].Address, q) || strings.HasPrefix(strings.ToLower(deduped[i].Name), q)
		jMatch := strings.HasPrefix(deduped[j].Address, q) || strings.HasPrefix(strings.ToLower(deduped[j].Name), q)
		return iMatch && !jMatch
	})
	if len(deduped) > 10 {
		deduped = deduped[:10]
	}
	writeJSON(w, http.StatusOK, nonNil(deduped))
}

type calendarEvent struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Start       time.Time       `json:"start"`
	End         time.Time       `json:"end"`
	SeriesStart time.Time       `json:"seriesStart"`
	AllDay      bool            `json:"allDay"`
	Location    string          `json:"location"`
	Description string          `json:"description"`
	Repeat      string          `json:"repeat"`
	Organizer   string          `json:"organizer,omitempty"`
	Attendees   []eventAttendee `json:"attendees"`
	CalendarID  string          `json:"calendarId,omitempty"`
	ReadOnly    bool            `json:"readOnly,omitempty"`
	Reminder    *int            `json:"reminderMinutes,omitempty"`
	transparent bool
	cancelled   bool
}

var repeatRules = map[string]string{"daily": "FREQ=DAILY", "weekly": "FREQ=WEEKLY", "monthly": "FREQ=MONTHLY", "yearly": "FREQ=YEARLY"}

func eventFromComponent(id string, comp *ical.Component) (calendarEvent, *ical.Event, error) {
	ev := &ical.Event{Component: comp}
	out := calendarEvent{ID: id, Organizer: eventOrganizer(comp).Address, Attendees: eventAttendees(comp)}
	out.Title, _ = comp.Props.Text(ical.PropSummary)
	out.Location, _ = comp.Props.Text(ical.PropLocation)
	out.Description, _ = comp.Props.Text(ical.PropDescription)
	transp, _ := comp.Props.Text(ical.PropTransparency)
	out.transparent = strings.EqualFold(transp, "TRANSPARENT")
	status, _ := comp.Props.Text(ical.PropStatus)
	out.cancelled = strings.EqualFold(status, "CANCELLED")
	if start := comp.Props.Get(ical.PropDateTimeStart); start != nil && start.ValueType() == ical.ValueDate {
		out.AllDay = true
	}
	if rule := comp.Props.Get(ical.PropRecurrenceRule); rule != nil {
		out.Repeat = "custom"
		for name, value := range repeatRules {
			if strings.EqualFold(rule.Value, value) {
				out.Repeat = name
			}
		}
	}
	var err error
	if out.Start, err = ev.DateTimeStart(time.UTC); err != nil {
		return out, ev, err
	}
	out.SeriesStart = out.Start
	if out.End, err = ev.DateTimeEnd(time.UTC); err != nil || out.End.IsZero() {
		out.End = out.Start.Add(time.Hour)
		if out.AllDay {
			out.End = out.Start.AddDate(0, 0, 1)
		}
	}
	out.Reminder = eventReminder(comp, out.Start)
	return out, ev, nil
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	from, err1 := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
	to, err2 := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
	if err1 != nil || err2 != nil || !to.After(from) || to.Sub(from) > 400*24*time.Hour {
		writeError(w, http.StatusUnprocessableEntity, "invalid_range", "Give start and end as RFC 3339 times, at most 400 days apart.")
		return
	}
	calendars, err := s.davCollections(ctx, "calendar")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := eventsIn(ctx, s.db, calendars, from, to)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func eventsIn(ctx context.Context, db queryer, calendars []collectionRow, from, to time.Time) ([]calendarEvent, error) {
	byID := map[string]collectionRow{}
	ids := make([]string, len(calendars))
	for i, c := range calendars {
		byID[c.ID], ids[i] = c, c.ID
	}
	subs, err := subscribedIDs(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
		SELECT o.collection_id, o.name, o.data FROM dav_objects o
		WHERE o.collection_id = ANY($1) AND o.component = 'VEVENT' AND (o.recurring OR (o.starts_at < $3 AND coalesce(o.ends_at, o.starts_at + interval '1 day') > $2))`, ids, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []calendarEvent{}
	for rows.Next() {
		var collectionID, name, data string
		if err := rows.Scan(&collectionID, &name, &data); err != nil {
			return nil, err
		}
		cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
		if err != nil {
			continue
		}
		for _, e := range expandObject(name, cal, from, to) {
			e.CalendarID, e.ReadOnly = byID[collectionID].Slug, byID[collectionID].Access == "read" || subs[collectionID]
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, rows.Err()
}

func expandObject(name string, cal *ical.Calendar, from, to time.Time) []calendarEvent {
	out := []calendarEvent{}
	var master *ical.Component
	overrides := map[int64]*ical.Component{}
	for _, comp := range cal.Events() {
		if comp.Props.Get(ical.PropRecurrenceID) == nil {
			master = comp.Component
		} else if rid, err := comp.Props.DateTime(ical.PropRecurrenceID, time.UTC); err == nil {
			overrides[rid.Unix()] = comp.Component
		}
	}
	var series calendarEvent
	if master != nil {
		base, ev, err := eventFromComponent(name, master)
		if err != nil {
			return out
		}
		series = base
		set, err := ev.RecurrenceSet(time.UTC)
		if err != nil || set == nil {
			if base.Start.Before(to) && base.End.After(from) {
				out = append(out, base)
			}
		} else {
			length := base.End.Sub(base.Start)
			for _, occurrence := range set.Between(from.Add(-length), to, true) {
				if overrides[occurrence.Unix()] != nil {
					continue
				}
				o := base
				o.Start, o.End = occurrence, occurrence.Add(length)
				out = append(out, o)
				if len(out) > 5000 {
					break
				}
			}
		}
	}
	for _, comp := range overrides {
		if status, _ := comp.Props.Text(ical.PropStatus); strings.EqualFold(status, "CANCELLED") {
			continue
		}
		o, _, err := eventFromComponent(name, comp)
		if err != nil || !o.Start.Before(to) || !o.End.After(from) {
			continue
		}
		if master != nil {
			o.SeriesStart, o.Repeat = series.SeriesStart, series.Repeat
		}
		out = append(out, o)
	}
	return out
}

type eventInput struct {
	Title       string    `json:"title"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	AllDay      bool      `json:"allDay"`
	Location    string    `json:"location"`
	Description string    `json:"description"`
	Repeat      string    `json:"repeat"`
	Attendees   []string  `json:"attendees"`
	CalendarID  string    `json:"calendarId"`
	Reminder    *int      `json:"reminderMinutes"`
}

func (s *Server) saveEvent(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var in eventInput
	if !decode(w, r, 64<<10, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	switch {
	case in.Title == "" || len(in.Title) > 300:
		writeError(w, http.StatusUnprocessableEntity, "invalid_event", "Give the event a title of up to 300 characters.")
		return
	case in.Start.IsZero() || !in.End.After(in.Start):
		writeError(w, http.StatusUnprocessableEntity, "invalid_event", "The event must end after it starts.")
		return
	case in.Repeat != "" && repeatRules[in.Repeat] == "":
		writeError(w, http.StatusUnprocessableEntity, "invalid_event", "Repeat must be daily, weekly, monthly, yearly, or empty.")
		return
	case len(in.Location) > 500 || len(in.Description) > 10000:
		writeError(w, http.StatusUnprocessableEntity, "invalid_event", "The location or description is too long.")
		return
	case in.Reminder != nil && (*in.Reminder < 0 || *in.Reminder > 7*24*60):
		writeError(w, http.StatusUnprocessableEntity, "invalid_event", "A reminder can be at the start of the event or up to 7 days before.")
		return
	}
	if in.CalendarID == "" {
		in.CalendarID = "personal"
	}
	calendar, err := s.writableCollection(ctx, "calendar", in.CalendarID)
	if err != nil {
		s.davError(w, r, err)
		return
	}
	name := r.PathValue("id")
	uid := secrets.Token(16)
	var prev *ical.Component
	var ifMatch webdav.ConditionalMatch
	status := http.StatusCreated
	if name == "" {
		name = uid + ".ics"
	} else {
		if !objectNamePattern.MatchString(name) {
			notFound(w)
			return
		}
		var etag, data string
		err := s.db.QueryRow(ctx, `SELECT uid, etag, data FROM dav_objects WHERE collection_id = $1 AND name = $2`, calendar.ID, name).Scan(&uid, &etag, &data)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound(w)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		ifMatch, status = webdav.ConditionalMatch(`"`+etag+`"`), http.StatusOK
		if cal, err := ical.NewDecoder(strings.NewReader(data)).Decode(); err == nil {
			prev = masterEvent(cal)
		}
	}
	organizer, err := s.organizerMailbox(ctx, p.User)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var attendees, removed []mailpkg.Address
	invited := prev != nil && eventOrganizer(prev).Address != "" && (organizer == nil || eventOrganizer(prev).Address != strings.ToLower(organizer.Address))
	if !invited && (len(in.Attendees) > 0 || prev != nil && len(eventAttendees(prev)) > 0) {
		if organizer == nil {
			writeError(w, http.StatusUnprocessableEntity, "no_mailbox", "You need a mailbox of your own to send invitations.")
			return
		}
		var ok bool
		if attendees, ok = parseAttendees(in.Attendees, strings.ToLower(organizer.Address)); !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid_event", "Give attendees as valid email addresses, at most 100.")
			return
		}
	}
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, uid)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	if in.AllDay {
		event.Props.SetDate(ical.PropDateTimeStart, in.Start)
		event.Props.SetDate(ical.PropDateTimeEnd, in.End)
	} else {
		event.Props.SetDateTime(ical.PropDateTimeStart, in.Start.UTC())
		event.Props.SetDateTime(ical.PropDateTimeEnd, in.End.UTC())
	}
	event.Props.SetText(ical.PropSummary, in.Title)
	if in.Location != "" {
		event.Props.SetText(ical.PropLocation, in.Location)
	}
	if in.Description != "" {
		event.Props.SetText(ical.PropDescription, in.Description)
	}
	if in.Repeat != "" {
		event.Props.Set(&ical.Prop{Name: ical.PropRecurrenceRule, Value: repeatRules[in.Repeat]})
	}
	if in.Reminder != nil {
		setReminder(event.Component, *in.Reminder, in.Title)
	}
	sequence := 0
	if prev != nil {
		sequence = eventSequence(prev) + 1
		event.Props.SetText(ical.PropSequence, strconv.Itoa(sequence))
	}
	if invited {
		event.Props.Set(prev.Props.Get(ical.PropOrganizer))
		event.Props[ical.PropAttendee] = prev.Props[ical.PropAttendee]
	} else if organizer != nil && (len(attendees) > 0 || prev != nil && len(eventAttendees(prev)) > 0) {
		previous := map[string]string{}
		if prev != nil {
			for _, a := range eventAttendees(prev) {
				previous[a.Address] = strings.ToUpper(a.Status)
			}
		}
		if len(attendees) > 0 {
			event.Props.Set(organizerProp(mailpkg.Address{Name: organizer.DisplayName, Address: strings.ToLower(organizer.Address)}))
		}
		addresses := make([]string, len(attendees))
		for i, a := range attendees {
			addresses[i] = a.Address
		}
		kinds, err := s.resourceKinds(ctx, addresses)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		for _, a := range attendees {
			status := previous[a.Address]
			if status == "" {
				status = "NEEDS-ACTION"
			}
			delete(previous, a.Address)
			prop := attendeeProp(a, status, status == "NEEDS-ACTION")
			if kind := kinds[a.Address]; kind != "" {
				prop.Params.Set(ical.ParamCalendarUserType, resourceUserTypes[kind])
				prop.Params.Set(ical.ParamRole, "NON-PARTICIPANT")
			}
			event.Props.Add(&prop)
		}
		for address := range previous {
			removed = append(removed, mailpkg.Address{Address: address})
		}
	}
	doc := ical.NewCalendar()
	doc.Props.SetText(ical.PropVersion, "2.0")
	doc.Props.SetText(ical.PropProductID, "-//Scala Studios//Aether//EN")
	doc.Children = append(doc.Children, event.Component)
	f, err := calendarFields(doc)
	if err != nil {
		s.davError(w, r, err)
		return
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(doc); err != nil {
		s.internalError(w, r, err)
		return
	}
	if _, err := s.putDAVObject(ctx, calendar.ID, name, buf.String(), f, ifMatch, ""); err != nil {
		s.davError(w, r, err)
		return
	}
	kind := "request"
	if prev != nil {
		kind = "update"
	}
	if len(removed) > 0 {
		cancel := cancelledCopy(event.Component, removed, sequence)
		cancel.Props.Set(organizerProp(mailpkg.Address{Name: organizer.DisplayName, Address: strings.ToLower(organizer.Address)}))
		_, err = s.sendITIP(ctx, organizer, nil, removed, "cancel", in.Title, "CANCEL", cancel)
	}
	var bookings []bookingOutcome
	if err == nil {
		bookings, err = s.sendITIP(ctx, organizer, nil, attendees, kind, in.Title, "REQUEST", event.Component)
	}
	if err != nil {
		s.log.Warn("invitation rejected", "user", p.User.ID, "err", err)
		writeError(w, http.StatusBadGateway, "send_failed", "The event was saved, but the invitations could not be sent: "+err.Error())
		return
	}
	out, _, _ := eventFromComponent(name, event.Component)
	out.CalendarID = calendar.Slug
	writeJSON(w, status, struct {
		calendarEvent
		Bookings []bookingOutcome `json:"bookings,omitempty"`
	}{out, bookings})
}

func (s *Server) deleteEvent(w http.ResponseWriter, r *http.Request, p *principal) {
	name := r.PathValue("id")
	if !objectNamePattern.MatchString(name) {
		notFound(w)
		return
	}
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	slug := r.URL.Query().Get("calendar")
	if slug == "" {
		slug = "personal"
	}
	calendar, err := s.writableCollection(ctx, "calendar", slug)
	if err != nil {
		s.davError(w, r, err)
		return
	}
	var data string
	if calendar.Access != "owner" {
		err = s.db.QueryRow(ctx, `SELECT data FROM dav_objects WHERE collection_id = $1 AND name = $2`, calendar.ID, name).Scan(&data)
		if cal, decodeErr := ical.NewDecoder(strings.NewReader(data)).Decode(); err == nil && decodeErr == nil && masterEvent(cal) != nil && eventOrganizer(masterEvent(cal)).Address != "" {
			writeError(w, http.StatusForbidden, "meeting", "This is a meeting with other people. Only the calendar's owner can cancel it.")
			return
		}
	}
	err = s.db.QueryRow(ctx, `DELETE FROM dav_objects WHERE collection_id = $1 AND name = $2 RETURNING data`, calendar.ID, name).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.notifyDeletedEvent(r.Context(), p.User, data); err != nil {
		s.log.Warn("cancellation rejected", "user", p.User.ID, "err", err)
		writeError(w, http.StatusBadGateway, "send_failed", "The event was deleted, but the other attendees could not be told: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
