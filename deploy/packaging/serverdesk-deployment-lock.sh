#!/bin/sh
# Sourced only after the entry script verifies this helper. Keep fd9 open until
# process exit, including rollback/cleanup. Never unlink the lock inode.
for command in id stat flock mkdir chmod; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "[FAIL] required deployment command is unavailable: $command" >&2
    exit 1
  }
done
[ "$(id -u)" = 0 ] || { echo '[FAIL] run with sudo' >&2; exit 1; }
lock_dir=/run/lock/serverdesk-deploy
if [ -L "$lock_dir" ] || { [ -e "$lock_dir" ] && [ ! -d "$lock_dir" ]; }; then
  echo '[FAIL] unsafe deployment lock directory' >&2
  exit 1
fi
if [ ! -e "$lock_dir" ]; then
  (umask 077; mkdir "$lock_dir") || {
    [ -d "$lock_dir" ] && [ ! -L "$lock_dir" ] || exit 1
  }
fi
[ "$(stat -c '%u:%g:%a' "$lock_dir")" = 0:0:700 ] || {
  echo '[FAIL] deployment lock directory must be root-owned mode 700' >&2
  exit 1
}
lock_file=$lock_dir/transaction.lock
if [ -L "$lock_file" ] || { [ -e "$lock_file" ] && [ ! -f "$lock_file" ]; }; then
  echo '[FAIL] unsafe deployment lock file' >&2
  exit 1
fi
if [ ! -e "$lock_file" ]; then
  (umask 077; set -C; : > "$lock_file") 2>/dev/null || :
fi
[ "$(stat -c '%u:%g:%a:%h' "$lock_file")" = 0:0:600:1 ] || {
  echo '[FAIL] deployment lock file must be root-owned mode 600, single-link' >&2
  exit 1
}
exec 9<> "$lock_file"
flock -n -x 9 || {
  echo '[FAIL] deployment transaction is already running' >&2
  exit 1
}
