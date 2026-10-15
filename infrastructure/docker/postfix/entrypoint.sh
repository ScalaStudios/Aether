#!/bin/sh
set -eu

: "${AETHER_MAIL_HOSTNAME:?required}"
: "${AETHER_MAIL_DB_PASSWORD:?required}"
DB_HOST="${AETHER_DB_HOST:-postgres}"
DB_NAME="${AETHER_DB_NAME:-aether}"

sed -i \
  -e "s|__HOSTNAME__|${AETHER_MAIL_HOSTNAME}|" \
  -e "s|__RELAYHOST__|${AETHER_RELAYHOST:-}|" \
  -e "s|__REPORTS__|${AETHER_REPORT_LMTP:-api:2424}|" \
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
write_map hosted-senders "SELECT '%s' FROM mail_domains WHERE name = '%d'"

srs_secret=/var/spool/postfix/srs.secret
[ -s "$srs_secret" ] || (umask 077 && head -c 32 /dev/urandom | base64 > "$srs_secret")
postsrsd -s"$srs_secret" -d"$AETHER_MAIL_HOSTNAME" -l127.0.0.1 -4 -upostsrsd -D

if [ -n "${AETHER_RELAYHOST:-}" ] && [ -n "${AETHER_RELAY_USER:-}" ]; then
  : "${AETHER_RELAY_PASSWORD:?required when AETHER_RELAY_USER is set}"
  umask 077
  printf '%s %s:%s\n' "$AETHER_RELAYHOST" "$AETHER_RELAY_USER" "$AETHER_RELAY_PASSWORD" > /etc/postfix/sasl_passwd
  postmap /etc/postfix/sasl_passwd
  postconf -e \
    smtp_sasl_auth_enable=yes \
    smtp_sasl_password_maps=hash:/etc/postfix/sasl_passwd \
    smtp_sasl_security_options=noanonymous \
    smtp_sasl_tls_security_options=noanonymous \
    smtp_tls_security_level=encrypt
fi

[ -f /etc/aliases ] || echo "postmaster: root" > /etc/aliases
newaliases
cp /etc/resolv.conf /etc/hosts /var/spool/postfix/etc/ 2>/dev/null || true
postfix check
mkdir -p /var/log/postfix
touch /var/log/postfix/mail.log
tail -n 0 -F /var/log/postfix/mail.log &
(while sleep 86400; do postfix logrotate; find /var/log/postfix -name 'mail.log.*' -mtime +7 -delete; done) &
exec postfix start-fg
