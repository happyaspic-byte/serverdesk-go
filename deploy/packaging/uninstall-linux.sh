#!/bin/sh
# serverdesk Linux uninstaller:
#   sudo sh uninstall-linux.sh          # preserve customer state/config/credentials
#   sudo sh uninstall-linux.sh --full   # remove all Serverdesk-owned data
set -eu

[ "$(id -u)" = 0 ] || { echo '[FAIL] run with sudo' >&2; exit 1; }
# Installed helper preferred; a trusted package permits uninstalling older installs.
LOCK_HELPER=/opt/serverdesk/serverdesk-deployment-lock
if [ ! -e "$LOCK_HELPER" ] && [ ! -L "$LOCK_HELPER" ]; then
  LOCK_HELPER=deploy/packaging/serverdesk-deployment-lock.sh
fi
if [ -L "$LOCK_HELPER" ] || [ ! -f "$LOCK_HELPER" ] ||
  [ "$(realpath "$LOCK_HELPER")" != "$(realpath -s "$LOCK_HELPER")" ] ||
  [ "$(stat -c '%u:%g:%h' "$LOCK_HELPER")" != 0:0:1 ]; then
  echo '[FAIL] unsafe deployment lock helper' >&2; exit 1
fi
case "$(stat -c '%a' "$LOCK_HELPER")" in
  644|600|755|700) ;; *) echo '[FAIL] unsafe deployment lock helper permissions' >&2; exit 1 ;;
esac
helper_parent=$(dirname "$(realpath -s "$LOCK_HELPER")")
while :; do
  [ "$(stat -c '%u:%g' "$helper_parent")" = 0:0 ] || {
    echo '[FAIL] unsafe deployment lock helper parent owner' >&2; exit 1
  }
  case "$(stat -c '%a' "$helper_parent")" in
    *[2367]?|*[2367]|????) echo '[FAIL] unsafe deployment lock helper parent permissions' >&2; exit 1 ;;
  esac
  [ "$helper_parent" = / ] && break
  helper_parent=$(dirname "$helper_parent")
done
case "$LOCK_HELPER" in /*) . "$LOCK_HELPER" ;; *) . "./$LOCK_HELPER" ;; esac

DST=/opt/serverdesk
STATE=/var/lib/serverdesk
SERVICE_USER=serverdesk
CONFIG_DIR=/etc/serverdesk
UNIT=/etc/systemd/system/serverdesk.service
NET_UNIT=/etc/systemd/system/serverdesk-net.service

case "${1:-}" in ''|--full) ;; *)
  echo "usage: $0 [--full]" >&2
  exit 2
esac

for transaction_path in \
  "$DST/.serverdesk.new" "$DST/serverdesk.install-backup" \
  "$DST/.serverdesk-network.new" "$DST/serverdesk-network.install-backup" \
  "$UNIT.install-backup" "$NET_UNIT.install-backup" \
  "$DST/.serverdesk-deployment-lock.new" "$DST/serverdesk-deployment-lock.install-backup"; do
  if [ -e "$transaction_path" ] || [ -L "$transaction_path" ]; then
    echo "[FAIL] deployment transaction/recovery path requires operator inspection before uninstall: $transaction_path" >&2
    exit 1
  fi
done

for command in sudo systemctl rm rmdir getent; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "[FAIL] required command is unavailable: $command" >&2
    exit 1
  }
done

stop_disable_unit() {
  name=$1
  if sudo systemctl cat "$name" >/dev/null 2>&1; then
    if sudo systemctl is-active --quiet "$name"; then
      # serverdesk-net ExecStop removes only resources recorded as Serverdesk-owned.
      sudo systemctl stop "$name"
    fi
    sudo systemctl disable "$name"
  fi
}

# Preserve and retry ownership before deleting any executable or unit. The
# package helper handles upgrades from the old /run ownership format.
NETWORK_HELPER=deploy/packaging/serverdesk-network.sh
if [ -L "$NETWORK_HELPER" ] || [ ! -f "$NETWORK_HELPER" ] ||
  [ "$(realpath "$NETWORK_HELPER")" != "$(realpath -s "$NETWORK_HELPER")" ] ||
  [ "$(stat -c '%u:%g:%h' "$NETWORK_HELPER")" != 0:0:1 ]; then
  echo '[FAIL] unsafe network cleanup helper; use the trusted package' >&2; exit 1
fi
case "$(stat -c '%a' "$NETWORK_HELPER")" in
  644|600|755|700) ;; *) echo '[FAIL] unsafe network cleanup helper permissions' >&2; exit 1 ;;
esac
helper_parent=$(dirname "$(realpath -s "$NETWORK_HELPER")")
while :; do
  [ "$(stat -c '%u:%g' "$helper_parent")" = 0:0 ] || {
    echo '[FAIL] unsafe network cleanup helper parent owner' >&2; exit 1
  }
  case "$(stat -c '%a' "$helper_parent")" in
    *[2367]?|*[2367]|????) echo '[FAIL] unsafe network cleanup helper parent permissions' >&2; exit 1 ;;
  esac
  [ "$helper_parent" = / ] && break
  helper_parent=$(dirname "$helper_parent")
done
sudo sh "$NETWORK_HELPER" --config "$CONFIG_DIR/network.env" \
  --state /var/lib/serverdesk-net/applied.env migrate
stop_disable_unit serverdesk
stop_disable_unit serverdesk-net
sudo sh "$NETWORK_HELPER" --config /var/lib/serverdesk-net/applied.env \
  --expected-config "$CONFIG_DIR/network.env" remove
# Remove only the now-empty managed journal directory, never unknown contents.
if [ -d /var/lib/serverdesk-net ]; then sudo rmdir /var/lib/serverdesk-net; fi
sudo rm -f "$UNIT" "$NET_UNIT"
sudo systemctl daemon-reload

if sudo systemctl cat serverdesk >/dev/null 2>&1 || sudo systemctl cat serverdesk-net >/dev/null 2>&1; then
  echo '[FAIL] systemd unit removal could not be verified' >&2
  exit 1
fi

if [ "${1:-}" = '--full' ]; then
  sudo rm -rf /etc/systemd/system/serverdesk.service.d
  sudo rm -rf "$DST" "$STATE" "$CONFIG_DIR"
  for path in "$DST" "$STATE" "$CONFIG_DIR" /etc/systemd/system/serverdesk.service.d; do
    if [ -e "$path" ] || [ -L "$path" ]; then
      echo "[FAIL] complete removal could not remove: $path" >&2
      exit 1
    fi
  done
  if getent passwd "$SERVICE_USER" >/dev/null 2>&1; then
    sudo userdel "$SERVICE_USER"
  fi
  if getent passwd "$SERVICE_USER" >/dev/null 2>&1; then
    echo "[FAIL] service-account removal could not be verified: $SERVICE_USER" >&2
    exit 1
  fi
  echo "[OK] removed units, program, state, managed credentials, and deployment config"
else
  # Delete only package-owned executables. Preserve licensed/customer-provisioned
  # MIB, AVCLI, JRE, TLS, and any unknown files under /opt/serverdesk.
  sudo rm -f "$DST/serverdesk" "$DST/serverdesk-network" "$DST/serverdesk-deployment-lock"
  for path in "$DST/serverdesk" "$DST/serverdesk-network" "$DST/serverdesk-deployment-lock"; do
    if [ -e "$path" ] || [ -L "$path" ]; then
      echo "[FAIL] package-owned executable removal could not be verified: $path" >&2
      exit 1
    fi
  done
  echo "[OK] removed units and package executables; preserved $STATE, $CONFIG_DIR, and customer files under $DST"
  echo '     journal history is retained by systemd; use --full only after backing up customer state'
fi
