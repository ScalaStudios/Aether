package api

import "testing"

func TestEffectiveSendingCaps(t *testing.T) {
	caps := effectiveSendingCaps([]byte(`{"messagesPerHour": 10, "spamPercent": 0}`))
	if caps.MessagesPerHour != 10 || caps.SpamPercent != 0 || caps.MessagesPerDay != defaultSendingCaps.MessagesPerDay {
		t.Fatalf("caps = %+v", caps)
	}
	if got := effectiveSendingCaps([]byte(`not json`)); got != defaultSendingCaps {
		t.Fatalf("invalid overrides gave %+v", got)
	}
}

func TestSendingLimitsProblem(t *testing.T) {
	cases := []struct {
		limits map[string]int
		bad    bool
	}{
		{map[string]int{"messagesPerHour": 50, "spamPercent": 100}, false},
		{map[string]int{"messagesPerHour": -1}, true},
		{map[string]int{"spamPercent": 101}, true},
		{map[string]int{"bogus": 1}, true},
	}
	for _, c := range cases {
		if got := sendingLimitsProblem(c.limits) != ""; got != c.bad {
			t.Errorf("%v: problem = %v, want %v", c.limits, got, c.bad)
		}
	}
}

func TestOverLimit(t *testing.T) {
	exceeded, near := overLimit([]sendingCount{{limit: "a", count: 7, max: 10}, {limit: "b", count: 8, max: 10}, {limit: "c", count: 500, max: 0}})
	if exceeded != nil || len(near) != 1 || near[0].limit != "b" {
		t.Fatalf("exceeded = %v, near = %v", exceeded, near)
	}
	exceeded, _ = overLimit([]sendingCount{{limit: "a", count: 10, max: 10}, {limit: "b", count: 11, max: 10}})
	if exceeded == nil || exceeded.limit != "b" {
		t.Fatalf("exceeded = %v", exceeded)
	}
}

func TestSpamExceeded(t *testing.T) {
	cases := []struct {
		percent        int
		messages, spam int64
		want           bool
	}{
		{50, 9, 9, false},
		{50, 10, 4, false},
		{50, 10, 5, true},
		{0, 100, 100, false},
	}
	for _, c := range cases {
		if got := spamExceeded(c.percent, c.messages, c.spam); got != c.want {
			t.Errorf("spamExceeded(%d, %d, %d) = %v", c.percent, c.messages, c.spam, got)
		}
	}
}
