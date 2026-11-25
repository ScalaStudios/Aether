//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

type addressBookView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Access string `json:"access"`
	Owner  *struct {
		Email string `json:"email"`
	} `json:"owner"`
	Shares []struct {
		ID     string `json:"id"`
		Access string `json:"access"`
	} `json:"shares"`
}

type contactView struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Emails   []string `json:"emails"`
	BookID   string   `json:"bookId"`
	ReadOnly bool     `json:"readOnly"`
}

type groupView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BookID  string `json:"bookId"`
	Members []struct {
		ID     string   `json:"id"`
		Emails []string `json:"emails"`
	} `json:"members"`
}

func TestContactGroupsAndSharedAddressBooks(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	bookNamed := func(c *apiClient, name string) *addressBookView {
		t.Helper()
		var list []addressBookView
		c.must("GET", "/contacts/books", nil, &list, 200)
		for i := range list {
			if list[i].Name == name {
				return &list[i]
			}
		}
		return nil
	}

	var ada contactView
	w.alice.must("POST", "/contacts", map[string]any{"name": "Ada Lovelace", "emails": []string{"ada@example.org"}}, &ada, 201)
	if ada.BookID != "contacts" || ada.Emails[0] != "ada@example.org" {
		t.Fatalf("contact: %+v", ada)
	}
	var grace contactView
	w.alice.must("POST", "/contacts", map[string]any{"name": "Grace Hopper", "emails": []string{"grace@example.org", "hopper@example.org"}}, &grace, 201)

	groupName := "Suppliers " + randomSuffix()
	var group groupView
	w.alice.must("POST", "/contacts/groups", map[string]any{"name": groupName, "members": []string{ada.ID, grace.ID, ada.ID}}, &group, 201)
	if group.BookID != "contacts" || len(group.Members) != 2 {
		t.Fatalf("group: %+v", group)
	}
	var contacts []contactView
	w.alice.must("GET", "/contacts?q="+strings.ReplaceAll(groupName, " ", "%20"), nil, &contacts, 200)
	if len(contacts) != 0 {
		t.Fatalf("group listed as a contact: %+v", contacts)
	}
	var groups []groupView
	w.alice.must("GET", "/contacts/groups?q=suppliers", nil, &groups, 200)
	if len(groups) != 1 || groups[0].ID != group.ID || len(groups[0].Members) != 2 {
		t.Fatalf("groups: %+v", groups)
	}

	var suggestions []struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Members []struct {
			Address string `json:"address"`
		} `json:"members"`
	}
	w.alice.must("GET", "/recipients?q=suppliers", nil, &suggestions, 200)
	if len(suggestions) != 1 || suggestions[0].Source != "group" || suggestions[0].Name != groupName || len(suggestions[0].Members) != 3 {
		t.Fatalf("group suggestion: %+v", suggestions)
	}

	if code := w.alice.do("POST", "/contacts/groups", map[string]any{"name": "", "members": []string{}}, nil); code != 422 {
		t.Fatalf("nameless group returned %d", code)
	}
	tooMany := make([]string, 501)
	for i := range tooMany {
		tooMany[i] = "missing.vcf"
	}
	if code := w.alice.do("POST", "/contacts/groups", map[string]any{"name": "Too big", "members": tooMany}, nil); code != 422 {
		t.Fatalf("oversized group returned %d", code)
	}
	var nested groupView
	w.alice.must("POST", "/contacts/groups", map[string]any{"name": "Nested " + randomSuffix()}, &nested, 201)
	if code := w.alice.do("PUT", "/contacts/groups/"+group.ID, map[string]any{"name": groupName, "bookId": "contacts", "members": []string{nested.ID}}, nil); code != 422 {
		t.Fatalf("group containing a group returned %d", code)
	}

	adaUID := strings.TrimSuffix(ada.ID, ".vcf")
	imported := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:import-" + randomSuffix() + "\r\nFN:Imported Club\r\nX-ADDRESSBOOKSERVER-KIND:group\r\nX-ADDRESSBOOKSERVER-MEMBER:urn:uuid:" + adaUID + "\r\nEND:VCARD\r\n"
	w.alice.mustRaw("POST", "/contacts/import", "text/vcard", imported, nil, 200)
	w.alice.must("GET", "/contacts?q=Imported%20Club", nil, &contacts, 200)
	if len(contacts) != 0 {
		t.Fatalf("imported group listed as a contact: %+v", contacts)
	}
	w.alice.must("GET", "/contacts/groups?q=Imported", nil, &groups, 200)
	if len(groups) != 1 || len(groups[0].Members) != 1 || groups[0].Members[0].ID != ada.ID {
		t.Fatalf("imported group: %+v", groups)
	}

	base := env(t, "AETHER_TEST_API", "http://api:8080")
	httpClient := webdav.HTTPClientWithBasicAuth(http.DefaultClient, alice, w.aliceAppPassword)
	cards, err := carddav.NewClient(httpClient, base+"/.well-known/carddav")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	principal, err := cards.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home, err := cards.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	davBooks, err := cards.FindAddressBooks(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	personal := ""
	for _, book := range davBooks {
		if strings.HasSuffix(book.Path, "/contacts/") {
			personal = book.Path
		}
	}
	if personal == "" {
		t.Fatalf("personal address book missing: %+v", davBooks)
	}
	obj, err := cards.GetAddressObject(ctx, personal+group.ID)
	if err != nil || !strings.EqualFold(obj.Card.Value("X-ADDRESSBOOKSERVER-KIND"), "group") || obj.Card.Value(vcard.FieldFormattedName) != groupName {
		t.Fatalf("group over CardDAV: %v %+v", err, obj)
	}
	if !strings.Contains(strings.Join(obj.Card.Values("X-ADDRESSBOOKSERVER-MEMBER"), " "), adaUID) {
		t.Fatalf("group members over CardDAV: %v", obj.Card.Values("X-ADDRESSBOOKSERVER-MEMBER"))
	}

	w.alice.must("DELETE", "/contacts/"+ada.ID+"?book=contacts", nil, nil, 204)
	w.alice.must("GET", "/contacts/groups/"+group.ID+"?book=contacts", nil, &group, 200)
	for _, member := range group.Members {
		if member.ID == ada.ID {
			t.Fatalf("deleted contact still in group: %+v", group.Members)
		}
	}
	obj, err = cards.GetAddressObject(ctx, personal+group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(strings.Join(obj.Card.Values("X-ADDRESSBOOKSERVER-MEMBER"), " ")), strings.ToLower(adaUID)) || strings.Contains(strings.ToLower(strings.Join(obj.Card.Values(vcard.FieldMember), " ")), strings.ToLower(adaUID)) {
		t.Fatalf("deleted member still in the group card: apple=%v member=%v", obj.Card.Values("X-ADDRESSBOOKSERVER-MEMBER"), obj.Card.Values(vcard.FieldMember))
	}

	sales := "Sales " + randomSuffix()
	var book addressBookView
	w.alice.must("POST", "/contacts/books", map[string]string{"name": sales, "description": "Customer contacts", "color": "#10b981"}, &book, 201)
	var customer contactView
	w.alice.must("POST", "/contacts", map[string]any{"name": "Casey Customer", "emails": []string{"casey@example.org"}, "bookId": book.ID}, &customer, 201)
	if customer.BookID != book.ID {
		t.Fatalf("contact book: %+v", customer)
	}
	if bookNamed(w.bob, sales) != nil {
		t.Fatal("bob sees the address book before it is shared")
	}
	if code := w.alice.do("POST", "/contacts/books/"+book.ID+"/shares", map[string]string{"email": "nobody@example.com", "access": "read"}, nil); code != 422 {
		t.Fatalf("sharing with an unknown address returned %d", code)
	}
	if code := w.alice.do("POST", "/contacts/books/"+book.ID+"/shares", map[string]string{"orgId": w.orgID, "access": "read"}, nil); code != 403 {
		t.Fatalf("a member sharing with the organization returned %d", code)
	}
	w.alice.must("POST", "/contacts/books/"+book.ID+"/shares", map[string]string{"email": bob, "access": "read"}, nil, 204)

	shared := bookNamed(w.bob, sales)
	if shared == nil || shared.Access != "read" || shared.Owner == nil || shared.Owner.Email != alice || !strings.HasPrefix(shared.ID, "shared-") {
		t.Fatalf("shared book: %+v", shared)
	}
	var bobContacts []contactView
	w.bob.must("GET", "/contacts?book="+shared.ID, nil, &bobContacts, 200)
	if len(bobContacts) != 1 || bobContacts[0].Name != "Casey Customer" || !bobContacts[0].ReadOnly || bobContacts[0].BookID != shared.ID {
		t.Fatalf("bob's view: %+v", bobContacts)
	}
	if code := w.bob.do("POST", "/contacts", map[string]any{"name": "Extra", "bookId": shared.ID}, nil); code != 403 {
		t.Fatalf("write to a read-only book returned %d", code)
	}
	if code := w.bob.do("DELETE", "/contacts/"+customer.ID+"?book="+shared.ID, nil, nil); code != 403 {
		t.Fatalf("delete in a read-only book returned %d", code)
	}

	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	bobClient := webdav.HTTPClientWithBasicAuth(http.DefaultClient, bob, bobPass)
	bobCards, err := carddav.NewClient(bobClient, base+"/.well-known/carddav")
	if err != nil {
		t.Fatal(err)
	}
	bobPrincipal, err := bobCards.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bobHome, err := bobCards.FindAddressBookHomeSet(ctx, bobPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	bobBooks, err := bobCards.FindAddressBooks(ctx, bobHome)
	if err != nil {
		t.Fatal(err)
	}
	sharedPath := ""
	for _, item := range bobBooks {
		if strings.HasSuffix(item.Path, "/"+shared.ID+"/") {
			sharedPath = item.Path
		}
	}
	if sharedPath == "" {
		t.Fatalf("shared book missing from CardDAV: %+v", bobBooks)
	}
	sharedCard, err := bobCards.GetAddressObject(ctx, sharedPath+customer.ID)
	if err != nil || sharedCard.Card.PreferredValue(vcard.FieldFormattedName) != "Casey Customer" {
		t.Fatalf("shared contact over CardDAV: %v %+v", err, sharedCard)
	}
	outside := vcard.Card{}
	outside.SetValue(vcard.FieldVersion, "4.0")
	outside.SetValue(vcard.FieldUID, "from-app")
	outside.SetValue(vcard.FieldFormattedName, "From a contacts app")
	outside.SetValue(vcard.FieldEmail, "from-app@example.org")
	if _, err := bobCards.PutAddressObject(ctx, sharedPath+"from-app.vcf", outside); err == nil {
		t.Fatal("CardDAV write to a read-only address book should fail")
	}

	w.alice.must("POST", "/contacts/books/"+book.ID+"/shares", map[string]string{"email": bob, "access": "write"}, nil, 204)
	if _, err := bobCards.PutAddressObject(ctx, sharedPath+"from-app.vcf", outside); err != nil {
		t.Fatalf("CardDAV write with edit access: %v", err)
	}
	w.bob.must("POST", "/contacts", map[string]any{"name": "Bob's lead", "emails": []string{"lead@example.org"}, "bookId": shared.ID}, nil, 201)
	w.alice.must("GET", "/contacts?book="+book.ID, nil, &contacts, 200)
	if len(contacts) != 3 {
		t.Fatalf("alice should see her contact and both of bob's: %+v", contacts)
	}

	var orgBook addressBookView
	w.admin.must("POST", "/contacts/books", map[string]string{"name": "Company " + randomSuffix()}, &orgBook, 201)
	w.admin.must("POST", "/contacts/books/"+orgBook.ID+"/shares", map[string]string{"orgId": w.orgID, "access": "read"}, nil, 204)
	if seen := bookNamed(w.bob, orgBook.Name); seen == nil || seen.Access != "read" {
		t.Fatalf("organization address book: %+v", seen)
	}

	owned := bookNamed(w.alice, sales)
	if owned == nil || len(owned.Shares) != 1 || owned.Shares[0].Access != "write" {
		t.Fatalf("owner should see the share: %+v", owned)
	}
	w.alice.must("DELETE", "/contacts/books/"+book.ID+"/shares/"+owned.Shares[0].ID, nil, nil, 204)
	if bookNamed(w.bob, sales) != nil {
		t.Fatal("bob still sees the address book after it was unshared")
	}
	if code := w.alice.do("DELETE", "/contacts/books/contacts", nil, nil); code != 422 {
		t.Fatalf("deleting the main address book returned %d", code)
	}
	w.alice.must("DELETE", "/contacts/books/"+book.ID, nil, nil, 204)
	if bookNamed(w.alice, sales) != nil {
		t.Fatal("deleted address book is still listed")
	}
}
