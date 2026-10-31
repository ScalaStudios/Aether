//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

type proxy struct {
	t    *testing.T
	base string
}

func dovecotProxy(t *testing.T, name string) *proxy {
	t.Helper()
	p := &proxy{t: t, base: env(t, "AETHER_TEST_TOXIPROXY", "") + "/proxies/" + name}
	t.Cleanup(p.restore)
	return p
}

func (p *proxy) call(method, path string, body any, want ...int) {
	p.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, p.base+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatalf("toxiproxy %s %s: %v", method, path, err)
	}
	resp.Body.Close()
	if !slices.Contains(want, resp.StatusCode) {
		p.t.Fatalf("toxiproxy %s %s: HTTP %d", method, path, resp.StatusCode)
	}
}

func (p *proxy) cut() {
	p.call("POST", "", map[string]any{"enabled": false}, 200)
}

func (p *proxy) hold(ms int) {
	p.call("POST", "/toxics", map[string]any{"name": "hold", "type": "timeout", "stream": "downstream", "toxicity": 1.0, "attributes": map[string]any{"timeout": ms}}, 200)
}

func (p *proxy) delay(ms int) {
	p.call("POST", "/toxics", map[string]any{"name": "delay", "type": "latency", "stream": "downstream", "toxicity": 1.0, "attributes": map[string]any{"latency": ms, "jitter": 0}}, 200)
}

func (p *proxy) restore() {
	p.call("DELETE", "/toxics/hold", nil, 204, 404)
	p.call("DELETE", "/toxics/delay", nil, 204, 404)
	p.call("POST", "", map[string]any{"enabled": true}, 200)
}
