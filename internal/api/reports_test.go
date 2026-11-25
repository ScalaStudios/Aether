package api

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"mime"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
)

func TestParseTagRecord(t *testing.T) {
	tags := parseTagRecord(" v=DMARC1 ; p=Reject ; p=none ; rua=mailto:Agg@Reports.Example.com!10m ", "DMARC1")
	if tags["p"] != "Reject" || tags["rua"] != "mailto:Agg@Reports.Example.com!10m" || tags["v"] != "DMARC1" {
		t.Fatalf("tags = %#v", tags)
	}
	if parseTagRecord("v=spf1; p=none", "DMARC1") != nil {
		t.Fatal("accepted a record with the wrong version")
	}
	if parseTagRecord("v=tlsrptv1; rua=mailto:tls@example.com", "TLSRPTv1") == nil {
		t.Fatal("rejected a TLS-RPT record because of case")
	}
}

func TestReportDestinations(t *testing.T) {
	addr, limit := dmarcMailto("mailto:Agg@Reports.Example.com!10m")
	if addr != "Agg@reports.example.com" || limit != 10<<20 {
		t.Fatalf("mailto = %q %d", addr, limit)
	}
	if addr, limit := dmarcMailto("mailto:a@b.com!10k"); addr != "a@b.com" || limit != 10<<10 {
		t.Fatalf("kilobyte limit = %q %d", addr, limit)
	}
	if addr, limit := dmarcMailto("mailto:a@b.com!0"); addr != "" || limit != 0 {
		t.Fatalf("zero limit = %q %d", addr, limit)
	}
	if addr, _ := dmarcMailto("https://reports.example/collect"); addr != "" {
		t.Fatalf("non-mailto rua = %q", addr)
	}
	if got := tlsReportDestination("mailto:tls@Example.com"); got != "tls@example.com" {
		t.Fatalf("tls mailto = %q", got)
	}
	if got := tlsReportDestination("https://User:pw@reports.example/collect"); got != "" {
		t.Fatalf("tls https with userinfo = %q", got)
	}
	if got := tlsReportDestination("https://reports.example/collect"); got != "https://reports.example/collect" {
		t.Fatalf("tls https = %q", got)
	}
	if got := tlsReportDestination("https://reports.example:8443/collect"); got != "" {
		t.Fatalf("tls https on another port = %q", got)
	}
}

func TestDMARCReportRowsAndExternalDestinations(t *testing.T) {
	if domain, ok := dmarcReportDomain("dmarc_rpt;Example.COM;mailto:a@b.com,mailto:c@d.com;20261122", "20261122"); !ok || domain != "example.com" {
		t.Fatalf("report key = %q %v", domain, ok)
	}
	if _, ok := dmarcReportDomain("dmarc_rpt;example.com;20261122", "20261122"); ok {
		t.Fatal("accepted a report key without a rua")
	}
	row := "203.0.113.4,pass,fail,quarantine,sampled_out,example.com,one.example|two.example,bad.example,,,spf.example,softfail"
	record, ok := rspamdDMARCRecord(row, 4)
	if !ok || record.Row.Count != 4 || record.Row.Evaluated.Reason == nil || record.Row.Evaluated.Reason.Type != "sampled_out" || len(record.Auth.DKIM) != 3 || record.Auth.SPF[0].Result != "softfail" {
		t.Fatalf("record = %+v %v", record, ok)
	}
	if _, ok := rspamdDMARCRecord("203.0.113.4,pass,fail,none,,example.com,,,,,,,,,,", 1); ok {
		t.Fatal("accepted a short row")
	}
	live, ok := rspamdDMARCRecord("203.0.113.50,fail,fail,none,,gmail.com,,,,,gmail.com,softfail", 1)
	if !ok || live.Row.Evaluated.Disposition != "none" || live.Identifiers.HeaderFrom != "gmail.com" || len(live.Auth.DKIM) != 0 || live.Auth.SPF[0].Result != "softfail" {
		t.Fatalf("row from rspamd 4.3.1 = %+v %v", live, ok)
	}
	if domain, ok := dmarcReportDomain("dmarc_rpt;gmail.com;mailto:mailauth-reports@google.com;20261124", "20261124"); !ok || domain != "gmail.com" {
		t.Fatalf("rspamd report key = %q %v", domain, ok)
	}
	if !sameReportDomain("mail.example.com", "reports.example.com") || !sameReportDomain("a.example.co.uk", "b.example.co.uk") {
		t.Fatal("organizational domains did not match")
	}
	if sameReportDomain("a.github.io", "b.github.io") || sameReportDomain("example.com", "example.org") {
		t.Fatal("unrelated domains matched")
	}

	ctx := context.Background()
	same := &Server{resolve: txtResolver{err: errors.New("dns used")}}
	addr, limit, err := same.dmarcDestination(ctx, "mail.example.com", "mailto:dmarc@reports.example.com")
	if err != nil || addr != "dmarc@reports.example.com" || limit != 0 {
		t.Fatalf("same organization = %q %d %v", addr, limit, err)
	}
	external := &Server{resolve: txtResolver{txt: map[string][]string{
		"example.com._report._dmarc.collector.test": {"v=DMARC1; rua=mailto:agg@collector.test!1k"},
	}}}
	addr, limit, err = external.dmarcDestination(ctx, "example.com", "mailto:agg@collector.test!10m")
	if err != nil || addr != "agg@collector.test" || limit != 1<<10 {
		t.Fatalf("external destination = %q %d %v", addr, limit, err)
	}
	addr, _, err = external.dmarcDestination(ctx, "example.com", "mailto:other@collector.test")
	if err != nil || addr != "" {
		t.Fatalf("unlisted external destination = %q %v", addr, err)
	}
	plain := &Server{resolve: txtResolver{txt: map[string][]string{
		"example.com._report._dmarc.collector.test": {"v=DMARC1; p=none"},
	}}}
	if addr, _, err := plain.dmarcDestination(ctx, "example.com", "mailto:agg@collector.test"); err != nil || addr != "" {
		t.Fatalf("external record without rua = %q %v", addr, err)
	}
}

func TestDMARCMessageAndTLSLog(t *testing.T) {
	rep := reporter{from: "dmarc-reports@example.com", org: "Example", submitter: "example.com"}
	record, ok := rspamdDMARCRecord("203.0.113.4,pass,fail,none,,example.com,one.example,,,,spf.example,pass", 2)
	if !ok {
		t.Fatal("row was rejected")
	}
	message := rep.build(reportMessage{
		rcpts: []string{"agg@reports.example.com"}, subject: "Report Domain: example.com Submitter: example.com Report-ID: <example.com.20261122@example.com>",
		text: "This message contains a DMARC aggregate report.", filename: "example.com!example.com!1!2.xml.gz",
		contentType: "application/gzip", data: gzipBytes([]byte("<feedback/>")),
	}, time.Date(2026, 11, 23, 1, 2, 3, 0, time.UTC))
	parsed, err := mail.ReadMessage(bytes.NewReader(message))
	if err != nil {
		t.Fatal(err)
	}
	from, err := mail.ParseAddress(parsed.Header.Get("From"))
	if err != nil || from.Address != "dmarc-reports@example.com" || from.Name != "Example" || parsed.Header.Get("Auto-Submitted") != "auto-generated" {
		t.Fatalf("headers from=%q auto=%q err=%v", parsed.Header.Get("From"), parsed.Header.Get("Auto-Submitted"), err)
	}
	media, _, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || media != "multipart/mixed" {
		t.Fatalf("content type = %q %v", parsed.Header.Get("Content-Type"), err)
	}
	tlsMessage := rep.build(reportMessage{
		rcpts: []string{"tls@example.com"}, subject: "Report Domain: other.test Submitter: example.com Report-ID: <other.test.20261122@example.com>",
		headers: [][2]string{{"TLS-Report-Domain", "other.test"}, {"TLS-Report-Submitter", "example.com"}}, reportType: "tlsrpt",
		text: "This message contains an SMTP TLS aggregate report.", filename: "example.com!other.test!1!2.json.gz",
		contentType: "application/tlsrpt+gzip", data: gzipBytes([]byte(`{"report-id":"1"}`)),
	}, time.Date(2026, 11, 23, 1, 2, 3, 0, time.UTC))
	tlsParsed, err := mail.ReadMessage(bytes.NewReader(tlsMessage))
	if err != nil {
		t.Fatal(err)
	}
	tlsMedia, tlsParams, err := mime.ParseMediaType(tlsParsed.Header.Get("Content-Type"))
	if err != nil || tlsMedia != "multipart/report" || tlsParams["report-type"] != "tlsrpt" || tlsParsed.Header.Get("TLS-Report-Domain") != "other.test" {
		t.Fatalf("tls message type=%q params=%v domain=%q err=%v", tlsMedia, tlsParams, tlsParsed.Header.Get("TLS-Report-Domain"), err)
	}
	var report dmarcFeedback
	report.Version = "1.0"
	report.Metadata.OrgName = rep.org
	report.Metadata.Email = rep.from
	report.Metadata.ReportID = "example.com.20261122@example.com"
	report.Metadata.DateRange.Begin = 1
	report.Metadata.DateRange.End = 2
	report.Policy.Domain = "example.com"
	report.Policy.P = "none"
	report.Records = []dmarcRecord{record}
	payload, err := xml.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	body := append([]byte(xml.Header), payload...)
	if bytes.Contains(body, []byte("<reason>")) {
		t.Fatalf("empty reason was included: %s", body)
	}
	decoded, err := decodeDMARCReport(body)
	if err != nil || decoded.Version != "1.0" || decoded.Policy.Domain != "example.com" || decoded.Records[0].Row.Count != 2 || decoded.Records[0].Auth.DKIM[0].Domain != "one.example" {
		t.Fatalf("decoded report = %+v %v", decoded, err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "mail.log")
	log := strings.Join([]string{
		"Nov 22 01:02:03 mail postfix/qmgr[100]: ABC123: from=<user@example.com>, size=100, nrcpt=1 (queue active)",
		"Nov 22 01:02:04 mail postfix/smtp[200]: Trusted TLS connection established to mx.other.test[203.0.113.9]:25: TLSv1.3 with cipher TLS_AES_256_GCM_SHA384 (256/256 bits)",
		"Nov 22 01:02:05 mail postfix/smtp[200]: ABC123: to=<a@other.test>, relay=mx.other.test[203.0.113.9]:25, delay=1, status=sent (250 ok)",
		"Nov 22 01:02:06 mail postfix/smtp[200]: ABC123: to=<b@other.test>, relay=mx.other.test[203.0.113.9]:25, delay=1, status=sent (250 ok)",
		"Nov 22 01:03:00 mail postfix/smtp[201]: DEF456: to=<b@bad.test>, relay=mx.bad.test[203.0.113.10]:25, delay=1, status=deferred (TLS is required, but was not offered by host)",
		"Nov 22 01:04:00 mail postfix/smtp[202]: Verified TLS connection established to dane.test[203.0.113.11]:25: TLSv1.3 with cipher TLS_AES_256_GCM_SHA384 (256/256 bits)",
		"Nov 22 01:04:01 mail postfix/smtp[202]: GHI789: to=<c@dane.test>, relay=dane.test[203.0.113.11]:25, delay=1, status=sent (250 ok)",
		"Nov 22 01:05:00 mail postfix/qmgr[100]: REP123: from=<dmarc-reports@example.com>, size=10, nrcpt=1 (queue active)",
		"Nov 22 01:05:01 mail postfix/smtp[203]: Trusted TLS connection established to mx.other.test[203.0.113.9]:25: TLSv1.3 with cipher TLS_AES_256_GCM_SHA384 (256/256 bits)",
		"Nov 22 01:05:02 mail postfix/smtp[203]: REP123: to=<rua@other.test>, relay=mx.other.test[203.0.113.9]:25, delay=1, status=sent (250 ok)",
		"Nov 21 23:59:00 mail postfix/smtp[204]: OLD123: to=<z@old.test>, relay=old.test[203.0.113.12]:25, delay=1, status=sent (250 ok)",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(log+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	policies, err := readTLSReportLog(logPath, "dmarc-reports@example.com", day)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies["other.test"]) != 1 || policies["other.test"][0].Policy.Type != "no-policy-found" || policies["other.test"][0].Summary.Successful != 1 {
		t.Fatalf("other.test = %+v", policies["other.test"])
	}
	if policies["bad.test"][0].Summary.Failed != 1 || policies["bad.test"][0].Failures[0].Type != "starttls-not-supported" || policies["bad.test"][0].Failures[0].IP != "203.0.113.10" {
		t.Fatalf("bad.test = %+v", policies["bad.test"])
	}
	if policies["dane.test"][0].Policy.Type != "tlsa" || policies["dane.test"][0].Summary.Successful != 1 {
		t.Fatalf("dane.test = %+v", policies["dane.test"])
	}
	if _, ok := policies["old.test"]; ok {
		t.Fatal("counted a delivery from another day")
	}

	var httpErr error = &reportHTTPError{code: 404}
	var retry error = &reportHTTPError{code: 503}
	var smtpErr error = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}}
	if !permanentReportError(httpErr) || permanentReportError(retry) || !permanentReportError(smtpErr) {
		t.Fatal("permanent report errors were classified incorrectly")
	}
	now := time.Date(2026, 11, 23, 0, 20, 0, 0, time.UTC)
	if got := reportDay(now); !got.Equal(time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("report day = %s", got)
	}
}

type txtResolver struct {
	txt map[string][]string
	err error
}

func (r txtResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	if values, ok := r.txt[name]; ok {
		return values, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (txtResolver) LookupMX(context.Context, string) ([]*net.MX, error)  { return nil, nil }
func (txtResolver) LookupHost(context.Context, string) ([]string, error) { return nil, nil }
func (txtResolver) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", nil, nil
}
