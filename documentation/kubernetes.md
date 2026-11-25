# Kubernetes (Helm)

The chart in `infrastructure/helm/aether` deploys the same services as the Compose stack. It uses the images that `.github/workflows/images.yml` publishes to `ghcr.io/scalastudios/aether-*`. While the repository is private, those images are private too: add a pull secret and list it in `imagePullSecrets`.

```sh
helm install aether infrastructure/helm/aether -n aether --create-namespace \
  --set publicUrl=https://mail.example.com \
  --set mailHostname=mail.example.com \
  --set mailTLS.certManager.enabled=true
```

## What the chart creates

- **Web and API:** Deployments with two replicas each. The API also serves Prometheus metrics on port 9090, the distribution list policy service on port 10040, the outbound relay credential service on port 10041, the mail flow milters on ports 10045 and 10046, and the mailbox access audit milter on port 10047, which the NetworkPolicy allows from Postfix only. Postfix allows the API on its Sieve relay port 10025, used to send on approved messages. Dovecot may reach the API on port 8080 for access token checks and mail events.
- **Mail services:** StatefulSets for Postfix and Dovecot, each with one replica and its own persistent volume for the mail queue and maildirs. The Postfix pod also runs the `mail-trace` container, which reads the Postfix log from a shared `emptyDir` volume into the [message trace](message-trace.md); `mailTrace.retentionDays` sets how long records are kept. The Postfix pod also runs a `mail-events` container from the API image. It reads the Postfix log from a shared emptyDir and records delivery events. Dovecot's doveadm HTTP API on port 8089 is reachable from the API pods only, through the NetworkPolicy, and is keyed with the `dovecotApiKey` entry of the Secret. The Postfix queue API on port 8025 is reachable from the API pods only and is keyed with `postfixApiKey`. The same policy lets Dovecot call the API on port 8080 to remove a deleted mailbox's directory from the vmail volume.
- **Supporting services:** Rspamd, Valkey, and Unbound, plus ClamAV with `clamav.enabled=true` and Apache Tika, for searching text inside attachments, with `tika.enabled=true`. The bundled PostgreSQL is optional: set `postgresql.enabled=false` and `secrets.databaseUrl` to use your own. That database needs an `aether_mail` role; see `infrastructure/docker/postgres/10-aether-mail-role.sh`.
- **Ingress:** sends `/api`, `/dav`, `/jmap`, `/.well-known/jmap`, and `/.well-known/cal|carddav` to the API, and everything else to the web app. The ingress keeps the live-event stream open (`proxy-read-timeout: 3600`) and does not buffer it. Web Push keys are generated in the database. To pin them, or to enable the iOS push slice, set `AETHER_VAPID_PUBLIC_KEY`, `AETHER_VAPID_PRIVATE_KEY`, `AETHER_APNS_CERT_FILE` and `AETHER_APNS_KEY_FILE` through `api.extraEnv` and mount the certificate files into the API pods. See [Notifications](notifications.md). The body limit follows `maxLinkBytes` (default 2 GB) and the proxy read and send timeouts are 30 minutes, so a download link can be uploaded and fetched. Download-link files are PostgreSQL large objects, so the chart adds no volume for them; a database backup includes them.
- **Mail Services:** two LoadBalancer Services, `-mail-smtp` (25, 465, 587) and `-mail-imap` (143, 993). They use `externalTrafficPolicy: Local` so that Postfix, Rspamd, and Dovecot see real client addresses.
- **NetworkPolicies:** deny inbound traffic by default, then allow only the connections each service needs. The ingress controller's namespace is selected by `networkPolicy.ingressControllerNamespaceSelector`.
- **A Secret with generated passwords and the encryption key.** Upgrades keep the existing values. Back up the original `secretKey` in protected storage separate from volume snapshots. Losing or replacing it makes encrypted mail, DKIM keys, and identity provider secrets unreadable until the original value is restored.

## Constraints

- **One release per namespace.** The mail configuration addresses other services by fixed names (`postgres`, `valkey`, `dovecot`, `rspamd`, `postfix`, `unbound`, `api`).
- **One Dovecot replica.** Maildir storage on a ReadWriteOnce volume cannot be shared between replicas.
- **Mail TLS needs a certificate.** Mail TLS comes from the secret named in `mailTLS.secretName`, issued by cert-manager when `mailTLS.certManager.enabled=true`. The mail pods do not start until that secret exists.
- **Backups:** the Compose backup script does not run in Kubernetes. Use your cluster's volume snapshots or a restic CronJob. A database dump includes download-link files.
- **Size ceilings:** `messageSizeLimit` (default 52428800) is Postfix's message size and the largest message an organization may allow. `maxLinkBytes` (default 2147483648) and `maxStorageBytes` (default 107374182400) cap download links. The API and Postfix receive these as `AETHER_MESSAGE_SIZE_LIMIT`, `AETHER_MAX_LINK_BYTES` and `AETHER_MAX_STORAGE_BYTES`.

## Encrypt existing mail

The upgraded Dovecot image encrypts new Maildir messages and can still read plaintext messages. To migrate existing mail, take a volume snapshot and back up the Secret before upgrading. Set `encryptExistingMail: true` in your Helm values for the upgrade. The Dovecot StatefulSet replaces its single pod and the `encrypt-mail` init container rewrites the message files before Dovecot starts. Keep other writers off the volume until it finishes.

Watch the rollout with `kubectl rollout status statefulset/dovecot -n aether` and check progress or errors with `kubectl logs dovecot-0 -n aether -c encrypt-mail`. The init container can retry after an interruption and skips files already encrypted. Once the rollout succeeds, remove the setting or set it to `false` at the next upgrade to avoid another scan. Re-enable it when restoring a snapshot containing plaintext mail.

The key and storage considerations are the same as for [Compose installations](installation.md#encrypt-existing-mail).

## Status

CI checks the chart with `helm lint`, renders it with `helm template`, and validates the result against Kubernetes schemas with `kubeconform`. It has not yet been installed on a real cluster.

To serve MTA-STS policies, list your mail domains in `mtaSts.domains`. The ingress then routes `mta-sts.<domain>` to the API and includes those hosts in its TLS certificate. Set `mtaSts.mode` to `enforce` once TLS reports show no failures.

DMARC aggregate reports are sent when `reports.enabled` is true. The API submits them to Postfix on port 10025, which the NetworkPolicy allows, and Rspamd signs them. TLS reports are not sent: the chart does not mount Postfix's mail log into the API, and a report built without that log would describe the wrong servers. Set `reports.from` and `reports.orgName` to override the sending address and the organization name.
