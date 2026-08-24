#!/bin/sh
# serverdesk Linux updater (run from the NEW package root). The installer is
# intentionally idempotent and owns the complete binary + unit transaction, so
# updates use the exact same validation, rollback, and health-check path.
set -eu

DST=/opt/serverdesk
STATE=/var/lib/serverdesk
INSTALLER=deploy/packaging/install-linux.sh

[ -f "$DST/serverdesk" ] || {
  echo "[INFO] not installed - run $INSTALLER instead" >&2
  exit 1
}
[ -d "$STATE" ] || {
  echo "[FAIL] missing state directory: $STATE" >&2
  exit 1
}
if [ ! -f "$INSTALLER" ] || [ -L "$INSTALLER" ]; then
  echo "[FAIL] missing or unsafe installer: $INSTALLER" >&2
  exit 1
fi

exec sh "$INSTALLER"
