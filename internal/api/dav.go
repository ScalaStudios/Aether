package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

const (
	davPrefix         = "/dav"
	davCalendars      = "calendars"
	davContacts       = "contacts"
	maxDAVObjectBytes = 1 << 20
)

type davUserKey struct{}

func davUser(ctx context.Context) string {
	return ctx.Value(davUserKey{}).(string)
}

func davNotFound() error {
	return webdav.NewHTTPError(http.StatusNotFound, errors.New("not found"))
}

func (s *Server) davAuthenticate(r *http.Request) (string, bool) {
	username, password, ok := r.BasicAuth()
	if !ok || password == "" {
		return "", false
	}
	var userID string
	err := s.db.QueryRow(r.Context(), `
		SELECT u.id FROM app_passwords p
		JOIN mailboxes m ON m.id = p.mailbox_id
		JOIN users u ON u.id = p.user_id
		WHERE m.address = lower($1) AND p.token_hash = $2 AND p.revoked_at IS NULL AND m.active AND u.status = 'active' AND m.owner_user_id = u.id`,
		strings.TrimSpace(username), secrets.SHA256Hex(password)).Scan(&userID)
	return userID, err == nil
}

func (s *Server) serveDAV(w http.ResponseWriter, r *http.Request) {
	if !s.allow(r.Context(), "dav-ip:"+s.clientIP(r), 600) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	userID, ok := s.davAuthenticate(r)
	if !ok && r.Header.Get("Authorization") != "" {
		authAttempts.WithLabelValues("dav", "failure").Inc()
	}
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="Aether", charset="UTF-8"`)
		http.Error(w, "Sign in with your mailbox address and an app password.", http.StatusUnauthorized)
		return
	}
	ctx := context.WithValue(r.Context(), davUserKey{}, userID)
	r = r.WithContext(ctx)
	if err := s.ensureDAVDefaults(ctx, userID); err != nil {
		s.internalError(w, r, err)
		return
	}
	principal := davPrefix + "/" + userID + "/"
	if r.URL.Path == "/.well-known/caldav" || r.URL.Path == "/.well-known/carddav" || r.URL.Path == davPrefix || r.URL.Path == davPrefix+"/" {
		http.Redirect(w, r, principal, http.StatusPermanentRedirect)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, davPrefix+"/")
	owner, sub, _ := strings.Cut(rest, "/")
	if owner != userID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	section, _, _ := strings.Cut(sub, "/")
	switch section {
	case "":
		webdav.ServePrincipal(w, r, &webdav.ServePrincipalOptions{
			CurrentUserPrincipalPath: principal,
			HomeSets: []webdav.BackendSuppliedHomeSet{
				caldav.NewCalendarHomeSet(principal + davCalendars + "/"),
				carddav.NewAddressBookHomeSet(principal + davContacts + "/"),
			},
			Capabilities: []webdav.Capability{caldav.CapabilityCalendar, carddav.CapabilityAddressBook},
		})
	case davCalendars:
		(&caldav.Handler{Backend: &calendarBackend{s}, Prefix: davPrefix}).ServeHTTP(w, r)
	case davContacts:
		(&carddav.Handler{Backend: &contactsBackend{s}, Prefix: davPrefix}).ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) ensureDAVDefaults(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO dav_collections (user_id, kind, slug, name) VALUES ($1, 'calendar', 'personal', 'Calendar'), ($1, 'addressbook', 'contacts', 'Contacts')
		ON CONFLICT (user_id, kind, slug) DO NOTHING`, userID)
	return err
}

type davObjectPath struct {
	section, slug, name string
}

func parseDAVPath(ctx context.Context, p, section string) (davObjectPath, error) {
	base := davPrefix + "/" + davUser(ctx) + "/" + section
	clean := path.Clean(p)
	if !strings.HasPrefix(clean, base+"/") {
		return davObjectPath{}, davNotFound()
	}
	parts := strings.Split(strings.TrimPrefix(clean, base+"/"), "/")
	if len(parts) > 2 || parts[0] == "" {
		return davObjectPath{}, davNotFound()
	}
	out := davObjectPath{section: section, slug: parts[0]}
	if len(parts) == 2 {
		out.name = parts[1]
	}
	return out, nil
}

type collectionRow struct {
	ID, Slug, Name, Description string
	UpdatedAt                   time.Time
}

func (s *Server) davCollections(ctx context.Context, kind string) ([]collectionRow, error) {
	rows, err := s.db.Query(ctx, `SELECT id, slug, name, description, updated_at FROM dav_collections WHERE user_id = $1 AND kind = $2 ORDER BY created_at`, davUser(ctx), kind)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[collectionRow])
}

func (s *Server) davCollection(ctx context.Context, kind, slug string) (*collectionRow, error) {
	var c collectionRow
	err := s.db.QueryRow(ctx, `SELECT id, slug, name, description, updated_at FROM dav_collections WHERE user_id = $1 AND kind = $2 AND slug = $3`, davUser(ctx), kind, slug).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, davNotFound()
	}
	return &c, err
}

func (s *Server) createDAVCollection(ctx context.Context, kind, p, name, description string) error {
	section := map[string]string{"calendar": davCalendars, "addressbook": davContacts}[kind]
	dp, err := parseDAVPath(ctx, p, section)
	if err != nil || dp.name != "" {
		return webdav.NewHTTPError(http.StatusForbidden, errors.New("collections can only be created in the home set"))
	}
	_, err = s.db.Exec(ctx, `INSERT INTO dav_collections (user_id, kind, slug, name, description) VALUES ($1, $2, $3, $4, $5)`, davUser(ctx), kind, dp.slug, name, description)
	if isUniqueViolation(err) {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, errors.New("collection exists"))
	}
	return err
}

type objectRow struct {
	Name, ETag, Data string
	UpdatedAt        time.Time
}

func (s *Server) davObjects(ctx context.Context, collectionID, name string) ([]objectRow, error) {
	rows, err := s.db.Query(ctx, `SELECT name, etag, data, updated_at FROM dav_objects WHERE collection_id = $1 AND ($2 = '' OR name = $2) ORDER BY name`, collectionID, name)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[objectRow])
}

type objectFields struct {
	UID       string
	Summary   string
	Emails    []string
	StartsAt  *time.Time
	EndsAt    *time.Time
	Recurring bool
}

func etagOf(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:16])
}

func (s *Server) putDAVObject(ctx context.Context, collectionID, name, data string, f objectFields, ifMatch, ifNoneMatch webdav.ConditionalMatch) (*objectRow, error) {
	if len(data) > maxDAVObjectBytes {
		return nil, webdav.NewHTTPError(http.StatusRequestEntityTooLarge, errors.New("object too large"))
	}
	if f.UID == "" {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, errors.New("object has no UID"))
	}
	row := &objectRow{Name: name, Data: data, ETag: etagOf(data), UpdatedAt: time.Now()}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var current string
		err := tx.QueryRow(ctx, `SELECT etag FROM dav_objects WHERE collection_id = $1 AND name = $2 FOR UPDATE`, collectionID, name).Scan(&current)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if ifNoneMatch.IsWildcard() && exists {
			return webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("object already exists"))
		}
		if ifMatch.IsSet() {
			want, err := ifMatch.ETag()
			if !ifMatch.IsWildcard() && (err != nil || !exists || want != current) || ifMatch.IsWildcard() && !exists {
				return webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("object changed"))
			}
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO dav_objects (collection_id, name, uid, etag, data, summary, emails, starts_at, ends_at, recurring)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (collection_id, name) DO UPDATE SET uid = EXCLUDED.uid, etag = EXCLUDED.etag, data = EXCLUDED.data, summary = EXCLUDED.summary,
				emails = EXCLUDED.emails, starts_at = EXCLUDED.starts_at, ends_at = EXCLUDED.ends_at, recurring = EXCLUDED.recurring, updated_at = now()`,
			collectionID, name, f.UID, row.ETag, data, f.Summary, nonNil(f.Emails), f.StartsAt, f.EndsAt, f.Recurring)
		if isUniqueViolation(err) {
			return webdav.NewHTTPError(http.StatusConflict, errors.New("another object in this collection has the same UID"))
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE dav_collections SET updated_at = now() WHERE id = $1`, collectionID)
		return err
	})
	return row, err
}

func (s *Server) deleteDAVObject(ctx context.Context, collectionID, name string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM dav_objects WHERE collection_id = $1 AND name = $2`, collectionID, name)
	if err == nil && tag.RowsAffected() == 0 {
		return davNotFound()
	}
	return err
}

type calendarBackend struct{ s *Server }

func (b *calendarBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return davPrefix + "/" + davUser(ctx) + "/", nil
}

func (b *calendarBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return davPrefix + "/" + davUser(ctx) + "/" + davCalendars + "/", nil
}

func (b *calendarBackend) calendarPath(ctx context.Context, slug string) string {
	return davPrefix + "/" + davUser(ctx) + "/" + davCalendars + "/" + slug + "/"
}

func (b *calendarBackend) toCalendar(ctx context.Context, c collectionRow) caldav.Calendar {
	return caldav.Calendar{Path: b.calendarPath(ctx, c.Slug), Name: c.Name, Description: c.Description, MaxResourceSize: maxDAVObjectBytes, SupportedComponentSet: []string{ical.CompEvent, ical.CompToDo}}
}

func (b *calendarBackend) CreateCalendar(ctx context.Context, c *caldav.Calendar) error {
	return b.s.createDAVCollection(ctx, "calendar", c.Path, c.Name, c.Description)
}

func (b *calendarBackend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	rows, err := b.s.davCollections(ctx, "calendar")
	out := make([]caldav.Calendar, len(rows))
	for i, c := range rows {
		out[i] = b.toCalendar(ctx, c)
	}
	return out, err
}

func (b *calendarBackend) GetCalendar(ctx context.Context, p string) (*caldav.Calendar, error) {
	dp, err := parseDAVPath(ctx, p, davCalendars)
	if err != nil {
		return nil, err
	}
	c, err := b.s.davCollection(ctx, "calendar", dp.slug)
	if err != nil {
		return nil, err
	}
	cal := b.toCalendar(ctx, *c)
	return &cal, nil
}

func (b *calendarBackend) objects(ctx context.Context, p string, single bool) ([]caldav.CalendarObject, error) {
	dp, err := parseDAVPath(ctx, p, davCalendars)
	if err != nil {
		return nil, err
	}
	if single && dp.name == "" {
		return nil, davNotFound()
	}
	c, err := b.s.davCollection(ctx, "calendar", dp.slug)
	if err != nil {
		return nil, err
	}
	rows, err := b.s.davObjects(ctx, c.ID, dp.name)
	if err != nil {
		return nil, err
	}
	if single && len(rows) == 0 {
		return nil, davNotFound()
	}
	out := make([]caldav.CalendarObject, 0, len(rows))
	for _, row := range rows {
		cal, err := ical.NewDecoder(strings.NewReader(row.Data)).Decode()
		if err != nil {
			continue
		}
		out = append(out, caldav.CalendarObject{Path: b.calendarPath(ctx, c.Slug) + row.Name, ModTime: row.UpdatedAt, ContentLength: int64(len(row.Data)), ETag: row.ETag, Data: cal})
	}
	return out, nil
}

func (b *calendarBackend) GetCalendarObject(ctx context.Context, p string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	objs, err := b.objects(ctx, p, true)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, davNotFound()
	}
	return &objs[0], nil
}

func (b *calendarBackend) ListCalendarObjects(ctx context.Context, p string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	return b.objects(ctx, p, false)
}

func (b *calendarBackend) QueryCalendarObjects(ctx context.Context, p string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	objs, err := b.objects(ctx, p, false)
	if err != nil {
		return nil, err
	}
	return caldav.Filter(query, objs)
}

func calendarFields(cal *ical.Calendar) (objectFields, error) {
	var f objectFields
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent && comp.Name != ical.CompToDo {
			continue
		}
		uid, err := comp.Props.Text(ical.PropUID)
		if err != nil || uid == "" {
			return f, webdav.NewHTTPError(http.StatusBadRequest, errors.New("component without UID"))
		}
		if f.UID != "" && f.UID != uid {
			return f, webdav.NewHTTPError(http.StatusBadRequest, errors.New("a calendar object must contain a single UID"))
		}
		f.UID = uid
		if comp.Props.Get(ical.PropRecurrenceID) != nil {
			continue
		}
		f.Summary, _ = comp.Props.Text(ical.PropSummary)
		f.Recurring = comp.Props.Get(ical.PropRecurrenceRule) != nil
		ev := ical.Event{Component: comp}
		if start, err := ev.DateTimeStart(time.UTC); err == nil && !start.IsZero() {
			f.StartsAt = &start
		}
		if end, err := ev.DateTimeEnd(time.UTC); err == nil && !end.IsZero() {
			f.EndsAt = &end
		}
	}
	if f.UID == "" {
		return f, webdav.NewHTTPError(http.StatusBadRequest, errors.New("no event or task in the calendar object"))
	}
	return f, nil
}

func (b *calendarBackend) PutCalendarObject(ctx context.Context, p string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	dp, err := parseDAVPath(ctx, p, davCalendars)
	if err != nil || dp.name == "" {
		return nil, davNotFound()
	}
	c, err := b.s.davCollection(ctx, "calendar", dp.slug)
	if err != nil {
		return nil, err
	}
	f, err := calendarFields(cal)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	row, err := b.s.putDAVObject(ctx, c.ID, dp.name, buf.String(), f, opts.IfMatch, opts.IfNoneMatch)
	if err != nil {
		return nil, err
	}
	return &caldav.CalendarObject{Path: b.calendarPath(ctx, c.Slug) + row.Name, ModTime: row.UpdatedAt, ContentLength: int64(len(row.Data)), ETag: row.ETag, Data: cal}, nil
}

func (b *calendarBackend) DeleteCalendarObject(ctx context.Context, p string) error {
	dp, err := parseDAVPath(ctx, p, davCalendars)
	if err != nil || dp.name == "" {
		return davNotFound()
	}
	c, err := b.s.davCollection(ctx, "calendar", dp.slug)
	if err != nil {
		return err
	}
	return b.s.deleteDAVObject(ctx, c.ID, dp.name)
}

type contactsBackend struct{ s *Server }

func (b *contactsBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return davPrefix + "/" + davUser(ctx) + "/", nil
}

func (b *contactsBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return davPrefix + "/" + davUser(ctx) + "/" + davContacts + "/", nil
}

func (b *contactsBackend) bookPath(ctx context.Context, slug string) string {
	return davPrefix + "/" + davUser(ctx) + "/" + davContacts + "/" + slug + "/"
}

func (b *contactsBackend) toBook(ctx context.Context, c collectionRow) carddav.AddressBook {
	return carddav.AddressBook{Path: b.bookPath(ctx, c.Slug), Name: c.Name, Description: c.Description, MaxResourceSize: maxDAVObjectBytes,
		SupportedAddressData: []carddav.AddressDataType{{ContentType: vcard.MIMEType, Version: "3.0"}, {ContentType: vcard.MIMEType, Version: "4.0"}}}
}

func (b *contactsBackend) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	rows, err := b.s.davCollections(ctx, "addressbook")
	out := make([]carddav.AddressBook, len(rows))
	for i, c := range rows {
		out[i] = b.toBook(ctx, c)
	}
	return out, err
}

func (b *contactsBackend) GetAddressBook(ctx context.Context, p string) (*carddav.AddressBook, error) {
	dp, err := parseDAVPath(ctx, p, davContacts)
	if err != nil {
		return nil, err
	}
	c, err := b.s.davCollection(ctx, "addressbook", dp.slug)
	if err != nil {
		return nil, err
	}
	book := b.toBook(ctx, *c)
	return &book, nil
}

func (b *contactsBackend) CreateAddressBook(ctx context.Context, book *carddav.AddressBook) error {
	return b.s.createDAVCollection(ctx, "addressbook", book.Path, book.Name, book.Description)
}

func (b *contactsBackend) DeleteAddressBook(ctx context.Context, p string) error {
	dp, err := parseDAVPath(ctx, p, davContacts)
	if err != nil || dp.name != "" {
		return davNotFound()
	}
	tag, err := b.s.db.Exec(ctx, `DELETE FROM dav_collections WHERE user_id = $1 AND kind = 'addressbook' AND slug = $2`, davUser(ctx), dp.slug)
	if err == nil && tag.RowsAffected() == 0 {
		return davNotFound()
	}
	return err
}

func (b *contactsBackend) objects(ctx context.Context, p string, single bool) ([]carddav.AddressObject, error) {
	dp, err := parseDAVPath(ctx, p, davContacts)
	if err != nil {
		return nil, err
	}
	if single && dp.name == "" {
		return nil, davNotFound()
	}
	c, err := b.s.davCollection(ctx, "addressbook", dp.slug)
	if err != nil {
		return nil, err
	}
	rows, err := b.s.davObjects(ctx, c.ID, dp.name)
	if err != nil {
		return nil, err
	}
	if single && len(rows) == 0 {
		return nil, davNotFound()
	}
	out := make([]carddav.AddressObject, 0, len(rows))
	for _, row := range rows {
		card, err := vcard.NewDecoder(strings.NewReader(row.Data)).Decode()
		if err != nil {
			continue
		}
		out = append(out, carddav.AddressObject{Path: b.bookPath(ctx, c.Slug) + row.Name, ModTime: row.UpdatedAt, ContentLength: int64(len(row.Data)), ETag: row.ETag, Card: card})
	}
	return out, nil
}

func (b *contactsBackend) GetAddressObject(ctx context.Context, p string, req *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	objs, err := b.objects(ctx, p, true)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, davNotFound()
	}
	return &objs[0], nil
}

func (b *contactsBackend) ListAddressObjects(ctx context.Context, p string, req *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	return b.objects(ctx, p, false)
}

func (b *contactsBackend) QueryAddressObjects(ctx context.Context, p string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	objs, err := b.objects(ctx, p, false)
	if err != nil {
		return nil, err
	}
	return carddav.Filter(query, objs)
}

func cardFields(card vcard.Card) (objectFields, error) {
	f := objectFields{UID: card.Value(vcard.FieldUID), Summary: card.PreferredValue(vcard.FieldFormattedName)}
	if f.UID == "" {
		return f, webdav.NewHTTPError(http.StatusBadRequest, errors.New("contact without UID"))
	}
	for _, e := range card.Values(vcard.FieldEmail) {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			f.Emails = append(f.Emails, e)
		}
	}
	return f, nil
}

func (b *contactsBackend) PutAddressObject(ctx context.Context, p string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	dp, err := parseDAVPath(ctx, p, davContacts)
	if err != nil || dp.name == "" {
		return nil, davNotFound()
	}
	c, err := b.s.davCollection(ctx, "addressbook", dp.slug)
	if err != nil {
		return nil, err
	}
	f, err := cardFields(card)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("invalid vCard: %w", err))
	}
	row, err := b.s.putDAVObject(ctx, c.ID, dp.name, buf.String(), f, opts.IfMatch, opts.IfNoneMatch)
	if err != nil {
		return nil, err
	}
	return &carddav.AddressObject{Path: b.bookPath(ctx, c.Slug) + row.Name, ModTime: row.UpdatedAt, ContentLength: int64(len(row.Data)), ETag: row.ETag, Card: card}, nil
}

func (b *contactsBackend) DeleteAddressObject(ctx context.Context, p string) error {
	dp, err := parseDAVPath(ctx, p, davContacts)
	if err != nil || dp.name == "" {
		return davNotFound()
	}
	c, err := b.s.davCollection(ctx, "addressbook", dp.slug)
	if err != nil {
		return err
	}
	return b.s.deleteDAVObject(ctx, c.ID, dp.name)
}
