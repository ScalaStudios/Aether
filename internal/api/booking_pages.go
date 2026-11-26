package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/secrets"
)

const maxBookingTypes = 20

type bookingWindow struct {
	Weekday     int `json:"weekday"`
	StartMinute int `json:"startMinute"`
	EndMinute   int `json:"endMinute"`
}

type bookingTypeView struct {
	ID                  string          `json:"id"`
	Slug                string          `json:"slug"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	DurationMinutes     int             `json:"durationMinutes"`
	BufferBeforeMinutes int             `json:"bufferBeforeMinutes"`
	BufferAfterMinutes  int             `json:"bufferAfterMinutes"`
	Location            string          `json:"location"`
	VideoURL            string          `json:"videoUrl"`
	CalendarID          string          `json:"calendarId"`
	Active              bool            `json:"active"`
	Windows             []bookingWindow `json:"windows"`
}

type bookingPageView struct {
	Slug             string            `json:"slug"`
	Title            string            `json:"title"`
	Description      string            `json:"description"`
	Timezone         string            `json:"timezone"`
	MinNoticeMinutes int               `json:"minNoticeMinutes"`
	HorizonDays      int               `json:"horizonDays"`
	Active           bool              `json:"active"`
	URL              string            `json:"url"`
	Types            []bookingTypeView `json:"types"`
}

type bookingPageInput struct {
	Slug             string `json:"slug"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Timezone         string `json:"timezone"`
	MinNoticeMinutes int    `json:"minNoticeMinutes"`
	HorizonDays      int    `json:"horizonDays"`
	Active           *bool  `json:"active"`
}

type bookingTypeInput struct {
	Name                string          `json:"name"`
	Slug                string          `json:"slug"`
	Description         string          `json:"description"`
	DurationMinutes     int             `json:"durationMinutes"`
	BufferBeforeMinutes int             `json:"bufferBeforeMinutes"`
	BufferAfterMinutes  int             `json:"bufferAfterMinutes"`
	Location            string          `json:"location"`
	VideoURL            string          `json:"videoUrl"`
	CalendarID          string          `json:"calendarId"`
	Active              *bool           `json:"active"`
	Windows             []bookingWindow `json:"windows"`
}

type publicBookingType struct {
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	DurationMinutes int    `json:"durationMinutes"`
	Location        string `json:"location"`
	Video           bool   `json:"video"`
}

type publicBookingPage struct {
	Slug        string              `json:"slug"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	HostName    string              `json:"hostName"`
	Timezone    string              `json:"timezone"`
	HorizonDays int                 `json:"horizonDays"`
	Types       []publicBookingType `json:"types"`
}

type bookingConfirmation struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Timezone   string    `json:"timezone"`
	HostName   string    `json:"hostName"`
	Location   string    `json:"location"`
	VideoURL   string    `json:"videoUrl,omitempty"`
	GuestEmail string    `json:"guestEmail"`
}

func (s *Server) bookingURL(slug string) string {
	return strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/book/" + slug
}

func validTimezone(name string) (*time.Location, bool) {
	if name == "" || name == "Local" || len(name) > 100 || strings.Contains(name, "..") {
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil || loc == time.Local {
		return nil, false
	}
	return loc, true
}

func validVideoURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && len(raw) <= 2000
}

func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	if s == "" || !slugPattern.MatchString(s) {
		return "meet"
	}
	return s
}

func minuteField(n, min, max int) bool {
	return n >= min && n <= max && n%5 == 0
}

func normalizeWindows(windows []bookingWindow, duration int) ([]bookingWindow, string) {
	if len(windows) == 0 || len(windows) > 21 {
		return nil, "Add between 1 and 21 availability windows."
	}
	out := make([]bookingWindow, len(windows))
	copy(out, windows)
	fits := false
	for i := range out {
		w := out[i]
		if w.Weekday < 0 || w.Weekday > 6 || !minuteField(w.StartMinute, 0, 1435) || w.EndMinute > 1440 || w.EndMinute <= w.StartMinute || w.EndMinute%5 != 0 {
			return nil, "Each window needs a day and a start and end time, in 5-minute steps."
		}
		if w.EndMinute-w.StartMinute >= duration {
			fits = true
		}
	}
	if !fits {
		return nil, "The meeting has to fit inside at least one availability window."
	}
	for i := 1; i < len(out); i++ {
		w := out[i]
		j := i
		for j > 0 && (out[j-1].Weekday > w.Weekday || out[j-1].Weekday == w.Weekday && out[j-1].StartMinute > w.StartMinute) {
			out[j] = out[j-1]
			j--
		}
		out[j] = w
	}
	for i := 1; i < len(out); i++ {
		if out[i].Weekday == out[i-1].Weekday && out[i].StartMinute < out[i-1].EndMinute {
			return nil, "Availability windows on the same day can't overlap."
		}
	}
	return out, ""
}

func (s *Server) loadBookingPage(ctx context.Context, userID string) (*bookingPageView, error) {
	page := &bookingPageView{Types: []bookingTypeView{}}
	var pageID string
	err := s.db.QueryRow(ctx, `
		SELECT id, slug, title, description, timezone, min_notice_minutes, horizon_days, active
		FROM booking_pages WHERE user_id = $1`, userID).
		Scan(&pageID, &page.Slug, &page.Title, &page.Description, &page.Timezone, &page.MinNoticeMinutes, &page.HorizonDays, &page.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	page.URL = s.bookingURL(page.Slug)
	rows, err := s.db.Query(ctx, `
		SELECT id, slug, name, description, duration_minutes, buffer_before_minutes, buffer_after_minutes, location, video_url, calendar_slug, active
		FROM booking_types WHERE page_id = $1 ORDER BY position, created_at`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	index := map[string]int{}
	var ids []string
	for rows.Next() {
		var t bookingTypeView
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.Description, &t.DurationMinutes, &t.BufferBeforeMinutes, &t.BufferAfterMinutes, &t.Location, &t.VideoURL, &t.CalendarID, &t.Active); err != nil {
			return nil, err
		}
		t.Windows = []bookingWindow{}
		index[t.ID] = len(page.Types)
		ids = append(ids, t.ID)
		page.Types = append(page.Types, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return page, nil
	}
	windows, err := s.db.Query(ctx, `SELECT type_id, weekday, start_minute, end_minute FROM booking_windows WHERE type_id = ANY($1) ORDER BY weekday, start_minute`, ids)
	if err != nil {
		return nil, err
	}
	defer windows.Close()
	for windows.Next() {
		var id string
		var w bookingWindow
		if err := windows.Scan(&id, &w.Weekday, &w.StartMinute, &w.EndMinute); err != nil {
			return nil, err
		}
		page.Types[index[id]].Windows = append(page.Types[index[id]].Windows, w)
	}
	return page, windows.Err()
}

func (s *Server) getBookingPage(w http.ResponseWriter, r *http.Request, p *principal) {
	page, err := s.loadBookingPage(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) saveBookingPage(w http.ResponseWriter, r *http.Request, p *principal) {
	var in bookingPageInput
	if !decode(w, r, 16<<10, &in) {
		return
	}
	in.Slug = strings.TrimSpace(strings.ToLower(in.Slug))
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.Timezone = strings.TrimSpace(in.Timezone)
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	if !slugPattern.MatchString(in.Slug) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "The link can use lowercase letters, numbers and hyphens, up to 63 characters.")
		return
	}
	if in.Title == "" || len(in.Title) > 200 || len(in.Description) > 2000 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "Give the page a title of up to 200 characters.")
		return
	}
	if _, ok := validTimezone(in.Timezone); !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_timezone", "Choose a time zone like Europe/London or America/New_York.")
		return
	}
	if in.MinNoticeMinutes < 0 || in.MinNoticeMinutes > 10080 || in.HorizonDays < 1 || in.HorizonDays > 180 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "Notice can be up to 7 days, and people can book up to 180 days ahead.")
		return
	}
	var existed bool
	if err := s.db.QueryRow(r.Context(), `SELECT true FROM booking_pages WHERE user_id = $1`, p.User.ID).Scan(&existed); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, r, err)
		return
	}
	_, err := s.db.Exec(r.Context(), `
		INSERT INTO booking_pages (user_id, slug, title, description, timezone, min_notice_minutes, horizon_days, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id) DO UPDATE SET slug = EXCLUDED.slug, title = EXCLUDED.title, description = EXCLUDED.description,
			timezone = EXCLUDED.timezone, min_notice_minutes = EXCLUDED.min_notice_minutes, horizon_days = EXCLUDED.horizon_days,
			active = EXCLUDED.active, updated_at = now()`,
		p.User.ID, in.Slug, in.Title, in.Description, in.Timezone, in.MinNoticeMinutes, in.HorizonDays, active)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "slug_taken", "That link is already in use. Choose another.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page, err := s.loadBookingPage(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	status := http.StatusOK
	if !existed {
		status = http.StatusCreated
	}
	writeJSON(w, status, page)
}

func (s *Server) prepareBookingType(w http.ResponseWriter, in *bookingTypeInput) ([]bookingWindow, bool) {
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.TrimSpace(strings.ToLower(in.Slug))
	in.Description = strings.TrimSpace(in.Description)
	in.Location = strings.TrimSpace(in.Location)
	in.VideoURL = strings.TrimSpace(in.VideoURL)
	in.CalendarID = strings.TrimSpace(in.CalendarID)
	if in.CalendarID == "" {
		in.CalendarID = "personal"
	}
	if in.Active == nil {
		active := true
		in.Active = &active
	}
	if in.Slug == "" {
		in.Slug = slugify(in.Name)
	}
	switch {
	case in.Name == "" || len(in.Name) > 100:
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "Give the meeting a name of up to 100 characters.")
	case !slugPattern.MatchString(in.Slug):
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "The meeting link can use lowercase letters, numbers and hyphens.")
	case len(in.Description) > 2000 || len(in.Location) > 500:
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "Shorten the description or the location.")
	case !minuteField(in.DurationMinutes, 5, 480):
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "Duration is between 5 minutes and 8 hours, in steps of 5 minutes.")
	case !minuteField(in.BufferBeforeMinutes, 0, 240) || !minuteField(in.BufferAfterMinutes, 0, 240):
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "Buffers are between 0 and 4 hours, in steps of 5 minutes.")
	case !validVideoURL(in.VideoURL):
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "The video link must be an https:// address.")
	default:
		windows, msg := normalizeWindows(in.Windows, in.DurationMinutes)
		if msg != "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_booking", msg)
			return nil, false
		}
		return windows, true
	}
	return nil, false
}

func (s *Server) ownedCalendarSlug(ctx context.Context, userID, slug string) bool {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM dav_collections WHERE user_id = $1 AND kind = 'calendar' AND slug = $2`, userID, slug).Scan(&id)
	if err != nil {
		return false
	}
	var subscribed bool
	if err := s.db.QueryRow(ctx, `SELECT true FROM calendar_subscriptions WHERE collection_id = $1`, id).Scan(&subscribed); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	return !subscribed
}

func (s *Server) pageID(ctx context.Context, userID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM booking_pages WHERE user_id = $1`, userID).Scan(&id)
	return id, err
}

func insertWindows(ctx context.Context, tx pgx.Tx, typeID string, windows []bookingWindow) error {
	if _, err := tx.Exec(ctx, `DELETE FROM booking_windows WHERE type_id = $1`, typeID); err != nil {
		return err
	}
	for _, w := range windows {
		if _, err := tx.Exec(ctx, `INSERT INTO booking_windows (type_id, weekday, start_minute, end_minute) VALUES ($1, $2, $3, $4)`, typeID, w.Weekday, w.StartMinute, w.EndMinute); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) createBookingType(w http.ResponseWriter, r *http.Request, p *principal) {
	var in bookingTypeInput
	windows, ok := []bookingWindow{}, false
	if !decode(w, r, 32<<10, &in) {
		return
	}
	if windows, ok = s.prepareBookingType(w, &in); !ok {
		return
	}
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !s.ownedCalendarSlug(ctx, p.User.ID, in.CalendarID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_calendar", "Choose one of your calendars. Subscribed calendars can't hold bookings.")
		return
	}
	pageID, err := s.pageID(ctx, p.User.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnprocessableEntity, "no_booking_page", "Set up the booking page before adding a meeting type.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM booking_types WHERE page_id = $1`, pageID).Scan(&count); err != nil {
		s.internalError(w, r, err)
		return
	}
	if count >= maxBookingTypes {
		writeError(w, http.StatusUnprocessableEntity, "too_many_types", "A booking page can have 20 meeting types.")
		return
	}
	var taken []string
	takenRows, err := s.db.Query(ctx, `SELECT slug FROM booking_types WHERE page_id = $1`, pageID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	taken, err = pgx.CollectRows(takenRows, pgx.RowTo[string])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	used := map[string]bool{}
	for _, slug := range taken {
		used[slug] = true
	}
	if used[in.Slug] && strings.TrimSpace(strings.ToLower(in.Slug)) == slugify(in.Name) {
		in.Slug = uniqueSlug(in.Slug, used)
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO booking_types (page_id, slug, name, description, duration_minutes, buffer_before_minutes, buffer_after_minutes, location, video_url, calendar_slug, active, position)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, (SELECT coalesce(max(position), 0) + 1 FROM booking_types WHERE page_id = $1))
			RETURNING id`,
			pageID, in.Slug, in.Name, in.Description, in.DurationMinutes, in.BufferBeforeMinutes, in.BufferAfterMinutes, in.Location, in.VideoURL, in.CalendarID, *in.Active).Scan(&id); err != nil {
			return err
		}
		return insertWindows(ctx, tx, id, windows)
	})
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "slug_taken", "Another meeting on this page already uses that link.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page, err := s.loadBookingPage(ctx, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, page)
}

func uniqueSlug(base string, used map[string]bool) string {
	for i := 2; i < 50; i++ {
		s := base + "-" + strconv.Itoa(i)
		if len(s) > 63 {
			s = strings.Trim(s[:63], "-")
		}
		if slugPattern.MatchString(s) && !used[s] {
			return s
		}
	}
	return slugify(base + "-" + secrets.Token(2))
}

func (s *Server) updateBookingType(w http.ResponseWriter, r *http.Request, p *principal) {
	typeID, ok := pathID(w, r, "typeId")
	if !ok {
		return
	}
	var in bookingTypeInput
	if !decode(w, r, 32<<10, &in) {
		return
	}
	windows, ok := s.prepareBookingType(w, &in)
	if !ok {
		return
	}
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !s.ownedCalendarSlug(ctx, p.User.ID, in.CalendarID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_calendar", "Choose one of your calendars. Subscribed calendars can't hold bookings.")
		return
	}
	var pageID string
	err = s.db.QueryRow(ctx, `
		SELECT t.page_id FROM booking_types t JOIN booking_pages p ON p.id = t.page_id
		WHERE t.id = $1 AND p.user_id = $2`, typeID, p.User.ID).Scan(&pageID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE booking_types SET slug = $3, name = $4, description = $5, duration_minutes = $6, buffer_before_minutes = $7,
				buffer_after_minutes = $8, location = $9, video_url = $10, calendar_slug = $11, active = $12
			WHERE id = $1 AND page_id = $2`,
			typeID, pageID, in.Slug, in.Name, in.Description, in.DurationMinutes, in.BufferBeforeMinutes, in.BufferAfterMinutes, in.Location, in.VideoURL, in.CalendarID, *in.Active)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return insertWindows(ctx, tx, typeID, windows)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "slug_taken", "Another meeting on this page already uses that link.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page, err := s.loadBookingPage(ctx, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) deleteBookingType(w http.ResponseWriter, r *http.Request, p *principal) {
	typeID, ok := pathID(w, r, "typeId")
	if !ok {
		return
	}
	tag, err := s.db.Exec(r.Context(), `
		DELETE FROM booking_types t USING booking_pages p
		WHERE t.id = $1 AND t.page_id = p.id AND p.user_id = $2`, typeID, p.User.ID)
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

func (s *Server) userAddresses(ctx context.Context, userID string) (map[string]bool, error) {
	out := map[string]bool{}
	var email string
	if err := s.db.QueryRow(ctx, `SELECT lower(email) FROM users WHERE id = $1`, userID).Scan(&email); err != nil {
		return nil, err
	}
	out[email] = true
	rows, err := s.db.Query(ctx, `SELECT lower(address) FROM mailboxes WHERE owner_user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return nil, err
		}
		out[address] = true
	}
	return out, rows.Err()
}

func declinedBy(e calendarEvent, addresses map[string]bool) bool {
	for _, a := range e.Attendees {
		if addresses[a.Address] && a.Status == "declined" {
			return true
		}
	}
	return false
}

func (s *Server) hostBusy(ctx context.Context, userID string, from, to time.Time) ([]timeInterval, error) {
	davCtx := contextWithDAVUser(ctx, userID)
	if err := s.ensureDAVDefaults(davCtx, userID); err != nil {
		return nil, err
	}
	calRows, err := s.db.Query(ctx, `
		SELECT c.id, c.slug, c.name, c.description, c.color, c.updated_at, 'owner' AS access
		FROM dav_collections c
		WHERE c.user_id = $1 AND c.kind = 'calendar'
			AND NOT EXISTS (SELECT 1 FROM calendar_subscriptions s WHERE s.collection_id = c.id)`, userID)
	if err != nil {
		return nil, err
	}
	calendars, err := pgx.CollectRows(calRows, pgx.RowToStructByPos[collectionRow])
	if err != nil {
		return nil, err
	}
	padFrom, padTo := from.Add(-4*time.Hour), to.Add(4*time.Hour)
	events, err := eventsIn(davCtx, s.db, calendars, padFrom, padTo)
	if err != nil {
		return nil, err
	}
	addresses, err := s.userAddresses(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []timeInterval{}
	for _, e := range events {
		if e.transparent || e.cancelled || declinedBy(e, addresses) {
			continue
		}
		out = append(out, timeInterval{Start: e.Start, End: e.End})
	}
	return out, nil
}

func contextWithDAVUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, davUserKey{}, userID)
}

type bookOffer struct {
	UserID    string
	HostEmail string
	HostName  string
	PageTitle string
	Timezone  string
	Notice    int
	Horizon   int
	TypeID    string
	TypeName  string
	Duration  int
	Before    int
	After     int
	Location  string
	VideoURL  string
	Calendar  string
	Windows   []availabilityWindow
}

func (s *Server) loadOffer(ctx context.Context, slug, typeSlug string) (*bookOffer, error) {
	offer := &bookOffer{}
	err := s.db.QueryRow(ctx, `
		SELECT p.user_id, u.email, coalesce(nullif(u.display_name, ''), u.email), p.title, p.timezone, p.min_notice_minutes, p.horizon_days,
			t.id, t.name, t.duration_minutes, t.buffer_before_minutes, t.buffer_after_minutes, t.location, t.video_url, t.calendar_slug
		FROM booking_pages p
		JOIN users u ON u.id = p.user_id
		JOIN booking_types t ON t.page_id = p.id
		WHERE p.slug = $1 AND p.active AND t.slug = $2 AND t.active AND u.status = 'active'`, slug, typeSlug).
		Scan(&offer.UserID, &offer.HostEmail, &offer.HostName, &offer.PageTitle, &offer.Timezone, &offer.Notice, &offer.Horizon, &offer.TypeID, &offer.TypeName, &offer.Duration, &offer.Before, &offer.After, &offer.Location, &offer.VideoURL, &offer.Calendar)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT weekday, start_minute, end_minute FROM booking_windows WHERE type_id = $1 ORDER BY weekday, start_minute`, offer.TypeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var w availabilityWindow
		if err := rows.Scan(&w.Weekday, &w.StartMinute, &w.EndMinute); err != nil {
			return nil, err
		}
		offer.Windows = append(offer.Windows, w)
	}
	return offer, rows.Err()
}

func (s *Server) publicBookingPage(w http.ResponseWriter, r *http.Request) {
	if !s.allow(r.Context(), "book-read:"+s.clientIP(r), 60) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many booking requests. Wait a minute and try again.")
		return
	}
	slug := strings.ToLower(r.PathValue("slug"))
	var page publicBookingPage
	var userID string
	err := s.db.QueryRow(r.Context(), `
		SELECT p.slug, p.title, p.description, coalesce(nullif(u.display_name, ''), u.email), p.timezone, p.horizon_days, p.user_id
		FROM booking_pages p JOIN users u ON u.id = p.user_id
		WHERE p.slug = $1 AND p.active AND u.status = 'active'`, slug).Scan(&page.Slug, &page.Title, &page.Description, &page.HostName, &page.Timezone, &page.HorizonDays, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT slug, name, description, duration_minutes, location, video_url <> ''
		FROM booking_types WHERE page_id = (SELECT id FROM booking_pages WHERE user_id = $1) AND active
		ORDER BY position, created_at`, userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page.Types = []publicBookingType{}
	page.Types, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (publicBookingType, error) {
		var t publicBookingType
		return t, row.Scan(&t.Slug, &t.Name, &t.Description, &t.DurationMinutes, &t.Location, &t.Video)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) publicBookingSlots(w http.ResponseWriter, r *http.Request) {
	if !s.allow(r.Context(), "book-read:"+s.clientIP(r), 60) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many booking requests. Wait a minute and try again.")
		return
	}
	offer, err := s.loadOffer(r.Context(), strings.ToLower(r.PathValue("slug")), strings.ToLower(r.PathValue("typeSlug")))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	from, err1 := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
	to, err2 := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
	if err1 != nil || err2 != nil || !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		writeError(w, http.StatusUnprocessableEntity, "invalid_range", "Give start and end as RFC 3339 times, at most 31 days apart.")
		return
	}
	loc, ok := validTimezone(offer.Timezone)
	if !ok {
		s.internalError(w, r, errors.New("booking page has an unknown time zone"))
		return
	}
	busy, err := s.hostBusy(r.Context(), offer.UserID, from, to)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	slots := bookingSlots(time.Now(), from, to, loc, offer.Duration, offer.Before, offer.After, offer.Notice, offer.Horizon, offer.Windows, busy)
	writeJSON(w, http.StatusOK, map[string]any{"timezone": offer.Timezone, "durationMinutes": offer.Duration, "slots": slots})
}

func bookingCalendar(event *ical.Component, start, end time.Time, loc *time.Location) *ical.Calendar {
	event.Props.SetDateTime(ical.PropDateTimeStart, start.In(loc))
	event.Props.SetDateTime(ical.PropDateTimeEnd, end.In(loc))
	doc := newCalendar()
	doc.Children = append(doc.Children, event)
	addTimezones(doc)
	return doc
}

func (s *Server) bookMeeting(w http.ResponseWriter, r *http.Request) {
	if !s.allow(r.Context(), "book:"+s.clientIP(r), 10) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many booking requests. Wait a minute and try again.")
		return
	}
	var in struct {
		Name  string    `json:"name"`
		Email string    `json:"email"`
		Start time.Time `json:"start"`
		Note  string    `json:"note"`
	}
	if !decode(w, r, 16<<10, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Note = strings.TrimSpace(in.Note)
	addr, err := mail.ParseAddress(strings.TrimSpace(in.Email))
	if in.Name == "" || len(in.Name) > 200 || err != nil || !strings.Contains(addr.Address, "@") || len(in.Note) > 2000 || in.Start.IsZero() {
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "Enter your name and email address, and choose a time.")
		return
	}
	email := strings.ToLower(addr.Address)
	if !s.allow(r.Context(), "book-email:"+email, 5) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many booking requests. Wait a minute and try again.")
		return
	}
	ctx := r.Context()
	offer, err := s.loadOffer(ctx, strings.ToLower(r.PathValue("slug")), strings.ToLower(r.PathValue("typeSlug")))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	loc, ok := validTimezone(offer.Timezone)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "unavailable", "This page can't accept bookings right now.")
		return
	}
	host := User{ID: offer.UserID, Email: offer.HostEmail, DisplayName: offer.HostName}
	organizer, err := s.organizerMailbox(ctx, host)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if organizer == nil {
		writeError(w, http.StatusUnprocessableEntity, "unavailable", "This page can't accept bookings right now.")
		return
	}
	if organizer.DisplayName == "" {
		organizer.DisplayName = offer.HostName
	}
	if email == strings.ToLower(organizer.Address) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking", "Use an email address that isn't the host's.")
		return
	}
	davCtx := contextWithDAVUser(ctx, offer.UserID)
	if err := s.ensureDAVDefaults(davCtx, offer.UserID); err != nil {
		s.internalError(w, r, err)
		return
	}
	var calendarID string
	err = s.db.QueryRow(ctx, `SELECT id FROM dav_collections WHERE user_id = $1 AND kind = 'calendar' AND slug = $2`, offer.UserID, offer.Calendar).Scan(&calendarID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnprocessableEntity, "unavailable", "This page can't accept bookings right now.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var subscribed bool
	err = s.db.QueryRow(ctx, `SELECT true FROM calendar_subscriptions WHERE collection_id = $1`, calendarID).Scan(&subscribed)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if subscribed {
		writeError(w, http.StatusUnprocessableEntity, "unavailable", "This page can't accept bookings right now.")
		return
	}
	start := in.Start.UTC()
	end := start.Add(time.Duration(offer.Duration) * time.Minute)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text), $2)`, offer.UserID, subscriptionLockKey); err != nil {
		s.internalError(w, r, err)
		return
	}
	busy, err := s.hostBusy(ctx, offer.UserID, start.Add(-time.Minute), end.Add(time.Minute))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	slots := bookingSlots(time.Now(), start.Add(-time.Second), start.Add(time.Second), loc, offer.Duration, offer.Before, offer.After, offer.Notice, offer.Horizon, offer.Windows, busy)
	available := false
	for _, slot := range slots {
		if slot.Equal(start) {
			available = true
			break
		}
	}
	if !available {
		writeError(w, http.StatusConflict, "slot_unavailable", "That time is no longer available. Choose another.")
		return
	}
	title := offer.TypeName + " with " + in.Name
	if len(title) > 300 {
		title = title[:300]
	}
	var description strings.Builder
	if in.Note != "" {
		description.WriteString(in.Note)
		description.WriteString("\n\n")
	}
	description.WriteString("Booked with " + offer.HostName + ".")
	if offer.VideoURL != "" {
		description.WriteString("\nVideo: " + offer.VideoURL)
	}
	uid := secrets.Token(16)
	name := uid + ".ics"
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, uid)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	event.Props.SetText(ical.PropSummary, title)
	if offer.Location != "" {
		event.Props.SetText(ical.PropLocation, offer.Location)
	} else if offer.VideoURL != "" {
		event.Props.SetText(ical.PropLocation, offer.VideoURL)
	}
	event.Props.SetText(ical.PropDescription, description.String())
	if offer.VideoURL != "" {
		if video, err := url.Parse(offer.VideoURL); err == nil {
			event.Props.SetURI(ical.PropURL, video)
		}
	}
	event.Props.Set(organizerProp(mailpkg.Address{Name: organizer.DisplayName, Address: strings.ToLower(organizer.Address)}))
	guest := mailpkg.Address{Name: in.Name, Address: email}
	attendee := attendeeProp(guest, "NEEDS-ACTION", true)
	event.Props.Add(&attendee)
	doc := bookingCalendar(event.Component, start, end, loc)
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
	row := &objectRow{Name: name, Data: buf.String(), ETag: etagOf(buf.String()), UpdatedAt: time.Now()}
	if err := insertDAVObject(ctx, tx, calendarID, row, fields, "", ""); err != nil {
		s.internalError(w, r, err)
		return
	}
	var bookingID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO bookings (type_id, host_user_id, event_uid, event_name, guest_name, guest_email, note, starts_at, ends_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		offer.TypeID, offer.UserID, uid, name, in.Name, email, in.Note, start, end).Scan(&bookingID); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.internalError(w, r, err)
		return
	}
	if _, err := s.sendITIP(ctx, organizer, nil, []mailpkg.Address{guest}, "request", title, "REQUEST", event.Component); err != nil {
		if _, delErr := s.db.Exec(ctx, `DELETE FROM bookings WHERE id = $1`, bookingID); delErr != nil {
			s.log.Error("booked meeting could not be rolled back", "booking", bookingID, "err", delErr)
			writeError(w, http.StatusBadGateway, "send_failed", "The time was reserved, but the invitation email could not be sent. The host has it on their calendar.")
			return
		}
		if _, delErr := s.db.Exec(ctx, `DELETE FROM dav_objects WHERE collection_id = $1 AND name = $2`, calendarID, name); delErr != nil {
			s.log.Error("booked event could not be rolled back", "calendar", calendarID, "err", delErr)
		}
		s.log.Warn("booking invitation rejected", "user", offer.UserID, "err", err)
		writeError(w, http.StatusBadGateway, "send_failed", "The invitation could not be sent, so the time was not reserved. Try again.")
		return
	}
	writeJSON(w, http.StatusCreated, bookingConfirmation{
		ID: bookingID, Title: title, Start: start, End: end, Timezone: offer.Timezone, HostName: offer.HostName,
		Location: offer.Location, VideoURL: offer.VideoURL, GuestEmail: email,
	})
}
