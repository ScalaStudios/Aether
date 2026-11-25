package mailtrace

import (
	"mime"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
)

type kind int

const (
	kindConnect kind = iota + 1
	kindDisconnect
	kindInboundTLS
	kindOutboundTLS
	kindClient
	kindPickup
	kindMessageID
	kindHeader
	kindQueued
	kindExpired
	kindRemoved
	kindDelivery
	kindAction
	kindNotified
	kindAdmin
)

type record struct {
	at            time.Time
	process       string
	service       string
	daemon        string
	queueID       string
	kind          kind
	client        string
	ip            string
	tls           string
	sasl          string
	sender        string
	recipient     string
	origRecipient string
	relay         string
	dsn           string
	status        string
	stage         string
	detail        string
	header        string
	value         string
	size          int64
	nrcpt         int
}

const idChars = `0-9B-DF-HJ-NP-TV-Zb-df-hj-np-tv-y`

var (
	linePattern      = regexp.MustCompile(`^([A-Z][a-z]{2} [ \d]\d \d\d:\d\d:\d\d) \S+ (postfix[^\[\s]*)\[(\d+)\]: (.*)$`)
	queueIDPattern   = regexp.MustCompile(`^([0-9A-F]{6,}|[` + idChars + `]{10,}z[` + idChars + `]+): (.*)$`)
	peerPattern      = regexp.MustCompile(`^([^\[]*)\[([^\]]*)\]`)
	connectPattern   = regexp.MustCompile(`^connect from (.*)$`)
	inboundTLS       = regexp.MustCompile(`^\S+ TLS connection established from \S+: (TLSv[\d.]+) with cipher (\S+)`)
	outboundTLS      = regexp.MustCompile(`^\S+ TLS connection established to (\S+): (TLSv[\d.]+) with cipher (\S+)`)
	clientPattern    = regexp.MustCompile(`^client=(\S+?)(?:, |$)`)
	saslPattern      = regexp.MustCompile(`sasl_username=([^,\s]+)`)
	pickupPattern    = regexp.MustCompile(`^uid=\d+ from=<([^>]*)>`)
	messageIDPattern = regexp.MustCompile(`^message-id=<?([^>]*)>?$`)
	headerPattern    = regexp.MustCompile(`^(info|milter-header-info): header ([\w-]+):\s?(.*) from \S+;(?: from=<[^>]*>)?(?: to=<([^>]*)>)?(?: proto=\S+)?(?: helo=<[^>]*>)?(?::.*)?$`)
	queuedPattern    = regexp.MustCompile(`^from=<([^>]*)>, size=(\d+), nrcpt=(\d+)`)
	expiredPattern   = regexp.MustCompile(`^from=<([^>]*)>, status=expired`)
	deliveryPattern  = regexp.MustCompile(`^to=<([^>]*)>, (?:orig_to=<([^>]*)>, )?relay=([^,]+), .*?dsn=(\d\.\d{1,3}\.\d{1,3}), status=(\w+)(?: \((.*)\))?$`)
	actionPattern    = regexp.MustCompile(`^(?:milter-)?(reject|discard|hold): (.*?); from=<([^>]*)>(?: to=<([^>]*)>)?(?: proto=\S+)?(?: helo=<[^>]*>)?(?:: (.*))?$`)
	stagePattern     = regexp.MustCompile(`^(\S+) from (\S+?)(?::\d+)?: (.*)$`)
	dsnPattern       = regexp.MustCompile(`\b([245]\.\d{1,3}\.\d{1,3})\b`)
	notifiedPattern  = regexp.MustCompile(`^sender non-delivery notification: (\S+)$`)
	spamPattern      = regexp.MustCompile(`^(Yes|No), score=(-?\d+(?:\.\d+)?)`)
	foldedWords      = regexp.MustCompile(`\?=\?[ ?]=\?`)
)

var wordDecoder = mime.WordDecoder{CharsetReader: message.CharsetReader}

func parse(line string, now time.Time) (record, bool) {
	m := linePattern.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if m == nil {
		return record{}, false
	}
	stamp, err := time.ParseInLocation("Jan _2 15:04:05", m[1], time.UTC)
	if err != nil {
		return record{}, false
	}
	at := time.Date(now.Year(), stamp.Month(), stamp.Day(), stamp.Hour(), stamp.Minute(), stamp.Second(), 0, time.UTC)
	if at.After(now.Add(24 * time.Hour)) {
		at = at.AddDate(-1, 0, 0)
	}
	program := m[2]
	daemon := program[strings.LastIndexByte(program, '/')+1:]
	service := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(program, daemon), "postfix"), "/")
	if service == "" {
		service = daemon
	}
	r := record{at: at, process: program + "[" + m[3] + "]", service: service, daemon: daemon}
	text := m[4]
	if q := queueIDPattern.FindStringSubmatch(text); q != nil {
		r.queueID, text = q[1], q[2]
		return r, r.parseQueued(text)
	}
	return r, r.parseSession(text)
}

func (r *record) parseSession(text string) bool {
	switch {
	case r.daemon == "smtpd" && strings.HasPrefix(text, "disconnect from "):
		r.kind = kindDisconnect
	case r.daemon == "smtpd" && connectPattern.MatchString(text):
		r.kind = kindConnect
		r.client, r.ip = peer(connectPattern.FindStringSubmatch(text)[1])
	case r.daemon == "smtpd" && inboundTLS.MatchString(text):
		t := inboundTLS.FindStringSubmatch(text)
		r.kind, r.tls = kindInboundTLS, t[1]+" "+t[2]
	case outboundTLS.MatchString(text):
		t := outboundTLS.FindStringSubmatch(text)
		r.kind, r.relay, r.tls = kindOutboundTLS, t[1], t[2]+" "+t[3]
	case strings.HasPrefix(text, "NOQUEUE: "):
		return r.parseAction(strings.TrimPrefix(text, "NOQUEUE: "))
	default:
		return false
	}
	return true
}

func (r *record) parseQueued(text string) bool {
	switch {
	case r.daemon == "smtpd" && clientPattern.MatchString(text):
		r.kind = kindClient
		r.client, r.ip = peer(clientPattern.FindStringSubmatch(text)[1])
		if s := saslPattern.FindStringSubmatch(text); s != nil {
			r.sasl = s[1]
		}
	case r.daemon == "pickup" && pickupPattern.MatchString(text):
		r.kind, r.sender = kindPickup, address(pickupPattern.FindStringSubmatch(text)[1])
	case r.daemon == "cleanup" && messageIDPattern.MatchString(text):
		r.kind, r.value = kindMessageID, strings.TrimSpace(messageIDPattern.FindStringSubmatch(text)[1])
	case r.daemon == "cleanup" && headerPattern.MatchString(text):
		h := headerPattern.FindStringSubmatch(text)
		r.kind, r.stage, r.header, r.value, r.recipient = kindHeader, h[1], strings.ToLower(h[2]), h[3], address(h[4])
	case r.daemon == "qmgr" && queuedPattern.MatchString(text):
		q := queuedPattern.FindStringSubmatch(text)
		r.kind, r.sender = kindQueued, address(q[1])
		r.size, _ = strconv.ParseInt(q[2], 10, 64)
		r.nrcpt, _ = strconv.Atoi(q[3])
	case r.daemon == "qmgr" && expiredPattern.MatchString(text):
		r.kind = kindExpired
	case r.daemon == "qmgr" && text == "removed":
		r.kind = kindRemoved
	case r.daemon == "postsuper":
		r.kind = kindAdmin
		switch text {
		case "removed":
			r.status = "deleted"
		case "placed on hold":
			r.status = "held"
		case "released from hold":
			r.status = "released"
		default:
			return false
		}
	case r.daemon == "bounce" && notifiedPattern.MatchString(text):
		r.kind, r.detail = kindNotified, notifiedPattern.FindStringSubmatch(text)[1]
	case deliveryPattern.MatchString(text):
		d := deliveryPattern.FindStringSubmatch(text)
		r.kind = kindDelivery
		r.recipient, r.origRecipient, r.relay, r.dsn, r.status, r.detail = address(d[1]), address(d[2]), d[3], d[4], d[5], d[6]
		if r.origRecipient == r.recipient {
			r.origRecipient = ""
		}
	default:
		return r.parseAction(text)
	}
	return true
}

func (r *record) parseAction(text string) bool {
	a := actionPattern.FindStringSubmatch(text)
	if a == nil {
		return false
	}
	r.kind, r.status, r.sender, r.recipient = kindAction, a[1], address(a[3]), address(a[4])
	r.detail = a[2]
	if s := stagePattern.FindStringSubmatch(a[2]); s != nil && !strings.Contains(s[1], ":") {
		r.stage, r.detail = s[1], s[3]
		r.client, r.ip = peer(s[2])
	}
	if a[5] != "" {
		r.detail = a[5]
	}
	if d := dsnPattern.FindStringSubmatch(r.detail); d != nil {
		r.dsn = d[1]
	}
	return true
}

func peer(s string) (string, string) {
	if p := peerPattern.FindStringSubmatch(s); p != nil {
		return p[1], p[2]
	}
	return s, ""
}

func address(a string) string {
	a = strings.ToLower(strings.TrimSpace(a))
	at := strings.LastIndexByte(a, '@')
	if at < 5 {
		return a
	}
	local := a[:at]
	if strings.HasPrefix(local, "srs1") {
		if _, rest, ok := strings.Cut(local, "=="); ok {
			local = "srs0=" + rest
		}
	}
	if strings.HasPrefix(local, "srs0") && strings.ContainsRune("=+-", rune(local[4])) {
		if p := strings.SplitN(local[5:], "=", 4); len(p) == 4 && p[2] != "" && p[3] != "" {
			return p[3] + "@" + p[2]
		}
	}
	return a
}

func spamVerdict(value string) (bool, float64, bool) {
	m := spamPattern.FindStringSubmatch(value)
	if m == nil {
		return false, 0, false
	}
	score, err := strconv.ParseFloat(m[2], 64)
	return m[1] == "Yes", score, err == nil
}

func decodeSubject(logged string) string {
	s := foldedWords.ReplaceAllString(unfold(logged), "?= =?")
	if decoded, err := wordDecoder.DecodeHeader(s); err == nil {
		s = decoded
	}
	return strings.TrimSpace(s)
}

func unfold(logged string) string {
	var b strings.Builder
	line := len("Subject: ")
	for i := 0; i < len(logged); i++ {
		c := logged[i]
		if c == '?' && i+1 < len(logged) && (logged[i+1] == ' ' || logged[i+1] == '?') && line >= 60 {
			b.WriteByte(' ')
			i++
			line = 1
			continue
		}
		b.WriteByte(c)
		line++
	}
	return b.String()
}
