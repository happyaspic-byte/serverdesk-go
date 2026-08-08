# NMS 개선 4종 구현 계획 (임계값/백업·복구/Quick Ack/가용성 CSV)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 외부 리뷰에서 채택한 4개 항목(경보 임계값 설정, 설정 백업/복구, 대시보드 Quick Ack, 가용성 CSV)을 serverdesk-go 에 구현한다.

**Architecture:** 임계값은 config.json 을 정본으로 하고 poller 패키지의 atomic 라이브 홀더로 런타임 반영, 프런트는 /api/devices 루트 필드로 수신. 백업/복구는 config.Store(파일 RMW) + webfront stateFile(ack/maint/notes/escal) 을 하나의 JSON 문서로 묶되 비밀 필드는 마스킹/머지. Quick Ack 는 기존 ackedAlerts + pushAck 인프라 재사용. CSV 는 AvailTracker.Days(30일 파일 영속)에서 생성.

**Tech Stack:** Go 1.26(stdlib-only, 프로젝트 내 .toolchain/go), Vanilla JS ES modules, Playwright(검증).

## Global Constraints

- 이 디렉터리는 **git 저장소가 아니다** — 커밋 단계는 전부 "빌드+테스트 통과" 체크포인트로 대체한다.
- 빌드/테스트: `cd /home/ubuntu/projects/serverdesk-go && export PATH=$PWD/.toolchain/go/bin:$PATH GOTOOLCHAIN=local && go build -o serverdesk ./cmd/serverdesk && go test ./...`
- 웹 자산은 embed.FS — `web/` 수정 후 반드시 재빌드. 재기동: `kill -TERM $(pgrep -x serverdesk); sleep 2; (PATH=/home/ubuntu/bin:$PATH nohup ./serverdesk -c config.local.json > /tmp/serverdesk-shadowNN.log 2>&1 &)`
- 한글 주석/문구는 기존 파일 톤을 따른다. Edit 도구가 한글 바이트 불일치로 실패하면 python3 치환을 쓴다.
- 기존 주석·설계 원칙(파일 상단의 번호 매긴 결정 #NNN, GNN)을 훼손하지 않는다. 새 결정은 같은 관례로 짧게 남긴다.
- **수집 대상 정의(관리 IP/타입/자격증명/노드 구성)는 API 로 바꾸지 않는다**(admin.go 상단 주석의 기존 원칙) — import 는 파일 기록 후 "재시작 필요" 응답으로 처리하고 라이브 장비 뮤테이션을 하지 않는다.
- `internal/topology/status.go` 의 sgWarnPct 85/sgCritPct 95 는 avcli_parse 계약이라 건드리지 않는다. 대상은 78/90 계열 뿐.

---

### Task 1: 임계값 백엔드 (config 필드 + 라이브 홀더 + PUT + /api/devices 노출)

**Files:**
- Modify: `internal/config/config.go` (Thresholds 타입 + Config 필드 + Load 기본값/검증)
- Create: `internal/poller/thresholds.go`
- Modify: `internal/poller/devices.go:782` (리터럴 → 홀더)
- Modify: `internal/httpapi/admin.go` (doPut 라우트 + putThresholds)
- Modify: `internal/httpapi/httpapi.go` (/api/devices 응답에 thresholds)
- Modify: `internal/config/persist.go` (SetSectionValue)
- Modify: `cmd/serverdesk/main.go` (기동 시 SetThresholds)
- Test: `internal/poller/thresholds_test.go`, `internal/config/config_test.go` 에 케이스 추가

**Interfaces:**
- Produces:
  - `poller.SetThresholds(warn, crit float64)` / `poller.UsageThresholds() (warn, crit float64)`
  - `config.Config.Thresholds` (`config.Thresholds{Warn, Crit float64}`, json `"thresholds":{"warn":N,"crit":N}`)
  - `(*config.Store).SetSectionValue(section string, value any) error`
  - `PUT /api/admin/thresholds` body `{"warn":80,"crit":95}` → 200 `{"ok":true,"warn":80,"crit":95}` / 400 사유
  - `/api/devices` 응답 루트 `thresholds: {"warn":N,"crit":N}`

- [x] **Step 1: 실패 테스트 작성**

`internal/poller/thresholds_test.go`:
```go
package poller

import "testing"

func TestUsageThresholdsDefault(t *testing.T) {
	w, c := UsageThresholds()
	if w != 78 || c != 90 {
		t.Fatalf("기본값 78/90 이어야 함, got %v/%v", w, c)
	}
}

func TestSetThresholds(t *testing.T) {
	SetThresholds(80, 95)
	defer SetThresholds(78, 90) // 다른 테스트 오염 방지
	w, c := UsageThresholds()
	if w != 80 || c != 95 {
		t.Fatalf("got %v/%v", w, c)
	}
}
```

`internal/config/config_test.go` 에 추가(기존 헬퍼 재사용):
```go
func TestLoadThresholdsDefaultAndValidation(t *testing.T) {
	// thresholds 키 없음 → 78/90
	// {"thresholds":{"warn":80,"crit":95}} → 80/95
	// {"thresholds":{"warn":95,"crit":80}} → Load 에러
	// {"thresholds":{"warn":80}} (crit 만 없음) → Load 에러(부분 지정 불가)
}
```

- [x] **Step 2: 테스트 실패 확인** — `go test ./internal/poller ./internal/config` → FAIL(심볼 없음)

- [x] **Step 3: 구현**

`internal/poller/thresholds.go` 신규:
```go
package poller

import "sync/atomic"

// 사용률 임계값 라이브 홀더 — PUT /api/admin/thresholds 로 런타임 변경된다.
// config.Config 를 직접 뮤테이션하지 않는다(폴리 워커와 데이터 레이스).
var threshLive atomic.Value // [2]float64{warn, crit}

func init() { threshLive.Store([2]float64{78, 90}) }

// SetThresholds 는 사용률 임계값을 갱신한다(0<warn<crit<=100 검증은 호출자 책임).
func SetThresholds(warn, crit float64) { threshLive.Store([2]float64{warn, crit}) }

// UsageThresholds 는 현재 사용률 임계값(warn, crit %)을 돌려준다.
func UsageThresholds() (float64, float64) {
	v := threshLive.Load().([2]float64)
	return v[0], v[1]
}
```

`internal/config/config.go`:
```go
// Thresholds 는 사용률 경보 임계값(%)이다. 파일에 없으면 78/90 으로 채운다.
// 부분 지정(한쪽만)은 오설정 방지로 에러다.
type Thresholds struct {
	Warn float64 `json:"warn"`
	Crit float64 `json:"crit"`
}
```
- Config 구조체에 `Thresholds Thresholds \`json:"thresholds"\`` 추가.
- Load 의 기본값/검증 구간에:
```go
if c.Thresholds.Warn == 0 && c.Thresholds.Crit == 0 {
	c.Thresholds = Thresholds{Warn: 78, Crit: 90}
} else if !(c.Thresholds.Warn > 0 && c.Thresholds.Warn < c.Thresholds.Crit && c.Thresholds.Crit <= 100) {
	return nil, fmt.Errorf("thresholds: 0 < warn < crit <= 100 이어야 합니다")
}
```

`internal/poller/devices.go:782` 부근:
```go
warn, crit := UsageThresholds()
if p >= crit {
	st = "down"
} else if p >= warn {
	st = "deg"
}
```

`internal/config/persist.go`:
```go
// SetSectionValue 는 최상위 키를 value 로 교체한다(객체/스칼라용 — 배열은 setSectionArray).
func (s *Store) SetSectionValue(section string, value any) error {
	return s.rmw(func(doc map[string]json.RawMessage) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		doc[section] = raw
		return nil
	})
}
```

`internal/httpapi/admin.go` — doPut 의 clusters 프리픽스 검사 **앞**에:
```go
if path == "/api/admin/thresholds" {
	s.putThresholds(w, r)
	return
}
```
핸들러:
```go
// putThresholds 는 PUT /api/admin/thresholds — 사용률 임계값 변경(즉시 라이브 반영).
func (s *Server) putThresholds(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readJSONBody(w, r)
	if !ok {
		return
	}
	num := func(k string) (float64, bool) {
		v, ok := body[k].(float64)
		return v, ok
	}
	warn, wok := num("warn")
	crit, cok := num("crit")
	if !wok || !cok || !(warn > 0 && warn < crit && crit <= 100) {
		s.send(w, r, 400, map[string]any{"error": "0 < warn < crit <= 100 (숫자 %) 이어야 합니다"})
		return
	}
	if err := s.Store.SetSectionValue("thresholds", map[string]any{"warn": warn, "crit": crit}); err != nil {
		s.send(w, r, 500, map[string]any{"error": "설정 저장 실패: " + err.Error()})
		return
	}
	poller.SetThresholds(warn, crit)
	s.send(w, r, 200, map[string]any{"ok": true, "warn": warn, "crit": crit})
}
```

`internal/httpapi/httpapi.go` doGet 의 /api/devices 조립 지점(BuildDevices 호출 직후):
```go
warnT, critT := poller.UsageThresholds()
out["thresholds"] = map[string]any{"warn": warnT, "crit": critT}
```

`cmd/serverdesk/main.go` — cfg 로드 직후(폴리 시작 전):
```go
poller.SetThresholds(cfg.Thresholds.Warn, cfg.Thresholds.Crit)
```

- [x] **Step 4: 테스트+빌드 통과** — `go test ./... && go build -o serverdesk ./cmd/serverdesk`

- [x] **Step 5: 실기 확인** — 재기동 후
  `curl -s -X PUT localhost:6005/api/admin/thresholds -d '{"warn":80,"crit":95}'` → ok,
  `curl -s localhost:6005/api/devices | python3 -c 'import json,sys; print(json.load(sys.stdin)["thresholds"])'` → 80/95,
  config.local.json 에 thresholds 섹션 기록 확인, 다시 78/90 으로 되돌리기.

---

### Task 2: 임계값 프런트 (전달 + 소비자 교체 + 설정 UI)

**Files:**
- Modify: `web/js/model/compute.js` (모듈 임계값 + 4곳 교체: L86-91 barColor, L1269-1270, L1603, L1614-1615)
- Modify: `web/js/model/data.js` (pull() 에 thresholds 통과)
- Modify: `web/js/app.js` (폴 성공 시 setUsageThresholds + setState)
- Modify: `web/js/screens/settings.js` (임계값 카드)
- Modify: `web/css/screens/settings.css` (입력 행 스타일)

**Interfaces:**
- Consumes: Task 1 의 `/api/devices` 루트 `thresholds`, `PUT /api/admin/thresholds`
- Produces: `compute.setUsageThresholds(warn, crit)`, store.state.thresholds `{warn,crit}|null`

- [x] **Step 1: 모델 테스트(실패)** — /tmp/model_test.mjs 에:
```js
C.setUsageThresholds(80, 95);
// barColor(85) 가 'warn' 인지(기본 78/90 에서는 85→warn 이므로 80/95 에서도 warn)
// → 차이가 보이는 값으로: barColor(79) 가 기본은 warn, 80/95 설정 후에는 중립이어야 함
C.setUsageThresholds(78, 90); // 원복
```

- [x] **Step 2: 구현** — compute.js:
```js
// 사용률 임계값 — 서버(config.thresholds)가 정본, /api/devices 폴 때마다 갱신된다.
// 폴 실패/미도달 시 78/90 폴리(종전 하드코딩과 동일).
let USAGE_THRESH = { warn: 78, crit: 90 };
export function setUsageThresholds(warn, crit) {
  const w = Number(warn), c = Number(crit);
  if (w > 0 && w < c && c <= 100) USAGE_THRESH = { warn: w, crit: c };
}
```
barColor 본문의 78/90 → USAGE_THRESH.warn/crit, 나머지 3곳 동일 교체.

data.js pull() 반환 객체에:
```js
thresholds: (j && j.thresholds && typeof j.thresholds.warn === 'number')
  ? { warn: j.thresholds.warn, crit: j.thresholds.crit } : null,
```

app.js — 폴 성공 처리부(devices 반영 직후):
```js
if (res.thresholds) {
  compute.setUsageThresholds(res.thresholds.warn, res.thresholds.crit);
  setState({ thresholds: res.thresholds });
}
```

settings.js — 임계값 카드(기존 카드 관례: cardHead + el('section','card sc-set-card')):
- 입력 2개(warn/crit, type=number, min=1 max=100) + [저장] [기본값으로] 버튼.
- 현재값은 `ctx.store.getState().thresholds` → 없으면 78/90.
- 저장: `fetch('/api/admin/thresholds', {method:'PUT', headers:{'Content-Type':'application/json'}, body: JSON.stringify({warn,crit})})` — 관리 쓰기와 같은 토큰 헤더 관례(manage.js 의 장비 저장 fetch 를 그대로 따른다 — 토큰 헤더 부착 코드 복제).
- 검증 실패 시 입력 아래 한글 사유 표시, 0<warn<crit≤100 아니면 저장 버튼 비활성.

- [x] **Step 3: 빌드+재기동+브라우저 확인** — 설정 화면에서 80/95 저장 → /api/devices thresholds 확인 → 개요/상세 바 색 반영(Playwright) → 78/90 원복.

---

### Task 3: 백업 export (GET /api/admin/config/export)

**Files:**
- Modify: `internal/config/persist.go` (ReadDoc)
- Create: `internal/httpapi/backup.go` (export/import 핸들러 + 마스킹/머지 로직)
- Modify: `internal/httpapi/httpapi.go` (doGet 라우트)
- Modify: `internal/webfront/state.go` (ExportUIState/ImportUIState)
- Test: `internal/httpapi/backup_test.go`

**Interfaces:**
- Produces:
  - `(*config.Store).ReadDoc() (map[string]json.RawMessage, error)`
  - `(*webfront.Server).ExportUIState() map[string]any` — `{"ack":...,"maint":...,"notes":...,"escal":...}`
  - `(*webfront.Server).ImportUIState(m map[string]any) error` — 위 4키만 받고 나머지 키는 무시
  - `GET /api/admin/config/export` → 200 attachment `serverdesk-backup-<YYYYMMDD>.json`
    `{"schema":"serverdesk-config/1","exported_at":"RFC3339","config":{...마스킹},"ui":{...}}`

- [x] **Step 1: 실패 테스트** — backup_test.go:
```go
// TestRedactSecrets: {"a":{"password":"x","name":"n"}} → password ""·name 보존
// TestExportShape: schema/exported_at/config/ui 4키 존재
```
마스킹 패턴(대소문자 무시, 부분문자열): `password|passwd|secret|token|community|api_key|private_key`

- [x] **Step 2: 구현**
- persist.go ReadDoc: rmw 의 읽기 부분만 분리한 읽기 전용 메서드.
- state.go:
```go
// ExportUIState 는 콘솔 공유 상태(백업용)다. 웹훅 URL 은 브라우저 localStorage 에만
// 있어 서버 상태가 아니라 제외다.
func (s *Server) ExportUIState() map[string]any {
	return map[string]any{
		"ack": s.ack.read(), "maint": s.maint.read(),
		"notes": s.notes.read(), "escal": s.escal.read(),
	}
}

// ImportUIState 는 ExportUIState 의 4키만 받아 교체한다(알 수 없는 키 무시).
func (s *Server) ImportUIState(m map[string]any) error {
	targets := map[string]*stateFile{"ack": s.ack, "maint": s.maint, "notes": s.notes, "escal": s.escal}
	for k, sf := range targets {
		v, ok := m[k]
		if !ok {
			continue
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("ui.%s 가 객체가 아닙니다", k)
		}
		if _, err := sf.update(func(cur map[string]any) map[string]any { return obj }); err != nil {
			return err
		}
	}
	return nil
}
```
- backup.go: `redactSecrets(v any) any`(재귀), `handleExport` — s.Store.ReadDoc → map 변환 → redact → ui 병합 → Content-Disposition. **민감 읽기라 writeGate 적용**.

- [x] **Step 3: 테스트+빌드 통과**

- [x] **Step 4: 실기 확인** — `curl -s localhost:6005/api/admin/config/export -o /tmp/bk.json` → 비밀 필드 "" 임을 python 으로 검사(실 config 의 snmp community 가 평문 노출되지 않는지).

---

### Task 4: 복구 import (POST /api/admin/config/import)

**Files:**
- Modify: `internal/httpapi/backup.go` (handleImport)
- Modify: `internal/httpapi/httpapi.go` (doPost 라우트 위임 — admin.go doPost 에 프리픽스 추가)
- Modify: `internal/config/persist.go` (ReplaceDoc)
- Test: `internal/httpapi/backup_test.go` 에 머지 케이스 추가

**Interfaces:**
- Consumes: Task 3 의 ReadDoc/ExportUIState/ImportUIState, Task 1 의 SetSectionValue 계열
- Produces:
  - `(*config.Store).ReplaceDoc(doc map[string]json.RawMessage) error` (rmw 로 전량 교체)
  - `POST /api/admin/config/import` body=export 문서 → 200 `{"ok":true,"restart_required":[...],"ui_applied":true}` / 400 사유
  - 규칙: 장비·수집 관련 섹션(clusters/edge_devices/intervals/listen/trap/sim_devices 등)이
    현재 파일과 다륵면 restart_required 에 나열(파일만 바뀌고 라이브 반영은 안 함 — 기존 원칙).
    thresholds 는 즉시 poller.SetThresholds 로 라이브 반영.

- [x] **Step 1: 실패 테스트**
```go
// TestImportMergeSecrets: 신 문서의 password:"" + 구 문서 password:"realpw" → 결과 "realpw"
// TestImportRejectsBadSchema: schema != "serverdesk-config/1" → 에러
// TestImportRejectsBadEntry: clusters 배열 요소에 key 없음 → 에러(인덱스 명시)
```

- [x] **Step 2: 구현** — handleImport 순서:
  1. readJSONBody(크기 상한) → schema 검사.
  2. config 섹션 구조 검증: clusters/edge_devices 가 있으면 배열+각 요소 객체+key 문자열.
  3. 비밀 머지: 신 문서를 재귀 순회하며 마스킹 패턴 키의 값이 "" 이면 구 문서 동일 경로 값으로 대체(경로: 섹션→key 항목 매칭; 매칭 없으면 "" 유지).
  4. ReplaceDoc 기록.
  5. thresholds 있으면 검증 후 SetThresholds(라이브).
  6. ui 있으면 ImportUIState.
  7. restart_required 계산: 바뀐 최상위 섹션 중 [clusters edge_devices nodes intervals listen trap sim_devices cors] ∩ 변경분.

- [x] **Step 3: 테스트+빌드 통과**

- [x] **Step 4: 실기 왕복 확인** — export 한 파일을 그대로 import → 200 + restart_required 목록. config.local.json 이 무손실(비밀 보존)임을 diff 로 확인. **주의: 검증 후 반드시 원본과 동일한지 확인 — 운영 config 훼손 금지.**

---

### Task 5: 설정 화면 백업/복구 카드

**Files:**
- Modify: `web/js/screens/settings.js` (카드 추가 — 임계값 카드 아래)
- Modify: `web/css/screens/settings.css`

**Interfaces:**
- Consumes: Task 3/4 엔드포인트, Task 7 의 `/api/availability.csv`
- Produces: 없음(화면 전용)

- [x] **Step 1: 구현** — "백업 / 복구" 카드:
  - [설정 JSON 다운로드]: `<a href="/api/admin/config/export" download>` 버튼 스타일.
  - [가용성 CSV 다운로드]: `<a href="/api/availability.csv" download>`(Task 7 이 만든다 — 없으면 404 라 Task 7 완료 전엔 비활성 표기).
  - [복구 파일 선택...]: `<input type="file" accept="application/json">` → 내용 읽기 → `confirm('현재 설정을 파일 내용으로 덮어씁니다. 계속할까요?')` → POST import → 결과 텍스트(성공 시 restart_required 가 있으면 '재시작 필요: ...' 한글 안내).
  - 에러는 기존 설정 화면의 인라인 에러 관례.

- [x] **Step 2: 빌드+재기동+브라우저 확인(Playwright)** — 카드 렌더, export 링크 응답, export→import 왕복, 결과 메시지.

---

### Task 6: 대시보드 Quick Ack

**Files:**
- Modify: `web/js/screens/overview.js` (renderAlerts 행 구조 + ack 버튼 + 키보드)
- Modify: `web/css/screens/overview.css` (버튼 스타일)

**Interfaces:**
- Consumes: 기존 `ctx.store` ackedAlerts + app.js pushAck 구독(app.js:605-607)
- Produces: 없음(화면 전용)

- [x] **Step 1: 구현** — renderAlerts(overview.js:685-725):
  - 행 루트를 `<button>` → `<div class="sc-ov-alert" role="button" tabindex="0">` 로 변경
    (네이티브 button 안에 ack button 을 중첩할 수 없기 때문). 기존 data-ov-dev 클릭 위임은 그대로 동작.
  - 키보드: overview 의 로컬 위임에 `[data-ov-dev]` Enter/Space → 클릭과 동일 동작 추가.
  - ack 버튼: 행 우측(ago 앞) `<button type="button" class="sc-ov-alert-ack">확인</button>`.
    patch 함수에서 `show(ackBtn, !a.acked)`. 클릭 핸들러:
```js
ackBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  const key = ackBtn.dataset.ackKey || '';
  if (!key) return;
  ctx.store.setState((s) => {
    const next = Object.assign({}, s.ackedAlerts);
    next[key] = Date.now(); // ← incidents.js:196-205 의 값 형태를 그대로 따른다(구현 시 원본 확인)
    return { ackedAlerts: next };
  });
});
```
  - patch 에서 `ackBtn.dataset.ackKey = a.ackKey || ''`.
  - CSS: .sc-ov-alert-ack — btn--sm 크기, hover 만 노출이 아니라 상시 노출(관제 즉결용).

- [x] **Step 2: 빌드+재기동+브라우저 확인** — 활성 경보가 있으면: [확인] 클릭 → 행 is-acked 전환 + 상세 이동 안 함 + /ack GET 에 반영. 활성 경보가 없으면: thresholds 를 일시적으로 낮춰(예: warn 5/crit 10) 스토리지 deg → 주의/경보 유발 시도 후 확인, 종료 후 78/90 원복. 그래도 경보 행이 안 생기면 DOM 단위 검증으로 대체하고 보고에 명시.

---

### Task 7: 가용성 CSV (GET /api/availability.csv)

**Files:**
- Modify: `internal/poller/avail.go` (CSVSnapshot)
- Modify: `internal/httpapi/httpapi.go` (doGet 라우트 + 핸들러)
- Test: `internal/poller/avail_test.go` 에 케이스 추가(기존 파일 있으면)

**Interfaces:**
- Produces:
  - `poller.AvailCSVRow{Day, Device string; Avail, ObservedSec float64}`
  - `(*poller.AvailTracker).CSVSnapshot() []AvailCSVRow` — 30일 창, 날짜 오름차순→장비 id 순
  - `GET /api/availability.csv` → 200 `text/csv; charset=utf-8`, attachment `availability-<YYYYMMDD>.csv`
    헤더 `date,device,availability_pct,observed_sec`

- [x] **Step 1: 실패 테스트** — Days 2일×2장비 픽스처 → 행 수/정렬/Avail 계산(100*(1-down/tot), round3 재사용).

- [x] **Step 2: 구현** — avail.go:
```go
// AvailCSVRow 는 가용성 CSV 의 한 행이다.
type AvailCSVRow struct {
	Day, Device string
	Avail       float64
	ObservedSec float64
}

// CSVSnapshot 은 30일 창의 (일자×장비) 실측 가용성을 돌려준다(날짜→장비 id 순).
// 관측 10분 미만 셀은 Apply 와 같은 이유(신뢰 불가)로 제외한다.
func (t *AvailTracker) CSVSnapshot() []AvailCSVRow {
	cut := kstDay(nowFloat() - availWindowDays*86400)
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []AvailCSVRow
	for id, rec := range t.state {
		for day, cell := range rec.Days {
			if day < cut || len(cell) < 2 || cell[0] < availMinObsSec {
				continue
			}
			out = append(out, AvailCSVRow{Day: day, Device: id,
				Avail: round3(100.0 * (1.0 - cell[1]/cell[0])), ObservedSec: cell[0]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		return out[i].Device < out[j].Device
	})
	return out
}
```
- httpapi.go doGet: `case "/api/availability.csv":` → encoding/csv 로 작성, `w.Header().Set("Content-Disposition", ...)`. 읽기지만 운영 데이터라 writeGate 와 같은 게이트 적용.

- [x] **Step 3: 테스트+빌드 통과 + 실기 확인** — `curl -s localhost:6005/api/availability.csv | head -5` 헤더+행 형식.

---

### Task 8: 최종 검증

**Files:** 없음(검증 전용)

- [x] **Step 1:** `go vet ./... && go test ./...` 전부 통과
- [x] **Step 2:** 재기동 후 Playwright 종합: 설정 임계값 변경→/api/devices 반영→바 색 변경→원복 / export 다운로드 내용 검사(비밀 마스킹) / import 왕복 / 백업 카드 렌더 / Quick Ack(가능하면 실클릭) / CSV 다운로드
- [x] **Step 3:** 모바일(390px) 설정 화면 가로 스크롤·겹침 없음 확인
- [x] **Step 4:** 재기동 후에도 thresholds·import 한 설정 유지 확인, config.local.json 무결성 최종 diff
