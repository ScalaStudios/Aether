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

`valkeydata` and `rspamd` hold caches and statistics, which are rebuilt automatically. DKIM keys in Valkey are re-published from PostgreSQL on every API start.

Automated, encrypted backup and verified restore are planned and not provided yet.

## Bare metal

Running without Docker is possible but is not yet a documented, tested path. To do it, configure the following yourself, using the files under `infrastructure/docker/*` as the reference configuration:

- Debian 12 packages: `postfix postfix-pgsql dovecot-imapd dovecot-lmtpd dovecot-pgsql dovecot-sieve rspamd`
- PostgreSQL 15+ and Valkey
- The `aether` binary (`go build ./cmd/aether`) and the Next.js standalone build
