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
HEALTH_URL=${SERVERDESK_HEALTH_URL:-http://127.0.0.1:6005/api/health}
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

case "$HEALTH_URL" in
  http://*) health_rest=${HEALTH_URL#http://} ;;
  https://*) health_rest=${HEALTH_URL#https://} ;;
  *) health_rest= ;;
esac
health_authority=${health_rest%%/*}
health_path=/${health_rest#*/}
health_host=${health_authority%:*}
health_port=${health_authority##*:}
case "$health_host" in 127.0.0.1|localhost) ;; *) health_host= ;; esac
case "$health_port" in ''|*[!0-9]*) health_port= ;; esac
if [ -z "$health_rest" ] || [ -z "$health_host" ] || [ -z "$health_port" ] ||
  [ "$health_rest" = "$health_authority" ] || [ "$health_path" != /api/health ] ||
  [ "$health_port" -lt 1 ] 2>/dev/null || [ "$health_port" -gt 65535 ] 2>/dev/null; then
  echo "[FAIL] SERVERDESK_HEALTH_URL must be http(s)://localhost:PORT/api/health or use 127.0.0.1" >&2
  exit 1
fi

cleanup_install() {
  if [ -n "$auth_init_tmp" ]; then
    sudo -u "$SERVICE_USER" rm -f "$auth_init_tmp" || :
  fi
  if [ "$auth_created" -eq 1 ]; then
    sudo -u "$SERVICE_USER" rm -f "$STATE/auth.json" "$STATE/initial-login.txt" || :
  fi
  if [ "$install_complete" -eq 0 ]; then
    if [ "$services_touched" -eq 1 ]; then
      sudo systemctl stop serverdesk serverdesk-net || :
      if [ "$service_was_enabled" -eq 0 ]; then
        sudo systemctl disable serverdesk || :
      fi
      if [ "$net_was_enabled" -eq 0 ]; then
        sudo systemctl disable serverdesk-net || :
      fi
    fi
    if [ "$binary_backup" -eq 1 ] && [ -f "$DST/serverdesk.install-backup" ]; then
      sudo install -o root -g root -m 755 "$DST/serverdesk.install-backup" "$DST/serverdesk" || :
    fi
    if [ "$unit_backup" -eq 1 ] && [ -f "$UNIT.install-backup" ]; then
      sudo cp "$UNIT.install-backup" "$UNIT" || :
    elif [ "$unit_created" -eq 1 ]; then
      sudo rm -f "$UNIT" || :
    fi
    if [ "$net_unit_backup" -eq 1 ] && [ -f "$NET_UNIT.install-backup" ]; then
      sudo cp "$NET_UNIT.install-backup" "$NET_UNIT" || :
    elif [ "$net_unit_created" -eq 1 ]; then
      sudo rm -f "$NET_UNIT" || :
    fi
    if [ "$net_helper_backup" -eq 1 ] && [ -f "$NET_HELPER.install-backup" ]; then
      sudo install -o root -g root -m 755 "$NET_HELPER.install-backup" "$NET_HELPER" || :
    elif [ "$net_helper_created" -eq 1 ]; then
      sudo rm -f "$NET_HELPER" || :
    fi
    if [ "$binary_created" -eq 1 ] && [ "$binary_backup" -eq 0 ]; then
      sudo rm -f "$DST/serverdesk" || :
    fi
    if [ "$net_config_created" -eq 1 ]; then
      sudo rm -f "$NET_CONFIG" || :
    fi
    sudo systemctl daemon-reload || :
    if [ "$net_was_active" -eq 1 ]; then
      sudo systemctl restart serverdesk-net || :
    fi
    if [ "$service_was_active" -eq 1 ]; then
      sudo systemctl restart serverdesk || :
    fi
  fi
  sudo rm -f "$DST/serverdesk.install-backup" "$UNIT.install-backup" \
    "$NET_UNIT.install-backup" "$NET_HELPER.install-backup" || :
}
trap 'cleanup_install' 0
trap 'exit 1' HUP INT TERM

for required in serverdesk-linux-amd64 config.example.json deploy/serverdesk.service \
  deploy/serverdesk-net.service deploy/packaging/serverdesk-network.sh \
  deploy/packaging/serverdesk-network.env.example; do
  if [ -L "$required" ] || [ ! -f "$required" ]; then
    echo "[FAIL] required package file is missing or unsafe: $required" >&2
    exit 1
  fi
done

# Catch malformed package assets before stopping a running installation.
sh -n deploy/packaging/serverdesk-network.sh
sh deploy/packaging/serverdesk-network.sh \
  --config deploy/packaging/serverdesk-network.env.example validate > /dev/null
if ! grep -q '"secret_policy"[[:space:]]*:[[:space:]]*"require-references"' config.example.json; then
  echo '[FAIL] config.example.json must enforce secret_policy=require-references' >&2
  exit 1
fi

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

if [ -L "$DST" ] || { [ -e "$DST" ] && [ ! -d "$DST" ]; }; then
  echo "[FAIL] program directory must be a regular directory: $DST" >&2
  exit 1
fi
if [ ! -e "$DST" ]; then
  sudo install -d -o root -g root -m 755 "$DST"
elif [ "$(sudo stat -c '%U:%G' "$DST")" != "root:root" ]; then
  echo "[FAIL] program directory must be root-owned: $DST" >&2
  exit 1
fi

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
  sudo cp "$DST/serverdesk" "$DST/serverdesk.install-backup"
  binary_backup=1
else
  binary_created=1
fi
if [ -f "$UNIT" ]; then
  sudo cp "$UNIT" "$UNIT.install-backup"
  unit_backup=1
else
  unit_created=1
fi
if [ -f "$NET_UNIT" ]; then
  sudo cp "$NET_UNIT" "$NET_UNIT.install-backup"
  net_unit_backup=1
else
  net_unit_created=1
fi
if [ -f "$NET_HELPER" ]; then
  sudo cp "$NET_HELPER" "$NET_HELPER.install-backup"
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
  auth_created=0
  echo "[INFO] Initial web credentials are in $STATE/initial-login.txt; securely record and remove or rotate them."
fi

sudo install -o root -g root -m 644 deploy/serverdesk.service "$UNIT"
sudo install -o root -g root -m 644 deploy/serverdesk-net.service "$NET_UNIT"
if command -v systemd-analyze > /dev/null 2>&1; then
  sudo systemd-analyze verify "$UNIT" "$NET_UNIT"
fi
sudo systemctl daemon-reload
sudo systemctl enable serverdesk serverdesk-net
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

install_complete=1
sudo rm -f "$DST/serverdesk.install-backup" "$UNIT.install-backup" \
  "$NET_UNIT.install-backup" "$NET_HELPER.install-backup"
echo "[OK] serverdesk is up - health=$HEALTH_URL"
