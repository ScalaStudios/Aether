package mail

import (
	"slices"
	"strings"
	"unicode"

	"github.com/emersion/go-imap/v2"
)

type ParsedQuery struct {
	Text           []string
	From           []string
	To             []string
	Filenames      []string
	HasAttachments bool
}

func ParseSearchQuery(raw string) ParsedQuery {
	runes := []rune(raw)
	var out ParsedQuery
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			i++
			continue
		}
		j := i
		for j < len(runes) && !unicode.IsSpace(runes[j]) && runes[j] != '"' && runes[j] != ':' {
			j++
		}
		key := ""
		if j < len(runes) && runes[j] == ':' {
			key = strings.ToLower(string(runes[i:j]))
		}
		switch key {
		case "from", "to", "filename", "has":
			val, next := readWord(runes, j+1)
			i = next
			switch {
			case val == "":
			case key == "from":
				out.From = append(out.From, val)
			case key == "to":
				out.To = append(out.To, val)
			case key == "filename":
				out.Filenames = append(out.Filenames, val)
			case strings.EqualFold(val, "attachment"):
				out.HasAttachments = true
			default:
				out.Text = append(out.Text, key+":"+val)
			}
		default:
			tok, next := readWord(runes, i)
			i = next
			if tok != "" {
				out.Text = append(out.Text, tok)
			}
		}
	}
	return out
}

func readWord(runes []rune, i int) (string, int) {
	var b strings.Builder
	for i < len(runes) && !unicode.IsSpace(runes[i]) {
		if runes[i] != '"' {
			b.WriteRune(runes[i])
			i++
			continue
		}
		for i++; i < len(runes) && runes[i] != '"'; i++ {
			b.WriteRune(runes[i])
		}
		i++
	}
	return b.String(), min(i, len(runes))
}

func matchesAttachments(bs imap.BodyStructure, filenames []string) bool {
	found := false
	var names []string
	if bs != nil {
		bs.Walk(func(path []int, part imap.BodyStructure) bool {
			if single, ok := part.(*imap.BodyStructureSinglePart); ok && isAttachment(single) {
				found = true
				names = append(names, strings.ToLower(single.Filename()))
			}
			return true
		})
	}
	return found && !slices.ContainsFunc(filenames, func(want string) bool {
		return !slices.ContainsFunc(names, func(name string) bool { return strings.Contains(name, strings.ToLower(want)) })
	})
}
