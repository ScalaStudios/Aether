#!/bin/sh
set -eu
: "${AETHER_VALKEY_PASSWORD:?required}"
cat > /etc/rspamd/local.d/redis.conf <<CONF
servers = "${AETHER_VALKEY_ADDR:-valkey:6379}";
password = "${AETHER_VALKEY_PASSWORD}";
CONF
cat > /etc/rspamd/local.d/worker-controller.inc <<CONF
bind_socket = "*:11334";
allow_file_and_shm_inputs = false;
password = "$(rspamadm pw -q -p "${AETHER_RSPAMD_METRICS_PASSWORD:-$(head -c 32 /dev/urandom | base64)}")";
enable_password = "$(rspamadm pw -q -p "$(head -c 32 /dev/urandom | base64)")";
CONF
exec /usr/bin/rspamd -f -u _rspamd -g _rspamd
