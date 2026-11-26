package api

import (
	"context"
	"testing"

	"github.com/scalastudios/aether/internal/config"
)

func TestDomainClaimNeedsTheClaimingOrgsRecord(t *testing.T) {
	dns := fakeDNS{txt: map[string][]string{}}
	s := &Server{cfg: &config.Config{SecretKey: []byte("0123456789abcdef0123456789abcdef")}, resolve: dns}
	rec, proven := s.domainClaimProven(context.Background(), "org-a", "x.test")
	if proven || rec.Name != "_aether.x.test" {
		t.Fatalf("claim without a record was accepted: %+v", rec)
	}
	other, _ := s.domainClaimProven(context.Background(), "org-b", "x.test")
	if other.Value == rec.Value {
		t.Fatal("two organizations got the same claim value")
	}
	dns.txt["_aether.x.test"] = []string{other.Value}
	if _, proven := s.domainClaimProven(context.Background(), "org-a", "x.test"); proven {
		t.Fatal("another organization's claim record was accepted")
	}
	dns.txt["_aether.x.test"] = []string{"v=spf1 -all", rec.Value}
	if _, proven := s.domainClaimProven(context.Background(), "org-a", "x.test"); !proven {
		t.Fatal("the claiming organization's record was refused")
	}
}
