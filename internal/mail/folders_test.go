package mail

import (
	"errors"
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func subscribed(t *testing.T, ic *imapclient.Client) []string {
	t.Helper()
	list, err := ic.List("", "*", &imap.ListOptions{SelectSubscribed: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range list {
		out = append(out, l.Mailbox)
	}
	slices.Sort(out)
	return out
}

func TestCreateAndRenameFolders(t *testing.T) {
	ic := memIMAP(t)
	if err := createFolder(ic, "Work"); err != nil {
		t.Fatal(err)
	}
	if err := createFolder(ic, "Work"); !errors.Is(err, ErrFolderExists) {
		t.Errorf("creating Work twice = %v, want ErrFolderExists", err)
	}
	if err := createFolder(ic, "Ghost/Child"); !errors.Is(err, ErrNotFound) {
		t.Errorf("creating under a missing parent = %v, want ErrNotFound", err)
	}
	if err := createFolder(ic, SnoozedFolder); err != nil {
		t.Fatal(err)
	}
	if err := renameFolder(ic, "Work", "Clients"); err != nil {
		t.Fatal(err)
	}
	if got := subscribed(t, ic); !slices.Equal(got, []string{"Clients", SnoozedFolder}) {
		t.Errorf("subscribed after rename = %v, want [Clients Snoozed]", got)
	}
	for _, name := range []string{"INBOX", SnoozedFolder} {
		if err := renameFolder(ic, name, "Other"); !errors.Is(err, ErrProtectedFolder) {
			t.Errorf("renaming %s = %v, want ErrProtectedFolder", name, err)
		}
	}
	if err := renameFolder(ic, "Missing", "Other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("renaming a missing folder = %v, want ErrNotFound", err)
	}
}

func TestDeleteFolderRefusesContentUnlessMoved(t *testing.T) {
	ic := memIMAP(t)
	for _, name := range []string{"Old", "Old/Child"} {
		if err := createFolder(ic, name); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage(t, ic, "Old", "kept@example.com", imap.FlagFlagged)
	if err := deleteFolder(ic, "Old", ""); !errors.Is(err, ErrFolderHasChildren) {
		t.Errorf("deleting a folder with subfolders = %v, want ErrFolderHasChildren", err)
	}
	if err := deleteFolder(ic, "Old/Child", ""); err != nil {
		t.Fatal(err)
	}
	if err := deleteFolder(ic, "Old", ""); !errors.Is(err, ErrFolderNotEmpty) {
		t.Errorf("deleting a folder with messages = %v, want ErrFolderNotEmpty", err)
	}
	if err := deleteFolder(ic, "Old", "INBOX"); err != nil {
		t.Fatal(err)
	}
	if inbox := folderMessages(t, ic, "INBOX"); !slices.Contains(inbox["kept@example.com"], imap.FlagFlagged) {
		t.Errorf("INBOX = %v, want the moved message", inbox)
	}
	if _, err := ic.Status("Old", &imap.StatusOptions{NumMessages: true}).Wait(); err == nil {
		t.Error("Old still exists")
	}
}
