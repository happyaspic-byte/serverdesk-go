# Serverdesk 보안 모델 및 정책 (Security Policy & Architecture)

이 문서는 `serverdesk-go` 제로 의존성 Go 단일 바이너리 관제 콘솔의 보안 모델, 통신 보안 메커니즘, 알려진 한계 및 완화 대책을 설명합니다.

---

## 1. 웹 인증 및 세션 보안 (Web Authentication & Session Model)

Serverdesk 웹 콘솔은 단일 관리자(`admin`) 계정 기반으로 작동하며, 외부 인증 라이브러리 없이 Go 표준 라이브러리만을 활용하여 안전한 인증 및 세션 제어를 구현합니다.

### 1.1 PBKDF2-HMAC-SHA256 기반 자격증명 파생 및 검증
- **키 파생 횟수**: 600,000회 (`credentialIterations = 600_000`)를 적용하여 무차별 대입 및 사전 공격을 방어합니다.
  - 관련 코드: `internal/webauth/credentials.go:28-29`
- **상수 시간 비교**: 비밀번호 검증 시 타이밍 공격(Timing Attack)을 방지하기 위해 `crypto/subtle.ConstantTimeCompare`를 사용합니다.
  - 관련 코드: `internal/webauth/auth.go:369-376`

### 1.2 세션 관리 및 Strict 쿠키 정책
- **단기 인메모리 세션**: 세션은 서버 인메모리에서만 관리되며(`sessionTTL = 8 * time.Hour`), 서비스 재시작 시 모든 세션이 즉시 만료됩니다.
  - 관련 코드: `internal/webauth/auth.go:26, 41`
- **보안 쿠키 설정**: `serverdesk_session` 쿠키에 `SameSite=Strict`, `HttpOnly=true`, HTTPS 연결 시 `Secure=true`를 강제하여 XSS를 통한 쿠키 탈취 및 교차 사이트 요청을 차단합니다.
  - 관련 코드: `internal/webauth/auth.go:178-187`, `internal/webauth/auth.go:235-244`

### 1.3 무차별 대입 방어 (Rate Limit & Account Locking)
- **로그인 실패 추적**: 10분 윈도우(`failureWindow = 10 * time.Minute`) 내 5회 연속 로그인 실패(`maxFailures = 5`) 시 해당 클라이언트 IP를 15분간 차단(`loginBlockDuration = 15 * time.Minute`)합니다.
- **차단 응답**: 차단 상태에서는 `HTTP 429 Too Many Requests` 상태 코드와 `Retry-After` 헤더를 반환합니다.
  - 관련 코드: `internal/webauth/auth.go:29-33`, `internal/webauth/auth.go:145-149`, `internal/webauth/auth.go:311-328`

### 1.4 CSRF 방어 (Same-Origin 검증)
- **Origin / Referer 헤더 검증**: 로그인, 로그아웃 및 상태 변경(POST/PUT/DELETE) 요청에 대해 요청의 `Origin` 및 `Referer`를 정규화된 `Host`와 엄격하게 비교(`sameOrigin`, `CheckSameOrigin`)하여 CSRF 공격을 차단합니다.
  - 관련 코드: `internal/webauth/auth.go:134, 173, 421-445`, `internal/webfront/hardening.go:28-48, 59-62`

### 1.5 관리자 변경 감사
- 인증을 통과한 모든 POST/PUT/PATCH/DELETE 요청(API와 webfront mutation 모두)은 운영자(`admin`),
  메서드, 이스케이프된 URL path, 최종 HTTP 상태, 검증된 client IP를 `audit` 컴포넌트에 기록합니다.
- loopback reverse proxy에서 전달된 마지막 `X-Forwarded-For` 값만 client IP로 인정하며, 원격 peer의
  forwarded header는 무시합니다. panic이 발생해도 감사 레코드에는 실패 상태가 남고 panic은 그대로 전파됩니다.
- query와 요청 본문은 기록하지 않으므로 장비 password나 token이 감사 로그에 섞이지 않습니다.
  - 관련 코드: `internal/webauth/audit.go`, `internal/webauth/auth.go`

---

## 2. 장비 통신 보안 (Device Communication & Infrastructure)

관제 대상 장비(FT 클러스터 노드, PVE, BMC, 네트워크 장비 등)와의 통신에서 발생할 수 있는 중간자 공격(MITM) 및 데이터 유출을 방어합니다.

### 2.1 SSH TOFU (Trust-On-First-Use) 및 `known_hosts` 정책
- **TOFU 키 학습**: `/dev/null`을 사용하지 않고 런타임 디렉터리 내 영속 파일(`known_hosts`, 권한 `0600`)에 `StrictHostKeyChecking=accept-new` 옵션으로 최초 1회만 호스트 키를 학습합니다.
  - 관련 코드: `internal/sshmetrics/runner.go:86-93, 122-134, 184-185`
- **호스트 키 변경 감지 시 차단 및 경보**: 호스트 키가 변경된 경우(장비 재이미징 또는 MITM 공격), `ErrHostKeyChanged` (`HostKeyError`) 에러를 발생시키며 메트릭 수집을 즉시 중단하고 보안 경보 로그를 남깁니다.
  - 관련 코드: `internal/sshmetrics/runner.go:24-46`, `internal/poller/worker.go:310-312`
- **암호 격리**: 노드 SSH 접속 암호는 환경 변수(`SSH_PW`)와 `SSH_ASKPASS` + `setsid -w` 메커니즘을 통해 전달되며, 프로세스 커맨드라인(`argv`)이나 로그에 노출되지 않습니다.
  - 관련 코드: `internal/sshmetrics/runner.go:54-58`

### 2.2 장비 통신 정책 및 TLS 피닝
- **검증 우선 기본값**: 장비 HTTPS는 TLS 1.2 이상, 시스템 CA 체인 및 호스트명 검증을 기본으로 사용합니다. `tls_fingerprint`가 없는 자체서명 인증서는 허용하지 않습니다.
- **자체서명 장비의 명시적 피닝**: Proxmox, BMC Redfish, 프린터처럼 사설 자체서명 인증서를 사용하는 장비는 `tls_fingerprint`에 SPKI SHA-256 지문을 명시합니다. 이 경우에만 CA 검증을 대체하며, 지문을 상수시간 비교해 불일치 연결을 차단합니다.
  - 관련 코드: `internal/edge/httpx.go`, `internal/edge/worker.go`

### 2.3 웹 리스너 전송 보안
- 평문 HTTP는 루프백 바인드만 기본 허용합니다. 비루프백 주소는 직접 TLS 인증서/키가 있어야 합니다.
  `allow_insecure_http`는 명시적 break-glass 호환 옵션이며, 비루프백 peer의 forwarded header는 계속
  신뢰하지 않으므로 운영 보안 구성이 아닙니다.
- 직접 TLS는 인증서와 개인키를 기동 전에 파싱합니다. 누락·불일치·손상, 유효기간 전/후 인증서,
  Unix에서 group/other가 읽을 수 있는 개인키는 작업 스레드를 시작하기 전에 거부합니다.
  - 관련 코드: `cmd/serverdesk/transport.go`, `cmd/serverdesk/main.go`

---

## 3. 알려진 한계 및 완화 대책 (Known Limitations & Mitigations)

### 3.1 `avcli -p` 프로세스 인자 노출
- **한계**: Stratus everRun / ztC Edge CLI인 `avcli`는 비밀번호를 파일이나 환경변수로 전달하는 표준 옵션이 없어, 실행 시 `-p <암호>` 형태로 커맨드라인(`argv`)에 일시 노출됩니다.
  - 관련 코드: `internal/avcli/client.go:45-46`
- **완화 대책**:
  - 시스템 관리자 외 일반 사용자가 `/proc` 프로세스 목록을 볼 수 없도록 Linux 커널 마운트 옵션 `hidepid=2` 적용을 권장합니다.
  - Serverdesk 기동 시 `/proc/mounts` 및 로컬 계정 상태를 점검하여 다중 사용자 환경에서 `hidepid` 미적용 시 경고를 출력합니다(`CheckArgvExposure`).
  - 관련 코드: `internal/config/checks.go:32-58, 83-143`

### 3.2 장비 자격증명 저장
- **운영 기본값**: `secret_policy=require-references`는 평문 비밀이 있는 설정의 기동을 거부합니다.
  `secret://NAME`은 worker 구성 전에 메모리에서만 해석되며 원본 JSON과 API 백업에는 참조만 남습니다.
- **Linux**: systemd `LoadCredential=`가 제공하는 private `CREDENTIALS_DIRECTORY`를 우선 사용합니다.
  source credential과 config는 각각 root-only 및 `0600`으로 제한합니다.
- **Windows**: migration은 machine-scoped DPAPI ciphertext만 기록합니다. 복사된 blob은 다른 호스트에서
  복호화할 수 없습니다.
- **안전한 이관**: `-migrate-secrets`는 다른 값의 기존 credential을 덮어쓰지 않고, 설정 교체 전 모든
  credential 기록을 완료하며, 최초 평문 원본을 `.pre-secrets.bak`으로 남깁니다.
  - 관련 코드: `internal/config/secretrefs.go`, `internal/config/secretrefs_*.go`
  - 운영 절차: [`CREDENTIALS.md`](CREDENTIALS.md)
