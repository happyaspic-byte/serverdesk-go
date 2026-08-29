# Production readiness

평가일: **2026-10-13 (사용자 로컬 날짜)**. 실행 로그의 UTC 날짜는 2026-10-12일 수 있다.

대상: `3008e31` 기반의 **미커밋 working tree**, remediation Tasks 1–6 통합 후 문서 수정 전 검증.
이 결과는 새 committed/released version, 실제 릴리스 archive 또는 고객 환경의 인증이 아니다.
과거 수치형 준비도 평가는 근거가 충분하지 않아 철회한다. **제품 출시 판정은 NO-GO**다.
코드 회귀 검증과 운영 배포 승인을 구분하며, 최종 whole-diff review와 문서 후 통합 게이트는 별도다.

## 측정된 검증 결과

근거는 workspace의 `.superpowers/sdd/serverdesk-remediation-20261012-plan/` 안에 있다.
`final-code-validation.log`와 `validation/`가 Task 6 최종 수정 후 결과다.
`pre-doc-final-validation.log`와 `validation-before-docs/`는 그 이전 결과이므로 최신 결과를 우선한다.
이 로컬 로그는 고객에게 배포된 artifact의 UAT 증거가 아니다.

| 검사 | 현재 관측 결과 | 범위 / 한계 |
|---|---|---|
| Go toolchain | `go1.26.9` | `go.mod` 기준 |
| gofmt / `go vet ./...` / `git diff --check` | PASS | 문서 수정 전 코드 검증 |
| `go test -count=1 -shuffle=on -race -covermode=atomic` | 15 packages PASS | 로컬 fixture/loopback; 실장비 미접촉 |
| 전체 statement coverage | **83.1%** (gate ≥80%) | edge 91.2%, alerting 85.6%, poller 81.2%; 제품 인증 점수가 아님 |
| `node --test web/tests/*.test.mjs` | **61/61 PASS**, skipped 0 | JS 계약 테스트 |
| Linux/Windows amd64 build, Windows test compile | PASS | Linux 호스트의 cross-build/compile; Windows 실행 아님 |
| Linux/Windows target `govulncheck` | **No vulnerabilities found** | 해당 target/source scan; 외부 vendor 구성요소 인증 아님 |
| 배포 validator | **15 mock tests + rootless validation PASS** | 기존 host XFS unit의 `CPUAccounting=` 경고 2건; 실제 서비스/네트워크 변경 없음 |
| 실제 Chromium 로컬 fixture smoke | PASS | NAS 표시 메타 수정, login/logout/requery, 기존 필드·secret reference 보존, console warning/error 0; 실장비·전체 UI UAT 아님 |
| ShellCheck / actionlint / 로컬 PowerShell parser | **NOT_TESTED** | 도구 미설치; 과거 CI PowerShell parse PASS는 현재 변경의 실행 검증이 아님 |
| 실제 release workflow / archive-bound UAT | **NOT_TESTED** | 이 working tree에서 release/publication을 실행하지 않음 |

## 구현된 계약과 호환성 경계

- `PUT /api/clusters/<key>`는 표시 메타만 수정한다: `label`, `company`, `factory`, `site`,
  `asset_tag`, `floor_pos`, 그리고 Edge의 `vendor`. IP/type/port/credential 등 운영 필드를 포함한
  기존 full-form 요청은 **400**이며 일부 메타만 저장하지 않는다. 응답 `applied`만 UI에 반영한다.
  PLC add/connection test의 명시적 `fins_port`는 정수 1–65535, 생략 시 9600이다.
- NAS 상세 health가 누락/null/미인식이면 정상으로 추정하지 않는다. 실제로 비어 있는 미발견
  inventory도 보수적으로 stale이다. 기존 fault는 명시적 정상 관측 전까지 보존된다.
  PLC의 동적 CIP fault/IO/link는 성공한 FINS round마다 다시 관측하며, 불확실한 응답은 정상 증거가 아니다.
- SNMP 응답의 sender/request/envelope/OID와 CIP framing을 검증한다.
  FINS/CIP transport는 parent deadline과 socket cancellation을 적용한다.
  유효한 Counter64가 `MaxInt64`보다 크면 GET은 raw bytes/non-numeric, trap은 hex로 보존한다.
  음수나 지원되는 수치로 변환하지 않는다.
- Availability는 연속된, freshness/readiness 검사를 통과한 관측 사이만 누적한다. Edge freshness는
  장비별 timestamp가 아닌 완료된 round의 시각·오류에 기반한 보수적 판정이라는 한계가 있다.
  최초/재개/unknown/긴 공백은
  downtime도 uptime도 추정하지 않는다. 기존 저장 이력을 보존하지만 과거의 과대 집계를 자동 교정하지
  않는다. CSV/비율은 **운영 추정치**, SLA 증명이 아니다. 인증된 `/api/admin/health`는 availability
  진행·저장 오류·busy/stale을 노출하고 공개 `/api/health`의 최소 3필드 계약은 유지한다.
- Fleet와 topology의 생성 시각을 분리한다. 이전 full graph의 fallback은 이전 생성 시각을 유지하고,
  partial flat topology는 stale/error로 표시한다. 새 fleet 시각이 오래된 topology를 신선하게 만들지 않는다.
- Webhook은 restart-safe **at-least-once**다. concurrency slot 확보 후 최신 관측 maintenance/ACK/source
  상태를 다시 확인하고 durable claim 후 전송하지만, 외부 상태와 원자적 transaction은 아니다.
  이미 시작한 send를 취소하거나 remote acceptance 후 crash의 중복을 제거한다는 보장은 없다.
  활성 condition의 dedupe는 유지하며 inactive retention은 해소 시점부터 시작한다.
  상세 retry/legacy JSON 계약은 [NOTIFICATIONS.md](NOTIFICATIONS.md)를 따른다.
- Shutdown의 공통 20초 예산은 HTTP drain/worker join/최종 persistence의 **대기**를 제한한다.
  이미 시작한 OS filesystem I/O·fsync를 취소하거나 그 시간 안의 디스크 내구성을 보장하지 않는다.
- Trap community는 수신 filter의 일시적 값이며 새 저장/표시·복원된 이력에서 제거한다.
  기존 디스크 파일은 성공적인 rewrite 전까지 비밀을 포함할 수 있다. Unix trap path는 canonical,
  symlink/hardlink 없는 service-owned file와 private parent가 필요하며 unsafe path를 chmod해서
  자동 수리하지 않는다. Windows 검사는 **구조만** 확인하며 ACL 인증은 아니다.
  [DEPLOYMENT.md](DEPLOYMENT.md)의 legacy trap 이관 절차를 따른다.
- Linux install/update/uninstall은 root-only 단일 `flock` transaction이다. 검증된 package의
  root-owned/non-writable ancestor staging이 필요하다. Network ownership은
  `/var/lib/serverdesk-net/applied.env`에 보존하고 legacy `/run` journal은 old unit stop 전에 이관한다.
  삭제/조회 실패나 확인되지 않은 잔여 자원은 성공으로 처리하지 않는다.

## 출시·배포 전 미완료 게이트

| 게이트 | 판정 | 필요한 증거 |
|---|---|---|
| Windows 최소권한/ACL/DPAPI/install/update/rollback/uninstall | **BLOCKED / NOT_TESTED** | 현재 `SYSTEM` Scheduled Task 제거, Program Files/ProgramData 분리, LocalService/전용 계정, 실제 Windows Server 검증 |
| Stratus everRun/ztC, Proxmox, Redfish, NAS, PLC, SNMP/프린터 | **NOT_TESTED** | 실장비 인증서·credential rotation·장애/복구·지원 조합 UAT |
| 실제 Linux systemd 설치·업데이트·실패 rollback·제거 | **NOT_TESTED** | 지원 배포판에서 transaction·권한·복구 확인 |
| 실제 network ownership migration/rollback/cleanup | **NOT_TESTED** | legacy/persistent journal, 재부팅, 부분 삭제 실패, 외부 자원 비인수 검증 |
| Restore / 72시간 endurance / capacity | **NOT_TESTED** | 승인된 환경·규모에서 복원·연속운영·부하 증거 |
| 전체 브라우저/키보드/스크린리더 UAT | **NOT_TESTED** | 로컬 NAS smoke 밖의 화면·CSRF 실패·NVDA/VoiceOver 검증 |
| Stable artifact promotion | **BLOCKED** | preflight에서 승인 environment 0개; 보호된 독립 승인, 정확한 candidate/provenance identity, archive-bound UAT를 별도 승인 후 구축 |

Stable tag는 **candidate만** 생성한다. 승인 workflow가 이미 존재하거나 자동 stable publication이
가능하다는 뜻이 아니다. 현재 workflow의 두 번 비교는 **동일 staged payload의 archive determinism**이며,
독립된 compiler rebuild의 재현성 증명이 아니다. [RELEASE.md](RELEASE.md)를 따른다.

구조화된 감사 `events.jsonl`은 최근 500건 범위다. 외부 포워더는 설정에서 명시적으로 활성화한
Syslog UDP/TCP만 운영 경로에 연결되며, queue depth/sent/errors/dropped/last error는 인증된
`/api/admin/health`의 `event_store.forwarder`에 노출된다. 포워더 오류·drop은 수집을 중단하지 않고
상세 health를 degraded로 만든다. Webhook 포워더와 고객의 장기·불변 규제 보존 요건은 별도 구현·보존
시험 없이는 지원한다고 표시하지 않는다. Credential 노출, TLS 우회, rollback 실패,
미지원 destructive action이 발견되면 배포를 중단한다. 지원/승인 기준은
[SUPPORT-MATRIX.md](SUPPORT-MATRIX.md), [COMMERCIAL-UAT.md](COMMERCIAL-UAT.md)를 따른다.
