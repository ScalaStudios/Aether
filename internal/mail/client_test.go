package mail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func originals(t *testing.T, ic *imapclient.Client, folder string) map[string]*imapclient.FetchMessageBuffer {
	t.Helper()
	if _, err := ic.Select(folder, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	body := &imap.FetchItemBodySection{Peek: true}
	msgs, err := ic.Fetch(imap.UIDSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{UID: true, Flags: true, InternalDate: true, Envelope: true, BodySection: []*imap.FetchItemBodySection{body}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*imapclient.FetchMessageBuffer{}
	for _, m := range msgs {
		out[m.Envelope.MessageID] = m
	}
	return out
}

func TestTransferMovesOriginalsBetweenMailboxes(t *testing.T) {
	from, to := memIMAP(t), memIMAP(t)
	appendMessage(t, from, "INBOX", "one@example.com", imap.FlagSeen, imap.FlagFlagged, "$Forwarded")
	appendMessage(t, from, "INBOX", "two@example.com")
	appendMessage(t, from, "INBOX", "stays@example.com")
	before := originals(t, from, "INBOX")

	n, err := transfer(from, to, "INBOX", []uint32{1, 2, 99}, "INBOX", false)
	if err != nil || n != 2 {
		t.Fatalf("transfer = %d, %v; want 2, nil", n, err)
	}
	after := originals(t, to, "INBOX")
	for _, id := range []string{"one@example.com", "two@example.com"} {
		got, want := after[id], before[id]
		if got == nil {
			t.Fatalf("%s did not arrive", id)
		}
		slices.Sort(got.Flags)
		slices.Sort(want.Flags)
		if !reflect.DeepEqual(got.BodySection, want.BodySection) || !got.InternalDate.Equal(want.InternalDate) || !slices.Equal(got.Flags, want.Flags) {
			t.Errorf("%s changed: flags %v → %v, date %v → %v", id, want.Flags, got.Flags, want.InternalDate, got.InternalDate)
		}
	}
	if left := originals(t, from, "INBOX"); len(left) != 1 || left["stays@example.com"] == nil {
		t.Errorf("source keeps %d messages, want only stays@example.com", len(left))
	}
}

func TestTransferCopyKeepsSourceAndChecksTarget(t *testing.T) {
	from, to := memIMAP(t), memIMAP(t)
	appendMessage(t, from, "INBOX", "one@example.com")
	if _, err := transfer(from, to, "INBOX", []uint32{1}, "Missing", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transfer to a missing folder = %v, want ErrNotFound", err)
	}
	if n, err := transfer(from, to, "INBOX", []uint32{1}, "INBOX", true); err != nil || n != 1 {
		t.Fatalf("copy = %d, %v; want 1, nil", n, err)
	}
	if len(originals(t, from, "INBOX")) != 1 || len(originals(t, to, "INBOX")) != 1 {
		t.Error("copy should leave the message in both mailboxes")
	}
}

func TestListCriteriaCombineFilters(t *testing.T) {
	since, until := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	got, _ := ListOptions{Query: ` invoice  "due 2026" from:"Amy B" to:ops`, Unread: true, Flagged: true, From: "billing", To: "abuse", Since: since, Until: until}.criteria()
	want := &imap.SearchCriteria{
		Text: []string{"invoice", "due 2026"}, NotFlag: []imap.Flag{imap.FlagSeen}, Flag: []imap.Flag{imap.FlagFlagged},
		Header: []imap.SearchCriteriaHeaderField{{Key: "From", Value: "Amy B"}, {Key: "From", Value: "billing"}, {Key: "To", Value: "ops"}, {Key: "To", Value: "abuse"}},
		Since:  since, Before: until.AddDate(0, 0, 1),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("criteria = %+v, want %+v", got, want)
	}
}

func TestThreadsFollowArrivalNotUIDs(t *testing.T) {
	threads := []imapclient.ThreadData{{Chain: []uint32{2, 3}}, {Chain: []uint32{1}}, {Chain: []uint32{4}}}
	arrival := []uint32{2, 3, 1, 4}
	groups := threadGroups(threads, arrival, arrival)
	if want := [][]uint32{{3, 2}, {1}, {4}}; !reflect.DeepEqual(groups, want) {
		t.Errorf("groups = %v, want %v", groups, want)
	}
	if groups := threadGroups(threads, arrival, []uint32{3, 1}); !reflect.DeepEqual(groups, [][]uint32{{3}, {1}}) {
		t.Errorf("filtered groups = %v, want [[3] [1]]", groups)
	}
}

func TestCursorFollowsAnchor(t *testing.T) {
	groups := [][]uint32{{4}, {1}, {3, 2}}
	cursor := *pageCursor(2, 1)
	if start, err := pageStart(groups, cursor); err != nil || start != 2 {
		t.Errorf("start = %d, %v; want 2", start, err)
	}
	if start, _ := pageStart([][]uint32{{5}, {4}, {1}, {3, 2}}, cursor); start != 3 {
		t.Errorf("start after new mail = %d, want 3", start)
	}
	if start, _ := pageStart([][]uint32{{4}, {3, 2}}, cursor); start != 1 {
		t.Errorf("start after the anchor was deleted = %d, want 1", start)
	}
	if _, err := pageStart(groups, "not a cursor"); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("garbage cursor = %v, want ErrInvalidCursor", err)
	}
}

func TestMailboxStorageSumsOwnFoldersOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/doveadm/v1" || r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req [][]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resp := [][]any{
			{"doveadmResponse", []map[string]any{
				{"mailbox": "INBOX", "messages": "3", "vsize": "1500"},
				{"mailbox": "Sent", "messages": 2, "vsize": 800},
				{"mailbox": "Shared/team@example.com/INBOX", "messages": "99", "vsize": "999999"},
			}, "alice@example.com"},
			{"error", map[string]any{"exitCode": 67}, "empty@example.com"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{doveadmURL: srv.URL, doveadmKey: "secret", http: srv.Client()}
	got, err := c.MailboxStorage(context.Background(), []string{"alice@example.com", "empty@example.com"})
	if err != nil {
		t.Fatalf("MailboxStorage: %v", err)
	}
	if alice := got["alice@example.com"]; alice.StorageBytes != 2300 || alice.MessageCount != 5 {
		t.Errorf("alice = %+v, want 2300 bytes / 5 messages", alice)
	}
	if empty, ok := got["empty@example.com"]; !ok || empty.StorageBytes != 0 || empty.MessageCount != 0 {
		t.Errorf("empty = %+v (ok=%v), want 0/0", empty, ok)
	}
}
