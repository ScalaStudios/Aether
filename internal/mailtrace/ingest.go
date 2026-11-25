package mailtrace

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	lockID    = 7316249
	batchSize = 500
	headSize  = 512
)

type Ingester struct {
	pool      *pgxpool.Pool
	log       *slog.Logger
	path      string
	retention int
	cursor    cursor
	sessions  map[string]session
	peers     map[string]session
	open      map[string]int64
	dirty     map[int64]bool
	latest    time.Time
	wrote     time.Time
	drained   string
}

type cursor struct {
	head     string
	position int64
}

type session struct {
	client, ip, tls, relay string
	seen                   time.Time
}

type execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func New(pool *pgxpool.Pool, log *slog.Logger, path string, retentionDays int) *Ingester {
	return &Ingester{pool: pool, log: log, path: path, retention: retentionDays}
}

func (in *Ingester) Run(ctx context.Context) {
	for {
		err := in.run(ctx)
		if ctx.Err() != nil {
			return
		}
		in.log.Error("mail trace ingestion stopped; retrying in 10 seconds", "path", in.path, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (in *Ingester) run(ctx context.Context) error {
	conn, err := in.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer func() {
		conn.Conn().Close(context.Background())
		conn.Release()
	}()
	for {
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&locked); err != nil {
			return err
		}
		if locked {
			break
		}
		in.log.Info("another mail trace ingester holds the lock; waiting")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
	in.sessions, in.peers, in.open, in.dirty = map[string]session{}, map[string]session{}, map[string]int64{}, map[int64]bool{}
	err = conn.QueryRow(ctx, `SELECT head, position FROM mail_trace_sources WHERE path = $1`, in.path).Scan(&in.cursor.head, &in.cursor.position)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	in.log.Info("mail trace ingestion started", "path", in.path, "retention_days", in.retention)
	var purged time.Time
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if err := in.poll(ctx, conn); err != nil {
			return err
		}
		if time.Since(purged) > time.Hour {
			if err := in.purge(ctx, conn); err != nil {
				return err
			}
			purged = time.Now()
		}
		if time.Since(in.wrote) > time.Minute {
			if err := in.save(ctx, conn, in.cursor); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (in *Ingester) poll(ctx context.Context, conn *pgxpool.Conn) error {
	current, err := head(in.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current != "" && current == in.cursor.head {
		return in.consume(ctx, conn, in.path, current, in.cursor.position)
	}
	if current == "" && in.drained != "" && in.drained == in.cursor.head {
		return nil
	}
	rotated, err := filepath.Glob(in.path + ".*")
	if err != nil {
		return err
	}
	rotated = latestCopies(rotated)
	start, offset := 0, int64(0)
	for i, p := range rotated {
		if h, err := head(p); err == nil && h != "" && h == in.cursor.head {
			start, offset = i, in.cursor.position
			break
		}
	}
	for i := start; i < len(rotated); i++ {
		h, err := head(rotated[i])
		if err != nil || h == "" {
			continue
		}
		from := int64(0)
		if i == start {
			from = offset
		}
		if err := in.consume(ctx, conn, rotated[i], h, from); err != nil {
			return err
		}
		in.drained = h
	}
	if current == "" {
		return nil
	}
	return in.consume(ctx, conn, in.path, current, 0)
}

func latestCopies(paths []string) []string {
	byBase := map[string]string{}
	for _, p := range paths {
		base := strings.TrimSuffix(p, ".gz")
		if existing, ok := byBase[base]; !ok || strings.HasSuffix(existing, ".gz") {
			byBase[base] = p
		}
	}
	out := make([]string, 0, len(byBase))
	for _, p := range byBase {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return strings.TrimSuffix(out[i], ".gz") < strings.TrimSuffix(out[j], ".gz") })
	return out
}

func open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil || !strings.HasSuffix(path, ".gz") {
		return f, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{gz, f}, nil
}

func head(path string) (string, error) {
	r, err := open(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	buf, err := io.ReadAll(io.LimitReader(r, headSize))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		return string(buf[:i+1]), nil
	}
	if len(buf) == headSize {
		return string(buf), nil
	}
	return "", nil
}

func (in *Ingester) consume(ctx context.Context, conn *pgxpool.Conn, path, first string, from int64) error {
	r, err := open(path)
	if err != nil {
		return err
	}
	defer r.Close()
	if f, ok := r.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Size() < from {
			from = 0
		}
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			return err
		}
	} else if _, err := io.CopyN(io.Discard, r, from); err != nil {
		return nil
	}
	br := bufio.NewReaderSize(r, 64<<10)
	pos, lines := from, 0
	var batch []record
	now := time.Now()
	for {
		line, err := br.ReadString('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		pos += int64(len(line))
		lines++
		if rec, ok := parse(line, now); ok {
			batch = append(batch, rec)
		}
		if lines == batchSize {
			if err := in.commit(ctx, conn, batch, cursor{first, pos}); err != nil {
				return err
			}
			batch, lines = nil, 0
		}
	}
	if pos == in.cursor.position && first == in.cursor.head {
		return nil
	}
	return in.commit(ctx, conn, batch, cursor{first, pos})
}

func (in *Ingester) commit(ctx context.Context, conn *pgxpool.Conn, batch []record, c cursor) error {
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		for _, r := range batch {
			if err := in.apply(ctx, tx, r); err != nil {
				return err
			}
		}
		for id := range in.dirty {
			if err := refresh(ctx, tx, id); err != nil {
				return err
			}
		}
		return in.save(ctx, tx, c)
	})
	clear(in.dirty)
	if err != nil {
		clear(in.open)
		return err
	}
	in.cursor = c
	return nil
}

func (in *Ingester) save(ctx context.Context, db execer, c cursor) error {
	_, err := db.Exec(ctx, `
		INSERT INTO mail_trace_sources (path, head, position, retention_days, read_at) VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (path) DO UPDATE SET head = EXCLUDED.head, position = EXCLUDED.position, retention_days = EXCLUDED.retention_days, read_at = now()`,
		in.path, c.head, c.position, in.retention)
	if err == nil {
		in.wrote = time.Now()
	}
	return err
}

func (in *Ingester) purge(ctx context.Context, conn *pgxpool.Conn) error {
	tag, err := conn.Exec(ctx, `DELETE FROM mail_trace_messages WHERE received_at < now() - make_interval(days => $1)`, in.retention)
	if err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		in.log.Info("removed expired mail trace records", "messages", n, "retention_days", in.retention)
	}
	for k, s := range in.sessions {
		if s.seen.Before(in.latest.Add(-time.Hour)) {
			delete(in.sessions, k)
		}
	}
	for k, s := range in.peers {
		if s.seen.Before(in.latest.Add(-time.Hour)) {
			delete(in.peers, k)
		}
	}
	if len(in.open) > 10000 {
		clear(in.open)
	}
	return nil
}
