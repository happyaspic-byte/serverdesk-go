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

### 2.2 폐쇄망 장비 통신 정책 및 TLS 피닝
- **폐쇄망 자체서명 통신**: 공인 CA 체인이 존재하지 않는 폐쇄망 장비(Proxmox, BMC Redfish 등)의 웹 API 통신은 `InsecureSkipVerify: true` 클라이언트를 제한된 패키지 내부에서만 격리하여 사용합니다.
  - 관련 코드: `internal/edge/httpx.go:46-57`, `internal/httpapi/admin.go:658-665`
- **신규 TLS 지문 피닝 (`tls_fingerprint`) 옵션**: 폐쇄망 자체서명 환경에서도 장비 스푸핑 및 불법 중간자 프록시를 원천 차단하기 위해, 장비의 SHA-256 인증서 지문을 설정에 명시하고 검증하는 피닝(Pinning) 옵션을 지원합니다.

---

## 3. 알려진 한계 및 완화 대책 (Known Limitations & Mitigations)

### 3.1 `avcli -p` 프로세스 인자 노출
- **한계**: Stratus everRun / ztC Edge CLI인 `avcli`는 비밀번호를 파일이나 환경변수로 전달하는 표준 옵션이 없어, 실행 시 `-p <암호>` 형태로 커맨드라인(`argv`)에 일시 노출됩니다.
  - 관련 코드: `internal/avcli/client.go:45-46`
- **완화 대책**:
  - 시스템 관리자 외 일반 사용자가 `/proc` 프로세스 목록을 볼 수 없도록 Linux 커널 마운트 옵션 `hidepid=2` 적용을 권장합니다.
  - Serverdesk 기동 시 `/proc/mounts` 및 로컬 계정 상태를 점검하여 다중 사용자 환경에서 `hidepid` 미적용 시 경고를 출력합니다(`CheckArgvExposure`).
  - 관련 코드: `internal/config/checks.go:32-58, 83-143`

### 3.2 `config.local.json` 설정 파일 평문 저장
- **한계**: 클러스터 관리 암호, 노드 SSH 암호 등이 설정 파일(`config.local.json`)에 평문으로 저장됩니다.
  - 관련 코드: `internal/config/config.go:1-3, 52-60, 79-100`
- **완화 대책**:
  - 설정 파일의 파일 시스템 권한을 소유자 전용(`0600` / `chmod 600`)으로 강제 제한하도록 검사합니다(`CheckPerms`).
  - `group` 또는 `other`에 읽기/쓰기 권한이 열려 있는 경우 기동 시 경고 로그를 출력합니다.
  - 관련 코드: `internal/config/checks.go:13-30`
