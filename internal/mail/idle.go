package mail

import (
	"context"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type FolderChange struct {
	UIDValidity uint32
	Kind        string
	Messages    []Summary
}

func (c *Client) Watch(ctx context.Context, address, folder string, emit func(FolderChange)) error {
	if SharedFolder(folder) {
		return ErrNotFound
	}
	notes := newMailboxNotes()
	ic, err := imapclient.DialStartTLS(c.imapAddr, &imapclient.Options{
		TLSConfig: c.tls,
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
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
		},
	})
	if err != nil {
		return err
	}
	defer ic.Close()
	if err := ic.Login(c.masterLogin(address), c.masterPassword).Wait(); err != nil {
		return err
	}
	defer ic.Logout().Wait()
	return watchLoop(ctx, ic, folder, notes, emit)
}

type mailboxNotes struct {
	mu   sync.Mutex
	kind string
	ch   chan struct{}
}

func newMailboxNotes() *mailboxNotes {
	return &mailboxNotes{ch: make(chan struct{}, 1)}
}

func (n *mailboxNotes) add(kind string) {
	n.mu.Lock()
	if noteRank(kind) >= noteRank(n.kind) {
		n.kind = kind
	}
	n.mu.Unlock()
	select {
	case n.ch <- struct{}{}:
	default:
	}
}

func (n *mailboxNotes) take() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	kind := n.kind
	n.kind = ""
	return kind
}

func noteRank(kind string) int {
	switch kind {
	case "exists":
		return 3
	case "expunge":
		return 2
	case "fetch":
		return 1
	default:
		return 0
	}
}

func watchLoop(ctx context.Context, ic *imapclient.Client, folder string, notes *mailboxNotes, emit func(FolderChange)) error {
	selected, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return ErrNotFound
	}
	next := selected.UIDNext
	if next == 0 {
		next = 1
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		idle, err := ic.Idle()
		if err != nil {
			return err
		}
		var kind string
		select {
		case <-ctx.Done():
			_ = idle.Close()
			_ = idle.Wait()
			return nil
		case <-notes.ch:
			kind = notes.take()
			if err := idle.Close(); err != nil {
				_ = idle.Wait()
				return err
			}
			if err := idle.Wait(); err != nil {
				return err
			}
		}
		change, err := readChange(ic, &next, selected.UIDValidity, kind)
		if err != nil {
			return err
		}
		if change.Kind != "" {
			emit(change)
		}
	}
}

func readChange(ic *imapclient.Client, next *imap.UID, validity uint32, kind string) (FolderChange, error) {
	msgs, err := readNew(ic, next)
	if err != nil {
		return FolderChange{}, err
	}
	out := FolderChange{UIDValidity: validity, Messages: msgs}
	switch {
	case len(unseenSummaries(msgs)) > 0:
		out.Kind = "new"
	case kind != "":
		out.Kind = "changed"
	}
	return out, nil
}

func readNew(ic *imapclient.Client, next *imap.UID) ([]Summary, error) {
	if *next == 0 {
		*next = 1
	}
	var set imap.UIDSet
	set.AddRange(*next, 0)
	data, err := ic.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{set}}, nil).Wait()
	if err != nil {
		return nil, err
	}
	var fresh []imap.UID
	var highest imap.UID
	for _, uid := range data.AllUIDs() {
		if uid < *next {
			continue
		}
		fresh = append(fresh, uid)
		if uid > highest {
			highest = uid
		}
	}
	if len(fresh) == 0 {
		return nil, nil
	}
	*next = highest + 1
	if len(fresh) > 20 {
		fresh = fresh[:20]
	}
	var fetch imap.UIDSet
	for _, uid := range fresh {
		fetch.AddNum(uid)
	}
	found, err := ic.Fetch(fetch, &imap.FetchOptions{UID: true, Flags: true, Envelope: true}).Collect()
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(found))
	for _, m := range found {
		out = append(out, summarize(m))
	}
	return out, nil
}

func unseenSummaries(msgs []Summary) []Summary {
	var out []Summary
	for _, m := range msgs {
		if !m.Seen {
			out = append(out, m)
		}
	}
	return out
}
