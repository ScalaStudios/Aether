# Kubernetes (Helm)

The chart in `infrastructure/helm/aether` deploys the same services as the Compose stack. It uses the images that `.github/workflows/images.yml` publishes to `ghcr.io/scalastudios/aether-*`. While the repository is private, those images are private too: add a pull secret and list it in `imagePullSecrets`.

```sh
helm install aether infrastructure/helm/aether -n aether --create-namespace \
  --set publicUrl=https://mail.example.com \
  --set mailHostname=mail.example.com \
  --set mailTLS.certManager.enabled=true
```

## What the chart creates

- **Web and API:** Deployments with two replicas each. The API also serves Prometheus metrics on port 9090.
- **Mail services:** StatefulSets for Postfix and Dovecot, each with one replica and its own persistent volume for the mail queue and maildirs.
- **Supporting services:** Rspamd, Valkey, and Unbound. The bundled PostgreSQL is optional: set `postgresql.enabled=false` and `secrets.databaseUrl` to use your own. That database needs an `aether_mail` role; see `infrastructure/docker/postgres/10-aether-mail-role.sh`.
- **Ingress:** sends `/api`, `/dav`, and `/.well-known/cal|carddav` to the API, and everything else to the web app.
- **Mail Services:** two LoadBalancer Services, `-mail-smtp` (25, 465, 587) and `-mail-imap` (143, 993). They use `externalTrafficPolicy: Local` so that Postfix, Rspamd, and Dovecot see real client addresses.
- **NetworkPolicies:** deny inbound traffic by default, then allow only the connections each service needs. The ingress controller's namespace is selected by `networkPolicy.ingressControllerNamespaceSelector`.
- **A Secret with generated passwords and the encryption key.** Upgrades keep the existing values. Back this Secret up: without `secretKey`, stored DKIM keys and identity provider secrets cannot be decrypted.

## Constraints

- **One release per namespace.** The mail configuration addresses other services by fixed names (`postgres`, `valkey`, `dovecot`, `rspamd`, `postfix`, `unbound`, `api`).
- **One Dovecot replica.** Maildir storage on a ReadWriteOnce volume cannot be shared between replicas.
- **Mail TLS needs a certificate.** Mail TLS comes from the secret named in `mailTLS.secretName`, issued by cert-manager when `mailTLS.certManager.enabled=true`. The mail pods do not start until that secret exists.
- **Backups:** the Compose backup script does not run in Kubernetes. Use your cluster's volume snapshots or a restic CronJob.

## Status

CI checks the chart with `helm lint`, renders it with `helm template`, and validates the result against Kubernetes schemas with `kubeconform`. It has not yet been installed on a real cluster.
