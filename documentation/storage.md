# Storage and quota warnings

A mailbox can have a quota, set when it is created or later with **Change quota…** in **Administration → Mailboxes**. Dovecot enforces it: once a mailbox is full, new messages to it are refused and the sending server returns them to their senders. The message that crosses the limit is still accepted if it brings the mailbox no more than 5% over.

Aether shows how full each mailbox is and warns people before that happens.

## In Aether Mail

- **Under the folder list**, your own mailbox shows how much of its quota is used. It links to **Settings → Storage**.
- **A banner above the mail** appears when the mailbox you are reading passes your organization's first warning level. It turns red at the final level and when the mailbox is full. You can close it until the next time you open Mail, except while the mailbox is full.
- **Settings → Storage** lists every mailbox you can open, including shared mailboxes you are a member of, with the size, message count and share of each folder, largest first. Trash and Junk have an **Empty** button for people who can change the mailbox.

The sizes are the ones Dovecot counts against the quota, so the folders always add up to the mailbox's usage.

## Warning emails

Aether measures every active mailbox every 5 minutes. When a mailbox with a quota passes one of its organization's warning levels, Aether puts a warning into that mailbox's Inbox. It comes from `no-reply@` the mail host name, under the organization's branding name or "Aether", and says how full the mailbox is, which folders are largest, and links to **Settings → Storage**. Members of a shared mailbox see its warnings in the shared Inbox.

- Each level is sent once. It is sent again only after the mailbox has dropped more than 5 percentage points below that level and passed it again.
- A mailbox that jumps past both levels between two checks gets only the final warning.
- Warnings are delivered straight into the mailbox, not sent over SMTP, so spam filtering, mail rules and forwarding do not apply to them.
- A mailbox that is already full cannot take the warning. Aether logs that, and the copies to administrators still go out.

With **Email administrators too** turned on, the organization's owners and administrators get a copy in their own mailbox, the one that matches their sign-in address when they have several. Owners and administrators without a mailbox in Aether get no copy and see the sizes in **Administration → Mailboxes** instead.

## Administering

- The **Storage** column in **Administration → Mailboxes** shows each mailbox's usage as of the last measurement, and its quota.
- **Storage warnings** sets one or two levels between 50% and 99% for the whole organization, and whether administrators get copies. Clear both levels to turn the warnings off; the banner in Aether Mail then appears only once a mailbox is full. New organizations start at 80% and 95%, without copies to administrators.
- Every change to the levels is recorded in the audit log as `organization.quota_warnings.update`.

## API

| Endpoint | What it does |
| --- | --- |
| `GET /api/v1/organizations/{orgId}/quota-warnings` | The organization's warning levels and whether administrators get copies |
| `PUT /api/v1/organizations/{orgId}/quota-warnings` | Changes them: `{"percents": [80, 95], "notifyAdmins": true}` |
| `GET /api/v1/mailboxes/{mailboxId}/folders` | Each folder now has `size`, in bytes |
| `GET /api/v1/organizations/{orgId}/mailboxes` | Each mailbox now has `usedBytes`, as of the last measurement |
| `GET /api/v1/me/mailboxes` | Each mailbox now has `quotaWarningPercents` |

## Operating

The API measures mailboxes through the [Dovecot management API](installation.md#dovecot-management-api), 50 mailboxes per request, and needs no change to Dovecot's configuration. `AETHER_QUOTA_CHECK_INTERVAL` (default `5m`, at least `10s`) sets how often. When several API processes run, each one measures, but a warning is claimed in the database before it is sent, so it is delivered once.
