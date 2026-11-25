package api

import (
	"slices"
	"testing"
)

func TestNormalizeAttachmentRules(t *testing.T) {
	rules := attachmentRules{Action: "remove", Extensions: []string{" .EXE", "js", "exe", "7z"}}
	if msg := normalizeAttachmentRules(&rules); msg != "" {
		t.Fatalf("valid rules rejected: %s", msg)
	}
	if want := []string{"7z", "exe", "js"}; !slices.Equal(rules.Extensions, want) {
		t.Errorf("extensions = %v, want %v", rules.Extensions, want)
	}
	for _, bad := range []attachmentRules{
		{Action: "quarantine", Extensions: []string{"exe"}},
		{Action: "reject", Extensions: []string{"tar.gz"}},
		{Action: "reject", Extensions: []string{""}},
		{Action: "reject", Extensions: []string{"*"}},
	} {
		if normalizeAttachmentRules(&bad) == "" {
			t.Errorf("accepted invalid rules %+v", bad)
		}
	}
}
