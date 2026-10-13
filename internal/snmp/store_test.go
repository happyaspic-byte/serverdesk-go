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
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "traps.jsonl")
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
func TestStorePrivateSanitizedLegacyRewrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "traps.jsonl")
	legacy := `{"ts":1,"src":"127.0.0.1","community":"legacy-secret","trap_oid":"1.3.6.1.6.3.1.1.5.1","_route":"cluster-a"}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	st := NewTrapStore(path, 10)
	loaded := st.Load()
	if len(loaded) != 1 || loaded[0].Community != "" || loaded[0].OID != "1.3.6.1.6.3.1.1.5.1" {
		t.Errorf("legacy load not sanitized/compatible: %+v", loaded)
	}
	tr := sampleTrap("new", 2)
	tr.Community = "new-secret"
	st.Add(tr)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "community") || strings.Contains(string(b), "secret") {
		t.Errorf("persisted community: %s", b)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("file mode=%o", info.Mode().Perm())
	}
	if snap := st.Snapshot(); len(snap) != 2 || snap[1].Community != "" {
		t.Error("store retains community")
	}
	nested := filepath.Join(dir, "new-dir", "nested", "traps.jsonl")
	NewTrapStore(nested, 10).Add(tr)
	info, err = os.Stat(filepath.Dir(nested))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("directory mode=%o", info.Mode().Perm())
	}
}

func TestStoreRefusesUnsafePathsWithoutChangingHistory(t *testing.T) {
	for _, kind := range []string{"exposed", "symlink", "parent-symlink", "hardlink", "writable-ancestor", "fixed-temp"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			parent := filepath.Join(dir, "private")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "traps.jsonl")
			original := []byte(`{"ts":1}` + "\n")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			st := NewTrapStore(path, 10)
			st.Load()
			victim := filepath.Join(dir, "victim")
			os.WriteFile(victim, []byte("untouched"), 0600)
			switch kind {
			case "exposed":
				os.Chmod(parent, 0755)
			case "symlink":
				os.Remove(path)
				os.Symlink(victim, path)
			case "parent-symlink":
				link := filepath.Join(dir, "link")
				os.Symlink(parent, link)
				st.path = filepath.Join(link, "traps.jsonl")
			case "hardlink":
				os.Remove(path)
				os.Link(victim, path)
			case "writable-ancestor":
				os.Chmod(dir, 0777)
			case "fixed-temp":
				os.Symlink(victim, path+".tmp")
			}
			st.Add(sampleTrap("new", 2))
			b, _ := os.ReadFile(victim)
			if string(b) != "untouched" {
				t.Fatal("victim overwritten")
			}
			if kind == "fixed-temp" {
				b, _ = os.ReadFile(path)
				if !strings.Contains(string(b), `"ts":2`) {
					t.Error("fixed temp should not block safe write")
				}
				return
			}
			if kind != "symlink" && kind != "hardlink" {
				b, _ = os.ReadFile(path)
				if string(b) != string(original) {
					t.Error("previous file overwritten")
				}
			}
			if len(st.Snapshot()) != 1 {
				t.Error("failed write changed in-memory good history")
			}
		})
	}
}

func TestStoreRejectsSymlinkDotDotPath(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "runtime")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{runtime, filepath.Join(outside, "child")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	goodPath := filepath.Join(runtime, "traps.jsonl")
	victim := filepath.Join(outside, "traps.jsonl")
	good := []byte(`{"ts":1}` + "\n")
	original := []byte(`{"ts":99}` + "\n")
	for path, data := range map[string][]byte{goodPath: good, victim: original} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "child"), filepath.Join(runtime, "jump")); err != nil {
		t.Fatal(err)
	}
	// Deliberately avoid filepath.Join: it would remove the vulnerable dot-dot.
	unsafe := runtime + string(os.PathSeparator) + "jump" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "traps.jsonl"
	if err := ValidateTrapPath(unsafe); err == nil {
		t.Error("noncanonical symlink-dotdot path accepted")
	}
	for _, path := range []string{unsafe, "jump/../traps.jsonl"} {
		if _, err := ResolveTrapPath(runtime, path); err == nil {
			t.Errorf("configured path accepted: %q", path)
		}
	}
	direct := NewTrapStore(unsafe, 10)
	if got := direct.Load(); len(got) != 0 {
		t.Errorf("constructor path read victim history: %+v", got)
	}
	direct.Add(sampleTrap("new", 2))
	if len(direct.Snapshot()) != 0 {
		t.Error("refused constructor write changed history")
	}
	st := NewTrapStore(goodPath, 10)
	st.Load()
	st.path = unsafe
	if got := st.Load(); len(got) != 0 {
		t.Error("unsafe reload accepted")
	}
	st.Add(sampleTrap("new", 2))
	if got := st.Snapshot(); len(got) != 1 || got[0].Ts != 1 {
		t.Errorf("previous history changed: %+v", got)
	}
	for path, want := range map[string][]byte{goodPath: good, victim: original} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != string(want) {
			t.Errorf("existing target changed: %q err=%v", path, err)
		}
	}
	for _, path := range []string{goodPath, "traps.jsonl"} {
		if _, err := ResolveTrapPath(runtime, path); err != nil {
			t.Errorf("valid path refused: %q %v", path, err)
		}
	}
}

func TestStoreDefaultRing(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "traps.jsonl")
	st := NewTrapStore(path, 0)
	for i := 0; i < 505; i++ {
		st.Add(sampleTrap("trap", float64(i)))
	}
	if got := len(st.Snapshot()); got != 500 {
		t.Errorf("기본 링 = %d, want 500", got)
	}
}
