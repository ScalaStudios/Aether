#!/bin/sh
set -eu
: "${AETHER_VALKEY_PASSWORD:?required}"
cat > /etc/rspamd/local.d/redis.conf <<CONF
servers = "${AETHER_VALKEY_ADDR:-valkey:6379}";
password = "${AETHER_VALKEY_PASSWORD}";
CONF
exec /usr/bin/rspamd -f -u _rspamd -g _rspamd
