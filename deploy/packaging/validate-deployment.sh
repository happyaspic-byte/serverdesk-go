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

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

printf '%s\n' '{"listen":"127.0.0.1:7443"}' > "$tmp_dir/http.json"
[ "$(sh deploy/packaging/serverdesk-endpoint.sh "$tmp_dir/http.json")" = \
  'http://127.0.0.1:7443/api/health' ] || fail 'endpoint did not derive custom HTTP listen port'
if sh deploy/packaging/serverdesk-endpoint.sh "$tmp_dir/http.json" \
  'http://localhost:6005/api/health' >/dev/null 2>&1; then
  fail 'endpoint accepted a health URL with the wrong configured port'
fi
if sh deploy/packaging/serverdesk-endpoint.sh "$tmp_dir/http.json" \
  'http://localhost:7443@evil.example/api/health' >/dev/null 2>&1; then
  fail 'endpoint accepted a userinfo health-check URL'
fi
if sh deploy/packaging/serverdesk-endpoint.sh "$tmp_dir/http.json" \
  'http://localhost:7443/not-health' >/dev/null 2>&1; then
  fail 'endpoint accepted an unexpected health-check path'
fi
if sh deploy/packaging/serverdesk-endpoint.sh "$tmp_dir/http.json" \
  'http://192.0.2.1:7443/api/health' >/dev/null 2>&1; then
  fail 'endpoint accepted a non-local health-check address'
fi
mkdir -p "$tmp_dir/mock-net-bin"
printf '%s\n' '#!/bin/sh' \
  'case "$2" in' \
  '  127.attacker.example) printf "%s\\n" "203.0.113.10 STREAM attacker" ;;' \
  '  mixed.example) printf "%s\\n" "127.0.0.1 STREAM mixed" "203.0.113.11 STREAM mixed" ;;' \
  '  *) exec /usr/bin/getent "$@" ;;' \
  'esac' > "$tmp_dir/mock-net-bin/getent"
printf '%s\n' '#!/bin/sh' \
  'printf "%s\\n" "1: lo inet 127.0.0.1/8 scope host lo" "2: eth0 inet 192.0.2.10/24 scope global eth0"' \
  > "$tmp_dir/mock-net-bin/ip"
chmod 755 "$tmp_dir/mock-net-bin/getent" "$tmp_dir/mock-net-bin/ip"
if PATH="$tmp_dir/mock-net-bin:$PATH" sh deploy/packaging/serverdesk-endpoint.sh "$tmp_dir/http.json" \
  'http://127.attacker.example:7443/api/health' >/dev/null 2>&1; then
  fail 'endpoint mistook a 127-prefixed hostname for an IPv4 loopback address'
fi
if PATH="$tmp_dir/mock-net-bin:$PATH" sh deploy/packaging/serverdesk-endpoint.sh "$tmp_dir/http.json" \
  'http://mixed.example:7443/api/health' >/dev/null 2>&1; then
  fail 'endpoint accepted a hostname with mixed local and remote DNS answers'
fi
: > "$tmp_dir/tls.crt"
: > "$tmp_dir/tls.key"
chmod 600 "$tmp_dir/tls.key"
printf '%s\n' '{"listen":"0.0.0.0:6443","tls_cert_file":"tls.crt","tls_key_file":"tls.key"}' > "$tmp_dir/tls.json"
if sh deploy/packaging/serverdesk-endpoint.sh "$tmp_dir/tls.json" >/dev/null 2>&1; then
  fail 'wildcard TLS endpoint did not require an explicit certificate-valid health URL'
fi
[ "$(sh deploy/packaging/serverdesk-endpoint.sh "$tmp_dir/tls.json" \
  'https://localhost:6443/api/health' "$(id -un)")" = 'https://localhost:6443/api/health' ] ||
  fail 'explicit local TLS health endpoint was rejected'
chmod 644 "$tmp_dir/tls.key"
if sh deploy/packaging/serverdesk-endpoint.sh "$tmp_dir/tls.json" \
  'https://localhost:6443/api/health' "$(id -un)" >/dev/null 2>&1; then
  fail 'endpoint accepted a group/other-readable TLS private key'
fi
chmod 600 "$tmp_dir/tls.key"
grep -F -q 'getent ahosts "$host"' deploy/packaging/serverdesk-endpoint.sh ||
  fail 'Linux health URL host is not DNS-resolved for local-address validation'
grep -F -q 'ip -o addr show' deploy/packaging/serverdesk-endpoint.sh ||
  fail 'Linux health URL validation does not compare against local interface addresses'
grep -F -q -- "--connect-timeout 3 --max-time 10" deploy/packaging/install-linux.sh ||
  fail 'installer health check must have connection and total timeouts'
if grep -n 'HEALTH_URL=.*127\.0\.0\.1:6005' deploy/packaging/install-linux.sh >/dev/null 2>&1; then
  fail 'Linux installer must derive health URL from config instead of fixed HTTP/6005'
fi

# Exercise the Linux collector trust boundary without requiring root. The sudo
# shim preserves real mode/link-count/read checks while presenting fixture and
# ancestor ownership as root (the production installer uses real sudo/stat).
collector_root="$tmp_dir/collector-fixtures"
collector_bin="$tmp_dir/collector-test-bin"
mkdir -p "$collector_root/safe" "$collector_root/writable-parent" "$collector_root/target-dir" \
  "$collector_bin"
chmod 755 "$collector_root" "$collector_root/safe" "$collector_root/target-dir"
chmod 777 "$collector_root/writable-parent"
printf '%s\n' '#!/bin/sh' 'exit 0' > "$collector_root/safe/avcli"
chmod 755 "$collector_root/safe/avcli"
cp "$collector_root/safe/avcli" "$collector_root/target-dir/java"
cp "$collector_root/safe/avcli" "$collector_root/writable-parent/avcli"
cp "$collector_root/safe/avcli" "$collector_root/hardlink-one"
ln "$collector_root/hardlink-one" "$collector_root/hardlink-two"
ln -s safe/avcli "$collector_root/trusted-symlink"
ln -s safe/avcli "$collector_root/untrusted-symlink"
ln -s target-dir "$collector_root/trusted-parent-symlink"
printf '%s\n' '#!/bin/sh' \
  'if [ "${1:-}" = -u ]; then shift 2; exec "$@"; fi' \
  'if [ "${1:-}" = stat ] && [ "${2:-}" = -c ]; then' \
  '  format=$3; path=$4' \
  '  case "$format" in' \
  '    %U) case "$path" in *untrusted-symlink*|*nonroot-owner*) printf "%s\n" runner ;; *) printf "%s\n" root ;; esac ;;' \
  '    %a) case "$path" in "$SERVERDESK_TEST_FIXTURE_ROOT"|"$SERVERDESK_TEST_FIXTURE_ROOT"/*) exec /usr/bin/stat -c %a "$path" ;; *) printf "%s\n" 755 ;; esac ;;' \
  '    %h) exec /usr/bin/stat -c %h "$path" ;;' \
  '    *) exec /usr/bin/stat -c "$format" "$path" ;;' \
  '  esac' \
  '  exit 0' \
  'fi' \
  'exec "$@"' > "$collector_bin/sudo"
chmod 755 "$collector_bin/sudo"
collector_preflight() {
  PATH="$collector_bin:$PATH" SERVERDESK_TEST_FIXTURE_ROOT="$collector_root" \
    sh deploy/packaging/serverdesk-collector-preflight.sh "$1" 'test collector asset' 1 "$(id -un)"
}
[ "$(collector_preflight "$collector_root/safe/avcli")" = \
  "$(realpath "$collector_root/safe/avcli")" ] || fail 'trusted collector asset was rejected'
[ "$(collector_preflight "$collector_root/trusted-symlink")" = \
  "$(realpath "$collector_root/safe/avcli")" ] || fail 'root-owned safe collector symlink was rejected'
[ "$(collector_preflight "$collector_root/trusted-parent-symlink/java")" = \
  "$(realpath "$collector_root/target-dir/java")" ] || fail 'root-owned safe ancestor symlink was rejected'
if collector_preflight "$collector_root/untrusted-symlink" >/dev/null 2>&1; then
  fail 'collector preflight accepted a non-root-owned symlink'
fi
if collector_preflight "$collector_root/hardlink-one" >/dev/null 2>&1; then
  fail 'collector preflight accepted a hard-linked executable'
fi
if collector_preflight "$collector_root/writable-parent/avcli" >/dev/null 2>&1; then
  fail 'collector preflight accepted a group/world-writable ancestor directory'
fi
cp "$collector_root/safe/avcli" "$collector_root/nonroot-owner-avcli"
if collector_preflight "$collector_root/nonroot-owner-avcli" >/dev/null 2>&1; then
  fail 'collector preflight accepted a non-root-owned executable'
fi

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
if grep -E -q '^(ProtectProc|ProcSubset)=' deploy/serverdesk.service; then
  fail 'service-private proc must not mask the host argv-exposure attestation'
fi
grep -F -q 'Environment=SERVERDESK_CREDENTIALS_STORE=/var/lib/serverdesk/credentials' \
  deploy/serverdesk.service || fail 'service must configure its writable managed credential store'
grep -F -q 'CapabilityBoundingSet=CAP_NET_ADMIN' deploy/serverdesk-net.service ||
  fail 'serverdesk-net.service must have only its documented network capability'
grep -F -q 'ExecStart=/opt/serverdesk/serverdesk-network --config /etc/serverdesk/network.env --state /run/serverdesk-net/applied.env apply' \
  deploy/serverdesk-net.service || fail 'network unit does not use the validated helper'
grep -q '"secret_policy"[[:space:]]*:[[:space:]]*"require-references"' config.example.json ||
  fail 'installed config template must require secret references'
grep -q '"mib_dir"[[:space:]]*:[[:space:]]*"mibs"' config.example.json ||
  fail 'installed config must use the separately provisioned MIB directory'
grep -F -q 'MibDir: "mibs"' internal/config/config.go ||
  fail 'config default must resolve the separately provisioned MIB directory beside the executable'
if grep -R -n -E 'testMIBDir[[:space:]]*=[[:space:]]*"(\.\./)*docs/mibs"' internal >/dev/null 2>&1; then
  fail 'MIB tests must use synthetic testdata instead of public/vendor paths'
fi
if grep -F -q 'SERVERDESK_ALLOW_PROCESS_ARGV_EXPOSURE' deploy/packaging/install-linux.sh; then
  fail 'packaged Linux install must not advertise an argv bypass it cannot pass to runtime'
fi
grep -F -q '*,hidepid=1,*|*,hidepid=2,*|*,hidepid=4,*' deploy/packaging/install-linux.sh ||
  fail 'Linux installer does not attest host-wide proc hidepid before Stratus collection'
grep -F -q 'packaged installation has no argv-exposure bypass' deploy/packaging/install-linux.sh ||
  fail 'Linux installer does not fail closed when host argv protection is absent'
grep -F -q 'sudo -u "$SERVICE_USER" test -r "$readable_tls_key"' deploy/packaging/install-linux.sh ||
  fail 'Linux installer does not verify TLS key readability as the service identity'
grep -F -q 'TLS private key must not be stored in the service-writable state tree' \
  deploy/packaging/install-linux.sh ||
  fail 'Linux installer permits a daemon-writable TLS private key'
grep -F -q 'select($jar_index != null' deploy/packaging/install-linux.sh ||
  fail 'Linux AVCLI preflight does not explicitly reject a missing -jar argument'
grep -F -q 'serverdesk-collector-preflight.sh' deploy/packaging/install-linux.sh ||
  fail 'Linux installer does not invoke the collector executable/JAR trust boundary'
grep -F -q 'single-link, and not special/group/world-writable' \
  deploy/packaging/serverdesk-collector-preflight.sh ||
  fail 'Linux AVCLI preflight permits a writable or hard-linked collector asset'
grep -F -q 'program directory contains a symlink' deploy/packaging/install-linux.sh ||
  fail 'Linux installer does not reject child symlinks under the root-written program directory'
grep -F -q 'stale deployment transaction path requires operator inspection' \
  deploy/packaging/install-linux.sh ||
  fail 'Linux installer does not fail closed on unsafe temp/backup transaction paths'
grep -F -q 'sudo cp -a "$DST/serverdesk" "$DST/serverdesk.install-backup"' \
  deploy/packaging/install-linux.sh ||
  fail 'Linux installer backup does not preserve original file metadata'
grep -F -q 'transaction backups were preserved for operator recovery' \
  deploy/packaging/install-linux.sh ||
  fail 'Linux rollback can discard recovery backups after an incomplete restore'

# Windows deployment contracts are statically enforced even on Linux CI runners.
for script in install-windows.ps1 update.ps1; do
  grep -F -q 'windows-deployment-common.ps1' "deploy/packaging/$script" ||
    fail "$script must use the shared endpoint/firewall contract"
done
if grep -n 'Get-ScheduledTaskInfo.*\.State' deploy/packaging/*.ps1 >/dev/null 2>&1; then
  fail 'task running state must come from Get-ScheduledTask, not Get-ScheduledTaskInfo'
fi
grep -F -q 'function Assert-ServerdeskManagedTask' deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows deployment does not protect against an unrelated scheduled-task name collision'
for script in install-windows.ps1 update.ps1 uninstall.ps1; do
  managed_task_line=$(grep -n -F 'Assert-ServerdeskManagedTask' "deploy/packaging/$script" | head -1 | cut -d: -f1)
  task_stop_line=$(grep -n -F 'Stop-ScheduledTask -TaskName serverdesk' "deploy/packaging/$script" | head -1 | cut -d: -f1)
  if [ -z "$managed_task_line" ] || [ -z "$task_stop_line" ] ||
    [ "$managed_task_line" -ge "$task_stop_line" ]; then
    fail "$script must validate task ownership/action before its first task mutation"
  fi
done
grep -F -q '$priorTask.State -eq '\''Running'\''' deploy/packaging/install-windows.ps1 ||
  fail 'Windows installer does not preserve prior running state'
grep -F -q '$priorTask.State -eq '\''Running'\''' deploy/packaging/update.ps1 ||
  fail 'Windows updater does not preserve prior running state'
grep -F -q 'TLS with a wildcard listener requires -HealthUrl' \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'wildcard TLS must require a certificate-valid explicit health URL'
grep -F -q '[Net.Dns]::GetHostAddresses($HostName)' \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows health hostnames are not resolved for all-local-address validation'
grep -F -q 'SERVERDESK_CREDENTIALS_STORE' deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows runner does not configure the managed credential store'
grep -F -q 'ACL may grant access only to SYSTEM and Administrators' \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows endpoint preflight does not enforce a private TLS key ACL'
grep -F -q 'runtime_dir must be a child of the protected installation directory' \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows endpoint preflight permits unmanaged external runtime data'
grep -F -q 'Assert-ServerdeskPathComponents -Path $runtimePath -AllowMissing' \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows endpoint preflight does not reject reparse components in managed paths'
grep -F -q "GetEnvironmentVariable('Path', 'Machine')" \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows AVCLI preflight does not resolve bare commands as the SYSTEM task would'
grep -F -q 'Assert-ServerdeskTrustedReadOnlyPath' deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows AVCLI preflight permits non-administrator-writable executable/JAR paths'
grep -F -q "Assert-ServerdeskTrustedReadOnlyPath \$resolved 'AVCLI executable'" \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows AVCLI executable is not passed through trusted ACL/owner validation'
grep -F -q "Assert-ServerdeskTrustedReadOnlyPath \$jar 'AVCLI JAR'" \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows AVCLI JAR is not passed through trusted ACL/owner validation'
grep -F -q 'grants write/modify rights to a non-administrative principal' \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows collector trust check does not reject non-administrator writers'
grep -F -q 'must grant SYSTEM or Administrators read/execute access' \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows collector trust check does not verify SYSTEM task readability/executability'
if grep -F -q '[bool](Get-ServerdeskProperty' deploy/packaging/windows-deployment-common.ps1; then
  fail 'Windows endpoint must not coerce non-Boolean allow_insecure_http values'
fi
grep -F -q 'allow_insecure_http must be a JSON Boolean' \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows endpoint does not require a JSON Boolean for allow_insecure_http'
grep -F -q 'Assert-ServerdeskRegularFile $jar '\''AVCLI JAR'\''' \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows AVCLI preflight does not reject a JAR reparse point'
grep -F -q 'Set-ServerdeskFirewall -Endpoint $endpoint' deploy/packaging/install-windows.ps1 ||
  fail 'Windows installer firewall does not follow the parsed endpoint'
grep -F -q 'Copy-Verified $new "$dst\.serverdesk.update-new"' deploy/packaging/update.ps1 ||
  fail 'Windows updater does not verify a staged binary before downtime'
touch_line=$(grep -n -F '$serviceTouched = $true' deploy/packaging/update.ps1 | head -1 | cut -d: -f1)
stop_line=$(grep -n -F 'Stop-ScheduledTask -TaskName serverdesk' deploy/packaging/update.ps1 | head -1 | cut -d: -f1)
if [ -z "$touch_line" ] || [ -z "$stop_line" ] || [ "$touch_line" -ge "$stop_line" ]; then
  fail 'Windows updater must enter rollback mode before the first scheduled-task mutation'
fi
grep -F -q 'Installation directory contains a reparse point' deploy/packaging/update.ps1 ||
  fail 'Windows updater does not reject child reparse points before mutation'
grep -F -q "Assert-ServerdeskTrustedReadOnlyPath \$dst 'Installation directory'" \
  deploy/packaging/update.ps1 ||
  fail 'Windows updater can install SYSTEM task code under a non-administrator-writable root'
grep -F -q "Assert-ServerdeskTrustedReadOnlyPath \$trustedInstalledPath 'Installed package/control file'" \
  deploy/packaging/update.ps1 ||
  fail 'Windows updater does not reject writable installed control files'
grep -F -q 'Managed runtime directory is missing; run the full installer before updating' \
  deploy/packaging/update.ps1 ||
  fail 'Windows updater can create an untracked runtime directory outside rollback state'
grep -F -q 'Resolve-ServerdeskConfigPath $preflightAvcliBin $configPath' deploy/packaging/update.ps1 ||
  fail 'Windows legacy AVCLI migration does not resolve relative paths against the installed config'
for script in install-windows.ps1 update.ps1; do
  grep -F -q 'package path must not traverse a reparse point' "deploy/packaging/$script" ||
    fail "$script dot-sources elevated deployment code before checking package path components"
done
for script in install-windows.ps1 update.ps1 uninstall.ps1; do
  grep -F -q 'Stop-ServerdeskInstalledProcess' "deploy/packaging/$script" ||
    fail "$script must stop only the exact installed executable path"
done
if grep -n -E 'Get-Process[[:space:]]+serverdesk.*Stop-Process' deploy/packaging/*.ps1 >/dev/null 2>&1; then
  fail 'Windows deployment must not terminate unrelated same-name processes'
fi
grep -F -q 'Installed Serverdesk process did not stop within 10 seconds' \
  deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows process stop helper does not verify exact target termination'
grep -F -q "'run-serverdesk.ps1', 'run-serverdesk.cmd', 'auth.json', 'initial-login.txt'" \
  deploy/packaging/install-windows.ps1 ||
  fail 'Windows reinstall does not snapshot maintenance/auth files'
grep -F -q 'Set-Acl -LiteralPath $entry.Path -AclObject $entry.Acl' \
  deploy/packaging/install-windows.ps1 || fail 'Windows reinstall does not restore prior ACLs'
grep -F -q 'New-ServerdeskTrackedDirectory' deploy/packaging/install-windows.ps1 ||
  fail 'Windows reinstall does not track newly created managed directories'
grep -F -q 'Join-Path $dst $runtimeValue' deploy/packaging/install-windows.ps1 ||
  fail 'Windows installer discards a custom relative runtime_dir instead of managing it below the install root'
grep -F -q 'Failed to set trusted owner on the runtime runner' deploy/packaging/install-windows.ps1 ||
  fail 'Windows installer leaves SYSTEM task code owned by the invoking account'
grep -F -q 'new transaction directory is not empty; preserving it for inspection' \
  deploy/packaging/install-windows.ps1 ||
  fail 'Windows reinstall does not report a non-exact directory rollback'
grep -F -q 'Full uninstall refuses to traverse customer/reparse path' deploy/packaging/uninstall.ps1 ||
  fail 'Windows full uninstall does not fail closed before traversing a junction'
grep -F -q 'Refusing to load deployment code through a reparse point' deploy/packaging/uninstall.ps1 ||
  fail 'Windows uninstall dot-sources maintenance code before validating path components'
uninstall_guard_line=$(grep -n -F 'Refusing to load deployment code through a reparse point' \
  deploy/packaging/uninstall.ps1 | head -1 | cut -d: -f1)
uninstall_dot_line=$(grep -n -F '. $commonPath' deploy/packaging/uninstall.ps1 | head -1 | cut -d: -f1)
if [ -z "$uninstall_guard_line" ] || [ -z "$uninstall_dot_line" ] ||
  [ "$uninstall_guard_line" -ge "$uninstall_dot_line" ]; then
  fail 'Windows uninstall must validate common-helper path components before dot-sourcing it'
fi
grep -F -q 'Required package asset is missing' deploy/packaging/install-windows.ps1 ||
  fail 'Windows direct installer does not fail closed on missing maintenance assets'
grep -F -q 'Stale installation backup requires operator inspection' \
  deploy/packaging/install-windows.ps1 ||
  fail 'Windows reinstall can overwrite a stale recovery backup'
grep -F -q 'Rollback binary verification failed' deploy/packaging/install-windows.ps1 ||
  fail 'Windows reinstall does not hash-verify the restored binary'
grep -F -q 'Set-Acl -LiteralPath $entry.Path -AclObject $entry.Acl' \
  deploy/packaging/update.ps1 || fail 'Windows updater does not restore prior ACLs on rollback'
grep -F -q 'Refusing stale update transaction path' deploy/packaging/update.ps1 ||
  fail 'Windows updater can overwrite stale transaction artifacts'
grep -F -q 'credentials, data, logs, TLS, MIB, AVCLI/JRE' deploy/packaging/uninstall.ps1 ||
  fail 'Windows default uninstall does not state preservation of customer-provisioned assets'
grep -F -q "Delete only known package-owned runtime files" deploy/packaging/uninstall.ps1 ||
  fail 'Windows default uninstall must use an allowlist of package-owned files'
grep -F -q 'Delete only package-owned executables' deploy/packaging/uninstall-linux.sh ||
  fail 'Linux default uninstall must preserve customer files under /opt/serverdesk'
grep -F -q 'deployment transaction/recovery path requires operator inspection before uninstall' \
  deploy/packaging/uninstall-linux.sh ||
  fail 'Linux uninstall can destroy a failed-install recovery backup'
grep -F -q 'A deployment transaction/recovery path is present' deploy/packaging/uninstall.ps1 ||
  fail 'Windows uninstall can destroy a failed deployment transaction or recovery backup'
grep -F -q 'ForEach-Object { Write-ServerdeskLog' deploy/packaging/windows-deployment-common.ps1 ||
  fail 'Windows runner must enforce log rotation while the process is running'
grep -F -q 'Windows packaged runtime — commercial NO-GO' docs/SECURITY.md ||
  fail 'Windows SYSTEM runtime must remain an explicit commercial production NO-GO'
if grep -n -E 'Invoke-WebRequest[[:space:]]+http://127\.0\.0\.1:6005|Expand-Archive.*(avcli|jre)' \
  deploy/packaging/*.ps1 >/dev/null 2>&1; then
  fail 'Windows deployment regressed to fixed health URL or bundled vendor extraction'
fi
grep -F -q 'docs/mibs must contain only a regular README.md; vendor MIBs are forbidden' \
  .github/workflows/release.yml ||
  fail 'release workflow must fail closed when public source contains vendor MIBs'
if grep -F -q 'find stage/docs/mibs -type f ! -name README.md -delete' .github/workflows/release.yml; then
  fail 'release workflow must not silently delete vendor material after copying it from source'
fi

sh deploy/packaging/serverdesk-network.sh \
  --config deploy/packaging/serverdesk-network.env.example validate >/dev/null

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
  cp config.example.json README.md NOTICE SECURITY.md THIRD_PARTY_NOTICES.md "$stage/"
  cp -R deploy docs "$stage/"
  rm -rf "$stage/docs/mibs"
  cp web/fonts/LICENSE-*.txt web/LICENSE-Reicon.txt "$stage/licenses/"
  if [ "$platform" = linux ]; then
    : > "$stage/serverdesk-linux-amd64"
    chmod 755 "$stage/serverdesk-linux-amd64"
  else
    : > "$stage/serverdesk-windows-amd64.exe"
    cp deploy/packaging/install-windows.ps1 deploy/packaging/update.ps1 \
      deploy/packaging/uninstall.ps1 deploy/packaging/windows-deployment-common.ps1 \
      deploy/packaging/setup.bat "$stage/"
  fi
  sh deploy/packaging/validate-release-payload.sh "$platform" "$stage" >/dev/null
  mkdir -p "$stage/docs/mibs"
  : > "$stage/docs/mibs/vendor.mib"
  if sh deploy/packaging/validate-release-payload.sh "$platform" "$stage" >/dev/null 2>&1; then
    fail "$platform payload validator accepted vendor MIBs"
  fi
  rm "$stage/docs/mibs/vendor.mib"
  : > "$stage/STRATUS-TEST-MIB.txt"
  if sh deploy/packaging/validate-release-payload.sh "$platform" "$stage" >/dev/null 2>&1; then
    fail "$platform payload validator accepted a vendor MIB artifact outside docs/mibs"
  fi
  rm "$stage/STRATUS-TEST-MIB.txt"
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

if command -v pwsh >/dev/null 2>&1; then
  for script in deploy/packaging/*.ps1; do
    pwsh -NoProfile -NonInteractive -Command \
      '$errors=$null; [void][Management.Automation.Language.Parser]::ParseFile($args[0],[ref]$null,[ref]$errors); if($errors.Count){$errors | ForEach-Object {Write-Error $_}; exit 1}' \
      "$script" || fail "PowerShell parser: $script"
  done
fi

echo '[OK] deployment assets passed rootless validation'
