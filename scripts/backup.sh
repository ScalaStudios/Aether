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
vmail="$(docker volume inspect "$(basename "$(pwd)")_vmail" --format '{{.Mountpoint}}')"
tar -C "$vmail" -czf "$out/vmail.tar.gz" .
cp .env "$out/env"
pg_size=$(stat -c %s "$out/aether.pgdump")
[ "$pg_size" -gt 0 ] || { echo "database dump is empty" >&2; exit 1; }
tar -tzf "$out/vmail.tar.gz" >/dev/null
find "$target" -mindepth 1 -maxdepth 1 -type d -mtime +"$keep" -exec rm -rf {} +
echo "Backup written to $out"
