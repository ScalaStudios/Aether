package mail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync/atomic"
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

func appendLarge(t *testing.T, ic *imapclient.Client, folder, id string, size int) {
	t.Helper()
	raw := []byte("Message-ID: <" + id + ">\r\nSubject: " + id + "\r\n\r\n" + strings.Repeat("x", size) + "\r\n")
	cmd := ic.Append(folder, int64(len(raw)), nil)
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
		if err := copyFolder(context.Background(), from, nil, to, folder.Mailbox, target, 1<<20, &p, func(ImportProgress) {}, nil); err != nil {
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

func TestImportNeverTouchesSharedFolders(t *testing.T) {
	from, to := memIMAP(t), memIMAP(t)
	support := SharedPrefix + "support@example.com"
	for _, f := range []string{support, support + "/Sent", SharedPrefix + "other@example.com"} {
		if err := from.Create(f, nil).Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if err := to.Create(support, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	appendMessage(t, from, "INBOX", "own@example.com")
	appendMessage(t, from, support, "known@example.com")
	appendMessage(t, from, support, "injected@example.com")
	appendMessage(t, from, support+"/Sent", "sub@example.com")
	appendMessage(t, from, SharedPrefix+"other@example.com", "created@example.com")
	appendMessage(t, to, support, "known@example.com")

	var p ImportProgress
	if err := importFolders(context.Background(), from, nil, to, "source", 1<<20, func(last ImportProgress) { p = last }, nil); err != nil {
		t.Fatal(err)
	}
	if p.FoldersDone != 4 || p.FoldersTotal != 4 || p.Copied != 1 || p.Skipped != 0 {
		t.Errorf("progress = %+v, want 4 folders done, 1 copied, 0 skipped", p)
	}
	if _, known := folderMessages(t, to, support)["known@example.com"]; !known || len(folderMessages(t, to, support)) != 1 {
		t.Errorf("%s changed, want only known@example.com", support)
	}
	list, err := to.List("", "*", nil).Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range list {
		if SharedFolder(l.Mailbox) && l.Mailbox != support {
			t.Errorf("import created %s", l.Mailbox)
		}
	}
}

func TestImportSkipsMessagesOverTheSizeLimit(t *testing.T) {
	from, to := memIMAP(t), memIMAP(t)
	appendMessage(t, from, "INBOX", "one@example.com")
	appendLarge(t, from, "INBOX", "big@example.com", 4096)
	appendMessage(t, from, "INBOX", "two@example.com")

	var p ImportProgress
	if err := copyFolder(context.Background(), from, nil, to, "INBOX", "INBOX", 1024, &p, func(ImportProgress) {}, nil); err != nil {
		t.Fatal(err)
	}
	if p.Copied != 2 || p.Skipped != 1 {
		t.Errorf("copied %d, skipped %d; want 2 and 1", p.Copied, p.Skipped)
	}
	if inbox := folderMessages(t, to, "INBOX"); len(inbox) != 2 || inbox["big@example.com"] != nil {
		t.Errorf("INBOX = %v, want the two small messages", inbox)
	}
}

func TestCollectLimitedDropsBodiesOverTheLimit(t *testing.T) {
	from := memIMAP(t)
	appendLarge(t, from, "INBOX", "big@example.com", 4096)
	if _, err := from.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	body := &imap.FetchItemBodySection{Peek: true}
	for _, limit := range []int64{1024, 1 << 20} {
		cmd := from.Fetch(imap.SeqSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{UID: true, Envelope: true, BodySection: []*imap.FetchItemBodySection{body}})
		m, err := collectLimited(cmd.Next(), nil, limit)
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Close(); err != nil {
			t.Fatal(err)
		}
		raw := m.FindBodySection(body)
		if limit == 1024 && raw != nil {
			t.Errorf("limit %d kept a %d byte body", limit, len(raw))
		}
		if limit > 1024 && (len(raw) < 4096 || importMessageID(m) != "big@example.com") {
			t.Errorf("limit %d returned %d bytes for %q, want the whole message", limit, len(raw), importMessageID(m))
		}
	}
}

func TestImportFailureReleasesTheSourceFetch(t *testing.T) {
	from, to := memIMAP(t), memIMAP(t)
	appendMessage(t, from, "INBOX", "one@example.com")
	appendMessage(t, from, "INBOX", "two@example.com")
	appendMessage(t, from, "INBOX", "three@example.com")

	var p ImportProgress
	closeTarget := func(p ImportProgress) {
		if p.Copied == 1 {
			to.Close()
		}
	}
	if err := copyFolder(context.Background(), from, nil, to, "INBOX", "INBOX", 1<<20, &p, closeTarget, nil); err == nil {
		t.Fatal("copy into a closed destination succeeded")
	}
	closed := make(chan error, 1)
	go func() { closed <- from.Close() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the source connection is still waiting on an unread fetch")
	}
}

func fakeSource(t *testing.T, respond func(w io.Writer, tag, cmd string)) (*imapclient.Client, *importBudget) {
	t.Helper()
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		io.WriteString(server, "* OK [CAPABILITY IMAP4rev1] ready\r\n")
		r := bufio.NewReader(server)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			tag, cmd, _ := strings.Cut(strings.TrimSpace(line), " ")
			respond(server, tag, strings.ToUpper(cmd))
		}
	}()
	budget := &importBudget{Conn: client}
	ic := imapclient.New(budget, nil)
	t.Cleanup(func() { ic.Close() })
	return ic, budget
}

func sendFiller(w io.Writer, n int, sent *atomic.Int64) {
	chunk := bytes.Repeat([]byte("a"), 64<<10)
	for ; n > 0; n -= len(chunk) {
		k, err := w.Write(chunk[:min(n, len(chunk))])
		sent.Add(int64(k))
		if err != nil {
			return
		}
	}
}

func fakeMailbox(w io.Writer, tag, cmd string, heads, body func(io.Writer)) {
	switch {
	case strings.HasPrefix(cmd, "EXAMINE"):
		fmt.Fprintf(w, "* 1 EXISTS\r\n* OK [UIDVALIDITY 7] ok\r\n%s OK [READ-ONLY] done\r\n", tag)
	case strings.HasPrefix(cmd, "UID SEARCH"):
		fmt.Fprintf(w, "* SEARCH 1\r\n%s OK done\r\n", tag)
	case strings.HasPrefix(cmd, "UID FETCH") && strings.Contains(cmd, "BODY"):
		body(w)
		fmt.Fprintf(w, "%s OK done\r\n", tag)
	case strings.HasPrefix(cmd, "UID FETCH"):
		heads(w)
		fmt.Fprintf(w, "%s OK done\r\n", tag)
	default:
		fmt.Fprintf(w, "%s OK done\r\n", tag)
	}
}

func TestImportFailsOnOversizedSourceResponses(t *testing.T) {
	cases := map[string]func(w io.Writer, tag, cmd string, sent *atomic.Int64){
		"folder name": func(w io.Writer, tag, cmd string, sent *atomic.Int64) {
			if !strings.HasPrefix(cmd, "LIST") {
				fmt.Fprintf(w, "%s OK done\r\n", tag)
				return
			}
			io.WriteString(w, `* LIST () "/" "`)
			sendFiller(w, 64<<20, sent)
			fmt.Fprintf(w, "\"\r\n%s OK done\r\n", tag)
		},
		"envelope subject": func(w io.Writer, tag, cmd string, sent *atomic.Int64) {
			if strings.HasPrefix(cmd, "LIST") {
				fmt.Fprintf(w, "* LIST () \"/\" INBOX\r\n%s OK done\r\n", tag)
				return
			}
			fakeMailbox(w, tag, cmd, func(w io.Writer) {
				fmt.Fprintf(w, "* 1 FETCH (UID 1 RFC822.SIZE 10 ENVELOPE (NIL {%d}\r\n", 64<<20)
				sendFiller(w, 64<<20, sent)
				io.WriteString(w, " NIL NIL NIL NIL NIL NIL NIL NIL))\r\n")
			}, func(io.Writer) {})
		},
		"repeated body sections": func(w io.Writer, tag, cmd string, sent *atomic.Int64) {
			if strings.HasPrefix(cmd, "LIST") {
				fmt.Fprintf(w, "* LIST () \"/\" INBOX\r\n%s OK done\r\n", tag)
				return
			}
			fakeMailbox(w, tag, cmd, func(w io.Writer) {
				io.WriteString(w, "* 1 FETCH (UID 1 RFC822.SIZE 10 ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL))\r\n")
			}, func(w io.Writer) {
				io.WriteString(w, "* 1 FETCH (UID 1")
				for range 6 {
					fmt.Fprintf(w, " BODY[] {%d}\r\n", 8<<20)
					sendFiller(w, 8<<20, sent)
				}
				io.WriteString(w, ")\r\n")
			})
		},
	}
	cases["repeated body sections over the message limit"] = cases["repeated body sections"]
	for name, respond := range cases {
		t.Run(name, func(t *testing.T) {
			var sent atomic.Int64
			from, budget := fakeSource(t, func(w io.Writer, tag, cmd string) { respond(w, tag, cmd, &sent) })
			to := memIMAP(t)
			maxBytes := int64(16 << 20)
			if strings.HasSuffix(name, "over the message limit") {
				maxBytes = 1 << 20
			}
			err := importFolders(context.Background(), from, budget, to, "source", maxBytes, func(ImportProgress) {}, nil)
			if !errors.Is(budget.failed(err), ErrImportResponseTooLarge) {
				t.Fatalf("import returned %.200v, want %v", err, ErrImportResponseTooLarge)
			}
			if n := sent.Load(); n > importResponseLimit+9<<20 {
				t.Errorf("the source delivered %d bytes before the import stopped", n)
			}
		})
	}
}

func TestImportStreamsBodiesLargerThanTheResponseLimit(t *testing.T) {
	head := "Message-ID: <big@example.com>\r\nSubject: big\r\n\r\n"
	size := importResponseLimit + 4<<20
	envelope := `(NIL NIL NIL NIL NIL NIL NIL NIL NIL "<big@example.com>")`
	var sent atomic.Int64
	from, budget := fakeSource(t, func(w io.Writer, tag, cmd string) {
		if strings.HasPrefix(cmd, "LIST") {
			fmt.Fprintf(w, "* LIST () \"/\" INBOX\r\n%s OK done\r\n", tag)
			return
		}
		fakeMailbox(w, tag, cmd, func(w io.Writer) {
			fmt.Fprintf(w, "* 1 FETCH (UID 1 RFC822.SIZE %d ENVELOPE %s)\r\n", size, envelope)
		}, func(w io.Writer) {
			fmt.Fprintf(w, "* 1 FETCH (UID 1 FLAGS () INTERNALDATE \"01-Jan-2024 00:00:00 +0000\" ENVELOPE %s BODY[] {%d}\r\n%s", envelope, size, head)
			sendFiller(w, size-len(head), &sent)
			io.WriteString(w, ")\r\n")
		})
	})
	to := memIMAP(t)
	if err := importFolders(context.Background(), from, budget, to, "source", 2*importResponseLimit, func(ImportProgress) {}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := folderMessages(t, to, "INBOX")["big@example.com"]; !ok {
		t.Fatal("the large message was not copied")
	}
}

func TestCheckImportSourceStopsOnAnOversizedGreeting(t *testing.T) {
	var sent atomic.Int64
	dial := func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			io.WriteString(server, "* OK ")
			sendFiller(server, 64<<20, &sent)
			io.WriteString(server, "\r\n")
		}()
		return client, nil
	}
	err := CheckImportSource(context.Background(), ImportSource{Host: "source.example", Port: 143, StartTLS: true, Dial: dial})
	if !errors.Is(err, ErrImportResponseTooLarge) {
		t.Fatalf("check returned %.200v, want %v", err, ErrImportResponseTooLarge)
	}
	if n := sent.Load(); n > importResponseLimit+1<<20 {
		t.Errorf("the source delivered %d bytes before the check stopped", n)
	}
}
