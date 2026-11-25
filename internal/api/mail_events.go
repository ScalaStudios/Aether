package api

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type dovecotEvent struct {
	Event  string `json:"event"`
	Fields struct {
		User        string `json:"user"`
		Session     string `json:"session"`
		RemoteIP    string `json:"remote_ip"`
		CmdName     string `json:"cmd_name"`
		CmdArgs     string `json:"cmd_args"`
		Mailbox     string `json:"mailbox"`
		OldMailbox  string `json:"old_mailbox"`
		NewMailbox  string `json:"new_mailbox"`
		UID         uint32 `json:"uid"`
		TaggedReply string `json:"tagged_reply"`
	} `json:"fields"`
}

type imapAccess struct {
	Target, Folder, Action    string
	Items                     []accessItem
	Meta                      map[string]any
	FetchAddress, FetchFolder string
}

func expandUIDs(set string) []uint32 {
	var out []uint32
	for _, part := range strings.Split(set, ",") {
		lo, hi, isRange := strings.Cut(part, ":")
		from, err1 := strconv.ParseUint(lo, 10, 32)
		to, err2 := from, error(nil)
		if isRange {
			to, err2 = strconv.ParseUint(hi, 10, 32)
		}
		if err1 != nil || err2 != nil || from == 0 || to == 0 {
			return nil
		}
		from, to = min(from, to), max(from, to)
		for uid := from; uid <= to; uid++ {
			if len(out) == maxAuditedMessages {
				return out
			}
			out = append(out, uint32(uid))
		}
	}
	return out
}

func copyUIDs(reply string) (src, dst []uint32) {
	_, rest, ok := strings.Cut(reply, "[COPYUID ")
	if !ok {
		return nil, nil
	}
	code, _, _ := strings.Cut(rest, "]")
	f := strings.Fields(code)
	if len(f) != 3 {
		return nil, nil
	}
	src, dst = expandUIDs(f[1]), expandUIDs(f[2])
	if len(src) != len(dst) {
		return nil, nil
	}
	return src, dst
}

func splitSharedFolder(login, folder string) (string, string, bool) {
	rest, ok := strings.CutPrefix(folder, "Shared/")
	if !ok {
		return login, folder, false
	}
	address, sub, _ := strings.Cut(rest, "/")
	return strings.ToLower(address), cmp.Or(sub, "INBOX"), true
}

func imapArgs(args string) []string {
	var out []string
	for args = strings.TrimSpace(args); args != ""; args = strings.TrimSpace(args) {
		if args[0] != '"' {
			word, rest, _ := strings.Cut(args, " ")
			out, args = append(out, word), rest
			continue
		}
		var b strings.Builder
		i := 1
		for ; i < len(args) && args[i] != '"'; i++ {
			if args[i] == '\\' && i+1 < len(args) {
				i++
			}
			b.WriteByte(args[i])
		}
		out, args = append(out, b.String()), args[min(i+1, len(args)):]
	}
	return out
}

func (e *dovecotEvent) access() (imapAccess, bool) {
	f := e.Fields
	login := strings.ToLower(f.User)
	var a imapAccess
	switch e.Event {
	case "mail_opened":
		a.Action, a.Items = "message.read", []accessItem{{UID: f.UID}}
		a.Target, a.Folder, _ = splitSharedFolder(login, f.Mailbox)
		a.FetchAddress, a.FetchFolder = a.Target, a.Folder
		return a, a.Folder != "" && f.UID != 0
	case "mail_expunged":
		if strings.Contains(f.CmdName, "MOVE") {
			return a, false
		}
		a.Action, a.Items = "message.delete", []accessItem{{UID: f.UID}}
	case "imap_command_finished":
		switch f.CmdName {
		case "SELECT", "EXAMINE":
			a.Action = "folder.open"
		case "CREATE":
			a.Action = "folder.create"
		case "DELETE":
			a.Action = "folder.delete"
		case "RENAME":
			a.Action = "folder.rename"
			target, from, _ := splitSharedFolder(login, f.OldMailbox)
			newTarget, to, _ := splitSharedFolder(login, f.NewMailbox)
			if newTarget != target {
				to = f.NewMailbox
			}
			return imapAccess{Target: target, Folder: from, Action: a.Action, Meta: map[string]any{"to": to}}, from != ""
		case "COPY", "UID COPY", "MOVE", "UID MOVE":
			a.Action = "message.copy"
			if strings.Contains(f.CmdName, "MOVE") {
				a.Action = "message.move"
			}
			args := imapArgs(f.CmdArgs)
			if len(args) < 2 {
				return a, false
			}
			target, _, _ := splitSharedFolder(login, f.Mailbox)
			destTarget, destFolder, _ := splitSharedFolder(login, args[1])
			dest := destFolder
			if destTarget != target {
				dest = args[1]
			} else if a.Action == "message.move" && destFolder == "Trash" {
				a.Action = "message.delete"
			}
			a.Meta = map[string]any{"messages": args[0], "to": dest}
			if src, dst := copyUIDs(f.TaggedReply); src != nil {
				for i := range src {
					a.Items = append(a.Items, accessItem{UID: src[i], FetchUID: dst[i]})
				}
				a.FetchAddress, a.FetchFolder = destTarget, destFolder
			} else if strings.HasPrefix(f.CmdName, "UID ") {
				for _, uid := range expandUIDs(args[0]) {
					a.Items = append(a.Items, accessItem{UID: uid})
				}
			}
		default:
			return a, false
		}
	default:
		return a, false
	}
	a.Target, a.Folder, _ = splitSharedFolder(login, f.Mailbox)
	return a, a.Folder != ""
}

func (s *Server) receiveMailEvent(w http.ResponseWriter, r *http.Request) {
	_, key, _ := r.BasicAuth()
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err != nil || publicAddress(ap.Addr()) || !constantTimeEqual(key, s.cfg.DovecotAPIKey) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	defer w.WriteHeader(http.StatusNoContent)
	var e dovecotEvent
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(&e); err != nil {
		return
	}
	a, ok := e.access()
	if !ok {
		return
	}
	ctx := r.Context()
	shared := strings.HasPrefix(e.Fields.Mailbox, "Shared/") || strings.HasPrefix(e.Fields.OldMailbox, "Shared/")
	var actorID, actorEmail, client string
	err := s.db.QueryRow(ctx, `SELECT u.id, u.email, ms.client FROM mail_sessions ms JOIN users u ON u.id = ms.user_id WHERE ms.session = $1`, e.Fields.Session).
		Scan(&actorID, &actorEmail, &client)
	if errors.Is(err, pgx.ErrNoRows) && shared {
		client = "imap"
		err = s.db.QueryRow(ctx, `SELECT u.id, u.email FROM mailboxes m JOIN users u ON u.id = m.owner_user_id WHERE m.address = $1`, strings.ToLower(e.Fields.User)).
			Scan(&actorID, &actorEmail)
	}
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("resolving a mail event actor failed", "err", err)
		}
		return
	}
	var mailboxID, orgID, kind string
	var owner *string
	err = s.db.QueryRow(ctx, `SELECT id, org_id, kind, owner_user_id FROM mailboxes WHERE address = $1`, a.Target).Scan(&mailboxID, &orgID, &kind, &owner)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("resolving a mail event mailbox failed", "err", err)
		}
		return
	}
	role := "delegate"
	switch {
	case owner != nil && *owner == actorID:
		role = "owner"
	case kind == "shared":
		role = "member"
	}
	st, err := s.loadMailboxAudit(ctx, orgID)
	if err != nil {
		s.log.Error("loading mailbox audit settings failed", "org", orgID, "err", err)
		return
	}
	if !st.covers(role, a.Action) {
		return
	}
	s.writeAccess(ctx, accessRecord{
		OrgID: orgID, MailboxID: mailboxID, Address: a.Target, ActorID: &actorID, ActorEmail: actorEmail, Role: role,
		Client: client, IP: e.Fields.RemoteIP, Action: a.Action, Folder: a.Folder, Items: a.Items, Meta: a.Meta,
		FetchAddress: a.FetchAddress, FetchFolder: a.FetchFolder,
	})
}
