package api

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type alias struct {
	ID               string     `json:"id"`
	OrgID            string     `json:"orgId"`
	DomainID         string     `json:"domainId"`
	Address          string     `json:"address"`
	LocalPart        string     `json:"localPart"`
	Destinations     []string   `json:"destinations"`
	Active           bool       `json:"active"`
	Kind             string     `json:"kind"`
	Name             string     `json:"name"`
	AcceptFrom       string     `json:"acceptFrom"`
	AllowedSenders   []string   `json:"allowedSenders"`
	SendAs           []string   `json:"sendAs"`
	SentMailboxID    string     `json:"sentMailboxId,omitempty"`
	Owners           []string   `json:"owners"`
	Groups           []string   `json:"groups"`
	MemberDomains    []string   `json:"memberDomains"`
	Moderated        bool       `json:"moderated"`
	Moderators       []string   `json:"moderators"`
	ModerationExempt []string   `json:"moderationExempt"`
	Hidden           bool       `json:"hiddenFromDirectory"`
	PersonalOwnerID  *string    `json:"personalOwnerId,omitempty"`
	Note             string     `json:"note,omitempty"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
}

const aliasColumns = `a.id, a.org_id, a.domain_id, a.address, a.local_part, a.destinations, a.active, a.kind, a.name, a.accept_from, a.allowed_senders, a.send_as, coalesce(a.sent_mailbox_id::text, ''),
	coalesce((SELECT array_agg(o.user_id::text ORDER BY o.user_id) FROM alias_owners o WHERE o.alias_id = a.id), '{}'),
	coalesce((SELECT array_agg(g.group_id::text ORDER BY g.group_id) FROM alias_groups g WHERE g.alias_id = a.id), '{}'), a.member_domains::text[],
	a.moderated, coalesce((SELECT array_agg(m.user_id::text ORDER BY m.user_id) FROM alias_moderators m WHERE m.alias_id = a.id), '{}'), a.moderation_exempt, a.hidden_from_directory, a.personal_owner_id, a.note, a.expires_at`

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
	SentMailboxID      *string   `json:"sentMailboxId"`
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
	orgID, ok := s.orgPerm(w, r, p, permAliasesRead, permListsRead)
	if !ok {
		return
	}
	listsOnly := !p.can(orgID, permAliasesRead)
	var rows pgx.Rows
	var err error
	if scoped, isScoped := p.scopedDomains(orgID); isScoped {
		if listsOnly {
			rows, err = s.db.Query(r.Context(), `SELECT `+aliasColumns+` FROM aliases a WHERE a.org_id = $1 AND a.kind = 'list' AND a.domain_id::text = ANY($2) ORDER BY a.address`, orgID, scoped)
		} else {
			rows, err = s.db.Query(r.Context(), `SELECT `+aliasColumns+` FROM aliases a WHERE a.org_id = $1 AND a.domain_id::text = ANY($2) ORDER BY a.address`, orgID, scoped)
		}
	} else if listsOnly {
		rows, err = s.db.Query(r.Context(), `SELECT `+aliasColumns+` FROM aliases a WHERE a.org_id = $1 AND a.kind = 'list' ORDER BY a.address`, orgID)
	} else {
		rows, err = s.db.Query(r.Context(), `SELECT `+aliasColumns+` FROM aliases a WHERE a.org_id = $1 ORDER BY a.address`, orgID)
	}
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

func parseAddresses(list []string, allowDomains bool) ([]string, *fieldError) {
	out := make([]string, 0, len(list))
	for _, d := range list {
		d = strings.ToLower(strings.TrimSpace(d))
		if allowDomains && strings.HasPrefix(d, "@") && domainPattern.MatchString(d[1:]) {
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
			continue
		}
		a, err := mail.ParseAddress(d)
		if err != nil || a.Name != "" {
			return nil, &fieldError{http.StatusUnprocessableEntity, "invalid_address", d + " is not a valid email address."}
		}
		addr := strings.ToLower(a.Address)
		if !slices.Contains(out, addr) {
			out = append(out, addr)
		}
	}
	return out, nil
}

func (s *Server) resolveAliasOwners(ctx context.Context, orgID string, raw []string, extraMemberEmails map[string]bool) ([]string, *fieldError, error) {
	if len(raw) == 0 {
		return []string{}, nil, nil
	}
	var uuids, emails []string
	for _, entry := range raw {
		entry = strings.ToLower(strings.TrimSpace(entry))
		switch {
		case uuidPattern.MatchString(entry):
			if !slices.Contains(uuids, entry) {
				uuids = append(uuids, entry)
			}
		case strings.Contains(entry, "@"):
			if !slices.Contains(emails, entry) {
				emails = append(emails, entry)
			}
		default:
			return nil, &fieldError{http.StatusUnprocessableEntity, "invalid_owners", "Owners must be members of this organization."}, nil
		}
	}
	resolved := make([]string, 0, len(uuids)+len(emails))
	if len(uuids) > 0 {
		var count int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM organization_members WHERE org_id = $1 AND user_id::text = ANY($2)`, orgID, uuids).Scan(&count); err != nil {
			return nil, nil, err
		}
		if count != len(uuids) {
			return nil, &fieldError{http.StatusUnprocessableEntity, "invalid_owners", "Owners must be members of this organization."}, nil
		}
		resolved = append(resolved, uuids...)
	}
	if len(emails) > 0 {
		rows, err := s.db.Query(ctx, `SELECT u.email, u.id::text FROM organization_members om JOIN users u ON u.id = om.user_id WHERE om.org_id = $1 AND u.email = ANY($2)`, orgID, emails)
		if err != nil {
			return nil, nil, err
		}
		defer rows.Close()
		found := map[string]string{}
		for rows.Next() {
			var email, id string
			if err := rows.Scan(&email, &id); err != nil {
				return nil, nil, err
			}
			found[email] = id
		}
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
		for _, email := range emails {
			if id, ok := found[email]; ok {
				resolved = append(resolved, id)
			} else if extraMemberEmails[email] {
				resolved = append(resolved, email)
			} else {
				return nil, &fieldError{http.StatusUnprocessableEntity, "invalid_owners", email + " is not a member of this organization."}, nil
			}
		}
	}
	return nonNil(slices.Compact(slices.Sorted(slices.Values(resolved)))), nil, nil
}

func (s *Server) applyAliasChanges(ctx context.Context, a *alias, in *aliasInput, extraMailboxes, extraMemberEmails map[string]bool) (*fieldError, error) {
	if in.Kind != nil {
		if *in.Kind != "alias" && *in.Kind != "list" {
			return &fieldError{http.StatusUnprocessableEntity, "invalid_kind", "kind must be alias or list."}, nil
		}
		a.Kind = *in.Kind
	}
	if in.Name != nil {
		a.Name = strings.TrimSpace(*in.Name)
		if len(a.Name) > 200 {
			return &fieldError{http.StatusUnprocessableEntity, "invalid_name", "Keep the name under 200 characters."}, nil
		}
	}
	if in.Active != nil {
		a.Active = *in.Active
	}
	if in.Hidden != nil {
		a.Hidden = *in.Hidden
	}
	var ie *fieldError
	if in.Destinations != nil {
		if a.Destinations, ie = parseAddresses(*in.Destinations, false); ie != nil {
			return ie, nil
		}
	}
	add, ie := parseAddresses(in.AddDestinations, false)
	if ie != nil {
		return ie, nil
	}
	for _, d := range add {
		if !slices.Contains(a.Destinations, d) {
			a.Destinations = append(a.Destinations, d)
		}
	}
	remove, ie := parseAddresses(in.RemoveDestinations, false)
	if ie != nil {
		return ie, nil
	}
	a.Destinations = slices.DeleteFunc(a.Destinations, func(d string) bool { return slices.Contains(remove, d) })
	if in.Groups != nil {
		a.Groups = nonNil(slices.Compact(slices.Sorted(slices.Values(*in.Groups))))
		var known int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM directory_groups WHERE id::text = ANY($1)`, a.Groups).Scan(&known); err != nil {
			return nil, err
		}
		if known != len(a.Groups) {
			return &fieldError{http.StatusUnprocessableEntity, "invalid_groups", "Choose groups provisioned by an identity provider."}, nil
		}
	}
	if in.MemberDomains != nil {
		a.MemberDomains = nonNil(slices.Compact(slices.Sorted(slices.Values(*in.MemberDomains))))
		var owned int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM domains WHERE org_id = $1 AND id::text = ANY($2)`, a.OrgID, a.MemberDomains).Scan(&owned); err != nil {
			return nil, err
		}
		if owned != len(a.MemberDomains) {
			return &fieldError{http.StatusUnprocessableEntity, "invalid_member_domains", "Choose domains that belong to this organization."}, nil
		}
	}
	limit := 100
	if a.Kind == "list" {
		limit = 5000
	}
	if len(a.Destinations) > limit {
		return &fieldError{http.StatusUnprocessableEntity, "invalid_destinations", "Add no more than " + strconv.Itoa(limit) + " destinations."}, nil
	}
	if len(a.Destinations) == 0 && (a.Kind == "alias" || len(a.Groups)+len(a.MemberDomains) == 0) {
		return &fieldError{http.StatusUnprocessableEntity, "invalid_destinations", "Add at least one destination."}, nil
	}
	if in.AcceptFrom != nil {
		if !slices.Contains(acceptFromValues, *in.AcceptFrom) {
			return &fieldError{http.StatusUnprocessableEntity, "invalid_accept_from", "acceptFrom must be anyone, organization, members or allowlist."}, nil
		}
		a.AcceptFrom = *in.AcceptFrom
	}
	if in.AllowedSenders != nil {
		if a.AllowedSenders, ie = parseAddresses(*in.AllowedSenders, true); ie != nil {
			return ie, nil
		}
		if len(a.AllowedSenders) > 1000 {
			return &fieldError{http.StatusUnprocessableEntity, "invalid_allowed_senders", "Add no more than 1,000 allowed senders."}, nil
		}
	}
	if in.SendAs != nil {
		if a.SendAs, ie = parseAddresses(*in.SendAs, false); ie != nil {
			return ie, nil
		}
		if len(a.SendAs) > 1000 {
			return &fieldError{http.StatusUnprocessableEntity, "invalid_send_as", "Add no more than 1,000 send-as addresses."}, nil
		}
		var toCheck []string
		for _, addr := range a.SendAs {
			if !extraMailboxes[addr] {
				toCheck = append(toCheck, addr)
			}
		}
		if len(toCheck) > 0 {
			var foreign int
			if err := s.db.QueryRow(ctx, `SELECT count(*) FROM unnest($1::text[]) x WHERE NOT EXISTS (SELECT 1 FROM mailboxes m WHERE m.address = x AND m.org_id = $2)`, toCheck, a.OrgID).Scan(&foreign); err != nil {
				return nil, err
			}
			if foreign > 0 {
				return &fieldError{http.StatusUnprocessableEntity, "invalid_send_as", "Only mailboxes in this organization can send as the list."}, nil
			}
		}
	}
	if in.SentMailboxID != nil {
		a.SentMailboxID = *in.SentMailboxID
	}
	if a.Kind == "alias" && in.SentMailboxID == nil {
		a.SentMailboxID = ""
	}
	if a.SentMailboxID != "" {
		var valid bool
		if a.Kind == "list" && uuidPattern.MatchString(a.SentMailboxID) {
			if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailboxes WHERE id = $1 AND org_id = $2 AND kind = 'shared')`, a.SentMailboxID, a.OrgID).Scan(&valid); err != nil {
				return nil, err
			}
		}
		if !valid {
			return &fieldError{http.StatusUnprocessableEntity, "invalid_sent_mailbox", "Choose a shared mailbox in this organization for the list's sent copies."}, nil
		}
	}
	if in.Owners != nil {
		owners, ie, err := s.resolveAliasOwners(ctx, a.OrgID, *in.Owners, extraMemberEmails)
		if ie != nil || err != nil {
			return ie, err
		}
		a.Owners = owners
	}
	if in.Moderated != nil {
		a.Moderated = *in.Moderated
	}
	if in.Moderators != nil {
		ids := slices.Compact(append([]string{}, slices.Sorted(slices.Values(*in.Moderators))...))
		var members int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM organization_members WHERE org_id = $1 AND user_id::text = ANY($2)`, a.OrgID, ids).Scan(&members); err != nil {
			return nil, err
		}
		if members != len(ids) {
			return &fieldError{http.StatusUnprocessableEntity, "invalid_moderators", "Moderators must be members of this organization."}, nil
		}
		a.Moderators = ids
	}
	if in.ModerationExempt != nil {
		if a.ModerationExempt, ie = parseAddresses(*in.ModerationExempt, true); ie != nil {
			return ie, nil
		}
	}
	if a.Kind == "list" && a.LocalPart == "" {
		return &fieldError{http.StatusUnprocessableEntity, "invalid_local_part", "A distribution list needs an address, not a catch-all."}, nil
	}
	if a.Kind == "alias" && (a.AcceptFrom != "anyone" || len(a.AllowedSenders) > 0 || len(a.SendAs) > 0 || len(a.Owners) > 0 || len(a.Groups) > 0 || len(a.MemberDomains) > 0 || a.Moderated || len(a.Moderators) > 0 || len(a.ModerationExempt) > 0 || a.Hidden) {
		return &fieldError{http.StatusUnprocessableEntity, "list_only", "Sender restrictions, moderation, send-as, owners, groups, domain members and hiding from the directory are only available on distribution lists."}, nil
	}
	if a.Moderated && len(a.Moderators) == 0 && len(a.Owners) == 0 {
		return &fieldError{http.StatusUnprocessableEntity, "invalid_moderators", "Add a moderator or an owner to approve messages sent to the list."}, nil
	}
	if a.Moderated && s.cfg.SieveRelayPassword == "" {
		return &fieldError{http.StatusUnprocessableEntity, "moderation_unavailable", "Moderation needs AETHER_SIEVE_RELAY_PASSWORD set on the API."}, nil
	}
	return nil, nil
}

func (s *Server) applyAliasInput(w http.ResponseWriter, r *http.Request, a *alias, in *aliasInput) bool {
	ie, err := s.applyAliasChanges(r.Context(), a, in, nil, nil)
	if ie != nil {
		writeError(w, ie.Status, ie.Code, ie.Message)
		return false
	}
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	return true
}

func (s *Server) checkAliasDryRun(ctx context.Context, address string) (*fieldError, error) {
	var mailboxExists, aliasExists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailboxes WHERE address = $1), EXISTS (SELECT 1 FROM aliases WHERE address = $1)`, address).Scan(&mailboxExists, &aliasExists); err != nil {
		return nil, err
	}
	if mailboxExists {
		return &fieldError{http.StatusConflict, "address_taken", address + " is already a mailbox."}, nil
	}
	if aliasExists {
		return &fieldError{http.StatusConflict, "address_taken", address + " is already an alias."}, nil
	}
	return nil, nil
}

func (s *Server) createAliasRecord(ctx context.Context, a *alias) (*fieldError, error) {
	err := s.saveAlias(ctx, a, true, false)
	if errors.Is(err, errAddressIsMailbox) {
		return &fieldError{http.StatusConflict, "address_taken", a.Address + " is already a mailbox."}, nil
	}
	var le *limitError
	if errors.As(err, &le) {
		return &fieldError{http.StatusConflict, le.code, le.message}, nil
	}
	if isUniqueViolation(err) {
		return &fieldError{http.StatusConflict, "address_taken", a.Address + " is already an alias."}, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, nil
}

var errAddressIsMailbox = errors.New("address is a mailbox")

func (s *Server) saveAlias(ctx context.Context, a *alias, create, kindChanged bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if create {
		if err := lockAddress(ctx, tx, a.Address); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailboxes WHERE address = $1)`, a.Address).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return errAddressIsMailbox
		}
	}
	if create || kindChanged {
		if err := s.checkAliasLimit(ctx, tx, a.OrgID, a.Kind); err != nil {
			return err
		}
	}
	if create {
		err = tx.QueryRow(ctx, `INSERT INTO aliases (org_id, domain_id, local_part, address, destinations, active, kind, name, accept_from, allowed_senders, send_as, member_domains, moderated, moderation_exempt, hidden_from_directory, sent_mailbox_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, nullif($16, '')::uuid) RETURNING id`,
			a.OrgID, a.DomainID, a.LocalPart, a.Address, a.Destinations, a.Active, a.Kind, a.Name, a.AcceptFrom, a.AllowedSenders, a.SendAs, a.MemberDomains, a.Moderated, a.ModerationExempt, a.Hidden, a.SentMailboxID).Scan(&a.ID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE aliases SET destinations = $2, active = $3, kind = $4, name = $5, accept_from = $6, allowed_senders = $7, send_as = $8, member_domains = $9, moderated = $10, moderation_exempt = $11, hidden_from_directory = $12, sent_mailbox_id = nullif($13, '')::uuid, updated_at = now() WHERE id = $1`,
			a.ID, a.Destinations, a.Active, a.Kind, a.Name, a.AcceptFrom, a.AllowedSenders, a.SendAs, a.MemberDomains, a.Moderated, a.ModerationExempt, a.Hidden, a.SentMailboxID)
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
	orgID, ok := s.orgPerm(w, r, p, permAliasesWrite, permListsWrite)
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
		writeError(w, http.StatusUnprocessableEntity, "invalid_local_part", "Use letters, digits, dots, hyphens, or underscores, or leave it empty for a catch-all. A plus sign marks a tag, so it cannot be part of an address.")
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
	if !p.canWriteAlias(orgID, in.DomainID, a.Kind) {
		forbidden(w)
		return
	}
	a.Address = in.LocalPart + "@" + domainName
	ie, err := s.createAliasRecord(r.Context(), a)
	if ie != nil {
		writeError(w, ie.Status, ie.Code, ie.Message)
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
	if !p.canReadAlias(a.OrgID, a.DomainID, a.Kind) && !slices.Contains(a.Owners, p.User.ID) && !a.personalOwner(p) {
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
	if a.PersonalOwnerID != nil {
		if !a.personalOwner(p) && !p.canAdminOrg(a.OrgID) {
			forbidden(w)
			return
		}
		s.updatePersonalAlias(w, r, p, a)
		return
	}
	var in aliasInput
	if !decode(w, r, 512<<10, &in) {
		return
	}
	canWrite := p.canWriteAlias(a.OrgID, a.DomainID, a.Kind)
	if !canWrite {
		membershipOnly := in.Kind == nil && in.Name == nil && in.Active == nil && in.AcceptFrom == nil && in.AllowedSenders == nil && in.SendAs == nil && in.Owners == nil && in.Groups == nil && in.MemberDomains == nil &&
			in.Moderated == nil && in.Moderators == nil && in.ModerationExempt == nil && in.Hidden == nil && in.SentMailboxID == nil
		if !slices.Contains(a.Owners, p.User.ID) || !membershipOnly {
			forbidden(w)
			return
		}
	} else if in.Kind != nil && *in.Kind != a.Kind && !p.canWriteAlias(a.OrgID, a.DomainID, *in.Kind) {
		forbidden(w)
		return
	}
	before := *a
	if !s.applyAliasInput(w, r, a, &in) {
		return
	}
	if err := s.saveAlias(r.Context(), a, false, before.Kind != a.Kind); err != nil {
		if writeLimitError(w, err) {
			return
		}
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &a.OrgID, "alias.update", "alias", a.ID, map[string]any{"address": a.Address, "before": aliasAuditFields(&before), "after": aliasAuditFields(a)})
	writeJSON(w, http.StatusOK, a)
}

func aliasAuditFields(a *alias) map[string]any {
	return map[string]any{"kind": a.Kind, "name": a.Name, "active": a.Active, "destinations": a.Destinations, "acceptFrom": a.AcceptFrom, "allowedSenders": a.AllowedSenders, "sendAs": a.SendAs, "owners": a.Owners, "groups": a.Groups, "memberDomains": a.MemberDomains,
		"moderated": a.Moderated, "moderators": a.Moderators, "moderationExempt": a.ModerationExempt, "hiddenFromDirectory": a.Hidden, "sentMailboxId": a.SentMailboxID}
}

func (s *Server) deleteAlias(w http.ResponseWriter, r *http.Request, p *principal) {
	a, ok := s.aliasFromPath(w, r, p)
	if !ok {
		return
	}
	if !p.canWriteAlias(a.OrgID, a.DomainID, a.Kind) && !a.personalOwner(p) {
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
