package mail

import (
	"bytes"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
)

type CalendarMessage struct {
	UID        uint32
	From       string
	ReturnPath string
	Calendar   string
}

func (c *Client) UnseenCalendars(address string, limit int) ([]CalendarMessage, error) {
	var out []CalendarMessage
	err := c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return err
		}
		found, err := ic.UIDSearch(&imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}, nil).Wait()
		if err != nil {
			return err
		}
		uids := found.AllUIDs()
		if len(uids) == 0 {
			return nil
		}
		uids = uids[:min(len(uids), limit)]
		header := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Return-Path"}, Peek: true}
		msgs, err := ic.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
			UID: true, Envelope: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true}, BodySection: []*imap.FetchItemBodySection{header},
		}).Collect()
		if err != nil {
			return err
		}
		for _, m := range msgs {
			cm := CalendarMessage{UID: uint32(m.UID)}
			if m.Envelope != nil && len(m.Envelope.From) > 0 {
				cm.From = strings.ToLower(m.Envelope.From[0].Addr())
			}
			if raw := m.FindBodySection(header); raw != nil {
				if e, err := message.Read(bytes.NewReader(append(raw, '\r', '\n'))); err == nil {
					cm.ReturnPath = strings.ToLower(strings.Trim(strings.TrimSpace(e.Header.Get("Return-Path")), "<>"))
				}
			}
			var part *imap.BodyStructureSinglePart
			var path []int
			if m.BodyStructure != nil {
				m.BodyStructure.Walk(func(p []int, b imap.BodyStructure) bool {
					if single, ok := b.(*imap.BodyStructureSinglePart); ok && part == nil && single.MediaType() == "text/calendar" && single.Size <= maxInlineBody {
						part, path = single, slices.Clone(p)
					}
					return part == nil
				})
			}
			if part != nil {
				body, err := fetchPart(ic, imap.UIDSetNum(m.UID), path)
				if err != nil {
					return err
				}
				cm.Calendar = decodeCharset(body, part.Params["charset"])
			}
			out = append(out, cm)
		}
		return nil
	})
	return out, err
}
