package mail

import (
	"bytes"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const AuthenticationHeader = "X-Aether-Authentication"

type AuthResult struct {
	Result string `json:"result"`
	Domain string `json:"domain,omitempty"`
}

type Authentication struct {
	Submission string     `json:"submission"`
	SPF        AuthResult `json:"spf"`
	DKIM       AuthResult `json:"dkim"`
	DMARC      AuthResult `json:"dmarc"`
}

var authValues = map[string][]string{
	"auth":  {"pass", "relay", "none"},
	"spf":   {"pass", "fail", "softfail", "neutral", "none", "temperror", "permerror"},
	"dkim":  {"pass", "fail", "none", "temperror", "permerror"},
	"dmarc": {"pass", "fail", "none", "temperror", "permerror"},
}

func ParseAuthentication(values []string) *Authentication {
	if len(values) != 1 {
		return nil
	}
	a := &Authentication{}
	fields := map[string]*string{
		"auth": &a.Submission, "spf": &a.SPF.Result, "dkim": &a.DKIM.Result, "dmarc": &a.DMARC.Result,
		"spf.domain": &a.SPF.Domain, "dkim.domain": &a.DKIM.Domain,
	}
	for _, token := range strings.Fields(values[0]) {
		key, value, ok := strings.Cut(token, "=")
		dst := fields[key]
		switch {
		case !ok || dst == nil || *dst != "":
			return nil
		case authValues[key] != nil && !slices.Contains(authValues[key], value):
			return nil
		case authValues[key] == nil && !validDomain(value):
			return nil
		}
		*dst = value
	}
	if a.Submission == "" || a.SPF.Result == "" || a.DKIM.Result == "" || a.DMARC.Result == "" {
		return nil
	}
	return a
}

func validDomain(v string) bool {
	if v == "" || len(v) > 253 || v[0] == '.' || v[0] == '-' {
		return false
	}
	for _, r := range v {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

func WithoutHeader(raw []byte, name string) []byte {
	out := make([]byte, 0, len(raw))
	skipping := false
	for rest := raw; len(rest) > 0; {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line = rest[:i+1]
		}
		rest = rest[len(line):]
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			return append(append(out, line...), rest...)
		}
		if skipping && (line[0] == ' ' || line[0] == '\t') {
			continue
		}
		field, _, ok := bytes.Cut(line, []byte(":"))
		skipping = ok && strings.EqualFold(strings.TrimRight(string(field), " \t"), name)
		if !skipping {
			out = append(out, line...)
		}
	}
	return out
}

const knownSenderSample = 20

func (c *Client) KnownSender(address, folder string, uid uint32, sender string) (bool, error) {
	if SharedFolder(folder) {
		return false, ErrNotFound
	}
	type search struct {
		folder string
		fields []string
	}
	known := false
	err := c.with(address, func(ic *imapclient.Client) error {
		list, err := ic.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
		if err != nil {
			return err
		}
		searches := []search{{folder, []string{"From"}}, {"INBOX", []string{"From"}}}
		for _, l := range list {
			switch use := specialUse(l); {
			case SharedFolder(l.Mailbox):
			case use == "archive":
				searches = append(searches, search{l.Mailbox, []string{"From"}})
			case use == "sent":
				searches = append(searches, search{l.Mailbox, []string{"To", "Cc"}})
			}
		}
		done := map[string]bool{}
		for _, s := range searches {
			if done[s.folder] {
				continue
			}
			done[s.folder] = true
			if _, err := ic.Select(s.folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
				continue
			}
			var any []imap.SearchCriteria
			for _, f := range s.fields {
				any = append(any, imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: f, Value: sender}}})
			}
			criteria := anyOf(any)
			data, err := ic.UIDSearch(&criteria, nil).Wait()
			if err != nil {
				return err
			}
			uids := slices.DeleteFunc(data.AllUIDs(), func(u imap.UID) bool { return s.folder == folder && uint32(u) == uid })
			if len(uids) == 0 {
				continue
			}
			msgs, err := ic.Fetch(imap.UIDSetNum(uids[max(0, len(uids)-knownSenderSample):]...), &imap.FetchOptions{UID: true, Envelope: true}).Collect()
			if err != nil {
				return err
			}
			for _, m := range msgs {
				if m.Envelope == nil {
					continue
				}
				candidates := m.Envelope.From
				if s.fields[0] == "To" {
					candidates = append(slices.Clone(m.Envelope.To), m.Envelope.Cc...)
				}
				if slices.ContainsFunc(addresses(candidates), func(a Address) bool { return strings.EqualFold(a.Address, sender) }) {
					known = true
					return nil
				}
			}
		}
		return nil
	})
	return known, err
}
