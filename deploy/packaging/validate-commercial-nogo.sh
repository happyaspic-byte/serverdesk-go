#!/bin/sh
# Fail closed while the Windows packaged task still runs as SYSTEM.
# Does not require getent, systemd, or a Windows host.
set -eu

script_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd)
repo_root=$(CDPATH='' cd "$script_dir/../.." && pwd)
cd "$repo_root"

fail() {
  echo "[FAIL] $*" >&2
  exit 1
}

grep -F "UserId 'SYSTEM'" deploy/packaging/windows-deployment-common.ps1 >/dev/null ||
  fail "expected packaged Windows task principal UserId 'SYSTEM' on this branch (or update this contract with LocalService + UAT evidence)"

grep -F 'Current automatic NO-GO — Windows' docs/COMMERCIAL-UAT.md >/dev/null ||
  fail 'SYSTEM Windows task is packaged but COMMERCIAL-UAT.md lost the Windows NO-GO banner'

if grep -Ei 'Windows Server[^\n]*[Cc]ertified' docs/SUPPORT-MATRIX.md >/dev/null; then
  fail 'SUPPORT-MATRIX must not mark Windows Server certified while the task runs as SYSTEM'
fi

if grep -Ei 'paid support|certified product' docs/SUPPORT-MATRIX.md | grep -Ei 'everRun|ztC Edge' | grep -Eiv 'not certified|Pilot|None' >/dev/null; then
  fail 'SUPPORT-MATRIX must not advertise paid/certified Stratus support without a Gate A–B evidence sheet'
fi

echo "[OK] Windows SYSTEM packaging remains a documented commercial NO-GO"
