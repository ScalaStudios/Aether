# Shared mailboxes

A shared mailbox is an address that a team reads together, such as `support@example.com`. Nobody signs in as it. Administrators add members, and each member opens it from their own account, in Aether Mail and in mail apps.

| | Your own mailbox | A delegated mailbox | A shared mailbox |
| --- | --- | --- | --- |
| Who signs in with the address | You | Its owner | Nobody |
| How others get access | An administrator adds delegates | An administrator adds delegates | An administrator adds members |
| In Aether Mail | Your folder tree | Chosen from the mailbox picker | Listed under your own mailbox |
| In mail apps | Its own app password | An app password, for full delegates with Send as | `Shared/<address>/` under your own account |

## Using a shared mailbox

Every shared mailbox you belong to appears in the sidebar below your own folders, with its unread count and its own folder menus. Delegated mailboxes of other people keep the mailbox picker at the top of the sidebar.

Members have one of two access levels:

| Access | What it allows |
| --- | --- |
| Full access | Read, organize and file mail, and manage folders |
| Read only | Read mail. Moving, deleting, flagging and drafts are refused |

Sending is a separate right, so full access on its own does not let a member send. There are two sending rights, and both put the shared address in From:

| Right | From | Sender header |
| --- | --- | --- |
| Send as | The shared address | None |
| Send on behalf | The shared address | Your own account address, so recipients see that you sent it |

A member who has both sends as the shared address. The composer lists every mailbox you may send from in its From menu, marking on-behalf identities. Sent copies land in the shared mailbox's Sent folder, not in yours, and so do scheduled messages. A scheduled message records the right it was scheduled with, Send as or Send on behalf, and is checked again when its time comes: it is sent only if your account is still active and you still hold that same right. Otherwise it is not sent and shows an error in the schedule; cancel it to get the draft back and schedule it again. Scheduling from a read-only membership leaves the draft in place, and a scheduled reply marks the original message as answered only if the sender still has full access when it goes out.

Replying to a calendar invitation that arrived in the shared mailbox needs a sending right too, because the reply goes out from the shared address; with Send on behalf only, the reply carries your address in Sender. An automatic reply is sent as the shared address with no Sender, so members can see or change it only with Send as, and changing it also needs full access. Organization administrators always can. Anyone else gets `403 cannot_send`.

### In mail apps

Sign in to your mail app as yourself, with your own app password or OpenID Connect access token. The shared mailbox appears as a folder named `Shared/<address>/` with the shared Inbox, Sent, Drafts and other folders beneath it. Dovecot enforces the access level on each folder, so a read-only member cannot move, delete or flag messages there either.

Members with Send as can send from the shared address through SMTP with their own login, which works from any user mailbox they own. Send on behalf is not available over SMTP yet, only in Aether Mail. A shared mailbox has no app passwords and cannot be the username of a mail app.

## Administering shared mailboxes

Shared mailboxes are managed under **Administration → Mailboxes**, or with the API using a key that has the `mailboxes:write` scope.

### Creating one

Choose **Shared mailbox** as the kind when creating the mailbox, and leave the password empty. No account is created and nothing can sign in with the address. If an account already signs in with that address, creation is refused with `address_is_account`: convert that person's mailbox instead, or choose another address. Identity providers cannot create an account with a shared address either: an OpenID Connect sign-in whose verified email is a shared address is rejected, and SCIM `POST` and `PUT /Users` with such an address answer `400 invalidValue`.

### Members and rights

Open **Members** on the mailbox to add a person by email. They must be an active member of the organization or own a mailbox in it. An account's email address may not contain `*` or `?`: SCIM answers `400 invalidValue` for such a `userName`, and single sign-on refuses to provision such an address. Each member has an access level, full or read only, and the Send as and Send on behalf switches, all of which can be changed in place. New members start with no sending rights.

Changing a member's rights takes effect at once in Aether Mail, SMTP and app-password checks, and the member's open IMAP sessions are ended when a right is lowered. Removing a member does the same: web, SMTP and app-password access end together, and their IMAP sessions are closed. Sessions opened with an identity provider access token end on the same changes as app-password sessions.

Removing a person from the organization removes every membership and delegation they had in it: their app passwords for delegated mailboxes are revoked, their IMAP sessions are ended, including sessions opened for delegated mailboxes with such app passwords or with an access token, and the audit log records one `mailbox.delegate_remove` event per mailbox next to `member.remove`. Adding the person again later does not bring those app passwords back.

Suspending an account ends its IMAP sessions, including sessions opened for delegated mailboxes with an app password or an access token, and removes it from the shared folders in Dovecot. Restoring the account brings its shared folders back, and its app passwords for delegated mailboxes work again. Revoking an app password under **Settings → App passwords** ends the IMAP sessions of the mailbox it was issued for, including the owner's.

The same dialog manages delegates of a user mailbox, labelled **Delegates** there. Existing delegates were migrated when the feature arrived: delegates with full access received Send as, and read-only delegates did not, which matches what each could already do, so nobody lost or gained a right.

### Converting a mailbox

**Convert to shared mailbox…** turns a user mailbox into a shared one, and **Convert to user mailbox…** turns it back. Mail stays in place either way. The conversion is refused when the owner is a platform administrator.

Converting a user mailbox to shared:

- Ownership is cleared, and every app password for the mailbox is revoked, whoever created it.
- When the owner account's email is the mailbox address, that account is deprovisioned, its web sessions are deleted and every app password it holds is revoked, including those for delegated mailboxes. Its password, identities, delegations and organization memberships are kept. An owner whose account signs in with a different address only loses ownership, and their open IMAP sessions end.
- Nothing reactivates that account while the mailbox is shared: an administrator's restore answers `409 mailbox_converted`, and SCIM `active` changes and identity provider suspend or restore events are accepted but change nothing. Only converting the mailbox back restores it.
- Existing delegates become members with the rights they had.
- Every IMAP session the former owner held ends, including sessions opened as a delegate of other mailboxes with an app password or an access token, and their other shared memberships are reconciled so that a deprovisioned account no longer appears in them.

Converting a shared mailbox back to a user mailbox:

- An account that the earlier conversion deprovisioned is restored, made the owner again and added to the organization as a member. Any membership it had on the mailbox is removed.
- Members keep their rights as delegates, but the `Shared/<address>/` folder disappears from their mail apps and their IMAP sessions are ended.
- App passwords revoked by the earlier conversion do not come back; the person creates new ones. Delegations the account still has work again, including access-token sign-in to those mailboxes, and its shared memberships are restored.

### Deactivating and deleting

Deactivating a shared mailbox removes every member's rights from its folders in Dovecot first. If Dovecot cannot be reached, the request answers `503 mail_unavailable` and nothing changes, so try again later. The same answer comes when the rights cannot be removed within 60 seconds, for example while another process is reconciling the mailbox or Dovecot is slow; a mailbox with several thousand folders may need more than one attempt. Only once the rights are removed is the mailbox closed and are the members' IMAP sessions ended. Deleting a shared mailbox works the same way. Member changes made while it is inactive are recorded and applied when it is reactivated. Reactivating marks the members for reconciliation, writes their rights to Dovecot and answers 200: in the normal case members regain access at once, and when Dovecot cannot be reached at that moment the background worker finishes the reconciliation, with the `aether_mail_acl_backlog_seconds` gauge showing how long it has been waiting. Dovecot ends sessions per account, so a member's own personal sessions end as well, as they do on a membership change. Deactivating or deleting a user mailbox ends the sessions signed in as its address, the owner's and delegates' alike. A domain cannot be deleted while any mailbox, active or not, still belongs to it: `DELETE /api/v1/domains/{domainId}` answers `409 domain_not_empty` and changes nothing. Delete the mailboxes first; each deletion removes its members' rights and ends their sessions as described above. Aliases are deleted with the domain.

### API

| Route | What it does |
| --- | --- |
| `POST /api/v1/organizations/{orgId}/mailboxes` with `kind: "shared"` | Creates a shared mailbox. `password` must be empty |
| `POST /api/v1/mailboxes/{mailboxId}/convert` with `kind: "shared"` or `"user"` | Converts a mailbox |
| `GET`, `POST /api/v1/mailboxes/{mailboxId}/delegates` | Lists members, or adds one or changes their rights with `email`, `access` (`full` or `read`), `sendAs` and `sendOnBehalf`. Omitted booleans keep their current value |
| `DELETE /api/v1/mailboxes/{mailboxId}/delegates/{userId}` | Removes a member |
| `GET /api/v1/me/mailboxes` | Every mailbox the caller may open, with `kind`, `access`, `sendAs` and `sendOnBehalf` |

API keys need the `mailboxes` scope. The changes appear in the audit log as `mailbox.create` with the kind, `mailbox.convert`, `mailbox.delegate_add` with the rights, and `mailbox.delegate_remove`.

## Operating shared mailboxes

Aether keeps membership in PostgreSQL and copies it to Dovecot as per-folder ACLs, so that mail apps see the right folders with the right permissions. The Dovecot image runs the ACL plugin, a shared namespace with the prefix `Shared/`, and the doveadm HTTP API on port 8089, which the API uses to end a member's IMAP sessions. ACLs are written over IMAP after each membership change. A sync lists the mailbox's folders, writes each one in name order, then lists again in a new IMAP session and repeats until a pass finds the same folder list, at most five passes, so that folders a member creates during the change are covered too. If Dovecot cannot be reached or the folder list keeps changing, the mailbox stays marked pending for a background worker. The worker claims the pending mailbox that has been due longest, one at a time, and never waits for a mailbox another process holds; a failed reconciliation is retried after 15 seconds, then 1 minute, then every 5 minutes, and any change to the mailbox's members, kind or active state makes it due at once again. A change that lands while the mailbox is being reconciled keeps it pending and sends it behind every mailbox already waiting, so a mailbox that changes constantly cannot delay the others. Each sync runs under a two-minute lease recorded on the mailbox row and renewed while it works, never inside a database transaction, so a second change to the same mailbox waits for the lease instead of a row lock, and the worker on any replica takes a pending mailbox over once a dead process's lease has run out. Each IMAP command of a reconciliation is given 30 seconds to be written and answered, counted from before the lease renewal that precedes every write, so a process that was paused for longer cannot send a stale command when it resumes, and a process that finds its lease taken over stops, closes its connection and marks the mailbox for another pass; the only state it can leave behind is a command Dovecot had received but not run when the connection was reset, which the next change to that mailbox corrects. A change made through the API waits at most 60 seconds for the mailbox's lease and for Dovecot; after that the rights are written by the worker instead.

Ending a member's IMAP sessions goes through the doveadm API. Each required kick is recorded in the `mail_kicks` table, one row per account address, in the same transaction as the change that requires it. A change that arrives while a kick for the same address is in flight is recorded on that row and delivered again, so no revocation is lost. Every API replica runs two background workers, one that reconciles rights and one that delivers kicks; they run at start and every 15 seconds, the kick worker also as soon as a change queues a kick, and neither can be switched off. A kick that Dovecot does not confirm is retried after 15 seconds, then 1 minute, then every 5 minutes, without limit. Two gauges report the backlog, both 0 when nothing is pending: `aether_mail_kick_backlog_seconds` is the age in seconds of the oldest kick still waiting, and `aether_mail_acl_backlog_seconds` the age of the oldest active mailbox still waiting for a successful reconciliation. Alert when either stays above 300 for 5 minutes. A growing kick backlog means the doveadm key or URL is wrong or Dovecot is down; a growing reconciliation backlog means the master IMAP login fails, Dovecot does not answer a command within 30 seconds, or one mailbox's folder list never settles, and the API log names the mailbox.

### Upgrading an existing installation

1. Add the new secret to `.env` before pulling up the new version. `scripts/setup.sh` generates it for new installations, but Compose refuses to start without it:

   ```sh
   echo "AETHER_DOVECOT_API_KEY=$(openssl rand -hex 24)" >> .env
   ```

   With Helm, the chart generates `dovecotApiKey` in its Secret, or takes `secrets.dovecotApiKey`.

2. Check that no mailbox has a top-level folder named `Shared`. After the upgrade, that name belongs to the shared namespace, and such a folder becomes unreachable in mail apps. Run this before upgrading, and have the owner rename any folder it lists:

   ```sh
   docker compose exec dovecot doveadm mailbox list -A 'Shared*'
   ```

3. Upgrade as usual. Dovecot restarts with the new configuration, which disconnects IMAP clients for a moment; they reconnect by themselves.

4. Messages that were scheduled before the upgrade are marked with an error by migration 0023 and are not sent. Their senders cancel them to get the draft back and schedule them again.

5. Migration 0024 adds the `mail_kicks` table for session kicks waiting on Dovecot. The API applies it at start; nothing to do.

6. Migration 0025 adds the reconciliation lease and generation columns to `mailboxes`, makes `mail_kicks` one row per address, and refuses to apply while any account email contains `*` or `?`, because Dovecot treats those characters as wildcards when ending sessions. Run this before upgrading and fix any row it lists; otherwise the API does not start:

   ```sh
   docker compose exec -T postgres psql -U aether -d aether -c "SELECT email FROM users WHERE email ~ '[*?]'"
   ```

7. Migration 0026 adds the attempt count and next-attempt time that schedule the reconciliation worker to `mailboxes`, and switches its foreign keys to `domains` and `organizations` from cascade to restrict: a domain that still has a mailbox, active or not, answers `409 domain_not_empty` on deletion, and a deletion by SQL of such a domain or organization fails. An installation that deletes domains by SQL must delete the mailboxes through the API first.

Port 8089 must never be published. The key allows any doveadm command, including reading mail. The Compose file does not map the port, and the Helm chart's NetworkPolicy admits it only from the API pods. `AETHER_DOVECOT_API_URL` (default `http://dovecot:8089`) tells the API where to find it.

## Limitations

- Members cannot come from identity provider groups; each one is added by hand or through the API.
- A shared mailbox has no calendar or address book, and no CalDAV or CardDAV access.
- No Outlook auto-mapping. Outlook shows the shared folders under the member's IMAP account, as every other client does.
- Send on behalf works only in Aether Mail, not over SMTP.
- Messages that members append over IMAP, such as copies dragged in from a mail app, are not counted against the shared mailbox's quota. Delivery and moves made in Aether Mail are.
