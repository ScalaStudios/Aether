package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

const (
	jmapCore     = "urn:ietf:params:jmap:core"
	jmapMail     = "urn:ietf:params:jmap:mail"
	jmapCal      = "urn:ietf:params:jmap:calendars"
	jmapCard     = "urn:ietf:params:jmap:contacts"
	jmapMaxCalls = 16
	jmapMaxObjs  = 128
	jmapMaxUp    = 25 << 20
	jmapMaxReq   = 10 << 20
)

type callError struct {
	Type, Description string
	Properties        []string
}

func (e *callError) Error() string { return e.Type }

func itemError(typ, desc string, props ...string) map[string]any {
	m := map[string]any{"type": typ}
	if desc != "" {
		m["description"] = desc
	}
	if len(props) > 0 {
		m["properties"] = props
	}
	return m
}

type jmapPrincipal struct {
	UserID, Email, DisplayName, Username, PasswordMailbox string
}

type jmapAccount struct {
	ID, Address, Name, Kind, Access, OrgID string
	SendAs, SendOnBehalf, Owner            bool
	Mail, Calendars, Contacts              bool
}

func (a jmapAccount) canWriteMail() bool { return a.Mail && a.Access != "read" }
func (a jmapAccount) canSend() bool      { return a.Mail && (a.SendAs || a.SendOnBehalf) }
func (a jmapAccount) personal() bool     { return a.Owner && a.Kind == "user" }
func (a jmapAccount) readOnly() bool {
	return !a.canWriteMail() && !a.canSend() && !a.Calendars && !a.Contacts
}

type jmapCall struct {
	Name   string
	Args   map[string]any
	ID     string
	Result any
	Failed bool
}

func (s *Server) jmapOptions(w http.ResponseWriter, r *http.Request) {
	jmapCORS(w)
	w.WriteHeader(http.StatusNoContent)
}

func jmapCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	h.Set("Access-Control-Max-Age", "86400")
}

func writeJMAP(w http.ResponseWriter, status int, v any) {
	jmapCORS(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func jmapProblem(w http.ResponseWriter, status int, typ, detail string) {
	writeJMAP(w, status, map[string]any{"type": "urn:ietf:params:jmap:error:" + typ, "status": status, "detail": detail})
}

func (s *Server) jmapUnauthorized(w http.ResponseWriter, r *http.Request, failed bool) {
	if failed && r.Header.Get("Authorization") != "" {
		authAttempts.WithLabelValues("jmap", "failure").Inc()
	}
	jmapCORS(w)
	w.Header().Set("WWW-Authenticate", `Basic realm="Aether", charset="UTF-8"`)
	w.Header().Add("WWW-Authenticate", `Bearer realm="Aether"`)
	jmapProblem(w, http.StatusUnauthorized, "unauthorized", "Sign in with a mailbox address and an app password, or an OpenID Connect access token.")
}

func (s *Server) jmapAuth(w http.ResponseWriter, r *http.Request) (*jmapPrincipal, bool) {
	if !s.allow(r.Context(), "jmap-ip:"+s.clientIP(r), 600) {
		jmapProblem(w, http.StatusTooManyRequests, "limit", "Too many JMAP requests from this address. Wait a minute and try again.")
		return nil, false
	}
	header := r.Header.Get("Authorization")
	if header == "" {
		s.jmapUnauthorized(w, r, false)
		return nil, false
	}
	var p *jmapPrincipal
	var err error
	switch {
	case strings.HasPrefix(header, "Basic "):
		user, pass, ok := r.BasicAuth()
		if !ok || pass == "" || len(pass) > 500 {
			s.jmapUnauthorized(w, r, true)
			return nil, false
		}
		p, err = s.jmapBasic(r.Context(), user, pass)
	case strings.HasPrefix(header, "Bearer "):
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if token == "" || len(token) > 8192 || strings.HasPrefix(token, apiKeyPrefix) {
			s.jmapUnauthorized(w, r, true)
			return nil, false
		}
		p, err = s.jmapBearer(r.Context(), token)
	default:
		s.jmapUnauthorized(w, r, true)
		return nil, false
	}
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.internalError(w, r, err)
			return nil, false
		}
		s.jmapUnauthorized(w, r, true)
		return nil, false
	}
	if !s.allow(r.Context(), "jmap-user:"+p.UserID, 180) {
		jmapProblem(w, http.StatusTooManyRequests, "limit", "Too many JMAP requests for this account. Wait a minute and try again.")
		return nil, false
	}
	authAttempts.WithLabelValues("jmap", "success").Inc()
	return p, true
}

func (s *Server) jmapBasic(ctx context.Context, username, password string) (*jmapPrincipal, error) {
	p := &jmapPrincipal{Username: strings.ToLower(strings.TrimSpace(username))}
	err := s.db.QueryRow(ctx, `
		SELECT u.id, u.email, u.display_name, m.id FROM app_passwords p
		JOIN mailboxes m ON m.id = p.mailbox_id
		JOIN domains d ON d.id = m.domain_id
		JOIN users u ON u.id = p.user_id
		WHERE m.address = $1 AND p.token_hash = $2 AND p.revoked_at IS NULL AND m.active AND m.kind = 'user'
		  AND d.active AND d.verified_at IS NOT NULL AND u.status = 'active' AND `+signInAsMailbox("u.id"),
		p.Username, secrets.SHA256Hex(password)).Scan(&p.UserID, &p.Email, &p.DisplayName, &p.PasswordMailbox)
	return p, err
}

func (s *Server) jmapBearer(ctx context.Context, token string) (*jmapPrincipal, error) {
	userID, _, ok := s.introspectMailToken(ctx, token)
	if !ok {
		return nil, pgx.ErrNoRows
	}
	p := &jmapPrincipal{UserID: userID}
	err := s.db.QueryRow(ctx, `SELECT email, display_name FROM users WHERE id = $1 AND status = 'active'`, userID).Scan(&p.Email, &p.DisplayName)
	p.Username = p.Email
	return p, err
}

func (s *Server) jmapAccounts(ctx context.Context, user *jmapPrincipal) ([]jmapAccount, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.id, m.address, m.display_name, m.kind, m.org_id, `+mailboxAccessLevel("$1")+`, `+mailboxSendAs("$1")+`, `+mailboxSendOnBehalf("$1")+`, coalesce(m.owner_user_id = $1, false)
		FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE `+mailboxAccess("$1")+` AND m.active AND d.active AND d.verified_at IS NOT NULL
		ORDER BY m.address`, user.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []jmapAccount
	var owned int
	var pimAt = -1
	for rows.Next() {
		var a jmapAccount
		var owner bool
		if err := rows.Scan(&a.ID, &a.Address, &a.Name, &a.Kind, &a.OrgID, &a.Access, &a.SendAs, &a.SendOnBehalf, &owner); err != nil {
			return nil, err
		}
		a.Owner = owner
		a.Mail = true
		if a.Name == "" {
			a.Name = a.Address
		}
		if owner && a.Kind == "user" {
			owned++
			if pimAt < 0 || strings.EqualFold(a.Address, user.Email) {
				pimAt = len(accounts)
			}
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if pimAt >= 0 {
		accounts[pimAt].Calendars = true
		accounts[pimAt].Contacts = true
	} else if !containsAccount(accounts, user.UserID) {
		name := user.DisplayName
		if name == "" {
			name = user.Email
		}
		accounts = append(accounts, jmapAccount{ID: user.UserID, Name: name, Kind: "user", Access: "owner", Owner: true, Calendars: true, Contacts: true})
	}
	if accountsHavePIM(accounts) {
		if err := s.ensureDAVDefaults(ctx, user.UserID); err != nil {
			return nil, err
		}
	}
	return accounts, nil
}

func containsAccount(accounts []jmapAccount, id string) bool {
	for _, a := range accounts {
		if a.ID == id {
			return true
		}
	}
	return false
}

func accountsHavePIM(accounts []jmapAccount) bool {
	for _, a := range accounts {
		if a.Calendars || a.Contacts {
			return true
		}
	}
	return false
}

func accountByID(accounts []jmapAccount, id string) *jmapAccount {
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i]
		}
	}
	return nil
}

func primaryAccount(accounts []jmapAccount, user *jmapPrincipal, mail bool) string {
	if mail && user.PasswordMailbox != "" && accountByID(accounts, user.PasswordMailbox) != nil && accountByID(accounts, user.PasswordMailbox).Mail {
		return user.PasswordMailbox
	}
	var firstMail, firstCap string
	for _, a := range accounts {
		if mail && a.Mail && strings.EqualFold(a.Address, user.Email) {
			return a.ID
		}
		if mail && a.Mail && firstMail == "" {
			firstMail = a.ID
		}
		if !mail && (a.Calendars || a.Contacts) && firstCap == "" {
			firstCap = a.ID
		}
	}
	if mail {
		return firstMail
	}
	return firstCap
}

func sessionState(accounts []jmapAccount) string {
	h := sha256.New()
	for _, a := range accounts {
		fmt.Fprintf(h, "%s|%s|%s|%v|%v|%v|%v|%v\n", a.ID, a.Access, a.Kind, a.SendAs, a.SendOnBehalf, a.Mail, a.Calendars, a.Contacts)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func (s *Server) jmapSession(w http.ResponseWriter, r *http.Request) {
	user, ok := s.jmapAuth(w, r)
	if !ok {
		return
	}
	accounts, err := s.jmapAccounts(r.Context(), user)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	base := strings.TrimRight(s.cfg.PublicURL.String(), "/")
	accs := map[string]any{}
	for _, a := range accounts {
		caps := map[string]any{}
		if a.Mail {
			caps[jmapMail] = mailCap()
		}
		if a.Calendars {
			caps[jmapCal] = calCap()
		}
		if a.Contacts {
			caps[jmapCard] = cardCap()
		}
		accs[a.ID] = map[string]any{"name": a.Name, "isPersonal": a.personal(), "isReadOnly": a.readOnly(), "accountCapabilities": caps}
	}
	primary := map[string]string{}
	if id := primaryAccount(accounts, user, true); id != "" {
		primary[jmapMail] = id
	}
	if id := primaryAccount(accounts, user, false); id != "" {
		if accountByID(accounts, id).Calendars {
			primary[jmapCal] = id
		}
		if accountByID(accounts, id).Contacts {
			primary[jmapCard] = id
		}
	}
	caps := map[string]any{jmapCore: coreCap()}
	if primary[jmapMail] != "" {
		caps[jmapMail] = mailCap()
	}
	if primary[jmapCal] != "" {
		caps[jmapCal] = calCap()
	}
	if primary[jmapCard] != "" {
		caps[jmapCard] = cardCap()
	}
	writeJMAP(w, http.StatusOK, map[string]any{
		"capabilities": caps, "accounts": accs, "primaryAccounts": primary, "username": user.Username,
		"apiUrl": base + "/jmap", "downloadUrl": base + "/jmap/download/{accountId}/{blobId}/{name}?type={type}",
		"uploadUrl": base + "/jmap/upload/{accountId}/", "eventSourceUrl": base + "/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
		"state": sessionState(accounts),
	})
}

func coreCap() map[string]any {
	return map[string]any{
		"maxSizeUpload": jmapMaxUp, "maxConcurrentUpload": 4, "maxSizeRequest": jmapMaxReq, "maxConcurrentRequests": 4,
		"maxCallsInRequest": jmapMaxCalls, "maxObjectsInGet": jmapMaxObjs, "maxObjectsInSet": jmapMaxObjs,
		"collationAlgorithms": []string{"i;ascii-casemap"},
	}
}

func mailCap() map[string]any {
	return map[string]any{
		"maxMailboxesPerEmail": 1, "maxMailboxDepth": nil, "maxSizeMailboxName": 400, "maxSizeAttachmentsPerEmail": jmapMaxUp,
		"emailQuerySortOptions": []string{"receivedAt", "sentAt", "size", "from", "to", "subject"}, "mayCreateTopLevelMailbox": true,
	}
}

func calCap() map[string]any {
	return map[string]any{
		"maxCalendarsPerEvent": 1, "minDateTime": "0000-01-01T00:00:00", "maxDateTime": "9999-12-31T23:59:59",
		"maxExpandedQueryDuration": "P90D", "maxParticipantsPerEvent": 100, "mayCreateCalendar": true,
	}
}

func cardCap() map[string]any {
	return map[string]any{"maxAddressBooksPerCard": 1, "mayCreateAddressBook": true}
}

func (s *Server) jmapAPI(w http.ResponseWriter, r *http.Request) {
	user, ok := s.jmapAuth(w, r)
	if !ok {
		return
	}
	if r.Header.Get("Content-Type") != "" && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		jmapProblem(w, http.StatusBadRequest, "notJSON", "The request body must be JSON.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, jmapMaxReq))
	if err != nil {
		jmapProblem(w, http.StatusRequestEntityTooLarge, "limit", "The request is larger than the server accepts.")
		return
	}
	var req struct {
		Using       []string            `json:"using"`
		MethodCalls [][]json.RawMessage `json:"methodCalls"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil || len(req.Using) == 0 || req.MethodCalls == nil {
		jmapProblem(w, http.StatusBadRequest, "notJSON", "The request is not a JMAP request.")
		return
	}
	known := map[string]bool{jmapCore: true, jmapMail: true, jmapCal: true, jmapCard: true}
	for _, u := range req.Using {
		if !known[u] {
			jmapProblem(w, http.StatusBadRequest, "unknownCapability", u+" is not a capability of this server.")
			return
		}
	}
	if len(req.MethodCalls) > jmapMaxCalls {
		jmapProblem(w, http.StatusBadRequest, "limit", "A request can contain at most 16 method calls.")
		return
	}
	accounts, err := s.jmapAccounts(r.Context(), user)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	using := map[string]bool{}
	for _, u := range req.Using {
		using[u] = true
	}
	created := map[string]string{}
	var calls []jmapCall
	var responses []any
	for _, raw := range req.MethodCalls {
		if len(raw) != 3 {
			jmapProblem(w, http.StatusBadRequest, "notRequest", "Each method call is a name, an arguments object, and a method id.")
			return
		}
		var name, id string
		if json.Unmarshal(raw[0], &name) != nil || json.Unmarshal(raw[2], &id) != nil || name == "" || id == "" {
			jmapProblem(w, http.StatusBadRequest, "notRequest", "Each method call is a name, an arguments object, and a method id.")
			return
		}
		var args any
		if json.Unmarshal(raw[1], &args) != nil {
			jmapProblem(w, http.StatusBadRequest, "notRequest", "Method arguments must be a JSON object.")
			return
		}
		rewritten, rerr := rewriteJMAP(args, created, calls)
		if rerr != nil {
			responses = append(responses, []any{"error", map[string]any{"type": "invalidResultReference", "description": rerr.Error()}, id})
			calls = append(calls, jmapCall{Name: name, ID: id, Failed: true})
			continue
		}
		obj, _ := rewritten.(map[string]any)
		if obj == nil {
			responses = append(responses, []any{"error", map[string]any{"type": "invalidArguments", "description": "Method arguments must be a JSON object."}, id})
			calls = append(calls, jmapCall{Name: name, ID: id, Failed: true})
			continue
		}
		result, cerr := s.jmapMethod(r, user, accounts, using, name, obj, created)
		call := jmapCall{Name: name, Args: obj, ID: id}
		if cerr != nil {
			body := map[string]any{"type": cerr.Type, "description": cerr.Description}
			if len(cerr.Properties) > 0 {
				body["properties"] = cerr.Properties
			}
			responses = append(responses, []any{"error", body, id})
			call.Failed = true
		} else {
			responses = append(responses, []any{name, result, id})
			call.Result = result
		}
		calls = append(calls, call)
	}
	out := map[string]any{"methodResponses": responses, "sessionState": sessionState(accounts)}
	if len(created) > 0 {
		out["createdIds"] = created
	}
	writeJMAP(w, http.StatusOK, out)
}

func (s *Server) jmapMethod(r *http.Request, user *jmapPrincipal, accounts []jmapAccount, using map[string]bool, name string, args map[string]any, created map[string]string) (any, *callError) {
	need := jmapCore
	switch {
	case strings.HasPrefix(name, "Mailbox/") || strings.HasPrefix(name, "Email/") || strings.HasPrefix(name, "Thread/") || strings.HasPrefix(name, "Identity/") || strings.HasPrefix(name, "EmailSubmission/") || strings.HasPrefix(name, "VacationResponse/"):
		need = jmapMail
	case strings.HasPrefix(name, "Calendar"):
		need = jmapCal
	case strings.HasPrefix(name, "AddressBook/") || strings.HasPrefix(name, "ContactCard/"):
		need = jmapCard
	}
	if name != "Core/echo" && !using[need] {
		return nil, &callError{Type: "unknownMethod", Description: name + " needs " + need + " in using."}
	}
	if name == "Core/echo" && !using[jmapCore] {
		return nil, &callError{Type: "unknownMethod", Description: "Core/echo needs " + jmapCore + " in using."}
	}
	switch name {
	case "Core/echo":
		return args, nil
	case "Mailbox/get":
		return s.jmapMailboxGet(r, accounts, args)
	case "Mailbox/query":
		return s.jmapMailboxQuery(r, accounts, args)
	case "Mailbox/set":
		return s.jmapMailboxSet(r, user, accounts, args, created)
	case "Email/query":
		return s.jmapEmailQuery(r, accounts, args)
	case "Email/get":
		return s.jmapEmailGet(r, accounts, args)
	case "Email/set":
		return s.jmapEmailSet(r, user, accounts, args, created)
	case "Email/import":
		return s.jmapEmailImport(r, user, accounts, args, created)
	case "Thread/get":
		return s.jmapThreadGet(r, accounts, args)
	case "Identity/get":
		return s.jmapIdentityGet(r, user, accounts, args)
	case "Identity/set":
		return s.jmapIdentitySet(accounts, args)
	case "EmailSubmission/set":
		return s.jmapSubmit(r, user, accounts, args, created)
	case "VacationResponse/get":
		return s.jmapVacationGet(r, accounts, args)
	case "VacationResponse/set":
		return s.jmapVacationSet(r, user, accounts, args)
	case "Calendar/get":
		return s.jmapCalendarGet(r, user, accounts, args)
	case "Calendar/query":
		return s.jmapCalendarQuery(r, user, accounts, args)
	case "Calendar/set":
		return s.jmapCalendarSet(r, user, accounts, args, created)
	case "CalendarEvent/get":
		return s.jmapEventGet(r, user, accounts, args)
	case "CalendarEvent/query":
		return s.jmapEventQuery(r, user, accounts, args)
	case "CalendarEvent/set":
		return s.jmapEventSet(r, user, accounts, args, created)
	case "AddressBook/get":
		return s.jmapBookGet(r, user, accounts, args)
	case "AddressBook/query":
		return s.jmapBookQuery(r, user, accounts, args)
	case "AddressBook/set":
		return s.jmapBookSet(r, user, accounts, args, created)
	case "ContactCard/get":
		return s.jmapCardGet(r, user, accounts, args)
	case "ContactCard/query":
		return s.jmapCardQuery(r, user, accounts, args)
	case "ContactCard/set":
		return s.jmapCardSet(r, user, accounts, args, created)
	default:
		return nil, &callError{Type: "unknownMethod", Description: name + " is not implemented."}
	}
}

func accountArg(accounts []jmapAccount, args map[string]any, capability string) (*jmapAccount, *callError) {
	id, _ := args["accountId"].(string)
	if id == "" {
		return nil, &callError{Type: "invalidArguments", Description: "accountId is required."}
	}
	a := accountByID(accounts, id)
	if a == nil {
		return nil, &callError{Type: "accountNotFound", Description: "That account is not available to this login."}
	}
	switch capability {
	case jmapMail:
		if !a.Mail {
			return nil, &callError{Type: "accountNotSupportedByMethod", Description: "This account has no mail."}
		}
	case jmapCal:
		if !a.Calendars {
			return nil, &callError{Type: "accountNotSupportedByMethod", Description: "This account has no calendars."}
		}
	case jmapCard:
		if !a.Contacts {
			return nil, &callError{Type: "accountNotSupportedByMethod", Description: "This account has no contacts."}
		}
	}
	return a, nil
}

func rewriteJMAP(v any, created map[string]string, calls []jmapCall) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range t {
			if strings.HasPrefix(k, "#") {
				if ref, ok := val.(map[string]any); ok && ref["resultOf"] != nil {
					resolved, err := resolveRef(ref, calls)
					if err != nil {
						return nil, err
					}
					out[k[1:]] = resolved
					continue
				}
				if id, ok := created[k[1:]]; ok && !strings.Contains(k[1:], "/") {
					k = id
				}
			}
			next, err := rewriteJMAP(val, created, calls)
			if err != nil {
				return nil, err
			}
			out[k] = next
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			next, err := rewriteJMAP(val, created, calls)
			if err != nil {
				return nil, err
			}
			out[i] = next
		}
		return out, nil
	case string:
		if strings.HasPrefix(t, "#") {
			if id, ok := created[t[1:]]; ok {
				return id, nil
			}
		}
		return t, nil
	default:
		return v, nil
	}
}

func resolveRef(ref map[string]any, calls []jmapCall) (any, error) {
	resultOf, _ := ref["resultOf"].(string)
	name, _ := ref["name"].(string)
	path, _ := ref["path"].(string)
	if resultOf == "" || name == "" || path == "" || !strings.HasPrefix(path, "/") {
		return nil, errors.New("A result reference needs resultOf, name, and a JSON pointer path.")
	}
	for _, c := range calls {
		if c.ID != resultOf {
			continue
		}
		if c.Failed || c.Name != name {
			return nil, errors.New("The referenced method call did not succeed.")
		}
		raw, err := json.Marshal(c.Result)
		if err != nil {
			return nil, err
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		return jsonPointer(doc, path)
	}
	return nil, errors.New("The referenced method call is not in this request.")
}

func jsonPointer(v any, path string) (any, error) {
	if path == "" || path == "/" {
		return v, nil
	}
	cur := v
	for _, raw := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, errors.New("The result reference path does not exist.")
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(node) {
				return nil, errors.New("The result reference path does not exist.")
			}
			cur = node[i]
		default:
			return nil, errors.New("The result reference path does not exist.")
		}
	}
	return cur, nil
}

func stringList(v any) ([]string, bool) {
	switch t := v.(type) {
	case nil:
		return nil, true
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

func pickProps(obj map[string]any, props []string) map[string]any {
	if props == nil {
		return obj
	}
	out := map[string]any{}
	if id, ok := obj["id"]; ok {
		out["id"] = id
	}
	for _, p := range props {
		if v, ok := obj[p]; ok {
			out[p] = v
		}
	}
	return out
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func numberInt(v any) (int, bool) {
	switch t := v.(type) {
	case json.Number:
		i, err := t.Int64()
		return int(i), err == nil
	case float64:
		return int(t), true
	default:
		return 0, false
	}
}

func hashParts(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func (s *Server) jmapUpload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.jmapAuth(w, r)
	if !ok {
		return
	}
	accounts, err := s.jmapAccounts(r.Context(), user)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	account := accountByID(accounts, strings.ToLower(r.PathValue("accountId")))
	if account == nil {
		http.NotFound(w, r)
		return
	}
	if account.readOnly() || (account.Mail && !account.canWriteMail() && !account.canSend() && !account.Calendars && !account.Contacts) {
		jmapProblem(w, http.StatusForbidden, "forbidden", "This account cannot store uploads.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, jmapMaxUp))
	if err != nil {
		jmapProblem(w, http.StatusRequestEntityTooLarge, "limit", "The upload is larger than 25 MB.")
		return
	}
	typ := r.Header.Get("Content-Type")
	if typ == "" || strings.ContainsAny(typ, "\r\n") {
		typ = "application/octet-stream"
	}
	id := "b." + secrets.Token(16)
	payload := strconv.Itoa(len(typ)) + "\n" + typ + string(body)
	if err := s.kv.Set(r.Context(), blobKey(user.UserID, account.ID, id), payload, time.Hour).Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJMAP(w, http.StatusCreated, map[string]any{"accountId": account.ID, "blobId": id, "type": typ, "size": len(body)})
}

func blobKey(user, account, id string) string {
	return "aether:jmapblob:" + user + ":" + account + ":" + id
}

func (s *Server) loadBlob(ctx context.Context, user, account, id string) (string, []byte, error) {
	raw, err := s.kv.Get(ctx, blobKey(user, account, id)).Bytes()
	if err != nil {
		return "", nil, err
	}
	n, rest, ok := strings.Cut(string(raw), "\n")
	size, err := strconv.Atoi(n)
	if !ok || err != nil || size < 0 || size > len(rest) {
		return "", nil, errors.New("stored blob is corrupt")
	}
	return rest[:size], []byte(rest[size:]), nil
}

func (s *Server) jmapDownload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.jmapAuth(w, r)
	if !ok {
		return
	}
	accounts, err := s.jmapAccounts(r.Context(), user)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	account := accountByID(accounts, strings.ToLower(r.PathValue("accountId")))
	if account == nil || !account.Mail && !strings.HasPrefix(r.PathValue("blobId"), "b.") {
		http.NotFound(w, r)
		return
	}
	blobID := r.PathValue("blobId")
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "\\\"\r\n") {
		name = "download"
	}
	var body []byte
	typ := r.URL.Query().Get("type")
	switch {
	case strings.HasPrefix(blobID, "b."):
		stored, data, err := s.loadBlob(r.Context(), user.UserID, account.ID, blobID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if typ == "" {
			typ = stored
		}
		body = data
	case account.Mail && strings.Contains(blobID, "~"):
		id, part, _ := strings.Cut(blobID, "~")
		data, content, err := s.jmapReadPart(r.Context(), account, id, part)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body = data
		if typ == "" {
			typ = content
		}
	case account.Mail && strings.HasPrefix(blobID, "e."):
		data, err := s.jmapReadRaw(r.Context(), account, blobID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body = data
		if typ == "" {
			typ = "message/rfc822"
		}
	default:
		http.NotFound(w, r)
		return
	}
	if typ == "" || strings.ContainsAny(typ, "\r\n") {
		typ = "application/octet-stream"
	}
	jmapCORS(w)
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(body)
}

func (s *Server) jmapEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := s.jmapAuth(w, r)
	if !ok {
		return
	}
	accounts, err := s.jmapAccounts(r.Context(), user)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	key := "aether:jmapev:" + user.UserID
	n, err := s.kv.Incr(r.Context(), key).Result()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.kv.Expire(r.Context(), key, 2*time.Minute)
	if n > 4 {
		s.kv.Decr(r.Context(), key)
		jmapProblem(w, http.StatusTooManyRequests, "limit", "Four event streams are already open for this account.")
		return
	}
	defer func() { s.kv.Decr(context.Background(), key) }()
	flusher, ok := w.(http.Flusher)
	if !ok {
		jmapProblem(w, http.StatusInternalServerError, "serverFail", "This connection cannot stream events.")
		return
	}
	jmapCORS(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	rc := http.NewResponseController(w)
	closeAfter := r.URL.Query().Get("closeafter")
	ping := 0
	if v := r.URL.Query().Get("ping"); v != "" {
		ping, _ = strconv.Atoi(v)
		if ping < 10 {
			ping = 10
		}
	}
	types := map[string]bool{}
	if raw := r.URL.Query().Get("types"); raw != "" && raw != "*" {
		for _, t := range strings.Split(raw, ",") {
			types[strings.TrimSpace(t)] = true
		}
	}
	prev := map[string]string{}
	deadline := time.Now().Add(30 * time.Minute)
	lastPing := time.Now()
	for {
		rc.SetWriteDeadline(time.Now().Add(2 * time.Minute))
		s.kv.Expire(r.Context(), key, 2*time.Minute)
		changed := map[string]map[string]string{}
		for _, a := range accounts {
			if a.Mail && typeWanted(types, "Mailbox", "Email", "Thread", "Identity", "EmailSubmission", "VacationResponse") {
				state, err := s.jmapMailState(r.Context(), &a)
				if err == nil && state != prev[a.ID+"mail"] {
					prev[a.ID+"mail"] = state
					if changed[a.ID] == nil {
						changed[a.ID] = map[string]string{}
					}
					changed[a.ID][jmapMail] = state
				}
			}
			if a.Calendars && typeWanted(types, "Calendar", "CalendarEvent") {
				state, err := s.jmapPIMState(r.Context(), user.UserID, "calendar")
				if err == nil && state != prev[a.ID+"cal"] {
					prev[a.ID+"cal"] = state
					if changed[a.ID] == nil {
						changed[a.ID] = map[string]string{}
					}
					changed[a.ID][jmapCal] = state
				}
			}
			if a.Contacts && typeWanted(types, "AddressBook", "ContactCard") {
				state, err := s.jmapPIMState(r.Context(), user.UserID, "addressbook")
				if err == nil && state != prev[a.ID+"card"] {
					prev[a.ID+"card"] = state
					if changed[a.ID] == nil {
						changed[a.ID] = map[string]string{}
					}
					changed[a.ID][jmapCard] = state
				}
			}
		}
		if len(changed) > 0 {
			payload, _ := json.Marshal(map[string]any{"changed": changed})
			fmt.Fprintf(w, "event: state\ndata: %s\n\n", payload)
			flusher.Flush()
			if closeAfter == "state" {
				return
			}
		}
		if ping > 0 && time.Since(lastPing) >= time.Duration(ping)*time.Second {
			fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
			flusher.Flush()
			lastPing = time.Now()
		}
		if time.Now().After(deadline) || r.Context().Err() != nil {
			return
		}
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func typeWanted(types map[string]bool, names ...string) bool {
	if len(types) == 0 {
		return true
	}
	for _, n := range names {
		if types[n] {
			return true
		}
	}
	return false
}
