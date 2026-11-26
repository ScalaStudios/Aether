package mail

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
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
	FlagsUpdated int    `json:"flagsUpdated"`
	Deleted      int    `json:"deleted"`
}

var ErrImportLogin = errors.New("the other server rejected the username or password")

var ErrImportResponseTooLarge = errors.New("the other server sent a response larger than the import allows")

const importResponseLimit = 16 << 20

type importBudget struct {
	net.Conn
	used     atomic.Int64
	exceeded atomic.Bool
}

func (b *importBudget) Read(p []byte) (int, error) {
	if b.used.Load() > importResponseLimit {
		b.exceeded.Store(true)
		return 0, ErrImportResponseTooLarge
	}
	n, err := b.Conn.Read(p)
	b.used.Add(int64(n))
	return n, err
}

func (b *importBudget) reset() {
	if b != nil {
		b.used.Store(0)
	}
}

func (b *importBudget) credit(n int64) {
	if b != nil {
		b.used.Add(-n)
	}
}

func (b *importBudget) failed(err error) error {
	if err != nil && b != nil && b.exceeded.Load() {
		return ErrImportResponseTooLarge
	}
	return err
}

var skippedSourceFolders = []imap.MailboxAttr{imap.MailboxAttrNoSelect, imap.MailboxAttrAll, imap.MailboxAttrFlagged, imap.MailboxAttrImportant}

const importBatch = 25

func dialSource(ctx context.Context, src ImportSource) (*imapclient.Client, *importBudget, func(), error) {
	conn, err := src.Dial(ctx, "tcp", net.JoinHostPort(src.Host, strconv.Itoa(src.Port)))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("connect to %s: %w", src.Host, err)
	}
	budget := &importBudget{Conn: conn}
	ic, closeConn, err := importConnection(ctx, budget, &tls.Config{ServerName: src.Host, MinVersion: tls.VersionTLS12}, src.StartTLS)
	return ic, budget, closeConn, budget.failed(err)
}

func importConnection(ctx context.Context, conn net.Conn, cfg *tls.Config, startTLS bool) (*imapclient.Client, func(), error) {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	closeConn := func() { stop(); conn.Close() }
	if startTLS {
		ic, err := imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: cfg})
		if err != nil {
			closeConn()
			return nil, nil, err
		}
		return ic, closeConn, nil
	}
	tc := tls.Client(conn, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		closeConn()
		return nil, nil, err
	}
	return imapclient.New(tc, nil), closeConn, nil
}

func CheckImportSource(ctx context.Context, src ImportSource) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	ic, budget, closeConn, err := dialSource(ctx, src)
	if err != nil {
		return err
	}
	defer closeConn()
	return budget.failed(importLogin(ic, src))
}

func importLogin(ic *imapclient.Client, src ImportSource) error {
	err := ic.Login(src.Username, src.Password).Wait()
	var response *imap.Error
	if errors.As(err, &response) && response.Type == imap.StatusResponseTypeNo {
		return ErrImportLogin
	}
	return err
}

func (c *Client) Import(ctx context.Context, address string, src ImportSource, maxBytes int64, progress func(ImportProgress), sync *ImportSync) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	from, budget, closeSource, err := dialSource(ctx, src)
	if err != nil {
		return err
	}
	defer closeSource()
	if err := importLogin(from, src); err != nil {
		return budget.failed(err)
	}
	dialer := net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", c.imapAddr)
	if err != nil {
		return err
	}
	to, closeTarget, err := importConnection(ctx, conn, c.tls, true)
	if err != nil {
		return err
	}
	defer closeTarget()
	if _, err := c.login(to, address); err != nil {
		return err
	}
	return budget.failed(importFolders(ctx, from, budget, to, src.Host, maxBytes, progress, sync))
}

func importFolders(ctx context.Context, from *imapclient.Client, budget *importBudget, to *imapclient.Client, host string, maxBytes int64, progress func(ImportProgress), sync *ImportSync) error {
	sources, err := from.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		return fmt.Errorf("list folders on %s: %w", host, err)
	}
	sources = slices.DeleteFunc(sources, func(l *imap.ListData) bool {
		return slices.ContainsFunc(l.Attrs, func(a imap.MailboxAttr) bool { return slices.Contains(skippedSourceFolders, a) })
	})
	targets, err := to.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		return err
	}
	targets = slices.DeleteFunc(targets, func(l *imap.ListData) bool { return SharedFolder(l.Mailbox) })

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
		if target != "" {
			if err := copyFolder(ctx, from, budget, to, folder.Mailbox, target, maxBytes, &p, progress, sync); err != nil {
				return fmt.Errorf("copy %s: %w", folder.Mailbox, err)
			}
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
	if SharedFolder(name) {
		return "", nil
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

func copyFolder(ctx context.Context, from *imapclient.Client, budget *importBudget, to *imapclient.Client, source, target string, maxBytes int64, p *ImportProgress, progress func(ImportProgress), sync *ImportSync) error {
	budget.reset()
	sourceData, err := from.Select(source, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return err
	}
	if sourceData.NumMessages == 0 && sync == nil {
		return nil
	}
	targetData, err := to.Select(target, &imap.SelectOptions{ReadOnly: sync == nil}).Wait()
	if err != nil {
		return err
	}
	folder := ImportFolder{source, target, sourceData.UIDValidity, targetData.UIDValidity}
	if sync != nil && !to.Caps().Has(imap.CapUIDPlus) {
		return errors.New("the destination does not support UIDPLUS")
	}
	var copies map[imap.UID]imap.UID
	if sync != nil {
		copies, err = sync.Load(ctx, folder)
		if err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	targets := map[imap.UID][]imap.Flag{}
	var missingIDs []imap.UID
	if targetData.NumMessages > 0 {
		msgs, err := to.Fetch(imap.SeqSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{UID: true, Envelope: true, Flags: sync != nil}).Collect()
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if id := importMessageID(m); id != "" {
				seen[id] = true
			} else {
				missingIDs = append(missingIDs, m.UID)
			}
			if sync != nil {
				targets[m.UID] = m.Flags
			}
		}
	}
	found, err := from.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return err
	}
	uids := found.AllUIDs()
	slices.Sort(uids)
	var targetHashes map[[32]byte]bool
	body := &imap.FetchItemBodySection{Peek: true}
	var fetching *imapclient.FetchCommand
	defer func() {
		if fetching != nil {
			go from.Close()
			fetching.Close()
		}
	}()
	for start := 0; start < len(uids); start += importBatch {
		if err := ctx.Err(); err != nil {
			return err
		}
		budget.reset()
		batch := uids[start:min(start+importBatch, len(uids))]
		heads, err := from.Fetch(imap.UIDSetNum(batch...), &imap.FetchOptions{UID: true, Envelope: true, RFC822Size: true, Flags: sync != nil}).Collect()
		if err != nil {
			return err
		}
		var wanted []imap.UID
		for _, m := range heads {
			if uid, tracked := copies[m.UID]; tracked {
				delete(copies, m.UID)
				if flags, exists := targets[uid]; exists {
					if err := sync.update(to, uid, flags, m.Flags, p); err != nil {
						return err
					}
					p.Skipped++
					continue
				}
			}
			if seen[importMessageID(m)] || m.RFC822Size > maxBytes || (sync != nil && sync.Deletions && slices.Contains(m.Flags, imap.FlagDeleted)) {
				p.Skipped++
				continue
			}
			wanted = append(wanted, m.UID)
		}
		if len(wanted) > 0 {
			cmd := from.Fetch(imap.UIDSetNum(wanted...), &imap.FetchOptions{UID: true, Flags: true, InternalDate: true, Envelope: true, BodySection: []*imap.FetchItemBodySection{body}})
			fetching = cmd
			for data := cmd.Next(); data != nil; data = cmd.Next() {
				m, err := collectLimited(data, budget, maxBytes)
				if err != nil {
					return err
				}
				raw, id := m.FindBodySection(body), importMessageID(m)
				if raw == nil || seen[id] || (sync != nil && sync.Deletions && slices.Contains(m.Flags, imap.FlagDeleted)) {
					p.Skipped++
					continue
				}
				if id == "" {
					if targetHashes == nil {
						targetHashes, err = importBodyHashes(to, missingIDs, body)
						if err != nil {
							return err
						}
					}
					hash := sha256.Sum256(raw)
					if targetHashes[hash] {
						p.Skipped++
						continue
					}
					targetHashes[hash] = true
				}
				appended, err := appendOriginal(to, target, m, raw)
				if err != nil {
					return err
				}
				p.Copied++
				progress(*p)
				if sync != nil {
					if appended == nil || appended.UIDValidity != folder.TargetValidity || appended.UID == 0 {
						return errors.New("the destination did not confirm the copied message UID")
					}
					if err := sync.Save(ctx, folder, m.UID, appended.UID); err != nil {
						return err
					}
				}
				if id != "" && len(id) <= 998 {
					seen[id] = true
				}
			}
			fetching = nil
			if err := cmd.Close(); err != nil {
				return err
			}
		}
		progress(*p)
	}
	if sync != nil && sync.Deletions {
		for sourceUID, uid := range copies {
			_, stillPresent := slices.BinarySearch(uids, sourceUID)
			if _, exists := targets[uid]; exists && !stillPresent {
				if err := deleteImportCopy(to, uid, p); err != nil {
					return err
				}
				progress(*p)
			}
		}
	}
	return nil
}

func collectLimited(data *imapclient.FetchMessageData, budget *importBudget, maxBytes int64) (*imapclient.FetchMessageBuffer, error) {
	m := &imapclient.FetchMessageBuffer{SeqNum: data.SeqNum}
	credited := false
	for item := data.Next(); item != nil; item = data.Next() {
		switch item := item.(type) {
		case imapclient.FetchItemDataBodySection:
			if item.Literal == nil || credited {
				continue
			}
			credited = true
			budget.credit(item.Literal.Size())
			if item.Literal.Size() > maxBytes {
				continue
			}
			raw, err := io.ReadAll(item.Literal)
			if err != nil {
				return nil, err
			}
			m.BodySection = append(m.BodySection, imapclient.FetchBodySectionBuffer{Section: item.Section, Bytes: raw})
		case imapclient.FetchItemDataUID:
			m.UID = item.UID
		case imapclient.FetchItemDataFlags:
			m.Flags = item.Flags
		case imapclient.FetchItemDataInternalDate:
			m.InternalDate = item.Time
		case imapclient.FetchItemDataEnvelope:
			m.Envelope = item.Envelope
		}
	}
	return m, nil
}

func importMessageID(m *imapclient.FetchMessageBuffer) string {
	if m.Envelope != nil {
		return m.Envelope.MessageID
	}
	return ""
}

func importBodyHashes(to *imapclient.Client, uids []imap.UID, body *imap.FetchItemBodySection) (map[[32]byte]bool, error) {
	hashes := map[[32]byte]bool{}
	for start := 0; start < len(uids); start += importBatch {
		cmd := to.Fetch(imap.UIDSetNum(uids[start:min(start+importBatch, len(uids))]...), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{body}})
		for data := cmd.Next(); data != nil; data = cmd.Next() {
			for item := data.Next(); item != nil; item = data.Next() {
				if section, ok := item.(imapclient.FetchItemDataBodySection); ok && section.Literal != nil {
					h := sha256.New()
					if _, err := io.Copy(h, section.Literal); err != nil {
						return nil, err
					}
					hashes[[32]byte(h.Sum(nil))] = true
				}
			}
		}
		if err := cmd.Close(); err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

func appendOriginal(to *imapclient.Client, target string, m *imapclient.FetchMessageBuffer, raw []byte) (*imap.AppendData, error) {
	flags := slices.DeleteFunc(slices.Clone(m.Flags), func(f imap.Flag) bool { return f == imap.FlagDeleted || f == "\\Recent" })
	date := m.InternalDate
	if date.IsZero() {
		date = time.Now()
	}
	raw = WithoutHeader(raw, AuthenticationHeader)
	cmd := to.Append(target, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: date})
	if _, err := cmd.Write(raw); err != nil {
		return nil, err
	}
	if err := cmd.Close(); err != nil {
		return nil, err
	}
	return cmd.Wait()
}
