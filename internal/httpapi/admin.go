package httpapi

// 장비 표시 메타 편집 / 추가 / 제거 / 연결 테스트 (poller.py do_PUT/do_DELETE/
// do_POST 포트).
//
// 편집 허용 범위는 **표시 메타만**(이름/회사/공장/사이트/자산태그[/엣지 vendor]).
// 관리 IP·타입·자격증명·노드 구성은 수집 대상 정의라 API 로 바꾸지 않는다 —
// config.local.json 수정 + 재시작 경로만 허용(실장비 오폴러 사고 방지).

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"serverdesk/internal/config"
	"serverdesk/internal/edge"
	"serverdesk/internal/poller"
	"serverdesk/internal/snmp"
)

// editFields 는 PUT 이 받는 표시 필드다(엣지는 vendor 추가).
var editFields = []string{"label", "company", "factory", "site", "asset_tag", "floor_pos"}

// typeToKind 는 프런트 타입 → 엣지 kind. PC/WIN/PI 는 실수집기 미구현이라 명시 거절.
var typeToKind = map[string]string{"SRV": "server", "NAS": "nas", "PLC": "plc", "PRN": "printer"}

// ftTypes 는 FT 클러스터 타입 코드다.
var ftTypes = map[string]bool{"EV": true, "EDGE": true, "END": true, "FTS": true}

var (
	keyRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{1,39}$`)
	ipRe       = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}$`)
	floorPosRe = regexp.MustCompile(`^\d{1,3}\s*[,\-]\s*\d{1,3}$`)
)

func bodyStr(body map[string]any, key string) string {
	s, _ := body[key].(string)
	return strings.TrimSpace(s)
}

// findClusterState 는 key 의 FT 클러스터 상태를 찾는다.
func (s *Server) findClusterState(key string) *poller.ClusterState {
	for _, st := range s.States {
		if st.Key == key {
			return st
		}
	}
	return nil
}

func (s *Server) findEdgeCfg(key string) *config.EdgeDevice {
	for i := range s.Cfg.EdgeDevices {
		if s.Cfg.EdgeDevices[i].Key == key {
			return &s.Cfg.EdgeDevices[i]
		}
	}
	return nil
}

// putThresholds 는 PUT /api/admin/thresholds — 사용률 임계값 변경(파일 기록 + 라이브 반영).
func (s *Server) putThresholds(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readJSONBody(w, r)
	if !ok {
		return
	}
	num := func(k string) (float64, bool) {
		v, ok := body[k].(float64)
		return v, ok
	}
	warn, wok := num("warn")
	crit, cok := num("crit")
	if !wok || !cok || !(warn > 0 && warn < crit && crit <= 100) {
		s.send(w, r, 400, map[string]any{"error": "0 < warn < crit <= 100 (숫자 %) 이어야 합니다"})
		return
	}
	if err := s.Store.SetSectionValue("thresholds", map[string]any{"warn": warn, "crit": crit}); err != nil {
		s.send(w, r, 500, map[string]any{"error": "설정 저장 실패: " + err.Error()})
		return
	}
	poller.SetThresholds(warn, crit)
	s.send(w, r, 200, map[string]any{"ok": true, "warn": warn, "crit": crit})
}

// doPut 은 PUT /api/clusters/<key> — 표시 메타 수정(poller.py do_PUT).
func (s *Server) doPut(w http.ResponseWriter, r *http.Request, path string) {
	if !s.writeGate(w, r) {
		return
	}
	if path == "/api/admin/thresholds" {
		s.putThresholds(w, r)
		return
	}
	if !strings.HasPrefix(path, "/api/clusters/") {
		s.send(w, r, 404, map[string]any{"error": "not found", "path": path})
		return
	}
	key, _ := url.PathUnescape(strings.TrimPrefix(path, "/api/clusters/"))
	body, ok := s.readJSONBody(w, r)
	if !ok {
		return
	}
	if body == nil {
		s.send(w, r, 400, map[string]any{"error": "JSON 본문이 필요합니다"})
		return
	}
	// 타입 검증: 문자열 외 값은 str() 강제변환으로 쓰레기가 config 에 박히므로 명시 거절.
	bad := []string{}
	for _, k := range append(append([]string{}, editFields...), "vendor") {
		if v, present := body[k]; present && v != nil {
			if _, isStr := v.(string); !isStr {
				bad = append(bad, k)
			}
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		s.send(w, r, 400, map[string]any{"error": "문자열이 아닌 필드: " + strings.Join(bad, ", ")})
		return
	}
	fields := map[string]string{}
	for _, k := range editFields {
		if _, present := body[k]; present {
			fields[k] = bodyStr(body, k)
		}
	}
	// 플로어 위치는 '행,열'(1-base 양의 정수) 또는 빈 값(자동 배치)만 허용.
	if fp := fields["floor_pos"]; fp != "" && !floorPosRe.MatchString(fp) {
		s.send(w, r, 400, map[string]any{"error": "floor_pos 형식: '행,열' (예: 1,3) 또는 빈 값"})
		return
	}

	// key 는 clusters[] 를 먼저 본다 — key 중복 시 엣지 쪽은 API 편집 불가(운영 계약).
	st := s.findClusterState(key)
	var edgeCfg *config.EdgeDevice
	if st == nil {
		edgeCfg = s.findEdgeCfg(key)
	}
	if st == nil && edgeCfg == nil {
		s.send(w, r, 404, map[string]any{"error": "장비 없음: " + key})
		return
	}

	// 관리 IP 변경 시도는 명시적으로 거절(조용히 무시하면 사용자가 속는다).
	wantIP := bodyStr(body, "mgmt")
	curIP := ""
	if st != nil {
		curIP = st.Cfg.MgmtIP
	} else {
		curIP = edgeCfg.IP
	}
	if wantIP != "" && wantIP != curIP {
		s.send(w, r, 400, map[string]any{"error": "관리 IP 는 여기서 못 바꿉니다 — config.local.json 수정 후 폴러 재시작"})
		return
	}

	if st == nil {
		if _, present := body["vendor"]; present { // 엣지는 vendor 도 표시 필드
			fields["vendor"] = bodyStr(body, "vendor")
		}
	}

	section := config.SectionClusters
	if st == nil {
		section = config.SectionEdgeDevices
	}
	if err := s.Store.UpdateDisplayMeta(section, key, fields); err != nil {
		logf("error", "manage", fmt.Sprintf("config 저장 실패(%s): %v", key, err))
		s.send(w, r, 500, map[string]any{"error": "config 저장 실패 — 폴러 로그를 확인하세요"})
		return
	}

	// 파일 저장 성공 후 메모리 반영.
	if st != nil {
		s.applyClusterMeta(st, fields)
	} else {
		applyEdgeMeta(edgeCfg, fields)
		if s.Edge != nil {
			s.Edge.PatchMeta(key, fields)
		}
	}
	applied := make([]string, 0, len(fields))
	for k := range fields {
		applied = append(applied, k)
	}
	sort.Strings(applied)
	logf("info", "manage", fmt.Sprintf("표시 메타 수정 %s <- %v", key, applied))
	s.send(w, r, 200, map[string]any{
		"ok": true, "applied": applied,
		"note": "표시 메타만 반영 — IP/타입/자격증명은 config 전용"})
}

// applyClusterMeta 는 PUT 결과를 실행 중 설정에 반영한다.
// 구조체에 없는 키(asset_tag/floor_pos)는 오버레이에만 둔다.
func (s *Server) applyClusterMeta(st *poller.ClusterState, fields map[string]string) {
	st.PatchDisplayMeta(fields)
	s.ovlMu.Lock()
	m := s.displayOverlay[st.Key]
	if m == nil {
		m = map[string]string{}
		s.displayOverlay[st.Key] = m
	}
	for k, v := range fields {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	s.ovlMu.Unlock()
}

// applyEdgeMeta 는 엣지 설정 구조체에 표시 메타를 반영한다(빈 값은 삭제 계약이라
// 구조체에서는 빈 문자열 대입이 곧 삭제다).
func applyEdgeMeta(d *config.EdgeDevice, fields map[string]string) {
	for k, v := range fields {
		switch k {
		case "label":
			d.Name = v
		case "company":
			d.Company = v
		case "factory":
			d.Factory = v
		case "site":
			d.Site = v
		case "asset_tag":
			d.AssetTag = v
		case "floor_pos":
			d.FloorPos = v
		case "vendor":
			d.Vendor = v
		}
	}
}

// doDelete 는 DELETE /api/clusters/<key> — 엣지 장비 제거(poller.py do_DELETE).
// FT 클러스터 제거는 거절된다(config 직접 수정 + 재시작 경로만 허용).
func (s *Server) doDelete(w http.ResponseWriter, r *http.Request, path string) {
	if !s.writeGate(w, r) {
		return
	}
	if !strings.HasPrefix(path, "/api/clusters/") {
		s.send(w, r, 404, map[string]any{"error": "not found", "path": path})
		return
	}
	key, _ := url.PathUnescape(strings.TrimPrefix(path, "/api/clusters/"))
	if s.findClusterState(key) != nil {
		s.send(w, r, 400, map[string]any{"error": "FT 클러스터는 API 로 제거하지 않습니다 — config.local.json 에서 삭제 후 폴러 재시작"})
		return
	}
	edgeCfg := s.findEdgeCfg(key)
	if edgeCfg == nil {
		s.send(w, r, 404, map[string]any{"error": "장비 없음: " + key})
		return
	}
	if err := s.Store.RemoveEdgeDevice(key); err != nil {
		logf("error", "manage", fmt.Sprintf("config 저장 실패(%s 제거): %v", key, err))
		s.send(w, r, 500, map[string]any{"error": "config 저장 실패 — 폴러 로그를 확인하세요"})
		return
	}
	// 메모리 제거: 설정 목록 + 실행 중 워커(스냅샷은 EdgeManager.Latest 가 걸러낸다).
	out := s.Cfg.EdgeDevices[:0]
	for _, d := range s.Cfg.EdgeDevices {
		if d.Key != key {
			out = append(out, d)
		}
	}
	s.Cfg.EdgeDevices = append([]config.EdgeDevice(nil), out...)
	if s.Edge != nil {
		s.Edge.Remove(key)
	}
	logf("info", "manage", "엣지 장비 제거 "+key)
	s.send(w, r, 200, map[string]any{"ok": true, "removed": key})
}

// doPost 는 POST /api/clusters (추가) · /api/clusters/test (연결 테스트).
func (s *Server) doPost(w http.ResponseWriter, r *http.Request, path string) {
	if !s.writeGate(w, r) {
		return
	}
	if path == "/api/admin/config/import" {
		s.handleConfigImport(w, r)
		return
	}
	if path != "/api/clusters" && path != "/api/clusters/test" {
		s.send(w, r, 404, map[string]any{"error": "not found", "path": path})
		return
	}
	body, ok := s.readJSONBody(w, r)
	if !ok {
		return
	}
	if body == nil {
		s.send(w, r, 400, map[string]any{"error": "JSON 본문이 필요합니다"})
		return
	}
	if path == "/api/clusters/test" {
		s.send(w, r, 200, s.connTest(r.Context(), body))
		return
	}
	s.addDevice(w, r, body)
}

// addDevice 는 장비 추가다(poller.py _add_device).
// 엣지는 핫애드(재시작 불필요), FT 클러스터는 저장 후 재시작 필요(명시 응답).
func (s *Server) addDevice(w http.ResponseWriter, r *http.Request, body map[string]any) {
	typ := strings.ToUpper(bodyStr(body, "type"))
	key := bodyStr(body, "key")
	ip := bodyStr(body, "mgmt")
	if !keyRe.MatchString(key) {
		s.send(w, r, 400, map[string]any{"error": "key 형식: 영숫자/._- 2~40자"})
		return
	}
	if !ipRe.MatchString(ip) {
		s.send(w, r, 400, map[string]any{"error": "관리 IP 형식이 올바르지 않습니다"})
		return
	}
	if s.findClusterState(key) != nil || s.findEdgeCfg(key) != nil {
		s.send(w, r, 409, map[string]any{"error": "이미 존재하는 key: " + key})
		return
	}
	disp := map[string]string{}
	for _, k := range []string{"company", "factory", "site", "asset_tag", "floor_pos", "vendor"} {
		disp[k] = bodyStr(body, k)
	}
	name := bodyStr(body, "label")
	if name == "" {
		name = key
	}

	if ftTypes[typ] {
		entry := map[string]any{"key": key, "name": name, "mgmt_ip": ip}
		for _, k := range []string{"company", "factory", "site", "asset_tag", "floor_pos"} {
			if v := disp[k]; v != "" {
				entry[k] = v
			}
		}
		if v := bodyStr(body, "admin_user"); v != "" {
			entry["admin_user"] = v
		}
		if v := bodyStr(body, "admin_pass"); v != "" {
			entry["admin_password"] = v
			config.RegisterSecret(v)
		}
		if v := bodyStr(body, "root_pass"); v != "" {
			entry["node_root_password"] = v
			config.RegisterSecret(v)
		}
		nodes := []any{}
		for i := 0; i < 2; i++ {
			nip := bodyStr(body, fmt.Sprintf("node%d", i))
			if nip == "" {
				continue
			}
			nd := map[string]any{"ip": nip}
			if v := bodyStr(body, fmt.Sprintf("node%d_user", i)); v != "" {
				nd["ssh_user"] = v
			}
			np := bodyStr(body, fmt.Sprintf("node%d_pass", i))
			if np == "" && i == 1 {
				np = bodyStr(body, "node0_pass")
			}
			if np != "" {
				nd["root_password"] = np
				config.RegisterSecret(np)
			}
			nodes = append(nodes, nd)
		}
		if len(nodes) > 0 {
			entry["nodes"] = nodes
		}
		if err := s.Store.AddEntry(config.SectionClusters, entry); err != nil {
			logf("error", "manage", fmt.Sprintf("config 추가 저장 실패(%s): %v", key, err))
			s.send(w, r, 500, map[string]any{"error": "config 저장 실패 — 폴러 로그를 확인하세요"})
			return
		}
		logf("info", "manage", fmt.Sprintf("FT 클러스터 추가(재시작 대기) %s %s", key, ip))
		s.send(w, r, 200, map[string]any{
			"ok": true, "key": key, "restart_required": true,
			"note": "저장 완료 — FT 클러스터 폴러는 폴러 재시작 후 시작됩니다"})
		return
	}

	kind, known := typeToKind[typ]
	if !known {
		s.send(w, r, 400, map[string]any{"error": fmt.Sprintf(
			"타입 %s 은(는) 아직 실수집기가 없습니다 (지원: everRun/ztC/SRV/NAS/PLC/프린터)", orUnknown(typ))})
		return
	}
	if kind == "server" && bodyStr(body, "platform") == "proxmox" {
		kind = "proxmox"
	}
	entry := map[string]any{"key": key, "kind": kind, "name": name, "ip": ip}
	for _, k := range []string{"company", "factory", "site", "asset_tag", "floor_pos", "vendor"} {
		if v := disp[k]; v != "" {
			entry[k] = v
		}
	}

	comm := bodyStr(body, "community")
	switch kind {
	case "printer", "nas":
		if comm == "" {
			s.send(w, r, 400, map[string]any{"error": "SNMP 커뮤니티가 필요합니다"})
			return
		}
		entry["community"] = comm
	case "plc":
		entry["fins_port"] = int(bodyNum(body, "fins_port", 9600))
		if tags, ok := body["tags"].([]any); ok && len(tags) > 0 {
			entry["tags"] = tags
		}
	case "proxmox":
		user := bodyStr(body, "admin_user")
		if user == "" {
			user = "root@pam"
		}
		pw := bodyStr(body, "admin_pass")
		if pw == "" {
			s.send(w, r, 400, map[string]any{"error": "Proxmox API 비밀번호가 필요합니다"})
			return
		}
		entry["user"], entry["password"] = user, pw
		config.RegisterSecret(pw)
	default: // server — SNMP·Redfish 중 1개 이상
		bmcIP := bodyStr(body, "bmc_ip")
		bmcUser := bodyStr(body, "bmc_user")
		bmcPW := bodyStr(body, "bmc_pass")
		if comm != "" {
			entry["community"] = comm
		}
		if bmcIP != "" && bmcUser != "" {
			entry["bmc_ip"], entry["bmc_user"] = bmcIP, bmcUser
			entry["bmc_password"] = bmcPW
			config.RegisterSecret(bmcPW)
		}
		if _, ok := entry["community"]; !ok {
			if _, ok := entry["bmc_ip"]; !ok {
				s.send(w, r, 400, map[string]any{"error": "SNMP 커뮤니티 또는 BMC(IP+계정) 중 하나는 필요합니다"})
				return
			}
		}
	}

	if err := s.Store.AddEntry(config.SectionEdgeDevices, entry); err != nil {
		logf("error", "manage", fmt.Sprintf("config 추가 저장 실패(%s): %v", key, err))
		s.send(w, r, 500, map[string]any{"error": "config 저장 실패 — 폴러 로그를 확인하세요"})
		return
	}
	// 핫애드 — 설정·워커 동시 반영(다음 라운드부터 실폴러, 수 초 내 첫 라운드).
	var devCfg config.EdgeDevice
	if b, err := json.Marshal(entry); err == nil {
		_ = json.Unmarshal(b, &devCfg)
	}
	s.Cfg.EdgeDevices = append(s.Cfg.EdgeDevices, devCfg)
	if s.Edge != nil {
		if b, err := json.Marshal(entry); err == nil {
			var dc edge.DeviceConfig
			if json.Unmarshal(b, &dc) == nil {
				s.Edge.Add(dc)
			}
		}
	}
	logf("info", "manage", fmt.Sprintf("엣지 장비 추가 %s kind=%s ip=%s", key, kind, ip))
	s.send(w, r, 200, map[string]any{
		"ok": true, "key": key, "kind": kind,
		"note": "추가 완료 — 다음 폴러 라운드(약 1분 내)부터 수집됩니다"})
}

func orUnknown(typ string) string {
	if typ == "" {
		return "?"
	}
	return typ
}

// bodyNum 은 JSON 숫자/문자열을 float64 로 읽는다(폼 입력 방어).
func bodyNum(body map[string]any, key string, def float64) float64 {
	switch v := body[key].(type) {
	case float64:
		return v
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f
		}
	}
	return def
}

// --- 연결 테스트 (poller.py _conn_test 포트) ----------------------------------
// {reachable, auth:{ok,error}, transport, version, warnings[]}를 실측으로 채운다.
// 모든 검사는 읽기 전용 프로브다.

func (s *Server) connTest(ctx context.Context, body map[string]any) map[string]any {
	typ := strings.ToUpper(bodyStr(body, "type"))
	ip := bodyStr(body, "mgmt")
	out := map[string]any{
		"ok": true, "reachable": false,
		"auth":      map[string]any{"ok": true, "error": ""},
		"transport": "", "version": "", "warnings": []any{},
	}
	warn := func(msg string) {
		out["warnings"] = append(out["warnings"].([]any), msg)
	}
	setAuthErr := func(msg string) {
		out["auth"] = map[string]any{"ok": false, "error": msg}
	}

	defer func() {
		if r := recover(); r != nil {
			logf("error", "manage", fmt.Sprintf("연결 테스트 예외: %v", r))
			warn("테스트 내부 오류")
		}
	}()

	if ftTypes[typ] {
		out["transport"] = "avcli"
		out["reachable"] = tcpOK(ip, 443) || tcpOK(ip, 22)
		if !out["reachable"].(bool) {
			warn("관리 콘솔(443/22) 응답 없음")
		}
		warn("자격증명 검증은 폴러 재시작 후 첫 수집에서 이뤄집니다")
		return out
	}
	if typ == "PLC" {
		out["transport"] = "fins"
		port := int(bodyNum(body, "fins_port", 9600))
		sa1 := byte(int(bodyNum(body, "fins_src_node", 84)))
		okResp, rtt := finsProbe(ctx, ip, port, []byte{0x06, 0x01}, sa1)
		out["reachable"] = okResp
		if okResp {
			out["version"] = fmt.Sprintf("FINS RTT %d ms", int64(rtt))
		}
		return out
	}
	if typ == "SRV" && bodyStr(body, "platform") == "proxmox" {
		out["transport"] = "pve-api"
		user := bodyStr(body, "admin_user")
		if user == "" {
			user = "root@pam"
		}
		pw := bodyStr(body, "admin_pass")
		code, err := pveTicket(ctx, ip, user, pw)
		switch {
		case err == nil:
			out["reachable"] = true
		case code != 0:
			out["reachable"] = true
			if code == 401 || code == 403 {
				setAuthErr("인증 실패")
			}
		default:
			warn("PVE API(8006) 접속 실패: " + errClass(err))
		}
		return out
	}

	// SRV(일반)/NAS/PRN/PC — SNMP + (SRV) Redfish
	comm := bodyStr(body, "community")
	if comm != "" {
		out["transport"] = "snmp"
		res, err := snmp.Get(ctx, ip, 161, comm,
			[]string{"1.3.6.1.2.1.1.1.0", "1.3.6.1.2.1.1.5.0"}, 3*time.Second)
		if err == nil {
			if v, ok := res["1.3.6.1.2.1.1.1.0"]; ok && v.Kind != snmp.KindNull {
				out["reachable"] = true
				ver := v.Str
				if len(ver) > 60 {
					ver = ver[:60]
				}
				out["version"] = ver
			}
		}
		if !out["reachable"].(bool) {
			warn("SNMP(161) 무응답 — 커뮤니티/방화벽 확인")
		}
	}
	bmcIP := bodyStr(body, "bmc_ip")
	bmcUser := bodyStr(body, "bmc_user")
	if typ == "SRV" && bmcIP != "" && bmcUser != "" {
		if t, _ := out["transport"].(string); t == "" {
			out["transport"] = "redfish"
		} else {
			out["transport"] = t + "+redfish"
		}
		code, err := redfishGet(ctx, bmcIP, bmcUser, bodyStr(body, "bmc_pass"), "/redfish/v1/Systems")
		switch {
		case err == nil:
			out["reachable"] = true
		case code != 0:
			out["reachable"] = true
			if code == 401 || code == 403 {
				setAuthErr("BMC 인증 실패")
			}
		default:
			warn("Redfish(BMC) 접속 실패: " + errClass(err))
		}
	}
	if comm == "" && !(typ == "SRV" && bmcIP != "") {
		warn("검증할 자격증명이 없습니다(커뮤니티/BMC 미입력)")
	}
	return out
}

// tcpOK 는 TCP 연결 가능 여부다(3초 상한 — Python _tcp).
func tcpOK(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), 3*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// finsProbe 는 옴론 FINS/UDP 명령 1회 송수신이다(읽기전용 — 0601 상태읽기에만 사용).
// edge 패키지의 finsReq 와 같은 프레임 계약: ICF=0x80, RSV=0, GCT=0x02, DNA=0,
// DA1=대상 IP 마지막 옥텟, DA2=0, SNA=0, SA1, SA2=0, SID=0.
func finsProbe(ctx context.Context, ip string, port int, cmd []byte, sa1 byte) (bool, float64) {
	da1 := byte(0)
	if i := strings.LastIndex(ip, "."); i >= 0 {
		var v int
		_, _ = fmt.Sscanf(ip[i+1:], "%d", &v)
		da1 = byte(v)
	}
	frame := append([]byte{0x80, 0x00, 0x02, 0x00, da1, 0x00, 0x00, sa1, 0x00, 0x00}, cmd...)
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "udp", net.JoinHostPort(ip, fmt.Sprintf("%d", port)))
	if err != nil {
		return false, 0
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	t0 := time.Now()
	if _, err := conn.Write(frame); err != nil {
		return false, 0
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return false, 0
	}
	rtt := float64(time.Since(t0)) / float64(time.Millisecond)
	resp := buf[:n]
	// finsOK: 최소 길이 + MRES/SRES(12,13바이트) 0.
	return len(resp) >= 14 && resp[12] == 0 && resp[13] == 0, rtt
}

// insecureHTTP 는 자체서명 인증서의 장비 웹 API(PVE/Redfish)용 클라이언트다.
// 폐쇄망 장비는 CA 체인이 없어 검증을 끈다(Python ssl._create_unverified_context).
var insecureHTTP = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec — 폐쇄망 장비 계약
	},
}

// pveTicket 은 Proxmox 티켓 발급 POST 다(읽기 목적의 인증 프로브).
// 반환: (HTTP 상태코드, 오류). 상태코드가 0 이면 전송 자체 실패다.
func pveTicket(ctx context.Context, ip, user, pw string) (int, error) {
	form := url.Values{"username": {user}, "password": {pw}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+ip+":8006/api2/json/access/ticket", strings.NewReader(form))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := insecureHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("http %d", resp.StatusCode)
	}
	return 0, nil
}

// redfishGet 은 Redfish GET 프로브다(기본 인증). (상태코드, 오류) 반환.
func redfishGet(ctx context.Context, host, user, pw, path string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(user, pw)
	resp, err := insecureHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("http %d", resp.StatusCode)
	}
	return 0, nil
}

// errClass 는 오류의 짧은 분류명이다(Python type(e).__name__ 에 해당).
func errClass(err error) string {
	if err == nil {
		return ""
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "TimeoutError"
	}
	return "OSError"
}
