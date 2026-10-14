#!/bin/sh
set -eu
cert="${1:?usage: sync-tls-cert.sh <certificate.crt> <private.key> [compose-dir]}"
key="${2:?usage: sync-tls-cert.sh <certificate.crt> <private.key> [compose-dir]}"
dir="${3:-$(dirname "$0")/..}"
cd "$dir"
volume="$(docker volume inspect "$(basename "$(pwd)")_tls" --format '{{.Mountpoint}}' 2>/dev/null || docker volume inspect aether_tls --format '{{.Mountpoint}}')"
if cmp -s "$cert" "$volume/fullchain.pem" && cmp -s "$key" "$volume/privkey.pem"; then
  exit 0
fi
install -m 0644 "$cert" "$volume/fullchain.pem"
install -m 0600 "$key" "$volume/privkey.pem"
docker compose exec -T postfix postfix reload
docker compose exec -T dovecot doveadm reload
echo "Installed new TLS certificate for mail services."
