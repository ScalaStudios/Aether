package api

import (
	"bufio"
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) ServePolicy(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.policyConn(conn)
	}
}

func (s *Server) policyConn(conn net.Conn) {
	defer conn.Close()
	rd := bufio.NewReader(conn)
	attrs := map[string]string{}
	for {
		conn.SetDeadline(time.Now().Add(5 * time.Minute))
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			if k, v, ok := strings.Cut(line, "="); ok {
				attrs[k] = v
			}
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		action, err := s.listPolicy(ctx, attrs["recipient"], attrs["sender"], attrs["sasl_username"])
		cancel()
		if err != nil {
			s.log.Warn("list policy check failed", "err", err)
			action = "DEFER_IF_PERMIT 4.3.0 Try again later"
		}
		if _, err := conn.Write([]byte("action=" + action + "\n\n")); err != nil {
			return
		}
		attrs = map[string]string{}
	}
}

func (s *Server) listPolicy(ctx context.Context, recipient, sender, saslUser string) (string, error) {
	local, domain, ok := strings.Cut(strings.ToLower(recipient), "@")
	if !ok {
		return "DUNNO", nil
	}
	local, _, _ = strings.Cut(local, "+")
	var orgID, acceptFrom string
	var members, allowed []string
	err := s.db.QueryRow(ctx, `SELECT org_id, accept_from, destinations, allowed_senders FROM aliases WHERE address = $1 AND active`, local+"@"+domain).
		Scan(&orgID, &acceptFrom, &members, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "DUNNO", nil
	}
	if err != nil || acceptFrom == "anyone" {
		return "DUNNO", err
	}
	sender = strings.ToLower(sender)
	_, senderDomain, _ := strings.Cut(sender, "@")
	var hosted, sameOrg bool
	err = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_domains WHERE name = $1), EXISTS (SELECT 1 FROM domains WHERE name = $1 AND org_id = $2)`, senderDomain, orgID).
		Scan(&hosted, &sameOrg)
	if err != nil {
		return "", err
	}
	authenticated := saslUser != ""
	trusted := sender != "" && (authenticated || !hosted)
	var permit bool
	switch acceptFrom {
	case "organization":
		permit = authenticated && sameOrg
	case "members":
		permit = trusted && slices.Contains(members, sender)
	case "allowlist":
		permit = trusted && (slices.Contains(allowed, sender) || slices.Contains(allowed, "@"+senderDomain))
	}
	if permit {
		return "DUNNO", nil
	}
	return "REJECT 5.7.1 " + local + "@" + domain + " does not accept mail from this sender", nil
}
