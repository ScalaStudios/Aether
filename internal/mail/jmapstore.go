package mail

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
)

type MailboxState struct {
	Name, Role           string
	Subscribed           bool
	Total, Unread        uint32
	UIDValidity, UIDNext uint32
	HighestModSeq        uint64
}

type MessageHit struct {
	Folder           string
	UIDValidity, UID uint32
}

type MessageFilter struct {
	Folders                                   []string
	Text, From, To, Cc, Subject, Body         string
	Seen, Flagged, Draft, Answered, Forwarded *bool
	Since, Before                             time.Time
	MinSize, MaxSize                          int64
	HasAttachment                             bool
	ThreadMessageID                           string
	Sort                                      string
	Descending                                bool
	Collapse                                  bool
}

type FetchWant struct {
	Preview, Text, HTML bool
}

type StoredPart struct {
	Path, Type, Charset, Name, Disposition string
	Size                                   int64
	Attachment                             bool
	Text                                   string
	Truncated                              bool
}

type StoredMessage struct {
	MessageHit
	Flags      []imap.Flag
	Internal   time.Time
	Size       int64
	Envelope   *imap.Envelope
	References []string
	Parts      []StoredPart
}

func (c *Client) MailboxStates(address string) ([]MailboxState, error) {
	var out []MailboxState
	err := c.with(address, func(ic *imapclient.Client) error {
		opts := &imap.StatusOptions{NumMessages: true, NumUnseen: true, UIDNext: true, UIDValidity: true}
		if ic.Caps().Has(imap.CapCondStore) {
			opts.HighestModSeq = true
		}
		list, err := ic.List("", "*", &imap.ListOptions{ReturnStatus: opts, ReturnSpecialUse: true, ReturnSubscribed: true}).Collect()
		if err != nil && opts.HighestModSeq {
			opts.HighestModSeq = false
			list, err = ic.List("", "*", &imap.ListOptions{ReturnStatus: opts, ReturnSpecialUse: true, ReturnSubscribed: true}).Collect()
		}
		if err != nil {
			return err
		}
		for _, l := range list {
			if slices.Contains(l.Attrs, imap.MailboxAttrNoSelect) || SharedFolder(l.Mailbox) {
				continue
			}
			st := MailboxState{Name: l.Mailbox, Role: specialUse(l), Subscribed: slices.Contains(l.Attrs, imap.MailboxAttrSubscribed)}
			if l.Status != nil {
				if l.Status.NumMessages != nil {
					st.Total = *l.Status.NumMessages
				}
				if l.Status.NumUnseen != nil {
					st.Unread = *l.Status.NumUnseen
				}
				st.UIDValidity = l.Status.UIDValidity
				st.UIDNext = uint32(l.Status.UIDNext)
				st.HighestModSeq = l.Status.HighestModSeq
			}
			out = append(out, st)
		}
		return nil
	})
	slices.SortStableFunc(out, func(a, b MailboxState) int {
		if d := slices.Index(folderOrder, a.Role) - slices.Index(folderOrder, b.Role); d != 0 {
			return d
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, err
}

func (f MessageFilter) criteria() *imap.SearchCriteria {
	c := &imap.SearchCriteria{}
	if f.Text != "" {
		c.Text = strings.Fields(f.Text)
	}
	if f.Body != "" {
		c.Body = strings.Fields(f.Body)
	}
	if f.From != "" {
		c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: "From", Value: f.From})
	}
	if f.To != "" {
		c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: "To", Value: f.To})
	}
	if f.Cc != "" {
		c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: "Cc", Value: f.Cc})
	}
	if f.Subject != "" {
		c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: "Subject", Value: f.Subject})
	}
	if f.Seen != nil {
		if *f.Seen {
			c.Flag = append(c.Flag, imap.FlagSeen)
		} else {
			c.NotFlag = append(c.NotFlag, imap.FlagSeen)
		}
	}
	if f.Flagged != nil {
		if *f.Flagged {
			c.Flag = append(c.Flag, imap.FlagFlagged)
		} else {
			c.NotFlag = append(c.NotFlag, imap.FlagFlagged)
		}
	}
	if f.Draft != nil {
		if *f.Draft {
			c.Flag = append(c.Flag, imap.FlagDraft)
		} else {
			c.NotFlag = append(c.NotFlag, imap.FlagDraft)
		}
	}
	if f.Answered != nil {
		if *f.Answered {
			c.Flag = append(c.Flag, imap.FlagAnswered)
		} else {
			c.NotFlag = append(c.NotFlag, imap.FlagAnswered)
		}
	}
	if f.Forwarded != nil {
		flag := imap.Flag("$Forwarded")
		if *f.Forwarded {
			c.Flag = append(c.Flag, flag)
		} else {
			c.NotFlag = append(c.NotFlag, flag)
		}
	}
	if !f.Since.IsZero() {
		c.Since = f.Since
	}
	if !f.Before.IsZero() {
		c.Before = f.Before
	}
	if f.MinSize > 0 {
		c.Larger = f.MinSize - 1
	}
	if f.MaxSize > 0 {
		c.Smaller = f.MaxSize + 1
	}
	if f.ThreadMessageID != "" {
		id := strings.Trim(f.ThreadMessageID, "<>")
		header := func(key string) imap.SearchCriteria {
			return imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: key, Value: id}}}
		}
		thread := imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{
			header("Message-ID"),
			{Or: [][2]imap.SearchCriteria{{header("References"), header("In-Reply-To")}}},
		}}}
		c.And(&thread)
	}
	return c
}

func (c *Client) SearchMessages(address string, f MessageFilter) ([]MessageHit, error) {
	var hits []MessageHit
	err := c.with(address, func(ic *imapclient.Client) error {
		folders := f.Folders
		if len(folders) == 0 {
			states, err := c.mailboxNames(ic)
			if err != nil {
				return err
			}
			folders = states
		}
		explicit := len(f.Folders) > 0
		for _, folder := range folders {
			if SharedFolder(folder) {
				if explicit {
					return ErrNotFound
				}
				continue
			}
			selected, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
			if err != nil {
				if explicit {
					return ErrNotFound
				}
				continue
			}
			criteria := f.criteria()
			var uids []uint32
			if ic.Caps().Has(imap.CapSort) {
				key := cmpOr(SortKeys[f.Sort], imapclient.SortKeyArrival)
				uids, err = sortUIDs(ic, criteria, key)
				if err != nil {
					uids, err = searchUIDs(ic, criteria)
				}
			} else {
				uids, err = searchUIDs(ic, criteria)
			}
			if err != nil {
				return err
			}
			if f.Descending {
				slices.Reverse(uids)
			}
			if f.HasAttachment {
				if uids, err = withAttachments(ic, uids, nil); err != nil {
					return err
				}
			}
			if f.Collapse && slices.Contains(ic.Caps().ThreadAlgorithms(), imap.ThreadReferences) {
				threads, err := ic.UIDThread(&imapclient.ThreadOptions{Algorithm: imap.ThreadReferences, SearchCriteria: criteria}).Wait()
				if err != nil {
					return err
				}
				uids = collapseSorted(uids, threads)
			}
			if len(uids) > 20000 {
				uids = uids[:20000]
			}
			for _, uid := range uids {
				hits = append(hits, MessageHit{Folder: folder, UIDValidity: selected.UIDValidity, UID: uid})
			}
		}
		return nil
	})
	return hits, err
}

func cmpOr[T comparable](v, fallback T) T {
	var zero T
	if v == zero {
		return fallback
	}
	return v
}

func searchUIDs(ic *imapclient.Client, criteria *imap.SearchCriteria) ([]uint32, error) {
	data, err := ic.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, err
	}
	uids := data.AllUIDs()
	out := make([]uint32, len(uids))
	for i, uid := range uids {
		out[i] = uint32(uid)
	}
	return out, nil
}

func collapseSorted(uids []uint32, threads []imapclient.ThreadData) []uint32 {
	group := map[uint32]int{}
	for i, t := range threads {
		for _, uid := range flattenThread(t) {
			group[uid] = i
		}
	}
	seen := map[int]bool{}
	var out []uint32
	for _, uid := range uids {
		g, ok := group[uid]
		if !ok {
			out = append(out, uid)
			continue
		}
		if seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, uid)
	}
	return out
}

func (c *Client) mailboxNames(ic *imapclient.Client) ([]string, error) {
	list, err := ic.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range list {
		if slices.Contains(l.Attrs, imap.MailboxAttrNoSelect) || SharedFolder(l.Mailbox) {
			continue
		}
		names = append(names, l.Mailbox)
	}
	return names, nil
}

func (c *Client) FetchStored(address string, hits []MessageHit, want FetchWant) ([]StoredMessage, error) {
	var out []StoredMessage
	if len(hits) == 0 {
		return out, nil
	}
	err := c.with(address, func(ic *imapclient.Client) error {
		byFolder := map[string][]MessageHit{}
		var order []string
		for _, h := range hits {
			if _, ok := byFolder[h.Folder]; !ok {
				order = append(order, h.Folder)
			}
			byFolder[h.Folder] = append(byFolder[h.Folder], h)
		}
		for _, folder := range order {
			group := byFolder[folder]
			selected, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
			if err != nil {
				continue
			}
			var uids []uint32
			for _, h := range group {
				if h.UIDValidity == selected.UIDValidity {
					uids = append(uids, h.UID)
				}
			}
			if len(uids) == 0 {
				continue
			}
			refs := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"References"}, Peek: true}
			msgs, err := ic.Fetch(uidSet(uids), &imap.FetchOptions{
				UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true,
				BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
				BodySection:   []*imap.FetchItemBodySection{refs},
			}).Collect()
			if err != nil {
				return err
			}
			for _, m := range msgs {
				stored := StoredMessage{MessageHit: MessageHit{Folder: folder, UIDValidity: selected.UIDValidity, UID: uint32(m.UID)}, Flags: m.Flags, Internal: m.InternalDate, Size: m.RFC822Size, Envelope: m.Envelope, References: []string{}}
				if hdr := m.FindBodySection(refs); hdr != nil {
					if entity, err := message.Read(bytes.NewReader(append(hdr, '\r', '\n'))); err == nil {
						stored.References = parseMessageIDs(entity.Header.Get("References"))
					}
				}
				stored.Parts = partsOf(m.BodyStructure)
				if err := fillBodies(ic, imap.UIDSetNum(m.UID), &stored, want); err != nil {
					return err
				}
				out = append(out, stored)
			}
		}
		return nil
	})
	return out, err
}

func partsOf(bs imap.BodyStructure) []StoredPart {
	var parts []StoredPart
	if bs == nil {
		return parts
	}
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		single, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		p := StoredPart{Path: partString(path), Type: single.MediaType(), Charset: single.Params["charset"], Name: single.Filename(), Size: int64(single.Size)}
		if d := single.Disposition(); d != nil {
			p.Disposition = strings.ToLower(d.Value)
		}
		p.Attachment = isAttachment(single)
		parts = append(parts, p)
		return true
	})
	return parts
}

const jmapBodyLimit = 1 << 20

func fillBodies(ic *imapclient.Client, set imap.UIDSet, stored *StoredMessage, want FetchWant) error {
	for i := range stored.Parts {
		p := &stored.Parts[i]
		wantText := want.Text && p.Type == "text/plain" && !p.Attachment && p.Text == ""
		wantHTML := want.HTML && p.Type == "text/html" && !p.Attachment && p.Text == ""
		wantPreview := want.Preview && p.Type == "text/plain" && !p.Attachment && p.Text == ""
		if !wantText && !wantHTML && !wantPreview {
			continue
		}
		if p.Size > jmapBodyLimit {
			p.Truncated = true
			continue
		}
		path := partPath(p.Path)
		body, err := fetchPart(ic, set, path)
		if err != nil {
			return err
		}
		p.Text = decodeCharset(body, p.Charset)
	}
	return nil
}

func partPath(s string) []int {
	var path []int
	if s == "" {
		return []int{1}
	}
	for _, piece := range strings.Split(s, ".") {
		n := 0
		for _, r := range piece {
			if r < '0' || r > '9' {
				return path
			}
			n = n*10 + int(r-'0')
		}
		if n > 0 {
			path = append(path, n)
		}
	}
	return path
}

func (c *Client) ReadMessage(address, folder string, validity, uid uint32) ([]byte, error) {
	var raw []byte
	err := c.with(address, func(ic *imapclient.Client) error {
		selected, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil || selected.UIDValidity != validity {
			return ErrNotFound
		}
		section := &imap.FetchItemBodySection{Peek: true}
		msgs, err := ic.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, RFC822Size: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 || msgs[0].RFC822Size > 25<<20 {
			if len(msgs) == 0 {
				return ErrNotFound
			}
			return ErrTooLarge
		}
		raw = msgs[0].FindBodySection(section)
		if raw == nil {
			return ErrNotFound
		}
		return nil
	})
	return raw, err
}

func (c *Client) ReadMessagePart(address, folder string, validity, uid uint32, part []int) ([]byte, error) {
	var body []byte
	err := c.with(address, func(ic *imapclient.Client) error {
		selected, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil || selected.UIDValidity != validity {
			return ErrNotFound
		}
		body, err = fetchPart(ic, imap.UIDSetNum(imap.UID(uid)), part)
		return err
	})
	return body, err
}

func (c *Client) AppendMessage(address, folder string, flags []imap.Flag, when time.Time, raw []byte) (uint32, uint32, error) {
	if SharedFolder(folder) || len(raw) == 0 || len(raw) > 25<<20 {
		if len(raw) > 25<<20 {
			return 0, 0, ErrTooLarge
		}
		return 0, 0, ErrNotFound
	}
	var validity, uid uint32
	err := c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Status(folder, &imap.StatusOptions{UIDValidity: true}).Wait(); err != nil {
			return ErrNotFound
		}
		if when.IsZero() {
			when = time.Now()
		}
		cmd := ic.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: when})
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
			return errors.New("the IMAP server did not report the appended UID (UIDPLUS)")
		}
		validity, uid = data.UIDValidity, uint32(data.UID)
		return nil
	})
	return validity, uid, err
}

func (c *Client) UpdateKeywords(address, folder string, validity, uid uint32, add, remove []imap.Flag) error {
	if SharedFolder(folder) {
		return ErrNotFound
	}
	return c.with(address, func(ic *imapclient.Client) error {
		selected, err := ic.Select(folder, nil).Wait()
		if err != nil || selected.UIDValidity != validity {
			return ErrNotFound
		}
		set := imap.UIDSetNum(imap.UID(uid))
		if len(add) > 0 {
			if err := ic.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: add, Silent: true}, nil).Close(); err != nil {
				return err
			}
		}
		if len(remove) > 0 {
			if err := ic.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsDel, Flags: remove, Silent: true}, nil).Close(); err != nil {
				return err
			}
		}
		return nil
	})
}

func (c *Client) RelocateMessage(address, folder string, validity, uid uint32, to string, copy bool) (uint32, uint32, error) {
	if SharedFolder(folder, to) {
		return 0, 0, ErrNotFound
	}
	var newValidity, newUID uint32
	err := c.with(address, func(ic *imapclient.Client) error {
		dest, err := ic.Status(to, &imap.StatusOptions{UIDValidity: true}).Wait()
		if err != nil {
			return ErrNotFound
		}
		selected, err := ic.Select(folder, nil).Wait()
		if err != nil || selected.UIDValidity != validity {
			return ErrNotFound
		}
		set := imap.UIDSetNum(imap.UID(uid))
		if copy {
			data, err := ic.Copy(set, to).Wait()
			if err != nil {
				return err
			}
			nums, ok := data.DestUIDs.Nums()
			if !ok || len(nums) != 1 {
				return errors.New("the IMAP server did not report the copied UID (UIDPLUS)")
			}
			newValidity, newUID = data.UIDValidity, uint32(nums[0])
			if newValidity == 0 {
				newValidity = dest.UIDValidity
			}
			return nil
		}
		data, err := ic.Move(set, to).Wait()
		if err != nil {
			return err
		}
		moved, ok := data.DestUIDs.(imap.UIDSet)
		if !ok {
			return errors.New("the IMAP server did not report the moved UID (UIDPLUS)")
		}
		nums, ok := moved.Nums()
		if !ok || len(nums) != 1 {
			return errors.New("the IMAP server did not report the moved UID (UIDPLUS)")
		}
		newValidity, newUID = data.UIDValidity, uint32(nums[0])
		if newValidity == 0 {
			newValidity = dest.UIDValidity
		}
		return nil
	})
	return newValidity, newUID, err
}

func (c *Client) ExpungeMessage(address, folder string, validity, uid uint32) error {
	if SharedFolder(folder) {
		return ErrNotFound
	}
	return c.with(address, func(ic *imapclient.Client) error {
		selected, err := ic.Select(folder, nil).Wait()
		if err != nil || selected.UIDValidity != validity {
			return ErrNotFound
		}
		return expungeSelected(ic, uid)
	})
}

func expungeSelected(ic *imapclient.Client, uid uint32) error {
	set := imap.UIDSetNum(imap.UID(uid))
	if err := ic.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
		return err
	}
	return ic.UIDExpunge(set).Close()
}

func (c *Client) FindMessage(address, messageID string) (MessageHit, error) {
	var hit MessageHit
	if messageID == "" {
		return hit, ErrNotFound
	}
	err := c.with(address, func(ic *imapclient.Client) error {
		names, err := c.mailboxNames(ic)
		if err != nil {
			return err
		}
		id := strings.Trim(messageID, "<>")
		for _, folder := range names {
			selected, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
			if err != nil {
				continue
			}
			data, err := ic.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: id}}}, nil).Wait()
			if err != nil || len(data.AllUIDs()) == 0 {
				continue
			}
			hit = MessageHit{Folder: folder, UIDValidity: selected.UIDValidity, UID: uint32(data.AllUIDs()[0])}
			return nil
		}
		return ErrNotFound
	})
	return hit, err
}
