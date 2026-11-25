package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

var ErrMailboxChanged = errors.New("the folder changed while its held messages were being checked")

type HoldScope struct {
	Scopes []SearchQuery
	Whole  bool
}

func NewHoldScope(scopes []SearchQuery) HoldScope {
	var h HoldScope
	for _, q := range scopes {
		if q.Attachment == "" && len(q.doveadmTerms()) == 0 {
			return HoldScope{Whole: true}
		}
		h.Scopes = append(h.Scopes, q)
	}
	return h
}

type candidate struct {
	Mailbox string `json:"mailbox"`
	GUID    string `json:"mailbox-guid"`
	UID     string `json:"uid"`
}

func (c *Client) ExpungeHeld(ctx context.Context, user string, query []string, hold HoldScope) error {
	if hold.Whole {
		return nil
	}
	if len(hold.Scopes) == 0 {
		return c.Expunge(ctx, user, query)
	}
	payload, err := c.doveadmOK(ctx, "fetch", map[string]any{"user": user, "field": []string{"mailbox", "mailbox-guid", "uid"}, "query": query}, 64<<20)
	if err != nil {
		return err
	}
	var rows []candidate
	if err := json.Unmarshal(payload, &rows); err != nil {
		return fmt.Errorf("doveadm fetch: %w", err)
	}
	type folder struct{ name, guid string }
	byFolder := map[folder][]imap.UID{}
	for _, r := range rows {
		uid, err := strconv.ParseUint(r.UID, 10, 32)
		if err != nil || r.Mailbox == "" || r.GUID == "" {
			return fmt.Errorf("doveadm fetch: unexpected row %+v", r)
		}
		f := folder{r.Mailbox, r.GUID}
		byFolder[f] = append(byFolder[f], imap.UID(uid))
	}
	var errs []error
	for f, uids := range byFolder {
		validity, err := c.folderValidity(ctx, user, f.name, f.guid)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		held, err := c.heldUIDs(ctx, user, f.name, validity, uids, hold.Scopes)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rest := slices.DeleteFunc(uids, func(uid imap.UID) bool { return held[uid] })
		if len(rest) == 0 {
			continue
		}
		errs = append(errs, c.Expunge(ctx, user, append([]string{"mailbox-guid", f.guid, "uid", imap.UIDSetNum(rest...).String()}, query...)))
	}
	return errors.Join(errs...)
}

func (c *Client) folderValidity(ctx context.Context, user, name, guid string) (uint32, error) {
	payload, err := c.doveadmOK(ctx, "mailboxStatus", map[string]any{"user": user, "field": []string{"guid", "uidvalidity"}, "mailboxMask": []string{name}}, 64<<10)
	if err != nil {
		return 0, err
	}
	var rows []struct {
		GUID        string `json:"guid"`
		UIDValidity string `json:"uidvalidity"`
	}
	if err := json.Unmarshal(payload, &rows); err != nil {
		return 0, fmt.Errorf("doveadm mailbox status: %w", err)
	}
	if len(rows) != 1 || rows[0].GUID != guid {
		return 0, fmt.Errorf("%w: %s", ErrMailboxChanged, name)
	}
	validity, err := strconv.ParseUint(rows[0].UIDValidity, 10, 32)
	if err != nil || validity == 0 {
		return 0, fmt.Errorf("doveadm mailbox status: unexpected UIDVALIDITY %q", rows[0].UIDValidity)
	}
	return uint32(validity), nil
}

func (c *Client) heldUIDs(ctx context.Context, user, folder string, validity uint32, uids []imap.UID, scopes []SearchQuery) (map[imap.UID]bool, error) {
	held := map[imap.UID]bool{}
	err := c.withBounded(ctx, user, func(ic *imapclient.Client, conn *boundedConn) error {
		for _, q := range scopes {
			q.uids, q.uidValidity = imap.UIDSetNum(uids...), validity
			if err := searchFolder(ctx, ic, conn.extend, folder, q, false, func(h Hit, _ []byte) error {
				held[imap.UID(h.UID)] = true
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return held, err
}
