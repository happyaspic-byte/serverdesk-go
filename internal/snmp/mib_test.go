package snmp

import (
	"testing"
)

// 테스트 기준 MIB는 공개 재배포가 가능한 최소 합성 fixture다. 실제 벤더 MIB는 고객이
// 승인된 채널에서 설치하며 저장소·공개 릴리스에 포함하지 않는다.
const testMIBDir = "testdata/mibs"

func TestMIBParseSyntheticFixtures(t *testing.T) {
	dec := NewDecoderFromDir(testMIBDir)
	if len(dec.LoadedFiles) != 2 {
		t.Fatalf("로드된 MIB 파일 수 = %v, want 2", dec.LoadedFiles)
	}
	if got := len(dec.OIDToName); got < 16 {
		t.Errorf("len(OIDToName) = %d, want at least 16 synthetic+standard names", got)
	}
	if got := len(dec.Traps); got != 10 {
		t.Errorf("len(Traps) = %d, want 10 (.N and .0.N aliases)", got)
	}

	// 스폿 체크 — 파이썬 디코더 출력과 동일해야 함
	spot := map[string]string{
		"1.3.6.1.4.1.458.115.2":     "everRunTrapId",
		"1.3.6.1.4.1.458.115.2.0.2": "everRunGuestCrashedTrap",    // RFC 3584 .0.N 형태
		"1.3.6.1.4.1.458.115.2.2":   "everRunGuestCrashedTrap",    // .N 형태
		"1.3.6.1.4.1.458.116.2.0.3": "ztCEdgeNodeUnreachableTrap", // ztC Edge MIB
		"1.3.6.1.4.1.458.115.3.1":   "everRunTrapDescription",     // OBJECT-TYPE 해석
		"1.3.6.1.2.1.1.3.0":         "sysUpTime",                  // 표준 이름
		"1.3.6.1.6.3.1.1.4.1.0":     "snmpTrapOID",                // 표준 이름
	}
	for oid, want := range spot {
		if got := dec.OIDToName[oid]; got != want {
			t.Errorf("OIDToName[%s] = %q, want %q", oid, got, want)
		}
	}

	// 트랩 메타데이터: 이름/변수/출처 MIB
	ti, ok := dec.Traps["1.3.6.1.4.1.458.115.2.0.2"]
	if !ok {
		t.Fatal("Traps 에 everRunGuestCrashedTrap (.0.2 형태) 없음")
	}
	if ti.Name != "everRunGuestCrashedTrap" || ti.MIB != "TEST-STRATUS-LIKE-MIB" {
		t.Errorf("트랩 메타 = %+v", ti)
	}
	if len(ti.Variables) != 1 || ti.Variables[0] != "everRunTrapDescription" {
		t.Errorf("트랩 변수 = %v, want [everRunTrapDescription]", ti.Variables)
	}
}

func TestMIBParseMissingDir(t *testing.T) {
	// MIB 부재가 트랩 수신을 막으면 안 된다 — 표준 이름만으로 동작해야 함
	dec := NewDecoderFromDir("/nonexistent/mibs")
	if len(dec.OIDToName) != len(stdNames) {
		t.Errorf("빈 디코더 이름 수 = %d, want %d", len(dec.OIDToName), len(stdNames))
	}
	if dec.NameFor(OIDSysUpTime) != "sysUpTime" {
		t.Error("표준 이름 해석 실패")
	}
}

func TestClassifySeverity(t *testing.T) {
	cases := []struct {
		name, desc, want string
	}{
		{"everRunNodeUnreachableTrap", "Node Unreachable Trap.", "critical"},
		{"everRunGuestCrashedTrap", "Guest Crashed Trap.", "critical"},
		{"everRunNodeMaintenanceTrap", "Node Maintenance Trap.", "warning"},
		{"everRunDoubleFaultPredictionTrap", "Double Fault Prediction Trap.", "warning"}, // warn 우선
		{"everRunGenericTrap", "Generic Trap.", "info"},
		{"unitNoQuorum", "No Quorum", "critical"},
		{"unitCallHomeNotEnabled", "Call Home Not Enabled", "warning"},
		{"", "", "info"},
	}
	for _, c := range cases {
		if got := ClassifySeverity(c.name, c.desc); got != c.want {
			t.Errorf("ClassifySeverity(%q, %q) = %s, want %s", c.name, c.desc, got, c.want)
		}
	}
}
