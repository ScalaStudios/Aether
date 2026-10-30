package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/image/draw"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const defaultSignatureHTML = `<table style="border-collapse: collapse"><tbody><tr>
{{#logo}}<td style="vertical-align: top; padding: 0 16px 0 0">{{logo}}</td>{{/logo}}
<td style="vertical-align: top{{#logo}}; padding: 0 0 0 16px; border-left: 1px solid #d1d5db{{/logo}}">
<p><strong><span style="font-size: 16px">{{displayName}}</span></strong>{{#title}}<br><span style="color: #6b7280">{{title}}</span>{{/title}}<br><strong>{{organization}}</strong></p>
<p><a href="mailto:{{address}}">{{address}}</a>{{#phone}}<br>{{phone}}{{/phone}}<br><a href="https://{{domain}}">{{domain}}</a></p>
{{#links}}<p><span style="font-size: 12px; color: #6b7280">{{links}}</span></p>{{/links}}
</td></tr></tbody></table>`

var signaturePlaceholders = []string{"displayName", "title", "phone", "organization", "domain", "address", "logo", "links"}

var signatureSections = func() map[string]*regexp.Regexp {
	out := map[string]*regexp.Regexp{}
	for _, key := range signaturePlaceholders {
		out[key] = regexp.MustCompile(`(?s)\{\{#` + key + `\}\}(.*?)\{\{/` + key + `\}\}`)
	}
	return out
}()

type signatureLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type signatureTemplate struct {
	DomainID  *string         `json:"domainId"`
	HTML      string          `json:"html"`
	Links     []signatureLink `json:"links"`
	LogoWidth int             `json:"logoWidth"`
	UpdatedAt *time.Time      `json:"updatedAt,omitempty"`
}

func builtInSignature() signatureTemplate {
	return signatureTemplate{HTML: defaultSignatureHTML, Links: []signatureLink{}, LogoWidth: 64}
}

func (t *signatureTemplate) problem() string {
	t.HTML = strings.TrimSpace(t.HTML)
	switch {
	case t.HTML == "":
		return "Write the template HTML."
	case len(t.HTML) > 32<<10:
		return "Keep the template under 32 KB."
	case t.LogoWidth < 16 || t.LogoWidth > 240:
		return "Set a logo width between 16 and 240 pixels."
	case len(t.Links) > 10:
		return "Add up to 10 links."
	}
	for i := range t.Links {
		l := &t.Links[i]
		l.Label, l.URL = strings.TrimSpace(l.Label), strings.TrimSpace(l.URL)
		u, err := url.Parse(l.URL)
		web := err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
		if l.Label == "" || utf8.RuneCountInString(l.Label) > 40 || len(l.URL) > 500 || !(web || err == nil && u.Scheme == "mailto") {
			return "Give each link a label of up to 40 characters and an address that starts with https:// or mailto:."
		}
	}
	t.Links = nonNil(t.Links)
	return ""
}

func (s *Server) listSignatureTemplates(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT domain_id, html, links, logo_width, updated_at FROM signature_templates WHERE org_id = $1 ORDER BY domain_id NULLS FIRST`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	templates, err := pgx.CollectRows(rows, pgx.RowToStructByPos[signatureTemplate])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"builtIn": builtInSignature(), "templates": nonNil(templates)})
}

func (s *Server) saveSignatureTemplate(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in signatureTemplate
	if !decode(w, r, 64<<10, &in) {
		return
	}
	if msg := in.problem(); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_template", msg)
		return
	}
	if in.DomainID != nil {
		var exists bool
		if uuidPattern.MatchString(*in.DomainID) {
			if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM domains WHERE id = $1 AND org_id = $2)`, *in.DomainID, orgID).Scan(&exists); err != nil {
				s.internalError(w, r, err)
				return
			}
		}
		if !exists {
			writeError(w, http.StatusUnprocessableEntity, "invalid_template", "Choose one of this organization's domains.")
			return
		}
	}
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO signature_templates (org_id, domain_id, html, links, logo_width) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id, domain_id) DO UPDATE SET html = excluded.html, links = excluded.links, logo_width = excluded.logo_width, updated_at = now()
		RETURNING updated_at`, orgID, in.DomainID, in.HTML, in.Links, in.LogoWidth).Scan(&in.UpdatedAt)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "signature_template.update", "organization", orgID, map[string]any{"domainId": in.DomainID})
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) deleteSignatureTemplate(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var domainID *string
	if v := r.URL.Query().Get("domainId"); v != "" {
		if !uuidPattern.MatchString(v) {
			notFound(w)
			return
		}
		domainID = &v
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM signature_templates WHERE org_id = $1 AND domain_id IS NOT DISTINCT FROM $2`, orgID, domainID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "signature_template.remove", "organization", orgID, map[string]any{"domainId": domainID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) previewSignatureTemplate(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		signatureTemplate
		MailboxID string `json:"mailboxId"`
	}
	if !decode(w, r, 64<<10, &in) {
		return
	}
	if msg := in.problem(); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_template", msg)
		return
	}
	if !uuidPattern.MatchString(in.MailboxID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_mailbox", "Choose a mailbox in this organization to preview with.")
		return
	}
	out, err := s.renderSignature(r.Context(), in.MailboxID, orgID, &in.signatureTemplate)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_mailbox", "Choose a mailbox in this organization to preview with.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": out})
}

func (s *Server) mailboxSignatureTemplate(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
	if !ok {
		return
	}
	t := builtInSignature()
	err := s.db.QueryRow(r.Context(), `
		SELECT html, links, logo_width FROM signature_templates
		WHERE org_id = $1 AND (domain_id IS NULL OR domain_id = (SELECT domain_id FROM mailboxes WHERE id = $2))
		ORDER BY domain_id NULLS LAST LIMIT 1`, m.OrgID, m.ID).Scan(&t.HTML, &t.Links, &t.LogoWidth)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, r, err)
		return
	}
	out, err := s.renderSignature(r.Context(), m.ID, m.OrgID, &t)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": out})
}

func (s *Server) renderSignature(ctx context.Context, mailboxID, orgID string, t *signatureTemplate) (string, error) {
	var address, local, display, domain, org, owner, title, phone, logoType string
	var logo []byte
	err := s.db.QueryRow(ctx, `
		SELECT m.address, m.local_part, m.display_name, d.name, o.name, coalesce(u.display_name, ''), coalesce(u.title, ''), coalesce(u.phone, ''), coalesce(i.content_type, ''), i.data
		FROM mailboxes m
		JOIN domains d ON d.id = m.domain_id
		JOIN organizations o ON o.id = m.org_id
		LEFT JOIN users u ON u.id = m.owner_user_id
		LEFT JOIN organization_brand_images i ON i.org_id = m.org_id AND i.kind = 'logo-light'
		WHERE m.id = $1 AND m.org_id = $2`, mailboxID, orgID).Scan(&address, &local, &display, &domain, &org, &owner, &title, &phone, &logoType, &logo)
	if err != nil {
		return "", err
	}
	values := map[string]string{
		"displayName":  html.EscapeString(firstNonEmpty(display, owner, local)),
		"title":        html.EscapeString(title),
		"phone":        html.EscapeString(phone),
		"organization": html.EscapeString(org),
		"domain":       html.EscapeString(domain),
		"address":      html.EscapeString(address),
	}
	if len(logo) > 0 {
		logo, logoType = signatureLogo(logo, logoType, 2*t.LogoWidth)
		values["logo"] = fmt.Sprintf(`<img src="data:%s;base64,%s" width="%d" alt="%s">`, logoType, base64.StdEncoding.EncodeToString(logo), t.LogoWidth, values["organization"])
	}
	links := make([]string, len(t.Links))
	for i, l := range t.Links {
		links[i] = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(l.URL), html.EscapeString(l.Label))
	}
	values["links"] = strings.Join(links, " · ")
	out := t.HTML
	for _, key := range signaturePlaceholders {
		keep := "$1"
		if values[key] == "" {
			keep = ""
		}
		out = strings.ReplaceAll(signatureSections[key].ReplaceAllString(out, keep), "{{"+key+"}}", values[key])
	}
	return mailpkg.SanitizeHTML(out), nil
}

func signatureLogo(data []byte, contentType string, width int) ([]byte, string) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil || img.Bounds().Dx() <= width {
		return data, contentType
	}
	b := img.Bounds()
	scaled := image.NewNRGBA(image.Rect(0, 0, width, max(1, b.Dy()*width/b.Dx())))
	draw.BiLinear.Scale(scaled, scaled.Bounds(), img, b, draw.Src, nil)
	var out bytes.Buffer
	if contentType == "image/jpeg" {
		jpeg.Encode(&out, scaled, &jpeg.Options{Quality: 88})
		return out.Bytes(), contentType
	}
	png.Encode(&out, scaled)
	return out.Bytes(), "image/png"
}
