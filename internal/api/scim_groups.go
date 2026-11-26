package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const scimGroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"

var (
	scimDisplayNameFilter = regexp.MustCompile(`(?i)^\s*displayName\s+eq\s+"((?:[^"\\]|\\.)*)"\s*$`)
	scimMemberPath        = regexp.MustCompile(`(?i)^\s*members\s*\[\s*value\s+eq\s+"((?:[^"\\]|\\.)*)"\s*\]\s*$`)
	errSCIMUnknownMember  = errors.New("scim unknown member")
)

type scimMember struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Ref     string `json:"$ref,omitempty"`
}

type scimGroup struct {
	Schemas     []string       `json:"schemas"`
	ID          string         `json:"id,omitempty"`
	ExternalID  string         `json:"externalId,omitempty"`
	DisplayName string         `json:"displayName"`
	Members     []scimMember   `json:"members,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

func scimFilterValue(raw string) (string, bool) {
	var v string
	return v, json.Unmarshal([]byte(`"`+raw+`"`), &v) == nil
}

func (s *Server) scimGroups(ctx context.Context, p *providerRow, where string, withMembers bool, args ...any) ([]scimGroup, error) {
	rows, err := s.db.Query(ctx, `SELECT id, external_id, display_name FROM directory_groups WHERE provider_id = $1 AND `+where+` ORDER BY display_name LIMIT `+strconv.Itoa(scimMaxResults), append([]any{p.ID}, args...)...)
	if err != nil {
		return nil, err
	}
	groups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (scimGroup, error) {
		g := scimGroup{Schemas: []string{scimGroupSchema}}
		err := row.Scan(&g.ID, &g.ExternalID, &g.DisplayName)
		g.Meta = map[string]any{"resourceType": "Group", "location": s.scimBase(p) + "/Groups/" + g.ID}
		return g, err
	})
	if err != nil || !withMembers || len(groups) == 0 {
		return groups, err
	}
	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
	}
	rows, err = s.db.Query(ctx, `SELECT gm.group_id, u.id, u.email FROM directory_group_members gm JOIN users u ON u.id = gm.user_id WHERE gm.group_id::text = ANY($1) ORDER BY u.email`, ids)
	if err != nil {
		return nil, err
	}
	var groupID, userID, email string
	_, err = pgx.ForEachRow(rows, []any{&groupID, &userID, &email}, func() error {
		i := slices.Index(ids, groupID)
		groups[i].Members = append(groups[i].Members, scimMember{Value: userID, Display: email, Ref: s.scimBase(p) + "/Users/" + userID})
		return nil
	})
	return groups, err
}

func (s *Server) scimListGroups(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	withMembers := !strings.Contains(strings.ToLower(r.URL.Query().Get("excludedAttributes")), "members")
	where, args := "true", []any{}
	if filter := r.URL.Query().Get("filter"); filter != "" {
		m := scimDisplayNameFilter.FindStringSubmatch(filter)
		if m == nil {
			scimError(w, http.StatusBadRequest, "invalidFilter", `Only filters of the form displayName eq "name" are supported.`)
			return
		}
		name, ok := scimFilterValue(m[1])
		if !ok {
			scimError(w, http.StatusBadRequest, "invalidFilter", "The displayName in the filter is not a valid string.")
			return
		}
		where, args = "display_name = $2", []any{name}
	}
	groups, err := s.scimGroups(r.Context(), p, where, withMembers, args...)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	groups = nonNil(groups)
	scimJSON(w, http.StatusOK, map[string]any{"schemas": []string{scimListSchema}, "totalResults": len(groups), "startIndex": 1, "itemsPerPage": len(groups), "Resources": groups})
}

func (s *Server) scimGroupFromPath(w http.ResponseWriter, r *http.Request, p *providerRow) (*scimGroup, bool) {
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		scimError(w, http.StatusNotFound, "", "No such group.")
		return nil, false
	}
	withMembers := !strings.Contains(strings.ToLower(r.URL.Query().Get("excludedAttributes")), "members")
	groups, err := s.scimGroups(r.Context(), p, "id = $2", withMembers, id)
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	if len(groups) == 0 {
		scimError(w, http.StatusNotFound, "", "No such group.")
		return nil, false
	}
	return &groups[0], true
}

func (s *Server) scimGetGroup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	if g, ok := s.scimGroupFromPath(w, r, p); ok {
		scimJSON(w, http.StatusOK, g)
	}
}

func decodeSCIMGroup(w http.ResponseWriter, r *http.Request) (*scimGroup, bool) {
	var in scimGroup
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", "The request body is not a SCIM group.")
		return nil, false
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.DisplayName == "" || len(in.DisplayName) > 256 || len(in.ExternalID) > 256 {
		scimError(w, http.StatusBadRequest, "invalidValue", "displayName is required, and displayName and externalId must be 256 characters or fewer.")
		return nil, false
	}
	return &in, true
}

func memberValues(members []scimMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, strings.ToLower(strings.TrimSpace(m.Value)))
	}
	return out
}

func scimSetMembers(ctx context.Context, tx pgx.Tx, providerID, groupID, op string, values []string) error {
	values = nonNil(slices.Compact(slices.Sorted(slices.Values(values))))
	if op != "remove" {
		for _, v := range values {
			if !uuidPattern.MatchString(v) {
				return errSCIMUnknownMember
			}
		}
		var known int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users u WHERE u.id::text = ANY($2) AND `+scimReach, providerID, values).Scan(&known); err != nil {
			return err
		}
		if known != len(values) {
			return errSCIMUnknownMember
		}
	}
	var err error
	switch op {
	case "replace":
		if _, err = tx.Exec(ctx, `DELETE FROM directory_group_members WHERE group_id = $1 AND NOT (user_id::text = ANY($2))`, groupID, values); err != nil {
			return err
		}
		fallthrough
	case "add":
		_, err = tx.Exec(ctx, `INSERT INTO directory_group_members (group_id, user_id) SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, groupID, values)
	case "remove":
		_, err = tx.Exec(ctx, `DELETE FROM directory_group_members WHERE group_id = $1 AND user_id::text = ANY($2)`, groupID, values)
	}
	return err
}

func (s *Server) scimGroupError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case isUniqueViolation(err):
		scimError(w, http.StatusConflict, "uniqueness", "A group with this displayName already exists.")
	case errors.Is(err, errSCIMUnknownMember):
		scimError(w, http.StatusBadRequest, "invalidValue", "Every member value must be the id of a user linked to this identity provider.")
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) scimCreateGroup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	in, ok := decodeSCIMGroup(w, r)
	if !ok {
		return
	}
	var id string
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `INSERT INTO directory_groups (provider_id, external_id, display_name) VALUES ($1, $2, $3) RETURNING id`, p.ID, in.ExternalID, in.DisplayName).Scan(&id); err != nil {
			return err
		}
		return scimSetMembers(r.Context(), tx, p.ID, id, "add", memberValues(in.Members))
	})
	if err != nil {
		s.scimGroupError(w, r, err)
		return
	}
	s.audit(r.Context(), r, nil, nil, "scim.group.create", "directory_group", id, map[string]any{"provider": p.Slug, "displayName": in.DisplayName, "members": len(in.Members)})
	r.SetPathValue("id", id)
	if g, ok := s.scimGroupFromPath(w, r, p); ok {
		scimJSON(w, http.StatusCreated, g)
	}
}

func (s *Server) scimReplaceGroup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	g, ok := s.scimGroupFromPath(w, r, p)
	if !ok {
		return
	}
	in, ok := decodeSCIMGroup(w, r)
	if !ok {
		return
	}
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE directory_groups SET display_name = $2, external_id = $3, updated_at = now() WHERE id = $1`, g.ID, in.DisplayName, in.ExternalID); err != nil {
			return err
		}
		return scimSetMembers(r.Context(), tx, p.ID, g.ID, "replace", memberValues(in.Members))
	})
	if err != nil {
		s.scimGroupError(w, r, err)
		return
	}
	s.audit(r.Context(), r, nil, nil, "scim.group.update", "directory_group", g.ID, map[string]any{"provider": p.Slug, "displayName": in.DisplayName, "members": len(in.Members)})
	if g, ok := s.scimGroupFromPath(w, r, p); ok {
		scimJSON(w, http.StatusOK, g)
	}
}

func scimMemberValues(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var members []scimMember
	if json.Unmarshal(raw, &members) != nil {
		var one scimMember
		if json.Unmarshal(raw, &one) != nil {
			return nil, false
		}
		members = []scimMember{one}
	}
	return memberValues(members), true
}

func (s *Server) scimPatchGroup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	g, ok := s.scimGroupFromPath(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Operations []struct {
			Op    string `json:"op"`
			Path  string `json:"path"`
			Value any    `json:"value"`
		} `json:"Operations"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", "The request body is not a SCIM patch.")
		return
	}
	invalid := ""
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		for _, o := range in.Operations {
			op := strings.ToLower(o.Op)
			if op != "add" && op != "remove" && op != "replace" {
				invalid = "op must be add, remove or replace."
				return nil
			}
			values := map[string]any{}
			if m := scimMemberPath.FindStringSubmatch(o.Path); m != nil {
				id, ok := scimFilterValue(m[1])
				if !ok || op != "remove" {
					invalid = "A members[value eq \"…\"] path can only be removed."
					return nil
				}
				values["members"] = []scimMember{{Value: id}}
			} else if o.Path != "" {
				values[o.Path] = o.Value
			} else if m, ok := o.Value.(map[string]any); ok {
				values = m
			}
			for path, value := range values {
				switch strings.ToLower(path) {
				case "members":
					ids, ok := scimMemberValues(value)
					if !ok {
						invalid = "members must be a list of objects with a value."
						return nil
					}
					mode := op
					if op == "remove" && value == nil {
						mode = "replace"
					}
					if err := scimSetMembers(r.Context(), tx, p.ID, g.ID, mode, ids); err != nil {
						return err
					}
				case "displayname":
					name, ok := value.(string)
					name = strings.TrimSpace(name)
					if !ok || op == "remove" || name == "" || len(name) > 256 {
						invalid = "displayName must be a non-empty string of 256 characters or fewer."
						return nil
					}
					if _, err := tx.Exec(r.Context(), `UPDATE directory_groups SET display_name = $2 WHERE id = $1`, g.ID, name); err != nil {
						return err
					}
				case "externalid":
					id, _ := value.(string)
					if len(id) > 256 {
						invalid = "externalId must be 256 characters or fewer."
						return nil
					}
					if _, err := tx.Exec(r.Context(), `UPDATE directory_groups SET external_id = $2 WHERE id = $1`, g.ID, id); err != nil {
						return err
					}
				}
			}
		}
		if invalid != "" {
			return nil
		}
		_, err := tx.Exec(r.Context(), `UPDATE directory_groups SET updated_at = now() WHERE id = $1`, g.ID)
		return err
	})
	if invalid != "" {
		scimError(w, http.StatusBadRequest, "invalidValue", invalid)
		return
	}
	if err != nil {
		s.scimGroupError(w, r, err)
		return
	}
	s.audit(r.Context(), r, nil, nil, "scim.group.update", "directory_group", g.ID, map[string]any{"provider": p.Slug})
	if g, ok := s.scimGroupFromPath(w, r, p); ok {
		scimJSON(w, http.StatusOK, g)
	}
}

func (s *Server) scimDeleteGroup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	g, ok := s.scimGroupFromPath(w, r, p)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM directory_groups WHERE id = $1`, g.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, nil, nil, "scim.group.delete", "directory_group", g.ID, map[string]any{"provider": p.Slug, "displayName": g.DisplayName})
	w.WriteHeader(http.StatusNoContent)
}
