# API keys, webhooks, and suppressions

Organization administrators can automate Aether with API keys, and receive changes through webhooks. Both are managed per organization.

## API keys

Create a key with `POST /api/v1/organizations/{orgId}/api-keys` while signed in. The response contains the token once; Aether stores only its SHA-256 hash.

```sh
curl -H "Authorization: Bearer aether_..." https://mail.example.com/api/v1/organizations/{orgId}/mailboxes
```

A key belongs to one organization and acts as an administrator of that organization only. Each request also needs the matching scope:

| Scope | Allows |
| --- | --- |
| `domains:read`, `domains:write` | Listing, adding, verifying, and removing domains, and DNS status |
| `mailboxes:read`, `mailboxes:write` | Listing, creating, changing, converting, and removing mailboxes, and managing their delegates or members, mail rules, automatic replies, signatures, calendar and contacts imports, and recurring mail imports (see below) |
| `aliases:read`, `aliases:write` | Listing, creating, changing, and removing aliases and distribution lists |
| `members:read`, `members:write` | Listing members, changing their roles, and removing members. A key cannot make anyone an owner. |
| `audit:read`, `audit:write` | Reading the audit log and mailbox access reports, and changing the [mailbox access auditing](mailbox-auditing.md) settings |
| `message-trace:read` | Searching and exporting the [message trace](message-trace.md) |
| `webhooks:read`, `webhooks:write` | Managing webhooks and reading deliveries |
| `mailflow:read`, `mailflow:write` | Listing and changing [mail flow rules](mail-flow-rules.md), reading their recent matches and held messages, and approving or rejecting held messages |
| `suppressions:read`, `suppressions:write` | Listing, adding, and removing suppressed addresses |

A `write` scope includes `read`. Keys cannot read mail, export a mailbox, use the platform administration endpoints, or create other keys. Requests made with a key do not need a CSRF token. Each key may make 600 requests per minute. Actions taken with a key appear in the audit log under the key's name.

To revoke a key, use `DELETE /api/v1/api-keys/{keyId}`. Revocation takes effect immediately.

Removing a member with `DELETE /api/v1/organizations/{orgId}/members/{userId}` also removes their delegated mailbox access and list ownership in that organization. Only an owner can remove another owner, and the last owner cannot be removed.

## Setting up mailboxes for other people

Organization administrators, signed in or using a key, can prepare a mailbox on its owner's behalf. These routes accept any active mailbox on a verified domain of the organization:

| Route | What it does |
| --- | --- |
| `GET`, `POST /api/v1/mailboxes/{mailboxId}/delegates` and `DELETE /api/v1/mailboxes/{mailboxId}/delegates/{userId}` | Delegates of a user mailbox, or members of a shared one, with `access`, `sendAs` and `sendOnBehalf` |
| `POST /api/v1/mailboxes/{mailboxId}/convert` | Converts a mailbox between `user` and `shared`, as [Shared mailboxes](shared-mailboxes.md#converting-a-mailbox) describes |
| `GET`, `PUT /api/v1/mailboxes/{mailboxId}/rules` and `/auto-reply` | Mail rules and automatic replies, compiled to Sieve as when the owner changes them |
| `GET`, `PUT /api/v1/mailboxes/{mailboxId}/signatures` | Signatures |
| `POST /api/v1/mailboxes/{mailboxId}/calendar/import` | An iCalendar file into the owner's personal calendar |
| `POST /api/v1/mailboxes/{mailboxId}/contacts/import` | A vCard file into the owner's address book. Group cards in the file become contact groups. `book` chooses another address book of that owner; without it, the file goes into their main address book. |
| `GET`, `PUT /api/v1/organizations/{orgId}/forwarding` | The organization's forwarding policy and every rule that forwards outside it, as [Architecture](architecture.md#forwarding-to-outside-addresses) describes |

An import takes a file of up to 10 MB with at most 5,000 events or contacts, answers with how many were imported, updated and skipped, and fails with `no_owner` when the mailbox has no owner. Delegate and member changes, conversions, rule, automatic reply and signature changes, and imports appear in the audit log with the administrator or the key as the actor.

## Recurring mail imports

During a migration, mail keeps arriving at the old provider until the MX record changes and DNS caches expire. A recurring import copies new mail from the old IMAP account every few minutes until it is stopped or reaches its end time. Administrators schedule them under **Administration → Mail imports**, and mailbox owners under **Settings → Import mail** by choosing a repeat interval.

| Route | What it does |
| --- | --- |
| `GET`, `POST /api/v1/organizations/{orgId}/import-schedules` | Lists the organization's schedules with their latest run, or creates up to 100 at once in `imports` |
| `POST /api/v1/organizations/{orgId}/import-schedules/stop` | Stops up to 100 schedules listed in `ids` |
| `GET /api/v1/organizations/{orgId}/import-schedules/{id}/runs` | The 20 most recent runs of one schedule |
| `GET`, `POST /api/v1/mailboxes/{mailboxId}/import-schedules` and `DELETE /api/v1/mailboxes/{mailboxId}/import-schedules/{id}` | The same for one mailbox, for its owner and delegates; read-only delegates can only list |

Each schedule takes the source `host`, `port`, `security` (`tls` or `starttls`), `username` and `password`, an `intervalMinutes` from 1 to 10080, an optional future `endsAt`, and two options that default to off:

- `syncFlags` applies the source's read, answered, flagged and draft flags to messages this schedule copied.
- `syncDeletions` removes copies made by this schedule when the source message is deleted or expunged from the same folder. It needs a destination with `UIDPLUS`, which Dovecot has.

Every run is incremental: messages already in the destination folder are skipped by Message-ID, or by content when they have none. Each run reports the messages copied, skipped, updated and deleted, and a failed run is retried at the next interval. A run stops after 24 hours or at the end time. The source password is stored encrypted and erased when the schedule is stopped, reaches its end time, or loses its mailbox, its domain or, for one created by a mailbox owner, that person's access. Each API process runs at most four imports at a time, and only one import runs for a mailbox at once. Scheduling and stopping appear in the audit log as `mail_import.schedule` and `mail_import.stop`. API keys need `mailboxes:read` to list and `mailboxes:write` to schedule or stop.

## Exporting and deleting a mailbox

A signed-in person downloads their own mailbox from **Settings → Export mailbox**. An organization administrator does the same for any mailbox in the organization from **Administration → Mailboxes**. Every export is an audit event `mailbox.export`. The request is `POST /api/v1/mailboxes/{mailboxId}/exports` with `format` `zip` (a ZIP of `.eml` files) or `mbox`, an optional `folder`, optional `since` and `until`, and `includeCalendar` and `includeContacts`. Calendars are ICS files and contacts are vCard files for the mailbox owner; a shared mailbox has no owner, so those files are omitted. The export runs in the background. `GET /api/v1/mailboxes/{mailboxId}/exports` lists it, and `GET .../exports/{exportId}/download` returns the file once `status` is `ready`. The link expires seven days after the file is ready (`410 export_expired`). API keys cannot call these routes.

`DELETE /api/v1/mailboxes/{mailboxId}` answers `204` when `graceDays` is omitted or `0`: the mailbox row is removed and its mail is deleted from the vmail volume. `graceDays` from 1 to 3650 answers `200` with `purgeAfter` set; the mailbox is closed and can be brought back with `POST /api/v1/mailboxes/{mailboxId}/restore` until that time. `onHold: true` (`PATCH` the mailbox) makes deletion close the mailbox and keep the mail. Activating a mailbox again also cancels a scheduled deletion.

## Webhooks

A webhook receives audit and mail delivery events for its organization that match its event list. Use `*` to receive all events. The audit log on the console shows the event names.

Aether sends each event as a JSON `POST`:

```json
{
  "id": "1234",
  "type": "mailbox.create",
  "createdAt": "2026-10-15T08:00:00Z",
  "organizationId": "…",
  "actor": { "userId": "…", "apiKeyId": null },
  "target": { "type": "mailbox", "id": "…" },
  "data": { "address": "alice@example.com" }
}
```

Events the server raises on its own have a null actor. `mailbox.sending_restricted` and `mailbox.sending_limit_near` carry `address`, `limit` (such as `messagesPerHour`), `count` and `max` in `data`. See [Sending limits and restricted senders](architecture.md#sending-limits-and-restricted-senders).

The request carries these headers:

- `Aether-Event`: the event name
- `Aether-Delivery`: a unique delivery ID. Use it to ignore duplicates.
- `Aether-Signature`: `t=<unix time>,v1=<signature>`. The signature is the hex HMAC-SHA256 of `t`, a dot, and the raw body, keyed with the webhook secret.

Check the signature, and reject timestamps more than five minutes old:

```js
const [t, v1] = header.split(",").map((p) => p.split("=")[1])
const expected = crypto.createHmac("sha256", secret).update(`${t}.${rawBody}`).digest("hex")
const valid = crypto.timingSafeEqual(Buffer.from(expected), Buffer.from(v1)) && Date.now() / 1000 - Number(t) < 300
```

Any 2xx response counts as delivered. Otherwise, Aether retries after 1 minute, then 5 minutes, 30 minutes, 2 hours, 6 hours, 12 hours, and 24 hours. After eight attempts, the delivery is marked failed. Delivery takes up to 15 seconds after the event. `GET /api/v1/webhooks/{id}/deliveries` lists the last 50 deliveries, and `POST /api/v1/webhooks/{id}/test` sends a `webhook.test` event.

Webhook URLs must use `https`. Aether refuses to connect to private, loopback, link-local, and other reserved addresses, and it does not follow redirects. This prevents a webhook from reaching services inside your network. For development only, `AETHER_WEBHOOK_ALLOW_PRIVATE=true` lifts both restrictions.

### Mail delivery events

| Event | When it is emitted |
| --- | --- |
| `message.delivered` | A recipient's mail server or a local mailbox accepts an authenticated submission |
| `message.deferred` | A delivery attempt for an authenticated submission fails temporarily and Postfix holds the message for retry |
| `message.bounced` | Delivery for an authenticated submission fails permanently or its retries expire |
| `message.received` | A hosted mailbox accepts the message through LMTP |
| `message.rejected` | Postfix rejects incoming mail for a hosted recipient during SMTP |

Authenticated submission events go to the sender's organization. Received and rejected events go to the recipient's organization. A local delivery of an authenticated submission can emit both `message.delivered` and `message.received`.

Mail events use the same signing and retry rules as audit events. The envelope includes `id`, `type`, `createdAt`, `organizationId`, and `data`. In `data`, `queueId` and `messageId` identify the message when available. `from`, `to`, and `originalTo` contain envelope addresses. `relay`, `dsn`, and `reply` contain the Postfix relay, enhanced status code, and SMTP reply. Unavailable fields are empty strings. For `message.bounced`, `suppressed` is true when that event added a suppression.

Compose and Helm run the Postfix log follower automatically. For a custom installation, run `aether mail-events` alongside the API with `AETHER_DATABASE_URL` and a readable Postfix log at `/var/log/postfix/mail.log` or the path set in `AETHER_MAIL_LOG`. The follower checkpoints its position and queued message metadata in PostgreSQL. On first start it processes the current log from the beginning.

## Suppression list

In **Administration → Integrations → Suppression list**, organization administrators can search, add, and remove addresses. Record a reported complaint by adding the address with reason `complaint`. Aether automatically adds recipients of hard bounces with DSN `5.1.1`, `5.1.2`, `5.1.3`, `5.1.6`, or `5.2.1`. A temporary failure or an expired retry does not add a suppression.

For authenticated SMTP submission, Aether checks the envelope sender's organization and rejects a suppressed recipient at `RCPT TO` with `550 5.7.1`. Remove the address to allow new submissions to it. The list is scoped per organization.

| Route | Behavior |
| --- | --- |
| `GET /api/v1/organizations/{orgId}/suppressions` | List up to 200 entries, newest first. Optional `q` matches part of the address without regard to case. |
| `POST /api/v1/organizations/{orgId}/suppressions` | Add an `address` with optional `reason` (`manual`, `complaint`, or `bounce`) and `detail` (up to 500 characters). Defaults to `manual`. A duplicate address returns `409 already_suppressed`. |
| `DELETE /api/v1/suppressions/{suppressionId}` | Remove an entry. Returns `204`. |

Manual changes appear as `suppression.create` and `suppression.delete` audit events.
