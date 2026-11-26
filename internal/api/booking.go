package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/secrets"
)

const conflictHorizon = 366 * 24 * time.Hour

type resource struct {
	ID           string `json:"id"`
	OrgID        string `json:"orgId"`
	CalendarID   string `json:"-"`
	Address      string `json:"address"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Capacity     *int   `json:"capacity"`
	Location     string `json:"location"`
	Description  string `json:"description"`
	Policy       string `json:"policy"`
	BookableBy   string `json:"bookableBy"`
	WindowDays   int    `json:"windowDays"`
	MaxMinutes   *int   `json:"maxMinutes"`
	WorkingHours bool   `json:"workingHours"`
	WorkDays     []int  `json:"workDays"`
	WorkStart    int    `json:"workStart"`
	WorkEnd      int    `json:"workEnd"`
	TimeZone     string `json:"timeZone"`
}

const (
	resourceColumns = `m.id, m.org_id, c.id, m.address, m.display_name, r.resource_type, r.capacity, r.location, r.description, r.booking_policy, r.bookable_by,
		r.window_days, r.max_minutes, r.working_hours, r.work_days, r.work_start, r.work_end, r.time_zone`
	resourceFrom = ` FROM mailboxes m JOIN resources r ON r.mailbox_id = m.id JOIN domains d ON d.id = m.domain_id
		JOIN dav_collections c ON c.mailbox_id = m.id AND c.kind = 'calendar'`
	bookableResource = ` WHERE m.kind = 'resource' AND m.active AND d.active AND d.verified_at IS NOT NULL`
)

func (s *Server) loadResources(ctx context.Context, cond string, args ...any) ([]resource, error) {
	rows, err := s.db.Query(ctx, `SELECT `+resourceColumns+resourceFrom+bookableResource+cond, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[resource])
}

func (r *resource) location() *time.Location {
	loc, err := time.LoadLocation(r.TimeZone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (r *resource) horizon(now time.Time) time.Time {
	return now.Add(max(time.Duration(r.WindowDays)*24*time.Hour, conflictHorizon))
}

type span struct {
	Start, End time.Time
}

func (r *resource) span(e calendarEvent) span {
	if !e.AllDay {
		return span{e.Start, e.End}
	}
	start := time.Date(e.Start.Year(), e.Start.Month(), e.Start.Day(), 0, 0, 0, 0, r.location())
	return span{start, start.AddDate(0, 0, max(1, int(e.End.Sub(e.Start).Round(24*time.Hour)/(24*time.Hour))))}
}

func (r *resource) occurrences(cal *ical.Calendar, from, to time.Time) []span {
	var out []span
	for _, e := range expandObject("", cal, from, to) {
		out = append(out, r.span(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

var weekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func weekdaysLabel(days []int) string {
	sorted := slices.Clone(days)
	slices.SortFunc(sorted, func(a, b int) int { return (a+6)%7 - (b+6)%7 })
	if len(sorted) == 7 {
		return "every day"
	}
	contiguous := len(sorted) >= 3
	for i := 1; i < len(sorted) && contiguous; i++ {
		contiguous = (sorted[i-1]+6)%7+1 == (sorted[i]+6)%7
	}
	if contiguous {
		return weekdayNames[sorted[0]] + " to " + weekdayNames[sorted[len(sorted)-1]]
	}
	names := make([]string, len(sorted))
	for i, d := range sorted {
		names[i] = weekdayNames[d]
	}
	if len(names) == 1 {
		return "on " + names[0]
	}
	return "on " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func clockLabel(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

func durationLabel(minutes int) string {
	unit := func(n int, noun string) string {
		if n == 1 {
			return "1 " + noun
		}
		return fmt.Sprintf("%d %ss", n, noun)
	}
	switch {
	case minutes%1440 == 0:
		return unit(minutes/1440, "day")
	case minutes%60 == 0:
		return unit(minutes/60, "hour")
	case minutes > 60:
		return unit(minutes/60, "hour") + " " + unit(minutes%60, "minute")
	}
	return unit(minutes, "minute")
}

func (r *resource) spanLabel(o span) string {
	loc := r.location()
	start, end := o.Start.In(loc), o.End.In(loc)
	if start.YearDay() == end.YearDay() && start.Year() == end.Year() {
		return fmt.Sprintf("on %s from %s to %s (%s)", start.Format("Mon 2 Jan"), start.Format("15:04"), end.Format("15:04"), r.TimeZone)
	}
	return fmt.Sprintf("from %s to %s (%s)", start.Format("Mon 2 Jan 15:04"), end.Format("Mon 2 Jan 15:04"), r.TimeZone)
}

func (r *resource) withinHours(o span) bool {
	loc := r.location()
	start, end := o.Start.In(loc), o.End.In(loc)
	open := time.Date(start.Year(), start.Month(), start.Day(), 0, r.WorkStart, 0, 0, loc)
	closing := time.Date(start.Year(), start.Month(), start.Day(), 0, r.WorkEnd, 0, 0, loc)
	return slices.Contains(r.WorkDays, int(start.Weekday())) && !start.Before(open) && !end.After(closing)
}

func (r *resource) violation(occurrences []span, now time.Time) string {
	if len(occurrences) == 0 {
		return "This booking is in the past."
	}
	if occurrences[0].Start.After(now.AddDate(0, 0, r.WindowDays)) {
		return fmt.Sprintf("%s can be booked at most %s ahead.", r.Name, durationLabel(r.WindowDays*1440))
	}
	for _, o := range occurrences {
		if r.MaxMinutes != nil && o.End.Sub(o.Start) > time.Duration(*r.MaxMinutes)*time.Minute {
			return fmt.Sprintf("%s can be booked for at most %s at a time.", r.Name, durationLabel(*r.MaxMinutes))
		}
		if r.WorkingHours && !r.withinHours(o) {
			return fmt.Sprintf("%s can only be booked %s, %s to %s (%s).", r.Name, weekdaysLabel(r.WorkDays), clockLabel(r.WorkStart), clockLabel(r.WorkEnd), r.TimeZone)
		}
	}
	return ""
}

func firstConflict(occurrences, booked []span) *span {
	for _, o := range occurrences {
		for _, b := range booked {
			if o.Start.Before(b.End) && b.Start.Before(o.End) {
				return &b
			}
		}
	}
	return nil
}

func bookedSpans(ctx context.Context, db queryer, r *resource, from, to time.Time, excludeName string) ([]span, error) {
	events, err := eventsIn(ctx, db, []collectionRow{{ID: r.CalendarID}}, from, to)
	if err != nil {
		return nil, err
	}
	var out []span
	for _, e := range events {
		if e.ID != excludeName && !e.transparent {
			out = append(out, r.span(e))
		}
	}
	return out, nil
}

func conflictReason(ctx context.Context, db queryer, r *resource, occurrences []span, from, to time.Time, excludeName string) (string, error) {
	booked, err := bookedSpans(ctx, db, r, from, to, excludeName)
	if err != nil {
		return "", err
	}
	if c := firstConflict(occurrences, booked); c != nil {
		return fmt.Sprintf("%s is already booked %s.", r.Name, r.spanLabel(*c)), nil
	}
	return "", nil
}

func (s *Server) bookerStatus(ctx context.Context, r *resource, organizer string) (allowed, approver bool, err error) {
	var inOrg, listed bool
	err = s.db.QueryRow(ctx, `
		SELECT m.org_id = $2 OR EXISTS (SELECT 1 FROM organization_members om WHERE om.user_id = m.owner_user_id AND om.org_id = $2),
		       EXISTS (SELECT 1 FROM resource_members rm WHERE rm.mailbox_id = $3 AND rm.user_id = m.owner_user_id AND rm.role = 'booker'),
		       EXISTS (SELECT 1 FROM resource_members rm WHERE rm.mailbox_id = $3 AND rm.user_id = m.owner_user_id AND rm.role = 'approver')
		FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.address = $1 AND m.kind <> 'resource' AND m.active AND d.active AND d.verified_at IS NOT NULL`, organizer, r.OrgID, r.ID).Scan(&inOrg, &listed, &approver)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return approver || inOrg && (r.BookableBy == "organization" || listed), approver, err
}

func (r *resource) notAllowedReason() string {
	if r.BookableBy == "selected" {
		return "Only people chosen by an administrator can book " + r.Name + "."
	}
	return "Only people in its organization can book " + r.Name + "."
}

type bookingOutcome struct {
	Address string `json:"address"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
}

type storedBooking struct {
	Name, ETag string
	Cal        *ical.Calendar
}

func loadBooking(ctx context.Context, tx pgx.Tx, collectionID, uid string) (*storedBooking, error) {
	var b storedBooking
	var data string
	err := tx.QueryRow(ctx, `SELECT name, etag, data FROM dav_objects WHERE collection_id = $1 AND uid = $2`, collectionID, uid).Scan(&b.Name, &b.ETag, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if b.Cal, err = ical.NewDecoder(strings.NewReader(data)).Decode(); err != nil {
		return nil, err
	}
	return &b, nil
}

func cloneCalendar(cal *ical.Calendar) (*ical.Calendar, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, err
	}
	return ical.NewDecoder(&buf).Decode()
}

func bookingDocument(request *ical.Calendar, stored *storedBooking) (*ical.Calendar, error) {
	source, err := cloneCalendar(request)
	if err != nil {
		return nil, err
	}
	doc := newCalendar()
	if masterEvent(source).Props.Get(ical.PropRecurrenceID) == nil || stored == nil {
		for _, c := range source.Children {
			if c.Name == ical.CompEvent || c.Name == ical.CompTimezone {
				doc.Children = append(doc.Children, c)
			}
		}
		return doc, nil
	}
	replaced := map[string]bool{}
	timezones := map[string]bool{}
	for _, c := range source.Events() {
		replaced[c.Props.Get(ical.PropRecurrenceID).Value] = true
	}
	for _, c := range stored.Cal.Children {
		if c.Name == ical.CompTimezone {
			id, _ := c.Props.Text(ical.PropTimezoneID)
			timezones[id] = true
		}
		if rid := c.Props.Get(ical.PropRecurrenceID); c.Name != ical.CompEvent || rid == nil || !replaced[rid.Value] {
			doc.Children = append(doc.Children, c)
		}
	}
	for _, c := range source.Children {
		id, _ := c.Props.Text(ical.PropTimezoneID)
		if c.Name == ical.CompEvent || c.Name == ical.CompTimezone && !timezones[id] {
			doc.Children = append(doc.Children, c)
		}
	}
	return doc, nil
}

func cancelInstance(cal *ical.Calendar, comp *ical.Component) {
	rid := comp.Props.Get(ical.PropRecurrenceID).Value
	kept := cal.Children[:0]
	for _, c := range cal.Children {
		if p := c.Props.Get(ical.PropRecurrenceID); c.Name != ical.CompEvent || p == nil || p.Value != rid {
			kept = append(kept, c)
		}
	}
	comp.Props.SetText(ical.PropStatus, "CANCELLED")
	cal.Children = append(kept, comp)
}

func encodeCalendar(cal *ical.Calendar) (string, objectFields, error) {
	addTimezones(cal)
	f, err := calendarFields(cal)
	if err != nil {
		return "", f, err
	}
	var buf bytes.Buffer
	err = ical.NewEncoder(&buf).Encode(cal)
	return buf.String(), f, err
}

func bookingName(uid string) string {
	if name := uid + ".ics"; objectNamePattern.MatchString(name) {
		return name
	}
	return secrets.Token(16) + ".ics"
}

func lockResource(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM resources WHERE mailbox_id = $1 FOR UPDATE`, id)
	return err
}

func (s *Server) bookResource(ctx context.Context, r *resource, organizer mailpkg.Address, request *ical.Calendar) (bookingOutcome, *ical.Calendar, error) {
	out := bookingOutcome{Address: r.Address, Name: r.Name}
	comp := masterEvent(request)
	uid, _ := comp.Props.Text(ical.PropUID)
	allowed, approver, err := s.bookerStatus(ctx, r, organizer.Address)
	if err != nil {
		return out, nil, err
	}
	now := time.Now()
	horizon := r.horizon(now)
	var doc *ical.Calendar
	var pending bool
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockResource(ctx, tx, r.ID); err != nil {
			return err
		}
		stored, err := loadBooking(ctx, tx, r.CalendarID, uid)
		if err != nil {
			return err
		}
		if stored != nil && eventSequence(masterEvent(stored.Cal)) > eventSequence(comp) {
			out.Status = "accepted"
			doc = stored.Cal
			return nil
		}
		if doc, err = bookingDocument(request, stored); err != nil {
			return err
		}
		for _, c := range doc.Events() {
			setPartStat(c.Component, r.Address, "ACCEPTED")
		}
		occurrences := r.occurrences(doc, now, horizon)
		storedName := ""
		if stored != nil {
			storedName = stored.Name
		}
		switch {
		case !allowed:
			out.Reason = r.notAllowedReason()
		default:
			out.Reason = r.violation(occurrences, now)
		}
		if out.Reason == "" {
			if out.Reason, err = conflictReason(ctx, tx, r, occurrences, now, horizon, storedName); err != nil {
				return err
			}
		}
		if out.Reason != "" {
			out.Status = "declined"
			return dropBooking(ctx, tx, r, uid, storedName)
		}
		unchanged := stored != nil && slices.EqualFunc(r.occurrences(stored.Cal, now, horizon), occurrences, func(a, b span) bool { return a.Start.Equal(b.Start) && a.End.Equal(b.End) })
		if r.Policy == "approval" && !approver && !unchanged {
			out.Status, out.Reason, pending = "tentative", r.Name+" needs approval. You will get another reply once it is approved or declined.", true
			return queueRequest(ctx, tx, r, organizer, uid, doc, occurrences, storedName)
		}
		data, f, err := encodeCalendar(doc)
		if err != nil {
			return err
		}
		name := storedName
		if name == "" {
			name = bookingName(uid)
		}
		if _, err := putDAVObjectIn(ctx, tx, r.CalendarID, name, data, f, "", ""); err != nil {
			return err
		}
		out.Status = "accepted"
		_, err = tx.Exec(ctx, `DELETE FROM resource_requests WHERE mailbox_id = $1 AND uid = $2`, r.ID, uid)
		return err
	})
	if err == nil && pending {
		if err := s.notifyApprovers(ctx, r, organizer, masterEvent(doc)); err != nil {
			s.log.Warn("telling resource approvers failed", "resource", r.ID, "err", err)
		}
	}
	return out, doc, err
}

func dropBooking(ctx context.Context, tx pgx.Tx, r *resource, uid, storedName string) error {
	if storedName != "" {
		if _, err := tx.Exec(ctx, `DELETE FROM dav_objects WHERE collection_id = $1 AND name = $2`, r.CalendarID, storedName); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM resource_requests WHERE mailbox_id = $1 AND uid = $2`, r.ID, uid)
	return err
}

func queueRequest(ctx context.Context, tx pgx.Tx, r *resource, organizer mailpkg.Address, uid string, doc *ical.Calendar, occurrences []span, storedName string) error {
	data, f, err := encodeCalendar(doc)
	if err != nil {
		return err
	}
	if storedName != "" {
		if _, err := tx.Exec(ctx, `DELETE FROM dav_objects WHERE collection_id = $1 AND name = $2`, r.CalendarID, storedName); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO resource_requests (mailbox_id, uid, organizer, organizer_name, summary, starts_at, ends_at, recurring, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (mailbox_id, uid) DO UPDATE SET organizer = EXCLUDED.organizer, organizer_name = EXCLUDED.organizer_name, summary = EXCLUDED.summary,
			starts_at = EXCLUDED.starts_at, ends_at = EXCLUDED.ends_at, recurring = EXCLUDED.recurring, data = EXCLUDED.data, created_at = now()`,
		r.ID, uid, organizer.Address, organizer.Name, f.Summary, occurrences[0].Start, occurrences[0].End, f.Recurring || len(occurrences) > 1, data)
	return err
}

func (s *Server) cancelResourceBooking(ctx context.Context, r *resource, organizer string, request *ical.Calendar) error {
	comp := masterEvent(request)
	uid, _ := comp.Props.Text(ical.PropUID)
	instance := comp.Props.Get(ical.PropRecurrenceID) != nil
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockResource(ctx, tx, r.ID); err != nil {
			return err
		}
		stored, err := loadBooking(ctx, tx, r.CalendarID, uid)
		if err != nil {
			return err
		}
		if stored != nil && eventOrganizer(masterEvent(stored.Cal)).Address == organizer {
			if !instance {
				if _, err := tx.Exec(ctx, `DELETE FROM dav_objects WHERE collection_id = $1 AND name = $2`, r.CalendarID, stored.Name); err != nil {
					return err
				}
			} else {
				cancelled, err := cloneCalendar(request)
				if err != nil {
					return err
				}
				cancelInstance(stored.Cal, masterEvent(cancelled))
				data, f, err := encodeCalendar(stored.Cal)
				if err != nil {
					return err
				}
				if _, err := putDAVObjectIn(ctx, tx, r.CalendarID, stored.Name, data, f, "", ""); err != nil {
					return err
				}
			}
		}
		if instance {
			return nil
		}
		_, err = tx.Exec(ctx, `DELETE FROM resource_requests WHERE mailbox_id = $1 AND uid = $2 AND organizer = $3`, r.ID, uid, organizer)
		return err
	})
}

func (s *Server) applyResourceStatus(ctx context.Context, organizer, uid, address, partStat string, sequence int) (bool, error) {
	var userID string
	err := s.db.QueryRow(ctx, `SELECT owner_user_id FROM mailboxes WHERE address = $1 AND owner_user_id IS NOT NULL`, organizer).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stored, err := s.eventByUID(ctx, userID, uid)
	if err != nil || stored == nil {
		return false, err
	}
	master := masterEvent(stored.Cal)
	if eventOrganizer(master).Address != organizer || eventSequence(master) > sequence {
		return false, nil
	}
	found := false
	for _, c := range stored.Cal.Children {
		if c.Name == ical.CompEvent && setPartStat(c, address, partStat) {
			found = true
		}
	}
	if !found {
		return false, nil
	}
	return true, s.storeEvent(ctx, stored.CollectionID, stored.Name, stored.Cal, stored.ETag)
}

func (s *Server) answerFromResource(ctx context.Context, r *resource, organizer mailpkg.Address, comp *ical.Component, out bookingOutcome, announce bool) error {
	partStat := strings.ToUpper(out.Status)
	uid, _ := comp.Props.Text(ical.PropUID)
	applied, err := s.applyResourceStatus(ctx, organizer.Address, uid, r.Address, partStat, eventSequence(comp))
	if err != nil {
		s.log.Warn("updating the organizer's event failed", "resource", r.ID, "err", err)
	}
	if applied && partStat == "ACCEPTED" && !announce {
		return nil
	}
	reply := replyCopy(comp, mailpkg.Address{Name: r.Name, Address: r.Address}, partStat)
	if out.Reason != "" {
		reply.Props.SetText(ical.PropComment, out.Reason)
	}
	title, _ := comp.Props.Text(ical.PropSummary)
	_, err = s.sendITIP(ctx, &ownedMailbox{ID: r.ID, OrgID: r.OrgID, Address: r.Address, DisplayName: r.Name}, nil, []mailpkg.Address{organizer}, replyKinds[partStat], title, "REPLY", reply)
	return err
}

func (s *Server) notifyApprovers(ctx context.Context, r *resource, organizer mailpkg.Address, comp *ical.Component) error {
	rows, err := s.db.Query(ctx, `
		SELECT u.display_name, u.email FROM resource_members rm JOIN users u ON u.id = rm.user_id
		WHERE rm.mailbox_id = $1 AND rm.role = 'approver' AND u.status = 'active' ORDER BY u.email`, r.ID)
	if err != nil {
		return err
	}
	approvers, err := pgx.CollectRows(rows, pgx.RowToStructByPos[mailpkg.Address])
	if err != nil || len(approvers) == 0 {
		return err
	}
	title, _ := comp.Props.Text(ical.PropSummary)
	link := strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/calendar?requests=1"
	out := &mailpkg.Outgoing{
		From:    mailpkg.Address{Name: r.Name, Address: r.Address},
		To:      approvers,
		Subject: "Approval needed: " + r.Name + " for " + title,
		Text:    fmt.Sprintf("%s asked to book %s.\r\n\r\nApprove or decline the request in Aether Calendar:\r\n%s\r\n\r\n", firstNonEmpty(organizer.Name, organizer.Address), r.Name, link) + describeEvent(comp, "en"),
	}
	raw, _, err := out.Build(s.cfg.MailHostname)
	if err != nil {
		return err
	}
	return s.mail.Send(out.From.Address, out.From.Address, out.Recipients(), raw)
}

func (s *Server) resourceKinds(ctx context.Context, addresses []string) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `SELECT m.address, r.resource_type FROM mailboxes m JOIN resources r ON r.mailbox_id = m.id WHERE m.address = ANY($1)`, addresses)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	var address, kind string
	_, err = pgx.ForEachRow(rows, []any{&address, &kind}, func() error {
		out[address] = kind
		return nil
	})
	return out, err
}

var resourceUserTypes = map[string]string{"room": "ROOM", "equipment": "RESOURCE"}

func (s *Server) deliverToResources(ctx context.Context, method string, to []mailpkg.Address, comps []*ical.Component) ([]mailpkg.Address, []bookingOutcome, error) {
	if method != "REQUEST" && method != "CANCEL" || len(to) == 0 {
		return to, nil, nil
	}
	addresses := make([]string, len(to))
	for i, a := range to {
		addresses[i] = strings.ToLower(a.Address)
	}
	list, err := s.loadResources(ctx, ` AND m.address = ANY($1)`, addresses)
	if err != nil || len(list) == 0 {
		return to, nil, err
	}
	byAddress := map[string]*resource{}
	for i := range list {
		byAddress[list[i].Address] = &list[i]
	}
	organizer := eventOrganizer(comps[0])
	request := newCalendar()
	request.Children = comps
	var people []mailpkg.Address
	var outcomes []bookingOutcome
	for _, a := range to {
		r := byAddress[strings.ToLower(a.Address)]
		switch {
		case r == nil:
			people = append(people, a)
		case method == "CANCEL":
			if err := s.cancelResourceBooking(ctx, r, organizer.Address, request); err != nil {
				return nil, nil, err
			}
		default:
			out, _, err := s.bookResource(ctx, r, organizer, request)
			if err != nil {
				return nil, nil, err
			}
			for _, c := range comps {
				setPartStat(c, r.Address, strings.ToUpper(out.Status))
			}
			if err := s.answerFromResource(ctx, r, organizer, comps[0], out, false); err != nil {
				s.log.Warn("resource reply failed", "resource", r.ID, "err", err)
			}
			outcomes = append(outcomes, out)
		}
	}
	return people, outcomes, nil
}

func (s *Server) RunResourceWorker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.processResourceInboxes(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) processResourceInboxes(ctx context.Context) {
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		s.log.Error("reading resource inboxes failed", "err", err)
		return
	}
	defer conn.Release()
	var leader bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(7316254)`).Scan(&leader); err != nil || !leader {
		return
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(7316254)`)
	if _, err := s.db.Exec(ctx, `DELETE FROM resource_requests WHERE NOT recurring AND ends_at < now()`); err != nil {
		s.log.Error("removing expired booking requests failed", "err", err)
	}
	list, err := s.loadResources(ctx, ``)
	if err != nil {
		s.log.Error("loading resources failed", "err", err)
		return
	}
	for i := range list {
		r := &list[i]
		messages, err := s.mail.UnseenCalendars(r.Address, 25)
		if err != nil {
			s.log.Warn("reading a resource inbox failed", "resource", r.ID, "err", err)
			continue
		}
		for _, m := range messages {
			if err := s.processResourceMessage(ctx, r, m); err != nil {
				s.log.Warn("processing a booking request failed; retrying", "resource", r.ID, "uid", m.UID, "err", err)
				continue
			}
			seen := true
			if err := s.mail.SetFlags(r.Address, "INBOX", m.UID, &seen, nil); err != nil {
				s.log.Warn("marking a booking request as processed failed", "resource", r.ID, "err", err)
			}
		}
	}
}

func (s *Server) processResourceMessage(ctx context.Context, r *resource, m mailpkg.CalendarMessage) error {
	request, comp, method := parseInvitation(m.Calendar)
	if comp == nil || method == "REPLY" {
		return nil
	}
	organizer := eventOrganizer(comp)
	if organizer.Address == "" || organizer.Address != m.From || organizer.Address != m.ReturnPath || attendeeStatus(comp, r.Address) == "" && method == "REQUEST" {
		s.log.Info("ignoring a calendar message the resource cannot trust", "resource", r.ID, "from", m.From, "organizer", organizer.Address)
		return nil
	}
	if method == "CANCEL" {
		return s.cancelResourceBooking(ctx, r, organizer.Address, request)
	}
	out, _, err := s.bookResource(ctx, r, organizer, request)
	if err != nil {
		return err
	}
	return s.answerFromResource(ctx, r, organizer, comp, out, true)
}
