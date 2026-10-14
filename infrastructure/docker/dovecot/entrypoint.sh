#!/bin/sh
set -eu

: "${AETHER_MAIL_DB_PASSWORD:?required}"
: "${AETHER_DOVECOT_MASTER_PASSWORD:?required}"
: "${AETHER_SIEVE_RELAY_PASSWORD:?required}"
DB_HOST="${AETHER_DB_HOST:-postgres}"
DB_NAME="${AETHER_DB_NAME:-aether}"
MASTER_USER="${AETHER_DOVECOT_MASTER_USER:-aether-webmail}"
MASTER_NETS="${AETHER_DOVECOT_MASTER_ALLOW_NETS:-10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.1}"

install -d -m 0750 -o root -g dovecot /run/dovecot-secrets
umask 027

cat > /run/dovecot-secrets/sql.conf.ext <<SQL
driver = pgsql
connect = host=${DB_HOST} dbname=${DB_NAME} user=aether_mail password=${AETHER_MAIL_DB_PASSWORD}
password_query = SELECT address AS user, NULL AS password, 'Y' AS nopassword FROM mail_credentials WHERE address = '%u' AND token_hash = '%{sha256:password}' LIMIT 1
user_query = SELECT '/var/vmail/' || id AS home, 'vmail' AS uid, 'vmail' AS gid, CASE WHEN quota_bytes > 0 THEN '*:bytes=' || quota_bytes END AS quota_rule FROM mail_users WHERE address = '%u'
iterate_query = SELECT address AS user FROM mail_users
SQL

printf '%s:%s::::::allow_nets=%s\n' "$MASTER_USER" "$(doveadm pw -s SHA512-CRYPT -p "$AETHER_DOVECOT_MASTER_PASSWORD")" "$MASTER_NETS" > /run/dovecot-secrets/master-users
printf 'aether-sieve-relay:%s::::::allow_nets=%s\n' "$(doveadm pw -s SHA512-CRYPT -p "$AETHER_SIEVE_RELAY_PASSWORD")" "$MASTER_NETS" > /run/dovecot-secrets/relay-users

install -d -m 0700 -o vmail -g vmail /run/aether-msmtp
cat > /run/aether-msmtp/msmtprc <<MSMTP
defaults
logfile -
account default
host ${AETHER_SIEVE_RELAY_HOST:-postfix}
port 10025
tls on
tls_starttls on
tls_trust_file /tls/fullchain.pem
tls_host_override ${AETHER_MAIL_HOSTNAME:?required}
auth plain
user aether-sieve-relay
password ${AETHER_SIEVE_RELAY_PASSWORD}
MSMTP
chown vmail:vmail /run/aether-msmtp/msmtprc
chmod 0600 /run/aether-msmtp/msmtprc
chgrp dovecot /run/dovecot-secrets/*
chmod 0640 /run/dovecot-secrets/*

install -d -o vmail -g vmail -m 0750 /var/vmail

exec dovecot -F
