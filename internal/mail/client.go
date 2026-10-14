package mail

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

var ErrNotFound = errors.New("message not found")

type Client struct {
	imapAddr       string
	submissionAddr string
	tls            *tls.Config
	masterUser     string
	masterPassword string
	hostname       string
}

func NewClient(imapAddr, submissionAddr, hostname, caFile, masterUser, masterPassword string) (*Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read mail TLS CA: %w", err)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", caFile)
		}
	}
	return &Client{
		imapAddr: imapAddr, submissionAddr: submissionAddr, hostname: hostname,
		tls:        &tls.Config{ServerName: hostname, RootCAs: roots, MinVersion: tls.VersionTLS12},
		masterUser: masterUser, masterPassword: masterPassword,
	}, nil
}

func (c *Client) masterLogin(address string) string {
	return address + "*" + c.masterUser
}

func (c *Client) open(address string) (*imapclient.Client, error) {
	ic, err := imapclient.DialStartTLS(c.imapAddr, &imapclient.Options{TLSConfig: c.tls})
	if err != nil {
		return nil, fmt.Errorf("imap connect: %w", err)
	}
	if err := ic.Login(c.masterLogin(address), c.masterPassword).Wait(); err != nil {
		ic.Close()
		return nil, fmt.Errorf("imap login for %s: %w", address, err)
	}
	return ic, nil
}

func (c *Client) with(address string, fn func(*imapclient.Client) error) error {
	ic, err := c.open(address)
	if err != nil {
		return err
	}
	defer func() {
		ic.Logout().Wait()
		ic.Close()
	}()
	return fn(ic)
}

type Folder struct {
	Name       string `json:"name"`
	SpecialUse string `json:"specialUse"`
	Total      uint32 `json:"total"`
	Unread     uint32 `json:"unread"`
}

var specialUses = map[imap.MailboxAttr]string{
	imap.MailboxAttrDrafts: "drafts", imap.MailboxAttrSent: "sent", imap.MailboxAttrTrash: "trash",
	imap.MailboxAttrJunk: "junk", imap.MailboxAttrArchive: "archive",
}

var folderOrder = []string{"inbox", "drafts", "sent", "archive", "junk", "trash", ""}

func (c *Client) Folders(address string) ([]Folder, error) {
	var out []Folder
	err := c.with(address, func(ic *imapclient.Client) error {
		list, err := ic.List("", "*", &imap.ListOptions{ReturnStatus: &imap.StatusOptions{NumMessages: true, NumUnseen: true}, ReturnSpecialUse: true}).Collect()
		if err != nil {
			return err
		}
		for _, l := range list {
			if slices.Contains(l.Attrs, imap.MailboxAttrNoSelect) {
				continue
			}
			f := Folder{Name: l.Mailbox}
			if strings.EqualFold(l.Mailbox, "INBOX") {
				f.SpecialUse = "inbox"
			}
			for _, a := range l.Attrs {
				if use, ok := specialUses[a]; ok {
					f.SpecialUse = use
				}
			}
			if l.Status != nil && l.Status.NumMessages != nil {
				f.Total = *l.Status.NumMessages
			}
			if l.Status != nil && l.Status.NumUnseen != nil {
				f.Unread = *l.Status.NumUnseen
			}
			out = append(out, f)
		}
		return nil
	})
	slices.SortStableFunc(out, func(a, b Folder) int {
		if d := slices.Index(folderOrder, a.SpecialUse) - slices.Index(folderOrder, b.SpecialUse); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, err
}

func (c *Client) specialFolder(ic *imapclient.Client, use string) (string, error) {
	list, err := ic.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		return "", err
	}
	for _, l := range list {
		for _, a := range l.Attrs {
			if specialUses[a] == use {
				return l.Mailbox, nil
			}
		}
	}
	return "", fmt.Errorf("no %s folder", use)
}

type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

type Summary struct {
	UID            uint32    `json:"uid"`
	Subject        string    `json:"subject"`
	From           []Address `json:"from"`
	To             []Address `json:"to"`
	Date           time.Time `json:"date"`
	Seen           bool      `json:"seen"`
	Flagged        bool      `json:"flagged"`
	Answered       bool      `json:"answered"`
	HasAttachments bool      `json:"hasAttachments"`
	Size           int64     `json:"size"`
	ThreadUIDs     []uint32  `json:"threadUids,omitempty"`
}

type Page struct {
	Messages   []Summary `json:"messages"`
	Total      int       `json:"total"`
	NextCursor *uint32   `json:"nextCursor"`
}

func addresses(in []imap.Address) []Address {
	out := []Address{}
	for _, a := range in {
		if addr := a.Addr(); addr != "" {
			out = append(out, Address{Name: a.Name, Address: addr})
		}
	}
	return out
}

func summarize(m *imapclient.FetchMessageBuffer) Summary {
	s := Summary{UID: uint32(m.UID), Size: m.RFC822Size, Date: m.InternalDate, From: []Address{}, To: []Address{}}
	if m.Envelope != nil {
		s.Subject = m.Envelope.Subject
		s.From = addresses(m.Envelope.From)
		s.To = addresses(m.Envelope.To)
		if !m.Envelope.Date.IsZero() {
			s.Date = m.Envelope.Date
		}
	}
	s.Seen = slices.Contains(m.Flags, imap.FlagSeen)
	s.Flagged = slices.Contains(m.Flags, imap.FlagFlagged)
	s.Answered = slices.Contains(m.Flags, imap.FlagAnswered)
	if m.BodyStructure != nil {
		m.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
			if single, ok := part.(*imap.BodyStructureSinglePart); ok && isAttachment(single) {
				s.HasAttachments = true
			}
			return true
		})
	}
	return s
}

func isAttachment(p *imap.BodyStructureSinglePart) bool {
	if d := p.Disposition(); d != nil && strings.EqualFold(d.Value, "attachment") {
		return true
	}
	return p.Filename() != "" && !strings.EqualFold(p.Type, "text")
}

func flattenThread(t imapclient.ThreadData) []uint32 {
	out := slices.Clone(t.Chain)
	for _, sub := range t.SubThreads {
		out = append(out, flattenThread(sub)...)
	}
	return out
}

func (c *Client) List(address, folder, query string, before uint32, limit int, threaded bool) (*Page, error) {
	page := &Page{Messages: []Summary{}}
	err := c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return ErrNotFound
		}
		criteria := &imap.SearchCriteria{}
		if q := strings.TrimSpace(query); q != "" {
			criteria.Text = strings.Fields(q)
		}
		var groups [][]uint32
		if threaded {
			threads, err := ic.UIDThread(&imapclient.ThreadOptions{Algorithm: imap.ThreadReferences, SearchCriteria: criteria}).Wait()
			if err != nil {
				return err
			}
			for _, t := range threads {
				uids := flattenThread(t)
				slices.SortFunc(uids, func(a, b uint32) int { return int(b) - int(a) })
				groups = append(groups, uids)
			}
		} else {
			data, err := ic.UIDSearch(criteria, nil).Wait()
			if err != nil {
				return err
			}
			for _, uid := range data.AllUIDs() {
				groups = append(groups, []uint32{uint32(uid)})
			}
		}
		slices.SortFunc(groups, func(a, b []uint32) int { return int(b[0]) - int(a[0]) })
		page.Total = len(groups)
		if before > 0 {
			i := 0
			for i < len(groups) && groups[i][0] >= before {
				i++
			}
			groups = groups[i:]
		}
		if len(groups) > limit {
			groups = groups[:limit]
			next := groups[len(groups)-1][0]
			page.NextCursor = &next
		}
		if len(groups) == 0 {
			return nil
		}
		var all []imap.UID
		for _, g := range groups {
			for _, uid := range g {
				all = append(all, imap.UID(uid))
			}
		}
		msgs, err := ic.Fetch(imap.UIDSetNum(all...), &imap.FetchOptions{
			UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true,
			BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		}).Collect()
		if err != nil {
			return err
		}
		byUID := map[uint32]Summary{}
		for _, m := range msgs {
			byUID[uint32(m.UID)] = summarize(m)
		}
		for _, g := range groups {
			latest, ok := byUID[g[0]]
			if !ok {
				continue
			}
			if len(g) > 1 {
				latest.ThreadUIDs = g
				for _, uid := range g[1:] {
					if m, ok := byUID[uid]; ok {
						latest.Seen = latest.Seen && m.Seen
						latest.Flagged = latest.Flagged || m.Flagged
						latest.HasAttachments = latest.HasAttachments || m.HasAttachments
					}
				}
			}
			page.Messages = append(page.Messages, latest)
		}
		return nil
	})
	return page, err
}

type Attachment struct {
	Part        string `json:"part"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

type Message struct {
	Summary
	Cc          []Address    `json:"cc"`
	Bcc         []Address    `json:"bcc"`
	ReplyTo     []Address    `json:"replyTo"`
	MessageID   string       `json:"messageId"`
	InReplyTo   string       `json:"inReplyTo"`
	References  []string     `json:"references"`
	Text        string       `json:"text"`
	HTML        string       `json:"html"`
	Attachments []Attachment `json:"attachments"`
}

func partString(path []int) string {
	s := make([]string, len(path))
	for i, p := range path {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ".")
}

const maxInlineBody = 8 << 20

func (c *Client) Get(address, folder string, uid uint32) (*Message, error) {
	var out *Message
	err := c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return ErrNotFound
		}
		uidSet := imap.UIDSetNum(imap.UID(uid))
		refs := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"References"}, Peek: true}
		msgs, err := ic.Fetch(uidSet, &imap.FetchOptions{
			UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true,
			BodyStructure: &imap.FetchItemBodyStructure{Extended: true}, BodySection: []*imap.FetchItemBodySection{refs},
		}).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return ErrNotFound
		}
		m := msgs[0]
		out = &Message{Summary: summarize(m), Cc: []Address{}, Bcc: []Address{}, ReplyTo: []Address{}, References: []string{}, Attachments: []Attachment{}}
		if m.Envelope != nil {
			out.Cc = addresses(m.Envelope.Cc)
			out.Bcc = addresses(m.Envelope.Bcc)
			out.ReplyTo = addresses(m.Envelope.ReplyTo)
			out.MessageID = angleID(m.Envelope.MessageID)
			if len(m.Envelope.InReplyTo) > 0 {
				out.InReplyTo = angleID(m.Envelope.InReplyTo[0])
			}
		}
		if hdr := m.FindBodySection(refs); hdr != nil {
			if e, err := message.Read(bytes.NewReader(append(hdr, '\r', '\n'))); err == nil {
				out.References = parseMessageIDs(e.Header.Get("References"))
			}
		}
		var textPart, htmlPart *imap.BodyStructureSinglePart
		var textPath, htmlPath []int
		if m.BodyStructure != nil {
			m.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
				single, ok := part.(*imap.BodyStructureSinglePart)
				if !ok {
					return true
				}
				switch {
				case isAttachment(single):
					out.Attachments = append(out.Attachments, Attachment{Part: partString(path), Filename: firstNonEmpty(single.Filename(), "attachment-"+partString(path)), ContentType: single.MediaType(), Size: int64(single.Size)})
				case single.MediaType() == "text/plain" && textPart == nil:
					textPart, textPath = single, slices.Clone(path)
				case single.MediaType() == "text/html" && htmlPart == nil:
					htmlPart, htmlPath = single, slices.Clone(path)
				}
				return true
			})
		}
		for _, p := range []struct {
			part *imap.BodyStructureSinglePart
			path []int
			dst  *string
		}{{textPart, textPath, &out.Text}, {htmlPart, htmlPath, &out.HTML}} {
			if p.part == nil || p.part.Size > maxInlineBody {
				continue
			}
			body, err := fetchPart(ic, uidSet, p.path)
			if err != nil {
				return err
			}
			*p.dst = decodeCharset(body, p.part.Params["charset"])
		}
		if out.HTML != "" {
			out.HTML = SanitizeHTML(out.HTML)
		}
		return nil
	})
	return out, err
}

func fetchPart(ic *imapclient.Client, uidSet imap.UIDSet, path []int) ([]byte, error) {
	section := &imap.FetchItemBinarySection{Part: path, Peek: true}
	msgs, err := ic.Fetch(uidSet, &imap.FetchOptions{UID: true, BinarySection: []*imap.FetchItemBinarySection{section}}).Collect()
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, ErrNotFound
	}
	return msgs[0].FindBinarySection(section), nil
}

func decodeCharset(body []byte, charset string) string {
	if charset == "" || strings.EqualFold(charset, "utf-8") || strings.EqualFold(charset, "us-ascii") {
		return strings.ToValidUTF8(string(body), "�")
	}
	r, err := message.CharsetReader(strings.ToLower(charset), bytes.NewReader(body))
	if err != nil {
		return strings.ToValidUTF8(string(body), "�")
	}
	decoded, err := io.ReadAll(r)
	if err != nil {
		return strings.ToValidUTF8(string(body), "�")
	}
	return string(decoded)
}

func angleID(id string) string {
	if id == "" || strings.HasPrefix(id, "<") {
		return id
	}
	return "<" + id + ">"
}

func parseMessageIDs(v string) []string {
	out := []string{}
	for _, f := range strings.Fields(v) {
		if strings.HasPrefix(f, "<") && strings.HasSuffix(f, ">") {
			out = append(out, f)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

type AttachmentData struct {
	Filename    string
	ContentType string
	Body        []byte
}

func (c *Client) Attachment(address, folder string, uid uint32, part []int) (*AttachmentData, error) {
	var out *AttachmentData
	err := c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return ErrNotFound
		}
		uidSet := imap.UIDSetNum(imap.UID(uid))
		msgs, err := ic.Fetch(uidSet, &imap.FetchOptions{UID: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true}}).Collect()
		if err != nil || len(msgs) == 0 || msgs[0].BodyStructure == nil {
			return ErrNotFound
		}
		var found *imap.BodyStructureSinglePart
		msgs[0].BodyStructure.Walk(func(path []int, p imap.BodyStructure) bool {
			if single, ok := p.(*imap.BodyStructureSinglePart); ok && slices.Equal(path, part) {
				found = single
			}
			return true
		})
		if found == nil {
			return ErrNotFound
		}
		body, err := fetchPart(ic, uidSet, part)
		if err != nil {
			return err
		}
		out = &AttachmentData{Filename: firstNonEmpty(found.Filename(), "attachment-"+partString(part)), ContentType: found.MediaType(), Body: body}
		return nil
	})
	return out, err
}

func (c *Client) SetFlags(address, folder string, uid uint32, seen, flagged *bool) error {
	return c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, nil).Wait(); err != nil {
			return ErrNotFound
		}
		uidSet := imap.UIDSetNum(imap.UID(uid))
		for _, f := range []struct {
			v    *bool
			flag imap.Flag
		}{{seen, imap.FlagSeen}, {flagged, imap.FlagFlagged}} {
			if f.v == nil {
				continue
			}
			op := imap.StoreFlagsDel
			if *f.v {
				op = imap.StoreFlagsAdd
			}
			if err := ic.Store(uidSet, &imap.StoreFlags{Op: op, Flags: []imap.Flag{f.flag}, Silent: true}, nil).Close(); err != nil {
				return err
			}
		}
		return nil
	})
}

func (c *Client) Move(address, folder string, uid uint32, to string) error {
	return c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Status(to, &imap.StatusOptions{NumMessages: true}).Wait(); err != nil {
			return ErrNotFound
		}
		if _, err := ic.Select(folder, nil).Wait(); err != nil {
			return ErrNotFound
		}
		_, err := ic.Move(imap.UIDSetNum(imap.UID(uid)), to).Wait()
		return err
	})
}

func (c *Client) Delete(address, folder string, uid uint32) error {
	return c.with(address, func(ic *imapclient.Client) error {
		trash, err := c.specialFolder(ic, "trash")
		if err != nil {
			return err
		}
		if _, err := ic.Select(folder, nil).Wait(); err != nil {
			return ErrNotFound
		}
		uidSet := imap.UIDSetNum(imap.UID(uid))
		if folder != trash {
			_, err := ic.Move(uidSet, trash).Wait()
			return err
		}
		return expungeUID(ic, folder, uid)
	})
}

func (c *Client) Send(from Address, rcpts []string, raw []byte) error {
	sc, err := smtp.DialStartTLS(c.submissionAddr, c.tls)
	if err != nil {
		return fmt.Errorf("submission connect: %w", err)
	}
	defer sc.Close()
	if err := sc.Auth(sasl.NewPlainClient("", c.masterLogin(from.Address), c.masterPassword)); err != nil {
		return fmt.Errorf("submission auth: %w", err)
	}
	if err := sc.SendMail(from.Address, rcpts, bytes.NewReader(raw)); err != nil {
		return err
	}
	return sc.Quit()
}

func (c *Client) SaveSent(address string, raw []byte) error {
	return c.with(address, func(ic *imapclient.Client) error {
		sent, err := c.specialFolder(ic, "sent")
		if err != nil {
			return err
		}
		cmd := ic.Append(sent, int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}, Time: time.Now()})
		if _, err := cmd.Write(raw); err != nil {
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		_, err = cmd.Wait()
		return err
	})
}

func (c *Client) SaveDraft(address string, raw []byte, replaceUID uint32) (uint32, string, error) {
	var uid uint32
	var drafts string
	err := c.with(address, func(ic *imapclient.Client) error {
		var err error
		if drafts, err = c.specialFolder(ic, "drafts"); err != nil {
			return err
		}
		cmd := ic.Append(drafts, int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagDraft, imap.FlagSeen}, Time: time.Now()})
		if _, err := cmd.Write(raw); err != nil {
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		data, err := cmd.Wait()
		if err != nil {
			return err
		}
		if data.UID == 0 {
			return errors.New("the IMAP server did not report the draft UID (UIDPLUS)")
		}
		uid = uint32(data.UID)
		if replaceUID != 0 && replaceUID != uid {
			return expungeUID(ic, drafts, replaceUID)
		}
		return nil
	})
	return uid, drafts, err
}

func (c *Client) DeleteDraft(address string, uid uint32) error {
	return c.with(address, func(ic *imapclient.Client) error {
		drafts, err := c.specialFolder(ic, "drafts")
		if err != nil {
			return err
		}
		return expungeUID(ic, drafts, uid)
	})
}

func expungeUID(ic *imapclient.Client, folder string, uid uint32) error {
	if _, err := ic.Select(folder, nil).Wait(); err != nil {
		return ErrNotFound
	}
	uidSet := imap.UIDSetNum(imap.UID(uid))
	if err := ic.Store(uidSet, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
		return err
	}
	return ic.UIDExpunge(uidSet).Close()
}

func (c *Client) MarkAnswered(address string, messageID string) error {
	return c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Select("INBOX", nil).Wait(); err != nil {
			return err
		}
		data, err := ic.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: messageID}}}, nil).Wait()
		if err != nil || len(data.AllUIDs()) == 0 {
			return err
		}
		return ic.Store(imap.UIDSetNum(data.AllUIDs()...), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagAnswered}, Silent: true}, nil).Close()
	})
}

func ContentDisposition(filename string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": filename})
}
