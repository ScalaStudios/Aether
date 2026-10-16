package mail

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func memIMAP(t *testing.T) *imapclient.Client {
	t.Helper()
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
	ic, err := imapclient.DialInsecure(ln.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ic.Close() })
	if err := ic.Login("user", "pass").Wait(); err != nil {
		t.Fatal(err)
	}
	if err := ic.Create("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	return ic
}

func appendMessage(t *testing.T, ic *imapclient.Client, folder, id string, flags ...imap.Flag) {
	t.Helper()
	raw := []byte("Message-ID: <" + id + ">\r\nFrom: a@example.com\r\nTo: b@example.com\r\nSubject: " + id + "\r\n\r\nBody of " + id + "\r\n")
	cmd := ic.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)})
	cmd.Write(raw)
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func folderMessages(t *testing.T, ic *imapclient.Client, folder string) map[string][]imap.Flag {
	t.Helper()
	if _, err := ic.Select(folder, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	msgs, err := ic.Fetch(imap.SeqSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{Envelope: true, Flags: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]imap.Flag{}
	for _, m := range msgs {
		out[m.Envelope.MessageID] = m.Flags
	}
	return out
}

func TestImportCopiesFoldersAndSkipsDuplicates(t *testing.T) {
	from, to := memIMAP(t), memIMAP(t)
	if err := from.Create("Work", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	appendMessage(t, from, "INBOX", "one@example.com", imap.FlagSeen)
	appendMessage(t, from, "INBOX", "two@example.com")
	appendMessage(t, from, "Work", "three@example.com", imap.FlagFlagged)
	appendMessage(t, to, "INBOX", "one@example.com")

	sources, err := from.List("", "*", nil).Collect()
	if err != nil {
		t.Fatal(err)
	}
	targets, err := to.List("", "*", nil).Collect()
	if err != nil {
		t.Fatal(err)
	}
	var p ImportProgress
	for _, folder := range sources {
		target, err := importTarget(to, folder, targets)
		if err != nil {
			t.Fatal(err)
		}
		if err := copyFolder(context.Background(), from, to, folder.Mailbox, target, &p, func(ImportProgress) {}); err != nil {
			t.Fatal(err)
		}
	}

	if p.Copied != 2 || p.Skipped != 1 {
		t.Errorf("copied %d, skipped %d; want 2 and 1", p.Copied, p.Skipped)
	}
	inbox := folderMessages(t, to, "INBOX")
	if len(inbox) != 2 {
		t.Errorf("INBOX has %d messages, want 2", len(inbox))
	}
	work := folderMessages(t, to, "Work")
	if !slices.Contains(work["three@example.com"], imap.FlagFlagged) {
		t.Errorf("Work = %v, want three@example.com flagged", work)
	}
}
