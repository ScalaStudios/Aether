package api

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

type taskItem struct {
	ID          string     `json:"id"`
	CalendarID  string     `json:"calendarId"`
	Title       string     `json:"title"`
	Notes       string     `json:"notes"`
	Due         *time.Time `json:"due"`
	Status      string     `json:"status"`
	Priority    int        `json:"priority"`
	CompletedAt *time.Time `json:"completedAt"`
	ReadOnly    bool       `json:"readOnly"`
}

type taskInput struct {
	Title      string     `json:"title"`
	Notes      string     `json:"notes"`
	Due        *time.Time `json:"due"`
	Status     string     `json:"status"`
	Priority   int        `json:"priority"`
	CalendarID string     `json:"calendarId"`
}

var taskStatusToICS = map[string]string{
	"needs-action": "NEEDS-ACTION",
	"in-process":   "IN-PROCESS",
	"completed":    "COMPLETED",
	"cancelled":    "CANCELLED",
}

var taskStatusFromICS = map[string]string{
	"NEEDS-ACTION": "needs-action",
	"IN-PROCESS":   "in-process",
	"COMPLETED":    "completed",
	"CANCELLED":    "cancelled",
}

func taskFromComponent(name string, comp *ical.Component) taskItem {
	out := taskItem{ID: name, Status: "needs-action"}
	out.Title, _ = comp.Props.Text(ical.PropSummary)
	out.Notes, _ = comp.Props.Text(ical.PropDescription)
	if status, _ := comp.Props.Text(ical.PropStatus); status != "" {
		if mapped, ok := taskStatusFromICS[strings.ToUpper(status)]; ok {
			out.Status = mapped
		}
	}
	if p := comp.Props.Get(ical.PropPriority); p != nil {
		n, _ := strconv.Atoi(strings.TrimSpace(p.Value))
		if n >= 0 && n <= 9 {
			out.Priority = n
		}
	}
	if due, err := comp.Props.DateTime(ical.PropDue, time.UTC); err == nil && !due.IsZero() {
		due = due.UTC()
		out.Due = &due
	}
	if done, err := comp.Props.DateTime(ical.PropCompleted, time.UTC); err == nil && !done.IsZero() {
		done = done.UTC()
		out.CompletedAt = &done
	}
	return out
}

func taskFromCalendar(name, data string) (taskItem, bool) {
	cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		return taskItem{}, false
	}
	for _, comp := range cal.Children {
		if comp.Name == ical.CompToDo && comp.Props.Get(ical.PropRecurrenceID) == nil {
			return taskFromComponent(name, comp), true
		}
	}
	return taskItem{}, false
}

func taskComponent(uid, title, notes, status string, priority, sequence int, due, completed *time.Time) *ical.Component {
	comp := ical.NewComponent(ical.CompToDo)
	comp.Props.SetText(ical.PropUID, uid)
	comp.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	comp.Props.SetText(ical.PropSummary, title)
	if notes != "" {
		comp.Props.SetText(ical.PropDescription, notes)
	}
	if ics := taskStatusToICS[status]; ics != "" {
		comp.Props.SetText(ical.PropStatus, ics)
	}
	if priority > 0 {
		prop := ical.NewProp(ical.PropPriority)
		prop.Value = strconv.Itoa(priority)
		comp.Props.Set(prop)
	}
	if due != nil {
		comp.Props.SetDateTime(ical.PropDue, due.UTC())
	}
	if completed != nil {
		comp.Props.SetDateTime(ical.PropCompleted, completed.UTC())
		done := ical.NewProp(ical.PropPercentComplete)
		done.Value = "100"
		comp.Props.Set(done)
	}
	if sequence > 0 {
		prop := ical.NewProp(ical.PropSequence)
		prop.Value = strconv.Itoa(sequence)
		comp.Props.Set(prop)
	}
	return comp
}

func validTaskInput(w http.ResponseWriter, in *taskInput) bool {
	in.Title = strings.TrimSpace(in.Title)
	in.Notes = strings.TrimSpace(in.Notes)
	if in.Status == "" {
		in.Status = "needs-action"
	}
	if in.CalendarID == "" {
		in.CalendarID = "personal"
	}
	switch {
	case in.Title == "" || len(in.Title) > 300:
		writeError(w, http.StatusUnprocessableEntity, "invalid_task", "Give the task a title of up to 300 characters.")
	case len(in.Notes) > 10000:
		writeError(w, http.StatusUnprocessableEntity, "invalid_task", "Keep the notes to 10,000 characters or fewer.")
	case taskStatusToICS[in.Status] == "":
		writeError(w, http.StatusUnprocessableEntity, "invalid_task", "Status must be needs-action, in-process, completed, or cancelled.")
	case in.Priority < 0 || in.Priority > 9:
		writeError(w, http.StatusUnprocessableEntity, "invalid_task", "Priority is a number from 0 to 9, where 1 is the highest and 0 means none.")
	case in.Due != nil && in.Due.IsZero():
		writeError(w, http.StatusUnprocessableEntity, "invalid_task", "The due date is not a valid time.")
	default:
		return true
	}
	return false
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	calendars, err := s.davCollections(ctx, "calendar")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(calendars) == 0 {
		writeJSON(w, http.StatusOK, []taskItem{})
		return
	}
	byID := map[string]collectionRow{}
	ids := make([]string, len(calendars))
	for i, c := range calendars {
		byID[c.ID], ids[i] = c, c.ID
	}
	subs, err := s.subscribedIDs(ctx, ids)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.db.Query(ctx, `
		SELECT collection_id, name, data FROM dav_objects
		WHERE collection_id = ANY($1) AND component = 'VTODO'
		ORDER BY name LIMIT 1000`, ids)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()
	out := []taskItem{}
	for rows.Next() {
		var collectionID, name, data string
		if err := rows.Scan(&collectionID, &name, &data); err != nil {
			s.internalError(w, r, err)
			return
		}
		item, ok := taskFromCalendar(name, data)
		if !ok {
			continue
		}
		cal := byID[collectionID]
		item.CalendarID = cal.Slug
		item.ReadOnly = cal.Access == "read" || subs[collectionID]
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	sortTasks(out)
	writeJSON(w, http.StatusOK, out)
}

func sortTasks(items []taskItem) {
	rank := func(t taskItem) int {
		if t.Status == "completed" || t.Status == "cancelled" {
			return 1
		}
		return 0
	}
	for i := 1; i < len(items); i++ {
		item := items[i]
		j := i
		for j > 0 && taskBefore(item, items[j-1], rank) {
			items[j] = items[j-1]
			j--
		}
		items[j] = item
	}
}

func taskBefore(a, b taskItem, rank func(taskItem) int) bool {
	if rank(a) != rank(b) {
		return rank(a) < rank(b)
	}
	switch {
	case a.Due == nil && b.Due != nil:
		return false
	case a.Due != nil && b.Due == nil:
		return true
	case a.Due != nil && b.Due != nil && !a.Due.Equal(*b.Due):
		return a.Due.Before(*b.Due)
	default:
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	}
}

func (s *Server) saveTask(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var in taskInput
	if !decode(w, r, 64<<10, &in) || !validTaskInput(w, &in) {
		return
	}
	calendar, err := s.writableCollection(ctx, "calendar", in.CalendarID)
	if err != nil {
		s.davError(w, r, err)
		return
	}
	name := r.PathValue("id")
	uid := secrets.Token(16)
	sequence := 0
	var ifMatch webdav.ConditionalMatch
	status := http.StatusCreated
	var prevCompleted *time.Time
	if name != "" {
		if !objectNamePattern.MatchString(name) {
			notFound(w)
			return
		}
		var etag, data, component string
		err := s.db.QueryRow(ctx, `SELECT uid, etag, data, component FROM dav_objects WHERE collection_id = $1 AND name = $2`, calendar.ID, name).Scan(&uid, &etag, &data, &component)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && component != "VTODO" {
			notFound(w)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if cal, decErr := ical.NewDecoder(strings.NewReader(data)).Decode(); decErr == nil {
			for _, comp := range cal.Children {
				if comp.Name == ical.CompToDo {
					sequence = eventSequence(comp) + 1
					if done, doneErr := comp.Props.DateTime(ical.PropCompleted, time.UTC); doneErr == nil && !done.IsZero() {
						done = done.UTC()
						prevCompleted = &done
					}
					break
				}
			}
		}
		ifMatch, status = webdav.ConditionalMatch(`"`+etag+`"`), http.StatusOK
	} else {
		name = uid + ".ics"
	}
	var completed *time.Time
	if in.Status == "completed" {
		if prevCompleted != nil {
			completed = prevCompleted
		} else {
			now := time.Now().UTC()
			completed = &now
		}
	}
	comp := taskComponent(uid, in.Title, in.Notes, in.Status, in.Priority, sequence, in.Due, completed)
	doc := newCalendar()
	doc.Children = append(doc.Children, comp)
	fields, err := calendarFields(doc)
	if err != nil {
		s.davError(w, r, err)
		return
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(doc); err != nil {
		s.internalError(w, r, err)
		return
	}
	if _, err := s.putDAVObject(ctx, calendar.ID, name, buf.String(), fields, ifMatch, ""); err != nil {
		s.davError(w, r, err)
		return
	}
	out := taskFromComponent(name, comp)
	out.CalendarID = calendar.Slug
	writeJSON(w, status, out)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request, p *principal) {
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
	tag, err := s.db.Exec(ctx, `DELETE FROM dav_objects WHERE collection_id = $1 AND name = $2 AND component = 'VTODO'`, calendar.ID, name)
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
