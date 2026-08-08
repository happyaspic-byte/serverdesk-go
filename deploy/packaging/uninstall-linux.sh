#!/bin/sh
# serverdesk Linux uninstaller:
#   sudo sh uninstall-linux.sh          (keeps config + data)
#   sudo sh uninstall-linux.sh --full   (removes everything)
set -eu
DST=/opt/serverdesk

sudo systemctl disable --now serverdesk 2>/dev/null || true
sudo rm -f /etc/systemd/system/serverdesk.service
sudo systemctl daemon-reload
if [ "${1:-}" = "--full" ]; then
  sudo rm -rf "$DST"
  echo "[OK] removed completely (unit + $DST)"
else
  sudo find "$DST" -mindepth 1 -maxdepth 1 ! -name data ! -name config.local.json -exec rm -rf {} + 2>/dev/null || true
  echo "[OK] removed program files - kept config.local.json and data/ (use --full to remove everything)"
fi
