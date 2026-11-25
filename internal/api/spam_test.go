package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSpamEndpointsRejectPublicCallersAndUnknownClasses(t *testing.T) {
	s := &Server{}
	cases := []struct {
		name   string
		remote string
		target string
		handle http.HandlerFunc
		want   int
	}{
		{"learn from public address", "203.0.113.7:4000", "/internal/spam-learn?class=spam&user=a@x.test", s.learnSpam, http.StatusForbidden},
		{"trust from public address", "203.0.113.7:4000", "/internal/trusted-sender", s.trustedSender, http.StatusForbidden},
		{"unknown class", "10.0.0.5:4000", "/internal/spam-learn?class=phish&user=a@x.test", s.learnSpam, http.StatusBadRequest},
		{"missing class", "10.0.0.5:4000", "/internal/spam-learn?user=a@x.test", s.learnSpam, http.StatusBadRequest},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, c.target, strings.NewReader("Subject: x\r\n\r\nbody"))
		r.RemoteAddr = c.remote
		w := httptest.NewRecorder()
		c.handle(w, r)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
}
