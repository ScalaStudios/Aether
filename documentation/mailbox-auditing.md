# Mailbox access auditing

The audit log records configuration changes, such as adding a delegate. Mailbox access auditing records what happens inside a mailbox when someone other than its owner works in it: a delegate, a member of a shared mailbox, or an administrator. It answers questions such as "who read my mail?" and "who deleted the message from the shared mailbox?".

It is off by default. Turn it on per organization under **Administration → Audit log → Mailbox access**, or with `PUT /api/v1/organizations/{orgId}/mailbox-audit`.

## What is recorded

| Action | Recorded as | Aether Mail | Mail apps |
| --- | --- | --- | --- |
| Open a folder | `folder.open` | Yes | Yes |
| Read a message or download an attachment | `message.read` | Yes | Yes, when the app downloads the message body |
| Move or copy messages | `message.move`, `message.copy` | Yes, one entry per message | One entry per message for `UID MOVE`, `UID COPY` and `COPY`; one entry for the whole set for `MOVE` by sequence number |
| Delete a message or a draft | `message.delete` | Yes | Yes, one entry per expunged message, and a move into Trash counts as a deletion, as it does in Aether Mail |
| Empty a folder | `folder.empty` | Yes | Recorded as the individual deletions |
| Send as, send on behalf | `message.send_as`, `message.send_on_behalf` | Yes, including scheduled messages and invitation replies | `message.send_as` for mail submitted over SMTP |
| Create, rename or delete a folder | `folder.create`, `folder.rename`, `folder.delete` | Yes | Yes |
| Change mail rules, the automatic reply or signatures | `rules.update`, `auto_reply.update`, `signatures.update` | Yes | Not applicable |

Each entry has the acting person and their role (delegate, member or administrator), the mailbox, the action, the folder, the subject and Message-ID, the client and the IP address. The client is one of:

| Client | Meaning |
| --- | --- |
| Aether Mail | The web app or the API |
| App password | A mail app signed in as the mailbox with a delegate's app password |
| Access token | A mail app signed in as the mailbox with a delegate's identity provider access token |
| IMAP | A member working in `Shared/<address>/` from their own mail app login |
| SMTP | Mail submitted on port 587 or 465 from a delegated or shared address |

Each message entry has the message's subject and Message-ID where Aether can know them:

- Aether Mail and SMTP: always, from the message itself.
- IMAP reads and copies: Aether reads the subject and Message-ID of that message, and only of messages it is about to record, after removing repeated reads.
- IMAP moves and deletions: the message has already left its folder when Dovecot reports the event, so Aether takes the subject and Message-ID from an earlier entry about the same message, such as a read or a copy. A message nobody other than the owner had touched before has only its UID.

Reads are noisy: a mail app that downloads every message records a read for each one. The same person opening the same folder or reading the same message again within 15 minutes is recorded once. Reads can be left out with **Include opened folders and read messages**.

## Settings

| Setting | Default | Effect |
| --- | --- | --- |
| Record mailbox access | Off | Turns recording on for every mailbox in the organization |
| Include opened folders and read messages | On | Records `folder.open` and `message.read` |
| Include deletions by the owner | Off | Also records `message.delete` and `folder.empty` by the mailbox owner. For mail apps, it applies to sessions that start after the change |
| Let owners see who accessed their mailbox | Off | Owners find the report under **Settings → Who accessed your mailbox** |
| Keep entries for | 180 days | Entries older than this are deleted, between 1 and 3650 days |

Changing the settings is recorded in the audit log as `mailbox_audit.update`.

## The access report

Open **Administration → Mailboxes**, then **Access report** in a mailbox's menu. The report lists who other than the owner accessed the mailbox in the chosen period, with the number of actions and the last access per person, followed by every entry, newest first. Owners see the same report in Settings when the organization allows it.

### API

| Route | What it does |
| --- | --- |
| `GET`, `PUT /api/v1/organizations/{orgId}/mailbox-audit` | Reads or replaces the settings: `enabled`, `logReads`, `logOwnerDeletes`, `ownersCanView` and `retentionDays` |
| `GET /api/v1/mailboxes/{mailboxId}/access-log` | The report. `since` and `until` take dates like `2026-10-31`; `before` takes the last entry's `id` for the next page of 100 |

Organization owners and administrators can use both. The mailbox owner can read the report when `ownersCanView` is on. API keys need the `audit` scope, `audit:write` to change the settings.

## How it works

Aether Mail talks to Dovecot through a master login as the mailbox, so Dovecot cannot tell who is acting. The API therefore records those actions itself, knowing the signed-in person.

Mail sent over SMTP is recorded by a milter in the API, listening on `AETHER_AUDIT_MILTER_LISTEN` (default `:10047`), that Postfix calls after Rspamd as `AETHER_AUDIT_MILTER_SERVICE` (default `api:10047`). It receives only the envelope and the headers, never the body, records the authenticated sender, the From address, the subject and the Message-ID, and accepts every message: if the API cannot be reached, mail flows unrecorded. Postfix's Dovecot authentication passes no session to Dovecot, so a delegate who signs in to SMTP as the delegated mailbox with an app password or an access token is signed in as their own address instead. They can still send as the delegated mailbox, because Send as allows it, and the entry names them. Sending as addresses that only the delegated mailbox may use, through that mailbox's credentials, is no longer possible. Mail sent from Aether Mail is recorded by the API and skipped by the milter, so it is not recorded twice.

Mail apps are recorded from Dovecot. Dovecot exports a narrow set of IMAP events, `SELECT`, `EXAMINE`, `CREATE`, `DELETE`, `RENAME`, `COPY` and `MOVE` commands, message body fetches and expunges, to `POST /internal/mail-events` on the API, authenticated with `AETHER_DOVECOT_API_KEY` and accepted only from private addresses. To tell a delegate from the owner when both sign in as the mailbox, the login records the delegate in the `mail_sessions` table: the SQL password lookup calls the `aether_mail_login` function, and the access token check passes Dovecot's session ID to the API. Owner sessions are recorded only when owner deletions are logged, and Aether Mail's master logins are never recorded, so their events are ignored. A member working in `Shared/<address>/` is identified by their own login. Session records are kept for 90 days; actions in an IMAP session older than that are not attributed.

### Upgrading

Migration 0037 adds the `mailbox_audit_settings`, `mailbox_access_events` and `mail_sessions` tables and the `aether_mail_login` function, and grants `aether_mail` permission to run it. The API applies it at start. Dovecot's image changes its password query to call the function and writes the event exporter to `/run/dovecot-secrets/mail-audit.conf` at start, so deploy the API before Dovecot: a Dovecot that starts before the migration rejects app password logins until the API has started.

Dovecot puts `AETHER_DOVECOT_API_KEY` percent-encoded into the exporter URL, so any key that works for the doveadm API works here. With Helm, the API needs ingress on port 10047 from Postfix and on port 8080 from Dovecot; the chart's NetworkPolicy allows both.

## Limitations

- Over IMAP, a move or deletion of a message with no earlier entry has its UID but no subject or Message-ID. Dovecot 2.3 reports moves and expunges after the message has left the folder: the `mail_expunged` event carries only the UID, the `MOVE` reply carries no destination UIDs, and the `mail_log` plugin that does print subjects writes only to the log, not to events.
- Mail submitted over SMTP is always recorded as `message.send_as`; Send on behalf is not available over SMTP.
- Administrators cannot open another person's messages, so administrator entries come from rule, automatic reply and signature changes.
- Entries are kept in PostgreSQL and are not sent to webhooks.
