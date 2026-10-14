package secrets

import "testing"

func TestBoxRoundTripAndContextBinding(t *testing.T) {
	box, err := NewBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed := box.Seal([]byte("dkim-key"), "domain:a")
	got, err := box.Open(sealed, "domain:a")
	if err != nil || string(got) != "dkim-key" {
		t.Fatalf("round trip failed: %v %q", err, got)
	}
	if _, err := box.Open(sealed, "domain:b"); err == nil {
		t.Fatal("ciphertext opened under a different context")
	}
	if a, b := box.Seal([]byte("x"), ""), box.Seal([]byte("x"), ""); string(a) == string(b) {
		t.Fatal("nonce reuse")
	}
}
