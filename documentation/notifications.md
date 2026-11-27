# Notifications

Aether Mail updates while it is open, and can notify a browser about new mail and calendar reminders when the tab is in the background. iOS Mail push is a server-side slice: it sends to Apple when a device is registered, and it does not make the bundled Dovecot speak the protocol iOS Mail requires.

## Live mail

The web app opens `GET /api/v1/events`, a server-sent event stream, after sign-in. The API holds an IMAP IDLE connection for the folders that need it:

- Inbox of every mailbox a signed-in tab can open, whatever its notification switch says, so unread counts stay current
- Inbox for people with a push subscription and no open tab, unless they turned Inbox notifications off
- any other folder they turned on under **Settings → Notifications**
- the folder they currently have open, even when it is not a notification folder
- Inbox of a mailbox that has a registered iOS Mail device

One API process takes a 30-second leader lock in Valkey (`aether:notify:leader`) and owns those IDLE connections. The others subscribe to the `aether:notify` channel and forward events to their own streams. If Valkey is down, every process watches; a delivery row stops a push from being sent twice. Each mailbox is watched in at most eight folders, with Inbox and the open folder kept ahead of the rest.

The stream events are `mail`, `calendar`, `ping` (every 20 seconds) and `resync` (the client should reload). A dropped stream falls back to polling every 30 seconds. While the stream is up, the mail list still refreshes every two minutes and folder counts every 30 seconds, which is how folders without a watch catch up.

Dovecot allows 100 IMAP connections per user per IP, up from 50, because each watched folder holds one. The API's own webmail requests use the same address, so a busy mailbox can still run out of connections; the cap of eight watches is there for that reason.

Caddy does not compress `text/event-stream` and flushes the API proxy immediately. The Helm ingress sets `proxy-read-timeout` to 3600 seconds and turns proxy buffering off, so nginx does not hold the stream.

These routes need a signed-in browser session. An organization API key receives 403.

## Browser notifications

**Settings → Notifications** stores two switches and one row per folder. Inbox defaults to on. Other folders default to off. Saving replaces that person's folder rows.

Web Push uses VAPID keys. Leave `AETHER_VAPID_PUBLIC_KEY` and `AETHER_VAPID_PRIVATE_KEY` empty and the API generates a pair on first use, seals the private key with `AETHER_SECRET_KEY` (context `push-vapid`) and stores it in `push_credentials`. Set both variables to pin a key across databases; setting only one refuses to start. The contact address on each push is `mailto:postmaster@` plus `AETHER_MAIL_HOSTNAME`.

Push endpoints are fetched with the same client as webhooks, so a subscription cannot point at a private address unless `AETHER_WEBHOOK_ALLOW_PRIVATE` is true. A push endpoint that answers 404 or 410 is deleted. Each message is recorded in `notification_deliveries` so a retry does not notify twice, and rows older than 14 days are removed.

The service worker shows the operating-system notification only when no Aether window is focused. A visible mail tab just refreshes. A visible calendar reminder also shows a toast.

## New mail sound

An open tab plays a short chime, synthesized with Web Audio, when a `mail` event brings unread mail to a folder that has notifications on. Each message in the event carries `fresh`. It is true only when the message is unread, was stored in the last two minutes and has not been announced for that mailbox before, which an `arrived:` row in `notification_deliveries` records. Unread mail that is moved or put back into a folder is still reported as new, but it is not fresh and does not chime. Mail from one of the person's own addresses is skipped as well.

The tab that plays holds a Web Locks lock for four seconds, and a tab that cannot take the lock stays silent, so several tabs and a burst of messages make one sound. Where the browser has no Web Locks API, each open tab plays. Nothing plays until the person has clicked or typed in the tab, because browsers block audio before that.

The switch and a test button are under **Settings → Notifications**. The choice is kept in that browser's local storage, not on the account, so each device can differ.

## Calendar reminders

An event can store a reminder from the event dialog, or as a `DISPLAY` `VALARM` in CalDAV. `reminderMinutes` is the number of minutes before the start, `0` is at the start, and `null` clears it. The maximum is 7 days. For an all-day event the start is the beginning of that day in UTC.

Every 15 seconds the notification loop looks for an alarm in the last 90 seconds or the next 20 seconds. The audience is the calendar owner, people the calendar is shared with, and members of an organization it is shared with, skipping anyone who turned calendar reminders off.

## iOS Mail

Apple's Mail app asks the IMAP server for `XAPPLEPUSHSERVICE` and then registers a device token. The Dovecot image Aether ships does not implement that command, and Debian's push-notification plugin is not a substitute for it. iOS Mail will keep fetching on its own schedule.

What is implemented, for a future plugin or gateway:

- `ios_push_devices` stores the mailbox, the account id the phone sent, the device token and the APNs topic (`com.apple.mobilemail` or `com.apple.mail.XServer`).
- `POST /internal/ios-push` and `DELETE /internal/ios-push` register and remove a device. They are not published through Caddy. Call the API directly with `Authorization: Bearer` set to `AETHER_DOVECOT_API_KEY` and a JSON body of `address`, `accountId`, `deviceToken` and `subtopic`. Delete sends `address` and `deviceToken`.
- When new unseen mail arrives in a watched Inbox, the API sends `{"aps":{"account-id":"…"}}` to `api.push.apple.com` (or `api.sandbox.push.apple.com` when `AETHER_APNS_SANDBOX=true`) using the certificate and key in `AETHER_APNS_CERT_FILE` and `AETHER_APNS_KEY_FILE`. A 410 response deletes the device.

Those two files are an Apple Push certificate and its private key, which Apple issues to a developer account. Aether does not ship them. Without the certificate the API accepts no devices and sends nothing. The payload matches the shape other self-hosted XAPS servers use; it has not been verified against a live Apple certificate.

## Metrics

`aether_mail_idle_watches` is the number of IDLE connections this process holds. `aether_notifications_total` counts deliveries by `channel` (`web`, `ios`, `calendar`) and `result` (`sent`, `failed`, `skipped`).
