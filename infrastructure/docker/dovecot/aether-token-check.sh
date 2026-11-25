#!/bin/bash
IFS= read -r -d '' user <&3
IFS= read -r -d '' token <&3
out=$(printf '%s' "$token" | curl -s -w '\n%{http_code}' --max-time 15 \
  --data-urlencode "user=$user" --data-urlencode "session=${AUTH_SESSION:-}" --data-urlencode "service=${AUTH_SERVICE:-}" --data-urlencode "token@-" "$(cat /run/dovecot-secrets/api-url)/internal/mail-token")
code=${out##*$'\n'}
case "$code" in
  204) exec "$@" ;;
  200) USER=${out%$'\n'*} exec "$@" ;;
  403) exit 1 ;;
  *) exit 111 ;;
esac
