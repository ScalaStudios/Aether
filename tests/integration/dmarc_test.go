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
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	suffix := randomSuffix()
	var otherOrg, otherDomain idOnly
	w.admin.must("POST", "/organizations", map[string]string{"name": "Other " + suffix, "slug": "other-" + suffix}, &otherOrg, 201)
	otherName := "other" + suffix + ".test"
	w.admin.must("POST", "/organizations/"+otherOrg.ID+"/domains", map[string]string{"name": otherName}, &otherDomain, 201)
	part := func(domain string) string {
		report := fmt.Sprintf(`<?xml version="1.0"?><feedback><report_metadata><org_name>receiver.example</org_name><report_id>x-%s</report_id>
<date_range><begin>%d</begin><end>%d</end></date_range></report_metadata><policy_published><domain>%s</domain><p>none</p></policy_published>
<record><row><source_ip>192.0.2.1</source_ip><count>1</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>pass</spf></policy_evaluated></row><identifiers><header_from>%s</header_from></identifiers></record>
</feedback>`, randomSuffix(), now-86400, now-60, domain, domain)
		return "--b\r\nContent-Type: text/xml\r\n\r\n" + report + "\r\n"
	}
	crossRcpt := "dmarc-reports@" + otherName
	cross := "From: reports@receiver.example\r\nTo: " + crossRcpt + "\r\nSubject: Report\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		part(w.domain) + part(otherName) + "--b--\r\n"
	if err := submit(t, smtpAddr, "", "", "reports@receiver.example", []string{crossRcpt}, cross, false); err != nil {
		t.Fatalf("delivering the report to another organization: %v", err)
	}
	var otherSummary struct{ Reports int }
	waitFor(t, "the other organization's report to be stored", func() bool {
		w.admin.must("GET", "/domains/"+otherDomain.ID+"/dmarc", nil, &otherSummary, 200)
		return otherSummary.Reports == 1
	})
	xml := fmt.Sprintf(`<?xml version="1.0"?><feedback><report_metadata><org_name>receiver.example</org_name><report_id>r-%s</report_id>
<date_range><begin>%d</begin><end>%d</end></date_range></report_metadata><policy_published><domain>%s</domain><p>quarantine</p></policy_published>
<record><row><source_ip>198.51.100.7</source_ip><count>9</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>fail</spf></policy_evaluated></row><identifiers><header_from>%s</header_from></identifiers></record>
<record><row><source_ip>203.0.113.9</source_ip><count>2</count><policy_evaluated><disposition>reject</disposition><dkim>fail</dkim><spf>fail</spf></policy_evaluated></row><identifiers><header_from>%s</header_from></identifiers></record>
<record><row><source_ip>not-an-ip</source_ip><count>1</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>pass</spf></policy_evaluated></row><identifiers><header_from>%s</header_from></identifiers></record>
<record><row><source_ip>192.0.2.9</source_ip><count>3000000000</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>pass</spf></policy_evaluated></row><identifiers><header_from>%s</header_from></identifiers></record>
</feedback>`, randomSuffix(), now-86400, now-60, w.domain, w.domain, w.domain, w.domain, w.domain)
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	gw.Write([]byte(xml))
	gw.Close()
	rcpt := "dmarc-reports@" + w.domain
	msg := "From: reports@receiver.example\r\nTo: " + rcpt + "\r\nSubject: Report domain: " + w.domain + "\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: application/gzip; name=report.xml.gz\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(gz.Bytes()) + "\r\n"
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
		t.Fatalf("a report was stored twice, under another organization's address, or sources are missing: %+v", summary)
	}
	top, second := summary.Sources[0], summary.Sources[1]
	if top.SourceIP != "198.51.100.7" || top.Messages != 9 || top.DKIMAligned != 9 || top.SPFAligned != 0 || top.Reporters[0] != "receiver.example" {
		t.Fatalf("unexpected top source %+v", top)
	}
	if second.Messages != 2 || second.Rejected != 2 {
		t.Fatalf("unexpected second source %+v", second)
	}
}
