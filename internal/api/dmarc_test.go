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
