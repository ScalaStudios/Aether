package queue

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const fixture = `{"queue_name": "deferred", "queue_id": "4B3E1D2A3F", "arrival_time": 1700000300, "message_size": 2048, "forced_expire": false, "sender": "Alice@Example.com", "recipients": [{"address": "bob@gmail.com", "delay_reason": "connect to gmail-smtp-in.l.google.com[142.250.0.27]:25: Connection refused"}, {"address": "carol@yahoo.com", "delay_reason": "host mx.yahoo.com said: 421 4.7.0 Try again later"}]}
{"queue_name": "hold", "queue_id": "9C1F00AB12", "arrival_time": 1700000100, "message_size": 512, "forced_expire": false, "sender": "spam@example.com", "recipients": [{"address": "victim@gmail.com"}]}
{"queue_name": "active", "queue_id": "77AA00BB11", "arrival_time": 1700000200, "message_size": 1024, "forced_expire": false, "sender": "", "recipients": [{"address": "dave@example.org"}]}
`

type fakePostfix struct {
	agent *Agent
	log   string
}

func newFakePostfix(t *testing.T) *fakePostfix {
	dir := t.TempDir()
	queueFile := filepath.Join(dir, "queue.json")
	logFile := filepath.Join(dir, "calls.log")
	if err := os.WriteFile(queueFile, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	script := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	postqueue := script("postqueue", `case "$1" in
-j) cat '`+queueFile+`' ;;
-i) echo "retry $2" >> '`+logFile+`' ;;
esac
`)
	postsuper := script("postsuper", `ids=$(cat)
echo "postsuper $1" $ids >> '`+logFile+`'
n=$(echo $ids | wc -w)
case "$1" in
-h) echo "postsuper: Placed on hold: $n messages" >&2 ;;
-H) echo "postsuper: Released from hold: $n message" >&2 ;;
-d) echo "postsuper: Deleted: $n messages" >&2 ;;
esac
`)
	spool := filepath.Join(dir, "spool")
	if err := os.MkdirAll(filepath.Join(spool, "deferred", "4"), 0o700); err != nil {
		t.Fatal(err)
	}
	deferred := filepath.Join(spool, "deferred", "4", "4B3E1D2A3F")
	if err := os.WriteFile(deferred, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	next := time.Unix(1700004000, 0)
	if err := os.Chtimes(deferred, next, next); err != nil {
		t.Fatal(err)
	}
	return &fakePostfix{agent: &Agent{Key: "secret", Spool: spool, Postqueue: postqueue, Postsuper: postsuper}, log: logFile}
}

func (f *fakePostfix) calls(t *testing.T) []string {
	raw, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func ids(msgs []Message) []string {
	out := []string{}
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

func TestFilterMatch(t *testing.T) {
	var msgs []Message
	if err := Decode(strings.NewReader(fixture), func(m Message) { msgs = append(msgs, m) }); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		filter Filter
		want   []string
	}{
		{Filter{}, []string{"4B3E1D2A3F", "9C1F00AB12", "77AA00BB11"}},
		{Filter{Sender: "alice@example"}, []string{"4B3E1D2A3F"}},
		{Filter{RecipientDomain: "@GMAIL.com"}, []string{"4B3E1D2A3F", "9C1F00AB12"}},
		{Filter{RecipientDomain: "mail.com"}, []string{}},
		{Filter{Error: "connection REFUSED"}, []string{"4B3E1D2A3F"}},
		{Filter{RecipientDomain: "yahoo.com", Error: "connection refused"}, []string{}},
		{Filter{RecipientDomain: "yahoo.com", Error: "try again"}, []string{"4B3E1D2A3F"}},
		{Filter{Queue: "hold", RecipientDomain: "gmail.com"}, []string{"9C1F00AB12"}},
	}
	for _, c := range cases {
		got := []string{}
		for i := range msgs {
			if c.filter.Match(&msgs[i]) {
				got = append(got, msgs[i].ID)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%+v matched %v, want %v", c.filter, got, c.want)
		}
	}
	if msgs[0].ArrivalTime != time.Unix(1700000300, 0).UTC() || msgs[0].Size != 2048 || msgs[0].Recipients[1].DelayReason == "" {
		t.Errorf("decoded %+v", msgs[0])
	}
}

func TestAgentList(t *testing.T) {
	f := newFakePostfix(t)
	l, err := f.agent.List(context.Background(), Filter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if l.Summary.Total != 3 || l.Summary.Queues["deferred"] != 1 || l.Summary.Queues["hold"] != 1 || l.Summary.Queues["active"] != 1 || l.Summary.Queues["incoming"] != 0 {
		t.Errorf("summary %+v", l.Summary)
	}
	if l.Summary.OldestArrival == nil || !l.Summary.OldestArrival.Equal(time.Unix(1700000200, 0)) {
		t.Errorf("oldest arrival %v, want the active message because held mail is not counted", l.Summary.OldestArrival)
	}
	if l.Matched != 3 || !slices.Equal(ids(l.Messages), []string{"9C1F00AB12", "77AA00BB11"}) {
		t.Errorf("matched %d, messages %v; want 3 matched and the two oldest", l.Matched, ids(l.Messages))
	}

	l, err = f.agent.List(context.Background(), Filter{Queue: "deferred"}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Messages) != 1 || l.Messages[0].NextAttempt == nil || !l.Messages[0].NextAttempt.Equal(time.Unix(1700004000, 0)) {
		t.Fatalf("deferred message %+v should carry the queue file's modification time as the next attempt", l.Messages)
	}

	l, err = f.agent.List(context.Background(), Filter{}, 0)
	if err != nil || l.Matched != 3 || len(l.Messages) != 0 {
		t.Fatalf("summary-only listing returned %+v, %v", l, err)
	}
}

func TestAgentApply(t *testing.T) {
	f := newFakePostfix(t)
	ctx := context.Background()

	n, err := f.agent.Apply(ctx, "hold", ActionRequest{Filter: &Filter{RecipientDomain: "gmail.com"}})
	if err != nil || n != 1 {
		t.Fatalf("hold by recipient domain affected %d, %v; want 1 because one match is already on hold", n, err)
	}
	n, err = f.agent.Apply(ctx, "retry", ActionRequest{IDs: []string{"4B3E1D2A3F", "9C1F00AB12", "77AA00BB11"}})
	if err != nil || n != 1 {
		t.Fatalf("retry affected %d, %v; want only the deferred message", n, err)
	}
	n, err = f.agent.Apply(ctx, "release", ActionRequest{IDs: []string{"4B3E1D2A3F", "9C1F00AB12"}})
	if err != nil || n != 1 {
		t.Fatalf("release affected %d, %v; want only the held message", n, err)
	}
	n, err = f.agent.Apply(ctx, "delete", ActionRequest{Filter: &Filter{Sender: "example.com"}})
	if err != nil || n != 2 {
		t.Fatalf("delete by sender affected %d, %v", n, err)
	}
	n, err = f.agent.Apply(ctx, "delete", ActionRequest{IDs: []string{"AAAAAAAAAA"}})
	if err != nil || n != 0 {
		t.Fatalf("deleting a message that left the queue affected %d, %v", n, err)
	}
	want := []string{
		"postsuper -h 4B3E1D2A3F",
		"retry 4B3E1D2A3F",
		"postsuper -H 9C1F00AB12",
		"postsuper -d 4B3E1D2A3F 9C1F00AB12",
	}
	if got := f.calls(t); !slices.Equal(got, want) {
		t.Fatalf("postfix commands\n%q\nwant\n%q", got, want)
	}
}

func TestActionRequestValidate(t *testing.T) {
	bad := []ActionRequest{
		{},
		{Filter: &Filter{}},
		{IDs: []string{"4B3E1D2A3F"}, Filter: &Filter{Queue: "hold"}},
		{IDs: []string{"../etc/passwd"}},
		{IDs: []string{"-d"}},
		{Filter: &Filter{Queue: "corrupt"}},
		{IDs: slices.Repeat([]string{"4B3E1D2A3F"}, MaxLimit+1)},
	}
	for _, in := range bad {
		if in.Validate() == nil {
			t.Errorf("%+v was accepted", in)
		}
	}
	for _, in := range []ActionRequest{{IDs: []string{"4B3E1D2A3F", "4XsS9V2TQRz1Lw"}}, {Filter: &Filter{Queue: "deferred"}}} {
		if err := in.Validate(); err != nil {
			t.Errorf("%+v was refused: %v", in, err)
		}
	}
}

func TestClientAgainstAgent(t *testing.T) {
	f := newFakePostfix(t)
	srv := httptest.NewServer(f.agent.Handler())
	defer srv.Close()
	ctx := context.Background()

	if _, err := NewClient(srv.URL, "wrong").List(ctx, Filter{}, 10); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("wrong key: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/queue", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("request without a key: %v %v", resp, err)
	}

	c := NewClient(srv.URL+"/", "secret")
	l, err := c.List(ctx, Filter{Sender: "spam@", Queue: "hold"}, 10)
	if err != nil || l.Matched != 1 || l.Summary.Total != 3 || !slices.Equal(ids(l.Messages), []string{"9C1F00AB12"}) {
		t.Fatalf("filtered list %+v, %v", l, err)
	}
	n, err := c.Apply(ctx, "delete", ActionRequest{IDs: []string{"9C1F00AB12"}})
	if err != nil || n != 1 {
		t.Fatalf("delete through the client affected %d, %v", n, err)
	}
	if _, err := c.Apply(ctx, "purge", ActionRequest{IDs: []string{"9C1F00AB12"}}); err == nil {
		t.Fatal("unknown action was accepted")
	}
	if _, err := c.Apply(ctx, "hold", ActionRequest{}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("empty action request: %v", err)
	}
}
