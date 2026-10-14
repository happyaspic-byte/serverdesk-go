#!/bin/sh
# Fail closed until Windows LocalService packaging has completed real Windows Server UAT.
# Does not require getent, systemd, or a Windows host.
set -eu

script_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd)
repo_root=$(CDPATH='' cd "$script_dir/../.." && pwd)
cd "$repo_root"

fail() {
  echo "[FAIL] $*" >&2
  exit 1
}

grep -F "UserId 'NT AUTHORITY\\LOCAL SERVICE'" deploy/packaging/windows-deployment-common.ps1 >/dev/null ||
  fail "expected packaged Windows task principal UserId 'NT AUTHORITY\\LOCAL SERVICE' on this branch"

if grep -F "UserId 'SYSTEM'" deploy/packaging/windows-deployment-common.ps1 >/dev/null; then
  fail "packaged Windows task must not register as UserId 'SYSTEM'"
fi

grep -F 'Current automatic NO-GO — Windows' docs/COMMERCIAL-UAT.md >/dev/null ||
  fail 'Windows UAT is not yet certified but COMMERCIAL-UAT.md lost the Windows NO-GO banner'

if grep -Ei 'Windows Server[^\n]*[Cc]ertified' docs/SUPPORT-MATRIX.md >/dev/null; then
  fail 'SUPPORT-MATRIX must not mark Windows Server certified before real Windows Server UAT passes'
fi

if grep -Ei 'paid support|certified product' docs/SUPPORT-MATRIX.md | grep -Ei 'everRun|ztC Edge' | grep -Eiv 'not certified|Pilot|None' >/dev/null; then
  fail 'SUPPORT-MATRIX must not advertise paid/certified Stratus support without a Gate A–B evidence sheet'
fi

echo "[OK] Windows LocalService packaging remains gated on real Windows Server UAT"
