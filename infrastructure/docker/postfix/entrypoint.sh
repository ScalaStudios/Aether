#!/bin/sh
set -eu

: "${AETHER_MAIL_HOSTNAME:?required}"
: "${AETHER_MAIL_DB_PASSWORD:?required}"
DB_HOST="${AETHER_DB_HOST:-postgres}"
DB_NAME="${AETHER_DB_NAME:-aether}"
MESSAGE_SIZE_LIMIT="${AETHER_MESSAGE_SIZE_LIMIT:-52428800}"
case "$MESSAGE_SIZE_LIMIT" in
  ''|*[!0-9]*) echo "AETHER_MESSAGE_SIZE_LIMIT must be an integer from 1 to 2147483647" >&2; exit 1 ;;
esac
if [ "$MESSAGE_SIZE_LIMIT" -lt 1 ] || [ "$MESSAGE_SIZE_LIMIT" -gt 2147483647 ]; then
  echo "AETHER_MESSAGE_SIZE_LIMIT must be an integer from 1 to 2147483647" >&2
  exit 1
fi

sed -i \
  -e "s|__HOSTNAME__|${AETHER_MAIL_HOSTNAME}|" \
  -e "s|__RELAYHOST__|${AETHER_RELAYHOST:-}|" \
  -e "s|__REPORTS__|${AETHER_REPORT_LMTP:-api:2424}|" \
  -e "s|__POLICY__|${AETHER_POLICY_SERVICE:-api:10040}|" \
  -e "s|__MILTER__|${AETHER_MILTER_SERVICE:-api:10045}|" \
  -e "s|__MILTER_AFTER__|${AETHER_MILTER_AFTER_SERVICE:-api:10046}|" \
  -e "s|__AUDIT_MILTER__|${AETHER_AUDIT_MILTER_SERVICE:-api:10047}|" \
  -e "s|__MESSAGE_SIZE_LIMIT__|${MESSAGE_SIZE_LIMIT}|" \
  /etc/postfix/main.cf
printf '/^X-Aether-Require-TLS:[[:space:]]*yes/ FILTER smtp-tls:%s\n' "${AETHER_RELAYHOST:-}" > /etc/postfix/milter_header_checks

install -d -m 0750 -o root -g postfix /etc/postfix/pgsql
write_map() {
  umask 027
  cat > "/etc/postfix/pgsql/$1.cf" <<MAP
hosts = ${DB_HOST}
user = aether_mail
password = ${AETHER_MAIL_DB_PASSWORD}
dbname = ${DB_NAME}
query = $(printf '%s' "$2" | tr '\n' ' ')
MAP
  chgrp postfix "/etc/postfix/pgsql/$1.cf"
}
write_map domains "SELECT 1 FROM mail_domains WHERE name = '%s'"
write_map mailboxes "SELECT 1 FROM mail_users WHERE address = '%s'"
write_map aliases "SELECT destination FROM mail_aliases WHERE address = '%s' UNION ALL SELECT address || '.aether-coexistence.invalid' FROM mail_coexistence_dual WHERE address = '%s'"
write_map coexist-recipients "SELECT 1 FROM mail_coexistence_domains WHERE name = '%d' AND '%u' <> '' AND NOT EXISTS (SELECT 1 FROM mail_coexistence_reserved WHERE address IN ('%s', split_part('%u', '+', 1) || '@' || '%d'))"
write_map coexist-transports "SELECT 'coexist:[' || c.host || ']:25' FROM mail_coexistence_domains c WHERE
  (c.name = '%d' AND NOT EXISTS (SELECT 1 FROM mail_aliases WHERE address IN ('%s', split_part('%u', '+', 1) || '@' || '%d'))
    AND NOT EXISTS (SELECT 1 FROM mail_coexistence_reserved WHERE address IN ('%s', split_part('%u', '+', 1) || '@' || '%d')))
  OR (c.name || '.aether-coexistence.invalid' = '%d' AND EXISTS (SELECT 1 FROM mail_coexistence_dual WHERE address IN ('%u' || '@' || c.name, split_part('%u', '+', 1) || '@' || c.name)))"
write_map senders "SELECT login FROM mail_senders WHERE sender = '%s'"
write_map restricted-lists "SELECT 'aether_list_policy' FROM mail_restricted_lists WHERE address = '%s' UNION ALL SELECT '550 5.1.1 Address unavailable' FROM mail_unavailable_personal_aliases WHERE address = '%s'"
write_map journal "SELECT archive_address FROM mail_journal WHERE match = '%s'"
tagged_mailbox="address = split_part('%u', '+', 1) || '@' || COALESCE((SELECT target_name FROM mail_alias_domains WHERE alias_name = '%d'), '%d') AND strpos('%u', '+') > 0"
write_map tagged-mail "SELECT address FROM mail_users WHERE ${tagged_mailbox} AND (tagged_mail IN ('inbox', 'reject') OR substr('%u', strpos('%u', '+') + 1) !~ '^[a-z0-9_-]+\$')"
write_map tagged-rejects "SELECT '550 5.1.1 This mailbox does not accept tagged addresses' FROM mail_users WHERE ${tagged_mailbox} AND tagged_mail = 'reject' AND NOT EXISTS (SELECT 1 FROM mail_aliases WHERE address = '%s')"
write_map hosted-senders "SELECT '%s' FROM mail_domains WHERE name = '%d'"

write_map outbound-transports "SELECT transport FROM mail_outbound_transports WHERE name = lower('%s')"
write_map outbound-relays "SELECT transport FROM mail_outbound_relays WHERE name = lower(regexp_replace('%s', '^.*@', '')) AND position('@' in '%s') > 0"
write_map outbound-tls "SELECT policy FROM mail_outbound_tls WHERE name = lower('%s')"
write_map outbound-tls-required "SELECT policy FROM mail_outbound_tls WHERE name = lower('%s') AND tls_mode IN ('encrypt', 'verify', 'secure')"

sasl_token=$(printf '%s' "$AETHER_MAIL_DB_PASSWORD" | sha256sum)
postconf -P "aether-sender-auth/unix/smtp_sasl_password_maps=socketmap:inet:${AETHER_OUTBOUND_SASL_SERVICE:-api:10041}:${sasl_token%% *}"
chmod 0640 /etc/postfix/master.cf
chgrp postfix /etc/postfix/master.cf

srs_secret=/var/spool/postfix/srs.secret
[ -s "$srs_secret" ] || (umask 077 && head -c 32 /dev/urandom | base64 > "$srs_secret")
loop_secret=/var/spool/postfix/coexist.secret
[ -s "$loop_secret" ] || (umask 077 && head -c 24 /dev/urandom | base64 > "$loop_secret")
loop_marker=$(tr '+/' '-_' < "$loop_secret")
printf '/^Date[[:space:]]*:/ PREPEND X-Aether-Coexistence: %s\n' "$loop_marker" > /etc/postfix/coexist_loop_stamp
printf '/^X-Aether-Coexistence:[[:space:]]*%s[[:space:]]*$/ REJECT 5.4.6 Coexistence routing loop\n' "$loop_marker" > /etc/postfix/coexist_loop_check
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
    smtp_tls_policy_maps=pgsql:/etc/postfix/pgsql/outbound-tls-required.cf \
    smtp_tls_security_level=encrypt
fi

[ -f /etc/aliases ] || echo "postmaster: root" > /etc/aliases
newaliases
cp /etc/resolv.conf /etc/hosts /var/spool/postfix/etc/ 2>/dev/null || true
postfix check
mkdir -p /var/log/postfix
touch /var/log/postfix/mail.log
chmod 0644 /var/log/postfix/mail.log /var/log/postfix/mail.log.* 2>/dev/null || true
tail -n 0 -F /var/log/postfix/mail.log &
(while sleep 86400; do postfix logrotate && sleep 60 && chmod 0644 /var/log/postfix/mail.log /var/log/postfix/mail.log.*; find /var/log/postfix -name 'mail.log.*' -mtime +7 -delete; done) &
[ -z "${AETHER_POSTFIX_API_KEY:-}" ] || (while :; do aether-postfix-api; sleep 5; done) &
exec postfix start-fg
