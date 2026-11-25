# Compliance

Aether can keep mail for a legal matter, search it across mailboxes, export it and copy new mail to an archive. These tools live under **Compliance** in Aether, and only compliance administrators see them.

## The compliance administrator role

Compliance administrator is a separate right on an organization membership, not one of the owner, administrator or member roles. An owner grants it under **Administration → Members** by editing a person and turning on **Compliance administrator**. Owners do not have it automatically, and platform administrators and API keys never have it.

A compliance administrator of an organization can read the mail of every mailbox in it, so grant the right sparingly. Every case change, hold, search, export and journal rule is written to the organization's audit log with the person who did it. These `compliance.*` events are not sent to webhooks, so a hold cannot leak to an outside system that follows the audit log.

## Cases

A case has a name, a description, one saved search and the mailboxes held for it. Closing a case releases its holds; you can still search and export a closed case, and reopen it.

### Searching

A search covers selected mailboxes, every mailbox of selected domains, or the whole organization, and can filter by:

| Field | Matches |
| --- | --- |
| From | Part of the sender's address or name |
| To, Cc or Bcc | Part of any recipient's address or name. Bcc is only known for copies the sender kept |
| Subject | Words in the subject |
| Body | Words in the message text |
| Attachment name | Part of an attachment's file name |
| Received from, until | The date the message arrived, both days included |

Every folder of each mailbox is searched, read-only, so nothing is marked as read, together with its recoverable items. Folders of other people's shared mailboxes are not included; search those mailboxes directly. A disabled mailbox is searched only while it is on hold, and the results list the mailboxes that were skipped and any that could not be searched. A search shows at most 500 matches and stops after 90 seconds; the export has no limit.

### Exporting

**Export .eml** downloads a ZIP with one `.eml` file per message, under `<mailbox>/<folder>/<uid>.eml`. **Export mbox** downloads a ZIP with one mbox file (mboxrd) per folder, under `<mailbox>/<folder>.mbox`. Both contain `manifest.csv`, with one line per message: mailbox, folder, UID, UIDVALIDITY, Message-ID, date, sender, recipients, subject, size, the SHA-256 of the exported message and its file in the ZIP. When a mailbox was skipped or failed part-way, `problems.csv` lists it. The export uses the saved search, so save changes before exporting.

## Holds

A hold keeps a mailbox's mail for a case. It keeps every message, or only the messages that match the case's saved search terms and dates at the time the hold is placed (**Messages matching the search**). Placing a hold again on a mailbox the case already holds replaces what it keeps. While at least one open case holds a mailbox:

- Retention policies do not delete the messages a hold keeps. A hold that keeps every message stops retention for the mailbox altogether.
- Deleting a message, emptying Trash or Junk, deleting a folder that still holds messages, or a mail app expunging straight away only moves the message into the mailbox's recoverable items, as [Recovering deleted mail](deleted-items.md) describes. Drafts are kept there too while the mailbox is held, although they are not otherwise.
- The hourly clean-up of recoverable items does not remove the messages a hold keeps, and Dovecot's own 30-day expiry of recoverable items is turned off for the mailbox.
- The person cannot remove anything from their recoverable items, even by opening the hidden `.EXPUNGED/` folders in a mail app. Restoring a message copies it back and leaves the kept copy in place.
- Deleting the mailbox keeps it as a **retained** mailbox instead: it is deactivated, receives no mail and nobody can sign in, but its mail stays searchable and exportable. The address stays taken while the mailbox is retained. Reactivating it under **Administration → Mailboxes** restores it as a normal mailbox. Once every hold is released, deleting it again deletes it.
- The database refuses to delete a held mailbox, whatever path the deletion takes.

Dovecot reads the hold settings when a mailbox signs in, so placing a hold ends the mailbox's open IMAP sessions; mail apps reconnect on their own.

Searches and exports include the recoverable items, under folders named `.EXPUNGED/<folder>`.

A scoped hold keeps a message when it matches every term: sender, recipient (To, Cc or Bcc), subject, body, attachment name and the received dates, checked the same way as a case search. For a hold that names an attachment, retention and the clean-up first list the messages they would remove, check each one's attachment names over IMAP, and remove only those that no hold keeps. They remove them by the folder's unique ID and the message UID, and only after confirming the folder has not been replaced in the meantime. If any step fails, or the folder changed, nothing in that folder is removed and the job tries again in an hour.

The person is not told that their mailbox is on hold. Organization administrators can see holds only through the audit log and the retained state of a deleted mailbox.

Releasing the last hold on a mailbox lets retention and the clean-up of recoverable items remove its mail again on their next run.

### Jobs that delete mail

Any job that removes mail permanently must respect holds. In the API code, `holdScope(ctx, mailboxID)` loads a mailbox's holds, and `ExpungeHeld` (or `PurgeRecoverable` for recoverable items) in the mail package removes only what no hold keeps: text and date terms become `NOT ( … )` terms of the `doveadm` query, and attachment names are checked per message as described above. If the holds cannot be loaded, nothing is removed. `mailboxHeld("m.id")` is the SQL condition for any hold, and the `mail_userdb` view's `held` column gives Dovecot the per-mailbox settings above.

## Journaling

A journal rule copies every message sent to or from an address, or every address of a domain (`@example.com`), to an archive address. It applies to mail from outside, mail between people in the organization and mail sent out. Each address or domain has one archive. The archive can be any address; when it is a mailbox in Aether, place it on hold so its copies are kept.

Postfix adds the copy as a blind copy with `sender_bcc_maps` and `recipient_bcc_maps`, reading the rules from the `mail_journal` view.

## API

| Endpoint | Purpose |
| --- | --- |
| `GET`, `POST /api/v1/organizations/{orgId}/compliance/cases` | List cases, create a case with `name`, `description` and `criteria` |
| `GET`, `PATCH /api/v1/compliance/cases/{caseId}` | Read a case; change `name`, `description`, `criteria`, or `closed` |
| `POST /api/v1/compliance/cases/{caseId}/search` | Run the saved search |
| `GET /api/v1/compliance/cases/{caseId}/export?format=eml\|mbox` | Download the export |
| `GET`, `POST /api/v1/compliance/cases/{caseId}/holds` | List holds, place holds with `mailboxIds` and, to keep only matching messages, `criteria` with the same terms as a case |
| `DELETE /api/v1/compliance/cases/{caseId}/holds/{mailboxId}` | Release a hold |
| `GET /api/v1/organizations/{orgId}/compliance/mailboxes` | Every mailbox and domain of the organization, with hold state |
| `GET`, `POST /api/v1/organizations/{orgId}/journal-rules` | List journal rules, add one with `match` and `archiveAddress` |
| `DELETE /api/v1/journal-rules/{ruleId}` | Remove a journal rule |

`criteria` has `scope` (`mailboxes`, `domains` or `organization`), `mailboxIds`, `domainIds`, `from`, `to`, `subject`, `body`, `attachment`, and `since` and `until` as `YYYY-MM-DD`. Searches and exports together are limited to 20 a minute per person. `POST /api/v1/organizations/{orgId}/members` takes an optional `compliance` boolean, which only owners may change.
