# serverdesk-go

Stratus everRun / ztC Edge와 엣지 장비(NAS·PLC·프린터·Proxmox·일반 서버)를 한 화면에서 감시하는 읽기 전용 단일 바이너리 모니터링 콘솔. Python 스택(everrun-poller + server-monitoring)을 하나의 Go 바이너리로 통합한 후속 프로젝트다.

> **지원 범위:** 현재 릴리스는 everRun/ztC Edge 모니터링용이다. ztC Endurance 화면 모델은
> 설계·표시 호환용 프리뷰이며, Endurance SNMPv3/OPC UA/IPMI 수집은 아직 구현·실장비 인증되지
> 않았다. 재부팅·종료·failover 같은 장비 관리 액션도 제공하지 않는다.

- 백엔드: Go(stdlib-only) — 폴리(avcli/SNMP/SSH), SNMP 트랩 수신, 실측 가용성 트래커, 이벤트 로그
- 프런트: Vanilla JS ES modules + CSS (빌드 단계 없음, embed.FS로 바이너리에 내장)
- Go 코드 의존성: 표준 라이브러리만 사용한다. 단, 운영 수집에는 고객이 적법하게 제공한
  Stratus AVCLI/JRE/MIB와 호스트의 OpenSSH 도구가 필요하며 공개 소스·릴리스에는 이를 포함하지 않는다.

## 빌드

```bash
go build -o serverdesk ./cmd/serverdesk
```

툴체인이 리포에 없으면 Go 1.26+ 아무 경로나 PATH 에 두면 된다.

## 실행

```bash
cp config.example.json config.local.json   # secret:// 참조만 입력, chmod 600
./serverdesk -auth auth.json -init-auth   # 최초 1회: 출력된 관리자 자격증명을 안전하게 기록
./serverdesk -c config.local.json -auth auth.json
```

- 웹 리스너: 기본 예시는 `127.0.0.1:6005`(루프백 HTTP) — 로그인 후 콘솔 UI + /api/* 사용
- SNMP 트랩: `trap.port`(기본 10162/udp). privileged 162/udp 리다이렉트나 PLC 보조 IP가
  필요하면 설치 후 `/etc/serverdesk/network.env`에 호스트별 값을 명시한다. 기본은 두 기능 모두
  비활성이고, `serverdesk-net.service`는 검증된 값만 CAP_NET_ADMIN 헬퍼에 전달한다.
- 웹 로그인: `-init-auth`가 auth 저장소마다 고유한 관리자 자격증명을 생성하고
  `ADMIN_USERNAME`/`ADMIN_PASSWORD`로 출력한다. `auth.json`에는 PBKDF2-HMAC-SHA256
  검증값만 저장한다. `-auth auth.json -check-auth`는 동일한 엄격한 런타임 규칙으로 파일을
  점검한다. 비밀번호 변경은 `-auth auth.json -set-auth-password`의 표준 입력으로 전달한다.
  파일 기반 인증 정보가 바뀌면 다음 보호 요청에서 즉시 다시 읽고 기존 세션을 모두 폐기한다.
- 장비 자격증명: 운영 설정은 `secret_policy=require-references`를 사용한다. 기존 평문 설정은
  `-migrate-secrets <directory>`로 참조형 설정과 보호된 credential 파일로 변환한다. Linux systemd
  Credentials, UI용 writable managed store, Windows machine-scoped DPAPI 절차는
  [`docs/CREDENTIALS.md`](docs/CREDENTIALS.md)에 있다.
- 평문 HTTP는 루프백 주소에서만 기동한다. 외부 접속은 다음 중 하나로 보호한다.
  - 직접 HTTPS: `tls_cert_file`과 `tls_key_file`을 함께 설정하거나 `-tls-cert`/`-tls-key`를 사용한다.
  - 동일 호스트 TLS 역프록시: Serverdesk는 루프백에 유지하고, 프록시가 `X-Forwarded-Proto`와 실제
    client IP가 마지막 값인 `X-Forwarded-For`를 전달한다.
  - `allow_insecure_http=true`(또는 `-allow-insecure-http`)는 레거시용 break-glass 호환 모드다.
    비루프백 peer의 forwarded header를 신뢰하지 않으므로 보안 운영 구성으로 간주하지 않는다.
    원격 프록시는 backend까지 TLS를 사용하고, 평문 역프록시는 동일 호스트 루프백에 둔다.
  기존 `0.0.0.0` 평문 설정은 break-glass 승인이나 TLS 키 쌍 없이는 보안 오류로 기동을 거부한다.
- 장비 HTTPS는 시스템 CA와 호스트명을 기본 검증한다. 자체서명 Proxmox/Redfish/프린터는 장비별
  `tls_fingerprint`에 SPKI SHA-256 지문을 설정해야 하며, 불일치하면 TLS 핸드셰이크를 차단한다.

### 샘플 데이터로 화면 확인

실제 Stratus/엣지 장비 자격증명 없이 화면을 확인할 때만 명시적 샘플 모드를 사용한다.
`config.local.json`의 `clusters`와 `edge_devices`는 빈 배열이어야 하며, 알림과 SNMP 트랩도
비활성 상태여야 한다.

```bash
cp config.example.json config.local.json
chmod 600 config.local.json                    # Linux/macOS
./serverdesk -auth auth.json -init-auth       # 최초 1회
./serverdesk -c config.local.json -auth auth.json -demo
```

브라우저에서 `http://127.0.0.1:6005`에 접속하면 `[샘플]`로 표시된 everRun, ztC Edge, NAS
3대를 볼 수 있다. 샘플 모드는 루프백 리스너에서만 기동하고 실제 장비 폴링·웹훅·트랩·장비
설정 변경을 차단한다. 화면의 `SAMPLE DATA` 배너가 보이지 않으면 샘플 모드로 간주하지 않는다.

## Linux 고객 설치

배포 패키지 디렉터리(`deploy/packaging/`)에서 다음을 실행하거나 프로젝트 루트에서 경로를 지정해 실행한다. 최초 설치는 비대화식으로 고유한 관리자 자격증명을
생성해 `/var/lib/serverdesk/initial-login.txt`에 기록한다. 이 파일은 `serverdesk` 서비스
계정 소유, mode `0600`이므로 안전하게 기록한 뒤 삭제하거나 비밀번호를 교체한다. 재설치와
업데이트는 기존 `auth.json`과 자격증명을 보존한다.

```bash
sudo sh deploy/packaging/install-linux.sh
sudo sh deploy/packaging/update-linux.sh
sudo sh deploy/packaging/uninstall-linux.sh          # 코드와 unit만 제거, /var/lib/serverdesk 상태 보존
sudo sh deploy/packaging/uninstall-linux.sh --full   # 코드·상태·서비스 계정까지 제거
```

설치기는 main unit과 선택적 network unit을 함께 설치한다. 네트워크 변경은 기본 비활성이며,
필요한 호스트에서만 설정을 편집·검증한 후 재시작한다:

```bash
sudoedit /etc/serverdesk/network.env
sudo /opt/serverdesk/serverdesk-network --config /etc/serverdesk/network.env validate
sudo systemctl restart serverdesk-net.service
```

고객 설치의 실행 파일은 `/opt/serverdesk/serverdesk`(root 소유)이고, `config.local.json`,
`auth.json`, `initial-login.txt`는 `/var/lib/serverdesk`에서 `serverdesk` 서비스 계정 소유,
mode `0600`으로 관리된다. 장비 비밀은 config에 `secret://NAME` 참조만 남기고 systemd
`LoadCredential=`로 전달한다. 마이그레이션, credential 드롭인, 네트워크 소유권 및 롤백 절차는
[`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md)를 따른다.

## Windows 고객 설치

> **상용 배포 NO-GO:** 현재 Windows 패키지는 Scheduled Task를 `SYSTEM`으로 실행한다. 프로그램과
> 상태 경로를 `Program Files`/`ProgramData`로 분리하고 LocalService 또는 전용 최소권한 계정으로
> 전환한 뒤 실제 Windows Server에서 설치·업데이트·롤백·제거·DPAPI를 검증하기 전에는 판매 제품의
> Windows 배포를 승인하지 않는다. 상세 조건은 [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md)에 있다.

최초 설치는 입력 없이 설치별 관리자 자격증명을 생성해 `C:\serverdesk\initial-login.txt`에
기록한다. 설치 디렉터리는 SYSTEM과 Administrators만 접근할 수 있다. 자격증명을 안전하게
기록한 뒤 이 파일을 삭제하거나 비밀번호를 교체한다. 재설치는 기존 `auth.json`을 보존한다.
고객이 별도로 프로비저닝한 AVCLI는 `cmd.exe`나 배치 래퍼 없이 승인된 `java.exe`를 직접 실행한다.
AVCLI/JRE/MIB는 저작권·라이선스 경계를 지키기 위해 공개 패키지에 번들하지 않는다.
장비 credential은 `-migrate-secrets C:\serverdesk\credentials`로 machine-scoped DPAPI blob으로
변환하고, config에는 `secret://NAME` 참조만 유지한다.

## 주요 기능

- 토폴로지(회사→공장→장비 계층도, 팬/줌/핏), 플릿 개요, 클러스터/노드/용량/인시던트 화면
- ztC Endurance 단일 2U 섀시 표시 모델 프리뷰 — 실제 수집 지원이 아니며 신규 장비 등록 대상에서
  제외된다. 향후 구현 기준은 [`docs/ENDURANCE-COLLECTION.md`](docs/ENDURANCE-COLLECTION.md)에 있다.
- 경보 임계값(warn/crit %) 설정 화면 편집 — `PUT /api/admin/thresholds`, 즉시 라이브 반영
- 설정 백업/복구 — `GET /api/admin/config/export`(자격증명 마스킹) / `POST /api/admin/config/import`
  (빈 자격증명은 기존 값 유지, 장비·수집 변경은 재시작 후 적용)
- 가용성 CSV — `GET /api/availability.csv` (30일 실측)
- 공개 `/api/health`는 최소 상태만 반환하고, 인증된 `/api/admin/health`는 캐시·수집 티어·
  Edge worker·이벤트 저장 상태를 제공한다. `events.jsonl`은 최근 이력으로 자동 압축된다.
- 대시보드 경보 Quick Ack — 서버 공유 상태(/ack)로 동기화
- 서버 상주 critical 웹훅 — 브라우저가 닫혀 있어도 초기/복구/미확인 escalation을 재시작 안전 큐로 전송.
  보호 저장·allowlist·retry/dead-letter/health 계약은 [`docs/NOTIFICATIONS.md`](docs/NOTIFICATIONS.md)에 있다.

## 개발

```bash
sh deploy/packaging/validate-deployment.sh
go vet ./... && go test ./...
```

웹 자산은 embed.FS 라 `web/` 수정 후 재빌드가 필요하다. 설계 문서는 `docs/superpowers/` 아래
스펙·계획으로 남긴다.

## 라이선스 / 제작

Roobicom (루비컴) — https://roobicom.co.kr. 이 저장소는 proprietary이며 공개 사용 허가는
[`NOTICE`](NOTICE)를 따른다. 제3자 구성요소 고지는 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md),
릴리스 검증 절차는 [`docs/RELEASE.md`](docs/RELEASE.md)에 있다.
현재 production-readiness 점수와 남은 현장 검증 항목은
[`docs/PRODUCTION-READINESS.md`](docs/PRODUCTION-READINESS.md)에 기록한다. 제품별 지원 범위는
[`docs/SUPPORT-MATRIX.md`](docs/SUPPORT-MATRIX.md), 출시 현장 시험은
[`docs/COMMERCIAL-UAT.md`](docs/COMMERCIAL-UAT.md)를 따른다.
