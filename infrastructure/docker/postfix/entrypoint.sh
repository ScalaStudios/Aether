#!/bin/sh
set -eu

: "${AETHER_MAIL_HOSTNAME:?required}"
: "${AETHER_MAIL_DB_PASSWORD:?required}"
DB_HOST="${AETHER_DB_HOST:-postgres}"
DB_NAME="${AETHER_DB_NAME:-aether}"

sed -i \
  -e "s|__HOSTNAME__|${AETHER_MAIL_HOSTNAME}|" \
  -e "s|__RELAYHOST__|${AETHER_RELAYHOST:-}|" \
  /etc/postfix/main.cf

install -d -m 0750 -o root -g postfix /etc/postfix/pgsql
write_map() {
  umask 027
  cat > "/etc/postfix/pgsql/$1.cf" <<MAP
hosts = ${DB_HOST}
user = aether_mail
password = ${AETHER_MAIL_DB_PASSWORD}
dbname = ${DB_NAME}
query = $2
MAP
  chgrp postfix "/etc/postfix/pgsql/$1.cf"
}
write_map domains "SELECT 1 FROM mail_domains WHERE name = '%s'"
write_map mailboxes "SELECT 1 FROM mail_users WHERE address = '%s'"
write_map aliases "SELECT destination FROM mail_aliases WHERE address = '%s'"
write_map senders "SELECT login FROM mail_senders WHERE sender = '%s'"

[ -f /etc/aliases ] || echo "postmaster: root" > /etc/aliases
newaliases
cp /etc/resolv.conf /etc/hosts /var/spool/postfix/etc/ 2>/dev/null || true
postfix check
exec postfix start-fg
