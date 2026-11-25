#!/bin/sh
set -eu

if [ "$#" -eq 0 ]; then
  exec 9>>/var/vmail/.aether-encrypt.lock
  if ! flock -n 9; then
    echo "Could not lock the mail volume for encryption." >&2
    exit 1
  fi
  find /var/vmail -maxdepth 5 -regextype posix-extended -type f \
    -regex '/var/vmail/[^/]+/Maildir/(\.[^/]+/)?(cur|new)/[^/]+' \
    -exec "$0" {} +
  sync -f /var/vmail
  echo "Maildir encryption migration complete."
  exit 0
fi

magic=$(printf 'CRYPTED\003\007')
tmp=
trap 'if [ -n "$tmp" ]; then rm -f "$tmp/message"; rmdir "$tmp"; fi' EXIT
trap 'exit 1' HUP INT TERM

for message; do
  header=$(head -c 9 "$message")
  [ "$header" = "$magic" ] && continue

  tmp=$(mktemp -d "${message%/*}/.aether-encrypt.XXXXXXXX")
  doveadm fs put crypt set_prefix=mail_crypt_global:posix: "$message" "$tmp/message"
  if [ "$(head -c 9 "$tmp/message")" != "$magic" ]; then
    echo "Dovecot did not encrypt $message" >&2
    exit 1
  fi
  chown --reference="$message" "$tmp/message"
  chmod --reference="$message" "$tmp/message"
  touch -r "$message" "$tmp/message"
  sync "$tmp/message"
  mv -fT "$tmp/message" "$message"
  rmdir "$tmp"
  tmp=
done
