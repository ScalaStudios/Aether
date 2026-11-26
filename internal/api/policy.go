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
		if attrs["policy_context"] == "suppression" {
			if action, err = s.suppressionPolicy(ctx, attrs["recipient"], attrs["sender"]); err != nil {
				s.log.Warn("policy check failed", "context", "suppression", "err", err)
				action = "DEFER_IF_PERMIT 4.3.0 Try again later"
			}
		} else if attrs["policy_context"] == "sending" {
			if action, err = s.sendingPolicy(ctx, attrs["sasl_username"], attrs["instance"], attrs["recipient"]); err != nil {
				s.log.Warn("policy check failed", "context", "sending", "err", err)
				action = "DEFER_IF_PERMIT 4.3.0 Try again later"
			}
		} else if attrs["policy_context"] == "senders" {
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
	base, _, _ := strings.Cut(local, "+")
	sender = strings.ToLower(sender)
	senderLocal, senderDomain, _ := strings.Cut(sender, "@")
	var targetDomain *string
	if err := s.db.QueryRow(ctx, `SELECT target_name FROM mail_alias_domains WHERE alias_name = $1`, senderDomain).Scan(&targetDomain); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	senders := []string{sender}
	if targetDomain != nil {
		senders = append(senders, senderLocal+"@"+*targetDomain)
	}
	var orgID, acceptFrom string
	var members, allowed []string
	var moderated, dynamicMember bool
	err := s.db.QueryRow(ctx, `WITH target AS (SELECT l || '@' || coalesce((SELECT target_name FROM mail_alias_domains WHERE alias_name = $2::text), $2::text) AS address FROM unnest($1::text[]) l)
		SELECT * FROM (SELECT split_part(address, '@', 1) AS local, org_id, accept_from, destinations, allowed_senders, moderated, EXISTS (SELECT 1 FROM list_dynamic_members lm WHERE lm.alias_id = aliases.id AND lm.address = ANY ($3))
		FROM aliases WHERE active AND address IN (SELECT address FROM target)
		UNION ALL SELECT split_part(address, '@', 1), org_id, 'resource', '{}', '{}', false, false FROM mailboxes WHERE kind = 'resource' AND active AND address IN (SELECT address FROM target)) found
		ORDER BY local = $4 DESC LIMIT 1`, []string{local, base}, domain, senders, local).
		Scan(&local, &orgID, &acceptFrom, &members, &allowed, &moderated, &dynamicMember)
	if errors.Is(err, pgx.ErrNoRows) {
		return "DUNNO", nil
	}
	if err != nil || (acceptFrom == "anyone" && !moderated) {
		return "DUNNO", err
	}
	reject := "REJECT 5.7.1 " + local + "@" + domain + " does not accept mail from this sender"
	var hosted, sameOrg bool
	err = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_domains WHERE name = $1), EXISTS (SELECT 1 FROM domains WHERE name = $1 AND org_id = $2)`, senderDomain, orgID).
		Scan(&hosted, &sameOrg)
	if err != nil {
		return "", err
	}
	listed := func(list []string) bool {
		return slices.ContainsFunc(senders, func(a string) bool { return slices.Contains(list, a) })
	}
	authenticated := saslUser != ""
	if moderated && hosted && !authenticated {
		return reject, nil
	}
	trusted := sender != "" && (authenticated || !hosted)
	permit := acceptFrom == "anyone"
	switch acceptFrom {
	case "resource":
		permit = trusted
	case "organization":
		permit = authenticated && sameOrg
	case "members":
		permit = trusted && (listed(members) || dynamicMember)
	case "allowlist":
		permit = trusted && (listed(allowed) || slices.Contains(allowed, "@"+senderDomain) || targetDomain != nil && slices.Contains(allowed, "@"+*targetDomain))
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
