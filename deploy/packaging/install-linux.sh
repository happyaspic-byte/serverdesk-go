#!/bin/sh
# serverdesk Linux installer: sudo sh install-linux.sh  (run from the package folder)
set -eu

DST=/opt/serverdesk
STATE=/var/lib/serverdesk
SERVICE_USER=serverdesk
UNIT=/etc/systemd/system/serverdesk.service
auth_init_tmp=
auth_created=0
service_was_active=0
install_complete=0
binary_backup=0
unit_backup=0

cleanup_install() {
  if [ -n "$auth_init_tmp" ]; then
    sudo -u "$SERVICE_USER" rm -f "$auth_init_tmp" || :
  fi
  if [ "$auth_created" -eq 1 ]; then
    sudo -u "$SERVICE_USER" rm -f "$STATE/auth.json" "$STATE/initial-login.txt" || :
  fi
  if [ "$install_complete" -eq 0 ]; then
    if [ "$binary_backup" -eq 1 ] && [ -f "$DST/serverdesk.install-backup" ]; then
      sudo install -o root -g root -m 755 "$DST/serverdesk.install-backup" "$DST/serverdesk" || :
    fi
    if [ "$unit_backup" -eq 1 ] && [ -f "$UNIT.install-backup" ]; then
      sudo cp "$UNIT.install-backup" "$UNIT" || :
      sudo systemctl daemon-reload || :
    fi
    if [ "$service_was_active" -eq 1 ]; then
      sudo systemctl restart serverdesk || :
    fi
  fi
  sudo rm -f "$DST/serverdesk.install-backup" "$UNIT.install-backup" || :
}
trap 'cleanup_install' 0
trap 'exit 1' HUP INT TERM

for required in serverdesk-linux-amd64 config.example.json deploy/serverdesk.service; do
  if [ -L "$required" ] || [ ! -f "$required" ]; then
    echo "[FAIL] required package file is missing or unsafe: $required" >&2
    exit 1
  fi
done

if ! getent passwd "$SERVICE_USER" > /dev/null; then
  sudo useradd --system --home-dir "$STATE" --shell /usr/sbin/nologin --user-group "$SERVICE_USER"
fi
sudo usermod --home "$STATE" --shell /usr/sbin/nologin --lock "$SERVICE_USER"

if sudo systemctl is-active --quiet serverdesk; then
  service_was_active=1
  sudo systemctl stop serverdesk
fi

if [ -L "$STATE" ]; then
  echo "[FAIL] state directory must not be a symlink: $STATE" >&2
  exit 1
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
fi
if [ -f "$UNIT" ]; then
  sudo cp "$UNIT" "$UNIT.install-backup"
  unit_backup=1
fi
sudo install -o root -g root -m 755 serverdesk-linux-amd64 "$DST/serverdesk"

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

sudo sed \
  -e "s|User=ubuntu|User=$SERVICE_USER|" \
  -e "s|WorkingDirectory=/home/ubuntu/projects/serverdesk-go|WorkingDirectory=$STATE|" \
  -e "s|EnvironmentFile=-/home/ubuntu/projects/serverdesk-go/serverdesk.local.env|EnvironmentFile=-$STATE/serverdesk.local.env|" \
  -e "s|/home/ubuntu/projects/serverdesk-go/serverdesk|$DST/serverdesk|g" \
  deploy/serverdesk.service | sudo tee "$UNIT" > /dev/null
sudo chmod 644 "$UNIT"
sudo systemctl daemon-reload
sudo systemctl enable serverdesk
sudo systemctl restart serverdesk
sleep 6

main_pid=$(sudo systemctl show -p MainPID --value serverdesk)
if [ "$main_pid" -le 0 ] ||
  [ "$(sudo readlink "/proc/$main_pid/exe")" != "$DST/serverdesk" ] ||
  ! curl -sf http://127.0.0.1:6005/api/health > /dev/null; then
  echo "[FAIL] restarted service health check failed - journalctl -u serverdesk" >&2
  exit 1
fi

install_complete=1
sudo rm -f "$DST/serverdesk.install-backup" "$UNIT.install-backup"
echo "[OK] serverdesk is up - http://<server-ip>:6005"
