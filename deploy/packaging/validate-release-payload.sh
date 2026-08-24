#!/bin/sh
# Validate the archive layout consumed by the platform installers.
set -eu

[ "$#" -eq 2 ] || { echo "usage: $0 linux|windows STAGE_DIR" >&2; exit 2; }
platform=$1
stage=$2
if [ ! -d "$stage" ] || [ -L "$stage" ]; then
  echo "invalid release stage: $stage" >&2
  exit 1
fi

for path in config.example.json README.md NOTICE SECURITY.md THIRD_PARTY_NOTICES.md deploy docs licenses; do
  if [ ! -e "$stage/$path" ] || [ -L "$stage/$path" ]; then
    echo "missing or unsafe release payload: $path" >&2
    exit 1
  fi
done
for license in LICENSE-Pretendard.txt LICENSE-SUIT.txt LICENSE-Reicon.txt; do
  [ -f "$stage/licenses/$license" ] || { echo "missing third-party license: $license" >&2; exit 1; }
done
if find "$stage" -type f \( -iname '*STRATUS*MIB*' -o -iname 'avcli*.jar' -o -iname 'java.exe' \) | grep -q .; then
  echo 'vendor MIB/AVCLI/JRE artifacts must not be redistributed by the public release workflow' >&2
  exit 1
fi
for forbidden in avcli.zip jre.zip; do
  if [ -e "$stage/$forbidden" ] || [ -L "$stage/$forbidden" ]; then
    echo "vendor artifact must not be bundled: $forbidden" >&2
    exit 1
  fi
done
if [ -e "$stage/docs/mibs" ] || [ -L "$stage/docs/mibs" ]; then
  if [ -L "$stage/docs/mibs" ] || [ ! -d "$stage/docs/mibs" ] ||
    [ -n "$(find "$stage/docs/mibs" -mindepth 1 ! -path "$stage/docs/mibs/README.md" -print -quit)" ] ||
    [ -L "$stage/docs/mibs/README.md" ] ||
    { [ -e "$stage/docs/mibs/README.md" ] && [ ! -f "$stage/docs/mibs/README.md" ]; }; then
    echo 'vendor MIB files must not be bundled (only a regular docs/mibs/README.md is allowed)' >&2
    exit 1
  fi
fi

case "$platform" in
  linux)
    [ -x "$stage/serverdesk-linux-amd64" ] || { echo 'missing executable serverdesk-linux-amd64' >&2; exit 1; }
    for path in deploy/packaging/install-linux.sh deploy/packaging/update-linux.sh \
      deploy/packaging/uninstall-linux.sh deploy/packaging/serverdesk-network.sh \
      deploy/packaging/serverdesk-endpoint.sh deploy/packaging/serverdesk-collector-preflight.sh \
      deploy/serverdesk.service deploy/serverdesk-net.service; do
      [ -f "$stage/$path" ] || { echo "missing Linux payload: $path" >&2; exit 1; }
    done
    ;;
  windows)
    [ -f "$stage/serverdesk-windows-amd64.exe" ] || { echo 'missing serverdesk-windows-amd64.exe' >&2; exit 1; }
    for path in install-windows.ps1 update.ps1 uninstall.ps1 windows-deployment-common.ps1 setup.bat; do
      [ -f "$stage/$path" ] || { echo "missing flattened Windows payload: $path" >&2; exit 1; }
    done
    ;;
  *) echo "unsupported platform: $platform" >&2; exit 2 ;;
esac

echo "[OK] $platform release payload contract is valid"
