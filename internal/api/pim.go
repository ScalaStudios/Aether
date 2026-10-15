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
	ctx := context.WithValue(r.Context(), davUserKey{}, p.User.ID)
	return ctx, s.ensureDAVDefaults(ctx, p.User.ID)
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
}

type contactInput struct {
	Name         string   `json:"name"`
	Emails       []string `json:"emails"`
	Phones       []string `json:"phones"`
	Organization string   `json:"organization"`
	Notes        string   `json:"notes"`
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
	q := "%" + strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))) + "%"
	rows, err := s.db.Query(ctx, `
		SELECT o.name, o.data FROM dav_objects o JOIN dav_collections c ON c.id = o.collection_id
		WHERE c.user_id = $1 AND c.kind = 'addressbook' AND (lower(o.summary) LIKE $2 OR EXISTS (SELECT 1 FROM unnest(o.emails) e WHERE e LIKE $2))
		ORDER BY lower(o.summary) LIMIT 500`, p.User.ID, q)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()
	out := []contact{}
	for rows.Next() {
		var name, data string
		if err := rows.Scan(&name, &data); err != nil {
			s.internalError(w, r, err)
			return
		}
		if card, err := vcard.NewDecoder(strings.NewReader(data)).Decode(); err == nil {
			out = append(out, contactFromCard(name, card))
		}
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
	book, err := s.davCollection(ctx, "addressbook", "contacts")
	if err != nil {
		s.davError(w, r, err)
		return
	}
	name := r.PathValue("id")
	var existing vcard.Card
	var ifMatch webdav.ConditionalMatch
	if name == "" {
		name = secrets.Token(16) + ".vcf"
	} else {
		if !objectNamePattern.MatchString(name) {
			notFound(w)
			return
		}
		book, existing, ifMatch, err = s.findContact(ctx, p.User.ID, name)
		if err != nil {
			s.davError(w, r, err)
			return
		}
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
	writeJSON(w, status, contactFromCard(name, card))
}

func (s *Server) findContact(ctx context.Context, userID, name string) (*collectionRow, vcard.Card, webdav.ConditionalMatch, error) {
	var c collectionRow
	var data, etag string
	err := s.db.QueryRow(ctx, `
		SELECT c.id, c.slug, o.data, o.etag FROM dav_objects o JOIN dav_collections c ON c.id = o.collection_id
		WHERE c.user_id = $1 AND c.kind = 'addressbook' AND o.name = $2 LIMIT 1`, userID, name).Scan(&c.ID, &c.Slug, &data, &etag)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, "", davNotFound()
	}
	if err != nil {
		return nil, nil, "", err
	}
	card, err := vcard.NewDecoder(strings.NewReader(data)).Decode()
	return &c, card, webdav.ConditionalMatch(`"` + etag + `"`), err
}

func (s *Server) deleteContact(w http.ResponseWriter, r *http.Request, p *principal) {
	name := r.PathValue("id")
	if !objectNamePattern.MatchString(name) {
		notFound(w)
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM dav_objects o USING dav_collections c WHERE c.id = o.collection_id AND c.user_id = $1 AND c.kind = 'addressbook' AND o.name = $2`, p.User.ID, name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type directoryEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Source  string `json:"source"`
}

func (s *Server) directory(ctx context.Context, userID, q string, limit int) ([]directoryEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT coalesce(nullif(m.display_name, ''), nullif(u.display_name, ''), ''), m.address, 'directory'
		FROM mailboxes m LEFT JOIN users u ON u.id = m.owner_user_id
		WHERE m.active AND m.org_id IN (SELECT org_id FROM organization_members WHERE user_id = $1)
		  AND (m.address LIKE $2 OR lower(m.display_name) LIKE $2 OR lower(coalesce(u.display_name, '')) LIKE $2)
		ORDER BY m.address LIMIT $3`, userID, q, limit)
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
		writeJSON(w, http.StatusOK, []directoryEntry{})
		return
	}
	like := "%" + q + "%"
	rows, err := s.db.Query(r.Context(), `
		SELECT o.summary, e, 'contact' FROM dav_objects o JOIN dav_collections c ON c.id = o.collection_id, unnest(o.emails) e
		WHERE c.user_id = $1 AND c.kind = 'addressbook' AND (e LIKE $2 OR lower(o.summary) LIKE $2) LIMIT 10`, p.User.ID, like)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	contacts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[directoryEntry])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	dir, err := s.directory(r.Context(), p.User.ID, like, 10)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	seen := map[string]bool{}
	out := []directoryEntry{}
	for _, e := range append(contacts, dir...) {
		if !seen[e.Address] {
			seen[e.Address] = true
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.HasPrefix(out[i].Address, q) && !strings.HasPrefix(out[j].Address, q)
	})
	if len(out) > 10 {
		out = out[:10]
	}
	writeJSON(w, http.StatusOK, out)
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
	transparent bool
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
	out, err := s.eventsIn(ctx, calendars, from, to)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) eventsIn(ctx context.Context, calendars []collectionRow, from, to time.Time) ([]calendarEvent, error) {
	byID := map[string]collectionRow{}
	ids := make([]string, len(calendars))
	for i, c := range calendars {
		byID[c.ID], ids[i] = c, c.ID
	}
	rows, err := s.db.Query(ctx, `
		SELECT o.collection_id, o.name, o.data FROM dav_objects o
		WHERE o.collection_id = ANY($1) AND (o.recurring OR (o.starts_at < $3 AND coalesce(o.ends_at, o.starts_at + interval '1 day') > $2))`, ids, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []calendarEvent{}
	for rows.Next() {
		first := len(out)
		var collectionID, name, data string
		if err := rows.Scan(&collectionID, &name, &data); err != nil {
			return nil, err
		}
		cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
		if err != nil {
			continue
		}
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
				continue
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
		for i := first; i < len(out); i++ {
			out[i].CalendarID, out[i].ReadOnly = byID[collectionID].Slug, byID[collectionID].Access == "read"
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, rows.Err()
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
		for _, a := range attendees {
			status := previous[a.Address]
			if status == "" {
				status = "NEEDS-ACTION"
			}
			delete(previous, a.Address)
			prop := attendeeProp(a, status, status == "NEEDS-ACTION")
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
	verb := "Invitation"
	if prev != nil {
		verb = "Updated invitation"
	}
	if len(removed) > 0 {
		cancel := cancelledCopy(event.Component, removed, sequence)
		cancel.Props.Set(organizerProp(mailpkg.Address{Name: organizer.DisplayName, Address: strings.ToLower(organizer.Address)}))
		err = s.sendITIP(organizer, removed, "Cancelled: "+in.Title, "CANCEL", cancel)
	}
	if err == nil {
		err = s.sendITIP(organizer, attendees, verb+": "+in.Title, "REQUEST", event.Component)
	}
	if err != nil {
		s.log.Warn("invitation rejected", "user", p.User.ID, "err", err)
		writeError(w, http.StatusBadGateway, "send_failed", "The event was saved, but the invitations could not be sent: "+err.Error())
		return
	}
	out, _, _ := eventFromComponent(name, event.Component)
	out.CalendarID = calendar.Slug
	writeJSON(w, status, out)
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
