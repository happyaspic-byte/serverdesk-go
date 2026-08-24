#!/bin/sh
# serverdesk Linux uninstaller:
#   sudo sh uninstall-linux.sh          (keeps state, config, and auth)
#   sudo sh uninstall-linux.sh --full   (removes everything)
set -eu
DST=/opt/serverdesk
STATE=/var/lib/serverdesk
SERVICE_USER=serverdesk
CONFIG_DIR=/etc/serverdesk

if sudo systemctl is-active --quiet serverdesk; then
  sudo systemctl stop serverdesk
fi
sudo systemctl disable serverdesk || true
if sudo systemctl is-active --quiet serverdesk-net; then
  # ExecStop removes only rules carrying Serverdesk's ownership comment.
  sudo systemctl stop serverdesk-net
fi
sudo systemctl disable serverdesk-net || true
sudo rm -f /etc/systemd/system/serverdesk.service /etc/systemd/system/serverdesk-net.service
sudo systemctl daemon-reload
if [ "${1:-}" = "--full" ]; then
  sudo rm -rf /etc/systemd/system/serverdesk.service.d
  sudo rm -rf "$DST" "$STATE" "$CONFIG_DIR"
  if getent passwd "$SERVICE_USER" > /dev/null; then
    sudo userdel "$SERVICE_USER"
  fi
  echo "[OK] removed completely (units, $DST, $STATE, and $CONFIG_DIR)"
else
  sudo rm -rf "$DST"
  echo "[OK] removed program files - kept $STATE and $CONFIG_DIR; use --full to remove everything"
fi
