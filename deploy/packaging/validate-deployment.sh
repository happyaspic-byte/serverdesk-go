#!/bin/sh
# Rootless static checks for Linux packaging. This never changes networking,
# systemd state, /opt, /etc, or /var. CI and release builders can run it.
set -eu

fail() {
  echo "[FAIL] $*" >&2
  exit 1
}

script_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd)
repo_root=$(CDPATH='' cd "$script_dir/../.." && pwd)
cd "$repo_root"

for script in deploy/packaging/*.sh; do
  sh -n "$script" || fail "shell syntax: $script"
done

if SERVERDESK_HEALTH_URL=http://example.invalid/api/health \
  sh deploy/packaging/install-linux.sh >/dev/null 2>&1; then
  fail 'installer accepted a non-local health-check URL'
fi
if SERVERDESK_HEALTH_URL=http://localhost:6005@evil.example/api/health \
  sh deploy/packaging/install-linux.sh >/dev/null 2>&1; then
  fail 'installer accepted a userinfo health-check URL'
fi
if SERVERDESK_HEALTH_URL=http://localhost:6005/not-health \
  sh deploy/packaging/install-linux.sh >/dev/null 2>&1; then
  fail 'installer accepted an unexpected local health-check path'
fi
grep -F -q -- "--connect-timeout 3 --max-time 10" deploy/packaging/install-linux.sh ||
  fail 'installer health check must have connection and total timeouts'

# Regression guard for the values that previously modified a specific host.
if grep -R -n -E --exclude=validate-deployment.sh \
  '192\.168\.250\.84|ens3f0' deploy >/dev/null 2>&1; then
  fail 'legacy host IP/interface is hardcoded under deploy/'
fi
if grep -n '/bin/sh[[:space:]]\+-c' deploy/*.service >/dev/null 2>&1; then
  fail 'systemd units must not interpolate deployment data through a shell'
fi

for directive in \
  'User=serverdesk' \
  'NoNewPrivileges=yes' \
  'CapabilityBoundingSet=' \
  'ProtectSystem=strict' \
  'ProtectHome=yes' \
  'ReadWritePaths=/var/lib/serverdesk' \
  'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6'; do
  grep -F -q "$directive" deploy/serverdesk.service ||
    fail "serverdesk.service is missing hardening directive: $directive"
done
grep -F -q 'CapabilityBoundingSet=CAP_NET_ADMIN' deploy/serverdesk-net.service ||
  fail 'serverdesk-net.service must have only its documented network capability'
grep -F -q 'ExecStart=/opt/serverdesk/serverdesk-network --config /etc/serverdesk/network.env --state /run/serverdesk-net/applied.env apply' \
  deploy/serverdesk-net.service || fail 'network unit does not use the validated helper'
grep -q '"secret_policy"[[:space:]]*:[[:space:]]*"require-references"' config.example.json ||
  fail 'installed config template must require secret references'

sh deploy/packaging/serverdesk-network.sh \
  --config deploy/packaging/serverdesk-network.env.example validate >/dev/null

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

printf '%s\n' \
  'SERVERDESK_NET_INTERFACE=eth0' \
  'SERVERDESK_AUX_ADDRESS=192.0.2.10/24' \
  'SERVERDESK_ENABLE_TRAP_REDIRECT=true' \
  'SERVERDESK_TRAP_SOURCE_PORT=162' \
  'SERVERDESK_TRAP_TARGET_PORT=10162' > "$tmp_dir/valid.env"
sh deploy/packaging/serverdesk-network.sh --config "$tmp_dir/valid.env" validate >/dev/null

printf '%s\n' 'SERVERDESK_NET_INTERFACE=eth0;touch/tmp/pwned' > "$tmp_dir/injection.env"
if sh deploy/packaging/serverdesk-network.sh --config "$tmp_dir/injection.env" validate >/dev/null 2>&1; then
  fail 'network parser accepted shell metacharacters'
fi
printf '%s\n' 'SERVERDESK_AUX_ADDRESS=999.1.1.1/24' 'SERVERDESK_NET_INTERFACE=eth0' > "$tmp_dir/bad-ip.env"
if sh deploy/packaging/serverdesk-network.sh --config "$tmp_dir/bad-ip.env" validate >/dev/null 2>&1; then
  fail 'network parser accepted an invalid IPv4 CIDR'
fi
printf '%s\n' 'UNSUPPORTED=value' > "$tmp_dir/unknown.env"
if sh deploy/packaging/serverdesk-network.sh --config "$tmp_dir/unknown.env" validate >/dev/null 2>&1; then
  fail 'network parser accepted an unknown key'
fi

# Assemble both archive layouts with placeholder binaries and verify that the
# installers will find every required source, license, and platform filename.
for platform in linux windows; do
  stage="$tmp_dir/stage-$platform"
  mkdir -p "$stage/licenses"
  cp config.example.json README.md NOTICE SECURITY.md "$stage/"
  cp -R deploy docs "$stage/"
  cp web/fonts/LICENSE-*.txt "$stage/licenses/"
  if [ "$platform" = linux ]; then
    : > "$stage/serverdesk-linux-amd64"
    chmod 755 "$stage/serverdesk-linux-amd64"
  else
    : > "$stage/serverdesk-windows-amd64.exe"
    cp deploy/packaging/install-windows.ps1 deploy/packaging/update.ps1 \
      deploy/packaging/uninstall.ps1 deploy/packaging/setup.bat "$stage/"
  fi
  sh deploy/packaging/validate-release-payload.sh "$platform" "$stage" >/dev/null
done

if command -v systemd-analyze >/dev/null 2>&1; then
  # verify checks executable existence as well as syntax. Replace only command
  # paths in temporary copies so this remains rootless on build hosts.
  sed 's|^ExecStart=.*|ExecStart=/bin/true|; s|^ExecStop=.*|ExecStop=/bin/true|' \
    deploy/serverdesk.service > "$tmp_dir/serverdesk.service"
  sed 's|^ExecStart=.*|ExecStart=/bin/true|; s|^ExecStop=.*|ExecStop=/bin/true|' \
    deploy/serverdesk-net.service > "$tmp_dir/serverdesk-net.service"
  systemd-analyze verify "$tmp_dir/serverdesk.service" "$tmp_dir/serverdesk-net.service"
fi

if command -v shellcheck >/dev/null 2>&1; then
  shellcheck deploy/packaging/*.sh
fi

echo '[OK] deployment assets passed rootless validation'
