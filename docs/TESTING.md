# serverdesk-go 테스트 가이드

`serverdesk-go` 프로젝트의 테스트 체계, 실행 방법, CI 게이트 규칙 및 테스트 작성 관례를 기술한다.

---

## 1. 테스트 실행 방법

### 1.1 사전 환경 설정

로컬 환경에서는 프로젝트 내 내장 툴체인을 우선 사용하도록 `PATH` 환경변수를 설정한다.

```bash
cd /path/to/serverdesk-go
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

PowerShell 파서와 `govulncheck`도 로컬에 도구가 있으면 실행하며, 최종 판정은 GitHub Actions의
동일 게이트 결과를 기준으로 한다. 정적·합성 검사는 실제 Windows/systemd/Stratus/브라우저 UAT를
대체하지 않는다.

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
