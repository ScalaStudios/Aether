package api

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	_ "golang.org/x/image/webp"
	"golang.org/x/oauth2"
)

const (
	avatarSide       = 256
	maxAvatarSource  = 2048
	maxAvatarBytes   = 4 << 20
	errInvalidAvatar = "Upload a JPEG, PNG, or WebP picture of at most 2048 by 2048 pixels."
)

func avatarJPEG(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width > maxAvatarSource || cfg.Height > maxAvatarSource || cfg.Width == 0 || cfg.Height == 0 {
		return nil, errors.New("picture too large")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err = jpeg.Encode(&out, squareThumbnail(img), &jpeg.Options{Quality: 88})
	return out.Bytes(), err
}

func squareThumbnail(img image.Image) image.Image {
	b := img.Bounds()
	side := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	size := min(side, avatarSide)
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			sx0, sx1 := x0+x*side/size, x0+max((x+1)*side/size, x*side/size+1)
			sy0, sy1 := y0+y*side/size, y0+max((y+1)*side/size, y*side/size+1)
			var r, g, bl, a, n uint32
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					r, g, bl, a, n = r+cr, g+cg, bl+cb, a+ca, n+1
				}
			}
			alpha := a / n
			white := 0xffff - alpha
			out.Set(x, y, color.RGBA64{R: uint16(r/n + white), G: uint16(g/n + white), B: uint16(bl/n + white), A: 0xffff})
		}
	}
	return out
}

func (s *Server) storeAvatar(ctx context.Context, userID string, img []byte, source string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO user_avatars (user_id, image, source) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET image = excluded.image, source = excluded.source, updated_at = now()
		WHERE excluded.source = 'upload' OR user_avatars.source = 'provider'`, userID, img, source)
	return err
}

func (s *Server) putAvatar(w http.ResponseWriter, r *http.Request, p *principal) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAvatarBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Choose a picture under 4 MB.")
		return
	}
	img, err := avatarJPEG(body)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_image", errInvalidAvatar)
		return
	}
	if err := s.storeAvatar(r.Context(), p.User.ID, img, "upload"); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncProviderAvatar(userID, issuer, picture string, token *oauth2.Token) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	url := picture
	if url == "" && strings.HasPrefix(issuer, "https://login.microsoftonline.com/") {
		url = "https://graph.microsoft.com/v1.0/me/photos/240x240/$value"
	}
	if !strings.HasPrefix(url, "https://") && !(s.cfg.WebhookAllowPrivate && strings.HasPrefix(url, "http://")) {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	if picture == "" || strings.HasPrefix(url, strings.TrimSuffix(issuer, "/")+"/") {
		token.SetAuthHeader(req)
	}
	resp, err := s.webhookClient.Do(req)
	if err != nil {
		s.log.Warn("fetching provider profile picture failed", "user", userID, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAvatarBytes))
	if err == nil {
		data, err = avatarJPEG(data)
	}
	if err == nil {
		err = s.storeAvatar(ctx, userID, data, "provider")
	}
	if err != nil {
		s.log.Warn("storing provider profile picture failed", "user", userID, "err", err)
	}
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
