package avcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeStub 은 가짜 avcli 실행 파일을 만든다. avcli 계약(실패 시 stdout 0바이트 +
// stderr 스택트레이스)을 흉낸낸다.
func writeStub(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "avcli")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestClient(bin string) *Client {
	c := NewClient("test", "10.9.9.9", "admin", "fake-secret")
	c.Bin = bin
	c.Timeout = 5 * time.Second
	c.RetryDelay = 10 * time.Millisecond
	return c
}
func TestClientPrefixArgs(t *testing.T) {
	bin := writeStub(t, "#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
	c := newTestClient(bin)
	c.PrefixArgs = []string{"-XX:+IgnoreUnrecognizedVMOptions", "-jar", "/opt/avcli.jar"}

	stdout, stderr := c.exec(context.Background(), "node-info", true)
	want := strings.Join([]string{
		"-XX:+IgnoreUnrecognizedVMOptions",
		"-jar",
		"/opt/avcli.jar",
		"-H",
		"10.9.9.9",
		"-u",
		"admin",
		"-p",
		"fake-secret",
		"-x",
		"node-info",
		"",
	}, "\n")
	if stdout != want || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if got := strings.Join(c.PrefixArgs, "\x00"); got != "-XX:+IgnoreUnrecognizedVMOptions\x00-jar\x00/opt/avcli.jar" {
		t.Errorf("PrefixArgs mutated: %q", got)
	}
}

func TestClientCallXMLSuccess(t *testing.T) {
	bin := writeStub(t, "#!/bin/sh\ncat <<'EOF'\n"+`<avance><node><name>node0</name><state>running</state></node></avance>`+"\nEOF\n")
	c := newTestClient(bin)
	root, err, fatal := c.CallXML3(context.Background(), "node-info")
	if err != nil || fatal {
		t.Fatalf("err=%v fatal=%v", err, fatal)
	}
	nodes := ParseNodeInfo(root)
	if len(nodes) != 1 || strVal(nodes[0].Name) != "node0" {
		t.Errorf("nodes = %#v", nodes)
	}
	st := c.Stats()
	if st.Calls != 1 || st.Errors != 0 || st.Retries != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestClientRetryOnEmptyResponse(t *testing.T) {
	// 첫 호출은 0바이트(벤더 동시접속 쿼크), 두 번째는 정상 — 1회 재시도로 회복돼야 한다
	bin := writeStub(t, `#!/bin/sh
n=$(cat "$AVCLI_STATE" 2>/dev/null || echo 0)
echo $((n+1)) > "$AVCLI_STATE"
if [ "$n" -eq 0 ]; then
  echo "SEVERE: session busy" >&2
  exit 0
fi
echo '<avance><node><name>node0</name></node></avance>'
`)
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("AVCLI_STATE", state)
	c := newTestClient(bin)
	root, err, fatal := c.CallXML3(context.Background(), "node-info")
	if err != nil || fatal {
		t.Fatalf("err=%v fatal=%v", err, fatal)
	}
	if root == nil {
		t.Fatal("nil root")
	}
	st := c.Stats()
	if st.Calls != 2 || st.Retries != 1 {
		t.Errorf("stats = %+v, want calls=2 retries=1", st)
	}
}

func TestClientFatalOnRawStderr(t *testing.T) {
	// ShortErr 요약본은 NPE 만 남지만 원본 stderr 에는 Connection refused 가 있다 —
	// fatal 판정은 반드시 원본 기준이어야 한다(poller.py call_xml3 주석의 실측 사례).
	bin := writeStub(t, `#!/bin/sh
cat >&2 <<'EOF'
java.lang.NullPointerException
	at com.avance.yak.cli.VolumeInfo.execute(VolumeInfo.java:59)
Caused by: java.net.ConnectException: Connection refused
EOF
`)
	c := newTestClient(bin)
	root, err, fatal := c.CallXML3(context.Background(), "volume-info")
	if root != nil || err == nil {
		t.Fatalf("root=%v err=%v", root, err)
	}
	if !fatal {
		t.Error("원본 stderr 의 'Connection refused' 로 fatal=true 여야 한다")
	}
	// 요약본은 첫 라인(NPE) — fatal 정규식에 안 걸리지만 판정에는 영향 없음
	if !strings.Contains(err.Error(), "NullPointerException") {
		t.Errorf("err = %q", err)
	}
	st := c.Stats()
	if st.Calls != 1 || st.Retries != 0 {
		t.Errorf("fatal 은 재시도하지 않는다: %+v", st)
	}
	if st.Errors != 1 || st.LastError == "" {
		t.Errorf("stats = %+v", st)
	}
}

func TestClientTimeout(t *testing.T) {
	bin := writeStub(t, "#!/bin/sh\nexec sleep 30\n")
	c := newTestClient(bin)
	c.Timeout = 1 * time.Second
	_, err, fatal := c.CallXML3(context.Background(), "node-info")
	if err == nil || !strings.Contains(err.Error(), "timeout after 1s") {
		t.Errorf("err = %v", err)
	}
	if !fatal {
		t.Error("'timeout after' 는 fatal 정규식에 걸린다")
	}
	if st := c.Stats(); st.Calls != 1 {
		t.Errorf("fatal(타임아웃)은 재시도 없음: %+v", st)
	}
}

func TestClientBinaryNotFound(t *testing.T) {
	c := newTestClient(filepath.Join(t.TempDir(), "no-such-avcli"))
	_, err, fatal := c.CallXML3(context.Background(), "node-info")
	if err == nil || !strings.Contains(err.Error(), "binary not found") {
		t.Errorf("err = %v", err)
	}
	if !fatal {
		t.Error("binary not found 는 fatal")
	}
}

func TestClientContextCancel(t *testing.T) {
	bin := writeStub(t, "#!/bin/sh\nexec sleep 30\n")
	c := newTestClient(bin)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err, _ := c.CallXML3(ctx, "node-info")
	if err == nil {
		t.Fatal("context cancel 은 에러여야 한다")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("취소가 즉시 반영되지 않음: %v", time.Since(start))
	}
}

func TestClientCallText(t *testing.T) {
	bin := writeStub(t, `#!/bin/sh
printf '  -> Community : public\nUptime : 1234\n'
`)
	c := newTestClient(bin)
	m, err := c.CallText(context.Background(), "snmp-info")
	if err != nil {
		t.Fatal(err)
	}
	if m["Community"] != "public" || m["Uptime"] != "1234" {
		t.Errorf("m = %#v", m)
	}

	// 빈 stdout 은 에러
	bin2 := writeStub(t, "#!/bin/sh\nexit 0\n")
	c2 := newTestClient(bin2)
	if _, err := c2.CallText(context.Background(), "snmp-info"); err == nil {
		t.Error("빈 텍스트 응답은 에러")
	}
}

func TestPerClusterLockShared(t *testing.T) {
	// Client 인스턴스가 달라도 같은 mgmt IP 면 같은 락(0바이트 응답 쿼크 대응)
	a := lockFor("10.1.1.1")
	b := lockFor("10.1.1.1")
	other := lockFor("10.1.1.2")
	if a != b {
		t.Error("같은 mgmt 는 같은 락이어야 한다")
	}
	if a == other {
		t.Error("다른 mgmt 는 다른 락")
	}
}

func TestShortErr(t *testing.T) {
	if got := ShortErr(""); got != "" {
		t.Errorf("empty = %q", got)
	}
	// <Error> 태그 우선
	if got := ShortErr("junk <Error>bad credentials</Error> tail"); got != "bad credentials" {
		t.Errorf("Error tag = %q", got)
	}
	// Exception 라인
	st := "java.lang.NullPointerException: boom\n\tat com.avance.X.y(X.java:1)\n"
	if got := ShortErr(st); got != "java.lang.NullPointerException: boom" {
		t.Errorf("exception = %q", got)
	}
	// SEVERE 라인
	st = "INFO: startup\nSEVERE: Connection refused\n\tat x.y(z:1)\n"
	if got := ShortErr(st); got != "SEVERE: Connection refused" {
		t.Errorf("SEVERE = %q", got)
	}
	// 매칭 없으면 첫 비어있지 않은 줄
	if got := ShortErr("\n\n  plain failure line\nmore\n"); got != "plain failure line" {
		t.Errorf("first line = %q", got)
	}
	// 200자 절삭
	long := strings.Repeat("x", 300)
	if got := ShortErr(long); len([]rune(got)) != 200 {
		t.Errorf("truncate = %d runes", len([]rune(got)))
	}
}

func TestIsFatalErr(t *testing.T) {
	for _, s := range []string{
		"invalid credentials", "Login failed", "connect timed out",
		"java.net.ConnectException: Connection refused", "no route to host",
		"unknown host", "timeout after 90s", "avcli binary not found: avcli",
	} {
		if !IsFatalErr(s) {
			t.Errorf("fatal 이어야 함: %q", s)
		}
	}
	for _, s := range []string{"", "java.lang.NullPointerException", "SEVERE: session busy"} {
		if IsFatalErr(s) {
			t.Errorf("fatal 아님: %q", s)
		}
	}
}
