package api

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

const (
	maxBrandImageBytes = 256 << 10
	maxBrandImageSide  = 2048
	maxCustomCSSBytes  = 32 << 10
)

var (
	brandImageKinds = []string{"logo-light", "logo-dark", "favicon"}
	brandImageTypes = []string{"image/png", "image/jpeg", "image/webp"}
	brandThemes     = []string{"dark", "light"}
	brandColorKeys  = []string{"accent", "accentHover", "accentActive", "background", "surface", "border", "text", "link"}
	hexColorPattern = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	cssEscape       = regexp.MustCompile(`\\(?:[0-9a-fA-F]{1,6}[ \t\r\n\f]?|(?s:.))`)
	cssURL          = regexp.MustCompile(`url\(\s*(?:"([^"]*)"|'([^']*)'|([^)]*))`)
)

type brandingSettings struct {
	Name          string                       `json:"name"`
	SignInMessage string                       `json:"signInMessage"`
	Colors        map[string]map[string]string `json:"colors"`
	CustomCSS     string                       `json:"customCss"`
}

type brandingImages struct {
	LogoLight *string `json:"logoLight"`
	LogoDark  *string `json:"logoDark"`
	Favicon   *string `json:"favicon"`
}

type storedBranding struct {
	orgName  string
	settings brandingSettings
	images   brandingImages
	cssURL   *string
}

func (b *storedBranding) empty() bool {
	return b.settings.Name == "" && b.settings.SignInMessage == "" && b.settings.CustomCSS == "" &&
		len(b.settings.Colors["dark"])+len(b.settings.Colors["light"]) == 0 && b.images == brandingImages{}
}

func brandAssetURL(orgID, asset string, at time.Time) *string {
	u := "/api/v1/branding/" + orgID + "/" + asset + "?v=" + strconv.FormatInt(at.UnixMilli(), 10)
	return &u
}

func (s *Server) loadBranding(ctx context.Context, orgID string) (*storedBranding, error) {
	b := &storedBranding{}
	var updated time.Time
	err := s.db.QueryRow(ctx, `
		SELECT o.name, coalesce(b.name, ''), coalesce(b.sign_in_message, ''), coalesce(b.colors, '{}'), coalesce(b.custom_css, ''), coalesce(b.updated_at, o.created_at)
		FROM organizations o LEFT JOIN organization_branding b ON b.org_id = o.id WHERE o.id = $1`, orgID).
		Scan(&b.orgName, &b.settings.Name, &b.settings.SignInMessage, &b.settings.Colors, &b.settings.CustomCSS, &updated)
	if err != nil {
		return nil, err
	}
	for _, theme := range brandThemes {
		if b.settings.Colors[theme] == nil {
			b.settings.Colors[theme] = map[string]string{}
		}
	}
	if s.cfg.CustomCSS && b.settings.CustomCSS != "" {
		b.cssURL = brandAssetURL(orgID, "custom.css", updated)
	}
	rows, err := s.db.Query(ctx, `SELECT kind, updated_at FROM organization_brand_images WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var at time.Time
		if err := rows.Scan(&kind, &at); err != nil {
			return nil, err
		}
		switch kind {
		case "logo-light":
			b.images.LogoLight = brandAssetURL(orgID, kind, at)
		case "logo-dark":
			b.images.LogoDark = brandAssetURL(orgID, kind, at)
		case "favicon":
			b.images.Favicon = brandAssetURL(orgID, kind, at)
		}
	}
	return b, rows.Err()
}

func (s *Server) writeBranding(w http.ResponseWriter, r *http.Request, orgID string) {
	b, err := s.loadBranding(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		brandingSettings
		brandingImages
		CustomCSSEnabled bool `json:"customCssEnabled"`
	}{b.settings, b.images, s.cfg.CustomCSS})
}

func (s *Server) getBranding(w http.ResponseWriter, r *http.Request, p *principal) {
	if orgID, ok := s.orgFromPath(w, r, p, true); ok {
		s.writeBranding(w, r, orgID)
	}
}

func (s *Server) saveBranding(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in brandingSettings
	if !decode(w, r, 128<<10, &in) {
		return
	}
	in.Name, in.SignInMessage = strings.TrimSpace(in.Name), strings.TrimSpace(in.SignInMessage)
	if msg := normalizeBranding(&in); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_branding", msg)
		return
	}
	before, err := s.loadBranding(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO organization_branding (org_id, name, sign_in_message, colors, custom_css) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id) DO UPDATE SET name = excluded.name, sign_in_message = excluded.sign_in_message, colors = excluded.colors, custom_css = excluded.custom_css, updated_at = now()`,
		orgID, in.Name, in.SignInMessage, in.Colors, in.CustomCSS)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var changed []string
	for field, same := range map[string]bool{
		"name":          before.settings.Name == in.Name,
		"signInMessage": before.settings.SignInMessage == in.SignInMessage,
		"colors":        maps.EqualFunc(before.settings.Colors, in.Colors, maps.Equal),
		"customCss":     before.settings.CustomCSS == in.CustomCSS,
	} {
		if !same {
			changed = append(changed, field)
		}
	}
	if len(changed) > 0 {
		slices.Sort(changed)
		s.audit(r.Context(), r, p, &orgID, "branding.update", "organization", orgID, map[string]any{"changed": changed})
	}
	s.writeBranding(w, r, orgID)
}

func normalizeBranding(b *brandingSettings) string {
	switch {
	case utf8.RuneCountInString(b.Name) > 100:
		return "Keep the name to 100 characters."
	case utf8.RuneCountInString(b.SignInMessage) > 500:
		return "Keep the sign-in message to 500 characters."
	case len(b.CustomCSS) > maxCustomCSSBytes:
		return "Keep custom CSS under 32 KB."
	}
	colors := map[string]map[string]string{}
	for _, theme := range brandThemes {
		colors[theme] = map[string]string{}
	}
	for theme, values := range b.Colors {
		if colors[theme] == nil {
			return "Colours can only be set for the dark and light themes."
		}
		for key, value := range values {
			value = strings.ToLower(value)
			if !slices.Contains(brandColorKeys, key) || !hexColorPattern.MatchString(value) {
				return "Give each colour as a hex value such as #fa7e26."
			}
			colors[theme][key] = value
		}
	}
	b.Colors = colors
	return customCSSProblem(b.CustomCSS)
}

func customCSSProblem(css string) string {
	plain := strings.ToLower(cssEscape.ReplaceAllStringFunc(css, func(esc string) string {
		if n, err := strconv.ParseUint(strings.TrimRight(esc[1:], " \t\r\n\f"), 16, 32); err == nil {
			if r := rune(n); utf8.ValidRune(r) && r != 0 {
				return string(r)
			}
			return "�"
		}
		return esc[1:]
	}))
	if strings.Contains(plain, "@import") {
		return "Custom CSS can't use @import. Paste the rules in directly."
	}
	if strings.Contains(plain, "image-set(") || strings.Contains(plain, "image(") || strings.Contains(plain, "src(") {
		return "Custom CSS can't use image(), image-set() or src(). Use url() with a path on this server."
	}
	for _, m := range cssURL.FindAllStringSubmatch(plain, -1) {
		target := strings.Map(func(r rune) rune {
			switch r {
			case ' ', '\t', '\n', '\r', '\f':
				return -1
			case '\\':
				return '/'
			}
			return r
		}, m[1]+m[2]+m[3])
		if u, err := url.Parse(target); err != nil || u.Host != "" || (u.Scheme != "" && u.Scheme != "data") {
			return "Custom CSS can only load files from this server, such as url(/api/v1/branding/…), or data: URLs."
		}
	}
	return ""
}

func (s *Server) putBrandImage(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	kind := r.PathValue("image")
	if !slices.Contains(brandImageKinds, kind) {
		notFound(w)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBrandImageBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Choose an image of 256 KB or less.")
		return
	}
	contentType := http.DetectContentType(data)
	size, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !slices.Contains(brandImageTypes, contentType) || size.Width > maxBrandImageSide || size.Height > maxBrandImageSide {
		writeError(w, http.StatusUnprocessableEntity, "invalid_image", "Upload a PNG, JPEG or WebP image of at most 2048 by 2048 pixels. SVG and other formats are not accepted.")
		return
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO organization_brand_images (org_id, kind, content_type, data) VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, kind) DO UPDATE SET content_type = excluded.content_type, data = excluded.data, updated_at = now()`, orgID, kind, contentType, data)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "branding.image.update", "organization", orgID, map[string]any{"image": kind, "contentType": contentType, "bytes": len(data)})
	s.writeBranding(w, r, orgID)
}

func (s *Server) deleteBrandImage(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	kind := r.PathValue("image")
	tag, err := s.db.Exec(r.Context(), `DELETE FROM organization_brand_images WHERE org_id = $1 AND kind = $2`, orgID, kind)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "branding.image.remove", "organization", orgID, map[string]any{"image": kind})
	s.writeBranding(w, r, orgID)
}

func (s *Server) brandAsset(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathID(w, r, "orgId")
	if !ok {
		return
	}
	asset := r.PathValue("asset")
	var contentType string
	var data []byte
	err := pgx.ErrNoRows
	switch {
	case asset == "custom.css" && s.cfg.CustomCSS:
		var css string
		err = s.db.QueryRow(r.Context(), `SELECT custom_css FROM organization_branding WHERE org_id = $1 AND custom_css <> ''`, orgID).Scan(&css)
		contentType, data = "text/css; charset=utf-8", []byte(css)
	case slices.Contains(brandImageKinds, asset):
		err = s.db.QueryRow(r.Context(), `SELECT content_type, data FROM organization_brand_images WHERE org_id = $1 AND kind = $2`, orgID, asset).Scan(&contentType, &data)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(data)
}

func (s *Server) currentBranding(w http.ResponseWriter, r *http.Request) {
	orgID, err := s.brandingOrg(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var b *storedBranding
	if orgID != "" {
		if b, err = s.loadBranding(r.Context(), orgID); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	if b == nil || b.empty() {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OrgID         string                       `json:"orgId"`
		Name          string                       `json:"name"`
		SignInMessage string                       `json:"signInMessage"`
		Colors        map[string]map[string]string `json:"colors"`
		CustomCSSURL  *string                      `json:"customCssUrl"`
		brandingImages
	}{orgID, firstNonEmpty(b.settings.Name, b.orgName), b.settings.SignInMessage, b.settings.Colors, b.cssURL, b.images})
}

func (s *Server) brandingOrg(r *http.Request) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(r.URL.Query().Get("host")), ".")
	var hostOrg string
	err := s.db.QueryRow(r.Context(), `
		SELECT org_id FROM domains
		WHERE active AND verified_at IS NOT NULL AND ($1 = name OR right($1, length(name) + 1) = '.' || name)
		ORDER BY length(name) DESC LIMIT 1`, host).Scan(&hostOrg)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return hostOrg, nil
	}
	var memberOrg string
	err = s.db.QueryRow(r.Context(), `
		SELECT m.org_id FROM sessions s
		JOIN users u ON u.id = s.user_id AND u.status = 'active'
		JOIN organization_members m ON m.user_id = u.id
		WHERE s.id_hash = $1 AND s.expires_at > now()
		ORDER BY m.org_id::text = $2 DESC, EXISTS (SELECT 1 FROM mailboxes b WHERE b.org_id = m.org_id AND b.address = u.email) DESC, m.created_at
		LIMIT 1`, secrets.SHA256(c.Value), hostOrg).Scan(&memberOrg)
	if errors.Is(err, pgx.ErrNoRows) {
		return hostOrg, nil
	}
	return memberOrg, err
}
