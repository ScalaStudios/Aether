package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	notifyChannel       = "aether:notify"
	notifyLeaderKey     = "aether:notify:leader"
	maxWatchedFolders   = 8
	maxNotificationRows = 200
)

type liveEvent struct {
	MailboxID string        `json:"mailboxId,omitempty"`
	Folder    string        `json:"folder,omitempty"`
	Kind      string        `json:"kind,omitempty"`
	Messages  []liveMessage `json:"messages,omitempty"`
	Title     string        `json:"title,omitempty"`
	Body      string        `json:"body,omitempty"`
	URL       string        `json:"url,omitempty"`
	Tag       string        `json:"tag,omitempty"`
	EventID   string        `json:"eventId,omitempty"`
	Start     string        `json:"start,omitempty"`
}

type liveMessage struct {
	UID     uint32 `json:"uid"`
	Subject string `json:"subject"`
	From    string `json:"from"`
	Address string `json:"address"`
	Seen    bool   `json:"seen"`
}

type notifyEnvelope struct {
	Origin    string          `json:"origin"`
	Name      string          `json:"name"`
	UserID    string          `json:"userId,omitempty"`
	MailboxID string          `json:"mailboxId,omitempty"`
	Data      json.RawMessage `json:"data"`
}

type liveClient struct {
	userID    string
	mailboxes map[string]struct{}
	ch        chan []byte
}

type presence struct {
	until        time.Time
	focusMailbox string
	focusFolder  string
	focusUntil   time.Time
	lastFocus    time.Time
}

type liveUser struct {
	id           string
	focusMailbox string
	focusFolder  string
}

type liveHub struct {
	mu       sync.Mutex
	clients  map[*liveClient]struct{}
	presence map[string]presence
}

func newLiveHub() *liveHub {
	return &liveHub{clients: map[*liveClient]struct{}{}, presence: map[string]presence{}}
}

func (h *liveHub) add(userID string, boxes map[string]struct{}) *liveClient {
	c := &liveClient{userID: userID, mailboxes: boxes, ch: make(chan []byte, 16)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	p := h.presence[userID]
	p.until = time.Now().Add(50 * time.Second)
	h.presence[userID] = p
	h.mu.Unlock()
	return c
}

func (h *liveHub) remove(c *liveClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *liveHub) setMailboxes(c *liveClient, boxes map[string]struct{}) {
	h.mu.Lock()
	c.mailboxes = boxes
	h.mu.Unlock()
}

func (h *liveHub) touch(userID string, until time.Time) {
	h.mu.Lock()
	p := h.presence[userID]
	p.until = until
	h.presence[userID] = p
	h.mu.Unlock()
}

func (h *liveHub) setFocus(userID, mailboxID, folder string, until time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.presence[userID]
	same := p.focusMailbox == mailboxID && p.focusFolder == folder
	if !same && !p.lastFocus.IsZero() && time.Since(p.lastFocus) < 2*time.Second {
		return false
	}
	if same && time.Since(p.lastFocus) < 5*time.Second {
		p.focusUntil = until
		if p.until.Before(until) {
			p.until = until
		}
		h.presence[userID] = p
		return true
	}
	p.lastFocus = time.Now()
	p.focusMailbox, p.focusFolder, p.focusUntil = mailboxID, folder, until
	if p.until.Before(until) {
		p.until = until
	}
	h.presence[userID] = p
	return true
}

func (h *liveHub) snapshot(now time.Time) []liveUser {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []liveUser
	for id, p := range h.presence {
		if !p.until.After(now) && !p.focusUntil.After(now) {
			delete(h.presence, id)
			continue
		}
		u := liveUser{id: id}
		if p.focusUntil.After(now) {
			u.focusMailbox, u.focusFolder = p.focusMailbox, p.focusFolder
		}
		if p.until.After(now) || u.focusMailbox != "" {
			out = append(out, u)
		}
	}
	return out
}

func (h *liveHub) hasUser(userID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.userID == userID {
			return true
		}
	}
	return false
}

func (h *liveHub) deliver(userID, mailboxID string, frame []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if userID != "" && c.userID != userID {
			continue
		}
		if mailboxID != "" {
			if _, ok := c.mailboxes[mailboxID]; !ok {
				continue
			}
		}
		select {
		case c.ch <- frame:
		default:
			select {
			case <-c.ch:
			default:
			}
			select {
			case c.ch <- resyncFrame:
			default:
			}
		}
	}
}

var resyncFrame = []byte("event: resync\ndata: {}\n\n")

func sseFrame(name string, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte("{}")
	}
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", name, b)
}

type watchSpec struct {
	mailboxID string
	address   string
	folder    string
	focus     bool
}

func (s *Server) wakeNotify() {
	select {
	case s.notifyWake <- struct{}{}:
	default:
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, p *principal) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "This server cannot stream events.")
		return
	}
	boxes, err := s.mailboxIDs(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	write := func(frame []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(45 * time.Second))
		if _, err := w.Write(frame); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !write([]byte(": ok\n\n")) {
		return
	}
	client := s.hub.add(p.User.ID, boxes)
	defer s.hub.remove(client)
	s.touchLive(r.Context(), p.User.ID)
	s.wakeNotify()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	refreshed := time.Now()
	for {
		select {
		case <-r.Context().Done():
			return
		case frame := <-client.ch:
			if !write(frame) {
				return
			}
		case <-ping.C:
			s.hub.touch(p.User.ID, time.Now().Add(50*time.Second))
			s.touchLive(r.Context(), p.User.ID)
			if time.Since(refreshed) > 2*time.Minute {
				if boxes, err := s.mailboxIDs(r.Context(), p.User.ID); err == nil {
					s.hub.setMailboxes(client, boxes)
					refreshed = time.Now()
				}
			}
			if !write([]byte("event: ping\ndata: {}\n\n")) {
				return
			}
		}
	}
}

func (s *Server) touchLive(ctx context.Context, userID string) {
	if s.kv == nil {
		return
	}
	_ = s.kv.Set(ctx, "aether:live:"+userID, "1", 50*time.Second).Err()
}

func (s *Server) mailboxIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.id FROM mailboxes m
		JOIN domains d ON d.id = m.domain_id
		WHERE m.active AND d.active AND d.verified_at IS NOT NULL AND `+mailboxAccess("$1"), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

type notificationView struct {
	MailEnabled     bool                 `json:"mailEnabled"`
	CalendarEnabled bool                 `json:"calendarEnabled"`
	VAPIDPublicKey  string               `json:"vapidPublicKey"`
	PushConfigured  bool                 `json:"pushConfigured"`
	Browsers        int                  `json:"browsers"`
	IOS             iosPushStatus        `json:"ios"`
	Folders         []folderNotification `json:"folders"`
}

type iosPushStatus struct {
	Configured bool `json:"configured"`
	Devices    int  `json:"devices"`
}

type folderNotification struct {
	MailboxID string `json:"mailboxId" db:"mailbox_id"`
	Folder    string `json:"folder" db:"folder"`
	Enabled   bool   `json:"enabled" db:"enabled"`
}

func (s *Server) notificationView(ctx context.Context, userID string) (notificationView, error) {
	out := notificationView{MailEnabled: true, CalendarEnabled: true, Folders: []folderNotification{}}
	err := s.db.QueryRow(ctx, `SELECT mail_enabled, calendar_enabled FROM notification_settings WHERE user_id = $1`, userID).Scan(&out.MailEnabled, &out.CalendarEnabled)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	rows, err := s.db.Query(ctx, `SELECT mailbox_id, folder, enabled FROM mail_folder_notifications WHERE user_id = $1 ORDER BY mailbox_id, folder`, userID)
	if err != nil {
		return out, err
	}
	out.Folders, err = pgx.CollectRows(rows, pgx.RowToStructByName[folderNotification])
	if err != nil {
		return out, err
	}
	if out.Folders == nil {
		out.Folders = []folderNotification{}
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM web_push_subscriptions WHERE user_id = $1`, userID).Scan(&out.Browsers); err != nil {
		return out, err
	}
	if err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM ios_push_devices i
		JOIN mailboxes m ON m.id = i.mailbox_id
		WHERE m.active AND `+mailboxAccess("$1"), userID).Scan(&out.IOS.Devices); err != nil {
		return out, err
	}
	out.IOS.Configured = s.apnsReady()
	pub, _, err := s.vapid(ctx)
	if err != nil {
		s.log.Warn("web push keys are not available", "err", err)
	} else {
		out.VAPIDPublicKey = pub
		out.PushConfigured = pub != ""
	}
	return out, nil
}

func (s *Server) getNotifications(w http.ResponseWriter, r *http.Request, p *principal) {
	out, err := s.notificationView(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveNotifications(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		MailEnabled     bool                 `json:"mailEnabled"`
		CalendarEnabled bool                 `json:"calendarEnabled"`
		Folders         []folderNotification `json:"folders"`
	}
	if !decode(w, r, 64<<10, &in) {
		return
	}
	if len(in.Folders) > maxNotificationRows {
		writeError(w, http.StatusUnprocessableEntity, "invalid_notification", "Choose notifications for at most 200 folders.")
		return
	}
	boxes, err := s.mailboxIDs(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	seen := map[string]struct{}{}
	for _, f := range in.Folders {
		if _, ok := boxes[f.MailboxID]; !ok || !validFolderName(f.Folder) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_notification", "One of these folders is not in a mailbox you can open.")
			return
		}
		key := f.MailboxID + "\n" + f.Folder
		if _, ok := seen[key]; ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid_notification", "Each folder can only be listed once.")
			return
		}
		seen[key] = struct{}{}
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO notification_settings (user_id, mail_enabled, calendar_enabled) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET mail_enabled = excluded.mail_enabled, calendar_enabled = excluded.calendar_enabled, updated_at = now()`,
		p.User.ID, in.MailEnabled, in.CalendarEnabled); err != nil {
		s.internalError(w, r, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM mail_folder_notifications WHERE user_id = $1`, p.User.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, f := range in.Folders {
		if _, err := tx.Exec(r.Context(), `INSERT INTO mail_folder_notifications (user_id, mailbox_id, folder, enabled) VALUES ($1, $2, $3, $4)`, p.User.ID, f.MailboxID, f.Folder, f.Enabled); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.wakeNotify()
	out, err := s.notificationView(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func validFolderName(name string) bool {
	return name != "" && len(name) <= 255 && !strings.ContainsAny(name, "\r\n\x00") && !mailpkg.SharedFolder(name)
}

func (s *Server) focusMailbox(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		MailboxID string `json:"mailboxId"`
		Folder    string `json:"folder"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	if !uuidPattern.MatchString(in.MailboxID) || !validFolderName(in.Folder) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_focus", "Choose a folder in a mailbox you can open.")
		return
	}
	if _, _, err := s.accessibleMailbox(r.Context(), in.MailboxID, p.User.ID); err != nil {
		notFound(w)
		return
	}
	until := time.Now().Add(50 * time.Second)
	if !s.hub.setFocus(p.User.ID, in.MailboxID, in.Folder, until) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Wait a moment before switching the folder Aether is watching.")
		return
	}
	if s.kv != nil {
		_ = s.kv.Set(r.Context(), "aether:focus:"+p.User.ID, in.MailboxID+"\n"+in.Folder, 50*time.Second).Err()
	}
	s.wakeNotify()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RunNotifications(ctx context.Context) {
	go s.consumeNotify(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		s.reconcileWatches(ctx)
		s.deliverReminders(ctx)
		select {
		case <-ctx.Done():
			s.stopWatches()
			return
		case <-ticker.C:
		case <-s.notifyWake:
		}
	}
}

func (s *Server) stopWatches() {
	s.watchesMu.Lock()
	defer s.watchesMu.Unlock()
	for key, cancel := range s.watches {
		cancel()
		delete(s.watches, key)
	}
	idleWatches.Set(0)
}

var renewLeader = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

func (s *Server) holdLeader(ctx context.Context) bool {
	if s.kv == nil {
		return true
	}
	ok, err := s.kv.SetNX(ctx, notifyLeaderKey, s.origin, 30*time.Second).Result()
	if err != nil {
		s.log.Warn("notification leader lock failed; watching from this process", "err", err)
		return true
	}
	if ok {
		return true
	}
	n, err := renewLeader.Run(ctx, s.kv, []string{notifyLeaderKey}, s.origin, "30000").Int()
	if err != nil {
		s.log.Warn("notification leader lock failed; watching from this process", "err", err)
		return true
	}
	return n == 1
}

func (s *Server) reconcileWatches(ctx context.Context) {
	if !s.holdLeader(ctx) {
		s.stopWatches()
		return
	}
	desired, err := s.desiredWatches(ctx)
	if err != nil {
		s.log.Error("choosing mailbox watches failed", "err", err)
		return
	}
	s.watchesMu.Lock()
	defer s.watchesMu.Unlock()
	if s.watches == nil {
		s.watches = map[string]context.CancelFunc{}
	}
	for key, cancel := range s.watches {
		if _, ok := desired[key]; !ok {
			cancel()
			delete(s.watches, key)
		}
	}
	for key, spec := range desired {
		if _, ok := s.watches[key]; ok {
			continue
		}
		watchCtx, cancel := context.WithCancel(ctx)
		s.watches[key] = cancel
		go s.runWatch(watchCtx, spec)
	}
	idleWatches.Set(float64(len(s.watches)))
}

func (s *Server) runWatch(ctx context.Context, spec watchSpec) {
	delay := 2 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := s.mail.Watch(ctx, spec.address, spec.folder, func(change mailpkg.FolderChange) {
			go s.onMail(context.WithoutCancel(ctx), spec, change)
		})
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			delay = 2 * time.Second
		}
		s.log.Warn("mailbox watch stopped", "mailbox", spec.mailboxID, "folder", spec.folder, "err", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay < time.Minute {
			delay *= 2
		}
	}
}

type mailboxInterest struct {
	id, address string
	explicit    map[string]bool
	focus       string
}

func (s *Server) desiredWatches(ctx context.Context) (map[string]watchSpec, error) {
	users := map[string]liveUser{}
	for _, u := range s.hub.snapshot(time.Now()) {
		users[u.id] = u
	}
	if s.kv != nil {
		if err := s.redisPresence(ctx, users); err != nil {
			s.log.Warn("reading live mail sessions failed", "err", err)
		}
	}
	pushUsers, err := s.pushUsers(ctx)
	if err != nil {
		return nil, err
	}
	ids := map[string]struct{}{}
	for id := range users {
		ids[id] = struct{}{}
	}
	for _, id := range pushUsers {
		ids[id] = struct{}{}
	}
	if len(ids) == 0 {
		ios, err := s.iosMailboxes(ctx)
		if err != nil {
			return nil, err
		}
		return limitWatches(iosOnly(ios)), nil
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	rows, err := s.db.Query(ctx, `
		SELECT u.id, m.id, m.address FROM users u
		JOIN mailboxes m ON m.active AND `+mailboxAccess("u.id")+`
		JOIN domains d ON d.id = m.domain_id AND d.active AND d.verified_at IS NOT NULL
		WHERE u.id = ANY($1::uuid[]) AND u.status = 'active'`, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	boxes := map[string]map[string]*mailboxInterest{}
	for rows.Next() {
		var userID, mailboxID, address string
		if err := rows.Scan(&userID, &mailboxID, &address); err != nil {
			return nil, err
		}
		if boxes[userID] == nil {
			boxes[userID] = map[string]*mailboxInterest{}
		}
		boxes[userID][mailboxID] = &mailboxInterest{id: mailboxID, address: address, explicit: map[string]bool{}}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	prefs, err := s.db.Query(ctx, `SELECT user_id, mailbox_id, folder, enabled FROM mail_folder_notifications WHERE user_id = ANY($1::uuid[])`, list)
	if err != nil {
		return nil, err
	}
	defer prefs.Close()
	for prefs.Next() {
		var userID, mailboxID, folder string
		var enabled bool
		if err := prefs.Scan(&userID, &mailboxID, &folder, &enabled); err != nil {
			return nil, err
		}
		if box := boxes[userID][mailboxID]; box != nil {
			box.explicit[folder] = enabled
		}
	}
	if err := prefs.Err(); err != nil {
		return nil, err
	}
	var specs []watchSpec
	seenUser := map[string]struct{}{}
	for _, id := range pushUsers {
		seenUser[id] = struct{}{}
		for _, box := range boxes[id] {
			for _, folder := range foldersFor(box.explicit, "") {
				specs = append(specs, watchSpec{mailboxID: box.id, address: box.address, folder: folder})
			}
		}
	}
	for id, live := range users {
		for _, box := range boxes[id] {
			focus := ""
			if live.focusMailbox == box.id {
				focus = live.focusFolder
			}
			for _, folder := range foldersFor(box.explicit, focus) {
				specs = append(specs, watchSpec{mailboxID: box.id, address: box.address, folder: folder, focus: folder == focus})
			}
		}
	}
	ios, err := s.iosMailboxes(ctx)
	if err != nil {
		return nil, err
	}
	for _, box := range ios {
		specs = append(specs, watchSpec{mailboxID: box.id, address: box.address, folder: "INBOX"})
	}
	return limitWatches(specs), nil
}

func iosOnly(boxes []mailboxRef) []watchSpec {
	var specs []watchSpec
	for _, box := range boxes {
		specs = append(specs, watchSpec{mailboxID: box.id, address: box.address, folder: "INBOX"})
	}
	return specs
}

type mailboxRef struct {
	id, address string
}

func (s *Server) pushUsers(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT s.user_id FROM web_push_subscriptions s
		JOIN users u ON u.id = s.user_id AND u.status = 'active'
		WHERE coalesce((SELECT mail_enabled FROM notification_settings n WHERE n.user_id = u.id), true)`)
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

func (s *Server) iosMailboxes(ctx context.Context) ([]mailboxRef, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT m.id, m.address FROM ios_push_devices i
		JOIN mailboxes m ON m.id = i.mailbox_id AND m.active
		JOIN domains d ON d.id = m.domain_id AND d.active AND d.verified_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mailboxRef
	for rows.Next() {
		var box mailboxRef
		if err := rows.Scan(&box.id, &box.address); err != nil {
			return nil, err
		}
		out = append(out, box)
	}
	return out, rows.Err()
}

func (s *Server) redisPresence(ctx context.Context, users map[string]liveUser) error {
	var cursor uint64
	for {
		keys, next, err := s.kv.Scan(ctx, cursor, "aether:live:*", 100).Result()
		if err != nil {
			return err
		}
		for _, key := range keys {
			id := strings.TrimPrefix(key, "aether:live:")
			if id == "" {
				continue
			}
			cur := users[id]
			cur.id = id
			if cur.focusMailbox == "" {
				if raw, err := s.kv.Get(ctx, "aether:focus:"+id).Result(); err == nil {
					mailboxID, folder, ok := strings.Cut(raw, "\n")
					if ok && uuidPattern.MatchString(mailboxID) && validFolderName(folder) {
						cur.focusMailbox, cur.focusFolder = mailboxID, folder
					}
				}
			}
			users[id] = cur
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

func foldersFor(explicit map[string]bool, focus string) []string {
	inbox := true
	var extra []string
	for folder, on := range explicit {
		if strings.EqualFold(folder, "INBOX") {
			inbox = on
			continue
		}
		if on && validFolderName(folder) {
			extra = append(extra, folder)
		}
	}
	var out []string
	if inbox {
		out = append(out, "INBOX")
	}
	slices.Sort(extra)
	out = append(out, extra...)
	if focus != "" && validFolderName(focus) && !slices.ContainsFunc(out, func(name string) bool { return name == focus }) {
		out = append(out, focus)
	}
	return out
}

func limitWatches(specs []watchSpec) map[string]watchSpec {
	grouped := map[string][]watchSpec{}
	var order []string
	for _, spec := range specs {
		if spec.mailboxID == "" || spec.address == "" || !validFolderName(spec.folder) {
			continue
		}
		if _, ok := grouped[spec.mailboxID]; !ok {
			order = append(order, spec.mailboxID)
		}
		replaced := false
		for i, have := range grouped[spec.mailboxID] {
			if have.folder == spec.folder {
				if spec.focus {
					grouped[spec.mailboxID][i].focus = true
				}
				replaced = true
				break
			}
		}
		if !replaced {
			grouped[spec.mailboxID] = append(grouped[spec.mailboxID], spec)
		}
	}
	out := map[string]watchSpec{}
	for _, id := range order {
		group := grouped[id]
		slices.SortFunc(group, func(a, b watchSpec) int {
			return strings.Compare(watchRank(a), watchRank(b))
		})
		if len(group) > maxWatchedFolders {
			group = group[:maxWatchedFolders]
		}
		for _, spec := range group {
			out[spec.mailboxID+"\n"+spec.folder] = spec
		}
	}
	return out
}

func watchRank(spec watchSpec) string {
	name := strings.ToLower(spec.folder)
	switch {
	case strings.EqualFold(spec.folder, "INBOX"):
		return "0"
	case spec.focus:
		return "1" + name
	default:
		return "2" + name
	}
}

func (s *Server) onMail(ctx context.Context, spec watchSpec, change mailpkg.FolderChange) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	messages := make([]liveMessage, 0, len(change.Messages))
	var unseen []liveMessage
	for _, m := range change.Messages {
		item := liveMessage{UID: m.UID, Subject: m.Subject, Seen: m.Seen}
		if len(m.From) > 0 {
			item.Address = m.From[0].Address
			item.From = m.From[0].Name
			if item.From == "" {
				item.From = item.Address
			}
		}
		messages = append(messages, item)
		if !m.Seen {
			unseen = append(unseen, item)
		}
	}
	kind := change.Kind
	if kind == "" {
		kind = "changed"
	}
	ev := liveEvent{MailboxID: spec.mailboxID, Folder: spec.folder, Kind: kind, Messages: messages}
	s.publishLive(ctx, "mail", "", spec.mailboxID, ev)
	if kind != "new" || len(unseen) == 0 {
		return
	}
	s.pushMail(ctx, spec, change.UIDValidity, unseen)
	s.pushIOS(ctx, spec, change.UIDValidity, unseen)
}

func (s *Server) publishLive(ctx context.Context, name, userID, mailboxID string, data any) {
	frame := sseFrame(name, data)
	s.hub.deliver(userID, mailboxID, frame)
	if s.kv == nil {
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	env, err := json.Marshal(notifyEnvelope{Origin: s.origin, Name: name, UserID: userID, MailboxID: mailboxID, Data: raw})
	if err != nil {
		return
	}
	if err := s.kv.Publish(ctx, notifyChannel, env).Err(); err != nil {
		s.log.Warn("publishing a notification failed", "err", err)
	}
}

func (s *Server) consumeNotify(ctx context.Context) {
	if s.kv == nil {
		return
	}
	for ctx.Err() == nil {
		sub := s.kv.Subscribe(ctx, notifyChannel)
		for msg := range sub.Channel() {
			var env notifyEnvelope
			if json.Unmarshal([]byte(msg.Payload), &env) != nil || env.Origin == s.origin || env.Name == "" {
				continue
			}
			s.hub.deliver(env.UserID, env.MailboxID, sseFrame(env.Name, json.RawMessage(env.Data)))
		}
		_ = sub.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Server) claimDelivery(ctx context.Context, key string) bool {
	tag, err := s.db.Exec(ctx, `INSERT INTO notification_deliveries (dedupe_key) VALUES ($1) ON CONFLICT DO NOTHING`, key)
	return err == nil && tag.RowsAffected() == 1
}

func (s *Server) releaseDelivery(ctx context.Context, keys []string) {
	if len(keys) == 0 {
		return
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM notification_deliveries WHERE dedupe_key = ANY($1)`, keys); err != nil {
		s.log.Warn("releasing a notification claim failed", "err", err)
	}
}

func mailURL(mailboxID, folder string, uid uint32) string {
	return "/mail?" + url.Values{
		"mailbox": {mailboxID},
		"folder":  {folder},
		"message": {fmt.Sprintf("%d", uid)},
	}.Encode()
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
