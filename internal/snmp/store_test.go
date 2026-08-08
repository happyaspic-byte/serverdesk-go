package snmp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleTrap(name string, ts float64) Trap {
	return Trap{
		Time: "2026-08-07 10:00:00", Ts: ts, Src: "10.0.0.1",
		Community: "public", Version: "v2c", PDU: "v2c-trap",
		OID: "1.3.6.1.4.1.458.115.2.0.2", Name: name,
		Sev: "critical", Severity: "critical", Desc: name + " desc",
		Varbinds: []Varbind{{OID: OIDSysUpTime, Name: "sysUpTime",
			Kind: "timeticks", Value: int64(12345), Display: "123.45s"}},
	}
}

// TestStoreRing — 링 한도를 넘으면 오래된 것부터 버리고 파일도 한도를 유지한다.
func TestStoreRing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traps.jsonl")
	st := NewTrapStore(path, 3)
	for i := 0; i < 5; i++ {
		st.Add(sampleTrap("trap", float64(1000+i)))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("파일 %d줄, want 3", len(lines))
	}
	// 첫 줄이 가장 오래된 생존자(ts=1002), 마지막이 최신(ts=1004)
	var first, last Trap
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &last); err != nil {
		t.Fatal(err)
	}
	if first.Ts != 1002 || last.Ts != 1004 {
		t.Errorf("링 순서: first=%v last=%v", first.Ts, last.Ts)
	}
	// 뷰 스키마 키가 그대로 실려 있는지(프런트 계약)
	for _, key := range []string{`"time"`, `"ts"`, `"src"`, `"oid"`, `"name"`,
		`"desc"`, `"sev"`, `"severity"`, `"pdu"`, `"varbinds"`} {
		if !strings.Contains(lines[0], key) {
			t.Errorf("JSONL 에 키 %s 없음: %s", key, lines[0])
		}
	}
}

// TestStoreLoad — 재시작 재분배: 파일에서 링을 복원하고 순서를 유지한다.
// 깨진 줄은 걸러낸다.
func TestStoreLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "traps.jsonl") // 부모 디렉터리 자동 생성 검증
	st := NewTrapStore(path, 500)
	for i := 0; i < 3; i++ {
		st.Add(sampleTrap("trap", float64(2000+i)))
	}
	// 깨진 줄을 파일 끝에 추가
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{not json\n")
	f.Close()

	st2 := NewTrapStore(path, 500)
	got := st2.Load()
	if len(got) != 3 {
		t.Fatalf("Load = %d건, want 3", len(got))
	}
	if got[0].Ts != 2000 || got[2].Ts != 2002 {
		t.Errorf("Load 순서: %v ... %v", got[0].Ts, got[2].Ts)
	}
	if got[0].Name != "trap" || got[0].Varbinds[0].Display != "123.45s" {
		t.Errorf("Load 내용: %+v", got[0])
	}
	// 로드된 버퍼 위에 Add 하면 기존 이력 뒤에 붙는다
	st2.Add(sampleTrap("trap", 2003))
	if all := st2.Snapshot(); len(all) != 4 || all[3].Ts != 2003 {
		t.Errorf("Add after Load: %+v", all)
	}
}

func TestStoreLoadMissing(t *testing.T) {
	st := NewTrapStore(filepath.Join(t.TempDir(), "nope.jsonl"), 10)
	if got := st.Load(); len(got) != 0 {
		t.Errorf("없는 파일 Load = %d건", len(got))
	}
}

// TestStoreDefaultRing — ring<=0 이면 500(파이썬 기본).
func TestStoreDefaultRing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traps.jsonl")
	st := NewTrapStore(path, 0)
	for i := 0; i < 505; i++ {
		st.Add(sampleTrap("trap", float64(i)))
	}
	if got := len(st.Snapshot()); got != 500 {
		t.Errorf("기본 링 = %d, want 500", got)
	}
}
