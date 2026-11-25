package maillog

import (
	"regexp"
	"strings"
	"time"
)

const (
	Delivered = "message.delivered"
	Deferred  = "message.deferred"
	Bounced   = "message.bounced"
	Received  = "message.received"
	Rejected  = "message.rejected"
)

type Event struct {
	Type              string
	Time              time.Time
	QueueID           string
	MessageID         string
	Sender            string
	Recipient         string
	OriginalRecipient string
	Relay             string
	DSN               string
	Reply             string
	Line              string
}

type message struct {
	Sender, MessageID string
	Authenticated     bool
	Deferred          map[string]bool
	Seen              time.Time
}

type Parser struct {
	Queue map[string]*message
	lines int
}

var (
	headRe      = regexp.MustCompile(`^(.*?) \S+ (postfix[^\[\s]*)\[\d+\]: (.*)$`)
	queueRe     = regexp.MustCompile(`^([0-9A-Z][0-9A-Za-z]{5,}): (.*)$`)
	rejectRe    = regexp.MustCompile(`^(?:milter-)?reject: [A-Z-]+ from \S+: (.*); from=<([^>]*)> to=<([^>]*)>`)
	clientRe    = regexp.MustCompile(`^client=\S+(?:, sasl_method=\S+, sasl_username=\S+)?`)
	messageIDRe = regexp.MustCompile(`^message-id=<?([^<>\s]*)>?`)
	fromRe      = regexp.MustCompile(`^from=<([^>]*)>, (?:size=|status=expired)`)
	statusRe    = regexp.MustCompile(`^to=<([^>]*)>, (?:orig_to=<([^>]*)>, )?relay=([^,]+), .*?dsn=([0-9.]+), status=(\w+)(?: \((.*)\))?$`)
)

func NewParser() *Parser {
	return &Parser{Queue: map[string]*message{}}
}

func parseTime(stamp string, now time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		return t
	}
	t, err := time.ParseInLocation(time.Stamp, stamp, time.Local)
	if err != nil {
		return now
	}
	t = t.AddDate(now.Year(), 0, 0)
	if t.After(now.Add(24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t
}

func (p *Parser) Parse(line string) []Event {
	now := time.Now()
	if p.lines++; p.lines%10000 == 0 {
		for id, m := range p.Queue {
			if now.Sub(m.Seen) > 7*24*time.Hour {
				delete(p.Queue, id)
			}
		}
	}
	head := headRe.FindStringSubmatch(line)
	if head == nil {
		return nil
	}
	program, body := head[2], head[3]
	at := parseTime(head[1], now)
	inbound := program == "postfix/smtpd" || program == "postfix/cleanup"
	if rest, ok := strings.CutPrefix(body, "NOQUEUE: "); ok {
		if r := rejectRe.FindStringSubmatch(rest); r != nil && inbound {
			return []Event{{Type: Rejected, Time: at, Sender: strings.ToLower(r[2]), Recipient: strings.ToLower(r[3]), Reply: r[1], Line: line}}
		}
		return nil
	}
	q := queueRe.FindStringSubmatch(body)
	if q == nil {
		return nil
	}
	id, rest := q[1], q[2]
	m := p.Queue[id]
	if m == nil {
		m = &message{Deferred: map[string]bool{}}
	}
	m.Seen = now
	switch {
	case rest == "removed":
		delete(p.Queue, id)
		return nil
	case clientRe.MatchString(rest):
		m.Authenticated = (program == "postfix/submission/smtpd" || program == "postfix/submissions/smtpd") && strings.Contains(rest, ", sasl_username=")
	case messageIDRe.MatchString(rest):
		m.MessageID = messageIDRe.FindStringSubmatch(rest)[1]
	case rejectRe.MatchString(rest):
		r := rejectRe.FindStringSubmatch(rest)
		if !inbound || m.Authenticated {
			return nil
		}
		return []Event{{Type: Rejected, Time: at, QueueID: id, MessageID: m.MessageID, Sender: strings.ToLower(r[2]), Recipient: strings.ToLower(r[3]), Reply: r[1], Line: line}}
	case fromRe.MatchString(rest):
		m.Sender = strings.ToLower(fromRe.FindStringSubmatch(rest)[1])
		if !strings.Contains(rest, "status=expired") {
			break
		}
		var out []Event
		if m.Authenticated {
			for rcpt := range m.Deferred {
				out = append(out, Event{Type: Bounced, Time: at, QueueID: id, MessageID: m.MessageID, Sender: m.Sender, Recipient: rcpt, DSN: "4.4.7", Reply: "message expired after being deferred", Line: line + " " + rcpt})
			}
		}
		clear(m.Deferred)
		p.Queue[id] = m
		return out
	case statusRe.MatchString(rest):
		s := statusRe.FindStringSubmatch(rest)
		p.Queue[id] = m
		return m.status(id, program, at, line, s)
	}
	p.Queue[id] = m
	return nil
}

func (m *message) status(id, program string, at time.Time, line string, s []string) []Event {
	rcpt := strings.ToLower(s[1])
	base := Event{Time: at, QueueID: id, MessageID: m.MessageID, Sender: m.Sender, Recipient: rcpt, OriginalRecipient: strings.ToLower(s[2]), Relay: s[3], DSN: s[4], Reply: s[6], Line: line}
	var out []Event
	outgoing := map[string]string{"sent": Delivered, "deferred": Deferred, "bounced": Bounced}[s[5]]
	if s[5] == "deferred" {
		m.Deferred[rcpt] = true
	} else {
		delete(m.Deferred, rcpt)
	}
	if m.Authenticated && outgoing != "" {
		e := base
		e.Type = outgoing
		out = append(out, e)
	}
	if s[5] == "sent" && strings.HasSuffix(program, "/lmtp") {
		e := base
		e.Type = Received
		out = append(out, e)
	}
	return out
}
