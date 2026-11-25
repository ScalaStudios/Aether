# Audit log

Aether records every administrative change, sign-in and export as an audit event: who did it, what they changed, when, and from which IP address. Organization administrators see their organization's events. Platform administrators see every event, including those that belong to no organization.

## In the console

Organization administrators find the log under **Administration → Audit log**. Each row shows when an event happened, who did it, what they did, what it changed and from which IP address. Select a row to see the event's details, including the action name that webhooks and the API use.

- **Search** looks through actions, addresses, IP addresses and details.
- **Action** narrows the list to one group of actions, such as sign-ins or mailboxes. **Actor** takes part of an email address or of an API key's name, and **Target** part of an address, a type or an id.
- **From** and **To** are days in your browser's time zone, and both days are included.
- **Load older events** fetches the next 50 events.
- **Export** downloads the events that match the filters as CSV or JSON.
- **Retention** sets how long the organization keeps its events: the platform default, a number of days, or forever. The page shows the period in force, and the dialog warns before a shorter period deletes events.

The filters are part of the page's address, so you can bookmark a filtered view or send it to another administrator.

Platform administrators also have **Administration → Platform audit log**, which lists the events of every organization and those that belong to none, and can narrow the list to one organization or to events without one. They manage platform webhooks under **Administration → Platform webhooks**.

## Which organization an event belongs to

Changes to an organization's domains, mailboxes, aliases, members, branding, API keys and webhooks belong to that organization.

Sign-in events (`auth.login`, `auth.login.failed`, `auth.oidc.rejected` and `auth.logout`) belong to the organization that holds the account's mailbox. A failed sign-in for an address that has no account belongs to the organization whose mailbox has that address, if there is one. Every other sign-in event, together with SCIM provisioning, identity provider changes and `platform_admin.bootstrap`, belongs to no organization.

## Searching

`GET /api/v1/organizations/{orgId}/audit` returns up to 50 events, newest first; `limit` raises that to 200. To get the next page, pass the `id` of the last event as `before`. Filters combine:

| Parameter | Matches |
| --- | --- |
| `actor` | A user or API key id exactly, or part of the actor's email address or the key's name |
| `action` | Action names, repeated or comma separated. `auth.*` matches every action that starts with `auth.` |
| `target` | Part of the target type or id, or of the address in the event |
| `targetType`, `targetId` | The target type or id exactly |
| `since`, `until` | An RFC 3339 time or a `YYYY-MM-DD` date in UTC. `until` with a date includes that whole day |
| `q` | Full-text search over the action, target, IP address, actor email and the event's details. Quoted phrases, `or` and `-word` work as in a web search |

```sh
curl -H "Authorization: Bearer aether_..." \
  "https://mail.example.com/api/v1/organizations/{orgId}/audit?action=auth.login.failed&since=2026-11-01"
```

Platform administrators can search across organizations with `GET /api/v1/admin/audit`. Add `orgId` to narrow it to one organization, or `orgId=none` for events without an organization.

## Exporting

`GET /api/v1/organizations/{orgId}/audit/export?format=csv` downloads the events that match the same filters, as CSV or JSON (the default). An export holds at most 50,000 events, newest first. For more, export again with `before` set to the `id` of the oldest event you received. In CSV files, cells that start with `=`, `+`, `-` or `@` begin with `'`, so that spreadsheets do not run them as formulas, and the event's details are a JSON string in the `metadata` column.

Every export is itself recorded, as `audit.export` with the format and the filters. Platform administrators export with `GET /api/v1/admin/audit/export`.

API keys need `audit:read` to search and export.

## Retention

Aether keeps audit events forever unless a retention period is set. `AETHER_AUDIT_RETENTION_DAYS` sets the platform default in days, from 0 (forever) to 3650. It also applies to events without an organization.

An organization can override the default:

```sh
curl -X PUT -H "Authorization: Bearer aether_..." -H "Content-Type: application/json" \
  -d '{"retentionDays": 365}' https://mail.example.com/api/v1/organizations/{orgId}/audit/retention
```

`0` keeps the organization's events forever, and `null` returns to the platform default. `GET` on the same path shows the organization's setting, the default and the value in force. Changing it needs an organization administrator or a key with `audit:write`, and is recorded as `audit.retention.update`. The API deletes expired events once an hour, and shortly after it starts.

## Sending events to other systems

[Webhooks](integrations.md#webhooks) receive the events of one organization. A platform administrator can also create platform webhooks under **Administration → Platform webhooks** or with `POST /api/v1/admin/webhooks`, which receive the events that belong to no organization, such as sign-ins of accounts without a mailbox.

The API can also send every audit event, whatever organization it belongs to, to a syslog server or an OpenTelemetry collector:

| Variable | Default | What it sets |
| --- | --- | --- |
| `AETHER_AUDIT_SYSLOG_ADDR` | none | `host:port` of a syslog server. Events are sent as RFC 5424 messages with the event as JSON |
| `AETHER_AUDIT_SYSLOG_NETWORK` | `udp` | `udp` or `tcp` |
| `AETHER_AUDIT_SYSLOG_FACILITY` | `auth` | A facility name, such as `local0`, or its number |
| `AETHER_AUDIT_SYSLOG_APP_NAME` | `aether` | The APP-NAME field |
| `AETHER_AUDIT_OTLP_ENDPOINT` | `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | An OTLP/HTTP collector. Without a path, `/v1/logs` is added. Events are sent as JSON log records |
| `AETHER_AUDIT_OTLP_HEADERS` | `OTEL_EXPORTER_OTLP_LOGS_HEADERS`, then `OTEL_EXPORTER_OTLP_HEADERS` | Headers for the collector, as `key=value` pairs separated by commas |

Failed sign-ins and rejected single sign-ons are sent with warning severity, everything else as informational. Delivery is best effort: the API queues up to 1,024 events and drops new ones while the queue is full, logging a warning. The audit log in the database stays complete either way. With Helm, set these variables in `api.extraEnv`.
