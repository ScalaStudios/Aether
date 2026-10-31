package mail

import (
	"errors"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

var (
	ErrFolderExists      = errors.New("folder already exists")
	ErrFolderNotEmpty    = errors.New("folder is not empty")
	ErrFolderHasChildren = errors.New("folder has subfolders")
	ErrProtectedFolder   = errors.New("folder cannot be renamed or deleted")
)

var allUIDs = imap.UIDSet{{Start: 1, Stop: 0}}

func parentExists(ic *imapclient.Client, name string) error {
	i := strings.LastIndex(name, "/")
	if i <= 0 {
		return nil
	}
	if _, err := ic.Status(name[:i], &imap.StatusOptions{NumMessages: true}).Wait(); err != nil {
		return ErrNotFound
	}
	return nil
}

func editableFolder(ic *imapclient.Client, name string) ([]*imap.ListData, error) {
	list, err := ic.List("", name+"*", &imap.ListOptions{ReturnSpecialUse: true, ReturnSubscribed: true}).Collect()
	if err != nil {
		return nil, err
	}
	list = slices.DeleteFunc(list, func(l *imap.ListData) bool { return l.Mailbox != name && !strings.HasPrefix(l.Mailbox, name+"/") })
	slices.SortFunc(list, func(a, b *imap.ListData) int { return strings.Compare(a.Mailbox, b.Mailbox) })
	switch {
	case len(list) == 0 || list[0].Mailbox != name:
		return nil, ErrNotFound
	case specialUse(list[0]) != "" || name == SnoozedFolder:
		return nil, ErrProtectedFolder
	}
	return list, nil
}

func alreadyExists(err error) error {
	var ie *imap.Error
	if errors.As(err, &ie) && ie.Code == imap.ResponseCodeAlreadyExists {
		return ErrFolderExists
	}
	return err
}

func createFolder(ic *imapclient.Client, name string) error {
	if err := parentExists(ic, name); err != nil {
		return err
	}
	if err := ic.Create(name, nil).Wait(); err != nil {
		return alreadyExists(err)
	}
	return ic.Subscribe(name).Wait()
}

func renameFolder(ic *imapclient.Client, from, to string) error {
	tree, err := editableFolder(ic, from)
	if err != nil {
		return err
	}
	if err := parentExists(ic, to); err != nil {
		return err
	}
	if err := ic.Rename(from, to, nil).Wait(); err != nil {
		return alreadyExists(err)
	}
	for _, l := range tree {
		if slices.Contains(l.Attrs, imap.MailboxAttrSubscribed) {
			ic.Subscribe(to + strings.TrimPrefix(l.Mailbox, from)).Wait()
			ic.Unsubscribe(l.Mailbox).Wait()
		}
	}
	return nil
}

func deleteFolder(ic *imapclient.Client, name, moveTo string) error {
	tree, err := editableFolder(ic, name)
	if err != nil {
		return err
	}
	if len(tree) > 1 {
		return ErrFolderHasChildren
	}
	data, err := ic.Select(name, nil).Wait()
	if err != nil {
		return ErrNotFound
	}
	if data.NumMessages > 0 {
		if moveTo == "" {
			return ErrFolderNotEmpty
		}
		if _, err := ic.Status(moveTo, &imap.StatusOptions{NumMessages: true}).Wait(); err != nil {
			return ErrNotFound
		}
		if _, err := ic.Move(allUIDs, moveTo).Wait(); err != nil {
			return err
		}
	}
	if err := ic.Unselect().Wait(); err != nil {
		return err
	}
	if err := ic.Delete(name).Wait(); err != nil {
		return err
	}
	ic.Unsubscribe(name).Wait()
	return nil
}

func (c *Client) CreateFolder(address, name string) error {
	return c.with(address, func(ic *imapclient.Client) error { return createFolder(ic, name) })
}

func (c *Client) RenameFolder(address, from, to string) error {
	if SharedFolder(from, to) {
		return ErrNotFound
	}
	return c.with(address, func(ic *imapclient.Client) error { return renameFolder(ic, from, to) })
}

func (c *Client) DeleteFolder(address, name, moveTo string) error {
	if SharedFolder(name, moveTo) {
		return ErrNotFound
	}
	return c.with(address, func(ic *imapclient.Client) error { return deleteFolder(ic, name, moveTo) })
}

func (c *Client) EmptyFolder(address, name string) error {
	if SharedFolder(name) {
		return ErrNotFound
	}
	return c.with(address, func(ic *imapclient.Client) error {
		list, err := ic.List("", name, &imap.ListOptions{ReturnSpecialUse: true}).Collect()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(list, func(l *imap.ListData) bool { return l.Mailbox == name })
		if i < 0 {
			return ErrNotFound
		}
		data, err := ic.Select(name, nil).Wait()
		if err != nil {
			return ErrNotFound
		}
		if data.NumMessages == 0 {
			return nil
		}
		if use := specialUse(list[i]); use == "trash" || use == "junk" {
			if err := ic.Store(allUIDs, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
				return err
			}
			return ic.UIDExpunge(allUIDs).Close()
		}
		trash, err := c.specialFolder(ic, "trash")
		if err != nil {
			return err
		}
		_, err = ic.Move(allUIDs, trash).Wait()
		return err
	})
}

func (c *Client) MarkFolderRead(address, name string) error {
	if SharedFolder(name) {
		return ErrNotFound
	}
	return c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Select(name, nil).Wait(); err != nil {
			return ErrNotFound
		}
		return ic.Store(allUIDs, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}, Silent: true}, nil).Close()
	})
}

func (c *Client) SetSubscribed(address, name string, subscribed bool) error {
	if SharedFolder(name) {
		return ErrNotFound
	}
	return c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Status(name, &imap.StatusOptions{NumMessages: true}).Wait(); err != nil {
			return ErrNotFound
		}
		if subscribed {
			return ic.Subscribe(name).Wait()
		}
		return ic.Unsubscribe(name).Wait()
	})
}
