package topology

import (
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// NIC <-> shared-network 매핑 추론
// ---------------------------------------------------------------------------
// avcli 에 port-info 계열 명령이 없어 물리 NIC-공유네트워크 매핑을 직접 얻을 수 없다.
// 우선순위: (1) 입력의 명시 매핑(nic_network_map) (2) 알림 문자열 근거
// (3) 이름 규칙 휴리스틱 (4) 개수 일치 짝짓기.

// nicRoleHeuristic 은 (정규식, kind, role) 규칙이다.
// kind: physical | bridge | guest-tap | ignore. role 은 "" 이면 '이름으로 단정 불가'.
var nicRoleHeuristic = []struct {
	rx   *regexp.Regexp
	kind string
	role string
}{
	{regexp.MustCompile(`(?i)^(priv|alink|sync)\d*$`), "physical", "a-link"},
	{regexp.MustCompile(`(?i)^ibiz\d*$`), "physical", "business"},
	// 일반 커널 NIC 이름(p38p2/eth0/enp3s0...)은 물리 포트라는 것만 확실하고 역할은
	// 알 수 없다. 예전엔 business 로 단정해 everRun 의 A-Link p38p2(MTU 9000,
	// 실제 소속 net_82)가 유일한 business 네트워크 network0 에 붙어버렸다.
	{regexp.MustCompile(`(?i)^(p\d+p\d+|eth|eno|enp|ens|em)\w*$`), "physical", ""},
	{regexp.MustCompile(`(?i)^(biz|network|br|virbr)\w*$`), "bridge", "business"},
	{regexp.MustCompile(`(?i)^(vnet|tap|macvtap)\w*$`), "guest-tap", ""},
	{regexp.MustCompile(`(?i)^(lo|docker|veth)\w*$`), "ignore", ""},
}

// nicKindsIncluded 는 물리 토폴로지에 포함할 kind 다(bridge/tap 은 노이즈라 제외).
const nicKindPhysical = "physical"

// GuessNICRole 은 인터페이스 이름을 (kind, role) 로 추정한다.
// 실장비 실측 이름 기준 휴리스틱이며, role 이 "" 이면 이름만으로는 단정할 수 없다는 뜻이다.
func GuessNICRole(ifname string) (string, string) {
	for _, h := range nicRoleHeuristic {
		if h.rx.MatchString(ifname) {
			return h.kind, h.role
		}
	}
	return "unknown", "unknown"
}

// heuristicNetForNIC 은 이름 규칙으로 NIC 가 붙을 shared-network 를 추정한다(신뢰도 낮음).
// 역할이 불확실하면 추측하지 오정보보다 미표시가 낫다). 못 찾으면 "".
func heuristicNetForNIC(ifname, role string, networks []NetworkInput, mtu *int64) string {
	if ifname == "" {
		return ""
	}
	up := strings.ToUpper(ifname)
	// ztC Edge: 물리 포트명 A1/A2/P1..P3 이 곧 shared-network 이름
	for _, net := range networks {
		if strings.ToUpper(net.Name) == up {
			return net.Name
		}
	}
	// 이름으로 역할을 모를 때 MTU 로 보정한다(a-link 9000 / business 1500).
	if role == "" && mtu != nil && *mtu != 0 {
		roles := map[string]bool{}
		for _, n := range networks {
			if n.MTU != nil && *n.MTU == *mtu {
				roles[strings.ToLower(n.Role)] = true
			}
		}
		if len(roles) == 1 {
			for r := range roles {
				role = r
			}
		}
	}
	if role == "" {
		return ""
	}
	// everRun: 역할만 일치시키고 후보가 유일할 때만 연결
	var cands []string
	for _, n := range networks {
		if strings.ToLower(n.Role) == role {
			cands = append(cands, n.Name)
		}
	}
	if len(cands) == 1 {
		return cands[0]
	}
	return ""
}

// ordinalNICMap 은 같은 역할(a-link/business)의 물리 NIC 개수와 shared-network
// 개수가 정확히 같을 때만 이름순으로 1:1 짝지어 준다. 개수가 다르면 추측하지
// 않는다(오정보 방지).
func ordinalNICMap(physLinks []LinkInput, networks []NetworkInput) map[string]string {
	byRoleNIC := map[string][]string{}
	var roleOrder []string
	for _, l := range physLinks {
		_, role := GuessNICRole(l.Name)
		if role == "" {
			// Python 의 role=None 키와 네트워크 빈 역할("")이 우연히 매칭되는 것을 막는다
			continue
		}
		if _, ok := byRoleNIC[role]; !ok {
			roleOrder = append(roleOrder, role)
		}
		byRoleNIC[role] = append(byRoleNIC[role], l.Name)
	}
	byRoleNet := map[string][]string{}
	for _, n := range networks {
		r := strings.ToLower(n.Role)
		byRoleNet[r] = append(byRoleNet[r], n.Name)
	}
	out := map[string]string{}
	for _, role := range roleOrder {
		nics := byRoleNIC[role]
		nets := byRoleNet[role]
		if len(nets) > 0 && len(nets) == len(nics) {
			sort.Strings(nics)
			sort.Strings(nets)
			for i := range nics {
				out[nics[i]] = nets[i]
			}
		}
	}
	return out
}

// normName 은 이름 매칭용 정규화다. internal-name 은 점이 제거되므로
// 특수문자를 모두 없앤다.
var normNameRe = regexp.MustCompile(`[^0-9A-Za-z_]`)

func normName(s string) string {
	return normNameRe.ReplaceAllString(s, "")
}

// matchContainerToVolume 은 image-container 이름('<internal-name>_<볼륨역할>_<uuid>')
// 에서 볼륨 gid 를 찾는다. 정식 id 참조가 없어 이름 접두 매칭이 유일한 조인 수단이며
// 신뢰도는 낮다. 이름이 중복되는 시스템 볼륨(root/swap/diagdata)은 모호하므로 연결하지 않는다.
func matchContainerToVolume(containerName string, volNameIndex map[string][]string) string {
	if containerName == "" {
		return ""
	}
	cn := normName(containerName)
	best := ""
	bestLen := 0
	for vname, gids := range volNameIndex {
		if vname == "" || len(gids) != 1 {
			continue // 동명 볼륨이 여러 개면 조인 불가
		}
		vn := normName(vname)
		if vn == "" {
			continue
		}
		if cn == vn || strings.HasPrefix(cn, vn+"_") {
			if len(vn) > bestLen {
				best, bestLen = gids[0], len(vn)
			}
		}
	}
	return best
}
