package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/google/uuid"

	"github.com/scalastudios/aether/internal/secrets"
)

func (s *Server) pimCtx(ctx context.Context, userID string) (context.Context, error) {
	ctx = context.WithValue(ctx, davUserKey{}, userID)
	return ctx, s.ensureDAVDefaults(ctx, userID)
}

func (s *Server) jmapPIMState(ctx context.Context, userID, kind string) (string, error) {
	ctx, err := s.pimCtx(ctx, userID)
	if err != nil {
		return "", err
	}
	cols, err := s.davCollections(ctx, kind)
	if err != nil {
		return "", err
	}
	parts := []string{kind}
	var ids []string
	for _, c := range cols {
		parts = append(parts, c.ID, c.Slug, c.Name, c.Color, c.Access, c.UpdatedAt.UTC().Format(time.RFC3339Nano))
		ids = append(ids, c.ID)
	}
	if len(ids) > 0 {
		rows, err := s.db.Query(ctx, `SELECT uid, etag FROM dav_objects WHERE collection_id = ANY($1) ORDER BY 1, 2`, ids)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		for rows.Next() {
			var uid, etag string
			if err := rows.Scan(&uid, &etag); err != nil {
				return "", err
			}
			parts = append(parts, uid, etag)
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
	}
	return hashParts(parts...), nil
}

func calendarRights(access string) map[string]bool {
	write := access == "owner" || access == "write"
	return map[string]bool{
		"mayReadFreeBusy": true, "mayReadItems": true, "mayWriteAll": write, "mayWriteOwn": write,
		"mayUpdatePrivate": write, "mayRSVP": write, "mayShare": false, "mayDelete": access == "owner",
	}
}

func (s *Server) jmapCalendarGet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCal)
	if cerr != nil {
		return nil, cerr
	}
	list, state, err := s.calendarList(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	return filterGet(account.ID, state, list, args), nil
}

func (s *Server) calendarList(ctx context.Context, userID string) ([]map[string]any, string, error) {
	state, err := s.jmapPIMState(ctx, userID, "calendar")
	if err != nil {
		return nil, "", err
	}
	ctx, err = s.pimCtx(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	cols, err := s.davCollections(ctx, "calendar")
	if err != nil {
		return nil, "", err
	}
	list := make([]map[string]any, 0, len(cols))
	for i, c := range cols {
		list = append(list, map[string]any{
			"id": c.ID, "name": c.Name, "description": nilOr(c.Description), "color": nilOr(c.Color), "sortOrder": i,
			"isSubscribed": true, "isVisible": true, "myRights": calendarRights(c.Access),
		})
	}
	return list, state, nil
}

func filterGet(account, state string, list []map[string]any, args map[string]any) map[string]any {
	props, _ := stringList(args["properties"])
	ids, _ := stringList(args["ids"])
	if ids == nil && args["ids"] == nil {
		for i := range list {
			list[i] = pickProps(list[i], props)
		}
		if list == nil {
			list = []map[string]any{}
		}
		return map[string]any{"accountId": account, "state": state, "list": list, "notFound": []string{}}
	}
	byID := map[string]map[string]any{}
	for _, item := range list {
		byID[item["id"].(string)] = item
	}
	var out []map[string]any
	var missing []string
	for _, id := range ids {
		if item, ok := byID[id]; ok {
			out = append(out, pickProps(item, props))
		} else {
			missing = append(missing, id)
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	if missing == nil {
		missing = []string{}
	}
	return map[string]any{"accountId": account, "state": state, "list": out, "notFound": missing}
}

func (s *Server) jmapCalendarQuery(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCal)
	if cerr != nil {
		return nil, cerr
	}
	list, state, err := s.calendarList(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	ids := make([]string, len(list))
	for i, item := range list {
		ids[i] = item["id"].(string)
	}
	return map[string]any{"accountId": account.ID, "queryState": state, "canCalculateChanges": false, "position": 0, "ids": ids, "total": len(ids)}, nil
}

func (s *Server) jmapCalendarSet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any, created map[string]string) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCal)
	if cerr != nil {
		return nil, cerr
	}
	ctx, err := s.pimCtx(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	old, _ := s.jmapPIMState(ctx, user.UserID, "calendar")
	createdOut := map[string]any{}
	updated := map[string]any{}
	var destroyed []string
	notCreated := map[string]any{}
	notUpdated := map[string]any{}
	notDestroyed := map[string]any{}
	if create, ok := args["create"].(map[string]any); ok {
		for cid, raw := range create {
			obj, _ := raw.(map[string]any)
			name := strings.TrimSpace(str(obj["name"]))
			color := str(obj["color"])
			if name == "" || len(name) > 100 || !colorPattern.MatchString(color) {
				notCreated[cid] = itemError("invalidProperties", "Give the calendar a name of up to 100 characters and a color like #3b82f6.", "name")
				continue
			}
			id := uuid.NewString()
			slug := secrets.Token(10)
			if _, err := s.db.Exec(ctx, `INSERT INTO dav_collections (id, user_id, kind, slug, name, color, description) VALUES ($1, $2, 'calendar', $3, $4, $5, $6)`, id, user.UserID, slug, name, color, str(obj["description"])); err != nil {
				notCreated[cid] = itemError("serverFail", "The calendar could not be created.")
				continue
			}
			created[cid] = id
			createdOut[cid] = map[string]any{"id": id}
		}
	}
	if update, ok := args["update"].(map[string]any); ok {
		for id, raw := range update {
			obj, _ := raw.(map[string]any)
			tag, err := s.db.Exec(ctx, `UPDATE dav_collections SET name = coalesce(nullif($3, ''), name), color = CASE WHEN $4 = '' THEN color ELSE $4 END, description = coalesce($5, description), updated_at = now() WHERE id = $1 AND user_id = $2 AND kind = 'calendar'`, id, user.UserID, str(obj["name"]), str(obj["color"]), obj["description"])
			if err != nil || tag.RowsAffected() == 0 {
				notUpdated[id] = itemError("notFound", "That calendar cannot be changed.")
				continue
			}
			updated[id] = nil
		}
	}
	if destroy, ok := args["destroy"].([]any); ok {
		for _, raw := range destroy {
			id, _ := raw.(string)
			var slug string
			var n int
			err := s.db.QueryRow(ctx, `SELECT slug, (SELECT count(*) FROM dav_objects WHERE collection_id = dav_collections.id) FROM dav_collections WHERE id = $1 AND user_id = $2 AND kind = 'calendar'`, id, user.UserID).Scan(&slug, &n)
			if err != nil {
				notDestroyed[id] = itemError("notFound", "That calendar cannot be deleted.")
				continue
			}
			if slug == "personal" {
				notDestroyed[id] = itemError("forbidden", "The personal calendar cannot be deleted.")
				continue
			}
			if n > 0 {
				notDestroyed[id] = itemError("calendarHasEvent", "Move or delete the events in this calendar first.")
				continue
			}
			if _, err := s.db.Exec(ctx, `DELETE FROM dav_collections WHERE id = $1`, id); err != nil {
				notDestroyed[id] = itemError("serverFail", "The calendar could not be deleted.")
				continue
			}
			destroyed = append(destroyed, id)
		}
	}
	newState, _ := s.jmapPIMState(ctx, user.UserID, "calendar")
	return setResult(account.ID, old, newState, createdOut, updated, destroyed, notCreated, notUpdated, notDestroyed), nil
}

func eventID(uid string) string {
	return "ev." + base64.RawURLEncoding.EncodeToString([]byte(uid))
}

func eventUID(id string) (string, bool) {
	enc, ok := strings.CutPrefix(id, "ev.")
	if !ok {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(raw) == 0 || bytes.Contains(raw, []byte{0}) {
		return "", false
	}
	return string(raw), true
}

type jmapStoredEvent struct {
	Collection, Name, Data, Access string
	Updated                        time.Time
}

func (s *Server) loadEvents(ctx context.Context, userID string) ([]jmapStoredEvent, map[string]collectionRow, error) {
	ctx, err := s.pimCtx(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	cols, err := s.davCollections(ctx, "calendar")
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]collectionRow{}
	var ids []string
	for _, c := range cols {
		byID[c.ID] = c
		ids = append(ids, c.ID)
	}
	if len(ids) == 0 {
		return nil, byID, nil
	}
	rows, err := s.db.Query(ctx, `SELECT collection_id, name, data, updated_at FROM dav_objects WHERE collection_id = ANY($1) ORDER BY updated_at DESC LIMIT 1000`, ids)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []jmapStoredEvent
	for rows.Next() {
		var ev jmapStoredEvent
		if err := rows.Scan(&ev.Collection, &ev.Name, &ev.Data, &ev.Updated); err != nil {
			return nil, nil, err
		}
		if c, ok := byID[ev.Collection]; ok {
			ev.Access = c.Access
		}
		out = append(out, ev)
	}
	return out, byID, rows.Err()
}

func (s *Server) jmapEventGet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCal)
	if cerr != nil {
		return nil, cerr
	}
	events, _, err := s.loadEvents(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	byID := map[string]map[string]any{}
	for _, ev := range events {
		if obj := eventObject(ev); obj != nil {
			byID[obj["id"].(string)] = obj
		}
	}
	state, _ := s.jmapPIMState(r.Context(), user.UserID, "calendar")
	var list []map[string]any
	for _, obj := range byID {
		list = append(list, obj)
	}
	return filterGet(account.ID, state, list, args), nil
}

func (s *Server) jmapEventQuery(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCal)
	if cerr != nil {
		return nil, cerr
	}
	filter, ferr := flattenFilter(rawJSON(args["filter"]))
	if ferr != nil {
		return nil, &callError{Type: "unsupportedFilter", Description: "This event filter is not supported."}
	}
	events, _, err := s.loadEvents(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	var ids []string
	for _, ev := range events {
		obj := eventObject(ev)
		if obj == nil || !eventMatches(obj, ev, filter) {
			continue
		}
		ids = append(ids, obj["id"].(string))
	}
	pos, limit, qerr := pageArgs(args)
	if qerr != nil {
		return nil, qerr
	}
	total := len(ids)
	if pos > len(ids) {
		pos = len(ids)
	}
	ids = ids[pos:]
	if len(ids) > limit {
		ids = ids[:limit]
	}
	state, _ := s.jmapPIMState(r.Context(), user.UserID, "calendar")
	out := map[string]any{"accountId": account.ID, "queryState": state, "canCalculateChanges": false, "position": pos, "ids": ids}
	if args["calculateTotal"] == true || args["calculateTotal"] == nil {
		out["total"] = total
	}
	return out, nil
}

func eventMatches(obj map[string]any, ev jmapStoredEvent, filter map[string]json.RawMessage) bool {
	if raw, ok := filter["inCalendars"]; ok {
		var ids []string
		if json.Unmarshal(raw, &ids) != nil || !containsString(ids, ev.Collection) {
			return false
		}
	}
	if raw, ok := filter["uid"]; ok {
		var uid string
		if json.Unmarshal(raw, &uid) != nil || obj["uid"] != uid {
			return false
		}
	}
	if raw, ok := filter["text"]; ok {
		var text string
		json.Unmarshal(raw, &text)
		blob := strings.ToLower(str(obj["title"]) + " " + str(obj["description"]))
		if text != "" && !strings.Contains(blob, strings.ToLower(text)) {
			return false
		}
	}
	start := str(obj["start"])
	if raw, ok := filter["after"]; ok {
		var after string
		json.Unmarshal(raw, &after)
		if start != "" && start < after[:min(len(after), len(start))] && !boolOf(obj["recurrenceRules"]) {
			return false
		}
	}
	if raw, ok := filter["before"]; ok {
		var before string
		json.Unmarshal(raw, &before)
		if start != "" && start > before {
			return false
		}
	}
	for k := range filter {
		switch k {
		case "inCalendars", "uid", "text", "after", "before":
		default:
			return false
		}
	}
	return true
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func boolOf(v any) bool {
	list, ok := v.([]any)
	return ok && len(list) > 0
}

func eventObject(ev jmapStoredEvent) map[string]any {
	cal, err := ical.NewDecoder(strings.NewReader(ev.Data)).Decode()
	if err != nil {
		return nil
	}
	var comp *ical.Component
	for _, child := range cal.Children {
		if child.Name == ical.CompEvent && child.Props.Get(ical.PropRecurrenceID) == nil {
			comp = child
			break
		}
	}
	if comp == nil {
		return nil
	}
	uid, _ := comp.Props.Text(ical.PropUID)
	if uid == "" {
		return nil
	}
	obj := map[string]any{
		"@type": "Event", "id": eventID(uid), "uid": uid, "calendarIds": map[string]bool{ev.Collection: true},
		"title": textProp(comp, ical.PropSummary), "description": nilOr(textProp(comp, ical.PropDescription)),
		"updated": ev.Updated.UTC().Format(time.RFC3339),
	}
	if seq, err := strconv.Atoi(textProp(comp, ical.PropSequence)); err == nil {
		obj["sequence"] = seq
	}
	evnt := ical.Event{Component: comp}
	if start, err := evnt.DateTimeStart(time.UTC); err == nil && !start.IsZero() {
		allDay := comp.Props.Get(ical.PropDateTimeStart) != nil && comp.Props.Get(ical.PropDateTimeStart).ValueType() == ical.ValueDate
		if allDay {
			obj["start"] = start.Format("2006-01-02")
			obj["showWithoutTime"] = true
			obj["duration"] = "P1D"
		} else {
			zone, name := time.UTC, "Etc/UTC"
			if loc, ok := validTimezone(comp.Props.Get(ical.PropDateTimeStart).Params.Get(ical.ParamTimezoneID)); ok {
				zone, name = loc, loc.String()
			}
			obj["start"] = start.In(zone).Format("2006-01-02T15:04:05")
			obj["timeZone"] = name
			obj["showWithoutTime"] = false
			if end, err := evnt.DateTimeEnd(time.UTC); err == nil && end.After(start) {
				obj["duration"] = formatDuration(end.Sub(start))
			}
		}
	}
	if loc := textProp(comp, ical.PropLocation); loc != "" {
		obj["locations"] = map[string]any{"1": map[string]any{"@type": "Location", "name": loc}}
	}
	if rule := comp.Props.Get(ical.PropRecurrenceRule); rule != nil && rule.Value != "" {
		obj["recurrenceRules"] = []any{rruleObject(rule.Value)}
	}
	if status := textProp(comp, ical.PropStatus); status != "" {
		obj["status"] = strings.ToLower(status)
	}
	return obj
}

func textProp(comp *ical.Component, name string) string {
	s, _ := comp.Props.Text(name)
	return s
}

func formatDuration(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return "P" + strconv.Itoa(int(d/(24*time.Hour))) + "D"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	out := "PT"
	if h > 0 {
		out += strconv.Itoa(h) + "H"
	}
	if m > 0 {
		out += strconv.Itoa(m) + "M"
	}
	if s > 0 || out == "PT" {
		out += strconv.Itoa(s) + "S"
	}
	return out
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(s, "P") {
		return 0, errUnsupportedFilter
	}
	var days, hours, mins, secs int
	rest := s[1:]
	if n, after, ok := cutInt(rest, "D"); ok {
		days, rest = n, after
	}
	if strings.HasPrefix(rest, "T") {
		rest = rest[1:]
		if n, after, ok := cutInt(rest, "H"); ok {
			hours, rest = n, after
		}
		if n, after, ok := cutInt(rest, "M"); ok {
			mins, rest = n, after
		}
		if n, after, ok := cutInt(rest, "S"); ok {
			secs, rest = n, after
		}
	}
	if rest != "" {
		return 0, errUnsupportedFilter
	}
	return time.Duration(days)*24*time.Hour + time.Duration(hours)*time.Hour + time.Duration(mins)*time.Minute + time.Duration(secs)*time.Second, nil
}

func cutInt(s, unit string) (int, string, bool) {
	i := strings.Index(s, unit)
	if i <= 0 {
		return 0, s, false
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, s, false
	}
	return n, s[i+len(unit):], true
}

func rruleObject(rule string) map[string]any {
	obj := map[string]any{"@type": "RecurrenceRule"}
	for _, part := range strings.Split(rule, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(k) {
		case "FREQ":
			obj["frequency"] = strings.ToLower(v)
		case "INTERVAL":
			n, _ := strconv.Atoi(v)
			obj["interval"] = n
		case "COUNT":
			n, _ := strconv.Atoi(v)
			obj["count"] = n
		case "UNTIL":
			obj["until"] = v
		case "BYDAY":
			var days []any
			for _, d := range strings.Split(v, ",") {
				days = append(days, strings.ToLower(d))
			}
			obj["byDay"] = days
		}
	}
	return obj
}

func (s *Server) jmapEventSet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any, created map[string]string) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCal)
	if cerr != nil {
		return nil, cerr
	}
	ctx, err := s.pimCtx(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	old, _ := s.jmapPIMState(ctx, user.UserID, "calendar")
	createdOut := map[string]any{}
	updated := map[string]any{}
	var destroyed []string
	notCreated := map[string]any{}
	notUpdated := map[string]any{}
	notDestroyed := map[string]any{}
	if create, ok := args["create"].(map[string]any); ok {
		for cid, raw := range create {
			obj, _ := raw.(map[string]any)
			id, uerr := s.saveEventObject(ctx, user.UserID, "", obj)
			if uerr != nil {
				notCreated[cid] = uerr
				continue
			}
			created[cid] = id
			createdOut[cid] = map[string]any{"id": id}
		}
	}
	if update, ok := args["update"].(map[string]any); ok {
		for id, raw := range update {
			obj, _ := raw.(map[string]any)
			if _, uerr := s.saveEventObject(ctx, user.UserID, id, obj); uerr != nil {
				notUpdated[id] = uerr
				continue
			}
			updated[id] = nil
		}
	}
	if destroy, ok := args["destroy"].([]any); ok {
		for _, raw := range destroy {
			id, _ := raw.(string)
			if uerr := s.deleteJMAPEvent(ctx, user.UserID, id); uerr != nil {
				notDestroyed[id] = uerr
				continue
			}
			destroyed = append(destroyed, id)
		}
	}
	newState, _ := s.jmapPIMState(ctx, user.UserID, "calendar")
	return setResult(account.ID, old, newState, createdOut, updated, destroyed, notCreated, notUpdated, notDestroyed), nil
}

func (s *Server) saveEventObject(ctx context.Context, userID, id string, patch map[string]any) (string, map[string]any) {
	if patch == nil {
		return "", itemError("invalidArguments", "An event needs an object.")
	}
	var existing jmapStoredEvent
	var comp *ical.Component
	var cal *ical.Calendar
	uid := ""
	if id != "" {
		var ok bool
		uid, ok = eventUID(id)
		if !ok {
			return "", itemError("notFound", "That event does not exist.")
		}
		events, _, err := s.loadEvents(ctx, userID)
		if err != nil {
			return "", itemError("serverFail", "The calendar could not be read.")
		}
		found := false
		for _, ev := range events {
			obj := eventObject(ev)
			if obj != nil && obj["id"] == id {
				existing = ev
				found = true
				break
			}
		}
		if !found {
			return "", itemError("notFound", "That event does not exist.")
		}
		if existing.Access == "read" {
			return "", itemError("forbidden", "This calendar is shared with you read-only.")
		}
		if err := s.checkSharedMeeting(ctx, &collectionRow{ID: existing.Collection, Access: existing.Access}, existing.Name); err != nil {
			return "", itemError("forbidden", "Only the calendar's owner can change a meeting with other people.")
		}
		cal, _ = ical.NewDecoder(strings.NewReader(existing.Data)).Decode()
		for _, child := range cal.Children {
			if child.Name == ical.CompEvent && child.Props.Get(ical.PropRecurrenceID) == nil {
				comp = child
			}
		}
	}
	if comp == nil {
		cal = ical.NewCalendar()
		cal.Props.SetText(ical.PropVersion, "2.0")
		cal.Props.SetText(ical.PropProductID, "-//Aether//JMAP//EN")
		ev := ical.NewEvent()
		comp = ev.Component
		cal.Children = append(cal.Children, comp)
		uid = uuid.NewString()
		comp.Props.SetText(ical.PropUID, uid)
		id = eventID(uid)
	}
	if title, ok := patch["title"].(string); ok {
		if strings.TrimSpace(title) == "" || len(title) > 500 {
			return "", itemError("invalidProperties", "Give the event a title of up to 500 characters.", "title")
		}
		comp.Props.SetText(ical.PropSummary, strings.TrimSpace(title))
	} else if existing.Data == "" && textProp(comp, ical.PropSummary) == "" {
		return "", itemError("invalidProperties", "Give the event a title.", "title")
	}
	if desc, ok := patch["description"].(string); ok {
		comp.Props.SetText(ical.PropDescription, desc)
	}
	if patch["description"] == nil && hasKey(patch, "description") {
		comp.Props.Del(ical.PropDescription)
	}
	if uerr := applyEventSchedule(comp, patch); uerr != nil {
		return "", uerr
	}
	if loc, ok := patch["locations"].(map[string]any); ok {
		name := ""
		for _, raw := range loc {
			item, _ := raw.(map[string]any)
			name = str(item["name"])
			break
		}
		if name == "" {
			comp.Props.Del(ical.PropLocation)
		} else {
			comp.Props.SetText(ical.PropLocation, name)
		}
	}
	if status, ok := patch["status"].(string); ok {
		switch strings.ToLower(status) {
		case "confirmed", "tentative", "cancelled":
			comp.Props.SetText(ical.PropStatus, strings.ToUpper(status))
		default:
			return "", itemError("invalidProperties", "status must be confirmed, tentative, or cancelled.", "status")
		}
	}
	comp.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	calID := existing.Collection
	if boxes, ok := patch["calendarIds"].(map[string]any); ok {
		var next string
		for id, v := range boxes {
			if v == true {
				if next != "" {
					return "", itemError("invalidProperties", "An event is stored in one calendar.", "calendarIds")
				}
				next = id
			}
		}
		if next == "" {
			return "", itemError("invalidProperties", "Choose a calendar.", "calendarIds")
		}
		calID = next
	}
	if calID == "" {
		cols, err := s.davCollections(ctx, "calendar")
		if err != nil || len(cols) == 0 {
			return "", itemError("invalidProperties", "Choose a calendar.", "calendarIds")
		}
		calID = cols[0].ID
	}
	col, ok := s.collectionAccess(ctx, userID, calID)
	if !ok || col.Access == "read" {
		return "", itemError("forbidden", "You cannot change that calendar.")
	}
	addTimezones(cal)
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return "", itemError("invalidProperties", "The event could not be saved.")
	}
	name := existing.Name
	if name == "" || calID != existing.Collection {
		name = secrets.Token(12) + ".ics"
	}
	fields, err := calendarFields(cal)
	if err != nil {
		return "", itemError("invalidProperties", "The event could not be saved.")
	}
	if _, err := s.putDAVObject(ctx, calID, name, buf.String(), fields, webdav.ConditionalMatch(""), webdav.ConditionalMatch("")); err != nil {
		return "", itemError("serverFail", "The event could not be saved.")
	}
	if existing.Name != "" && (existing.Collection != calID || existing.Name != name) {
		_ = s.deleteDAVObject(ctx, existing.Collection, existing.Name)
	}
	return eventID(uid), nil
}

func applyEventSchedule(comp *ical.Component, patch map[string]any) map[string]any {
	ev := ical.Event{Component: comp}
	prevStart, _ := ev.DateTimeStart(time.UTC)
	prevEnd, _ := ev.DateTimeEnd(time.UTC)
	zone := time.UTC
	if p := comp.Props.Get(ical.PropDateTimeStart); p != nil {
		if loc, ok := validTimezone(p.Params.Get(ical.ParamTimezoneID)); ok {
			zone = loc
		}
	}
	allDay := patch["showWithoutTime"] == true
	raw, hasStart := patch["start"].(string)
	if !hasStart && !prevStart.IsZero() {
		raw = prevStart.In(zone).Format("2006-01-02T15:04:05")
		if p := comp.Props.Get(ical.PropDateTimeStart); p.ValueType() == ical.ValueDate {
			raw = prevStart.Format("2006-01-02")
		}
	}
	if name, ok := patch["timeZone"].(string); ok || hasKey(patch, "timeZone") {
		zone = time.UTC
		if name != "" && name != "Etc/UTC" && name != "UTC" {
			loc, valid := validTimezone(name)
			if !valid {
				return itemError("invalidProperties", "timeZone must be a time zone like Europe/London.", "timeZone")
			}
			zone = loc
		}
	}
	_, hasDuration := patch["duration"].(string)
	if raw != "" && (hasStart || hasKey(patch, "timeZone") || hasDuration) {
		var start time.Time
		var err error
		if allDay || len(raw) == 10 {
			start, err = time.Parse("2006-01-02", raw[:10])
			allDay = true
		} else {
			start, err = time.ParseInLocation("2006-01-02T15:04:05", raw, zone)
			if err != nil {
				start, err = time.Parse(time.RFC3339, raw)
				start = start.In(zone)
			}
		}
		if err != nil {
			return itemError("invalidProperties", "start must be a local date or time.", "start")
		}
		length := prevEnd.Sub(prevStart)
		if d, ok := patch["duration"].(string); ok {
			length, err = parseDuration(d)
			if err != nil || length <= 0 {
				return itemError("invalidProperties", "duration must look like PT1H or P1D.", "duration")
			}
		}
		comp.Props.Del(ical.PropDuration)
		if allDay {
			comp.Props.SetDate(ical.PropDateTimeStart, start)
		} else {
			comp.Props.SetDateTime(ical.PropDateTimeStart, start)
		}
		if length > 0 {
			if allDay {
				comp.Props.SetDate(ical.PropDateTimeEnd, start.Add(length))
			} else {
				comp.Props.SetDateTime(ical.PropDateTimeEnd, start.Add(length))
			}
		}
	}
	if rules, ok := patch["recurrenceRules"].([]any); ok && len(rules) > 0 {
		rule, _ := rules[0].(map[string]any)
		freq := strings.ToUpper(str(rule["frequency"]))
		switch freq {
		case "YEARLY", "MONTHLY", "WEEKLY", "DAILY":
		default:
			return itemError("invalidProperties", "frequency must be yearly, monthly, weekly, or daily.", "recurrenceRules")
		}
		rr := "FREQ=" + freq
		if n, ok := numberInt(rule["interval"]); ok && n > 1 {
			rr += ";INTERVAL=" + strconv.Itoa(n)
		}
		if n, ok := numberInt(rule["count"]); ok && n > 0 {
			rr += ";COUNT=" + strconv.Itoa(n)
		}
		comp.Props.Set(&ical.Prop{Name: ical.PropRecurrenceRule, Value: rr, Params: ical.Params{}})
	}
	return nil
}

func (s *Server) collectionAccess(ctx context.Context, userID, id string) (collectionRow, bool) {
	cols, err := s.davCollections(ctx, "calendar")
	if err != nil {
		return collectionRow{}, false
	}
	for _, c := range cols {
		if c.ID == id {
			return c, true
		}
	}
	books, err := s.davCollections(ctx, "addressbook")
	if err != nil {
		return collectionRow{}, false
	}
	for _, c := range books {
		if c.ID == id {
			return c, true
		}
	}
	return collectionRow{}, false
}

func (s *Server) deleteJMAPEvent(ctx context.Context, userID, id string) map[string]any {
	events, _, err := s.loadEvents(ctx, userID)
	if err != nil {
		return itemError("serverFail", "The calendar could not be read.")
	}
	for _, ev := range events {
		obj := eventObject(ev)
		if obj == nil || obj["id"] != id {
			continue
		}
		if ev.Access == "read" {
			return itemError("forbidden", "This calendar is shared with you read-only.")
		}
		if err := s.checkSharedMeeting(ctx, &collectionRow{ID: ev.Collection, Access: ev.Access}, ev.Name); err != nil {
			return itemError("forbidden", "Only the calendar's owner can cancel a meeting with other people.")
		}
		if err := s.deleteDAVObject(ctx, ev.Collection, ev.Name); err != nil {
			return itemError("serverFail", "The event could not be deleted.")
		}
		return nil
	}
	return itemError("notFound", "That event does not exist.")
}

func cardID(uid string) string { return "cd." + base64.RawURLEncoding.EncodeToString([]byte(uid)) }

func cardUID(id string) (string, bool) {
	enc, ok := strings.CutPrefix(id, "cd.")
	if !ok {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	return string(raw), true
}

func (s *Server) jmapBookGet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCard)
	if cerr != nil {
		return nil, cerr
	}
	list, state, err := s.bookList(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	return filterGet(account.ID, state, list, args), nil
}

func (s *Server) bookList(ctx context.Context, userID string) ([]map[string]any, string, error) {
	state, err := s.jmapPIMState(ctx, userID, "addressbook")
	if err != nil {
		return nil, "", err
	}
	ctx, err = s.pimCtx(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	cols, err := s.davCollections(ctx, "addressbook")
	if err != nil {
		return nil, "", err
	}
	list := make([]map[string]any, 0, len(cols))
	for i, c := range cols {
		list = append(list, map[string]any{"id": c.ID, "name": c.Name, "sortOrder": i, "isSubscribed": true, "myRights": calendarRights(c.Access)})
	}
	return list, state, nil
}

func (s *Server) jmapBookQuery(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCard)
	if cerr != nil {
		return nil, cerr
	}
	list, state, err := s.bookList(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	ids := make([]string, len(list))
	for i, item := range list {
		ids[i] = item["id"].(string)
	}
	return map[string]any{"accountId": account.ID, "queryState": state, "canCalculateChanges": false, "position": 0, "ids": ids}, nil
}

func (s *Server) jmapBookSet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any, created map[string]string) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCard)
	if cerr != nil {
		return nil, cerr
	}
	ctx, err := s.pimCtx(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	old, _ := s.jmapPIMState(ctx, user.UserID, "addressbook")
	createdOut := map[string]any{}
	notCreated := map[string]any{}
	notDestroyed := map[string]any{}
	var destroyed []string
	if create, ok := args["create"].(map[string]any); ok {
		for cid, raw := range create {
			obj, _ := raw.(map[string]any)
			name := strings.TrimSpace(str(obj["name"]))
			if name == "" || len(name) > 100 {
				notCreated[cid] = itemError("invalidProperties", "Give the address book a name of up to 100 characters.", "name")
				continue
			}
			id := uuid.NewString()
			if _, err := s.db.Exec(ctx, `INSERT INTO dav_collections (id, user_id, kind, slug, name) VALUES ($1, $2, 'addressbook', $3, $4)`, id, user.UserID, secrets.Token(10), name); err != nil {
				notCreated[cid] = itemError("serverFail", "The address book could not be created.")
				continue
			}
			created[cid] = id
			createdOut[cid] = map[string]any{"id": id}
		}
	}
	if destroy, ok := args["destroy"].([]any); ok {
		for _, raw := range destroy {
			id, _ := raw.(string)
			var slug string
			var n int
			err := s.db.QueryRow(ctx, `SELECT slug, (SELECT count(*) FROM dav_objects WHERE collection_id = dav_collections.id) FROM dav_collections WHERE id = $1 AND user_id = $2 AND kind = 'addressbook'`, id, user.UserID).Scan(&slug, &n)
			if err != nil || slug == "contacts" {
				notDestroyed[id] = itemError("forbidden", "That address book cannot be deleted.")
				continue
			}
			if n > 0 {
				notDestroyed[id] = itemError("invalidArguments", "Delete the contacts in this address book first.")
				continue
			}
			s.db.Exec(ctx, `DELETE FROM dav_collections WHERE id = $1`, id)
			destroyed = append(destroyed, id)
		}
	}
	newState, _ := s.jmapPIMState(ctx, user.UserID, "addressbook")
	return setResult(account.ID, old, newState, createdOut, map[string]any{}, destroyed, notCreated, map[string]any{}, notDestroyed), nil
}

type storedCard struct {
	Collection, Name, Data, UID string
}

func (s *Server) loadCards(ctx context.Context, userID string) ([]storedCard, error) {
	ctx, err := s.pimCtx(ctx, userID)
	if err != nil {
		return nil, err
	}
	cols, err := s.davCollections(ctx, "addressbook")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range cols {
		ids = append(ids, c.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT collection_id, name, uid, data FROM dav_objects WHERE collection_id = ANY($1) ORDER BY lower(summary) LIMIT 1000`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedCard
	for rows.Next() {
		var c storedCard
		if err := rows.Scan(&c.Collection, &c.Name, &c.UID, &c.Data); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func cardObject(c storedCard) map[string]any {
	card, err := vcard.NewDecoder(strings.NewReader(c.Data)).Decode()
	if err != nil {
		return nil
	}
	uid := card.Value(vcard.FieldUID)
	if uid == "" {
		uid = c.UID
	}
	obj := map[string]any{"@type": "Card", "id": cardID(uid), "uid": uid, "addressBookIds": map[string]bool{c.Collection: true}, "name": map[string]any{"full": card.PreferredValue(vcard.FieldFormattedName)}}
	if emails := card.Values(vcard.FieldEmail); len(emails) > 0 {
		m := map[string]any{}
		for i, e := range emails {
			m[strconv.Itoa(i+1)] = map[string]any{"@type": "EmailAddress", "address": e}
		}
		obj["emails"] = m
	}
	if phones := card.Values(vcard.FieldTelephone); len(phones) > 0 {
		m := map[string]any{}
		for i, p := range phones {
			m[strconv.Itoa(i+1)] = map[string]any{"@type": "Phone", "number": p}
		}
		obj["phones"] = m
	}
	if org := card.PreferredValue(vcard.FieldOrganization); org != "" {
		obj["organizations"] = map[string]any{"1": map[string]any{"@type": "Organization", "name": org}}
	}
	if notes := card.Value(vcard.FieldNote); notes != "" {
		obj["notes"] = map[string]any{"1": map[string]any{"@type": "Note", "note": notes}}
	}
	return obj
}

func (s *Server) jmapCardGet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCard)
	if cerr != nil {
		return nil, cerr
	}
	cards, err := s.loadCards(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	var list []map[string]any
	for _, c := range cards {
		if obj := cardObject(c); obj != nil {
			list = append(list, obj)
		}
	}
	state, _ := s.jmapPIMState(r.Context(), user.UserID, "addressbook")
	return filterGet(account.ID, state, list, args), nil
}

func (s *Server) jmapCardQuery(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCard)
	if cerr != nil {
		return nil, cerr
	}
	filter, ferr := flattenFilter(rawJSON(args["filter"]))
	if ferr != nil {
		return nil, &callError{Type: "unsupportedFilter", Description: "This contact filter is not supported."}
	}
	cards, err := s.loadCards(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	var text string
	if raw, ok := filter["text"]; ok {
		json.Unmarshal(raw, &text)
		text = strings.ToLower(text)
	}
	var book string
	if raw, ok := filter["inAddressBook"]; ok {
		json.Unmarshal(raw, &book)
	}
	var ids []string
	for _, c := range cards {
		if book != "" && c.Collection != book {
			continue
		}
		obj := cardObject(c)
		if obj == nil {
			continue
		}
		if text != "" {
			blob := strings.ToLower(c.Data)
			if !strings.Contains(blob, text) {
				continue
			}
		}
		ids = append(ids, obj["id"].(string))
	}
	state, _ := s.jmapPIMState(r.Context(), user.UserID, "addressbook")
	return map[string]any{"accountId": account.ID, "queryState": state, "canCalculateChanges": false, "position": 0, "ids": ids}, nil
}

func (s *Server) jmapCardSet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any, created map[string]string) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapCard)
	if cerr != nil {
		return nil, cerr
	}
	ctx, err := s.pimCtx(r.Context(), user.UserID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	old, _ := s.jmapPIMState(ctx, user.UserID, "addressbook")
	createdOut := map[string]any{}
	updated := map[string]any{}
	var destroyed []string
	notCreated := map[string]any{}
	notUpdated := map[string]any{}
	notDestroyed := map[string]any{}
	if create, ok := args["create"].(map[string]any); ok {
		for cid, raw := range create {
			obj, _ := raw.(map[string]any)
			id, uerr := s.saveCard(ctx, user.UserID, "", obj)
			if uerr != nil {
				notCreated[cid] = uerr
				continue
			}
			created[cid] = id
			createdOut[cid] = map[string]any{"id": id}
		}
	}
	if update, ok := args["update"].(map[string]any); ok {
		for id, raw := range update {
			obj, _ := raw.(map[string]any)
			if _, uerr := s.saveCard(ctx, user.UserID, id, obj); uerr != nil {
				notUpdated[id] = uerr
				continue
			}
			updated[id] = nil
		}
	}
	if destroy, ok := args["destroy"].([]any); ok {
		for _, raw := range destroy {
			id, _ := raw.(string)
			if uerr := s.deleteCard(ctx, user.UserID, id); uerr != nil {
				notDestroyed[id] = uerr
				continue
			}
			destroyed = append(destroyed, id)
		}
	}
	newState, _ := s.jmapPIMState(ctx, user.UserID, "addressbook")
	return setResult(account.ID, old, newState, createdOut, updated, destroyed, notCreated, notUpdated, notDestroyed), nil
}

func (s *Server) saveCard(ctx context.Context, userID, id string, patch map[string]any) (string, map[string]any) {
	var card vcard.Card
	var colID, name string
	uid := uuid.NewString()
	if id != "" {
		var ok bool
		uid, ok = cardUID(id)
		if !ok {
			return "", itemError("notFound", "That contact does not exist.")
		}
		cards, err := s.loadCards(ctx, userID)
		if err != nil {
			return "", itemError("serverFail", "The address book could not be read.")
		}
		found := false
		for _, c := range cards {
			if c.UID == uid || cardID(c.UID) == id {
				decoded, err := vcard.NewDecoder(strings.NewReader(c.Data)).Decode()
				if err != nil {
					return "", itemError("serverFail", "The contact could not be read.")
				}
				card, colID, name, found = decoded, c.Collection, c.Name, true
				break
			}
		}
		if !found {
			return "", itemError("notFound", "That contact does not exist.")
		}
	} else {
		card = vcard.Card{}
		card.SetValue(vcard.FieldVersion, "4.0")
		card.SetValue(vcard.FieldUID, uid)
	}
	if nameObj, ok := patch["name"].(map[string]any); ok {
		full := strings.TrimSpace(str(nameObj["full"]))
		if full == "" || len(full) > 200 {
			return "", itemError("invalidProperties", "Give the contact a name of up to 200 characters.", "name")
		}
		card.SetValue(vcard.FieldFormattedName, full)
		card.SetName(&vcard.Name{GivenName: full})
	} else if id == "" {
		return "", itemError("invalidProperties", "Give the contact a name.", "name")
	}
	if emails, ok := patch["emails"].(map[string]any); ok {
		delete(card, vcard.FieldEmail)
		for _, raw := range emails {
			item, _ := raw.(map[string]any)
			addr, err := mail.ParseAddress(strings.TrimSpace(str(item["address"])))
			if err != nil {
				return "", itemError("invalidProperties", "A contact email address is not valid.", "emails")
			}
			card.Add(vcard.FieldEmail, &vcard.Field{Value: strings.ToLower(addr.Address)})
		}
	}
	if phones, ok := patch["phones"].(map[string]any); ok {
		delete(card, vcard.FieldTelephone)
		for _, raw := range phones {
			item, _ := raw.(map[string]any)
			if n := strings.TrimSpace(str(item["number"])); n != "" {
				card.Add(vcard.FieldTelephone, &vcard.Field{Value: n})
			}
		}
	}
	if orgs, ok := patch["organizations"].(map[string]any); ok {
		name := ""
		for _, raw := range orgs {
			item, _ := raw.(map[string]any)
			name = str(item["name"])
			break
		}
		if name == "" {
			delete(card, vcard.FieldOrganization)
		} else {
			card.SetValue(vcard.FieldOrganization, name)
		}
	}
	if notes, ok := patch["notes"].(map[string]any); ok {
		note := ""
		for _, raw := range notes {
			item, _ := raw.(map[string]any)
			note = str(item["note"])
			break
		}
		if note == "" {
			delete(card, vcard.FieldNote)
		} else {
			card.SetValue(vcard.FieldNote, note)
		}
	}
	if books, ok := patch["addressBookIds"].(map[string]any); ok {
		for id, v := range books {
			if v == true {
				colID = id
				break
			}
		}
	}
	if colID == "" {
		cols, err := s.davCollections(ctx, "addressbook")
		if err != nil || len(cols) == 0 {
			return "", itemError("invalidProperties", "Choose an address book.", "addressBookIds")
		}
		colID = cols[0].ID
	}
	if _, ok := s.collectionAccess(ctx, userID, colID); !ok {
		return "", itemError("forbidden", "You cannot change that address book.")
	}
	if name == "" {
		name = secrets.Token(12) + ".vcf"
	}
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return "", itemError("invalidProperties", "The contact could not be saved.")
	}
	emails := card.Values(vcard.FieldEmail)
	if _, err := s.putDAVObject(ctx, colID, name, buf.String(), objectFields{UID: uid, Summary: card.PreferredValue(vcard.FieldFormattedName), Emails: emails}, webdav.ConditionalMatch(""), webdav.ConditionalMatch("")); err != nil {
		return "", itemError("serverFail", "The contact could not be saved.")
	}
	return cardID(uid), nil
}

func (s *Server) deleteCard(ctx context.Context, userID, id string) map[string]any {
	cards, err := s.loadCards(ctx, userID)
	if err != nil {
		return itemError("serverFail", "The address book could not be read.")
	}
	for _, c := range cards {
		if cardID(c.UID) != id && c.UID != id {
			continue
		}
		if err := s.deleteDAVObject(ctx, c.Collection, c.Name); err != nil {
			return itemError("serverFail", "The contact could not be deleted.")
		}
		return nil
	}
	return itemError("notFound", "That contact does not exist.")
}
