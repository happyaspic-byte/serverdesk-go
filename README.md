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
./serverdesk -c config.local.json
```

- HTTP: `listen`(기본 예시 0.0.0.0:6005) — 콘솔 UI + /api/*
- SNMP 트랩: `trap.port`(10162/udp). EAC 가 보내는 162/udp 를 받으려면 리다이렉트가 필요하다 —
  `serverdesk-net.service`(everrun-poller 리포)가 162→10162 REDIRECT 와 PLC 서브넷 보조 IP를 담당한다.
- systemd: `deploy/serverdesk.service` 참고. 관리 토큰은 gitignore 되는 `serverdesk.local.env` 의
  `SERVERDESK_TOKEN` 으로 주입한다. 토큰이 설정되면 원격 쓰기(장비 추가/삭제, 임계값, 복구)는
  `X-Serverdesk-Token` 헤더가 필요하고, 콘솔은 설정 → 관리 API 토큰에 한 번 입력하면 된다.

## 주요 기능

- 토폴로지(회사→공장→장비 계층도, 팬/줌/핏), 플릿 개요, 클러스터/노드/용량/인시던트 화면
- ztC Endurance 단일 2U 섀시 모델 — CM-A/B 물리 식별자와 Active/Standby 역할 분리, IP 플랜 11개,
  서브시스템 이중화(Compute Active/Standby·Smart Exchange, Storage/I/O/PSU Active/Active)
- 경보 임계값(warn/crit %) 설정 화면 편집 — `PUT /api/admin/thresholds`, 즉시 라이브 반영
- 설정 백업/복구 — `GET /api/admin/config/export`(자격증명 마스킹) / `POST /api/admin/config/import`
  (빈 자격증명은 기존 값 유지, 장비·수집 변경은 재시작 후 적용)
- 가용성 CSV — `GET /api/availability.csv` (30일 실측)
- 대시보드 경보 Quick Ack — 서버 공유 상태(/ack)로 동기화

## 개발

```bash
go vet ./... && go test ./...
```

웹 자산은 embed.FS 라 `web/` 수정 후 재빌드가 필요하다. 설계 문서는 `docs/superpowers/` 아래
스펙·계획으로 남긴다.

## 라이선스 / 제작

Roobicom (루비컴) — https://roobicom.co.kr
