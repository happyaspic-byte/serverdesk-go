package config

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// 비밀 레지스트리.
//
// poller.py 의 register_secret/mask 포트. 설정 파일에서 읽은 자격증명이 로그나
// API 응답에 그대로 나가는 것을 막는다. 3자 미만은 등록하지 않는다 — "ab" 같은
// 짧은 값을 등록하면 정상 로그 문자열이 "***" 로 도배된다(poller.py 와 동일 기준).
var (
	secretsMu sync.RWMutex
	secretSet = map[string]bool{}
	secrets   []string // 길이 내림차순 — 겹치는 비밀이 있을 때 긴 쪽을 먼저 지운다
)

var (
	// 등록 누락에 대한 방어선: 명령행 `-p <값>` 과 SSH_ASKPASS 환경변수 `SSH_PW=<값>`
	// 패턴은 값이 무엇이든 지운다(poller.py mask() 와 동일).
	rePArg   = regexp.MustCompile(`(-p\s+)\S+`)
	reSSHPW  = regexp.MustCompile(`(SSH_PW=)\S+`)
	maskRepl = "${1}***"
)

// RegisterSecret 은 마스킹 대상 비밀 문자열을 등록한다. 3자 미만·빈 값은 무시.
func RegisterSecret(s string) {
	if len(s) < 3 {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	if secretSet[s] {
		return
	}
	secretSet[s] = true
	secrets = append(secrets, s)
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
}

// Mask 는 로그로 나갈 문자열에서 등록된 비밀 값을 "***" 로 치환한다.
// 혹시 남은 `-p <값>` / `SSH_PW=<값>` 패털도 방어적으로 제거한다.
func Mask(text string) string {
	if text == "" {
		return text
	}
	secretsMu.RLock()
	snapshot := make([]string, len(secrets))
	copy(snapshot, secrets)
	secretsMu.RUnlock()

	out := text
	for _, s := range snapshot {
		if strings.Contains(out, s) {
			out = strings.ReplaceAll(out, s, "***")
		}
	}
	out = rePArg.ReplaceAllString(out, maskRepl)
	out = reSSHPW.ReplaceAllString(out, maskRepl)
	return out
}
