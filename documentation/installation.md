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

## Behind an existing reverse proxy

If ports 80 and 443 already belong to another web server on the host, skip the bundled Caddy:

1. Add `COMPOSE_FILE=compose.yml:compose.external-proxy.yml` to `.env`. The API then listens on `127.0.0.1:20100` and the web app on `127.0.0.1:20101`. Change them with `AETHER_API_LOCAL_PORT` and `AETHER_WEB_LOCAL_PORT`.
2. In your proxy, send `/api/*`, `/dav/*`, `/.well-known/caldav`, `/.well-known/carddav`, `/.well-known/autoconfig/*`, `/autodiscover/*` and `/Autodiscover/*` to the API port, and everything else to the web port. With Caddy:

   ```caddyfile
   mail.example.com {
   	request_body {
   		max_size 32MB
   	}
   	@api path /api/* /dav /dav/* /.well-known/caldav /.well-known/carddav /.well-known/autoconfig/* /autodiscover/* /Autodiscover/*
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

### DANE

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

## Monitoring

The API serves Prometheus metrics on port 9090 inside the Compose network (`AETHER_METRICS_LISTEN`). That port is not published, and the reverse proxy does not route it. The metrics cover:

- request rate, errors, and latency by route,
- sign-in attempts by method and result,
- messages sent from Aether Mail,
- mail service errors,
- identity provider webhooks,
- Go runtime and process statistics.

No metric carries a user, mailbox, or address label.

Two mail services also serve metrics inside the Compose network:

- Dovecot, on port 9900: mailbox sign-ins, IMAP commands, and deliveries.
- Rspamd, on port 11334: scanned messages and filter actions. The endpoint requires the `Password` header, set to `AETHER_RSPAMD_METRICS_PASSWORD`. That password is read-only and cannot train the filter or change settings.

Postfix writes its log to `/var/log/postfix/mail.log` on the `postfix-logs` volume. The container also prints it, so `docker compose logs postfix` still works. It rotates the log daily and keeps 7 days. With the `monitoring` profile, `postfix-exporter` reads that log and the queue, and serves on port 9154: queue depth by queue, outbound results (sent, deferred, bounced), connections, rejections, and failed SMTP sign-ins.

### Tracing

The API exports OpenTelemetry traces over OTLP/HTTP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set in `.env`, for example `http://otel-collector:4318`. Set `OTEL_EXPORTER_OTLP_HEADERS` if the collector needs an API key. Each request becomes a span named after its route, with child spans for PostgreSQL queries and Valkey commands.

Spans record SQL text without parameter values, and Valkey command names without arguments. They do record the client IP address, the user agent, and the request path, and DAV paths contain user IDs. With Helm, set the same variables in `api.extraEnv`.

For a ready-made setup, start the optional profile:

```sh
docker compose --profile monitoring up -d
```

This runs Prometheus, which keeps 30 days of data and scrapes the API, Dovecot, Rspamd, and Postfix, and Grafana with an "Aether overview" dashboard. Grafana listens only on `127.0.0.1:3300` (set `AETHER_GRAFANA_PORT` to change it). Reach it through an SSH tunnel (`ssh -L 3300:127.0.0.1:3300 <host>`) or your VPN. Sign in as `admin` with `AETHER_GRAFANA_PASSWORD` from `.env`.

## Antivirus

ClamAV is optional, because it needs about 1.5 GB of memory. To turn it on, add both lines to `.env`, then run `docker compose up -d`:

```sh
COMPOSE_PROFILES=antivirus
AETHER_CLAMAV_ADDR=clamav:3310
```

If you already use other profiles, list them all, for example `COMPOSE_PROFILES=antivirus,monitoring`.

Rspamd then sends every attachment to ClamAV and rejects infected mail during the SMTP session, so the sender gets the error. This applies to both incoming and outgoing mail. ClamAV downloads signature updates by itself and keeps them in the `clamav` volume. If ClamAV is not reachable, mail is still delivered unscanned. With Helm, set `clamav.enabled=true`.

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

## Backups

Back up these volumes and files:

| Item | Contents |
| --- | --- |
| `pgdata` | All configuration and users. Use `docker compose exec postgres pg_dump -U aether aether`. |
| `vmail` | Mail, in Maildir format. Copying the files is safe while running. |
| `.env` | The secrets |
| `tls` | The mail certificate |

`scripts/backup.sh <target-dir> [keep-days]` writes these three items to a timestamped directory: a `pg_dump` custom-format dump, a tar of the maildirs, and a copy of `.env`. It checks that both archives are readable and deletes backups older than `keep-days` (default 14). Run it from a nightly timer, and copy the target directory to another machine; a backup on the same disk does not survive losing that disk.

To restore a local backup:

```sh
./scripts/restore.sh --yes /var/backups/aether/20261015T020000Z
```

The script stops the mail services and the API, then replaces the database and every mailbox with the backup's contents. When it finishes, it starts the stack again. It refuses to run without `--yes`. It also refuses if `AETHER_SECRET_KEY` in `.env` differs from the key in the backup's `env` file; on a new server, copy that file to `.env` first.

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
