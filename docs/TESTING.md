# serverdesk-go 테스트 가이드

`serverdesk-go` 프로젝트의 테스트 체계, 실행 방법, CI 게이트 규칙 및 테스트 작성 관례를 기술한다.

---

## 1. 테스트 실행 방법

### 1.1 사전 환경 설정

`go.mod`의 Go 1.26.9 툴체인을 사용한다. 저장소나 고객 패키지에 툴체인을 번들하지 않는다.

```bash
cd /path/to/serverdesk-go
export GOTOOLCHAIN=go1.26.9
go version
```

`go.mod`에 지정된 Go 버전을 사용한다. 저장소나 고객 패키지에 별도 툴체인을 번들하지 않는다.

### 1.2 Go 백엔드 테스트

전체 패키지 대상 레이스 컨디션 감지 테스트:

```bash
go test -race ./...
```

특정 패키지 또는 서브패키지만 실행:

```bash
# 특정 패키지 실행
go test -race ./internal/httpapi/...
go test -race ./internal/webauth/...
go test -race ./internal/edge/...

# 상세 출력(-v)
go test -v ./internal/snmp/...
```

### 1.3 커버리지 측정

패키지별 커버리지 요약 출력:

```bash
go test -cover ./...
```

커버리지 프로파일 파일 생성 및 함수별 상세 분석:

```bash
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
rm -f coverage.out
```

HTML 형태의 라인별 커버리지 뷰어(브라우저):

```bash
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html
```

### 1.4 프런트엔드 / 웹 자산 테스트

정적 에셋 내 잔류 시뮬레이션 데이터 및 무결성 검증 (Go 임베드 테스트):

```bash
go test -v ./web/...
```

JavaScript 단위 테스트 (Node.js 내장 테스트 러너):

```bash
node --test web/tests/*.test.mjs
```
*(참고: 프런트엔드 테스트는 `web/tests/*.test.mjs` 이름으로 추가해야 CI와 릴리스 게이트에 포함된다.)*

---

## 2. CI (Continuous Integration) 게이트

GitHub Actions 워크플로(`.github/workflows/ci.yml`)는 `push` 및 `pull_request` 발생 시 자동으로 실행되며, 다음 검사를 모두 통과해야 한다.

| 단계 | 명령어 | 설명 |
|---|---|---|
| **1. 포맷팅 검사** | `unformatted=$(gofmt -l .) && [ -z "$unformatted" ]` | Go 코드 포맷팅 표준 준수 여부 검사 (gofmt 기준 위반 시 실패) |
| **2. 정적 분석** | `go vet ./...` | 잠재적 버그, 잘못된 서식 문자열, 미사용 변수 등 컴파일러 레벨 정적 분석 |
| **3. 의존성 경계** | `go list -m all` | 제품 Go 코드가 표준 라이브러리 경계를 유지하는지 검사 |
| **4. 레이스·커버리지** | `go test -shuffle=on -race -covermode=atomic ...` | 순서 의존성·데이터 레이스를 찾고 전체 statement coverage 80% 이상 강제 |
| **5. 프런트 테스트** | `node --test web/tests/*.test.mjs` | 모든 명시적 JavaScript 테스트 파일 실행 |
| **6. 교차 빌드** | Linux/Windows amd64 build + Windows test compile | 두 고객 대상의 컴파일 계약 확인 |
| **7. Windows 스크립트 파싱** | PowerShell AST parser | 모든 배포 `.ps1`의 문법 오류 검사 |
| **8. 배포 자산** | `sh deploy/packaging/validate-deployment.sh` | 설치·업데이트·롤백·제거 및 릴리스 payload 정적 계약 검사 |
| **9. 취약점 검사** | `govulncheck ./...` | 도달 가능한 Go 취약점 검사 |

CI 게이트 로컬 사전 점검 명령:

```bash
unformatted=$(gofmt -l .); test -z "$unformatted"
go vet ./...
go test -shuffle=on -race -covermode=atomic -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tail -1
node --test web/tests/*.test.mjs
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/serverdesk-linux ./cmd/serverdesk
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o /tmp/serverdesk-windows.exe ./cmd/serverdesk
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -exec=/bin/true ./...
sh deploy/packaging/validate-deployment.sh
```

`-exec=/bin/true`는 Windows 테스트 바이너리를 컴파일하지만 실행하지 않는다. 이미 설치된 scanner가
있으면 `GOOS=linux GOARCH=amd64 govulncheck ./...`와 Windows target도 각각 실행한다.
PowerShell AST parser, ShellCheck, actionlint는 사용 가능할 때 별도 실행하고, 없으면 NOT_TESTED로
기록한다. 과거 CI 성공은 현재 working tree의 실행 증거가 아니다. CI 성공도 실제
Windows/systemd/network migration/장비/복원·72시간·capacity UAT나 stable promotion 승인을 대체하지 않는다.

### 2.1 현재 관측 결과 및 근거

2026-10-13 사용자 로컬 날짜 기준, `3008e31` 기반 미커밋 working tree의 Tasks 1–6 최종 통합 결과다.
새 release version/실제 archive UAT가 아니다. Workspace
`.superpowers/sdd/serverdesk-remediation-20261012-plan/final-code-validation.log`와 `validation/`에서
Go 1.26.9 vet/gofmt/race·shuffle 15 packages PASS, 전체 statement coverage **83.1%**,
JS **61/61 PASS**(skipped 0), Linux/Windows amd64 build + Windows test compile PASS,
두 target `govulncheck` **No vulnerabilities found**, 배포 **15 mock tests + rootless PASS**를 확인했다.
기존 host XFS unit의 `CPUAccounting=` 경고 2건은 validator 출력에 남아 있다.
문서 수정 전 `pre-doc-final-validation.log`/`validation-before-docs/`는 이전 결과이며 최신 로그를 우선한다.
UTC 실행 날짜와 사용자 로컬 날짜는 다를 수 있다.

실제 Chromium smoke는 disposable loopback NAS fixture에서 login/logout/requery와 표시 메타 edit를
확인했다. 미수정 필드와 disk secret reference 보존, console warning/error 0을 확인했지만
장비 수집/전체 UI/접근성 UAT는 아니다. 로컬 ShellCheck/actionlint/pwsh는 미설치로 NOT_TESTED다.
문서 후 whole-diff review/최종 통합 게이트는 별도 수행한다.

### 2.2 회귀 검증 범위

- Availability malformed record 격리, 연속 fresh observation만 누적, topology timestamp/fallback,
  bounded shutdown wait와 availability health 진행·저장 오류.
- Metadata-only `PUT /api/clusters/<key>`의 운영 필드 400/무부분저장, 응답 `applied`만 sparse overlay,
  raw password 보존, PLC port 정수 1–65535(생략 시 9600).
- NAS missing/unknown/미발견 empty inventory의 보수적 stale 및 fault 보존, PLC dynamic CIP/FINS fault 재관측.
- SNMP sender/request/envelope/OID 검증, signed BER/Counter64 overflow raw 보존,
  fragmented/coalesced CIP frame, FINS/CIP cancellation/deadline, canonical private trap path·community 제거.
- Notification pending retry의 maintenance/ACK/source pre-send 재확인, restart/recurrence/active dedupe,
  resolution 기반 inactive retention; at-least-once crash window는 남는다.
- Root-only install/update/uninstall single-lock contention, persistent network ownership migration/실패 보존,
  rollback 재생성 journal 보존, missing journal의 잔여 자원 fail closed, stable tag candidate-only 계약.

이 검사는 코드 계약을 확인할 뿐, 취소 불가능한 OS I/O의 내구성 시간 상한, Windows ACL 인증,
SLA 가용성 증명 또는 독립 compiler reproducibility를 보장하지 않는다.
[PRODUCTION-READINESS.md](PRODUCTION-READINESS.md)의 BLOCKED/NOT_TESTED 게이트를 유지한다.

---

## 3. 테스트 작성 관례 및 원칙

### 3.1 스텁(Stub) 원칙: 실제 장비 접촉 금지

- **실제 하드웨어 통신 절대 금지**: everRun, ztC Edge, PLC(Omron/Rockwell), NAS(Synology), 프린터, Proxmox 등 실 장비 또는 원격 운영 IP로의 실제 네트워크 요청(SNMP, HTTP/REST, Modbus/FINS/CIP, SSH)을 테스트 코드에서 발생시키지 않는다.
- **인메모리 / 로컬 스텁 활용**:
  - HTTP API는 `net/http/httptest`(`httptest.NewServer`, `httptest.NewRecorder`)를 사용하여 격리된 테스트 서버를 기동한다.
  - 소켓/UDP/TCP 프로토콜은 로컬 루프백(`127.0.0.1` 임의 포트) 리스너 또는 mock 핸들러를 사용한다.
  - SSH/avcli 등 외부 바이너리 호출 패키지는 인터페이스 또는 shim 계층을 통해 가짜 출력을 반환하도록 스텁한다.

### 3.2 고비용 연산 최적화 (TestMain 오버라이드)

- 암호학적 해시(PBKDF2 등)나 대규모 반복 작업으로 인해 테스트 시간이 길어지는 경우, 프로덕션 기본값은 유지한 채 테스트 시에만 매개변수를 축소하도록 `TestMain` 패턴을 적용한다.
- 예시 (`internal/webauth/auth_test.go`):
  ```go
  func TestMain(m *testing.M) {
      // 프로덕션 기본값(600,000회) 대신 테스트 전용 1,000회로 단축
      credentialIterations = 1000
      testCredentials = mustTestCredentials()
      os.Exit(m.Run())
  }
  ```

### 3.3 파일시스템 격리 (`t.TempDir()`)

- 파일 읽기/쓰기, 설정 파일 저장, UI 상태 저장(`state.json`), 인증 파일(`auth.json`) 등의 테스트는 항상 `t.TempDir()`을 기반으로 독립된 임시 디렉터리를 생성하여 수행한다.
- 테스트 종료 후 자동으로 임시 파일이 정리되어 호스트 환경을 오염시키지 않도록 한다.

### 3.4 동시성 및 레이스 컨디션 검증

- 공유 상태(Store, Cache, Map, Worker)를 다루는 코드는 동시 읽기/쓰기 상황을 가정한 goroutine 테스트를 작성한다.
- 모든 테스트는 `go test -race` 환경에서 무경고로 통과해야 한다.

### 3.5 프로덕션 자산 순수성 검증

- 시뮬레이션 플릿, 데모 시드(`SIM_SEED`), 가짜 장비 데이터가 실제 배포용 정적 자산(`web/`)에 포함되지 않도록 검증하는 전용 테스트(`web/web_test.go`)를 유지한다.
