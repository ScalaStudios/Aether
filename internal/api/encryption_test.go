package api

import "testing"

func TestEncryptionDenial(t *testing.T) {
	allow := encryptionPolicy{SMIME: "allow", OpenPGP: "allow", Password: "allow"}
	if code, _ := encryptionDenial(allow, "", false, true); code != "" {
		t.Fatal(code)
	}
	smime := encryptionPolicy{SMIME: "require", OpenPGP: "allow", Password: "allow"}
	if code, _ := encryptionDenial(smime, "", false, false); code != "encryption_required" {
		t.Fatal(code)
	}
	if code, _ := encryptionDenial(smime, "smime", true, false); code != "" {
		t.Fatal(code)
	}
	if code, _ := encryptionDenial(smime, "smime", false, false); code != "encryption_required" {
		t.Fatal(code)
	}
	if code, _ := encryptionDenial(smime, "openpgp", true, false); code != "encryption_required" {
		t.Fatal(code)
	}
	off := encryptionPolicy{SMIME: "off", OpenPGP: "allow", Password: "off"}
	if code, _ := encryptionDenial(off, "smime", true, false); code != "encryption_disabled" {
		t.Fatal(code)
	}
	if code, _ := encryptionDenial(off, "password", true, true); code != "protected_mail_disabled" {
		t.Fatal(code)
	}
	pass := encryptionPolicy{SMIME: "allow", OpenPGP: "allow", Password: "require"}
	if code, _ := encryptionDenial(pass, "", false, true); code != "password_required" {
		t.Fatal(code)
	}
	if code, _ := encryptionDenial(pass, "", false, false); code != "" {
		t.Fatal(code)
	}
	if code, _ := encryptionDenial(pass, "openpgp", true, true); code != "" {
		t.Fatal(code)
	}
	both := encryptionPolicy{SMIME: "require", OpenPGP: "require", Password: "allow"}
	if code, _ := encryptionDenial(both, "openpgp", true, false); code != "" {
		t.Fatal(code)
	}
	if code, _ := encryptionDenial(both, "password", true, true); code != "encryption_required" {
		t.Fatal(code)
	}
}
