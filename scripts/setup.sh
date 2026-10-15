#!/bin/sh
set -eu
cd "$(dirname "$0")/.."

profile="${1:-production}"
if [ -f .env ]; then
  echo ".env already exists; leaving it unchanged."
  exit 0
fi

rand() { head -c "$1" /dev/urandom | base64 | tr -d '\n/+=' | cut -c1-"$2"; }

cp .env.example .env
if [ "$profile" = "dev" ]; then
  sed -i \
    -e 's|^AETHER_PUBLIC_URL=.*|AETHER_PUBLIC_URL=http://localhost:8080|' \
    -e 's|^AETHER_SITE_ADDRESS=.*|AETHER_SITE_ADDRESS=:80|' \
    -e 's|^AETHER_MAIL_HOSTNAME=.*|AETHER_MAIL_HOSTNAME=mail.aether.test|' \
    -e 's|^AETHER_OIDC_ALLOW_HTTP=.*|AETHER_OIDC_ALLOW_HTTP=true|' \
    -e 's|^AETHER_WEBHOOK_ALLOW_PRIVATE=.*|AETHER_WEBHOOK_ALLOW_PRIVATE=true|' \
    -e 's|^AETHER_PORT_HTTP=.*|AETHER_PORT_HTTP=8080|' \
    -e 's|^AETHER_PORT_HTTPS=.*|AETHER_PORT_HTTPS=8443|' \
    -e 's|^AETHER_PORT_SMTP=.*|AETHER_PORT_SMTP=2525|' \
    -e 's|^AETHER_PORT_SUBMISSION=.*|AETHER_PORT_SUBMISSION=5870|' \
    -e 's|^AETHER_PORT_SUBMISSIONS=.*|AETHER_PORT_SUBMISSIONS=4650|' \
    -e 's|^AETHER_PORT_IMAP=.*|AETHER_PORT_IMAP=1430|' \
    -e 's|^AETHER_PORT_IMAPS=.*|AETHER_PORT_IMAPS=9930|' \
    .env
fi
sed -i \
  -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$(rand 48 40)|" \
  -e "s|^AETHER_MAIL_DB_PASSWORD=.*|AETHER_MAIL_DB_PASSWORD=$(rand 48 40)|" \
  -e "s|^AETHER_VALKEY_PASSWORD=.*|AETHER_VALKEY_PASSWORD=$(rand 48 40)|" \
  -e "s|^AETHER_DOVECOT_MASTER_PASSWORD=.*|AETHER_DOVECOT_MASTER_PASSWORD=$(rand 48 40)|" \
  -e "s|^AETHER_SIEVE_RELAY_PASSWORD=.*|AETHER_SIEVE_RELAY_PASSWORD=$(rand 48 40)|" \
  -e "s|^AETHER_GRAFANA_PASSWORD=.*|AETHER_GRAFANA_PASSWORD=$(rand 48 32)|" \
  -e "s|^AETHER_RSPAMD_METRICS_PASSWORD=.*|AETHER_RSPAMD_METRICS_PASSWORD=$(rand 48 40)|" \
  -e "s|^AETHER_SECRET_KEY=.*|AETHER_SECRET_KEY=$(head -c 32 /dev/urandom | base64)|" \
  .env
chmod 600 .env
echo "Wrote .env ($profile profile). Review the hostnames before starting: docker compose up -d --build"
