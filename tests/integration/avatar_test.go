//go:build integration

package integration

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"testing"
)

func TestProfilePictures(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	upload := func(c *apiClient, body []byte) int {
		req, _ := http.NewRequest("PUT", c.base+"/me/avatar", bytes.NewReader(body))
		req.Header.Set("Content-Type", "image/png")
		req.Header.Set("X-CSRF-Token", c.csrf)
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	fetch := func(c *apiClient, address string) (int, string) {
		resp, err := c.http.Get(c.base + "/avatars/" + address)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type")
	}
	picture := func(side int) []byte {
		img := image.NewRGBA(image.Rect(0, 0, side, side))
		img.Set(1, 1, color.RGBA{R: 200, A: 255})
		var buf bytes.Buffer
		png.Encode(&buf, img)
		return buf.Bytes()
	}

	if code, _ := fetch(w.bob, alice); code != 404 {
		t.Fatalf("no picture yet should be 404, got %d", code)
	}
	if code := upload(w.alice, []byte("<svg onload=alert(1)>")); code != 422 {
		t.Fatalf("non-image upload should be refused, got %d", code)
	}
	if code := upload(w.alice, picture(1024)); code != 422 {
		t.Fatalf("oversized picture should be refused, got %d", code)
	}
	if code := upload(w.alice, picture(64)); code != 204 {
		t.Fatalf("upload: %d", code)
	}
	if code, kind := fetch(w.bob, alice); code != 200 || kind != "image/jpeg" {
		t.Fatalf("colleague should see alice's picture as jpeg, got %d %q", code, kind)
	}
	outsider := setupWorld(t)
	if code, _ := fetch(outsider.bob, alice); code != 404 {
		t.Fatalf("someone in another organization should not see alice's picture, got %d", code)
	}
	w.alice.must("DELETE", "/me/avatar", nil, nil, 204)
	if code, _ := fetch(w.alice, alice); code != 404 {
		t.Fatalf("removed picture is still served: %d", code)
	}
}
