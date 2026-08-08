#!/bin/sh
# serverdesk Linux installer: sudo sh install.sh  (run from the package folder)
set -eu
DST=/opt/serverdesk

sudo mkdir -p "$DST"
sudo cp serverdesk-linux-amd64 "$DST/serverdesk"
sudo chmod +x "$DST/serverdesk"
if [ ! -f "$DST/config.local.json" ]; then
  sudo cp config.example.json "$DST/config.local.json"
  sudo chmod 600 "$DST/config.local.json"
  echo "[INFO] edit $DST/config.local.json (credentials), then: sudo systemctl restart serverdesk"
fi
sudo cp deploy/serverdesk.service /etc/systemd/system/serverdesk.service
sudo sed -i "s|/home/ubuntu/projects/serverdesk-go|$DST|g" /etc/systemd/system/serverdesk.service
sudo systemctl daemon-reload
sudo systemctl enable --now serverdesk
sleep 6
if curl -sf http://127.0.0.1:6005/api/health > /dev/null; then
  echo "[OK] serverdesk is up - http://<server-ip>:6005"
else
  echo "[FAIL] health check failed - journalctl -u serverdesk"
  exit 1
fi
