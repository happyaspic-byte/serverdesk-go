#!/bin/sh
# serverdesk Linux updater: sudo sh update.sh  (run from the NEW package folder)
set -eu
DST=/opt/serverdesk
STATE=/var/lib/serverdesk
[ -f "$DST/serverdesk" ] || { echo "[INFO] not installed - run install.sh instead"; exit 1; }
[ -d "$STATE" ] || { echo "[FAIL] missing state directory: $STATE" >&2; exit 1; }

sudo systemctl stop serverdesk
sudo cp "$DST/serverdesk" "$DST/serverdesk.bak"
sudo install -o root -g root -m 755 serverdesk-linux-amd64 "$DST/serverdesk"
sudo systemctl start serverdesk
sleep 6
if curl -sf http://127.0.0.1:6005/api/health > /dev/null; then
  echo "[OK] updated and healthy - previous build kept as serverdesk.bak; state remains in $STATE"
else
  echo "[FAIL] health check failed - rolling back"
  sudo systemctl stop serverdesk || true
  sudo install -o root -g root -m 755 "$DST/serverdesk.bak" "$DST/serverdesk"
  sudo systemctl start serverdesk
  echo "[INFO] rollback started"
  exit 1
fi
