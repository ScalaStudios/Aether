//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

func TestDMARCAggregateReports(t *testing.T) {
	w := setupWorld(t)
	now := time.Now().Unix()
	xml := fmt.Sprintf(`<?xml version="1.0"?><feedback><report_metadata><org_name>receiver.example</org_name><report_id>r-%s</report_id>
<date_range><begin>%d</begin><end>%d</end></date_range></report_metadata><policy_published><domain>%s</domain><p>quarantine</p></policy_published>
<record><row><source_ip>198.51.100.7</source_ip><count>9</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>fail</spf></policy_evaluated></row><identifiers><header_from>%s</header_from></identifiers></record>
<record><row><source_ip>203.0.113.9</source_ip><count>2</count><policy_evaluated><disposition>reject</disposition><dkim>fail</dkim><spf>fail</spf></policy_evaluated></row><identifiers><header_from>%s</header_from></identifiers></record>
</feedback>`, randomSuffix(), now-86400, now-60, w.domain, w.domain, w.domain)
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	gw.Write([]byte(xml))
	gw.Close()
	rcpt := "dmarc-reports@" + w.domain
	msg := "From: reports@receiver.example\r\nTo: " + rcpt + "\r\nSubject: Report domain: " + w.domain + "\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: application/gzip; name=report.xml.gz\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(gz.Bytes()) + "\r\n"
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	for i := 0; i < 2; i++ {
		if err := submit(t, smtpAddr, "", "", "reports@receiver.example", []string{rcpt}, msg, false); err != nil {
			t.Fatalf("delivering the report: %v", err)
		}
	}

	var summary struct {
		Reports int
		Sources []struct {
			SourceIP    string
			Messages    int
			DKIMAligned int
			SPFAligned  int
			Rejected    int
			Reporters   []string
		}
	}
	waitFor(t, "the report to be stored", func() bool {
		w.admin.must("GET", "/domains/"+w.domainID+"/dmarc", nil, &summary, 200)
		return summary.Reports > 0
	})
	if summary.Reports != 1 || len(summary.Sources) != 2 {
		t.Fatalf("duplicate report was stored twice or sources are missing: %+v", summary)
	}
	top, second := summary.Sources[0], summary.Sources[1]
	if top.SourceIP != "198.51.100.7" || top.Messages != 9 || top.DKIMAligned != 9 || top.SPFAligned != 0 || top.Reporters[0] != "receiver.example" {
		t.Fatalf("unexpected top source %+v", top)
	}
	if second.Messages != 2 || second.Rejected != 2 {
		t.Fatalf("unexpected second source %+v", second)
	}
}
