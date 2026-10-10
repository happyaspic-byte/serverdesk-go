#!/bin/sh
set -eu
umask 077

if [ "${1:-}" != serverdesk ]; then
  exec "$@"
fi
shift

state=/var/lib/serverdesk
cd "$state"
exec 9> .container.lock
flock -n 9 || { echo 'Another container is using this data volume.' >&2; exit 1; }

if [ ! -e config.local.json ] && [ ! -L config.local.json ]; then
  cp /usr/local/share/serverdesk/config.example.json config.local.json
fi

if [ ! -e auth.json ] && [ ! -L auth.json ]; then
  login=$(mktemp .initial-login.XXXXXX)
  trap 'rm -f "$login"' EXIT HUP INT TERM
  serverdesk -auth auth.json -init-auth > "$login"
  mv "$login" initial-login.txt
  trap - EXIT HUP INT TERM
  echo 'Initial credentials saved to /var/lib/serverdesk/initial-login.txt (not logged).'
fi

mkdir -p tls
if [ ! -e tls/server.crt ] && [ ! -L tls/server.crt ] && \
   [ ! -e tls/server.key ] && [ ! -L tls/server.key ]; then
  tls_tmp=$(mktemp -d tls/.init.XXXXXX)
  trap 'rm -rf "$tls_tmp"' EXIT HUP INT TERM
  openssl req -x509 -newkey rsa:3072 -nodes -days 365 \
    -subj '/CN=localhost' -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1' \
    -keyout "$tls_tmp/server.key" -out "$tls_tmp/server.crt" 2>/dev/null
  mv "$tls_tmp/server.key" tls/server.key
  mv "$tls_tmp/server.crt" tls/server.crt
  rmdir "$tls_tmp"
  trap - EXIT HUP INT TERM
  echo 'Local HTTPS certificate created. Use a trusted certificate for production access.'
fi

if [ ! -s tls/server.crt ] || [ ! -s tls/server.key ]; then
  echo 'TLS requires both tls/server.crt and tls/server.key; existing files were not replaced.' >&2
  exit 1
fi

exec serverdesk -c "$state/config.local.json" -auth "$state/auth.json" \
  -listen 0.0.0.0:6005 -tls-cert "$state/tls/server.crt" -tls-key "$state/tls/server.key" "$@"
