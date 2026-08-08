package snmp

import (
	"encoding/hex"
	"testing"
)

// 골든 패킷 — 파이썬 trap_receiver.build_v2c_trap / 수제 v1 빌더가 만든 실제
// 바이트열이다. Go 디코더가 파이썬 디코더와 같은 결과를 내는지가 검증 대상.
const (
	// build_v2c_trap('public', '...458.115.2.0.2', [(trapDesc, 'str', 'Guest Crashed Trap.')], 12345)
	goldenV2cHex = "306602010104067075626c6963a759020101020100020100304e300e06082b06010201010300430230393019060a2b060106030101040100060b2b06010401834a730200023021060a2b06010401834a73030104134775657374204372617368656420547261702e"
	// build_v2c_trap('private', '...458.115.2.2') — '.0.' 없는 형태의 트랩 OID
	goldenV2cNoDotZeroHex = "3043020101040770726976617465a735020101020100020100302a300e06082b06010201010300430230393018060a2b060106030101040100060a2b06010401834a730202"
	// v1: enterprise=everRunTrapId, generic=6(enterpriseSpecific), specific=3
	goldenV1Hex = "305002010004067075626c6963a44306092b06010401834a730240040a000007020106020103430203e730263024060a2b06010401834a73030104164e6f646520556e726561636861626c6520547261702e"
)

func mustHex(t *testing.T, h string) []byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testDecoder(t *testing.T) *Decoder {
	t.Helper()
	dec := NewDecoderFromDir(testMIBDir)
	if len(dec.LoadedFiles) != 2 {
		t.Fatalf("MIB 로드 실패: %v", dec.LoadedFiles)
	}
	return dec
}

// TestDecodeV2cGolden — 파이썬이 만든 v2c 트랩을 파이썬과 같게 디코드하는지.
func TestDecodeV2cGolden(t *testing.T) {
	trap, ok := testDecoder(t).Decode(mustHex(t, goldenV2cHex), "192.0.2.10")
	if !ok {
		t.Fatal("골든 v2c 트랩 디코드 실패")
	}
	if trap.Src != "192.0.2.10" || trap.Community != "public" ||
		trap.Version != "v2c" || trap.PDU != "v2c-trap" {
		t.Errorf("헤더 필드: %+v", trap)
	}
	if trap.OID != "1.3.6.1.4.1.458.115.2.0.2" {
		t.Errorf("trap OID = %q", trap.OID)
	}
	if trap.Name != "everRunGuestCrashedTrap" {
		t.Errorf("name = %q", trap.Name)
	}
	if trap.Sev != "critical" || trap.Severity != "critical" {
		t.Errorf("sev = %q/%q", trap.Sev, trap.Severity)
	}
	// *TrapDescription varbind 가 설명으로 승격됐는지
	if trap.Desc != "Guest Crashed Trap." {
		t.Errorf("desc = %q", trap.Desc)
	}
	if trap.Ts <= 0 || trap.Time == "" {
		t.Errorf("타임스탬프 누락: ts=%v time=%q", trap.Ts, trap.Time)
	}
	if len(trap.Varbinds) != 3 {
		t.Fatalf("varbinds = %d개, want 3", len(trap.Varbinds))
	}
	vb := trap.Varbinds[0]
	if vb.OID != OIDSysUpTime || vb.Name != "sysUpTime" || vb.Kind != "timeticks" ||
		vb.Value != int64(12345) || vb.Display != "123.45s" {
		t.Errorf("varbind[0] = %+v", vb)
	}
	vb = trap.Varbinds[1]
	if vb.OID != OIDSNMPTrapOID || vb.Name != "snmpTrapOID" || vb.Kind != "oid" ||
		vb.Value != "1.3.6.1.4.1.458.115.2.0.2" {
		t.Errorf("varbind[1] = %+v", vb)
	}
	vb = trap.Varbinds[2]
	if vb.OID != "1.3.6.1.4.1.458.115.3.1" || vb.Name != "everRunTrapDescription" ||
		vb.Kind != "string" || vb.Value != "Guest Crashed Trap." {
		t.Errorf("varbind[2] = %+v", vb)
	}
}

// TestBuildV2cTrapByteParity — Go 빌더가 파이썬 빌더와 바이트 단위로 같아야 한다.
func TestBuildV2cTrapByteParity(t *testing.T) {
	pkt, err := BuildV2cTrap("public", "1.3.6.1.4.1.458.115.2.0.2",
		[]V2cVarbind{{OID: "1.3.6.1.4.1.458.115.3.1", Kind: "str", Value: "Guest Crashed Trap."}},
		12345)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(pkt); got != goldenV2cHex {
		t.Errorf("BuildV2cTrap 바이트 불일치:\n got %s\nwant %s", got, goldenV2cHex)
	}
}

// TestDecodeV2cNoDotZero — '.0.' 없는 트랩 OID 도 같은 이름으로 해석되는지.
func TestDecodeV2cNoDotZero(t *testing.T) {
	trap, ok := testDecoder(t).Decode(mustHex(t, goldenV2cNoDotZeroHex), "192.0.2.11")
	if !ok {
		t.Fatal("골든 v2c(.N 형태) 디코드 실패")
	}
	if trap.OID != "1.3.6.1.4.1.458.115.2.2" {
		t.Errorf("trap OID = %q", trap.OID)
	}
	if trap.Name != "everRunGuestCrashedTrap" {
		t.Errorf("name = %q — .N↔.0.N 보정 실패", trap.Name)
	}
	// TrapDescription varbind 가 없으면 이름이 설명으로 간다(파이썬과 동일)
	if trap.Desc != "everRunGuestCrashedTrap" {
		t.Errorf("desc = %q", trap.Desc)
	}
	if trap.Community != "private" {
		t.Errorf("community = %q", trap.Community)
	}
}

// TestDecodeV1Golden — v1 enterprise+specific → OID 조립과 이름 해석.
func TestDecodeV1Golden(t *testing.T) {
	trap, ok := testDecoder(t).Decode(mustHex(t, goldenV1Hex), "10.1.1.5")
	if !ok {
		t.Fatal("골든 v1 트랩 디코드 실패")
	}
	if trap.Version != "v1" || trap.PDU != "v1-trap" {
		t.Errorf("version/pdu = %q/%q", trap.Version, trap.PDU)
	}
	if trap.OID != "1.3.6.1.4.1.458.115.2.0.3" {
		t.Errorf("trap OID = %q (enterprise.0.specific 조립)", trap.OID)
	}
	if trap.Name != "everRunNodeUnreachableTrap" || trap.Sev != "critical" {
		t.Errorf("name/sev = %q/%q", trap.Name, trap.Sev)
	}
	if trap.Desc != "Node Unreachable Trap." {
		t.Errorf("desc = %q", trap.Desc)
	}
	if len(trap.Varbinds) != 1 ||
		trap.Varbinds[0].Name != "everRunTrapDescription" {
		t.Errorf("varbinds = %+v", trap.Varbinds)
	}
}

// TestDecodeInform — INFORM PDU(v2c 와 같은 본문 구조)도 받는다.
func TestDecodeInform(t *testing.T) {
	pkt, err := BuildV2cTrap("public", "1.3.6.1.4.1.458.115.2.0.4", nil, 999)
	if err != nil {
		t.Fatal(err)
	}
	// PDU 태그만 0xA7 → 0xA6 으로 바꿔 inform 을 만든다(길이 동일).
	// 헤더(버전/커뮤니티)에는 0xA7 바이트가 없으므로 첫 매치가 PDU 태그다.
	found := false
	for i := 0; i+1 < len(pkt); i++ {
		if pkt[i] == pduV2Trap {
			pkt[i] = pduInform
			found = true
			break
		}
	}
	if !found {
		t.Fatal("PDU 태그 위치를 못 찾음")
	}
	trap, ok := testDecoder(t).Decode(pkt, "192.0.2.12")
	if !ok {
		t.Fatal("inform 디코드 실패")
	}
	if trap.PDU != "inform" {
		t.Errorf("pdu = %q, want inform", trap.PDU)
	}
	if trap.Name != "everRunNodeMaintenanceTrap" {
		t.Errorf("name = %q", trap.Name)
	}
	if trap.Sev != "warning" {
		t.Errorf("sev = %q, want warning", trap.Sev)
	}
}

// TestDecodeCorrupt — 손상/쓰레기 패킷이 ok=false 로 흡수되고 panic 없음.
func TestDecodeCorrupt(t *testing.T) {
	dec := testDecoder(t)
	inputs := [][]byte{
		{},
		{0x30},
		{0x30, 0x05, 0x02, 0x01, 0x01},           // 잘린 시퀀스
		mustHex(t, goldenV2cHex)[:20],            // 골든 절단
		[]byte("not an snmp packet at all"),      // 쓰레기
		mustHex(t, "300602010104067075626c6963"), // PDU 없는 메시지
	}
	for i, in := range inputs {
		if _, ok := dec.Decode(in, "192.0.2.1"); ok {
			t.Errorf("입력 %d: ok=true — 손상 패킷인데 디코드 성공", i)
		}
	}
}
