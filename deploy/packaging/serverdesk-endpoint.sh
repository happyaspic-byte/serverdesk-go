#!/bin/sh
# Print a single local /api/health URL derived from config, or validate an override.
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 3 ]; then
  echo "usage: $0 CONFIG_JSON [HEALTH_URL] [SERVICE_USER]" >&2
  exit 2
fi
config=$1
override=${2:-}
service_user=${3:-serverdesk}
case "$service_user" in ''|*[!A-Za-z0-9_.-]*) echo '[FAIL] invalid service user' >&2; exit 1;; esac
for command in jq getent ip stat realpath; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "[FAIL] endpoint validation requires $command" >&2
    exit 1
  }
done
if [ ! -f "$config" ] || [ -L "$config" ]; then
  echo "[FAIL] unsafe config: $config" >&2
  exit 1
fi

assert_local_health_host() {
  host=$1
  resolved=$(getent ahosts "$host" 2>/dev/null | awk '!seen[$1]++ { print $1 }') || :
  if [ -z "$resolved" ]; then
    echo "[FAIL] health URL host does not resolve: $host" >&2
    exit 1
  fi
  non_loopback_addresses=
  for address in $resolved; do
    address_without_scope=${address%%%*}
    case "$address_without_scope" in
      127.*|::1) continue ;;
    esac
    non_loopback_addresses="$non_loopback_addresses $address_without_scope"
  done
  [ -n "$non_loopback_addresses" ] || return 0
  local_address_output=$(ip -o addr show 2>/dev/null) || {
    echo '[FAIL] unable to enumerate local addresses for health URL validation' >&2
    exit 1
  }
  local_addresses=$(printf '%s\n' "$local_address_output" |
    awk '{ value=$4; sub("/.*", "", value); print value }')
  for address_without_scope in $non_loopback_addresses; do
    if ! printf '%s\n' "$local_addresses" | grep -F -x -q "$address_without_scope"; then
      echo "[FAIL] health URL host resolves to a non-local address: $host -> $address_without_scope" >&2
      exit 1
    fi
  done
}

listen=$(jq -er '.listen // "127.0.0.1:9891" | select(type == "string" and length > 0)' "$config") || {
  echo '[FAIL] listen must be a non-empty string' >&2
  exit 1
}
case "$listen" in
  \[*\]:*)
    listen_tail=${listen#*\]}
    case "$listen_tail" in :*) ;; *) echo '[FAIL] malformed bracketed listen address' >&2; exit 1;; esac
    listen_host=${listen#\[}
    listen_host=${listen_host%%\]*}
    listen_port=${listen_tail#:}
    ;;
  *:*)
    listen_host=${listen%:*}
    listen_port=${listen##*:}
    case "$listen_host" in *:*) echo '[FAIL] IPv6 listen addresses require brackets' >&2; exit 1;; esac
    ;;
  *) echo "[FAIL] listen must be HOST:PORT: $listen" >&2; exit 1 ;;
esac
case "$listen_host" in ''|*[!A-Za-z0-9_.:%+*-]*) echo "[FAIL] invalid listen host: $listen_host" >&2; exit 1;; esac
case "$listen_port" in ''|*[!0-9]*) echo "[FAIL] invalid listen port: $listen_port" >&2; exit 1;; esac
if [ "$listen_port" -lt 1 ] || [ "$listen_port" -gt 65535 ]; then
  echo "[FAIL] listen port is outside 1..65535: $listen_port" >&2
  exit 1
fi

tls_cert=$(jq -er '.tls_cert_file // "" | select(type == "string")' "$config") || exit 1
tls_key=$(jq -er '.tls_key_file // "" | select(type == "string")' "$config") || exit 1
if { [ -n "$tls_cert" ] && [ -z "$tls_key" ]; } || { [ -z "$tls_cert" ] && [ -n "$tls_key" ]; }; then
  echo '[FAIL] tls_cert_file and tls_key_file must be configured together' >&2
  exit 1
fi
scheme=http
if [ -n "$tls_cert" ]; then
  scheme=https
  config_dir=$(CDPATH='' cd "$(dirname "$config")" && pwd)
  case "$tls_cert" in /*) ;; *) tls_cert=$config_dir/$tls_cert ;; esac
  case "$tls_key" in /*) ;; *) tls_key=$config_dir/$tls_key ;; esac
  if [ ! -f "$tls_cert" ] || [ -L "$tls_cert" ]; then
    echo "[FAIL] unsafe TLS certificate: $tls_cert" >&2
    exit 1
  fi
  if [ ! -f "$tls_key" ] || [ -L "$tls_key" ]; then
    echo "[FAIL] unsafe TLS key: $tls_key" >&2
    exit 1
  fi
  if [ "$(realpath -s "$tls_key")" != "$(realpath "$tls_key")" ]; then
    echo "[FAIL] TLS key path must not traverse symlinks: $tls_key" >&2
    exit 1
  fi
  key_owner=$(stat -c '%U' "$tls_key")
  key_mode=$(stat -c '%a' "$tls_key")
  case "$key_owner" in root|"$service_user") ;; *)
    echo "[FAIL] TLS key must be owned by root or $service_user: $tls_key" >&2
    exit 1
    ;;
  esac
  case "$key_mode" in 400|600) ;; *)
    echo "[FAIL] TLS key must be private (mode 400 or 600): $tls_key" >&2
    exit 1
    ;;
  esac
fi

case "$listen_host" in 127.0.0.1|localhost|::1) exposed=0 ;; *) exposed=1 ;; esac
allow_insecure=$(jq -r '.allow_insecure_http // false | select(type == "boolean")' "$config") || exit 1
case "$allow_insecure" in true|false) ;; *) echo '[FAIL] allow_insecure_http must be boolean' >&2; exit 1;; esac
if [ "$exposed" -eq 1 ] && [ "$scheme" = http ] && [ "$allow_insecure" != true ]; then
  echo '[FAIL] non-loopback HTTP requires TLS or allow_insecure_http=true (break-glass only)' >&2
  exit 1
fi

wildcard=0
case "$listen_host" in 0.0.0.0|::|'*'|'+') wildcard=1 ;; esac
if [ -z "$override" ]; then
  if [ "$scheme" = https ] && [ "$wildcard" -eq 1 ]; then
    echo '[FAIL] TLS with a wildcard listener requires SERVERDESK_HEALTH_URL using a certificate-valid local hostname' >&2
    exit 1
  fi
  case "$listen_host" in
    0.0.0.0|'*'|'+') health_host=127.0.0.1 ;;
    ::) health_host='[::1]' ;;
    *:*) health_host="[$listen_host]" ;;
    *) health_host=$listen_host ;;
  esac
  local_health_host=${health_host#\[}
  local_health_host=${local_health_host%\]}
  assert_local_health_host "$local_health_host"
  printf '%s\n' "$scheme://$health_host:$listen_port/api/health"
  exit 0
fi

case "$override" in
  http://*) override_scheme=http; health_rest=${override#http://} ;;
  https://*) override_scheme=https; health_rest=${override#https://} ;;
  *) echo '[FAIL] health URL scheme must be http or https' >&2; exit 1 ;;
esac
case "$health_rest" in *'?'*|*'#'*|*'@'*) echo '[FAIL] health URL must not contain userinfo, query, or fragment' >&2; exit 1;; esac
health_authority=${health_rest%%/*}
health_path=/${health_rest#*/}
if [ "$health_rest" = "$health_authority" ] || [ "$health_path" != /api/health ]; then
  echo '[FAIL] health URL path must be exactly /api/health' >&2
  exit 1
fi
case "$health_authority" in
  \[*\]:*)
    health_tail=${health_authority#*\]}
    case "$health_tail" in :*) ;; *) echo '[FAIL] malformed bracketed health URL host' >&2; exit 1;; esac
    health_host=${health_authority#\[}
    health_host=${health_host%%\]*}
    health_port=${health_tail#:}
    ;;
  *:*)
    health_host=${health_authority%:*}
    health_port=${health_authority##*:}
    case "$health_host" in *:*) echo '[FAIL] IPv6 health URL hosts require brackets' >&2; exit 1;; esac
    ;;
  *) echo '[FAIL] health URL must include an explicit port' >&2; exit 1 ;;
esac
case "$health_host" in ''|*[!A-Za-z0-9_.:%-]*) echo '[FAIL] invalid health URL host' >&2; exit 1;; esac
case "$health_port" in ''|*[!0-9]*) echo '[FAIL] invalid health URL port' >&2; exit 1;; esac
if [ "$override_scheme" != "$scheme" ] || [ "$health_port" -ne "$listen_port" ]; then
  echo '[FAIL] health URL scheme/port must match the configured listener' >&2
  exit 1
fi

assert_local_health_host "$health_host"
printf '%s\n' "$override"
