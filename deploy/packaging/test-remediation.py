#!/usr/bin/env python3
"""Rootless transaction/network/release regressions; privileged commands are shims."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
PACK = ROOT / 'deploy/packaging'


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.env = dict(os.environ, PATH=f'{self.bin}:{os.environ["PATH"]}', TEST_ROOT=str(self.root))
        self.shim('sudo', 'exec "$@"')
        self.shim('id', 'printf "0\\n"')
        self.shim('stat', '''case "$2" in
%U:%G) printf 'root:root\\n';;
%u:%g) printf '0:0\\n';;
%u:%g:%h) printf '0:0:%s\\n' "$(/usr/bin/stat -c %h "$3")";;
%u:%g:%a) printf '0:0:%s\\n' "$(/usr/bin/stat -c %a "$3")";;
%u:%g:%a:%h) printf '0:0:%s:%s\\n' "$(/usr/bin/stat -c %a "$3")" "$(/usr/bin/stat -c %h "$3")";;
%a) case "$3" in "$TEST_ROOT"|"$TEST_ROOT"/*|deploy/*) exec /usr/bin/stat "$@";; *) printf '755\\n';; esac;;
*) exec /usr/bin/stat "$@";;
esac''')
        for name in ('systemctl', 'ip', 'iptables', 'useradd', 'usermod', 'userdel'):
            self.shim(name, 'printf "%s %s\\n" "$(basename "$0")" "$*" >> "$TEST_ROOT/mutations"; exit 99')
        self.paths = {
            '/opt/serverdesk': str(self.root / 'opt'),
            '/var/lib/serverdesk-net': str(self.root / 'net-state'),
            '/var/lib/serverdesk': str(self.root / 'state'),
            '/etc/serverdesk': str(self.root / 'etc'),
            '/etc/systemd/system': str(self.root / 'units'),
            '/run/lock/serverdesk-deploy': str(self.root / 'lock'),
            '/run/serverdesk-net': str(self.root / 'legacy-net'),
        }
        self.package = self.root / 'package'
        (self.package / 'deploy/packaging').mkdir(parents=True)
        for path in PACK.glob('*.sh'):
            text = path.read_text()
            for old, new in self.paths.items():
                text = text.replace(old, new)
            dest = self.package / 'deploy/packaging' / path.name
            dest.write_text(text)
            dest.chmod(0o755)

    def shim(self, name, body):
        path = self.bin / name
        path.write_text('#!/bin/sh\nset -eu\n' + body + '\n')
        path.chmod(0o755)

    def run_script(self, name, *args, **env):
        return subprocess.run(['sh', str(self.package / 'deploy/packaging' / name), *args],
                              cwd=self.package, env=dict(self.env, **env), text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=8)

    def lock(self):
        directory = self.root / 'lock'
        directory.mkdir(mode=0o700, exist_ok=True)
        path = directory / 'transaction.lock'
        path.touch(mode=0o600)
        proc = subprocess.Popen(['flock', '-x', str(path), 'sh', '-c', 'printf ready; read line'],
                                stdin=subprocess.PIPE, stdout=subprocess.PIPE)
        self.assertEqual(proc.stdout.read(5), b'ready')
        self.addCleanup(proc.stdout.close)
        self.addCleanup(proc.stdin.close)
        self.addCleanup(proc.wait)
        self.addCleanup(proc.terminate)
        return path

    def test_install_update_uninstall_contend_before_validation_or_mutation(self):
        self.lock()
        for name in ('install-linux.sh', 'update-linux.sh', 'uninstall-linux.sh'):
            with self.subTest(name=name):
                result = self.run_script(name, SERVERDESK_DEPLOY_LOCK_HELD='1')
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('deployment transaction is already running', result.stdout)
                self.assertFalse((self.root / 'mutations').exists())

    def test_lock_inode_persists_after_failed_validation(self):
        result = self.run_script('install-linux.sh')
        self.assertNotEqual(result.returncode, 0)
        lock = self.root / 'lock/transaction.lock'
        self.assertTrue(lock.is_file())
        inode = lock.stat().st_ino
        result = self.run_script('install-linux.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(lock.stat().st_ino, inode)

    def test_nonroot_rejected_before_mutation(self):
        self.shim('id', 'printf "1000\\n"')
        for name in ('install-linux.sh', 'update-linux.sh', 'uninstall-linux.sh'):
            result = self.run_script(name)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('run with sudo', result.stdout)
        self.assertFalse((self.root / 'mutations').exists())
        self.assertFalse((self.root / 'lock').exists())

    def test_unsafe_lock_and_helper_rejected(self):
        lockdir = self.root / 'lock'
        lockdir.mkdir(mode=0o700)
        victim = self.root / 'victim'
        victim.write_text('unchanged')
        (lockdir / 'transaction.lock').symlink_to(victim)
        result = self.run_script('uninstall-linux.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(victim.read_text(), 'unchanged')
        (lockdir / 'transaction.lock').unlink()
        helper = self.package / 'deploy/packaging/serverdesk-deployment-lock.sh'
        if helper.exists():
            helper.chmod(0o777)
        result = self.run_script('install-linux.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('unsafe deployment lock helper', result.stdout)
        self.assertFalse((self.root / 'mutations').exists())
        helper.chmod(0o755)
        parent = helper.parent
        parent.chmod(0o777)
        result = self.run_script('install-linux.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('unsafe deployment lock helper parent', result.stdout)
        parent.chmod(0o755)
        self.shim('flock', 'exit 2')
        result = self.run_script('uninstall-linux.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'mutations').exists())

    def installed_fixture(self):
        for name, mode in [('opt', 0o755), ('state', 0o700), ('etc', 0o700), ('units', 0o755)]:
            (self.root / name).mkdir(mode=mode, exist_ok=True)
        (self.root / 'state/credentials').mkdir(mode=0o700)
        (self.root / 'opt/mibs').mkdir(mode=0o755)
        for name in ('config.local.json', 'auth.json'):
            path = self.root / 'state' / name
            path.write_text('{"listen":"127.0.0.1:6005","clusters":[]}')
            path.chmod(0o600)
        (self.root / 'opt/serverdesk').write_text('old binary')
        (self.root / 'opt/customer-file').write_text('preserve')
        (self.root / 'units/serverdesk.service').write_text('old unit')
        (self.root / 'units/serverdesk-net.service').write_text('old net unit')
        binary = self.package / 'serverdesk-linux-amd64'
        binary.write_text('#!/bin/sh\nexit 0\n')
        binary.chmod(0o755)
        shutil.copyfile(ROOT / 'config.example.json', self.package / 'config.example.json')
        shutil.copyfile(PACK / 'serverdesk-network.env.example', self.package / 'deploy/packaging/serverdesk-network.env.example')
        for unit in ('serverdesk.service', 'serverdesk-net.service'):
            shutil.copyfile(ROOT / 'deploy' / unit, self.package / 'deploy' / unit)
        self.shim('stat', '''case "$2" in
%U:%G) case "$3" in "$TEST_ROOT/state"|"$TEST_ROOT/state"/*) printf 'serverdesk:serverdesk\\n';; *) printf 'root:root\\n';; esac;;
%u:%g) printf '0:0\\n';;
%u:%g:%h) printf '0:0:%s\\n' "$(/usr/bin/stat -c %h "$3")";;
%u:%g:%a) printf '0:0:%s\\n' "$(/usr/bin/stat -c %a "$3")";;
%u:%g:%a:%h) printf '0:0:%s:%s\\n' "$(/usr/bin/stat -c %a "$3")" "$(/usr/bin/stat -c %h "$3")";;
%a) case "$3" in "$TEST_ROOT"|"$TEST_ROOT"/*|deploy/*) exec /usr/bin/stat "$@";; *) printf '755\\n';; esac;;
*) exec /usr/bin/stat "$@";; esac''')
        self.shim('getent', 'case "$1" in ahosts) printf "127.0.0.1 STREAM localhost\\n";; esac; exit 0')
        self.shim('usermod', 'exit 0')
        self.shim('install', '''if [ "${1:-}" = -d ]; then shift; fi
while [ "$#" -gt 0 ]; do
case "$1" in -o|-g) shift 2;; -m) mode=$2; shift 2;; *) break;; esac
done
if [ "$#" -eq 1 ]; then mkdir -p "$1"; chmod "$mode" "$1"; else cp "$1" "$2"; chmod "$mode" "$2"; fi''')
        self.shim('curl', 'exit 0')
        self.shim('readlink', 'printf "%s/opt/serverdesk\\n" "$TEST_ROOT"')
        self.shim('sleep', 'exit 0')
        self.shim('systemd-analyze', 'exit 0')
        self.shim('systemctl', '''printf '%s\\n' "$*" >> "$TEST_ROOT/services"
case "$1" in
is-active|is-enabled) exit 0;;
show) printf '123\\n';;
cat) [ -e "$TEST_ROOT/units/$2.service" ];;
stop) [ "${FAIL_STOP:-0}" = 0 ] || exit 2;;
*) exit 0;; esac''')

    def test_update_delegates_one_lock_installs_helper_uninstall_preserves_customer(self):
        self.installed_fixture()
        result = self.run_script('update-linux.sh')
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertTrue((self.root / 'opt/serverdesk-deployment-lock').is_file())
        self.assertEqual((self.root / 'opt/serverdesk').read_text(), '#!/bin/sh\nexit 0\n')
        result = self.run_script('uninstall-linux.sh')
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse((self.root / 'opt/serverdesk-deployment-lock').exists())
        self.assertFalse((self.root / 'opt/serverdesk-network').exists())
        self.assertEqual((self.root / 'opt/customer-file').read_text(), 'preserve')
        self.assertTrue((self.root / 'state/auth.json').exists())

    def test_failed_old_unit_stop_preserves_migrated_authority_and_original_binary(self):
        self.installed_fixture()
        config, applied = self.network(state=False)
        shutil.copyfile(config, self.root / 'etc/network.env')
        (self.root / 'etc/network.env').chmod(0o600)
        legacy = self.root / 'legacy-net'
        legacy.mkdir(mode=0o700)
        shutil.copyfile(config, legacy / 'applied.env')
        (legacy / 'applied.env').chmod(0o600)
        result = self.run_script('update-linux.sh', FAIL_STOP='1')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertTrue(applied.exists())
        self.assertTrue((legacy / 'applied.env').exists())
        self.assertEqual((self.root / 'opt/serverdesk').read_text(), 'old binary')

    def test_uninstall_partial_delete_keeps_helper_and_authority_for_retry(self):
        self.installed_fixture()
        _, applied = self.network()
        (self.root / 'address').touch()
        (self.root / 'redirect').touch()
        installed = self.root / 'opt/serverdesk-network'
        installed.write_text('cleanup executable')
        result = self.run_script('uninstall-linux.sh', FAIL_RULE_DELETE='1')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertFalse((self.root / 'address').exists())
        self.assertTrue((self.root / 'redirect').exists())
        self.assertTrue(applied.exists())
        self.assertTrue(installed.exists())
        self.assertTrue((self.root / 'units/serverdesk-net.service').exists())
        result = self.run_script('uninstall-linux.sh')
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse(applied.exists())
        self.assertFalse(installed.exists())

    def test_rollback_legacy_restart_repersists_authority_before_next_failed_stop(self):
        self.installed_fixture()
        config, applied = self.network(state=False)
        shutil.copyfile(config, self.root / 'etc/network.env')
        (self.root / 'etc/network.env').chmod(0o600)
        legacy = self.root / 'legacy-net'
        legacy.mkdir(mode=0o700)
        shutil.copyfile(config, legacy / 'applied.env')
        (legacy / 'applied.env').chmod(0o600)
        (self.root / 'address').touch()
        (self.root / 'redirect').touch()
        self.shim('curl', 'exit 2')
        self.shim('systemctl', '''case "$1:${2:-}" in
is-active:*|is-enabled:*) exit 0;;
show:*) printf '123\\n';;
stop:serverdesk-net) printf 'old-stop %s\\n' "${FAIL_STOP:-0}" >> "$TEST_ROOT/old-stops"; rm -f "$TEST_ROOT/legacy-net/applied.env"; [ "${FAIL_STOP:-0}" = 0 ] || exit 2;;
restart:serverdesk-net) if grep -q 'old net unit' "$TEST_ROOT/units/serverdesk-net.service"; then
cp "$TEST_ROOT/etc/network.env" "$TEST_ROOT/legacy-net/applied.env"; chmod 600 "$TEST_ROOT/legacy-net/applied.env"
touch "$TEST_ROOT/address" "$TEST_ROOT/redirect"
fi;;
*) exit 0;; esac''')
        result = self.run_script('update-linux.sh')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertTrue((legacy / 'applied.env').exists(), result.stdout)
        self.assertTrue(applied.exists(), result.stdout)
        self.assertEqual((self.root / 'opt/serverdesk').read_text(), 'old binary')
        self.assertEqual((self.root / 'units/serverdesk.service').read_text(), 'old unit')
        self.assertEqual((self.root / 'units/serverdesk-net.service').read_text(), 'old net unit')
        # Recovery artifacts must block uninstall before any service operation.
        before_stops = (self.root / 'old-stops').read_text()
        result = self.run_script('uninstall-linux.sh', FAIL_STOP='1')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('transaction/recovery path', result.stdout)
        self.assertEqual((self.root / 'old-stops').read_text(), before_stops)
        # Simulate operator-verified backup retirement, exclusively in this temp fixture.
        backups = [self.root / 'opt/serverdesk.install-backup',
                   self.root / 'units/serverdesk.service.install-backup',
                   self.root / 'units/serverdesk-net.service.install-backup']
        for backup in backups:
            original = backup.with_name(backup.name.removesuffix('.install-backup'))
            self.assertEqual(backup.read_bytes(), original.read_bytes())
            backup.unlink()
        result = self.run_script('uninstall-linux.sh', FAIL_STOP='1')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual((self.root / 'old-stops').read_text(), before_stops + 'old-stop 1\n')
        self.assertFalse((legacy / 'applied.env').exists())
        self.assertTrue(applied.exists())
        self.assertEqual(applied.read_bytes(), config.read_bytes())
        self.assertTrue((self.root / 'units/serverdesk-net.service').exists())

    def test_missing_journal_residual_blocks_uninstall_without_adopting_external(self):
        self.installed_fixture()
        config, applied = self.network(state=False)
        shutil.copyfile(config, self.root / 'etc/network.env')
        (self.root / 'etc/network.env').chmod(0o600)
        (self.root / 'address').touch()
        (self.root / 'redirect').touch()
        (self.root / 'opt/serverdesk-network').write_text('cleanup executable')
        result = self.run_script('uninstall-linux.sh')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('ownership is unknown', result.stdout)
        self.assertFalse(applied.exists())
        self.assertTrue((self.root / 'address').exists())
        self.assertTrue((self.root / 'redirect').exists())
        self.assertTrue((self.root / 'opt/serverdesk-network').exists())
        self.assertTrue((self.root / 'units/serverdesk-net.service').exists())
        # Never enabled: disabled configuration, no journal, confirmed no resources.
        (self.root / 'address').unlink()
        (self.root / 'redirect').unlink()
        shutil.copyfile(PACK / 'serverdesk-network.env.example', self.root / 'etc/network.env')
        result = self.run_script('uninstall-linux.sh')
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse((self.root / 'opt/serverdesk-network').exists())

    def network(self, *, state=True):
        directory = self.root / 'net-state'
        directory.mkdir(mode=0o700, exist_ok=True)
        config = self.root / 'network.env'
        config.write_text('SERVERDESK_NET_INTERFACE=lo\nSERVERDESK_AUX_ADDRESS=192.0.2.10/24\n'
                          'SERVERDESK_ENABLE_TRAP_REDIRECT=true\nSERVERDESK_TRAP_SOURCE_PORT=162\n'
                          'SERVERDESK_TRAP_TARGET_PORT=10162\n')
        applied = directory / 'applied.env'
        if state:
            shutil.copyfile(config, applied)
            applied.chmod(0o600)
        self.shim('ip', '''case "$*" in
'-o address show dev lo') [ -e "$TEST_ROOT/address" ] && printf '1: lo inet 192.0.2.10/24 scope global lo\\n'; exit 0;;
'address delete 192.0.2.10/24 dev lo') [ "${FAIL_DELETE:-0}" = 0 ] || exit 2; rm -f "$TEST_ROOT/address";;
'address add 192.0.2.10/24 dev lo') touch "$TEST_ROOT/address";;
*) exit 99;; esac''')
        self.shim('iptables', '''case "$*" in
*' -S '*) [ "${FAIL_QUERY:-0}" = 0 ] || exit 4; if [ -e "$TEST_ROOT/redirect" ]; then printf '%s\\n' '-A PREROUTING -p udp -m udp --dport 162 -m comment --comment serverdesk-managed-trap-redirect -j REDIRECT --to-ports 10162'; fi;;
*' -D '*) [ "${FAIL_RULE_DELETE:-0}" = 0 ] || exit 2; rm -f "$TEST_ROOT/redirect";;
*' -A '*) touch "$TEST_ROOT/redirect";;
*) exit 99;; esac''')
        return config, applied

    def test_failed_address_deletion_retains_authority_then_retry_cleans(self):
        _, applied = self.network()
        (self.root / 'address').touch()
        (self.root / 'redirect').touch()
        result = self.run_script('serverdesk-network.sh', '--config', str(applied), 'remove', FAIL_DELETE='1')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertTrue(applied.exists())
        result = self.run_script('serverdesk-network.sh', '--config', str(applied), 'remove')
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse(applied.exists())
        self.assertFalse((self.root / 'address').exists())
        self.assertFalse((self.root / 'redirect').exists())

    def test_failed_rule_query_is_not_verified_absence(self):
        _, applied = self.network()
        result = self.run_script('serverdesk-network.sh', '--config', str(applied), 'remove', FAIL_QUERY='1')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertTrue(applied.exists())

    def test_preexisting_resources_are_never_adopted_or_deleted(self):
        config, applied = self.network(state=False)
        (self.root / 'address').touch()
        (self.root / 'redirect').touch()
        result = self.run_script('serverdesk-network.sh', '--config', str(config), '--state', str(applied), 'apply')
        self.assertEqual(result.returncode, 0, result.stdout)
        result = self.run_script('serverdesk-network.sh', '--config', str(applied), 'remove')
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertTrue((self.root / 'address').exists())
        self.assertTrue((self.root / 'redirect').exists())
        self.assertFalse(applied.exists())

    def test_legacy_ownership_migrates_before_reapply(self):
        config, applied = self.network(state=False)
        legacy = self.root / 'legacy-net'
        legacy.mkdir(mode=0o700)
        shutil.copyfile(config, legacy / 'applied.env')
        (legacy / 'applied.env').chmod(0o600)
        (self.root / 'address').touch()
        (self.root / 'redirect').touch()
        result = self.run_script('serverdesk-network.sh', '--config', str(config), '--state', str(applied), 'apply')
        self.assertEqual(result.returncode, 0, result.stdout)
        result = self.run_script('serverdesk-network.sh', '--config', str(applied), 'remove')
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse((self.root / 'address').exists())
        self.assertFalse((self.root / 'redirect').exists())


class ReleaseTests(unittest.TestCase):
    def test_signed_tag_classification_and_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gh = root / 'gh'
            gh.write_text('''#!/bin/sh
case "$2" in
*/git/ref/tags/*) printf '%s\\n' '{"object":{"type":"tag","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}';;
*/git/tags/*) printf '{"tag":"%s","object":{"type":"commit","sha":"%s"},"verification":{"verified":%s}}\\n' "$GITHUB_REF_NAME" "${TAG_COMMIT:-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}" "${VERIFIED:-true}";;
*) exit 99;; esac
''')
            gh.chmod(0o755)
            env = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}',
                       GITHUB_REPOSITORY='owner/repo', GITHUB_SHA='b' * 40)
            for tag, kind in [('v1.2.3', 'stable'), ('v1.2.3-rc.1', 'prerelease')]:
                result = subprocess.run(['sh', str(PACK / 'serverdesk-release.sh'), 'classify'],
                                        env=dict(env, GITHUB_REF_NAME=tag), capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f'kind={kind}', result.stdout)
            for changes in [dict(GITHUB_REF_NAME='v1.2.3-', VERIFIED='true'),
                            dict(GITHUB_REF_NAME='v1.2.3', VERIFIED='false'),
                            dict(GITHUB_REF_NAME='v1.2.3', TAG_COMMIT='c' * 40)]:
                result = subprocess.run(['sh', str(PACK / 'serverdesk-release.sh'), 'classify'],
                                        env=dict(env, **changes), capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)

    def test_seal_binds_exact_archives_and_rejects_extra_or_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GITHUB_REF_NAME='v1.2.3', GITHUB_REPOSITORY='owner/repo',
                       GITHUB_SHA='b' * 40, SERVERDESK_TAG_OBJECT='a' * 40,
                       GITHUB_RUN_ID='123', GITHUB_RUN_ATTEMPT='1', GITHUB_WORKFLOW_REF='owner/repo/.github/workflows/release.yml@refs/tags/v1.2.3')
            for name in ['serverdesk-v1.2.3-linux-amd64.tar.gz', 'serverdesk-v1.2.3-windows-amd64.zip',
                         'serverdesk-linux-amd64.cdx.json', 'serverdesk-windows-amd64.cdx.json']:
                (root / name).write_text(name)
            result = subprocess.run(['sh', str(PACK / 'serverdesk-release.sh'), 'seal', str(root)],
                                    env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            import json
            manifest = json.loads((root / 'candidate-manifest.json').read_text())
            self.assertEqual(manifest['commit'], 'b' * 40)
            self.assertEqual(manifest['tag_object'], 'a' * 40)
            self.assertEqual(manifest['run_id'], '123')
            import hashlib
            self.assertEqual(manifest['files']['serverdesk-v1.2.3-linux-amd64.tar.gz'],
                             hashlib.sha256(b'serverdesk-v1.2.3-linux-amd64.tar.gz').hexdigest())
            result = subprocess.run(['sha256sum', '-c', 'SHA256SUMS'], cwd=root, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout)
            (root / 'serverdesk-v1.2.3-linux-amd64.tar.gz').write_text('tampered')
            result = subprocess.run(['sha256sum', '-c', 'SHA256SUMS'], cwd=root, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            (root / 'extra.json').write_text('unapproved')
            result = subprocess.run(['sh', str(PACK / 'serverdesk-release.sh'), 'seal', str(root)],
                                    env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('exactly two archives', result.stderr)
            (root / 'extra.json').unlink()
            (root / 'candidate-manifest.json').unlink()
            (root / 'SHA256SUMS').unlink()
            target = root / 'serverdesk-v1.2.3-windows-amd64.zip'
            target.unlink()
            target.symlink_to(root / 'serverdesk-v1.2.3-linux-amd64.tar.gz')
            result = subprocess.run(['sh', str(PACK / 'serverdesk-release.sh'), 'seal', str(root)],
                                    env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('unsafe candidate file', result.stderr)


if __name__ == '__main__':
    unittest.main()
