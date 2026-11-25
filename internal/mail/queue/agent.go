package queue

import (
	"bytes"
	"cmp"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Agent struct {
	Key       string
	Spool     string
	Postqueue string
	Postsuper string
}

type ActionRequest struct {
	IDs    []string `json:"ids,omitempty"`
	Filter *Filter  `json:"filter,omitempty"`
}

type ActionResult struct {
	Affected int `json:"affected"`
}

func (r ActionRequest) Validate() error {
	switch {
	case len(r.IDs) > 0 && r.Filter != nil:
		return errors.New("Send either ids or filter, not both.")
	case len(r.IDs) == 0 && (r.Filter == nil || r.Filter.Empty()):
		return errors.New("Send the queue IDs to change, or a filter with at least one value.")
	case len(r.IDs) > MaxLimit:
		return fmt.Errorf("Send at most %d queue IDs at a time, or use a filter.", MaxLimit)
	}
	for _, id := range r.IDs {
		if !ValidID(id) {
			return fmt.Errorf("%q is not a Postfix queue ID.", id)
		}
	}
	if r.Filter != nil {
		return r.Filter.Validate()
	}
	return nil
}

func ParseQuery(q url.Values) (Filter, int, error) {
	f := Filter{Sender: q.Get("sender"), RecipientDomain: q.Get("recipientDomain"), Error: q.Get("error"), Queue: q.Get("queue")}
	limit := 200
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > MaxLimit {
			return f, 0, fmt.Errorf("The limit must be a whole number from 0 to %d.", MaxLimit)
		}
		limit = n
	}
	return f, limit, f.Validate()
}

func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /queue", a.serveList)
	mux.HandleFunc("POST /queue/{action}", a.serveAction)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if a.Key == "" || subtle.ConstantTimeCompare([]byte(token), []byte(a.Key)) != 1 {
			writeAgentError(w, http.StatusUnauthorized, "missing or wrong API key")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *Agent) serveList(w http.ResponseWriter, r *http.Request) {
	f, limit, err := ParseQuery(r.URL.Query())
	if err != nil {
		writeAgentError(w, http.StatusBadRequest, err.Error())
		return
	}
	l, err := a.List(r.Context(), f, limit)
	if err != nil {
		writeAgentError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeAgentJSON(w, http.StatusOK, l)
}

func (a *Agent) serveAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if !slices.Contains(Actions, action) {
		writeAgentError(w, http.StatusNotFound, "unknown action")
		return
	}
	var in ActionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeAgentError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.Validate(); err != nil {
		writeAgentError(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := a.Apply(r.Context(), action, in)
	if err != nil {
		writeAgentError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeAgentJSON(w, http.StatusOK, ActionResult{Affected: n})
}

func (a *Agent) List(ctx context.Context, f Filter, limit int) (*Listing, error) {
	l := &Listing{Summary: newSummary(), Messages: []Message{}}
	err := a.scan(ctx, func(m Message) {
		l.Summary.add(&m)
		if f.Match(&m) {
			l.Matched++
			if limit > 0 {
				l.Messages = append(l.Messages, m)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(l.Messages, func(x, y Message) int {
		return cmp.Or(x.ArrivalTime.Compare(y.ArrivalTime), strings.Compare(x.ID, y.ID))
	})
	l.Messages = l.Messages[:min(limit, len(l.Messages))]
	a.fillNextAttempts(l.Messages)
	return l, nil
}

func (a *Agent) fillNextAttempts(msgs []Message) {
	wanted := map[string]*Message{}
	for i := range msgs {
		if msgs[i].Queue == "deferred" {
			wanted[msgs[i].ID] = &msgs[i]
		}
	}
	if len(wanted) == 0 {
		return
	}
	filepath.WalkDir(filepath.Join(a.Spool, "deferred"), func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if m := wanted[d.Name()]; m != nil {
			if info, err := d.Info(); err == nil {
				at := info.ModTime().UTC()
				m.NextAttempt = &at
			}
		}
		return nil
	})
}

func applies(action, queueName string) bool {
	switch action {
	case "retry":
		return queueName == "deferred"
	case "hold":
		return queueName != "hold"
	case "release":
		return queueName == "hold"
	}
	return true
}

func (a *Agent) Apply(ctx context.Context, action string, in ActionRequest) (int, error) {
	wanted := map[string]bool{}
	for _, id := range in.IDs {
		wanted[id] = true
	}
	var targets []string
	err := a.scan(ctx, func(m Message) {
		if (wanted[m.ID] || (in.Filter != nil && in.Filter.Match(&m))) && applies(action, m.Queue) {
			targets = append(targets, m.ID)
		}
	})
	if err != nil || len(targets) == 0 {
		return 0, err
	}
	switch action {
	case "retry":
		return a.retry(ctx, targets)
	case "hold":
		return a.postsuper(ctx, "-h", targets)
	case "release":
		return a.postsuper(ctx, "-H", targets)
	default:
		return a.postsuper(ctx, "-d", targets)
	}
}

func (a *Agent) retry(ctx context.Context, ids []string) (int, error) {
	for i, id := range ids {
		if out, err := exec.CommandContext(ctx, a.Postqueue, "-i", id).CombinedOutput(); err != nil {
			return i, fmt.Errorf("postqueue -i %s: %w: %s", id, err, bytes.TrimSpace(out))
		}
	}
	return len(ids), nil
}

var postsuperCount = regexp.MustCompile(`(?m)^postsuper: (?:Deleted|Placed on hold|Released from hold): (\d+) messages?$`)

func (a *Agent) postsuper(ctx context.Context, flag string, ids []string) (int, error) {
	cmd := exec.CommandContext(ctx, a.Postsuper, flag, "-")
	cmd.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("postsuper %s: %w: %s", flag, err, bytes.TrimSpace(out))
	}
	n := 0
	for _, m := range postsuperCount.FindAllSubmatch(out, -1) {
		count, _ := strconv.Atoi(string(m[1]))
		n += count
	}
	return n, nil
}

func (a *Agent) scan(ctx context.Context, each func(Message)) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.Postqueue, "-j")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("postqueue -j: %w", err)
	}
	decodeErr := Decode(out, each)
	if decodeErr != nil {
		cancel()
		io.Copy(io.Discard, out)
	}
	if err := cmd.Wait(); err != nil && decodeErr == nil {
		return fmt.Errorf("postqueue -j: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return decodeErr
}

func writeAgentJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeAgentError(w http.ResponseWriter, status int, message string) {
	writeAgentJSON(w, status, map[string]string{"error": message})
}
