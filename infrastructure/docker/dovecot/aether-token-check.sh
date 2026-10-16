#!/bin/bash
IFS= read -r -d '' user <&3
IFS= read -r -d '' token <&3
code=$(printf '%s' "$token" | curl -s -o /dev/null -w '%{http_code}' --max-time 15 \
  --data-urlencode "user=$user" --data-urlencode "token@-" "$(cat /run/dovecot-secrets/api-url)/internal/mail-token")
case "$code" in
  204) exec "$@" ;;
  403) exit 1 ;;
  *) exit 111 ;;
esac
