package mail

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const ExpungedPrefix = ".EXPUNGED/"

const maxRecoverable = 2000

var ErrOverQuota = errors.New("mailbox is over quota")

type Recoverable struct {
	Summary
	Folder    string    `json:"folder"`
	DeletedAt time.Time `json:"deletedAt"`
}

type RecoverableRef struct {
	Folder string `json:"folder"`
	UID    uint32 `json:"uid"`
}

type expungedRow struct {
	Mailbox string `json:"mailbox"`
	UID     string `json:"uid"`
	Saved   string `json:"date.saved.unixtime"`
}

func (c *Client) doveadmOK(ctx context.Context, cmd string, params any, limit int64) (json.RawMessage, error) {
	kind, payload, status, err := c.doveadm(ctx, c.bulk, cmd, params, limit)
	if err != nil {
		return nil, err
	}
	if kind != "doveadmResponse" || status != http.StatusOK {
		return nil, fmt.Errorf("doveadm %s: HTTP status %d: %s", cmd, status, payload)
	}
	return payload, nil
}

func newestExpunged(rows []expungedRow, limit int) []Recoverable {
	out := make([]Recoverable, 0, len(rows))
	for _, r := range rows {
		uid, err1 := strconv.ParseUint(r.UID, 10, 32)
		saved, err2 := strconv.ParseInt(r.Saved, 10, 64)
		folder, ok := strings.CutPrefix(r.Mailbox, ExpungedPrefix)
		if err1 != nil || err2 != nil || !ok || folder == "" {
			continue
		}
		out = append(out, Recoverable{Summary: Summary{UID: uint32(uid)}, Folder: folder, DeletedAt: time.Unix(saved, 0).UTC()})
	}
	slices.SortFunc(out, func(a, b Recoverable) int {
		return cmp.Or(b.DeletedAt.Compare(a.DeletedAt), strings.Compare(a.Folder, b.Folder), cmp.Compare(b.UID, a.UID))
	})
	return out[:min(len(out), limit)]
}

func (c *Client) Recoverable(ctx context.Context, address string) ([]Recoverable, error) {
	payload, err := c.doveadmOK(ctx, "fetch", map[string]any{
		"user":  address,
		"field": []string{"mailbox", "uid", "date.saved.unixtime"},
		"query": []string{"mailbox", ExpungedPrefix + "*"},
	}, 64<<20)
	if err != nil {
		return nil, err
	}
	var rows []expungedRow
	if err := json.Unmarshal(payload, &rows); err != nil {
		return nil, fmt.Errorf("doveadm fetch: %w", err)
	}
	items := newestExpunged(rows, maxRecoverable)
	if len(items) == 0 {
		return items, nil
	}
	byFolder := map[string][]uint32{}
	for _, it := range items {
		byFolder[it.Folder] = append(byFolder[it.Folder], it.UID)
	}
	summaries := map[RecoverableRef]Summary{}
	err = c.with(address, func(ic *imapclient.Client) error {
		for folder, uids := range byFolder {
			if _, err := ic.Select(ExpungedPrefix+folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
				continue
			}
			msgs, err := ic.Fetch(uidSet(uids), &imap.FetchOptions{
				UID: true, Envelope: true, InternalDate: true, RFC822Size: true,
				BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
			}).Collect()
			if err != nil {
				return err
			}
			for _, m := range msgs {
				summaries[RecoverableRef{folder, uint32(m.UID)}] = summarize(m)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := items[:0]
	for _, it := range items {
		if s, ok := summaries[RecoverableRef{it.Folder, it.UID}]; ok {
			it.Summary = s
			out = append(out, it)
		}
	}
	return out, nil
}

func (c *Client) Restore(address string, refs []RecoverableRef) (int, error) {
	byFolder := map[string][]uint32{}
	for _, r := range refs {
		byFolder[r.Folder] = append(byFolder[r.Folder], r.UID)
	}
	n := 0
	err := c.with(address, func(ic *imapclient.Client) error {
		for folder, uids := range byFolder {
			to := folder
			if _, err := ic.Status(to, &imap.StatusOptions{NumMessages: true}).Wait(); err != nil {
				to = "INBOX"
			}
			if _, err := ic.Select(ExpungedPrefix+folder, nil).Wait(); err != nil {
				continue
			}
			data, err := ic.Move(uidSet(uids), to).Wait()
			var ie *imap.Error
			if errors.As(err, &ie) && ie.Code == imap.ResponseCodeOverQuota {
				return ErrOverQuota
			}
			if err != nil {
				return err
			}
			if data != nil {
				if moved, ok := data.SourceUIDs.(imap.UIDSet); ok {
					nums, _ := moved.Nums()
					n += len(nums)
				}
			}
		}
		return nil
	})
	if err == nil && n == 0 {
		return 0, ErrNotFound
	}
	return n, err
}

func (c *Client) PurgeRecoverable(ctx context.Context, address string, days int) error {
	_, err := c.doveadmOK(ctx, "expunge", map[string]any{
		"user":  address,
		"query": []string{"mailbox", ExpungedPrefix + "*", "savedbefore", strconv.Itoa(days) + "d"},
	}, 64<<10)
	return err
}
