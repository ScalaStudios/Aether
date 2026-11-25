package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type SyslogSink struct {
	network  string
	addr     string
	facility int
	appName  string
	hostname string
	procID   string
	dialer   net.Dialer
	mu       sync.Mutex
	conn     net.Conn
}

func NewSyslogSink(network, addr string, facility int, appName, hostname string) *SyslogSink {
	if network != "tcp" {
		network = "udp"
	}
	if facility < 0 || facility > 23 {
		facility = 4
	}
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	return &SyslogSink{
		network:  network,
		addr:     addr,
		facility: facility,
		appName:  sanitizeSyslogToken(appName, 48),
		hostname: sanitizeSyslogToken(hostname, 255),
		procID:   sanitizeSyslogToken(strconv.Itoa(os.Getpid()), 128),
		dialer:   net.Dialer{Timeout: 5 * time.Second},
	}
}

func (s *SyslogSink) FormatRFC5424(ev Event) []byte {
	sev := 6
	if isWarningAction(ev.Action) {
		sev = 4
	}
	pri := s.facility*8 + sev
	ts := ev.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000Z")
	msgID := sanitizeSyslogToken(ev.Action, 32)
	sd := formatStructuredData(ev)
	payload, err := json.Marshal(ev)
	if err != nil {
		payload = []byte("{}")
	}
	var b strings.Builder
	b.Grow(len(sd) + len(payload) + 128)
	b.WriteByte('<')
	b.WriteString(strconv.Itoa(pri))
	b.WriteString(">1 ")
	b.WriteString(ts)
	b.WriteByte(' ')
	b.WriteString(s.hostname)
	b.WriteByte(' ')
	b.WriteString(s.appName)
	b.WriteByte(' ')
	b.WriteString(s.procID)
	b.WriteByte(' ')
	b.WriteString(msgID)
	b.WriteByte(' ')
	b.WriteString(sd)
	b.WriteByte(' ')
	b.Write(payload)
	return []byte(b.String())
}

func (s *SyslogSink) Send(ctx context.Context, ev Event) error {
	msg := s.FormatRFC5424(ev)
	if s.network == "tcp" {
		msg = append(msg, '\n')
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.network == "tcp" && s.conn != nil && isTCPConnDead(s.conn) {
		s.conn.Close()
		s.conn = nil
	}
	if err := s.writeOnce(ctx, msg); err != nil {
		if s.conn != nil {
			s.conn.Close()
			s.conn = nil
		}
		return s.writeOnce(ctx, msg)
	}
	return nil
}

func isTCPConnDead(conn net.Conn) bool {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return true
	}
	dead := false
	err = raw.Read(func(fd uintptr) bool {
		var buf [1]byte
		n, _, recvErr := syscall.Recvfrom(int(fd), buf[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
		switch {
		case recvErr == syscall.EAGAIN || recvErr == syscall.EWOULDBLOCK:
			dead = false
		case n == 0 && recvErr == nil:
			dead = true
		case recvErr != nil:
			dead = true
		}
		return true
	})
	return err != nil || dead
}

func (s *SyslogSink) writeOnce(ctx context.Context, msg []byte) error {
	if s.conn == nil {
		conn, err := s.dialer.DialContext(ctx, s.network, s.addr)
		if err != nil {
			return fmt.Errorf("syslog dial %s %s: %w", s.network, s.addr, err)
		}
		s.conn = conn
	}
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.conn.SetWriteDeadline(deadline)
	_, err := s.conn.Write(msg)
	return err
}

func (s *SyslogSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

func formatStructuredData(ev Event) string {
	var b strings.Builder
	b.WriteString("[aether@32473")
	writeSDParam(&b, "eventId", strconv.FormatInt(ev.ID, 10))
	writeSDParam(&b, "action", ev.Action)
	if ev.OrgID != nil && *ev.OrgID != "" {
		writeSDParam(&b, "orgId", *ev.OrgID)
	}
	if ev.ActorUserID != nil && *ev.ActorUserID != "" {
		writeSDParam(&b, "actorUserId", *ev.ActorUserID)
	}
	if ev.ActorEmail != "" {
		writeSDParam(&b, "actorEmail", ev.ActorEmail)
	}
	if ev.TargetType != "" {
		writeSDParam(&b, "targetType", ev.TargetType)
	}
	if ev.TargetID != "" {
		writeSDParam(&b, "targetId", ev.TargetID)
	}
	if ev.IP != "" {
		writeSDParam(&b, "ip", ev.IP)
	}
	b.WriteByte(']')
	return b.String()
}

func writeSDParam(b *strings.Builder, name, val string) {
	b.WriteByte(' ')
	b.WriteString(name)
	b.WriteString(`="`)
	for _, r := range val {
		switch r {
		case '\\', '"', ']':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\r', '\n':
			b.WriteByte(' ')
		default:
			if r >= 0x20 && r != 0x7f {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

func sanitizeSyslogToken(s string, maxLen int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < maxLen; i++ {
		c := s[i]
		if c >= 33 && c <= 126 {
			b.WriteByte(c)
		}
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}
