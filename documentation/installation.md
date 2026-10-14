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
8. Publish the five DNS records shown on the domain page: ownership TXT, MX, SPF, DKIM, and DMARC.
9. Click **Verify**. Mail for a domain is accepted only after the domain is verified.

## Behind an existing reverse proxy

If ports 80 and 443 already belong to another web server on the host, skip the bundled Caddy:

1. Add `COMPOSE_FILE=compose.yml:compose.external-proxy.yml` to `.env`. The API then listens on `127.0.0.1:20100` and the web app on `127.0.0.1:20101`. Change them with `AETHER_API_LOCAL_PORT` and `AETHER_WEB_LOCAL_PORT`.
2. In your proxy, send `/api/*` to the API port and everything else to the web port. With Caddy:

   ```caddyfile
   mail.example.com {
   	request_body {
   		max_size 32MB
   	}
   	handle /api/* {
   		reverse_proxy 127.0.0.1:20100
   	}
   	handle {
   		reverse_proxy 127.0.0.1:20101
   	}
   }
   ```

3. Reuse the proxy's certificate for SMTP and IMAP:

   ```sh
   ./scripts/sync-tls-cert.sh /path/to/mail.example.com.crt /path/to/mail.example.com.key
   ```

   The script copies the files only when they have changed, then reloads Postfix and Dovecot. Run it from a timer so that renewals are picked up.

## Outbound relay

Some hosting providers block outbound port 25. In that case, send outbound mail through an authenticated relay:

```sh
AETHER_RELAYHOST=[smtp.relay.example]:587
AETHER_RELAY_USER=...
AETHER_RELAY_PASSWORD=...
```

With these set, Postfix requires TLS to the relay. Mail between Aether's own domains is still delivered locally. Make sure the domain's SPF record includes the relay.

## Upgrades

```sh
git pull
docker compose up -d --build
```

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

To restore, stop the stack. Then:

1. Recreate the volumes.
2. Load the database with `pg_restore -d aether --no-owner aether.pgdump`.
3. Extract `vmail.tar.gz` into the `vmail` volume.
4. Restore `.env`.

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

To restore from a snapshot:

```sh
docker run --rm -e RESTIC_REPOSITORY=... -e RESTIC_PASSWORD=... -e AWS_ACCESS_KEY_ID=... -e AWS_SECRET_ACCESS_KEY=... \
  -v "$PWD/restore:/restore" restic/restic:0.19.1 restore latest --target /restore
```

The database dump is in `restore/aether/backup/` and the maildirs are in `restore/aether/vmail/`.

## Bare metal

Running without Docker is possible but is not yet a documented, tested path. To do it, configure the following yourself, using the files under `infrastructure/docker/*` as the reference configuration:

- Debian 12 packages: `postfix postfix-pgsql dovecot-imapd dovecot-lmtpd dovecot-pgsql dovecot-sieve rspamd`
- PostgreSQL 15+ and Valkey
- The `aether` binary (`go build ./cmd/aether`) and the Next.js standalone build
