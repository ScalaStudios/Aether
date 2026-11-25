//go:build integration

package integration

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

type queueListing struct {
	Summary struct {
		Total  int            `json:"total"`
		Queues map[string]int `json:"queues"`
	} `json:"summary"`
	Matched  int `json:"matched"`
	Messages []struct {
		ID          string     `json:"id"`
		Queue       string     `json:"queue"`
		ArrivalTime time.Time  `json:"arrivalTime"`
		Sender      string     `json:"sender"`
		NextAttempt *time.Time `json:"nextAttempt"`
		Recipients  []struct {
			Address     string `json:"address"`
			DelayReason string `json:"delayReason"`
		} `json:"recipients"`
	} `json:"messages"`
}

func TestMailQueue(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	addrs, err := net.LookupHost("valkey")
	if err != nil || len(addrs) == 0 {
		t.Fatalf("resolve a host that refuses SMTP: %v", err)
	}
	rcpt := "queue-" + randomSuffix() + "@[" + addrs[0] + "]"
	if err := submit(t, env(t, "AETHER_TEST_SUBMISSION", "postfix:587"), alice, w.aliceAppPassword, alice, []string{rcpt}, rfc822(alice, rcpt, "queued", "x"), false); err != nil {
		t.Fatalf("submit: %v", err)
	}

	list := func(query string) queueListing {
		var q queueListing
		w.admin.must("GET", "/admin/mail-queue?sender="+url.QueryEscape(alice)+query, nil, &q, 200)
		return q
	}
	var q queueListing
	waitUpTo(t, 90*time.Second, "the message to be deferred", func() bool {
		q = list("")
		return len(q.Messages) == 1 && q.Messages[0].Queue == "deferred"
	})
	m := q.Messages[0]
	if q.Matched != 1 || q.Summary.Queues["deferred"] < 1 || m.Sender != alice || len(m.Recipients) != 1 || m.Recipients[0].Address != rcpt {
		t.Fatalf("listing %+v", q)
	}
	if !strings.Contains(m.Recipients[0].DelayReason, "Connection refused") {
		t.Errorf("delay reason %q", m.Recipients[0].DelayReason)
	}
	if m.NextAttempt == nil || !m.NextAttempt.After(m.ArrivalTime) {
		t.Errorf("next attempt %v should follow the arrival at %v", m.NextAttempt, m.ArrivalTime)
	}
	if got := list("&recipientDomain=elsewhere.example"); got.Matched != 0 {
		t.Errorf("recipient domain filter matched %d", got.Matched)
	}
	if got := list("&error=connection+REFUSED&queue=deferred"); got.Matched != 1 {
		t.Errorf("error filter matched %d", got.Matched)
	}

	act := func(action string, body any, want int) {
		t.Helper()
		var out struct {
			Affected int `json:"affected"`
		}
		w.admin.must("POST", "/admin/mail-queue/"+action, body, &out, 200)
		if out.Affected != want {
			t.Fatalf("%s %v affected %d, want %d", action, body, out.Affected, want)
		}
	}
	byID := map[string][]string{"ids": {m.ID}}
	act("release", byID, 0)
	act("hold", byID, 1)
	if got := list(""); len(got.Messages) != 1 || got.Messages[0].Queue != "hold" || got.Messages[0].NextAttempt != nil {
		t.Fatalf("after hold: %+v", got)
	}
	act("retry", byID, 0)
	act("release", byID, 1)
	if got := list(""); len(got.Messages) != 1 || got.Messages[0].Queue != "deferred" {
		t.Fatalf("after release: %+v", got)
	}
	act("retry", byID, 1)
	act("hold", map[string]any{"filter": map[string]string{"sender": alice}}, 1)

	for body, want := range map[string]int{`{}`: 422, `{"ids":["-d"]}`: 422, `{"filter":{}}`: 422, `{"filter":{"queue":"corrupt"}}`: 422, `{"ids":["` + m.ID + `"],"filter":{"queue":"hold"}}`: 422} {
		w.admin.mustRaw("POST", "/admin/mail-queue/delete", "application/json", body, nil, want)
	}
	w.admin.mustRaw("POST", "/admin/mail-queue/purge", "application/json", `{"ids":["`+m.ID+`"]}`, nil, 404)
	if got := w.admin.do("GET", "/admin/mail-queue?limit=5000", nil, nil); got != 422 {
		t.Errorf("limit above 1000 returned %d", got)
	}

	act("delete", map[string]any{"filter": map[string]string{"sender": alice, "queue": "hold"}}, 1)
	if got := list(""); got.Matched != 0 {
		t.Fatalf("after delete: %+v", got)
	}

	rows, err := testDB(t).Query(context.Background(), `
		SELECT action || ' ' || (metadata->>'affected') FROM audit_events
		WHERE action LIKE 'mail_queue.%' AND (target_id = $1 OR metadata->'filter'->>'sender' = $2) ORDER BY id`, m.ID, alice)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for rows.Next() {
		var e string
		rows.Scan(&e)
		events = append(events, e)
	}
	want := []string{"mail_queue.release 0", "mail_queue.hold 1", "mail_queue.retry 0", "mail_queue.release 1", "mail_queue.retry 1", "mail_queue.hold 1", "mail_queue.delete 1"}
	if !slices.Equal(events, want) {
		t.Errorf("audit events %v, want %v", events, want)
	}

	waitUpTo(t, 90*time.Second, "the queue gauges", func() bool {
		resp, err := http.Get(env(t, "AETHER_TEST_METRICS", "http://api:9090/metrics"))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(raw), `aether_mail_queue_messages{queue="deferred"}`) && strings.Contains(string(raw), "aether_mail_queue_oldest_seconds ")
	})
}
