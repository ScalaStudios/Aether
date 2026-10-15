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
if [ -n "${AETHER_CLAMAV_ADDR:-}" ]; then
  cat > /etc/rspamd/local.d/antivirus.conf <<CONF
clamav {
  type = "clamav";
  servers = "${AETHER_CLAMAV_ADDR}";
  action = "reject";
  message = "Message rejected: a virus was found (\${VIRUS})";
  scan_mime_parts = true;
  scan_text_mime = false;
  symbol = "CLAM_VIRUS";
  log_clean = false;
}
CONF
else
  : > /etc/rspamd/local.d/antivirus.conf
fi
exec /usr/bin/rspamd -f -u _rspamd -g _rspamd
