//go:build integration

package integration

import (
	"strings"
	"testing"
)

type dkimKey struct {
	ID          string `json:"id"`
	Selector    string `json:"selector"`
	Algorithm   string `json:"algorithm"`
	Bits        int    `json:"bits"`
	State       string `json:"state"`
	DNSName     string `json:"dnsName"`
	Record      string `json:"record"`
	RotationDue bool   `json:"rotationDue"`
}

const testEd25519Key = `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEIBPbJfrZpmELN3oOnx055OU5yrcn4kEoftPSyxttXj82
-----END PRIVATE KEY-----`

func TestDKIMKeyLifecycle(t *testing.T) {
	admin := adminClient(t)
	suffix := randomSuffix()
	var org idOnly
	admin.must("POST", "/organizations", map[string]string{"name": "DKIM " + suffix, "slug": "dkim-" + suffix}, &org, 201)
	domain := "dkim" + suffix + ".test"
	var dom struct {
		ID           string `json:"id"`
		DKIMSelector string `json:"dkimSelector"`
	}
	admin.must("POST", "/organizations/"+org.ID+"/domains", map[string]string{"name": domain}, &dom, 201)
	base := "/domains/" + dom.ID + "/dkim"

	var keys []dkimKey
	admin.must("GET", base, nil, &keys, 200)
	if len(keys) != 1 || keys[0].State != "active" || keys[0].Algorithm != "rsa" || keys[0].Bits != 2048 || keys[0].Selector != dom.DKIMSelector || keys[0].RotationDue {
		t.Fatalf("new domain keys: %+v, domain selector %s", keys, dom.DKIMSelector)
	}
	current := keys[0]

	var rotated dkimKey
	admin.must("POST", base, map[string]any{"algorithm": "rsa", "bits": 4096}, &rotated, 201)
	if rotated.State != "pending" || rotated.Bits != 4096 || rotated.Selector == current.Selector || !strings.HasPrefix(rotated.Record, "v=DKIM1; k=rsa; p=") {
		t.Fatalf("rotation key: %+v", rotated)
	}
	admin.must("POST", base, map[string]any{"algorithm": "rsa"}, nil, 409)
	admin.must("POST", base+"/"+rotated.ID+"/activate", nil, nil, 422)
	admin.must("DELETE", base+"/"+current.ID, nil, nil, 409)

	var dns struct {
		Records []struct{ Name, Value, Purpose string }
	}
	admin.must("GET", "/domains/"+dom.ID+"/dns", nil, &dns, 200)
	published := map[string]string{}
	for _, r := range dns.Records {
		if r.Purpose == "dkim" {
			published[r.Name] = r.Value
		}
	}
	if published[current.DNSName] != current.Record || published[rotated.DNSName] != rotated.Record || len(published) != 2 {
		t.Fatalf("DNS guidance during rotation: %v", published)
	}

	var imported dkimKey
	admin.must("POST", base, map[string]any{"selector": " S1 ", "privateKey": testEd25519Key}, &imported, 201)
	if imported.State != "pending" || imported.Algorithm != "ed25519" || imported.Selector != "s1" || imported.DNSName != "s1._domainkey."+domain || !strings.HasPrefix(imported.Record, "v=DKIM1; k=ed25519; p=") {
		t.Fatalf("imported key: %+v", imported)
	}
	admin.must("POST", base, map[string]any{"selector": "s2", "privateKey": "not a key"}, nil, 422)
	admin.must("POST", base, map[string]any{"selector": "bad selector", "privateKey": testEd25519Key}, nil, 422)
	admin.must("POST", base, map[string]any{"algorithm": "ed25519", "bits": 4096}, nil, 422)
	admin.must("POST", base, map[string]any{"algorithm": "rsa", "bits": 1024}, nil, 422)

	admin.must("DELETE", base+"/"+rotated.ID, nil, nil, 204)
	admin.must("DELETE", base+"/"+imported.ID, nil, nil, 204)
	admin.must("GET", base, nil, &keys, 200)
	if len(keys) != 1 || keys[0].ID != current.ID || keys[0].State != "active" {
		t.Fatalf("keys after cancelling: %+v", keys)
	}
}
