package api

import (
	"slices"
	"testing"
)

func TestSCIMMemberValues(t *testing.T) {
	cases := []struct {
		in   any
		want []string
		ok   bool
	}{
		{nil, nil, true},
		{[]any{map[string]any{"value": " ABC "}, map[string]any{"value": "def", "display": "x"}}, []string{"abc", "def"}, true},
		{map[string]any{"value": "one"}, []string{"one"}, true},
		{"not members", nil, false},
	}
	for _, c := range cases {
		got, ok := scimMemberValues(c.in)
		if ok != c.ok || !slices.Equal(got, c.want) {
			t.Errorf("scimMemberValues(%v) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSCIMMemberPath(t *testing.T) {
	m := scimMemberPath.FindStringSubmatch(`members[value eq "2819c223-7f76-453a-919d-413861904646"]`)
	if m == nil || m[1] != "2819c223-7f76-453a-919d-413861904646" {
		t.Fatalf("member path not matched: %v", m)
	}
	if scimMemberPath.MatchString("members") || scimMemberPath.MatchString(`emails[value eq "x"]`) {
		t.Fatal("matched a path that is not a member filter")
	}
	if m := scimDisplayNameFilter.FindStringSubmatch(`displayName eq "Sales \"EU\""`); m == nil {
		t.Fatal("displayName filter not matched")
	} else if v, ok := scimFilterValue(m[1]); !ok || v != `Sales "EU"` {
		t.Fatalf("filter value %q %v", v, ok)
	}
}
