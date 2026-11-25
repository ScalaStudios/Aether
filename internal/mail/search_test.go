package mail

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

func TestSearchFolderMatchesRecipientsAndAttachments(t *testing.T) {
	ic := memIMAP(t)
	for _, raw := range []string{
		"From: alice@example.com\r\nTo: bob@example.com\r\nSubject: plain\r\n\r\nhello\r\n",
		"From: alice@example.com\r\nTo: bob@example.com\r\nCc: carol@example.com\r\nSubject: contract\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"Contract-Final.pdf\"\r\n\r\nJVBERi0=\r\n--b--\r\n",
	} {
		cmd := ic.Append("INBOX", int64(len(raw)), &imap.AppendOptions{Time: time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)})
		cmd.Write([]byte(raw))
		cmd.Close()
		if _, err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	search := func(q SearchQuery, withBody bool) ([]Hit, [][]byte) {
		var hits []Hit
		var bodies [][]byte
		if err := searchFolder(context.Background(), ic, func() {}, "INBOX", q, withBody, func(h Hit, raw []byte) error {
			hits = append(hits, h)
			bodies = append(bodies, raw)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return hits, bodies
	}
	if hits, _ := search(SearchQuery{To: "carol@"}, false); len(hits) != 1 || hits[0].Subject != "contract" {
		t.Fatalf("cc search: %+v", hits)
	}
	if hits, _ := search(SearchQuery{Attachment: "contract-final"}, false); len(hits) != 1 || len(hits[0].Attachments) != 1 || hits[0].Attachments[0] != "Contract-Final.pdf" {
		t.Fatalf("attachment search: %+v", hits)
	}
	if hits, _ := search(SearchQuery{From: "alice", Since: time.Date(2024, 5, 2, 0, 0, 0, 0, time.UTC)}, false); len(hits) != 0 {
		t.Fatalf("date search: %+v", hits)
	}
	hits, bodies := search(SearchQuery{Subject: "plain"}, true)
	if len(hits) != 1 || !bytes.Contains(bodies[0], []byte("hello")) || hits[0].UIDValidity == 0 {
		t.Fatalf("body fetch: %+v %q", hits, bodies)
	}
	stopped := 0
	err := searchFolder(context.Background(), ic, func() {}, "INBOX", SearchQuery{}, false, func(Hit, []byte) error {
		stopped++
		return ErrStopSearch
	})
	if err != ErrStopSearch || stopped != 1 {
		t.Fatalf("stop: %v after %d", err, stopped)
	}
}

func TestWriteMboxEscapesFromLines(t *testing.T) {
	var buf bytes.Buffer
	h := Hit{From: []Address{{Address: "alice@example.com"}}, Date: time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)}
	if err := WriteMbox(&buf, h, []byte("Subject: x\r\n\r\nFrom here\r\n>From there\r\nok\r\n")); err != nil {
		t.Fatal(err)
	}
	want := "From alice@example.com Wed May  1 09:00:00 2024\nSubject: x\n\n>From here\n>>From there\nok\n\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
	buf.Reset()
	WriteMbox(&buf, Hit{From: []Address{{Address: "bad address@x"}}}, []byte("x"))
	if !bytes.HasPrefix(buf.Bytes(), []byte("From MAILER-DAEMON ")) {
		t.Fatalf("unsafe sender kept: %q", buf.String())
	}
}

func TestNewHoldScope(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := NewHoldScope([]SearchQuery{{Subject: "Atlas", Since: since, Before: since.AddDate(0, 1, 0)}, {To: "carol@example.com"}, {Subject: "Atlas", Attachment: "invoice"}, {Attachment: "contract"}})
	want := []string{"NOT", "(", "subject", "Atlas", "since", "2026-01-01", "before", "2026-02-01", ")", "NOT", "(", "(", "to", "carol@example.com", "OR", "cc", "carol@example.com", "OR", "bcc", "carol@example.com", ")", ")"}
	if h.Whole || !slices.Equal(h.Exclude, want) || len(h.Attachment) != 2 {
		t.Fatalf("got %+v", h)
	}
	if h := NewHoldScope([]SearchQuery{{Attachment: "contract"}, {}}); !h.Whole {
		t.Fatal("a hold without terms must keep the whole mailbox")
	}
	if h := NewHoldScope(nil); h.Whole || h.Exclude != nil || h.Attachment != nil {
		t.Fatal("no holds must exclude nothing")
	}
}

func TestSearchFolderChecksIdentityAndCandidates(t *testing.T) {
	ic := memIMAP(t)
	for _, name := range []string{"keep.pdf", "keep.pdf", "drop.jpg"} {
		raw := "Subject: x\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nx\r\n--b\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"" + name + "\"\r\n\r\neA==\r\n--b--\r\n"
		cmd := ic.Append("INBOX", int64(len(raw)), nil)
		cmd.Write([]byte(raw))
		cmd.Close()
		if _, err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	sel, err := ic.Select("INBOX", nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	var held []uint32
	q := SearchQuery{Attachment: "KEEP", uids: imap.UIDSetNum(2, 3), uidValidity: sel.UIDValidity}
	if err := searchFolder(context.Background(), ic, func() {}, "INBOX", q, false, func(h Hit, _ []byte) error {
		held = append(held, h.UID)
		return nil
	}); err != nil || !slices.Equal(held, []uint32{2}) {
		t.Fatalf("held %v, err %v", held, err)
	}
	q.uidValidity++
	if err := searchFolder(context.Background(), ic, func() {}, "INBOX", q, false, func(Hit, []byte) error { return nil }); !errors.Is(err, ErrMailboxChanged) {
		t.Fatalf("a changed UIDVALIDITY was not refused: %v", err)
	}
}
