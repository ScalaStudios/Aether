package auth

import "testing"

func TestPasswordHashing(t *testing.T) {
	h := HashPassword("correct horse battery staple")
	if ok, err := VerifyPassword(h, "correct horse battery staple"); !ok || err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	if ok, _ := VerifyPassword(h, "wrong"); ok {
		t.Fatal("invalid password accepted")
	}
	if h == HashPassword("correct horse battery staple") {
		t.Fatal("salt not applied")
	}
}
