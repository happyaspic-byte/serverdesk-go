# Production readiness

평가일: 2026-08-24

대상: production-hardening 변경이 적용된 `serverdesk-go`
판정: **코드 준비도 96/100 — 목표(95점 초과) 달성**

이 점수는 저장소 안에서 재현 가능한 코드·테스트·배포 자산의 준비도다. 실제 고객 환경의 운영 배포
인증은 아래 현장 UAT가 끝날 때까지 조건부다. **현재 제품 출시 판정은 NO-GO**다. 특히 Windows는
`SYSTEM` Scheduled Task가 제거되기 전까지 자동 NO-GO이며, Linux도 실제 systemd·Stratus·브라우저
증거가 PASS가 되기 전에는 판매 배포를 승인하지 않는다.

## 점수 근거

| 영역 | 배점 | 점수 | 근거 |
|---|---:|---:|---|
| 전송·자격증명·웹 보안 | 25 | 25 | loopback-only HTTP, TLS/SPKI 검증, secret reference, CSRF·세션, DNS rebinding/private-address 차단, 사유가 있는 구조화 감사 |
| 신뢰성·실패 안전성 | 20 | 19 | 수집원별 격리, false recovery 차단, 재시작 안전 알림 큐, corrupt state fail-closed, CAS 설정·감사 rollback |
| 자동화 테스트·정적 품질 | 20 | 19 | shuffle+race 통과, Go statement coverage 81.0%, `go vet`, JavaScript 44/44, 실제 바이너리 E2E, Linux·Windows cross-build |
| 설치·업데이트·제거 | 15 | 14 | systemd capability 분리, 자원 소유권 state, 검증된 rollback, rootless payload 계약; Windows 최소권한 전환은 미완료 |
| UI·운영 안전성 | 10 | 9 | 초기 CONNECTING, LIVE/STALE/OFFLINE 진실성, durable write-first outbox, typed/reason 확인; 실제 브라우저·스크린리더 UAT 필요 |
| 문서·공급망·릴리스 | 10 | 10 | SHA-pinned Actions, signed annotated tag gate, govulncheck, 재현 빌드 이중 비교, SBOM·checksum·provenance·NOTICE·라이선스 |
| **합계** | **100** | **96** | **95점 초과** |

## 재현된 품질 게이트

```text
Go toolchain                                      go1.26.6
gofmt + go vet                                    PASS
go test -shuffle=on -race -covermode=atomic ./... PASS
Go statement coverage                            81.0% (gate: >=80.0%)
node --test web/tests/*.test.mjs                  44/44 PASS
JavaScript syntax check                           PASS
Linux amd64 build                                 PASS (clean Git clone + VCS metadata)
Windows amd64 build + test compilation            PASS (clean Git clone + VCS metadata)
deployment/release payload validation             PASS
workflow exact-SHA and repository secret hygiene PASS
govulncheck                                       No vulnerabilities found
git diff --check                                  PASS
```

CI와 tag release는 같은 race·coverage·JavaScript·배포·취약점 게이트를 다시 실행한다. Release는
서명되고 검증된 semver annotated tag만 허용하고, 플랫폼별 payload를 두 번 빌드해 byte-for-byte
동일성을 검사한 후 SBOM, SHA-256 checksum, GitHub build provenance를 게시한다.

## 운영 배포 전 필수 UAT

- 지원 Linux 배포판에서 신규 설치, in-place update, health 실패 rollback, 일반/`--full` 제거를 실제
  systemd로 검증한다.
- Windows에서 installer/update/uninstall, machine-scoped DPAPI, ACL, Scheduled Task를 실제 호스트로
  검증한다. 그 전에 Program Files/ProgramData 경로 분리와 LocalService/전용 최소권한 계정 전환을
  완료해야 한다.
- Stratus everRun/ztC, Proxmox, Redfish, SNMP/프린터의 인증서·credential rotation·장애 복구를 실장비로
  검증한다.
- Chromium 계열 브라우저에서 로그인, action 비활성화, detail/topology 렌더링, CSRF 실패 UI를 DOM
  E2E로 검증하고, NVDA/VoiceOver 키보드·스크린리더 UAT를 완료한다.
- 구조화된 감사 이벤트는 현재 `events.jsonl` 최근 500건 범위다. 외부 포워더는 설정에서 명시적으로 활성화한
  Syslog UDP/TCP만 운영 경로에 연결되며, queue depth/sent/errors/dropped/last error는 인증된
  `/api/admin/health`의 `event_store.forwarder`에 노출된다. 포워더 오류·drop은 수집을 중단하지 않고
  상세 health를 degraded로 만든다. Webhook 포워더와 고객의 장기·불변 규제 보존 요건은 별도 구현·보존
  시험 없이는 지원한다고 표시하지 않는다.

UAT 중 credential 노출, TLS 검증 우회, rollback 실패, 미지원 destructive action 실행이 한 건이라도
발견되면 배포를 중단하고 이 점수를 재평가한다.
