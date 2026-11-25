package mail

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
)

var keywordPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

var appleBits = []string{"$MailFlagBit0", "$MailFlagBit1", "$MailFlagBit2"}

var appleFlags = map[string][]string{
	"red":    {"$MailFlagBit0"},
	"orange": {"$MailFlagBit1"},
	"yellow": {"$MailFlagBit0", "$MailFlagBit1"},
	"green":  {"$MailFlagBit2"},
	"blue":   {"$MailFlagBit0", "$MailFlagBit2"},
	"purple": {"$MailFlagBit1", "$MailFlagBit2"},
	"gray":   {"$MailFlagBit0", "$MailFlagBit1", "$MailFlagBit2"},
	"pink":   {},
}

type LabelRef struct {
	ID       string
	Keyword  string
	Color    string
	Position int
}

func ValidLabelColor(color string) bool {
	_, ok := appleFlags[color]
	return ok
}

func ValidKeyword(keyword string) bool {
	return keywordPattern.MatchString(keyword)
}

func AppleFlags(color string) []string {
	flags := appleFlags[color]
	if len(flags) == 0 {
		return nil
	}
	return slices.Clone(flags)
}

func reservedKeyword(keyword string) bool {
	switch keyword {
	case "seen", "flagged", "answered", "deleted", "draft", "recent", "forwarded", "junk", "notjunk", "mdnsent":
		return true
	default:
		return false
	}
}

func Keyword(name string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			underscore = false
			continue
		}
		if b.Len() > 0 && !underscore {
			b.WriteByte('_')
			underscore = true
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" {
		s = "label"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "n_" + s
	}
	if reservedKeyword(s) {
		s = "tag_" + s
	}
	if len(s) > 63 {
		s = strings.Trim(s[:63], "_")
	}
	if !ValidKeyword(s) {
		return "label"
	}
	return s
}

func NextKeyword(name string, taken func(string) bool) string {
	base := Keyword(name)
	if !taken(base) {
		return base
	}
	for n := 2; n <= 999; n++ {
		suffix := "_" + strconv.Itoa(n)
		stem := base
		if len(stem)+len(suffix) > 63 {
			stem = strings.Trim(stem[:63-len(suffix)], "_")
			if stem == "" {
				stem = "label"
			}
		}
		candidate := stem + suffix
		if ValidKeyword(candidate) && !taken(candidate) {
			return candidate
		}
	}
	return ""
}

func hiddenKeyword(keyword string) bool {
	switch keyword {
	case "$MailFlagBit0", "$MailFlagBit1", "$MailFlagBit2", "$Forwarded", "$MDNSent", "$Junk", "$NotJunk", "$Submitted", "$Pending", "$Phishing", "$NotPhishing":
		return true
	default:
		return false
	}
}

func flagStrings(flags []imap.Flag) []string {
	out := make([]string, len(flags))
	for i, flag := range flags {
		out[i] = string(flag)
	}
	return out
}

func messageKeywords(flags []imap.Flag) []string {
	var out []string
	seen := map[string]bool{}
	for _, flag := range flags {
		keyword := string(flag)
		if keyword == "" || keyword[0] == '\\' || hiddenKeyword(keyword) || seen[keyword] {
			continue
		}
		seen[keyword] = true
		out = append(out, keyword)
	}
	slices.Sort(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func FlagColor(flags []string) string {
	have := map[string]bool{}
	for _, flag := range flags {
		have[flag] = true
	}
	for _, color := range []string{"red", "orange", "yellow", "green", "blue", "purple", "gray"} {
		bits := appleFlags[color]
		if len(bits) == 0 || !exactBits(have, bits) {
			continue
		}
		return color
	}
	return ""
}

func exactBits(have map[string]bool, bits []string) bool {
	need := map[string]bool{}
	for _, bit := range bits {
		if !have[bit] {
			return false
		}
		need[bit] = true
	}
	for _, bit := range appleBits {
		if have[bit] != need[bit] {
			return false
		}
	}
	return true
}

func DiffLabelFlags(current []string, catalog []LabelRef, addKeywords, dropKeywords []string) (add, remove []string) {
	drop := map[string]bool{}
	for _, keyword := range dropKeywords {
		drop[keyword] = true
	}
	byKeyword := map[string]LabelRef{}
	for _, label := range catalog {
		if !drop[label.Keyword] {
			byKeyword[label.Keyword] = label
		}
	}
	seen := map[string]bool{}
	var applied []LabelRef
	consider := func(keyword string) {
		if drop[keyword] || seen[keyword] {
			return
		}
		label, ok := byKeyword[keyword]
		if !ok {
			return
		}
		seen[keyword] = true
		applied = append(applied, label)
	}
	for _, keyword := range current {
		consider(keyword)
	}
	for _, keyword := range addKeywords {
		consider(keyword)
	}
	slices.SortStableFunc(applied, func(a, b LabelRef) int { return cmp.Compare(a.Position, b.Position) })

	want := map[string]bool{}
	var wanted []string
	keep := func(keyword string) {
		if keyword == "" || want[keyword] {
			return
		}
		want[keyword] = true
		wanted = append(wanted, keyword)
	}
	for _, label := range applied {
		keep(label.Keyword)
	}
	manageApple := len(applied) > 0
	if !manageApple {
		for _, keyword := range current {
			if drop[keyword] {
				manageApple = true
				break
			}
			if _, ok := byKeyword[keyword]; ok {
				manageApple = true
				break
			}
		}
	}
	if len(applied) > 0 {
		for _, bit := range AppleFlags(applied[0].Color) {
			keep(bit)
		}
	}
	managed := map[string]bool{}
	for _, label := range catalog {
		managed[label.Keyword] = true
	}
	for _, keyword := range dropKeywords {
		managed[keyword] = true
	}
	if manageApple {
		for _, bit := range appleBits {
			managed[bit] = true
		}
	}
	have := map[string]bool{}
	for _, keyword := range current {
		have[keyword] = true
	}
	for _, keyword := range wanted {
		if !have[keyword] {
			add = append(add, keyword)
		}
	}
	removed := map[string]bool{}
	for _, keyword := range current {
		if managed[keyword] && !want[keyword] && !removed[keyword] {
			removed[keyword] = true
			remove = append(remove, keyword)
		}
	}
	return add, remove
}

func WithLabelColors(rules []Rule, catalog []LabelRef) []Rule {
	color := map[string]string{}
	for _, label := range catalog {
		color[label.Keyword] = label.Color
	}
	out := make([]Rule, len(rules))
	for i, rule := range rules {
		out[i] = rule
		if len(rule.Actions.Labels) == 0 {
			continue
		}
		flags := slices.Clone(rule.Actions.Labels)
		if bits := AppleFlags(color[flags[0]]); len(bits) > 0 {
			flags = append(flags, bits...)
		}
		out[i].Actions.Labels = flags
	}
	return out
}
