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
실패한 transaction의 backup은 자동 삭제하지 않으므로, rollback 검증 또는 재시작이 실패하면
`.install-backup` 파일을 보존한 채 운영자가 원본 metadata와 service 상태를 확인해야 한다.

직접 TLS나 사용자 지정 포트를 이미 쓰는 업데이트는 로컬 health URL을 명시한다. 값은 SSRF를 막기
위해 configured scheme/port의 `http(s)://HOST:PORT/api/health` 정확한 형태만 허용하며,
userinfo·query·fragment를 거부한다. Host의 모든 DNS 주소가 loopback 또는 이 서버의 실제 interface
주소여야 하므로 원격 Serverdesk 응답으로 로컬 업데이트 성공을 가장할 수 없다. HTTPS 인증서는
시스템 trust store에서 검증돼야 한다.

```bash
sudo SERVERDESK_HEALTH_URL=https://localhost:6443/api/health \
  sh deploy/packaging/update-linux.sh
```

일반 제거는 `/var/lib/serverdesk`, `/etc/serverdesk`, `/opt/serverdesk/mibs` 및 고객이 별도
프로비저닝한 AVCLI/JRE/TLS 파일을 보존하고 package-owned 실행 파일만 제거한다. `--full`은 인증
저장소, managed credential, vendor 파일, network 설정까지 제거하므로 백업 후에만 사용한다.

## 2. systemd 보안 경계

main daemon은 전용 `serverdesk` 사용자, 빈 capability set, `NoNewPrivileges`, read-only system,
private `/tmp`와 device view, kernel/control-group 보호로 실행된다. 쓰기는
`/var/lib/serverdesk`만 허용된다. 네트워크 변경은 별도 oneshot unit만 `CAP_NET_ADMIN`을 가지며,
main daemon으로 capability가 전달되지 않는다.

비밀값을 `/etc/serverdesk/serverdesk.env` 또는 unit의 `Environment=`에 넣지 않는다. unit에는
비밀값이 아니라 writable store 경로인 `SERVERDESK_CREDENTIALS_STORE`만 기록한다.

Stratus cluster의 AVCLI는 vendor 제약 때문에 비밀번호를 잠시 argv에 포함한다. Unit-private
`ProtectProc`는 host의 다른 login account를 차단한다는 증거가 아니므로 packaged unit은 runtime이
실제 host `/proc` 상태를 보게 유지한다. Installer는 cluster가 있으면 host `/proc`의 `hidepid=2`를
확인하고, 그것이 없을 때는 non-root interactive login account가 하나도 없는 경우에만 계속한다.
Packaged install에는 우회 옵션이 없다. 수동 실행의 `-allow-argv-exposure`는 기록·승인된
break-glass일 뿐 production acceptance 통과로 취급하지 않는다.

Linux TLS private key는 symlink 없는 regular file, owner `root` 또는 `serverdesk`, mode `0400` 또는
`0600`이어야 하며 installer가 `serverdesk` identity로 실제 읽기까지 확인한다. Key와 모든 parent
directory는 symlink가 아니어야 하고 parent는 root-owned/non-writable이어야 한다. Writable state tree인
`/var/lib/serverdesk` 아래 key는 거부한다. 권장 배치는 root-owned `/opt/serverdesk/tls` directory 안의
`serverdesk:serverdesk 0400` key다. Unit의 `ProtectSystem=strict`가 daemon namespace에서 `/opt`를
read-only로 유지한다. 인증서는 공개 권한이어도 되지만 key의 group/other 접근은 거부한다.

```bash
sudo install -d -o root -g root -m 0755 /opt/serverdesk/tls
sudo install -o serverdesk -g serverdesk -m 0400 customer-server.key /opt/serverdesk/tls/server.key
sudo install -o root -g root -m 0644 customer-server.crt /opt/serverdesk/tls/server.crt
```

## 3. 장비 credential과 평문 이관

운영 config는 top-level `"secret_policy": "require-references"`를 사용한다. 비밀 필드는
`"secret://NAME"`만 저장하고 값은 managed store에서 읽는다. 기존 평문 config를 이관할 때는
서비스를 멈추고 서비스 계정으로 원자적 migration을 실행한다.

```bash
sudo systemctl stop serverdesk
sudo install -d -o serverdesk -g serverdesk -m 0700 /var/lib/serverdesk/credentials
sudo -u serverdesk /opt/serverdesk/serverdesk \
  -c /var/lib/serverdesk/config.local.json \
  -migrate-secrets /var/lib/serverdesk/credentials
```

UI/CLI가 관리하는 기본 store는 `/var/lib/serverdesk/credentials`다. 외부에서 별도 프로비저닝한
read-only credential만 systemd drop-in으로 추가하며, 저장소의 example을 시작점으로 사용할 수 있다.

```ini
[Service]
LoadCredential=NAME:/etc/serverdesk/credentials/NAME
```

managed store는 serverdesk 소유(mode 0700/0600)를 유지한다. systemd는 선택적 외부 credential만
서비스 전용 `CREDENTIALS_DIRECTORY`에 read-only로 제공하며 Serverdesk는 이 디렉터리에 쓰지 않는다.
drop-in을 사용했다면 다음을 실행한다.

```bash
sudo systemctl daemon-reload
sudo systemctl restart serverdesk
sudo systemctl --no-pager --full status serverdesk
```

이관은 같은 값의 기존 credential을 재사용하고 다른 값으로 덮어쓰지 않으므로 재실행할 수 있다.
실패 시 config는 교체되지 않는다. migration을 서비스 계정으로 실행해 config 소유권과 store 쓰기
권한을 그대로 유지한다.

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
HEALTH_URL=$(sudo sh deploy/packaging/serverdesk-endpoint.sh /var/lib/serverdesk/config.local.json)
curl --fail --silent --show-error "$HEALTH_URL"
```

## 6. Windows endpoint, update, and vendor dependencies

> **Commercial production NO-GO:** current Windows packages run the Scheduled Task as `SYSTEM`.
> Do not approve or score Windows production readiness until program/state paths are split into
> `C:\Program Files\Serverdesk` (read/execute only) and `C:\ProgramData\Serverdesk` (narrow writable
> state), the task runs as LocalService/a dedicated least-privilege identity, and DPAPI plus
> install/update/rollback/uninstall migration is validated on real Windows Server. The controls below
> reduce deployment risk but do not waive this privilege blocker.

Windows installer/updater reads `listen`, TLS certificate/key, and `allow_insecure_http` from the
installed config. A loopback listener creates no inbound firewall rule. A non-loopback listener opens
only the configured TCP port, installed executable, `LocalSubnet`, and Domain/Private profiles.
The success message never advertises remote access for a loopback-only configuration.

`runtime_dir` must resolve below `C:\serverdesk`; external runtime directories are rejected because
their ACL and rollback state are outside the managed transaction. A TLS private key must also be below
`C:\serverdesk`, be owned by SYSTEM or Administrators, and have ACL entries only for those principals.

TLS with a wildcard listener requires an explicit certificate-valid local health name; certificate
validation is never bypassed:

```powershell
powershell -ExecutionPolicy Bypass -File .\install-windows.ps1 `
  -HealthUrl https://serverdesk.customer.local:6443/api/health
powershell -ExecutionPolicy Bypass -File C:\serverdesk\update.ps1 `
  -Binary C:\staging\serverdesk-windows-amd64.exe `
  -HealthUrl https://serverdesk.customer.local:6443/api/health
```

Update snapshots and SHA-256 verifies the binary, config, maintenance scripts, scheduled task, and
managed firewall state before stopping the old process. A failed health check restores them and
restarts only when the prior task was running. A previously Ready/Disabled task is tested
transiently, then returned to that state.
The updater also refuses an unrecognized same-name task, missing runtime directory, reparse path,
or installation/control/runtime path writable by non-administrators; use the full installer to
harden a recognized legacy installation before updating it.

Default uninstall removes only the scheduled task, managed firewall rule, executable, and runner.
It preserves config/auth, DPAPI credentials, data/logs, TLS material, licensed MIB/AVCLI/JRE, and
unknown customer files. `-Full` is the only mode that removes the complete installation tree.
Either uninstall mode fails before service mutation when an install/update transaction or recovery
backup remains; inspect and recover that evidence before deliberately removing it.

Public packages do not contain AVCLI, JRE, or vendor MIBs. Provision licensed dependencies first and
set `avcli_bin`/`avcli_args`. Place licensed MIB files in `/opt/serverdesk/mibs` or
`C:\serverdesk\mibs`; `config.example.json` points `trap.mib_dir` to `mibs`. If a Stratus cluster is
configured but AVCLI/JRE is missing, install/update fails before downtime. The explicit
`SERVERDESK_ALLOW_DEGRADED_COLLECTION=1` override records an operator decision to run without that
collector; it is not suitable for a completed production acceptance test.
