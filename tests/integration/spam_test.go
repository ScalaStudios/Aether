//go:build integration

package integration

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func spamLearned(t *testing.T, class string, results ...string) float64 {
	t.Helper()
	resp, err := http.Get(env(t, "AETHER_TEST_METRICS", "http://api:9090/metrics"))
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var sum float64
	for _, result := range results {
		prefix := `aether_spam_learned_total{class="` + class + `",result="` + result + `"} `
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(line, prefix); ok {
				f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
				sum += f
			}
		}
	}
	return sum
}

func imapMoveSubject(t *testing.T, c *imapclient.Client, from, to, subject string) {
	t.Helper()
	if _, err := c.Select(from, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", from, err)
	}
	data, err := c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: subject}}}, nil).Wait()
	if err != nil || len(data.AllUIDs()) != 1 {
		t.Fatalf("find %q in %s: %v", subject, from, err)
	}
	if _, err := c.Move(imap.UIDSetNum(data.AllUIDs()...), to).Wait(); err != nil {
		t.Fatalf("move %q to %s: %v", subject, to, err)
	}
}

func spamBody() string {
	words := make([]string, 0, 80)
	for range 80 {
		words = append(words, randomSuffix())
	}
	return strings.Join(words, " ")
}

func TestSpamLearningFromJunkMoves(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	ic, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer ic.Close()

	suffix := randomSuffix()
	reported, trashed, rescued, ignored := "reported "+suffix, "trashed "+suffix, "rescued "+suffix, "ignored "+suffix
	for _, subject := range []string{reported, trashed, rescued, ignored} {
		imapAppend(t, ic, "INBOX", rfc822("offers@external.example", alice, subject, spamBody()), time.Now())
	}

	spam, ham := spamLearned(t, "spam", "learned", "duplicate"), spamLearned(t, "ham", "learned", "duplicate")
	imapMoveSubject(t, ic, "INBOX", "Junk", reported)
	if got := spamLearned(t, "spam", "learned", "duplicate"); got != spam+1 {
		t.Fatalf("spam learned %v after a move into Junk, want %v", got, spam+1)
	}

	imapMoveSubject(t, ic, "INBOX", "Junk", trashed)
	imapMoveSubject(t, ic, "Junk", "Trash", trashed)
	if got := spamLearned(t, "ham", "learned", "duplicate"); got != ham {
		t.Fatalf("ham learned %v after a move from Junk to Trash, want %v", got, ham)
	}

	imapMoveSubject(t, ic, "INBOX", "Junk", rescued)
	uid := findInAPI(w.alice, w.aliceMailbox, "Junk", rescued)
	w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/messages/"+itoaUID(uid)+"/move?folder=Junk", map[string]any{"to": "INBOX"}, nil, 204)
	if got := spamLearned(t, "ham", "learned", "duplicate"); got != ham+1 {
		t.Fatalf("ham learned %v after Not spam, want %v", got, ham+1)
	}

	var org struct {
		SpamLearning bool `json:"spamLearning"`
	}
	w.admin.must("PATCH", "/organizations/"+w.orgID, map[string]any{"spamLearning": false}, &org, 200)
	if org.SpamLearning {
		t.Fatal("spam learning is still on after turning it off")
	}
	disabled := spamLearned(t, "spam", "disabled")
	imapMoveSubject(t, ic, "INBOX", "Junk", ignored)
	if got := spamLearned(t, "spam", "disabled"); got != disabled+1 {
		t.Fatalf("disabled spam learning counted %v, want %v", got, disabled+1)
	}
}
