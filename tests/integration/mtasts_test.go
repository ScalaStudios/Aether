//go:build integration

package integration

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMTASTSPolicy(t *testing.T) {
	admin := adminClient(t)
	suffix := randomSuffix()
	var org, dom idOnly
	admin.must("POST", "/organizations", map[string]string{"name": "STS " + suffix, "slug": "sts-" + suffix}, &org, 201)
	domain := "sts" + suffix + ".test"
	admin.must("POST", "/organizations/"+org.ID+"/domains", map[string]string{"name": domain}, &dom, 201)

	base := env(t, "AETHER_TEST_API", "http://api:8080")
	get := func(path, host string) (int, string) {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	code, policy := get("/.well-known/mta-sts.txt", "mta-sts."+domain)
	mailHost := env(t, "AETHER_TEST_MAIL_HOSTNAME", "mail.aether.test")
	if code != 200 || !strings.HasPrefix(policy, "version: STSv1\r\n") || !strings.Contains(policy, "\r\nmx: "+mailHost+"\r\n") {
		t.Fatalf("policy for %s: %d %q", domain, code, policy)
	}
	if code, _ := get("/.well-known/mta-sts.txt", "mta-sts.unknown-"+suffix+".test"); code != 404 {
		t.Fatalf("policy for an unknown domain returned %d", code)
	}
	if code, _ := get("/internal/tls-ask?domain=mta-sts."+domain, "api"); code != 200 {
		t.Fatalf("tls ask for mta-sts.%s returned %d", domain, code)
	}
	if code, _ := get("/internal/tls-ask?domain="+domain, "api"); code != 404 {
		t.Fatalf("tls ask for the bare domain returned %d", code)
	}

	var dns struct {
		Records []struct{ Name, Value, Purpose string }
	}
	admin.must("GET", "/domains/"+dom.ID+"/dns", nil, &dns, 200)
	purposes := map[string]string{}
	for _, r := range dns.Records {
		purposes[r.Purpose] = r.Name + " " + r.Value
	}
	if !strings.HasPrefix(purposes["mta-sts"], "_mta-sts."+domain+" v=STSv1; id=") || !strings.HasPrefix(purposes["tls-rpt"], "_smtp._tls."+domain+" v=TLSRPTv1;") || !strings.HasPrefix(purposes["mta-sts-host"], "mta-sts."+domain+" ") {
		t.Fatalf("DNS guidance is missing MTA-STS or TLS-RPT records: %v", purposes)
	}
}
