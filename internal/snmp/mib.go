package snmp

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// MIB 파서 — TRAP-TYPE / OBJECT-TYPE / OBJECT IDENTIFIER 에서 OID↔이름 추출.
// 파이썬 trap_receiver.py 의 parse_mib 포팅. Stratus MIB 가 SMIv1 TRAP-TYPE
// 매크로를 쓰므로 완전한 ASN.1 파서 없이 정규식으로 필요한 관계만 뽑는다.

// SMI 숫자 루트 시드. 나머지는 MIB 의 { parent N } 관계로부터 반복 해석한다.
var oidSeed = map[string]string{
	"iso":         "1",
	"org":         "1.3",
	"dod":         "1.3.6",
	"internet":    "1.3.6.1",
	"private":     "1.3.6.1.4",
	"enterprises": "1.3.6.1.4.1",
}

var (
	reOIDNode = regexp.MustCompile(
		`(?m)^\s*([A-Za-z][\w-]*)\s+OBJECT\s+IDENTIFIER\s*::=\s*\{\s*([A-Za-z][\w-]*)\s+(\d+)\s*\}`)
	reObjType    = regexp.MustCompile(`([A-Za-z][\w-]*)\s+OBJECT-TYPE\b`)
	reTrapType   = regexp.MustCompile(`([A-Za-z][\w-]*)\s+TRAP-TYPE\b`)
	reNotifType  = regexp.MustCompile(`([A-Za-z][\w-]*)\s+NOTIFICATION-TYPE\b`)
	reAssignBr   = regexp.MustCompile(`::=\s*\{\s*([A-Za-z][\w-]*)\s+(\d+)\s*\}`)
	reAssignNum  = regexp.MustCompile(`::=\s*(\d+)`)
	reEnterprise = regexp.MustCompile(`ENTERPRISE\s+([A-Za-z][\w-]*)`)
	reObjects    = regexp.MustCompile(`OBJECTS\s*\{([^}]*)\}`)
	reVariables  = regexp.MustCompile(`VARIABLES\s*\{([^}]*)\}`)
	reMIBName    = regexp.MustCompile(`([A-Za-z][\w-]*)\s+DEFINITIONS\b`)
	reCommentLn  = regexp.MustCompile(`^\s*--`)
)

// TrapInfo 는 트랩 OID 1개에 대한 MIB 메타데이터다.
type TrapInfo struct {
	Name      string   // TRAP-TYPE / NOTIFICATION-TYPE 매크로 이름
	Variables []string // VARIABLES/OBJECTS 절의 varbind 이름 목록
	MIB       string   // 정의된 MIB 모듈명
}

// MIBMap 은 MIB 텍스트 1개에서 추출한 OID↔이름 / 트랩 표다(파이썬 parse_mib 반환값).
type MIBMap struct {
	MIB       string
	OIDToName map[string]string
	NameToOID map[string]string
	Traps     map[string]TrapInfo
}

// stripFullLineComments 는 전체가 주석인 줄(^\s*--)만 제거한다.
// DESCRIPTION 문자열 안의 인라인 '--' 는 구조 토큰(::=, {}, ENTERPRISE,
// VARIABLES)과 겹치지 않으므로 보존한다(파이썬 동명 함수와 동일 규칙).
func stripFullLineComments(text string) string {
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, ln := range lines {
		if !reCommentLn.MatchString(ln) {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// splitCSV 는 VARIABLES { a, b, c } 절의 이름 목록을 자른다.
func splitCSV(s string) []string {
	var out []string
	for _, v := range strings.Split(strings.ReplaceAll(s, "\n", " "), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ParseMIB 는 MIB 텍스트 1개를 파싱해 OID↔이름 표와 트랩 표를 만든다.
// SMIv1 TRAP-TYPE(everRun/ztC MIB 가 사용) + SMIv2 NOTIFICATION-TYPE(호환) 지원.
func ParseMIB(text string) *MIBMap {
	text = stripFullLineComments(text)
	m := &MIBMap{
		OIDToName: map[string]string{},
		NameToOID: map[string]string{},
		Traps:     map[string]TrapInfo{},
	}
	if mm := reMIBName.FindStringSubmatch(text); mm != nil {
		m.MIB = mm[1]
	}

	// 1) 이름 → { parent, N } 관계 수집 (OBJECT IDENTIFIER + OBJECT-TYPE)
	type rel struct {
		parent string
		sub    int
	}
	rels := map[string]rel{}
	for _, mm := range reOIDNode.FindAllStringSubmatch(text, -1) {
		sub, _ := strconv.Atoi(mm[3])
		rels[mm[1]] = rel{mm[2], sub}
	}
	for _, loc := range reObjType.FindAllStringSubmatchIndex(text, -1) {
		name := text[loc[2]:loc[3]]
		end := loc[1] + 4000
		if end > len(text) {
			end = len(text)
		}
		tail := text[loc[1]:end]
		if am := reAssignBr.FindStringSubmatch(tail); am != nil {
			if _, dup := rels[name]; !dup { // 파이썬 setdefault 와 동일 — 첫 정의 우선
				sub, _ := strconv.Atoi(am[2])
				rels[name] = rel{am[1], sub}
			}
		}
	}

	// 2) 반복 해석으로 이름 → 숫자 OID 확정
	resolved := map[string]string{}
	for k, v := range oidSeed {
		resolved[k] = v
	}
	for i := 0; i < 40; i++ {
		progressed := false
		for name, r := range rels {
			if _, ok := resolved[name]; ok {
				continue
			}
			if base, ok := resolved[r.parent]; ok && base != "" {
				resolved[name] = base + "." + strconv.Itoa(r.sub)
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	for name, oid := range resolved {
		if _, seed := oidSeed[name]; seed {
			continue
		}
		m.OIDToName[oid] = name
		m.NameToOID[name] = oid
	}

	regTrap := func(trapOID, name string, variables []string) {
		m.Traps[trapOID] = TrapInfo{Name: name, Variables: variables, MIB: m.MIB}
		if _, ok := m.OIDToName[trapOID]; !ok {
			m.OIDToName[trapOID] = name
		}
		if _, ok := m.NameToOID[name]; !ok {
			m.NameToOID[name] = trapOID
		}
	}

	// 3) 트랩 추출 — SMIv1 TRAP-TYPE
	for _, loc := range reTrapType.FindAllStringSubmatchIndex(text, -1) {
		name := text[loc[2]:loc[3]]
		end := loc[1] + 4000
		if end > len(text) {
			end = len(text)
		}
		block := text[loc[1]:end]
		num := reAssignNum.FindStringSubmatchIndex(block)
		ent := reEnterprise.FindStringSubmatch(block)
		if num == nil || ent == nil {
			continue
		}
		spec, _ := strconv.Atoi(block[num[2]:num[3]])
		entOID, ok := resolved[ent[1]]
		if !ok {
			continue
		}
		var variables []string
		if vm := reVariables.FindStringSubmatch(block[:num[0]]); vm != nil {
			variables = splitCSV(vm[1])
		}
		// RFC 3584: v1 트랩을 v2c 로 보낼 때 snmpTrapOID = enterprise.0.specific.
		// 발신 에이전트에 따라 '.0.' 이 없을 수도 있어 두 형태 모두 이름에 매핑한다.
		regTrap(entOID+".0."+strconv.Itoa(spec), name, variables)
		plain := entOID + "." + strconv.Itoa(spec)
		if _, ok := m.Traps[plain]; !ok {
			m.Traps[plain] = TrapInfo{Name: name, Variables: variables, MIB: m.MIB}
			if _, ok := m.OIDToName[plain]; !ok {
				m.OIDToName[plain] = name
			}
		}
	}

	// 3b) SMIv2 NOTIFICATION-TYPE (이 MIB엔 없지만 호환용)
	for _, loc := range reNotifType.FindAllStringSubmatchIndex(text, -1) {
		name := text[loc[2]:loc[3]]
		end := loc[1] + 4000
		if end > len(text) {
			end = len(text)
		}
		block := text[loc[1]:end]
		am := reAssignBr.FindStringSubmatchIndex(block)
		if am == nil {
			continue
		}
		base, ok := resolved[block[am[2]:am[3]]]
		if !ok {
			continue
		}
		trapOID := base + "." + block[am[4]:am[5]]
		var variables []string
		if om := reObjects.FindStringSubmatch(block[:am[0]]); om != nil {
			variables = splitCSV(om[1])
		}
		regTrap(trapOID, name, variables)
	}
	return m
}

// loadMIBDir 는 디렉터리의 *.txt / *.mib 를 모두 읽어 MIBMap 목록으로 돌린다.
// 개별 파일 실패는 걸러낸다(파이썬 TrapDecoder.from_dir 과 동일한 관용 정책 —
// MIB 하나가 깨져도 트랩 수신 자체는 계속돼야 한다).
func loadMIBDir(dir string) (maps []*MIBMap, loaded []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	names := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		low := strings.ToLower(e.Name())
		if strings.HasSuffix(low, ".txt") || strings.HasSuffix(low, ".mib") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, fn := range names {
		data, err := os.ReadFile(filepath.Join(dir, fn))
		if err != nil {
			continue
		}
		maps = append(maps, ParseMIB(string(data)))
		loaded = append(loaded, fn)
	}
	return maps, loaded
}
