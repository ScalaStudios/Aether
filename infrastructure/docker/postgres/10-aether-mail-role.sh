#!/bin/sh
set -eu
psql -v ON_ERROR_STOP=1 -v pw="$AETHER_MAIL_DB_PASSWORD" --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
CREATE ROLE aether_mail LOGIN PASSWORD :'pw';
SQL
