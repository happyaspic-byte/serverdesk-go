#!/bin/sh
# Validate the archive layout consumed by the platform installers.
set -eu

[ "$#" -eq 2 ] || { echo "usage: $0 linux|windows STAGE_DIR" >&2; exit 2; }
platform=$1
stage=$2
[ -d "$stage" ] && [ ! -L "$stage" ] || { echo "invalid release stage: $stage" >&2; exit 1; }

for path in config.example.json README.md NOTICE SECURITY.md deploy docs licenses; do
  [ -e "$stage/$path" ] && [ ! -L "$stage/$path" ] || {
    echo "missing or unsafe release payload: $path" >&2
    exit 1
  }
done
for license in LICENSE-Pretendard.txt LICENSE-SUIT.txt; do
  [ -f "$stage/licenses/$license" ] || { echo "missing font license: $license" >&2; exit 1; }
done

case "$platform" in
  linux)
    [ -x "$stage/serverdesk-linux-amd64" ] || { echo 'missing executable serverdesk-linux-amd64' >&2; exit 1; }
    for path in deploy/packaging/install-linux.sh deploy/packaging/update-linux.sh \
      deploy/packaging/uninstall-linux.sh deploy/packaging/serverdesk-network.sh \
      deploy/serverdesk.service deploy/serverdesk-net.service; do
      [ -f "$stage/$path" ] || { echo "missing Linux payload: $path" >&2; exit 1; }
    done
    ;;
  windows)
    [ -f "$stage/serverdesk-windows-amd64.exe" ] || { echo 'missing serverdesk-windows-amd64.exe' >&2; exit 1; }
    for path in install-windows.ps1 update.ps1 uninstall.ps1 setup.bat; do
      [ -f "$stage/$path" ] || { echo "missing flattened Windows payload: $path" >&2; exit 1; }
    done
    ;;
  *) echo "unsupported platform: $platform" >&2; exit 2 ;;
esac

echo "[OK] $platform release payload contract is valid"
