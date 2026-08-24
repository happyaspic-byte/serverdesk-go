#!/bin/sh
# Validate an executable/JAR that will receive or process Stratus credentials.
# Prints the canonical path on success. The packaged installer always requires
# root ownership; symlinks are allowed only when both the link chain and target
# chain remain root-owned and non-writable by group/other.
set -eu

[ "$#" -eq 4 ] || {
  echo "usage: $0 PATH DESCRIPTION REQUIRE_EXECUTABLE SERVICE_USER" >&2
  exit 2
}
configured_path=$1
asset_description=$2
require_executable=$3
service_user=$4

case "$require_executable" in 0|1) ;; *) echo '[FAIL] REQUIRE_EXECUTABLE must be 0 or 1' >&2; exit 2;; esac
case "$service_user" in ''|*[!A-Za-z0-9_.-]*) echo '[FAIL] invalid service user' >&2; exit 2;; esac
for command in sudo stat realpath dirname; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "[FAIL] collector preflight requires $command" >&2
    exit 1
  }
done

mode_is_group_or_world_writable() {
  case "$1" in *[2367]?|*[2367]) return 0 ;; *) return 1 ;; esac
}

mode_has_special_bits() {
  case "$1" in ???) return 1 ;; *) return 0 ;; esac
}

validate_root_owned_directory_chain() {
  asset_path=$1
  chain_cursor=$(dirname "$asset_path")
  while :; do
    if [ -L "$chain_cursor" ]; then
      if [ "$(sudo stat -c '%U' "$chain_cursor")" != root ]; then
        echo "[FAIL] collector path uses a non-root-owned symlink directory: $chain_cursor" >&2
        exit 1
      fi
    elif [ ! -d "$chain_cursor" ]; then
      echo "[FAIL] collector path parent is not a directory: $chain_cursor" >&2
      exit 1
    else
      chain_owner=$(sudo stat -c '%U' "$chain_cursor")
      chain_mode=$(sudo stat -c '%a' "$chain_cursor")
      if [ "$chain_owner" != root ] ||
        mode_is_group_or_world_writable "$chain_mode" ||
        mode_has_special_bits "$chain_mode"; then
        echo "[FAIL] collector path parent must be root-owned without special/group/world-write bits: $chain_cursor" >&2
        exit 1
      fi
    fi
    [ "$chain_cursor" = / ] && break
    chain_cursor=$(dirname "$chain_cursor")
  done
}

lexical_path=$(realpath -s "$configured_path")
if [ -L "$lexical_path" ] && [ "$(sudo stat -c '%U' "$lexical_path")" != root ]; then
  echo "[FAIL] $asset_description symlink must be root-owned: $lexical_path" >&2
  exit 1
fi
validate_root_owned_directory_chain "$lexical_path"
canonical_path=$(realpath "$configured_path" 2>/dev/null) || {
  echo "[FAIL] $asset_description cannot be resolved: $configured_path" >&2
  exit 1
}
validate_root_owned_directory_chain "$canonical_path"
if [ ! -f "$canonical_path" ]; then
  echo "[FAIL] $asset_description must resolve to a regular file: $canonical_path" >&2
  exit 1
fi
asset_owner=$(sudo stat -c '%U' "$canonical_path")
asset_mode=$(sudo stat -c '%a' "$canonical_path")
asset_links=$(sudo stat -c '%h' "$canonical_path")
if [ "$asset_owner" != root ] || [ "$asset_links" -ne 1 ] ||
  mode_is_group_or_world_writable "$asset_mode" || mode_has_special_bits "$asset_mode"; then
  echo "[FAIL] $asset_description must be root-owned, single-link, and not special/group/world-writable: $canonical_path" >&2
  exit 1
fi
if ! sudo -u "$service_user" test -r "$canonical_path"; then
  echo "[FAIL] $asset_description is not readable by $service_user: $canonical_path" >&2
  exit 1
fi
if [ "$require_executable" -eq 1 ] && ! sudo -u "$service_user" test -x "$canonical_path"; then
  echo "[FAIL] $asset_description is not executable by $service_user: $canonical_path" >&2
  exit 1
fi
printf '%s\n' "$canonical_path"
