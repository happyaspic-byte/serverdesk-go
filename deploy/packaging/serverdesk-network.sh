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
  echo "usage: $0 --config PATH [--state /var/lib/serverdesk-net/applied.env] [--expected-config PATH] validate|apply|remove|migrate" >&2
  exit 2
}

config=
expected_config=
no_authority=0
state=
while [ "$#" -gt 1 ]; do
  case "$1" in
    --config)
      [ "$#" -ge 3 ] || usage
      config=$2
      shift 2
      ;;
    --expected-config)
      [ "$#" -ge 3 ] || usage
      expected_config=$2
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
  validate|apply|remove|migrate) ;;
  *) usage ;;
esac
[ -n "$config" ] || fail "--config is required"
[ -z "$state" ] || [ "$state" = /var/lib/serverdesk-net/applied.env ] ||
  fail '--state must be /var/lib/serverdesk-net/applied.env'

check_state_dir() {
  directory=$1
  if [ -L "$directory" ] || [ ! -d "$directory" ] ||
    [ "$(stat -c '%u:%g:%a' "$directory")" != 0:0:700 ]; then
    fail "unsafe network state directory: $directory"
  fi
}
check_state_file() {
  file=$1
  if [ -L "$file" ] || [ ! -f "$file" ] ||
    [ "$(stat -c '%u:%g:%a:%h' "$file")" != 0:0:600:1 ]; then
    fail "unsafe network ownership file: $file"
  fi
}
legacy=/run/serverdesk-net/applied.env
if [ "$action" != validate ]; then
  for command in stat realpath cp mv rm mkdir; do
    command -v "$command" >/dev/null 2>&1 || fail "required network state command is unavailable: $command"
  done
  if [ -n "$state" ]; then
    [ "$(realpath -s /var/lib/serverdesk-net)" = "$(realpath /var/lib/serverdesk-net)" ] ||
      fail 'network state path traverses a symlink'
    if [ ! -e /var/lib/serverdesk-net ] && [ ! -L /var/lib/serverdesk-net ]; then
      (umask 077; mkdir /var/lib/serverdesk-net)
    fi
    check_state_dir /var/lib/serverdesk-net
    if [ -e "$state" ] || [ -L "$state" ]; then check_state_file "$state"; fi
    if [ -e "$legacy" ] || [ -L "$legacy" ]; then
      check_state_dir /run/serverdesk-net
      check_state_file "$legacy"
      sh "$0" --config "$legacy" validate >/dev/null
      if [ -e "$state" ]; then
        cmp -s "$legacy" "$state" || fail 'legacy and persistent network ownership conflict; operator inspection required'
      else
        (umask 077; set -C; cp "$legacy" "$state")
        check_state_file "$state"
      fi
    fi
  fi
  if [ "$action" = migrate ]; then
    [ -n "$state" ] || fail 'migration requires --state'
    echo '[OK] network ownership migration completed'
    exit 0
  fi
  if [ "$action" = remove ]; then
    [ "$config" = /var/lib/serverdesk-net/applied.env ] || [ "$config" = "$legacy" ] ||
      fail 'remove requires an applied ownership file, not host configuration'
    if [ ! -e "$config" ] && [ ! -L "$config" ]; then
      # A legacy authority must not be silently ignored by a new unit.
      if [ -e "$legacy" ] || [ -L "$legacy" ]; then
        fail 'legacy ownership requires migration before removal'
      fi
      [ -n "$expected_config" ] || fail 'network ownership is unknown; missing journal and expected configuration'
      config=$expected_config
      no_authority=1
    fi
    if [ "$no_authority" -eq 0 ]; then
    [ "$(realpath "$config")" = "$(realpath -s "$config")" ] || fail 'network ownership path traverses a symlink'
    check_state_dir "$(dirname "$config")"
    check_state_file "$config"
    if [ "$config" != "$legacy" ] && { [ -e "$legacy" ] || [ -L "$legacy" ]; }; then
      check_state_dir /run/serverdesk-net
      check_state_file "$legacy"
      cmp -s "$config" "$legacy" || fail 'legacy and persistent ownership conflict; cleanup halted'
    fi
    fi
  fi
fi
if [ ! -f "$config" ] || [ -L "$config" ] || [ ! -r "$config" ]; then
  fail "configuration must be a readable, regular, non-symlink file: $config"
fi

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
  # Intentional field splitting turns the IPv4 address into four octets.
  # shellcheck disable=SC2086
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
  if [ -z "$net_interface" ] || [ -z "$aux_address" ]; then
    fail "SERVERDESK_NET_INTERFACE and SERVERDESK_AUX_ADDRESS must be set together"
  fi
  case "$net_interface" in -*|*[!A-Za-z0-9_.:@-]*) fail "invalid SERVERDESK_NET_INTERFACE" ;; esac
  valid_ipv4_cidr "$aux_address" || fail "SERVERDESK_AUX_ADDRESS must be an IPv4 CIDR such as 192.0.2.10/24"
fi

if [ "$action" = validate ]; then
  echo "[OK] network configuration is valid"
  exit 0
fi

[ "$action" != apply ] || [ -n "$state" ] || fail 'apply requires --state to retain cleanup authority'
address_owned=0
redirect_owned=0
ip_bin=
iptables_bin=

# Preserve ownership across repeated apply calls. The state file contains only
# resources created by Serverdesk; pre-existing host configuration is never
# adopted merely because it has the same value.
if [ "$action" = apply ] && [ -n "$state" ] && { [ -e "$state" ] || [ -L "$state" ]; }; then
  if [ ! -f "$state" ] || [ -L "$state" ]; then
    fail "applied state must be a regular non-symlink file: $state"
  fi
  sh "$0" --config "$state" validate >/dev/null
  if grep -q '^SERVERDESK_AUX_ADDRESS=..*$' "$state"; then
    if ! grep -F -x -q "SERVERDESK_NET_INTERFACE=$net_interface" "$state" ||
      ! grep -F -x -q "SERVERDESK_AUX_ADDRESS=$aux_address" "$state"; then
      fail "applied state owns a different auxiliary address; remove it before apply"
    fi
    address_owned=1
  fi
  if grep -F -x -q 'SERVERDESK_ENABLE_TRAP_REDIRECT=true' "$state"; then
    if ! grep -F -x -q "SERVERDESK_TRAP_SOURCE_PORT=$source_port" "$state" ||
      ! grep -F -x -q "SERVERDESK_TRAP_TARGET_PORT=$target_port" "$state"; then
      fail "applied state owns a different trap redirect; remove it before apply"
    fi
    redirect_owned=1
  fi
fi

persist_ownership() {
  state_tmp=$state.tmp.$$
  if [ -e "$state_tmp" ] || [ -L "$state_tmp" ]; then
    fail 'unsafe network ownership temporary file'
  fi
  state_interface=
  state_address=
  state_redirect=false
  if [ "$address_owned" -eq 1 ]; then
    state_interface=$net_interface
    state_address=$aux_address
  fi
  if [ "$redirect_owned" -eq 1 ]; then state_redirect=true; fi
  if ! (umask 077; set -C; {
    printf 'SERVERDESK_NET_INTERFACE=%s\n' "$state_interface"
    printf 'SERVERDESK_AUX_ADDRESS=%s\n' "$state_address"
    printf 'SERVERDESK_ENABLE_TRAP_REDIRECT=%s\n' "$state_redirect"
    printf 'SERVERDESK_TRAP_SOURCE_PORT=%s\n' "$source_port"
    printf 'SERVERDESK_TRAP_TARGET_PORT=%s\n' "$target_port"
  } > "$state_tmp") || ! mv -f "$state_tmp" "$state"; then
    fail 'failed to persist network ownership; retry cleanup before apply'
  fi
}
address_present() {
  addresses=$("$ip_bin" -o address show dev "$net_interface") || fail 'failed to inspect auxiliary address'
  printf '%s\n' "$addresses" | grep -F -q " $aux_address "
}
redirect_present() {
  rules=$("$iptables_bin" -w 5 -t nat -S PREROUTING) || fail 'failed to inspect trap redirect rules'
  printf '%s\n' "$rules" | grep -F -x -q -- "-A PREROUTING -p udp -m udp --dport $source_port -m comment --comment serverdesk-managed-trap-redirect -j REDIRECT --to-ports $target_port"
}

if [ "$enable_redirect" = true ]; then
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
    if ! address_present; then
      address_owned=1
      persist_ownership
      "$ip_bin" address add "$aux_address" dev "$net_interface"
    fi
  else
    if address_present; then
      [ "$no_authority" -eq 0 ] || fail 'network ownership is unknown; configured auxiliary address remains'
      "$ip_bin" address delete "$aux_address" dev "$net_interface" || fail 'failed to remove owned auxiliary address; ownership retained'
    fi
    if address_present; then fail 'owned auxiliary address remains; ownership retained'; fi
  fi
fi

if [ "$enable_redirect" = true ]; then
  iptables_bin=$(command -v iptables || :)
  [ -n "$iptables_bin" ] || fail "iptables is required when trap redirect is enabled"
  comment=serverdesk-managed-trap-redirect
  if [ "$action" = apply ]; then
    if ! redirect_present; then
      redirect_owned=1
      persist_ownership
      if ! "$iptables_bin" -w 5 -t nat -A PREROUTING -p udp --dport "$source_port" \
        -m comment --comment "$comment" -j REDIRECT --to-ports "$target_port"; then
        fail 'failed to install the Serverdesk trap redirect; ownership retained for verified cleanup'
      fi
      redirect_owned=1
    fi
  else
    if redirect_present; then
      [ "$no_authority" -eq 0 ] || fail 'network ownership is unknown; configured trap redirect remains'
      "$iptables_bin" -w 5 -t nat -D PREROUTING -p udp --dport "$source_port" \
        -m comment --comment "$comment" -j REDIRECT --to-ports "$target_port" ||
        fail 'failed to remove owned trap redirect; ownership retained'
    fi
    if redirect_present; then fail 'owned trap redirect remains; ownership retained'; fi
  fi
fi

if [ "$action" = apply ]; then
  persist_ownership
  # Persistent authority is durable before the old runtime copy is retired.
  if [ -e "$legacy" ]; then rm "$legacy"; fi
elif [ "$action" = remove ]; then
  if [ "$no_authority" -eq 1 ]; then
    echo '[OK] no journal; configured network resources confirmed absent'
    exit 0
  fi
  # Both inspections succeeded, all owned resources are absent.
  rm "$config"
  if [ "$config" != "$legacy" ] && [ -e "$legacy" ]; then rm "$legacy"; fi
fi

echo "[OK] network configuration $action completed"
