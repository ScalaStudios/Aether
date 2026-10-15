//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestAntivirusRejectsEICAR(t *testing.T) {
	if env(t, "AETHER_TEST_ANTIVIRUS", "") != "true" {
		t.Skip("ClamAV is not enabled; set AETHER_CLAMAV_ADDR and start the antivirus profile")
	}
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	bob := "bob@" + w.domain
	eicar := `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`
	msg := "From: sender@external.example\r\nTo: " + bob + "\r\nSubject: eicar\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nattached\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=eicar.com\r\n\r\n" + eicar + "\r\n--b--\r\n"
	err := submit(t, smtpAddr, "", "", "sender@external.example", []string{bob}, msg, false)
	if err == nil || !strings.Contains(err.Error(), "virus") {
		t.Fatalf("EICAR message was not rejected as a virus: %v", err)
	}
	clean := strings.Replace(msg, eicar, "harmless attachment", 1)
	if err := submit(t, smtpAddr, "", "", "sender@external.example", []string{bob}, clean, false); err != nil {
		t.Fatalf("clean attachment was rejected: %v", err)
	}
}
