//go:build integration

package integration

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

type directoryEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Source  string `json:"source"`
	Company string `json:"company"`
	Phone   string `json:"phone"`
}

type orgContactBody struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Company string `json:"company"`
	Phone   string `json:"phone"`
}

func directoryAddresses(c *apiClient, path string) []string {
	var entries []directoryEntry
	c.must("GET", path, nil, &entries, 200)
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Address)
	}
	return out
}

func TestDirectoryVisibilityAndOrganizationContacts(t *testing.T) {
	w := setupWorld(t)
	ctx := context.Background()
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	suffix := randomSuffix()
	outside := "accounts-" + suffix + "@ledger.example"
	contacts := "/organizations/" + w.orgID + "/contacts"

	var contact orgContactBody
	w.admin.must("POST", contacts, map[string]string{"name": "Ledger Accounts", "email": " Accounts-" + suffix + "@Ledger.example ", "company": "Ledger LLP", "phone": "+44 20 7946 0000"}, &contact, 201)
	if contact.Email != outside || contact.Company != "Ledger LLP" {
		t.Fatalf("created contact %+v", contact)
	}
	if code := w.admin.do("POST", contacts, map[string]string{"name": "Again", "email": outside}, nil); code != 409 {
		t.Fatalf("duplicate contact: %d, want 409", code)
	}
	if code := w.admin.do("POST", contacts, map[string]string{"name": "", "email": "x@ledger.example"}, nil); code != 422 {
		t.Fatalf("nameless contact: %d, want 422", code)
	}
	if code := w.alice.do("GET", contacts, nil, nil); code != 403 {
		t.Fatalf("member listed organization contacts: %d, want 403", code)
	}
	w.admin.must("PUT", contacts+"/"+contact.ID, map[string]string{"name": "Ledger Accounts", "email": outside, "company": "Ledger Partners LLP", "phone": "+44 20 7946 0000"}, &contact, 200)

	var dir []directoryEntry
	w.alice.must("GET", "/directory?q=ledger", nil, &dir, 200)
	if len(dir) != 1 || dir[0].Address != outside || dir[0].Name != "Ledger Accounts" || dir[0].Company != "Ledger Partners LLP" || dir[0].Phone != "+44 20 7946 0000" {
		t.Fatalf("organization contact in the directory: %+v", dir)
	}
	var suggestions []directoryEntry
	w.alice.must("GET", "/recipients?q=ledger", nil, &suggestions, 200)
	if len(suggestions) != 1 || suggestions[0].Address != outside || suggestions[0].Source != "directory" {
		t.Fatalf("organization contact suggestion: %+v", suggestions)
	}

	var list struct {
		ID           string   `json:"id"`
		Address      string   `json:"address"`
		Destinations []string `json:"destinations"`
		Hidden       bool     `json:"hiddenFromDirectory"`
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "finance", "kind": "list", "name": "Finance", "destinations": []string{alice}, "hiddenFromDirectory": true}, &list, 201)
	if !list.Hidden {
		t.Fatalf("created list %+v", list)
	}
	if code := w.admin.do("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "plain", "destinations": []string{alice}, "hiddenFromDirectory": true}, nil); code != 422 {
		t.Fatalf("hidden plain alias: %d, want 422", code)
	}
	var mailbox struct {
		Hidden bool `json:"hiddenFromDirectory"`
	}
	w.admin.must("PATCH", "/mailboxes/"+w.bobMailbox, map[string]bool{"hiddenFromDirectory": true}, &mailbox, 200)
	if !mailbox.Hidden {
		t.Fatal("PATCH hiddenFromDirectory=true left the mailbox visible")
	}

	listed := directoryAddresses(w.alice, "/directory")
	if !slices.Contains(listed, alice) || !slices.Contains(listed, outside) || slices.Contains(listed, bob) || slices.Contains(listed, list.Address) {
		t.Fatalf("directory with hidden entries: %v", listed)
	}
	for _, q := range []string{"bob", "finance"} {
		if got := directoryAddresses(w.alice, "/recipients?q="+q); slices.Contains(got, bob) || slices.Contains(got, list.Address) {
			t.Fatalf("hidden address suggested for %q: %v", q, got)
		}
	}

	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	w.bob.must("GET", "/session", nil, &me, 200)
	w.admin.must("PATCH", "/aliases/"+list.ID, map[string]any{"owners": []string{me.User.ID}}, nil, 200)
	if code := w.bob.do("PATCH", "/aliases/"+list.ID, map[string]any{"hiddenFromDirectory": false}, nil); code != 403 {
		t.Fatalf("list owner changed directory visibility: %d, want 403", code)
	}

	subject := "hidden " + suffix
	if err := submit(t, env(t, "AETHER_TEST_SUBMISSION", "postfix:587"), alice, w.aliceAppPassword, alice, []string{bob, list.Address}, rfc822(alice, bob, subject, "x"), false); err != nil {
		t.Fatalf("sending to hidden addresses: %v", err)
	}
	waitFor(t, "delivery to a hidden mailbox", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 })
	waitFor(t, "delivery through a hidden list", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", subject) != 0 })

	w.admin.must("PATCH", "/aliases/"+list.ID, map[string]any{"addDestinations": []string{contact.Email}}, &list, 200)
	if !slices.Contains(list.Destinations, outside) {
		t.Fatalf("organization contact as a list member: %+v", list)
	}

	httpClient := webdav.HTTPClientWithBasicAuth(http.DefaultClient, alice, w.aliceAppPassword)
	cards, err := carddav.NewClient(httpClient, env(t, "AETHER_TEST_API", "http://api:8080")+"/.well-known/carddav")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cards.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home, err := cards.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	books, err := cards.FindAddressBooks(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(books, func(b carddav.AddressBook) bool { return b.Name == "Directory" })
	if at < 0 || books[0].Name == "Directory" {
		t.Fatalf("address books %+v", books)
	}
	objects, err := cards.QueryAddressBook(ctx, books[at].Path, &carddav.AddressBookQuery{DataRequest: carddav.AddressDataRequest{AllProp: true}, FilterTest: carddav.FilterAllOf})
	if err != nil {
		t.Fatalf("query directory over CardDAV: %v", err)
	}
	var emails []string
	var ledger *carddav.AddressObject
	for i, o := range objects {
		emails = append(emails, o.Card.Value(vcard.FieldEmail))
		if o.Card.Value(vcard.FieldEmail) == outside {
			ledger = &objects[i]
		}
	}
	if !slices.Contains(emails, alice) || slices.Contains(emails, bob) || slices.Contains(emails, list.Address) || ledger == nil {
		t.Fatalf("CardDAV directory: %v", emails)
	}
	if ledger.Card.Value(vcard.FieldOrganization) != "Ledger Partners LLP" || ledger.Card.Value(vcard.FieldTelephone) != "+44 20 7946 0000" {
		t.Fatalf("CardDAV organization contact: %v", ledger.Card)
	}
	if one, err := cards.GetAddressObject(ctx, ledger.Path); err != nil || one.Card.PreferredValue(vcard.FieldFormattedName) != "Ledger Accounts" {
		t.Fatalf("GET one directory card: %v %v", one, err)
	}
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, "intruder")
	card.SetValue(vcard.FieldFormattedName, "Intruder")
	if _, err := cards.PutAddressObject(ctx, books[at].Path+"intruder.vcf", card); err == nil {
		t.Fatal("wrote a card into the read-only directory")
	}

	w.admin.must("PATCH", "/mailboxes/"+w.bobMailbox, map[string]bool{"hiddenFromDirectory": false}, nil, 200)
	if got := directoryAddresses(w.alice, "/directory?q=bob"); !slices.Contains(got, bob) {
		t.Fatalf("unhidden mailbox missing from the directory: %v", got)
	}

	var created struct {
		Token string `json:"token"`
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/api-keys", map[string]any{"name": "directory", "scopes": []string{"contacts:read"}}, &created, 201)
	key := &apiClient{t: t, base: w.admin.base, http: &http.Client{Timeout: 30 * time.Second}, bearer: created.Token}
	var viaKey []orgContactBody
	key.must("GET", contacts, nil, &viaKey, 200)
	if len(viaKey) != 1 || viaKey[0].ID != contact.ID {
		t.Fatalf("contacts through an API key: %+v", viaKey)
	}
	if code := key.do("POST", contacts, map[string]string{"name": "Read only", "email": "ro@ledger.example"}, nil); code != 403 {
		t.Fatalf("contacts:read created a contact: %d, want 403", code)
	}

	w.admin.must("DELETE", contacts+"/"+contact.ID, nil, nil, 204)
	if got := directoryAddresses(w.alice, "/directory?q=ledger"); len(got) != 0 {
		t.Fatalf("deleted contact still in the directory: %v", got)
	}
	w.admin.must("DELETE", "/aliases/"+list.ID, nil, nil, 204)
}
