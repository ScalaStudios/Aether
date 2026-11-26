package api

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func parseOffset(t *testing.T, s string) int {
	t.Helper()
	off, err := time.Parse("-0700", s)
	if err != nil {
		t.Fatalf("offset %q: %v", s, err)
	}
	_, secs := off.Zone()
	return secs
}

func TestVTimezoneMatchesEveryZone(t *testing.T) {
	root := "/usr/share/zoneinfo"
	if _, err := os.Stat(root); err != nil {
		t.Skip("no zoneinfo directory")
	}
	var ids []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() && (rel == "posix" || rel == "right") {
			return filepath.SkipDir
		}
		if !d.IsDir() && !strings.Contains(rel, ".") && rel != "posixrules" {
			ids = append(ids, rel)
		}
		return nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, id := range ids {
			loc, ok := validTimezone(id)
			if !ok {
				continue
			}
			tz := vtimezone(id, loc, 1970, 2100)
			prev := time.Time{}
			for i, o := range tz.Children {
				start := o.Props.Get(ical.PropDateTimeStart).Value
				fromText := o.Props.Get(ical.PropTimezoneOffsetFrom).Value
				toText := o.Props.Get(ical.PropTimezoneOffsetTo).Value
				from, want := parseOffset(t, fromText), parseOffset(t, toText)
				local, err := time.Parse("20060102T150405", start)
				if err != nil {
					t.Errorf("%s: DTSTART %q", id, start)
					return
				}
				onset := local.Add(-time.Duration(from) * time.Second)
				if i > 0 && !onset.After(prev) {
					t.Errorf("%s: observance %d at %s does not follow %s", id, i, onset, prev)
					return
				}
				prev = onset
				if _, got := onset.In(loc).Zone(); got != want && i > 0 {
					t.Errorf("%s: at %s offset %d, observance says %d", id, onset, got, want)
					return
				}
				if _, got := onset.Add(-time.Second).In(loc).Zone(); got != from && i > 0 {
					t.Errorf("%s: before %s offset %d, observance says %d", id, onset, got, from)
					return
				}
			}
			if len(tz.Children) == 0 || len(tz.Children) > maxObservances {
				t.Errorf("%s: %d observances", id, len(tz.Children))
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("generating time zones did not finish")
	}
	if len(ids) < 300 {
		t.Fatalf("only %d zones found", len(ids))
	}
}
