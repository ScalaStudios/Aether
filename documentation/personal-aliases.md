# Personal aliases

People can create their own aliases under **Settings → Personal aliases**, on the domains an administrator allows and up to a limit per person. A personal alias forwards only to a mailbox its creator owns, so nobody can use one to read someone else's mail. The section appears once an administrator allows personal aliases.

There are two kinds:

| | Masked address | Chosen address |
| --- | --- | --- |
| Created with | **Generate masked address**, one click | **Create alias**, typing the part before `@` |
| Looks like | `k7xq2m9fa4bd3@id.example.com`: 13 random lowercase letters and digits | `supplier-portal@id.example.com` |
| Typical use | One per site or sign-up form, so a leak shows who leaked it | An address you want to read out or remember |

Both can carry a note, such as where the address was used, and an expiry date.

## Using personal aliases

Each alias has a switch. Turning it off rejects mail sent to it during the SMTP session with `550 5.1.1 Address unavailable`, also for tagged forms such as `name+news@`, so a catch-all never picks it up, and mail apps can no longer send as it. Turning it back on restores both at once. An alias with an expiry date behaves as if it were turned off from the end of that day, in the browser's time zone; clearing the date or moving it later brings it back.

Deleting an alias releases the address, and anyone in the organization who may create personal aliases can take it afterwards. To stop mail but keep the address, turn it off instead.

A chosen address must start and end with a letter or digit, may contain dots, hyphens and underscores, but no plus sign or two dots in a row, and cannot be `abuse`, `admin`, `administrator`, `dmarc-reports`, `hostmaster`, `postmaster`, `security` or `webmaster`, or an address that already belongs to a mailbox or alias.

A personal alias stops receiving mail when its owner is suspended or removed from the organization, or when the mailbox it forwards to is deactivated, converted to a shared mailbox or given to someone else.

## Password managers

Aether answers the SimpleLogin random alias endpoint, `POST /api/alias/random/new`, so password managers can create a masked address while they fill in a sign-up form.

1. Under **Settings → Personal aliases**, choose **Create key** and copy the key. Aether shows it only once.
2. In Bitwarden's username generator, choose **Forwarded email alias**, then **SimpleLogin**, enter the server address shown next to the key, such as `https://mail.example.com`, and paste the key.

Each person has one key per organization. It creates addresses on the domain chosen when it was made, forwarding to the mailbox selected in Settings at that moment. **Replace key** issues a new key and stops the old one at once; **Revoke key** removes it, and addresses it already created keep working.

Other clients can call the endpoint directly:

```sh
curl -X POST -H "Authentication: <key>" -H "Content-Type: application/json" \
  -d '{"note": "Conference registration"}' \
  "https://mail.example.com/api/alias/random/new?hostname=conference.example.org"
```

`hostname` becomes the note when the body has none. A successful call answers `201` with `id`, `email`, `alias` (the same address, which Bitwarden reads), `enabled` and `note`. It answers `401` for a missing or unknown key, `403` when the policy no longer allows the key's domain or mailbox, `409 alias_limit` at the limit, and `429` after 60 requests from one person in a minute. Responses allow any origin, so browser-based password managers can call it; the key is only ever sent in a header.

## Administering personal aliases

Under **Administration → Aliases → Personal aliases**, **Edit policy** sets the number of aliases per person and the domains people may use. Both start off: a limit of 0 or no allowed domain means nobody can create one. Only verified, active domains can be used. The limit counts every alias a person keeps in the organization, including ones that are off or expired. Policy changes apply to new aliases; existing ones keep working until they are deleted.

Allow a separate domain, such as `id.example.com`, where you can. On an allowed domain a personal alias can take any free address, and while it exists no mailbox can be created at that address: the console and API answer `409 address_taken`, and single sign-on and SCIM create the account without that mailbox. After the alias is deleted, the person's next sign-in through a provider that creates accounts adds the mailbox.

The same page lists every personal alias with its owner and state, and administrators can delete any of them. Administrators cannot point a personal alias elsewhere; through the API they can change only `active`, `note` and `expiresAt`, like the owner.

Every change is in the audit log and sent to webhooks: `alias.create`, `alias.update` and `alias.delete` for aliases, with `personalOwnerId` on creation, `alias.policy.update` for the policy, and `alias.key.issue` and `alias.key.revoke` for password manager keys.

### API

| Route | What it does |
| --- | --- |
| `GET /api/v1/organizations/{orgId}/personal-alias-policy` | The limit per person and the allowed domains, for any member |
| `PUT /api/v1/organizations/{orgId}/personal-alias-policy` | Sets `limitPerUser` and `domainIds`, for administrators |
| `GET /api/v1/organizations/{orgId}/personal-aliases` | The caller's personal aliases and the mailbox their key is bound to |
| `POST /api/v1/organizations/{orgId}/personal-aliases` | Creates one with `mailboxId`, `domainId` and optional `localPart`, `note` and `expiresAt`. An empty `localPart` generates a masked address |
| `PATCH /api/v1/aliases/{aliasId}` | Changes `active`, `note` or `expiresAt`, for the owner or an administrator. `expiresAt: null` clears the expiry |
| `DELETE /api/v1/aliases/{aliasId}` | Deletes it, for the owner or an administrator |
| `PUT`, `DELETE /api/v1/organizations/{orgId}/personal-alias-key` | Issues or replaces the caller's password manager key for a `mailboxId` and `domainId`, or revokes it |

Administrators also see personal aliases in `GET /api/v1/organizations/{orgId}/aliases`, marked by `personalOwnerId`.

## How it works

Personal aliases are rows in the `aliases` table with an owner, the mailbox they forward to, a note and an optional expiry. Migration 0046 adds the view `mail_unavailable_personal_aliases`, which lists every personal alias that is off, expired, or whose owner or mailbox no longer qualifies, and leaves those addresses out of the `mail_aliases` and `mail_senders` views that Postfix reads. Postfix's recipient check rejects the same addresses with `550 5.1.1`. Expiry is evaluated on every lookup, so no background job is involved.

Postfix must run the image from this release. An older image still refuses mail to an alias that is off or expired when the domain has no catch-all, but delivers it to the catch-all when there is one.
