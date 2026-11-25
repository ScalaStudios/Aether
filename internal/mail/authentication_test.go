package mail

import "testing"

func TestParseAuthentication(t *testing.T) {
	a := ParseAuthentication([]string{"auth=none spf=pass spf.domain=bounce.example.com dmarc=fail dkim=pass dkim.domain=example.com"})
	if a == nil || a.Submission != "none" || a.SPF != (AuthResult{"pass", "bounce.example.com"}) || a.DKIM != (AuthResult{"pass", "example.com"}) || a.DMARC != (AuthResult{"fail", ""}) {
		t.Fatalf("parsed %+v", a)
	}
	for _, values := range [][]string{
		nil,
		{"auth=pass spf=none dmarc=none dkim=none", "auth=pass spf=none dmarc=none dkim=none"},
		{"auth=pass spf=none dmarc=none"},
		{"auth=admin spf=none dmarc=none dkim=none"},
		{"auth=none auth=pass spf=none dmarc=none dkim=none"},
		{"auth=none spf=pass spf.domain=Evil;dmarc=pass dmarc=none dkim=none"},
		{"auth=none spf=pass dmarc=none dkim=none extra=1"},
	} {
		if a := ParseAuthentication(values); a != nil {
			t.Errorf("ParseAuthentication(%q) = %+v, want nil", values, a)
		}
	}
}

func TestWithoutHeader(t *testing.T) {
	raw := "Received: from a\r\nX-Aether-Authentication: auth=pass\r\n  spf=pass\r\nSubject: hi\r\nx-aether-authentication : auth=pass\r\n\r\nX-Aether-Authentication: body line\r\n"
	want := "Received: from a\r\nSubject: hi\r\n\r\nX-Aether-Authentication: body line\r\n"
	if got := string(WithoutHeader([]byte(raw), AuthenticationHeader)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := string(WithoutHeader([]byte("Subject: hi\nX-Aether-Authentication: auth=pass\n\nbody"), AuthenticationHeader)); got != "Subject: hi\n\nbody" {
		t.Errorf("LF only: got %q", got)
	}
}
