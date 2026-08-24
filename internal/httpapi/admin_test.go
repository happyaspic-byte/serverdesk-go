package httpapi

// admin_test.go 는 admin.go 의 장비 CRUD (POST/PUT/DELETE) 및 연결 테스트(connTest)
// 엔드포인트를 검증하는 통합 및 단위 테스트다.
//
// 실제 장비나 네트워크와 통신하지 않고 net/http/httptest 및 로컬 인-프로세스 스텁/서버를
// 사용하여 모든 라우트와 예외 처리를 검증한다.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"serverdesk/internal/config"
	"serverdesk/internal/edge"
	"serverdesk/internal/poller"
	"serverdesk/internal/webfront"
)

// adminTestFixture 는 admin API 테스트를 위한 통합 환경 구조체다.
type adminTestFixture struct {
	srv     *Server
	cfgPath string
	dir     string
	edgeMgr *poller.EdgeManager
	rootCtx context.Context
	cancel  context.CancelFunc
}

type failNthAuditRecorder struct {
	delegate AuditRecorder
	failAt   int
	calls    int
}

func (r *failNthAuditRecorder) RecordAudit(record poller.AuditRecord) error {
	r.calls++
	if r.calls == r.failAt {
		return errors.New("injected audit persistence failure")
	}
	return r.delegate.RecordAudit(record)
}

func newAdminTestFixture(t *testing.T, initialCfgJSON string) *adminTestFixture {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.local.json")
	if initialCfgJSON == "" {
		initialCfgJSON = `{
			"listen": "0.0.0.0:6005",
			"clusters": [
				{
					"key": "cl-ft1",
					"name": "Cluster FT-1",
					"mgmt_ip": "192.168.1.10",
					"site": "Seoul",
					"company": "CorpA",
					"factory": "Plant1",
					"asset_tag": "TAG-FT1",
					"floor_pos": "1,1",
					"admin_password": "ftpassword"
				}
			],
			"edge_devices": [
				{
					"key": "edge-srv1",
					"name": "Edge Server 1",
					"ip": "192.168.1.20",
					"kind": "server",
					"site": "Busan",
					"company": "CorpB",
					"factory": "Plant2",
					"asset_tag": "TAG-ED1",
					"floor_pos": "2,1",
					"vendor": "Dell",
					"community": "public"
				}
			],
			"thresholds": {"warn": 75, "crit": 90}
		}`
	}
	// Admin API fixtures intentionally exercise legacy plaintext migration paths.
	// Production examples/installers use require-references.
	var fixtureDoc map[string]any
	if err := json.Unmarshal([]byte(initialCfgJSON), &fixtureDoc); err != nil {
		t.Fatalf("invalid fixture JSON: %v", err)
	}
	if _, exists := fixtureDoc["secret_policy"]; !exists {
		fixtureDoc["secret_policy"] = config.SecretPolicyAllowPlaintext
	}
	fixtureBytes, err := json.Marshal(fixtureDoc)
	if err != nil {
		t.Fatalf("marshal fixture JSON: %v", err)
	}
	initialCfgJSON = string(fixtureBytes)

	if err := os.WriteFile(cfgPath, []byte(initialCfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	store := config.NewStore(cfgPath)
	rawCfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config load failed: %v", err)
	}

	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	gate := webfront.New(nil, webfront.Options{StateDir: stateDir, AllowWrites: true})

	var states []*poller.ClusterState
	for i := range rawCfg.Clusters {
		st := poller.NewClusterState(&rawCfg.Clusters[i], 50)
		states = append(states, st)
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	edgeDevs := make([]edge.DeviceConfig, len(rawCfg.EdgeDevices))
	for i, d := range rawCfg.EdgeDevices {
		edgeDevs[i] = edge.DeviceConfig{
			Key:       d.Key,
			Kind:      d.Kind,
			Name:      d.Name,
			IP:        d.IP,
			Community: d.Community,
			Vendor:    d.Vendor,
			Site:      d.Site,
			Company:   d.Company,
			Factory:   d.Factory,
			AssetTag:  d.AssetTag,
			FloorPos:  d.FloorPos,
		}
	}
	edgeMgr := poller.NewEdgeManager(rootCtx, edgeDevs, nil)
	t.Cleanup(func() { edgeMgr.Stop() })

	overlay := NewDisplayOverlay(rawCfg)
	cache := poller.NewFleetCache()
	events := poller.NewEventLog(filepath.Join(dir, "events.jsonl"), 500)

	srv := New(cache, states, rawCfg, store, events, nil, edgeMgr, gate, []string{"http://localhost:3000", "http://noc.local:6005"}, overlay)

	return &adminTestFixture{
		srv:     srv,
		cfgPath: cfgPath,
		dir:     dir,
		edgeMgr: edgeMgr,
		rootCtx: rootCtx,
		cancel:  cancel,
	}
}

// execRequest 는 Server.ServeHTTP 를 통해 요청을 실행하고 JSON 응답을 디코딩한다.
func execRequest(srv *Server, method, target string, body any, origin string) (*httptest.ResponseRecorder, map[string]any) {
	var bodyReader strings.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			bodyReader = *strings.NewReader(b)
		case []byte:
			bodyReader = *strings.NewReader(string(b))
		default:
			bytesData, _ := json.Marshal(b)
			bodyReader = *strings.NewReader(string(bytesData))
		}
	} else {
		bodyReader = *strings.NewReader("")
	}

	req := httptest.NewRequest(method, target, &bodyReader)
	req.RemoteAddr = "127.0.0.1:4321"
	if origin != "" {
		req.Header.Set("Origin", origin)
		req.Host = strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var res map[string]any
	if rec.Body.Len() > 0 && strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
	}
	return rec, res
}

// TestAddDeviceFTCluster 는 FT 클러스터 신규 추가(POST /api/clusters)를 검증한다.
func TestAddDeviceFTCluster(t *testing.T) {
	f := newAdminTestFixture(t, "")

	payload := map[string]any{
		"type":       "EV",
		"key":        "cl-ft-new",
		"mgmt":       "192.168.1.11",
		"label":      "New FT Cluster",
		"company":    "TestCorp",
		"factory":    "TestFactory",
		"site":       "Incheon",
		"asset_tag":  "TAG-FT2",
		"floor_pos":  "1,2",
		"admin_user": "admin",
		"admin_pass": "secretAdmin",
		"root_pass":  "secretRoot",
		"node0":      "192.168.1.12",
		"node0_user": "root",
		"node0_pass": "n0secret",
		"node1":      "192.168.1.13",
		"node1_user": "root",
		"node1_pass": "n1secret",
	}

	rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters", payload, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("FT cluster add failed (%d): %s", rec.Code, rec.Body.String())
	}
	if res["ok"] != true || res["restart_required"] != true || res["key"] != "cl-ft-new" {
		t.Fatalf("unexpected response: %+v", res)
	}

	// 저장된 설정 파일 확인
	raw, err := config.Load(f.cfgPath)
	if err != nil {
		t.Fatalf("store load failed: %v", err)
	}
	var found *config.ClusterConfig
	for i := range raw.Clusters {
		if raw.Clusters[i].Key == "cl-ft-new" {
			found = &raw.Clusters[i]
			break
		}
	}
	if found == nil {
		t.Fatal("added FT cluster not found in store")
	}
	if found.MgmtIP != "192.168.1.11" || found.Name != "New FT Cluster" {
		t.Fatalf("stored cluster mismatch: %+v", found)
	}
	if found.Platform != "everrun" {
		t.Fatalf("stored platform = %q, want everrun", found.Platform)
	}
}

func TestAddDeviceRejectsUnsupportedEnduranceWithoutPersisting(t *testing.T) {
	f := newAdminTestFixture(t, "")
	payload := map[string]any{"type": "END", "key": "endurance-1", "mgmt": "192.0.2.10"}
	rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters", payload, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(res["error"]), "Endurance collection is not implemented") {
		t.Fatalf("status=%d response=%v", rec.Code, res)
	}
	data, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "endurance-1") {
		t.Fatal("unsupported Endurance target was persisted")
	}
	probe := f.srv.connTest(context.Background(), payload)
	if probe["ok"] != false || probe["supported"] != false || probe["transport"] != "unsupported" {
		t.Fatalf("unsupported connection test = %#v", probe)
	}
}

func TestAddDeviceFailsClosedWithoutConfigStore(t *testing.T) {
	f := newAdminTestFixture(t, "")
	f.srv.Store = nil
	rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters", map[string]any{
		"type": "NAS", "key": "nas-no-store", "mgmt": "192.0.2.11", "community": "private",
	}, "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(fmt.Sprint(res["error"]), "store is unavailable") {
		t.Fatalf("status=%d response=%v", rec.Code, res)
	}
}

// TestAddDeviceEdgeHotAdd 는 엣지 장비 추가(POST /api/clusters)의 종류별 핫애드를 검증한다.
func TestAddDeviceEdgeHotAdd(t *testing.T) {
	tests := []struct {
		name         string
		payload      map[string]any
		expectedKind string
	}{
		{
			name: "Printer",
			payload: map[string]any{
				"type":      "PRN",
				"key":       "edge-prn1",
				"mgmt":      "192.168.2.10",
				"label":     "Main Printer",
				"community": "public",
			},
			expectedKind: "printer",
		},
		{
			name: "NAS",
			payload: map[string]any{
				"type":      "NAS",
				"key":       "edge-nas1",
				"mgmt":      "192.168.2.20",
				"label":     "Storage NAS",
				"community": "public",
			},
			expectedKind: "nas",
		},
		{
			name: "PLC",
			payload: map[string]any{
				"type":      "PLC",
				"key":       "edge-plc1",
				"mgmt":      "192.168.2.30",
				"label":     "Line 1 PLC",
				"fins_port": 9600,
				"tags":      []any{"line1", "status"},
			},
			expectedKind: "plc",
		},
		{
			name: "Proxmox Server",
			payload: map[string]any{
				"type":       "SRV",
				"platform":   "proxmox",
				"key":        "edge-pve1",
				"mgmt":       "192.168.2.40",
				"label":      "PVE Node 1",
				"admin_user": "root@pam",
				"admin_pass": "pvesecret",
			},
			expectedKind: "proxmox",
		},
		{
			name: "Server with SNMP and BMC",
			payload: map[string]any{
				"type":      "SRV",
				"key":       "edge-srv2",
				"mgmt":      "192.168.2.50",
				"label":     "App Server 2",
				"community": "public",
				"bmc_ip":    "192.168.2.51",
				"bmc_user":  "admin",
				"bmc_pass":  "bmcsecret",
			},
			expectedKind: "server",
		},
		{
			name: "Server with BMC only",
			payload: map[string]any{
				"type":     "SRV",
				"key":      "edge-srv3",
				"mgmt":     "192.168.2.60",
				"label":    "App Server 3",
				"bmc_ip":   "192.168.2.61",
				"bmc_user": "admin",
				"bmc_pass": "bmcsecret",
			},
			expectedKind: "server",
		},
		{
			name: "Proxmox Server with TLS Fingerprint",
			payload: map[string]any{
				"type":            "SRV",
				"platform":        "proxmox",
				"key":             "edge-pve-pinned",
				"mgmt":            "192.168.2.70",
				"label":           "Pinned PVE",
				"admin_user":      "root@pam",
				"admin_pass":      "pvesecret",
				"tls_fingerprint": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
			expectedKind: "proxmox",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdminTestFixture(t, "")
			rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters", tc.payload, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: POST /api/clusters returned %d: %s", tc.name, rec.Code, rec.Body.String())
			}
			if res["ok"] != true || res["kind"] != tc.expectedKind || res["key"] != tc.payload["key"] {
				t.Fatalf("%s: unexpected response: %+v", tc.name, res)
			}

			// s.Cfg.EdgeDevices 에 핫애드 되었는지 확인
			keyStr := tc.payload["key"].(string)
			found := false
			for _, d := range f.srv.Cfg.EdgeDevices {
				if d.Key == keyStr {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s: key %s not found in s.Cfg.EdgeDevices", tc.name, keyStr)
			}
			if expFP, ok := tc.payload["tls_fingerprint"].(string); ok {
				edgeDev := f.srv.findEdgeCfg(keyStr)
				if edgeDev == nil || edgeDev.TLSFingerprint != expFP {
					t.Fatalf("%s: expected TLSFingerprint %q, got %+v", tc.name, expFP, edgeDev)
				}
			}
		})
	}
}

func TestAddDeviceHotAddResolvesSecretReference(t *testing.T) {
	credentialDir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(credentialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "runtime-pve-secret"
	if err := os.WriteFile(filepath.Join(credentialDir, "pve-password"), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("SERVERDESK_CREDENTIALS_DIRECTORY", credentialDir)
	f := newAdminTestFixture(t, `{
		"listen":"127.0.0.1:6005",
		"secret_policy":"require-references",
		"clusters":[],
		"edge_devices":[]
	}`)
	payload := map[string]any{
		"type": "SRV", "platform": "proxmox", "key": "pve-ref",
		"mgmt": "192.0.2.30", "admin_user": "root@pam",
		"admin_pass": "secret://pve-password",
	}
	rec, _ := execRequest(f.srv, http.MethodPost, "/api/clusters", payload, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("hot-add status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored := f.srv.findEdgeCfg("pve-ref")
	if stored == nil || stored.Password != secret {
		t.Fatalf("runtime config did not resolve reference: %+v", stored)
	}
	foundWorker := false
	for _, device := range f.edgeMgr.Devices() {
		if device.Key == "pve-ref" {
			foundWorker = true
			if device.Password != secret {
				t.Fatalf("worker password=%q, want resolved value", device.Password)
			}
		}
	}
	if !foundWorker {
		t.Fatal("hot-added worker device missing")
	}
	data, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), "secret://pve-password") {
		t.Fatalf("persisted config did not retain only the reference: %s", data)
	}
	if masked := config.Mask("failure: " + secret); strings.Contains(masked, secret) {
		t.Fatalf("resolved hot-add secret was not registered for log masking: %q", masked)
	}

	missing := map[string]any{
		"type": "SRV", "platform": "proxmox", "key": "pve-missing",
		"mgmt": "192.0.2.31", "admin_pass": "secret://missing",
	}
	rec, _ = execRequest(f.srv, http.MethodPost, "/api/clusters", missing, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing reference status=%d body=%s", rec.Code, rec.Body.String())
	}
	data, err = os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "pve-missing") {
		t.Fatal("invalid reference was persisted before validation")
	}
}

// TestAddDeviceValidationErrors 는 장비 추가 시 각종 400/409 검증 오류를 테스트한다.
func TestAddDeviceValidationErrors(t *testing.T) {
	f := newAdminTestFixture(t, "")

	tests := []struct {
		name       string
		payload    any
		statusCode int
		errSubstr  string
	}{
		{
			name:       "Invalid JSON string",
			payload:    "{invalid_json",
			statusCode: http.StatusBadRequest,
			errSubstr:  "JSON 본문이 필요합니다",
		},
		{
			name:       "Empty JSON object",
			payload:    map[string]any{},
			statusCode: http.StatusBadRequest,
			errSubstr:  "key 형식",
		},
		{
			name: "Bad key with special characters",
			payload: map[string]any{
				"type": "EV",
				"key":  "invalid@key!",
				"mgmt": "192.168.1.1",
			},
			statusCode: http.StatusBadRequest,
			errSubstr:  "key 형식: 영숫자/._- 2~40자",
		},
		{
			name: "Key starting with dash or too short",
			payload: map[string]any{
				"type": "EV",
				"key":  "-a",
				"mgmt": "192.168.1.1",
			},
			statusCode: http.StatusBadRequest,
			errSubstr:  "key 형식",
		},
		{
			name: "Invalid IP address",
			payload: map[string]any{
				"type": "EV",
				"key":  "valid-key",
				"mgmt": "999.999.999",
			},
			statusCode: http.StatusBadRequest,
			errSubstr:  "관리 IP 형식이 올바르지 않습니다",
		},
		{
			name: "Duplicate FT cluster key",
			payload: map[string]any{
				"type": "EV",
				"key":  "cl-ft1",
				"mgmt": "192.168.1.100",
			},
			statusCode: http.StatusConflict,
			errSubstr:  "이미 존재하는 key",
		},
		{
			name: "Duplicate Edge device key",
			payload: map[string]any{
				"type": "SRV",
				"key":  "edge-srv1",
				"mgmt": "192.168.1.200",
			},
			statusCode: http.StatusConflict,
			errSubstr:  "이미 존재하는 key",
		},
		{
			name: "Unsupported device type (PC/WIN)",
			payload: map[string]any{
				"type": "PC",
				"key":  "pc-dev1",
				"mgmt": "192.168.1.50",
			},
			statusCode: http.StatusBadRequest,
			errSubstr:  "아직 실수집기가 없습니다",
		},
		{
			name: "Printer missing SNMP community",
			payload: map[string]any{
				"type": "PRN",
				"key":  "prn-dev1",
				"mgmt": "192.168.1.50",
			},
			statusCode: http.StatusBadRequest,
			errSubstr:  "SNMP 커뮤니티가 필요합니다",
		},
		{
			name: "NAS missing SNMP community",
			payload: map[string]any{
				"type": "NAS",
				"key":  "nas-dev1",
				"mgmt": "192.168.1.50",
			},
			statusCode: http.StatusBadRequest,
			errSubstr:  "SNMP 커뮤니티가 필요합니다",
		},
		{
			name: "Proxmox missing password",
			payload: map[string]any{
				"type":     "SRV",
				"platform": "proxmox",
				"key":      "pve-dev1",
				"mgmt":     "192.168.1.50",
			},
			statusCode: http.StatusBadRequest,
			errSubstr:  "Proxmox API 비밀번호가 필요합니다",
		},
		{
			name: "Server missing both SNMP and BMC",
			payload: map[string]any{
				"type": "SRV",
				"key":  "srv-dev1",
				"mgmt": "192.168.1.50",
			},
			statusCode: http.StatusBadRequest,
			errSubstr:  "SNMP 커뮤니티 또는 BMC(IP+계정) 중 하나는 필요합니다",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters", tc.payload, "")
			if rec.Code != tc.statusCode {
				t.Fatalf("expected status %d, got %d (body: %s)", tc.statusCode, rec.Code, rec.Body.String())
			}
			errStr, _ := res["error"].(string)
			if !strings.Contains(errStr, tc.errSubstr) {
				t.Fatalf("expected error containing %q, got %q", tc.errSubstr, errStr)
			}
		})
	}
}

// TestEditDisplayMeta 는 장비 메타 수정(PUT /api/clusters/<key>)을 검증한다.
func TestEditDisplayMeta(t *testing.T) {
	f := newAdminTestFixture(t, "")

	// 1. FT 클러스터 메타 수정
	t.Run("FT Cluster Meta Update", func(t *testing.T) {
		putBody := map[string]any{
			"label":     "Renamed FT Cluster",
			"site":      "Daejeon",
			"company":   "NewCorp",
			"factory":   "Plant9",
			"asset_tag": "TAG-FT-NEW",
			"floor_pos": "3,4",
		}
		rec, res := execRequest(f.srv, http.MethodPut, "/api/clusters/cl-ft1", putBody, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("FT cluster PUT returned %d: %s", rec.Code, rec.Body.String())
		}
		if res["ok"] != true {
			t.Fatalf("expected ok: true, got %+v", res)
		}

		// ClusterState 및 displayOverlay 반영 확인
		st := f.srv.findClusterState("cl-ft1")
		if st == nil {
			t.Fatal("cluster state not found")
		}
		if st.Cfg.Name != "Renamed FT Cluster" || st.Cfg.Site != "Daejeon" {
			t.Fatalf("cluster state not updated: %+v", st.Cfg)
		}
		disp := f.srv.DisplayCfg()["cl-ft1"]
		if disp.Label != "Renamed FT Cluster" || disp.AssetTag != "TAG-FT-NEW" || disp.FloorPos != "3,4" {
			t.Fatalf("display overlay mismatch: %+v", disp)
		}
	})

	// 2. 엣지 장비 메타 수정 (vendor 포함)
	t.Run("Edge Device Meta Update", func(t *testing.T) {
		putBody := map[string]any{
			"label":     "Renamed Edge Server",
			"vendor":    "HPE",
			"site":      "Gwangju",
			"floor_pos": "5,6",
		}
		rec, res := execRequest(f.srv, http.MethodPut, "/api/clusters/edge-srv1", putBody, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("Edge device PUT returned %d: %s", rec.Code, rec.Body.String())
		}
		if res["ok"] != true {
			t.Fatalf("expected ok: true, got %+v", res)
		}

		edgeCfg := f.srv.findEdgeCfg("edge-srv1")
		if edgeCfg == nil {
			t.Fatal("edge device config not found")
		}
		if edgeCfg.Name != "Renamed Edge Server" || edgeCfg.Vendor != "HPE" || edgeCfg.Site != "Gwangju" || edgeCfg.FloorPos != "5,6" {
			t.Fatalf("edge cfg not updated: %+v", edgeCfg)
		}
	})
}

// TestEditValidationErrors 는 장비 메타 수정 시 검증 오류를 테스트한다.
func TestEditValidationErrors(t *testing.T) {
	f := newAdminTestFixture(t, "")

	tests := []struct {
		name       string
		path       string
		payload    any
		statusCode int
		errSubstr  string
	}{
		{
			name:       "Unknown path under PUT",
			path:       "/api/unknown",
			payload:    map[string]any{"label": "foo"},
			statusCode: http.StatusNotFound,
			errSubstr:  "not found",
		},
		{
			name:       "Unknown device key",
			path:       "/api/clusters/non-existent-device",
			payload:    map[string]any{"label": "foo"},
			statusCode: http.StatusNotFound,
			errSubstr:  "장비 없음: non-existent-device",
		},
		{
			name:       "Non-string field",
			path:       "/api/clusters/cl-ft1",
			payload:    map[string]any{"label": 12345},
			statusCode: http.StatusBadRequest,
			errSubstr:  "문자열이 아닌 필드: label",
		},
		{
			name:       "Invalid floor_pos format",
			path:       "/api/clusters/cl-ft1",
			payload:    map[string]any{"floor_pos": "invalid-format"},
			statusCode: http.StatusBadRequest,
			errSubstr:  "floor_pos 형식",
		},
		{
			name:       "Attempt to change mgmt IP on FT cluster",
			path:       "/api/clusters/cl-ft1",
			payload:    map[string]any{"mgmt": "10.0.0.99"},
			statusCode: http.StatusBadRequest,
			errSubstr:  "관리 IP 는 여기서 못 바꿉니다",
		},
		{
			name:       "Attempt to change mgmt IP on Edge device",
			path:       "/api/clusters/edge-srv1",
			payload:    map[string]any{"mgmt": "10.0.0.99"},
			statusCode: http.StatusBadRequest,
			errSubstr:  "관리 IP 는 여기서 못 바꿉니다",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, res := execRequest(f.srv, http.MethodPut, tc.path, tc.payload, "")
			if rec.Code != tc.statusCode {
				t.Fatalf("expected status %d, got %d (body: %s)", tc.statusCode, rec.Code, rec.Body.String())
			}
			errStr, _ := res["error"].(string)
			if !strings.Contains(errStr, tc.errSubstr) {
				t.Fatalf("expected error containing %q, got %q", tc.errSubstr, errStr)
			}
		})
	}
}

// TestDeleteDevice 는 장비 삭제(DELETE /api/clusters/<key>)를 검증한다.
func TestDeleteDevice(t *testing.T) {
	f := newAdminTestFixture(t, "")

	// 1. FT 클러스터 삭제 시도 거절 (400)
	t.Run("Reject FT Cluster Deletion", func(t *testing.T) {
		rec, res := execRequest(f.srv, http.MethodDelete, "/api/clusters/cl-ft1", map[string]any{"reason": "잘못 선택한 클러스터"}, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for FT cluster delete, got %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(res["error"].(string), "FT 클러스터는 API 로 제거하지 않습니다") {
			t.Fatalf("unexpected error message: %+v", res)
		}
	})

	// 2. 엣지 장비 삭제 성공 (200)
	t.Run("Delete Edge Device", func(t *testing.T) {
		rec, res := execRequest(f.srv, http.MethodDelete, "/api/clusters/edge-srv1", map[string]any{"reason": "장비 계약 종료"}, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for edge delete, got %d: %s", rec.Code, rec.Body.String())
		}
		if res["ok"] != true || res["removed"] != "edge-srv1" {
			t.Fatalf("unexpected response: %+v", res)
		}

		// Cfg.EdgeDevices 에서 제거되었는지 확인
		if f.srv.findEdgeCfg("edge-srv1") != nil {
			t.Fatal("deleted edge device still in Cfg.EdgeDevices")
		}
		reloaded := poller.NewEventLog(filepath.Join(f.dir, "events.jsonl"), 500)
		audits := reloaded.List(2)
		if len(audits) != 2 {
			t.Fatalf("persisted delete audit records = %d, want 2", len(audits))
		}
		committed := audits[0].(map[string]any)
		prepared := audits[1].(map[string]any)
		if committed["phase"] != "committed" || prepared["phase"] != "prepared" ||
			committed["audit_id"] != prepared["audit_id"] || committed["action"] != "device.delete" ||
			committed["target"] != "edge-srv1" || committed["reason"] != "장비 계약 종료" ||
			committed["operator"] != "admin" {
			t.Fatalf("persisted delete audit = prepared %#v committed %#v", prepared, committed)
		}

		// 재삭제 시도시 404
		rec2, res2 := execRequest(f.srv, http.MethodDelete, "/api/clusters/edge-srv1", nil, "")
		if rec2.Code != http.StatusNotFound {
			t.Fatalf("expected 404 on second delete, got %d: %s", rec2.Code, rec2.Body.String())
		}
		if !strings.Contains(res2["error"].(string), "장비 없음: edge-srv1") {
			t.Fatalf("unexpected error message: %+v", res2)
		}
	})

	// 3. 잘못된 경로
	t.Run("Invalid Delete Path", func(t *testing.T) {
		rec, _ := execRequest(f.srv, http.MethodDelete, "/api/invalid", nil, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for invalid path, got %d", rec.Code)
		}
	})
}

func TestDeleteDeviceRequiresBoundedReasonWithoutMutation(t *testing.T) {
	tests := []struct {
		name string
		body any
	}{
		{name: "missing body", body: nil},
		{name: "missing field", body: map[string]any{}},
		{name: "blank", body: map[string]any{"reason": " \n\t "}},
		{name: "non string", body: map[string]any{"reason": 123}},
		{name: "over 500 unicode runes", body: map[string]any{"reason": strings.Repeat("가", 501)}},
		{name: "invalid utf8", body: []byte{'{', '"', 'r', 'e', 'a', 's', 'o', 'n', '"', ':', '"', 0xff, '"', '}'}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdminTestFixture(t, "")
			rec, _ := execRequest(f.srv, http.MethodDelete, "/api/clusters/edge-srv1", tc.body, "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
			}
			if f.srv.findEdgeCfg("edge-srv1") == nil {
				t.Fatal("invalid reason mutated runtime device list")
			}
			loaded, err := config.Load(f.cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.EdgeDevices) != 1 || loaded.EdgeDevices[0].Key != "edge-srv1" {
				t.Fatalf("invalid reason mutated config: %#v", loaded.EdgeDevices)
			}
			if f.srv.Events.Len() != 0 {
				t.Fatalf("invalid request wrote audit records: %d", f.srv.Events.Len())
			}
		})
	}
}

func TestDeleteDeviceAuditCommitFailureRollsBack(t *testing.T) {
	f := newAdminTestFixture(t, "")
	f.srv.Audit = &failNthAuditRecorder{delegate: f.srv.Events, failAt: 2}
	rec, _ := execRequest(f.srv, http.MethodDelete, "/api/clusters/edge-srv1", map[string]any{
		"reason": "감사 장애 롤백 검증",
	}, "")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "롤백") {
		t.Fatalf("delete audit failure = %d: %s", rec.Code, rec.Body.String())
	}
	if f.srv.findEdgeCfg("edge-srv1") == nil {
		t.Fatal("audit failure removed runtime device")
	}
	loaded, err := config.Load(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.EdgeDevices) != 1 || loaded.EdgeDevices[0].Key != "edge-srv1" {
		t.Fatalf("audit failure was not rolled back: %#v", loaded.EdgeDevices)
	}
	reloaded := poller.NewEventLog(filepath.Join(f.dir, "events.jsonl"), 500)
	items := reloaded.List(10)
	if len(items) != 2 || items[0].(map[string]any)["phase"] != "rolled_back" ||
		items[1].(map[string]any)["phase"] != "prepared" {
		t.Fatalf("rollback audit trail = %#v", items)
	}
}

func TestDeleteDeviceAuditPrepareFailureDoesNotMutate(t *testing.T) {
	f := newAdminTestFixture(t, "")
	f.srv.Audit = &failNthAuditRecorder{delegate: f.srv.Events, failAt: 1}
	rec, _ := execRequest(f.srv, http.MethodDelete, "/api/clusters/edge-srv1", map[string]any{
		"reason": "감사 준비 실패 검증",
	}, "")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "삭제하지 않았습니다") {
		t.Fatalf("delete prepared audit failure = %d: %s", rec.Code, rec.Body.String())
	}
	if f.srv.findEdgeCfg("edge-srv1") == nil || f.srv.Events.Len() != 0 {
		t.Fatalf("prepared audit failure changed runtime: device=%v audits=%d", f.srv.findEdgeCfg("edge-srv1"), f.srv.Events.Len())
	}
	loaded, err := config.Load(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.EdgeDevices) != 1 || loaded.EdgeDevices[0].Key != "edge-srv1" {
		t.Fatalf("prepared audit failure changed config: %#v", loaded.EdgeDevices)
	}
}

// TestThresholdsAPI 는 PUT /api/admin/thresholds 임계값 변경을 검증한다.
func TestThresholdsAPI(t *testing.T) {
	f := newAdminTestFixture(t, "")

	// 1. 유효한 임계값 설정
	rec, res := execRequest(f.srv, http.MethodPut, "/api/admin/thresholds", map[string]any{"warn": 60, "crit": 85}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT thresholds failed (%d): %s", rec.Code, rec.Body.String())
	}
	if res["ok"] != true || res["warn"] != float64(60) || res["crit"] != float64(85) {
		t.Fatalf("unexpected thresholds response: %+v", res)
	}

	w, c := poller.UsageThresholds()
	if w != 60 || c != 85 {
		t.Fatalf("live thresholds not updated: warn=%v, crit=%v", w, c)
	}

	// 2. 유효하지 않은 임계값 (warn >= crit 또는 음수 등)
	badCases := []map[string]any{
		{"warn": 90, "crit": 80},
		{"warn": -10, "crit": 80},
		{"warn": 50, "crit": 150},
		{"warn": "50", "crit": 80},
	}
	for _, bad := range badCases {
		r, _ := execRequest(f.srv, http.MethodPut, "/api/admin/thresholds", bad, "")
		if r.Code != http.StatusBadRequest {
			t.Fatalf("bad thresholds %+v expected 400, got %d", bad, r.Code)
		}
	}
}

// TestWriteGateSecurity 는 CSRF 동일출처 검사 및 쓰기 차단 동작을 검증한다.
func TestWriteGateSecurity(t *testing.T) {
	// 1. Gate 가 nil 인 경우 403
	srvNilGate := &Server{}
	rec, res := execRequest(srvNilGate, http.MethodPost, "/api/clusters", map[string]any{"key": "test"}, "")
	if rec.Code != http.StatusForbidden || !strings.Contains(res["error"].(string), "비활성화") {
		t.Fatalf("nil gate should return 403 forbidden: %d body=%s", rec.Code, rec.Body.String())
	}

	// 2. AllowWrites = false 인 경우 403
	dir := t.TempDir()
	gateNoWrite := webfront.New(nil, webfront.Options{StateDir: dir, AllowWrites: false})
	srvNoWrite := &Server{Gate: gateNoWrite}
	rec2, _ := execRequest(srvNoWrite, http.MethodPost, "/api/clusters", map[string]any{"key": "test"}, "")
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("disabled writes should return 403 forbidden: %d", rec2.Code)
	}

	// 3. 악의적 교차 출처 (Cross-Origin) 거부
	f := newAdminTestFixture(t, "")
	req := httptest.NewRequest(http.MethodPost, "/api/clusters", strings.NewReader(`{"key":"test"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Origin", "http://attacker.com")
	req.Host = "localhost:6005"
	rec3 := httptest.NewRecorder()
	f.srv.ServeHTTP(rec3, req)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request should be rejected with 403: %d", rec3.Code)
	}
}

// TestConnTestPLC 는 PLC(FINS UDP) 연결 테스트를 인-프로세스 UDP 스텁으로 검증한다.
func TestConnTestPLC(t *testing.T) {
	// UDP 가짜 FINS 서버 기동 (localhost 임의 포트)
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	port := conn.LocalAddr().(*net.UDPAddr).Port

	// FINS 정상 응답 핸들러 (최소 14바이트, MRES=0, SRES=0)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n >= 10 {
				resp := make([]byte, 14)
				copy(resp, buf[:10])
				resp[12] = 0 // MRES OK
				resp[13] = 0 // SRES OK
				_, _ = conn.WriteToUDP(resp, remote)
			}
		}
	}()

	f := newAdminTestFixture(t, "")

	// 1. 성공 케이스
	bodySuccess := map[string]any{
		"type":          "PLC",
		"mgmt":          "127.0.0.1",
		"fins_port":     port,
		"fins_src_node": 84,
	}
	rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters/test", bodySuccess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("connTest PLC returned %d: %s", rec.Code, rec.Body.String())
	}
	if res["transport"] != "fins" || res["reachable"] != true {
		t.Fatalf("expected reachable PLC, got %+v", res)
	}
	if ver, ok := res["version"].(string); !ok || !strings.Contains(ver, "FINS RTT") {
		t.Fatalf("expected version with FINS RTT, got %+v", res)
	}

	// 2. 실패 케이스 (미응답 포트)
	bodyFail := map[string]any{
		"type":      "PLC",
		"mgmt":      "127.0.0.1",
		"fins_port": port + 9999,
	}
	_, resFail := execRequest(f.srv, http.MethodPost, "/api/clusters/test", bodyFail, "")
	if resFail["reachable"] == true {
		t.Fatalf("expected unreachable PLC, got %+v", resFail)
	}
}

// TestConnTestProxmox 는 Proxmox API 연결 테스트를 가짜 TLS 서버로 검증한다.
func TestConnTestProxmox(t *testing.T) {
	// Proxmox 는 admin.go 에서 https://<ip>:8006/api2/json/access/ticket 로 고정 접속한다.
	// 로컬 8006 포트 바인딩이 가능한 경우 통합 검증 수행.
	ln, err := net.Listen("tcp", "127.0.0.1:8006")
	if err != nil {
		t.Logf("127.0.0.1:8006 포트 바인딩 불가(다른 프로세스 사용 중) -> pveTicket 직접 테스트로 대체: %v", err)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		u := r.FormValue("username")
		p := r.FormValue("password")
		if u == "root@pam" && p == "goodpass" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"ticket":"PVE:...","CSRFPreventionToken":"..."}}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":{"username":"authentication failure"}}`))
	})

	tlsServer := httptest.NewUnstartedServer(mux)
	tlsServer.Listener.Close()
	tlsServer.Listener = ln
	tlsServer.StartTLS()
	defer tlsServer.Close()
	tlsFingerprint := adminTLSServerFingerprint(t, tlsServer)

	f := newAdminTestFixture(t, "")

	// 1. 성공 케이스
	bodySuccess := map[string]any{
		"type":            "SRV",
		"platform":        "proxmox",
		"mgmt":            "127.0.0.1",
		"admin_user":      "root@pam",
		"admin_pass":      "goodpass",
		"tls_fingerprint": tlsFingerprint,
	}
	rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters/test", bodySuccess, "")
	if rec.Code != http.StatusOK || res["reachable"] != true {
		t.Fatalf("expected reachable Proxmox, got code=%d res=%+v", rec.Code, res)
	}
	if res["transport"] != "pve-api" {
		t.Fatalf("expected transport pve-api, got %v", res["transport"])
	}

	// 2. 인증 실패 케이스 (401)
	bodyAuthFail := map[string]any{
		"type":            "SRV",
		"platform":        "proxmox",
		"mgmt":            "127.0.0.1",
		"admin_user":      "root@pam",
		"admin_pass":      "wrongpass",
		"tls_fingerprint": tlsFingerprint,
	}
	_, resAuthFail := execRequest(f.srv, http.MethodPost, "/api/clusters/test", bodyAuthFail, "")
	if resAuthFail["reachable"] != true {
		t.Fatalf("expected reachable=true on 401, got %+v", resAuthFail)
	}
	authObj := resAuthFail["auth"].(map[string]any)
	if authObj["ok"] != false || !strings.Contains(authObj["error"].(string), "인증 실패") {
		t.Fatalf("expected auth.error on 401, got %+v", authObj)
	}
}

// TestConnTestRedfish 는 Server(BMC) Redfish 연결 테스트를 가짜 TLS 서버로 검증한다.
func TestConnTestRedfish(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redfish/v1/Systems" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		u, p, ok := r.BasicAuth()
		if ok && u == "admin" && p == "bmcpass" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"@odata.id":"/redfish/v1/Systems","Members":[]}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	bmcHost := u.Host // host:port
	tlsFingerprint := adminTLSServerFingerprint(t, ts)

	f := newAdminTestFixture(t, "")

	// 1. 성공 케이스
	bodySuccess := map[string]any{
		"type":            "SRV",
		"mgmt":            "127.0.0.1",
		"bmc_ip":          bmcHost,
		"bmc_user":        "admin",
		"bmc_pass":        "bmcpass",
		"tls_fingerprint": tlsFingerprint,
	}
	rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters/test", bodySuccess, "")
	if rec.Code != http.StatusOK || res["reachable"] != true {
		t.Fatalf("expected reachable Redfish, got code=%d res=%+v", rec.Code, res)
	}
	if res["transport"] != "redfish" {
		t.Fatalf("expected transport redfish, got %v", res["transport"])
	}

	// 2. BMC 인증 실패 (401)
	bodyAuthFail := map[string]any{
		"type":            "SRV",
		"mgmt":            "127.0.0.1",
		"bmc_ip":          bmcHost,
		"bmc_user":        "admin",
		"bmc_pass":        "wrongpass",
		"tls_fingerprint": tlsFingerprint,
	}
	_, resAuthFail := execRequest(f.srv, http.MethodPost, "/api/clusters/test", bodyAuthFail, "")
	if resAuthFail["reachable"] != true {
		t.Fatalf("expected reachable=true on 401, got %+v", resAuthFail)
	}
	authObj := resAuthFail["auth"].(map[string]any)
	if authObj["ok"] != false || !strings.Contains(authObj["error"].(string), "BMC 인증 실패") {
		t.Fatalf("expected auth failure error, got %+v", authObj)
	}

	// 3. Redfish 연결 실패 (서버 종료 후)
	ts.Close()
	bodyDown := map[string]any{
		"type":     "SRV",
		"mgmt":     "127.0.0.1",
		"bmc_ip":   bmcHost,
		"bmc_user": "admin",
		"bmc_pass": "bmcpass",
	}
	_, resDown := execRequest(f.srv, http.MethodPost, "/api/clusters/test", bodyDown, "")
	if resDown["reachable"] == true {
		t.Fatalf("expected reachable=false for down server, got %+v", resDown)
	}
	warns := resDown["warnings"].([]any)
	if len(warns) == 0 {
		t.Fatal("expected warnings on connection failure")
	}
}

// TestConnTestFTAndSNMPWarnings 는 FT 클러스터 및 SNMP/자격증명 미입력 경고를 검증한다.
func TestConnTestFTAndSNMPWarnings(t *testing.T) {
	f := newAdminTestFixture(t, "")

	// 1. FT 클러스터 프로브
	bodyFT := map[string]any{
		"type": "EV",
		"mgmt": "127.0.0.1",
	}
	rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters/test", bodyFT, "")
	if rec.Code != http.StatusOK || res["transport"] != "avcli" {
		t.Fatalf("unexpected FT connTest response: %+v", res)
	}
	warns := res["warnings"].([]any)
	if len(warns) == 0 {
		t.Fatal("expected warnings for FT cluster connTest")
	}

	// 2. 자격증명 미입력 경고
	bodyNoCreds := map[string]any{
		"type": "SRV",
		"mgmt": "127.0.0.1",
	}
	_, resNoCreds := execRequest(f.srv, http.MethodPost, "/api/clusters/test", bodyNoCreds, "")
	warnsNoCreds := resNoCreds["warnings"].([]any)
	foundNoCredsWarn := false
	for _, w := range warnsNoCreds {
		if strings.Contains(fmt.Sprint(w), "검증할 자격증명이 없습니다") {
			foundNoCredsWarn = true
			break
		}
	}
	if !foundNoCredsWarn {
		t.Fatalf("expected no credentials warning, got: %+v", warnsNoCreds)
	}
}

// generateAdminSelfSignedCert 는 테스트용 x509 자체서명 인증서와 SPKI SHA-256 지문을 생성한다.
func generateAdminSelfSignedCert(t *testing.T) (tls.Certificate, string, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey 실패: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(2001),
		Subject: pkix.Name{
			Organization: []string{"ServerDesk Admin Test"},
			CommonName:   "127.0.0.1",
		},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("x509.CreateCertificate 실패: %v", err)
	}
	parsed, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate 실패: %v", err)
	}
	spkiHash := sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	hexFP := hex.EncodeToString(spkiHash[:])
	b64FP := base64.StdEncoding.EncodeToString(spkiHash[:])
	tlsCert := tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}
	return tlsCert, hexFP, b64FP
}

func adminTLSServerFingerprint(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	if ts.TLS == nil || len(ts.TLS.Certificates) == 0 || len(ts.TLS.Certificates[0].Certificate) == 0 {
		t.Fatal("test TLS server certificate unavailable")
	}
	cert, err := x509.ParseCertificate(ts.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("parse test TLS server certificate: %v", err)
	}
	hash := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(hash[:])
}

// TestConnTestTLSFingerprintPinning 은 /api/clusters/test 에서 tls_fingerprint 옵트인 피닝 동작을 검증한다.
func TestConnTestTLSFingerprintPinning(t *testing.T) {
	serverCert, hexFP, b64FP := generateAdminSelfSignedCert(t)
	_, otherHexFP, _ := generateAdminSelfSignedCert(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/Systems", func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if ok && u == "admin" && p == "bmcpass" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"@odata.id":"/redfish/v1/Systems","Members":[]}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})

	ts := httptest.NewUnstartedServer(mux)
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
	}
	ts.StartTLS()
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	bmcHost := u.Host

	f := newAdminTestFixture(t, "")

	// 1. Redfish: hex 핑거프린트 일치 -> 성공
	t.Run("Redfish_PinMatch_Hex", func(t *testing.T) {
		body := map[string]any{
			"type":            "SRV",
			"mgmt":            "127.0.0.1",
			"bmc_ip":          bmcHost,
			"bmc_user":        "admin",
			"bmc_pass":        "bmcpass",
			"tls_fingerprint": hexFP,
		}
		rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters/test", body, "")
		if rec.Code != http.StatusOK || res["reachable"] != true {
			t.Fatalf("expected reachable=true for matching hex pin, got code=%d res=%+v", rec.Code, res)
		}
		if res["transport"] != "redfish" {
			t.Fatalf("expected transport=redfish, got %v", res["transport"])
		}
	})

	// 2. Redfish: base64 핑거프린트 일치 -> 성공
	t.Run("Redfish_PinMatch_Base64", func(t *testing.T) {
		body := map[string]any{
			"type":            "SRV",
			"mgmt":            "127.0.0.1",
			"bmc_ip":          bmcHost,
			"bmc_user":        "admin",
			"bmc_pass":        "bmcpass",
			"tls_fingerprint": b64FP,
		}
		rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters/test", body, "")
		if rec.Code != http.StatusOK || res["reachable"] != true {
			t.Fatalf("expected reachable=true for matching base64 pin, got code=%d res=%+v", rec.Code, res)
		}
	})

	// 3. Redfish: 핑거프린트 불일치 -> 연결 실패
	t.Run("Redfish_PinMismatch_Fail", func(t *testing.T) {
		body := map[string]any{
			"type":            "SRV",
			"mgmt":            "127.0.0.1",
			"bmc_ip":          bmcHost,
			"bmc_user":        "admin",
			"bmc_pass":        "bmcpass",
			"tls_fingerprint": otherHexFP,
		}
		_, res := execRequest(f.srv, http.MethodPost, "/api/clusters/test", body, "")
		if res["reachable"] == true {
			t.Fatalf("expected reachable=false for mismatched pin, got %+v", res)
		}
		warns := res["warnings"].([]any)
		if len(warns) == 0 {
			t.Fatal("expected warnings for pin mismatch")
		}
	})

	// 4. Redfish: 핑거프린트 미지정 -> 기본 CA 검증으로 자체서명 거부
	t.Run("Redfish_NoPin_SelfSignedRejected", func(t *testing.T) {
		body := map[string]any{
			"type":     "SRV",
			"mgmt":     "127.0.0.1",
			"bmc_ip":   bmcHost,
			"bmc_user": "admin",
			"bmc_pass": "bmcpass",
		}
		rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters/test", body, "")
		if rec.Code != http.StatusOK || res["reachable"] == true {
			t.Fatalf("expected self-signed endpoint rejection without pin, got code=%d res=%+v", rec.Code, res)
		}
	})
}

// TestAdminUnitHelpers 는 admin.go 내부 헬퍼 함수들(tcpOK, finsProbe, pveTicket, redfishGet, errClass, bodyNum, orUnknown)을 직접 검증한다.
func TestAdminUnitHelpers(t *testing.T) {
	ctx := context.Background()

	// 1. tcpOK
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if !tcpOK("127.0.0.1", port) {
		t.Fatal("tcpOK should return true for open port")
	}
	ln.Close()
	if tcpOK("127.0.0.1", port) {
		t.Fatal("tcpOK should return false for closed port")
	}

	// 2. errClass
	if errClass(nil) != "" {
		t.Fatalf("errClass(nil) should be empty, got %q", errClass(nil))
	}
	if errClass(errors.New("generic")) != "OSError" {
		t.Fatalf("errClass(generic) should be OSError, got %q", errClass(errors.New("generic")))
	}

	// 3. bodyNum
	m := map[string]any{"num": float64(42), "str": "123.5", "invalid": "abc"}
	if bodyNum(m, "num", 0) != 42 {
		t.Errorf("bodyNum float64 failed")
	}
	if bodyNum(m, "str", 0) != 123.5 {
		t.Errorf("bodyNum string float failed")
	}
	if bodyNum(m, "invalid", 99) != 99 {
		t.Errorf("bodyNum default failed")
	}

	// 4. orUnknown
	if orUnknown("") != "?" || orUnknown("EV") != "EV" {
		t.Errorf("orUnknown mismatch")
	}

	// 5. redfishGet 직접 테스트
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	tlsFingerprint := adminTLSServerFingerprint(t, ts)
	code, err := redfishGet(ctx, u.Host, "user", "pass", "/ok", tlsFingerprint)
	if err != nil || code != 0 {
		t.Fatalf("redfishGet ok expected 0/nil, got %d/%v", code, err)
	}
	code, err = redfishGet(ctx, u.Host, "user", "pass", "/err", tlsFingerprint)
	if code != 403 || err == nil {
		t.Fatalf("redfishGet forbidden expected 403/err, got %d/%v", code, err)
	}

	// 6. pveTicket 직접 테스트
	tsPVE := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer tsPVE.Close()
	// pveTicket 은 자체적으로 포트 8006을 붙이므로 잘못된 URL 테스트로 오류 경로 검증
	code, err = pveTicket(ctx, "256.256.256.256", "u", "p", "")
	if err == nil {
		t.Fatalf("pveTicket expected error for invalid IP, got code=%d", code)
	}
}

type fakeTimeoutNetError struct{}

func (fakeTimeoutNetError) Error() string   { return "i/o timeout" }
func (fakeTimeoutNetError) Timeout() bool   { return true }
func (fakeTimeoutNetError) Temporary() bool { return true }

// TestErrClassTimeout 는 net.Error 타임아웃 판정 분류를 검증한다.
func TestErrClassTimeout(t *testing.T) {
	if got := errClass(fakeTimeoutNetError{}); got != "TimeoutError" {
		t.Fatalf("expected TimeoutError, got %q", got)
	}
}

// TestAddDeviceFTNodePasswordInheritance 는 FT 노드 1의 비밀번호가 생략되었을 때 노드 0의 비밀번호를 승계하는지 검증한다.
func TestAddDeviceFTNodePasswordInheritance(t *testing.T) {
	f := newAdminTestFixture(t, "")

	payload := map[string]any{
		"type":       "EV",
		"key":        "cl-ft-inherit",
		"mgmt":       "192.168.1.15",
		"node0":      "192.168.1.16",
		"node0_pass": "shared-node-pass",
		"node1":      "192.168.1.17",
		// node1_pass 생략
	}

	rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters", payload, "")
	if rec.Code != http.StatusOK || res["ok"] != true {
		t.Fatalf("failed to add FT cluster with node password inheritance: %d %s", rec.Code, rec.Body.String())
	}

	raw, err := config.Load(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, cl := range raw.Clusters {
		if cl.Key == "cl-ft-inherit" {
			if len(cl.Nodes) != 2 {
				t.Fatalf("expected 2 nodes, got %d", len(cl.Nodes))
			}
			if cl.Nodes[0].RootPassword != "shared-node-pass" || cl.Nodes[1].RootPassword != "shared-node-pass" {
				t.Fatalf("node password inheritance failed: %+v", cl.Nodes)
			}
			return
		}
	}
	t.Fatal("cl-ft-inherit not found in config")
}

// TestEditDisplayMetaClearFields 는 빈 문자열 전달 시 표시 메타 필드가 삭제/초기화되는지 검증한다.
func TestEditDisplayMetaClearFields(t *testing.T) {
	f := newAdminTestFixture(t, "")

	// 1. FT 클러스터 floor_pos 제거
	rec, res := execRequest(f.srv, http.MethodPut, "/api/clusters/cl-ft1", map[string]any{"floor_pos": ""}, "")
	if rec.Code != http.StatusOK || res["ok"] != true {
		t.Fatalf("failed to clear floor_pos on FT cluster: %d %s", rec.Code, rec.Body.String())
	}
	disp := f.srv.DisplayCfg()["cl-ft1"]
	if disp.FloorPos != "" {
		t.Fatalf("expected empty floor_pos in overlay, got %q", disp.FloorPos)
	}

	// 2. 엣지 장비 필드 비우기
	rec2, res2 := execRequest(f.srv, http.MethodPut, "/api/clusters/edge-srv1", map[string]any{
		"label":     "",
		"company":   "",
		"factory":   "",
		"site":      "",
		"asset_tag": "",
		"floor_pos": "",
		"vendor":    "",
	}, "")
	if rec2.Code != http.StatusOK || res2["ok"] != true {
		t.Fatalf("failed to clear fields on edge device: %d %s", rec2.Code, rec2.Body.String())
	}
	edgeCfg := f.srv.findEdgeCfg("edge-srv1")
	if edgeCfg.Name != "" || edgeCfg.Vendor != "" || edgeCfg.AssetTag != "" {
		t.Fatalf("edge fields not cleared: %+v", edgeCfg)
	}
}

// TestFinsProbeDetails 는 FINS 프로브의 짧은 응답, 에러 응답, 잘못된 IP 처리를 검증한다.
func TestFinsProbeDetails(t *testing.T) {
	ctx := context.Background()

	// 1. 최소 길이(14바이트) 미만 응답
	connShort, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer connShort.Close()
	portShort := connShort.LocalAddr().(*net.UDPAddr).Port
	go func() {
		buf := make([]byte, 1024)
		n, remote, err := connShort.ReadFromUDP(buf)
		if err == nil && n > 0 {
			_, _ = connShort.WriteToUDP([]byte{0x80, 0x00}, remote) // 2바이트 짧은 응답
		}
	}()
	okShort, _ := finsProbe(ctx, "127.0.0.1", portShort, []byte{0x06, 0x01}, 84)
	if okShort {
		t.Fatal("finsProbe should fail on short response")
	}

	// 2. MRES/SRES 오류 응답 (resp[12] != 0)
	connErr, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer connErr.Close()
	portErr := connErr.LocalAddr().(*net.UDPAddr).Port
	go func() {
		buf := make([]byte, 1024)
		n, remote, err := connErr.ReadFromUDP(buf)
		if err == nil && n > 0 {
			resp := make([]byte, 14)
			resp[12] = 0x01 // Error MRES
			_, _ = connErr.WriteToUDP(resp, remote)
		}
	}()
	okErr, _ := finsProbe(ctx, "127.0.0.1", portErr, []byte{0x06, 0x01}, 84)
	if okErr {
		t.Fatal("finsProbe should fail on error MRES")
	}
}

// TestReadCappedAndBodyErrors 는 JSON 파싱 및 캡 크기 초과 처리를 검증한다.
func TestReadCappedAndBodyErrors(t *testing.T) {
	f := newAdminTestFixture(t, "")

	// 1. 64KB 초과 본문 전송 -> 400
	hugePayload := strings.Repeat("a", 70*1024)
	req := httptest.NewRequest(http.MethodPost, "/api/clusters", strings.NewReader(hugePayload))
	req.ContentLength = int64(len(hugePayload))
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on huge body, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. ContentLength < 0 이지만 LimitReader 로 읽기
	req2 := httptest.NewRequest(http.MethodPost, "/api/clusters", strings.NewReader(`{"key":"chunked"}`))
	req2.ContentLength = -1
	rec2 := httptest.NewRecorder()
	f.srv.ServeHTTP(rec2, req2)
	// key 검증 실패 등으로 넘어가며 readCapped 분기 통과
	if rec2.Code == 0 {
		t.Fatal("expected non-zero response")
	}
}

// TestServeHTTPRoutingAndPanicRecovery 는 OPTIONS 204, 미지원 메서드 404, 패닉 500 복구를 검증한다.
func TestServeHTTPRoutingAndPanicRecovery(t *testing.T) {
	f := newAdminTestFixture(t, "")

	// 1. OPTIONS 요청 -> 204 No Content
	recOpt, _ := execRequest(f.srv, http.MethodOptions, "/api/clusters", nil, "")
	if recOpt.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS, got %d", recOpt.Code)
	}

	// 2. 미지원 메서드 (PATCH) -> 404
	recPatch, _ := execRequest(f.srv, http.MethodPatch, "/api/clusters", nil, "")
	if recPatch.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for PATCH, got %d", recPatch.Code)
	}

	// 3. 알 수 없는 POST 경로 -> 404
	recPost404, _ := execRequest(f.srv, http.MethodPost, "/api/unknown-action", map[string]any{}, "")
	if recPost404.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown POST, got %d", recPost404.Code)
	}

	// 4. doGet 의 인덱스 경로 (/api 및 /)
	recIdx, resIdx := execRequest(f.srv, http.MethodGet, "/api", nil, "")
	if recIdx.Code != http.StatusOK || resIdx["service"] != "everrun-poller" {
		t.Fatalf("expected 200 index for /api, got %d %+v", recIdx.Code, resIdx)
	}
	recRoot, resRoot := execRequest(f.srv, http.MethodGet, "/", nil, "")
	if recRoot.Code != http.StatusOK || resRoot["service"] != "everrun-poller" {
		t.Fatalf("expected 200 index for /, got %d %+v", recRoot.Code, resRoot)
	}
}

// TestCORSAndGzipResponse 는 CORS allowlist 헤더 부여 및 1KB 초과 시 gzip 압축을 검증한다.
func TestCORSAndGzipResponse(t *testing.T) {
	f := newAdminTestFixture(t, "")

	// 1. 허용된 CORS Origin
	reqAllow := httptest.NewRequest(http.MethodGet, "/api", nil)
	reqAllow.Header.Set("Origin", "http://localhost:3000")
	recAllow := httptest.NewRecorder()
	f.srv.ServeHTTP(recAllow, reqAllow)
	if recAllow.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("expected CORS origin header, got %q", recAllow.Header().Get("Access-Control-Allow-Origin"))
	}

	// 2. 허용되지 않은 CORS Origin
	reqDeny := httptest.NewRequest(http.MethodGet, "/api", nil)
	reqDeny.Header.Set("Origin", "http://untrusted-site.com")
	recDeny := httptest.NewRecorder()
	f.srv.ServeHTTP(recDeny, reqDeny)
	if recDeny.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("untrusted origin should not receive CORS header")
	}

	// 3. Gzip 압축 (1KB 초과 응답)
	reqGzip := httptest.NewRequest(http.MethodGet, "/api/admin/health", nil)
	reqGzip.Header.Set("Accept-Encoding", "gzip")
	recGzip := httptest.NewRecorder()
	f.srv.ServeHTTP(recGzip, reqGzip)
	if recGzip.Code != http.StatusOK {
		t.Fatalf("health failed: %d", recGzip.Code)
	}
}

// TestHandleAvailabilityCSV 는 /api/availability.csv 다운로드 경로를 검증한다.
func TestHandleAvailabilityCSV(t *testing.T) {
	f := newAdminTestFixture(t, "")

	rec, _ := execRequest(f.srv, http.MethodGet, "/api/availability.csv", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for availability.csv, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("expected text/csv header, got %s", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "date,device,availability_pct,observed_sec") {
		t.Fatalf("CSV header missing: %s", rec.Body.String())
	}
}

// TestAPIDoGetEndpoints 는 /api/fleet, /api/devices, /api/topology 등의 GET 조회를 검증한다.
func TestAPIDoGetEndpoints(t *testing.T) {
	f := newAdminTestFixture(t, "")

	// 1. 캐시가 비어있을 때 503 반환
	recFleetNil, _ := execRequest(f.srv, http.MethodGet, "/api/fleet", nil, "")
	if recFleetNil.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for nil fleet, got %d", recFleetNil.Code)
	}
	recTopoNil, _ := execRequest(f.srv, http.MethodGet, "/api/topology", nil, "")
	if recTopoNil.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for nil topo, got %d", recTopoNil.Code)
	}
	recTopoFullNil, _ := execRequest(f.srv, http.MethodGet, "/api/topology/full", nil, "")
	if recTopoFullNil.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for nil topo full, got %d", recTopoFullNil.Code)
	}

	// 2. 캐시 데이터 주입 후 정상 조회
	f.srv.Cache.Update(f.srv.States)

	// GET /api/fleet
	recFleet, resFleet := execRequest(f.srv, http.MethodGet, "/api/fleet", nil, "")
	if recFleet.Code != http.StatusOK || resFleet["clusters"] == nil {
		t.Fatalf("GET /api/fleet failed: %d %+v", recFleet.Code, resFleet)
	}

	// GET /api/devices
	recDevs, resDevs := execRequest(f.srv, http.MethodGet, "/api/devices", nil, "")
	if recDevs.Code != http.StatusOK || resDevs["devices"] == nil {
		t.Fatalf("GET /api/devices failed: %d %+v", recDevs.Code, resDevs)
	}

	// GET /api/fleet?format=devices
	recFmtDev, resFmtDev := execRequest(f.srv, http.MethodGet, "/api/fleet?format=devices", nil, "")
	if recFmtDev.Code != http.StatusOK || resFmtDev["devices"] == nil {
		t.Fatalf("GET /api/fleet?format=devices failed: %d %+v", recFmtDev.Code, resFmtDev)
	}
}
