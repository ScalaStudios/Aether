package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"

	mailpkg "github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/secrets"
)

var errUnsupportedFilter = errors.New("unsupported filter")

func (s *Server) jmapMailState(ctx context.Context, account *jmapAccount) (string, error) {
	_, state, err := s.loadMailboxes(ctx, account)
	return state, err
}

func (s *Server) loadMailboxes(ctx context.Context, account *jmapAccount) ([]map[string]any, string, error) {
	states, err := s.mail.MailboxStates(account.Address)
	if err != nil {
		mailBackendErrs.Inc()
		s.log.Error("jmap mailbox list failed", "account", account.ID, "err", err)
		return nil, "", err
	}
	names := make([]string, len(states))
	for i, st := range states {
		names[i] = st.Name
	}
	ids, err := s.ensureMailboxIDs(ctx, account.ID, names)
	if err != nil {
		return nil, "", err
	}
	byName := map[string]string{}
	for name, id := range ids {
		byName[name] = id
	}
	var parts []string
	list := make([]map[string]any, 0, len(states))
	for i, st := range states {
		parentID := ""
		short := st.Name
		if i := strings.LastIndex(st.Name, "/"); i > 0 {
			if id, ok := byName[st.Name[:i]]; ok {
				parentID = id
				short = st.Name[i+1:]
			}
		}
		obj := map[string]any{
			"id": ids[st.Name], "name": short, "parentId": nilOr(parentID), "role": nilOr(st.Role), "sortOrder": i,
			"totalEmails": st.Total, "unreadEmails": st.Unread, "isSubscribed": st.Subscribed,
			"myRights": mailboxRights(account),
		}
		list = append(list, obj)
		parts = append(parts, st.Name, ids[st.Name], strconv.FormatUint(uint64(st.UIDValidity), 10), strconv.FormatUint(uint64(st.UIDNext), 10), strconv.FormatUint(uint64(st.Total), 10), strconv.FormatUint(uint64(st.Unread), 10), strconv.FormatUint(st.HighestModSeq, 10), strconv.FormatBool(st.Subscribed))
	}
	return list, hashParts(parts...), nil
}

func nilOr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func mailboxRights(account *jmapAccount) map[string]bool {
	write := account.canWriteMail()
	return map[string]bool{
		"mayReadItems": write || account.Access == "read" || account.Access == "owner" || account.Access == "full",
		"mayAddItems":  write, "mayRemoveItems": write, "maySetSeen": write, "maySetKeywords": write,
		"mayCreateChild": write, "mayRename": write, "mayDelete": write, "maySubmit": account.canSend(),
	}
}

func (s *Server) jmapMailboxGet(r *http.Request, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	list, state, err := s.loadMailboxes(r.Context(), account)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	if stateErr := matchState(args["ifInState"], state); stateErr != nil {
		return nil, stateErr
	}
	props, _ := stringList(args["properties"])
	ids, idsOK := stringList(args["ids"])
	if !idsOK {
		return nil, &callError{Type: "invalidArguments", Description: "ids must be a list of strings or null."}
	}
	if ids == nil {
		for i := range list {
			list[i] = pickProps(list[i], props)
		}
		return map[string]any{"accountId": account.ID, "state": state, "list": list, "notFound": []string{}}, nil
	}
	byID := map[string]map[string]any{}
	for _, item := range list {
		byID[item["id"].(string)] = item
	}
	var out []map[string]any
	var missing []string
	for _, id := range ids {
		if item, ok := byID[id]; ok {
			out = append(out, pickProps(item, props))
		} else {
			missing = append(missing, id)
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	if missing == nil {
		missing = []string{}
	}
	return map[string]any{"accountId": account.ID, "state": state, "list": out, "notFound": missing}, nil
}

func matchState(v any, state string) *callError {
	s, ok := v.(string)
	if v == nil || (ok && s == "") {
		return nil
	}
	if !ok || s != state {
		return &callError{Type: "stateMismatch", Description: "The data changed. Fetch it again and retry."}
	}
	return nil
}

func (s *Server) jmapFail(err error) *callError {
	if errors.Is(err, mailpkg.ErrNotFound) {
		return &callError{Type: "notFound", Description: "The folder or message no longer exists."}
	}
	if errors.Is(err, mailpkg.ErrTooLarge) {
		return &callError{Type: "tooLarge", Description: "The message is larger than 25 MB."}
	}
	s.log.Error("jmap request failed", "err", err)
	mailBackendErrs.Inc()
	return &callError{Type: "serverFail", Description: "The mail server did not respond as expected. Try again in a moment."}
}

func (s *Server) jmapMailboxQuery(r *http.Request, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	if args["anchor"] != nil {
		return nil, &callError{Type: "invalidArguments", Description: "anchor is not supported. Page with position."}
	}
	list, state, err := s.loadMailboxes(r.Context(), account)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	filter, ferr := flattenFilter(rawJSON(args["filter"]))
	if ferr != nil {
		return nil, &callError{Type: "unsupportedFilter", Description: "This mailbox filter is not supported."}
	}
	for k := range filter {
		switch k {
		case "parentId", "name", "role", "isSubscribed", "hasAnyRole":
		default:
			return nil, &callError{Type: "unsupportedFilter", Description: "This mailbox filter is not supported."}
		}
	}
	filtered := list[:0]
	for _, item := range list {
		if mailboxMatches(item, filter) {
			filtered = append(filtered, item)
		}
	}
	if err := sortMailboxes(filtered, args["sort"]); err != nil {
		return nil, err
	}
	total := len(filtered)
	pos, limit, cerr := pageArgs(args)
	if cerr != nil {
		return nil, cerr
	}
	if pos > len(filtered) {
		pos = len(filtered)
	}
	filtered = filtered[pos:]
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	ids := make([]string, len(filtered))
	for i, item := range filtered {
		ids[i] = item["id"].(string)
	}
	out := map[string]any{"accountId": account.ID, "queryState": state, "canCalculateChanges": false, "position": pos, "ids": ids}
	if args["calculateTotal"] == true {
		out["total"] = total
	}
	return out, nil
}

func mailboxMatches(item map[string]any, filter map[string]json.RawMessage) bool {
	if raw, ok := filter["parentId"]; ok {
		if string(raw) == "null" {
			if item["parentId"] != nil {
				return false
			}
		} else {
			var want string
			if json.Unmarshal(raw, &want) != nil || item["parentId"] != want {
				return false
			}
		}
	}
	if raw, ok := filter["name"]; ok {
		var want string
		if json.Unmarshal(raw, &want) != nil || item["name"] != want {
			return false
		}
	}
	if raw, ok := filter["role"]; ok {
		if string(raw) == "null" {
			if item["role"] != nil {
				return false
			}
		} else {
			var want string
			if json.Unmarshal(raw, &want) != nil || item["role"] != want {
				return false
			}
		}
	}
	if raw, ok := filter["isSubscribed"]; ok {
		var want bool
		if json.Unmarshal(raw, &want) != nil || item["isSubscribed"] != want {
			return false
		}
	}
	if raw, ok := filter["hasAnyRole"]; ok {
		var want bool
		if json.Unmarshal(raw, &want) != nil || (item["role"] != nil) != want {
			return false
		}
	}
	for k := range filter {
		switch k {
		case "parentId", "name", "role", "isSubscribed", "hasAnyRole":
		default:
			return false
		}
	}
	return true
}

func sortMailboxes(list []map[string]any, sort any) *callError {
	rows, _ := sort.([]any)
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > 1 {
		return &callError{Type: "unsupportedSort", Description: "Sort mailboxes by one property."}
	}
	row, _ := rows[0].(map[string]any)
	prop, _ := row["property"].(string)
	asc := row["isAscending"] != false
	if prop != "name" && prop != "sortOrder" {
		return &callError{Type: "unsupportedSort", Description: "Sort mailboxes by name or sortOrder."}
	}
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			less := false
			if prop == "name" {
				less = list[j]["name"].(string) < list[i]["name"].(string)
			} else {
				less = list[j]["sortOrder"].(int) < list[i]["sortOrder"].(int)
			}
			if !asc {
				less = !less
			}
			if less {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	return nil
}

func pageArgs(args map[string]any) (int, int, *callError) {
	pos := 0
	if v, ok := args["position"]; ok && v != nil {
		n, ok := numberInt(v)
		if !ok || n < 0 {
			return 0, 0, &callError{Type: "invalidArguments", Description: "position must be zero or greater."}
		}
		pos = n
	}
	limit := 50
	if v, ok := args["limit"]; ok && v != nil {
		n, ok := numberInt(v)
		if !ok || n < 0 || n > 256 {
			return 0, 0, &callError{Type: "invalidArguments", Description: "limit must be from 0 to 256."}
		}
		limit = n
	}
	return pos, limit, nil
}

func rawJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}

func flattenFilter(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil, errUnsupportedFilter
	}
	if op, ok := obj["operator"]; ok {
		var name string
		if json.Unmarshal(op, &name) != nil || name != "AND" {
			return nil, errUnsupportedFilter
		}
		var conds []json.RawMessage
		if json.Unmarshal(obj["conditions"], &conds) != nil {
			return nil, errUnsupportedFilter
		}
		merged := map[string]json.RawMessage{}
		for _, c := range conds {
			inner, err := flattenFilter(c)
			if err != nil {
				return nil, err
			}
			for k, v := range inner {
				if _, exists := merged[k]; exists {
					return nil, errUnsupportedFilter
				}
				merged[k] = v
			}
		}
		return merged, nil
	}
	return obj, nil
}

func (s *Server) jmapMailboxSet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any, created map[string]string) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	if !account.canWriteMail() {
		return nil, &callError{Type: "forbidden", Description: "You have read-only access to this mailbox."}
	}
	list, state, err := s.loadMailboxes(r.Context(), account)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	if stateErr := matchState(args["ifInState"], state); stateErr != nil {
		return nil, stateErr
	}
	byID := map[string]map[string]any{}
	for _, item := range list {
		byID[item["id"].(string)] = item
	}
	folders := map[string]string{}
	for _, item := range list {
		folder, err := s.mailboxFolder(r.Context(), account.ID, item["id"].(string))
		if err == nil {
			folders[item["id"].(string)] = folder
		}
	}
	createdOut := map[string]any{}
	updated := map[string]any{}
	var destroyed []string
	notCreated := map[string]any{}
	notUpdated := map[string]any{}
	notDestroyed := map[string]any{}
	if create, ok := args["create"].(map[string]any); ok {
		if len(create) > jmapMaxObjs {
			return nil, &callError{Type: "invalidArguments", Description: "Create at most 128 mailboxes at a time."}
		}
		for cid, raw := range create {
			obj, _ := raw.(map[string]any)
			id, item, uerr := s.createJMAPMailbox(r.Context(), account, obj, byID, folders)
			if uerr != nil {
				notCreated[cid] = uerr
				continue
			}
			created[cid] = id
			createdOut[cid] = item
			byID[id] = item
		}
	}
	if update, ok := args["update"].(map[string]any); ok {
		for id, raw := range update {
			patch, _ := raw.(map[string]any)
			if uerr := s.updateJMAPMailbox(r.Context(), account, id, patch, folders); uerr != nil {
				notUpdated[id] = uerr
				continue
			}
			updated[id] = nil
		}
	}
	removeEmails := args["onDestroyRemoveEmails"] == true
	if destroy, ok := args["destroy"].([]any); ok {
		for _, raw := range destroy {
			id, _ := raw.(string)
			if uerr := s.destroyJMAPMailbox(r.Context(), account, id, folders, removeEmails); uerr != nil {
				notDestroyed[id] = uerr
				continue
			}
			destroyed = append(destroyed, id)
		}
	}
	_, newState, err := s.loadMailboxes(r.Context(), account)
	if err != nil {
		newState = state
	}
	return setResult(account.ID, state, newState, createdOut, updated, destroyed, notCreated, notUpdated, notDestroyed), nil
}

func setResult(account, old, newState string, created, updated map[string]any, destroyed []string, notCreated, notUpdated, notDestroyed map[string]any) map[string]any {
	if destroyed == nil {
		destroyed = []string{}
	}
	return map[string]any{
		"accountId": account, "oldState": old, "newState": newState,
		"created": created, "updated": updated, "destroyed": destroyed,
		"notCreated": notCreated, "notUpdated": notUpdated, "notDestroyed": notDestroyed,
	}
}

func (s *Server) createJMAPMailbox(ctx context.Context, account *jmapAccount, obj map[string]any, byID map[string]map[string]any, folders map[string]string) (string, map[string]any, map[string]any) {
	if obj == nil {
		return "", nil, itemError("invalidArguments", "A mailbox needs an object.")
	}
	if obj["role"] != nil {
		return "", nil, itemError("invalidProperties", "The server chooses mailbox roles.", "role")
	}
	name, _ := obj["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "/") || strings.ContainsAny(name, "\r\n") || len(name) > 400 {
		return "", nil, itemError("invalidProperties", "Use a folder name without slashes, of at most 400 characters.", "name")
	}
	full := name
	if parent, ok := obj["parentId"].(string); ok && parent != "" {
		base, ok := folders[parent]
		if !ok {
			return "", nil, itemError("invalidProperties", "The parent folder does not exist.", "parentId")
		}
		full = base + "/" + name
	}
	if err := s.mail.CreateFolder(account.Address, full); err != nil {
		return "", nil, s.itemMail(err)
	}
	if obj["isSubscribed"] == false {
		_ = s.mail.SetSubscribed(account.Address, full, false)
	}
	ids, err := s.ensureMailboxIDs(ctx, account.ID, []string{full})
	if err != nil {
		return "", nil, itemError("serverFail", "The folder was created but could not be recorded.")
	}
	folders[ids[full]] = full
	item := map[string]any{"id": ids[full], "name": name, "role": nil}
	return ids[full], item, nil
}

func (s *Server) updateJMAPMailbox(ctx context.Context, account *jmapAccount, id string, patch map[string]any, folders map[string]string) map[string]any {
	folder, ok := folders[id]
	if !ok || patch == nil {
		return itemError("notFound", "That folder does not exist.")
	}
	for k := range patch {
		switch k {
		case "name", "parentId", "isSubscribed":
		default:
			return itemError("invalidProperties", "Only name, parentId, and isSubscribed can be changed.", k)
		}
	}
	next := folder
	renamed := false
	if _, ok := patch["name"]; ok || patch["parentId"] != nil || hasKey(patch, "parentId") {
		short := folder
		if i := strings.LastIndex(folder, "/"); i >= 0 {
			short = folder[i+1:]
		}
		if name, ok := patch["name"].(string); ok {
			short = strings.TrimSpace(name)
		}
		if short == "" || strings.Contains(short, "/") || strings.ContainsAny(short, "\r\n") {
			return itemError("invalidProperties", "Use a folder name without slashes.", "name")
		}
		parent := ""
		if i := strings.LastIndex(folder, "/"); i >= 0 {
			parent = folder[:i]
		}
		if raw, ok := patch["parentId"]; ok {
			if raw == nil {
				parent = ""
			} else if pid, ok := raw.(string); ok && pid != "" {
				base, ok := folders[pid]
				if !ok {
					return itemError("invalidProperties", "The parent folder does not exist.", "parentId")
				}
				parent = base
			}
		}
		if parent == "" {
			next = short
		} else {
			next = parent + "/" + short
		}
		renamed = next != folder
	}
	if renamed {
		if err := s.mail.RenameFolder(account.Address, folder, next); err != nil {
			return s.itemMail(err)
		}
		if err := s.renameJMAPFolders(ctx, account.ID, folder, next); err != nil {
			return itemError("serverFail", "The folder was renamed but the index could not be updated.")
		}
		folders[id] = next
		folder = next
	}
	if v, ok := patch["isSubscribed"].(bool); ok {
		if err := s.mail.SetSubscribed(account.Address, folder, v); err != nil {
			return s.itemMail(err)
		}
	}
	return nil
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

func (s *Server) destroyJMAPMailbox(ctx context.Context, account *jmapAccount, id string, folders map[string]string, removeEmails bool) map[string]any {
	folder, ok := folders[id]
	if !ok {
		return itemError("notFound", "That folder does not exist.")
	}
	if removeEmails {
		if err := s.mail.EmptyFolder(account.Address, folder); err != nil && !errors.Is(err, mailpkg.ErrNotFound) {
			return s.itemMail(err)
		}
	}
	if err := s.mail.DeleteFolder(account.Address, folder, ""); err != nil {
		return s.itemMail(err)
	}
	if err := s.deleteJMAPFolder(ctx, account.ID, folder); err != nil {
		return itemError("serverFail", "The folder was deleted but the index could not be updated.")
	}
	delete(folders, id)
	return nil
}

func (s *Server) itemMail(err error) map[string]any {
	switch {
	case errors.Is(err, mailpkg.ErrNotFound):
		return itemError("notFound", "The folder or message no longer exists.")
	case errors.Is(err, mailpkg.ErrTooLarge):
		return itemError("tooLarge", "The message is larger than 25 MB.")
	case errors.Is(err, mailpkg.ErrProtectedFolder):
		return itemError("forbidden", "Inbox, Drafts, Sent, Archive, Junk, Trash, and Snoozed cannot be renamed or deleted.")
	case errors.Is(err, mailpkg.ErrFolderExists):
		return itemError("invalidProperties", "A folder with this name already exists.", "name")
	case errors.Is(err, mailpkg.ErrFolderHasChildren):
		return itemError("mailboxHasChild", "This folder has subfolders.")
	case errors.Is(err, mailpkg.ErrFolderNotEmpty):
		return itemError("mailboxHasEmail", "This folder still has messages. Destroy it with onDestroyRemoveEmails, or move the messages first.")
	case strings.Contains(strings.ToLower(err.Error()), "quota"):
		return itemError("overQuota", "The mailbox is over its quota.")
	default:
		mailBackendErrs.Inc()
		s.log.Error("jmap mail change failed", "err", err)
		return itemError("serverFail", "The mail server did not respond as expected.")
	}
}

func (s *Server) jmapEmailQuery(r *http.Request, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	if args["anchor"] != nil {
		return nil, &callError{Type: "invalidArguments", Description: "anchor is not supported. Page with position."}
	}
	filter, ferr := flattenFilter(rawJSON(args["filter"]))
	if ferr != nil {
		return nil, &callError{Type: "unsupportedFilter", Description: "This email filter is not supported. Filters can be combined with AND."}
	}
	mf, ferr := s.emailFilter(r.Context(), account, filter, args["sort"], args["collapseThreads"] == true)
	if ferr != nil {
		if errors.Is(ferr, errUnsupportedFilter) {
			return nil, &callError{Type: "unsupportedFilter", Description: ferr.Error()}
		}
		var ce *callError
		if errors.As(ferr, &ce) {
			return nil, ce
		}
		return nil, s.jmapFail(ferr)
	}
	hits, err := s.mail.SearchMessages(account.Address, mf)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	total := len(hits)
	pos, limit, cerr := pageArgs(args)
	if cerr != nil {
		return nil, cerr
	}
	if pos > len(hits) {
		pos = len(hits)
	}
	hits = hits[pos:]
	if len(hits) > limit {
		hits = hits[:limit]
	}
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		id, err := s.ensureEmailID(r.Context(), account.ID, hit, "")
		if err != nil {
			return nil, s.jmapFail(err)
		}
		ids = append(ids, id)
	}
	state, _ := s.jmapMailState(r.Context(), account)
	out := map[string]any{"accountId": account.ID, "queryState": state, "canCalculateChanges": false, "position": pos, "ids": ids}
	if args["calculateTotal"] == true {
		out["total"] = total
	}
	return out, nil
}

func (s *Server) emailFilter(ctx context.Context, account *jmapAccount, filter map[string]json.RawMessage, sort any, collapse bool) (mailpkg.MessageFilter, error) {
	var mf mailpkg.MessageFilter
	mf.Collapse = collapse
	mf.Descending = true
	mf.Sort = "date"
	if sort != nil {
		rows, _ := sort.([]any)
		if len(rows) > 1 {
			return mf, &callError{Type: "unsupportedSort", Description: "Sort email by one property."}
		}
		if len(rows) == 1 {
			row, _ := rows[0].(map[string]any)
			prop, _ := row["property"].(string)
			mapped := map[string]string{"receivedAt": "date", "sentAt": "sent", "size": "size", "from": "from", "to": "to", "subject": "subject"}[prop]
			if mapped == "" {
				return mf, &callError{Type: "unsupportedSort", Description: "Sort by receivedAt, sentAt, size, from, to, or subject."}
			}
			mf.Sort = mapped
			mf.Descending = row["isAscending"] == false
		}
	}
	for k, raw := range filter {
		switch k {
		case "inMailbox":
			var id string
			if json.Unmarshal(raw, &id) != nil {
				return mf, errUnsupportedFilter
			}
			folder, err := s.mailboxFolder(ctx, account.ID, id)
			if err != nil {
				return mf, &callError{Type: "invalidArguments", Description: "inMailbox is not a mailbox in this account."}
			}
			mf.Folders = []string{folder}
		case "inMailboxOtherThan":
			var ids []string
			if json.Unmarshal(raw, &ids) != nil {
				return mf, errUnsupportedFilter
			}
			states, err := s.mail.MailboxStates(account.Address)
			if err != nil {
				return mf, err
			}
			skip := map[string]bool{}
			for _, id := range ids {
				folder, err := s.mailboxFolder(ctx, account.ID, id)
				if err != nil {
					return mf, &callError{Type: "invalidArguments", Description: "inMailboxOtherThan contains an unknown mailbox."}
				}
				skip[folder] = true
			}
			for _, st := range states {
				if !skip[st.Name] {
					mf.Folders = append(mf.Folders, st.Name)
				}
			}
		case "text":
			json.Unmarshal(raw, &mf.Text)
		case "from":
			json.Unmarshal(raw, &mf.From)
		case "to":
			json.Unmarshal(raw, &mf.To)
		case "cc":
			json.Unmarshal(raw, &mf.Cc)
		case "subject":
			json.Unmarshal(raw, &mf.Subject)
		case "body":
			json.Unmarshal(raw, &mf.Body)
		case "hasAttachment":
			json.Unmarshal(raw, &mf.HasAttachment)
		case "after":
			t, err := parseJMAPTime(raw)
			if err != nil {
				return mf, err
			}
			mf.Since = t
		case "before":
			t, err := parseJMAPTime(raw)
			if err != nil {
				return mf, err
			}
			mf.Before = t
		case "minSize":
			var n int64
			if json.Unmarshal(raw, &n) != nil || n < 0 {
				return mf, errUnsupportedFilter
			}
			mf.MinSize = n
		case "maxSize":
			var n int64
			if json.Unmarshal(raw, &n) != nil || n < 0 {
				return mf, errUnsupportedFilter
			}
			mf.MaxSize = n
		case "hasKeyword", "notKeyword":
			var kw string
			if json.Unmarshal(raw, &kw) != nil {
				return mf, errUnsupportedFilter
			}
			flag, ok := keywordPtr(kw)
			if !ok {
				return mf, errUnsupportedFilter
			}
			on := k == "hasKeyword"
			switch flag {
			case imap.FlagSeen:
				mf.Seen = boolPtr(on)
			case imap.FlagFlagged:
				mf.Flagged = boolPtr(on)
			case imap.FlagDraft:
				mf.Draft = boolPtr(on)
			case imap.FlagAnswered:
				mf.Answered = boolPtr(on)
			default:
				mf.Forwarded = boolPtr(on)
			}
		case "inThread":
			var id string
			if json.Unmarshal(raw, &id) != nil {
				return mf, errUnsupportedFilter
			}
			root, single, err := decodeThread(id)
			if err != nil {
				return mf, &callError{Type: "invalidArguments", Description: "inThread is not a thread id."}
			}
			if single != "" {
				return mf, &callError{Type: "unsupportedFilter", Description: "This thread has no message id to search for."}
			}
			mf.ThreadMessageID = root
		default:
			return mf, errUnsupportedFilter
		}
	}
	return mf, nil
}

func boolPtr(v bool) *bool { return &v }

func keywordPtr(kw string) (imap.Flag, bool) {
	switch strings.ToLower(kw) {
	case "$seen":
		return imap.FlagSeen, true
	case "$flagged":
		return imap.FlagFlagged, true
	case "$draft":
		return imap.FlagDraft, true
	case "$answered":
		return imap.FlagAnswered, true
	case "$forwarded":
		return imap.Flag("$Forwarded"), true
	default:
		return "", false
	}
}

func parseJMAPTime(raw json.RawMessage) (time.Time, error) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return time.Time{}, errUnsupportedFilter
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z", s); err == nil {
		return t, nil
	}
	return time.Time{}, errUnsupportedFilter
}

func (s *Server) jmapEmailGet(r *http.Request, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	ids, idsOK := stringList(args["ids"])
	if !idsOK || ids == nil {
		return nil, &callError{Type: "invalidArguments", Description: "Email/get requires ids from Email/query."}
	}
	if len(ids) > jmapMaxObjs {
		return nil, &callError{Type: "requestTooLarge", Description: "Fetch at most 128 emails at a time."}
	}
	props, _ := stringList(args["properties"])
	want := emailWant(props)
	type located struct {
		id  string
		hit mailpkg.MessageHit
		mid string
	}
	var locs []located
	var hits []mailpkg.MessageHit
	var missing []string
	for _, id := range ids {
		hit, mid, err := s.emailHit(r.Context(), account.ID, id)
		if err != nil {
			missing = append(missing, id)
			continue
		}
		locs = append(locs, located{id: id, hit: hit, mid: mid})
		hits = append(hits, hit)
	}
	stored, err := s.mail.FetchStored(account.Address, hits, want)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	found := map[string]mailpkg.StoredMessage{}
	for _, m := range stored {
		found[m.Folder+"\x00"+strconv.FormatUint(uint64(m.UIDValidity), 10)+"\x00"+strconv.FormatUint(uint64(m.UID), 10)] = m
	}
	folders, _ := s.ensureMailboxIDs(r.Context(), account.ID, folderNames(stored))
	var list []map[string]any
	for _, loc := range locs {
		key := loc.hit.Folder + "\x00" + strconv.FormatUint(uint64(loc.hit.UIDValidity), 10) + "\x00" + strconv.FormatUint(uint64(loc.hit.UID), 10)
		msg, ok := found[key]
		if !ok && loc.mid != "" {
			if again, ferr := s.mail.FindMessage(account.Address, loc.mid); ferr == nil {
				_ = s.retargetEmail(r.Context(), account.ID, loc.id, again)
				if got, ferr := s.mail.FetchStored(account.Address, []mailpkg.MessageHit{again}, want); ferr == nil && len(got) == 1 {
					msg, ok = got[0], true
					if id, err := s.ensureMailboxIDs(r.Context(), account.ID, []string{msg.Folder}); err == nil {
						folders[msg.Folder] = id[msg.Folder]
					}
				}
			}
		}
		if !ok {
			_ = s.deleteEmailID(r.Context(), account.ID, loc.id)
			missing = append(missing, loc.id)
			continue
		}
		if msg.Envelope != nil && msg.Envelope.MessageID != "" {
			s.rememberMessageID(r.Context(), account.ID, loc.id, angle(msg.Envelope.MessageID))
		}
		list = append(list, pickProps(emailObject(loc.id, folders[msg.Folder], msg), props))
	}
	if list == nil {
		list = []map[string]any{}
	}
	if missing == nil {
		missing = []string{}
	}
	state, _ := s.jmapMailState(r.Context(), account)
	return map[string]any{"accountId": account.ID, "state": state, "list": list, "notFound": missing}, nil
}

func folderNames(msgs []mailpkg.StoredMessage) []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range msgs {
		if !seen[m.Folder] {
			seen[m.Folder] = true
			names = append(names, m.Folder)
		}
	}
	return names
}

func emailWant(props []string) mailpkg.FetchWant {
	if props == nil {
		return mailpkg.FetchWant{Preview: true, Text: true, HTML: true}
	}
	var w mailpkg.FetchWant
	for _, p := range props {
		switch p {
		case "preview":
			w.Preview = true
			w.HTML = true
		case "textBody", "bodyValues":
			w.Text = true
		case "htmlBody":
			w.HTML = true
		}
	}
	return w
}

func emailObject(id, mailboxID string, msg mailpkg.StoredMessage) map[string]any {
	thread, _ := threadOf(msg, id)
	obj := map[string]any{
		"id": id, "blobId": id, "threadId": thread, "mailboxIds": map[string]bool{}, "keywords": keywordsOf(msg.Flags),
		"size": msg.Size, "receivedAt": msg.Internal.UTC().Format(time.RFC3339), "hasAttachment": false,
		"preview": previewOf(msg.Parts),
	}
	if mailboxID != "" {
		obj["mailboxIds"] = map[string]bool{mailboxID: true}
	}
	if msg.Envelope != nil {
		env := msg.Envelope
		obj["subject"] = env.Subject
		obj["sentAt"] = env.Date.UTC().Format(time.RFC3339)
		obj["from"] = jmapAddresses(env.From)
		obj["to"] = jmapAddresses(env.To)
		obj["cc"] = jmapAddresses(env.Cc)
		obj["bcc"] = jmapAddresses(env.Bcc)
		obj["replyTo"] = jmapAddresses(env.ReplyTo)
		if env.MessageID != "" {
			obj["messageId"] = []string{angle(env.MessageID)}
		}
		if len(env.InReplyTo) > 0 {
			var ids []string
			for _, id := range env.InReplyTo {
				ids = append(ids, angle(id))
			}
			obj["inReplyTo"] = ids
		}
	}
	if len(msg.References) > 0 {
		obj["references"] = msg.References
	}
	var textBody, htmlBody, attachments []any
	bodyValues := map[string]any{}
	for _, p := range msg.Parts {
		part := map[string]any{"partId": p.Path, "blobId": id + "~" + p.Path, "size": p.Size, "type": p.Type}
		if p.Charset != "" {
			part["charset"] = p.Charset
		}
		if p.Name != "" {
			part["name"] = p.Name
		}
		if p.Disposition != "" {
			part["disposition"] = p.Disposition
		}
		if p.Attachment {
			obj["hasAttachment"] = true
			attachments = append(attachments, part)
			continue
		}
		if p.Text != "" || p.Truncated {
			bodyValues[p.Path] = map[string]any{"value": p.Text, "isEncodingProblem": false, "isTruncated": p.Truncated}
		}
		switch p.Type {
		case "text/plain":
			textBody = append(textBody, part)
		case "text/html":
			htmlBody = append(htmlBody, part)
		default:
			if p.Disposition == "attachment" || p.Name != "" {
				obj["hasAttachment"] = true
				attachments = append(attachments, part)
			}
		}
	}
	if textBody == nil {
		textBody = []any{}
	}
	if htmlBody == nil {
		htmlBody = []any{}
	}
	if attachments == nil {
		attachments = []any{}
	}
	obj["textBody"] = textBody
	obj["htmlBody"] = htmlBody
	obj["attachments"] = attachments
	obj["bodyValues"] = bodyValues
	return obj
}

func jmapAddresses(in []imap.Address) []any {
	out := []any{}
	for _, a := range in {
		if a.Addr() == "" {
			continue
		}
		item := map[string]any{"email": a.Addr()}
		if a.Name != "" {
			item["name"] = a.Name
		}
		out = append(out, item)
	}
	return out
}

func keywordsOf(flags []imap.Flag) map[string]bool {
	out := map[string]bool{}
	for _, f := range flags {
		switch f {
		case imap.FlagSeen:
			out["$seen"] = true
		case imap.FlagDraft:
			out["$draft"] = true
		case imap.FlagFlagged:
			out["$flagged"] = true
		case imap.FlagAnswered:
			out["$answered"] = true
		default:
			if strings.EqualFold(string(f), "$Forwarded") || (strings.HasPrefix(string(f), "$") && keywordOK(string(f))) {
				out[strings.ToLower(string(f))] = true
			}
		}
	}
	return out
}

func keywordOK(s string) bool {
	if len(s) < 2 || s[0] != '$' {
		return false
	}
	for _, r := range s[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

func flagsOf(keywords map[string]bool) (add []imap.Flag) {
	for kw, on := range keywords {
		if !on {
			continue
		}
		if f, ok := keywordPtr(kw); ok {
			add = append(add, f)
			continue
		}
		if keywordOK(kw) {
			add = append(add, imap.Flag(kw))
		}
	}
	return add
}

func previewOf(parts []mailpkg.StoredPart) string {
	var plain, html string
	for _, p := range parts {
		if p.Attachment {
			continue
		}
		if p.Type == "text/plain" && plain == "" {
			plain = p.Text
		}
		if p.Type == "text/html" && html == "" {
			html = p.Text
		}
	}
	s := plain
	if s == "" {
		s = stripTags(html)
	}
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 256 {
		s = string(r[:256])
	}
	return s
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func angle(id string) string {
	if id == "" || strings.HasPrefix(id, "<") {
		return id
	}
	return "<" + id + ">"
}

func threadOf(msg mailpkg.StoredMessage, emailID string) (string, string) {
	root := ""
	if len(msg.References) > 0 {
		root = msg.References[0]
	} else if msg.Envelope != nil && len(msg.Envelope.InReplyTo) > 0 {
		root = angle(msg.Envelope.InReplyTo[0])
	} else if msg.Envelope != nil && msg.Envelope.MessageID != "" {
		root = angle(msg.Envelope.MessageID)
	}
	if root == "" {
		return "th." + base64.RawURLEncoding.EncodeToString([]byte(emailID)), ""
	}
	return "th." + base64.RawURLEncoding.EncodeToString([]byte(root)), root
}

func decodeThread(id string) (root, single string, err error) {
	enc, ok := strings.CutPrefix(id, "th.")
	if !ok {
		return "", "", errors.New("bad thread")
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", "", err
	}
	s := string(raw)
	if strings.HasPrefix(s, "e.") && !strings.Contains(s, "@") {
		return "", s, nil
	}
	return s, "", nil
}

func (s *Server) jmapEmailSet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any, created map[string]string) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	if !account.canWriteMail() {
		return nil, &callError{Type: "forbidden", Description: "You have read-only access to this mailbox."}
	}
	state, _ := s.jmapMailState(r.Context(), account)
	if stateErr := matchState(args["ifInState"], state); stateErr != nil {
		return nil, stateErr
	}
	createdOut := map[string]any{}
	updated := map[string]any{}
	var destroyed []string
	notCreated := map[string]any{}
	notUpdated := map[string]any{}
	notDestroyed := map[string]any{}
	if create, ok := args["create"].(map[string]any); ok {
		if len(create) > jmapMaxObjs {
			return nil, &callError{Type: "invalidArguments", Description: "Create at most 128 emails at a time."}
		}
		for cid, raw := range create {
			obj, _ := raw.(map[string]any)
			id, result, uerr := s.createJMAPEmail(r.Context(), user, account, obj)
			if uerr != nil {
				notCreated[cid] = uerr
				continue
			}
			created[cid] = id
			createdOut[cid] = result
		}
	}
	if update, ok := args["update"].(map[string]any); ok {
		for id, raw := range update {
			patch, _ := raw.(map[string]any)
			result, uerr := s.updateJMAPEmail(r.Context(), account, id, patch)
			if uerr != nil {
				notUpdated[id] = uerr
				continue
			}
			updated[id] = result
		}
	}
	if destroy, ok := args["destroy"].([]any); ok {
		for _, raw := range destroy {
			id, _ := raw.(string)
			if uerr := s.destroyJMAPEmail(r.Context(), account, id); uerr != nil {
				notDestroyed[id] = uerr
				continue
			}
			destroyed = append(destroyed, id)
		}
	}
	newState, _ := s.jmapMailState(r.Context(), account)
	return setResult(account.ID, state, newState, createdOut, updated, destroyed, notCreated, notUpdated, notDestroyed), nil
}

func (s *Server) createJMAPEmail(ctx context.Context, user *jmapPrincipal, account *jmapAccount, obj map[string]any) (string, map[string]any, map[string]any) {
	if obj == nil {
		return "", nil, itemError("invalidArguments", "An email needs an object.")
	}
	folder, _, uerr := s.oneMailbox(ctx, account, obj["mailboxIds"], nil)
	if uerr != nil {
		return "", nil, uerr
	}
	kw, uerr := keywordMap(obj["keywords"])
	if uerr != nil {
		return "", nil, uerr
	}
	var raw []byte
	if blob, _ := obj["blobId"].(string); blob != "" {
		data, _, err := s.readUpload(ctx, user.UserID, account.ID, blob)
		if err != nil {
			return "", nil, itemError("blobNotFound", "The uploaded message is missing. Upload it again.")
		}
		if _, err := mail.ReadMessage(bytes.NewReader(data)); err != nil {
			return "", nil, itemError("invalidEmail", "The upload is not an RFC 5322 message.")
		}
		raw = data
	} else {
		built, uerr := buildEmail(user, account, obj)
		if uerr != nil {
			return "", nil, uerr
		}
		var err error
		raw, _, err = built.Build(s.cfg.MailHostname)
		if err != nil {
			return "", nil, itemError("invalidEmail", "The message could not be built.")
		}
	}
	when := time.Time{}
	if s, ok := obj["receivedAt"].(string); ok {
		when, _ = time.Parse(time.RFC3339, s)
	}
	validity, uid, err := s.mail.AppendMessage(account.Address, folder, flagsOf(kw), when, raw)
	if err != nil {
		return "", nil, s.itemMail(err)
	}
	id, err := s.ensureEmailID(ctx, account.ID, mailpkg.MessageHit{Folder: folder, UIDValidity: validity, UID: uid}, "")
	if err != nil {
		return "", nil, itemError("serverFail", "The message was saved but could not be indexed.")
	}
	return id, map[string]any{"id": id, "blobId": id, "threadId": "th." + base64.RawURLEncoding.EncodeToString([]byte(id)), "size": len(raw), "mailboxIds": obj["mailboxIds"], "keywords": kw}, nil
}

func (s *Server) oneMailbox(ctx context.Context, account *jmapAccount, raw any, base map[string]bool) (string, map[string]bool, map[string]any) {
	ids := map[string]bool{}
	for k, v := range base {
		ids[k] = v
	}
	switch t := raw.(type) {
	case map[string]any:
		for k, v := range t {
			if v == nil {
				delete(ids, k)
				continue
			}
			on, ok := v.(bool)
			if !ok || !on {
				return "", nil, itemError("invalidProperties", "mailboxIds values must be true or null.", "mailboxIds")
			}
			ids[k] = true
		}
	case nil:
	default:
		return "", nil, itemError("invalidProperties", "mailboxIds must be an object.", "mailboxIds")
	}
	if len(ids) != 1 {
		return "", nil, itemError("invalidProperties", "An email is stored in exactly one folder.", "mailboxIds")
	}
	var id string
	for k := range ids {
		id = k
	}
	folder, err := s.mailboxFolder(ctx, account.ID, id)
	if err != nil {
		return "", nil, itemError("invalidProperties", "That folder does not exist.", "mailboxIds")
	}
	return folder, map[string]bool{}, nil
}

func keywordMap(raw any) (map[string]bool, map[string]any) {
	if raw == nil {
		return map[string]bool{}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, itemError("invalidProperties", "keywords must be an object.", "keywords")
	}
	out := map[string]bool{}
	for k, v := range obj {
		if v == nil {
			continue
		}
		on, ok := v.(bool)
		if _, known := keywordPtr(k); !ok || !on || (!known && !keywordOK(k)) {
			return nil, itemError("invalidProperties", "Unknown keyword "+k+".", "keywords")
		}
		out[strings.ToLower(k)] = true
	}
	return out, nil
}

func buildEmail(user *jmapPrincipal, account *jmapAccount, obj map[string]any) (*mailpkg.Outgoing, map[string]any) {
	out := &mailpkg.Outgoing{From: mailpkg.Address{Name: account.Name, Address: account.Address}, Subject: str(obj["subject"])}
	if from, ok := obj["from"].([]any); ok && len(from) > 0 {
		addrs, err := parseJMAPAddrs(from)
		if err != nil || len(addrs) != 1 || !strings.EqualFold(addrs[0].Address, account.Address) {
			return nil, itemError("forbiddenFrom", "From must be this account's address.", "from")
		}
		out.From = addrs[0]
	}
	var uerr map[string]any
	if out.To, uerr = optionalAddrs(obj["to"], "to"); uerr != nil {
		return nil, uerr
	}
	if out.Cc, uerr = optionalAddrs(obj["cc"], "cc"); uerr != nil {
		return nil, uerr
	}
	if out.Bcc, uerr = optionalAddrs(obj["bcc"], "bcc"); uerr != nil {
		return nil, uerr
	}
	if len(out.To)+len(out.Cc)+len(out.Bcc) > 100 {
		return nil, itemError("invalidProperties", "A message can have at most 100 recipients.")
	}
	values, _ := obj["bodyValues"].(map[string]any)
	for _, part := range anyList(obj["textBody"]) {
		if text := bodyValue(values, part); text != "" {
			out.Text = text
			break
		}
	}
	for _, part := range anyList(obj["htmlBody"]) {
		if text := bodyValue(values, part); text != "" {
			out.HTML = text
			break
		}
	}
	if obj["textBody"] == nil && obj["htmlBody"] == nil {
		if text, _ := obj["textBodyValue"].(string); text != "" {
			out.Text = text
		}
	}
	if len(out.Text) > 1<<20 || len(out.HTML) > 1<<20 {
		return nil, itemError("tooLarge", "The message body is larger than 1 MB.")
	}
	if !account.SendAs && account.SendOnBehalf && user.Email != "" {
		out.Sender = &mailpkg.Address{Name: user.DisplayName, Address: user.Email}
	}
	return out, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func anyList(v any) []any {
	list, _ := v.([]any)
	return list
}

func bodyValue(values map[string]any, part any) string {
	obj, _ := part.(map[string]any)
	id, _ := obj["partId"].(string)
	val, _ := values[id].(map[string]any)
	text, _ := val["value"].(string)
	return text
}

func optionalAddrs(v any, prop string) ([]mailpkg.Address, map[string]any) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, itemError("invalidProperties", prop+" must be a list of addresses.", prop)
	}
	return parseJMAPAddrs(list)
}

func parseJMAPAddrs(list []any) ([]mailpkg.Address, map[string]any) {
	var out []mailpkg.Address
	for _, raw := range list {
		obj, _ := raw.(map[string]any)
		email, _ := obj["email"].(string)
		addr, err := mail.ParseAddress(strings.TrimSpace(email))
		if err != nil || addr.Name != "" {
			return nil, itemError("invalidProperties", email+" is not an email address.", "to")
		}
		name, _ := obj["name"].(string)
		out = append(out, mailpkg.Address{Name: name, Address: addr.Address})
	}
	return out, nil
}

func (s *Server) readUpload(ctx context.Context, user, account, id string) ([]byte, string, error) {
	if !strings.HasPrefix(id, "b.") {
		return nil, "", errors.New("not an upload")
	}
	typ, data, err := s.loadBlob(ctx, user, account, id)
	return data, typ, err
}

func (s *Server) updateJMAPEmail(ctx context.Context, account *jmapAccount, id string, patch map[string]any) (any, map[string]any) {
	hit, mid, err := s.emailHit(ctx, account.ID, id)
	if err != nil {
		return nil, itemError("notFound", "That message does not exist.")
	}
	if patch == nil {
		return nil, itemError("invalidPatch", "The update must be an object.")
	}
	curBox := map[string]bool{}
	ids, err := s.ensureMailboxIDs(ctx, account.ID, []string{hit.Folder})
	if err == nil {
		curBox[ids[hit.Folder]] = true
	}
	stored, err := s.mail.FetchStored(account.Address, []mailpkg.MessageHit{hit}, mailpkg.FetchWant{})
	keywords := map[string]bool{}
	if err == nil && len(stored) == 1 {
		keywords = keywordsOf(stored[0].Flags)
		if stored[0].Envelope != nil {
			mid = angle(stored[0].Envelope.MessageID)
			s.rememberMessageID(ctx, account.ID, id, mid)
		}
	}
	nextBox := map[string]bool{}
	for k, v := range curBox {
		nextBox[k] = v
	}
	nextKw := map[string]bool{}
	for k, v := range keywords {
		nextKw[k] = v
	}
	for k, v := range patch {
		switch {
		case k == "mailboxIds":
			obj, _ := v.(map[string]any)
			for id, flag := range obj {
				if flag == nil {
					delete(nextBox, id)
				} else if flag == true {
					nextBox[id] = true
				} else {
					return nil, itemError("invalidPatch", "mailboxIds values must be true or null.")
				}
			}
		case k == "keywords":
			obj, _ := v.(map[string]any)
			for kw, flag := range obj {
				if _, ok := keywordPtr(kw); !ok && !keywordOK(kw) {
					return nil, itemError("invalidPatch", "Unknown keyword "+kw+".")
				}
				if flag == nil {
					delete(nextKw, strings.ToLower(kw))
				} else if flag == true {
					nextKw[strings.ToLower(kw)] = true
				} else {
					return nil, itemError("invalidPatch", "keywords values must be true or null.")
				}
			}
		case strings.HasPrefix(k, "mailboxIds/"):
			box := k[len("mailboxIds/"):]
			if v == nil {
				delete(nextBox, box)
			} else if v == true {
				nextBox[box] = true
			} else {
				return nil, itemError("invalidPatch", "mailboxIds values must be true or null.")
			}
		case strings.HasPrefix(k, "keywords/"):
			kw := k[len("keywords/"):]
			if _, ok := keywordPtr(kw); !ok && !keywordOK(kw) {
				return nil, itemError("invalidPatch", "Unknown keyword "+kw+".")
			}
			if v == nil {
				delete(nextKw, strings.ToLower(kw))
			} else if v == true {
				nextKw[strings.ToLower(kw)] = true
			} else {
				return nil, itemError("invalidPatch", "keywords values must be true or null.")
			}
		default:
			return nil, itemError("invalidProperties", "Only keywords and mailboxIds can be changed.", k)
		}
	}
	if len(nextBox) != 1 {
		return nil, itemError("invalidProperties", "An email is stored in exactly one folder.", "mailboxIds")
	}
	var boxID string
	for k := range nextBox {
		boxID = k
	}
	folder, err := s.mailboxFolder(ctx, account.ID, boxID)
	if err != nil {
		return nil, itemError("invalidProperties", "That folder does not exist.", "mailboxIds")
	}
	add, remove := keywordDiff(keywords, nextKw)
	if len(add)+len(remove) > 0 {
		if err := s.mail.UpdateKeywords(account.Address, hit.Folder, hit.UIDValidity, hit.UID, add, remove); err != nil {
			return nil, s.itemMail(err)
		}
	}
	if folder != hit.Folder {
		validity, uid, err := s.mail.RelocateMessage(account.Address, hit.Folder, hit.UIDValidity, hit.UID, folder, false)
		if err != nil {
			return nil, s.itemMail(err)
		}
		if err := s.retargetEmail(ctx, account.ID, id, mailpkg.MessageHit{Folder: folder, UIDValidity: validity, UID: uid}); err != nil {
			return nil, itemError("serverFail", "The message moved but the index could not be updated.")
		}
	}
	return map[string]any{"id": id, "mailboxIds": nextBox, "keywords": nextKw}, nil
}

func keywordDiff(old, next map[string]bool) (add, remove []imap.Flag) {
	for kw := range next {
		if !old[kw] {
			if f, ok := keywordPtr(kw); ok {
				add = append(add, f)
			} else if keywordOK(kw) {
				add = append(add, imap.Flag(kw))
			}
		}
	}
	for kw := range old {
		if !next[kw] {
			if f, ok := keywordPtr(kw); ok {
				remove = append(remove, f)
			} else if keywordOK(kw) {
				remove = append(remove, imap.Flag(kw))
			}
		}
	}
	return add, remove
}

func (s *Server) destroyJMAPEmail(ctx context.Context, account *jmapAccount, id string) map[string]any {
	hit, _, err := s.emailHit(ctx, account.ID, id)
	if err != nil {
		return itemError("notFound", "That message does not exist.")
	}
	if err := s.mail.ExpungeMessage(account.Address, hit.Folder, hit.UIDValidity, hit.UID); err != nil {
		return s.itemMail(err)
	}
	_ = s.deleteEmailID(ctx, account.ID, id)
	return nil
}

func (s *Server) jmapEmailImport(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any, created map[string]string) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	if !account.canWriteMail() {
		return nil, &callError{Type: "forbidden", Description: "You have read-only access to this mailbox."}
	}
	emails, _ := args["emails"].(map[string]any)
	if len(emails) > jmapMaxObjs {
		return nil, &callError{Type: "invalidArguments", Description: "Import at most 128 emails at a time."}
	}
	out := map[string]any{}
	not := map[string]any{}
	state, _ := s.jmapMailState(r.Context(), account)
	for cid, raw := range emails {
		obj, _ := raw.(map[string]any)
		if obj == nil {
			not[cid] = itemError("invalidArguments", "Each import needs an object.")
			continue
		}
		id, result, uerr := s.createJMAPEmail(r.Context(), user, account, obj)
		if uerr != nil {
			not[cid] = uerr
			continue
		}
		created[cid] = id
		out[cid] = result
	}
	newState, _ := s.jmapMailState(r.Context(), account)
	return map[string]any{"accountId": account.ID, "oldState": state, "newState": newState, "created": out, "notCreated": not}, nil
}

func (s *Server) jmapThreadGet(r *http.Request, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	ids, idsOK := stringList(args["ids"])
	if !idsOK || ids == nil {
		return nil, &callError{Type: "invalidArguments", Description: "Thread/get requires ids."}
	}
	var list []map[string]any
	var missing []string
	for _, id := range ids {
		root, single, err := decodeThread(id)
		if err != nil {
			missing = append(missing, id)
			continue
		}
		var emailIDs []string
		if single != "" {
			if _, _, err := s.emailHit(r.Context(), account.ID, single); err != nil {
				missing = append(missing, id)
				continue
			}
			emailIDs = []string{single}
		} else {
			hits, err := s.mail.SearchMessages(account.Address, mailpkg.MessageFilter{ThreadMessageID: root, Sort: "date", Descending: true})
			if err != nil {
				return nil, s.jmapFail(err)
			}
			if len(hits) > 100 {
				hits = hits[:100]
			}
			if len(hits) == 0 {
				missing = append(missing, id)
				continue
			}
			for _, hit := range hits {
				eid, err := s.ensureEmailID(r.Context(), account.ID, hit, root)
				if err == nil {
					emailIDs = append(emailIDs, eid)
				}
			}
		}
		list = append(list, map[string]any{"id": id, "emailIds": emailIDs})
	}
	if list == nil {
		list = []map[string]any{}
	}
	if missing == nil {
		missing = []string{}
	}
	state, _ := s.jmapMailState(r.Context(), account)
	return map[string]any{"accountId": account.ID, "state": state, "list": list, "notFound": missing}, nil
}

func (s *Server) jmapIdentityGet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	state := hashParts(account.ID, account.Address, strconv.FormatBool(account.canSend()))
	var list []map[string]any
	if account.canSend() {
		list = []map[string]any{{
			"id": account.ID, "name": account.Name, "email": account.Address, "replyTo": nil, "bcc": nil,
			"textSignature": "", "htmlSignature": "", "mayDelete": false,
		}}
	}
	if list == nil {
		list = []map[string]any{}
	}
	ids, _ := stringList(args["ids"])
	if ids != nil {
		var filtered []map[string]any
		var missing []string
		have := map[string]map[string]any{}
		for _, item := range list {
			have[item["id"].(string)] = item
		}
		for _, id := range ids {
			if item, ok := have[id]; ok {
				filtered = append(filtered, item)
			} else {
				missing = append(missing, id)
			}
		}
		if filtered == nil {
			filtered = []map[string]any{}
		}
		if missing == nil {
			missing = []string{}
		}
		return map[string]any{"accountId": account.ID, "state": state, "list": filtered, "notFound": missing}, nil
	}
	_ = user
	return map[string]any{"accountId": account.ID, "state": state, "list": list, "notFound": []string{}}, nil
}

func (s *Server) jmapIdentitySet(accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	notCreated := map[string]any{}
	notUpdated := map[string]any{}
	notDestroyed := map[string]any{}
	if create, ok := args["create"].(map[string]any); ok {
		for id := range create {
			notCreated[id] = itemError("forbidden", "Identities are the mailbox address. Create an alias to send as another address.")
		}
	}
	if update, ok := args["update"].(map[string]any); ok {
		for id := range update {
			notUpdated[id] = itemError("forbidden", "The mailbox address cannot be changed here.")
		}
	}
	if destroy, ok := args["destroy"].([]any); ok {
		for _, raw := range destroy {
			id, _ := raw.(string)
			notDestroyed[id] = itemError("forbidden", "The mailbox identity cannot be deleted.")
		}
	}
	state := hashParts(account.ID, account.Address)
	return setResult(account.ID, state, state, map[string]any{}, map[string]any{}, []string{}, notCreated, notUpdated, notDestroyed), nil
}

func (s *Server) jmapSubmit(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any, created map[string]string) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	create, _ := args["create"].(map[string]any)
	if len(create) > jmapMaxObjs {
		return nil, &callError{Type: "invalidArguments", Description: "Submit at most 128 messages at a time."}
	}
	createdOut := map[string]any{}
	notCreated := map[string]any{}
	for cid, raw := range create {
		obj, _ := raw.(map[string]any)
		id, result, uerr := s.submitOne(r, user, account, obj)
		if uerr != nil {
			notCreated[cid] = uerr
			continue
		}
		created[cid] = id
		createdOut[cid] = result
	}
	state := hashParts("submission", account.ID)
	return setResult(account.ID, state, state, createdOut, map[string]any{}, []string{}, notCreated, map[string]any{}, map[string]any{}), nil
}

func (s *Server) submitOne(r *http.Request, user *jmapPrincipal, account *jmapAccount, obj map[string]any) (string, map[string]any, map[string]any) {
	if !account.canSend() {
		return "", nil, itemError("forbiddenFrom", "You can read this mailbox but not send from it.")
	}
	if obj == nil {
		return "", nil, itemError("invalidArguments", "A submission needs an object.")
	}
	identity, _ := obj["identityId"].(string)
	if identity != "" && identity != account.ID {
		return "", nil, itemError("invalidProperties", "identityId must be this account.", "identityId")
	}
	emailID, _ := obj["emailId"].(string)
	raw, err := s.jmapReadRaw(r.Context(), account, emailID)
	if err != nil {
		return "", nil, itemError("invalidArguments", "emailId does not exist.", "emailId")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", nil, itemError("invalidEmail", "The message is not valid RFC 5322.")
	}
	from, err := msg.Header.AddressList("From")
	if err != nil || len(from) != 1 || !strings.EqualFold(from[0].Address, account.Address) {
		return "", nil, itemError("forbiddenFrom", "From must be this account's address.")
	}
	if !account.SendAs && account.SendOnBehalf {
		raw = withSender(raw, user.Email, user.DisplayName)
	}
	var rcpts []string
	if env, ok := obj["envelope"].(map[string]any); ok {
		if mailFrom, ok := env["mailFrom"].(map[string]any); ok {
			addr, _ := mailFrom["email"].(string)
			if !strings.EqualFold(addr, account.Address) {
				return "", nil, itemError("forbiddenMailFrom", "The envelope sender must be this account's address.")
			}
		}
		for _, raw := range anyList(env["rcptTo"]) {
			item, _ := raw.(map[string]any)
			addr, _ := item["email"].(string)
			parsed, err := mail.ParseAddress(addr)
			if err != nil {
				return "", nil, itemError("forbiddenTo", addr+" is not an email address.")
			}
			rcpts = append(rcpts, parsed.Address)
		}
	}
	if len(rcpts) == 0 {
		for _, key := range []string{"To", "Cc", "Bcc"} {
			list, _ := msg.Header.AddressList(key)
			for _, a := range list {
				rcpts = append(rcpts, a.Address)
			}
		}
	}
	if len(rcpts) == 0 || len(rcpts) > 100 {
		return "", nil, itemError("invalidArguments", "The message needs between 1 and 100 recipients.")
	}
	if !s.allow(r.Context(), "send:"+account.ID, 30) {
		return "", nil, itemError("forbiddenTo", "This mailbox is sending too quickly. Wait a minute.")
	}
	if err := s.mail.Send(account.Address, account.Address, rcpts, raw); err != nil {
		mailSends.WithLabelValues("rejected").Inc()
		s.log.Warn("jmap submission rejected", "account", account.ID, "err", err)
		text := strings.Join(strings.Fields(err.Error()), " ")
		if len(text) > 300 {
			text = text[:300]
		}
		return "", nil, itemError("forbiddenTo", "The mail server did not accept the message: "+text)
	}
	mailSends.WithLabelValues("accepted").Inc()
	if updates, ok := obj["onSuccessUpdateEmail"].(map[string]any); ok {
		if patch, ok := updates[emailID].(map[string]any); ok {
			if _, uerr := s.updateJMAPEmail(r.Context(), account, emailID, patch); uerr != nil {
				s.log.Warn("jmap submission update failed", "email", emailID, "err", uerr["description"])
			}
		}
	}
	if destroy, _ := obj["onSuccessDestroyEmail"].(string); destroy == emailID || destroy == "#"+emailID {
		_ = s.destroyJMAPEmail(r.Context(), account, emailID)
	}
	id := "s." + secrets.Token(16)
	return id, map[string]any{"id": id, "emailId": emailID, "threadId": "", "sendAt": time.Now().UTC().Format(time.RFC3339)}, nil
}

func withSender(raw []byte, address, name string) []byte {
	sep := []byte("\r\n\r\n")
	head, body, ok := bytes.Cut(raw, sep)
	if !ok {
		sep = []byte("\n\n")
		head, body, ok = bytes.Cut(raw, sep)
		if !ok {
			return raw
		}
	}
	nl := []byte("\r\n")
	if !bytes.Contains(head, nl) {
		nl = []byte("\n")
	}
	var kept [][]byte
	skip := false
	for _, line := range bytes.Split(head, nl) {
		if skip && (bytes.HasPrefix(line, []byte(" ")) || bytes.HasPrefix(line, []byte("\t"))) {
			continue
		}
		skip = false
		if bytes.HasPrefix(bytes.ToLower(line), []byte("sender:")) {
			skip = true
			continue
		}
		kept = append(kept, line)
	}
	display := address
	if name != "" {
		display = strconv.Quote(name) + " <" + address + ">"
	}
	line := []byte("Sender: " + display)
	if len(kept) == 0 {
		kept = [][]byte{line}
	} else {
		kept = append([][]byte{kept[0], line}, kept[1:]...)
	}
	return append(append(bytes.Join(kept, nl), sep...), body...)
}

func (s *Server) jmapReadRaw(ctx context.Context, account *jmapAccount, id string) ([]byte, error) {
	hit, mid, err := s.emailHit(ctx, account.ID, id)
	if err != nil {
		return nil, err
	}
	raw, err := s.mail.ReadMessage(account.Address, hit.Folder, hit.UIDValidity, hit.UID)
	if err == nil {
		return raw, nil
	}
	if mid == "" {
		return nil, err
	}
	found, ferr := s.mail.FindMessage(account.Address, mid)
	if ferr != nil {
		return nil, ferr
	}
	_ = s.retargetEmail(ctx, account.ID, id, found)
	return s.mail.ReadMessage(account.Address, found.Folder, found.UIDValidity, found.UID)
}

func (s *Server) jmapReadPart(ctx context.Context, account *jmapAccount, id, part string) ([]byte, string, error) {
	hit, _, err := s.emailHit(ctx, account.ID, id)
	if err != nil {
		return nil, "", err
	}
	stored, err := s.mail.FetchStored(account.Address, []mailpkg.MessageHit{hit}, mailpkg.FetchWant{})
	if err != nil || len(stored) == 0 {
		return nil, "", mailpkg.ErrNotFound
	}
	typ := "application/octet-stream"
	for _, p := range stored[0].Parts {
		if p.Path == part {
			typ = p.Type
		}
	}
	body, err := s.mail.ReadMessagePart(account.Address, hit.Folder, hit.UIDValidity, hit.UID, partPath(part))
	return body, typ, err
}

func partPath(s string) []int {
	var path []int
	for _, piece := range strings.Split(s, ".") {
		n, err := strconv.Atoi(piece)
		if err != nil || n <= 0 {
			return []int{1}
		}
		path = append(path, n)
	}
	if len(path) == 0 {
		return []int{1}
	}
	return path
}

func (s *Server) jmapVacationGet(r *http.Request, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	_, reply, err := s.loadSieveSettings(r.Context(), account.ID)
	if err != nil {
		if account.OrgID == "" {
			return map[string]any{"accountId": account.ID, "state": "0", "list": []any{}, "notFound": []string{}}, nil
		}
		return nil, s.jmapFail(err)
	}
	obj := vacationObject(reply)
	return map[string]any{"accountId": account.ID, "state": hashParts(strconv.FormatBool(reply.Enabled), reply.Subject, reply.Message, reply.Start, reply.End), "list": []any{obj}, "notFound": []string{}}, nil
}

func vacationObject(reply mailpkg.AutoReply) map[string]any {
	obj := map[string]any{"id": "singleton", "isEnabled": reply.Enabled, "subject": nilOr(reply.Subject), "textBody": nilOr(reply.Message), "htmlBody": nil}
	if reply.Start != "" {
		obj["fromDate"] = reply.Start + "T00:00:00Z"
	}
	if reply.End != "" {
		obj["toDate"] = reply.End + "T00:00:00Z"
	}
	return obj
}

func (s *Server) jmapVacationSet(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, args map[string]any) (any, *callError) {
	account, cerr := accountArg(accounts, args, jmapMail)
	if cerr != nil {
		return nil, cerr
	}
	if !account.canSend() {
		return nil, &callError{Type: "forbidden", Description: "Automatic replies are sent as this mailbox. Send as is required."}
	}
	rules, reply, err := s.loadSieveSettings(r.Context(), account.ID)
	if err != nil {
		return nil, s.jmapFail(err)
	}
	old := hashParts(strconv.FormatBool(reply.Enabled), reply.Subject, reply.Message, reply.Start, reply.End)
	notCreated := map[string]any{}
	notUpdated := map[string]any{}
	notDestroyed := map[string]any{}
	updated := map[string]any{}
	var destroyed []string
	apply := func(obj map[string]any) map[string]any {
		if v, ok := obj["isEnabled"].(bool); ok {
			reply.Enabled = v
		}
		if v, ok := obj["subject"].(string); ok {
			reply.Subject = v
		}
		if obj["subject"] == nil && hasKey(obj, "subject") {
			reply.Subject = ""
		}
		if v, ok := obj["textBody"].(string); ok {
			reply.Message = v
		}
		if v, ok := obj["htmlBody"].(string); ok && reply.Message == "" {
			reply.Message = stripTags(v)
		}
		if v, ok := obj["fromDate"].(string); ok {
			reply.Start = dateOnly(v)
		}
		if v, ok := obj["toDate"].(string); ok {
			reply.End = dateOnly(v)
		}
		if err := reply.Validate(); err != nil {
			return itemError("invalidProperties", err.Error())
		}
		box := &ownedMailbox{ID: account.ID, Address: account.Address, OrgID: account.OrgID}
		if err := s.persistSieve(r.Context(), box, rules, reply); err != nil {
			mailBackendErrs.Inc()
			s.log.Error("jmap vacation failed", "account", account.ID, "err", err)
			return itemError("serverFail", "The automatic reply could not be installed.")
		}
		s.audit(r.Context(), r, &principal{User: User{ID: user.UserID, Email: user.Email, DisplayName: user.DisplayName}}, &account.OrgID, "mailbox.rules.update", "mailbox", account.ID, map[string]any{"autoReply": reply.Enabled, "via": "jmap"})
		return nil
	}
	if create, ok := args["create"].(map[string]any); ok {
		for id, raw := range create {
			obj, _ := raw.(map[string]any)
			if uerr := apply(obj); uerr != nil {
				notCreated[id] = uerr
			}
		}
	}
	if update, ok := args["update"].(map[string]any); ok {
		for id, raw := range update {
			if id != "singleton" {
				notUpdated[id] = itemError("notFound", "The vacation response id is singleton.")
				continue
			}
			obj, _ := raw.(map[string]any)
			if uerr := apply(obj); uerr != nil {
				notUpdated[id] = uerr
				continue
			}
			updated[id] = nil
		}
	}
	if destroy, ok := args["destroy"].([]any); ok {
		for _, raw := range destroy {
			id, _ := raw.(string)
			if id != "singleton" {
				notDestroyed[id] = itemError("notFound", "The vacation response id is singleton.")
				continue
			}
			reply.Enabled = false
			if uerr := apply(map[string]any{"isEnabled": false}); uerr != nil {
				notDestroyed[id] = uerr
				continue
			}
			destroyed = append(destroyed, id)
		}
	}
	newState := hashParts(strconv.FormatBool(reply.Enabled), reply.Subject, reply.Message, reply.Start, reply.End)
	return setResult(account.ID, old, newState, map[string]any{}, updated, destroyed, notCreated, notUpdated, notDestroyed), nil
}

func dateOnly(s string) string {
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[:10]
	}
	return ""
}
