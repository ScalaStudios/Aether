package mailflow

import (
	"html"
	"regexp"
	"slices"
	"strings"
)

type Message struct {
	Sender        string
	Authenticated bool
	Recipients    []string
	Header        []Field
	Body          []byte
	Size          int
	SpamScore     float64

	parsed      bool
	texts       []part
	attachments []part
}

type Directory struct {
	Domains map[string]string
	Lists   map[string][]string
}

type Match struct {
	OrgID     string
	Rule      *Rule
	Direction string
	Test      bool
}

type Redirect struct {
	From []string
	To   []string
}

type Outcome struct {
	Matches       []Match
	Reject        string
	Disclaimers   []Disclaimer
	Bcc           []string
	Redirects     []Redirect
	AddHeaders    []HeaderValue
	RemoveHeaders []string
	SpamScore     float64
	RequireTLS    bool
	Approvers     []string
	Stopped       []string
}

func domainOf(address string) string {
	_, d, _ := strings.Cut(address, "@")
	return d
}

func (m *Message) parse() {
	if m.parsed {
		return
	}
	m.parsed = true
	walk(m.Header, crlf(m.Body), 0, func(p part) {
		if p.attachment {
			m.attachments = append(m.attachments, p)
		} else if p.text != "" {
			m.texts = append(m.texts, p)
		}
	})
}

func (m *Message) Orgs(dir *Directory) []string {
	var out []string
	if org := dir.Domains[domainOf(m.Sender)]; org != "" && m.Authenticated {
		out = append(out, org)
	}
	for _, r := range m.Recipients {
		if org := dir.Domains[domainOf(r)]; org != "" && !slices.Contains(out, org) {
			out = append(out, org)
		}
	}
	return out
}

func (m *Message) senderInside(org string, dir *Directory) bool {
	return m.Authenticated && dir.Domains[domainOf(m.Sender)] == org
}

func (m *Message) Direction(org string, dir *Directory) string {
	if !m.senderInside(org, dir) {
		return "incoming"
	}
	for _, r := range m.Recipients {
		if dir.Domains[domainOf(r)] != org {
			return "outgoing"
		}
	}
	return "internal"
}

func containsAny(haystack string, needles []string) bool {
	haystack = strings.ToLower(haystack)
	for _, n := range needles {
		if strings.Contains(haystack, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

func (c *Condition) address(addr string, inside bool, dir *Directory) bool {
	switch c.Scope {
	case "address":
		return slices.Contains(c.Values, addr)
	case "domain":
		return slices.Contains(c.Values, domainOf(addr))
	case "inside":
		return inside
	case "outside":
		return !inside
	case "memberOf":
		for _, list := range c.Values {
			if addr == list || slices.Contains(dir.Lists[list], addr) {
				return true
			}
		}
	}
	return false
}

func (c *Condition) matches(m *Message, org, direction string, dir *Directory) bool {
	switch c.Field {
	case "sender":
		return c.address(m.Sender, m.senderInside(org, dir), dir)
	case "recipient":
		for _, r := range m.Recipients {
			if c.address(r, dir.Domains[domainOf(r)] == org, dir) {
				return true
			}
		}
		return false
	case "direction":
		return slices.Contains(c.Values, direction)
	case "sizeOver":
		return float64(m.Size) > c.Number*1024
	case "spamScoreOver":
		return m.SpamScore > c.Number
	case "subject":
		return containsAny(DecodeHeader(get(m.Header, "Subject")), c.Values)
	case "header":
		for _, f := range m.Header {
			if strings.EqualFold(f.Name, c.Header) && containsAny(DecodeHeader(f.Value), c.Values) {
				return true
			}
		}
		return false
	}
	m.parse()
	switch c.Field {
	case "body":
		return slices.ContainsFunc(m.texts, func(p part) bool { return containsAny(p.text, c.Values) })
	case "attachmentName":
		return slices.ContainsFunc(m.attachments, func(p part) bool { return containsAny(p.filename, c.Values) })
	case "attachmentType":
		return slices.ContainsFunc(m.attachments, func(p part) bool { return containsAny(p.contentType, c.Values) })
	}
	return false
}

func (r *Rule) applies(m *Message, org, direction string, dir *Directory) bool {
	for i := range r.Conditions {
		if !r.Conditions[i].matches(m, org, direction, dir) {
			return false
		}
	}
	for i := range r.Exceptions {
		if r.Exceptions[i].matches(m, org, direction, dir) {
			return false
		}
	}
	return true
}

func Evaluate(m *Message, rules map[string][]Rule, dir *Directory, afterSpam bool, stopped []string) Outcome {
	out := Outcome{Stopped: stopped}
	m.Sender = strings.ToLower(m.Sender)
	for i, r := range m.Recipients {
		m.Recipients[i] = strings.ToLower(r)
	}
	for _, org := range m.Orgs(dir) {
		direction := m.Direction(org, dir)
		for i := range rules[org] {
			if out.Reject != "" {
				return out
			}
			if slices.Contains(out.Stopped, org) {
				break
			}
			r := &rules[org][i]
			if r.Mode == "off" || r.SpamDependent() != afterSpam || !r.applies(m, org, direction, dir) {
				continue
			}
			test := r.Mode == "test"
			out.Matches = append(out.Matches, Match{OrgID: org, Rule: r, Direction: direction, Test: test})
			if test {
				continue
			}
			out.apply(m, org, direction, dir, r)
		}
	}
	return out
}

func (o *Outcome) apply(m *Message, org, direction string, dir *Directory, r *Rule) {
	a := r.Actions
	if a.Reject != "" {
		o.Reject = a.Reject
		return
	}
	if a.Disclaimer != nil {
		o.Disclaimers = append(o.Disclaimers, *a.Disclaimer)
	}
	for _, b := range a.Bcc {
		if !slices.Contains(o.Bcc, b) {
			o.Bcc = append(o.Bcc, b)
		}
	}
	if len(a.RedirectTo) > 0 {
		var from []string
		for _, rcpt := range m.Recipients {
			if direction != "incoming" || dir.Domains[domainOf(rcpt)] == org {
				from = append(from, rcpt)
			}
		}
		o.Redirects = append(o.Redirects, Redirect{From: from, To: a.RedirectTo})
	}
	o.AddHeaders = append(o.AddHeaders, a.AddHeaders...)
	o.RemoveHeaders = append(o.RemoveHeaders, a.RemoveHeaders...)
	if a.SpamScore != nil {
		o.SpamScore += *a.SpamScore
	}
	o.RequireTLS = o.RequireTLS || a.RequireTLS
	for _, ap := range a.Approvers {
		if !slices.Contains(o.Approvers, ap) {
			o.Approvers = append(o.Approvers, ap)
		}
	}
	if r.Stop {
		o.Stopped = append(o.Stopped, org)
	}
}

var (
	tags   = regexp.MustCompile(`(?s)<(style|script)[^>]*>.*?</(style|script)>|<[^>]*>`)
	spaces = regexp.MustCompile(`[ \t]*\r?\n(\s*\r?\n)+`)
)

func Preview(m *Message, limit int) string {
	m.parse()
	var text string
	for _, p := range m.texts {
		if p.contentType == "text/plain" {
			text = p.text
			break
		}
		if text == "" {
			text = html.UnescapeString(tags.ReplaceAllString(p.text, " "))
		}
	}
	text = strings.TrimSpace(spaces.ReplaceAllString(text, "\n\n"))
	if r := []rune(text); len(r) > limit {
		text = string(r[:limit]) + "…"
	}
	return text
}

func (o *Outcome) Recipients(original []string) []string {
	out := slices.Clone(original)
	for _, rd := range o.Redirects {
		out = slices.DeleteFunc(out, func(a string) bool { return slices.Contains(rd.From, a) })
		for _, to := range rd.To {
			if !slices.Contains(out, to) {
				out = append(out, to)
			}
		}
	}
	for _, b := range o.Bcc {
		if !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	return out
}

func (a Actions) Summary() []string {
	var out []string
	add := func(ok bool, s string) {
		if ok {
			out = append(out, s)
		}
	}
	add(a.Reject != "", "reject")
	add(a.Disclaimer != nil, "disclaimer")
	add(len(a.Bcc) > 0, "bcc")
	add(len(a.RedirectTo) > 0, "redirect")
	add(len(a.AddHeaders) > 0, "add header")
	add(len(a.RemoveHeaders) > 0, "remove header")
	add(a.SpamScore != nil, "spam score")
	add(a.RequireTLS, "require tls")
	add(len(a.Approvers) > 0, "approval")
	return out
}
