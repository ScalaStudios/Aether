package api

import (
	"errors"
	"math"
	"net/http/httptest"
	"testing"
)

func limitCode(err error) string {
	var e *limitError
	if errors.As(err, &e) {
		return e.code
	}
	return ""
}

func TestCheckCount(t *testing.T) {
	cases := []struct {
		used, limit int64
		want        string
	}{
		{5, 0, ""},
		{4, 5, ""},
		{5, 5, "mailbox_limit_reached"},
		{9, 5, "mailbox_limit_reached"},
	}
	for _, c := range cases {
		if got := limitCode(checkCount("mailbox_limit_reached", "mailboxes", c.used, c.limit)); got != c.want {
			t.Errorf("used %d limit %d: got %q, want %q", c.used, c.limit, got, c.want)
		}
	}
}

func TestCheckStorage(t *testing.T) {
	cases := []struct {
		allocated, quota, limit int64
		want                    string
	}{
		{0, 0, 0, ""},
		{math.MaxInt64, math.MaxInt64, 0, ""},
		{0, 0, 1000, "quota_required"},
		{400, 600, 1000, ""},
		{401, 600, 1000, "storage_limit_reached"},
		{100, math.MaxInt64, 1000, "storage_limit_reached"},
		{1500, 1, 1000, "storage_limit_reached"},
	}
	for _, c := range cases {
		if got := limitCode(checkStorage(c.allocated, c.quota, c.limit)); got != c.want {
			t.Errorf("allocated %d quota %d limit %d: got %q, want %q", c.allocated, c.quota, c.limit, got, c.want)
		}
	}
}

func TestWriteLimitError(t *testing.T) {
	w := httptest.NewRecorder()
	if !writeLimitError(w, errors.Join(errors.New("tx"), &limitError{"domain_limit_reached", "full"})) || w.Code != 409 {
		t.Fatalf("wrapped limit error: handled with %d", w.Code)
	}
	if writeLimitError(httptest.NewRecorder(), errors.New("other")) {
		t.Fatal("non-limit error was handled")
	}
}
