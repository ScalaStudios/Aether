# API keys and webhooks

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
| `mailboxes:read`, `mailboxes:write` | Listing, creating, changing, and removing mailboxes |
| `aliases:read`, `aliases:write` | Listing, creating, and removing aliases |
| `members:read`, `members:write` | Listing members and changing their roles. A key cannot make anyone an owner. |
| `audit:read` | Reading the audit log |
| `webhooks:read`, `webhooks:write` | Managing webhooks and reading deliveries |

A `write` scope includes `read`. Keys cannot read mail, use the platform administration endpoints, or create other keys. Requests made with a key do not need a CSRF token. Each key may make 600 requests per minute. Actions taken with a key appear in the audit log under the key's name.

To revoke a key, use `DELETE /api/v1/api-keys/{keyId}`. Revocation takes effect immediately.

## Webhooks

A webhook receives every audit event of its organization that matches its event list, for example `mailbox.create`, `domain.verify`, or `alias.delete`. Use `*` to receive all events. The audit log on the console shows the event names.

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
