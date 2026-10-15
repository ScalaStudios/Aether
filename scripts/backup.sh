#!/bin/sh
set -eu
target="${1:?usage: backup.sh <target-dir> [keep-days] [compose-dir]}"
keep="${2:-14}"
dir="${3:-$(dirname "$0")/..}"
cd "$dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
out="$target/$stamp"
umask 077
mkdir -p "$out"
docker compose exec -T postgres pg_dump -U aether -d aether --format=custom > "$out/aether.pgdump"
vmail="${COMPOSE_PROJECT_NAME:-aether}_vmail"
docker run --rm -v "$vmail:/vmail:ro" alpine:3 tar -C /vmail -czf - . > "$out/vmail.tar.gz"
cp .env "$out/env"
pg_size=$(stat -c %s "$out/aether.pgdump")
[ "$pg_size" -gt 0 ] || { echo "database dump is empty" >&2; exit 1; }
tar -tzf "$out/vmail.tar.gz" >/dev/null
find "$target" -mindepth 1 -maxdepth 1 -type d -mtime +"$keep" -exec rm -rf {} +
echo "Backup written to $out"

set -a
. ./.env
set +a
[ -n "${AETHER_BACKUP_REPOSITORY:-}" ] || exit 0
: "${AETHER_BACKUP_PASSWORD:?AETHER_BACKUP_PASSWORD is required when AETHER_BACKUP_REPOSITORY is set}"
export RESTIC_REPOSITORY="$AETHER_BACKUP_REPOSITORY" RESTIC_PASSWORD="$AETHER_BACKUP_PASSWORD"
export AWS_ACCESS_KEY_ID="${AETHER_BACKUP_S3_ACCESS_KEY:-}" AWS_SECRET_ACCESS_KEY="${AETHER_BACKUP_S3_SECRET_KEY:-}" AWS_DEFAULT_REGION="${AETHER_BACKUP_S3_REGION:-auto}"
restic() {
  docker run --rm --network host \
    -e RESTIC_REPOSITORY -e RESTIC_PASSWORD -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_DEFAULT_REGION \
    -v "$out:/aether/backup:ro" -v "$vmail:/aether/vmail:ro" -v aether-restic-cache:/root/.cache/restic \
    restic/restic:0.19.1 "$@"
}
restic cat config >/dev/null 2>&1 || restic init
restic backup --host aether --tag aether --exclude /aether/backup/vmail.tar.gz /aether
restic forget --host aether --tag aether --keep-daily "$keep" --keep-weekly 8 --keep-monthly 12 --prune
restic check
echo "Off-site copy stored in $AETHER_BACKUP_REPOSITORY"
