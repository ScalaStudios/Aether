# Mail queue

When Postfix cannot deliver a message straight away, for example because the receiving server is down, greylists the sender or refuses the connection, the message waits in the Postfix queue and Postfix tries again later. Platform administrators can see and manage that queue under **Administration → Mail queue**, without a shell on the host.

## Using the queue page

The page lists queued messages oldest first, up to 200 at a time, and refreshes every 15 seconds while nothing is selected. Each row shows the envelope sender, the first recipient, the status, the last delivery error, when the message arrived, when Postfix will try again and the size. Open a message to see every recipient with its own delivery error.

Aether shows the envelope only. Subjects, headers and bodies stay private, in line with the rule that platform administrators cannot read mail.

| Status | Meaning |
| --- | --- |
| Deferred | A delivery attempt failed for a temporary reason. Postfix tries again at the time shown under **Next attempt**. |
| On hold | An administrator held the message. Postfix does not try to deliver it until it is released. |
| Delivering | Postfix is delivering the message right now. |
| Incoming | Postfix accepted the message and has not started delivering it yet. |

Filter by queue, by text in the sender address, by the exact domain of a recipient, or by text in the delivery error. The counts next to **All**, **Deferred** and **On hold** always cover the whole queue.

Select messages to act on them:

- **Retry now** asks Postfix to deliver deferred messages immediately instead of waiting for the next attempt. Messages in other states are skipped.
- **Hold** stops delivery attempts. Use it to stop a burst of mail from a compromised account while you investigate.
- **Release** moves held messages back to the deferred queue, keeping their scheduled next attempt. Use **Retry now** afterwards to send them straight away.
- **Delete** removes messages from the queue. Postfix does not tell the sender or the recipients, and it cannot be undone.

When at least one filter is set, every shown message is selected and more match, **Select all matching messages** applies the action to every message that matches the filters when you confirm, including messages that arrived after the list was loaded. Deleting that way asks you to type `delete`. To retry everything waiting for one domain, filter by that recipient domain, select all, and choose **Retry now**.

Every action is recorded in the audit log as `mail_queue.retry`, `mail_queue.hold`, `mail_queue.release` or `mail_queue.delete`, with the queue IDs or the filter and the number of messages changed. These events belong to no organization.

## API

| Method and path | Purpose |
| --- | --- |
| `GET /api/v1/admin/mail-queue` | List messages. Query parameters: `sender`, `recipientDomain`, `error`, `queue` (`maildrop`, `incoming`, `active`, `deferred` or `hold`) and `limit` (0 to 1000, default 200; 0 returns only the counts). |
| `POST /api/v1/admin/mail-queue/{action}` | `action` is `retry`, `hold`, `release` or `delete`. The body is either `{"ids": [...]}` with up to 1000 queue IDs, or `{"filter": {...}}` with the same fields as the list, at least one of them set. The response is `{"affected": n}`. |

Both need a platform administrator's session; API keys and organization administrators get `403`. When `AETHER_POSTFIX_API_KEY` is not set the API answers `503 queue_not_configured`, and when Postfix cannot be reached `503 queue_unavailable`.

## How it works

A small service, `aether-postfix-api`, runs inside the Postfix container on port 8025. It reads the queue with `postqueue -j`, retries with `postqueue -i`, and holds, releases and deletes with `postsuper`, passing queue IDs on standard input and never through a shell. The next attempt of a deferred message is the modification time of its queue file, which Postfix sets to the time of the next delivery attempt. The service accepts only requests that carry `AETHER_POSTFIX_API_KEY` as a bearer token. The API filters nothing itself: it validates the request and forwards it, so the queue is read once per request however large it is.

The Postfix container starts the service only when `AETHER_POSTFIX_API_KEY` is set, and restarts it if it exits. Mail delivery never depends on it.

## Monitoring and alerts

Every API process reads the queue once a minute and exports two gauges:

- `aether_mail_queue_messages{queue="..."}`: messages in each queue.
- `aether_mail_queue_oldest_seconds`: age of the oldest message that is not on hold, 0 when there is none.

With the `monitoring` profile, Prometheus loads `infrastructure/monitoring/alerts.yml`, which fires:

- `AetherMailQueueBacklog` when more than 100 messages that are not on hold have been queued for 15 minutes.
- `AetherMailQueueStale` when a message that is not on hold has waited for more than 4 hours, for 5 minutes.

Edit the thresholds in that file to suit your volume. Held messages are left out of both alerts, so mail you hold on purpose does not page anyone. The bundled Prometheus has no Alertmanager; the alerts show on its **Alerts** page. Point `alerting` in `infrastructure/monitoring/prometheus.yml` at your Alertmanager to be notified. With several API replicas each exports the same values, so the rules take the maximum across instances.

## Upgrading an existing installation

`scripts/setup.sh` generates the key for new installations. For an existing one:

1. Add the key to `.env`:

   ```sh
   echo "AETHER_POSTFIX_API_KEY=$(openssl rand -hex 24)" >> .env
   ```

2. Rebuild and recreate the containers:

   ```sh
   docker compose up -d --build
   ```

Until the key is set, the page explains that the queue is not connected and everything else keeps working. The Helm chart generates `postfixApiKey` in its Secret; with `secrets.existingSecret`, add a `postfixApiKey` entry to that Secret.

Port 8025 must never be published. The key allows deleting any queued message. The Compose file does not map the port, and the Helm chart's NetworkPolicy admits it only from the API pods. If the API runs elsewhere, set `AETHER_POSTFIX_API_URL` (default `http://postfix:8025`).
