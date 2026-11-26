package api

import (
	"bytes"
	"compress/gzip"
	"runtime"
	"strings"
	"testing"
)

func gzipDMARC(t *testing.T, head, repeated, tail string, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write([]byte(head))
	chunk := []byte(strings.Repeat(repeated, 10000))
	for i := 0; i < n/10000; i++ {
		gw.Write(chunk)
	}
	gw.Write([]byte(tail))
	gw.Close()
	return buf.Bytes()
}

func TestDecodeDMARCReportMemoryBoundedByRecordLimit(t *testing.T) {
	head := `<?xml version="1.0"?><feedback><report_metadata><org_name>r</org_name><report_id>1</report_id></report_metadata><policy_published><domain>example.com</domain></policy_published>`
	cases := map[string][]byte{
		"records":      gzipDMARC(t, head, "<record/>", "</feedback>", 6_000_000),
		"auth results": gzipDMARC(t, head+"<record><auth_results>", "<dkim/>", "</auth_results></record></feedback>", 6_000_000),
	}
	for name, body := range cases {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		fb, err := decodeDMARCReport(body)
		runtime.ReadMemStats(&after)
		if name == "records" && (err != nil || len(fb.Records) != maxReportRecords) {
			t.Errorf("records: %v", err)
		}
		if name == "auth results" && err == nil {
			t.Errorf("auth results: decoded %d dkim results", len(fb.Records[0].Auth.DKIM))
		}
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 32<<20 {
			t.Errorf("%s: allocated %d MiB", name, alloc>>20)
		}
	}
}

func TestParseDMARCMessageCapsReportParts(t *testing.T) {
	msg := "From: a@b.c\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		strings.Repeat("--b\r\nContent-Type: text/xml\r\n\r\n"+sampleDMARC+"\r\n", maxReportParts+5) + "--b--\r\n"
	stored := 0
	if err := parseDMARCMessage(strings.NewReader(msg), func(*dmarcFeedback) error { stored++; return nil }); err != nil || stored != maxReportParts {
		t.Fatalf("stored %d reports, err %v", stored, err)
	}
}

func TestReportRecipientDomain(t *testing.T) {
	id := "0b4a3c2d-1e2f-4a5b-8c7d-9e0f1a2b3c4d"
	cases := map[string]string{
		"reports+" + id + "@aether-reports.internal":       id,
		"Reports+" + id + "+other@aether-reports.internal": id,
		"reports@aether-reports.internal":                  "",
		"reports+not-a-uuid@aether-reports.internal":       "",
		"list-" + id + "@aether-reports.internal":          "",
		"reports+" + id + "@example.com":                   "",
	}
	for rcpt, want := range cases {
		if got, ok := reportRecipientDomain(rcpt); ok != (want != "") || (ok && got != want) {
			t.Errorf("reportRecipientDomain(%q) = %q, %v", rcpt, got, ok)
		}
	}
}

func TestValidDMARCRecordAndTruncate(t *testing.T) {
	for ip, count := range map[string]int{"198.51.100.7": 3, "2001:db8::1": 0} {
		var rec dmarcRecord
		rec.Row.SourceIP, rec.Row.Count = ip, count
		if !validDMARCRecord(rec) {
			t.Errorf("%s %d rejected", ip, count)
		}
	}
	for ip, count := range map[string]int{"not-an-ip": 1, "fe80::1%eth0": 1, "192.0.2.1": 3_000_000_000, "192.0.2.2": -1} {
		var rec dmarcRecord
		rec.Row.SourceIP, rec.Row.Count = ip, count
		if validDMARCRecord(rec) {
			t.Errorf("%s %d accepted", ip, count)
		}
	}
	if got := truncate("aé", 2); got != "a" {
		t.Errorf("truncate split a rune: %q", got)
	}
}
