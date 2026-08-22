package sshmetrics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultTimeout 은 Python SshRunner.run 의 timeout=20 과 같은 기본값이다.
const defaultTimeout = 20 * time.Second

// Logf 는 호스트 로거 연결 훅이다(level 은 "debug"/"warn"/"error" 등). 기본 no-op.
var Logf = func(level, host, msg string) {}

// ErrHostKeyChanged 는 SSH 연결 시 대상 호스트의 호스트 키가 known_hosts 와 일치하지 않아
// 검증에 실패했음을 나타낸다(장비 재이미징 또는 MITM 감지).
var ErrHostKeyChanged = errors.New("ssh host key verification failed")

// HostKeyError 는 호스트 키 검증 실패에 대한 전용 에러 타입이다.
// 일반 네트워크/인증 실패와 구분하여 관리자가 호스트키 변경(재이미징/MITM)을 즉시 인지하도록 한다.
type HostKeyError struct {
	Host   string
	User   string
	Detail string
}

func (e *HostKeyError) Error() string {
	return fmt.Sprintf("ssh %s@%s: [HOST_KEY_VERIFICATION_FAILED] 호스트 키 검증 실패: %s", e.User, e.Host, e.Detail)
}

func (e *HostKeyError) Unwrap() error {
	return ErrHostKeyChanged
}

func (e *HostKeyError) Is(target error) bool {
	return target == ErrHostKeyChanged
}

func isHostKeyFailure(stderr string) bool {
	return strings.Contains(stderr, "Host key verification failed") ||
		strings.Contains(stderr, "REMOTE HOST IDENTIFICATION HAS CHANGED") ||
		strings.Contains(stderr, "HOST IDENTIFICATION HAS CHANGED")
}

// Runner 는 노드 SSH 실행기다. 비밀번호는 env(SSH_PW)로만 전달하고 로그·인자에
// 남기지 않는다.
//
// sshpass 가 없는 환경이라 SSH_ASKPASS + `setsid -w` 로 암호를 넘긴다
// (`setsid -w` 가 없으면 askpass 가 호출되지 않는다).
// ControlMaster 재사용으로 세션당 접속 1회(0.01초)로 줄어들고 노드의 auth.log
// 부담도 준다. 주의: ControlPersist(300s) 동안은 기존 마스터 소켓이 재사용되므로,
// 설정에서 암호를 바꿔도 소켓이 만료되기 전까지는 옛 인증이 그대로 통한다.
//
// Runner 는 호스트별 직전 샘플·리부트 상태를 안에서 들고 있다(고루틴 안전).
// 같은 호스트에 대해 Collect 를 직렬로 부르는 것은 호출자 몫이지만, 병렬로
// 불러도 상태가 깨지지는 않는다.
type Runner struct {
	dir        string
	askpass    string
	knownHosts string
	cmDir      string
	timeout    time.Duration

	mu    sync.Mutex
	hosts map[string]*hostState
}

// hostState 는 델타 기준(직전 누적 샘플)과 리부트 감지 상태다.
// SSH 실패 때 지우지 않는다 — 재접속 후 dt 가 긴 델타로 이어져야 하고,
// 실패 동안 일어난 리부트도 다음 성공 수집에서 잡아야 하기 때문이다.
type hostState struct {
	prev       *rawSample
	uptimeLast *int64
	rebootAt   *time.Time
}

// NewRunner 는 runtimeDir 아래에 askpass 스크립트(0700)·known_hosts(0600)·
// ControlMaster 소켓 디렉터리(cm, 0700)를 준비한다.
//
// known_hosts 를 영속 파일로 두고 accept-new 를 쓰는 이유: /dev/null 로 두면
// 호스트 인증이 0 이라 관리망 MITM(ARP 스푸핑)이 자기 호스트키로 KEX 를 끝낼 때
// 클라이언트가 조용히 수락하고 askpass 가 노드 root 암호를 공격자에게 넘긴다.
// TOFU 로 최초 1회만 키를 학습하고 이후 키 변경(재이미징/MITM)은 거부한다.
// timeout 이 0 이하면 20초(Python 기본값)를 쓴다.
func NewRunner(runtimeDir string, timeout time.Duration) (*Runner, error) {
	if runtimeDir == "" {
		return nil, errors.New("sshmetrics: runtimeDir 이 비어 있다")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	r := &Runner{dir: runtimeDir, timeout: timeout, hosts: map[string]*hostState{}}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("sshmetrics: runtime dir 생성: %w", err)
	}
	// MkdirAll 은 umask 에 눌릴 수 있고 기존 디렉터리 모드는 그대로라 모드를 고정한다.
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("sshmetrics: runtime dir 모드: %w", err)
	}
	r.askpass = filepath.Join(runtimeDir, askpassFile)
	if _, err := os.Stat(r.askpass); errors.Is(err, fs.ErrNotExist) {
		// 이미 있으면 내용을 덮지 않는다(현장 수정본 보존). Python 동일.
		// 내용은 플랫폼별 상수(sshwrap_*.go) — Windows 는 askpass.bat.
		if err := os.WriteFile(r.askpass, []byte(askpassScript), 0o700); err != nil {
			return nil, fmt.Errorf("sshmetrics: askpass 생성: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("sshmetrics: askpass 확인: %w", err)
	}
	if err := os.Chmod(r.askpass, 0o700); err != nil {
		return nil, fmt.Errorf("sshmetrics: askpass 모드: %w", err)
	}
	r.knownHosts = filepath.Join(runtimeDir, "known_hosts")
	if _, err := os.Stat(r.knownHosts); errors.Is(err, fs.ErrNotExist) {
		f, err := os.OpenFile(r.knownHosts, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("sshmetrics: known_hosts 생성: %w", err)
		}
		_ = f.Close()
	} else if err != nil {
		return nil, fmt.Errorf("sshmetrics: known_hosts 확인: %w", err)
	}
	if err := os.Chmod(r.knownHosts, 0o600); err != nil {
		return nil, fmt.Errorf("sshmetrics: known_hosts 모드: %w", err)
	}
	r.cmDir = filepath.Join(runtimeDir, "cm")
	if err := os.MkdirAll(r.cmDir, 0o700); err != nil {
		return nil, fmt.Errorf("sshmetrics: cm dir 생성: %w", err)
	}
	if err := os.Chmod(r.cmDir, 0o700); err != nil {
		return nil, fmt.Errorf("sshmetrics: cm dir 모드: %w", err)
	}
	return r, nil
}

// Collect 는 host 에 SSH 로 MetricsScript 를 실행하고, 파싱 결과에 직전 샘플과의
// 델타·리부트 감지를 얹어 반환한다. 실패하면 (nil, error) — 파생 메트릭을 폐기하는
// 것은 Python 과 같은 이유(오래된 값이 현재값 행세를 하면 안 된다)로 호출자의 계약이다.
//
// port 가 0 이하이면 ssh 기본(22)에 맡긴다. user 가 비면 root 다.
func (r *Runner) Collect(ctx context.Context, host string, port int, user string, password string) (*Metrics, error) {
	if user == "" {
		user = "root"
	}
	raw, err := r.execSSH(ctx, host, port, user, password, MetricsScript)
	if err != nil {
		return nil, err
	}
	m, sample := parseMetrics(raw, time.Now())
	m.IP = host

	r.mu.Lock()
	h := r.hosts[host]
	if h == nil {
		h = &hostState{}
		r.hosts[host] = h
	}
	applyDeltasLocked(h, m, sample)
	rebootCheckLocked(h, m, time.Now())
	h.prev = sample
	r.mu.Unlock()
	return m, nil
}

// execSSH 는 Python SshRunner.run 에 해당한다. (stdout, ok) 대신 (string, error) 를
// 반환하며, 다음이 모두 Python 의 ok=False 에 해당한다: 실행 실패, 타임아웃/취소,
// rc!=0 이면서 stdout 이 빈 경우. rc!=0 이어도 stdout 이 있으면 파싱을 진행한다
// (일부 섹션만 나와도 유효한 부분 출력이므로). rc==0 인데 stdout 이 비면 에러다.
func (r *Runner) execSSH(ctx context.Context, host string, port int, user, password, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	args := []string{
		"-w", "ssh",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + r.knownHosts,
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=no",
		"-o", "LogLevel=ERROR",
	}
	args = append(args, extraSSHArgs(r.cmDir)...)
	if password != "" {
		args = append(args, "-o", "PreferredAuthentications=password",
			"-o", "PubkeyAuthentication=no")
	}
	if port > 0 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	args = append(args, user+"@"+host, command)

	cmd := sshCmd(ctx, args) // 플랫폼별 래퍼 — sshwrap_*.go(Windows 는 setsid 없이 직접 호출)
	// Stdin nil = /dev/null(Python 의 stdin=DEVNULL). TTY 가 없어야 askpass 로 간다.
	cmd.Stdin = nil
	cmd.WaitDelay = 3 * time.Second
	env := os.Environ()
	env = setEnv(env, "SSH_PW", password)
	env = setEnv(env, "SSH_ASKPASS", r.askpass)
	env = setEnv(env, "SSH_ASKPASS_REQUIRE", "force")
	// Python 의 setdefault("DISPLAY", ":0"): 키가 아예 없을 때만 넣는다.
	if _, ok := os.LookupEnv("DISPLAY"); !ok {
		env = append(env, "DISPLAY=:0")
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	out := stdout.String()
	stderrStr := stderr.String()
	if isHostKeyFailure(stderrStr) {
		Logf("error", host, fmt.Sprintf("[SECURITY] 호스트 키 검증 실패(Host key verification failed): %s@%s — known_hosts 불일치 또는 MITM 위험 (%s)", user, host, truncate(stderrStr, 200)))
		return "", &HostKeyError{Host: host, User: user, Detail: truncate(stderrStr, 200)}
	}
	if runErr != nil {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("ssh %s@%s: %w", user, host, err)
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			if strings.TrimSpace(out) == "" {
				return "", fmt.Errorf("ssh %s@%s: rc=%d: %s",
					user, host, exitErr.ExitCode(), truncate(stderrStr, 200))
			}
			return out, nil
		}
		return "", fmt.Errorf("ssh %s@%s 실행 실패: %w", user, host, runErr)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("ssh %s@%s: 빈 출력", user, host)
	}
	return out, nil
}

// setEnv 은 env 슬라이스에서 key 를 정확히 하나로 만든다. os.Environ() 에 같은
// 키가 있으면 execve 에 중복 전달돼 glibc getenv 가 첫 값(=낡은 값)을 고를 수 있어
// Python 의 dict 덮어쓰기와 같은 의미를 여기서 재현한다.
func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}

// truncate 는 stderr 를 로그 한 줄 분량으로 자른다(Python: stderr.strip()[:200]).
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
