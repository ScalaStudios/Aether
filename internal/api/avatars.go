package api

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

const maxAvatarSide = 512

func (s *Server) putAvatar(w http.ResponseWriter, r *http.Request, p *principal) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Choose a picture under 2 MB.")
		return
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width > maxAvatarSide || cfg.Height > maxAvatarSide {
		writeError(w, http.StatusUnprocessableEntity, "invalid_image", "Upload a JPEG or PNG picture of at most 512 by 512 pixels.")
		return
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_image", "Upload a JPEG or PNG picture of at most 512 by 512 pixels.")
		return
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 88}); err != nil {
		s.internalError(w, r, err)
		return
	}
	if _, err := s.db.Exec(r.Context(), `
		INSERT INTO user_avatars (user_id, image) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET image = excluded.image, updated_at = now()`, p.User.ID, out.Bytes()); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteAvatar(w http.ResponseWriter, r *http.Request, p *principal) {
	if _, err := s.db.Exec(r.Context(), `DELETE FROM user_avatars WHERE user_id = $1`, p.User.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getAvatar(w http.ResponseWriter, r *http.Request, p *principal) {
	var img []byte
	err := s.db.QueryRow(r.Context(), `
		SELECT a.image FROM user_avatars a
		WHERE a.user_id = $1 AND lower($2) = (SELECT lower(email) FROM users WHERE id = $1)
		UNION ALL
		SELECT a.image FROM mailboxes m JOIN user_avatars a ON a.user_id = m.owner_user_id
		WHERE m.active AND m.address = lower($2) AND m.org_id IN (SELECT org_id FROM organization_members WHERE user_id = $1)
		LIMIT 1`, p.User.ID, strings.TrimSpace(r.PathValue("address"))).Scan(&img)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(img)
}
