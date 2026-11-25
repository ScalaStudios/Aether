package api

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/jackc/pgx/v5"
)

const reportSettleDelay = 30 * time.Minute

var errNoReportSender = errors.New("no verified domain matches the mail host name; set AETHER_REPORT_FROM")

type reporter struct {
	from, org, submitter string
}

func (s *Server) RunReports(ctx context.Context) {
	if !s.cfg.SendReports {
		return
	}
	if s.cfg.SieveRelayPassword == "" {
		s.log.Warn("AETHER_SIEVE_RELAY_PASSWORD is not set; DMARC aggregate and TLS reports are not sent")
		return
	}
	https := newWebhookClient(false)
	defer https.CloseIdleConnections()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		rep, err := s.reportIdentity(ctx)
		if err != nil {
			s.log.Warn("DMARC aggregate and TLS reports are not sent", "err", err)
		} else {
			end := reportDay(time.Now())
			for _, day := range []time.Time{end.AddDate(0, 0, -2), end.AddDate(0, 0, -1)} {
				s.sendDMARCReports(ctx, rep, day)
				s.sendTLSReports(ctx, rep, day, https)
			}
			if _, err := s.db.Exec(ctx, `DELETE FROM report_deliveries WHERE day < $1`, end.AddDate(0, 0, -7)); err != nil {
				s.log.Warn("pruning report deliveries failed", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func reportDay(now time.Time) time.Time {
	return now.UTC().Add(-reportSettleDelay).Truncate(24 * time.Hour)
}

func (s *Server) reportIdentity(ctx context.Context) (reporter, error) {
	from := s.cfg.ReportFrom
	if from == "" {
		var name string
		err := s.db.QueryRow(ctx, `
			SELECT name FROM domains WHERE active AND verified_at IS NOT NULL AND ($1 = name OR $1 LIKE '%.' || name)
			ORDER BY length(name) DESC LIMIT 1`, s.cfg.MailHostname).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return reporter{}, errNoReportSender
		}
		if err != nil {
			return reporter{}, err
		}
		from = "dmarc-reports@" + name
	}
	submitter, _ := addressDomain(from)
	var verified bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM domains WHERE name = $1 AND active AND verified_at IS NOT NULL)`, submitter).Scan(&verified)
	if err != nil {
		return reporter{}, err
	}
	if !verified {
		return reporter{}, errNoReportSender
	}
	if err := s.kv.HGet(ctx, dkimSelectorsHash, submitter).Err(); err != nil {
		return reporter{}, fmt.Errorf("loading report sender DKIM selector: %w", err)
	}
	return reporter{from: from, org: cmp.Or(s.cfg.ReportOrgName, submitter), submitter: submitter}, nil
}

func parseTagRecord(txt, version string) map[string]string {
	tags := map[string]string{}
	for i, part := range strings.Split(txt, ";") {
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if i == 0 && (!ok || k != "v" || !strings.EqualFold(v, version)) {
			return nil
		}
		if ok && k != "" {
			if _, seen := tags[k]; !seen {
				tags[k] = v
			}
		}
	}
	return tags
}

func mailtoAddress(uri string) (string, bool) {
	if len(uri) < 7 || !strings.EqualFold(uri[:7], "mailto:") {
		return "", false
	}
	path, _, _ := strings.Cut(uri[7:], "?")
	addr, err := url.PathUnescape(path)
	if err != nil {
		return "", false
	}
	domain, ok := addressDomain(addr)
	parsed, err := mail.ParseAddress(addr)
	if !ok || err != nil || parsed.Address != addr || strings.ContainsAny(addr, "\r\n") || !domainPattern.MatchString(domain) {
		return "", false
	}
	local := addr[:strings.LastIndexByte(addr, '@')]
	return local + "@" + domain, true
}

type reportMessage struct {
	rcpts       []string
	subject     string
	headers     [][2]string
	reportType  string
	text        string
	filename    string
	contentType string
	data        []byte
}

func gzipBytes(data []byte) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write(data)
	gw.Close()
	return buf.Bytes()
}

func (rep reporter) build(m reportMessage, now time.Time) []byte {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	text, _ := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"7bit"},
	})
	text.Write([]byte(strings.ReplaceAll(m.text, "\n", "\r\n")))
	attachment, _ := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {mime.FormatMediaType(m.contentType, map[string]string{"name": m.filename})},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": m.filename})},
	})
	encoded := base64.StdEncoding.EncodeToString(m.data)
	for len(encoded) > 76 {
		attachment.Write([]byte(encoded[:76] + "\r\n"))
		encoded = encoded[76:]
	}
	attachment.Write([]byte(encoded + "\r\n"))
	mw.Close()

	id := make([]byte, 16)
	rand.Read(id)
	contentType := map[string]string{"boundary": mw.Boundary()}
	mediaType := "multipart/mixed"
	if m.reportType != "" {
		mediaType = "multipart/report"
		contentType["report-type"] = m.reportType
	}
	var out bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&out, "%s: %s\r\n", k, v) }
	header("From", (&mail.Address{Name: rep.org, Address: rep.from}).String())
	header("To", strings.Join(m.rcpts, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", m.subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+rep.submitter+">")
	for _, h := range m.headers {
		header(h[0], h[1])
	}
	header("Auto-Submitted", "auto-generated")
	header("MIME-Version", "1.0")
	header("Content-Type", mime.FormatMediaType(mediaType, contentType))
	out.WriteString("\r\n")
	out.Write(body.Bytes())
	return out.Bytes()
}

type reportHTTPError struct{ code int }

func (e *reportHTTPError) Error() string {
	return fmt.Sprintf("HTTPS collector returned status %d", e.code)
}

func permanentSMTPError(err error) bool {
	var smtpErr *smtp.SMTPError
	return errors.As(err, &smtpErr) && smtpErr.Code >= 500
}

func permanentReportError(err error) bool {
	var httpErr *reportHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.code >= 400 && httpErr.code < 500 && httpErr.code != http.StatusRequestTimeout && httpErr.code != http.StatusTooManyRequests
	}
	return permanentSMTPError(err)
}

func (s *Server) reportTXT(ctx context.Context, name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	values, err := s.resolve.LookupTXT(ctx, name)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, nil
	}
	return values, err
}

func (s *Server) reportTags(ctx context.Context, name, version string) (map[string]string, error) {
	values, err := s.reportTXT(ctx, name)
	if err != nil {
		return nil, err
	}
	var tags map[string]string
	for _, value := range values {
		if candidate := parseTagRecord(value, version); candidate != nil {
			if tags != nil {
				return nil, nil
			}
			tags = candidate
		}
	}
	return tags, nil
}

func (s *Server) deliverReport(ctx context.Context, rep reporter, kind, domain string, day time.Time, dest string, message reportMessage, https *http.Client) {
	err := s.sendReport(ctx, rep, kind, domain, day, dest, message, https)
	if err != nil {
		s.log.Warn("report delivery failed", "kind", kind, "domain", domain, "err", err)
	}
}

func (s *Server) sendReport(ctx context.Context, rep reporter, kind, domain string, day time.Time, dest string, message reportMessage, https *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	tag, err := tx.Exec(ctx, `
		INSERT INTO report_deliveries (kind, day, domain, destination) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, kind, day, domain, dest)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if strings.HasPrefix(dest, "https://") {
		err = postReport(ctx, https, dest, message)
	} else {
		message.rcpts = []string{dest}
		err = s.mail.Relay(rep.from, message.rcpts, rep.build(message, time.Now()), nil)
	}
	if err != nil && !permanentReportError(err) {
		return err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return commitErr
	}
	if err == nil {
		reportsSent.WithLabelValues(kind).Inc()
	}
	return err
}

func postReport(ctx context.Context, https *http.Client, dest string, message reportMessage) error {
	if https == nil {
		return errors.New("HTTPS report collector is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, dest, bytes.NewReader(message.data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", message.contentType)
	response, err := https.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &reportHTTPError{code: response.StatusCode}
	}
	return nil
}
