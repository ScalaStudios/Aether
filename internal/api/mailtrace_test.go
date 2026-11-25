package api

import (
	"slices"
	"testing"
)

func TestTraceCSVRowNeutralizesFormulas(t *testing.T) {
	got := traceCSVRow("=HYPERLINK(\"http://x\")", "+1", "-2", "@SUM(A1)", "\tx", "plain", "")
	want := []string{"'=HYPERLINK(\"http://x\")", "'+1", "'-2", "'@SUM(A1)", "'\tx", "plain", ""}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestTraceLikeEscapesWildcards(t *testing.T) {
	cases := map[string]string{"": "", "john_doe@x": `%john\_doe@x%`, "100%": `%100\%%`, `a\b`: `%a\\b%`}
	for in, want := range cases {
		if got := traceLike(in); got != want {
			t.Errorf("traceLike(%q) = %q, want %q", in, got, want)
		}
	}
}
