package mailtrace

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLatestCopiesPrefersFinishedRotations(t *testing.T) {
	got := latestCopies([]string{
		"/l/mail.log.20261121-191850.gz",
		"/l/mail.log.20261115-153511.gz",
		"/l/mail.log.20261122-153512",
		"/l/mail.log.20261122-153512.gz",
	})
	want := []string{"/l/mail.log.20261115-153511.gz", "/l/mail.log.20261121-191850.gz", "/l/mail.log.20261122-153512"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestHeadIdentifiesLogsByTheirFirstLine(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	long := strings.Repeat("x", 700) + "\nnext\n"
	cases := map[string]string{
		write("short", "Nov 22 19:46:25 mail postfix/master[1]: daemon started\nmore\n"): "Nov 22 19:46:25 mail postfix/master[1]: daemon started\n",
		write("long", long):           long[:headSize],
		write("partial", "Nov 22 19"): "",
		write("empty", ""):            "",
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write([]byte("first line\nsecond\n"))
	w.Close()
	cases[write("rotated.gz", gz.String())] = "first line\n"
	for path, want := range cases {
		if got, err := head(path); err != nil || got != want {
			t.Errorf("head(%s) = %q, %v; want %q", filepath.Base(path), got, err, want)
		}
	}
}
