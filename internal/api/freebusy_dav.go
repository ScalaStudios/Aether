package api

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

const (
	caldavNamespace   = "urn:ietf:params:xml:ns:caldav"
	davScheduleInbox  = "inbox"
	davScheduleOutbox = "outbox"
	maxOutboxRange    = 92 * 24 * time.Hour
)

type davHrefs struct {
	XMLName xml.Name
	Hrefs   []string `xml:"DAV: href"`
}

func (p *davHrefs) GetXMLName() xml.Name {
	return p.XMLName
}

type scheduleResponse struct {
	XMLName   xml.Name            `xml:"urn:ietf:params:xml:ns:caldav schedule-response"`
	Responses []scheduleRecipient `xml:"urn:ietf:params:xml:ns:caldav response"`
}

type scheduleRecipient struct {
	Recipient struct {
		Href string `xml:"DAV: href"`
	} `xml:"urn:ietf:params:xml:ns:caldav recipient"`
	Status string `xml:"urn:ietf:params:xml:ns:caldav request-status"`
	Data   string `xml:"urn:ietf:params:xml:ns:caldav calendar-data,omitempty"`
}

func (s *Server) calendarUserAddresses(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT 'mailto:' || address FROM (
			SELECT m.address, 0 AS rank FROM mailboxes m WHERE m.owner_user_id = $1 AND m.active
			UNION SELECT u.email, 1 FROM users u WHERE u.id = $1
		) a GROUP BY address ORDER BY min(rank), address`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (s *Server) schedulingProps(ctx context.Context, userID, principal string) ([]*davHrefs, error) {
	addresses, err := s.calendarUserAddresses(ctx, userID)
	return []*davHrefs{
		{XMLName: xml.Name{Space: caldavNamespace, Local: "schedule-inbox-URL"}, Hrefs: []string{principal + davScheduleInbox + "/"}},
		{XMLName: xml.Name{Space: caldavNamespace, Local: "schedule-outbox-URL"}, Hrefs: []string{principal + davScheduleOutbox + "/"}},
		{XMLName: xml.Name{Space: caldavNamespace, Local: "calendar-user-address-set"}, Hrefs: addresses},
	}, err
}

func serveScheduleBox(w http.ResponseWriter, r *http.Request, box string) {
	switch r.Method {
	case http.MethodOptions:
		allow := "OPTIONS, PROPFIND"
		if box == davScheduleOutbox {
			allow += ", POST"
		}
		w.Header().Set("Allow", allow)
		w.WriteHeader(http.StatusNoContent)
	case "PROPFIND":
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprintf(w, `%s<d:multistatus xmlns:d="DAV:" xmlns:c="%s"><d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype><d:collection/><c:schedule-%s/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`,
			xml.Header, caldavNamespace, r.URL.Path, box)
	default:
		w.Header().Set("Allow", "OPTIONS, PROPFIND")
		http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
	}
}

func (s *Server) serveScheduleOutbox(w http.ResponseWriter, r *http.Request, userID string) {
	if r.Method != http.MethodPost {
		serveScheduleBox(w, r, davScheduleOutbox)
		return
	}
	cal, err := ical.NewDecoder(io.LimitReader(r.Body, maxDAVObjectBytes)).Decode()
	if err != nil {
		http.Error(w, "The request body is not a valid iCalendar object.", http.StatusBadRequest)
		return
	}
	method, _ := cal.Props.Text(ical.PropMethod)
	var query *ical.Component
	for _, c := range cal.Children {
		if c.Name == ical.CompFreeBusy {
			query = c
			break
		}
	}
	if !strings.EqualFold(method, "REQUEST") || query == nil {
		http.Error(w, "Only free/busy requests are supported here. Aether delivers invitations by email.", http.StatusForbidden)
		return
	}
	start, err1 := query.Props.DateTime(ical.PropDateTimeStart, time.UTC)
	end, err2 := query.Props.DateTime(ical.PropDateTimeEnd, time.UTC)
	recipients := []string{}
	for _, p := range query.Props.Values(ical.PropAttendee) {
		recipients = append(recipients, calAddress(p.Value))
	}
	if err1 != nil || err2 != nil || !end.After(start) || end.Sub(start) > maxOutboxRange || len(recipients) == 0 || len(recipients) > maxFreeBusyAttendees {
		http.Error(w, "Ask for at most 92 days and 1 to 50 attendees.", http.StatusBadRequest)
		return
	}
	addresses, err := s.calendarUserAddresses(r.Context(), userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !slices.Contains(addresses, "mailto:"+eventOrganizer(query).Address) {
		http.Error(w, "The organizer must be one of your own addresses.", http.StatusForbidden)
		return
	}
	results, err := s.busyFor(r.Context(), userID, slices.Clone(recipients), start, end)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	uid, _ := query.Props.Text(ical.PropUID)
	if uid == "" {
		uid = secrets.Token(16)
	}
	out := scheduleResponse{}
	for _, fb := range results {
		item := scheduleRecipient{Status: "3.7;Invalid calendar user"}
		item.Recipient.Href = "mailto:" + fb.Email
		if fb.Known {
			reply := newCalendar()
			reply.Props.SetText(ical.PropMethod, "REPLY")
			c := ical.NewComponent(ical.CompFreeBusy)
			c.Props.SetText(ical.PropUID, uid)
			c.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
			c.Props.SetDateTime(ical.PropDateTimeStart, start)
			c.Props.SetDateTime(ical.PropDateTimeEnd, end)
			c.Props.Set(query.Props.Get(ical.PropOrganizer))
			c.Props.Add(&ical.Prop{Name: ical.PropAttendee, Params: ical.Params{}, Value: "mailto:" + fb.Email})
			for _, b := range fb.Busy {
				if b.Start.Before(start) {
					b.Start = start
				}
				if b.End.After(end) {
					b.End = end
				}
				period := &ical.Prop{Name: ical.PropFreeBusy, Params: ical.Params{}, Value: b.Start.UTC().Format("20060102T150405Z") + "/" + b.End.UTC().Format("20060102T150405Z")}
				period.Params.Set(ical.ParamFreeBusyType, "BUSY")
				c.Props.Add(period)
			}
			reply.Children = append(reply.Children, c)
			var buf bytes.Buffer
			if err := ical.NewEncoder(&buf).Encode(reply); err != nil {
				s.internalError(w, r, err)
				return
			}
			item.Status, item.Data = "2.0;Success", buf.String()
		}
		out.Responses = append(out.Responses, item)
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, xml.Header)
	xml.NewEncoder(w).Encode(out)
}
