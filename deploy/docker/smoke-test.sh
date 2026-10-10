#!/usr/bin/env bash
set -euo pipefail
umask 077

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

for tool in docker curl openssl sha256sum python3; do
  command -v "$tool" >/dev/null || { printf 'Required tool is missing: %s\n' "$tool" >&2; exit 1; }
done
docker info >/dev/null

port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
project="serverdesk-smoke-$$"
tmpdir=$(mktemp -d)
export SERVERDESK_TLS_CA_FILE=/var/lib/serverdesk/tls/server.crt

compose() {
  SERVERDESK_BIND=127.0.0.1 \
    SERVERDESK_PORT="$port" \
    SERVERDESK_TLS_HOST=localhost \
    SERVERDESK_TLS_CA_FILE="$SERVERDESK_TLS_CA_FILE" \
    docker compose --project-name "$project" "$@"
}

fail() {
  printf 'Docker smoke failed: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  result=$?
  trap - EXIT
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf -- "$tmpdir"
  [ "$result" -eq 0 ] || printf 'Removed smoke project resources: %s\n' "$project" >&2
  exit "$result"
}
trap cleanup EXIT

wait_healthy() {
  local cid state health
  for _ in $(seq 1 60); do
    cid=$(compose ps --all --quiet serverdesk 2>/dev/null | head -n 1 || true)
    if [ -n "$cid" ]; then
      state=$(docker inspect --format '{{.State.Status}}' "$cid" 2>/dev/null || true)
      [ "$state" != exited ] && [ "$state" != dead ] || fail 'container exited before becoming healthy'
      health=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$cid" 2>/dev/null || true)
      [ "$health" = healthy ] && return
    fi
    sleep 1
  done
  fail 'container did not become healthy'
}

assert_no_secret_logs() {
  compose logs --no-color serverdesk > "$tmpdir/container.log" 2>&1 || true
  if grep -Fq 'ADMIN_PASSWORD=' "$tmpdir/container.log" || grep -Fq -f "$tmpdir/password" "$tmpdir/container.log"; then
    fail 'credentials appeared in container logs'
  fi
}

https_status() {
  curl --noproxy '*' --cacert "$1" --resolve "localhost:$port:127.0.0.1" \
    --silent --show-error --output /dev/null --write-out '%{http_code}' "$2"
}

login_and_check_cookie() {
  local ca_file=$1 base="https://localhost:$port" status
  status=$(curl --noproxy '*' --cacert "$ca_file" --resolve "localhost:$port:127.0.0.1" --silent --show-error \
    --output /dev/null --write-out '%{http_code}' \
    --dump-header "$tmpdir/login.headers" --cookie-jar "$tmpdir/cookies" \
    --header "Origin: https://localhost:$port" --header 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode 'username=admin' --data-urlencode "password@$tmpdir/password" "$base/login")
  [ "$status" = 303 ] || fail 'same-origin login did not return 303'
  grep -Eiq '^Set-Cookie:.*;[[:space:]]*HttpOnly;[[:space:]]*Secure;[[:space:]]*SameSite=Strict' \
    "$tmpdir/login.headers" || fail 'session cookie security attributes are missing'
  status=$(curl --noproxy '*' --cacert "$ca_file" --resolve "localhost:$port:127.0.0.1" \
    --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --cookie "$tmpdir/cookies" "$base/api/devices")
  [ "$status" = 200 ] || fail 'authenticated /api/devices did not return 200'
}

compose config --quiet
compose up -d --build
wait_healthy
cid=$(compose ps --quiet serverdesk)

security=$(docker inspect --format '{{.Config.User}}|{{.HostConfig.ReadonlyRootfs}}|{{json .HostConfig.CapDrop}}|{{json .HostConfig.SecurityOpt}}|{{.HostConfig.Privileged}}|{{.HostConfig.Init}}' "$cid")
[ "$security" = '10001:10001|true|["ALL"]|["no-new-privileges:true"]|false|true' ] ||
  fail 'container user or hardening options differ from the deployment contract'
[ "$(compose exec -T serverdesk id -u)" = 10001 ] || fail 'runtime process is not UID 10001'
mapping=$(docker inspect --format '{{range (index .HostConfig.PortBindings "6005/tcp")}}{{.HostIp}}:{{.HostPort}}{{end}}' "$cid")
[ "$mapping" = "127.0.0.1:$port" ] || fail 'application port is not loopback-only'

for tool in ssh setsid; do
  compose exec -T serverdesk sh -c "command -v $tool >/dev/null" || fail "runtime tool missing: $tool"
done
for path_mode in \
  '/var/lib/serverdesk 700' \
  '/var/lib/serverdesk/tls 700' \
  '/var/lib/serverdesk/config.local.json 600' \
  '/var/lib/serverdesk/auth.json 600' \
  '/var/lib/serverdesk/initial-login.txt 600' \
  '/var/lib/serverdesk/tls/server.crt 600' \
  '/var/lib/serverdesk/tls/server.key 600'; do
  path=${path_mode% *}
  mode=${path_mode##* }
  [ "$(compose exec -T serverdesk stat -c '%u:%g:%a' "$path")" = "10001:10001:$mode" ] ||
    fail "unexpected owner or mode: $path"
done

compose exec -T serverdesk cat /var/lib/serverdesk/tls/server.crt > "$tmpdir/server.crt"
base="https://localhost:$port"
[ "$(https_status "$tmpdir/server.crt" "$base/api/health")" = 200 ] || fail 'certificate-verified HTTPS health failed'
[ "$(https_status "$tmpdir/server.crt" "$base/api/devices")" = 401 ] || fail 'unauthenticated /api/devices was not 401'

compose exec -T serverdesk cat /var/lib/serverdesk/initial-login.txt > "$tmpdir/initial-login.txt"
grep -Fxq 'ADMIN_USERNAME=admin' "$tmpdir/initial-login.txt" || fail 'initial username was not initialized'
password_line=$(sed -n 's/^ADMIN_PASSWORD=//p' "$tmpdir/initial-login.txt")
[ -n "$password_line" ] || fail 'initial password was not initialized'
printf '%s' "$password_line" > "$tmpdir/password"
unset password_line
login_and_check_cookie "$tmpdir/server.crt"
assert_no_secret_logs
compose exec -T serverdesk rm -f /var/lib/serverdesk/initial-login.txt
compose exec -T serverdesk sh -c 'umask 077; printf "%s\n" "smoke-persisted-state" > /var/lib/serverdesk/data/.smoke-persisted'
before=$(compose exec -T serverdesk sha256sum /var/lib/serverdesk/config.local.json /var/lib/serverdesk/auth.json \
  /var/lib/serverdesk/tls/server.crt /var/lib/serverdesk/tls/server.key /var/lib/serverdesk/data/.smoke-persisted)

compose down
compose up -d
wait_healthy
after=$(compose exec -T serverdesk sha256sum /var/lib/serverdesk/config.local.json /var/lib/serverdesk/auth.json \
  /var/lib/serverdesk/tls/server.crt /var/lib/serverdesk/tls/server.key /var/lib/serverdesk/data/.smoke-persisted)
[ "$before" = "$after" ] || fail 'state changed across container recreation'
if compose exec -T serverdesk test -e /var/lib/serverdesk/initial-login.txt; then
  fail 'initial-login.txt was unexpectedly regenerated'
fi
login_and_check_cookie "$tmpdir/server.crt"
assert_no_secret_logs
printf 'First boot, hardening, HTTPS login, and volume persistence passed.\n'

openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -subj '/CN=serverdesk smoke CA' \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -keyout "$tmpdir/ca.key" -out "$tmpdir/ca.crt" >/dev/null 2>&1
openssl req -new -newkey rsa:2048 -nodes -subj '/CN=localhost' \
  -keyout "$tmpdir/issued.key" -out "$tmpdir/issued.csr" >/dev/null 2>&1
printf '%s\n' \
  'subjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1' \
  'extendedKeyUsage=serverAuth' \
  'keyUsage=digitalSignature,keyEncipherment' > "$tmpdir/issued.ext"
openssl x509 -req -in "$tmpdir/issued.csr" -CA "$tmpdir/ca.crt" \
  -CAkey "$tmpdir/ca.key" -CAcreateserial -days 2 -sha256 \
  -extfile "$tmpdir/issued.ext" -out "$tmpdir/issued.crt" >/dev/null 2>&1

compose down
export SERVERDESK_TLS_CA_FILE=/var/lib/serverdesk/tls/smoke-ca.crt
compose run --rm --no-deps -T --entrypoint sh serverdesk -c 'umask 077; cat > /var/lib/serverdesk/tls/server.crt' < "$tmpdir/issued.crt"
compose run --rm --no-deps -T --entrypoint sh serverdesk -c 'umask 077; cat > /var/lib/serverdesk/tls/server.key' < "$tmpdir/issued.key"
compose run --rm --no-deps -T --entrypoint sh serverdesk -c 'umask 077; cat > /var/lib/serverdesk/tls/smoke-ca.crt' < "$tmpdir/ca.crt"
compose up -d
wait_healthy
login_and_check_cookie "$tmpdir/ca.crt"
printf 'Custom CA healthcheck and certificate-verified login passed.\n'

cert_before=$(compose exec -T serverdesk sha256sum /var/lib/serverdesk/tls/server.crt)
key_before=$(compose exec -T serverdesk sha256sum /var/lib/serverdesk/tls/server.key)
compose down
compose run --rm --no-deps -T --entrypoint sh serverdesk -c \
  'mv /var/lib/serverdesk/tls/server.key /var/lib/serverdesk/tls/server.key.smoke-backup'
compose up -d
found_error=false
for _ in $(seq 1 30); do
  compose logs --no-color serverdesk > "$tmpdir/fail-closed.log" 2>&1 || true
  if grep -Fq 'TLS requires both tls/server.crt and tls/server.key' "$tmpdir/fail-closed.log"; then
    found_error=true
    break
  fi
  sleep 1
done
[ "$found_error" = true ] || fail 'partial TLS pair did not fail closed'
compose stop -t 1 serverdesk >/dev/null
compose run --rm --no-deps -T --entrypoint sh serverdesk -c \
  'test -s /var/lib/serverdesk/tls/server.crt &&
   test -s /var/lib/serverdesk/tls/server.key.smoke-backup &&
   test ! -e /var/lib/serverdesk/tls/server.key'
cert_after=$(compose run --rm --no-deps -T --entrypoint sha256sum serverdesk \
  /var/lib/serverdesk/tls/server.crt)
key_after=$(compose run --rm --no-deps -T --entrypoint sha256sum serverdesk \
  /var/lib/serverdesk/tls/server.key.smoke-backup)
[ "${cert_before%% *}" = "${cert_after%% *}" ] || fail 'failed TLS startup replaced the existing certificate'
[ "${key_before%% *}" = "${key_after%% *}" ] || fail 'failed TLS startup changed the existing private key'
if grep -Fq 'ADMIN_PASSWORD=' "$tmpdir/fail-closed.log" || grep -Fq -f "$tmpdir/password" "$tmpdir/fail-closed.log"; then
  fail 'credentials appeared in fail-closed container logs'
fi
compose run --rm --no-deps -T --entrypoint sh serverdesk -c \
  'mv /var/lib/serverdesk/tls/server.key.smoke-backup /var/lib/serverdesk/tls/server.key'
compose up -d
wait_healthy
assert_no_secret_logs
printf 'Partial TLS pair failed closed without replacing persisted material.\n'
printf 'Docker deployment smoke passed; cleanup is limited to project %s.\n' "$project"
