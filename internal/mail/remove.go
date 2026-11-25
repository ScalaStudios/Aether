package mail

import (
	"context"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func (c *Client) DeleteStoredMail(ctx context.Context, address string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.with(address, func(ic *imapclient.Client) error {
		list, err := ic.List("", "*", nil).Collect()
		if err != nil {
			return err
		}
		var names []string
		for _, l := range list {
			if SharedFolder(l.Mailbox) || slices.Contains(l.Attrs, imap.MailboxAttrNoSelect) || slices.Contains(l.Attrs, imap.MailboxAttrNonExistent) {
				continue
			}
			names = append(names, l.Mailbox)
		}
		slices.SortFunc(names, func(a, b string) int {
			if d := strings.Count(b, "/") - strings.Count(a, "/"); d != 0 {
				return d
			}
			return strings.Compare(a, b)
		})
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			if strings.EqualFold(name, "INBOX") {
				selected, err := ic.Select("INBOX", nil).Wait()
				if err != nil || selected.NumMessages == 0 {
					continue
				}
				if err := ic.Store(allUIDs, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
					return err
				}
				if err := ic.UIDExpunge(allUIDs).Close(); err != nil {
					return err
				}
				continue
			}
			if err := ic.Delete(name).Wait(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "nonexistent") {
				return err
			}
		}
		return nil
	})
}
