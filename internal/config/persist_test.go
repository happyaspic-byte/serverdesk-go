package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyFixture 는 픽스처를 임시 디렉터리에 0600 으로 복사한다.
func copyFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/config.json")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "config.local.json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("rewritten file is not valid JSON: %v", err)
	}
	return doc
}

func TestCompareAndReplaceDocRejectsStaleExpected(t *testing.T) {
	path := copyFixture(t)
	store := NewStore(path)
	expected, err := store.ReadDoc()
	if err != nil {
		t.Fatal(err)
	}
	replacement := make(map[string]json.RawMessage, len(expected))
	for key, value := range expected {
		replacement[key] = value
	}
	replacement["_comment"] = json.RawMessage(`"replacement"`)

	if err := store.UpdateDisplayMeta(SectionClusters, "everrun", map[string]string{"label": "concurrent"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndReplaceDoc(expected, replacement); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("stale compare-and-replace error = %v", err)
	}
	if got := readDoc(t, path)["clusters"].([]any)[0].(map[string]any)["name"]; got != "concurrent" {
		t.Fatalf("concurrent update was overwritten: %v", got)
	}

	current, err := store.ReadDoc()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndReplaceDoc(current, replacement); err != nil {
		t.Fatalf("fresh compare-and-replace failed: %v", err)
	}
	if got := readDoc(t, path)["_comment"]; got != "replacement" {
		t.Fatalf("replacement not stored: %v", got)
	}
	if err := store.CompareAndReplaceDoc(replacement, current); err != nil {
		t.Fatalf("semantic compare after formatted write failed: %v", err)
	}
	if got := readDoc(t, path)["clusters"].([]any)[0].(map[string]any)["name"]; got != "concurrent" {
		t.Fatalf("semantic compare replacement did not restore current doc: %v", got)
	}
}

func TestUpdateDisplayMetaCluster(t *testing.T) {
	path := copyFixture(t)
	orig, _ := os.ReadFile(path)
	s := NewStore(path)

	err := s.UpdateDisplayMeta(SectionClusters, "everrun", map[string]string{
		"label":   "새 이름", // label → name
		"site":    "",     // 빈 값 = 키 삭제
		"company": "바뀐회사",
	})
	if err != nil {
		t.Fatalf("UpdateDisplayMeta: %v", err)
	}

	doc := readDoc(t, path)
	if doc["_comment"] == nil {
		t.Error("최상위 _comment 키가 재작성에서 유실됐다")
	}
	cl := doc["clusters"].([]any)[0].(map[string]any)
	if cl["name"] != "새 이름" {
		t.Errorf("name = %v", cl["name"])
	}
	if _, ok := cl["site"]; ok {
		t.Error("빈 값은 키 삭제여야 한다")
	}
	if cl["company"] != "바뀐회사" {
		t.Errorf("company = %v", cl["company"])
	}
	if cl["_cluster_note"] == nil {
		t.Error("항목 내부의 미지의 키(_cluster_note)가 유실됐다")
	}
	// 자격증명이 그대로 남아 있어야 한다(마스킹되면 안 됨)
	if cl["admin_password"] != "fake-admin-password-1" {
		t.Errorf("admin_password changed: %v", cl["admin_password"])
	}

	// .bak 은 직전본 그대로
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf(".bak: %v", err)
	}
	if string(bak) != string(orig) {
		t.Error(".bak 내용이 직전본과 다르다")
	}
	// tmp+rename 규약상 본문 파일은 항상 0600
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode().Perm())
	}
	// 저장된 파일이 그대로 Load 가능해야 한다
	if _, err := Load(path); err != nil {
		t.Fatalf("re-Load: %v", err)
	}
}

func TestUpdateDisplayMetaEdge(t *testing.T) {
	path := copyFixture(t)
	s := NewStore(path)
	err := s.UpdateDisplayMeta(SectionEdgeDevices, "srv-1", map[string]string{
		"vendor": "Dell", "floor_pos": "1,3",
	})
	if err != nil {
		t.Fatalf("UpdateDisplayMeta: %v", err)
	}
	doc := readDoc(t, path)
	dev := doc["edge_devices"].([]any)[4].(map[string]any)
	if dev["vendor"] != "Dell" || dev["floor_pos"] != "1,3" {
		t.Errorf("edge fields = %v/%v", dev["vendor"], dev["floor_pos"])
	}
	if _, ok := dev["_note"]; ok && dev["key"] == "printer-1" {
		// printer-1 의 _note 는 다른 항목 — 여기서는 관계없음
	}
	if err := s.UpdateDisplayMeta(SectionClusters, "no-such-key", map[string]string{"label": "x"}); err == nil {
		t.Error("없는 key 는 에러여야 한다")
	}
}

func TestAddEntry(t *testing.T) {
	path := copyFixture(t)
	s := NewStore(path)

	err := s.AddEntry(SectionEdgeDevices, map[string]any{
		"key": "nas-2", "kind": "nas", "ip": "10.0.0.100", "community": "fake-nas2-community",
	})
	if err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	doc := readDoc(t, path)
	arr := doc["edge_devices"].([]any)
	if len(arr) != 6 {
		t.Fatalf("edge_devices = %d, want 6", len(arr))
	}
	if arr[5].(map[string]any)["key"] != "nas-2" {
		t.Errorf("appended = %v", arr[5])
	}

	// 클러스터 추가
	err = s.AddEntry(SectionClusters, map[string]any{
		"key": "newcluster", "mgmt_ip": "10.0.0.200", "admin_password": "fake-new-pw",
	})
	if err != nil {
		t.Fatalf("AddEntry cluster: %v", err)
	}
	doc = readDoc(t, path)
	if len(doc["clusters"].([]any)) != 3 {
		t.Error("cluster append failed")
	}

	// 중복 key 거절
	if err := s.AddEntry(SectionEdgeDevices, map[string]any{"key": "nas-2"}); err == nil {
		t.Error("중복 key 는 거절돼야 한다")
	}
	// key 없음 거절
	if err := s.AddEntry(SectionEdgeDevices, map[string]any{"kind": "nas"}); err == nil {
		t.Error("key 없는 entry 는 거절돼야 한다")
	}

	// 추가된 파일이 Load 가능해야 한다
	if _, err := Load(path); err != nil {
		t.Fatalf("re-Load: %v", err)
	}
}

func TestRemoveEdgeDevice(t *testing.T) {
	path := copyFixture(t)
	s := NewStore(path)
	if err := s.RemoveEdgeDevice("nas-1"); err != nil {
		t.Fatalf("RemoveEdgeDevice: %v", err)
	}
	doc := readDoc(t, path)
	for _, d := range doc["edge_devices"].([]any) {
		if d.(map[string]any)["key"] == "nas-1" {
			t.Error("nas-1 이 남아 있다")
		}
	}
	if len(doc["edge_devices"].([]any)) != 4 {
		t.Error("edge_devices count wrong")
	}
	if err := s.RemoveEdgeDevice("nas-1"); err == nil {
		t.Error("이미 지운 항목 재삭제는 에러여야 한다")
	}
	// FT 클러스터는 이 API 로 못 지운다 — 섹션이 고정돼 있다
	if err := s.RemoveEdgeDevice("everrun"); err == nil {
		t.Error("clusters 의 key 는 edge 섹션에서 못 찾아 에러여야 한다")
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("re-Load: %v", err)
	}
}

func TestStoreKeepsTrailingNewlineAndUTF8(t *testing.T) {
	path := copyFixture(t)
	s := NewStore(path)
	if err := s.UpdateDisplayMeta(SectionClusters, "everrun", map[string]string{"label": "한글이름"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("trailing newline missing (Python dumps + \"\\n\" 규약)")
	}
	if !strings.Contains(string(data), "한글이름") {
		t.Error("비ASCII 가 이스케이프됐다(ensure_ascii=False 규약 위반)")
	}
}
