package api

import (
	"strings"
	"testing"
	"time"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func TestTransferLimitsStayInsideThePlatform(t *testing.T) {
	platform := platformCeilings{MessageBytes: 50 << 20, LinkFileBytes: 2 << 30, StorageBytes: 100 << 30}
	defaults := defaultTransferLimits(platform)
	if defaults.MessageBytes != defaultMessageBytes || defaults.AttachmentBytes != defaultAttachmentBytes || defaults.LinkThresholdBytes != defaultLinkThresholdBytes {
		t.Fatalf("defaults changed: %+v", defaults)
	}
	if msg := validateTransferLimits(defaults, platform); msg != "" {
		t.Fatal(msg)
	}
	lowered := platformCeilings{MessageBytes: 8 << 20, LinkFileBytes: 4 << 20, StorageBytes: 1 << 20}
	got := defaults.effective(lowered)
	if got.MessageBytes != lowered.MessageBytes || got.AttachmentBytes != lowered.MessageBytes || got.LinkThresholdBytes != lowered.LinkFileBytes || got.LinkFileBytes != lowered.LinkFileBytes || got.StorageBytes != lowered.StorageBytes {
		t.Fatalf("limits were not clamped to the platform: %+v", got)
	}
	if validateTransferLimits(transferLimits{MessageBytes: platform.MessageBytes + 1, AttachmentBytes: 1, LinkThresholdBytes: 1, LinkFileBytes: 1, StorageBytes: 0}, platform) == "" {
		t.Fatal("message size above the platform limit was accepted")
	}
	if validateTransferLimits(transferLimits{MessageBytes: 10, AttachmentBytes: 11, LinkThresholdBytes: 1, LinkFileBytes: 11, StorageBytes: 0}, platform) == "" {
		t.Fatal("attachment size above the message size was accepted")
	}
}

func TestOversizedFilesBecomeDownloadLinks(t *testing.T) {
	limits := transferLimits{MessageBytes: 50 << 20, AttachmentBytes: 20 << 20, LinkThresholdBytes: 5 << 20, LinkFileBytes: 8 << 20, StorageBytes: 10 << 30}
	small := mailpkg.OutgoingAttachment{Filename: "note.txt", ContentType: "text/plain", Data: []byte("hello")}
	over := mailpkg.OutgoingAttachment{Filename: "clip.mp4", ContentType: "video/mp4", Data: make([]byte, 6<<20)}
	huge := mailpkg.OutgoingAttachment{Filename: "raw.bin", ContentType: "application/octet-stream", Data: make([]byte, 9<<20)}
	inline, linked, msg := partitionAttachments([]mailpkg.OutgoingAttachment{small, over}, limits)
	if msg != "" || len(inline) != 1 || len(linked) != 1 || linked[0].Filename != "clip.mp4" {
		t.Fatalf("partition = inline %d linked %d %q", len(inline), len(linked), msg)
	}
	if _, _, msg = partitionAttachments([]mailpkg.OutgoingAttachment{huge}, limits); msg == "" || !strings.Contains(msg, "raw.bin") {
		t.Fatalf("file over the link limit was accepted: %q", msg)
	}
	var many []mailpkg.OutgoingAttachment
	for i := 0; i < maxInlineFiles+1; i++ {
		many = append(many, mailpkg.OutgoingAttachment{Filename: "a.txt", Data: []byte("x")})
	}
	inline, linked, msg = partitionAttachments(many, limits)
	if msg != "" || len(inline) != maxInlineFiles || len(linked) != 1 {
		t.Fatalf("extra file was not linked: inline %d linked %d %q", len(inline), len(linked), msg)
	}
}

func TestLinkSectionEscapesFileNames(t *testing.T) {
	when := time.Date(2026, 12, 1, 15, 4, 0, 0, time.UTC)
	notices := []linkNotice{{
		ID: "11111111-1111-1111-1111-111111111111", Filename: `<img src=x onerror=alert(1)>`, ContentType: "text/plain",
		URL: "https://mail.example/files/abc", Size: 2048, Expires: &when, Password: true,
	}}
	var out mailpkg.Outgoing
	out.Text = "See the files."
	out.HTML = "<p>See the files.</p>"
	applyLinkSection(&out, notices)
	if strings.Contains(out.HTML, "<img") || !strings.Contains(out.HTML, "&lt;img") || !strings.Contains(out.HTML, "https://mail.example/files/abc") {
		t.Fatalf("html was not escaped:\n%s", out.HTML)
	}
	if !strings.Contains(out.Text, "A password is required") || !strings.Contains(out.Text, "https://mail.example/files/abc") {
		t.Fatalf("text section missing:\n%s", out.Text)
	}
	if len(out.FileLinkIDs) != 1 {
		t.Fatalf("ids = %#v", out.FileLinkIDs)
	}
	raw, _, err := out.Build("mail.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "X-Aether-File-Links: 11111111-1111-1111-1111-111111111111") {
		t.Fatalf("header missing:\n%s", raw)
	}
	if int64(len(raw)) > 50<<20 && messageSizeError(raw, 50<<20) == "" {
		t.Fatal("oversized message was accepted")
	}
}

func TestLinkPasswordAndExpiry(t *testing.T) {
	if validateLinkPassword("short") == "" || validateLinkPassword(strings.Repeat("a", 8)) != "" {
		t.Fatal("password length check failed")
	}
	if validateLinkPassword("ok-password\n") == "" {
		t.Fatal("control character accepted")
	}
	now := time.Now()
	if validateLinkExpiry(now.Add(30*time.Second), now) == "" {
		t.Fatal("expiry too soon was accepted")
	}
	if validateLinkExpiry(now.Add(2*time.Hour), now) != "" {
		t.Fatal("future expiry rejected")
	}
	if validateLinkExpiry(now.Add(maxLinkExpiry+time.Hour), now) == "" {
		t.Fatal("expiry past 5 years accepted")
	}
	hash, expires, msg := resolveLinkAccess(fileLinkRef{ClearPassword: true, ClearExpiry: true}, ptr("hash"), ptrTime(now.Add(time.Hour)), now)
	if msg != "" || hash != nil || expires != nil {
		t.Fatalf("clear failed: %v %v %q", hash, expires, msg)
	}
	if _, _, msg = resolveLinkAccess(fileLinkRef{Password: ptr("short"), ClearPassword: true}, nil, nil, now); msg == "" {
		t.Fatal("password and clear together was accepted")
	}
	if hash, _, msg = resolveLinkAccess(fileLinkRef{Password: ptr("correct horse battery"), passwordHash: "prehashed"}, nil, nil, now); msg != "" || hash == nil || *hash != "prehashed" {
		t.Fatalf("new password did not use the hash made before the transaction: %v %q", hash, msg)
	}
}

func TestFileToken(t *testing.T) {
	token, err := newFileToken()
	if err != nil {
		t.Fatal(err)
	}
	if !fileTokenPattern.MatchString(token) {
		t.Fatalf("token %q", token)
	}
}

func ptr[T any](v T) *T { return &v }

func ptrTime(v time.Time) *time.Time { return &v }
