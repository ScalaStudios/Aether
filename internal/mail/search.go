package mail

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type SearchQuery struct {
	From, To, Subject, Body, Attachment string
	Since, Before                       time.Time
	uids                                imap.UIDSet
	uidValidity                         uint32
}

type Hit struct {
	Folder      string    `json:"folder"`
	UID         uint32    `json:"uid"`
	UIDValidity uint32    `json:"uidValidity"`
	MessageID   string    `json:"messageId"`
	Subject     string    `json:"subject"`
	From        []Address `json:"from"`
	To          []Address `json:"to"`
	Date        time.Time `json:"date"`
	Size        int64     `json:"size"`
	Attachments []string  `json:"attachments"`
}

var ErrStopSearch = errors.New("search stopped")

func headerCriteria(key, value string) imap.SearchCriteria {
	return imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: key, Value: value}}}
}

func (q SearchQuery) criteria() *imap.SearchCriteria {
	c := &imap.SearchCriteria{Since: q.Since, Before: q.Before}
	if len(q.uids) > 0 {
		c.UID = []imap.UIDSet{q.uids}
	}
	if q.From != "" {
		c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: "From", Value: q.From})
	}
	if q.Subject != "" {
		c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: "Subject", Value: q.Subject})
	}
	if q.Body != "" {
		c.Body = []string{q.Body}
	}
	if q.To != "" {
		c.Or = append(c.Or, [2]imap.SearchCriteria{
			headerCriteria("To", q.To),
			{Or: [][2]imap.SearchCriteria{{headerCriteria("Cc", q.To), headerCriteria("Bcc", q.To)}}},
		})
	}
	return c
}

func (q SearchQuery) doveadmTerms() []string {
	var terms []string
	add := func(key, value string) {
		if value != "" {
			terms = append(terms, key, value)
		}
	}
	add("from", q.From)
	add("subject", q.Subject)
	add("body", q.Body)
	if q.To != "" {
		terms = append(terms, "(", "to", q.To, "OR", "cc", q.To, "OR", "bcc", q.To, ")")
	}
	if !q.Since.IsZero() {
		add("since", q.Since.Format(time.DateOnly))
	}
	if !q.Before.IsZero() {
		add("before", q.Before.Format(time.DateOnly))
	}
	return terms
}

func attachmentNames(bs imap.BodyStructure) []string {
	names := []string{}
	if bs != nil {
		bs.Walk(func(path []int, part imap.BodyStructure) bool {
			if single, ok := part.(*imap.BodyStructureSinglePart); ok && single.Filename() != "" {
				names = append(names, single.Filename())
			}
			return true
		})
	}
	return names
}

func (q SearchQuery) matchesAttachments(names []string) bool {
	if q.Attachment == "" {
		return true
	}
	want := strings.ToLower(q.Attachment)
	return slices.ContainsFunc(names, func(n string) bool { return strings.Contains(strings.ToLower(n), want) })
}

func (c *Client) Search(ctx context.Context, address string, q SearchQuery, withBody bool, fn func(Hit, []byte) error) error {
	return c.withBounded(ctx, address, func(ic *imapclient.Client, conn *boundedConn) error {
		conn.extend()
		list, err := ic.List("", "*", nil).Collect()
		if err != nil {
			return err
		}
		conn.extend()
		expunged, err := ic.List("", ExpungedPrefix+"*", nil).Collect()
		if err != nil {
			return err
		}
		list = append(list, expunged...)
		for _, l := range list {
			if slices.Contains(l.Attrs, imap.MailboxAttrNoSelect) || SharedFolder(l.Mailbox) {
				continue
			}
			if err := searchFolder(ctx, ic, conn.extend, l.Mailbox, q, withBody, fn); err != nil {
				return err
			}
		}
		return nil
	})
}

func searchFolder(ctx context.Context, ic *imapclient.Client, extend func(), folder string, q SearchQuery, withBody bool, fn func(Hit, []byte) error) error {
	extend()
	sel, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return fmt.Errorf("open %s: %w", folder, err)
	}
	if q.uidValidity != 0 && sel.UIDValidity != q.uidValidity {
		return fmt.Errorf("%w: %s", ErrMailboxChanged, folder)
	}
	extend()
	data, err := ic.UIDSearch(q.criteria(), nil).Wait()
	if err != nil {
		return fmt.Errorf("search %s: %w", folder, err)
	}
	uids := data.AllUIDs()
	batch := 100
	body := &imap.FetchItemBodySection{Peek: true}
	opts := &imap.FetchOptions{UID: true, Envelope: true, InternalDate: true, RFC822Size: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true}}
	if withBody {
		batch = 1
		opts.BodySection = []*imap.FetchItemBodySection{body}
	}
	for start := 0; start < len(uids); start += batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		extend()
		msgs, err := ic.Fetch(imap.UIDSetNum(uids[start:min(start+batch, len(uids))]...), opts).Collect()
		if err != nil {
			return fmt.Errorf("fetch %s: %w", folder, err)
		}
		slices.SortFunc(msgs, func(a, b *imapclient.FetchMessageBuffer) int { return cmp.Compare(a.UID, b.UID) })
		for _, m := range msgs {
			h := Hit{Folder: folder, UID: uint32(m.UID), UIDValidity: sel.UIDValidity, Size: m.RFC822Size, Date: m.InternalDate, From: []Address{}, To: []Address{}, Attachments: attachmentNames(m.BodyStructure)}
			if !q.matchesAttachments(h.Attachments) {
				continue
			}
			if m.Envelope != nil {
				h.MessageID = angleID(m.Envelope.MessageID)
				h.Subject = m.Envelope.Subject
				h.From = addresses(m.Envelope.From)
				h.To = addresses(slices.Concat(m.Envelope.To, m.Envelope.Cc, m.Envelope.Bcc))
				if !m.Envelope.Date.IsZero() {
					h.Date = m.Envelope.Date
				}
			}
			if err := fn(h, m.FindBodySection(body)); err != nil {
				return err
			}
		}
	}
	return nil
}

func WriteMbox(w io.Writer, h Hit, raw []byte) error {
	sender := "MAILER-DAEMON"
	if len(h.From) > 0 && h.From[0].Address != "" && !strings.ContainsAny(h.From[0].Address, " \t\r\n") {
		sender = h.From[0].Address
	}
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "From %s %s\n", sender, h.Date.UTC().Format("Mon Jan _2 15:04:05 2006"))
	lines := bytes.Split(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		if bytes.HasPrefix(bytes.TrimLeft(line, ">"), []byte("From ")) {
			bw.WriteByte('>')
		}
		bw.Write(line)
		bw.WriteByte('\n')
	}
	bw.WriteByte('\n')
	return bw.Flush()
}
