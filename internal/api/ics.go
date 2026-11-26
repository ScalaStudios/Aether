package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/emersion/go-vcard"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

const (
	maxImportBytes   = 10 << 20
	maxImportObjects = 5000
)

func newCalendar() *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//Scala Studios//Aether//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	return cal
}

func referencedTZIDs(comps []*ical.Component) map[string]int {
	ids := map[string]int{}
	var walk func(c *ical.Component)
	walk = func(c *ical.Component) {
		for _, props := range c.Props {
			for _, p := range props {
				tz := p.Params.Get(ical.ParamTimezoneID)
				if tz == "" {
					continue
				}
				year := time.Now().Year()
				if t, err := p.DateTime(time.UTC); err == nil {
					year = t.Year()
				}
				if seen, ok := ids[tz]; !ok || year < seen {
					ids[tz] = year
				}
			}
		}
		for _, child := range c.Children {
			walk(child)
		}
	}
	for _, c := range comps {
		walk(c)
	}
	return ids
}

func addTimezones(cal *ical.Calendar) {
	have := map[string]bool{}
	var comps []*ical.Component
	for _, c := range cal.Children {
		if c.Name == ical.CompTimezone {
			id, _ := c.Props.Text(ical.PropTimezoneID)
			have[id] = true
			continue
		}
		comps = append(comps, c)
	}
	years := referencedTZIDs(comps)
	var ids []string
	for id := range years {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if loc, ok := validTimezone(id); ok && !have[id] {
			cal.Children = append([]*ical.Component{vtimezone(id, loc, max(years[id]-1, 1970), time.Now().Year()+1)}, cal.Children...)
		}
	}
}

func vtimezone(id string, loc *time.Location, from, to int) *ical.Component {
	tz := ical.NewComponent(ical.CompTimezone)
	tz.Props.SetText(ical.PropTimezoneID, id)
	t := time.Date(min(from, to), 1, 1, 0, 0, 0, 0, loc)
	for {
		_, next := t.ZoneBounds()
		if next.IsZero() || next.Year() > to {
			break
		}
		tz.Children = append(tz.Children, observance(t, next, next.Year() == to))
		t = next
	}
	if len(tz.Children) == 0 {
		tz.Children = append(tz.Children, observance(t, t, false))
	}
	return tz
}

func observance(before, at time.Time, repeat bool) *ical.Component {
	_, from := before.Zone()
	name, to := at.Zone()
	kind := ical.CompTimezoneStandard
	if at.IsDST() {
		kind = ical.CompTimezoneDaylight
	}
	c := ical.NewComponent(kind)
	local := at.In(time.FixedZone("", from))
	c.Props.Set(&ical.Prop{Name: ical.PropDateTimeStart, Value: local.Format("20060102T150405"), Params: ical.Params{}})
	if repeat {
		n := (local.Day()-1)/7 + 1
		if local.Day()+7 > time.Date(local.Year(), local.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day() {
			n = -1
		}
		rule := fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%d%s", local.Month(), n, strings.ToUpper(local.Weekday().String()[:2]))
		c.Props.Set(&ical.Prop{Name: ical.PropRecurrenceRule, Value: rule, Params: ical.Params{}})
	}
	c.Props.SetText(ical.PropTimezoneName, name)
	c.Props.Set(&ical.Prop{Name: ical.PropTimezoneOffsetFrom, Value: utcOffset(from), Params: ical.Params{}})
	c.Props.Set(&ical.Prop{Name: ical.PropTimezoneOffsetTo, Value: utcOffset(to), Params: ical.Params{}})
	return c
}

func utcOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	return fmt.Sprintf("%s%02d%02d", sign, seconds/3600, seconds/60%60)
}

func (s *Server) importCalendar(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.importCalendarInto(ctx, w, r)
}

func (s *Server) importMailboxCalendar(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, m, ok := s.mailboxOwnerContext(w, r, p)
	if !ok {
		return
	}
	if counts, ok := s.importCalendarInto(ctx, w, r); ok {
		s.audit(r.Context(), r, p, &m.OrgID, "calendar.import", "mailbox", m.ID, counts)
	}
}

func (s *Server) importCalendarInto(ctx context.Context, w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	source, err := ical.NewDecoder(r.Body).Decode()
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_calendar", "The file is not a valid iCalendar (.ics) file.")
		return nil, false
	}
	calendar, err := s.davCollection(ctx, "calendar", "personal")
	if err != nil {
		s.davError(w, r, err)
		return nil, false
	}
	timezones := map[string]*ical.Component{}
	groups := map[string][]*ical.Component{}
	var order []string
	for _, comp := range source.Children {
		switch comp.Name {
		case ical.CompTimezone:
			if id, _ := comp.Props.Text(ical.PropTimezoneID); id != "" {
				timezones[id] = comp
			}
		case ical.CompEvent, ical.CompToDo:
			uid, _ := comp.Props.Text(ical.PropUID)
			if uid == "" {
				uid = secrets.Token(16)
				comp.Props.SetText(ical.PropUID, uid)
			}
			if _, seen := groups[uid]; !seen {
				order = append(order, uid)
			}
			groups[uid] = append(groups[uid], comp)
		}
	}
	if len(order) > maxImportObjects {
		writeError(w, http.StatusUnprocessableEntity, "too_many_events", "Import at most 5,000 events at a time.")
		return nil, false
	}
	var imported, updated, skipped int
	for _, uid := range order {
		obj := newCalendar()
		for id := range referencedTZIDs(groups[uid]) {
			if tz := timezones[id]; tz != nil {
				obj.Children = append(obj.Children, tz)
			}
		}
		obj.Children = append(obj.Children, groups[uid]...)
		fields, err := calendarFields(obj)
		var buf bytes.Buffer
		if err == nil {
			err = ical.NewEncoder(&buf).Encode(obj)
		}
		if err != nil {
			skipped++
			continue
		}
		var name string
		err = s.db.QueryRow(ctx, `SELECT name FROM dav_objects WHERE collection_id = $1 AND uid = $2`, calendar.ID, uid).Scan(&name)
		exists := err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			name = uid + ".ics"
			if !objectNamePattern.MatchString(name) {
				name = secrets.Token(16) + ".ics"
			}
		} else if err != nil {
			s.internalError(w, r, err)
			return nil, false
		}
		if _, err := s.putDAVObject(ctx, calendar.ID, name, buf.String(), fields, webdav.ConditionalMatch(""), webdav.ConditionalMatch("")); err != nil {
			skipped++
			continue
		}
		if exists {
			updated++
		} else {
			imported++
		}
	}
	counts := map[string]any{"imported": imported, "updated": updated, "skipped": skipped}
	writeJSON(w, http.StatusOK, counts)
	return counts, true
}

func (s *Server) exportCalendar(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	body, err := s.calendarICS(ctx, p.User.ID, "personal")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="calendar.ics"`)
	w.Write([]byte(body))
}

func (s *Server) calendarICS(ctx context.Context, userID, slug string) (string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT o.data FROM dav_objects o JOIN dav_collections c ON c.id = o.collection_id
		WHERE c.user_id = $1 AND c.kind = 'calendar' AND c.slug = $2 ORDER BY o.starts_at NULLS LAST, o.name`, userID, slug)
	if err != nil {
		return "", err
	}
	objects, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	return compileCalendar(objects)
}

func compileCalendar(objects []string) (string, error) {
	out := newCalendar()
	seenTZ := map[string]bool{}
	var events []*ical.Component
	for _, data := range objects {
		cal, err := ical.NewDecoder(bytes.NewReader([]byte(data))).Decode()
		if err != nil {
			continue
		}
		for _, comp := range cal.Children {
			if comp.Name == ical.CompTimezone {
				id, _ := comp.Props.Text(ical.PropTimezoneID)
				if id == "" || seenTZ[id] {
					continue
				}
				seenTZ[id] = true
				out.Children = append(out.Children, comp)
				continue
			}
			events = append(events, comp)
		}
	}
	out.Children = append(out.Children, events...)
	if len(out.Children) == 0 {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Scala Studios//Aether//EN\r\nEND:VCALENDAR\r\n", nil
	}
	addTimezones(out)
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(out); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (s *Server) importContacts(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.importContactsInto(ctx, w, r)
}

func (s *Server) importMailboxContacts(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, m, ok := s.mailboxOwnerContext(w, r, p)
	if !ok {
		return
	}
	if counts, ok := s.importContactsInto(ctx, w, r); ok {
		s.audit(r.Context(), r, p, &m.OrgID, "contacts.import", "mailbox", m.ID, counts)
	}
}

func (s *Server) importContactsInto(ctx context.Context, w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	dec := vcard.NewDecoder(http.MaxBytesReader(w, r.Body, maxImportBytes))
	var cards []vcard.Card
	for {
		card, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_contacts", "The file is not a valid vCard (.vcf) file.")
			return nil, false
		}
		if len(cards) == maxImportObjects {
			writeError(w, http.StatusUnprocessableEntity, "too_many_contacts", "Import at most 5,000 contacts at a time.")
			return nil, false
		}
		cards = append(cards, card)
	}
	slug := r.URL.Query().Get("book")
	if slug == "" {
		slug = defaultAddressBook
	}
	book, err := s.writableCollection(ctx, "addressbook", slug)
	if err != nil {
		s.davError(w, r, err)
		return nil, false
	}
	var imported, updated, skipped int
	for _, card := range cards {
		if card.Value(vcard.FieldUID) == "" {
			card.SetValue(vcard.FieldUID, secrets.Token(16))
		}
		if card.Value(vcard.FieldVersion) == "" {
			card.SetValue(vcard.FieldVersion, "4.0")
		}
		fields, err := cardFields(card)
		var buf bytes.Buffer
		if err == nil {
			err = vcard.NewEncoder(&buf).Encode(card)
		}
		if err != nil {
			skipped++
			continue
		}
		var name string
		err = s.db.QueryRow(ctx, `SELECT name FROM dav_objects WHERE collection_id = $1 AND uid = $2`, book.ID, fields.UID).Scan(&name)
		exists := err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			name = fields.UID + ".vcf"
			if !objectNamePattern.MatchString(name) {
				name = secrets.Token(16) + ".vcf"
			}
		} else if err != nil {
			s.internalError(w, r, err)
			return nil, false
		}
		if _, err := s.putDAVObject(ctx, book.ID, name, buf.String(), fields, webdav.ConditionalMatch(""), webdav.ConditionalMatch("")); err != nil {
			skipped++
			continue
		}
		if exists {
			updated++
		} else {
			imported++
		}
	}
	counts := map[string]any{"imported": imported, "updated": updated, "skipped": skipped}
	writeJSON(w, http.StatusOK, counts)
	return counts, true
}
