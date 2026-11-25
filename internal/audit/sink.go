package audit

import (
	"context"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
)

type Event struct {
	ID          int64          `json:"id"`
	CreatedAt   time.Time      `json:"createdAt"`
	OrgID       *string        `json:"orgId,omitempty"`
	ActorUserID *string        `json:"actorUserId,omitempty"`
	ActorEmail  string         `json:"actorEmail,omitempty"`
	Action      string         `json:"action"`
	TargetType  string         `json:"targetType"`
	TargetID    string         `json:"targetId"`
	IP          string         `json:"ip"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	TraceID     string         `json:"traceId,omitempty"`
	SpanID      string         `json:"spanId,omitempty"`
}

type Sink interface {
	Send(ctx context.Context, ev Event) error
	Close() error
}

type Dispatcher struct {
	sinks  []Sink
	log    *slog.Logger
	ch     chan Event
	done   chan struct{}
	mu     sync.RWMutex
	closed bool
}

func NewDispatcher(log *slog.Logger, sinks ...Sink) *Dispatcher {
	var active []Sink
	for _, s := range sinks {
		if s != nil {
			active = append(active, s)
		}
	}
	if len(active) == 0 {
		return nil
	}
	d := &Dispatcher{
		sinks: active,
		log:   log,
		ch:    make(chan Event, 1024),
		done:  make(chan struct{}),
	}
	go d.run()
	return d
}

func (d *Dispatcher) Emit(ctx context.Context, ev Event) {
	if d == nil {
		return
	}
	if ev.TraceID == "" || ev.SpanID == "" {
		sc := trace.SpanFromContext(ctx).SpanContext()
		if sc.IsValid() {
			if ev.TraceID == "" {
				ev.TraceID = sc.TraceID().String()
			}
			if ev.SpanID == "" {
				ev.SpanID = sc.SpanID().String()
			}
		}
	}
	if ev.Metadata != nil {
		ev.Metadata = maps.Clone(ev.Metadata)
	}
	if ev.OrgID != nil {
		v := *ev.OrgID
		ev.OrgID = &v
	}
	if ev.ActorUserID != nil {
		v := *ev.ActorUserID
		ev.ActorUserID = &v
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return
	}
	select {
	case d.ch <- ev:
	default:
		if d.log != nil {
			d.log.Warn("audit sink queue full; dropping event", "id", ev.ID, "action", ev.Action)
		}
	}
}

func (d *Dispatcher) run() {
	defer close(d.done)
	for ev := range d.ch {
		for _, sink := range d.sinks {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := sink.Send(ctx, ev)
			cancel()
			if err != nil && d.log != nil {
				d.log.Warn("audit sink delivery failed", "action", ev.Action, "id", ev.ID, "err", err)
			}
		}
	}
	for _, sink := range d.sinks {
		if err := sink.Close(); err != nil && d.log != nil {
			d.log.Warn("audit sink close failed", "err", err)
		}
	}
}

func (d *Dispatcher) Close(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.ch)
	}
	d.mu.Unlock()
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isWarningAction(action string) bool {
	return strings.HasSuffix(action, ".failed") || strings.HasSuffix(action, ".rejected")
}
