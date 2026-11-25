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
		var action string
		if attrs["policy_context"] == "senders" {
			if action, err = s.senderPolicy(ctx, attrs); err != nil {
				s.log.Warn("sender list check failed; accepting", "err", err)
				action = "DUNNO"
			}
		} else {
			check := s.listPolicy
			if attrs["sasl_username"] == "aether-sieve-relay" {
				check = s.forwardingPolicyCheck
			}
			if action, err = check(ctx, attrs["recipient"], attrs["sender"], attrs["sasl_username"]); err != nil {
				s.log.Warn("policy check failed", "err", err)
				action = "DEFER_IF_PERMIT 4.3.0 Try again later"
			}
		}
		cancel()
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
	sender = strings.ToLower(sender)
	var orgID, acceptFrom string
	var members, allowed []string
	var moderated, dynamicMember bool
	err := s.db.QueryRow(ctx, `SELECT org_id, accept_from, destinations, allowed_senders, moderated, EXISTS (SELECT 1 FROM list_dynamic_members lm WHERE lm.alias_id = aliases.id AND lm.address = $2)
		FROM aliases WHERE address = $1 AND active`, local+"@"+domain, sender).
		Scan(&orgID, &acceptFrom, &members, &allowed, &moderated, &dynamicMember)
	if errors.Is(err, pgx.ErrNoRows) {
		return "DUNNO", nil
	}
	if err != nil || (acceptFrom == "anyone" && !moderated) {
		return "DUNNO", err
	}
	reject := "REJECT 5.7.1 " + local + "@" + domain + " does not accept mail from this sender"
	_, senderDomain, _ := strings.Cut(sender, "@")
	var hosted, sameOrg bool
	err = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_domains WHERE name = $1), EXISTS (SELECT 1 FROM domains WHERE name = $1 AND org_id = $2)`, senderDomain, orgID).
		Scan(&hosted, &sameOrg)
	if err != nil {
		return "", err
	}
	authenticated := saslUser != ""
	if moderated && hosted && !authenticated {
		return reject, nil
	}
	trusted := sender != "" && (authenticated || !hosted)
	permit := acceptFrom == "anyone"
	switch acceptFrom {
	case "organization":
		permit = authenticated && sameOrg
	case "members":
		permit = trusted && (slices.Contains(members, sender) || dynamicMember)
	case "allowlist":
		permit = trusted && (slices.Contains(allowed, sender) || slices.Contains(allowed, "@"+senderDomain))
	}
	if permit {
		return "DUNNO", nil
	}
	return reject, nil
}

func (s *Server) forwardingPolicyCheck(ctx context.Context, recipient, sender, _ string) (string, error) {
	local, domain, ok := strings.Cut(strings.ToLower(sender), "@")
	if !ok {
		return "DUNNO", nil
	}
	base, _, _ := strings.Cut(local, "+")
	var orgID string
	err := s.db.QueryRow(ctx, `SELECT org_id FROM mailboxes WHERE address IN ($1, $2) ORDER BY address = $1 DESC LIMIT 1`, local+"@"+domain, base+"@"+domain).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "DUNNO", nil
	}
	if err != nil {
		return "", err
	}
	policy, err := s.forwardPolicy(ctx, orgID)
	if err != nil || policy.Allows(recipient) {
		return "DUNNO", err
	}
	return "REJECT 5.7.1 Your organization does not allow automatic forwarding to " + recipient, nil
}
