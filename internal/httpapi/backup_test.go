package httpapi

// backup.go(설정 내보내기/복구) 테스트 — 마스킹·머지·검증·왕복.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"serverdesk/internal/config"
	"serverdesk/internal/poller"
	"serverdesk/internal/webfront"
)

func TestRedactSecrets(t *testing.T) {
	in := map[string]any{
		"clusters": []any{map[string]any{
			"key": "a", "admin_password": "realpw", "snmp_community": "pub1",
			"nodes": []any{map[string]any{"name": "n0", "root_password": "npw"}},
		}},
		"listen": "0.0.0.0:6005",
	}
	out := redactSecrets(in).(map[string]any)
	cl := out["clusters"].([]any)[0].(map[string]any)
	if cl["admin_password"] != "" || cl["snmp_community"] != "" {
		t.Errorf("비밀 필드가 마스킹되지 않음: %+v", cl)
	}
	if cl["key"] != "a" {
		t.Errorf("비밀 아닌 필드까지 지워짐: %+v", cl)
	}
	nd := cl["nodes"].([]any)[0].(map[string]any)
	if nd["root_password"] != "" {
		t.Errorf("중첩 노드 비밀이 마스킹되지 않음: %+v", nd)
	}
	if out["listen"] != "0.0.0.0:6005" {
		t.Errorf("스칼라 훼손: %+v", out)
	}
}

func TestMergeSecrets(t *testing.T) {
	oldCfg := map[string]any{
		"clusters": []any{map[string]any{"key": "a", "admin_password": "realpw"}},
	}
	newCfg := map[string]any{
		"clusters": []any{map[string]any{"key": "a", "admin_password": "", "label": "새이름"}},
	}
	mergeSecrets(newCfg, oldCfg)
	cl := newCfg["clusters"].([]any)[0].(map[string]any)
	if cl["admin_password"] != "realpw" {
		t.Errorf("빈 비밀은 기존 값을 이어받아야 함: %+v", cl)
	}
	if cl["label"] != "새이름" {
		t.Errorf("비밀 외 필드가 머지로 훼손됨: %+v", cl)
	}
	// 새 장비(구 문서에 없는 key)는 빈 값 유지 — 지어내지 않는다.
	newCfg2 := map[string]any{
		"clusters": []any{map[string]any{"key": "b", "admin_password": ""}},
	}
	mergeSecrets(newCfg2, oldCfg)
	if newCfg2["clusters"].([]any)[0].(map[string]any)["admin_password"] != "" {
		t.Error("신규 장비 비밀을 지어내면 안 됨")
	}
}

func TestValidateConfigDoc(t *testing.T) {
	if err := validateConfigDoc(map[string]any{
		"clusters": []any{map[string]any{"key": "a"}},
	}); err != nil {
		t.Errorf("정상 문서 거부: %v", err)
	}
	err := validateConfigDoc(map[string]any{
		"edge_devices": []any{map[string]any{"name": "nokey"}},
	})
	if err == nil || !strings.Contains(err.Error(), "edge_devices[0]") {
		t.Errorf("key 없는 항목을 인덱스와 함께 거부해야 함: %v", err)
	}
}

// backupTestSrv 는 임시 config + 콘솔 상태로 최소 Server 를 만든다.
func backupTestSrv(t *testing.T, cfgJSON string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.local.json")
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Server{
		Store: config.NewStore(cfgPath),
		Gate:  webfront.New(nil, webfront.Options{StateDir: stateDir}),
	}, cfgPath
}

// loopbackReq 는 루프백 요청(writeGate 통과)과 레코더를 만든다.
func loopbackReq(method, target, body string) (*httptest.ResponseRecorder, *http.Request) {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:4321"
	return httptest.NewRecorder(), r
}

func TestExportMasksAndImportRoundtrip(t *testing.T) {
	srv, cfgPath := backupTestSrv(t, `{
		"listen": "0.0.0.0:6005",
		"clusters": [{"key": "a", "mgmt_ip": "10.0.0.1", "admin_password": "realpw"}],
		"thresholds": {"warn": 80, "crit": 95}
	}`)

	// export
	w, r := loopbackReq("GET", "/api/admin/config/export", "")
	srv.handleConfigExport(w, r)
	if w.Code != 200 {
		t.Fatalf("export %d: %s", w.Code, w.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("export JSON: %v", err)
	}
	if doc["schema"] != backupSchema {
		t.Errorf("schema: %v", doc["schema"])
	}
	if doc["exported_at"] == nil || doc["config"] == nil || doc["ui"] == nil {
		t.Errorf("export 4키 부족: %v", doc)
	}
	exCl := doc["config"].(map[string]any)["clusters"].([]any)[0].(map[string]any)
	if exCl["admin_password"] != "" {
		t.Errorf("export 에 비밀 평문 노출: %+v", exCl)
	}

	// import — 라벨만 바꾼 문서를 되돌려 넣는다(비밀은 머지로 보존돼야 함).
	doc["config"].(map[string]any)["clusters"].([]any)[0].(map[string]any)["label"] = "바뀐라벨"
	payload, _ := json.Marshal(doc)
	w2, r2 := loopbackReq("POST", "/api/admin/config/import", string(payload))
	srv.handleConfigImport(w2, r2)
	if w2.Code != 200 {
		t.Fatalf("import %d: %s", w2.Code, w2.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("import JSON: %v", err)
	}
	// thresholds 는 라이브 반영 섹션이라 restart 목록에 없어야 하고, clusters 도 값이 같으면 안 뜬다.
	// (여기서는 clusters 가 바뀌었으므로 restart_required 에 포함)
	rr, _ := resp["restart_required"].([]any)
	found := false
	for _, k := range rr {
		if k == "clusters" {
			found = true
		}
		if k == "thresholds" {
			t.Error("thresholds 는 라이브 반영이라 restart_required 에 오면 안 됨")
		}
	}
	if !found {
		t.Errorf("clusters 변경이 restart_required 에 없음: %v", rr)
	}

	// 파일 검사 — 비밀 머지 + 라벨 반영.
	data, _ := os.ReadFile(cfgPath)
	var fileDoc map[string]any
	if err := json.Unmarshal(data, &fileDoc); err != nil {
		t.Fatalf("저장 파일 파싱: %v", err)
	}
	fCl := fileDoc["clusters"].([]any)[0].(map[string]any)
	if fCl["admin_password"] != "realpw" {
		t.Errorf("복구 후 비밀이 유실됨: %+v", fCl)
	}
	if fCl["label"] != "바뀐라벨" {
		t.Errorf("복구 후 라벨 미반영: %+v", fCl)
	}
	// 라이브 임계값 반영 확인(export 문서의 80/95).
	if wv, cv := poller.UsageThresholds(); wv != 80 || cv != 95 {
		t.Errorf("import 후 라이브 임계값 미반영: %v/%v", wv, cv)
	}
}

func TestImportRejectsBadSchema(t *testing.T) {
	srv, _ := backupTestSrv(t, `{"clusters": [{"key": "a", "mgmt_ip": "10.0.0.1"}]}`)
	w, r := loopbackReq("POST", "/api/admin/config/import", `{"schema":"other/9","config":{}}`)
	srv.handleConfigImport(w, r)
	if w.Code != 400 {
		t.Errorf("schema 오류는 400 이어야 함: %d", w.Code)
	}
}
