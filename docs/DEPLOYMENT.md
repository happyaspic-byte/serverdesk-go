# Serverdesk production deployment

이 문서는 Linux/systemd 고객 설치의 보안 경계, 장비 credential 이관, 선택적 네트워크 준비,
검증 및 롤백 절차를 정의한다. 설치·업데이트 스크립트는 프로젝트 루트에서 실행한다.

## 1. 설치·업데이트·제거

```bash
sudo sh deploy/packaging/install-linux.sh
sudo sh deploy/packaging/update-linux.sh
sudo sh deploy/packaging/uninstall-linux.sh
sudo sh deploy/packaging/uninstall-linux.sh --full
```

`update-linux.sh`는 별도 축약 경로가 아니라 idempotent installer를 재사용한다. installer는 패키지
자산과 기존 파일 권한을 먼저 검사하고, 실행 중이던 서비스를 멈춘 뒤 바이너리와 두 systemd unit을
교체한다. 새 프로세스의 실행 파일 경로와 `/api/health`가 모두 확인되지 않으면 바이너리·unit·network
helper를 이전 사본으로 복구하고 원래 실행 중이던 서비스를 다시 시작한다.

직접 TLS나 사용자 지정 포트를 이미 쓰는 업데이트는 로컬 health URL을 명시한다. 값은 SSRF를 막기
위해 `127.0.0.1` 또는 `localhost`의 `http(s)://HOST:PORT/api/health` 정확한 형태만 허용하며,
userinfo·query·fragment는 거부한다. HTTPS 인증서는 시스템 trust store에서 검증돼야 한다.

```bash
sudo SERVERDESK_HEALTH_URL=https://localhost:6443/api/health \
  sh deploy/packaging/update-linux.sh
```

일반 제거는 `/var/lib/serverdesk`와 `/etc/serverdesk`를 보존한다. `--full`은 인증 저장소,
장비 credential 원본, network 설정까지 제거하므로 백업을 확인한 뒤에만 사용한다.

## 2. systemd 보안 경계

main daemon은 전용 `serverdesk` 사용자, 빈 capability set, `NoNewPrivileges`, read-only system,
private `/tmp`와 device view, kernel/control-group 보호로 실행된다. 쓰기는
`/var/lib/serverdesk`만 허용된다. 네트워크 변경은 별도 oneshot unit만 `CAP_NET_ADMIN`을 가지며,
main daemon으로 capability가 전달되지 않는다.

비밀값을 `/etc/serverdesk/serverdesk.env` 또는 unit의 `Environment=`에 넣지 않는다. 이 파일은
비밀이 아닌 런타임 설정만을 위한 선택적 파일이다.

## 3. 장비 credential과 평문 이관

운영 config는 top-level `"secret_policy": "require-references"`를 사용한다. 비밀 필드는
`"secret://NAME"`만 저장하고 값은 systemd credential로 주입한다. 기존 평문 config를 이관할 때는
서비스를 멈추고 새 바이너리의 원자적 migration을 실행한다.

```bash
sudo systemctl stop serverdesk
sudo install -d -o root -g root -m 0700 /etc/serverdesk/credentials
sudo /opt/serverdesk/serverdesk \
  -c /var/lib/serverdesk/config.local.json \
  -migrate-secrets /etc/serverdesk/credentials
sudo chown serverdesk:serverdesk /var/lib/serverdesk/config.local.json
sudo chmod 0600 /var/lib/serverdesk/config.local.json
```

출력된 `CREDENTIAL=NAME`마다 `/etc/systemd/system/serverdesk.service.d/credentials.conf`에 한 줄을
추가한다. 저장소의 `deploy/serverdesk-credentials.conf.example`을 시작점으로 사용할 수 있다.

```ini
[Service]
LoadCredential=NAME:/etc/serverdesk/credentials/NAME
```

credential 원본 디렉터리와 파일은 root 소유(mode 0700/0600)를 유지한다. systemd는 서비스 전용
`CREDENTIALS_DIRECTORY` 아래에 read-only 사본을 만들고 Serverdesk가 참조 이름으로 읽게 한다.
설정 후 다음을 실행한다.

```bash
sudo systemctl daemon-reload
sudo systemctl restart serverdesk
sudo systemctl --no-pager --full status serverdesk
```

이관은 같은 값의 기존 credential을 재사용하고 다른 값으로 덮어쓰지 않으므로 재실행할 수 있다.
실패 시 config는 교체되지 않는다. migration 후 config 소유자를 `serverdesk`로 되돌리는 단계를
생략하면 hardened unit이 파일을 읽지 못한다.

## 4. 선택적 보조 IP와 trap redirect

`/etc/serverdesk/network.env`의 기본값은 보조 IP와 redirect를 모두 비활성화한다. 필요한 경우
인터페이스/IPv4 CIDR 쌍을 지정하고, trap redirect를 명시적으로 켠다.

```text
SERVERDESK_NET_INTERFACE=enp2s0
SERVERDESK_AUX_ADDRESS=192.0.2.10/24
SERVERDESK_ENABLE_TRAP_REDIRECT=true
SERVERDESK_TRAP_SOURCE_PORT=162
SERVERDESK_TRAP_TARGET_PORT=10162
```

적용 전 root 권한 없이 문법만 검사할 수 있다. 실제 apply에서는 인터페이스 존재 여부와 `ip` /
`iptables` 가용성도 확인한다.

```bash
/opt/serverdesk/serverdesk-network --config /etc/serverdesk/network.env validate
sudo systemctl restart serverdesk-net
```

helper는 설정을 shell로 source/eval하지 않고 허용된 key·문자·IPv4 CIDR·port 범위를 검증한다.
redirect rule에는 `serverdesk-managed-trap-redirect` 소유권 comment를 붙여 중복 생성을 막는다.
apply 시 새로 만든 주소와 rule만 root-only state에 소유 자원으로 기록하며, 기존 자원은 절대 인수하지
않는다. 재적용은 기존 소유권을 보존하고 stop/uninstall은 이 state에 기록된 자원만 제거한다.

## 5. 배포 전 정적 검증

다음 검사는 root 권한이나 실행 중 systemd가 필요 없다. 모든 shell 문법, legacy host IP/NIC
하드코딩 회귀, main/network capability 분리, 필수 sandbox 지시자, secret policy, 악성 network
설정 거부, 그리고 사용 가능한 경우 `systemd-analyze verify`와 ShellCheck를 검사한다.

```bash
sh deploy/packaging/validate-deployment.sh
```

실제 설치 후에는 다음도 확인한다.

```bash
sudo systemd-analyze verify /etc/systemd/system/serverdesk.service \
  /etc/systemd/system/serverdesk-net.service
sudo systemd-analyze security serverdesk.service
curl --fail --silent http://127.0.0.1:6005/api/health
```
