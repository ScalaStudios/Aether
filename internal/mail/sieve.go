package mail

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

type RuleCondition struct {
	Field    string `json:"field"`
	Contains string `json:"contains"`
}

type RuleActions struct {
	MoveTo   string `json:"moveTo,omitempty"`
	MarkRead bool   `json:"markRead,omitempty"`
	Flag     bool   `json:"flag,omitempty"`
	Stop     bool   `json:"stop,omitempty"`
}

type Rule struct {
	Name       string          `json:"name"`
	Enabled    bool            `json:"enabled"`
	Match      string          `json:"match"`
	Conditions []RuleCondition `json:"conditions"`
	Actions    RuleActions     `json:"actions"`
}

var ruleHeaders = map[string]string{"from": "From", "to": "To", "cc": "Cc", "subject": "Subject"}

func sieveString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (r Rule) Validate() error {
	switch {
	case strings.TrimSpace(r.Name) == "" || len(r.Name) > 100:
		return errors.New("Give each rule a name of up to 100 characters.")
	case r.Match != "all" && r.Match != "any":
		return errors.New("Match must be all or any.")
	case len(r.Conditions) == 0 || len(r.Conditions) > 20:
		return errors.New("Each rule needs between 1 and 20 conditions.")
	case r.Actions.MoveTo == "" && !r.Actions.MarkRead && !r.Actions.Flag:
		return errors.New("Each rule needs at least one action.")
	case strings.ContainsAny(r.Actions.MoveTo, "\r\n\x00"):
		return errors.New("The folder name is not valid.")
	}
	for _, c := range r.Conditions {
		if _, ok := ruleHeaders[c.Field]; !ok {
			return errors.New("Conditions can check from, to, cc, or subject.")
		}
		if strings.TrimSpace(c.Contains) == "" || len(c.Contains) > 500 || strings.ContainsAny(c.Contains, "\r\n\x00") {
			return errors.New("Each condition needs text to look for, up to 500 characters on one line.")
		}
	}
	return nil
}

func CompileSieve(rules []Rule) string {
	var b strings.Builder
	b.WriteString(`require ["fileinto", "imap4flags", "mailbox"];` + "\n")
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		tests := make([]string, len(r.Conditions))
		for i, c := range r.Conditions {
			tests[i] = fmt.Sprintf("header :contains %s %s", sieveString(ruleHeaders[c.Field]), sieveString(c.Contains))
		}
		op := "allof"
		if r.Match == "any" {
			op = "anyof"
		}
		fmt.Fprintf(&b, "if %s (%s) {\n", op, strings.Join(tests, ", "))
		if r.Actions.MarkRead {
			b.WriteString("  addflag \"\\\\Seen\";\n")
		}
		if r.Actions.Flag {
			b.WriteString("  addflag \"\\\\Flagged\";\n")
		}
		if r.Actions.MoveTo != "" {
			fmt.Fprintf(&b, "  fileinto :create %s;\n", sieveString(r.Actions.MoveTo))
		}
		if r.Actions.Stop {
			b.WriteString("  stop;\n")
		}
		b.WriteString("}\n")
	}
	return b.String()
}

type sieveConn struct {
	conn net.Conn
	r    *bufio.Reader
}

func (s *sieveConn) response() error {
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "OK"):
			return nil
		case strings.HasPrefix(line, "NO"), strings.HasPrefix(line, "BYE"):
			return fmt.Errorf("managesieve: %s", line)
		}
	}
}

func (s *sieveConn) command(format string, args ...any) error {
	s.conn.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := fmt.Fprintf(s.conn, format+"\r\n", args...); err != nil {
		return err
	}
	return s.response()
}

func (c *Client) PutSieve(address, script string) error {
	raw, err := net.DialTimeout("tcp", c.sieveAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("managesieve connect: %w", err)
	}
	defer raw.Close()
	s := &sieveConn{conn: raw, r: bufio.NewReader(raw)}
	raw.SetDeadline(time.Now().Add(20 * time.Second))
	if err := s.response(); err != nil {
		return err
	}
	if err := s.command("STARTTLS"); err != nil {
		return err
	}
	tlsConn := tls.Client(raw, c.tls)
	if err := tlsConn.Handshake(); err != nil {
		return fmt.Errorf("managesieve starttls: %w", err)
	}
	s = &sieveConn{conn: tlsConn, r: bufio.NewReader(tlsConn)}
	if err := s.response(); err != nil {
		return err
	}
	creds := base64.StdEncoding.EncodeToString([]byte("\x00" + c.masterLogin(address) + "\x00" + c.masterPassword))
	if err := s.command(`AUTHENTICATE "PLAIN" "%s"`, creds); err != nil {
		return err
	}
	if err := s.command("PUTSCRIPT \"aether\" {%d+}\r\n%s", len(script), script); err != nil {
		return err
	}
	if err := s.command(`SETACTIVE "aether"`); err != nil {
		return err
	}
	s.command("LOGOUT")
	return nil
}
