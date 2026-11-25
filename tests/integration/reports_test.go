//go:build integration

package integration

import (
	"bytes"
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReportsAndMailboxMetrics(t *testing.T) {
	w := setupWorld(t)
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice := "alice@" + w.domain
	bob := "bob@" + w.domain

	var carolMB idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{
		"domainId":    w.domainID,
		"localPart":   "carol",
		"displayName": "=SUM(A1:A2)",
		"password":    "carol-integration-pw",
	}, &carolMB, 201)

	var sharedMB idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{
		"domainId":    w.domainID,
		"localPart":   "support",
		"displayName": "Support Team",
		"kind":        "shared",
	}, &sharedMB, 201)

	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{
		"domainId":     w.domainID,
		"localPart":    "team",
		"destinations": []string{bob},
	}, nil, 201)

	ic, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	ic.Close()

	payload := strings.Repeat("Report body data for sizing. ", 60)
	subj1 := "report-direct-" + randomSuffix()
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{bob}, rfc822(alice, bob, subj1, payload), false); err != nil {
		t.Fatalf("direct submission: %v", err)
	}
	subj2 := "report-alias-" + randomSuffix()
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{"team@" + w.domain}, rfc822(alice, "team@"+w.domain, subj2, payload), false); err != nil {
		t.Fatalf("alias submission: %v", err)
	}

	waitFor(t, "bob to receive direct message", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subj1) != 0 })
	waitFor(t, "bob to receive aliased message", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subj2) != 0 })

	type mailboxItem struct {
		ID               string  `json:"id"`
		Address          string  `json:"address"`
		DisplayName      string  `json:"displayName"`
		Kind             string  `json:"kind"`
		StorageBytes     int64   `json:"storageBytes"`
		MessageCount     int64   `json:"messageCount"`
		StorageUpdatedAt *string `json:"storageUpdatedAt"`
		LastWebLoginAt   *string `json:"lastWebLoginAt"`
		LastImapLoginAt  *string `json:"lastImapLoginAt"`
		LastSmtpLoginAt  *string `json:"lastSmtpLoginAt"`
		LastSignInAt     *string `json:"lastSignInAt"`
		Sent30d          int64   `json:"sent30d"`
		Received30d      int64   `json:"received30d"`
	}

	waitFor(t, "mail volume stats to be recorded", func() bool {
		var list []mailboxItem
		w.admin.must("GET", "/organizations/"+w.orgID+"/mailboxes", nil, &list, 200)
		var aliceSent, bobReceived int64
		for _, m := range list {
			if m.Address == alice {
				aliceSent = m.Sent30d
			}
			if m.Address == bob {
				bobReceived = m.Received30d
			}
		}
		return aliceSent >= 2 && bobReceived >= 2
	})

	var refreshed struct {
		Updated          int    `json:"updated"`
		StorageUpdatedAt string `json:"storageUpdatedAt"`
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/reports/refresh", nil, &refreshed, 200)
	if refreshed.Updated < 3 || refreshed.StorageUpdatedAt == "" {
		t.Fatalf("unexpected refresh result: %+v", refreshed)
	}

	var list []mailboxItem
	w.admin.must("GET", "/organizations/"+w.orgID+"/mailboxes", nil, &list, 200)
	byAddr := map[string]mailboxItem{}
	for _, m := range list {
		byAddr[m.Address] = m
	}

	aliceRow := byAddr[alice]
	if aliceRow.LastWebLoginAt == nil || aliceRow.LastImapLoginAt == nil || aliceRow.LastSmtpLoginAt == nil || aliceRow.LastSignInAt == nil {
		t.Fatalf("expected alice web, imap, smtp and combined sign-in timestamps, got %+v", aliceRow)
	}
	if aliceRow.Sent30d < 2 {
		t.Fatalf("expected alice sent30d >= 2, got %d", aliceRow.Sent30d)
	}

	bobRow := byAddr[bob]
	if bobRow.LastWebLoginAt == nil || bobRow.LastSignInAt == nil {
		t.Fatalf("expected bob web sign-in timestamp, got %+v", bobRow)
	}
	if bobRow.LastImapLoginAt != nil || bobRow.LastSmtpLoginAt != nil {
		t.Fatalf("expected bob imap/smtp sign-in nil, got %+v", bobRow)
	}
	if bobRow.StorageBytes <= 0 || bobRow.MessageCount < 2 || bobRow.StorageUpdatedAt == nil {
		t.Fatalf("expected bob storage > 0 and messageCount >= 2, got %+v", bobRow)
	}
	if bobRow.Received30d < 2 {
		t.Fatalf("expected bob received30d >= 2, got %d", bobRow.Received30d)
	}

	carolRow := byAddr["carol@"+w.domain]
	if carolRow.LastSignInAt != nil {
		t.Fatalf("expected dormant carol lastSignInAt nil, got %+v", carolRow)
	}

	var rep struct {
		Days         int `json:"days"`
		InactiveDays int `json:"inactiveDays"`
		Summary      struct {
			TotalMailboxes    int64 `json:"totalMailboxes"`
			ActiveMailboxes   int64 `json:"activeMailboxes"`
			InactiveMailboxes int64 `json:"inactiveMailboxes"`
			TotalStorageBytes int64 `json:"totalStorageBytes"`
			TotalMessages     int64 `json:"totalMessages"`
			SentTotal         int64 `json:"sentTotal"`
			ReceivedTotal     int64 `json:"receivedTotal"`
		} `json:"summary"`
		InactiveMailboxes []struct {
			Address string `json:"address"`
			Kind    string `json:"kind"`
		} `json:"inactiveMailboxes"`
		LargestMailboxes []struct {
			Address      string `json:"address"`
			StorageBytes int64  `json:"storageBytes"`
		} `json:"largestMailboxes"`
		TopSenders []struct {
			Address   string `json:"address"`
			SentCount int64  `json:"sentCount"`
		} `json:"topSenders"`
		TopReceivers []struct {
			Address       string `json:"address"`
			ReceivedCount int64  `json:"receivedCount"`
		} `json:"topReceivers"`
		VolumeSeries []struct {
			Day           string `json:"day"`
			SentCount     int64  `json:"sentCount"`
			ReceivedCount int64  `json:"receivedCount"`
		} `json:"volumeSeries"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/reports?days=14&inactiveDays=30&limit=10", nil, &rep, 200)

	if rep.Days != 14 || rep.InactiveDays != 30 {
		t.Fatalf("unexpected report params: days=%d inactiveDays=%d", rep.Days, rep.InactiveDays)
	}
	if rep.Summary.TotalMailboxes != 4 || rep.Summary.ActiveMailboxes != 4 || rep.Summary.InactiveMailboxes != 1 {
		t.Fatalf("unexpected summary counts: %+v", rep.Summary)
	}
	if rep.Summary.TotalStorageBytes <= 0 || rep.Summary.SentTotal < 2 || rep.Summary.ReceivedTotal < 2 {
		t.Fatalf("unexpected summary totals: %+v", rep.Summary)
	}
	if len(rep.InactiveMailboxes) != 1 || rep.InactiveMailboxes[0].Address != "carol@"+w.domain {
		t.Fatalf("expected only carol in inactiveMailboxes (not shared mailbox), got %+v", rep.InactiveMailboxes)
	}
	if len(rep.LargestMailboxes) == 0 || rep.LargestMailboxes[0].Address != bob || rep.LargestMailboxes[0].StorageBytes <= 0 {
		t.Fatalf("expected bob first in largestMailboxes, got %+v", rep.LargestMailboxes)
	}
	if len(rep.TopSenders) == 0 || rep.TopSenders[0].Address != alice || rep.TopSenders[0].SentCount < 2 {
		t.Fatalf("expected alice first in topSenders, got %+v", rep.TopSenders)
	}
	if len(rep.TopReceivers) == 0 || rep.TopReceivers[0].Address != bob || rep.TopReceivers[0].ReceivedCount < 2 {
		t.Fatalf("expected bob first in topReceivers, got %+v", rep.TopReceivers)
	}
	if len(rep.VolumeSeries) != 14 {
		t.Fatalf("expected 14 volume points, got %d", len(rep.VolumeSeries))
	}
	lastPoint := rep.VolumeSeries[len(rep.VolumeSeries)-1]
	if lastPoint.SentCount < 2 || lastPoint.ReceivedCount < 2 {
		t.Fatalf("expected today volume >= 2 sent and received, got %+v", lastPoint)
	}

	csvEndpoints := []struct {
		path       string
		wantHeader string
	}{
		{"/organizations/" + w.orgID + "/reports/mailboxes?format=csv", "address"},
		{"/organizations/" + w.orgID + "/reports/inactive?format=csv&inactiveDays=30", "address"},
		{"/organizations/" + w.orgID + "/reports/largest?format=csv", "address"},
		{"/organizations/" + w.orgID + "/reports/activity?format=csv&sort=sent&days=30", "address"},
		{"/organizations/" + w.orgID + "/reports/volume?format=csv&days=7", "day"},
	}
	for _, ep := range csvEndpoints {
		req, _ := http.NewRequest("GET", w.admin.base+ep.path, nil)
		resp, err := w.admin.http.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", ep.path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s = %d, want 200: %s", ep.path, resp.StatusCode, raw)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
			t.Fatalf("GET %s Content-Type = %q", ep.path, ct)
		}
		records, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
		if err != nil {
			t.Fatalf("GET %s invalid csv: %v", ep.path, err)
		}
		if len(records) < 2 || records[0][0] != ep.wantHeader {
			t.Fatalf("GET %s unexpected csv rows: %+v", ep.path, records)
		}
		if strings.Contains(ep.path, "/reports/mailboxes") {
			foundCarol := false
			for _, row := range records[1:] {
				if row[0] == "carol@"+w.domain {
					foundCarol = true
					if row[1] != "'=SUM(A1:A2)" {
						t.Fatalf("expected formula-neutralized display name, got %q", row[1])
					}
				}
			}
			if !foundCarol {
				t.Fatalf("carol missing from mailboxes csv")
			}
		}
	}

	reqAccept, _ := http.NewRequest("GET", w.admin.base+"/organizations/"+w.orgID+"/reports/volume?days=7", nil)
	reqAccept.Header.Set("Accept", "text/csv")
	respAccept, err := w.admin.http.Do(reqAccept)
	if err != nil {
		t.Fatalf("Accept text/csv request: %v", err)
	}
	respAccept.Body.Close()
	if respAccept.StatusCode != 200 || !strings.HasPrefix(respAccept.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("Accept text/csv returned %d / %q", respAccept.StatusCode, respAccept.Header.Get("Content-Type"))
	}

	if code := w.admin.do("GET", "/organizations/"+w.orgID+"/reports/unknown", nil, nil); code != 404 {
		t.Fatalf("unknown report returned %d, want 404", code)
	}
	if code := w.admin.do("GET", "/organizations/"+w.orgID+"/reports/activity?sort=bogus", nil, nil); code != 422 {
		t.Fatalf("invalid sort returned %d, want 422", code)
	}
	if code := w.alice.do("GET", "/organizations/"+w.orgID+"/reports", nil, nil); code != 403 {
		t.Fatalf("non-admin member reports returned %d, want 403", code)
	}

	var otherOrg idOnly
	w.admin.must("POST", "/organizations", map[string]string{"name": "Other " + randomSuffix(), "slug": "other-rep-" + randomSuffix()}, &otherOrg, 201)

	var readKeyResp struct {
		Key   struct{ ID string }
		Token string
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/api-keys", map[string]any{
		"name": "reports-reader", "scopes": []string{"reports:read"},
	}, &readKeyResp, 201)
	readKey := &apiClient{t: t, base: w.admin.base, http: &http.Client{Timeout: 30 * time.Second}, bearer: readKeyResp.Token}
	readKey.must("GET", "/organizations/"+w.orgID+"/reports", nil, nil, 200)
	readKey.must("GET", "/organizations/"+w.orgID+"/reports/mailboxes", nil, nil, 200)
	if code := readKey.do("POST", "/organizations/"+w.orgID+"/reports/refresh", nil, nil); code != 403 {
		t.Fatalf("reports:read key refresh returned %d, want 403", code)
	}
	if code := readKey.do("GET", "/organizations/"+otherOrg.ID+"/reports", nil, nil); code != 404 {
		t.Fatalf("cross-org reports key returned %d, want 404", code)
	}

	var writeKeyResp struct {
		Key   struct{ ID string }
		Token string
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/api-keys", map[string]any{
		"name": "reports-writer", "scopes": []string{"reports:write"},
	}, &writeKeyResp, 201)
	writeKey := &apiClient{t: t, base: w.admin.base, http: &http.Client{Timeout: 30 * time.Second}, bearer: writeKeyResp.Token}
	writeKey.must("GET", "/organizations/"+w.orgID+"/reports", nil, nil, 200)
	writeKey.must("POST", "/organizations/"+w.orgID+"/reports/refresh", nil, nil, 200)
}
