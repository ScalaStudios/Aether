package auth

import (
	"strings"
	"testing"
	"time"
)

func TestTOTPMatchesRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037"}
	for unix, want := range vectors {
		if got := totpCode(secret, unix/totpPeriod); got != want {
			t.Errorf("code at %d = %s, want %s", unix, got, want)
		}
		if step, ok := VerifyTOTP(secret, want, time.Unix(unix, 0), 0); !ok || step != unix/totpPeriod {
			t.Errorf("valid code at %d rejected", unix)
		}
	}
}

func TestTOTPRejectsReplayAndDrift(t *testing.T) {
	secret := NewTOTPSecret()
	now := time.Unix(1_800_000_000, 0)
	step := now.Unix() / totpPeriod
	code := totpCode(secret, step)
	if _, ok := VerifyTOTP(secret, code, now, step); ok {
		t.Fatal("a code at or before the last used step was accepted")
	}
	if _, ok := VerifyTOTP(secret, totpCode(secret, step-1), now, 0); !ok {
		t.Fatal("the previous step was rejected")
	}
	if _, ok := VerifyTOTP(secret, totpCode(secret, step-2), now, 0); ok {
		t.Fatal("a code two steps old was accepted")
	}
	if _, ok := VerifyTOTP(secret, code+"0", now, 0); ok {
		t.Fatal("a seven digit code was accepted")
	}
}

func TestTOTPURI(t *testing.T) {
	uri := TOTPURI("Aether", "ana@example.com", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	if !strings.HasPrefix(uri, "otpauth://totp/Aether:ana@example.com?") || !strings.Contains(uri, "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ") || !strings.Contains(uri, "issuer=Aether") {
		t.Fatalf("unexpected URI %s", uri)
	}
}
