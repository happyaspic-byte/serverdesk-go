#!/bin/sh
# serverdesk Linux installer: sudo sh install-linux.sh  (run from the package folder)
set -eu

DST=/opt/serverdesk
STATE=/var/lib/serverdesk
SERVICE_USER=serverdesk
UNIT=/etc/systemd/system/serverdesk.service
NET_UNIT=/etc/systemd/system/serverdesk-net.service
CONFIG_DIR=/etc/serverdesk
NET_CONFIG=$CONFIG_DIR/network.env
NET_HELPER=$DST/serverdesk-network
COLLECTOR_PREFLIGHT=deploy/packaging/serverdesk-collector-preflight.sh
CREDENTIAL_STORE=$STATE/credentials
MIB_DIR=$DST/mibs
HEALTH_URL_OVERRIDE=${SERVERDESK_HEALTH_URL:-}
HEALTH_URL=
ALLOW_DEGRADED_COLLECTION=${SERVERDESK_ALLOW_DEGRADED_COLLECTION:-0}
auth_init_tmp=
auth_created=0
service_was_active=0
net_was_active=0
service_was_enabled=0
net_was_enabled=0
services_touched=0
install_complete=0
binary_backup=0
unit_backup=0
net_unit_backup=0
net_helper_backup=0
binary_created=0
unit_created=0
net_unit_created=0
net_helper_created=0
net_config_created=0
installation_was_existing=0
rollback_failed=0

cleanup_install() {
  if [ -n "$auth_init_tmp" ]; then
    sudo -u "$SERVICE_USER" rm -f "$auth_init_tmp" || {
      echo "[CRITICAL] could not remove temporary initial-login material: $auth_init_tmp" >&2
      rollback_failed=1
    }
  fi
  if [ "$auth_created" -eq 1 ] && [ "$install_complete" -eq 0 ]; then
    sudo -u "$SERVICE_USER" rm -f "$STATE/auth.json" "$STATE/initial-login.txt" || {
      echo '[CRITICAL] rollback could not remove newly generated auth/initial-login data' >&2
      rollback_failed=1
    }
  fi
  if [ "$install_complete" -eq 0 ]; then
    if [ "$services_touched" -eq 1 ]; then
      sudo systemctl stop serverdesk serverdesk-net || {
        echo '[CRITICAL] rollback could not stop replacement services' >&2
        rollback_failed=1
      }
      if [ "$service_was_enabled" -eq 0 ]; then
        sudo systemctl disable serverdesk || {
          echo '[CRITICAL] rollback could not restore serverdesk disabled state' >&2
          rollback_failed=1
        }
      fi
      if [ "$net_was_enabled" -eq 0 ]; then
        sudo systemctl disable serverdesk-net || {
          echo '[CRITICAL] rollback could not restore serverdesk-net disabled state' >&2
          rollback_failed=1
        }
      fi
    fi
    if [ "$binary_backup" -eq 1 ] && [ -f "$DST/serverdesk.install-backup" ]; then
      sudo cp -a "$DST/serverdesk.install-backup" "$DST/serverdesk" &&
        sudo cmp -s "$DST/serverdesk.install-backup" "$DST/serverdesk" || {
        echo "[CRITICAL] rollback could not verify the original binary; backup preserved at $DST/serverdesk.install-backup" >&2
        rollback_failed=1
      }
    fi
    if [ "$unit_backup" -eq 1 ] && [ -f "$UNIT.install-backup" ]; then
      sudo cp -a "$UNIT.install-backup" "$UNIT" &&
        sudo cmp -s "$UNIT.install-backup" "$UNIT" || {
        echo "[CRITICAL] rollback could not verify the original unit; backup preserved at $UNIT.install-backup" >&2
        rollback_failed=1
      }
    elif [ "$unit_created" -eq 1 ]; then
      sudo rm -f "$UNIT" || {
        echo "[CRITICAL] rollback could not remove newly installed unit: $UNIT" >&2
        rollback_failed=1
      }
    fi
    if [ "$net_unit_backup" -eq 1 ] && [ -f "$NET_UNIT.install-backup" ]; then
      sudo cp -a "$NET_UNIT.install-backup" "$NET_UNIT" &&
        sudo cmp -s "$NET_UNIT.install-backup" "$NET_UNIT" || {
        echo "[CRITICAL] rollback could not verify the original network unit; backup preserved at $NET_UNIT.install-backup" >&2
        rollback_failed=1
      }
    elif [ "$net_unit_created" -eq 1 ]; then
      sudo rm -f "$NET_UNIT" || {
        echo "[CRITICAL] rollback could not remove newly installed network unit: $NET_UNIT" >&2
        rollback_failed=1
      }
    fi
    if [ "$net_helper_backup" -eq 1 ] && [ -f "$NET_HELPER.install-backup" ]; then
      sudo cp -a "$NET_HELPER.install-backup" "$NET_HELPER" &&
        sudo cmp -s "$NET_HELPER.install-backup" "$NET_HELPER" || {
        echo "[CRITICAL] rollback could not verify the original network helper; backup preserved at $NET_HELPER.install-backup" >&2
        rollback_failed=1
      }
    elif [ "$net_helper_created" -eq 1 ]; then
      sudo rm -f "$NET_HELPER" || {
        echo "[CRITICAL] rollback could not remove newly installed network helper: $NET_HELPER" >&2
        rollback_failed=1
      }
    fi
    if [ "$binary_created" -eq 1 ] && [ "$binary_backup" -eq 0 ]; then
      sudo rm -f "$DST/serverdesk" || {
        echo "[CRITICAL] rollback could not remove newly installed binary: $DST/serverdesk" >&2
        rollback_failed=1
      }
    fi
    if [ "$net_config_created" -eq 1 ]; then
      sudo rm -f "$NET_CONFIG" || {
        echo "[CRITICAL] rollback could not remove newly installed network config: $NET_CONFIG" >&2
        rollback_failed=1
      }
    fi
    if [ "$unit_backup" -eq 1 ] || [ "$unit_created" -eq 1 ] ||
      [ "$net_unit_backup" -eq 1 ] || [ "$net_unit_created" -eq 1 ]; then
      sudo systemctl daemon-reload || {
        echo '[CRITICAL] rollback systemd daemon-reload failed' >&2
        rollback_failed=1
      }
    fi
    if [ "$net_was_active" -eq 1 ]; then
      sudo systemctl restart serverdesk-net || {
        echo '[CRITICAL] rollback could not restart the original serverdesk-net service' >&2
        rollback_failed=1
      }
    fi
    if [ "$service_was_active" -eq 1 ]; then
      sudo systemctl restart serverdesk || {
        echo '[CRITICAL] rollback could not restart the original serverdesk service' >&2
        rollback_failed=1
      }
    fi
    if [ "$rollback_failed" -eq 1 ]; then
      echo '[CRITICAL] deployment rollback was incomplete; transaction backups were preserved for operator recovery' >&2
    elif [ "$binary_backup" -eq 1 ] || [ "$unit_backup" -eq 1 ] ||
      [ "$net_unit_backup" -eq 1 ] || [ "$net_helper_backup" -eq 1 ]; then
      echo '[INFO] previous files were restored; transaction backups were preserved for operator verification' >&2
    fi
  fi
  if [ "$install_complete" -eq 1 ]; then
    if [ "$binary_backup" -eq 1 ]; then sudo rm -f "$DST/serverdesk.install-backup" || :; fi
    if [ "$unit_backup" -eq 1 ]; then sudo rm -f "$UNIT.install-backup" || :; fi
    if [ "$net_unit_backup" -eq 1 ]; then sudo rm -f "$NET_UNIT.install-backup" || :; fi
    if [ "$net_helper_backup" -eq 1 ]; then sudo rm -f "$NET_HELPER.install-backup" || :; fi
  fi
}
trap 'cleanup_install' 0
trap 'exit 1' HUP INT TERM

for required in serverdesk-linux-amd64 config.example.json deploy/serverdesk.service \
  deploy/serverdesk-net.service deploy/packaging/serverdesk-network.sh \
  deploy/packaging/serverdesk-network.env.example deploy/packaging/serverdesk-endpoint.sh \
  "$COLLECTOR_PREFLIGHT"; do
  if [ -L "$required" ] || [ ! -f "$required" ]; then
    echo "[FAIL] required package file is missing or unsafe: $required" >&2
    exit 1
  fi
done

for command in sudo systemctl getent useradd usermod install stat cp cmp mv curl jq find ip readlink realpath dirname awk; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "[FAIL] required deployment command is unavailable: $command" >&2
    exit 1
  }
done
case "$ALLOW_DEGRADED_COLLECTION" in 0|1) ;; *)
  echo '[FAIL] SERVERDESK_ALLOW_DEGRADED_COLLECTION must be 0 or 1' >&2
  exit 1
esac
unexpected_mib_payload=
if [ -e docs/mibs ] || [ -L docs/mibs ]; then
  if [ -L docs/mibs ] || [ ! -d docs/mibs ]; then
    unexpected_mib_payload=docs/mibs
  else
    unexpected_mib_payload=$(find docs/mibs -mindepth 1 ! -path docs/mibs/README.md -print -quit)
    if [ -L docs/mibs/README.md ] ||
      { [ -e docs/mibs/README.md ] && [ ! -f docs/mibs/README.md ]; }; then
      unexpected_mib_payload=docs/mibs/README.md
    fi
  fi
fi
if [ -e avcli.zip ] || [ -L avcli.zip ] || [ -e jre.zip ] || [ -L jre.zip ] ||
  [ -n "$unexpected_mib_payload" ]; then
  echo '[FAIL] release payload contains vendor AVCLI/JRE/MIB files; provision licensed dependencies separately' >&2
  exit 1
fi

# Catch malformed package assets before stopping a running installation.
sh -n deploy/packaging/serverdesk-network.sh
sh deploy/packaging/serverdesk-network.sh \
  --config deploy/packaging/serverdesk-network.env.example validate > /dev/null
if ! grep -q '"secret_policy"[[:space:]]*:[[:space:]]*"require-references"' config.example.json; then
  echo '[FAIL] config.example.json must enforce secret_policy=require-references' >&2
  exit 1
fi

mode_is_group_or_world_writable() {
  case "$1" in *[2367]?|*[2367]) return 0 ;; *) return 1 ;; esac
}

mode_has_special_bits() {
  case "$1" in ???) return 1 ;; *) return 0 ;; esac
}

validate_root_directory() {
  path=$1
  description=$2
  if [ -L "$path" ] || { [ -e "$path" ] && [ ! -d "$path" ]; }; then
    echo "[FAIL] $description must be a regular directory: $path" >&2
    exit 1
  fi
  if [ -e "$path" ]; then
    owner=$(sudo stat -c '%U:%G' "$path")
    mode=$(sudo stat -c '%a' "$path")
    if [ "$owner" != root:root ] || mode_is_group_or_world_writable "$mode" ||
      mode_has_special_bits "$mode"; then
      echo "[FAIL] $description must be root-owned without special or group/world-write bits: $path" >&2
      exit 1
    fi
  fi
}

validate_package_target() {
  path=$1
  description=$2
  if [ -L "$path" ] || { [ -e "$path" ] && [ ! -f "$path" ]; }; then
    echo "[FAIL] $description must be a regular non-symlink file: $path" >&2
    exit 1
  fi
  if [ -e "$path" ]; then
    owner=$(sudo stat -c '%U:%G' "$path")
    links=$(sudo stat -c '%h' "$path")
    mode=$(sudo stat -c '%a' "$path")
    if [ "$owner" != root:root ] || [ "$links" -ne 1 ] ||
      mode_is_group_or_world_writable "$mode" || mode_has_special_bits "$mode"; then
      echo "[FAIL] $description must be root-owned, single-link, and have no special/group/world-write bits: $path" >&2
      exit 1
    fi
  fi
}

# Validate every root-written path before creating users, directories, backups,
# temporary files, or stopping a running installation.
validate_root_directory "$DST" 'program directory'
if [ -e "$DST" ]; then
  unsafe_child=$(sudo find "$DST" -mindepth 1 -type l -print -quit)
  if [ -n "$unsafe_child" ]; then
    echo "[FAIL] program directory contains a symlink: $unsafe_child" >&2
    exit 1
  fi
fi
validate_package_target "$DST/serverdesk" 'installed serverdesk binary'
validate_package_target "$NET_HELPER" 'installed network helper'
validate_package_target "$UNIT" 'serverdesk service unit'
validate_package_target "$NET_UNIT" 'serverdesk network service unit'
for transaction_path in \
  "$DST/.serverdesk.new" "$DST/serverdesk.install-backup" \
  "$DST/.serverdesk-network.new" "$NET_HELPER.install-backup" \
  "$UNIT.install-backup" "$NET_UNIT.install-backup"; do
  if [ -e "$transaction_path" ] || [ -L "$transaction_path" ]; then
    echo "[FAIL] stale deployment transaction path requires operator inspection: $transaction_path" >&2
    exit 1
  fi
done

if ! getent passwd "$SERVICE_USER" > /dev/null; then
  sudo useradd --system --home-dir "$STATE" --shell /usr/sbin/nologin --user-group "$SERVICE_USER"
fi
sudo usermod --home "$STATE" --shell /usr/sbin/nologin --lock "$SERVICE_USER"

if [ -L "$STATE" ]; then
  echo "[FAIL] state directory must not be a symlink: $STATE" >&2
  exit 1
fi

if [ -L "$CONFIG_DIR" ] || { [ -e "$CONFIG_DIR" ] && [ ! -d "$CONFIG_DIR" ]; }; then
  echo "[FAIL] deployment config directory must be a regular directory: $CONFIG_DIR" >&2
  exit 1
fi
if [ ! -e "$CONFIG_DIR" ]; then
  sudo install -d -o root -g root -m 700 "$CONFIG_DIR"
elif [ "$(sudo stat -c '%U:%G' "$CONFIG_DIR")" != "root:root" ]; then
  echo "[FAIL] deployment config directory must be root-owned: $CONFIG_DIR" >&2
  exit 1
else
  sudo chmod 700 "$CONFIG_DIR"
fi

if [ -L "$CONFIG_DIR/credentials" ] ||
  { [ -e "$CONFIG_DIR/credentials" ] && [ ! -d "$CONFIG_DIR/credentials" ]; }; then
  echo "[FAIL] credential source must be a regular directory: $CONFIG_DIR/credentials" >&2
  exit 1
fi
if [ ! -e "$CONFIG_DIR/credentials" ]; then
  sudo install -d -o root -g root -m 700 "$CONFIG_DIR/credentials"
elif [ "$(sudo stat -c '%U:%G' "$CONFIG_DIR/credentials")" != 'root:root' ]; then
  echo "[FAIL] credential source must be root-owned: $CONFIG_DIR/credentials" >&2
  exit 1
else
  sudo chmod 700 "$CONFIG_DIR/credentials"
fi

if [ -e "$NET_CONFIG" ] || [ -L "$NET_CONFIG" ]; then
  if [ -L "$NET_CONFIG" ] || [ ! -f "$NET_CONFIG" ] ||
    [ "$(sudo stat -c '%U:%G' "$NET_CONFIG")" != "root:root" ] ||
    [ "$(sudo stat -c '%a' "$NET_CONFIG")" != 600 ]; then
    echo "[FAIL] $NET_CONFIG must be a root-owned regular file with mode 600" >&2
    exit 1
  fi
  sudo sh deploy/packaging/serverdesk-network.sh --config "$NET_CONFIG" validate > /dev/null
else
  sudo install -o root -g root -m 600 \
    deploy/packaging/serverdesk-network.env.example "$NET_CONFIG"
  net_config_created=1
fi
if [ -e "$STATE" ]; then
  if [ ! -d "$STATE" ] ||
    [ "$(sudo stat -c '%U:%G' "$STATE")" != "$SERVICE_USER:$SERVICE_USER" ] ||
    [ "$(sudo stat -c '%a' "$STATE")" != 700 ]; then
    echo "[FAIL] state directory must be $SERVICE_USER-owned mode 700: $STATE" >&2
    exit 1
  fi
else
  sudo install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 700 "$STATE"
fi
if [ -L "$CREDENTIAL_STORE" ] || { [ -e "$CREDENTIAL_STORE" ] && [ ! -d "$CREDENTIAL_STORE" ]; }; then
  echo "[FAIL] runtime credential store must be a regular directory: $CREDENTIAL_STORE" >&2
  exit 1
fi
if [ ! -e "$CREDENTIAL_STORE" ]; then
  sudo install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 700 "$CREDENTIAL_STORE"
elif [ "$(sudo stat -c '%U:%G' "$CREDENTIAL_STORE")" != "$SERVICE_USER:$SERVICE_USER" ] ||
  [ "$(sudo stat -c '%a' "$CREDENTIAL_STORE")" != 700 ]; then
  echo "[FAIL] runtime credential store must be $SERVICE_USER-owned mode 700: $CREDENTIAL_STORE" >&2
  exit 1
fi

if [ -L "$DST" ] || { [ -e "$DST" ] && [ ! -d "$DST" ]; }; then
  echo "[FAIL] program directory must be a regular directory: $DST" >&2
  exit 1
fi
if [ ! -e "$DST" ]; then
  sudo install -d -o root -g root -m 755 "$DST"
fi
if [ -L "$MIB_DIR" ] || { [ -e "$MIB_DIR" ] && [ ! -d "$MIB_DIR" ]; }; then
  echo "[FAIL] MIB path must be a regular directory: $MIB_DIR" >&2
  exit 1
fi
if [ ! -e "$MIB_DIR" ]; then
  sudo install -d -o root -g root -m 755 "$MIB_DIR"
else
  mib_owner=$(sudo stat -c '%U:%G' "$MIB_DIR")
  mib_mode=$(sudo stat -c '%a' "$MIB_DIR")
  if [ "$mib_owner" != root:root ] || mode_is_group_or_world_writable "$mib_mode" ||
    mode_has_special_bits "$mib_mode"; then
    echo "[FAIL] existing MIB directory must be root-owned without special or group/world-write bits: $MIB_DIR" >&2
    exit 1
  fi
fi
echo "[INFO] Licensed MIB files may be provisioned separately in $MIB_DIR; vendor MIBs are not bundled."

validate_state_file() {
  path=$1
  if [ -L "$path" ] || [ ! -f "$path" ]; then
    echo "[FAIL] state file must be a regular non-symlink file: $path" >&2
    exit 1
  fi
  if [ "$(sudo stat -c '%U:%G' "$path")" != "$SERVICE_USER:$SERVICE_USER" ] ||
    [ "$(sudo stat -c '%a' "$path")" != 600 ]; then
    echo "[FAIL] state file must be $SERVICE_USER-owned mode 600: $path" >&2
    exit 1
  fi
}

if [ -e "$STATE/config.local.json" ] || [ -L "$STATE/config.local.json" ]; then
  validate_state_file "$STATE/config.local.json"
else
  sudo install -o "$SERVICE_USER" -g "$SERVICE_USER" -m 600 config.example.json "$STATE/config.local.json"
  echo "[INFO] edit $STATE/config.local.json (credentials), then restart serverdesk"
fi

HEALTH_URL=$(sudo sh deploy/packaging/serverdesk-endpoint.sh \
  "$STATE/config.local.json" "$HEALTH_URL_OVERRIDE" "$SERVICE_USER") || {
  echo '[FAIL] configured listen/TLS/health endpoint failed preflight' >&2
  exit 1
}
echo "[INFO] local health endpoint: $HEALTH_URL"
configured_tls_key=$(sudo jq -er '.tls_key_file // "" | select(type == "string")' \
  "$STATE/config.local.json") || {
  echo '[FAIL] tls_key_file must be a string' >&2
  exit 1
}
if [ -n "$configured_tls_key" ]; then
  case "$configured_tls_key" in
    /*) readable_tls_key=$configured_tls_key ;;
    *) readable_tls_key=$STATE/$configured_tls_key ;;
  esac
  readable_tls_key=$(realpath -s "$readable_tls_key")
  case "$readable_tls_key" in
    "$STATE"|"$STATE"/*)
      echo "[FAIL] TLS private key must not be stored in the service-writable state tree: $readable_tls_key" >&2
      exit 1
      ;;
  esac
  tls_parent=$(dirname "$readable_tls_key")
  while :; do
    tls_parent_owner=$(sudo stat -c '%U:%G' "$tls_parent")
    tls_parent_mode=$(sudo stat -c '%a' "$tls_parent")
    if [ "$tls_parent_owner" != root:root ] ||
      mode_is_group_or_world_writable "$tls_parent_mode" ||
      mode_has_special_bits "$tls_parent_mode"; then
      echo "[FAIL] TLS key parent must be root-owned without special/group/world-write bits: $tls_parent" >&2
      exit 1
    fi
    [ "$tls_parent" = / ] && break
    tls_parent=$(dirname "$tls_parent")
  done
  if ! sudo -u "$SERVICE_USER" test -r "$readable_tls_key"; then
    echo "[FAIL] TLS private key is not readable by $SERVICE_USER: $readable_tls_key" >&2
    exit 1
  fi
fi

# Validate licensed Stratus collector dependencies before a running service is stopped.
cluster_count=$(sudo jq -er '(.clusters // []) | if type == "array" then length else error("clusters must be an array") end' \
  "$STATE/config.local.json") || {
  echo '[FAIL] config.local.json is invalid or clusters is not an array' >&2
  exit 1
}
if [ "$cluster_count" -gt 0 ]; then
  proc_options=$(awk '$2 == "/proc" { print $4; exit }' /proc/mounts)
  argv_protected=0
  case ",$proc_options," in
    *,hidepid=1,*|*,hidepid=2,*|*,hidepid=4,*|*,hidepid=noaccess,*|*,hidepid=invisible,*|*,hidepid=ptraceable,*)
      argv_protected=1
      ;;
  esac
  if [ "$argv_protected" -eq 0 ]; then
    login_accounts=$(getent passwd | awk -F: -v service_user="$SERVICE_USER" '
      $3 >= 1000 && $3 < 65534 && $1 != service_user && $7 != "" &&
      $7 !~ /(nologin|false|sync)$/ { print $1 }
    ')
    if [ -n "$login_accounts" ]; then
      echo '[FAIL] Stratus collection requires host-wide /proc hidepid protection when interactive accounts exist' >&2
      echo "       interactive accounts: $(printf '%s' "$login_accounts" | tr '\n' ' ')" >&2
      echo '       packaged installation has no argv-exposure bypass; configure hidepid=2 (recommended) before retrying' >&2
      exit 1
    else
      echo '[OK] no non-root interactive login accounts can inspect AVCLI process arguments'
    fi
  else
    echo '[OK] host /proc hidepid protects AVCLI process arguments from other users'
  fi

  avcli_bin=$(sudo jq -er '.avcli_bin // "avcli" | select(type == "string" and length > 0)' \
    "$STATE/config.local.json") || {
    echo '[FAIL] configured Stratus clusters require a non-empty avcli_bin' >&2
    exit 1
  }
  case "$avcli_bin" in
    /*) avcli_path=$avcli_bin ;;
    */*) avcli_path=$STATE/$avcli_bin ;;
    *) avcli_path=$(sudo -u "$SERVICE_USER" sh -c 'command -v "$1" || :' sh "$avcli_bin") ;;
  esac
  avcli_problem=
  if [ -z "$avcli_path" ] || [ ! -f "$avcli_path" ] || [ ! -x "$avcli_path" ]; then
    avcli_problem="AVCLI executable is unavailable: $avcli_bin"
  else
    avcli_path=$(sh "$COLLECTOR_PREFLIGHT" "$avcli_path" 'AVCLI executable' 1 "$SERVICE_USER")
  fi
  case "$avcli_path" in
    *java|*java[0-9])
      avcli_jar=$(sudo jq -er '.avcli_args // [] |
        select(type == "array") |
        index("-jar") as $jar_index |
        select($jar_index != null and ($jar_index + 1) < length) |
        .[$jar_index + 1] |
        select(type == "string" and length > 0)' \
        "$STATE/config.local.json" 2>/dev/null || :)
      case "$avcli_jar" in /*) ;; *) avcli_jar=$STATE/$avcli_jar ;; esac
      if [ ! -f "$avcli_jar" ]; then
        avcli_problem="Java AVCLI configuration is missing a readable -jar target: $avcli_jar"
      else
        avcli_jar=$(sh "$COLLECTOR_PREFLIGHT" "$avcli_jar" 'AVCLI JAR' 0 "$SERVICE_USER")
      fi
      ;;
  esac
  if [ -n "$avcli_problem" ]; then
    if [ "$ALLOW_DEGRADED_COLLECTION" -eq 1 ]; then
      echo "[WARN] $avcli_problem; Stratus collection is explicitly accepted as degraded" >&2
    else
      echo "[FAIL] $avcli_problem" >&2
      echo '       provision licensed Stratus AVCLI/JRE, or explicitly acknowledge degradation with SERVERDESK_ALLOW_DEGRADED_COLLECTION=1' >&2
      exit 1
    fi
  else
    echo "[OK] AVCLI/JRE preflight passed: $avcli_path"
  fi
else
  echo '[INFO] no Stratus clusters configured; AVCLI/JRE preflight is deferred'
fi

if [ -e "$STATE/auth.json" ] || [ -L "$STATE/auth.json" ]; then
  validate_state_file "$STATE/auth.json"
  if ! sudo ./serverdesk-linux-amd64 -auth "$STATE/auth.json" -check-auth > /dev/null; then
    echo "[FAIL] existing auth.json failed strict validation" >&2
    exit 1
  fi
  if [ -e "$STATE/initial-login.txt" ] || [ -L "$STATE/initial-login.txt" ]; then
    validate_state_file "$STATE/initial-login.txt"
  fi
elif [ -e "$STATE/initial-login.txt" ] || [ -L "$STATE/initial-login.txt" ]; then
  echo "[FAIL] initial-login.txt exists without auth.json" >&2
  exit 1
fi

if [ -f "$DST/serverdesk" ]; then
  installation_was_existing=1
  sudo cp -a "$DST/serverdesk" "$DST/serverdesk.install-backup"
  sudo cmp -s "$DST/serverdesk" "$DST/serverdesk.install-backup" || {
    echo '[FAIL] binary backup verification failed before service stop' >&2
    exit 1
  }
  binary_backup=1
else
  binary_created=1
fi
if [ -f "$UNIT" ]; then
  sudo cp -a "$UNIT" "$UNIT.install-backup"
  sudo cmp -s "$UNIT" "$UNIT.install-backup" || {
    echo '[FAIL] service-unit backup verification failed before service stop' >&2
    exit 1
  }
  unit_backup=1
else
  unit_created=1
fi
if [ -f "$NET_UNIT" ]; then
  sudo cp -a "$NET_UNIT" "$NET_UNIT.install-backup"
  sudo cmp -s "$NET_UNIT" "$NET_UNIT.install-backup" || {
    echo '[FAIL] network-unit backup verification failed before service stop' >&2
    exit 1
  }
  net_unit_backup=1
else
  net_unit_created=1
fi
if [ -f "$NET_HELPER" ]; then
  sudo cp -a "$NET_HELPER" "$NET_HELPER.install-backup"
  sudo cmp -s "$NET_HELPER" "$NET_HELPER.install-backup" || {
    echo '[FAIL] network-helper backup verification failed before service stop' >&2
    exit 1
  }
  net_helper_backup=1
else
  net_helper_created=1
fi

if sudo systemctl is-enabled --quiet serverdesk; then
  service_was_enabled=1
fi
if sudo systemctl is-enabled --quiet serverdesk-net; then
  net_was_enabled=1
fi
services_touched=1
if sudo systemctl is-active --quiet serverdesk; then
  service_was_active=1
  sudo systemctl stop serverdesk
fi
if sudo systemctl is-active --quiet serverdesk-net; then
  net_was_active=1
  sudo systemctl stop serverdesk-net
fi

# Stage executable files beside their destination, then atomically rename.
sudo install -o root -g root -m 755 serverdesk-linux-amd64 "$DST/.serverdesk.new"
sudo mv -f "$DST/.serverdesk.new" "$DST/serverdesk"
sudo install -o root -g root -m 755 deploy/packaging/serverdesk-network.sh "$DST/.serverdesk-network.new"
sudo mv -f "$DST/.serverdesk-network.new" "$NET_HELPER"

if [ ! -e "$STATE/auth.json" ]; then
  auth_created=1
  auth_init_tmp=$(sudo -u "$SERVICE_USER" mktemp "$STATE/.initial-login.XXXXXX")
  if ! sudo -u "$SERVICE_USER" sh -c \
    'umask 077; exec "$1" -auth "$2" -init-auth > "$3"' \
    sh "$DST/serverdesk" "$STATE/auth.json" "$auth_init_tmp"; then
    echo "[FAIL] authentication initialization failed" >&2
    exit 1
  fi
  if [ "$(sudo -u "$SERVICE_USER" sed -n '$=' "$auth_init_tmp")" != 2 ] ||
    ! sudo -u "$SERVICE_USER" sed -n '1p' "$auth_init_tmp" | grep -q '^ADMIN_USERNAME=..*$' ||
    ! sudo -u "$SERVICE_USER" sed -n '2p' "$auth_init_tmp" | grep -q '^ADMIN_PASSWORD=..*$'; then
    echo "[FAIL] authentication initialization returned malformed credentials" >&2
    exit 1
  fi
  sudo -u "$SERVICE_USER" mv "$auth_init_tmp" "$STATE/initial-login.txt"
  auth_init_tmp=
  validate_state_file "$STATE/auth.json"
  validate_state_file "$STATE/initial-login.txt"
fi

sudo install -o root -g root -m 644 deploy/serverdesk.service "$UNIT"
sudo install -o root -g root -m 644 deploy/serverdesk-net.service "$NET_UNIT"
if command -v systemd-analyze > /dev/null 2>&1; then
  sudo systemd-analyze verify "$UNIT" "$NET_UNIT"
fi
sudo systemctl daemon-reload
if [ "$installation_was_existing" -eq 0 ]; then
  sudo systemctl enable serverdesk serverdesk-net
else
  if [ "$service_was_enabled" -eq 1 ]; then
    sudo systemctl enable serverdesk
  else
    sudo systemctl disable serverdesk
  fi
  if [ "$net_was_enabled" -eq 1 ]; then
    sudo systemctl enable serverdesk-net
  else
    sudo systemctl disable serverdesk-net
  fi
fi
sudo systemctl restart serverdesk-net
sudo systemctl restart serverdesk
sleep 6

main_pid=$(sudo systemctl show -p MainPID --value serverdesk)
if [ "$main_pid" -le 0 ] ||
  [ "$(sudo readlink "/proc/$main_pid/exe")" != "$DST/serverdesk" ] ||
  ! curl --fail --silent --show-error --proto '=http,https' \
    --connect-timeout 3 --max-time 10 "$HEALTH_URL" > /dev/null; then
  echo "[FAIL] restarted service health check failed - journalctl -u serverdesk" >&2
  exit 1
fi

# An update validates the new service transiently, then restores prior active state.
if [ "$installation_was_existing" -eq 1 ] && [ "$service_was_active" -eq 0 ]; then
  sudo systemctl stop serverdesk
fi
if [ "$installation_was_existing" -eq 1 ] && [ "$net_was_active" -eq 0 ]; then
  sudo systemctl stop serverdesk-net
fi

install_complete=1
if [ "$auth_created" -eq 1 ]; then
  echo "[INFO] Initial web credentials are in $STATE/initial-login.txt; securely record and remove or rotate them."
fi
sudo rm -f "$DST/serverdesk.install-backup" "$UNIT.install-backup" \
  "$NET_UNIT.install-backup" "$NET_HELPER.install-backup"
if [ "$installation_was_existing" -eq 1 ] && [ "$service_was_active" -eq 0 ]; then
  echo "[OK] update passed transient health validation and the previously stopped service remains stopped - health=$HEALTH_URL"
else
  echo "[OK] serverdesk is up - health=$HEALTH_URL"
fi
