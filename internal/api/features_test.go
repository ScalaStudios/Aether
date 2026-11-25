package api

import (
	"slices"
	"strings"
	"testing"

	mailpkg "github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/migrations"
)

func TestFeatureListsMatchMigration(t *testing.T) {
	body, err := migrations.FS.ReadFile("0079_organization_features.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range organizationFeatures {
		if !strings.Contains(string(body), "'"+f+"'") {
			t.Errorf("migration does not allow feature %s", f)
		}
	}
	for _, f := range platformAdminFeatures {
		if !slices.Contains(organizationFeatures, f) {
			t.Errorf("platform administrator feature %s is not a known feature", f)
		}
	}
}

func TestOutsideForwardsAreStrippedFromSieve(t *testing.T) {
	rules := []mailpkg.Rule{{
		Name: "copy", Enabled: true, Match: "all",
		Conditions: []mailpkg.RuleCondition{{Field: "from", Contains: "boss"}},
		Actions:    mailpkg.RuleActions{ForwardTo: []string{"team@example.com", "me@Gmail.com"}},
	}}
	policy := mailpkg.ForwardPolicy{Mode: "blocked", OrgDomains: []string{"example.com"}, FeatureOff: true}
	script := mailpkg.CompileSieve(rules, mailpkg.AutoReply{}, "a@example.com", policy, "")
	if strings.Contains(script, "Gmail") || !strings.Contains(script, `redirect "team@example.com"`) {
		t.Fatalf("script still forwards outside or lost the inside forward:\n%s", script)
	}
	if len(rules[0].Actions.ForwardTo) != 2 {
		t.Fatal("stripping forwards changed the stored rules")
	}
}
