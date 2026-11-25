# Installation (Docker Compose)

## Requirements

- A Linux host with Docker Engine 24+ and Compose v2.
- 2 vCPU, 4 GB RAM, and disk space for mail.
- A static public IPv4 address. The reverse DNS (PTR) of that address must match the mail hostname.
- Inbound access on TCP 25, 465, 587, 143, 993, 80, and 443.
- Outbound access on TCP 25, or an authenticated relay if your provider blocks it (see "Outbound relay" below).

## Steps

1. Set up DNS:
   - an `A` record for the web host, for example `mail.example.com`
   - an `A` record for the mail hostname (it can be the same name)
   - a matching PTR record set at your hosting provider
2. Generate configuration:

   ```sh
   ./scripts/setup.sh
   ```

3. Edit `.env` and set these values:

   | Variable | Example |
   | --- | --- |
   | `AETHER_PUBLIC_URL` | `https://mail.example.com` |
   | `AETHER_SITE_ADDRESS` | `mail.example.com` (Caddy then obtains a certificate automatically) |
   | `AETHER_MAIL_HOSTNAME` | `mail.example.com` |

   `setup.sh` generates the random secrets. Back up `.env`: if `AETHER_SECRET_KEY` is lost, the stored DKIM keys and OIDC client secrets cannot be decrypted.
4. Install a trusted certificate for the mail hostname in the `tls` volume, as `fullchain.pem` and `privkey.pem`. If none is present, the stack generates a self-signed certificate, and mail clients will warn about it. One way to do this with certbot on the host:

   ```sh
   docker run --rm -v aether_tls:/tls -v /etc/letsencrypt:/le:ro alpine sh -c \
     'cp /le/live/mail.example.com/fullchain.pem /le/live/mail.example.com/privkey.pem /tls/ && chmod 600 /tls/privkey.pem'
   docker compose restart postfix dovecot api
   ```

5. Start the stack and create the administrator:

   ```sh
   docker compose up -d --build
   docker compose exec api aether bootstrap-admin --email you@example.com
   ```

6. Sign in at `AETHER_PUBLIC_URL`.
7. Create an organization and add your domain.
8. Publish the DNS records shown on the domain page. Ownership TXT, MX, SPF, DKIM, and DMARC are required. The MTA-STS and TLS reporting records are recommended (see "MTA-STS and TLS reporting" below).
9. Click **Verify**. Mail for a domain is accepted only after the domain is verified.

### SPF status

The domain page checks every record live. For SPF, Aether evaluates the record as a receiving server would, for each address of `AETHER_MAIL_HOSTNAME`, following `include`, `redirect`, `a`, `mx`, `ip4`, `ip6` and `exists` within the limit of 10 DNS lookups:

| Status | Meaning |
| --- | --- |
| OK | Every address of the mail host passes |
| Not strict | An address gets softfail or neutral, or the record cannot be evaluated: more than 10 lookups, two SPF records, or a term such as a macro that Aether does not support |
| Mismatch | An address of the mail host fails |
| Missing | The domain has no `v=spf1` record |
| Lookup error | DNS did not answer |

A record that authorizes the server only through an `include` or an IP address is OK. The suggested record stays `v=spf1 mx -all`.

## Behind an existing reverse proxy

If ports 80 and 443 already belong to another web server on the host, skip the bundled Caddy:

1. Add `COMPOSE_FILE=compose.yml:compose.external-proxy.yml` to `.env`. The API then listens on `127.0.0.1:20100` and the web app on `127.0.0.1:20101`. Change them with `AETHER_API_LOCAL_PORT` and `AETHER_WEB_LOCAL_PORT`.
2. In your proxy, send `/api/*`, `/dav/*`, `/jmap`, `/jmap/*`, `/.well-known/jmap`, `/.well-known/caldav`, `/.well-known/carddav`, `/.well-known/autoconfig/*`, `/autodiscover/*` and `/Autodiscover/*` to the API port, and everything else to the web port. With Caddy:

   ```caddyfile
   mail.example.com {
   	request_body {
   		max_size 32MB
   	}
   	@api path /api/* /dav /dav/* /jmap /jmap/* /.well-known/jmap /.well-known/caldav /.well-known/carddav /.well-known/autoconfig/* /autodiscover/* /Autodiscover/*
   	handle @api {
   		reverse_proxy 127.0.0.1:20100
   	}
   	handle {
   		reverse_proxy 127.0.0.1:20101
   	}
   }
   ```

3. For each mail domain, also serve `mta-sts.<domain>`, `autoconfig.<domain>` and `autodiscover.<domain>`, and send their policy and configuration paths to the API port:

   ```caddyfile
   mta-sts.example.com, autoconfig.example.com, autodiscover.example.com {
   	@served path /.well-known/mta-sts.txt /mail/config-v1.1.xml /autodiscover/* /Autodiscover/*
   	reverse_proxy @served 127.0.0.1:20100
   }
   ```

4. Reuse the proxy's certificate for SMTP and IMAP:

   ```sh
   ./scripts/sync-tls-cert.sh /path/to/mail.example.com.crt /path/to/mail.example.com.key
   ```

   The script copies the files only when they have changed, then reloads Postfix and Dovecot. Run it from a timer so that renewals are picked up.

## Mail app setup

Aether answers the lookups that mail apps make when someone types only their address and password:

| Record | What uses it |
| --- | --- |
| `autoconfig.<domain>` CNAME to the web host | Thunderbird, which fetches `https://autoconfig.<domain>/mail/config-v1.1.xml` |
| `autodiscover.<domain>` CNAME to the web host | Clients that use the Autodiscover XML format, which post to `https://autodiscover.<domain>/autodiscover/autodiscover.xml` |
| `_imaps._tcp.<domain>` and `_submissions._tcp.<domain>` SRV | Clients that look up services as RFC 6186 and RFC 8314 describe |

The domain page lists these records with your values. Both files describe IMAP on port 993 and SMTP on port 465 with implicit TLS, and the full address as the username; the Thunderbird file also lists the STARTTLS ports. The bundled Caddy obtains certificates for the `autoconfig` and `autodiscover` hosts on demand, but only for verified domains.

For iPhone, iPad and Mac, **Settings → App passwords → Set up an Apple device** downloads a configuration profile with mail, calendar and contacts accounts. It contains a new app password for the chosen mailbox, which can be revoked like any other.

## MTA-STS and TLS reporting

MTA-STS tells other mail servers to deliver to your domain only over TLS, and only to the mail hostname with a valid certificate. Aether serves the policy at `https://mta-sts.<domain>/.well-known/mta-sts.txt` for every verified domain. The bundled Caddy obtains certificates for these hosts on demand, but only for domains that Aether has verified.

To turn MTA-STS on for a domain, publish the three records from the domain page:

| Record | Purpose |
| --- | --- |
| `mta-sts.<domain>` | Points to the web host, so the policy can be fetched |
| `_mta-sts.<domain>` TXT | Announces the policy. Its `id` changes whenever the policy changes, and the domain page then shows the record as a mismatch until you update it. |
| `_smtp._tls.<domain>` TXT | Asks other servers to send daily TLS reports to `postmaster@<domain>` |

The policy starts in `testing` mode: senders report failures but still deliver. Once the mail certificate is trusted and the TLS reports show no failures, set `AETHER_MTA_STS_MODE=enforce` in `.env`, restart the API, and update the `_mta-sts` record with the new id.

### DMARC reports

The DMARC record that Aether suggests asks receivers such as Google and Microsoft to send daily aggregate reports to `dmarc-reports@<domain>`. Every verified domain accepts mail at that address without a mailbox. Postfix hands the reports to the API, which unpacks the gzip, zip, or XML attachment and stores the results. The domain page then shows, for each sending IP address, how many messages passed DKIM and SPF alignment and how many receivers quarantined or rejected. Use this to find services that send as your domain but are not covered by SPF or DKIM before you tighten the policy.

Aether also sends aggregate reports to domains that send mail here. After a UTC day has been over for half an hour, it builds one report from the DMARC results Rspamd stored and sends it to each `mailto` address in that domain's current `rua` tag. An address at another organization is used only when that organization publishes a DMARC record at `<their-domain>._report._dmarc.<collector>` naming the same address. The message is submitted on the internal authenticated relay and signed with the DKIM key of the sending domain. The address is `dmarc-reports@` the verified domain that matches `AETHER_MAIL_HOSTNAME`, unless `AETHER_REPORT_FROM` names another address on a verified domain that has a DKIM key. `AETHER_REPORT_ORG_NAME` is the organization name in the report; it otherwise defaults to the sending domain. `AETHER_SEND_REPORTS=false` turns this off, and so does an empty `AETHER_SIEVE_RELAY_PASSWORD`.

### Reports Aether sends about TLS

For each domain that publishes a `_smtp._tls` record, Aether sends the daily TLS report RFC 8460 describes. It reads Postfix's mail log (`AETHER_MAIL_LOG`, `/var/log/postfix/mail.log` in Compose) and counts TLS sessions to that domain: a DANE-verified session is reported under the `tlsa` policy, and any other TLS session under `no-policy-found`. The report goes to each `mailto` or `https` address in the record's `rua` tag. HTTPS collectors must be on port 443 with a public name; private addresses are refused the same way as webhooks. This is skipped when `AETHER_RELAYHOST` is set, because Postfix then records the connection to the relay, not to the recipient's server. The same `AETHER_SEND_REPORTS`, `AETHER_REPORT_FROM` and `AETHER_REPORT_ORG_NAME` settings apply.

`aether_reports_sent_total` counts reports that Postfix or an HTTPS collector accepted, labeled `dmarc` or `tls`. A rejected address is not retried when the collector returns a permanent error.

### DKIM key rotation

Manage keys with the domain's DKIM API under `/api/v1/domains/{domainId}/dkim`. Organization administrators can use their session or an API key with `domains:write`. Use `domains:read` to list keys and their status.

1. POST to the DKIM collection with `{"algorithm":"rsa","bits":4096}` to prepare a replacement. RSA also supports 2048 bits, the default. Use `{"algorithm":"ed25519"}` to add or rotate the optional second signature. To import a key, send `selector` and `privateKey` with its single-label selector and unencrypted RSA or Ed25519 PEM.
2. Publish the returned `record` as a TXT record at `dnsName`. For long RSA values, split the value into quoted strings of at most 255 bytes in the **same** TXT record if your DNS provider requires it. Keep the current TXT record published. Wait for the new record to propagate, including expiry of any cached negative response for the selector.
3. POST to `/api/v1/domains/{domainId}/dkim/{keyId}/activate`. Aether checks DNS and switches signing for the selected algorithm only when it finds the matching key. Check the domain's DNS page or GET the DKIM collection for the selectors and states.
4. Leave the old TXT record published through the retired key's `removeAfter` time, seven days after the switch. After that time, remove the old DNS record and DELETE the retired key at `/api/v1/domains/{domainId}/dkim/{keyId}` to clear its metadata and cached key material.

DELETE on a pending key cancels it. DELETE on the active Ed25519 key stops the second signature and starts the same seven-day retirement period. Replace the active RSA key through rotation. Aether flags active keys at 365 days via `rotationDue` on the key and `dkimRotationDue` on the domain. Existing selectors stay active on upgrade.

## DANE

Outbound mail checks DANE. The bundled Unbound resolver validates DNSSEC, and Postfix uses it to look up TLSA records. When a receiving domain publishes signed TLSA records, Postfix only delivers over TLS to a server whose certificate matches them. Domains without TLSA records get opportunistic TLS as before. Unbound sits on an internal `resolver` network at `172.29.53.53`. If `172.29.53.0/24` is already in use on the host, change the subnet and the two addresses in `compose.yml`.

Aether does not publish TLSA records for its own domains, because they need DNSSEC on the domain and a mail key that stays the same across certificate renewals.

## Outbound relay

Some hosting providers block outbound port 25. In that case, send outbound mail through an authenticated relay:

```sh
AETHER_RELAYHOST=[smtp.relay.example]:587
AETHER_RELAY_USER=...
AETHER_RELAY_PASSWORD=...
```

With these set, Postfix requires TLS to the relay. Mail between Aether's own domains is still delivered locally. Make sure the domain's SPF record includes the relay.

## Outbound routing

Platform administrators can manage routing in **Administration → Outbound routing**. A sender relay applies to a verified hosted sender domain. A recipient transport applies to an external recipient domain and takes precedence over the sender relay. Use a fixed SMTP endpoint such as `[smtp.partner.example]:587`. Unmatched messages use the server’s global relay or direct delivery.

TLS policies apply to the delivery next hop. Use the recipient domain for direct MX delivery and the exact endpoint for a fixed transport or relay. Routes through the same endpoint share its TLS policy. `none` uses plaintext, `may` attempts STARTTLS, `encrypt` requires TLS, and `dane` uses DNSSEC and TLSA when available. `verify` and `secure` validate certificates and support an optional list of matching hostnames. Authenticated relays always require TLS and honor stricter `verify` and `secure` policies.

Per-sender relay credentials are encrypted under `AETHER_SECRET_KEY`. Postfix retrieves them from the API over the internal socketmap listener. Custom deployments must provide the same `AETHER_MAIL_DB_PASSWORD` to the API and Postfix and allow Postfix to reach the API’s `AETHER_OUTBOUND_SASL_LISTEN` (default `:10041`). Set `AETHER_OUTBOUND_SASL_SERVICE` on Postfix if the API endpoint differs from `api:10041`. Keep the listener on a private network.

## Policy service

Postfix decides who may send to a restricted distribution list, and whether a signed-in account is within its sending limits, by asking the API on port 10040 during the SMTP session. Without it, submission answers every recipient with a temporary error. The API listens on `AETHER_POLICY_LISTEN` (default `:10040`), and Postfix connects to `AETHER_POLICY_SERVICE` (default `api:10040`). In Compose the port is not published and is reachable only inside the Compose network. If the API runs elsewhere, open that port to Postfix and set `AETHER_POLICY_SERVICE` on the Postfix container.

## Mail flow milters

Postfix runs [mail flow rules](mail-flow-rules.md) by calling two milters in the API, one before Rspamd on port 10045 and one after it on port 10046. The API listens on `AETHER_MILTER_LISTEN` (default `:10045`) and `AETHER_MILTER_AFTER_LISTEN` (default `:10046`), and Postfix connects to `AETHER_MILTER_SERVICE` (default `api:10045`) and `AETHER_MILTER_AFTER_SERVICE` (default `api:10046`). Like the policy service, the ports are reachable only inside the Compose network. Postfix answers `451` while the API is unreachable, so start the API before Postfix, as Compose does.

The API passes approved messages back to Postfix through the Sieve relay port 10025, signed in with `AETHER_SIEVE_RELAY_PASSWORD`; Compose and the Helm chart give it the same password as Dovecot. If the API runs elsewhere, set `AETHER_SIEVE_RELAY_ADDR` (default `postfix:10025`) and open that port to the API.

Postfix also calls the API's [mailbox access audit](mailbox-auditing.md) milter on port 10047 for every message. The API listens on `AETHER_AUDIT_MILTER_LISTEN` (default `:10047`), and Postfix connects to `AETHER_AUDIT_MILTER_SERVICE` (default `api:10047`). If the API cannot be reached, mail is still delivered, unrecorded.

Moderated lists also need the API to submit mail on Postfix's internal port 10025. Compose gives the API `AETHER_SIEVE_RELAY_PASSWORD`; if the API runs elsewhere, set it there together with `AETHER_SIEVE_RELAY_ADDR` (default `postfix:10025`), and make sure Dovecot's `allow_nets` for the relay login includes the API's network.

## Coexistence during a migration

Coexistence lets a domain move to Aether one mailbox at a time. Point the domain's MX at Aether, then open the domain under **Administration → Domains**, select **Configure coexistence** and enter the old server's direct SMTP hostname, for example the Microsoft 365 or Google Workspace MX host for the domain. `PUT /api/v1/domains/{domainId}/coexistence` does the same through the API.

While coexistence is on:

- Mail for an address that exists in Aether, as an active mailbox, alias, list or catch-all, is delivered here as usual.
- Mail for any other address on the domain is accepted and relayed to the old server on port 25 instead of being rejected. Addresses of deactivated mailboxes and aliases are still rejected.
- Mailboxes selected under **Dual delivery** keep their Aether copy and also get a copy on the old server, which is useful during a test period.
- The domain list and domain page show **Coexistence on**, and turning it on or off records a `domain.coexistence.enable` or `domain.coexistence.disable` audit event.

The relayed mail goes straight to the old server, even when an outbound relay is configured, so the host must be reachable on port 25. The old server must still accept mail for the domain, and it should trust Aether's address as an inbound gateway, because the relayed messages fail SPF there. Each relayed message carries an `X-Aether-Coexistence` header with a value unique to the installation; if the old server sends it back to Aether, Postfix rejects it as a routing loop, so keep that header intact on the old server. Because every address on the domain is accepted, a message for an address the old server does not know bounces from Aether to its sender. Turn coexistence off once every mailbox has moved.

## Dovecot management API

Dovecot serves its doveadm HTTP API on port 8089 inside the Compose network, protected by `AETHER_DOVECOT_API_KEY`, which `setup.sh` generates. The API uses it to end a member's IMAP sessions when their shared mailbox access changes, and to measure mailbox sizes for [storage warnings](storage.md). Only the API and the Dovecot containers receive the key, and neither starts without it; Postfix does not get it. Never publish the port: the key allows every doveadm command, including reading any mailbox. If the API runs elsewhere, set `AETHER_DOVECOT_API_URL` (default `http://dovecot:8089`) and open the port to the API only.

Rspamd's controller accepts training requests only with `AETHER_RSPAMD_CONTROLLER_PASSWORD`, which `setup.sh` also generates. Only the API and Rspamd receive it; the API uses it to teach the spam filter when someone moves a message into or out of Junk. Installations created before spam learning existed must add it to `.env`, for example with `printf 'AETHER_RSPAMD_CONTROLLER_PASSWORD=%s\n' "$(head -c 48 /dev/urandom | base64 | tr -d '\n/+=' | cut -c1-40)" >> .env`, before upgrading.

Live mail holds an IMAP IDLE connection for each watched folder. Dovecot allows 100 connections per user from one IP. Web Push keys are generated and stored encrypted when `AETHER_VAPID_PUBLIC_KEY` and `AETHER_VAPID_PRIVATE_KEY` are left empty; set both to pin them. iOS Mail push needs an Apple certificate in `AETHER_APNS_CERT_FILE` and `AETHER_APNS_KEY_FILE`, and the bundled Dovecot still does not implement `XAPPLEPUSHSERVICE`. See [Notifications](notifications.md).

Installations created before shared mailboxes existed must add the key to `.env` before upgrading. [Shared mailboxes](shared-mailboxes.md#upgrading-an-existing-installation) lists the steps, including the check for existing folders named `Shared`.

## Postfix queue API

The Postfix container runs `aether-postfix-api` on port 8025 inside the Compose network, protected by `AETHER_POSTFIX_API_KEY`, which `setup.sh` generates. The API uses it for the [mail queue](mail-queue.md) page: listing, retrying, holding, releasing and deleting queued messages. Only the API and the Postfix containers receive the key. Without it both still start and deliver mail, and the queue page says it is not connected. Never publish the port: the key allows deleting any queued message. If the API runs elsewhere, set `AETHER_POSTFIX_API_URL` (default `http://postfix:8025`) and open the port to the API only.

## Monitoring

The API serves Prometheus metrics on port 9090 inside the Compose network (`AETHER_METRICS_LISTEN`). That port is not published, and the reverse proxy does not route it. Every API process also runs the loops for scheduled sends, snoozes, webhooks, and calendar subscription refreshes, the loop that sends DMARC and TLS reports, and two access workers that reconcile shared mailbox rights in Dovecot and deliver IMAP session kicks. The report loop is the one that `AETHER_SEND_REPORTS=false` switches off. The schedule loop also wakes when a message is due within 30 seconds, so an undo delay is not rounded up to the 15 second pass. The metrics cover:

- request rate, errors, and latency by route,
- sign-in attempts by method and result,
- messages sent from Aether Mail,
- mail service errors,
- identity provider webhooks,
- calendar subscription refreshes by outcome (`aether_calendar_subscription_refresh_total`),
- pending IMAP session kicks (age of the oldest),
- pending rights reconciliation (age of the oldest),
- messages sent to the spam filter for learning, by class (`spam`, `ham`) and result (`learned`, `duplicate`, `disabled`, `failed`),
- messages in the Postfix queue by queue, and the age of the oldest one not on hold,
- DMARC aggregate and TLS reports accepted by a collector (`aether_reports_sent_total`, labeled `dmarc` or `tls`),
- Go runtime and process statistics.

No metric carries a user, mailbox, or address label.

Two mail services also serve metrics inside the Compose network:

- Dovecot, on port 9900: mailbox sign-ins, IMAP commands, and deliveries.
- Rspamd, on port 11334: scanned messages and filter actions. The endpoint requires the `Password` header, set to `AETHER_RSPAMD_METRICS_PASSWORD`. That password is read-only and cannot train the filter or change settings.

Postfix writes its log to `/var/log/postfix/mail.log` on the `postfix-logs` volume. The container also prints it, so `docker compose logs postfix` still works. It rotates the log daily and keeps 7 days. With the `monitoring` profile, `postfix-exporter` reads that log and the queue, and serves on port 9154: queue depth by queue, outbound results (sent, deferred, bounced), connections, rejections, and failed SMTP sign-ins. The `mail-trace` service reads the same log into the [message trace](message-trace.md), which administrators search under **Administration → Message trace**.

### Tracing

The API exports OpenTelemetry traces over OTLP/HTTP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set in `.env`, for example `http://otel-collector:4318`. Set `OTEL_EXPORTER_OTLP_HEADERS` if the collector needs an API key. Each request becomes a span named after its route, with child spans for PostgreSQL queries and Valkey commands.

Spans record SQL text without parameter values, and Valkey command names without arguments. They do record the client IP address, the user agent, and the request path, and DAV paths contain user IDs. With Helm, set the same variables in `api.extraEnv`.

For a ready-made setup, start the optional profile:

```sh
docker compose --profile monitoring up -d
```

This runs Prometheus, which keeps 30 days of data, scrapes the API, Dovecot, Rspamd, and Postfix, and evaluates the mail queue alerts in `infrastructure/monitoring/alerts.yml` (see [Mail queue](mail-queue.md#monitoring-and-alerts)), and Grafana with an "Aether overview" dashboard. Grafana listens only on `127.0.0.1:3300` (set `AETHER_GRAFANA_PORT` to change it). Reach it through an SSH tunnel (`ssh -L 3300:127.0.0.1:3300 <host>`) or your VPN. Sign in as `admin` with `AETHER_GRAFANA_PASSWORD` from `.env`.

## Antivirus

ClamAV is optional, because it needs about 1.5 GB of memory. To turn it on, add both lines to `.env`, then run `docker compose up -d`:

```sh
COMPOSE_PROFILES=antivirus
AETHER_CLAMAV_ADDR=clamav:3310
```

If you already use other profiles, list them all, for example `COMPOSE_PROFILES=antivirus,monitoring`.

Rspamd then sends every attachment to ClamAV and rejects infected mail during the SMTP session, so the sender gets the error. This applies to both incoming and outgoing mail. ClamAV downloads signature updates by itself and keeps them in the `clamav` volume. If ClamAV is not reachable, mail is still delivered unscanned. With Helm, set `clamav.enabled=true`.

## Attachment filtering

Each organization chooses blocked file types under **Administration → Attachments**, with separate lists for incoming and outgoing mail. For each list the action is one of:

- **Reject message:** Rspamd refuses the message during the SMTP session. Outside senders get a bounce from their own server, and people sending from Aether Mail see the error.
- **Remove attachment:** the message is delivered, and each blocked attachment is replaced by a short text note naming the file. A message whose only part is a blocked file is rejected instead.
- **Off:** attachments are not checked.

An attachment is blocked when its file name ends in a listed extension (case and trailing dots or spaces are ignored, so `invoice.pdf.exe` and `INVOICE.EXE.` match `exe`), when Rspamd detects a listed type from the content (an executable named `report.pdf` matches `exe`), or when a ZIP, 7z or gzip archive contains a file with a listed extension. Attachments inside forwarded messages are checked too.

Organizations that never changed the setting reject incoming mail carrying executables, scripts, disk images and macro-enabled Office files; outgoing mail is not checked. **Use the default list** restores that list.

Exceptions skip the check for listed envelope senders, either an address or `@domain` for a whole domain. Other servers can forge the envelope sender, so add only senders you trust.

The API stores the policy in PostgreSQL and publishes it to Valkey under the hash `AETHER_ATTACHMENT_POLICY`, one field per domain. Rspamd reads it for each message: by recipient domain for incoming mail, and by sender domain for authenticated submissions. The API republishes every policy at startup, so restart it if Valkey lost its data. Mail for a domain with no published policy is not filtered. Mail forwarded by Sieve rules is not checked again. Each change is recorded in the audit log as `attachment_policy.update`.

## Message size and download links

`AETHER_MESSAGE_SIZE_LIMIT` is the largest message Postfix will accept, in bytes. The default is 52428800 (50 MB), and it must be an integer from 1 to 2147483647. The same value is the ceiling for every organization's message size. `AETHER_MAX_LINK_BYTES` (default 2147483648, 2 GB, at most 8 GB) is the largest file that can be sent as a download link, and `AETHER_MAX_STORAGE_BYTES` (default 107374182400, 100 GB) is the most storage all of an organization's links may use. Organization administrators set their own limits, within those ceilings, under **Administration → Limits**.

A download link is a page at `{AETHER_PUBLIC_URL}/files/{token}`. The file itself is `POST`ed to the API as the raw body of `/api/v1/mailboxes/{id}/file-links`, so the reverse proxy in front of the API must accept a body of `AETHER_MAX_LINK_BYTES` on that path and on `/api/v1/files/*/content`, and should allow the transfer 30 minutes. Other API requests, including a message with inline attachments, need a body of up to 256 MB. The bundled Caddyfile does this. A proxy that keeps the old 32 MB limit will refuse both a large link and a message whose base64 attachments exceed 32 MB.

The files live in the PostgreSQL database as large objects. `pg_dump` includes them, and deleting a link removes the object, so no extra volume is required.

## Upgrades

Each release is a tag, such as `v0.1.0`, with notes in [CHANGELOG.md](../CHANGELOG.md). Back up first, then check out the new tag and rebuild:

```sh
./scripts/backup.sh /var/backups/aether
git fetch --tags
git checkout v0.2.0
docker compose up -d --build
```

To follow development instead, stay on `main` and run `git pull` before rebuilding.

Database migrations run automatically when the API starts. They are transactional and serialized with an advisory lock.

### Encrypt existing mail

Dovecot encrypts new messages written to Maildir using a key derived from `AETHER_SECRET_KEY`. It can still read existing plaintext mail. After upgrading the source, migrate the existing messages during a mail maintenance window:

1. Back up the stack and verify you have a protected copy of the current `.env`.
2. Build the upgraded Dovecot image, stop Dovecot, and run the migration against the same `vmail` volume.

   ```sh
   docker compose build tls-init
   docker compose stop dovecot
   docker compose run --rm --no-deps dovecot encrypt-mail.sh
   ```

3. Once the command succeeds, bring the stack up with the upgraded images.

   ```sh
   docker compose up -d --build
   ```

IMAP and mailbox delivery are unavailable while Dovecot is stopped. Keep all writers off the volume during the migration. Allow enough free space for a temporary encrypted copy of the largest message and for encryption overhead across the volume. The migration replaces each message atomically, preserves its Maildir filename and modification time, and skips encrypted files on retry. If it fails or is interrupted, keep Dovecot stopped and rerun the migration command.

This protects message files on stolen disks or copied volumes when the key is stored separately. A running host with access to the key can read messages. Dovecot indexes and caches can contain message metadata, and Postfix queues and earlier backups can still contain plaintext. Use encrypted storage and backups to protect those copies.

## Backups

Back up these volumes and files:

| Item | Contents |
| --- | --- |
| `pgdata` | All configuration, users, and download-link files, which are stored as PostgreSQL large objects. Use `docker compose exec postgres pg_dump -U aether aether`. |
| `vmail` | Mail, in Maildir format. Copying the files is safe while running. Deleting a mailbox removes its directory here, immediately or after the grace period you choose. A mailbox on hold is closed and its directory is kept. |
| `.env` | The secrets, including the key needed to read encrypted mail |
| `tls` | The mail certificate |

`scripts/backup.sh <target-dir> [keep-days]` writes these three items to a timestamped directory: a `pg_dump` custom-format dump, a tar of the maildirs, and a copy of `.env`. It checks that both archives are readable and deletes backups older than `keep-days` (default 14). Run it from a nightly timer, and copy the target directory to another machine; a backup on the same disk does not survive losing that disk.

To restore a local backup:

```sh
./scripts/restore.sh --yes /var/backups/aether/20261015T020000Z
```

The script stops the mail services and the API, then replaces the database and every mailbox with the backup's contents. When it finishes, it starts the stack again. It refuses to run without `--yes`. It also refuses if `AETHER_SECRET_KEY` in `.env` differs from the key in the backup's `env` file; on a new server, copy that file to `.env` first.

Keep a protected copy of the original `AETHER_SECRET_KEY` off-host, with access controlled separately from volume snapshots. Losing or replacing it makes encrypted mail unreadable until the original key is restored. The local backup contains both the mail and the key, so protect the entire backup directory. After restoring a plaintext or mixed backup, rerun the [Maildir migration](#encrypt-existing-mail) to encrypt any remaining plaintext messages.

`valkeydata` and `rspamd` hold caches and statistics, which are rebuilt automatically. DKIM keys in Valkey are re-published from PostgreSQL on every API start.

### Off-site copies

When `AETHER_BACKUP_REPOSITORY` is set in `.env`, `backup.sh` also sends each backup to a restic repository. It does this with the `restic/restic` container, so nothing has to be installed on the host. Restic encrypts everything with `AETHER_BACKUP_PASSWORD` before upload. Keep that password somewhere other than the server: without it, the backup cannot be read.

After each upload, the script prunes to `keep-days` daily, 8 weekly, and 12 monthly snapshots, then runs `restic check`. It uploads the maildirs directly rather than the tarball, so unchanged mail is not uploaded again.

| Provider | `AETHER_BACKUP_REPOSITORY` | `AETHER_BACKUP_S3_REGION` |
| --- | --- | --- |
| Cloudflare R2 | `s3:https://<account-id>.r2.cloudflarestorage.com/<bucket>` | `auto` |
| Backblaze B2 | `s3:https://s3.<region>.backblazeb2.com/<bucket>` | the bucket region, for example `us-west-004` |
| IDrive e2 | `s3:https://<endpoint>/<bucket>` | the endpoint region |
| AWS S3 | `s3:https://s3.<region>.amazonaws.com/<bucket>` | the bucket region |
| MinIO or another S3 server | `s3:https://<host>:<port>/<bucket>` | `us-east-1` unless configured otherwise |

Set `AETHER_BACKUP_S3_ACCESS_KEY` and `AETHER_BACKUP_S3_SECRET_KEY` to a key that can only read and write that bucket.

To restore from the off-site repository, use `restic:` followed by a snapshot ID, or `latest`:

```sh
./scripts/restore.sh --yes restic:latest
```

The repository settings come from `.env`. To list snapshots:

```sh
docker run --rm -e RESTIC_REPOSITORY=... -e RESTIC_PASSWORD=... -e AWS_ACCESS_KEY_ID=... -e AWS_SECRET_ACCESS_KEY=... \
  restic/restic:0.19.1 snapshots
```

## Bare metal

Running without Docker is possible but is not yet a documented, tested path. To do it, configure the following yourself, using the files under `infrastructure/docker/*` as the reference configuration:

- Debian 12 packages: `postfix postfix-pgsql dovecot-imapd dovecot-lmtpd dovecot-pgsql dovecot-sieve rspamd`
- PostgreSQL 15+ and Valkey
- The `aether` binary (`go build ./cmd/aether`) and the Next.js standalone build
