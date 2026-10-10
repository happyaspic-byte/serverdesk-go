# Docker Compose 배포

Docker 배포는 Docker Engine 또는 Docker Desktop과 Compose v2를 호스트 전제조건으로 사용한다.
호스트 Go/Node 설치는 필요하지 않다. 이 안내는 컨테이너 시작 절차이지 제품의 실장비 인증을 의미하지 않으며,
현재 지원 범위와 Windows 상용 배포 NO-GO 조건은 [지원 매트릭스](SUPPORT-MATRIX.md)와
[운영 준비도](PRODUCTION-READINESS.md)를 계속 따른다.

## 시작과 최초 로그인

프로젝트 루트에서 실행한다.

```bash
docker compose up -d --build --wait
docker compose exec serverdesk cat /var/lib/serverdesk/initial-login.txt
```

두 번째 명령은 `ADMIN_USERNAME`과 초기 `ADMIN_PASSWORD`를 터미널에 표시한다. 비밀번호를 안전한 암호
관리자에 기록하고 로그·티켓·공유 터미널에 복사하지 않는다. 컨테이너 초기화는 자격증명을 컨테이너 로그에
출력하지 않는다. 기본 포트는 호스트의 `127.0.0.1:6005`에만 공개된다. 브라우저에서
[`https://localhost:6005`](https://localhost:6005)에 접속한다. 기본 TLS 인증서는 `localhost`,
`127.0.0.1`, `::1` SAN을 가진 자체서명 인증서이므로 브라우저의 신뢰 경고는 로컬 온보딩에서만 예상된다.

기록 후 관리자 암호를 표준 입력으로 교체하고 평문 초기 암호 파일을 지운다. 아래 명령은 암호를 터미널
입력으로만 받고 argv나 환경 변수에 넣지 않는다.

```bash
read -r -s -p '새 관리자 비밀번호: ' new_password
printf '\n'
printf '%s\n' "$new_password" |
  docker compose exec -T serverdesk serverdesk \
    -auth /var/lib/serverdesk/auth.json -set-auth-password
unset new_password
docker compose exec -T serverdesk rm -f /var/lib/serverdesk/initial-login.txt
```

초기화가 중단됐거나 초기 암호 파일을 잃었지만 `auth.json`은 유효하면 같은 stdin 방식으로 암호를 다시
설정한다. 인증 파일이 없거나 손상된 경우 named volume을 삭제하지 말고, 먼저 상태를 백업해 운영자가
복구를 판단한다. 비밀번호·auth 초기화 파일은 Docker 로그로 확인하려 하지 않는다.

## 영속 상태, 업데이트와 백업

`serverdesk-data` named volume은 `/var/lib/serverdesk`에 마운트된다. config, `auth.json`,
`initial-login.txt`, `credentials/`, TLS 인증서와 개인키, 그리고 config의 기본 `runtime_dir`인
`data/`가 여기에 유지된다. 컨테이너와 상태 디렉터리는 UID:GID `10001:10001`로 실행·소유되고,
상태 디렉터리는 `0700`, 민감 파일은 `0600`으로 생성된다. 쓰기 가능 영역은 이 volume과 제한된
`/tmp`뿐이며 컨테이너 rootfs는 read-only다. 한 volume을 둘 이상의 Serverdesk 컨테이너가 동시에
사용하지 않는다.

설정 파일은 volume 안에서 non-root UID로 갱신해야 한다. `docker cp`는 파일 소유권/모드를 잘못
설정할 수 있으므로 사용하지 않는다. 유지보수 중에는 서비스를 정지하고 stdin으로 새 config를 안전하게
기록한 뒤 재기동한다.

```bash
docker compose stop serverdesk
docker compose run --rm --no-deps -T --entrypoint sh serverdesk -c \
  'umask 077; cat > /var/lib/serverdesk/config.local.json.new &&
   mv /var/lib/serverdesk/config.local.json.new /var/lib/serverdesk/config.local.json' \
  < config.local.json
docker compose up -d
```

상태에는 인증 verifier와 TLS 개인키가 포함되므로 업데이트 전 백업을 암호화된 접근제한 저장소에 둔다.
일관된 백업을 위해 서비스를 정지하고 volume을 읽는 일회성 non-root 컨테이너의 stdout을 호스트 파일로
리다이렉트한다. 호스트 umask가 백업 파일을 `0600`으로 만든다.

```bash
docker compose stop serverdesk
umask 077
docker compose run --rm --no-deps -T --entrypoint tar serverdesk \
  -C /var/lib/serverdesk -czf - . > serverdesk-state.tar.gz
docker compose up -d
```

업데이트는 백업 후 새 소스에서 다시 빌드한다. 기존 named volume은 그대로 보존된다.

```bash
docker compose up -d --build
docker compose ps
```

일반 정지·재생성에는 `docker compose down` 또는 위 `up -d --build`를 사용한다. `docker compose down -v`는
해당 프로젝트의 전체 설정·auth·credential·TLS·runtime 상태를 삭제하므로 상태 폐기를 명시적으로 승인한
경우에만 사용한다.

## TLS, 포트와 외부 접근

Compose는 앱 리스너를 `0.0.0.0:6005`에서 TLS로 실행한다. 이 CLI 인수는 config의 `listen`,
`tls_cert_file`, `tls_key_file`을 의도적으로 우선한다. 호스트 공개 주소/포트는 `SERVERDESK_BIND`
(기본 `127.0.0.1`) 및 `SERVERDESK_PORT`(기본 `6005`)로 조정한다. 기본값에서 외부 네트워크에는
포트를 공개하지 않는다.

기본 자체서명 인증서는 최초 한 번만 생성한다. 파일이 이미 있으면 덮어쓰지 않으며, 인증서나 개인키 중
하나만 있거나 쌍이 잘못되면 컨테이너는 안전하게 시작을 거부한다. 인터넷/LAN 운영은 `tls/server.crt`에
호스트 이름 SAN이 포함된 신뢰된 인증서(필요하면 full chain)와 일치하는 `tls/server.key`를
`/var/lib/serverdesk` volume에 설치한다. 파일을 교체할 때는 먼저 서비스를 멈추고 non-root 일회성
컨테이너의 stdin으로 각각 복사한다. 이 방법은 호스트의 `docker cp`나 root 소유 파일을 피하고 소유권을
UID 10001로 유지한다. 재기동 전에 아래 설명에 따라 `.env`의 `SERVERDESK_TLS_HOST`와
`SERVERDESK_TLS_CA_FILE`을 새 인증서에 맞게 설정한다.

```bash
docker compose stop serverdesk
docker compose run --rm --no-deps -T --entrypoint sh serverdesk -c \
  'umask 077; cat > /var/lib/serverdesk/tls/server.crt' < server.crt
docker compose run --rm --no-deps -T --entrypoint sh serverdesk -c \
  'umask 077; cat > /var/lib/serverdesk/tls/server.key' < server.key
docker compose up -d
```

갱신 시에도 config, auth, credential은 그대로 두고 인증서/키 쌍만 교체한다. 인증서는 자동 갱신되지
않으므로 만료 전에 백업하고 같은 절차로 갱신한 뒤 health와 외부 브라우저의 호스트 이름 검증을 확인한다.
컨테이너 healthcheck는 `SERVERDESK_TLS_HOST`(기본 `localhost`)를 인증서 이름으로 검증하며 요청을
컨테이너 루프백으로 보낸다. 기본 self-signed 인증서에는 volume의 `tls/server.crt`가 검증 CA다.
사설 CA 인증서를 사용할 때 `SERVERDESK_TLS_CA_FILE`을 volume 안 CA 파일 경로로 설정하고, 공개 CA
인증서는 컨테이너의 `/etc/ssl/certs/ca-certificates.crt`를 지정한다. 사설 CA PEM도 TLS 파일과 같은
non-root stdin 방식으로 `tls/ca.crt`에 설치하고 루트 `.env`에
`SERVERDESK_TLS_CA_FILE=/var/lib/serverdesk/tls/ca.crt`를 둔다. `.env`는 이 저장소에서 무시된다.
검증을 비활성화하지 않는다.

LAN 공개는 신뢰된 인증서·정확한 `SERVERDESK_TLS_HOST`와 호스트 방화벽을 준비한 뒤에만 `.env`에
`SERVERDESK_BIND=0.0.0.0`을 명시해 opt-in한다. 공개 범위가 의도한 인터페이스로 제한되는지 방화벽에서
확인한다. 사용자 정의 compose 설정은 해당 호스트와 인증서에 맞춰 검토한다.

## 장비 네트워크와 외부 구성요소

공개 Docker image에는 저작권/라이선스가 필요한 AVCLI, JRE, Stratus MIB가 없다. 실제 Stratus 수집이
필요하면 허가받은 고객 제공 파일을 별도 read-only bind mount로 추가하는 `compose.override.yaml`을
현장별로 관리하고, config의 `avcli_bin`을 컨테이너 안 절대 경로로 지정한다. 필요한 JRE 경로도 해당
override의 PATH에 추가한다. `trap.mib_dir`은 마운트한 MIB 경로를 명시한다. UID 10001이 파일을 읽고
실행할 수 있게 호스트 권한을 점검한다. 예제 override나 proprietary 파일은 이 저장소에 포함하지 않는다.

SNMP trap은 예제 설정에서 비활성이고 UDP는 기본 publish되지 않는다. 현장에서 필요성을 확인한 뒤
`trap.enabled`, `trap.bind`, `trap.port`를 명시하고, 방화벽 정책에 맞는 주소로 `10162/udp`를 별도
Compose override에 추가한다. PLC 보조 IP나 호스트 network mode가 필요한 환경도 사이트별 라우팅과
보안 검토가 필요하다. 기본 설정은 host network, privileged mode, 추가 capability를 허용하지 않는다.
컨테이너 smoke 테스트는 배포 경계만 검증하며 실제 장비 연결·벤더 지원·상용 인증을 주장하지 않는다.
