package mail

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func eachChunk(uids []uint32, n int, fn func([]uint32) error) error {
	for len(uids) > 0 {
		end := min(n, len(uids))
		if err := fn(uids[:end]); err != nil {
			return err
		}
		uids = uids[end:]
	}
	return nil
}

func storeKeywordFlags(ic *imapclient.Client, uids []uint32, add bool, flags []string) error {
	if len(uids) == 0 || len(flags) == 0 {
		return nil
	}
	op := imap.StoreFlagsDel
	if add {
		op = imap.StoreFlagsAdd
	}
	list := make([]imap.Flag, len(flags))
	for i, flag := range flags {
		list[i] = imap.Flag(flag)
	}
	return ic.Store(uidSet(uids), &imap.StoreFlags{Op: op, Flags: list, Silent: true}, nil).Close()
}

func applyKeywordEdits(ic *imapclient.Client, uids []uint32, diff func([]string) ([]string, []string)) error {
	type edit struct {
		add, remove []string
		uids        []uint32
	}
	groups := map[string]*edit{}
	saw := false
	err := eachChunk(uids, 200, func(part []uint32) error {
		msgs, err := ic.Fetch(uidSet(part), &imap.FetchOptions{UID: true, Flags: true}).Collect()
		if err != nil {
			return err
		}
		if len(msgs) > 0 {
			saw = true
		}
		for _, msg := range msgs {
			add, remove := diff(flagStrings(msg.Flags))
			if len(add) == 0 && len(remove) == 0 {
				continue
			}
			key := strings.Join(add, "\x1e") + "\x1f" + strings.Join(remove, "\x1e")
			group := groups[key]
			if group == nil {
				group = &edit{add: add, remove: remove}
				groups[key] = group
			}
			group.uids = append(group.uids, uint32(msg.UID))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !saw {
		return ErrNotFound
	}
	for _, group := range groups {
		if err := storeKeywordFlags(ic, group.uids, true, group.add); err != nil {
			return err
		}
		if err := storeKeywordFlags(ic, group.uids, false, group.remove); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) AdjustKeywords(address, folder string, uids []uint32, diff func([]string) ([]string, []string)) error {
	if len(uids) == 0 || SharedFolder(folder) {
		return ErrNotFound
	}
	return c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, nil).Wait(); err != nil {
			return ErrNotFound
		}
		return applyKeywordEdits(ic, uids, diff)
	})
}

func (c *Client) AdjustKeyword(address string, folders []string, keyword string, diff func([]string) ([]string, []string)) error {
	if keyword == "" || !ValidKeyword(keyword) {
		return nil
	}
	return c.with(address, func(ic *imapclient.Client) error {
		for _, folder := range folders {
			if SharedFolder(folder) {
				continue
			}
			if _, err := ic.Select(folder, nil).Wait(); err != nil {
				return ErrNotFound
			}
			uids, err := sortUIDs(ic, &imap.SearchCriteria{Flag: []imap.Flag{imap.Flag(keyword)}}, imapclient.SortKeyArrival)
			if err != nil {
				return err
			}
			if len(uids) == 0 {
				continue
			}
			if err := applyKeywordEdits(ic, uids, diff); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		return nil
	})
}

type mailHit struct {
	summary Summary
	arrival time.Time
	sent    time.Time
	from    string
	to      string
	subject string
	size    int64
}

type acrossCursor struct {
	O int    `json:"o"`
	F string `json:"f"`
	U uint32 `json:"u"`
}

func displayAddress(addresses []Address) string {
	if len(addresses) == 0 {
		return ""
	}
	if addresses[0].Name != "" {
		return addresses[0].Name
	}
	return addresses[0].Address
}

func compareHits(key string, a, b mailHit) int {
	var d int
	switch key {
	case "sent":
		d = a.sent.Compare(b.sent)
	case "from":
		d = strings.Compare(strings.ToLower(a.from), strings.ToLower(b.from))
	case "to":
		d = strings.Compare(strings.ToLower(a.to), strings.ToLower(b.to))
	case "subject":
		d = strings.Compare(strings.ToLower(a.subject), strings.ToLower(b.subject))
	case "size":
		d = cmp.Compare(a.size, b.size)
	default:
		d = a.arrival.Compare(b.arrival)
	}
	if d != 0 {
		return d
	}
	if d = strings.Compare(a.summary.Folder, b.summary.Folder); d != 0 {
		return d
	}
	return cmp.Compare(a.summary.UID, b.summary.UID)
}

func acrossStart(hits []mailHit, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrInvalidCursor
	}
	var decoded acrossCursor
	if json.Unmarshal(raw, &decoded) != nil || decoded.O < 1 || decoded.F == "" || decoded.U == 0 {
		return 0, ErrInvalidCursor
	}
	for i, hit := range hits {
		if hit.summary.Folder == decoded.F && hit.summary.UID == decoded.U {
			return i + 1, nil
		}
	}
	return min(decoded.O-1, len(hits)), nil
}

func acrossCursorAt(offset int, hit mailHit) *string {
	raw, _ := json.Marshal(acrossCursor{O: offset, F: hit.summary.Folder, U: hit.summary.UID})
	cursor := base64.RawURLEncoding.EncodeToString(raw)
	return &cursor
}

func attachmentFlags(ic *imapclient.Client, uids []uint32) (map[uint32]bool, error) {
	out := map[uint32]bool{}
	err := eachChunk(uids, 200, func(part []uint32) error {
		msgs, err := ic.Fetch(uidSet(part), &imap.FetchOptions{UID: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true}}).Collect()
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			out[uint32(msg.UID)] = hasAttachments(msg.BodyStructure)
		}
		return nil
	})
	return out, err
}

func (c *Client) ListAcross(address string, folders []string, o ListOptions) (*Page, error) {
	if len(folders) == 1 {
		return c.List(address, folders[0], o)
	}
	page := &Page{Messages: []Summary{}}
	if len(folders) == 0 {
		return page, nil
	}
	if o.Limit < 1 {
		o.Limit = 50
	}
	err := c.with(address, func(ic *imapclient.Client) error {
		var hits []mailHit
		for _, folder := range folders {
			if SharedFolder(folder) {
				continue
			}
			if _, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
				return ErrNotFound
			}
			sorted, err := sortUIDs(ic, o.criteria(), cmp.Or(SortKeys[o.Sort], imapclient.SortKeyArrival))
			if err != nil {
				return err
			}
			if o.HasAttachments {
				if sorted, err = withAttachments(ic, sorted); err != nil {
					return err
				}
			}
			if len(sorted) == 0 {
				continue
			}
			if err := eachChunk(sorted, 200, func(part []uint32) error {
				msgs, err := ic.Fetch(uidSet(part), &imap.FetchOptions{UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true}).Collect()
				if err != nil {
					return err
				}
				for _, msg := range msgs {
					summary := summarize(msg)
					summary.Folder = folder
					if o.HasAttachments {
						summary.HasAttachments = true
					}
					sent := msg.InternalDate
					if msg.Envelope != nil && !msg.Envelope.Date.IsZero() {
						sent = msg.Envelope.Date
					}
					hits = append(hits, mailHit{
						summary: summary, arrival: msg.InternalDate, sent: sent,
						from: displayAddress(summary.From), to: displayAddress(summary.To), subject: summary.Subject, size: summary.Size,
					})
				}
				return nil
			}); err != nil {
				return err
			}
		}
		slices.SortFunc(hits, func(a, b mailHit) int {
			d := compareHits(o.Sort, a, b)
			if o.Desc {
				return -d
			}
			return d
		})
		page.Total = len(hits)
		start, err := acrossStart(hits, o.Cursor)
		if err != nil {
			return err
		}
		hits = hits[start:]
		if len(hits) > o.Limit {
			page.NextCursor = acrossCursorAt(start+o.Limit, hits[o.Limit-1])
			hits = hits[:o.Limit]
		}
		if !o.HasAttachments && len(hits) > 0 {
			byFolder := map[string][]uint32{}
			for _, hit := range hits {
				byFolder[hit.summary.Folder] = append(byFolder[hit.summary.Folder], hit.summary.UID)
			}
			has := map[string]map[uint32]bool{}
			for folder, uids := range byFolder {
				if _, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
					return ErrNotFound
				}
				flags, err := attachmentFlags(ic, uids)
				if err != nil {
					return err
				}
				has[folder] = flags
			}
			for i := range hits {
				hits[i].summary.HasAttachments = has[hits[i].summary.Folder][hits[i].summary.UID]
			}
		}
		for _, hit := range hits {
			page.Messages = append(page.Messages, hit.summary)
		}
		return nil
	})
	return page, err
}
