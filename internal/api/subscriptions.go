package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

const (
	maxFeedBytes        = 8 << 20
	maxSubscriptions    = 20
	subscriptionLockKey = 731
)

type calendarSubscription struct {
	URL            string     `json:"url"`
	RefreshMinutes int        `json:"refreshMinutes"`
	LastFetchedAt  *time.Time `json:"lastFetchedAt"`
	LastError      string     `json:"lastError,omitempty"`
	Events         int        `json:"events,omitempty"`
	NotModified    bool       `json:"notModified,omitempty"`
}

type subscriptionRow struct {
	CollectionID   string
	URL            string
	RefreshMinutes int
	ETag           string
	LastModified   string
}

type feedFailure struct {
	status  int
	code    string
	message string
}

func (e *feedFailure) Error() string { return e.message }

func feedErr(status int, code, message string) error {
	return &feedFailure{status: status, code: code, message: message}
}

func normalizeFeedURL(raw string, allowHTTP bool) (string, error) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "webcal://"):
		raw = "https://" + raw[len("webcal://"):]
	case strings.HasPrefix(lower, "webcals://"):
		raw = "https://" + raw[len("webcals://"):]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || len(raw) > 2048 || u.Scheme == "" {
		return "", feedErr(http.StatusUnprocessableEntity, "invalid_url", "Enter an https:// calendar link, or a webcal:// link, without a username or password.")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && allowHTTP) {
		return "", feedErr(http.StatusUnprocessableEntity, "invalid_url", "Enter an https:// calendar link, or a webcal:// link, without a username or password.")
	}
	u.Fragment = ""
	return u.String(), nil
}

func stableObjectName(uid string) string {
	name := uid + ".ics"
	if len(name) <= 255 && objectNamePattern.MatchString(name) {
		return name
	}
	sum := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(sum[:12]) + ".ics"
}

func (s *Server) feedHTTP() *http.Client {
	dialer := publicDialer(s.cfg.WebhookAllowPrivate)
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return feedErr(http.StatusUnprocessableEntity, "invalid_url", "The calendar link redirected too many times.")
			}
			if _, err := normalizeFeedURL(req.URL.String(), s.cfg.WebhookAllowPrivate); err != nil {
				return feedErr(http.StatusUnprocessableEntity, "invalid_url", "The calendar link redirected to an address Aether won't fetch.")
			}
			return nil
		},
		Transport: &http.Transport{
			Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second, MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second,
		},
	}
}

func (s *Server) fetchFeed(ctx context.Context, rawURL, etag, lastModified string) (body, newETag, newModified string, notModified bool, err error) {
	normalized, err := normalizeFeedURL(rawURL, s.cfg.WebhookAllowPrivate)
	if err != nil {
		return "", "", "", false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized, nil)
	if err != nil {
		return "", "", "", false, feedErr(http.StatusUnprocessableEntity, "invalid_url", "Enter an https:// calendar link, or a webcal:// link, without a username or password.")
	}
	req.Header.Set("User-Agent", "Aether-Calendar")
	req.Header.Set("Accept", "text/calendar, text/plain, */*")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	resp, err := s.feedHTTP().Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddress) {
			return "", "", "", false, feedErr(http.StatusUnprocessableEntity, "invalid_url", "That link resolves to a private or reserved address.")
		}
		var ff *feedFailure
		if errors.As(err, &ff) {
			return "", "", "", false, ff
		}
		return "", "", "", false, feedErr(http.StatusUnprocessableEntity, "fetch_failed", "The calendar link could not be read. Check the address and try again.")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return "", etag, lastModified, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return "", "", "", false, feedErr(http.StatusUnprocessableEntity, "fetch_failed", "The calendar link returned HTTP "+resp.Status+".")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return "", "", "", false, feedErr(http.StatusUnprocessableEntity, "fetch_failed", "The calendar link could not be read. Check the address and try again.")
	}
	if len(data) > maxFeedBytes {
		return "", "", "", false, feedErr(http.StatusUnprocessableEntity, "feed_too_large", "The calendar is larger than 8 MB.")
	}
	return string(data), resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), false, nil
}

type feedObject struct {
	Name   string
	Data   string
	Fields objectFields
}

func objectsFromFeed(body string) ([]feedObject, error) {
	source, err := ical.NewDecoder(strings.NewReader(body)).Decode()
	if err != nil {
		return nil, feedErr(http.StatusUnprocessableEntity, "invalid_calendar", "That link is not a valid iCalendar feed.")
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
			if comp.Props.Get(ical.PropDateTimeStamp) == nil {
				comp.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
			}
			if _, seen := groups[uid]; !seen {
				order = append(order, uid)
			}
			groups[uid] = append(groups[uid], comp)
		}
	}
	if len(order) > maxImportObjects {
		return nil, feedErr(http.StatusUnprocessableEntity, "too_many_events", "A subscribed calendar can contain at most 5,000 events.")
	}
	out := make([]feedObject, 0, len(order))
	for _, uid := range order {
		obj := newCalendar()
		var tzIDs []string
		for id := range referencedTZIDs(groups[uid]) {
			if timezones[id] != nil {
				tzIDs = append(tzIDs, id)
			}
		}
		sort.Strings(tzIDs)
		for _, id := range tzIDs {
			obj.Children = append(obj.Children, timezones[id])
		}
		obj.Children = append(obj.Children, groups[uid]...)
		fields, err := calendarFields(obj)
		var buf bytes.Buffer
		if err == nil {
			err = ical.NewEncoder(&buf).Encode(obj)
		}
		if err != nil {
			continue
		}
		out = append(out, feedObject{Name: stableObjectName(uid), Data: buf.String(), Fields: fields})
	}
	if len(order) > 0 && len(out) == 0 {
		return nil, feedErr(http.StatusUnprocessableEntity, "invalid_calendar", "That link is not a valid iCalendar feed.")
	}
	return out, nil
}

func (s *Server) replaceFeedObjects(ctx context.Context, tx pgx.Tx, collectionID string, objects []feedObject) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(user_id::text), $2) FROM dav_collections WHERE id = $1`, collectionID, subscriptionLockKey); err != nil {
		return err
	}
	uids := []string{}
	for _, obj := range objects {
		name := obj.Name
		var existing string
		err := tx.QueryRow(ctx, `SELECT name FROM dav_objects WHERE collection_id = $1 AND uid = $2`, collectionID, obj.Fields.UID).Scan(&existing)
		if err == nil {
			name = existing
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		row := &objectRow{Name: name, Data: obj.Data, ETag: etagOf(obj.Data), UpdatedAt: time.Now()}
		if err := insertDAVObject(ctx, tx, collectionID, row, obj.Fields, "", ""); err != nil {
			return err
		}
		uids = append(uids, obj.Fields.UID)
	}
	_, err := tx.Exec(ctx, `DELETE FROM dav_objects WHERE collection_id = $1 AND NOT (uid = ANY($2))`, collectionID, uids)
	return err
}

func (s *Server) subscribedIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	return subscribedIDs(ctx, s.db, ids)
}

func subscribedIDs(ctx context.Context, db queryer, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `SELECT collection_id FROM calendar_subscriptions WHERE collection_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (s *Server) finishSubscription(ctx context.Context, collectionID, etag, modified string, refresh int, fetched bool, fetchErr error) {
	message := ""
	if fetchErr != nil {
		message = fetchErr.Error()
		if len(message) > 300 {
			message = message[:300]
		}
	}
	interval := refresh
	if fetchErr != nil && interval > 60 {
		interval = 60
	}
	if fetchErr != nil && interval < 15 {
		interval = 15
	}
	if fetched {
		_, err := s.db.Exec(ctx, `
			UPDATE calendar_subscriptions
			SET etag = $2, last_modified = $3, last_fetched_at = now(), last_error = $4, next_fetch_at = now() + make_interval(mins => $5)
			WHERE collection_id = $1`, collectionID, etag, modified, message, interval)
		if err != nil {
			s.log.Error("calendar subscription status update failed", "collection", collectionID, "err", err)
		}
		return
	}
	_, err := s.db.Exec(ctx, `
		UPDATE calendar_subscriptions SET last_error = $2, next_fetch_at = now() + make_interval(mins => $3) WHERE collection_id = $1`,
		collectionID, message, interval)
	if err != nil {
		s.log.Error("calendar subscription status update failed", "collection", collectionID, "err", err)
	}
}

func (s *Server) refreshSubscription(ctx context.Context, row subscriptionRow) (int, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, etag, modified, notModified, err := s.fetchFeed(ctx, row.URL, row.ETag, row.LastModified)
	if err != nil {
		s.finishSubscription(context.WithoutCancel(ctx), row.CollectionID, row.ETag, row.LastModified, row.RefreshMinutes, false, err)
		subscriptionRefresh.WithLabelValues("error").Inc()
		return 0, false, err
	}
	if notModified {
		s.finishSubscription(context.WithoutCancel(ctx), row.CollectionID, etag, modified, row.RefreshMinutes, true, nil)
		subscriptionRefresh.WithLabelValues("not_modified").Inc()
		var count int
		_ = s.db.QueryRow(context.WithoutCancel(ctx), `SELECT count(*) FROM dav_objects WHERE collection_id = $1`, row.CollectionID).Scan(&count)
		return count, true, nil
	}
	objects, err := objectsFromFeed(body)
	if err != nil {
		s.finishSubscription(context.WithoutCancel(ctx), row.CollectionID, row.ETag, row.LastModified, row.RefreshMinutes, false, err)
		subscriptionRefresh.WithLabelValues("error").Inc()
		return 0, false, err
	}
	err = pgx.BeginFunc(context.WithoutCancel(ctx), s.db, func(tx pgx.Tx) error {
		return s.replaceFeedObjects(context.WithoutCancel(ctx), tx, row.CollectionID, objects)
	})
	if err != nil {
		wrapped := feedErr(http.StatusInternalServerError, "internal", "The calendar was fetched, but it could not be saved.")
		s.finishSubscription(context.WithoutCancel(ctx), row.CollectionID, row.ETag, row.LastModified, row.RefreshMinutes, false, wrapped)
		subscriptionRefresh.WithLabelValues("error").Inc()
		s.log.Error("calendar subscription replace failed", "collection", row.CollectionID, "err", err)
		return 0, false, wrapped
	}
	s.finishSubscription(context.WithoutCancel(ctx), row.CollectionID, etag, modified, row.RefreshMinutes, true, nil)
	subscriptionRefresh.WithLabelValues("ok").Inc()
	return len(objects), false, nil
}

func (s *Server) claimSubscription(ctx context.Context) (*subscriptionRow, error) {
	var row subscriptionRow
	err := s.db.QueryRow(ctx, `
		UPDATE calendar_subscriptions SET next_fetch_at = now() + interval '10 minutes'
		WHERE collection_id = (
			SELECT collection_id FROM calendar_subscriptions
			WHERE next_fetch_at <= now()
			ORDER BY next_fetch_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING collection_id, url, refresh_minutes, etag, last_modified`).Scan(&row.CollectionID, &row.URL, &row.RefreshMinutes, &row.ETag, &row.LastModified)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Server) RunSubscriptionRefresh(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		for i := 0; i < 3; i++ {
			if ctx.Err() != nil {
				return
			}
			row, err := s.claimSubscription(ctx)
			if err != nil {
				s.log.Error("calendar subscription claim failed", "err", err)
				break
			}
			if row == nil {
				break
			}
			if _, _, err := s.refreshSubscription(ctx, *row); err != nil {
				s.log.Warn("calendar subscription refresh failed", "collection", row.CollectionID, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func validRefresh(minutes int) bool {
	return minutes >= 15 && minutes <= 10080 && minutes%5 == 0
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		URL            string `json:"url"`
		Name           string `json:"name"`
		Color          string `json:"color"`
		RefreshMinutes int    `json:"refreshMinutes"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = "Subscribed calendar"
	}
	if in.RefreshMinutes == 0 {
		in.RefreshMinutes = 360
	}
	cal := calendarInput{Name: in.Name, Color: in.Color}
	if !validCalendarInput(w, &cal) {
		return
	}
	if !validRefresh(in.RefreshMinutes) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_refresh", "Choose a refresh interval from 15 minutes to 7 days, in steps of 5 minutes.")
		return
	}
	normalized, err := normalizeFeedURL(in.URL, s.cfg.WebhookAllowPrivate)
	if err != nil {
		writeFeedError(w, err)
		return
	}
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM calendar_subscriptions s JOIN dav_collections c ON c.id = s.collection_id WHERE c.user_id = $1`, p.User.ID).Scan(&count); err != nil {
		s.internalError(w, r, err)
		return
	}
	if count >= maxSubscriptions {
		writeError(w, http.StatusUnprocessableEntity, "too_many_subscriptions", "You can subscribe to 20 calendars.")
		return
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, etag, modified, _, err := s.fetchFeed(fetchCtx, normalized, "", "")
	if err != nil {
		writeFeedError(w, err)
		return
	}
	objects, err := objectsFromFeed(body)
	if err != nil {
		writeFeedError(w, err)
		return
	}
	slug := secrets.Token(10)
	var collectionID string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO dav_collections (user_id, kind, slug, name, color) VALUES ($1, 'calendar', $2, $3, $4) RETURNING id`,
			p.User.ID, slug, cal.Name, cal.Color).Scan(&collectionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO calendar_subscriptions (collection_id, url, refresh_minutes, etag, last_modified, last_fetched_at, next_fetch_at)
			VALUES ($1, $2, $3, $4, $5, now(), now() + make_interval(mins => $3))`,
			collectionID, normalized, in.RefreshMinutes, etag, modified); err != nil {
			return err
		}
		return s.replaceFeedObjects(ctx, tx, collectionID, objects)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	subscriptionRefresh.WithLabelValues("ok").Inc()
	now := time.Now().UTC()
	writeJSON(w, http.StatusCreated, calendarInfo{
		ID: slug, Name: cal.Name, Color: cal.Color, Access: "owner", Subscribed: true,
		Subscription: &calendarSubscription{URL: normalized, RefreshMinutes: in.RefreshMinutes, LastFetchedAt: &now, Events: len(objects)},
	})
}

func writeFeedError(w http.ResponseWriter, err error) {
	var ff *feedFailure
	if errors.As(err, &ff) {
		writeError(w, ff.status, ff.code, ff.message)
		return
	}
	writeError(w, http.StatusUnprocessableEntity, "fetch_failed", "The calendar link could not be read. Check the address and try again.")
}

func (s *Server) ownedSubscription(ctx context.Context, userID, slug string) (subscriptionRow, error) {
	var row subscriptionRow
	err := s.db.QueryRow(ctx, `
		SELECT s.collection_id, s.url, s.refresh_minutes, s.etag, s.last_modified
		FROM calendar_subscriptions s JOIN dav_collections c ON c.id = s.collection_id
		WHERE c.user_id = $1 AND c.kind = 'calendar' AND c.slug = $2`, userID, slug).
		Scan(&row.CollectionID, &row.URL, &row.RefreshMinutes, &row.ETag, &row.LastModified)
	return row, err
}

func (s *Server) refreshSubscriptionNow(w http.ResponseWriter, r *http.Request, p *principal) {
	row, err := s.ownedSubscription(r.Context(), p.User.ID, r.PathValue("calendarId"))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	events, notModified, err := s.refreshSubscription(r.Context(), row)
	if err != nil {
		writeFeedError(w, err)
		return
	}
	var info calendarSubscription
	var fetched *time.Time
	if err := s.db.QueryRow(r.Context(), `SELECT url, refresh_minutes, last_fetched_at, last_error FROM calendar_subscriptions WHERE collection_id = $1`, row.CollectionID).
		Scan(&info.URL, &info.RefreshMinutes, &fetched, &info.LastError); err != nil {
		s.internalError(w, r, err)
		return
	}
	info.LastFetchedAt = fetched
	info.Events = events
	info.NotModified = notModified
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request, p *principal) {
	row, err := s.ownedSubscription(r.Context(), p.User.ID, r.PathValue("calendarId"))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var in struct {
		URL            string `json:"url"`
		RefreshMinutes int    `json:"refreshMinutes"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if in.RefreshMinutes == 0 {
		in.RefreshMinutes = row.RefreshMinutes
	}
	if !validRefresh(in.RefreshMinutes) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_refresh", "Choose a refresh interval from 15 minutes to 7 days, in steps of 5 minutes.")
		return
	}
	nextURL := row.URL
	if strings.TrimSpace(in.URL) != "" {
		normalized, err := normalizeFeedURL(in.URL, s.cfg.WebhookAllowPrivate)
		if err != nil {
			writeFeedError(w, err)
			return
		}
		nextURL = normalized
	}
	if _, err := s.db.Exec(r.Context(), `
		UPDATE calendar_subscriptions SET url = $2, refresh_minutes = $3, etag = CASE WHEN url = $2 THEN etag ELSE '' END,
			last_modified = CASE WHEN url = $2 THEN last_modified ELSE '' END, next_fetch_at = now()
		WHERE collection_id = $1`, row.CollectionID, nextURL, in.RefreshMinutes); err != nil {
		s.internalError(w, r, err)
		return
	}
	if nextURL != row.URL {
		row.URL, row.ETag, row.LastModified = nextURL, "", ""
		row.RefreshMinutes = in.RefreshMinutes
		if _, _, err := s.refreshSubscription(r.Context(), row); err != nil {
			writeFeedError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
