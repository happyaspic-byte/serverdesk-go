#!/bin/sh
# serverdesk Linux updater (run from the NEW package root). The installer is
# intentionally idempotent and owns the complete binary + unit transaction, so
# updates use the exact same validation, rollback, and health-check path.
set -eu

[ "$(id -u)" = 0 ] || { echo '[FAIL] run with sudo' >&2; exit 1; }
INSTALLER=deploy/packaging/install-linux.sh
if [ ! -f "$INSTALLER" ] || [ -L "$INSTALLER" ]; then
  echo "[FAIL] missing or unsafe installer: $INSTALLER" >&2
  exit 1
fi

exec sh "$INSTALLER" --update
