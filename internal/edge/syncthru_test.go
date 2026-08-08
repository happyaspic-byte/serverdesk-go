package edge

import (
	"encoding/json"
	"testing"
)

func TestLenientJSON(t *testing.T) {
	// SyncThru 원문 관행: 미인용 키, 중첩 객체, 트레일링 콤마.
	raw := `{status1: "Ready...", identity: {model_name: "SL-C565W",}, list: [1, 2,],}`
	var v map[string]any
	if err := json.Unmarshal([]byte(lenientJSON(raw)), &v); err != nil {
		t.Fatalf("lenient parse: %v", err)
	}
	if v["status1"] != "Ready..." {
		t.Fatalf("status1 = %v", v["status1"])
	}
	if jm(v["identity"])["model_name"] != "SL-C565W" {
		t.Fatalf("identity = %v", v["identity"])
	}
	if n := len(jl(v["list"])); n != 2 {
		t.Fatalf("list len = %d", n)
	}
	// 이미 인용된 키는 그대로.
	raw2 := `{"a": 1, "b_c": {"D9": "x"}}`
	var v2 map[string]any
	if err := json.Unmarshal([]byte(lenientJSON(raw2)), &v2); err != nil {
		t.Fatalf("quoted parse: %v", err)
	}
	if jm(v2["b_c"])["D9"] != "x" {
		t.Fatalf("v2 = %v", v2)
	}
}

func TestMapSyncThru(t *testing.T) {
	home := map[string]any{
		"identity": map[string]any{
			"model_name": "SL-C565W", "Product_num": "SS123",
			"host_name": "prn01", "mac_addr": "D0:00:06:13:27:3E",
			"location": "1F",
		},
		"status":       map[string]any{"status1": "Ready. "},
		"toner_black":  map[string]any{"cnt": 45},
		"toner_yellow": map[string]any{"cnt": "30"}, // 문자열 숫자도 허용(Python _num)
		"toner_cyan":   map[string]any{},            // cnt 없음 → 제외
	}
	cnt := map[string]any{
		"GXI_BILLING_SIMPLEX_BW_TOTAL_CNT":    100.0,
		"GXI_BILLING_DUPLEX_BW_TOTAL_CNT":     30.0,
		"GXI_BILLING_SIMPLEX_COLOR_TOTAL_CNT": 5.0,
		"GXI_BILLING_DUPLEX_COLOR_TOTAL_CNT":  2.0,
	}
	d := mapSyncThru(home, cnt)
	if d.MonoTotal != 130 || d.ColorTotal != 7 {
		t.Fatalf("totals = %d %d", d.MonoTotal, d.ColorTotal)
	}
	if d.StatusText != "Ready" {
		t.Fatalf("statusText = %q", d.StatusText)
	}
	if d.WebModel != "SL-C565W" || d.ProductNum != "SS123" || d.HostName != "prn01" ||
		d.MAC != "D0:00:06:13:27:3E" || d.Location != "1F" {
		t.Fatalf("ident = %+v", d)
	}
	if len(d.TonerCnt) != 2 || d.TonerCnt["black"] != 45 || d.TonerCnt["yellow"] != 30 {
		t.Fatalf("tonerCnt = %v", d.TonerCnt)
	}
	// 빈 입력도 안전.
	d2 := mapSyncThru(map[string]any{}, map[string]any{})
	if d2.MonoTotal != 0 || len(d2.TonerCnt) != 0 || d2.StatusText != "" {
		t.Fatalf("empty = %+v", d2)
	}
}
