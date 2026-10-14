#!/bin/sh
set -eu
: "${AETHER_MAIL_HOSTNAME:?required}"
if [ -s /tls/fullchain.pem ] && [ -s /tls/privkey.pem ]; then
  echo "TLS certificate present in /tls; leaving it unchanged."
  exit 0
fi
openssl req -x509 -newkey rsa:3072 -sha256 -days 825 -nodes \
  -keyout /tls/privkey.pem -out /tls/fullchain.pem \
  -subj "/CN=${AETHER_MAIL_HOSTNAME}" \
  -addext "subjectAltName=DNS:${AETHER_MAIL_HOSTNAME}" >/dev/null 2>&1
chmod 0600 /tls/privkey.pem
chmod 0644 /tls/fullchain.pem
echo "Generated a self-signed certificate for ${AETHER_MAIL_HOSTNAME}. Replace /tls/fullchain.pem and /tls/privkey.pem with a trusted certificate before production use."
