package snmp

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// 표준 OID(파이썬 trap_receiver.py 의 동명 상수).
const (
	OIDSNMPTrapOID        = "1.3.6.1.6.3.1.1.4.1.0" // snmpTrapOID.0 — v2c 트랩 식별 OID
	OIDSNMPTrapEnterprise = "1.3.6.1.6.3.1.1.4.3.0" // snmpTrapEnterprise.0
)

// v1 generic-trap 표준 이름(파이썬 _V1_GENERIC).
var v1Generic = []struct {
	name string
	oid  string
}{
	{"coldStart", "1.3.6.1.6.3.1.1.5.1"},
	{"warmStart", "1.3.6.1.6.3.1.1.5.2"},
	{"linkDown", "1.3.6.1.6.3.1.1.5.3"},
	{"linkUp", "1.3.6.1.6.3.1.1.5.4"},
	{"authenticationFailure", "1.3.6.1.6.3.1.1.5.5"},
	{"egpNeighborLoss", "1.3.6.1.6.3.1.1.5.6"},
}

// 잘 알려진 표준 OID — MIB 에 없어도 varbind 표시를 읽기 좋게(파이썬 _STD_NAMES).
var stdNames = map[string]string{
	OIDSysUpTime:          "sysUpTime",
	"1.3.6.1.2.1.1.3":     "sysUpTime",
	OIDSNMPTrapOID:        "snmpTrapOID",
	OIDSNMPTrapEnterprise: "snmpTrapEnterprise",
}

// Varbind 는 트랩에 실린 OID-값 쌍 1개다. 파이썬 varbind dict 와 같은 필드.
// Value 는 kind 에 따라 int64 또는 string(또는 nil)이다 — JSONL 저장소를
// 거쳐 다시 읽으면 encoding/json 특성상 수치가 float64 로 복원되는 점에 주의.
type Varbind struct {
	OID     string `json:"oid"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // string/hex/oid/int/timeticks/ipaddress/null
	Value   any    `json:"value"`
	Display string `json:"display"`
}

// Trap 은 수신·정규화된 트랩 1건이다. 파이썬 트랩 dict 필드(ts, src, community,
// version, pdu, trap_oid, name, sev, desc, varbinds)에 프런트 뷰 필드
// (time, oid, severity)를 합친 모양이다 — Go 쪽에서는 디코더가 곧바로 뷰
// 스키마를 만들어 폴터의 재가공 단계를 없앤다.
// Sev 와 Severity 는 같은 값이다(프런트가 두 키를 모두 소비).
type Trap struct {
	Time      string    `json:"time"`      // "2006-01-02 15:04:05" (수신 호스트 로컬)
	Ts        float64   `json:"ts"`        // epoch 초(파이썬 time.time 호환)
	Src       string    `json:"src"`       // 발신 IP
	Community string    `json:"community"` // 패킷의 community (필터 전 원값)
	Version   string    `json:"version"`   // "v1" / "v2c"
	PDU       string    `json:"pdu"`       // "v1-trap" / "v2c-trap" / "inform"
	OID       string    `json:"oid"`       // 트랩 OID (파이썬 trap_oid)
	Name      string    `json:"name"`      // MIB 해석 이름 (못 찾으면 "")
	Sev       string    `json:"sev"`       // critical / warning / info
	Severity  string    `json:"severity"`  // Sev 와 동일 — 프런트 호환용 별칭
	Desc      string    `json:"desc"`      // *TrapDescription varbind 우선
	Varbinds  []Varbind `json:"varbinds"`
}

// 심각도 휴리스틱 — MIB 에 severity 필드가 없어 트랩 이름 기반으로 분류한다.
// 프런트가 소비하는 어휘와 동일: critical / warning / info.
var (
	sevWarn = regexp.MustCompile(`(?i)predict|maintenance|notenabled|not-enabled|degrad|threshold|warn|blacklist`)
	sevCrit = regexp.MustCompile(`(?i)crash|unreachable|doublefault|double-fault|diskproblem|disk-problem|badnetwork|bad-network|badsensor|bad-sensor|rebootedunexpectedly|unexpected|bootfail|boot-fail|noquorum|no-quorum|fail|fault|down|lost|error`)
)

// ClassifySeverity 는 트랩 이름(우선)·설명으로 심각도를 분류한다.
// 예측/점검/설정경고는 warning, 크래시/도달불가/부팅실패 등은 critical, 그 외 info.
// warning 을 critical 보다 먼저 검사한다 — 'double fault prediction' 처럼
// 예측성 알림이 critical 로 과대 보고되는 것을 막기 위함이다(파이썬과 같은 순서).
func ClassifySeverity(name, desc string) string {
	text := (name + " " + desc)
	if sevWarn.MatchString(text) {
		return "warning"
	}
	if sevCrit.MatchString(text) {
		return "critical"
	}
	return "info"
}

// Decoder 는 MIB 맵과 PDU 파서를 결합한 트랩 디코더다(파이썬 TrapDecoder).
type Decoder struct {
	OIDToName   map[string]string
	Traps       map[string]TrapInfo
	LoadedFiles []string // 로드된 MIB 파일명(운영 로그용)
}

// NewDecoder 는 MIBMap 목록으로 디코더를 만든다. nil 이면 표준 이름만 쓴다.
func NewDecoder(maps ...*MIBMap) *Decoder {
	d := &Decoder{OIDToName: map[string]string{}, Traps: map[string]TrapInfo{}}
	for k, v := range stdNames {
		d.OIDToName[k] = v
	}
	for _, mp := range maps {
		if mp == nil {
			continue
		}
		for k, v := range mp.OIDToName {
			d.OIDToName[k] = v
		}
		for k, v := range mp.Traps {
			d.Traps[k] = v
		}
	}
	return d
}

// NewDecoderFromDir 은 mibDir 의 *.txt / *.mib 를 모두 읽어 디코더를 만든다.
// 디렉터리가 없거나 파일이 깨져도 빈 맵으로 동작한다 — MIB 부재가 트랩 수신을
// 막으면 안 되기 때문이다(이름만 OID 로 표시될 뿐 수신은 계속된다).
func NewDecoderFromDir(mibDir string) *Decoder {
	maps, loaded := loadMIBDir(mibDir)
	d := NewDecoder(maps...)
	d.LoadedFiles = loaded
	return d
}

// NameFor 는 OID 에 대응하는 MIB 이름을 돌려준다(없으면 "").
func (d *Decoder) NameFor(oid string) string { return d.OIDToName[oid] }

// Decode 는 UDP 페이로드를 정규화 Trap 으로 디코드한다.
// ok=false 는 파싱 불가(손상 패킷). 어떤 손상 패킷도 수신 루프를 죽이면 안
// 되므로 낮은 수준의 모든 실패를 여기서 흡수한다(파이썬 decode 의 포괄 except 와 동일).
func (d *Decoder) Decode(data []byte, srcIP string) (t Trap, ok bool) {
	defer func() {
		if recover() != nil { // 혹시 남은 경계 버그가 있어도 수신 루프 보호
			t, ok = Trap{}, false
		}
	}()
	t, err := d.decode(data, srcIP)
	if err != nil {
		return Trap{}, false
	}
	return t, true
}

func (d *Decoder) decode(data []byte, srcIP string) (Trap, error) {
	top := &reader{b: data}
	_, seq, err := top.read()
	if err != nil {
		return Trap{}, err
	}
	r := &reader{b: seq}
	_, verB, err := r.read()
	if err != nil {
		return Trap{}, err
	}
	version := int64(0)
	if len(verB) > 0 {
		version = int64(decUint(verB))
	}
	_, commB, err := r.read()
	if err != nil {
		return Trap{}, err
	}
	community := string(commB) // 파이썬 decode(errors="replace") 와 유사 — 원본 보존
	pduTag, pduB, err := r.read()
	if err != nil {
		return Trap{}, err
	}

	var varbinds []Varbind
	trapOID := ""
	pduType := map[byte]string{
		pduV2Trap: "v2c-trap",
		pduInform: "inform",
		pduV1Trap: "v1-trap",
	}[pduTag]
	if pduType == "" {
		pduType = fmt.Sprintf("0x%02x", pduTag)
	}

	switch pduTag {
	case pduV2Trap, pduInform:
		p := &reader{b: pduB}
		if _, _, err = p.read(); err != nil { // request-id
			return Trap{}, err
		}
		if _, _, err = p.read(); err != nil { // error-status
			return Trap{}, err
		}
		if _, _, err = p.read(); err != nil { // error-index
			return Trap{}, err
		}
		_, vbl, err := p.read() // variable-bindings SEQUENCE
		if err != nil {
			return Trap{}, err
		}
		varbinds, err = d.readVarbinds(vbl)
		if err != nil {
			return Trap{}, err
		}
		for _, vb := range varbinds {
			if vb.OID == OIDSNMPTrapOID && vb.Kind == "oid" {
				trapOID, _ = vb.Value.(string)
			}
		}
	case pduV1Trap:
		p := &reader{b: pduB}
		_, entB, err := p.mustRead() // enterprise OID
		if err != nil {
			return Trap{}, err
		}
		enterprise, err := decodeOID(entB)
		if err != nil {
			return Trap{}, err
		}
		if _, _, err = p.read(); err != nil { // agent-addr
			return Trap{}, err
		}
		_, genB, err := p.mustRead() // generic-trap
		if err != nil {
			return Trap{}, err
		}
		_, specB, err := p.mustRead() // specific-trap
		if err != nil {
			return Trap{}, err
		}
		if _, _, err = p.read(); err != nil { // time-stamp
			return Trap{}, err
		}
		_, vbl, err := p.read() // varbinds
		if err != nil {
			return Trap{}, err
		}
		varbinds, err = d.readVarbinds(vbl)
		if err != nil {
			return Trap{}, err
		}
		generic := int64(0)
		if len(genB) > 0 {
			generic = int64(decUint(genB))
		}
		specific := uint64(0)
		if len(specB) > 0 {
			specific = decUint(specB)
		}
		if generic == 6 { // enterpriseSpecific
			trapOID = fmt.Sprintf("%s.0.%d", enterprise, specific)
		} else if generic >= 0 && int(generic) < len(v1Generic) {
			trapOID = v1Generic[generic].oid
		}
	default:
		return Trap{}, fmt.Errorf("snmp: 지원하지 않는 PDU 0x%02x", pduTag)
	}

	name := ""
	if trapOID != "" {
		name = d.OIDToName[trapOID]
	}
	// '.0.' 유무 차이로 못 찾으면 반대 형태로 재시도(파이썬과 동일한 두 단계 보정)
	if name == "" && trapOID != "" && strings.Contains(trapOID, ".0.") {
		name = d.OIDToName[strings.ReplaceAll(trapOID, ".0.", ".")]
	}
	if name == "" && trapOID != "" {
		// everRunTrapId.0.N ↔ everRunTrapId.N 보정
		if i := strings.LastIndex(trapOID, "."); i > 0 {
			name = d.OIDToName[trapOID[:i]+".0."+trapOID[i+1:]]
		}
	}

	// 설명: TrapDescription varbind 값 우선 → 이름 → OID
	desc := ""
	for _, vb := range varbinds {
		if strings.HasSuffix(vb.Name, "TrapDescription") && (vb.Kind == "string" || vb.Kind == "hex") {
			desc = fmt.Sprintf("%v", vb.Value)
			break
		}
	}
	if desc == "" {
		switch {
		case name != "":
			desc = name
		case trapOID != "":
			desc = trapOID
		default:
			desc = "SNMP trap"
		}
	}

	sev := ClassifySeverity(name, desc)
	verStr := map[int64]string{0: "v1", 1: "v2c"}[version]
	if verStr == "" {
		verStr = fmt.Sprintf("%d", version)
	}
	now := time.Now()
	return Trap{
		Time:      now.Format("2006-01-02 15:04:05"),
		Ts:        float64(now.UnixNano()) / 1e9,
		Src:       srcIP,
		Community: community,
		Version:   verStr,
		PDU:       pduType,
		OID:       trapOID,
		Name:      name,
		Sev:       sev,
		Severity:  sev,
		Desc:      desc,
		Varbinds:  varbinds,
	}, nil
}

// readVarbinds 는 variable-bindings SEQUENCE 본문을 Varbind 목록으로 읽는다
// (파이썬 _read_varbinds 와 동일).
func (d *Decoder) readVarbinds(vbl []byte) ([]Varbind, error) {
	out := []Varbind{}
	vr := &reader{b: vbl}
	for vr.more() {
		_, vb, err := vr.read()
		if err != nil {
			return nil, err
		}
		one := &reader{b: vb}
		_, oidB, err := one.mustRead()
		if err != nil {
			return nil, err
		}
		vt, vv, err := one.mustRead()
		if err != nil {
			return nil, err
		}
		oid, err := decodeOID(oidB)
		if err != nil {
			return nil, err
		}
		py, kind, disp := decVarbindValue(vt, vv)
		name := d.OIDToName[oid]
		if name == "" {
			name = oid
		}
		out = append(out, Varbind{OID: oid, Name: name, Kind: kind, Value: py, Display: disp})
	}
	return out, nil
}
