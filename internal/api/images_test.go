package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scalastudios/aether/internal/config"
	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func testImageServer() *Server {
	return &Server{
		cfg: &config.Config{SecretKey: []byte("0123456789abcdef0123456789abcdef")},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestImageProxyToken(t *testing.T) {
	s := testImageServer()
	raw := "https://cdn.example/logo.png?utm_source=email"
	normalized, ok := mailpkg.NormalizeImageURL(raw)
	if !ok {
		t.Fatal("normalize")
	}
	signed := s.signImageProxy(normalized)
	token := strings.TrimPrefix(signed, "/api/v1/image-proxy/")
	if token == signed || strings.Contains(token, "cdn.example") {
		t.Fatalf("token = %q", signed)
	}
	got, err := s.openImageProxy(token)
	if err != nil || got != normalized {
		t.Fatalf("open = %q %v", got, err)
	}
	if _, err := s.openImageProxy(token + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}
	if s.signImageProxy("https://127.0.0.1/a.png") != "" || s.signImageProxy("https://pixel.mailtrack.io/a.gif") != "" {
		t.Fatal("signed a URL the proxy must not fetch")
	}
	body, err := json.Marshal(imageTicket{U: "https://cdn.example/logo.png", E: time.Now().Add(-time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, s.cfg.SecretKey)
	mac.Write([]byte(imageProxyContext))
	mac.Write(body)
	expired := base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := s.openImageProxy(expired); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestFetchProxiedImage(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	var gotUA, gotCookie, gotAccept string
	var hops int
	s := testImageServer()
	s.imageClient = &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			hops++
			gotUA, gotCookie, gotAccept = r.Header.Get("User-Agent"), r.Header.Get("Cookie"), r.Header.Get("Accept")
			switch r.URL.Path {
			case "/redirect":
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://cdn.example/logo.png"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			case "/html":
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("<html></html>")), Request: r}, nil
			case "/huge":
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.LimitReader(neverEnding('A'), maxProxyImageBytes+2)), Request: r}, nil
			default:
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(png))), Request: r}, nil
			}
		}),
	}
	body, ctype, err := s.fetchProxiedImage(context.Background(), "https://cdn.example/redirect")
	if err != nil || ctype != "image/png" || string(body) != string(png) || hops != 2 {
		t.Fatalf("redirect fetch: %v %s hops=%d err=%v", body, ctype, hops, err)
	}
	if gotUA != imageProxyUserAgent || gotCookie != "" || !strings.Contains(gotAccept, "image/png") {
		t.Fatalf("headers ua=%q cookie=%q accept=%q", gotUA, gotCookie, gotAccept)
	}
	if _, _, err := s.fetchProxiedImage(context.Background(), "https://cdn.example/html"); err == nil {
		t.Fatal("html accepted as an image")
	}
	if _, _, err := s.fetchProxiedImage(context.Background(), "https://cdn.example/huge"); err == nil {
		t.Fatal("oversized image accepted")
	}
	hops = 0
	if _, _, err := s.fetchProxiedImage(context.Background(), "https://pixel.mailtrack.io/a.gif"); err == nil || hops != 0 {
		t.Fatalf("tracker was fetched, hops=%d err=%v", hops, err)
	}
	s.imageClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"http://127.0.0.1/secret"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if _, _, err := s.fetchProxiedImage(context.Background(), "https://cdn.example/logo.png"); err == nil {
		t.Fatal("redirect to loopback accepted")
	}
}

func TestImageClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("loopback server was contacted")
	}))
	t.Cleanup(srv.Close)
	client := newImageClient()
	if _, err := client.Get(srv.URL); err == nil {
		t.Fatal("image client dialed a loopback address")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := client.Get("http://" + ln.Addr().String() + "/a.png"); err == nil {
		t.Fatal("image client dialed an explicit loopback address")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type neverEnding byte

func (b neverEnding) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}
