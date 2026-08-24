# Production readiness

평가일: 2026-08-24

대상: production-hardening 변경이 적용된 `serverdesk-go`
판정: **코드 준비도 96/100 — 목표(95점 초과) 달성**

이 점수는 저장소 안에서 재현 가능한 코드·테스트·배포 자산의 준비도다. 실제 고객 환경의 운영 배포
인증은 아래 현장 UAT가 끝날 때까지 조건부다.

## 점수 근거

| 영역 | 배점 | 점수 | 근거 |
|---|---:|---:|---|
| 전송·자격증명·웹 보안 | 25 | 25 | loopback-only HTTP 기본값, 직접 TLS 사전 검증, 장비 SPKI 피닝, secret reference 강제, 안전한 migration, CSRF·세션·mutation 감사 |
| 신뢰성·실패 안전성 | 20 | 19 | action capability fail-closed, 응답 크기·redirect 제한, 원자적 설정 교체, transactional installer와 rollback |
| 자동화 테스트·정적 품질 | 20 | 19 | 전체 race 통과, Go statement coverage 80.5%, `go vet`, JavaScript 24/24, Linux·Windows cross-build |
| 설치·업데이트·제거 | 15 | 14 | systemd capability 분리, 네트워크 자원 소유권 state, health URL 제한, rootless 배포 계약 검증 |
| UI·운영 안전성 | 10 | 9 | 미지원 action 숨김/차단, 관리자 health, 보안 이벤트 감사; 실제 브라우저·실장비 E2E는 UAT 필요 |
| 문서·공급망·릴리스 | 10 | 10 | SHA-pinned Actions, signed annotated tag gate, govulncheck, 재현 빌드 이중 비교, SBOM·checksum·provenance·NOTICE·라이선스 |
| **합계** | **100** | **96** | **95점 초과** |

## 재현된 품질 게이트

```text
Go toolchain                  go1.26.6
go test ./...                 PASS
go vet ./...                  PASS
go test -race ./...           PASS
Go statement coverage        80.5% (gate: >=80.0%)
node --test web/tests/        24/24 PASS
Linux amd64 build             PASS
Windows amd64 build           PASS
deployment asset validation  PASS
GitHub Actions YAML/pins      PASS
govulncheck                   No vulnerabilities found
git diff --check              PASS
```

CI와 tag release는 같은 race·coverage·JavaScript·배포·취약점 게이트를 다시 실행한다. Release는
서명되고 검증된 semver annotated tag만 허용하고, 플랫폼별 payload를 두 번 빌드해 byte-for-byte
동일성을 검사한 후 SBOM, SHA-256 checksum, GitHub build provenance를 게시한다.

## 운영 배포 전 필수 UAT

- 지원 Linux 배포판에서 신규 설치, in-place update, health 실패 rollback, 일반/`--full` 제거를 실제
  systemd로 검증한다.
- Windows에서 installer/update/uninstall, machine-scoped DPAPI, ACL, Scheduled Task를 실제 호스트로
  검증한다.
- Stratus everRun/ztC, Proxmox, Redfish, SNMP/프린터의 인증서·credential rotation·장애 복구를 실장비로
  검증한다.
- Chromium 계열 브라우저에서 로그인, action 비활성화, detail/topology 렌더링, CSRF 실패 UI를 DOM
  E2E로 검증한다.

UAT 중 credential 노출, TLS 검증 우회, rollback 실패, 미지원 destructive action 실행이 한 건이라도
발견되면 배포를 중단하고 이 점수를 재평가한다.
