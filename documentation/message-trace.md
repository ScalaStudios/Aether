# Message trace

Message trace answers "they say they sent it, I never got it" and "my mail to them bounced" without shell access. It shows where each message came from, what the spam filter thought of it, and what happened to every recipient: delivered, filed in Junk, forwarded, deferred, bounced or rejected, with the reply the other server gave.

Open it under **Administration → Message trace**, or use the API.

## What you can see

Organization owners and admins see mail for their own organization:

- mail that arrived for one of the organization's domains, showing only the recipients on those domains, and
- mail sent by the organization's signed-in users, showing every recipient.

A message addressed to people in two organizations appears in both, and each sees only its own recipients and their outcomes. Mail that merely claims a sender on your domain, sent by someone who did not sign in to Aether, appears only for the organizations it was addressed to.

Platform administrators can switch to **Whole server**, which also shows mail for domains no organization owns, such as rejected attempts to deliver to unknown domains.

## Searching

| Filter | Matches |
| --- | --- |
| Sender | Part of the envelope sender, for example `example.com`. Sender Rewriting Scheme addresses (`SRS0=…`) are shown and searched as the original sender. |
| Recipient | Part of a recipient address, including the alias or list the message was sent to. |
| Subject | Words anywhere in the subject, ignoring case. |
| Message-ID | The exact Message-ID, with or without angle brackets. |
| Received | The last hour, 24 hours, 7 days, everything kept, or a custom range. |
| Status | The outcome described below. |

Results are newest first, 50 at a time. **Export CSV** downloads up to 50,000 matching messages with their status, recipients, client address, encryption, spam score and the latest server reply. Spreadsheet formulas in the export are neutralized, and each export is recorded in the audit log as `message_trace.export`.

## Statuses

When a message has several recipients, the list shows the least successful outcome among the recipients you can see. The detail view shows each recipient separately.

| Status | Meaning |
| --- | --- |
| Delivered | Stored in the mailbox, or accepted by the next server for outside addresses and forwards. |
| Delivered to Junk | Rspamd marked it as spam (`X-Spam: Yes`), so Aether's Sieve filter filed it in the recipient's Junk folder. |
| In queue | Accepted and waiting for its first delivery attempt. |
| Deferred | Delivery failed temporarily. Postfix retries for up to five days. |
| Held | Held in the Postfix queue until an administrator releases it. |
| Bounced | A recipient could not be reached, or retries ran out. The sender got a non-delivery report. |
| Rejected | Refused during the SMTP conversation, for example an unknown recipient or a spam rejection. The sending server got the reason, which is shown in the detail view. A rejection with a `4.x.x` code is temporary and the sender should retry. |
| Discarded | Accepted and dropped without delivery or report. |
| Deleted | Removed from the queue by an administrator. |

A copy that someone's mail rule forwards is a separate message with the original Message-ID. The detail view lists such copies under **Same Message-ID**. Alias expansions stay in the original message, with the alias shown above each recipient it reached.

Delivery to a folder other than Inbox or Junk by a person's own mail rules is not traced; Aether records that the message reached the mailbox.

## How it works

Postfix writes every event to `/var/log/postfix/mail.log`. Two header checks add what the log would otherwise miss: the subject of each message, and the `X-Spam-Status` and `X-Spam` headers that Rspamd adds, which carry the spam score and verdict. Only headers added by Rspamd are trusted for the score; a sender cannot fake it by writing those headers into the message.

The `mail-trace` service runs the API image with the command `aether mail-trace`. It reads the log, including rotated and compressed copies, groups the lines by Postfix queue ID and stores the result in PostgreSQL, usually within a second. It remembers its position in the database, so a restart or a log rotation loses nothing as long as the rotated file still exists; Postfix keeps 7 days. On its first start it reads every rotated log, so existing installations get the last week straight away. A database lock lets only one copy write at a time.

The log is readable only by root, so the service runs as root, with a read-only file system, no Linux capabilities and the log volume mounted read-only.

| Setting | Default | Meaning |
| --- | --- | --- |
| `AETHER_MAIL_TRACE_RETENTION_DAYS` | `30` | Days to keep records. Older records are deleted every hour. |
| `AETHER_MAIL_LOG` | `/var/log/postfix/mail.log` | The Postfix log to read. |

With Helm, the service runs as a `mail-trace` container next to Postfix, sharing the log through an `emptyDir` volume, and `mailTrace.retentionDays` sets the retention.

If the page warns that recent mail may be missing, the log has not been read for more than 10 minutes. Check the service with `docker compose ps mail-trace` and `docker compose logs mail-trace`.

### Privacy

Subjects now appear in the Postfix log and therefore in `docker compose logs postfix`. Message trace stores addresses, subjects, client IP addresses and server replies, never message bodies. Records are deleted after the retention period.

### Times

Postfix writes local time without a year or time zone. The service reads it as UTC, which is the default in the Postfix container. If you set `TZ` on the Postfix container, times in message trace are off by that offset.

## API

| Method and path | Purpose |
| --- | --- |
| `GET /api/v1/organizations/{orgId}/message-trace` | Search, with the query parameters `sender`, `recipient`, `subject`, `messageId`, `status`, `from`, `to` (RFC 3339) and `before` for the next page |
| `GET /api/v1/organizations/{orgId}/message-trace/export` | The same search as CSV |
| `GET /api/v1/organizations/{orgId}/message-trace/{id}` | One message with its events, per-recipient status and related copies |
| `GET /api/v1/admin/message-trace`, `/export`, `/{id}` | The same for the whole server, platform administrators only |

API keys need the `message-trace:read` scope. See [API keys and webhooks](integrations.md) and `api/openapi.yaml`.
