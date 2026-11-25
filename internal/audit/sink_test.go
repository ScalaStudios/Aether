package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func TestSyslogFormatRFC5424(t *testing.T) {
	orgID := "11111111-1111-1111-1111-111111111111"
	userID := "22222222-2222-2222-2222-222222222222"
	sink := NewSyslogSink("udp", "127.0.0.1:514", 4, "aether", "mail.example.com")
	ev := Event{
		ID:          42,
		CreatedAt:   time.Date(2026, 11, 24, 15, 4, 5, 123456000, time.UTC),
		OrgID:       &orgID,
		ActorUserID: &userID,
		ActorEmail:  "alice@example.com",
		Action:      "auth.login.failed",
		TargetType:  "user",
		TargetID:    `bob"\]` + "\n" + "injected",
		IP:          "203.0.113.10",
		Metadata:    map[string]any{"method": "password"},
	}
	got := string(sink.FormatRFC5424(ev))
	if !strings.HasPrefix(got, "<36>1 2026-11-24T15:04:05.123456Z mail.example.com aether ") {
		t.Fatalf("unexpected header: %s", got)
	}
	if !strings.Contains(got, " auth.login.failed [aether@32473 ") {
		t.Fatalf("missing msgid or sd element: %s", got)
	}
	if !strings.Contains(got, `targetId="bob\"\\\] injected"`) {
		t.Fatalf("structured data value not escaped: %s", got)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("RFC 5424 message contains raw newline: %q", got)
	}
	infoEv := ev
	infoEv.Action = "mailbox.create"
	infoMsg := string(sink.FormatRFC5424(infoEv))
	if !strings.HasPrefix(infoMsg, "<38>1 ") {
		t.Fatalf("expected PRI 38 (auth.info), got: %s", infoMsg)
	}
}

func TestSyslogSinkUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	sink := NewSyslogSink("udp", pc.LocalAddr().String(), 16, "aether", "host.test")
	defer sink.Close()

	ev := Event{
		ID:         7,
		CreatedAt:  time.Now().UTC(),
		Action:     "domain.verify",
		TargetType: "domain",
		TargetID:   "example.com",
		IP:         "198.51.100.1",
	}
	if err := sink.Send(context.Background(), ev); err != nil {
		t.Fatalf("Send UDP failed: %v", err)
	}
	pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom UDP failed: %v", err)
	}
	line := string(buf[:n])
	if !strings.HasPrefix(line, "<134>1 ") || !strings.Contains(line, `"action":"domain.verify"`) {
		t.Fatalf("unexpected UDP datagram: %s", line)
	}
}

func TestSyslogSinkTCPAndReconnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	received := make(chan string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(conn)
			if sc.Scan() {
				received <- sc.Text()
			}
			conn.Close()
		}
	}()

	sink := NewSyslogSink("tcp", ln.Addr().String(), 4, "aether", "host.test")
	defer sink.Close()

	ev1 := Event{ID: 1, CreatedAt: time.Now().UTC(), Action: "auth.login", TargetType: "user", TargetID: "u1"}
	if err := sink.Send(context.Background(), ev1); err != nil {
		t.Fatalf("first TCP send failed: %v", err)
	}
	select {
	case msg := <-received:
		if !strings.Contains(msg, `"id":1`) {
			t.Fatalf("unexpected first TCP message: %s", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first TCP message")
	}

	time.Sleep(20 * time.Millisecond)
	ev2 := Event{ID: 2, CreatedAt: time.Now().UTC(), Action: "auth.logout", TargetType: "user", TargetID: "u1"}
	if err := sink.Send(context.Background(), ev2); err != nil {
		t.Fatalf("second TCP send after close failed: %v", err)
	}
	select {
	case msg := <-received:
		if !strings.Contains(msg, `"id":2`) {
			t.Fatalf("unexpected second TCP message: %s", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second TCP message")
	}
}

func TestOTLPSinkHTTPJSON(t *testing.T) {
	var mu sync.Mutex
	var gotHeader string
	var gotContentType string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotHeader = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotBody = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewOTLPSink(srv.URL+"/v1/logs", map[string]string{"Authorization": "Bearer secret-token"}, "aether-test")
	defer sink.Close()

	orgID := "33333333-3333-3333-3333-333333333333"
	ev := Event{
		ID:         99,
		CreatedAt:  time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC),
		OrgID:      &orgID,
		ActorEmail: "admin@example.com",
		Action:     "auth.login.failed",
		TargetType: "user",
		TargetID:   "alice@example.com",
		IP:         "198.51.100.42",
		Metadata:   map[string]any{"method": "password"},
		TraceID:    "0102030405060708090a0b0c0d0e0f10",
		SpanID:     "0102030405060708",
	}
	if err := sink.Send(context.Background(), ev); err != nil {
		t.Fatalf("OTLP Send failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotHeader != "Bearer secret-token" || gotContentType != "application/json" {
		t.Fatalf("unexpected headers: auth=%q content-type=%q", gotHeader, gotContentType)
	}
	var parsed otlpExportLogsRequest
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("invalid OTLP JSON: %v", err)
	}
	if len(parsed.ResourceLogs) != 1 || len(parsed.ResourceLogs[0].ScopeLogs) != 1 || len(parsed.ResourceLogs[0].ScopeLogs[0].LogRecords) != 1 {
		t.Fatalf("unexpected OTLP structure: %s", gotBody)
	}
	resAttrs := parsed.ResourceLogs[0].Resource.Attributes
	if len(resAttrs) != 1 || resAttrs[0].Key != "service.name" || *resAttrs[0].Value.StringValue != "aether-test" {
		t.Fatalf("unexpected resource attributes: %+v", resAttrs)
	}
	rec := parsed.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.SeverityNumber != 13 || rec.SeverityText != "WARN" {
		t.Fatalf("expected WARN/13, got %s/%d", rec.SeverityText, rec.SeverityNumber)
	}
	if rec.TraceID != ev.TraceID || rec.SpanID != ev.SpanID {
		t.Fatalf("trace context mismatch: trace=%q span=%q", rec.TraceID, rec.SpanID)
	}
	if *rec.Body.StringValue != "auth.login.failed" {
		t.Fatalf("body = %q", *rec.Body.StringValue)
	}
	attrs := map[string]otlpAnyValue{}
	for _, a := range rec.Attributes {
		attrs[a.Key] = a.Value
	}
	if attrs["audit.id"].IntValue == nil || *attrs["audit.id"].IntValue != "99" {
		t.Fatalf("audit.id attr = %+v", attrs["audit.id"])
	}
	if attrs["audit.org_id"].StringValue == nil || *attrs["audit.org_id"].StringValue != orgID {
		t.Fatalf("audit.org_id attr = %+v", attrs["audit.org_id"])
	}
	if attrs["client.address"].StringValue == nil || *attrs["client.address"].StringValue != "198.51.100.42" {
		t.Fatalf("client.address attr = %+v", attrs["client.address"])
	}
}

type memorySink struct {
	mu     sync.Mutex
	events []Event
	closed bool
}

func (m *memorySink) Send(_ context.Context, ev Event) error {
	m.mu.Lock()
	m.events = append(m.events, ev)
	m.mu.Unlock()
	return nil
}

func (m *memorySink) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return nil
}

func TestDispatcherFlushesAndExtractsTraceContext(t *testing.T) {
	if NewDispatcher(slog.New(slog.NewTextHandler(io.Discard, nil))) != nil {
		t.Fatal("expected nil dispatcher when no sinks are configured")
	}
	sink := &memorySink{}
	d := NewDispatcher(slog.New(slog.NewTextHandler(io.Discard, nil)), sink)

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))

	d.Emit(ctx, Event{ID: 1, Action: "mailbox.create", CreatedAt: time.Now().UTC()})
	d.Emit(context.Background(), Event{ID: 2, Action: "mailbox.delete", CreatedAt: time.Now().UTC()})

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.Close(closeCtx); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	d.Emit(context.Background(), Event{ID: 3, Action: "after.close", CreatedAt: time.Now().UTC()})

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !sink.closed {
		t.Fatal("sink was not closed")
	}
	if len(sink.events) != 2 {
		t.Fatalf("got %d events, want 2", len(sink.events))
	}
	if sink.events[0].TraceID != traceID.String() || sink.events[0].SpanID != spanID.String() {
		t.Fatalf("trace context not extracted: %+v", sink.events[0])
	}
}
