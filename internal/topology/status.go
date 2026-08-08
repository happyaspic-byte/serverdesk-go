// Package topology 는 everRun / ztC Edge 폴러의 정규화 데이터를
// 프런트가 바로 그릴 수 있는 10단계 토폴로지 그래프 JSON 으로 변환한다.
//
// everrun-poller 프로젝트의 topology.py / topology_adapter.py 를 Go 로 이식한
// 것으로, 출력 JSON 스키마(schema_version 1.0.0)와 상태 전파 규칙 R1~R12 의
// 동작은 원본과 동일하다. 설계 문서는 everrun-poller/docs/topology-model.md.
//
// 원본과의 의도적 차이 (스키마에는 영향 없음):
//   - generated_by 값이 "serverdesk-go/internal/topology" 이다.
//   - NIC 노드 생성 순서는 노드 이름 정렬 순이다(원본은 node_metrics 삽입 순).
//     밴드 정렬 키(cluster, lane_offset, type, label)가 유일하므로 레이아웃
//     결과는 동일하다.
//   - mtu / sector size 는 int64 로 정규화한다(원본은 입력 경로에 따라
//     문자열/정수가 갈렸다).
//   - JSON 숫자 표기가 Go 규칙을 따른다(1.0 -> 1). 값은 동일하다.
package topology

import "fmt"

// SchemaVersion 은 출력 그래프 JSON 의 스키마 버전이다.
// 프런트 렌더러가 이 값으로 해석기를 고르므로, 스키마를 바꾸면 올려야 한다.
const SchemaVersion = "1.0.0"

// generatedBy 는 이 그래프를 만든 구현체 표기다.
const generatedBy = "serverdesk-go/internal/topology"

// 스토리지 그룹 임계값은 평면 토폴로지(/api/fleet)와 이 상세 모델의 색칠이
// 어긋나지 않도록 avcli_parse 와 반드시 같은 값을 유지해야 한다.
// (예전엔 여기만 90/95 라 사용률 85~89.9% 구간에서 상세 모델만 ok 로 나왔다.)
const (
	sgWarnPct = 85
	sgCritPct = 95
)

// ---------------------------------------------------------------------------
// 상태(status) 모델
// ---------------------------------------------------------------------------
// 모든 그래프 노드/엣지는 아래 5가지 상태 중 하나로 정규화된다.
// rank 가 클수록 심각하며, 상위 계층 롤업은 max(rank) 로 계산한다.
var statusRank = []kv{
	{"ok", 0},
	{"unknown", 1},
	{"warning", 2},
	{"degraded", 3},
	{"critical", 4},
}

var statusRankByName = map[string]int{
	"ok": 0, "unknown": 1, "warning": 2, "degraded": 3, "critical": 4,
}

var statusByRank = map[int]string{
	0: "ok", 1: "unknown", 2: "warning", 3: "degraded", 4: "critical",
}

// StatusMax 는 여러 상태 중 가장 심각한 것을 반환한다. 인자가 없으면 "unknown".
func StatusMax(statuses ...string) string {
	best := -1
	for _, s := range statuses {
		r, ok := statusRankByName[s]
		if !ok {
			r = 1 // 모르는 상태 문자열은 unknown 취급 (Python STATUS_RANK.get(s, 1))
		}
		if r > best {
			best = r
		}
	}
	if st, ok := statusByRank[best]; ok {
		return st
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// 계층(level) / 레인(lane) 정의 — 레이아웃 힌트의 근간
// ---------------------------------------------------------------------------

// levels 는 노드 타입 -> 밴드 번호다. nic/disk 가 같은 4번 밴드를 쓰는 식으로
// 타입과 1:1 이 아니므로 프런트는 type 이 아니라 level 로 밴드를 그려야 한다.
var levels = map[string]int{
	"fleet":          0,
	"site":           1,
	"cluster":        2,
	"node":           3,
	"nic":            4, // 노드 로컬 자원
	"disk":           4, // 노드별 논리 디스크 (disk:oNNN)
	"sharednetwork":  5,
	"storagegroup":   5,
	"quorum":         5,
	"vm":             6,
	"localvm":        7, // VM 의 노드별 인스턴스 (localvirtualmachine:oNNN)
	"volume":         7,
	"diskimage":      8, // 볼륨의 노드별 미러 조각
	"imagecontainer": 9,
}

// levelLabelsKO 는 밴드 헤더용 한글 라벨이다. level 오름차순을 유지한다.
var levelLabelsKO = []LevelInfo{
	{0, "플릿"},
	{1, "사이트"},
	{2, "클러스터"},
	{3, "물리 노드"},
	{4, "노드 로컬 자원 (NIC / 논리디스크)"},
	{5, "공유 패브릭 (네트워크 / 스토리지그룹 / 쿼럼)"},
	{6, "가상 머신"},
	{7, "VM 인스턴스 / 가상 볼륨"},
	{8, "디스크 이미지 (미러 조각)"},
	{9, "이미지 컨테이너 (실사용량)"},
}

// LaneShared 는 공유 패브릭 레인이다. 좌(node0)/중앙(shared)/우(node1) 구도의 중앙.
const LaneShared = "shared"

// 레이아웃 상수 (프런트가 그대로 써도 되고, 힌트로만 써도 된다)
const (
	// LevelGapY 는 인접 밴드의 세로 간격(px)이다.
	LevelGapY = 130
	// NodeGapX 는 같은 밴드 안 노드의 가로 간격(px)이다.
	NodeGapX = 190
)

// ---------------------------------------------------------------------------
// 엣지 종류(kind)
// ---------------------------------------------------------------------------

// edgeKinds 는 엣지 종류 -> 설명 사전이다. 출력에 그대로 실리므로 순서를 유지한다.
var edgeKinds = []kv{
	{"contains", "계층 포함 (fleet>site>cluster>node 등 트리 간선)"},
	{"ft-pair", "FT/HA 페어를 이루는 두 노드 사이의 논리적 짝"},
	{"placement", "노드 -> VM 배치/구동 (FT 는 lockstep, HA 는 active/standby)"},
	{"resides-on", "VM 로컬 인스턴스 / 디스크 이미지가 실제로 올라가 있는 물리 노드"},
	{"instance-of", "VM <- 노드별 로컬 인스턴스"},
	{"attaches", "VM <- 가상 볼륨(vbd) 연결"},
	{"mirror", "볼륨 <- 노드별 디스크 이미지 (미러 조각)"},
	{"stored-on", "볼륨/디스크이미지 -> 스토리지 그룹"},
	{"member-of", "논리 디스크 -> 스토리지 그룹"},
	{"vnic", "VM -> shared-network (가상 NIC)"},
	{"uplink", "물리 NIC -> shared-network"},
	{"backs", "이미지 컨테이너 -> 볼륨 (실사용량 소스)"},
	{"quorum", "클러스터 -> 쿼럼 서버"},
}

// ---------------------------------------------------------------------------
// 상태 전파 규칙 (UI 툴팁/문서화용으로 그래프 JSON 에 그대로 실린다)
// ---------------------------------------------------------------------------

// propagationRules 는 R1~R12 규칙 목록이다. 실제 판정 로직은 build.go /
// propagate.go 에 있고, 이 목록은 소비자가 "왜 이 상태인가"를 설명할 때 쓴다.
var propagationRules = []RuleInfo{
	{"R1-localvm-to-vm", "local-virtual-machine 2개 중 1개가 DISABLED",
		"VM.redundancy_state=simplex, VM.status>=degraded"},
	{"R2-localvm-none", "ENABLED local-virtual-machine 이 0개",
		"VM.status=critical (보호 없음)"},
	{"R3-diskimage-to-volume", "볼륨의 disk-image 중 1개만 ENABLED",
		"Volume.mirror_state=simplex, Volume.status>=degraded"},
	{"R4-unit-syncing", "unit-info/syncing = true",
		"모든 mirror 엣지 sync_state=syncing, Cluster.status>=warning"},
	{"R5-volume-to-vm", "VM 에 붙은 볼륨이 degraded/critical",
		"VM.rollup_status 에 반영 (자식 롤업)"},
	{"R6-node-down", "노드 state != running",
		"Node.status=critical. 단, 살아있는 노드가 1개 이상이면 Cluster 는 degraded 까지만 (FT 흡수)"},
	{"R7-all-nodes-down", "모든 노드 state != running",
		"Cluster.status=critical"},
	{"R8-node-maintenance", "노드 mode != normal",
		"Node.maintenance=true, Node.status>=warning"},
	{"R9-nic-down", "물리 NIC operstate=down 이고 어떤 shared-network 에 속함",
		"NIC.status=critical -> uplink 엣지 critical -> 네트워크: 전부 다운이면 critical, 일부면 degraded"},
	{"R9b-nic-unused", "NIC operstate=down 인데 소속 shared-network 없음",
		"NIC.meta.unused=true, status=ok (케이블 미연결 예비 포트로 간주)"},
	{"R10-storage-usage", fmt.Sprintf("스토리지 그룹 사용률 >=%d%% / >=%d%%", sgWarnPct, sgCritPct),
		"warning / critical"},
	{"R11-alert-overlay", "알림 description 에서 대상 객체가 식별됨",
		"해당 그래프 노드 status 를 알림 심각도까지 상승. 미식별 시 클러스터에 부착"},
	{"R12-redundancy-damping", "감쇠 대상({cluster:node, vm:localvm, volume:diskimage}) 자식 중 정상 형제가 존재",
		"critical 자식을 degraded 로 낮춰 롤업 (이중화가 장애를 흡수)"},
}

// damping 은 R12 이중화 감쇠 대상 {부모타입: 감쇠 대상 자식타입} 이다.
// 같은 타입의 형제 중 정상인 것이 하나라도 있으면 critical 자식은 degraded 로
// 낮춰 롤업한다(이중화가 장애를 흡수). 스토리지 사용량처럼 비이중화 요소는
// 감쇠하지 않으므로 클러스터까지 critical 로 올라간다.
var damping = map[string]map[string]bool{
	"cluster": {"node": true},      // 노드 1대 장애 -> FT 짝이 흡수
	"vm":      {"localvm": true},   // 로컬 인스턴스 1개 장애 -> 심플렉스로 계속 서비스
	"volume":  {"diskimage": true}, // 미러 조각 1개 오프라인 -> 심플렉스로 계속 서비스
}

// authoritativeTypes 는 권위 있는 실시간 상태 필드를 가진 타입이다.
// 이 타입들은 알림(과거 이력 포함 가능)보다 실시간 필드를 믿는다 (§5.3).
var authoritativeTypes = map[string]bool{
	"node": true, "vm": true, "volume": true, "localvm": true,
	"diskimage": true, "sharednetwork": true, "storagegroup": true,
	"disk": true, "nic": true, "cluster": true,
}

// alertDampedCeiling 은 알림만으로 올릴 수 있는 최대 심각도다.
// alert-info 는 해소된 과거 이력도 함께 반환하므로, 실시간 상태가 ok 인 객체는
// 알림으로 최대 warning 까지만 올라간다.
const alertDampedCeiling = "warning"
