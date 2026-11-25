# Quarantine

Mail that Rspamd would reject, mail that scores 10 or more, and mail in which ClamAV finds a virus is held in a quarantine that Aether stores in PostgreSQL. The sender's server gets a normal `250` reply, nothing reaches the mailbox, and the recipient or an administrator can release the message later. Mail that scores from 6 to just under 10 still goes to Junk.

| Category | When | Default access |
| --- | --- | --- |
| Spam | A score of 10 or more | People release it |
| High-confidence spam | Rspamd's action is reject: a score of 15 or more, or a rule that rejects outright | People request release |
| Phishing | Any Rspamd symbol whose name starts with `PHISH`, such as `PHISHING` or `PHISHED_OPENPHISH` | People request release |
| Malware | ClamAV's `CLAM_VIRUS` symbol | Administrators only |

A message matching more than one category takes the most severe one, in the order malware, phishing, high-confidence spam, spam.

A message refused by the organization's [attachment policy](installation.md#attachment-filtering) is still rejected during the SMTP session and is not quarantined.

## For people

**Quarantine** appears under the folders of the mailbox you are looking at in Aether Mail, with the number of messages held for it. Messages held as categories you may not see are not listed.

- **Preview.** The preview never loads remote content, removes every link, and lists attachments without letting you open them. It says why the message was held, its spam score, and when it will be deleted, and **Spam filter signals** lists the Rspamd symbols that fired.
- **Release to Inbox** puts the original message in the Inbox. Releasing phishing or malware asks for confirmation first.
- **Release and allow sender** also adds the sender's address to the mailbox's trusted senders, the same list as **Settings → Blocked and trusted senders**. Later mail from that address, or from a domain on that list, that would be held as spam or high-confidence spam is delivered to the Inbox instead, but only while your organization lets people release that category, and never when the message fails the sender's DMARC policy. Phishing and malware are always held. The person icon in the quarantine header lists the allowed senders and removes them.
- **Request release** appears instead when your organization only lets people ask. An administrator sees the request under **Requested**.
- **Delete** removes the message from quarantine for good. The sender is not told.

Delegates and members with full access can do the same for the mailboxes they open. Read-only access shows the quarantine but no actions.

### Digests

Each mailbox with newly held messages it can see gets a digest in its Inbox, at most once a day or once a week as the organization chooses. It lists up to 50 messages with links to review each one. The links open Aether Mail, which asks you to sign in; nothing is released from the email itself. The digest is placed directly in the mailbox, so it is never scanned or held itself.

## For administrators

**Administration → Quarantine** lists every message held for the organization's mailboxes, newest first, with the mailbox, the address it was sent to when that was an alias or list, the sender, the category and the status. Search covers the sender, subject, mailbox and recipient. Select messages to release or delete them in bulk; releasing phishing or malware, and every deletion, asks for confirmation. Released messages stay listed under **Released**, with who released them, until they expire.

The tab is shown to owners and administrators of the organization. Platform administrators who are not members of it cannot review its quarantine, because it contains mail.

**Policy** sets:

| Setting | Values | Default |
| --- | --- | --- |
| Access per category | People release it, People request release, Administrators only | As in the table above |
| Keep held mail for | 1 to 90 days | 30 days |
| Digest emails | Daily, Weekly, Off | Daily |

A policy change applies to messages already held. Retention counts from the moment a message was held.

Every release, deletion, release request, allowed sender added or removed, and policy change is recorded in the audit log as `quarantine.release`, `quarantine.delete`, `quarantine.request_release`, `quarantine.allow_sender`, `quarantine.allowed_sender.remove` and `quarantine.policy.update`, and sent to the organization's webhooks.

## How it works

1. Rspamd scores the message during the SMTP session as before. A postfilter named `AETHER_QUARANTINE`, which runs after greylisting, looks at the final action. It skips mail from authenticated senders, including forwards from Sieve, and mail that is greylisted or deferred.
2. When the action is reject, or add header with a score of 10 or more, it posts the message to the API's `/internal/quarantine` with the score, the symbols and the envelope in an `X-Aether-Scan` header. Like `/internal/mail-token`, the endpoint answers only callers on private networks, and Caddy does not route `/internal/`.
3. The API expands the recipients the way Postfix does, through aliases, lists, `+` addresses and catch-alls, and stores one copy of the message with one entry per mailbox. Outside addresses on a list get nothing. When every recipient is outside, the API answers `204` and Rspamd keeps its original action.
4. On `201`, Rspamd switches the action to `discard`, which it is told about in `local.d/actions.conf`, and Postfix accepts the message and drops it.

If the API cannot be reached, answers with an error, or the message is over 25 MB, Rspamd keeps its own action: mail that would have been rejected is rejected with a bounce, and mail in the 10 to 15 range goes to Junk. Nothing is lost because the quarantine is down.

Releasing appends the original message to the mailbox's Inbox over IMAP with the master user, so it does not pass the spam filter or the mailbox's Sieve rules again. A background job on every API replica deletes messages older than their organization's retention and the stored copies nobody refers to any more, and sends digests, claiming each mailbox's pending messages with `FOR UPDATE SKIP LOCKED` so that two replicas never send the same digest.

To change the score that starts quarantining, edit `quarantine_score` in `infrastructure/docker/rspamd/rspamd.local.lua`; the reject threshold stays in `local.d/actions.conf`.

## API

These routes need a signed-in session. API keys cannot use them, because they read mail.

| Route | What it does |
| --- | --- |
| `GET /api/v1/mailboxes/{mailboxId}/quarantine` | Messages held for the mailbox that the caller may see, 50 a page with `before` set to the last `nextCursor`, the total and the retention in days |
| `GET`, `DELETE /api/v1/mailboxes/{mailboxId}/quarantine/{id}` | The safe preview with the category, score and symbols; deleting needs full access |
| `POST /api/v1/mailboxes/{mailboxId}/quarantine/{id}/release`, `/request` and `/allow-sender` | Release, ask for release, or release and allow the sender, as the organization's policy permits |
| `GET`, `DELETE /api/v1/mailboxes/{mailboxId}/allowed-senders` | The trusted addresses in the mailbox's sender list; delete one with `?sender=` |
| `GET /api/v1/organizations/{orgId}/quarantine` | The organization's quarantine for owners and administrators, filtered by `status` (`held`, `requested` or `released`), `category` and `q`, with the number of open requests |
| `GET /api/v1/organizations/{orgId}/quarantine/{id}` | The preview of any message held for the organization |
| `POST /api/v1/organizations/{orgId}/quarantine/release` and `/delete` | Release or delete up to 100 messages given as `{"ids": [...]}` |
| `GET`, `PUT /api/v1/organizations/{orgId}/quarantine-policy` | `{"access": {"spam": "release", "high_confidence_spam": "request", "phishing": "request", "malware": "none"}, "retentionDays": 30, "digest": "daily"}` |

## Upgrading

Migration `0027_quarantine` adds the tables. Mail that scored 15 or more used to be rejected and mail from 10 to 15 used to go to Junk; after the upgrade both are held instead. Rebuild or pull the `rspamd` image along with the API.

On Kubernetes, the chart's network policy now lets the Rspamd pod reach the API on port 8080.
