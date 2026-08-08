package edge

import "encoding/json"

// DeviceConfig — config 의 edge_devices[] 1건에 대응하는 디코딩 구조체.
// kind 는 "kind"(Python 원본 관행) 또는 "type" 키 모두 받는다 — 통합자가
// 어느 쪽으로 넣어도 동작해야 설정 마이그레이션 중 장비가 조용히 빠지는
// 사고를 막을 수 있다.
type DeviceConfig struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"` // printer | nas | plc | proxmox | server
	Type      string `json:"type"` // kind 별칭
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Community string `json:"community"`

	Vendor   string `json:"vendor"`
	Company  string `json:"company"`
	Factory  string `json:"factory"`
	Site     string `json:"site"`
	AssetTag string `json:"asset_tag"`
	FloorPos string `json:"floor_pos"`

	ExtraIPs []string `json:"extra_ips"` // NAS 제2 랜포트 등 정보성 프로브 대상

	FinsPort    int `json:"fins_port"`     // PLC FINS/UDP (기본 9600)
	FinsSrcNode int `json:"fins_src_node"` // FINS 소스 노드 SA1 (기본 84)

	User     string `json:"user"`     // Proxmox API (기본 root@pam)
	Password string `json:"password"` // Proxmox API

	BMCIP       string `json:"bmc_ip"`   // server kind Redfish BMC
	BMCUser     string `json:"bmc_user"` // 설정 시 Redfish 활성
	BMCPassword string `json:"bmc_password"`
}

// kind 는 kind/type 둘 중 설정된 쪽을 돌려준다 (kind 우선).
func (d DeviceConfig) kind() string {
	if d.Kind != "" {
		return d.Kind
	}
	return d.Type
}

// LoadDevices — edge_devices[] JSON 을 DeviceConfig 로 디코딩한다.
// 형식이 잘못된 항목이 있으면 즉시 에러를 돌린다 — 잘못된 설정을 조용히
// 걸러낸 채 감시가 빠진 장비가 생기는 것보다 부팅 때 실패하는 편이 낫다.
func LoadDevices(raw []json.RawMessage) ([]DeviceConfig, error) {
	out := make([]DeviceConfig, 0, len(raw))
	for i, r := range raw {
		var d DeviceConfig
		if err := json.Unmarshal(r, &d); err != nil {
			return nil, &ConfigError{Index: i, Err: err}
		}
		out = append(out, d)
	}
	return out, nil
}

// ConfigError — edge_devices[i] 디코딩 실패.
type ConfigError struct {
	Index int
	Err   error
}

func (e *ConfigError) Error() string {
	return "edge: edge_devices[" + itoa(e.Index) + "] decode: " + e.Err.Error()
}

func (e *ConfigError) Unwrap() error { return e.Err }
