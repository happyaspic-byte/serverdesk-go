package poller

// ztC Endurance 시뮬 장비 생성기 — 데모/발표용으로 실장비 뒤에 붙는 4대.
//
// 구조 정확성은 다음 원문을 따른다:
//   - ztC Endurance Training integrated 20251226.pdf (Active/Standby, PCIe Fabric,
//     Smart Exchange 절차, PSU Active/Active·단일 PSU 운영, Passive Midplane)
//   - Endurance 설치 절차서 Windows v2.0.docx (IP 11개 구성: BMC 4 + Standby OS 4
//     + Windows 1 + Management UI 2, AUL 버전, Standby OS=모듈 납입 M.2 의 Ubuntu)
//
// 핵심 모델링 규칙:
//   - A/B 는 물리 모듈 식별자, Active/Standby 는 현재 역할이다. 기본은 CM-A=Active.
//   - Active 모듈만 System OS(Windows/RHEL/ESXi)를 베어메탈로 실행하고 PCIe Fabric
//     으로 Storage/I/O 에 연결된다. 가상머신 레이어는 없다 — Windows Server 를
//     깔면 그냥 Windows Server 다(사용자 정정: 고객 VM/Management VM 모델 제거).
//   - Standby 모듈은 납입 M.2 의 Ubuntu Standby OS 만 실행하고, 정상 상태에서는
//     Storage/I/O 에 연결되지 않는다.
//   - Storage/I/O/PSU 는 Active/Active(미러링·NIC Teaming·부하 공유)다.
//   - everRun 용어(node0/node1, A-Link, Quorum, Lockstep, Checkpoint)는 쓰지 않는다.
//
// 프런트 normalizeDevice 화이트리스트를 통과하는 필드(nodes/unit/license/vmList
// 등)는 실장비와 같은 모양이고, 세부 이중화 정보는 meta.endurance 에 담는다 —
// 화이트리스트 밖이라 화면에는 렌더되지 않지만 /api/devices 원문 계약으로는 유효하다.
//
// 결정성: 정적 골격은 상수, 텔레메트리만 now 기반 사인 합성. 임의 난수 금지.

import "math"

// enduranceSpec 은 모델별 공시 사양이다(2세대, 5th Gen Xeon).
type enduranceSpec struct {
	Model    string // 표시 모델명
	Key      string // device id
	Cores    int64  // 컴퓨트 모듈당 코어(공시)
	CPUModel string // CPU 표시명
	MemGiB   float64
	MemMTs   int64   // DDR5 MT/s
	SystemOS string  // Active 모듈의 System OS(베어메탈)
	Subnet3  int64   // 10.10.30.<n> 대역의 세 번째 옥텟 오프셋 베이스
	Phase    float64
}

var enduranceSpecs = []enduranceSpec{
	{Model: "ztC Endurance 3110", Key: "endurance-3110", Cores: 12, CPUModel: "1× Intel Xeon Silver 4510 (12C)", MemGiB: 256, MemMTs: 4400, SystemOS: "Windows Server 2022", Subnet3: 0, Phase: 0.0},
	{Model: "ztC Endurance 5110", Key: "endurance-5110", Cores: 24, CPUModel: "2× Intel Xeon Silver 4510 (12C)", MemGiB: 512, MemMTs: 4400, SystemOS: "RHEL 9.4", Subnet3: 40, Phase: 1.3},
	{Model: "ztC Endurance 7110", Key: "endurance-7110", Cores: 56, CPUModel: "2× Intel Xeon 5520 (28C)", MemGiB: 768, MemMTs: 4800, SystemOS: "Windows Server 2022", Subnet3: 80, Phase: 2.6},
	{Model: "ztC Endurance 9110", Key: "endurance-9110", Cores: 64, CPUModel: "2× Intel Xeon Gold 6548N (32C)", MemGiB: 1024, MemMTs: 5200, SystemOS: "VMware ESXi 8.0", Subnet3: 120, Phase: 3.9},
}

// standbyOS 는 양 모듈 공통으로 깔리는 Standby OS 표기다(AUL ISO 2.1.0.1 기준 Ubuntu).
const standbyOS = "Ubuntu 22.04 LTS (Standby OS)"

// simWave 는 base±amp 범위의 매끄러운 결정적 파형이다(사인 합성 — 급변 금지).
func simWave(now, phase, base, amp float64) float64 {
	v := base + amp*0.6*math.Sin(now/600+phase) + amp*0.4*math.Sin(now/97+phase*2)
	if v < 0 {
		v = 0
	}
	if v > 99 {
		v = 99
	}
	return v
}

// simHist 는 48포인트 히스토리를 파형으로 채운다(30초 간격, 과거→현재 순).
func simHist(now, phase, base, amp float64) []any {
	out := make([]any, 0, 48)
	for i := 0; i < 48; i++ {
		t := now - float64((47-i)*30)
		out = append(out, int64(simWave(t, phase, base, amp)+0.5))
	}
	return out
}

// enduranceIPs 는 장비 1대의 사용자 할당 IP 11개 구성이다.
// 배열 규칙은 설치 절차서의 yaml 구성 정보와 같다:
// BMC 4(모듈당 eth0/eth1) + Standby OS 4(모듈당 eno1/eno2) + Windows Host 1 + Management UI 2.
// 데모 대역은 10.10.30.0/24 예시. Gateway/DNS 는 설정값이며 11개에 포함하지 않는다.
type enduranceIPs struct {
	BmcA0, BmcA1, BmcB0, BmcB1     string
	StbyA1, StbyA2, StbyB1, StbyB2 string
	WindowsHost                    string
	MgmtUI1, MgmtUI2               string
}

func makeEnduranceIPs(base int64) enduranceIPs {
	ip := func(n int64) string { return "10.10.30." + itoa(base+n) }
	return enduranceIPs{
		BmcA0: ip(11), BmcA1: ip(12), BmcB0: ip(13), BmcB1: ip(14),
		StbyA1: ip(21), StbyA2: ip(22), StbyB1: ip(23), StbyB2: ip(24),
		WindowsHost: ip(40),
		MgmtUI1:     ip(31), MgmtUI2: ip(32),
	}
}

// simReach 는 데모용 가상 도달성이다 — 대부분 ok(수 ms), 드물게 1개 박스가 slow 로
// 순환한다(15분 주기, 박스 슬롯 기반 결정적). 실장비에서는 ProbeTCP 실측값이 같은
// 자리(meta.endurance.reach)를 채운다.
func simReach(now, phase float64, slot int64) map[string]any {
	slowSlot := int64(math.Mod(now/900+phase, 9))
	if slowSlot == slot {
		return map[string]any{"state": "slow", "ms": round1(820 + 40*math.Sin(now/97+phase))}
	}
	ms := 1.6 + 1.1*math.Sin(now/300+phase+float64(slot))
	if ms < 0.4 {
		ms = 0.4
	}
	return map[string]any{"state": "ok", "ms": round1(ms)}
}

// EnduranceSimDevices 는 ztC Endurance 4모델의 데모 device 목록을 만든다.
// now 는 unix 초(float). cfg sim_devices > 0 일 때만 호출된다.
func EnduranceSimDevices(now float64) []map[string]any {
	out := make([]map[string]any, 0, len(enduranceSpecs))
	for _, sp := range enduranceSpecs {
		cpu := simWave(now, sp.Phase, 34, 22)
		mem := simWave(now, sp.Phase+0.7, 58, 14)
		ips := makeEnduranceIPs(sp.Subnet3)

		// CM-A/CM-B 는 물리 식별자, Active/Standby 는 현재 역할 — 기본 CM-A=Active.
		// primary 플래그가 프런트의 주 노드(Active)/보조 노드(Standby) 배지를 결정한다.
		computeModule := func(letter string, active bool, bmc0, bmc1, stby1, stby2 string) map[string]any {
			role := "Standby"
			os := standbyOS // Standby 모듈은 납입 M.2 의 Ubuntu 만 실행
			if active {
				role = "Active"
				os = sp.SystemOS // Active 모듈이 System OS + 고객 워크로드 실행
			}
			return map[string]any{
				"name": "CM-" + letter, "module": letter, "role": role,
				"state": "running", "standing": "normal",
				"mode": "normal", "primary": active,
				"manufacturer": "Stratus", "model": sp.Model,
				"cores": sp.Cores, "cpuModel": sp.CPUModel,
				"memory": f1(sp.MemGiB) + " GiB DDR5-" + itoa(sp.MemMTs),
				"memGiB": sp.MemGiB,
				// 역할 구분 주소: Active 는 System OS(Windows Host) 주소,
				// Standby 는 Standby OS 주소가 대표 주소다.
				"ip":          map[bool]string{true: ips.WindowsHost, false: stby1}[active],
				"os":          os,
				"bmc":         map[string]any{"eth0": bmc0, "eth1": bmc1},
				"standbyNic":  map[string]any{"eno1": stby1, "eno2": stby2},
				"bootDevice":  map[bool]string{true: "Storage Module A/B (미러)", false: "Internal M.2 NVMe (Standby OS)"}[active],
				"fabricConn":  active, // Active 만 PCIe Fabric 으로 Storage/I/O 연결
				"serial":      "EN" + itoa(sp.Subnet3) + "-CM" + letter,
				"bios":        "Stratus UEFI 3.2",
				"reachable":   true,
				"cpu_pct":     map[bool]float64{true: round1(cpu), false: 2.1}[active], // Standby 는 Ubuntu 아이들 수준
				"mem_pct":     map[bool]float64{true: round1(mem), false: 8.4}[active],
				"tempMaxC":    41.0 + sp.Phase,
				"loadAvg":     []any{round2(cpu / 10), round2(cpu/10 - 0.1), round2(cpu/10 - 0.2)},
				"fsMaxPct":    int64(38 + sp.Phase*3),
				"syncState":   "mirrored",
				"healthWatch": "AUL", // Automated Uptime Layer 가 헬스 모니터링
			}
		}

		meta := map[string]any{
			"label": sp.Model, "company": "루비컴", "factory": "김해 IDC",
			"vendor": "Stratus", "platform": "endurance",
			"version": "2.1.0.1-120", // AUL (zen-aul-system-win_2.1.0.1-120)
			"mgmt":    ips.MgmtUI1,   // Endurance Console = Management UI 1
			"uuid":    "sim-" + sp.Key + "-0000-0000-0000",
			"assetTag": "", "floorPos": "",
			"license": map[string]any{
				"name": "ze-e-" + itoa(100+sp.Subnet3), "type": "standard", "edition": "Endurance",
				"install": "Mon Jun 15 04:41:53 UTC 2026", "expire": "", "expires": false,
				"activated": true, "daysLeft": nil,
			},
			"nodes": []any{
				computeModule("A", true, ips.BmcA0, ips.BmcA1, ips.StbyA1, ips.StbyA2),
				computeModule("B", false, ips.BmcB0, ips.BmcB1, ips.StbyB1, ips.StbyB2),
			},
			"alerts": []any{}, "traps": []any{}, "events": []any{},
			"healthLevel": "ok", "healthReasons": []any{}, "alertCounts": map[string]any{},
			// 세부 이중화 구조 — 화이트리스트 밖이라 화면엔 안 나오지만 API 계약으로 유효.
			// 어디에도 node0/node1·A-Link·Quorum·Lockstep·Checkpoint 를 쓰지 않는다.
			"endurance": map[string]any{
				"chassis":       "2U single chassis",
				"midplane":      "Passive Midplane / PCIe Fabric",
				"failover":      "Smart Exchange (재부팅 없는 서비스 연속성)",
				"compute":       map[string]any{"modules": []any{"CM-A", "CM-B"}, "mode": "Active/Standby"},
				"storage":       map[string]any{"modules": []any{"A", "B"}, "mode": "Active/Active", "redundancy": "RDM 또는 Software RAID 1 미러링"},
				"io":            map[string]any{"modules": []any{"A", "B"}, "mode": "Active/Active", "redundancy": "Windows NIC Teaming"},
				"psu":           map[string]any{"modules": []any{"A", "B"}, "mode": "Active/Active", "note": "부하 공유, 단일 PSU 운영 가능"},
				"managementIPs": []any{ips.MgmtUI1, ips.MgmtUI2},
				"windowsHost":   ips.WindowsHost,
				"ipPlan":        "BMC 4 + Standby OS 4 + Windows Host 1 + Management UI 2 = 11",
				// 박스별 도달 상태(시뮬은 가상 응답 — 실장비는 ProbeTCP 실측).
				"reach": map[string]any{
					"bmcA": simReach(now, sp.Phase, 0), "bmcB": simReach(now, sp.Phase, 1),
					"stbyA": simReach(now, sp.Phase, 2), "stbyB": simReach(now, sp.Phase, 3),
					"mgmt1": simReach(now, sp.Phase, 4), "mgmt2": simReach(now, sp.Phase, 5),
					"windows": simReach(now, sp.Phase, 6),
				},
			},
			"sim": true,
		}

		out = append(out, map[string]any{
			"id": sp.Key, "host": sp.Key, "type": "END", "site": ips.MgmtUI1,
			"status": "op",
			// seven-nines 등급 — Endurance 의 존재 이유라 명목값을 그대로 쓴다.
			"availN":  99.99999,
			"cpu0":    int64(cpu + 0.5),
			"mem0":    int64(mem + 0.5),
			"cpuNA":   false,
			"memNA":   false,
			"sync":    "sync",
			"uptime":  int64(120) + sp.Subnet3,
			"live":    true,
			"meta":    meta,
			"histCpu": simHist(now, sp.Phase, 34, 22),
			"histMem": simHist(now, sp.Phase+0.7, 58, 14),
			"histRtt": []any{},
		})
	}
	return out
}

// itoa 는 sim 전용 정수→문자열 변환이다.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func f1(v float64) string {
	// 소수 1자리 고정 — 실장비 node.memory 등 문자열 필드와 같은 형태.
	iv := int64(v*10 + 0.5)
	s := itoa(iv)
	if iv < 0 {
		return s
	}
	if len(s) == 1 {
		return "0." + s
	}
	return s[:len(s)-1] + "." + s[len(s)-1:]
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
