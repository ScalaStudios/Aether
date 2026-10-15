#!/bin/sh
set -eu
usage="usage: restore.sh --yes <backup-dir | restic:<snapshot>> [compose-dir]"
[ "${1:-}" = "--yes" ] || { echo "$usage" >&2; echo "This replaces the database and all mail. Pass --yes to continue." >&2; exit 2; }
source="${2:?$usage}"
dir="${3:-$(dirname "$0")/..}"
cd "$dir"
set -a
. ./.env
set +a
vmail="${COMPOSE_PROJECT_NAME:-aether}_vmail"
staging="${COMPOSE_PROJECT_NAME:-aether}_restore-staging"

case "$source" in
restic:*)
  : "${AETHER_BACKUP_REPOSITORY:?AETHER_BACKUP_REPOSITORY is not set in .env}"
  docker volume rm -f "$staging" >/dev/null
  trap 'docker volume rm -f "$staging" >/dev/null' EXIT
  docker run --rm --network host \
    -e RESTIC_REPOSITORY="$AETHER_BACKUP_REPOSITORY" -e RESTIC_PASSWORD="${AETHER_BACKUP_PASSWORD:?}" \
    -e AWS_ACCESS_KEY_ID="${AETHER_BACKUP_S3_ACCESS_KEY:-}" -e AWS_SECRET_ACCESS_KEY="${AETHER_BACKUP_S3_SECRET_KEY:-}" \
    -e AWS_DEFAULT_REGION="${AETHER_BACKUP_S3_REGION:-auto}" \
    -v "$staging:/restore" restic/restic:0.19.1 restore "${source#restic:}" --host aether --tag aether --target /restore
  mount="$staging"
  dump=/source/aether/backup/aether.pgdump
  env_file=/source/aether/backup/env
  copy_mail='cp -a /source/aether/vmail/. /vmail/'
  ;;
*)
  mount="$(cd "$source" && pwd)"
  dump=/source/aether.pgdump
  env_file=/source/env
  copy_mail='tar -C /vmail -xzpf /source/vmail.tar.gz'
  ;;
esac

in_source() { docker run --rm -i -v "$mount:/source:ro" -v "$vmail:/vmail" alpine:3 sh -c "$1"; }

in_source "test -s $dump && { test -d /source/aether/vmail || test -f /source/vmail.tar.gz; }" || { echo "$source does not contain a database dump and mail" >&2; exit 1; }
saved_key="$(in_source "test -f $env_file && sed -n 's/^AETHER_SECRET_KEY=//p' $env_file || true")"
if [ -n "$saved_key" ] && [ "$saved_key" != "$AETHER_SECRET_KEY" ]; then
  echo "AETHER_SECRET_KEY in .env differs from the backup. Restore the backup's env file as .env first, or DKIM keys and OIDC secrets cannot be decrypted." >&2
  exit 1
fi

docker compose stop web api postfix dovecot rspamd
docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U aether -d postgres \
  -c "DROP DATABASE IF EXISTS aether WITH (FORCE)" -c "CREATE DATABASE aether OWNER aether"
in_source "cat $dump" | docker compose exec -T postgres pg_restore -U aether -d aether --no-owner --exit-on-error
in_source "find /vmail -mindepth 1 -delete && $copy_mail"

docker compose up -d
echo "Restored from $source"
