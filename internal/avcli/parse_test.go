package avcli

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func parseFixture(t *testing.T, name string) *Element {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	root, err := ParseXML(string(data))
	if err != nil {
		t.Fatalf("ParseXML(%s): %v", name, err)
	}
	return root
}

const (
	gib = 1024 * 1024 * 1024
)

func TestParseXMLContract(t *testing.T) {
	if _, err := ParseXML(""); err == nil {
		t.Error("빈 응답은 에러여야 한다(removable-disk-info 계약)")
	}
	if _, err := ParseXML("   \n "); err == nil {
		t.Error("공백만 있는 응답도 에러")
	}
	if _, err := ParseXML("<Error>auth failed</Error>"); err == nil || err.Error() != "auth failed" {
		t.Errorf("루트 <Error> 는 침묵 실패 방지를 위해 에러: %v", err)
	}
	if _, err := ParseXML("<avance><unclosed>"); err == nil {
		t.Error("깨진 XML 은 에러")
	}
	// 정상 빈 결과(diagnostic-info 등)는 에러가 아니다
	root, err := ParseXML(`<?xml version="1.0"?><avance/>`)
	if err != nil || root == nil || root.Tag != "avance" {
		t.Errorf("빈 <avance/>: root=%v err=%v", root, err)
	}
	// BOM 제거
	if _, err := ParseXML("\uFEFF<avance/>"); err != nil {
		t.Errorf("BOM: %v", err)
	}
}

func TestParseNodeInfo(t *testing.T) {
	nodes := ParseNodeInfo(parseFixture(t, "node_info.xml"))
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d", len(nodes))
	}
	// fixture 는 node1 이 먼저 — 이름순 정렬돼야 한다
	if strVal(nodes[0].Name) != "node0" {
		t.Fatalf("정렬: first = %q", strVal(nodes[0].Name))
	}
	n0, n1 := nodes[0], nodes[1]
	if !n0.Primary || n1.Primary {
		t.Error("primary 플래그")
	}
	if !n0.Healthy || !n1.Healthy {
		t.Error("healthy 판정")
	}
	if int64Val(n0.MemoryBytes) != 16*gib {
		t.Errorf("memory = %d, want %d", int64Val(n0.MemoryBytes), 16*gib)
	}
	if strVal(n0.MemoryRaw) != "16 GiB" {
		t.Errorf("memory_raw = %q", strVal(n0.MemoryRaw))
	}
	if strVal(n0.IP) != "172.30.1.11" || strVal(n0.Gateway) != "172.30.1.254" {
		t.Errorf("node0 net = %v/%v", n0.IP, n0.Gateway)
	}
	if len(n1.DNS) != 2 {
		t.Errorf("node1 dns = %#v", n1.DNS)
	}
	if len(n0.VMPlacements) != 1 || strVal(n0.VMPlacements[0].ID) != "vm:o920" {
		t.Errorf("node0 placements = %#v", n0.VMPlacements)
	}
	if len(n1.VMPlacements) != 0 {
		t.Error("node1 <virtual-machines/> 는 빈 배열이어야 한다")
	}
	if n0.Vulnerability.MeltdownPatch == nil || !*n0.Vulnerability.MeltdownPatch {
		t.Error("meltdown patch")
	}
	// 빈 <sub-state/> 는 nil 이어야 한다(ElementTree .text is None 방어)
	if n0.SubState != nil {
		t.Errorf("sub_state = %q, want nil", *n0.SubState)
	}
	if DetectPlatform(nodes) != "everrun" {
		t.Error("ECS/H110M4-C43 은 everrun")
	}
}

func TestDetectPlatformEdge(t *testing.T) {
	root, err := ParseXML(`<avance><node>
		<name>node0</name><manufacturer>Stratus</manufacturer><model>ztC Edge</model>
	</node></avance>`)
	if err != nil {
		t.Fatal(err)
	}
	if got := DetectPlatform(ParseNodeInfo(root)); got != "ztcedge" {
		t.Errorf("DetectPlatform = %q, want ztcedge", got)
	}
}

func TestParseUnitInfo(t *testing.T) {
	u := ParseUnitInfo(parseFixture(t, "unit_info.xml"))
	if u == nil {
		t.Fatal("nil unit")
	}
	if strVal(u.ID) != "supernova:o32" || strVal(u.Version) != "8.1.0.2-19" {
		t.Errorf("unit id/version = %v/%v", u.ID, u.Version)
	}
	if u.Syncing {
		t.Error("syncing=false 여야 한다")
	}
	r := u.Resources
	if int64Val(r.TotalVcpus) != 6 || *r.UsedVcpus != 3.0 {
		t.Errorf("vcpus = %v/%v", r.TotalVcpus, r.UsedVcpus)
	}
	if r.VcpuPct == nil || *r.VcpuPct != 50.0 {
		t.Errorf("vcpu_pct = %v, want 50", r.VcpuPct)
	}
	if int64Val(r.TotalMemoryBytes) != 16*gib || int64Val(r.UsedMemoryBytes) != 8*gib {
		t.Errorf("mem = %v/%v", r.TotalMemoryBytes, r.UsedMemoryBytes)
	}
	if r.MemoryPct == nil || *r.MemoryPct != 50.0 {
		t.Errorf("memory_pct = %v", r.MemoryPct)
	}
	if len(u.SharedNetworks) != 1 || strVal(u.SharedNetworks[0].Role) != "business" {
		t.Errorf("shared_networks = %#v", u.SharedNetworks)
	}
	if len(u.StorageGroups) != 1 || strVal(u.StorageGroups[0].ID) != "storagegroup:o96" {
		t.Errorf("storage_groups = %#v", u.StorageGroups)
	}
	if len(u.VirtualMachines) != 1 || strVal(u.VirtualMachines[0].HaMode) != "ft" {
		t.Errorf("virtual_machines = %#v", u.VirtualMachines)
	}
	if len(u.Ntp) != 1 || u.Ntp[0] != "172.30.1.5" {
		t.Errorf("ntp = %#v", u.Ntp)
	}
}

func TestParseNetworkInfo(t *testing.T) {
	nets := ParseNetworkInfo(parseFixture(t, "network_info.xml"))
	if len(nets) != 3 {
		t.Fatalf("nets = %d", len(nets))
	}
	priv := nets[0]
	if strVal(priv.Role) != "a-link" || int64Val(priv.Mtu) != 9000 {
		t.Errorf("priv0 = %#v", priv)
	}
	if int64Val(priv.BandwidthBps) != 1_000_000_000 {
		t.Errorf("bandwidth = %d, want 1e9", int64Val(priv.BandwidthBps))
	}
	if strVal(priv.BandwidthRaw) != "1 Gb/s" {
		t.Errorf("bandwidth_raw = %q", strVal(priv.BandwidthRaw))
	}
}

func TestParseVMInfo(t *testing.T) {
	vms := ParseVMInfo(parseFixture(t, "vm_info.xml"))
	if len(vms) != 2 {
		t.Fatalf("vms = %d", len(vms))
	}
	// 이름순: ubt_server < winsrv_2022
	ubt, win := vms[0], vms[1]
	if strVal(ubt.Name) != "ubt_server" || strVal(win.Name) != "winsrv_2022" {
		t.Fatalf("정렬: %q, %q", strVal(ubt.Name), strVal(win.Name))
	}

	// --- winsrv: 정상 FT ---
	if win.Redundancy != "redundant" {
		t.Errorf("winsrv redundancy = %q", win.Redundancy)
	}
	if !win.DiskMirrored || !win.NicRedundant {
		t.Error("winsrv disk/nic 이중화")
	}
	if len(win.Nodes) != 2 || win.Nodes[0] != "node0" || win.Nodes[1] != "node1" {
		t.Errorf("winsrv nodes = %#v", win.Nodes)
	}
	if strVal(win.HaMode) != "ft" || int64Val(win.Cpus) != 4 || int64Val(win.MemoryBytes) != 10*gib {
		t.Errorf("winsrv basic = %v/%v/%v", win.HaMode, win.Cpus, win.MemoryBytes)
	}
	// a-links 동적 태그
	if len(win.ALinks) != 2 {
		t.Fatalf("a_links = %d", len(win.ALinks))
	}
	alNames := map[string]bool{}
	for _, a := range win.ALinks {
		alNames[a.Network] = true
		if strVal(a.Role) != "a-link" || int64Val(a.BandwidthBps) != 1_000_000_000 {
			t.Errorf("alink = %#v", a)
		}
	}
	if !alNames["net_82"] || !alNames["priv0"] {
		t.Errorf("a-link 네트워크명 = %v", alNames)
	}
	// 볼륨: vda(미러 정상) + cdrom
	if len(win.Volumes) != 2 {
		t.Fatalf("volumes = %d", len(win.Volumes))
	}
	vda := win.Volumes[0]
	if vda.IsCdrom || vda.Mirrored == nil || !*vda.Mirrored {
		t.Errorf("vda mirrored = %v", vda.Mirrored)
	}
	if int64Val(vda.SizeBytes) != 50*gib || int64Val(vda.SectorSizeBytes) != 512 {
		t.Errorf("vda size = %v/%v", vda.SizeBytes, vda.SectorSizeBytes)
	}
	if len(vda.DiskImages) != 2 || strVal(vda.DiskImages[1].NodeID) != "host:o1078" {
		t.Errorf("vda images = %#v", vda.DiskImages)
	}
	cd := win.Volumes[1]
	if !cd.IsCdrom || cd.Mirrored != nil || cd.Name != nil {
		t.Errorf("cdrom = is_cdrom %v mirrored %v name %v", cd.IsCdrom, cd.Mirrored, cd.Name)
	}
	if strVal(cd.DeviceID) != "vbd:o923" {
		t.Errorf("cdrom device_id = %v", cd.DeviceID)
	}
	// 인스턴스: <ID> 대문자 태그
	if len(win.Instances) != 2 || strVal(win.Instances[0].ID) != "localvirtualmachine:o917" {
		t.Errorf("instances = %#v", win.Instances)
	}

	// --- ubt: simplex + 미러 깨짐 ---
	if ubt.Redundancy != "simplex" {
		t.Errorf("ubt redundancy = %q (인스턴스 1개)", ubt.Redundancy)
	}
	if ubt.DiskMirrored {
		t.Error("ubt disk_mirrored should be false (disk-image 1개+DISABLED)")
	}
	if ubt.NicRedundant {
		t.Error("ubt nic_redundant should be false (net1 DISABLED)")
	}
	if strVal(ubt.Interfaces[0].Net1Status) != "DISABLED" {
		t.Errorf("ubt net1 = %v", ubt.Interfaces[0].Net1Status)
	}
}

func TestParseStorageInfoV2(t *testing.T) {
	groups := ParseStorageInfo(parseFixture(t, "storage_info_v2.xml"))
	if len(groups) != 1 {
		t.Fatalf("groups = %d", len(groups))
	}
	g := groups[0]
	if int64Val(g.SizeBytes) != 100*gib || int64Val(g.UsedBytes) != int64(85.5*gib) {
		t.Errorf("size = %v/%v", g.SizeBytes, g.UsedBytes)
	}
	if g.UsedPct == nil || math.Abs(*g.UsedPct-85.5) > 1e-9 {
		t.Errorf("used_pct = %v, want 85.5", g.UsedPct)
	}
	if g.FreeBytes == nil || *g.FreeBytes != int64(14.5*gib) {
		t.Errorf("free_bytes = %v", g.FreeBytes)
	}
	// v2 는 sector-size 가 없고 논리/물리로 나뉜다 → 논리 섹터 폴백
	if int64Val(g.SectorSizeBytes) != 512 || strVal(g.SectorSizeRaw) != "512 B" {
		t.Errorf("sector fallback = %v/%q", g.SectorSizeBytes, strVal(g.SectorSizeRaw))
	}
	if strVal(g.DiskType) != "512n" {
		t.Errorf("disk_type = %v", g.DiskType)
	}
	if len(g.Disks) != 2 {
		t.Fatalf("disks = %d", len(g.Disks))
	}
	if strVal(g.Disks[1].NodeName) != "node1" || strVal(g.Disks[1].StandingState) != "broken" {
		t.Errorf("disk1 = %#v", g.Disks[1])
	}
	if len(g.Volumes) != 2 || strVal(g.Volumes[0].Name) != "root" {
		t.Errorf("volumes = %#v", g.Volumes)
	}
	if g.Volumes[0].ID != nil {
		t.Error("v2 --volumes 의 볼륨에는 id 가 없어야 한다")
	}
	if int64Val(g.Volumes[0].SectorSizeBytes) != 512 {
		t.Errorf("volume-sector-size = %v", g.Volumes[0].SectorSizeBytes)
	}
}

func TestParseStorageInfoLegacy(t *testing.T) {
	root, err := ParseXML(`<avance><storage-group>
		<name>sg</name><id>storagegroup:o1</id>
		<size>10 GiB</size><size-used>5 GiB</size-used>
		<sector-size>512 B</sector-size>
	</storage-group></avance>`)
	if err != nil {
		t.Fatal(err)
	}
	g := ParseStorageInfo(root)[0]
	// 구버전은 sector-size 를 그대로 준다(폴백 아님)
	if int64Val(g.SectorSizeBytes) != 512 {
		t.Errorf("legacy sector = %v", g.SectorSizeBytes)
	}
	if len(g.Disks) != 0 || len(g.Volumes) != 0 {
		t.Error("구버전에는 disks/volumes 가 없다 — 빈 배열이어야 한다")
	}
	if g.UsedPct == nil || *g.UsedPct != 50.0 {
		t.Errorf("used_pct = %v", g.UsedPct)
	}
}

func TestParseVolumeInfo(t *testing.T) {
	vols := ParseVolumeInfo(parseFixture(t, "volume_info.xml"))
	if len(vols) != 2 {
		t.Fatalf("vols = %d", len(vols))
	}
	boot := vols[0]
	if strVal(boot.ID) != "volume:o891" || int64Val(boot.SizeBytes) != 50*gib {
		t.Errorf("boot = %#v", boot)
	}
	if boot.Bootable == nil || !*boot.Bootable {
		t.Error("bootable")
	}
	if strVal(boot.StorageGroupID) != "storagegroup:o96" {
		t.Errorf("sg join = %v", boot.StorageGroupID)
	}
	iso := vols[1]
	if iso.SectorSizeBytes != nil {
		t.Error("ISO 볼륨에는 sector-size 가 없다")
	}
}

func TestParseImageContainerInfoAndJoin(t *testing.T) {
	cs := ParseImageContainerInfo(parseFixture(t, "image_container_info.xml"))
	if len(cs) != 2 {
		t.Fatalf("containers = %d", len(cs))
	}
	c0 := cs[0]
	if c0.UsedPct == nil || *c0.UsedPct != 50.0 {
		t.Errorf("used_pct = %v", c0.UsedPct)
	}
	if c0.HasFilesystem == nil || !*c0.HasFilesystem || c0.IsLocal == nil || *c0.IsLocal {
		t.Errorf("camelCase bools = %v/%v", c0.HasFilesystem, c0.IsLocal)
	}

	// 이름 접두어 조인: internal-name "winsrv_2022_en_EVAL_01" + "_"
	vms := ParseVMInfo(parseFixture(t, "vm_info.xml"))
	vmPtrs := []*VMInfo{}
	for i := range vms {
		vmPtrs = append(vmPtrs, &vms[i])
	}
	cPtrs := []*ImageContainer{}
	for i := range cs {
		cPtrs = append(cPtrs, &cs[i])
	}
	JoinImageContainers(vmPtrs, cPtrs)
	if strVal(cPtrs[0].VmID) != "vm:o920" || strVal(cPtrs[0].VmName) != "winsrv_2022" {
		t.Errorf("joined = %v/%v", cPtrs[0].VmID, cPtrs[0].VmName)
	}
	var win *VMInfo
	for _, vm := range vmPtrs {
		if strVal(vm.Name) == "winsrv_2022" {
			win = vm
		}
	}
	if win == nil || len(win.ImageContainers) != 1 || win.ImageContainers[0] != "imagecontainer:o871" {
		t.Errorf("vm.image_containers = %#v", win.ImageContainers)
	}
	if cPtrs[1].VmID != nil {
		t.Error("매칭 실패 컨테이너는 orphan — 조용히 무시")
	}
}

func TestParseAlertInfoAndTimezone(t *testing.T) {
	alerts := ParseAlertInfo(parseFixture(t, "alert_info.xml"))
	if len(alerts) != 3 {
		t.Fatalf("alerts = %d", len(alerts))
	}
	// 시각 내림차순
	if strVal(alerts[0].ID) != "alert:11800" || strVal(alerts[2].ID) != "alert:11728" {
		t.Fatalf("정렬: %q .. %q", strVal(alerts[0].ID), strVal(alerts[2].ID))
	}
	// 분류: unreachable → critical, rebooted unexpectedly → warning, tooFew → warning
	if alerts[0].Severity != "critical" {
		t.Errorf("unreachable severity = %q", alerts[0].Severity)
	}
	if alerts[1].Severity != "warning" || alerts[2].Severity != "warning" {
		t.Errorf("severities = %q/%q", alerts[1].Severity, alerts[2].Severity)
	}
	// 숫자 원문의 순진한 해석도 함께 노출
	if strVal(alerts[2].SeverityNumeric) != "critical" { // severity=0
		t.Errorf("severity_numeric = %v", alerts[2].SeverityNumeric)
	}
	if alerts[0].AgeSecs != nil {
		t.Error("age_secs 는 TZ 보정 전에는 없어야 한다(omitempty)")
	}

	// TZ 보정: 노드 로컬시각 = UTC+9 (KST 32400)
	naive := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC).Unix()
	if *alerts[0].TimeEpochNaive != naive {
		t.Fatalf("naive epoch = %d, want %d", *alerts[0].TimeEpochNaive, naive)
	}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	alerts = ApplyAlertTimezone(alerts, 32400, now)
	if *alerts[0].TimeEpoch != naive-32400 {
		t.Errorf("corrected epoch = %d, want %d", *alerts[0].TimeEpoch, naive-32400)
	}
	// 보정 후 시각은 2026-07-20 00:00:00 UTC → now(12:00)까지 12h
	if *alerts[0].AgeSecs != 12*3600 {
		t.Errorf("age = %d, want %d", *alerts[0].AgeSecs, 12*3600)
	}
	if alerts[0].TzOffsetSecs != 32400 {
		t.Errorf("tz_offset_secs = %d", alerts[0].TzOffsetSecs)
	}
}

func TestClassifyAlert(t *testing.T) {
	cases := []struct {
		name, desc, sev, want string
	}{
		{"Node Maintenance", "", "0", "warning"}, // 숫자(0=critical 추정)에 속으면 안 된다
		{"The quorum server is offline", "", "1", "critical"},
		{"Node Unreachable", "Node node1 is unreachable", "1", "critical"},
		{"some odd event", "", "0", "warning"}, // info 텍스트는 숫자로 warning 까지만 끌어올림
		{"some odd event", "", "2", "info"},
		{"Call Home Not Enabled", "", "", "warning"},
		{"VM failed to start", "", "2", "critical"}, // 숫자 2 여도 키워드가 우선
		{"unit_isSyncing", "", "", "info"},          // `_` 도 단어 문자라 \bsync 는 매칭되지 않는다
	}
	for _, c := range cases {
		if got := ClassifyAlert(c.name, c.desc, c.sev); got != c.want {
			t.Errorf("ClassifyAlert(%q,%q,%q) = %q, want %q", c.name, c.desc, c.sev, got, c.want)
		}
	}
}

func TestParseLicenseInfo(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	lic := parseLicenseInfo(parseFixture(t, "license_info.xml"), now)
	if lic == nil {
		t.Fatal("nil license")
	}
	if strVal(lic.Name) != "eE_UNTRACKED_TRIAL" || strVal(lic.Type) != "trial" {
		t.Errorf("lic = %v/%v", lic.Name, lic.Type)
	}
	wantInstall := time.Date(2026, 7, 6, 7, 35, 25, 0, time.UTC).Unix()
	if *lic.InstallEpoch != wantInstall {
		t.Errorf("install_epoch = %d, want %d", *lic.InstallEpoch, wantInstall)
	}
	wantExpire := time.Date(2026, 9, 17, 7, 35, 26, 0, time.UTC).Unix()
	if lic.ExpireEpoch == nil || *lic.ExpireEpoch != wantExpire {
		t.Errorf("expire_epoch = %v, want %d", lic.ExpireEpoch, wantExpire)
	}
	// 2026-08-01 → 2026-09-17 07:35:26 : 47일 + 7h35m → floor 47
	if lic.DaysLeft == nil || *lic.DaysLeft != 47 {
		t.Errorf("days_left = %v, want 47", lic.DaysLeft)
	}
	if !lic.Expires {
		t.Error("expires")
	}
}

func TestParseLicenseInfoEdge(t *testing.T) {
	// ztC Edge: expires=false 라 expire-date 요소 자체가 없다
	root, err := ParseXML(`<avance><license>
		<name>ze-p-22267</name><id>license:o5</id><type>standard</type>
		<edition>Standard</edition><install-date>Mon Jul 06 07:35:25 UTC 2026</install-date>
		<allow-features>true</allow-features><activated>true</activated>
		<expires>false</expires><installed>true</installed>
	</license></avance>`)
	if err != nil {
		t.Fatal(err)
	}
	lic := parseLicenseInfo(root, time.Now())
	if lic.Expires {
		t.Error("expires=false")
	}
	if lic.ExpireDate != nil || lic.ExpireEpoch != nil || lic.DaysLeft != nil {
		t.Error("expires=false 이면 expire 필드는 null 이어야 한다")
	}
	if strVal(lic.Name) != "ze-p-22267" {
		t.Errorf("name = %v", lic.Name)
	}
}

func TestParseLEDInfo(t *testing.T) {
	// ztC Edge 실측 형태: 동적 태그 <node0>flashing</node0>
	leds := ParseLEDInfo(parseFixture(t, "led_info.xml"))
	if len(leds) != 2 || strVal(leds[0].Node) != "node0" || strVal(leds[0].Led) != "flashing" {
		t.Fatalf("leds = %#v", leds)
	}
	if strVal(leds[1].Led) != "off" {
		t.Errorf("leds[1] = %#v", leds[1])
	}
	// 정규 node 형태도 받는다
	root, _ := ParseXML(`<avance><node><name>node0</name><LED>Flashing</LED></node></avance>`)
	leds = ParseLEDInfo(root)
	if len(leds) != 1 || strVal(leds[0].Led) != "flashing" {
		t.Errorf("node-form leds = %#v", leds)
	}
}

func TestSummarizeClusterHealth(t *testing.T) {
	nodes := ParseNodeInfo(parseFixture(t, "node_info.xml"))
	unit := ParseUnitInfo(parseFixture(t, "unit_info.xml"))
	vms := ParseVMInfo(parseFixture(t, "vm_info.xml"))
	groups := ParseStorageInfo(parseFixture(t, "storage_info_v2.xml"))
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	alerts := ApplyAlertTimezone(ParseAlertInfo(parseFixture(t, "alert_info.xml")), 32400, now)
	lic := parseLicenseInfo(parseFixture(t, "license_info.xml"), now)

	h := SummarizeClusterHealth(unit, nodes, vms, groups, alerts, lic)
	// ubt_server simplex + 미러 깨짐 + storage 85.5%(>=85) → authoritative warning.
	// critical 알림 1건이 권위 위에 올라 level critical.
	if h.AuthoritativeLevel != "warning" {
		t.Errorf("authoritative = %q, want warning", h.AuthoritativeLevel)
	}
	if h.Level != "critical" {
		t.Errorf("level = %q, want critical (critical 알림 오버레이)", h.Level)
	}
	if h.AlertLevel != "critical" {
		t.Errorf("alert_level = %q", h.AlertLevel)
	}
	if h.AlertCounts.Critical != 1 || h.AlertCounts.Warning != 2 {
		t.Errorf("counts = %+v", h.AlertCounts)
	}
	if h.AlertCountsRecent.Critical != 1 {
		t.Errorf("recent = %+v (24h 내)", h.AlertCountsRecent)
	}
	joined := ""
	for _, r := range h.Reasons {
		joined += r + "\n"
	}
	for _, want := range []string{
		"VM ubt_server 이중화 상실(simplex)",
		"VM ubt_server 디스크 미러 비정상",
		"스토리지그룹 StorageGroup1 사용률 85.5%",
		"critical 알림 1건",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("reasons 에 %q 없음:\n%s", want, joined)
		}
	}

	// 권위 ok 일 때는 알림이 warning 까지만 올린다(이벤트 로그는 해소 플래그가 없다)
	h2 := SummarizeClusterHealth(nil, nil, nil, nil, alerts, nil)
	if h2.Level != "warning" || h2.AuthoritativeLevel != "ok" || h2.AlertLevel != "critical" {
		t.Errorf("cap: %+v", h2)
	}
	if !strings.Contains(h2.Reasons[0], "권위 상태는 정상") {
		t.Errorf("cap reason = %q", h2.Reasons[0])
	}
}

func TestSummarizeClusterHealthStorageCritical(t *testing.T) {
	root, _ := ParseXML(`<avance><storage-group>
		<name>sg</name><size>100 GiB</size><size-used>96 GiB</size-used>
	</storage-group></avance>`)
	groups := ParseStorageInfo(root)
	h := SummarizeClusterHealth(nil, nil, nil, groups, nil, nil)
	if h.Level != "critical" {
		t.Errorf("96%% → level = %q", h.Level)
	}
}

func TestValueParsers(t *testing.T) {
	sizeCases := []struct {
		in   string
		want int64
	}{
		{"512 B", 512},
		{"16 GiB", 16 * gib},
		{"1.5 KiB", 1536},
		{"10 GB", 10_000_000_000},
		{"1 TiB", 1099511627776},
		{"1,024 KiB", 1048576}, // 콤마 제거
		{"1024", 1024},         // 단위 없음 = B
	}
	for _, c := range sizeCases {
		got := ParseSize(c.in)
		if got == nil || *got != c.want {
			t.Errorf("ParseSize(%q) = %v, want %d", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "10 XB", "GiB"} {
		if got := ParseSize(bad); got != nil {
			t.Errorf("ParseSize(%q) = %v, want nil", bad, *got)
		}
	}

	bwCases := []struct {
		in   string
		want int64
	}{
		{"1 Gb/s", 1_000_000_000},
		{"10 Gb/s", 10_000_000_000},
		{"10 Gbps", 10_000_000_000},
		{"100 Mb/s", 100_000_000},
	}
	for _, c := range bwCases {
		got := ParseBandwidth(c.in)
		if got == nil || *got != c.want {
			t.Errorf("ParseBandwidth(%q) = %v, want %d", c.in, got, c.want)
		}
	}
	if got := ParseBandwidth("fast"); got != nil {
		t.Errorf("ParseBandwidth(fast) = %v", *got)
	}

	boolCases := map[string]bool{"true": true, "ENABLED": true, "1": true, "false": false, "DISABLED": false, "0": false}
	for in, want := range boolCases {
		if got := ParseBool(in); got == nil || *got != want {
			t.Errorf("ParseBool(%q) = %v, want %v", in, got, want)
		}
	}
	if got := ParseBool("maybe"); got != nil {
		t.Errorf("ParseBool(maybe) = %v", *got)
	}

	// java Date.toString (UTC)
	got := ParseJavaDate("Mon Jul 06 07:35:25 UTC 2026")
	want := time.Date(2026, 7, 6, 7, 35, 25, 0, time.UTC).Unix()
	if got == nil || *got != want {
		t.Errorf("ParseJavaDate(java) = %v, want %d", got, want)
	}
	// ISO-like 은 naive UTC 로 읽는다(호스트 TZ 이중 적용 방지)
	got = ParseJavaDate("2026-07-14 16:02:24")
	want = time.Date(2026, 7, 14, 16, 2, 24, 0, time.UTC).Unix()
	if got == nil || *got != want {
		t.Errorf("ParseJavaDate(iso) = %v, want %d", got, want)
	}
	if got := ParseJavaDate("not a date"); got != nil {
		t.Errorf("ParseJavaDate(bad) = %v", *got)
	}
	if got := ParseJavaDate(""); got != nil {
		t.Errorf("ParseJavaDate(empty) = %v", *got)
	}
}

func TestParseTextKV(t *testing.T) {
	raw := "  -> Community : public\nUptime : 1234\n-> Community : private\nno colon here\n"
	m := ParseTextKV(raw)
	if m["Uptime"] != "1234" {
		t.Errorf("Uptime = %v", m["Uptime"])
	}
	list, ok := m["Community"].([]string)
	if !ok || len(list) != 2 || list[0] != "public" || list[1] != "private" {
		t.Errorf("Community dup = %#v", m["Community"])
	}
	if len(ParseTextKV("")) != 0 {
		t.Error("empty")
	}
}
