# NMS 개선 4종 설계 — 임계값 설정 / 백업·복구 / Quick Ack / 가용성 CSV

날짜: 2026-08-08 · 대상: /home/ubuntu/projects/serverdesk-go · 상태: 승인됨(auto)

## 배경

외부 코드 리뷰의 "맞는 지적"만 채택했다. 제외한 것: 멀티사이트 필터·정기점검 스케줄·
TSDB·SMTP(현 규모에 과함), 로그 tail 제거(이미 기본 접힘)·About 카드 축소(사용자
요구사항)·sim 빌드 분리(config 게이트로 충분).

## 1. 경보 임계값 사용자 설정

### 현재
- warn 78 / crit 90 하드코딩: 프런트 `web/js/model/compute.js`(barColor L86-91,
  L1269-1270, L1603, L1614-1615) + 백엔드 `internal/poller/devices.go` L782-784
  (스토리지 그룹 pct → deg/down).
- `internal/topology/status.go`의 sgWarnPct 85 / sgCritPct 95 는 avcli_parse 와의
  색칠 계약이라 **본 작업 범위 밖**(그대로 유지).

### 설계
- `internal/config/config.go`: `Thresholds struct { Warn float64; Crit float64 }`
  `json:"thresholds"`, 기본값 78/90(파일에 없으면 기본값 — 기존 동작과 무손실).
  유효성: `0 < warn < crit <= 100`.
- 쓰기: `PUT /api/admin/thresholds` (admin.go, 기존 writeGate 적용).
  검증 후 config.Store.rmw 로 파일 기록 + 폴리가 읽는 라이브 홀더 갱신.
  라이브 홀더: `internal/poller` 에 `atomic.Value` 기반 SetThresholds/GetThresholds
  (devices.go 판정이 읽음) — Cfg 구조체 직접 뮤테이션은 데이터 레이스라 금지.
- 읽기 전달: `/api/devices` 응답 루트에 `thresholds: {warn, crit}` 추가.
  프런트 `data.js`가 state 에 싣고, compute.js 의 78/90 리터럴을 state 값
  (없으면 78/90 폴리)로 교체.
- 설정 UI: settings.js 에 임계값 카드 — warn/crit 숫자 입력 2개 + 저장/되돌리기.
  저장 성공 시 다음 폴(3초)부터 전역 반영.

## 2. 설정 백업 / 복구

### 현재
- 백업/복구 없음. config.local.json 은 자격증명 평문(secrets.go 는 마스킹
  레지스트리일 뿐 암호화 아님) → 원본 export 는 자격증명 유출.

### 설계
- `GET /api/admin/config/export` (writeGate 아닌 읽기지만 자격증명 포함 경로라
  GateWrite 와 같은 기준 적용):
  ```json
  {
    "schema": "serverdesk-config/1",
    "exported_at": "...",
    "config": { ...clusters/edge_devices/intervals/thresholds..., "비밀 필드": "" },
    "ui": { "ack": ..., "maint": ..., "notes": ..., "escal": ..., "notify": ... }
  }
  ```
  비밀 필드는 필드명 패턴으로 판별한다(대소문자 무시): password, passwd, secret,
  token, community, api_key, private_key 를 포함하는 키 → 값을 빈 문자열로 마스킹.
  ui 는 webfront stateFile 들(/ack /maint /notes /escal + notify 설정).
- `POST /api/admin/config/import` (writeGate): schema 검증 → 항목 형식 검증
  (config.Load 의 검증 재사용) → 비밀 필드가 빈 값이면 기존 값 유지 머지 →
  Store 기록 → Edge 라이브 반영(Add/Remove/PatchMeta 재사용) → thresholds 는
  라이브 홀더 즉시 반영. listen 포트·trap 포트·sim_devices 는 재시작 필요라고
  응답의 `restart_required: [...]` 에 명시.
- UI: 설정 "백업 / 복구" 카드 — [설정 JSON 다운로드] [가용성 CSV] [파일 선택 후 복구]
  + 복구는 확인 다이얼로그(덮어쓰기 경고) 경유.

## 3. 대시보드 Quick Ack

### 현재
- ack 인프라 존재: store.ackedAlerts + app.js 구독이 pushAck 로 서버 동기화
  (app.js:605-607). incidents.js 에 행별 확인 버튼 있음(L189-205).
  overview 의 실시간 경보 행은 클릭 시 상세 이동만 가능.

### 설계
- overview.js 경보 행 우측에 [확인] 버튼(미확인 행만). 클릭 시
  incidents.js:196-205 와 같은 store 뮤테이션(ackKey 병합) → 서버 동기화는
  기존 구독이 처리. 버튼 클릭이 행 네비게이션을 타지 않게 stopPropagation.
- 이미 acked 인 행(is-acked)에는 버튼 없음.

## 4. 가용성 CSV 다운로드

- `GET /api/availability.csv` — poller.AvailTracker.Days(날짜별 샘플,
  availability.json, 30일 보존)를 CSV 로: `date,device,availability_pct`.
- 설정의 백업/복구 카드에 다운로드 링크.

## 오류 처리

- thresholds: 검증 실패 400 + 한글 사유. 프런트 입력값 이상이면 저장 버튼 비활성.
- import: schema 불일치 400, 항목 검증 실패 시 어떤 항목이 문제인지 인덱스 명시.
  부분 적용 없음(전부 검증 통과 후에만 기록).
- CSV: 관측 데이터 없으면 헤더만 Y(200).

## 테스트

- Go: thresholds 유효성(0<w<c≤100, 기본값), export 마스킹(비밀 "" + 나머지 보존),
  import 머지(빈 비밀=기존 유지, 신규 항목 추가, schema 거부), CSV 형식.
  기존 `go test ./...` 8 패키지 무회귀.
- 브라우저(Playwright): 설정에서 임계값 78→80 변경 후 대시보드 바 색 반영,
  export JSON 다운로드 내용, import 후 장비 목록 동일, 개요 [확인] 버튼 클릭 시
  행이 is-acked 로 전환 + 상세 이동 안 함, CSV 다운로드.
- 수동: 재기동 후에도 thresholds/가져온 설정 유지 확인.
