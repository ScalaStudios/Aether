package api

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestCustomCSSKeepsLoadsOnThisServer(t *testing.T) {
	allowed := []string{
		`body { color: #222 }`,
		`.md\:w-24 { width: 6rem } q::before { content: "\201C" }`,
		`header { background: url(/api/v1/branding/x/logo-light) }`,
		`header { background: url("logo.png") }`,
		`li { list-style-image: url(data:image/png;base64,AAAA) }`,
	}
	for _, css := range allowed {
		if msg := customCSSProblem(css); msg != "" {
			t.Errorf("%q was refused: %s", css, msg)
		}
	}
	refused := []string{
		`@import url(/other.css);`,
		`@IMPORT "x.css";`,
		`@\69mport "x.css";`,
		`a { background: url(https://evil.test/a.png) }`,
		`a { background: URL( "//evil.test/a" ) }`,
		`a { background: url("/\\evil.test/a") }`,
		"a { background: url(\"/\t/evil.test/a\") }",
		`a { background: u\72l(https://evil.test/a) }`,
		`a { background: u\000072l(https://evil.test/a) }`,
		`@font-face { font-family: x; src: url('https://evil.test/f.woff2') }`,
		`a { background: image-set("https://evil.test/a.png" 1x) }`,
		`a { background: image("https://evil.test/a.png") }`,
		`a { cursor: url(javascript:alert(1)), auto }`,
	}
	for _, css := range refused {
		if customCSSProblem(css) == "" {
			t.Errorf("%q was accepted", css)
		}
	}
}

func TestSignatureLogoIsScaledDownToWidth(t *testing.T) {
	var wide bytes.Buffer
	png.Encode(&wide, image.NewNRGBA(image.Rect(0, 0, 1000, 250)))
	out, kind := signatureLogo(wide.Bytes(), "image/png", 128)
	size, err := png.DecodeConfig(bytes.NewReader(out))
	if err != nil || kind != "image/png" || size.Width != 128 || size.Height != 32 {
		t.Fatalf("got %s %dx%d: %v", kind, size.Width, size.Height, err)
	}
	if kept, _ := signatureLogo(out, "image/png", 160); !bytes.Equal(kept, out) {
		t.Fatal("a logo narrower than the width should be kept as it is")
	}
}
