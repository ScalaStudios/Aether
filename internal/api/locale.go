package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/scalastudios/aether/internal/i18n"
)

func (s *Server) orgDefaultLocale(ctx context.Context, orgID string) string {
	if orgID == "" {
		return ""
	}
	var loc string
	if err := s.db.QueryRow(ctx, `SELECT default_locale FROM organizations WHERE id = $1`, orgID).Scan(&loc); err != nil {
		return ""
	}
	return loc
}

func (s *Server) mailboxLocale(ctx context.Context, mailboxID, orgID string) string {
	var userLoc, orgLoc string
	err := s.db.QueryRow(ctx, `
		SELECT coalesce(u.locale, ''), o.default_locale
		FROM mailboxes m
		JOIN organizations o ON o.id = m.org_id
		LEFT JOIN users u ON u.id = m.owner_user_id
		WHERE m.id = $1`, mailboxID).Scan(&userLoc, &orgLoc)
	if err != nil {
		return i18n.Resolve(s.orgDefaultLocale(ctx, orgID))
	}
	return i18n.Resolve(userLoc, orgLoc)
}

func (s *Server) recipientLocale(ctx context.Context, address, fallbackOrgID string) string {
	addr := strings.ToLower(strings.TrimSpace(address))
	if addr != "" {
		var userLoc, orgLoc string
		err := s.db.QueryRow(ctx, `
			SELECT coalesce(u.locale, ''), o.default_locale
			FROM mailboxes m
			JOIN organizations o ON o.id = m.org_id
			LEFT JOIN users u ON u.id = m.owner_user_id
			WHERE lower(m.address) = $1
			LIMIT 1`, addr).Scan(&userLoc, &orgLoc)
		if err == nil {
			return i18n.Resolve(userLoc, orgLoc, s.orgDefaultLocale(ctx, fallbackOrgID))
		}
		err = s.db.QueryRow(ctx, `
			SELECT u.locale, coalesce((
				SELECT o.default_locale FROM organization_members om
				JOIN organizations o ON o.id = om.org_id
				WHERE om.user_id = u.id AND o.default_locale <> ''
				ORDER BY om.created_at LIMIT 1
			), '')
			FROM users u
			WHERE lower(u.email) = $1
			LIMIT 1`, addr).Scan(&userLoc, &orgLoc)
		if err == nil {
			return i18n.Resolve(userLoc, orgLoc, s.orgDefaultLocale(ctx, fallbackOrgID))
		}
	}
	return i18n.Resolve(s.orgDefaultLocale(ctx, fallbackOrgID))
}

func (s *Server) requestLocale(ctx context.Context, r *http.Request, p *principal, orgID string) string {
	var userLoc, orgLoc string
	if p != nil {
		userLoc = p.User.Locale
	}
	if orgID != "" {
		orgLoc = s.orgDefaultLocale(ctx, orgID)
	} else if p != nil {
		_ = s.db.QueryRow(ctx, `
			SELECT o.default_locale FROM organization_members om
			JOIN organizations o ON o.id = om.org_id
			WHERE om.user_id = $1 AND o.default_locale <> ''
			ORDER BY om.created_at LIMIT 1`, p.User.ID).Scan(&orgLoc)
	}
	return i18n.Resolve(userLoc, orgLoc, i18n.FromAcceptLanguage(r.Header.Get("Accept-Language")))
}
