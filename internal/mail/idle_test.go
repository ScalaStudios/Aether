package mail

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func TestWatchReportsOnlyNewMail(t *testing.T) {
	mem := imapmemserver.New()
	mem.AddUser(imapmemserver.NewUser("user", "pass"))
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	dial := func(handler *imapclient.UnilateralDataHandler) *imapclient.Client {
		t.Helper()
		ic, err := imapclient.DialInsecure(ln.Addr().String(), &imapclient.Options{UnilateralDataHandler: handler})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ic.Close() })
		if err := ic.Login("user", "pass").Wait(); err != nil {
			t.Fatal(err)
		}
		return ic
	}

	writer := dial(nil)
	if err := writer.Create("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	appendMessage(t, writer, "INBOX", "old@example.com")

	notes := newMailboxNotes()
	watcher := dial(&imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { notes.add("expunge") },
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages != nil {
				notes.add("exists")
			}
		},
		Fetch: func(msg *imapclient.FetchMessageData) {
			_, _ = msg.Collect()
			notes.add("fetch")
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan FolderChange, 4)
	errCh := make(chan error, 1)
	go func() {
		errCh <- watchLoop(ctx, watcher, "INBOX", notes, func(change FolderChange) { got <- change })
	}()
	time.Sleep(300 * time.Millisecond)
	select {
	case change := <-got:
		t.Fatalf("existing mail was reported: %+v", change)
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
	appendMessage(t, writer, "INBOX", "new@example.com")
	select {
	case change := <-got:
		if change.Kind != "new" || len(change.Messages) != 1 || change.Messages[0].Subject != "new@example.com" || change.Messages[0].Seen || change.Messages[0].Received.IsZero() {
			t.Fatalf("change = %+v", change)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the new message")
	}
	cancel()
}

func TestWatchSelectsTheFolderBeforeLoggingOut(t *testing.T) {
	c, afterLogin := fakeIMAP(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Watch(ctx, "team@example.test", "INBOX", func(FolderChange) {}); err != nil {
		t.Fatal(err)
	}
	got := string(<-afterLogin)
	examine, logout := strings.Index(got, "EXAMINE"), strings.Index(got, "LOGOUT")
	if examine < 0 || logout < examine {
		t.Fatalf("commands after login = %q, want EXAMINE before LOGOUT", got)
	}
}
