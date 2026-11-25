package mail

import (
	"strings"
	"testing"
)

func TestNormalizeImageURL(t *testing.T) {
	got, ok := NormalizeImageURL("https://CDN.Example/a.png?utm_source=email&id=1#x")
	if !ok || got != "https://cdn.example/a.png?id=1" {
		t.Fatalf("normalized = %q %v", got, ok)
	}
	again, ok := NormalizeImageURL(got)
	if !ok || again != got {
		t.Fatalf("not idempotent: %q", again)
	}
	if _, ok := NormalizeImageURL("//cdn.example/a.png"); !ok {
		t.Fatal("protocol-relative URL rejected")
	}
	for _, raw := range []string{
		"javascript:alert(1)",
		"https://user:pass@cdn.example/a.png",
		"https://127.0.0.1/a.png",
		"http://localhost/a.png",
		"https://cdn.example:8080/a.png",
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/a.png",
		"https://10.1.2.3/a.png",
		"https://metadata.google.internal/a.png",
		"file:///etc/passwd",
		"https://cdn.example/" + strings.Repeat("a", 2100),
	} {
		if _, ok := NormalizeImageURL(raw); ok {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestTrackingImageURL(t *testing.T) {
	for _, raw := range []string{
		"https://pixel.mailtrack.io/a.gif",
		"https://u1.ct.sendgrid.net/wf/open?upn=abc",
		"https://example.com/track/open.php?id=1",
		"https://example.com/ss/o/abc",
		"https://cdn.example/pixel.gif",
	} {
		if !IsTrackingImageURL(raw) {
			t.Errorf("not a tracker: %s", raw)
		}
	}
	if IsTrackingImageURL("https://cdn.example/logo.png") {
		t.Fatal("logo classified as a tracker")
	}
}

func TestPresentRemoteImages(t *testing.T) {
	in := `<p style="color:red;background-image:url(https://tracker.example/p.gif)">Hi</p>` +
		`<script>alert(1)</script>` +
		`<img src="https://cdn.example/logo.png?utm_source=email" srcset="https://cdn.example/logo-2x.png 2x" alt="Logo">` +
		`<img src="https://pixel.mailtrack.io/o.gif">` +
		`<img src="https://news.example/t.gif?u=1" width="1" height="1">` +
		`<img src="https://cdn.example/dot.gif" width="1" height="1">` +
		`<img src="https://127.0.0.1/secret.png">` +
		`<img src="data:image/png;base64,iVBORw0KGgo=">` +
		`<a href="https://example.com">ok</a>` +
		`<video src="https://cdn.example/a.mp4"></video>` +
		`<img src="https://cdn.example/pixel.gif">`
	var asked []string
	out, stats := PresentRemoteImages(in, true, func(raw string) (string, bool) {
		asked = append(asked, raw)
		return "/api/v1/image-proxy/signed", true
	})
	if stats.Remote != 2 || stats.Trackers < 3 {
		t.Fatalf("stats = %+v, want 2 remote and at least 3 trackers", stats)
	}
	for _, banned := range []string{"<script", "tracker.example", "mailtrack.io", "127.0.0.1", "logo.png", "logo-2x.png", "open.php", "pixel.gif", "spacer.gif", "t.gif", "a.mp4", "url(", "dot.gif"} {
		if strings.Contains(out, banned) {
			t.Errorf("output still contains %q: %s", banned, out)
		}
	}
	for _, kept := range []string{"color:red", `href="https://example.com"`, "data:image/png", "/api/v1/image-proxy/signed", "Logo"} {
		if !strings.Contains(strings.ReplaceAll(out, " ", ""), strings.ReplaceAll(kept, " ", "")) && !strings.Contains(out, kept) {
			t.Errorf("output lost %q: %s", kept, out)
		}
	}
	if len(asked) != 2 {
		t.Fatalf("proxy asked for %q", asked)
	}
	hidden, hiddenStats := PresentRemoteImages(in, false, func(string) (string, bool) { return "", true })
	if hiddenStats.Remote != 2 || strings.Contains(hidden, "cdn.example") || strings.Contains(hidden, "image-proxy") {
		t.Fatalf("hidden = %+v %s", hiddenStats, hidden)
	}
}
