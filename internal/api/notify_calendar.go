package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

func eventReminder(comp *ical.Component, start time.Time) *int {
	for _, child := range comp.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		trig := child.Props.Get(ical.PropTrigger)
		if trig == nil {
			continue
		}
		if dur, err := trig.Duration(); err == nil {
			return clampReminder(int((-dur) / time.Minute))
		}
		if when, err := trig.DateTime(time.UTC); err == nil && !start.IsZero() {
			return clampReminder(int(start.Sub(when) / time.Minute))
		}
	}
	return nil
}

func clampReminder(minutes int) *int {
	if minutes < 0 {
		minutes = 0
	}
	if minutes > 7*24*60 {
		minutes = 7 * 24 * 60
	}
	return &minutes
}

func setReminder(event *ical.Component, minutes int, title string) {
	alarm := ical.NewComponent(ical.CompAlarm)
	alarm.Props.SetText(ical.PropAction, "DISPLAY")
	if title == "" {
		title = "Reminder"
	}
	alarm.Props.SetText(ical.PropDescription, title)
	prop := ical.NewProp(ical.PropTrigger)
	prop.SetDuration(-time.Duration(minutes) * time.Minute)
	alarm.Props.Set(prop)
	event.Children = append(event.Children, alarm)
}

func reminderDue(start time.Time, minutes int, now time.Time) bool {
	alarm := start.Add(-time.Duration(minutes) * time.Minute)
	return !alarm.Before(now.Add(-90*time.Second)) && alarm.Before(now.Add(20*time.Second))
}

func (s *Server) deliverReminders(ctx context.Context) {
	if s.db == nil {
		return
	}
	s.db.Exec(ctx, `DELETE FROM notification_deliveries WHERE created_at < now() - interval '14 days'`)
	now := time.Now().UTC()
	rows, err := s.db.Query(ctx, `
		SELECT c.id, c.user_id, o.uid, o.name, o.data
		FROM dav_objects o
		JOIN dav_collections c ON c.id = o.collection_id AND c.kind = 'calendar' AND c.user_id IS NOT NULL
		WHERE o.recurring OR (o.starts_at IS NOT NULL AND o.starts_at < now() + interval '8 days' AND coalesce(o.ends_at, o.starts_at + interval '1 day') > now() - interval '8 days')
		ORDER BY o.starts_at NULLS LAST
		LIMIT 2000`)
	if err != nil {
		s.log.Error("loading calendar reminders failed", "err", err)
		return
	}
	defer rows.Close()
	type row struct {
		collection, owner, uid, name, data string
	}
	var items []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.collection, &item.owner, &item.uid, &item.name, &item.data); err != nil {
			s.log.Error("loading calendar reminders failed", "err", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		s.log.Error("loading calendar reminders failed", "err", err)
		return
	}
	for _, item := range items {
		s.remindEvent(ctx, now, item.collection, item.owner, item.uid, item.name, item.data)
	}
}

func (s *Server) remindEvent(ctx context.Context, now time.Time, collection, owner, uid, name, data string) {
	cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		return
	}
	master := masterEvent(cal)
	if master == nil {
		return
	}
	base, ev, err := eventFromComponent(name, master)
	if err != nil {
		return
	}
	overrides := map[int64]*ical.Component{}
	for _, comp := range cal.Events() {
		if comp.Props.Get(ical.PropRecurrenceID) == nil {
			continue
		}
		if rid, err := comp.Props.DateTime(ical.PropRecurrenceID, time.UTC); err == nil {
			overrides[rid.Unix()] = comp.Component
		}
	}
	from := now.Add(-2 * time.Minute)
	to := now.Add(8 * 24 * time.Hour)
	var starts []time.Time
	set, err := ev.RecurrenceSet(time.UTC)
	if err != nil || set == nil {
		if !base.Start.Before(from) && base.Start.Before(to) {
			starts = []time.Time{base.Start}
		}
	} else {
		starts = occurrencesIn(set.Iterator(), from, to)
	}
	for _, start := range starts {
		if comp := overrides[start.Unix()]; comp != nil {
			if status, _ := comp.Props.Text(ical.PropStatus); strings.EqualFold(status, "CANCELLED") {
				continue
			}
			o, _, err := eventFromComponent(name, comp)
			if err != nil || o.Reminder == nil || !reminderDue(o.Start, *o.Reminder, now) {
				continue
			}
			s.fireReminder(ctx, collection, owner, uid, o.Title, o.Start, *o.Reminder)
			continue
		}
		if base.Reminder == nil || !reminderDue(start, *base.Reminder, now) {
			continue
		}
		s.fireReminder(ctx, collection, owner, uid, base.Title, start, *base.Reminder)
	}
}

func (s *Server) fireReminder(ctx context.Context, collection, owner, uid, title string, start time.Time, minutes int) {
	if title == "" {
		title = "Calendar reminder"
	}
	users, err := s.calendarAudience(ctx, collection, owner)
	if err != nil {
		s.log.Error("loading calendar reminder audience failed", "err", err)
		return
	}
	when := start.UTC().Format(time.RFC3339)
	body := "Starting now"
	if minutes > 0 {
		body = fmt.Sprintf("Starts in %s", reminderPhrase(minutes))
	}
	for _, userID := range users {
		key := fmt.Sprintf("cal:%s:%s:%d:%d", userID, uid, start.UTC().Unix(), minutes)
		if !s.claimDelivery(ctx, key) {
			continue
		}
		ev := liveEvent{Kind: "calendar", Title: title, Body: body, URL: "/calendar", Tag: "cal:" + uid, EventID: uid, Start: when}
		delivered := s.userIsLive(ctx, userID)
		if delivered {
			s.publishLive(ctx, "calendar", userID, "", ev)
		}
		if s.pushCalendar(ctx, userID, ev) {
			delivered = true
		}
		if !delivered {
			s.releaseDelivery(ctx, []string{key})
			continue
		}
		notifySent.WithLabelValues("calendar", "sent").Inc()
	}
}

func reminderPhrase(minutes int) string {
	switch {
	case minutes%1440 == 0:
		days := minutes / 1440
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case minutes%60 == 0:
		hours := minutes / 60
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	default:
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
}

func (s *Server) userIsLive(ctx context.Context, userID string) bool {
	if s.hub.hasUser(userID) {
		return true
	}
	if s.kv == nil {
		return false
	}
	n, err := s.kv.Exists(ctx, "aether:live:"+userID).Result()
	return err == nil && n == 1
}

func (s *Server) calendarAudience(ctx context.Context, collection, owner string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT u.id FROM users u
		WHERE u.status = 'active' AND (
			u.id = $1
			OR EXISTS (SELECT 1 FROM calendar_shares s JOIN organization_members a ON a.user_id = u.id JOIN organization_members b ON b.org_id = a.org_id AND b.user_id = $1 WHERE s.collection_id = $2 AND s.user_id = u.id)
			OR EXISTS (
				SELECT 1 FROM calendar_shares s
				JOIN organization_members m ON m.org_id = s.org_id AND m.user_id = u.id
				WHERE s.collection_id = $2 AND s.org_id IS NOT NULL
			)
		)
		AND NOT EXISTS (SELECT 1 FROM notification_settings n WHERE n.user_id = u.id AND NOT n.calendar_enabled)`, owner, collection)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Server) pushCalendar(ctx context.Context, userID string, ev liveEvent) bool {
	public, private, err := s.vapid(ctx)
	if err != nil || public == "" {
		return false
	}
	rows, err := s.db.Query(ctx, `SELECT id, endpoint, p256dh, auth FROM web_push_subscriptions WHERE user_id = $1`, userID)
	if err != nil {
		return false
	}
	defer rows.Close()
	var subs []pushSub
	for rows.Next() {
		var sub pushSub
		if err := rows.Scan(&sub.id, &sub.endpoint, &sub.p256dh, &sub.auth); err != nil {
			return false
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil || len(subs) == 0 {
		return false
	}
	payload, _ := json.Marshal(ev)
	return s.sendWebPush(ctx, subs, payload, public, private)
}
