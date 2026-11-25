package mail

import (
	"strings"
	"testing"
)

func TestParseFileLinkIDs(t *testing.T) {
	id := "abcdefab-cdef-abcd-efab-cdefabcdefab"
	got := parseFileLinkIDs(id + " not-an-id " + strings.ToUpper(id))
	if len(got) != 1 || got[0] != id {
		t.Fatalf("parsed %#v", got)
	}
}
