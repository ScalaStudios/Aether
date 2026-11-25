# Recovering deleted mail

Messages that are permanently deleted are kept for a while, so that a mistake can be undone without restoring a backup. That covers emptying Trash or Junk, deleting a message that is already in Trash, a mail app that expunges straight away, deleting a folder that still holds messages, and the automatic 30-day clean-up of Trash and Junk. Kept messages are hidden from mail apps and do not count towards the mailbox quota.

## Restoring messages

In Aether Mail, open the menu of the **Trash** folder and choose **Recover deleted items**. The list shows what was permanently deleted, newest first, with the folder each message was deleted from. Select messages and choose **Restore**. Each message goes back to the folder it was deleted from, keeping its flags and date, or to Inbox if that folder no longer exists. The owner of a mailbox and its full delegates or members can restore; read-only members cannot.

Organization administrators restore for someone else under **Administration → Mailboxes**, from the mailbox's **More actions** menu. Every restore is recorded in the audit log as `mailbox.messages.restore`, with the folders and the number of messages, and an administrator opening the list is recorded as `mailbox.recoverable.view`.

The list holds the 2,000 most recently deleted messages. The filter above it searches subject, sender and folder.

## How long messages are kept

Each organization chooses from 1 to 30 days under **Administration → Mailboxes → Deleted item retention**; the default is 14. The time counts from the moment a message was permanently deleted, not from when it arrived. The API removes expired messages from every active mailbox once an hour, and Dovecot removes anything older than 30 days whenever a mailbox is opened, as a backstop. Changing the setting is recorded as `organization.update`.

## What is not kept

- Drafts. Saving a draft replaces the previous version, so the Drafts folder is excluded, including drafts deleted by hand.
- Moves. Moving a message, including deleting it to Trash, keeps one copy only.
- Messages a delegate or a shared mailbox member permanently deletes through the `Shared/` folders of their own mail app. Deletions in Aether Mail are kept for every mailbox.
- Messages a mail rule discards. They are never stored.

## API

| Route | What it does |
| --- | --- |
| `GET /api/v1/mailboxes/{mailboxId}/recoverable` | `retentionDays` and `items`: each message's `folder`, `uid`, `deletedAt`, subject, sender, date and size |
| `POST /api/v1/mailboxes/{mailboxId}/recoverable/restore` | Restores `items`, 1 to 500 `{folder, uid}` pairs from the list, and answers with the number `restored`. `404` when none of them exist any more, `507 over_quota` when the mailbox is full |
| `PATCH /api/v1/organizations/{orgId}` | Sets `deletedItemRetentionDays`, from 1 to 30. Administrators only |

API keys cannot use these routes, because the list shows the subjects and senders of a person's mail.

## Operating

Dovecot runs the `lazy_expunge` plugin. An expunged message is copied into a hidden namespace with the prefix `.EXPUNGED/`, stored in `Expunged/` next to `Maildir/` in the mailbox's home directory, under the name of the folder it came from. The namespace is left out of folder lists and of the quota. A global ACL in `/etc/dovecot/dovecot-acl` lets the owner read, flag and expunge there but not store messages, so a mail app cannot use it as free space; the plugin itself writes past the ACL. Copying only the last instance of a message keeps moves from creating copies.

While a mailbox is on a legal hold, the messages the hold keeps are not removed after the retention period and the person cannot remove them; see [Compliance](compliance.md#holds).

When a retention keep policy applies to the mailbox, deleted items are also kept until they are older than the keep period, counted from the message date. Such mailboxes leave the purge to the API instead of Dovecot's 30-day `autoexpunge`; see [Retention](architecture.md#retention).

Disk use grows by the mail deleted during the retention period. `doveadm mailbox status -u <address> messages '.EXPUNGED/*'` shows what a mailbox holds.

### Upgrading an existing installation

Migration 0035 adds `deleted_item_retention_days` to `organizations`; the API applies it at start. Dovecot restarts with the new configuration, which disconnects IMAP clients for a moment. Only messages deleted after the upgrade can be recovered.
