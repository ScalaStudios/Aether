package api

import (
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-milter"

	"github.com/scalastudios/aether/internal/mailflow"
)

func milterSession(t *testing.T, after bool) *milter.ClientSession {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go s.ServeMilter(ln, after)
	c := milter.NewClientWithOptions("tcp", ln.Addr().String(), milter.ClientOptions{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		ActionMask:   milter.OptAddHeader | milter.OptChangeHeader | milter.OptChangeBody | milter.OptAddRcpt | milter.OptRemoveRcpt,
	})
	t.Cleanup(func() { c.Close() })
	session, err := c.Session()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestRelayedRequireTLSHeaderIsReissuedByTheMilter(t *testing.T) {
	session := milterSession(t, false)
	if err := session.Macros(milter.CodeMail, "{auth_authen}", relayUser); err != nil {
		t.Fatal(err)
	}
	if act, err := session.Mail("sender@example.com", nil); err != nil || act.Code != milter.ActContinue {
		t.Fatalf("mail: %+v %v", act, err)
	}
	session.Rcpt("partner@example.org", nil)
	session.HeaderField(tlsHeader, "yes")
	session.HeaderField("Subject", "hello")
	session.HeaderEnd()
	mods, act, err := session.BodyReadFrom(strings.NewReader("body\r\n"))
	if err != nil || act.Code != milter.ActAccept {
		t.Fatalf("body: %+v %v", act, err)
	}
	if len(mods) != 2 || mods[0].Code != milter.ActChangeHeader || mods[0].HeaderValue != "" || mods[1].Code != milter.ActAddHeader || mods[1].HeaderName != tlsHeader || mods[1].HeaderValue != "yes" {
		t.Fatalf("modifications %+v", mods)
	}
}

func TestRelayedMailSkipsTheAfterSpamPass(t *testing.T) {
	session := milterSession(t, true)
	session.Macros(milter.CodeMail, "{auth_authen}", relayUser)
	if act, err := session.Mail("sender@example.com", nil); err != nil || act.Code != milter.ActAccept {
		t.Fatalf("mail: %+v %v", act, err)
	}
}

func TestFlowEditsDeleteEveryOccurrenceFromTheLast(t *testing.T) {
	e := &flowEdits{header: []mailflow.Field{{Name: "X-Spam-Status", Value: "a"}, {Name: "Subject", Value: "s"}, {Name: "x-spam-status", Value: "b"}}}
	e.remove("X-Spam-Status")
	e.set("Content-Type", "text/plain")
	e.set("Subject", "")
	if len(e.ops) != 4 || e.ops[0].index != 2 || e.ops[1].index != 1 || !e.ops[2].add || e.ops[3].value != "" {
		t.Fatalf("ops %+v", e.ops)
	}
	if len(e.header) != 1 || e.header[0].Name != "Content-Type" {
		t.Fatalf("header %+v", e.header)
	}
}
