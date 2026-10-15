package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/secrets"
)

const maxAttendees = 100

var partStats = map[string]string{"accepted": "ACCEPTED", "tentative": "TENTATIVE", "declined": "DECLINED"}

var replyVerbs = map[string]string{"ACCEPTED": "Accepted", "TENTATIVE": "Tentatively accepted", "DECLINED": "Declined"}

type eventAttendee struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
	Status  string `json:"status"`
}

func calAddress(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 7 && strings.EqualFold(v[:7], "mailto:") {
		v = v[7:]
	}
	return strings.ToLower(v)
}

func eventOrganizer(comp *ical.Component) mailpkg.Address {
	p := comp.Props.Get(ical.PropOrganizer)
	if p == nil {
		return mailpkg.Address{}
	}
	return mailpkg.Address{Name: p.Params.Get(ical.ParamCommonName), Address: calAddress(p.Value)}
}

func eventAttendees(comp *ical.Component) []eventAttendee {
	out := []eventAttendee{}
	for _, p := range comp.Props.Values(ical.PropAttendee) {
		status := strings.ToLower(p.Params.Get(ical.ParamParticipationStatus))
		if status == "" {
			status = "needs-action"
		}
		out = append(out, eventAttendee{Name: p.Params.Get(ical.ParamCommonName), Address: calAddress(p.Value), Status: status})
	}
	return out
}

func attendeeProp(a mailpkg.Address, partStat string, rsvp bool) ical.Prop {
	p := ical.Prop{Name: ical.PropAttendee, Params: ical.Params{}, Value: "mailto:" + a.Address}
	if a.Name != "" {
		p.Params.Set(ical.ParamCommonName, a.Name)
	}
	p.Params.Set(ical.ParamRole, "REQ-PARTICIPANT")
	p.Params.Set(ical.ParamParticipationStatus, partStat)
	if rsvp {
		p.Params.Set(ical.ParamRSVP, "TRUE")
	}
	return p
}

func organizerProp(a mailpkg.Address) *ical.Prop {
	p := &ical.Prop{Name: ical.PropOrganizer, Params: ical.Params{}, Value: "mailto:" + a.Address}
	if a.Name != "" {
		p.Params.Set(ical.ParamCommonName, a.Name)
	}
	return p
}

func eventSequence(comp *ical.Component) int {
	if p := comp.Props.Get(ical.PropSequence); p != nil {
		n, _ := strconv.Atoi(p.Value)
		return n
	}
	return 0
}

func setPartStat(comp *ical.Component, address, partStat string) bool {
	found := false
	for i, p := range comp.Props[ical.PropAttendee] {
		if calAddress(p.Value) == address {
			comp.Props[ical.PropAttendee][i].Params.Set(ical.ParamParticipationStatus, partStat)
			comp.Props[ical.PropAttendee][i].Params.Del(ical.ParamRSVP)
			found = true
		}
	}
	return found
}

func parseAttendees(list []string, organizer string) ([]mailpkg.Address, bool) {
	seen := map[string]bool{organizer: true}
	out := []mailpkg.Address{}
	for _, raw := range list {
		a, err := mail.ParseAddress(strings.TrimSpace(raw))
		if err != nil {
			return nil, false
		}
		address := strings.ToLower(a.Address)
		if !seen[address] {
			seen[address] = true
			out = append(out, mailpkg.Address{Name: a.Name, Address: address})
		}
	}
	return out, len(out) <= maxAttendees
}

func (s *Server) organizerMailbox(ctx context.Context, user User) (*ownedMailbox, error) {
	m := &ownedMailbox{}
	err := s.db.QueryRow(ctx, `
		SELECT m.id, m.org_id, m.address, m.display_name FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.owner_user_id = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL
		ORDER BY lower(m.address) = lower($2) DESC, m.created_at LIMIT 1`, user.ID, user.Email).
		Scan(&m.ID, &m.OrgID, &m.Address, &m.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

func describeEvent(comp *ical.Component) string {
	ev, _, _ := eventFromComponent("", comp)
	when := ev.Start.Format("Monday, 2 January 2006 15:04") + " – " + ev.End.Format("15:04 MST")
	if ev.AllDay {
		when = ev.Start.Format("Monday, 2 January 2006")
	}
	lines := []string{ev.Title, "", "When: " + when}
	if ev.Location != "" {
		lines = append(lines, "Where: "+ev.Location)
	}
	if org := eventOrganizer(comp); org.Address != "" {
		lines = append(lines, "Organizer: "+firstNonEmpty(org.Name, org.Address))
	}
	if ev.Description != "" {
		lines = append(lines, "", ev.Description)
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

func (s *Server) sendITIP(from *ownedMailbox, to []mailpkg.Address, subject, method string, comps ...*ical.Component) error {
	if len(to) == 0 {
		return nil
	}
	cal := newCalendar()
	cal.Props.SetText(ical.PropMethod, method)
	cal.Children = append(cal.Children, comps...)
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return err
	}
	out := &mailpkg.Outgoing{
		From:     mailpkg.Address{Name: from.DisplayName, Address: from.Address},
		To:       to,
		Subject:  subject,
		Text:     describeEvent(comps[0]),
		Calendar: &mailpkg.CalendarPart{Method: method, Data: buf.String()},
	}
	raw, _, err := out.Build(s.cfg.MailHostname)
	if err != nil {
		return err
	}
	return s.mail.Send(out.From, out.Recipients(), raw)
}

func cancelledCopy(comp *ical.Component, attendees []mailpkg.Address, sequence int) *ical.Component {
	c := ical.NewComponent(ical.CompEvent)
	for _, name := range []string{ical.PropUID, ical.PropOrganizer, ical.PropSummary, ical.PropDateTimeStart, ical.PropDateTimeEnd, ical.PropRecurrenceID} {
		if p := comp.Props.Get(name); p != nil {
			c.Props.Set(p)
		}
	}
	c.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	c.Props.SetText(ical.PropSequence, strconv.Itoa(sequence))
	c.Props.SetText(ical.PropStatus, "CANCELLED")
	for _, a := range attendees {
		c.Props.Add(&ical.Prop{Name: ical.PropAttendee, Params: ical.Params{}, Value: "mailto:" + a.Address})
	}
	return c
}

func replyCopy(comp *ical.Component, me mailpkg.Address, partStat string) *ical.Component {
	c := ical.NewComponent(ical.CompEvent)
	for _, name := range []string{ical.PropUID, ical.PropOrganizer, ical.PropSummary, ical.PropDateTimeStart, ical.PropDateTimeEnd, ical.PropRecurrenceID, ical.PropSequence} {
		if p := comp.Props.Get(name); p != nil {
			c.Props.Set(p)
		}
	}
	c.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	p := attendeeProp(me, partStat, false)
	p.Params.Del(ical.ParamRole)
	c.Props.Add(&p)
	return c
}

func masterEvent(cal *ical.Calendar) *ical.Component {
	var first *ical.Component
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent {
			continue
		}
		if comp.Props.Get(ical.PropRecurrenceID) == nil {
			return comp
		}
		if first == nil {
			first = comp
		}
	}
	return first
}

type storedEvent struct {
	CollectionID, Name, ETag string
	Cal                      *ical.Calendar
}

func (s *Server) eventByUID(ctx context.Context, userID, uid string) (*storedEvent, error) {
	var e storedEvent
	var data string
	err := s.db.QueryRow(ctx, `
		SELECT c.id, o.name, o.etag, o.data FROM dav_objects o JOIN dav_collections c ON c.id = o.collection_id
		WHERE c.user_id = $1 AND c.kind = 'calendar' AND o.uid = $2 ORDER BY c.slug = 'personal' DESC LIMIT 1`, userID, uid).Scan(&e.CollectionID, &e.Name, &e.ETag, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if e.Cal, err = ical.NewDecoder(strings.NewReader(data)).Decode(); err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Server) storeEvent(ctx context.Context, collectionID, name string, cal *ical.Calendar, ifMatch string) error {
	f, err := calendarFields(cal)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return err
	}
	match := webdav.ConditionalMatch("")
	if ifMatch != "" {
		match = webdav.ConditionalMatch(`"` + ifMatch + `"`)
	}
	_, err = s.putDAVObject(ctx, collectionID, name, buf.String(), f, match, "")
	return err
}

type invitation struct {
	Method    string          `json:"method"`
	Event     calendarEvent   `json:"event"`
	Organizer mailpkg.Address `json:"organizer"`
	Status    string          `json:"status"`
	EventID   string          `json:"eventId,omitempty"`
	Applied   bool            `json:"applied"`
}

func parseInvitation(data string) (*ical.Calendar, *ical.Component, string) {
	cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		return nil, nil, ""
	}
	method, _ := cal.Props.Text(ical.PropMethod)
	method = strings.ToUpper(method)
	comp := masterEvent(cal)
	if comp == nil || (method != "REQUEST" && method != "REPLY" && method != "CANCEL") {
		return nil, nil, ""
	}
	if uid, _ := comp.Props.Text(ical.PropUID); uid == "" {
		return nil, nil, ""
	}
	return cal, comp, method
}

func attendeeStatus(comp *ical.Component, address string) string {
	for _, a := range eventAttendees(comp) {
		if a.Address == address {
			return a.Status
		}
	}
	return ""
}

func (s *Server) describeInvitation(ctx context.Context, user User, mailbox *ownedMailbox, from string, data string) *invitation {
	_, comp, method := parseInvitation(data)
	if comp == nil {
		return nil
	}
	uid, _ := comp.Props.Text(ical.PropUID)
	ev, _, err := eventFromComponent("", comp)
	if err != nil {
		return nil
	}
	ev.Attendees = eventAttendees(comp)
	ev.Organizer = eventOrganizer(comp).Address
	out := &invitation{Method: strings.ToLower(method), Event: ev, Organizer: eventOrganizer(comp)}
	me := strings.ToLower(mailbox.Address)
	switch method {
	case "REQUEST":
		out.Status = attendeeStatus(comp, me)
	case "REPLY":
		out.Status = attendeeStatus(comp, from)
	}
	stored, err := s.eventByUID(ctx, user.ID, uid)
	if err != nil {
		s.log.Error("looking up invited event failed", "user", user.ID, "err", err)
	}
	if stored == nil {
		return out
	}
	out.EventID = stored.Name
	master := masterEvent(stored.Cal)
	switch method {
	case "REQUEST":
		if st := attendeeStatus(master, me); st != "" {
			out.Status = st
		}
		out.Applied = eventSequence(master) >= eventSequence(comp)
	case "REPLY":
		out.Applied = attendeeStatus(master, from) == out.Status
	}
	return out
}

func (s *Server) respondToInvitation(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Response string `json:"response"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	msg, err := s.mail.Get(m.Address, folderParam(r), uid)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	cal, comp, method := parseInvitation(msg.Calendar)
	if comp == nil {
		writeError(w, http.StatusUnprocessableEntity, "no_invitation", "This message does not contain a calendar invitation.")
		return
	}
	eventUID, _ := comp.Props.Text(ical.PropUID)
	stored, err := s.eventByUID(ctx, p.User.ID, eventUID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	me := mailpkg.Address{Name: m.DisplayName, Address: strings.ToLower(m.Address)}
	sender := ""
	if len(msg.From) > 0 {
		sender = strings.ToLower(msg.From[0].Address)
	}
	switch {
	case method == "REQUEST" && partStats[in.Response] != "":
		partStat := partStats[in.Response]
		organizer := eventOrganizer(comp)
		if organizer.Address == "" || organizer.Address == me.Address {
			writeError(w, http.StatusUnprocessableEntity, "no_organizer", "This invitation has no organizer to reply to.")
			return
		}
		delete(cal.Props, ical.PropMethod)
		for _, c := range cal.Children {
			if c.Name == ical.CompEvent && !setPartStat(c, me.Address, partStat) {
				p := attendeeProp(me, partStat, false)
				c.Props.Add(&p)
			}
		}
		if partStat == "DECLINED" {
			if stored != nil {
				if _, err := s.db.Exec(ctx, `DELETE FROM dav_objects WHERE collection_id = $1 AND name = $2`, stored.CollectionID, stored.Name); err != nil {
					s.internalError(w, r, err)
					return
				}
			}
		} else {
			collectionID, name, etag := "", eventUID+".ics", ""
			if stored != nil {
				collectionID, name, etag = stored.CollectionID, stored.Name, stored.ETag
			} else {
				calendar, err := s.davCollection(ctx, "calendar", "personal")
				if err != nil {
					s.davError(w, r, err)
					return
				}
				collectionID = calendar.ID
				if !objectNamePattern.MatchString(name) {
					name = secrets.Token(16) + ".ics"
				}
			}
			if err := s.storeEvent(ctx, collectionID, name, cal, etag); err != nil {
				s.davError(w, r, err)
				return
			}
		}
		title, _ := comp.Props.Text(ical.PropSummary)
		if err := s.sendITIP(m, []mailpkg.Address{organizer}, replyVerbs[partStat]+": "+title, "REPLY", replyCopy(comp, me, partStat)); err != nil {
			s.log.Warn("invitation reply rejected", "mailbox", m.ID, "err", err)
			writeError(w, http.StatusBadGateway, "send_failed", "Your calendar was updated, but the reply to the organizer could not be sent: "+err.Error())
			return
		}
	case method == "REPLY" && in.Response == "apply":
		if stored == nil {
			writeError(w, http.StatusNotFound, "not_found", "The event this reply is about is no longer in your calendar.")
			return
		}
		partStat := strings.ToUpper(attendeeStatus(comp, sender))
		if partStat == "" || eventOrganizer(masterEvent(stored.Cal)).Address != me.Address {
			writeError(w, http.StatusUnprocessableEntity, "invalid_reply", "This reply does not match an attendee of an event you organize.")
			return
		}
		for _, c := range stored.Cal.Children {
			if c.Name == ical.CompEvent {
				setPartStat(c, sender, partStat)
			}
		}
		if err := s.storeEvent(ctx, stored.CollectionID, stored.Name, stored.Cal, stored.ETag); err != nil {
			s.davError(w, r, err)
			return
		}
	case method == "CANCEL" && in.Response == "remove":
		if stored == nil {
			break
		}
		if organizer := eventOrganizer(masterEvent(stored.Cal)); organizer.Address != sender {
			writeError(w, http.StatusUnprocessableEntity, "invalid_cancel", "Only the organizer can cancel this event.")
			return
		}
		if comp.Props.Get(ical.PropRecurrenceID) == nil {
			if _, err := s.db.Exec(ctx, `DELETE FROM dav_objects WHERE collection_id = $1 AND name = $2`, stored.CollectionID, stored.Name); err != nil {
				s.internalError(w, r, err)
				return
			}
			break
		}
		rid := comp.Props.Get(ical.PropRecurrenceID).Value
		kept := stored.Cal.Children[:0]
		for _, c := range stored.Cal.Children {
			if p := c.Props.Get(ical.PropRecurrenceID); c.Name != ical.CompEvent || p == nil || p.Value != rid {
				kept = append(kept, c)
			}
		}
		comp.Props.SetText(ical.PropStatus, "CANCELLED")
		stored.Cal.Children = append(kept, comp)
		if err := s.storeEvent(ctx, stored.CollectionID, stored.Name, stored.Cal, stored.ETag); err != nil {
			s.davError(w, r, err)
			return
		}
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid_response", "Respond with accepted, tentative, or declined to an invitation, apply to a reply, or remove to a cancellation.")
		return
	}
	writeJSON(w, http.StatusOK, s.describeInvitation(ctx, p.User, m, sender, msg.Calendar))
}

func (s *Server) notifyDeletedEvent(ctx context.Context, user User, data string) error {
	cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		return nil
	}
	comp := masterEvent(cal)
	if comp == nil || eventOrganizer(comp).Address == "" {
		return nil
	}
	m, err := s.organizerMailbox(ctx, user)
	if err != nil || m == nil {
		return err
	}
	me := strings.ToLower(m.Address)
	title, _ := comp.Props.Text(ical.PropSummary)
	organizer := eventOrganizer(comp)
	if organizer.Address != me {
		if status := attendeeStatus(comp, me); status == "" || status == "declined" {
			return nil
		}
		return s.sendITIP(m, []mailpkg.Address{organizer}, "Declined: "+title, "REPLY", replyCopy(comp, mailpkg.Address{Name: m.DisplayName, Address: me}, "DECLINED"))
	}
	var attendees []mailpkg.Address
	for _, a := range eventAttendees(comp) {
		if a.Address != me {
			attendees = append(attendees, mailpkg.Address{Name: a.Name, Address: a.Address})
		}
	}
	return s.sendITIP(m, attendees, "Cancelled: "+title, "CANCEL", cancelledCopy(comp, attendees, eventSequence(comp)+1))
}
