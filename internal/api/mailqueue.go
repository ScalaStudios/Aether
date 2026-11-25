package api

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/scalastudios/aether/internal/mail/queue"
)

func (s *Server) mailQueueReady(w http.ResponseWriter, p *principal) bool {
	if !s.requirePlatformAdmin(w, p) {
		return false
	}
	if s.cfg.PostfixAPIKey == "" {
		writeError(w, http.StatusServiceUnavailable, "queue_not_configured", "The mail queue is not connected. Set AETHER_POSTFIX_API_KEY on the api and postfix containers, then recreate them.")
		return false
	}
	return true
}

func (s *Server) listMailQueue(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.mailQueueReady(w, p) {
		return
	}
	f, limit, err := queue.ParseQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_filter", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	l, err := s.queue.List(ctx, f, limit)
	if err != nil {
		s.queueUnavailable(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) changeMailQueue(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.mailQueueReady(w, p) {
		return
	}
	action := r.PathValue("action")
	if !slices.Contains(queue.Actions, action) {
		notFound(w)
		return
	}
	var in queue.ActionRequest
	if !decode(w, r, 256<<10, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_selection", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()
	n, err := s.queue.Apply(ctx, action, in)
	if err != nil {
		s.queueUnavailable(w, err)
		return
	}
	target, meta := "", map[string]any{"affected": n, "ids": in.IDs}
	if len(in.IDs) == 1 {
		target = in.IDs[0]
	}
	if in.Filter != nil {
		meta = map[string]any{"affected": n, "filter": in.Filter}
	}
	s.audit(r.Context(), r, p, nil, "mail_queue."+action, "mail_queue", target, meta)
	writeJSON(w, http.StatusOK, queue.ActionResult{Affected: n})
}

func (s *Server) queueUnavailable(w http.ResponseWriter, err error) {
	s.log.Error("postfix queue service failed", "err", err)
	writeError(w, http.StatusServiceUnavailable, "queue_unavailable", "Aether could not reach the Postfix queue ("+err.Error()+"). Check that the postfix container is running and that it has the same AETHER_POSTFIX_API_KEY as the API.")
}

func (s *Server) RunQueueMetrics(ctx context.Context) {
	if s.cfg.PostfixAPIKey == "" {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.refreshQueueMetrics(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) refreshQueueMetrics(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	l, err := s.queue.List(ctx, queue.Filter{}, 0)
	if err != nil {
		s.log.Warn("reading the mail queue for metrics failed", "err", err)
		return
	}
	for name, n := range l.Summary.Queues {
		queueMessages.WithLabelValues(name).Set(float64(n))
	}
	oldest := 0.0
	if l.Summary.OldestArrival != nil {
		oldest = time.Since(*l.Summary.OldestArrival).Seconds()
	}
	queueOldest.Set(oldest)
}
