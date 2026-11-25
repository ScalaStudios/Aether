package maillog

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func appendLine(t *testing.T, path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(line)
	f.Close()
}

func TestFollowRotationAndResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mail.log")
	appendLine(t, path, "old\n")
	fi, _ := os.Stat(path)
	start := Position{Inode: inode(fi), Offset: 0}
	appendLine(t, path, "a\nb\npartial")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []string
	var last Position
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, path, start, func(lines []string, pos Position) error {
			got = append(got, lines...)
			last = pos
			if slices.Contains(lines, "d") {
				cancel()
			}
			return nil
		})
	}()
	time.Sleep(300 * time.Millisecond)
	appendLine(t, path, "-done\n")
	os.Rename(path, path+".20261123-100000")
	appendLine(t, path, "c\nd\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := []string{"old", "a", "b", "partial-done", "c", "d"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if last.Offset != 4 {
		t.Fatalf("position = %+v", last)
	}

	appendLine(t, path, "e\n")
	os.Rename(path, path+".20261124-100000")
	appendLine(t, path, "f\n")
	got = nil
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	Follow(ctx2, path, last, func(lines []string, pos Position) error {
		got = append(got, lines...)
		if slices.Contains(lines, "f") {
			cancel2()
		}
		return nil
	})
	if !slices.Equal(got, []string{"e", "f"}) {
		t.Fatalf("resumed lines = %q", got)
	}
}
