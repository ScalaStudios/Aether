package mail

import (
	"strings"
	"testing"
)

func TestSanitizeHTMLRemovesActiveAndRemoteContent(t *testing.T) {
	in := `<p style="color:red;position:fixed">Hi</p><script>alert(1)</script><img src="https://tracker.example/p.gif"><a href="javascript:alert(1)">x</a><a href="https://example.com">ok</a><iframe src="https://evil"></iframe><div onclick="steal()">d</div><img src="data:image/png;base64,iVBORw0KGgo=">`
	out := SanitizeHTML(in)
	for _, banned := range []string{"<script", "tracker.example", "javascript:", "<iframe", "onclick", "position"} {
		if strings.Contains(out, banned) {
			t.Errorf("sanitized output still contains %q: %s", banned, out)
		}
	}
	for _, kept := range []string{"color: red", `href="https://example.com"`, `rel="nofollow noreferrer noopener"`, "data:image/png"} {
		if !strings.Contains(out, kept) {
			t.Errorf("sanitized output lost %q: %s", kept, out)
		}
	}
}
