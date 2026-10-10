#!/bin/sh
set -eu
host=${SERVERDESK_TLS_HOST:-localhost}
ca=${SERVERDESK_TLS_CA_FILE:-/var/lib/serverdesk/tls/server.crt}
exec curl --fail --silent --show-error --noproxy '*' --connect-timeout 2 --max-time 4 \
  --cacert "$ca" \
  --resolve "$host:6005:127.0.0.1" "https://$host:6005/api/health" --output /dev/null
