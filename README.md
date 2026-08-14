# serverdesk-go

Stratus FT 인프라(everRun / ztC Edge / ztC Endurance)와 엣지 장비(NAS·PLC·프린터·Proxmox·일반 서버)를 한 화면에서 감시하는 단일 바이너리 모니터링 콘솔. Python 스택(everrun-poller + server-monitoring)을 하나의 Go 바이너리로 통합한 후속 프로젝트다.

- 백엔드: Go(stdlib-only) — 폴리(avcli/SNMP/SSH), SNMP 트랩 수신, 실측 가용성 트래커, 이벤트 로그
- 프런트: Vanilla JS ES modules + CSS (빌드 단계 없음, embed.FS로 바이너리에 내장)
- 외부 의존성 없음 — Go 툴체인만 있으면 된다

## 빌드

```bash
go build -o serverdesk ./cmd/serverdesk
```

툴체인이 리포에 없으면 Go 1.26+ 아무 경로나 PATH 에 두면 된다.

## 실행

```bash
cp config.example.json config.local.json   # 자격증명 입력, chmod 600
./serverdesk -auth auth.json -init-auth   # 최초 1회: 출력된 관리자 자격증명을 안전하게 기록
./serverdesk -c config.local.json -auth auth.json
```

- HTTP: `listen`(기본 예시 0.0.0.0:6005) — 로그인 후 콘솔 UI + /api/* 사용
- SNMP 트랩: `trap.port`(10162/udp). EAC 가 보내는 162/udp 를 받으려면 리다이렉트가 필요하다 —
  `serverdesk-net.service`(everrun-poller 리포)가 162→10162 REDIRECT 와 PLC 서브넷 보조 IP를 담당한다.
- 웹 로그인: `-init-auth`가 auth 저장소마다 고유한 관리자 자격증명을 생성하고
  `ADMIN_USERNAME`/`ADMIN_PASSWORD`로 출력한다. `auth.json`에는 PBKDF2-HMAC-SHA256
  검증값만 저장한다. `-auth auth.json -check-auth`는 동일한 엄격한 런타임 규칙으로 파일을
  점검한다. 비밀번호 변경은 `-auth auth.json -set-auth-password`의 표준 입력으로 전달한다.
  파일 기반 인증 정보가 바뀌면 다음 보호 요청에서 즉시 다시 읽고 기존 세션을 모두 폐기한다.
- 현재 기본 리스너는 HTTP이므로 신뢰된 내부망에서만 사용한다. 외부망은 동일 호스트 TLS
  역프록시로 종단하고 `X-Forwarded-Proto`와 실제 client IP가 마지막 값인 `X-Forwarded-For`를 전달한다.
## Linux 고객 설치

패키지 디렉터리에서 다음을 실행한다. 최초 설치는 비대화식으로 고유한 관리자 자격증명을
생성해 `/var/lib/serverdesk/initial-login.txt`에 기록한다. 이 파일은 `serverdesk` 서비스
계정 소유, mode `0600`이므로 안전하게 기록한 뒤 삭제하거나 비밀번호를 교체한다. 재설치와
업데이트는 기존 `auth.json`과 자격증명을 보존한다.

```bash
sudo sh install-linux.sh
sudo sh update-linux.sh
sudo sh uninstall-linux.sh          # 코드와 unit만 제거, /var/lib/serverdesk 상태 보존
sudo sh uninstall-linux.sh --full   # 코드·상태·서비스 계정까지 제거
```

고객 설치의 실행 파일은 `/opt/serverdesk/serverdesk`(root 소유)이고, `config.local.json`,
`auth.json`, `initial-login.txt`는 `/var/lib/serverdesk`에서 `serverdesk` 서비스 계정 소유,
mode `0600`으로 관리된다.

## Windows 고객 설치

최초 설치는 입력 없이 설치별 관리자 자격증명을 생성해 `C:\serverdesk\initial-login.txt`에
기록한다. 설치 디렉터리는 SYSTEM과 Administrators만 접근할 수 있다. 자격증명을 안전하게
기록한 뒤 이 파일을 삭제하거나 비밀번호를 교체한다. 재설치는 기존 `auth.json`을 보존한다.
번들 avcli는 `cmd.exe`나 배치 래퍼 없이 포함된 `java.exe`를 직접 실행한다.

## 주요 기능

- 토폴로지(회사→공장→장비 계층도, 팬/줌/핏), 플릿 개요, 클러스터/노드/용량/인시던트 화면
- ztC Endurance 단일 2U 섀시 모델 — CM-A/B 물리 식별자와 Active/Standby 역할 분리, IP 플랜 11개,
  서브시스템 이중화(Compute Active/Standby·Smart Exchange, Storage/I/O/PSU Active/Active)
- 읽기 전용 BMC/IPMI·SNMP·OPC UA 수집 기준: [`docs/ENDURANCE-COLLECTION.md`](docs/ENDURANCE-COLLECTION.md)
- 경보 임계값(warn/crit %) 설정 화면 편집 — `PUT /api/admin/thresholds`, 즉시 라이브 반영
- 설정 백업/복구 — `GET /api/admin/config/export`(자격증명 마스킹) / `POST /api/admin/config/import`
  (빈 자격증명은 기존 값 유지, 장비·수집 변경은 재시작 후 적용)
- 가용성 CSV — `GET /api/availability.csv` (30일 실측)
- 공개 `/api/health`는 최소 상태만 반환하고, 인증된 `/api/admin/health`는 캐시·수집 티어·
  Edge worker·이벤트 저장 상태를 제공한다. `events.jsonl`은 최근 이력으로 자동 압축된다.
- 대시보드 경보 Quick Ack — 서버 공유 상태(/ack)로 동기화

## 개발

```bash
go vet ./... && go test ./...
```

웹 자산은 embed.FS 라 `web/` 수정 후 재빌드가 필요하다. 설계 문서는 `docs/superpowers/` 아래
스펙·계획으로 남긴다.

## 라이선스 / 제작

Roobicom (루비컴) — https://roobicom.co.kr
