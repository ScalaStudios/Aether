package api

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/jackc/pgx/v5"
)

const (
	defaultAddressBook     = "contacts"
	appleAddressBookKind   = "X-ADDRESSBOOKSERVER-KIND"
	appleAddressBookMember = "X-ADDRESSBOOKSERVER-MEMBER"
	maxGroupMembers        = 500
)

var errAmbiguousObject = errors.New("ambiguous address book object")

func normalizeUID(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 9 && strings.EqualFold(raw[:9], "urn:uuid:") {
		return strings.TrimSpace(raw[9:])
	}
	return raw
}

func memberRef(uid string) string {
	uid = normalizeUID(uid)
	if uid == "" {
		return ""
	}
	return "urn:uuid:" + uid
}

func isGroupCard(card vcard.Card) bool {
	if strings.EqualFold(strings.TrimSpace(card.Value(vcard.FieldKind)), string(vcard.KindGroup)) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(card.Value(appleAddressBookKind)), "group")
}

func memberUIDs(card vcard.Card) []string {
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		uid := normalizeUID(raw)
		if uid == "" {
			return
		}
		key := strings.ToLower(uid)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, uid)
	}
	for _, value := range card.Values(appleAddressBookMember) {
		add(value)
	}
	for _, value := range card.Values(vcard.FieldMember) {
		add(value)
	}
	return out
}

func buildGroupCard(uid, name string, members []string) vcard.Card {
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldProductID, "-//Scala Studios//Aether//EN")
	card.SetValue(vcard.FieldUID, uid)
	card.SetValue(vcard.FieldFormattedName, name)
	card.SetName(&vcard.Name{FamilyName: name})
	card.SetValue(appleAddressBookKind, "group")
	card.SetKind(vcard.KindGroup)
	for _, id := range members {
		ref := memberRef(id)
		if ref == "" {
			continue
		}
		card.AddValue(appleAddressBookMember, ref)
		card.AddValue(vcard.FieldMember, ref)
	}
	card.SetRevision(time.Now().UTC())
	return card
}

func stripMember(card vcard.Card, uid string) {
	target := strings.ToLower(normalizeUID(uid))
	for _, key := range []string{appleAddressBookMember, vcard.FieldMember} {
		var kept []*vcard.Field
		for _, field := range card[key] {
			if strings.ToLower(normalizeUID(field.Value)) == target {
				continue
			}
			kept = append(kept, field)
		}
		if len(kept) == 0 {
			delete(card, key)
		} else {
			card[key] = kept
		}
	}
}

func detachFromGroups(ctx context.Context, tx pgx.Tx, collectionID, uid string) error {
	target := strings.ToLower(normalizeUID(uid))
	if target == "" {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT name, data FROM dav_objects
		WHERE collection_id = $1 AND is_group AND EXISTS (
			SELECT 1 FROM unnest(member_uids) AS member WHERE lower(member) = $2
		) FOR UPDATE`, collectionID, target)
	if err != nil {
		return err
	}
	defer rows.Close()
	type hit struct{ name, data string }
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.name, &h.data); err != nil {
			return err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, h := range hits {
		card, err := vcard.NewDecoder(strings.NewReader(h.data)).Decode()
		if err != nil {
			return err
		}
		stripMember(card, uid)
		fields, err := cardFields(card)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
			return err
		}
		data := buf.String()
		if _, err := tx.Exec(ctx, `
			UPDATE dav_objects
			SET data = $3, etag = $4, summary = $5, emails = $6, member_uids = $7, is_group = $8, updated_at = now()
			WHERE collection_id = $1 AND name = $2`,
			collectionID, h.name, data, etagOf(data), fields.Summary, nonNil(fields.Emails), nonNil(fields.MemberUIDs), fields.Group); err != nil {
			return err
		}
	}
	return nil
}
