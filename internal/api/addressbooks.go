package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

type addressBook struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Color       string          `json:"color"`
	Access      string          `json:"access"`
	Owner       *calendarOwner  `json:"owner,omitempty"`
	Shares      []calendarShare `json:"shares,omitempty"`
}

type bookInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

func (s *Server) addressBooks(ctx context.Context, slug string) ([]collectionRow, error) {
	books, err := s.davCollections(ctx, "addressbook")
	if err != nil || slug == "" {
		return books, err
	}
	var out []collectionRow
	for _, book := range books {
		if book.Slug == slug {
			out = append(out, book)
		}
	}
	if len(out) == 0 {
		return nil, davNotFound()
	}
	return out, nil
}

func (s *Server) listAddressBooks(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.davCollections(ctx, "addressbook")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]addressBook, len(rows))
	index := map[string]int{}
	ids := make([]string, len(rows))
	for i, c := range rows {
		out[i] = addressBook{ID: c.Slug, Name: c.Name, Description: c.Description, Color: c.Color, Access: c.Access}
		index[c.ID], ids[i] = i, c.ID
	}
	owners, err := s.db.Query(ctx, `SELECT c.id, u.display_name, u.email FROM dav_collections c JOIN users u ON u.id = c.user_id WHERE c.id = ANY($1) AND c.user_id <> $2`, ids, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer owners.Close()
	for owners.Next() {
		var id string
		var o calendarOwner
		if err := owners.Scan(&id, &o.Name, &o.Email); err != nil {
			s.internalError(w, r, err)
			return
		}
		out[index[id]].Owner = &o
	}
	if err := owners.Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	shares, err := s.db.Query(ctx, `
		SELECT s.collection_id, s.id, s.access, u.email, u.display_name, o.id, o.name FROM calendar_shares s
		JOIN dav_collections c ON c.id = s.collection_id LEFT JOIN users u ON u.id = s.user_id LEFT JOIN organizations o ON o.id = s.org_id
		WHERE c.id = ANY($1) AND c.user_id = $2 ORDER BY s.created_at`, ids, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer shares.Close()
	for shares.Next() {
		var id string
		var sh calendarShare
		if err := shares.Scan(&id, &sh.ID, &sh.Access, &sh.Email, &sh.Name, &sh.OrgID, &sh.OrgName); err != nil {
			s.internalError(w, r, err)
			return
		}
		out[index[id]].Shares = append(out[index[id]].Shares, sh)
	}
	if err := shares.Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func validBookInput(w http.ResponseWriter, in *bookInput) bool {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Color = strings.TrimSpace(in.Color)
	switch {
	case in.Name == "" || len(in.Name) > 100:
		writeError(w, http.StatusUnprocessableEntity, "invalid_book", "Give the address book a name of up to 100 characters.")
		return false
	case len(in.Description) > 500:
		writeError(w, http.StatusUnprocessableEntity, "invalid_book", "Use 500 characters or fewer for the description.")
		return false
	case !colorPattern.MatchString(in.Color):
		writeError(w, http.StatusUnprocessableEntity, "invalid_book", "Choose a color like #3b82f6, or leave it empty.")
		return false
	}
	return true
}

func (s *Server) createAddressBook(w http.ResponseWriter, r *http.Request, p *principal) {
	var in bookInput
	if !decode(w, r, 8<<10, &in) || !validBookInput(w, &in) {
		return
	}
	if _, err := s.pimContext(r, p); err != nil {
		s.internalError(w, r, err)
		return
	}
	slug := secrets.Token(10)
	if _, err := s.db.Exec(r.Context(), `INSERT INTO dav_collections (user_id, kind, slug, name, description, color) VALUES ($1, 'addressbook', $2, $3, $4, $5)`, p.User.ID, slug, in.Name, in.Description, in.Color); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, addressBook{ID: slug, Name: in.Name, Description: in.Description, Color: in.Color, Access: "owner"})
}

func (s *Server) ownedAddressBook(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal) (string, bool) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM dav_collections WHERE user_id = $1 AND kind = 'addressbook' AND slug = $2`, p.User.ID, r.PathValue("bookId")).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return "", false
	}
	if err != nil {
		s.internalError(w, r, err)
		return "", false
	}
	return id, true
}

func (s *Server) updateAddressBook(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.ownedAddressBook(r.Context(), w, r, p)
	if !ok {
		return
	}
	var in bookInput
	if !decode(w, r, 8<<10, &in) || !validBookInput(w, &in) {
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE dav_collections SET name = $2, description = $3, color = $4, updated_at = now() WHERE id = $1`, id, in.Name, in.Description, in.Color); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteAddressBook(w http.ResponseWriter, r *http.Request, p *principal) {
	if r.PathValue("bookId") == defaultAddressBook {
		writeError(w, http.StatusUnprocessableEntity, "default_book", "Your main address book can’t be deleted.")
		return
	}
	id, ok := s.ownedAddressBook(r.Context(), w, r, p)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM dav_collections WHERE id = $1`, id); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) shareAddressBook(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.ownedAddressBook(r.Context(), w, r, p)
	if !ok {
		return
	}
	s.shareCollection(w, r, p, id, "address book")
}

func (s *Server) unshareAddressBook(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.ownedAddressBook(r.Context(), w, r, p)
	if !ok {
		return
	}
	s.unshareCollection(w, r, id)
}

type groupMember struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Emails []string `json:"emails"`
}

type contactGroup struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	BookID   string        `json:"bookId"`
	ReadOnly bool          `json:"readOnly,omitempty"`
	Members  []groupMember `json:"members"`
}

type groupInput struct {
	Name    string   `json:"name"`
	BookID  string   `json:"bookId"`
	Members []string `json:"members"`
}

type parsedGroup struct {
	book collectionRow
	name string
	card vcard.Card
}

func (s *Server) listContactGroups(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	books, err := s.addressBooks(ctx, r.URL.Query().Get("book"))
	if err != nil {
		s.davError(w, r, err)
		return
	}
	groups, err := s.groupsIn(ctx, books, "%"+strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))+"%")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) getContactGroup(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	group, err := s.loadGroup(ctx, r.PathValue("id"), r.URL.Query().Get("book"))
	if errors.Is(err, errAmbiguousObject) {
		writeError(w, http.StatusConflict, "ambiguous", "This group is in more than one address book. Choose the address book.")
		return
	}
	if err != nil {
		s.davError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, group)
}

func (s *Server) saveContactGroup(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var in groupInput
	if !decode(w, r, 64<<10, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_group", "Give the group a name of up to 200 characters.")
		return
	}
	if len(in.Members) > maxGroupMembers {
		writeError(w, http.StatusUnprocessableEntity, "invalid_group", "A group can contain up to 500 people.")
		return
	}
	name := r.PathValue("id")
	var book *collectionRow
	var uid string
	var ifMatch webdav.ConditionalMatch
	status := http.StatusCreated
	slug := in.BookID
	if slug == "" {
		slug = r.URL.Query().Get("book")
	}
	if name == "" {
		if slug == "" {
			slug = defaultAddressBook
		}
		book, err = s.writableCollection(ctx, "addressbook", slug)
		uid = secrets.Token(16)
		name = uid + ".vcf"
	} else {
		var card vcard.Card
		book, card, ifMatch, err = s.findAddressObject(ctx, name, slug, true)
		if errors.Is(err, errAmbiguousObject) {
			writeError(w, http.StatusConflict, "ambiguous", "This group is in more than one address book. Choose the address book.")
			return
		}
		if err != nil {
			s.davError(w, r, err)
			return
		}
		if _, err = s.writableCollection(ctx, "addressbook", book.Slug); err != nil {
			s.davError(w, r, err)
			return
		}
		uid = strings.TrimSpace(card.Value(vcard.FieldUID))
		if uid == "" {
			uid = strings.TrimSuffix(name, ".vcf")
		}
		status = http.StatusOK
	}
	if err != nil {
		s.davError(w, r, err)
		return
	}
	members, msg, err := s.groupMemberUIDs(ctx, book.ID, in.Members)
	if msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_group", msg)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	card := buildGroupCard(uid, in.Name, members)
	fields, err := cardFields(card)
	if err != nil {
		s.davError(w, r, err)
		return
	}
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		s.internalError(w, r, err)
		return
	}
	if _, err := s.putDAVObject(ctx, book.ID, name, buf.String(), fields, ifMatch, ""); err != nil {
		s.davError(w, r, err)
		return
	}
	group, err := s.presentGroup(ctx, parsedGroup{book: *book, name: name, card: card})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, group)
}

func (s *Server) deleteContactGroup(w http.ResponseWriter, r *http.Request, p *principal) {
	name := r.PathValue("id")
	if !objectNamePattern.MatchString(name) {
		notFound(w)
		return
	}
	ctx, err := s.pimContext(r, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	book, _, _, err := s.findAddressObject(ctx, name, r.URL.Query().Get("book"), true)
	if errors.Is(err, errAmbiguousObject) {
		writeError(w, http.StatusConflict, "ambiguous", "This group is in more than one address book. Choose the address book.")
		return
	}
	if err != nil {
		s.davError(w, r, err)
		return
	}
	book, err = s.writableCollection(ctx, "addressbook", book.Slug)
	if err != nil {
		s.davError(w, r, err)
		return
	}
	if err := s.deleteDAVObject(ctx, book.ID, name); err != nil {
		s.davError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) groupMemberUIDs(ctx context.Context, collectionID string, names []string) ([]string, string, error) {
	seen := map[string]bool{}
	var unique []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !objectNamePattern.MatchString(name) {
			return nil, "Choose people from this address book.", nil
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		unique = append(unique, name)
	}
	if len(unique) == 0 {
		return []string{}, "", nil
	}
	if len(unique) > maxGroupMembers {
		return nil, "A group can contain up to 500 people.", nil
	}
	rows, err := s.db.Query(ctx, `SELECT name, uid, is_group FROM dav_objects WHERE collection_id = $1 AND name = ANY($2)`, collectionID, unique)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	type foundContact struct {
		uid   string
		group bool
	}
	found := map[string]foundContact{}
	for rows.Next() {
		var name, uid string
		var group bool
		if err := rows.Scan(&name, &uid, &group); err != nil {
			return nil, "", err
		}
		found[name] = foundContact{uid: uid, group: group}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(found) != len(unique) {
		return nil, "Choose people from this address book.", nil
	}
	uids := make([]string, 0, len(unique))
	seenUID := map[string]bool{}
	for _, name := range unique {
		item := found[name]
		if item.group {
			return nil, "A group can’t contain another group.", nil
		}
		uid := normalizeUID(item.uid)
		if uid == "" {
			return nil, "One of these contacts has no identifier and can’t be added to a group.", nil
		}
		if seenUID[strings.ToLower(uid)] {
			continue
		}
		seenUID[strings.ToLower(uid)] = true
		uids = append(uids, uid)
	}
	return uids, "", nil
}

func (s *Server) loadGroup(ctx context.Context, name, bookSlug string) (contactGroup, error) {
	book, card, _, err := s.findAddressObject(ctx, name, bookSlug, true)
	if err != nil {
		return contactGroup{}, err
	}
	return s.presentGroup(ctx, parsedGroup{book: *book, name: name, card: card})
}

func (s *Server) groupsIn(ctx context.Context, books []collectionRow, q string) ([]contactGroup, error) {
	out := []contactGroup{}
	if len(books) == 0 {
		return out, nil
	}
	byID := map[string]collectionRow{}
	ids := make([]string, len(books))
	for i, book := range books {
		byID[book.ID] = book
		ids[i] = book.ID
	}
	rows, err := s.db.Query(ctx, `
		SELECT collection_id, name, data FROM dav_objects
		WHERE collection_id = ANY($1) AND is_group AND lower(summary) LIKE $2
		ORDER BY lower(summary), name LIMIT 500`, ids, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var parsed []parsedGroup
	for rows.Next() {
		var collectionID, name, data string
		if err := rows.Scan(&collectionID, &name, &data); err != nil {
			return nil, err
		}
		card, err := vcard.NewDecoder(strings.NewReader(data)).Decode()
		if err != nil {
			continue
		}
		parsed = append(parsed, parsedGroup{book: byID[collectionID], name: name, card: card})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.presentGroups(ctx, parsed)
}

func (s *Server) presentGroup(ctx context.Context, item parsedGroup) (contactGroup, error) {
	groups, err := s.presentGroups(ctx, []parsedGroup{item})
	if err != nil || len(groups) == 0 {
		return contactGroup{}, err
	}
	return groups[0], nil
}

func (s *Server) presentGroups(ctx context.Context, items []parsedGroup) ([]contactGroup, error) {
	out := []contactGroup{}
	if len(items) == 0 {
		return out, nil
	}
	ids := map[string]struct{}{}
	var lowered []string
	seenUID := map[string]bool{}
	for _, item := range items {
		ids[item.book.ID] = struct{}{}
		for _, uid := range memberUIDs(item.card) {
			key := strings.ToLower(uid)
			if seenUID[key] {
				continue
			}
			seenUID[key] = true
			lowered = append(lowered, key)
		}
	}
	collectionIDs := make([]string, 0, len(ids))
	for id := range ids {
		collectionIDs = append(collectionIDs, id)
	}
	members, err := s.membersByUID(ctx, collectionIDs, lowered)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		name := strings.TrimSpace(item.card.PreferredValue(vcard.FieldFormattedName))
		if name == "" {
			name = "Unnamed group"
		}
		group := contactGroup{ID: item.name, Name: name, BookID: item.book.Slug, ReadOnly: item.book.Access == "read", Members: []groupMember{}}
		seen := map[string]bool{}
		for _, uid := range memberUIDs(item.card) {
			key := strings.ToLower(uid)
			if seen[key] {
				continue
			}
			seen[key] = true
			if member, ok := members[item.book.ID][key]; ok {
				group.Members = append(group.Members, member)
			}
		}
		out = append(out, group)
	}
	return out, nil
}

func (s *Server) membersByUID(ctx context.Context, collectionIDs, lowered []string) (map[string]map[string]groupMember, error) {
	out := map[string]map[string]groupMember{}
	if len(collectionIDs) == 0 || len(lowered) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT collection_id, name, uid, summary, emails FROM dav_objects
		WHERE collection_id = ANY($1) AND NOT is_group
		  AND lower(regexp_replace(uid, '^[Uu][Rr][Nn]:[Uu][Uu][Ii][Dd]:', '')) = ANY($2)`, collectionIDs, lowered)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var collectionID, name, uid, summary string
		var emails []string
		if err := rows.Scan(&collectionID, &name, &uid, &summary, &emails); err != nil {
			return nil, err
		}
		if out[collectionID] == nil {
			out[collectionID] = map[string]groupMember{}
		}
		out[collectionID][strings.ToLower(normalizeUID(uid))] = groupMember{ID: name, Name: summary, Emails: nonNil(emails)}
	}
	return out, rows.Err()
}

type recipientMember struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

type recipient struct {
	Name    string            `json:"name"`
	Address string            `json:"address"`
	Source  string            `json:"source"`
	Kind    string            `json:"kind,omitempty"`
	Company string            `json:"company,omitempty"`
	Phone   string            `json:"phone,omitempty"`
	Members []recipientMember `json:"members,omitempty"`
}

func (s *Server) suggestGroups(ctx context.Context, books []collectionRow, like string) ([]recipient, error) {
	if len(books) == 0 {
		return nil, nil
	}
	byID := map[string]collectionRow{}
	ids := make([]string, len(books))
	for i, book := range books {
		byID[book.ID] = book
		ids[i] = book.ID
	}
	rows, err := s.db.Query(ctx, `
		SELECT collection_id, name, data FROM dav_objects
		WHERE collection_id = ANY($1) AND is_group AND lower(summary) LIKE $2
		ORDER BY lower(summary), name LIMIT 4`, ids, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var parsed []parsedGroup
	for rows.Next() {
		var collectionID, name, data string
		if err := rows.Scan(&collectionID, &name, &data); err != nil {
			return nil, err
		}
		card, err := vcard.NewDecoder(strings.NewReader(data)).Decode()
		if err != nil {
			continue
		}
		parsed = append(parsed, parsedGroup{book: byID[collectionID], name: name, card: card})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	groups, err := s.presentGroups(ctx, parsed)
	if err != nil {
		return nil, err
	}
	out := make([]recipient, 0, len(groups))
	for _, group := range groups {
		seen := map[string]bool{}
		var members []recipientMember
		for _, member := range group.Members {
			for _, email := range member.Emails {
				key := strings.ToLower(email)
				if email == "" || seen[key] {
					continue
				}
				seen[key] = true
				members = append(members, recipientMember{Name: member.Name, Address: email})
			}
		}
		name := group.Name
		if name == "" {
			name = "Unnamed group"
		}
		out = append(out, recipient{Name: name, Address: "group:" + group.BookID + "/" + group.ID, Source: "group", Members: members})
	}
	return out, nil
}
