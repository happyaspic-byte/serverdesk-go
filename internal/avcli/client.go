// Package avcli 는 Stratus everRun / ztC Edge 의 avcli CLI 를 실행하고
// 그 XML 응답을 정규화 구조체로 파싱한다.
// (poller.py 의 AvcliClient + avcli_parse.py 의 Go 포트, 표준 라이브러리만 사용)
package avcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Logf 는 호스트 로거 연결 훅이다(level 은 "debug"/"warn" 등). 기본 no-op.
var Logf = func(level, cluster, msg string) {}

// 클러스터별 직렬화 락.
//
// 같은 클러스터에 avcli 를 병렬로 던지면 응답이 0바이트로 비는 사례를 실측했다
// (volume-info 가 0바이트로 돌아오고 재실행하니 정상). 벤더 측 동시접속 제한으로
// 보이므로 mgmt IP 당 하나씩만 실행한다. Client 인스턴스가 달라도 같은 클러스터면
// 같은 락을 쓰도록 패키지 전역으로 둔다.
var clusterLocks sync.Map // mgmtIP -> *sync.Mutex

func lockFor(mgmt string) *sync.Mutex {
	l, _ := clusterLocks.LoadOrStore(mgmt, &sync.Mutex{})
	return l.(*sync.Mutex)
}

// Stats 는 Client 의 호출 통계다(poller.py 의 stats dict 와 같은 키).
type Stats struct {
	Calls     int    `json:"calls"`
	Errors    int    `json:"errors"`
	Retries   int    `json:"retries"`
	LastError string `json:"last_error"`
}

// Client 는 한 FT 클러스터에 대한 avcli 실행기다.
//
// avcli 는 `-p <암호>` 외에 암호를 넘길 수단이 없어 암호가 argv 에 노출된다 —
// 반드시 config.CheckArgvExposure 로 배포 조건을 점검한 뒤 써야 한다.
// 1회 호출이 실측 15~40초(JVM 기동+로그인)이고 매 호출이 장비 감사 로그에
// 로그인 레코드를 남기므로, 호출 수를 줄이는 것이 폴링 최적화의 핵심이다.
type Client struct {
	Key        string // 클러스터 key(로그 표기용)
	Mgmt       string // 관리 IP — 직렬화 락 키로도 쓰인다
	User       string
	Bin        string        // avcli 바이너리(기본 "avcli")
	PrefixArgs []string      // avcli 실행 전에 전달할 고정 인수
	Timeout    time.Duration // 기본 90s — 1콜 실측 15~40초의 상한
	RetryDelay time.Duration // 빈 응답 재시도 대기(기본 5s)

	pw      string
	statsMu sync.Mutex
	stats   Stats

	warnMu         sync.Mutex
	lastWarnReason string // 같은 사유의 WARN 반복 억제용(상시 실패 시 60초마다 같은 로그가 쌓인다)
}

// NewClient 는 기본값(Bin "avcli", Timeout 90s, RetryDelay 5s)의 Client 를 만든다.
func NewClient(clusterKey, mgmtIP, user, password string) *Client {
	return &Client{
		Key:        clusterKey,
		Mgmt:       mgmtIP,
		User:       user,
		Bin:        "avcli",
		Timeout:    90 * time.Second,
		RetryDelay: 5 * time.Second,
		pw:         password,
	}
}

// warnOnce 는 같은 실패 사유의 WARN 을 한 번만 낸다. avcli 미설치·망 단절 같은
// 상시 실패가 티어 주기(60s)마다 동일 로그를 쌓는 것을 막는다 — 사유가 바뀌거나
// 복구되면 다시 로그한다.
func (c *Client) warnOnce(reason, msg string) {
	c.warnMu.Lock()
	defer c.warnMu.Unlock()
	if reason == c.lastWarnReason {
		return
	}
	c.lastWarnReason = reason
	Logf("warn", c.Key, msg)
}

// warnReset 은 성공 시 호출 — 실패 상태였으면 복구를 1회 알리고 억제를 푼다.
func (c *Client) warnReset(command string) {
	c.warnMu.Lock()
	defer c.warnMu.Unlock()
	if c.lastWarnReason != "" {
		Logf("info", c.Key, "avcli 복구: "+command)
		c.lastWarnReason = ""
	}
}

// Stats 는 현재 통계의 사본을 반환한다.
func (c *Client) Stats() Stats {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	return c.stats
}

func (c *Client) bumpStats(f func(*Stats)) {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	f(&c.stats)
}

// exec 는 avcli 를 실행하고 stdout/stderr 를 **분리해서** 돌려준다.
//
// 실패 시 stdout 0바이트 + stderr 스택트레이스가 계약이다. 절대 합치지 않는다 —
// stderr 를 stdout 에 섞으면 XML 파서가 오염된다. 종료코드는 보지 않는다
// (rc=0 인데 stderr 에 스택트레이스가 있는 경우가 실측됨 — poller.py 와 동일하게
// stdout 내용으로만 성공을 판정한다).
func (c *Client) exec(ctx context.Context, command string, xmlMode bool) (string, string) {
	args := make([]string, 0, len(c.PrefixArgs)+8)
	args = append(args, c.PrefixArgs...)
	args = append(args, "-H", c.Mgmt, "-u", c.User, "-p", c.pw)
	if xmlMode {
		args = append(args, "-x")
	}
	args = append(args, strings.Fields(command)...)
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := avcliCmd(ctx, c.Bin, args)
	cmd.Stdin = nil // subprocess.DEVNULL 에 해당
	// 타임아웃으로 종료한 프로세스가 stdout 파이프를 쥐고 남으면 Wait 가 EOF까지
	// 블록된다 — WaitDelay 로 상한을 둔다.
	cmd.WaitDelay = 3 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Sprintf("timeout after %ds", int(c.Timeout.Seconds()))
	}
	if ctx.Err() == context.Canceled {
		return "", "context canceled"
	}
	if err != nil {
		// PATH 검색 실패(exec.ErrNotFound)와 명시 경로 부재(ENOENT) 모두
		// "binary not found" 다(Python FileNotFoundError 에 해당).
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Sprintf("avcli binary not found: %s", c.Bin)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// 종료코드가 아닌 실행 자체 실패(권한 등)만 오류 문자열로 돌린다.
			return "", err.Error()
		}
	}
	return stdout.String(), strings.TrimSpace(stderr.String())
}

var (
	errTagRe = regexp.MustCompile(`(?s)<Error>(.*?)</Error>`)
	excRe    = regexp.MustCompile(`(?m)^\w[\w.]*Exception:.*$`)
	severeRe = regexp.MustCompile(`(?m)^SEVERE:\s*(.*)$`)
	errorRe  = regexp.MustCompile(`(?m)^ERROR:\s*(.*)$`)
)

// ShortErr 은 avcli 실패 stderr(수 KB 짜리 Java 스택트레이스)를 원인 한 줄로 줄인다.
// API 응답과 로그에는 이 한 줄만 싣는다(원문을 싣으면 응답이 수십 KB 로 불어난다).
// 200자에서 자른다. 매칭되는 것이 없으면 첫 비어있지 않은 줄.
func ShortErr(err string) string {
	if err == "" {
		return ""
	}
	if m := errTagRe.FindStringSubmatch(err); m != nil {
		return truncate(strings.TrimSpace(m[1]), 200)
	}
	for _, re := range []*regexp.Regexp{excRe, severeRe, errorRe} {
		if m := re.FindString(err); m != "" {
			return truncate(strings.TrimSpace(m), 200)
		}
	}
	for _, ln := range strings.Split(err, "\n") {
		if v := strings.TrimSpace(ln); v != "" {
			return truncate(v, 200)
		}
	}
	return ""
}

func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit])
	}
	return s
}

// fatalRe — 클러스터 전체가 못 쓰는 상태를 뜻하는 오류들. 같은 티어의 남은 명령을
// 더 던져봐야 전부 같은 이유로 실패하고 타임아웃만 누적된다(도달불가 클러스터에서
// slow 티어 7콜 = 수 분). 첫 콜에서 이 오류가 나오면 티어를 조기 종료한다.
var fatalRe = regexp.MustCompile(
	`(?i)invalid credentials|login failed|connect timed out|connection refused` +
		`|no route to host|unknown host|timeout after|binary not found`)

// IsFatalErr 은 오류 문자열이 치명(재시도·후속 콜 무의미)인지 판정한다.
//
// 반드시 **원본 stderr** 에 대고 판정해야 한다. ShortErr 요약본은 근본 원인이
// 사라지는 경우가 있다(실측: EAC 포트가 닫힌 클러스터의 stderr 는
// 'Connection refused' 를 담지만 요약본은 NullPointerException 만 남아
// IsFatalErr(요약본)=false 가 되어 조기 종료가 안 걸린다).
func IsFatalErr(err string) bool {
	return err != "" && fatalRe.MatchString(err)
}

// CallXML 은 XML 모드로 호출해 파싱된 루트를 반환한다. 실패 시 (nil, err).
//
// 치명 여부는 CallXML3 로 받아야 정확하다 — 아래 주석 참조.
func (c *Client) CallXML(ctx context.Context, command string) (*Element, error) {
	root, err, _ := c.CallXML3(ctx, command)
	return root, err
}

// CallXML3 는 (root, err, fatal) 을 반환한다.
//
// fatal 은 **원본 stderr** 기준 판정이다(IsFatalErr 주석 참조). 빈 응답은
// 세션/동시접속 이슈로 간헐 발생하므로 치명이 아닐 때만 RetryDelay 후 1회 재시도한다.
// 인증 실패·도달 불가처럼 원인이 확정된 오류는 재시도해도 같은 결과다 — 무의미한
// 재호출은 장비 감사 로그에 로그인 레코드만 더 남긴다.
func (c *Client) CallXML3(ctx context.Context, command string) (*Element, error, bool) {
	mu := lockFor(c.Mgmt)
	mu.Lock()
	t0 := time.Now()
	c.bumpStats(func(s *Stats) { s.Calls++ })
	out, rawErr := c.exec(ctx, command, true)
	if strings.TrimSpace(out) == "" && !IsFatalErr(rawErr) && ctx.Err() == nil {
		c.bumpStats(func(s *Stats) { s.Retries++ })
		Logf("debug", c.Key, "빈 응답, 재시도: "+command)
		select {
		case <-ctx.Done():
			rawErr = "context canceled"
		case <-time.After(c.RetryDelay):
			if ctx.Err() == nil {
				c.bumpStats(func(s *Stats) { s.Calls++ })
				out, rawErr = c.exec(ctx, command, true)
			}
		}
	}
	dur := time.Since(t0).Round(100 * time.Millisecond)
	fatal := IsFatalErr(rawErr)
	mu.Unlock()

	root, perr := ParseXML(out)
	if perr != nil {
		reason := ShortErr(rawErr)
		if reason == "" {
			reason = perr.Error()
		}
		err := fmt.Errorf("%s: %s", command, reason)
		c.bumpStats(func(s *Stats) {
			s.Errors++
			s.LastError = err.Error()
		})
		c.warnOnce(reason, fmt.Sprintf("avcli 실패 %s (%.1fs): %s — 같은 사유의 반복 로그는 생략", command, dur.Seconds(), reason))
		return nil, err, fatal
	}
	c.warnReset(command)
	Logf("debug", c.Key, fmt.Sprintf("avcli ok %s (%.1fs)", command, dur.Seconds()))
	return root, nil, false
}

// CallText 는 텍스트 모드 폴백(-x 없이)이다.
// snmp-info 처럼 XML 생성이 깨지는 명령용. 반환 map 은 ParseTextKV 결과다.
func (c *Client) CallText(ctx context.Context, command string) (map[string]any, error) {
	mu := lockFor(c.Mgmt)
	mu.Lock()
	out, rawErr := c.exec(ctx, command, false)
	mu.Unlock()
	if strings.TrimSpace(out) == "" {
		if rawErr == "" {
			rawErr = "empty"
		}
		return nil, errors.New(rawErr)
	}
	return ParseTextKV(out), nil
}
