package mail

import (
	"strings"
	"testing"

	"github.com/emersion/go-message"
)

func TestParseListUnsubscribe(t *testing.T) {
	list := "<mailto:list@example.com?subject=unsub>, <https://example.com/u?id=1>, <http://example.com/u>, <javascript:alert(1)>, <https://user:pass@example.com/u>"
	got := ParseListUnsubscribe(list, "List-Unsubscribe=One-Click")
	if !got.OneClick || len(got.HTTPS) != 1 || got.HTTPS[0] != "https://example.com/u?id=1" || len(got.HTTP) != 1 || len(got.Mailto) != 1 {
		t.Fatalf("parsed %+v", got)
	}
	folded := "List-Unsubscribe: <mailto:news@example.com?subject=unsubscribe>\r\nList-Unsubscribe-Post: (rfc8058) List-Unsubscribe=One-Click\r\n\r\n"
	e, err := message.Read(strings.NewReader(folded))
	if err != nil {
		t.Fatal(err)
	}
	got = ParseListUnsubscribe(headerJoined(e.Header, "List-Unsubscribe"), headerJoined(e.Header, "List-Unsubscribe-Post"))
	if !got.OneClick || len(got.Mailto) != 1 || len(got.HTTPS) != 0 {
		t.Fatalf("folded %+v", got)
	}
	both := ParseListUnsubscribe("<mailto:a@b.com?subject=hello,world>, <https://ex.com/u>", "list-unsubscribe=\"one-click\"")
	if !both.OneClick || len(both.Mailto) != 1 || both.Mailto[0] != "mailto:a@b.com?subject=hello,world" || len(both.HTTPS) != 1 {
		t.Fatalf("comma inside mailto %+v", both)
	}
	if ParseListUnsubscribe("<https://ex.com/u>", "List-Unsubscribe=Two-Click").OneClick {
		t.Fatal("accepted a post value other than One-Click")
	}
}

func TestParseMailto(t *testing.T) {
	to, subject, body, err := ParseMailto("mailto:News@Example.com?subject=unsubscribe&body=Please%20stop")
	if err != nil || to != "news@example.com" || subject != "unsubscribe" || body != "Please stop" {
		t.Fatalf("%s %s %s %v", to, subject, body, err)
	}
	to, subject, body, err = ParseMailto("mailto:list@example.com")
	if err != nil || to != "list@example.com" || subject != "Unsubscribe" || body != "Please unsubscribe this address." {
		t.Fatalf("defaults %s %s %s %v", to, subject, body, err)
	}
	for _, raw := range []string{
		"mailto:a@b.com?cc=c@d.com",
		"mailto:a@b.com?bcc=c@d.com",
		"mailto:a@b.com,c@d.com",
		"mailto:?to=a@b.com&to=c@d.com",
		"mailto:a@b.com?subject=line%0Abcc:x",
		"https://example.com/u",
		"mailto:not an address",
	} {
		if _, _, _, err := ParseMailto(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
