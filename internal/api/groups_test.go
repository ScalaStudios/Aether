package api

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
)

func TestGroupCardRoundTrip(t *testing.T) {
	card := buildGroupCard("suppliers", "Suppliers", []string{"ada", "urn:uuid:grace"})
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, want := range []string{"X-ADDRESSBOOKSERVER-KIND:group", "KIND:group", "urn:uuid:ada", "urn:uuid:grace"} {
		if !strings.Contains(text, want) {
			t.Fatalf("group card missing %q:\n%s", want, text)
		}
	}
	decoded, err := vcard.NewDecoder(strings.NewReader(text)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	fields, err := cardFields(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !fields.Group || fields.Summary != "Suppliers" || len(fields.Emails) != 0 {
		t.Fatalf("fields %+v", fields)
	}
	if len(fields.MemberUIDs) != 2 || fields.MemberUIDs[0] != "ada" || fields.MemberUIDs[1] != "grace" {
		t.Fatalf("members %v", fields.MemberUIDs)
	}
	stripMember(decoded, "URN:UUID:Ada")
	if uids := memberUIDs(decoded); len(uids) != 1 || uids[0] != "grace" {
		t.Fatalf("after strip %v", uids)
	}
}

func TestAppleGroupWithoutKind(t *testing.T) {
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldUID, "g1")
	card.SetValue(vcard.FieldFormattedName, "Board")
	card.SetValue(appleAddressBookKind, "group")
	card.AddValue(appleAddressBookMember, "urn:uuid:person-1")
	card.AddValue(appleAddressBookMember, "urn:uuid:person-1")
	fields, err := cardFields(card)
	if err != nil || !fields.Group || len(fields.MemberUIDs) != 1 || fields.MemberUIDs[0] != "person-1" {
		t.Fatalf("%+v %v", fields, err)
	}
}

func TestKindGroup(t *testing.T) {
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, "g")
	card.SetValue(vcard.FieldFormattedName, "Team")
	card.SetKind(vcard.KindGroup)
	card.AddValue(vcard.FieldMember, "urn:uuid:one")
	fields, err := cardFields(card)
	if err != nil || !fields.Group || len(fields.MemberUIDs) != 1 || fields.MemberUIDs[0] != "one" || len(fields.Emails) != 0 {
		t.Fatalf("%+v %v", fields, err)
	}
}

func TestIndividualCardIsNotAGroup(t *testing.T) {
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, "p1")
	card.SetValue(vcard.FieldFormattedName, "Ada")
	card.AddValue(vcard.FieldEmail, "Ada@Example.org")
	fields, err := cardFields(card)
	if err != nil || fields.Group || len(fields.MemberUIDs) != 0 || len(fields.Emails) != 1 || fields.Emails[0] != "ada@example.org" {
		t.Fatalf("%+v %v", fields, err)
	}
}
