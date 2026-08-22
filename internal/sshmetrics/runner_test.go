package sshmetrics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// runner_test 는 진짜 SSH 를 쓰지 않는다. PATH 앞에 가짜 setsid/ssh 스크립트를
// 얹어 Runner 가 만드는 인자·환경·반환 규약만 검증한다.

// setupFakeSSH 는 가짜 setsid/ssh 를 만들고 PATH 에 올린다.
// 가짜 ssh 는 인자를 argsDump 에, 환경을 envDump 에 남기고 FAKE_MODE 대로 동작한다:
//
//	""(기본) → $FAKE_OUT 내용을 stdout 으로(cat)
//	partial  → 출력 후 rc=1 (부분 출력 — 성공이어야 함)
//	fail     → stderr 만 남기고 rc=255 (빈 stdout + rc!=0 — 실패여야 함)
//	empty    → rc=0, 출력 없음(실패여야 함)
//	sleep    → 30초 수면(타임아웃 검증용)
func setupFakeSSH(t *testing.T, mode string) (envDump, argsDump, outFile string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	envDump = filepath.Join(dir, "env.txt")
	argsDump = filepath.Join(dir, "args.txt")
	outFile = filepath.Join(dir, "out.txt")
	// exec 체인으로 마지막 프로세스가 직접 자식이 되게 한다 — 그래야 타임아웃
	// kill 이 고아 프로세스를 남기지 않는다.
	setsid := "#!/bin/sh\nshift\nexec \"$@\"\n"
	ssh := `#!/bin/sh
[ -n "$FAKE_ARGS_DUMP" ] && printf '%s\n' "$@" > "$FAKE_ARGS_DUMP"
[ -n "$FAKE_ENV_DUMP" ] && env > "$FAKE_ENV_DUMP"
case "$FAKE_MODE" in
partial) cat "$FAKE_OUT"; exit 1 ;;
fail) echo "Permission denied, please try again." >&2; exit 255 ;;
hostkey) echo "Host key verification failed." >&2; exit 255 ;;
empty) exit 0 ;;
sleep) exec sleep 30 ;;
*) cat "$FAKE_OUT" ;;
esac
`
	for name, content := range map[string]string{"setsid": setsid, "ssh": ssh} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_ARGS_DUMP", argsDump)
	t.Setenv("FAKE_ENV_DUMP", envDump)
	t.Setenv("FAKE_OUT", outFile)
	t.Setenv("FAKE_MODE", mode)
	return envDump, argsDump, outFile
}

func TestNewRunnerFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rt")
	r, err := NewRunner(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.timeout != defaultTimeout {
		t.Errorf("timeout 기본값 = %v", r.timeout)
	}
	checkMode := func(path string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s 모드 = %o, want %o", path, got, want)
		}
	}
	checkMode(dir, 0o700)
	checkMode(filepath.Join(dir, "askpass.sh"), 0o700)
	checkMode(filepath.Join(dir, "known_hosts"), 0o600)
	checkMode(filepath.Join(dir, "cm"), 0o700)

	b, err := os.ReadFile(filepath.Join(dir, "askpass.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "#!/bin/sh\necho \"$SSH_PW\"\n" {
		t.Errorf("askpass 내용 = %q", b)
	}

	// 두 번째 생성: 기존 파일을 덮지 않고 모드만 바로잡는다.
	custom := []byte("#!/bin/sh\necho custom\n")
	if err := os.WriteFile(filepath.Join(dir, "askpass.sh"), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner(dir, 0); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "askpass.sh"))
	if string(b) != string(custom) {
		t.Errorf("기존 askpass 를 덮어썼다: %q", b)
	}
	checkMode(filepath.Join(dir, "askpass.sh"), 0o700)
	checkMode(dir, 0o700) // 0755 → 0700 으로 고정

	if _, err := NewRunner("", 0); err == nil {
		t.Error("빈 runtimeDir 은 에러여야 한다")
	}
}

// TestCollectSuccess 는 두 번의 수집으로 델타가 계산되는 전체 경로와,
// ssh 인자·환경(비밀번호가 인자에 없고 env 로만 간다)을 검증한다.
func TestCollectSuccess(t *testing.T) {
	envDump, argsDump, outFile := setupFakeSSH(t, "")
	// DISPLAY 가 없을 때만 ":0" 이 들어가는지(setdefault) 본다.
	if old, ok := os.LookupEnv("DISPLAY"); ok {
		if err := os.Unsetenv("DISPLAY"); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Setenv("DISPLAY", old) }()
	}

	r, err := NewRunner(t.TempDir(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outFile, []byte(sampleA), 0o644); err != nil {
		t.Fatal(err)
	}
	m1, err := r.Collect(context.Background(), "10.0.0.1", 2222, "", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if m1.TS != 1700000000 || m1.IP != "10.0.0.1" {
		t.Errorf("m1 = ts %d ip %s", m1.TS, m1.IP)
	}
	if m1.CPUPct != nil || m1.Net != nil {
		t.Errorf("첫 수집에 델타가 있으면 안 된다")
	}

	// ssh 인자 검증(포트·옵션·대상). 비밀번호는 인자 어디에도 없어야 한다.
	ab, err := os.ReadFile(argsDump)
	if err != nil {
		t.Fatal(err)
	}
	args := string(ab)
	for _, want := range []string{
		"-o\nStrictHostKeyChecking=accept-new\n",
		"-o\nUserKnownHostsFile=" + r.knownHosts + "\n",
		"-o\nConnectTimeout=10\n",
		"-o\nBatchMode=no\n",
		"-o\nLogLevel=ERROR\n",
		"-o\nControlMaster=auto\n",
		"-o\nControlPath=" + filepath.Join(r.cmDir, "%r@%h:%p") + "\n",
		"-o\nControlPersist=300\n",
		"-o\nPreferredAuthentications=password\n",
		"-o\nPubkeyAuthentication=no\n",
		"-p\n2222\n",
		"root@10.0.0.1\n" + MetricsScript, // user 기본값 root, 마지막 인자가 스크립트
	} {
		if !strings.Contains(args, want) {
			t.Errorf("인자에 %q 가 없다:\n%s", want, args)
		}
	}
	if strings.Contains(args, "s3cret") {
		t.Errorf("비밀번호가 인자에 새 나갔다:\n%s", args)
	}

	// env 검증: SSH_PW/ASKPASS 강제, DISPLAY 는 부재 시에만 ":0".
	eb, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatal(err)
	}
	env := string(eb)
	for _, want := range []string{"SSH_PW=s3cret\n", "SSH_ASKPASS_REQUIRE=force\n",
		"SSH_ASKPASS=" + r.askpass + "\n", "DISPLAY=:0\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("env 에 %q 가 없다", want)
		}
	}

	// 두 번째 수집: 델타 계산.
	if err := os.WriteFile(outFile, []byte(sampleB), 0o644); err != nil {
		t.Fatal(err)
	}
	m2, err := r.Collect(context.Background(), "10.0.0.1", 2222, "root", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if got := fval(t, m2.CPUPct); got != 25.0 {
		t.Errorf("cpu_pct = %v, want 25.0", got)
	}
	if got := ival(t, m2.InterconnectDrops); got != 4 {
		t.Errorf("interconnect_drops = %d, want 4", got)
	}
	if len(m2.Net) != 3 {
		t.Errorf("net = %+v", m2.Net)
	}
	if m2.RecentlyBooted == nil || *m2.RecentlyBooted {
		t.Errorf("uptime 86435s 는 recently_booted=false: %+v", m2.RecentlyBooted)
	}
}

// TestCollectFailureKeepsPrevSample 은 SSH 실패 시 (nil, error) 만 반환하고
// 델타 기준 샘플은 지우지 않는 것(재접속 후 델타가 이어진다)을 검증한다.
func TestCollectFailureKeepsPrevSample(t *testing.T) {
	_, _, outFile := setupFakeSSH(t, "")
	r, err := NewRunner(t.TempDir(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := os.WriteFile(outFile, []byte(sampleA), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Collect(ctx, "10.0.0.2", 0, "root", "pw"); err != nil {
		t.Fatal(err)
	}

	// rc=255 + 빈 stdout → 에러. Metrics 는 nil 이어야 한다(오래된 값 방치 금지).
	t.Setenv("FAKE_MODE", "fail")
	m, err := r.Collect(ctx, "10.0.0.2", 0, "root", "pw")
	if err == nil || m != nil {
		t.Fatalf("실패 수집은 (nil, error) 여야 한다: m=%+v err=%v", m, err)
	}
	if !strings.Contains(err.Error(), "rc=255") ||
		!strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("에러에 rc/stderr 가 없다: %v", err)
	}

	// 실패 후 다음 성공 수집도 직전 성공 샘플(A) 기준으로 델타가 나와야 한다.
	t.Setenv("FAKE_MODE", "")
	if err := os.WriteFile(outFile, []byte(sampleB), 0o644); err != nil {
		t.Fatal(err)
	}
	m2, err := r.Collect(ctx, "10.0.0.2", 0, "root", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if got := fval(t, m2.CPUPct); got != 25.0 {
		t.Errorf("실패 뒤 재접속 cpu_pct = %v, want 25.0 (A 기준 델타)", got)
	}
}

// TestCollectEmptyAndPartialOutput 은 rc==0 빈 출력(실패)과 rc!=0 부분 출력(성공)
// 규약을 검증한다.
func TestCollectEmptyAndPartialOutput(t *testing.T) {
	_, _, outFile := setupFakeSSH(t, "empty")
	r, err := NewRunner(t.TempDir(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if m, err := r.Collect(ctx, "10.0.0.3", 0, "root", "pw"); err == nil || m != nil {
		t.Errorf("빈 출력은 에러여야 한다: m=%+v err=%v", m, err)
	}

	t.Setenv("FAKE_MODE", "partial")
	if err := os.WriteFile(outFile, []byte(sampleA), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := r.Collect(ctx, "10.0.0.3", 0, "root", "pw")
	if err != nil {
		t.Fatalf("rc!=0 이어도 출력이 있으면 성공이다: %v", err)
	}
	if m.TS != 1700000000 {
		t.Errorf("ts = %d", m.TS)
	}
}

// TestCollectTimeout 은 Runner timeout 이 context 마감으로 전달되는지 검증한다.
func TestCollectTimeout(t *testing.T) {
	setupFakeSSH(t, "sleep")
	r, err := NewRunner(t.TempDir(), 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	m, err := r.Collect(context.Background(), "10.0.0.4", 0, "root", "pw")
	if err == nil || m != nil {
		t.Fatalf("타임아웃은 에러여야 한다: m=%+v", m)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("DeadlineExceeded 가 아니다: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("타임아웃이 적용되지 않았다: %v", time.Since(start))
	}
}

// TestCollectConcurrent 는 여러 고루틴이 같은/다른 호스트로 Collect 을 동시에
// 호출해도 호스트 상태(맵·직전 샘플)가 깨지지 않는지 본다(-race 로 실행).
func TestCollectConcurrent(t *testing.T) {
	_, _, outFile := setupFakeSSH(t, "")
	if err := os.WriteFile(outFile, []byte(sampleA), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(t.TempDir(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			host := "10.0.1." + string(rune('0'+i%4))
			for j := 0; j < 3; j++ {
				if _, err := r.Collect(context.Background(), host, 0, "root", "pw"); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	r.mu.Lock()
	n := len(r.hosts)
	r.mu.Unlock()
	if n != 4 {
		t.Errorf("hosts = %d, want 4", n)
	}
}

// TestSetEnv 는 env 키 덮어쓰기(glibc 는 중복 키의 첫 값을 고르뿀로)를 검증한다.
func TestSetEnv(t *testing.T) {
	env := setEnv([]string{"A=1", "B=2"}, "A", "9")
	if got := strings.Join(env, ","); got != "A=9,B=2" {
		t.Errorf("setEnv 덮어쓰기 = %s", got)
	}
	env = setEnv(env, "C", "3")
	if got := strings.Join(env, ","); got != "A=9,B=2,C=3" {
		t.Errorf("setEnv 추가 = %s", got)
	}
}

// TestCollectHostKeyVerificationFailed 는 호스트 키 불일치 시 ErrHostKeyChanged 및 HostKeyError 전용 타입 반환을 검증한다.
func TestCollectHostKeyVerificationFailed(t *testing.T) {
	_, _, _ = setupFakeSSH(t, "hostkey")
	r, err := NewRunner(t.TempDir(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	var loggedLevel, loggedHost, loggedMsg string
	Logf = func(level, host, msg string) {
		loggedLevel = level
		loggedHost = host
		loggedMsg = msg
	}
	defer func() { Logf = func(level, host, msg string) {} }()

	ctx := context.Background()
	m, err := r.Collect(ctx, "10.0.0.99", 22, "root", "secret")
	if m != nil {
		t.Errorf("m = %+v, want nil on error", m)
	}
	if err == nil {
		t.Fatal("호스트키 불일치는 에러여야 한다")
	}
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Errorf("err should match ErrHostKeyChanged, got: %v", err)
	}
	var hkErr *HostKeyError
	if !errors.As(err, &hkErr) {
		t.Errorf("err should be *HostKeyError, got: %T (%v)", err, err)
	} else {
		if hkErr.Host != "10.0.0.99" || hkErr.User != "root" {
			t.Errorf("unexpected HostKeyError content: %+v", hkErr)
		}
	}
	if loggedLevel != "error" || loggedHost != "10.0.0.99" || !strings.Contains(loggedMsg, "Host key verification failed") {
		t.Errorf("Logf not called properly: level=%s host=%s msg=%s", loggedLevel, loggedHost, loggedMsg)
	}
}
