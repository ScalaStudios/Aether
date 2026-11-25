package api

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type alias struct {
	ID               string   `json:"id"`
	OrgID            string   `json:"orgId"`
	DomainID         string   `json:"domainId"`
	Address          string   `json:"address"`
	LocalPart        string   `json:"localPart"`
	Destinations     []string `json:"destinations"`
	Active           bool     `json:"active"`
	Kind             string   `json:"kind"`
	Name             string   `json:"name"`
	AcceptFrom       string   `json:"acceptFrom"`
	AllowedSenders   []string `json:"allowedSenders"`
	SendAs           []string `json:"sendAs"`
	Owners           []string `json:"owners"`
	Groups           []string `json:"groups"`
	MemberDomains    []string `json:"memberDomains"`
	Moderated        bool     `json:"moderated"`
	Moderators       []string `json:"moderators"`
	ModerationExempt []string `json:"moderationExempt"`
	Hidden           bool     `json:"hiddenFromDirectory"`
}

const aliasColumns = `a.id, a.org_id, a.domain_id, a.address, a.local_part, a.destinations, a.active, a.kind, a.name, a.accept_from, a.allowed_senders, a.send_as,
	coalesce((SELECT array_agg(o.user_id::text ORDER BY o.user_id) FROM alias_owners o WHERE o.alias_id = a.id), '{}'),
	coalesce((SELECT array_agg(g.group_id::text ORDER BY g.group_id) FROM alias_groups g WHERE g.alias_id = a.id), '{}'), a.member_domains::text[],
	a.moderated, coalesce((SELECT array_agg(m.user_id::text ORDER BY m.user_id) FROM alias_moderators m WHERE m.alias_id = a.id), '{}'), a.moderation_exempt, a.hidden_from_directory`

var acceptFromValues = []string{"anyone", "organization", "members", "allowlist"}

type aliasInput struct {
	Kind               *string   `json:"kind"`
	Name               *string   `json:"name"`
	Destinations       *[]string `json:"destinations"`
	AddDestinations    []string  `json:"addDestinations"`
	RemoveDestinations []string  `json:"removeDestinations"`
	Active             *bool     `json:"active"`
	AcceptFrom         *string   `json:"acceptFrom"`
	AllowedSenders     *[]string `json:"allowedSenders"`
	SendAs             *[]string `json:"sendAs"`
	Owners             *[]string `json:"owners"`
	Groups             *[]string `json:"groups"`
	MemberDomains      *[]string `json:"memberDomains"`
	Moderated          *bool     `json:"moderated"`
	Moderators         *[]string `json:"moderators"`
	ModerationExempt   *[]string `json:"moderationExempt"`
	Hidden             *bool     `json:"hiddenFromDirectory"`
}

func (s *Server) loadAlias(ctx context.Context, id string) (*alias, error) {
	rows, err := s.db.Query(ctx, `SELECT `+aliasColumns+` FROM aliases a WHERE a.id = $1`, id)
	if err != nil {
		return nil, err
	}
	a, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByPos[alias])
	return a, err
}

func (s *Server) listAliases(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+aliasColumns+` FROM aliases a WHERE a.org_id = $1 ORDER BY a.address`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[alias])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) myLists(w http.ResponseWriter, r *http.Request, p *principal) {
	rows, err := s.db.Query(r.Context(), `SELECT `+aliasColumns+` FROM aliases a JOIN alias_owners o ON o.alias_id = a.id WHERE o.user_id = $1 ORDER BY a.address`, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[alias])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func parseAddressList(w http.ResponseWriter, list []string, allowDomains bool) ([]string, bool) {
	out := make([]string, 0, len(list))
	for _, d := range list {
		d = strings.ToLower(strings.TrimSpace(d))
		if allowDomains && strings.HasPrefix(d, "@") && domainPattern.MatchString(d[1:]) {
			out = append(out, d)
			continue
		}
		a, err := mail.ParseAddress(d)
		if err != nil || a.Name != "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_address", d+" is not a valid email address.")
			return nil, false
		}
		if !slices.Contains(out, strings.ToLower(a.Address)) {
			out = append(out, strings.ToLower(a.Address))
		}
	}
	return out, true
}

func (s *Server) applyAliasInput(w http.ResponseWriter, r *http.Request, a *alias, in *aliasInput) bool {
	if in.Kind != nil {
		if *in.Kind != "alias" && *in.Kind != "list" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_kind", "kind must be alias or list.")
			return false
		}
		a.Kind = *in.Kind
	}
	if in.Name != nil {
		a.Name = strings.TrimSpace(*in.Name)
		if len(a.Name) > 200 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_name", "Keep the name under 200 characters.")
			return false
		}
	}
	if in.Active != nil {
		a.Active = *in.Active
	}
	if in.Hidden != nil {
		a.Hidden = *in.Hidden
	}
	var ok bool
	if in.Destinations != nil {
		if a.Destinations, ok = parseAddressList(w, *in.Destinations, false); !ok {
			return false
		}
	}
	add, ok := parseAddressList(w, in.AddDestinations, false)
	if !ok {
		return false
	}
	for _, d := range add {
		if !slices.Contains(a.Destinations, d) {
			a.Destinations = append(a.Destinations, d)
		}
	}
	remove, ok := parseAddressList(w, in.RemoveDestinations, false)
	if !ok {
		return false
	}
	a.Destinations = slices.DeleteFunc(a.Destinations, func(d string) bool { return slices.Contains(remove, d) })
	if in.Groups != nil {
		a.Groups = nonNil(slices.Compact(slices.Sorted(slices.Values(*in.Groups))))
		var known int
		if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM directory_groups WHERE id::text = ANY($1)`, a.Groups).Scan(&known); err != nil {
			s.internalError(w, r, err)
			return false
		}
		if known != len(a.Groups) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_groups", "Choose groups provisioned by an identity provider.")
			return false
		}
	}
	if in.MemberDomains != nil {
		a.MemberDomains = nonNil(slices.Compact(slices.Sorted(slices.Values(*in.MemberDomains))))
		var owned int
		if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM domains WHERE org_id = $1 AND id::text = ANY($2)`, a.OrgID, a.MemberDomains).Scan(&owned); err != nil {
			s.internalError(w, r, err)
			return false
		}
		if owned != len(a.MemberDomains) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_member_domains", "Choose domains that belong to this organization.")
			return false
		}
	}
	limit := 100
	if a.Kind == "list" {
		limit = 5000
	}
	if len(a.Destinations) > limit {
		writeError(w, http.StatusUnprocessableEntity, "invalid_destinations", "Add no more than "+strconv.Itoa(limit)+" destinations.")
		return false
	}
	if len(a.Destinations) == 0 && (a.Kind == "alias" || len(a.Groups)+len(a.MemberDomains) == 0) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_destinations", "Add at least one destination.")
		return false
	}
	if in.AcceptFrom != nil {
		if !slices.Contains(acceptFromValues, *in.AcceptFrom) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_accept_from", "acceptFrom must be anyone, organization, members or allowlist.")
			return false
		}
		a.AcceptFrom = *in.AcceptFrom
	}
	if in.AllowedSenders != nil {
		if a.AllowedSenders, ok = parseAddressList(w, *in.AllowedSenders, true); !ok {
			return false
		}
	}
	if in.SendAs != nil {
		if a.SendAs, ok = parseAddressList(w, *in.SendAs, false); !ok {
			return false
		}
		var foreign int
		if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM unnest($1::text[]) x WHERE NOT EXISTS (SELECT 1 FROM mailboxes m WHERE m.address = x AND m.org_id = $2)`, a.SendAs, a.OrgID).Scan(&foreign); err != nil {
			s.internalError(w, r, err)
			return false
		}
		if foreign > 0 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_send_as", "Only mailboxes in this organization can send as the list.")
			return false
		}
	}
	if in.Owners != nil {
		if a.Owners, ok = s.orgUsers(w, r, a.OrgID, *in.Owners, "invalid_owners", "Owners must be members of this organization."); !ok {
			return false
		}
	}
	if in.Moderated != nil {
		a.Moderated = *in.Moderated
	}
	if in.Moderators != nil {
		if a.Moderators, ok = s.orgUsers(w, r, a.OrgID, *in.Moderators, "invalid_moderators", "Moderators must be members of this organization."); !ok {
			return false
		}
	}
	if in.ModerationExempt != nil {
		if a.ModerationExempt, ok = parseAddressList(w, *in.ModerationExempt, true); !ok {
			return false
		}
	}
	if a.Kind == "list" && a.LocalPart == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_local_part", "A distribution list needs an address, not a catch-all.")
		return false
	}
	if a.Kind == "alias" && (a.AcceptFrom != "anyone" || len(a.AllowedSenders) > 0 || len(a.SendAs) > 0 || len(a.Owners) > 0 || len(a.Groups) > 0 || len(a.MemberDomains) > 0 || a.Moderated || len(a.Moderators) > 0 || len(a.ModerationExempt) > 0 || a.Hidden) {
		writeError(w, http.StatusUnprocessableEntity, "list_only", "Sender restrictions, moderation, send-as, owners, groups, domain members and hiding from the directory are only available on distribution lists.")
		return false
	}
	if a.Moderated && len(a.Moderators) == 0 && len(a.Owners) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_moderators", "Add a moderator or an owner to approve messages sent to the list.")
		return false
	}
	if a.Moderated && s.cfg.SieveRelayPassword == "" {
		writeError(w, http.StatusUnprocessableEntity, "moderation_unavailable", "Moderation needs AETHER_SIEVE_RELAY_PASSWORD set on the API.")
		return false
	}
	return true
}

func (s *Server) orgUsers(w http.ResponseWriter, r *http.Request, orgID string, ids []string, code, message string) ([]string, bool) {
	ids = slices.Compact(append([]string{}, slices.Sorted(slices.Values(ids))...))
	var members int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM organization_members WHERE org_id = $1 AND user_id::text = ANY($2)`, orgID, ids).Scan(&members); err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	if members != len(ids) {
		writeError(w, http.StatusUnprocessableEntity, code, message)
		return nil, false
	}
	return ids, true
}

func (s *Server) saveAlias(ctx context.Context, a *alias, create bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if create {
		err = tx.QueryRow(ctx, `INSERT INTO aliases (org_id, domain_id, local_part, address, destinations, active, kind, name, accept_from, allowed_senders, send_as, member_domains, moderated, moderation_exempt, hidden_from_directory)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id`,
			a.OrgID, a.DomainID, a.LocalPart, a.Address, a.Destinations, a.Active, a.Kind, a.Name, a.AcceptFrom, a.AllowedSenders, a.SendAs, a.MemberDomains, a.Moderated, a.ModerationExempt, a.Hidden).Scan(&a.ID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE aliases SET destinations = $2, active = $3, kind = $4, name = $5, accept_from = $6, allowed_senders = $7, send_as = $8, member_domains = $9, moderated = $10, moderation_exempt = $11, hidden_from_directory = $12, updated_at = now() WHERE id = $1`,
			a.ID, a.Destinations, a.Active, a.Kind, a.Name, a.AcceptFrom, a.AllowedSenders, a.SendAs, a.MemberDomains, a.Moderated, a.ModerationExempt, a.Hidden)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM alias_owners WHERE alias_id = $1 AND NOT (user_id::text = ANY($2))`, a.ID, a.Owners); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO alias_owners (alias_id, user_id) SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, a.ID, a.Owners); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM alias_groups WHERE alias_id = $1 AND NOT (group_id::text = ANY($2))`, a.ID, a.Groups); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO alias_groups (alias_id, group_id) SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, a.ID, a.Groups); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM alias_moderators WHERE alias_id = $1 AND NOT (user_id::text = ANY($2))`, a.ID, a.Moderators); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO alias_moderators (alias_id, user_id) SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, a.ID, a.Moderators); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) createAlias(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		aliasInput
		DomainID  string `json:"domainId"`
		LocalPart string `json:"localPart"`
	}
	if !decode(w, r, 512<<10, &in) {
		return
	}
	in.LocalPart = strings.ToLower(strings.TrimSpace(in.LocalPart))
	if in.LocalPart != "" && !localPartPattern.MatchString(in.LocalPart) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_local_part", "Use letters, digits, dots, hyphens, underscores, or plus signs, or leave it empty for a catch-all.")
		return
	}
	a := &alias{OrgID: orgID, DomainID: in.DomainID, LocalPart: in.LocalPart, Active: true, Kind: "alias", AcceptFrom: "anyone", Destinations: []string{}, AllowedSenders: []string{}, SendAs: []string{}, Owners: []string{}, Groups: []string{}, MemberDomains: []string{}, Moderators: []string{}, ModerationExempt: []string{}}
	if !s.applyAliasInput(w, r, a, &in.aliasInput) {
		return
	}
	domainName, err := s.orgDomain(r, orgID, in.DomainID)
	if errors.Is(err, pgx.ErrNoRows) || !uuidPattern.MatchString(in.DomainID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_domain", "Choose a domain that belongs to this organization.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	a.Address = in.LocalPart + "@" + domainName
	var exists bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM mailboxes WHERE address = $1)`, a.Address).Scan(&exists); err != nil {
		s.internalError(w, r, err)
		return
	}
	if exists {
		writeError(w, http.StatusConflict, "address_taken", a.Address+" is already a mailbox.")
		return
	}
	err = s.saveAlias(r.Context(), a, true)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "address_taken", a.Address+" is already an alias.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "alias.create", "alias", a.ID, map[string]any{"address": a.Address, "kind": a.Kind, "destinations": a.Destinations})
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) aliasFromPath(w http.ResponseWriter, r *http.Request, p *principal) (*alias, bool) {
	id, ok := pathID(w, r, "aliasId")
	if !ok {
		return nil, false
	}
	a, err := s.loadAlias(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.orgRole(a.OrgID) == "") {
		notFound(w)
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return a, true
}

func (s *Server) getAlias(w http.ResponseWriter, r *http.Request, p *principal) {
	a, ok := s.aliasFromPath(w, r, p)
	if !ok {
		return
	}
	if !p.canAdminOrg(a.OrgID) && !slices.Contains(a.Owners, p.User.ID) {
		forbidden(w)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) updateAlias(w http.ResponseWriter, r *http.Request, p *principal) {
	a, ok := s.aliasFromPath(w, r, p)
	if !ok {
		return
	}
	var in aliasInput
	if !decode(w, r, 512<<10, &in) {
		return
	}
	if !p.canAdminOrg(a.OrgID) {
		membershipOnly := in.Kind == nil && in.Name == nil && in.Active == nil && in.AcceptFrom == nil && in.AllowedSenders == nil && in.SendAs == nil && in.Owners == nil && in.Groups == nil && in.MemberDomains == nil &&
			in.Moderated == nil && in.Moderators == nil && in.ModerationExempt == nil && in.Hidden == nil
		if !slices.Contains(a.Owners, p.User.ID) || !membershipOnly {
			forbidden(w)
			return
		}
	}
	before := *a
	if !s.applyAliasInput(w, r, a, &in) {
		return
	}
	if err := s.saveAlias(r.Context(), a, false); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &a.OrgID, "alias.update", "alias", a.ID, map[string]any{"address": a.Address, "before": aliasAuditFields(&before), "after": aliasAuditFields(a)})
	writeJSON(w, http.StatusOK, a)
}

func aliasAuditFields(a *alias) map[string]any {
	return map[string]any{"kind": a.Kind, "name": a.Name, "active": a.Active, "destinations": a.Destinations, "acceptFrom": a.AcceptFrom, "allowedSenders": a.AllowedSenders, "sendAs": a.SendAs, "owners": a.Owners, "groups": a.Groups, "memberDomains": a.MemberDomains,
		"moderated": a.Moderated, "moderators": a.Moderators, "moderationExempt": a.ModerationExempt, "hiddenFromDirectory": a.Hidden}
}

func (s *Server) deleteAlias(w http.ResponseWriter, r *http.Request, p *principal) {
	a, ok := s.aliasFromPath(w, r, p)
	if !ok {
		return
	}
	if !p.canAdminOrg(a.OrgID) {
		forbidden(w)
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM aliases WHERE id = $1`, a.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &a.OrgID, "alias.delete", "alias", a.ID, map[string]any{"address": a.Address})
	w.WriteHeader(http.StatusNoContent)
}

type resolvedMember struct {
	Address string   `json:"address"`
	Name    string   `json:"name"`
	Direct  bool     `json:"direct"`
	Domain  bool     `json:"domain"`
	Groups  []string `json:"groups"`
}

func (s *Server) listResolvedMembers(w http.ResponseWriter, r *http.Request, p *principal) {
	a, ok := s.aliasFromPath(w, r, p)
	if !ok {
		return
	}
	if !p.canAdminOrg(a.OrgID) && !slices.Contains(a.Owners, p.User.ID) {
		forbidden(w)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT x.address, coalesce(min(coalesce(nullif(m.display_name, ''), nullif(u.display_name, ''))), ''),
			bool_or(x.direct), bool_or(NOT x.direct AND x.group_id IS NULL),
			coalesce(array_agg(DISTINCT g.display_name ORDER BY g.display_name) FILTER (WHERE g.id IS NOT NULL), '{}')
		FROM (
			SELECT unnest(destinations) AS address, true AS direct, NULL::uuid AS group_id FROM aliases WHERE id = $1
			UNION ALL
			SELECT address, false, group_id FROM list_dynamic_members WHERE alias_id = $1
		) x
		LEFT JOIN directory_groups g ON g.id = x.group_id
		LEFT JOIN mailboxes m ON m.address = x.address
		LEFT JOIN users u ON u.id = m.owner_user_id
		GROUP BY x.address ORDER BY x.address`, a.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[resolvedMember])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

type directoryGroup struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Members  int    `json:"members"`
}

func (s *Server) listDirectoryGroups(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT g.id, g.display_name, p.name,
			(SELECT count(DISTINCT m.address)::int FROM directory_group_members gm
				JOIN users u ON u.id = gm.user_id AND u.status <> 'deprovisioned'
				JOIN mailboxes m ON m.owner_user_id = u.id AND m.org_id = $1 AND m.kind = 'user' AND m.active
				WHERE gm.group_id = g.id)
		FROM directory_groups g
		JOIN oidc_providers p ON p.id = g.provider_id
		WHERE EXISTS (SELECT 1 FROM directory_group_members gm JOIN mailboxes m ON m.owner_user_id = gm.user_id AND m.org_id = $1 WHERE gm.group_id = g.id)
			OR EXISTS (SELECT 1 FROM alias_groups ag JOIN aliases a ON a.id = ag.alias_id AND a.org_id = $1 WHERE ag.group_id = g.id)
		ORDER BY g.display_name, p.name`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[directoryGroup])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}
