package topology

import (
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// 알림(alert) 분류 및 대상 객체 추정
// ---------------------------------------------------------------------------
// avcli alert-info 는 대상 객체 id 를 주지 않는다. description 문자열에서 추정한다.
// 결과는 evidence="alert-text" 로 표시된다.

// alertTargetPattern 은 (정규식, 대상타입, 캡처그룹인덱스) 다.
// 구분자 문자클래스에 '.' 를 넣으면 안 된다: 대상 이름 자체에 점이 흔하다
// (ztC Edge VM 은 전부 'ubuntu_Server_26.04_03' 처럼 점 포함). '.' 를 구분자로 두면
// 비탐욕 \S+? 가 첫 점에서 멈춰 이름이 잘리고(→그래프 라벨 매칭 실패), 문장 끝의
// 마침표는 TrimRight(".,:;") 가 이미 떼어낸다. 그래서 구분자는 공백/쉼표만.
var alertTargetPatterns = []struct {
	rx  *regexp.Regexp
	typ string
	gi  int
}{
	{regexp.MustCompile(`(?i)\bon node (\S+)`), "node", 1},
	{regexp.MustCompile(`(?i)\bNode (\S+?)[\s,]`), "node", 1},
	{regexp.MustCompile(`(?i)\bNode (\S+)$`), "node", 1},
	{regexp.MustCompile(`(?i)\bVM (\S+?)[\s,]`), "vm", 1},
	{regexp.MustCompile(`(?i)\bVM (\S+)$`), "vm", 1},
	// node/vm/volume 과 같은 형태의 일반 규칙. 동사 화이트리스트(is|reports|currently)를
	// 쓰면 "Network P1 has lost connectivity with the intranet." 의 has 를 못 받아
	// 알림이 클러스터 폴백으로 떨어진다. seen 셋이 중복을 걸러준다.
	{regexp.MustCompile(`(?i)\b(?:shared)?network (\S+?)[\s,]`), "sharednetwork", 1},
	{regexp.MustCompile(`(?i)\b(?:shared)?network (\S+)$`), "sharednetwork", 1},
	{regexp.MustCompile(`(?i)\b(?:business|BUSINESS) network (\S+?)[\s,]`), "sharednetwork", 1},
	{regexp.MustCompile(`(?i)\bVolume (\S+?)[\s,]`), "volume", 1},
	{regexp.MustCompile(`(?i)\bVolume (\S+)$`), "volume", 1},
	{regexp.MustCompile(`(?i)\b(\S+) localNetwork`), "nic", 1},
	{regexp.MustCompile(`(?i)\bQuorum server (\d{1,3}(?:\.\d{1,3}){3})`), "quorum", 1},
}

// 키워드 기반 심각도 보정 (severity 숫자만으로는 신뢰 불가 — 조사 계약 참조)
var alertCriticalKW = []string{
	"unreachable", "offline", "failed", "failure", "down", "lost", "miswired",
	"rebooted unexpectedly", "simplex", "single pm", "not redundant", "broken",
}

var alertDegradedKW = []string{
	"degraded", "syncing", "sync", "slow", "disconnected", "uncabled",
	"too few", "maintenance", "mismatch",
}

var alertInfoKW = []string{
	"not enabled", "not configured", "notification",
}

// AlertTarget 은 알림 문자열에서 추정한 대상 객체 후보다.
type AlertTarget struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Evidence string `json:"evidence"`
}

// ClassifyAlert 는 알림을 (심각도, 근거문자열) 로 분류한다.
// severity 숫자(0/1/2, 의미 미확정)보다 name+description 키워드를 우선한다.
func ClassifyAlert(a AlertInput) (string, string) {
	blob := strings.ToLower(a.Name) + " " + strings.ToLower(a.Description)
	sev := strings.TrimSpace(a.Severity)

	for _, kw := range alertInfoKW {
		if strings.Contains(blob, kw) {
			return "warning", "keyword:" + kw
		}
	}
	for _, kw := range alertCriticalKW {
		if strings.Contains(blob, kw) {
			return "critical", "keyword:" + kw
		}
	}
	for _, kw := range alertDegradedKW {
		if strings.Contains(blob, kw) {
			return "degraded", "keyword:" + kw
		}
	}
	// 키워드 미매치 시에만 숫자 severity 사용 (0=심각으로 관측)
	switch sev {
	case "0":
		return "degraded", "severity:0"
	case "1":
		return "warning", "severity:1"
	case "2":
		return "warning", "severity:2"
	}
	return "unknown", "unclassified"
}

// ExtractAlertTargets 는 name/description 에서 (타입, 이름) 후보 목록을 중복 없이 추출한다.
//
// name 과 description 을 이어 붙인 텍스트만 훑으면 비중첩 스캔 때문에
// 진짜 대상을 놓친다. 실측: "Detection of Bad Network" + "Network P1 has lost ..."
// 을 이으면 첫 매치가 'Network Network' 를 통째로 소비해 P1 이 사라진다.
// 그래서 각 조각과 이어붙인 텍스트를 모두 훑고 seen 셋으로 중복만 거른다.
// (해석되지 않는 이름은 applyAlerts 의 gid 조회에서 그냥 버려진다.)
func ExtractAlertTargets(a AlertInput) []AlertTarget {
	texts := []string{a.Description, a.Name, a.Name + " " + a.Description}
	var out []AlertTarget
	seen := map[[2]string]bool{}
	for _, text := range texts {
		if text == "" {
			continue
		}
		for _, p := range alertTargetPatterns {
			for _, m := range p.rx.FindAllStringSubmatch(text, -1) {
				nm := strings.TrimRight(strings.TrimSpace(m[p.gi]), ".,:;")
				if nm == "" {
					continue
				}
				key := [2]string{p.typ, nm}
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, AlertTarget{Type: p.typ, Name: nm, Evidence: "alert-text"})
			}
		}
	}
	return out
}

// nicNetAlertRe 는 알림 문자열에서 물리 NIC <-> shared-network 매핑을 뽑는다.
var nicNetAlertRe = regexp.MustCompile(`(?i)sharedNetwork\s+(\S+?)\s+.*?\b(\S+)\s+localNetwork`)

// DeriveNICMapFromAlerts 는 알림 문자열에서 물리 NIC <-> shared-network 매핑을 추출한다.
// 예: "SharedNetwork P3 currently has a disconnected or uncabled ibiz2 localNetwork"
// -> {"ibiz2": "P3"}. avcli 에 port-info 계열 명령이 없어 이 경로가
// 유일한 '근거 있는' 매핑이다(설정 매핑이 없을 때).
func DeriveNICMapFromAlerts(alerts []AlertInput) map[string]string {
	out := map[string]string{}
	for _, a := range alerts {
		text := a.Name + " " + a.Description
		for _, m := range nicNetAlertRe.FindAllStringSubmatch(text, -1) {
			net := strings.TrimRight(m[1], ".,")
			out[m[2]] = net
		}
	}
	return out
}
