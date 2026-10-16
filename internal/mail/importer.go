package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type ImportSource struct {
	Host     string
	Port     int
	StartTLS bool
	Username string
	Password string
	Dial     func(ctx context.Context, network, address string) (net.Conn, error)
}

type ImportProgress struct {
	Folder       string `json:"folder"`
	FoldersDone  int    `json:"foldersDone"`
	FoldersTotal int    `json:"foldersTotal"`
	Copied       int    `json:"copied"`
	Skipped      int    `json:"skipped"`
}

var ErrImportLogin = errors.New("the other server rejected the username or password")

var skippedSourceFolders = []imap.MailboxAttr{imap.MailboxAttrNoSelect, imap.MailboxAttrAll, imap.MailboxAttrFlagged, imap.MailboxAttrImportant}

const importBatch = 25

func dialSource(ctx context.Context, src ImportSource) (*imapclient.Client, error) {
	conn, err := src.Dial(ctx, "tcp", net.JoinHostPort(src.Host, strconv.Itoa(src.Port)))
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", src.Host, err)
	}
	cfg := &tls.Config{ServerName: src.Host, MinVersion: tls.VersionTLS12}
	if src.StartTLS {
		ic, err := imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: cfg})
		if err != nil {
			return nil, fmt.Errorf("start TLS with %s: %w", src.Host, err)
		}
		return ic, nil
	}
	tc := tls.Client(conn, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("TLS with %s: %w", src.Host, err)
	}
	return imapclient.New(tc, nil), nil
}

func CheckImportSource(ctx context.Context, src ImportSource) error {
	ic, err := dialSource(ctx, src)
	if err != nil {
		return err
	}
	defer ic.Close()
	if err := ic.Login(src.Username, src.Password).Wait(); err != nil {
		return ErrImportLogin
	}
	ic.Logout().Wait()
	return nil
}

func (c *Client) Import(ctx context.Context, address string, src ImportSource, progress func(ImportProgress)) error {
	from, err := dialSource(ctx, src)
	if err != nil {
		return err
	}
	defer from.Close()
	if err := from.Login(src.Username, src.Password).Wait(); err != nil {
		return ErrImportLogin
	}
	to, err := c.open(address)
	if err != nil {
		return err
	}
	defer func() {
		to.Logout().Wait()
		to.Close()
	}()

	sources, err := from.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		return fmt.Errorf("list folders on %s: %w", src.Host, err)
	}
	sources = slices.DeleteFunc(sources, func(l *imap.ListData) bool {
		return slices.ContainsFunc(l.Attrs, func(a imap.MailboxAttr) bool { return slices.Contains(skippedSourceFolders, a) })
	})
	targets, err := to.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		return err
	}

	p := ImportProgress{FoldersTotal: len(sources)}
	for _, folder := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.Folder = folder.Mailbox
		progress(p)
		target, err := importTarget(to, folder, targets)
		if err != nil {
			return err
		}
		if err := copyFolder(ctx, from, to, folder.Mailbox, target, &p, progress); err != nil {
			return fmt.Errorf("copy %s: %w", folder.Mailbox, err)
		}
		p.FoldersDone++
		progress(p)
	}
	return nil
}

func importTarget(to *imapclient.Client, folder *imap.ListData, targets []*imap.ListData) (string, error) {
	if strings.EqualFold(folder.Mailbox, "INBOX") {
		return "INBOX", nil
	}
	for _, a := range folder.Attrs {
		use, ok := specialUses[a]
		if !ok {
			continue
		}
		for _, t := range targets {
			for _, ta := range t.Attrs {
				if specialUses[ta] == use {
					return t.Mailbox, nil
				}
			}
		}
	}
	name := folder.Mailbox
	if folder.Delim != 0 && folder.Delim != '/' {
		name = strings.ReplaceAll(strings.ReplaceAll(name, "/", "-"), string(folder.Delim), "/")
	}
	if slices.ContainsFunc(targets, func(t *imap.ListData) bool { return t.Mailbox == name }) {
		return name, nil
	}
	err := to.Create(name, nil).Wait()
	var ie *imap.Error
	if err != nil && !(errors.As(err, &ie) && ie.Code == imap.ResponseCodeAlreadyExists) {
		return "", fmt.Errorf("create folder %s: %w", name, err)
	}
	return name, nil
}

func existingMessageIDs(to *imapclient.Client, target string) (map[string]bool, error) {
	seen := map[string]bool{}
	data, err := to.Select(target, nil).Wait()
	if err != nil {
		return nil, err
	}
	if data.NumMessages == 0 {
		return seen, nil
	}
	msgs, err := to.Fetch(imap.SeqSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{Envelope: true}).Collect()
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if m.Envelope != nil && m.Envelope.MessageID != "" {
			seen[m.Envelope.MessageID] = true
		}
	}
	return seen, nil
}

func copyFolder(ctx context.Context, from, to *imapclient.Client, source, target string, p *ImportProgress, progress func(ImportProgress)) error {
	seen, err := existingMessageIDs(to, target)
	if err != nil {
		return err
	}
	data, err := from.Select(source, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return err
	}
	if data.NumMessages == 0 {
		return nil
	}
	found, err := from.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return err
	}
	uids := found.AllUIDs()
	body := &imap.FetchItemBodySection{Peek: true}
	for start := 0; start < len(uids); start += importBatch {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch := uids[start:min(start+importBatch, len(uids))]
		heads, err := from.Fetch(imap.UIDSetNum(batch...), &imap.FetchOptions{UID: true, Envelope: true}).Collect()
		if err != nil {
			return err
		}
		var wanted []imap.UID
		for _, m := range heads {
			if m.Envelope != nil && m.Envelope.MessageID != "" && seen[m.Envelope.MessageID] {
				p.Skipped++
				continue
			}
			wanted = append(wanted, m.UID)
		}
		if len(wanted) == 0 {
			progress(*p)
			continue
		}
		msgs, err := from.Fetch(imap.UIDSetNum(wanted...), &imap.FetchOptions{UID: true, Flags: true, InternalDate: true, Envelope: true, BodySection: []*imap.FetchItemBodySection{body}}).Collect()
		if err != nil {
			return err
		}
		for _, m := range msgs {
			raw := m.FindBodySection(body)
			if raw == nil {
				p.Skipped++
				continue
			}
			flags := slices.DeleteFunc(slices.Clone(m.Flags), func(f imap.Flag) bool { return f == imap.FlagDeleted || f == "\\Recent" })
			date := m.InternalDate
			if date.IsZero() {
				date = time.Now()
			}
			cmd := to.Append(target, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: date})
			if _, err := cmd.Write(raw); err != nil {
				return err
			}
			if err := cmd.Close(); err != nil {
				return err
			}
			if _, err := cmd.Wait(); err != nil {
				return err
			}
			if m.Envelope != nil && m.Envelope.MessageID != "" {
				seen[m.Envelope.MessageID] = true
			}
			p.Copied++
		}
		progress(*p)
	}
	return nil
}
