package mail

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

type relayBackend struct {
	mu        sync.Mutex
	datas     int
	failData  int
	delivered map[string]int
}

func (b *relayBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &relaySession{b: b}, nil
}

type relaySession struct {
	b     *relayBackend
	rcpts []string
}

func (s *relaySession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (s *relaySession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, user, pass string) error {
		if user != "aether-sieve-relay" || pass != "secret" {
			return smtp.ErrAuthFailed
		}
		return nil
	}), nil
}
func (s *relaySession) Mail(string, *smtp.MailOptions) error { return nil }
func (s *relaySession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if to == "gone@example.com" {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such user"}
	}
	s.rcpts = append(s.rcpts, to)
	return nil
}
func (s *relaySession) Data(r io.Reader) error {
	io.Copy(io.Discard, r)
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.datas++
	if s.b.datas == s.b.failData {
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Try again later"}
	}
	for _, r := range s.rcpts {
		s.b.delivered[r]++
	}
	return nil
}
func (s *relaySession) Reset()        { s.rcpts = nil }
func (s *relaySession) Logout() error { return nil }

func TestRelayResumesAfterAFailedBatch(t *testing.T) {
	key, der, roots := localhostCert(t)
	backend := &relayBackend{failData: 2, delivered: map[string]int{}}
	srv := smtp.NewServer(backend)
	srv.Domain = "localhost"
	srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.ReadTimeout, srv.WriteTimeout = 10*time.Second, 10*time.Second
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	c := &Client{relayAddr: "localhost:" + port, relayPassword: "secret", tls: &tls.Config{ServerName: "localhost", RootCAs: roots}}

	all := []string{"gone@example.com"}
	for i := range relayBatch + 20 {
		all = append(all, fmt.Sprintf("member%d@example.com", i))
	}
	var done, rejected []string
	progress := func(a, r []string) error {
		done = append(done, a...)
		done = append(done, r...)
		rejected = append(rejected, r...)
		return nil
	}
	raw := []byte("Subject: hi\r\n\r\nbody\r\n")
	if err := c.Relay("sender@example.com", all, raw, progress); err == nil {
		t.Fatal("relay reported success although the second batch failed")
	}
	if len(done) != relayBatch || !slices.Equal(rejected, []string{"gone@example.com"}) {
		t.Fatalf("after the failure: %d recipients done, rejected %v", len(done), rejected)
	}
	remaining := slices.DeleteFunc(slices.Clone(all), func(r string) bool { return slices.Contains(done, r) })
	if err := c.Relay("sender@example.com", remaining, raw, progress); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(backend.delivered) != len(all)-1 {
		t.Fatalf("%d of %d members received the message", len(backend.delivered), len(all)-1)
	}
	for rcpt, n := range backend.delivered {
		if n != 1 {
			t.Fatalf("%s received the message %d times", rcpt, n)
		}
	}
}
