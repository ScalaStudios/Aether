package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDovecotEventAccess(t *testing.T) {
	cases := []struct {
		raw  string
		want imapAccess
		ok   bool
	}{
		{`{"event":"imap_command_finished","fields":{"user":"Bob@x.test","cmd_name":"SELECT","mailbox":"Shared/Alice@x.test/INBOX"}}`,
			imapAccess{Target: "alice@x.test", Folder: "INBOX", Action: "folder.open"}, true},
		{`{"event":"imap_command_finished","fields":{"user":"alice@x.test","cmd_name":"EXAMINE","mailbox":"Archive/2026"}}`,
			imapAccess{Target: "alice@x.test", Folder: "Archive/2026", Action: "folder.open"}, true},
		{`{"event":"mail_opened","fields":{"user":"bob@x.test","cmd_name":"FETCH","mailbox":"Shared/alice@x.test/INBOX","uid":7}}`,
			imapAccess{Target: "alice@x.test", Folder: "INBOX", Action: "message.read", Items: []accessItem{{UID: 7}}, FetchAddress: "alice@x.test", FetchFolder: "INBOX"}, true},
		{`{"event":"mail_expunged","fields":{"user":"bob@x.test","mailbox":"Shared/alice@x.test/INBOX","uid":3}}`,
			imapAccess{Target: "alice@x.test", Folder: "INBOX", Action: "message.delete", Items: []accessItem{{UID: 3}}}, true},
		{`{"event":"mail_expunged","fields":{"user":"bob@x.test","cmd_name":"UID MOVE","mailbox":"Shared/alice@x.test/INBOX","uid":3}}`,
			imapAccess{}, false},
		{`{"event":"imap_command_finished","fields":{"user":"bob@x.test","cmd_name":"UID MOVE","cmd_args":"1:4 \"Shared/alice@x.test/Old \\\"mail\\\"\"","mailbox":"Shared/alice@x.test/INBOX"}}`,
			imapAccess{Target: "alice@x.test", Folder: "INBOX", Action: "message.move", Items: []accessItem{{UID: 1}, {UID: 2}, {UID: 3}, {UID: 4}}, Meta: map[string]any{"messages": "1:4", "to": `Old "mail"`}}, true},
		{`{"event":"imap_command_finished","fields":{"user":"bob@x.test","cmd_name":"COPY","cmd_args":"2 INBOX","mailbox":"Shared/alice@x.test/INBOX"}}`,
			imapAccess{Target: "alice@x.test", Folder: "INBOX", Action: "message.copy", Meta: map[string]any{"messages": "2", "to": "INBOX"}}, true},
		{`{"event":"imap_command_finished","fields":{"user":"bob@x.test","cmd_name":"COPY","cmd_args":"2:3 Shared/alice@x.test/Work","mailbox":"Shared/alice@x.test/INBOX","tagged_reply":"OK [COPYUID 17 5:6 41:42] Copy completed."}}`,
			imapAccess{Target: "alice@x.test", Folder: "INBOX", Action: "message.copy", Items: []accessItem{{UID: 5, FetchUID: 41}, {UID: 6, FetchUID: 42}}, Meta: map[string]any{"messages": "2:3", "to": "Work"}, FetchAddress: "alice@x.test", FetchFolder: "Work"}, true},
		{`{"event":"imap_command_finished","fields":{"user":"bob@x.test","cmd_name":"UID MOVE","cmd_args":"9 Shared/alice@x.test/Trash","mailbox":"Shared/alice@x.test/INBOX","tagged_reply":"OK Move completed."}}`,
			imapAccess{Target: "alice@x.test", Folder: "INBOX", Action: "message.delete", Items: []accessItem{{UID: 9}}, Meta: map[string]any{"messages": "9", "to": "Trash"}}, true},
		{`{"event":"imap_command_finished","fields":{"user":"bob@x.test","cmd_name":"UID COPY","cmd_args":"1:* INBOX","mailbox":"Archive"}}`,
			imapAccess{Target: "bob@x.test", Folder: "Archive", Action: "message.copy", Meta: map[string]any{"messages": "1:*", "to": "INBOX"}}, true},
		{`{"event":"imap_command_finished","fields":{"user":"bob@x.test","cmd_name":"RENAME","old_mailbox":"Shared/alice@x.test/Work/New","new_mailbox":"Shared/alice@x.test/Work/Done"}}`,
			imapAccess{Target: "alice@x.test", Folder: "Work/New", Action: "folder.rename", Meta: map[string]any{"to": "Work/Done"}}, true},
		{`{"event":"imap_command_finished","fields":{"user":"bob@x.test","cmd_name":"FETCH","mailbox":"INBOX"}}`, imapAccess{}, false},
		{`{"event":"imap_command_finished","fields":{"user":"bob@x.test","cmd_name":"SELECT","mailbox":"Shared/alice@x.test"}}`,
			imapAccess{Target: "alice@x.test", Folder: "INBOX", Action: "folder.open"}, true},
		{`{"event":"imap_command_finished","fields":{"user":"bob@x.test","cmd_name":"SELECT"}}`, imapAccess{}, false},
	}
	for _, c := range cases {
		var e dovecotEvent
		if err := json.Unmarshal([]byte(c.raw), &e); err != nil {
			t.Fatal(err)
		}
		got, ok := e.access()
		if ok != c.ok || (ok && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("%s: got %+v %v, want %+v %v", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestMailboxAuditCovers(t *testing.T) {
	off := mailboxAuditSettings{LogReads: true}
	on := mailboxAuditSettings{Enabled: true, LogReads: true}
	noReads := mailboxAuditSettings{Enabled: true}
	ownerDeletes := mailboxAuditSettings{Enabled: true, LogOwnerDeletes: true}
	cases := []struct {
		st           mailboxAuditSettings
		role, action string
		want         bool
	}{
		{off, "delegate", "message.delete", false},
		{on, "delegate", "message.read", true},
		{on, "member", "message.send_as", true},
		{on, "admin", "rules.update", true},
		{on, "owner", "message.read", false},
		{on, "owner", "message.delete", false},
		{noReads, "delegate", "folder.open", false},
		{noReads, "delegate", "message.move", true},
		{ownerDeletes, "owner", "message.delete", true},
		{ownerDeletes, "owner", "folder.empty", true},
		{ownerDeletes, "owner", "message.move", false},
	}
	for _, c := range cases {
		if got := c.st.covers(c.role, c.action); got != c.want {
			t.Errorf("%+v %s %s: got %v", c.st, c.role, c.action, got)
		}
	}
}

func TestExpandUIDs(t *testing.T) {
	if got := expandUIDs("3,7:5,9"); !reflect.DeepEqual(got, []uint32{3, 5, 6, 7, 9}) {
		t.Errorf("got %v", got)
	}
	for _, bad := range []string{"1:*", "0", "x", ""} {
		if got := expandUIDs(bad); got != nil {
			t.Errorf("%q: got %v", bad, got)
		}
	}
	if got := expandUIDs("1:100000"); len(got) != maxAuditedMessages {
		t.Errorf("not capped: %d", len(got))
	}
	if src, dst := copyUIDs("OK [COPYUID 1 1:2 5] done"); src != nil || dst != nil {
		t.Errorf("mismatched COPYUID accepted: %v %v", src, dst)
	}
}
