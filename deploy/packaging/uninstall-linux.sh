#!/bin/sh
# serverdesk Linux uninstaller:
#   sudo sh uninstall-linux.sh          (keeps state, config, and auth)
#   sudo sh uninstall-linux.sh --full   (removes everything)
set -eu
DST=/opt/serverdesk
STATE=/var/lib/serverdesk
SERVICE_USER=serverdesk

if sudo systemctl is-active --quiet serverdesk; then
  sudo systemctl stop serverdesk
fi
sudo systemctl disable serverdesk || true
sudo rm -f /etc/systemd/system/serverdesk.service
sudo systemctl daemon-reload
if [ "${1:-}" = "--full" ]; then
  sudo rm -rf "$DST" "$STATE"
  if getent passwd "$SERVICE_USER" > /dev/null; then
    sudo userdel "$SERVICE_USER"
  fi
  echo "[OK] removed completely (unit, $DST, and $STATE)"
else
  sudo rm -rf "$DST"
  echo "[OK] removed program files - kept $STATE (config.local.json and auth.json; use --full to remove everything)"
fi
