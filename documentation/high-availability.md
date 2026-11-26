# High availability

By default Aether runs every service on one host. This page shows how to spread it over several hosts, one step at a time, and what is and is not safe today. Each step is independent: take the ones that remove the outage you care about most.

Each section says how far it has been checked:

- **Checked in code:** read in the Aether source and covered by its tests.
- **Configuration checked:** the settings come from the upstream documentation and were checked with the versions Aether ships (Dovecot 2.3.19.1 and Postfix 3.7.11 from Debian 12), but the setup has not run end to end.
- **Untested:** a design that has not been run.

## What can run more than once

| Component | More than one instance | Why |
| --- | --- | --- |
| Web (`web`) | Yes | A static Next.js app with no server-side state. |
| API (`api`) | Yes | See [API and web](#step-1-api-and-web-behind-a-load-balancer). |
| Postfix | One primary, plus backup MX hosts | Each Postfix generates its own SRS secret in its spool, so two primaries cannot reverse each other's rewritten addresses. A backup MX does not rewrite, so it is not affected. |
| Dovecot | One active instance | The API, Postfix and the session kicks all talk to one Dovecot address. Failover means moving the mail storage, or replicating it, to a standby. |
| PostgreSQL | One primary, with replicas for failover | Every component writes to or reads from the primary through one address. |
| Valkey | One | The API takes one address (`AETHER_VALKEY_ADDR`) and has no Sentinel or cluster support. |
| Rspamd | One per Postfix | Untested with more than one. All instances would share Valkey. |

## Step 1: API and web behind a load balancer

Status: **checked in code** for the API; the host layout is **untested**.

The API keeps no state of its own between requests:

- Sessions, CSRF tokens and OpenID Connect sign-in state are rows in PostgreSQL. Any replica can serve any request, so the load balancer needs no sticky sessions.
- Rate limits, the cache of checked mail access tokens, DKIM keys for Rspamd and requests to cancel running mail imports live in Valkey.
- Every API process runs the background workers. Each claims its rows before working on them, so two replicas never process the same item: scheduled messages with a claim committed before the send, webhook deliveries with a two-minute lease, snoozed messages with a five-minute claim, shared mailbox reconciliation with a two-minute lease, and IMAP session kicks with a 30-second claim.
- Database migrations run at startup inside a transaction behind an advisory lock, so replicas that start together apply them once.
- A mail import runs inside the replica that started it. That replica holds a PostgreSQL advisory lock for the mailbox, so every replica sees the import as running and refuses a second import into the same mailbox. Cancelling on any replica sets a flag in Valkey that the running import checks every two seconds. If that replica stops, its database session ends, the lock is released and the import shows as interrupted; start it again, and messages already copied are skipped.
- The Postfix policy service (port 10040) and the DMARC report receiver (port 2424) answer each connection on their own, so Postfix can reach any replica.

### With Docker Compose

Keep the full stack on the first host (the mail host). On each additional app host, run only the API and the web app, pointed at the mail host over a private network. The connections to PostgreSQL, Valkey, the doveadm HTTP API, the Postfix policy service and the DMARC report receiver are not encrypted, so use a private network or a VPN such as WireGuard between the hosts.

1. On the mail host, publish the data services on its private address only, in `compose.override.yml`. Replace `10.0.0.1` with that address:

   ```yaml
   services:
     postgres:
       ports: ["10.0.0.1:5432:5432"]
     valkey:
       ports: ["10.0.0.1:6379:6379"]
     dovecot:
       ports: ["10.0.0.1:4190:4190", "10.0.0.1:8089:8089"]
   ```

   Dovecot already publishes 143 and 993, and Postfix 587. Dovecot accepts the API's master login from `10.0.0.0/8`, `172.16.0.0/12` and `192.168.0.0/16` by default; if your private network uses other addresses, set `AETHER_DOVECOT_MASTER_ALLOW_NETS` on the `dovecot` service.

2. On each app host, clone the same release and copy `.env` from the mail host. Every API instance needs the same `AETHER_SECRET_KEY`, and the same passwords for PostgreSQL, Valkey and Dovecot.

3. Copy the mail certificate chain to the app host. The API checks Dovecot's and Postfix's certificate against `/tls/fullchain.pem`:

   ```sh
   docker volume create aether_tls
   docker run --rm -i -v aether_tls:/tls alpine sh -c 'cat > /tls/fullchain.pem' < fullchain.pem
   ```

4. On the app host, map the service names to the mail host in `compose.override.yml`, so the defaults in `compose.yml` keep working:

   ```yaml
   services:
     api:
       extra_hosts:
         - "postgres:10.0.0.1"
         - "valkey:10.0.0.1"
         - "dovecot:10.0.0.1"
         - "postfix:10.0.0.1"
   ```

5. Start only the API and the web app, without their dependencies:

   ```sh
   docker compose up -d --no-deps api web
   ```

6. Put a load balancer in front of every host's API and web app, with the routes from `infrastructure/docker/caddy/Caddyfile`: `/api`, `/dav`, `/.well-known/caldav`, `/.well-known/carddav`, the autoconfig and autodiscover paths and `/.well-known/mta-sts.txt` go to the API on port 8080, and everything else to the web app on port 3000. Check the API with `GET /readyz`, which fails when PostgreSQL or Valkey cannot be reached, and the web app with a TCP check. If the load balancer terminates TLS, keep `AETHER_TRUST_PROXY=true` and make sure it sets `X-Forwarded-For`. If you keep Caddy instead, run it on one host only, or give every instance the same certificate storage.

This removes the app hosts as a single point of failure. The mail host still is one: while it is down, the API instances fail their readiness check. The next steps remove the services on it one by one.

### With Kubernetes

The Helm chart already runs two API and two web replicas (`replicas.api`, `replicas.web`). Two values, both off by default, keep them apart:

```sh
helm upgrade aether infrastructure/helm/aether --reuse-values \
  --set highAvailability.podDisruptionBudgets=true \
  --set highAvailability.spreadAcrossNodes=true
```

- `podDisruptionBudgets` adds a PodDisruptionBudget with `maxUnavailable: 1` for the API and the web app, so a node drain takes down one replica at a time.
- `spreadAcrossNodes` adds a preferred pod anti-affinity on `kubernetes.io/hostname`, so the scheduler places replicas on different nodes when it can.

## Step 2: PostgreSQL with a replica

Status: **untested**.

Aether needs one writable PostgreSQL 17 database. Run it with streaming replication and a failover manager, such as Patroni, or CloudNativePG on Kubernetes, and give every component one address that always points at the primary: a virtual IP, a proxy, or the operator's read-write Service.

- The API connects through `AETHER_DATABASE_URL`. Its connection pool replaces broken connections, so after a failover it continues once the address points at the new primary.
- Postfix and Dovecot connect as the `aether_mail` role to `AETHER_DB_HOST` (default `postgres`). Create that role on the new cluster with `infrastructure/docker/postgres/10-aether-mail-role.sh`; replication copies it to the replicas.
- With the Helm chart, set `postgresql.enabled=false`, `secrets.databaseUrl` to the read-write address, and `postgresql.externalHost` to the same host, so Postfix and Dovecot use it too. Before this release, Postfix and Dovecot always connected to `postgres`.
- The connection string in `compose.yml` uses `sslmode=disable`. Across hosts, either keep the database on a private network or change the URL to require TLS.

A read replica on the backup MX host lets the backup check recipients while the primary database is down; see the next step.

## Step 3: a backup MX

Status: **configuration checked** against Postfix's [Standard Configuration README](https://www.postfix.org/STANDARD_CONFIGURATION_README.html), and with Postfix 3.7.11 using fixed tables in place of the database: `postfix check` passes, listed recipients and `+tag` addresses are accepted, and unknown recipients are refused. **Untested** with Aether's database.

Other mail servers already queue mail for several days while your MX is down. A backup MX on another host takes the mail during the outage instead, keeps it in its queue, and forwards it to the primary when the primary is back. It does not deliver to mailboxes and does not need Dovecot.

1. Install Postfix with PostgreSQL support on a Debian 12 host: `apt install postfix postfix-pgsql`.

2. Give the host read access to Aether's mail views: allow the `aether_mail` role from this host in `pg_hba.conf`, preferably on a streaming replica on the same host.

3. Write three lookup tables in `/etc/postfix/pgsql/`, readable only by root and the `postfix` group. They use the same views and queries as the primary's `infrastructure/docker/postfix/entrypoint.sh`. `domains.cf`:

   ```
   hosts = 10.0.0.2
   user = aether_mail
   password = <AETHER_MAIL_DB_PASSWORD>
   dbname = aether
   query = SELECT 1 FROM mail_domains WHERE name = '%s'
   ```

   `mailboxes.cf` is the same with `query = SELECT 1 FROM mail_users WHERE address = '%s'`, and `aliases.cf` with `query = SELECT destination FROM mail_aliases WHERE address = '%s'`.

4. Add to `/etc/postfix/main.cf`:

   ```
   myhostname = mx2.example.com
   mydestination =
   recipient_delimiter = +
   relay_domains = pgsql:/etc/postfix/pgsql/domains.cf
   relay_recipient_maps = pgsql:/etc/postfix/pgsql/mailboxes.cf, pgsql:/etc/postfix/pgsql/aliases.cf
   smtpd_relay_restrictions = permit_mynetworks, reject_unauth_destination
   smtpd_tls_chain_files = /etc/ssl/private/mx2.key, /etc/ssl/certs/mx2.fullchain.pem
   smtpd_tls_security_level = may
   smtp_tls_security_level = may
   ```

   `relay_recipient_maps` makes the backup refuse unknown recipients during the SMTP session, instead of accepting them and bouncing later. `recipient_delimiter` must match the primary's, so that `alice+tag@example.com` is checked as `alice@example.com`; without it the backup refuses those addresses permanently. If the host receives mail through NAT or a proxy that presents another public address, add that address as `proxy_interfaces`, so Postfix recognizes itself in the MX records. The certificate must be publicly trusted and name `mx2.example.com` once the MTA-STS policy is in `enforce` mode. If it cannot reach the database, Postfix answers with a temporary error and the sender retries. Mail stays in the queue for `maximal_queue_lifetime`, five days by default.

5. Publish a second MX record with a higher preference for each domain, such as `MX 20 mx2.example.com.`, next to `MX 10 mail.example.com.`. A host that is a lower-priority MX for a domain forwards that domain's mail to the better MX hosts only, so the backup delivers to the primary.

6. Set `AETHER_BACKUP_MX=mx2.example.com` on the API (comma-separated for several hosts; with Helm, through `api.extraEnv`) and restart it. The MTA-STS policy then lists the backup next to `AETHER_MAIL_HOSTNAME`. The policy id changes with it, so update each domain's `_mta-sts` TXT record with the id shown on the domain's DNS page. Without this, senders that enforce MTA-STS skip the backup.

Limits of a backup MX today:

- Postfix on the primary passes forwarded mail to Rspamd with the backup's address as the client. SPF and the IP reputation checks therefore judge the backup host, not the original sender, so mail through the backup is filtered less well and may be scored wrongly.
- The backup runs no spam filter of its own, so spam sent to the backup on purpose reaches the primary's queue.
- Restricted distribution lists are still enforced: the primary checks the envelope sender when the backup forwards the message, and the backup returns any rejection to the sender.

## Step 4: mailboxes that survive the mail host

Dovecot keeps mail in Maildir under `/var/vmail`, one directory per mailbox named after its id in PostgreSQL. There are two ways to have that data on a second host.

### Recommended: one Dovecot on storage that can move

Status: **untested**.

Run one Dovecot, and keep `/var/vmail` on storage that a standby host can mount after a failure: a replicated block device such as DRBD or Ceph RBD, or a network file system. Start Dovecot on the standby only after the first host is fenced, so two Dovecot processes never write the same mailboxes. Then point `dovecot` at the standby: move the private address, or change the `extra_hosts` entries and `AETHER_IMAP_ADDR` and `AETHER_DOVECOT_API_URL` on the API hosts.

This keeps everything Dovecot stores on one volume, including `/var/vmail/shared-mailboxes.db`, which lists the shared mailboxes each person can see. Dovecot 2.4 removed the replicator and names shared storage as its replacement, so this path also survives a future Dovecot upgrade.

On Kubernetes, the Dovecot StatefulSet does the same on a ReadWriteOnce volume from a replicated storage class. If its node fails, Kubernetes does not start the pod elsewhere until the node is removed from the cluster or the pod is force-deleted.

### Not recommended yet: dsync replication

Status: **configuration checked** against [Replication with dsync](https://doc.dovecot.org/2.3/configuration_manual/replication/) and parsed by Dovecot 2.3.19.1; **untested** between two hosts.

Dovecot 2.3 can replicate each mailbox to a second Dovecot with `dsync`, in both directions. Two facts weigh against it for Aether today:

- Dovecot's documentation says shared folder replication does not work correctly and can create duplicate messages. Aether's shared mailboxes are shared folders.
- Dovecot 2.4 removed the replicator. A setup built on it has to change before Dovecot can be upgraded.

If you use it anyway, for example in an installation without shared mailboxes, Aether's Dovecot reads every `*.conf` file in `/etc/dovecot/local.d/` before its own configuration. Mount a file such as this one on both hosts, with `mail_replica` pointing at the other host:

```
mail_plugins = $mail_plugins notify replication

service replicator {
  process_min_avail = 1
  unix_listener replicator-doveadm {
    mode = 0600
    user = vmail
  }
}

service aggregator {
  fifo_listener replication-notify-fifo {
    user = vmail
  }
  unix_listener replication-notify {
    user = vmail
  }
}

service doveadm {
  inet_listener dsync {
    port = 12346
    ssl = yes
  }
}

doveadm_port = 12346
doveadm_password = <a long random secret, the same on both hosts>
replication_dsync_parameters = -d -l 30 -U
ssl_client_ca_file = /etc/ssl/certs/ca-certificates.crt

plugin {
  mail_replica = tcps:mail2.example.com
}
```

- The file sets `mail_plugins` before Aether's configuration on purpose: Dovecot does not apply a global `mail_plugins` set after the `protocol imap` and `protocol lmtp` blocks to those blocks.
- Port 12345, the port in Dovecot's examples, is already Dovecot's authentication service for Postfix in Aether, so dsync uses 12346. Publish it to the other host only.
- `replication_dsync_parameters` leaves out the default `-N`. Without it, `doveadm sync` skips namespaces that set their own location, which is how Aether's `Shared/` namespace is configured, so each shared mailbox is replicated once, as its own account, rather than once for every member.
- `tcps:` checks the other host's certificate against `ssl_client_ca_file`, using Dovecot's mail certificate, so that certificate must name the host given in `mail_replica`. The two hosts also need different host names.
- `/var/vmail/shared-mailboxes.db` is outside every mailbox and is not replicated. Whether Sieve scripts and ACLs replicate has not been checked; compare `doveadm sieve list -u <address>` and `doveadm acl get -u <address> INBOX` on both hosts before relying on it.
- Check progress with `doveadm replicator status '*'`.

## Valkey

Status: **checked in code**.

Valkey holds data that the rest of the system can rebuild: rate limit counters, the mail access token cache, mail import cancel requests, and Rspamd's DKIM and ARC keys and sender rate limits. The API publishes every DKIM key to Valkey again each time it starts.

It is still a dependency for availability:

- The API's `/readyz` fails without Valkey, so every API replica leaves the load balancer while Valkey is down.
- Rate limit checks fail closed: sign-in, sending and API key requests are refused while Valkey is unreachable.
- A mail import cannot be cancelled while Valkey is unreachable; it keeps running until it finishes or Valkey is back.
- Rspamd signs outgoing mail with the keys in Valkey. After Valkey loses its data, restart one API instance to publish the keys again; until then, outgoing mail is not DKIM-signed.

The API cannot follow a Sentinel or cluster failover by itself. Keep Valkey's restart time short, or put it behind an address that always points at the current primary, such as a proxy that checks the `role` of each instance.

## What is not safe today

- Two active Dovecot instances on the same mailboxes.
- Two primary Postfix instances: each generates its own SRS secret, so a bounce to an address one rewrote is refused by the other.
- dsync replication with shared mailboxes, as above.
- Several Caddy instances with separate certificate storage: each obtains its own certificates and may hit the certificate authority's rate limits.

## Kubernetes status

CI runs `helm lint`, `helm template` and `kubeconform` on the chart with its default values. The `highAvailability` values and `postgresql.externalHost` were checked the same way by hand. The chart has not been installed on a real cluster.
