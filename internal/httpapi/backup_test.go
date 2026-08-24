package httpapi

// backup.go(설정 내보내기/복구) 테스트 — 마스킹·머지·검증·왕복.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func TestSecretPolicyIsNotRedacted(t *testing.T) {
	doc := map[string]any{"secret_policy": config.SecretPolicyRequireReferences, "password": "value"}
	redacted := redactSecrets(doc).(map[string]any)
	if redacted["secret_policy"] != config.SecretPolicyRequireReferences || redacted["password"] != "" {
		t.Fatalf("redaction contract = %#v", redacted)
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
	duplicate := validateConfigDoc(map[string]any{
		"clusters": []any{map[string]any{"key": "a"}, map[string]any{"key": "a"}},
	})
	if duplicate == nil || !strings.Contains(duplicate.Error(), "중복") {
		t.Errorf("중복 key를 거부해야 함: %v", duplicate)
	}
}

func TestMergeSecretsDoesNotUseArrayPosition(t *testing.T) {
	oldCfg := map[string]any{
		"clusters": []any{map[string]any{
			"key": "cluster-a",
			"nodes": []any{
				map[string]any{"ip": "10.0.0.1", "root_password": "one"},
				map[string]any{"ip": "10.0.0.2", "root_password": "two"},
			},
		}},
	}
	newCfg := map[string]any{
		"clusters": []any{map[string]any{
			"key": "cluster-a",
			"nodes": []any{
				map[string]any{"ip": "10.0.0.3", "root_password": ""},
				map[string]any{"ip": "10.0.0.2", "root_password": ""},
				map[string]any{"ip": "10.0.0.1", "root_password": ""},
			},
		}},
	}

	mergeSecrets(newCfg, oldCfg)
	nodes := newCfg["clusters"].([]any)[0].(map[string]any)["nodes"].([]any)
	if nodes[0].(map[string]any)["root_password"] != "" {
		t.Errorf("새 IP 노드가 배열 위치의 비밀을 받음: %+v", nodes[0])
	}
	if nodes[1].(map[string]any)["root_password"] != "two" || nodes[2].(map[string]any)["root_password"] != "one" {
		t.Errorf("IP 기반 재정렬 비밀 머지 실패: %+v", nodes)
	}
}

func TestMergeSecretsDoesNotTransferChangedEndpoint(t *testing.T) {
	oldCfg := map[string]any{"nodes": []any{map[string]any{"ip": "10.0.0.1", "root_password": "one"}}}
	newCfg := map[string]any{"nodes": []any{map[string]any{"ip": "10.0.0.9", "root_password": ""}}}

	mergeSecrets(newCfg, oldCfg)
	node := newCfg["nodes"].([]any)[0].(map[string]any)
	if node["root_password"] != "" {
		t.Errorf("변경된 endpoint에 비밀이 이동함: %+v", node)
	}
}

func TestMergeSecretsDoesNotTransferChangedDeviceEndpoints(t *testing.T) {
	oldCfg := map[string]any{
		"clusters": []any{map[string]any{
			"key": "cluster-a", "mgmt_ip": "10.0.0.1", "admin_password": "cluster-secret",
		}},
		"edge_devices": []any{map[string]any{
			"key": "server-a", "ip": "10.0.0.2", "bmc_ip": "10.0.0.3",
			"password": "host-secret", "bmc_password": "bmc-secret",
		}},
	}
	newCfg := map[string]any{
		"clusters": []any{map[string]any{
			"key": "cluster-a", "mgmt_ip": "10.0.0.9", "admin_password": "",
		}},
		"edge_devices": []any{map[string]any{
			"key": "server-a", "ip": "10.0.0.8", "bmc_ip": "10.0.0.7",
			"password": "", "bmc_password": "",
		}},
	}

	mergeSecrets(newCfg, oldCfg)
	cluster := newCfg["clusters"].([]any)[0].(map[string]any)
	if cluster["admin_password"] != "" {
		t.Errorf("changed cluster endpoint received old secret: %+v", cluster)
	}
	edge := newCfg["edge_devices"].([]any)[0].(map[string]any)
	if edge["password"] != "" || edge["bmc_password"] != "" {
		t.Errorf("changed edge endpoint received old secrets: %+v", edge)
	}
}

// backupTestSrv 는 임시 config + 콘솔 상태로 최소 Server 를 만든다.
func backupTestSrv(t *testing.T, cfgJSON string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.local.json")
	var fixtureDoc map[string]any
	if err := json.Unmarshal([]byte(cfgJSON), &fixtureDoc); err != nil {
		t.Fatalf("invalid fixture config: %v", err)
	}
	if _, exists := fixtureDoc["secret_policy"]; !exists {
		fixtureDoc["secret_policy"] = config.SecretPolicyAllowPlaintext
	}
	fixtureJSON, err := json.Marshal(fixtureDoc)
	if err != nil {
		t.Fatalf("marshal fixture config: %v", err)
	}
	if err := os.WriteFile(cfgPath, fixtureJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	events := poller.NewEventLog(filepath.Join(dir, "events.jsonl"), 500)
	return &Server{
		Store:  config.NewStore(cfgPath),
		Gate:   webfront.New(nil, webfront.Options{StateDir: stateDir, AllowWrites: true}),
		Events: events,
		Audit:  events,
	}, cfgPath
}

// loopbackReq 는 Origin 없는 로컬 자동화 요청과 레코더를 만든다.
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
	doc["reason"] = "검증된 백업 복구"
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
	reloaded := poller.NewEventLog(filepath.Join(filepath.Dir(cfgPath), "events.jsonl"), 500)
	audits := reloaded.List(2)
	if len(audits) != 2 {
		t.Fatalf("persisted restore audit records = %d, want 2", len(audits))
	}
	committed := audits[0].(map[string]any)
	prepared := audits[1].(map[string]any)
	if committed["phase"] != "committed" || prepared["phase"] != "prepared" ||
		committed["audit_id"] != prepared["audit_id"] || committed["action"] != "config.restore" ||
		committed["target"] != "configuration" || committed["reason"] != "검증된 백업 복구" ||
		committed["operator"] != "admin" {
		t.Fatalf("persisted restore audit = prepared %#v committed %#v", prepared, committed)
	}
}

func TestImportRejectsBadSchema(t *testing.T) {
	srv, _ := backupTestSrv(t, `{"clusters": [{"key": "a", "mgmt_ip": "10.0.0.1"}]}`)
	w, r := loopbackReq("POST", "/api/admin/config/import", `{"schema":"other/9","config":{},"reason":"스키마 검증"}`)
	srv.handleConfigImport(w, r)
	if w.Code != 400 {
		t.Errorf("schema 오류는 400 이어야 함: %d", w.Code)
	}
}
func TestImportRejectsProcessConfigWithoutWrite(t *testing.T) {
	srv, cfgPath := backupTestSrv(t, `{"listen":"127.0.0.1:6005","clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}]}`)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"schema":"serverdesk-config/1","config":{"listen":"0.0.0.0:9999","clusters":[{"key":"a","mgmt_ip":"10.0.0.2"}]},"reason":"프로세스 설정 복구"}`
	w, r := loopbackReq("POST", "/api/admin/config/import", payload)
	srv.handleConfigImport(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "listen") {
		t.Fatalf("process config import = %d: %s", w.Code, w.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("거부된 process config import가 설정을 썼음")
	}
}

func TestImportRestoresRedactedProtectedSecrets(t *testing.T) {
	srv, cfgPath := backupTestSrv(t, `{
		"snmp_community":"top-secret",
		"trap":{"enabled":true,"community":"trap-secret","port":10162},
		"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}]
	}`)
	payload := `{
		"schema":"serverdesk-config/1",
		"reason":"보호된 비밀 유지 복구",
		"config":{
			"snmp_community":"",
			"trap":{"enabled":true,"community":"","port":10162},
			"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}]
		}
	}`
	w, r := loopbackReq("POST", "/api/admin/config/import", payload)
	srv.handleConfigImport(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("redacted protected secret import = %d: %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["snmp_community"] != "top-secret" ||
		stored["trap"].(map[string]any)["community"] != "trap-secret" {
		t.Fatalf("protected secrets were not restored: %+v", stored)
	}
}

func TestImportRejectsInvalidUIWithoutConfigWrite(t *testing.T) {
	srv, cfgPath := backupTestSrv(t, `{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}]}`)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"schema":"serverdesk-config/1","config":{"clusters":[{"key":"a","mgmt_ip":"10.0.0.2"}]},"ui":{"notes":"invalid"},"reason":"콘솔 상태 복구"}`
	w, r := loopbackReq("POST", "/api/admin/config/import", payload)
	srv.handleConfigImport(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "ui.notes") {
		t.Fatalf("invalid UI import = %d: %s", w.Code, w.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("invalid UI import가 설정을 썼음")
	}
}

func TestConfigImportRequiresBoundedReasonWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		reason any
		omit   bool
	}{
		{name: "missing", omit: true},
		{name: "blank", reason: " \n\t "},
		{name: "non string", reason: 42},
		{name: "over 500 unicode runes", reason: strings.Repeat("나", 501)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, cfgPath := backupTestSrv(t, `{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}]}`)
			before, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			doc := map[string]any{
				"schema": backupSchema,
				"config": map[string]any{"clusters": []any{map[string]any{"key": "a", "mgmt_ip": "10.0.0.2"}}},
			}
			if !tc.omit {
				doc["reason"] = tc.reason
			}
			payload, _ := json.Marshal(doc)
			w, r := loopbackReq(http.MethodPost, "/api/admin/config/import", string(payload))
			srv.handleConfigImport(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("import = %d: %s", w.Code, w.Body.String())
			}
			after, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) || srv.Events.Len() != 0 {
				t.Fatalf("invalid reason changed state: config_equal=%v audits=%d", string(after) == string(before), srv.Events.Len())
			}
		})
	}
}

func TestConfigImportRejectsInvalidUTF8WithoutMutation(t *testing.T) {
	srv, cfgPath := backupTestSrv(t, `{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}]}`)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte(`{"schema":"serverdesk-config/1","config":{},"reason":"`), 0xff)
	payload = append(payload, []byte(`"}`)...)
	w, r := loopbackReq(http.MethodPost, "/api/admin/config/import", string(payload))
	srv.handleConfigImport(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid UTF-8 import = %d: %s", w.Code, w.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || srv.Events.Len() != 0 {
		t.Fatalf("invalid UTF-8 changed state: config_equal=%v audits=%d", string(after) == string(before), srv.Events.Len())
	}
}

func TestConfigImportPreparedAuditFailureDoesNotMutate(t *testing.T) {
	srv, cfgPath := backupTestSrv(t, `{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}]}`)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv.Audit = &failNthAuditRecorder{delegate: srv.Events, failAt: 1}
	payload := `{"schema":"serverdesk-config/1","reason":"감사 준비 실패 검증","config":{"clusters":[{"key":"a","mgmt_ip":"10.0.0.2"}]}}`
	w, r := loopbackReq(http.MethodPost, "/api/admin/config/import", payload)
	srv.handleConfigImport(w, r)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "복구하지 않았습니다") {
		t.Fatalf("prepared audit failure = %d: %s", w.Code, w.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || srv.Events.Len() != 0 {
		t.Fatalf("prepared audit failure mutated state: config_equal=%v audits=%d", string(after) == string(before), srv.Events.Len())
	}
}

func TestConfigImportCommittedAuditFailureRollsBack(t *testing.T) {
	srv, cfgPath := backupTestSrv(t, `{
		"clusters":[{"key":"a","mgmt_ip":"10.0.0.1","name":"old"}],
		"thresholds":{"warn":70,"crit":90}
	}`)
	before, err := srv.Store.ReadDoc()
	if err != nil {
		t.Fatal(err)
	}
	previousWarn, previousCrit := poller.UsageThresholds()
	t.Cleanup(func() { poller.SetThresholds(previousWarn, previousCrit) })
	poller.SetThresholds(73, 91)
	oldUI := map[string]any{
		"ack":   map[string]any{"old-ack": "2026-08-24T00:00:00Z"},
		"maint": map[string]any{"old-maint": map[string]any{"until": "2026-08-25T00:00:00Z"}},
		"notes": map[string]any{"old-note": map[string]any{"text": "before"}},
		"escal": map[string]any{"old-escal": "2026-08-24T00:00:00Z"},
	}
	if err := srv.Gate.ImportUIState(oldUI); err != nil {
		t.Fatalf("seed UI state: %v", err)
	}
	srv.Audit = &failNthAuditRecorder{delegate: srv.Events, failAt: 2}
	payload := `{
		"schema":"serverdesk-config/1",
		"reason":"감사 완료 실패 롤백 검증",
		"config":{
			"clusters":[{"key":"a","mgmt_ip":"10.0.0.1","name":"new"}],
			"thresholds":{"warn":60,"crit":80}
		},
		"ui":{
			"ack":{"new-ack":"2026-08-24T01:00:00Z"},
			"maint":{"new-maint":{"until":"2026-08-26T00:00:00Z"}},
			"notes":{"new-note":{"text":"after"}},
			"escal":{"new-escal":"2026-08-24T01:00:00Z"}
		}
	}`
	w, r := loopbackReq(http.MethodPost, "/api/admin/config/import", payload)
	srv.handleConfigImport(w, r)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "롤백") {
		t.Fatalf("committed audit failure = %d: %s", w.Code, w.Body.String())
	}
	after, err := srv.Store.ReadDoc()
	if err != nil {
		t.Fatal(err)
	}
	beforePlain, beforeErr := rawToAny(before)
	afterPlain, afterErr := rawToAny(after)
	if beforeErr != nil || afterErr != nil || !reflect.DeepEqual(beforePlain, afterPlain) {
		t.Fatalf("config was not rolled back: before=%#v after=%#v path=%s", before, after, cfgPath)
	}
	if warn, crit := poller.UsageThresholds(); warn != 73 || crit != 91 {
		t.Fatalf("runtime thresholds were not rolled back: %v/%v", warn, crit)
	}
	restoredUI, err := srv.Gate.ExportUIStateWithError()
	if err != nil || !reflect.DeepEqual(restoredUI, oldUI) {
		t.Fatalf("UI state was not rolled back: got=%#v want=%#v err=%v", restoredUI, oldUI, err)
	}
	reloaded := poller.NewEventLog(filepath.Join(filepath.Dir(cfgPath), "events.jsonl"), 500)
	items := reloaded.List(10)
	if len(items) != 2 || items[0].(map[string]any)["phase"] != "rolled_back" ||
		items[1].(map[string]any)["phase"] != "prepared" {
		t.Fatalf("restore rollback audit trail = %#v", items)
	}
}

type callbackAuditRecorder struct {
	delegate AuditRecorder
	once     func()
}

func (r *callbackAuditRecorder) RecordAudit(record poller.AuditRecord) error {
	if err := r.delegate.RecordAudit(record); err != nil {
		return err
	}
	if r.once != nil {
		callback := r.once
		r.once = nil
		callback()
	}
	return nil
}

func TestConfigImportConcurrentChangePreservedAndAuditedFailed(t *testing.T) {
	srv, cfgPath := backupTestSrv(t, `{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}]}`)
	var callbackErr error
	srv.Audit = &callbackAuditRecorder{delegate: srv.Events, once: func() {
		callbackErr = srv.Store.SetSectionValue("_concurrent", "preserved")
	}}
	payload := `{"schema":"serverdesk-config/1","reason":"동시 변경 충돌 검증","config":{"clusters":[{"key":"a","mgmt_ip":"10.0.0.2"}]}}`
	w, r := loopbackReq(http.MethodPost, "/api/admin/config/import", payload)
	srv.handleConfigImport(w, r)
	if callbackErr != nil {
		t.Fatalf("inject concurrent change: %v", callbackErr)
	}
	if w.Code != http.StatusConflict {
		t.Fatalf("concurrent import = %d: %s", w.Code, w.Body.String())
	}
	current, err := srv.Store.ReadDoc()
	if err != nil {
		t.Fatal(err)
	}
	var concurrent string
	if err := json.Unmarshal(current["_concurrent"], &concurrent); err != nil || concurrent != "preserved" {
		t.Fatalf("concurrent state lost: value=%q err=%v doc=%#v", concurrent, err, current)
	}
	var clusters []map[string]any
	if err := json.Unmarshal(current["clusters"], &clusters); err != nil || clusters[0]["mgmt_ip"] != "10.0.0.1" {
		t.Fatalf("stale import was applied: clusters=%#v err=%v", clusters, err)
	}
	reloaded := poller.NewEventLog(filepath.Join(filepath.Dir(cfgPath), "events.jsonl"), 500)
	items := reloaded.List(2)
	if len(items) != 2 || items[0].(map[string]any)["phase"] != "failed" ||
		items[1].(map[string]any)["phase"] != "prepared" {
		t.Fatalf("conflict audit trail = %#v", items)
	}
}
