package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

type resourceInput struct {
	DisplayName  *string  `json:"displayName"`
	Type         string   `json:"type"`
	Capacity     *int     `json:"capacity"`
	Location     string   `json:"location"`
	Description  string   `json:"description"`
	Policy       string   `json:"policy"`
	BookableBy   string   `json:"bookableBy"`
	WindowDays   int      `json:"windowDays"`
	MaxMinutes   *int     `json:"maxMinutes"`
	WorkingHours bool     `json:"workingHours"`
	WorkDays     []int    `json:"workDays"`
	WorkStart    int      `json:"workStart"`
	WorkEnd      int      `json:"workEnd"`
	TimeZone     string   `json:"timeZone"`
	Approvers    []string `json:"approvers"`
	Bookers      []string `json:"bookers"`
}

func userIDs(list []string) ([]string, bool) {
	out := []string{}
	for _, id := range list {
		id = strings.ToLower(strings.TrimSpace(id))
		if !uuidPattern.MatchString(id) {
			return nil, false
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, true
}

func (in *resourceInput) normalize() (string, string) {
	in.Location, in.Description = strings.TrimSpace(in.Location), strings.TrimSpace(in.Description)
	in.Policy, in.BookableBy, in.TimeZone = cmp.Or(in.Policy, "auto"), cmp.Or(in.BookableBy, "organization"), cmp.Or(in.TimeZone, "UTC")
	in.WindowDays = cmp.Or(in.WindowDays, 180)
	if len(in.WorkDays) == 0 {
		in.WorkDays = []int{1, 2, 3, 4, 5}
	}
	if in.WorkStart == 0 && in.WorkEnd == 0 {
		in.WorkStart, in.WorkEnd = 480, 1080
	}
	slices.Sort(in.WorkDays)
	in.WorkDays = slices.Compact(in.WorkDays)
	var approversOK, bookersOK bool
	in.Approvers, approversOK = userIDs(in.Approvers)
	in.Bookers, bookersOK = userIDs(in.Bookers)
	_, zoneErr := time.LoadLocation(in.TimeZone)
	switch {
	case in.Type != "room" && in.Type != "equipment":
		return "invalid_type", "Choose whether this is a room or equipment."
	case in.Capacity != nil && (*in.Capacity < 1 || *in.Capacity > 10000):
		return "invalid_capacity", "Enter a capacity from 1 to 10,000 people, or leave it empty."
	case len(in.Location) > 200:
		return "invalid_location", "Keep the location to 200 characters."
	case len(in.Description) > 2000:
		return "invalid_description", "Keep the description to 2,000 characters."
	case in.Policy != "auto" && in.Policy != "approval":
		return "invalid_policy", "Bookings are either accepted automatically or approved by someone."
	case !approversOK || in.Policy == "approval" && len(in.Approvers) == 0:
		return "invalid_approvers", "Add at least one approver, or accept bookings automatically."
	case in.BookableBy != "organization" && in.BookableBy != "selected":
		return "invalid_bookable_by", "Choose who can book: everyone in the organization or selected people."
	case !bookersOK:
		return "invalid_bookers", "Choose people from this organization."
	case in.WindowDays < 1 || in.WindowDays > 1095:
		return "invalid_window", "Allow booking from 1 to 1,095 days ahead."
	case in.MaxMinutes != nil && (*in.MaxMinutes < 15 || *in.MaxMinutes > 20160):
		return "invalid_duration", "Set a maximum length from 15 minutes to 14 days, or no limit."
	case in.WorkDays[0] < 0 || in.WorkDays[len(in.WorkDays)-1] > 6:
		return "invalid_hours", "Choose working days from Monday to Sunday."
	case in.WorkStart < 0 || in.WorkEnd > 1440 || in.WorkEnd <= in.WorkStart:
		return "invalid_hours", "Working hours must end after they start, within the same day."
	case zoneErr != nil || in.TimeZone == "Local":
		return "invalid_time_zone", "Choose a time zone such as Europe/Berlin."
	}
	return "", ""
}

func (s *Server) validResource(ctx context.Context, w http.ResponseWriter, r *http.Request, orgID string, in *resourceInput) bool {
	if code, message := in.normalize(); code != "" {
		writeError(w, http.StatusUnprocessableEntity, code, message)
		return false
	}
	var members int
	people := append(slices.Clone(in.Approvers), in.Bookers...)
	slices.Sort(people)
	people = slices.Compact(people)
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM organization_members WHERE org_id = $1 AND user_id::text = ANY($2)`, orgID, people).Scan(&members); err != nil {
		s.internalError(w, r, err)
		return false
	}
	if members != len(people) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_people", "Approvers and people who can book must be members of this organization.")
		return false
	}
	return true
}

func saveResource(ctx context.Context, tx pgx.Tx, mailboxID string, in *resourceInput) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO resources (mailbox_id, resource_type, capacity, location, description, booking_policy, bookable_by, window_days, max_minutes, working_hours, work_days, work_start, work_end, time_zone)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (mailbox_id) DO UPDATE SET resource_type = EXCLUDED.resource_type, capacity = EXCLUDED.capacity, location = EXCLUDED.location,
			description = EXCLUDED.description, booking_policy = EXCLUDED.booking_policy, bookable_by = EXCLUDED.bookable_by, window_days = EXCLUDED.window_days,
			max_minutes = EXCLUDED.max_minutes, working_hours = EXCLUDED.working_hours, work_days = EXCLUDED.work_days, work_start = EXCLUDED.work_start,
			work_end = EXCLUDED.work_end, time_zone = EXCLUDED.time_zone, updated_at = now()`,
		mailboxID, in.Type, in.Capacity, in.Location, in.Description, in.Policy, in.BookableBy, in.WindowDays, in.MaxMinutes, in.WorkingHours, in.WorkDays, in.WorkStart, in.WorkEnd, in.TimeZone)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM resource_members WHERE mailbox_id = $1`, mailboxID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO resource_members (mailbox_id, user_id, role)
		SELECT $1::uuid, unnest($2::uuid[]), 'approver' UNION ALL SELECT $1::uuid, unnest($3::uuid[]), 'booker'`, mailboxID, in.Approvers, in.Bookers); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO dav_collections (mailbox_id, kind, slug, name) VALUES ($1, 'calendar', 'bookings', 'Bookings')
		ON CONFLICT (mailbox_id, kind, slug) WHERE mailbox_id IS NOT NULL DO NOTHING`, mailboxID)
	return err
}

type adminResource struct {
	resource
	DomainID  string   `json:"domainId"`
	LocalPart string   `json:"localPart"`
	Active    bool     `json:"active"`
	Approvers []string `json:"approvers"`
	Bookers   []string `json:"bookers"`
}

func (s *Server) listResources(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+resourceColumns+`, m.domain_id, m.local_part, m.active,
		coalesce((SELECT array_agg(rm.user_id::text ORDER BY rm.user_id) FROM resource_members rm WHERE rm.mailbox_id = m.id AND rm.role = 'approver'), '{}'),
		coalesce((SELECT array_agg(rm.user_id::text ORDER BY rm.user_id) FROM resource_members rm WHERE rm.mailbox_id = m.id AND rm.role = 'booker'), '{}')`+resourceFrom+`
		WHERE m.org_id = $1 AND m.kind = 'resource' ORDER BY r.resource_type = 'equipment', lower(m.display_name), m.address`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[adminResource])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) updateResource(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForAdmin(w, r, p)
	if !ok {
		return
	}
	if m.Kind != "resource" {
		notFound(w)
		return
	}
	var in resourceInput
	if !decode(w, r, 32<<10, &in) {
		return
	}
	if in.DisplayName == nil || strings.TrimSpace(*in.DisplayName) == "" || len(strings.TrimSpace(*in.DisplayName)) > 200 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", "Give the room or equipment a name of up to 200 characters.")
		return
	}
	if !s.validResource(r.Context(), w, r, m.OrgID, &in) {
		return
	}
	name := strings.TrimSpace(*in.DisplayName)
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE mailboxes SET display_name = $2 WHERE id = $1`, m.ID, name); err != nil {
			return err
		}
		return saveResource(r.Context(), tx, m.ID, &in)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "resource.update", "mailbox", m.ID, map[string]any{"address": m.Address, "name": name, "type": in.Type, "policy": in.Policy, "bookableBy": in.BookableBy})
	w.WriteHeader(http.StatusNoContent)
}

type resourceChoice struct {
	resource
	Bookable     bool   `json:"bookable"`
	Availability string `json:"availability,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

func (r *resource) conflictLabel(c span) string {
	return fmt.Sprintf("%s is already booked %s.", r.Name, r.spanLabel(c))
}

func (s *Server) findResources(w http.ResponseWriter, r *http.Request, p *principal) {
	q := r.URL.Query()
	var start, end time.Time
	timed := q.Get("start") != "" || q.Get("end") != ""
	if timed {
		var err1, err2 error
		start, err1 = time.Parse(time.RFC3339, q.Get("start"))
		end, err2 = time.Parse(time.RFC3339, q.Get("end"))
		if err1 != nil || err2 != nil || !end.After(start) || end.Sub(start) > 31*24*time.Hour {
			writeError(w, http.StatusUnprocessableEntity, "invalid_range", "Give start and end as RFC 3339 times, at most 31 days apart.")
			return
		}
	}
	list, err := s.loadResources(r.Context(), ` AND m.org_id IN (SELECT org_id FROM organization_members WHERE user_id = $1) ORDER BY r.resource_type = 'equipment', lower(m.display_name), m.address`, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	roles := map[string][]string{}
	rows, err := s.db.Query(r.Context(), `SELECT mailbox_id::text, role FROM resource_members WHERE user_id = $1`, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var id, role string
	if _, err := pgx.ForEachRow(rows, []any{&id, &role}, func() error {
		roles[id] = append(roles[id], role)
		return nil
	}); err != nil {
		s.internalError(w, r, err)
		return
	}
	organizer, err := s.organizerMailbox(r.Context(), p.User)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	busy := map[string][]span{}
	if timed && len(list) > 0 {
		if busy, err = s.resourceBusy(r.Context(), p.User.ID, list, q.Get("exclude"), start, end); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	now := time.Now()
	out := make([]resourceChoice, len(list))
	for i := range list {
		res := &list[i]
		c := resourceChoice{resource: *res}
		switch {
		case organizer == nil:
			c.Reason = "You need a mailbox of your own to book rooms and equipment."
		case !slices.Contains(roles[res.ID], "approver") && res.BookableBy == "selected" && !slices.Contains(roles[res.ID], "booker"):
			c.Reason = res.notAllowedReason()
		default:
			c.Bookable = true
		}
		if timed {
			var occurrences []span
			if end.After(now) {
				occurrences = []span{{start, end}}
			}
			switch {
			case !c.Bookable:
				c.Availability = "restricted"
			case res.violation(occurrences, now) != "":
				c.Availability, c.Reason = "restricted", res.violation(occurrences, now)
			case firstConflict(occurrences, busy[res.ID]) != nil:
				c.Availability, c.Reason = "busy", res.conflictLabel(*firstConflict(occurrences, busy[res.ID]))
			default:
				c.Availability = "free"
			}
		}
		out[i] = c
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) resourceBusy(ctx context.Context, userID string, list []resource, exclude string, start, end time.Time) (map[string][]span, error) {
	collections := make([]collectionRow, len(list))
	byID := map[string]*resource{}
	ids := make([]string, len(list))
	for i := range list {
		collections[i], ids[i] = collectionRow{ID: list[i].CalendarID, Slug: list[i].ID}, list[i].CalendarID
		byID[list[i].ID] = &list[i]
	}
	excluded := map[string]bool{}
	if exclude != "" {
		rows, err := s.db.Query(ctx, `
			SELECT o.collection_id::text || '/' || o.name FROM dav_objects o
			WHERE o.collection_id = ANY($2) AND o.uid IN (
				SELECT e.uid FROM dav_objects e JOIN dav_collections c ON c.id = e.collection_id WHERE c.user_id = $1 AND c.kind = 'calendar' AND e.name = $3)`, userID, ids, exclude)
		if err != nil {
			return nil, err
		}
		keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			excluded[k] = true
		}
	}
	events, err := eventsIn(ctx, s.db, collections, start, end)
	if err != nil {
		return nil, err
	}
	out := map[string][]span{}
	for _, e := range events {
		res := byID[e.CalendarID]
		if res != nil && !e.transparent && !excluded[res.CalendarID+"/"+e.ID] {
			out[res.ID] = append(out[res.ID], res.span(e))
		}
	}
	return out, nil
}

type bookingRequest struct {
	ID          string          `json:"id"`
	Resource    bookingSubject  `json:"resource"`
	Title       string          `json:"title"`
	Start       time.Time       `json:"start"`
	End         time.Time       `json:"end"`
	AllDay      bool            `json:"allDay"`
	Repeat      string          `json:"repeat"`
	Location    string          `json:"location"`
	Organizer   mailpkg.Address `json:"organizer"`
	Conflict    string          `json:"conflict,omitempty"`
	RequestedAt time.Time       `json:"requestedAt"`
}

type bookingSubject struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Type    string `json:"type"`
}

func (s *Server) listBookingRequests(w http.ResponseWriter, r *http.Request, p *principal) {
	rows, err := s.db.Query(r.Context(), `
		SELECT q.id, q.mailbox_id, q.organizer, q.organizer_name, q.data, q.created_at FROM resource_requests q
		WHERE (q.recurring OR q.ends_at > now())
		  AND EXISTS (SELECT 1 FROM resource_members rm WHERE rm.mailbox_id = q.mailbox_id AND rm.user_id = $1 AND rm.role = 'approver')
		ORDER BY q.starts_at LIMIT 200`, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	type row struct {
		ID, MailboxID, Organizer, OrganizerName, Data string
		CreatedAt                                     time.Time
	}
	queued, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ids := make([]string, len(queued))
	for i, q := range queued {
		ids[i] = q.MailboxID
	}
	list, err := s.loadResources(r.Context(), ` AND m.id = ANY($1)`, ids)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	byID := map[string]*resource{}
	for i := range list {
		byID[list[i].ID] = &list[i]
	}
	now := time.Now()
	out := []bookingRequest{}
	for _, q := range queued {
		res := byID[q.MailboxID]
		doc, err := ical.NewDecoder(strings.NewReader(q.Data)).Decode()
		if res == nil || err != nil || masterEvent(doc) == nil {
			continue
		}
		occurrences := res.occurrences(doc, now, res.horizon(now))
		ev, _, err := eventFromComponent("", masterEvent(doc))
		if err != nil || len(occurrences) == 0 {
			continue
		}
		conflict, err := conflictReason(r.Context(), s.db, res, occurrences, now, res.horizon(now), "")
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out = append(out, bookingRequest{
			ID: q.ID, Resource: bookingSubject{ID: res.ID, Name: res.Name, Address: res.Address, Type: res.Type},
			Title: ev.Title, Start: occurrences[0].Start, End: occurrences[0].End, AllDay: ev.AllDay, Repeat: ev.Repeat, Location: ev.Location,
			Organizer: mailpkg.Address{Name: q.OrganizerName, Address: q.Organizer}, Conflict: conflict, RequestedAt: q.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

var errRequestHandled = errors.New("the booking request was already handled")

func (s *Server) decideBookingRequest(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "requestId")
	if !ok {
		return
	}
	var in struct {
		Decision string `json:"decision"`
		Comment  string `json:"comment"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	in.Comment = strings.TrimSpace(in.Comment)
	if in.Decision != "approve" && in.Decision != "decline" || len(in.Comment) > 500 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_decision", "Approve or decline the request, with an optional message of up to 500 characters.")
		return
	}
	var mailboxID string
	err := s.db.QueryRow(r.Context(), `
		SELECT q.mailbox_id FROM resource_requests q WHERE q.id = $1
		  AND EXISTS (SELECT 1 FROM resource_members rm WHERE rm.mailbox_id = q.mailbox_id AND rm.user_id = $2 AND rm.role = 'approver')`, id, p.User.ID).Scan(&mailboxID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	list, err := s.loadResources(r.Context(), ` AND m.id = $1`, mailboxID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(list) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "resource_inactive", "This room or equipment is turned off. Turn it back on under Administration to handle its requests.")
		return
	}
	res := &list[0]
	var organizer mailpkg.Address
	var doc *ical.Calendar
	var uid, conflict string
	now := time.Now()
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if err := lockResource(r.Context(), tx, res.ID); err != nil {
			return err
		}
		var data string
		err := tx.QueryRow(r.Context(), `DELETE FROM resource_requests WHERE id = $1 RETURNING uid, organizer, organizer_name, data`, id).Scan(&uid, &organizer.Address, &organizer.Name, &data)
		if errors.Is(err, pgx.ErrNoRows) {
			return errRequestHandled
		}
		if err != nil {
			return err
		}
		if doc, err = ical.NewDecoder(strings.NewReader(data)).Decode(); err != nil {
			return err
		}
		if in.Decision == "decline" {
			return nil
		}
		stored, err := loadBooking(r.Context(), tx, res.CalendarID, uid)
		if err != nil {
			return err
		}
		name := bookingName(uid)
		if stored != nil {
			name = stored.Name
		}
		occurrences := res.occurrences(doc, now, res.horizon(now))
		if conflict, err = conflictReason(r.Context(), tx, res, occurrences, now, res.horizon(now), name); err != nil || conflict != "" {
			if err == nil {
				err = errRequestHandled
			}
			return err
		}
		body, f, err := encodeCalendar(doc)
		if err != nil {
			return err
		}
		_, err = putDAVObjectIn(r.Context(), tx, res.CalendarID, name, body, f, "", "")
		return err
	})
	switch {
	case conflict != "":
		writeError(w, http.StatusConflict, "conflict", conflict+" Decline the request, or ask the organizer to choose another time.")
		return
	case errors.Is(err, errRequestHandled):
		writeError(w, http.StatusConflict, "already_handled", "Someone else already approved or declined this request.")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	out := bookingOutcome{Address: res.Address, Name: res.Name, Status: "accepted"}
	if in.Decision == "decline" {
		out.Status, out.Reason = "declined", cmp.Or(in.Comment, "The request was declined by "+firstNonEmpty(p.User.DisplayName, p.User.Email)+".")
	}
	title, _ := masterEvent(doc).Props.Text(ical.PropSummary)
	s.audit(r.Context(), r, p, &res.OrgID, "resource.booking_"+in.Decision, "mailbox", res.ID, map[string]any{"address": res.Address, "organizer": organizer.Address, "title": title, "uid": uid})
	if err := s.answerFromResource(r.Context(), res, organizer, masterEvent(doc), out, true); err != nil {
		s.log.Warn("telling the organizer about a booking decision failed", "resource", res.ID, "err", err)
		writeError(w, http.StatusBadGateway, "send_failed", "The decision was saved, but the organizer could not be told: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
