//go:build integration

package integration

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

type orgBranding struct {
	Name             string                       `json:"name"`
	SignInMessage    string                       `json:"signInMessage"`
	Colors           map[string]map[string]string `json:"colors"`
	CustomCSS        string                       `json:"customCss"`
	LogoLight        *string                      `json:"logoLight"`
	CustomCSSEnabled bool                         `json:"customCssEnabled"`
}

type resolvedBranding struct {
	OrgID        string  `json:"orgId"`
	Name         string  `json:"name"`
	CustomCSSURL *string `json:"customCssUrl"`
	LogoLight    *string `json:"logoLight"`
}

func putBody(t *testing.T, c *apiClient, path string, body []byte) (int, []byte) {
	req, _ := http.NewRequest("PUT", c.base+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func fetch(t *testing.T, url string) (int, string, string) {
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
}

func TestBranding(t *testing.T) {
	w := setupWorld(t)
	path := "/organizations/" + w.orgID + "/branding"
	host := "mail." + w.domain
	anon := newClient(t)
	origin := strings.TrimSuffix(anon.base, "/api/v1")

	var current orgBranding
	w.admin.must("GET", path, nil, &current, 200)
	if current.Name != "" || current.LogoLight != nil || current.Colors["dark"] == nil || current.Colors["light"] == nil {
		t.Fatalf("a new organization should have empty branding with both themes: %+v", current)
	}
	if body := strings.TrimSpace(string(anon.raw("GET", "/branding?host="+host))); body != "null" {
		t.Fatalf("an organization without branding should leave Aether's look, got %s", body)
	}

	settings := map[string]any{"name": "Lunar", "signInMessage": "Locked out? Call IT.", "colors": map[string]any{"dark": map[string]string{"accent": "#3366FF"}}, "customCss": "header { color: #333 }"}
	if code := w.bob.do("PUT", path, settings, nil); code != 403 {
		t.Fatalf("a member changed branding: %d", code)
	}
	for _, bad := range []map[string]any{
		{"name": "x", "signInMessage": "", "colors": map[string]any{}, "customCss": "a { background: url(https://evil.test/x.png) }"},
		{"name": "x", "signInMessage": "", "colors": map[string]any{}, "customCss": "@import '/x.css';"},
		{"name": "x", "signInMessage": "", "colors": map[string]any{"dark": map[string]string{"accent": "orange"}}, "customCss": ""},
		{"name": "x", "signInMessage": "", "colors": map[string]any{"sepia": map[string]string{}}, "customCss": ""},
		{"name": "x", "signInMessage": "", "colors": map[string]any{}, "customCss": strings.Repeat("a", 33<<10)},
	} {
		if code := w.admin.do("PUT", path, bad, nil); code != 422 {
			t.Errorf("branding %v was accepted: %d", bad, code)
		}
	}
	w.admin.must("PUT", path, settings, &current, 200)
	if current.Name != "Lunar" || current.Colors["dark"]["accent"] != "#3366ff" || len(current.Colors["light"]) != 0 {
		t.Fatalf("branding did not round trip: %+v", current)
	}

	if code, _ := putBody(t, w.admin, path+"/logo-light", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)); code != 422 {
		t.Fatalf("an svg logo should be refused, got %d", code)
	}
	if code, _ := putBody(t, w.admin, path+"/logo-light", picture(t, 3000)); code != 422 {
		t.Fatalf("a logo over 2048 pixels should be refused, got %d", code)
	}
	if code, _ := putBody(t, w.admin, path+"/logo-light", bytes.Repeat([]byte{0x89}, 300<<10)); code != 413 {
		t.Fatalf("a logo over 256 KB should be refused, got %d", code)
	}
	if code, body := putBody(t, w.admin, path+"/logo-light", picture(t, 64)); code != 200 {
		t.Fatalf("logo upload: %d %s", code, body)
	}

	var resolved resolvedBranding
	anon.must("GET", "/branding?host="+host, nil, &resolved, 200)
	if resolved.OrgID != w.orgID || resolved.Name != "Lunar" || resolved.LogoLight == nil || resolved.CustomCSSURL == nil {
		t.Fatalf("the sign-in page on %s should get the organization's branding: %+v", host, resolved)
	}
	if code, kind, _ := fetch(t, origin+*resolved.LogoLight); code != 200 || kind != "image/png" {
		t.Fatalf("logo should be public as png, got %d %q", code, kind)
	}
	if code, kind, css := fetch(t, origin+*resolved.CustomCSSURL); code != 200 || !strings.HasPrefix(kind, "text/css") || css != "header { color: #333 }" {
		t.Fatalf("custom css should be served as a stylesheet, got %d %q %q", code, kind, css)
	}
	if body := strings.TrimSpace(string(anon.raw("GET", "/branding?host=mail.not-"+w.domain))); body != "null" {
		t.Fatalf("a host on no organization's domain got branding: %s", body)
	}
	var mine resolvedBranding
	w.bob.must("GET", "/branding", nil, &mine, 200)
	if mine.OrgID != w.orgID {
		t.Fatalf("a signed-in member should get their organization's branding without a host: %+v", mine)
	}

	var other idOnly
	w.admin.must("POST", "/organizations", map[string]string{"name": "Other " + randomSuffix(), "slug": "other-" + randomSuffix()}, &other, 201)
	if code := w.bob.do("GET", "/organizations/"+other.ID+"/branding", nil, nil); code != 404 {
		t.Fatalf("someone outside the organization read its branding settings: %d", code)
	}

	w.admin.must("DELETE", path+"/logo-light", nil, &current, 200)
	if current.LogoLight != nil {
		t.Fatalf("logo was not removed: %+v", current)
	}
	w.admin.must("PUT", path, map[string]any{"name": "", "signInMessage": "", "colors": map[string]any{}, "customCss": ""}, &current, 200)
	if body := strings.TrimSpace(string(anon.raw("GET", "/branding?host="+host))); body != "null" {
		t.Fatalf("clearing every setting should bring back Aether's look, got %s", body)
	}
	var audit []struct {
		Action string `json:"action"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &audit, 200)
	seen := map[string]bool{}
	for _, e := range audit {
		seen[e.Action] = true
	}
	if !seen["branding.update"] || !seen["branding.image.update"] || !seen["branding.image.remove"] {
		t.Fatalf("branding changes are missing from the audit log: %v", seen)
	}
}

func TestSignatureTemplates(t *testing.T) {
	w := setupWorld(t)
	base := "/organizations/" + w.orgID + "/signature-templates"
	mine := "/mailboxes/" + w.aliceMailbox + "/signature-template"
	var out struct {
		HTML string `json:"html"`
	}

	w.alice.must("PUT", "/me/profile", map[string]string{"title": "Engineer <b>", "phone": "+1 555 0100"}, nil, 200)
	w.alice.must("GET", mine, nil, &out, 200)
	for _, want := range []string{"Alice", "Engineer &lt;b&gt;", "+1 555 0100", "alice@" + w.domain, "Integration "} {
		if !strings.Contains(out.HTML, want) {
			t.Errorf("default template is missing %q: %s", want, out.HTML)
		}
	}
	if strings.Contains(out.HTML, "<img") || strings.Contains(out.HTML, "{{") || strings.Contains(out.HTML, "border-left") {
		t.Fatalf("an organization without a logo should get no logo column: %s", out.HTML)
	}
	if code := w.bob.do("GET", mine, nil, nil); code != 404 {
		t.Fatalf("bob filled in alice's template: %d", code)
	}

	template := map[string]any{"html": `<p>{{displayName}} · {{organization}}</p>{{#links}}<p>{{links}}</p>{{/links}}{{#logo}}{{logo}}{{/logo}}<script>alert(1)</script>`, "links": []map[string]string{{"label": "Site", "url": "https://example.com"}}, "logoWidth": 80}
	if code := w.bob.do("PUT", base, template, nil); code != 403 {
		t.Fatalf("a member changed the template: %d", code)
	}
	if code := w.admin.do("PUT", base, map[string]any{"html": "<p>x</p>", "logoWidth": 64, "links": []map[string]string{{"label": "Bad", "url": "javascript:alert(1)"}}}, nil); code != 422 {
		t.Fatalf("a javascript: link was accepted: %d", code)
	}
	w.admin.must("PUT", base, template, nil, 200)
	if code, body := putBody(t, w.admin, "/organizations/"+w.orgID+"/branding/logo-light", picture(t, 64)); code != 200 {
		t.Fatalf("logo upload: %d %s", code, body)
	}
	w.alice.must("GET", mine, nil, &out, 200)
	if !strings.Contains(out.HTML, `href="https://example.com"`) || !strings.Contains(out.HTML, "data:image/png;base64,") || !strings.Contains(out.HTML, `width="80"`) || strings.Contains(out.HTML, "<script") {
		t.Fatalf("organization template was not filled in and sanitized: %s", out.HTML)
	}

	w.admin.must("PUT", base, map[string]any{"domainId": w.domainID, "html": "<p>Only {{domain}}</p>", "logoWidth": 64}, nil, 200)
	w.alice.must("GET", mine, nil, &out, 200)
	if !strings.Contains(out.HTML, "Only "+w.domain) {
		t.Fatalf("the domain's own template should win: %s", out.HTML)
	}
	w.admin.must("DELETE", base+"?domainId="+w.domainID, nil, nil, 204)
	w.alice.must("GET", mine, nil, &out, 200)
	if !strings.Contains(out.HTML, "Site") {
		t.Fatalf("removing the domain template should fall back to the organization's: %s", out.HTML)
	}

	w.admin.must("POST", base+"/preview", map[string]any{"mailboxId": w.bobMailbox, "html": "<p>{{displayName}}</p>", "logoWidth": 64}, &out, 200)
	if !strings.Contains(out.HTML, "Bob") {
		t.Fatalf("preview should use the chosen mailbox: %s", out.HTML)
	}
	other := setupWorld(t)
	if code := w.admin.do("POST", base+"/preview", map[string]any{"mailboxId": other.aliceMailbox, "html": "<p>{{address}}</p>", "logoWidth": 64}, nil); code != 422 {
		t.Fatalf("previewed with another organization's mailbox: %d", code)
	}
	var list struct {
		BuiltIn   struct{ HTML string } `json:"builtIn"`
		Templates []struct {
			DomainID *string `json:"domainId"`
		} `json:"templates"`
	}
	w.admin.must("GET", base, nil, &list, 200)
	if list.BuiltIn.HTML == "" || len(list.Templates) != 1 || list.Templates[0].DomainID != nil {
		t.Fatalf("expected the built-in layout and one organization-wide template: %+v", list)
	}
}
