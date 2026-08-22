package topology

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 상태 판정 규칙 (R1~R4, R6~R10 의 개별 객체 판정부)
// ---------------------------------------------------------------------------

// nodeStatus 는 물리 노드의 권위 상태를 판정한다 (R6/R7/R8 의 노드 측).
func nodeStatus(nd NodeInput) (string, []string) {
	state := strings.ToLower(nd.State)
	standing := strings.ToLower(nd.StandingState)
	mode := strings.ToLower(nd.Mode)
	var reasons []string
	st := "ok"
	if state != "running" {
		st = "critical"
		s := state
		if s == "" {
			s = "?"
		}
		reasons = append(reasons, fmt.Sprintf("노드 state=%s (running 아님)", s))
	}
	if mode != "" && mode != "normal" {
		st = StatusMax(st, "warning")
		reasons = append(reasons, fmt.Sprintf("노드 mode=%s (유지보수 등 비정상 모드)", mode))
	}
	if standing != "" && standing != "normal" {
		st = StatusMax(st, "degraded")
		reasons = append(reasons, fmt.Sprintf("노드 standing-state=%s", standing))
	}
	if state == "" {
		st = StatusMax(st, "unknown")
	}
	return st, reasons
}

// vmRedundancy 는 VM 이중화를 판정한다 (R1/R2).
// 반환: (redundancy_state, status, reasons)
func vmRedundancy(vm VMInput) (string, string, []string) {
	insts := vm.Instances
	var enabled, badMTBF []VMInstanceInput
	for _, i := range insts {
		if strings.ToUpper(i.EnableStatus) == "ENABLED" {
			enabled = append(enabled, i)
		}
		mtbf := i.MTBF
		if mtbf == "" {
			mtbf = "normal"
		}
		if strings.ToLower(mtbf) != "normal" {
			badMTBF = append(badMTBF, i)
		}
	}
	var reasons []string
	var rstate, st string
	switch {
	case len(insts) >= 2 && len(enabled) >= 2 && len(badMTBF) == 0:
		rstate, st = "protected", "ok"
	case len(enabled) == 1:
		rstate, st = "simplex", "degraded"
		reasons = append(reasons, "로컬 VM 인스턴스 중 1개만 ENABLED — 이중화 상실(심플렉스)")
	case len(enabled) == 0:
		rstate, st = "unprotected", "critical"
		reasons = append(reasons, "ENABLED 로컬 VM 인스턴스 없음")
	default:
		rstate, st = "protected", "ok"
	}
	if len(badMTBF) > 0 {
		st = StatusMax(st, "degraded")
		reasons = append(reasons, fmt.Sprintf("mtbf 상태 비정상 인스턴스 %d개", len(badMTBF)))
	}
	return rstate, st, reasons
}

// vmStatus 는 VM 의 실행 상태를 판정한다.
func vmStatus(vm VMInput) (string, []string) {
	state := strings.ToLower(vm.State)
	standing := strings.ToLower(vm.StandingState)
	var reasons []string
	st := "ok"
	if state == "stopped" || state == "shutoff" || state == "shut off" {
		st = "warning"
		reasons = append(reasons, fmt.Sprintf("VM 정지 상태(state=%s)", state))
	} else if state != "" && state != "running" {
		st = "degraded"
		reasons = append(reasons, fmt.Sprintf("VM state=%s", state))
	}
	if standing != "" && standing != "normal" {
		st = StatusMax(st, "degraded")
		reasons = append(reasons, fmt.Sprintf("VM standing-state=%s", standing))
	}
	return st, reasons
}

// volumeMirrorStatus 는 볼륨 미러 상태를 판정한다 (R3/R4).
// 반환: (mirror_state, status, reasons)
func volumeMirrorStatus(vol VMVolumeInput, syncing bool) (string, string, []string) {
	imgs := vol.DiskImages
	var enabled []DiskImageInput
	for _, i := range imgs {
		if strings.ToUpper(i.EnableStatus) == "ENABLED" {
			enabled = append(enabled, i)
		}
	}
	var reasons []string
	switch {
	case len(imgs) == 0:
		return "unknown", "unknown", []string{"디스크 이미지 정보 없음"}
	case len(enabled) >= 2 && !syncing:
		return "mirrored", "ok", reasons
	case len(enabled) >= 2 && syncing:
		return "syncing", "warning", []string{"유닛 동기화 진행 중(unit syncing=true)"}
	case len(enabled) == 1:
		return "simplex", "degraded", []string{"디스크 이미지 1개만 ENABLED — 미러 이중화 상실"}
	}
	return "broken", "critical", []string{"ENABLED 디스크 이미지 없음"}
}

// sgStatus 는 스토리지 그룹 상태를 판정한다 (R10).
// 반환: (status, reasons, usedPct). 용량 정보가 없으면 디스크 점검 없이 즉시 unknown.
func sgStatus(sg StorageGroupInput) (string, []string, *float64) {
	u := Pct(sg.UsedBytes, sg.SizeBytes)
	var reasons []string
	if u == nil {
		return "unknown", []string{"스토리지 그룹 용량 정보 없음"}, nil
	}
	st := "ok"
	if *u >= sgCritPct {
		st = "critical"
		reasons = append(reasons, fmt.Sprintf("스토리지 그룹 사용률 %.1f%% (>=%d%%)", *u, sgCritPct))
	} else if *u >= sgWarnPct {
		st = "warning"
		reasons = append(reasons, fmt.Sprintf("스토리지 그룹 사용률 %.1f%% (>=%d%%)", *u, sgWarnPct))
	}
	for _, d := range sg.Disks {
		ss := strings.ToLower(d.StandingState)
		if ss != "" && ss != "normal" {
			st = StatusMax(st, "degraded")
			reasons = append(reasons, fmt.Sprintf("논리디스크 %s(%s) standing-state=%s", d.Name, d.Node, ss))
		}
	}
	return st, reasons, u
}

// networkStatus 는 shared-network 상태를 판정한다.
func networkStatus(net NetworkInput) (string, []string) {
	ft := strings.ToLower(net.FaultTolerant)
	if ft != "" && ft != "ft" && ft != "ha" {
		return "degraded", []string{fmt.Sprintf("shared-network fault-tolerant=%s", ft)}
	}
	return "ok", nil
}
