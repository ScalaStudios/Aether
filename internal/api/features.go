package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	featureCalendar           = "calendar"
	featureDAV                = "dav"
	featureMailImport         = "mailImport"
	featureAppPasswords       = "appPasswords"
	featureExternalForwarding = "externalForwarding"
	featurePersonalAliases    = "personalAliases"
	featureWebhooks           = "webhooks"
	featureAPIKeys            = "apiKeys"
)

var organizationFeatures = []string{featureCalendar, featureDAV, featureMailImport, featureAppPasswords, featureExternalForwarding, featurePersonalAliases, featureWebhooks, featureAPIKeys}

var platformAdminFeatures = []string{featureExternalForwarding, featureMailImport, featureWebhooks}

const userOrganizations = `(SELECT org_id FROM organization_members WHERE user_id = $1 UNION SELECT org_id FROM mailboxes WHERE owner_user_id = $1)`

type featureSettings struct {
	Features          map[string]bool `json:"features"`
	PlatformAdminOnly []string        `json:"platformAdminOnly"`
}

func (s *Server) featureOn(ctx context.Context, orgID, feature string) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM organization_disabled_features WHERE org_id = $1 AND feature = $2)`, orgID, feature).Scan(&on)
	return on, err
}

func (s *Server) userFeaturesOn(ctx context.Context, userID string, features ...string) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM organization_disabled_features WHERE feature = ANY ($2) AND org_id IN `+userOrganizations+`)`, userID, features).Scan(&on)
	return on, err
}

func featureOff(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "feature_disabled", "This feature is turned off for your organization.")
}

func (s *Server) requireFeature(w http.ResponseWriter, r *http.Request, orgID, feature string) bool {
	on, err := s.featureOn(r.Context(), orgID, feature)
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	if !on {
		featureOff(w)
	}
	return on
}

func (s *Server) calendarFeature(h func(http.ResponseWriter, *http.Request, *principal)) func(http.ResponseWriter, *http.Request, *principal) {
	return func(w http.ResponseWriter, r *http.Request, p *principal) {
		on, err := s.userFeaturesOn(r.Context(), p.User.ID, featureCalendar)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !on {
			featureOff(w)
			return
		}
		h(w, r, p)
	}
}

func (s *Server) loadFeatures(ctx context.Context, orgID string) (featureSettings, error) {
	out := featureSettings{Features: map[string]bool{}, PlatformAdminOnly: platformAdminFeatures}
	for _, f := range organizationFeatures {
		out.Features[f] = true
	}
	rows, err := s.db.Query(ctx, `SELECT feature FROM organization_disabled_features WHERE org_id = $1`, orgID)
	if err != nil {
		return out, err
	}
	disabled, err := pgx.CollectRows(rows, pgx.RowTo[string])
	for _, f := range disabled {
		out.Features[f] = false
	}
	return out, err
}

func (s *Server) getFeatures(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, false)
	if !ok {
		return
	}
	out, err := s.loadFeatures(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveFeatures(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, false)
	if !ok {
		return
	}
	if p.orgRole(orgID) != "owner" {
		forbidden(w)
		return
	}
	var in map[string]bool
	if !decode(w, r, 4<<10, &in) {
		return
	}
	before, err := s.loadFeatures(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var changed []string
	for f, on := range in {
		if !slices.Contains(organizationFeatures, f) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_feature", f+" is not a feature that can be turned on or off.")
			return
		}
		if before.Features[f] == on {
			continue
		}
		if slices.Contains(platformAdminFeatures, f) && !p.User.IsPlatformAdmin {
			writeError(w, http.StatusForbidden, "platform_admin_only", "Only a platform administrator can turn "+f+" on or off.")
			return
		}
		changed = append(changed, f)
	}
	slices.Sort(changed)
	var kicked []string
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		for _, f := range changed {
			if in[f] {
				if _, err := tx.Exec(r.Context(), `DELETE FROM organization_disabled_features WHERE org_id = $1 AND feature = $2`, orgID, f); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.Exec(r.Context(), `INSERT INTO organization_disabled_features (org_id, feature) VALUES ($1, $2)`, orgID, f); err != nil {
				return err
			}
			if f == featureAppPasswords {
				rows, err := tx.Query(r.Context(), `SELECT DISTINCT m.address FROM app_passwords a JOIN mailboxes m ON m.id = a.mailbox_id WHERE m.org_id = $1 AND a.revoked_at IS NULL`, orgID)
				if err != nil {
					return err
				}
				if kicked, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
					return err
				}
				if err := queueKicks(r.Context(), tx, kicked); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, f := range changed {
		action := "feature.disable"
		if in[f] {
			action = "feature.enable"
		}
		s.audit(r.Context(), r, p, &orgID, action, "feature", f, map[string]any{"feature": f})
	}
	if len(kicked) > 0 {
		wake(s.kickWake)
	}
	if on, ok := in[featureMailImport]; ok && !on {
		if err := s.cancelOrgImports(r.Context(), orgID); err != nil {
			s.log.Warn("cancelling running mail imports failed", "org", orgID, "err", err)
		}
	}
	if slices.Contains(changed, featureExternalForwarding) {
		policy, err := s.forwardPolicy(r.Context(), orgID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		failed, err := s.reinstallForwarders(r.Context(), orgID, policy)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if len(failed) > 0 {
			writeError(w, http.StatusBadGateway, "rules_not_updated", "The switch was saved, but the mail rules of "+strings.Join(failed, ", ")+" could not be updated on the mail server. Save again to retry.")
			return
		}
	}
	out, err := s.loadFeatures(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
