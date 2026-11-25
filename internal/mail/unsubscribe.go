package mail

import (
	"bytes"
	"errors"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
)

const maxNewsletterMessages = 200

type Unsubscribe struct {
	OneClick bool `json:"oneClick"`
	Link     bool `json:"link"`
	Mailto   bool `json:"mailto"`
}

type ListUnsubscribe struct {
	OneClick bool
	HTTPS    []string
	HTTP     []string
	Mailto   []string
}

type Newsletter struct {
	Address   string
	Name      string
	Subject   string
	Count     int
	LatestUID uint32
	Folder    string
	Date      time.Time
	List      ListUnsubscribe
}

func (m *Message) ParsedUnsubscribe() ListUnsubscribe {
	return ParseListUnsubscribe(m.listUnsubscribe, m.listUnsubscribePost)
}

func ParseListUnsubscribe(list, post string) ListUnsubscribe {
	var out ListUnsubscribe
	out.OneClick = oneClickPost(post)
	for _, raw := range angleURIs(list) {
		if len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00 ") {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.User != nil {
			continue
		}
		switch strings.ToLower(u.Scheme) {
		case "https":
			if u.Hostname() == "" {
				continue
			}
			out.HTTPS = append(out.HTTPS, raw)
		case "http":
			if u.Hostname() == "" {
				continue
			}
			out.HTTP = append(out.HTTP, raw)
		case "mailto":
			out.Mailto = append(out.Mailto, raw)
		}
		if len(out.HTTPS)+len(out.HTTP)+len(out.Mailto) >= 8 {
			break
		}
	}
	return out
}

func oneClickPost(v string) bool {
	v = stripComments(v)
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"`)
		if strings.EqualFold(strings.TrimSpace(key), "List-Unsubscribe") && strings.EqualFold(val, "One-Click") {
			return true
		}
	}
	return false
}

func stripComments(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func angleURIs(v string) []string {
	v = stripComments(v)
	if len(v) > 16384 {
		v = v[:16384]
	}
	var out []string
	for len(out) < 8 {
		start := strings.IndexByte(v, '<')
		if start < 0 {
			break
		}
		v = v[start+1:]
		end := strings.IndexByte(v, '>')
		if end < 0 {
			break
		}
		if u := strings.TrimSpace(v[:end]); u != "" {
			out = append(out, u)
		}
		v = v[end+1:]
	}
	return out
}

var errBadMailto = errors.New("invalid mailto unsubscribe")

func ParseMailto(raw string) (to, subject, body string, err error) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "mailto") {
		return "", "", "", errBadMailto
	}
	rest := u.Opaque
	if rest == "" {
		rest = strings.TrimPrefix(u.EscapedPath(), "/")
	}
	query := u.RawQuery
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, query = rest[:i], rest[i+1:]
	}
	addr, err := url.PathUnescape(rest)
	if err != nil {
		return "", "", "", errBadMailto
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return "", "", "", errBadMailto
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = strings.TrimSpace(q.Get("to"))
	}
	if q.Get("cc") != "" || q.Get("bcc") != "" || len(q["to"]) > 1 || strings.ContainsAny(addr, ",;") {
		return "", "", "", errBadMailto
	}
	if len(q["to"]) == 1 && !strings.EqualFold(strings.TrimSpace(q.Get("to")), addr) {
		return "", "", "", errBadMailto
	}
	parsed, err := mail.ParseAddress(addr)
	if err != nil || !strings.EqualFold(parsed.Address, addr) || strings.ContainsAny(parsed.Address, "\r\n ") {
		return "", "", "", errBadMailto
	}
	subject, body = q.Get("subject"), q.Get("body")
	if strings.ContainsAny(subject, "\r\n\x00") || strings.ContainsAny(body, "\r\n\x00") || len(subject) > 200 || len(body) > 5000 {
		return "", "", "", errBadMailto
	}
	if subject == "" {
		subject = "Unsubscribe"
	}
	if body == "" {
		body = "Please unsubscribe this address."
	}
	return strings.ToLower(parsed.Address), subject, body, nil
}

func headerJoined(h message.Header, key string) string {
	var b strings.Builder
	fields := h.FieldsByKey(key)
	for fields.Next() {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fields.Value())
		if b.Len() > 16384 {
			break
		}
	}
	return b.String()
}

func readListHeader(raw []byte) (list, post string) {
	if len(raw) == 0 {
		return "", ""
	}
	e, err := message.Read(bytes.NewReader(append(raw, '\r', '\n')))
	if err != nil {
		return "", ""
	}
	return headerJoined(e.Header, "List-Unsubscribe"), headerJoined(e.Header, "List-Unsubscribe-Post")
}

func (c *Client) ListUnsubscribe(address, folder string, uid uint32) (ListUnsubscribe, error) {
	if SharedFolder(folder) || uid == 0 {
		return ListUnsubscribe{}, ErrNotFound
	}
	var out ListUnsubscribe
	err := c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return ErrNotFound
		}
		section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"List-Unsubscribe", "List-Unsubscribe-Post"}, Peek: true}
		msgs, err := ic.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return ErrNotFound
		}
		list, post := readListHeader(msgs[0].FindBodySection(section))
		out = ParseListUnsubscribe(list, post)
		return nil
	})
	return out, err
}

func (c *Client) Newsletters(address string, usable func(ListUnsubscribe) bool) ([]Newsletter, error) {
	var out []Newsletter
	err := c.with(address, func(ic *imapclient.Client) error {
		if _, err := ic.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return ErrNotFound
		}
		criteria := imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "List-Unsubscribe", Value: "<"}}}
		data, err := ic.UIDSearch(&criteria, nil).Wait()
		if err != nil {
			return err
		}
		if data == nil {
			return nil
		}
		uids := data.AllUIDs()
		if len(uids) == 0 {
			return nil
		}
		slices.Sort(uids)
		if len(uids) > maxNewsletterMessages {
			uids = uids[len(uids)-maxNewsletterMessages:]
		}
		section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"List-Unsubscribe", "List-Unsubscribe-Post"}, Peek: true}
		msgs, err := ic.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
			UID: true, Envelope: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{section},
		}).Collect()
		if err != nil {
			return err
		}
		bySender := map[string]*Newsletter{}
		for _, msg := range msgs {
			if msg.Envelope == nil {
				continue
			}
			from := addresses(msg.Envelope.From)
			if len(from) == 0 || from[0].Address == "" {
				continue
			}
			list, post := readListHeader(msg.FindBodySection(section))
			parsed := ParseListUnsubscribe(list, post)
			if usable != nil && !usable(parsed) {
				continue
			}
			key := strings.ToLower(from[0].Address)
			item := bySender[key]
			if item == nil {
				item = &Newsletter{Address: key, Name: from[0].Name, Folder: "INBOX"}
				bySender[key] = item
			}
			item.Count++
			if item.LatestUID == 0 || msg.InternalDate.After(item.Date) {
				item.Date = msg.InternalDate
				item.LatestUID = uint32(msg.UID)
				item.Subject = msg.Envelope.Subject
				if from[0].Name != "" {
					item.Name = from[0].Name
				}
				item.List = parsed
			}
		}
		out = make([]Newsletter, 0, len(bySender))
		for _, item := range bySender {
			out = append(out, *item)
		}
		slices.SortFunc(out, func(a, b Newsletter) int { return b.Date.Compare(a.Date) })
		return nil
	})
	return out, err
}
