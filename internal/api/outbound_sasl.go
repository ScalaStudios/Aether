package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) ServeOutboundSASL(ln net.Listener) error {
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(s.cfg.MailDBPassword)))
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.outboundSASLConn(conn, token)
	}
}

func (s *Server) outboundSASLConn(conn net.Conn, token string) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return
		}
		request, err := readOutboundSASLRequest(reader)
		if err != nil {
			return
		}
		response := "PERM invalid map"
		if mapName, key, ok := strings.Cut(request, " "); ok && constantTimeEqual(mapName, token) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			response = s.lookupOutboundSASL(ctx, key)
			cancel()
		}
		if _, err := fmt.Fprintf(conn, "%d:%s,", len(response), response); err != nil {
			return
		}
	}
}

func readOutboundSASLRequest(reader *bufio.Reader) (string, error) {
	var sizeText []byte
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		if b == ':' && len(sizeText) > 0 {
			break
		}
		if b < '0' || b > '9' || len(sizeText) == 4 {
			return "", errors.New("invalid socketmap length")
		}
		sizeText = append(sizeText, b)
	}
	size, _ := strconv.Atoi(string(sizeText))
	payload := make([]byte, size+1)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return "", err
	}
	if payload[size] != ',' {
		return "", errors.New("invalid socketmap terminator")
	}
	return string(payload[:size]), nil
}

func (s *Server) lookupOutboundSASL(ctx context.Context, sender string) string {
	at := strings.LastIndexByte(sender, '@')
	if at <= 0 {
		return "NOTFOUND "
	}
	domain := strings.ToLower(sender[at+1:])
	if !outboundDomain(domain) {
		return "NOTFOUND "
	}
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT r.credentials FROM outbound_routes r
		JOIN mail_domains d ON d.name = r.sender_domain
		WHERE d.name = $1 AND r.credentials IS NOT NULL`, domain).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "NOTFOUND "
	}
	if err == nil {
		var credentials []byte
		credentials, err = s.box.Open(sealed, outboundCredentialContext(domain))
		if err == nil {
			return "OK " + string(credentials)
		}
	}
	s.log.Error("outbound relay credentials unavailable", "domain", domain, "err", err)
	return "TEMP outbound relay credentials unavailable"
}
