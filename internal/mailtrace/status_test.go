package mailtrace

import (
	"maps"
	"testing"
)

func TestSummarize(t *testing.T) {
	ev := func(kind, rcpt, orig string) traceEvent { return traceEvent{kind, rcpt, orig} }
	cases := []struct {
		name       string
		events     []traceEvent
		recipients []string
		spam       bool
		removed    bool
		expected   int
		want       string
		per        map[string]string
	}{
		{"just received", nil, nil, false, false, 0, "queued", map[string]string{}},
		{"delivered", []traceEvent{ev("delivered", "a@x", "")}, []string{"a@x"}, false, true, 1, "delivered", map[string]string{"a@x": "delivered"}},
		{"spam lands in junk", []traceEvent{ev("delivered", "a@x", "")}, []string{"a@x"}, true, true, 1, "junk", map[string]string{"a@x": "junk"}},
		{"spam forwarded outside is not junk", []traceEvent{ev("forwarded", "a@y", "f@x")}, []string{"f@x", "a@y"}, true, true, 1, "delivered", map[string]string{"f@x": "delivered", "a@y": "delivered"}},
		{"second recipient not tried yet", []traceEvent{ev("delivered", "a@x", "")}, []string{"a@x"}, false, false, 2, "queued", map[string]string{"a@x": "delivered"}},
		{"deferred then delivered", []traceEvent{ev("deferred", "a@x", ""), ev("delivered", "a@x", "")}, []string{"a@x"}, false, true, 1, "delivered", map[string]string{"a@x": "delivered"}},
		{"still deferred", []traceEvent{ev("deferred", "a@x", "")}, []string{"a@x"}, false, false, 1, "deferred", map[string]string{"a@x": "deferred"}},
		{"one of two bounced", []traceEvent{ev("delivered", "a@x", ""), ev("bounced", "b@y", ""), ev("notified", "", "")}, []string{"a@x", "b@y"}, false, true, 2, "bounced",
			map[string]string{"a@x": "delivered", "b@y": "bounced"}},
		{"expired", []traceEvent{ev("deferred", "a@y", ""), ev("expired", "", "")}, []string{"a@y"}, false, true, 1, "bounced", map[string]string{"a@y": "bounced"}},
		{"one recipient rejected at RCPT", []traceEvent{ev("rejected", "ghost@x", ""), ev("delivered", "a@x", "")}, []string{"ghost@x", "a@x"}, false, true, 1, "rejected",
			map[string]string{"ghost@x": "rejected", "a@x": "delivered"}},
		{"rejected by content filter", []traceEvent{ev("rejected", "", "")}, []string{"a@x"}, false, true, 0, "rejected", map[string]string{"a@x": "rejected"}},
		{"held", []traceEvent{ev("held", "", "")}, []string{"a@x"}, false, false, 1, "held", map[string]string{"a@x": "held"}},
		{"released and delivered", []traceEvent{ev("held", "", ""), ev("released", "", ""), ev("delivered", "a@x", "")}, []string{"a@x"}, false, true, 1, "delivered", map[string]string{"a@x": "delivered"}},
		{"deleted by an administrator", []traceEvent{ev("delivered", "a@x", ""), ev("deferred", "b@y", ""), ev("deleted", "", "")}, []string{"a@x", "b@y"}, false, true, 2, "deleted",
			map[string]string{"a@x": "delivered", "b@y": "deleted"}},
		{"alias takes the worst expansion", []traceEvent{ev("delivered", "a@x", "team@x"), ev("bounced", "b@y", "team@x")}, []string{"team@x", "a@x", "b@y"}, false, true, 2, "bounced",
			map[string]string{"team@x": "bounced", "a@x": "delivered", "b@y": "bounced"}},
		{"removed without records", nil, []string{"a@x"}, false, true, 1, "unknown", map[string]string{"a@x": "unknown"}},
	}
	for _, c := range cases {
		got, per := summarize(c.events, c.recipients, c.spam, c.removed, c.expected)
		if got != c.want || !maps.Equal(per, c.per) {
			t.Errorf("%s: got %s %v, want %s %v", c.name, got, per, c.want, c.per)
		}
	}
}
