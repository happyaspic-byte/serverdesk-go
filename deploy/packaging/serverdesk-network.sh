#!/bin/sh
# Apply or remove the optional Serverdesk-owned auxiliary IPv4 address and
# SNMP trap redirect. The config parser deliberately does not source/eval the
# file because this helper runs with CAP_NET_ADMIN.
set -eu
set -f

fail() {
  echo "[FAIL] $*" >&2
  exit 1
}

usage() {
  echo "usage: $0 --config PATH [--state /run/serverdesk-net/FILE] validate|apply|remove" >&2
  exit 2
}

config=
state=
while [ "$#" -gt 1 ]; do
  case "$1" in
    --config)
      [ "$#" -ge 3 ] || usage
      config=$2
      shift 2
      ;;
    --state)
      [ "$#" -ge 3 ] || usage
      state=$2
      shift 2
      ;;
    *) usage ;;
  esac
done
[ "$#" -eq 1 ] || usage
action=$1
case "$action" in
  validate|apply|remove) ;;
  *) usage ;;
esac
[ -n "$config" ] || fail "--config is required"
[ -z "$state" ] || case "$state" in
  /run/serverdesk-net/*) ;;
  *) fail "--state must be below /run/serverdesk-net" ;;
esac
if [ "$action" = remove ] && [ ! -e "$config" ] && [ ! -L "$config" ]; then
  echo "[OK] no applied network state to remove"
  exit 0
fi
[ -f "$config" ] && [ ! -L "$config" ] && [ -r "$config" ] ||
  fail "configuration must be a readable, regular, non-symlink file: $config"

net_interface=
aux_address=
enable_redirect=false
source_port=162
target_port=10162
seen_interface=0
seen_address=0
seen_enable=0
seen_source=0
seen_target=0
line_no=0

while IFS= read -r line || [ -n "$line" ]; do
  line_no=$((line_no + 1))
  case "$line" in
    ''|'#'*) continue ;;
    *[!A-Za-z0-9_=./:@-]*) fail "$config:$line_no contains whitespace, quoting, or an unsupported character" ;;
    *=*) ;;
    *) fail "$config:$line_no must be KEY=VALUE" ;;
  esac
  key=${line%%=*}
  value=${line#*=}
  case "$key" in
    SERVERDESK_NET_INTERFACE)
      [ "$seen_interface" -eq 0 ] || fail "$config:$line_no duplicates $key"
      net_interface=$value
      seen_interface=1
      ;;
    SERVERDESK_AUX_ADDRESS)
      [ "$seen_address" -eq 0 ] || fail "$config:$line_no duplicates $key"
      aux_address=$value
      seen_address=1
      ;;
    SERVERDESK_ENABLE_TRAP_REDIRECT)
      [ "$seen_enable" -eq 0 ] || fail "$config:$line_no duplicates $key"
      enable_redirect=$value
      seen_enable=1
      ;;
    SERVERDESK_TRAP_SOURCE_PORT)
      [ "$seen_source" -eq 0 ] || fail "$config:$line_no duplicates $key"
      source_port=$value
      seen_source=1
      ;;
    SERVERDESK_TRAP_TARGET_PORT)
      [ "$seen_target" -eq 0 ] || fail "$config:$line_no duplicates $key"
      target_port=$value
      seen_target=1
      ;;
    *) fail "$config:$line_no uses unknown key: $key" ;;
  esac
done < "$config"

valid_port() {
  case "$1" in ''|*[!0-9]*) return 1 ;; esac
  [ "$1" -ge 1 ] 2>/dev/null && [ "$1" -le 65535 ] 2>/dev/null
}

valid_ipv4_cidr() {
  value=$1
  case "$value" in */*) ;; *) return 1 ;; esac
  ip=${value%/*}
  prefix=${value##*/}
  case "$prefix" in ''|*[!0-9]*) return 1 ;; esac
  [ "$prefix" -le 32 ] 2>/dev/null || return 1
  old_ifs=$IFS
  IFS=.
  set -- $ip
  IFS=$old_ifs
  [ "$#" -eq 4 ] || return 1
  for octet in "$@"; do
    case "$octet" in ''|*[!0-9]*) return 1 ;; esac
    [ "$octet" -le 255 ] 2>/dev/null || return 1
  done
}

case "$enable_redirect" in true|false) ;; *) fail "SERVERDESK_ENABLE_TRAP_REDIRECT must be true or false" ;; esac
valid_port "$source_port" || fail "SERVERDESK_TRAP_SOURCE_PORT must be 1..65535"
valid_port "$target_port" || fail "SERVERDESK_TRAP_TARGET_PORT must be 1..65535"

if [ -n "$net_interface" ] || [ -n "$aux_address" ]; then
  [ -n "$net_interface" ] && [ -n "$aux_address" ] ||
    fail "SERVERDESK_NET_INTERFACE and SERVERDESK_AUX_ADDRESS must be set together"
  case "$net_interface" in -*|*[!A-Za-z0-9_.:@-]*) fail "invalid SERVERDESK_NET_INTERFACE" ;; esac
  valid_ipv4_cidr "$aux_address" || fail "SERVERDESK_AUX_ADDRESS must be an IPv4 CIDR such as 192.0.2.10/24"
fi

if [ "$action" = validate ]; then
  echo "[OK] network configuration is valid"
  exit 0
fi

address_added=0
redirect_added=0
address_owned=0
redirect_owned=0
ip_bin=
iptables_bin=

# Preserve ownership across repeated apply calls. The state file contains only
# resources created by Serverdesk; pre-existing host configuration is never
# adopted merely because it has the same value.
if [ "$action" = apply ] && [ -n "$state" ] && { [ -e "$state" ] || [ -L "$state" ]; }; then
  [ -f "$state" ] && [ ! -L "$state" ] || fail "applied state must be a regular non-symlink file: $state"
  "$0" --config "$state" validate >/dev/null
  if grep -q '^SERVERDESK_AUX_ADDRESS=..*$' "$state"; then
    grep -F -x -q "SERVERDESK_NET_INTERFACE=$net_interface" "$state" &&
      grep -F -x -q "SERVERDESK_AUX_ADDRESS=$aux_address" "$state" ||
      fail "applied state owns a different auxiliary address; remove it before apply"
    address_owned=1
  fi
  if grep -F -x -q 'SERVERDESK_ENABLE_TRAP_REDIRECT=true' "$state"; then
    grep -F -x -q "SERVERDESK_TRAP_SOURCE_PORT=$source_port" "$state" &&
      grep -F -x -q "SERVERDESK_TRAP_TARGET_PORT=$target_port" "$state" ||
      fail "applied state owns a different trap redirect; remove it before apply"
    redirect_owned=1
  fi
fi

if [ "$action" = apply ] && [ "$enable_redirect" = true ]; then
  # Resolve every required tool before the first mutation so a missing
  # iptables binary cannot leave an auxiliary address behind.
  iptables_bin=$(command -v iptables || :)
  [ -n "$iptables_bin" ] || fail "iptables is required when trap redirect is enabled"
fi
if [ -n "$net_interface" ]; then
  [ -d "/sys/class/net/$net_interface" ] || fail "network interface does not exist: $net_interface"
  ip_bin=$(command -v ip || :)
  [ -n "$ip_bin" ] || fail "ip command is required when an auxiliary address is configured"
  if [ "$action" = apply ]; then
    if ! "$ip_bin" -o address show dev "$net_interface" 2>/dev/null |
      grep -F -q " $aux_address "; then
      "$ip_bin" address replace "$aux_address" dev "$net_interface"
      address_added=1
      address_owned=1
    fi
  else
    "$ip_bin" address delete "$aux_address" dev "$net_interface" 2>/dev/null || :
  fi
fi

if [ "$enable_redirect" = true ]; then
  iptables_bin=$(command -v iptables || :)
  [ -n "$iptables_bin" ] || fail "iptables is required when trap redirect is enabled"
  comment=serverdesk-managed-trap-redirect
  if [ "$action" = apply ]; then
    if ! "$iptables_bin" -w 5 -t nat -C PREROUTING -p udp --dport "$source_port" \
      -m comment --comment "$comment" -j REDIRECT --to-ports "$target_port" 2>/dev/null; then
      if ! "$iptables_bin" -w 5 -t nat -A PREROUTING -p udp --dport "$source_port" \
        -m comment --comment "$comment" -j REDIRECT --to-ports "$target_port"; then
        if [ "$address_added" -eq 1 ]; then
          "$ip_bin" address delete "$aux_address" dev "$net_interface" 2>/dev/null || :
        fi
        fail "failed to install the Serverdesk trap redirect; auxiliary address rolled back"
      fi
      redirect_added=1
      redirect_owned=1
    fi
  else
    while "$iptables_bin" -w 5 -t nat -C PREROUTING -p udp --dport "$source_port" \
      -m comment --comment "$comment" -j REDIRECT --to-ports "$target_port" 2>/dev/null; do
      "$iptables_bin" -w 5 -t nat -D PREROUTING -p udp --dport "$source_port" \
        -m comment --comment "$comment" -j REDIRECT --to-ports "$target_port"
    done
  fi
fi

if [ "$action" = apply ] && [ -n "$state" ]; then
  state_tmp=$state.tmp.$$
  state_interface=
  state_address=
  state_redirect=false
  if [ "$address_owned" -eq 1 ]; then
    state_interface=$net_interface
    state_address=$aux_address
  fi
  if [ "$redirect_owned" -eq 1 ]; then
    state_redirect=true
  fi
  if ! (umask 077 && {
    printf 'SERVERDESK_NET_INTERFACE=%s\n' "$state_interface"
    printf 'SERVERDESK_AUX_ADDRESS=%s\n' "$state_address"
    printf 'SERVERDESK_ENABLE_TRAP_REDIRECT=%s\n' "$state_redirect"
    printf 'SERVERDESK_TRAP_SOURCE_PORT=%s\n' "$source_port"
    printf 'SERVERDESK_TRAP_TARGET_PORT=%s\n' "$target_port"
  } > "$state_tmp" && mv -f "$state_tmp" "$state"); then
    if [ "$redirect_added" -eq 1 ]; then
      "$iptables_bin" -w 5 -t nat -D PREROUTING -p udp --dport "$source_port" \
        -m comment --comment serverdesk-managed-trap-redirect \
        -j REDIRECT --to-ports "$target_port" 2>/dev/null || :
    fi
    if [ "$address_added" -eq 1 ]; then
      "$ip_bin" address delete "$aux_address" dev "$net_interface" 2>/dev/null || :
    fi
    fail "failed to persist applied network state; changes rolled back"
  fi
fi

echo "[OK] network configuration $action completed"
