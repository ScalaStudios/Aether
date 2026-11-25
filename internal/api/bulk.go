package api

import (
	"net/http"
	"strings"
)

const (
	maxBulkOperations = 1000
	maxBulkBodyBytes  = 4 << 20
)

type bulkRequest struct {
	DryRun     bool            `json:"dryRun"`
	Operations []bulkOperation `json:"operations"`
}

type bulkOperation struct {
	Type           string   `json:"type"`
	Address        string   `json:"address"`
	DomainID       string   `json:"domainId"`
	LocalPart      string   `json:"localPart"`
	DisplayName    string   `json:"displayName"`
	Kind           string   `json:"kind"`
	QuotaBytes     *int64   `json:"quotaBytes"`
	Password       string   `json:"password"`
	Active         *bool    `json:"active"`
	Name           string   `json:"name"`
	Destinations   []string `json:"destinations"`
	AcceptFrom     string   `json:"acceptFrom"`
	AllowedSenders []string `json:"allowedSenders"`
	SendAs         []string `json:"sendAs"`
	Owners         []string `json:"owners"`
}

type bulkError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type bulkResult struct {
	Index   int        `json:"index"`
	Type    string     `json:"type"`
	Address string     `json:"address,omitempty"`
	Status  string     `json:"status"`
	Mailbox *mailbox   `json:"mailbox,omitempty"`
	Alias   *alias     `json:"alias,omitempty"`
	Error   *bulkError `json:"error,omitempty"`
}

type bulkSummary struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

type bulkResponse struct {
	DryRun  bool         `json:"dryRun"`
	Summary bulkSummary  `json:"summary"`
	Results []bulkResult `json:"results"`
}

func checkBulkAPIKeyScopes(key *apiKeyIdentity, ops []bulkOperation) *fieldError {
	if key == nil {
		return nil
	}
	var needsMailboxes, needsAliases bool
	for _, op := range ops {
		switch strings.ToLower(strings.TrimSpace(op.Type)) {
		case "mailbox":
			needsMailboxes = true
		case "alias", "list":
			needsAliases = true
		}
	}
	if needsMailboxes && !key.permits(http.MethodPost, "mailboxes") {
		return &fieldError{http.StatusForbidden, "insufficient_scope", "This API key does not have the mailboxes scope needed for this request."}
	}
	if needsAliases && !key.permits(http.MethodPost, "aliases") {
		return &fieldError{http.StatusForbidden, "insufficient_scope", "This API key does not have the aliases scope needed for this request."}
	}
	if !needsMailboxes && !needsAliases && !key.permits(http.MethodPost, "mailboxes") && !key.permits(http.MethodPost, "aliases") {
		return &fieldError{http.StatusForbidden, "insufficient_scope", "This API key does not have the mailboxes or aliases scope needed for this request."}
	}
	return nil
}

func resolveBulkAddress(domainsByID, domainsByName map[string]string, address, domainID, localPart string) (string, string, string, string, *fieldError) {
	address = strings.ToLower(strings.TrimSpace(address))
	domainID = strings.ToLower(strings.TrimSpace(domainID))
	localPart = strings.ToLower(strings.TrimSpace(localPart))
	if address != "" {
		local, dom, ok := strings.Cut(address, "@")
		if !ok || dom == "" || strings.Contains(dom, "@") {
			return "", "", "", address, &fieldError{http.StatusUnprocessableEntity, "invalid_address", address + " is not a valid email address."}
		}
		id, ok := domainsByName[dom]
		if !ok {
			return "", "", "", address, &fieldError{http.StatusUnprocessableEntity, "invalid_domain", dom + " does not belong to this organization."}
		}
		if domainID != "" && domainID != id {
			return "", "", "", address, &fieldError{http.StatusUnprocessableEntity, "invalid_domain", "The address domain does not match domainId."}
		}
		if localPart != "" && localPart != local {
			return "", "", "", address, &fieldError{http.StatusUnprocessableEntity, "invalid_local_part", "The address local part does not match localPart."}
		}
		return id, dom, local, local + "@" + dom, nil
	}
	if domainID != "" {
		dom, ok := domainsByID[domainID]
		if !ok {
			return "", "", "", "", &fieldError{http.StatusUnprocessableEntity, "invalid_domain", "Choose a domain that belongs to this organization."}
		}
		return domainID, dom, localPart, localPart + "@" + dom, nil
	}
	return "", "", "", "", &fieldError{http.StatusUnprocessableEntity, "invalid_address", "Enter an email address or a domainId and localPart."}
}

func (s *Server) bulkImport(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permMailboxesWrite, permAliasesWrite, permListsWrite)
	if !ok {
		return
	}
	var in bulkRequest
	if !decode(w, r, maxBulkBodyBytes, &in) {
		return
	}
	if r.URL.Query().Get("dryRun") == "true" {
		in.DryRun = true
	}
	if len(in.Operations) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_operations", "Include at least one operation.")
		return
	}
	if len(in.Operations) > maxBulkOperations {
		writeError(w, http.StatusUnprocessableEntity, "too_many_operations", "A bulk request can include at most 1,000 operations.")
		return
	}
	if ie := checkBulkAPIKeyScopes(p.apiKey, in.Operations); ie != nil {
		writeError(w, ie.Status, ie.Code, ie.Message)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, name FROM domains WHERE org_id = $1`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	domainsByID := map[string]string{}
	domainsByName := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			s.internalError(w, r, err)
			return
		}
		domainsByID[id] = name
		domainsByName[name] = id
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		s.internalError(w, r, err)
		return
	}
	rows.Close()

	plannedAddresses := map[string]string{}
	plannedMailboxes := map[string]bool{}
	plannedMembers := map[string]bool{}
	results := make([]bulkResult, 0, len(in.Operations))
	var succeeded, failed int

	failItem := func(index int, opType, address, code, message string) {
		failed++
		results = append(results, bulkResult{
			Index:   index,
			Type:    opType,
			Address: address,
			Status:  "failed",
			Error:   &bulkError{Code: code, Message: message},
		})
	}

	for i, op := range in.Operations {
		if r.Context().Err() != nil {
			return
		}
		opType := strings.ToLower(strings.TrimSpace(op.Type))
		switch opType {
		case "mailbox":
			domainID, domainName, localPart, address, ie := resolveBulkAddress(domainsByID, domainsByName, op.Address, op.DomainID, op.LocalPart)
			if ie != nil {
				failItem(i, opType, address, ie.Code, ie.Message)
				continue
			}
			if !p.canOnDomain(orgID, domainID, permMailboxesWrite) {
				failItem(i, opType, address, "forbidden", "You do not have permission to create mailboxes on this domain.")
				continue
			}
			active := true
			if op.Active != nil {
				active = *op.Active
			}
			req := mailboxCreateInput{
				DomainID:    domainID,
				LocalPart:   localPart,
				DisplayName: op.DisplayName,
				QuotaBytes:  op.QuotaBytes,
				Password:    op.Password,
				Kind:        op.Kind,
				Active:      active,
			}
			if ie := normalizeAndValidateMailbox(&req); ie != nil {
				failItem(i, opType, address, ie.Code, ie.Message)
				continue
			}
			if in.DryRun {
				if _, dup := plannedAddresses[address]; dup {
					failItem(i, opType, address, "address_taken", address+" appears more than once in this request.")
					continue
				}
				ie, err := s.checkMailboxDryRun(r.Context(), p, address, req.Kind, req.Password)
				if err != nil {
					s.log.Error("bulk mailbox dry-run failed", "index", i, "address", address, "err", err)
					failItem(i, opType, address, "internal", "Something went wrong on the server.")
					continue
				}
				if ie != nil {
					failItem(i, opType, address, ie.Code, ie.Message)
					continue
				}
				plannedAddresses[address] = "mailbox"
				plannedMailboxes[address] = true
				if req.Kind == "user" {
					plannedMembers[address] = true
				}
				succeeded++
				results = append(results, bulkResult{Index: i, Type: opType, Address: address, Status: "valid"})
				continue
			}
			m, ie, err := s.createMailboxRecord(r.Context(), p, orgID, domainName, req)
			if err != nil {
				s.log.Error("bulk mailbox create failed", "index", i, "address", address, "err", err)
				failItem(i, opType, address, "internal", "Something went wrong on the server.")
				continue
			}
			if ie != nil {
				failItem(i, opType, address, ie.Code, ie.Message)
				continue
			}
			s.audit(r.Context(), r, p, &orgID, "mailbox.create", "mailbox", m.ID, map[string]any{"address": m.Address, "kind": m.Kind})
			succeeded++
			results = append(results, bulkResult{Index: i, Type: opType, Address: m.Address, Status: "created", Mailbox: m})

		case "alias", "list":
			domainID, _, localPart, address, ie := resolveBulkAddress(domainsByID, domainsByName, op.Address, op.DomainID, op.LocalPart)
			if ie != nil {
				failItem(i, opType, address, ie.Code, ie.Message)
				continue
			}
			if op.Kind != "" && strings.ToLower(strings.TrimSpace(op.Kind)) != opType {
				failItem(i, opType, address, "invalid_kind", "kind must match type.")
				continue
			}
			if !p.canWriteAlias(orgID, domainID, opType) {
				failItem(i, opType, address, "forbidden", "You do not have permission to create this "+opType+" on this domain.")
				continue
			}
			if localPart != "" && !localPartPattern.MatchString(localPart) {
				failItem(i, opType, address, "invalid_local_part", "Use letters, digits, dots, hyphens, or underscores, or leave it empty for a catch-all. A plus sign marks a tag, so it cannot be part of an address.")
				continue
			}
			a := &alias{
				OrgID:            orgID,
				DomainID:         domainID,
				LocalPart:        localPart,
				Address:          address,
				Active:           true,
				Kind:             opType,
				AcceptFrom:       "anyone",
				Destinations:     []string{},
				AllowedSenders:   []string{},
				SendAs:           []string{},
				Owners:           []string{},
				Groups:           []string{},
				MemberDomains:    []string{},
				Moderators:       []string{},
				ModerationExempt: []string{},
			}
			ai := aliasInput{
				Kind:         &opType,
				Destinations: &op.Destinations,
				Active:       op.Active,
			}
			if op.Name != "" {
				name := op.Name
				ai.Name = &name
			}
			if acceptFrom := strings.ToLower(strings.TrimSpace(op.AcceptFrom)); acceptFrom != "" {
				ai.AcceptFrom = &acceptFrom
			}
			if len(op.AllowedSenders) > 0 {
				allowed := op.AllowedSenders
				ai.AllowedSenders = &allowed
			}
			if len(op.SendAs) > 0 {
				sendAs := op.SendAs
				ai.SendAs = &sendAs
			}
			if len(op.Owners) > 0 {
				owners := op.Owners
				ai.Owners = &owners
			}
			var extraMailboxes, extraMembers map[string]bool
			if in.DryRun {
				extraMailboxes = plannedMailboxes
				extraMembers = plannedMembers
			}
			ie, err := s.applyAliasChanges(r.Context(), a, &ai, extraMailboxes, extraMembers)
			if err != nil {
				s.log.Error("bulk alias validation failed", "index", i, "address", address, "err", err)
				failItem(i, opType, address, "internal", "Something went wrong on the server.")
				continue
			}
			if ie != nil {
				failItem(i, opType, address, ie.Code, ie.Message)
				continue
			}
			if in.DryRun {
				if _, dup := plannedAddresses[address]; dup {
					failItem(i, opType, address, "address_taken", address+" appears more than once in this request.")
					continue
				}
				ie, err := s.checkAliasDryRun(r.Context(), address)
				if err != nil {
					s.log.Error("bulk alias dry-run failed", "index", i, "address", address, "err", err)
					failItem(i, opType, address, "internal", "Something went wrong on the server.")
					continue
				}
				if ie != nil {
					failItem(i, opType, address, ie.Code, ie.Message)
					continue
				}
				plannedAddresses[address] = opType
				succeeded++
				results = append(results, bulkResult{Index: i, Type: opType, Address: address, Status: "valid"})
				continue
			}
			ie, err = s.createAliasRecord(r.Context(), a)
			if err != nil {
				s.log.Error("bulk alias create failed", "index", i, "address", address, "err", err)
				failItem(i, opType, address, "internal", "Something went wrong on the server.")
				continue
			}
			if ie != nil {
				failItem(i, opType, address, ie.Code, ie.Message)
				continue
			}
			s.audit(r.Context(), r, p, &orgID, "alias.create", "alias", a.ID, map[string]any{"address": a.Address, "kind": a.Kind, "destinations": a.Destinations})
			succeeded++
			results = append(results, bulkResult{Index: i, Type: opType, Address: a.Address, Status: "created", Alias: a})

		default:
			failItem(i, opType, strings.ToLower(strings.TrimSpace(op.Address)), "invalid_type", "Type must be mailbox, alias, or list.")
		}
	}

	writeJSON(w, http.StatusOK, bulkResponse{
		DryRun: in.DryRun,
		Summary: bulkSummary{
			Total:     len(results),
			Succeeded: succeeded,
			Failed:    failed,
		},
		Results: results,
	})
}
