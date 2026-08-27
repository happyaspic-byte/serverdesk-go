package poller

import (
	"testing"

	"serverdesk/internal/avcli"
	"serverdesk/internal/config"
)

func pStr(s string) *string {
	return &s
}

func clusterView(st *ClusterState) map[string]any {
	snap := st.snapshot()
	nodes := make([]any, 0, len(snap.nodes))
	for _, n := range snap.nodes {
		name := ""
		if n.Name != nil {
			name = *n.Name
		}
		standing := ""
		if n.StandingState != nil {
			standing = *n.StandingState
		}
		mode := ""
		if n.Mode != nil {
			mode = *n.Mode
		}
		nodes = append(nodes, map[string]any{
			"name":           name,
			"state":          n.State,
			"standing_state": standing,
			"mode":           mode,
		})
	}
	vms := make([]any, 0, len(snap.vms))
	for _, vm := range snap.vms {
		vols := make([]any, 0, len(vm.Volumes))
		for _, vol := range vm.Volumes {
			images := make([]any, 0, len(vol.DiskImages))
			for _, img := range vol.DiskImages {
				imgName := ""
				if img.Name != nil {
					imgName = *img.Name
				}
				images = append(images, map[string]any{"name": imgName, "enabled": img.Enabled})
			}
			volName := ""
			if vol.Name != nil {
				volName = *vol.Name
			}
			vols = append(vols, map[string]any{"name": volName, "is_cdrom": vol.IsCdrom, "disk_images": images})
		}
		vmName := ""
		if vm.Name != nil {
			vmName = *vm.Name
		}
		vms = append(vms, map[string]any{"name": vmName, "state": vm.State, "volumes": vols})
	}
	return map[string]any{
		"nodes":          nodes,
		"vms":            vms,
		"unit":           map[string]any{"syncing": false},
		"storage_groups": []any{},
	}
}

func TestPollerE2EClusterFailoverAndRecovery(t *testing.T) {
	cfg := &config.ClusterConfig{
		Key:      "cluster-e2e",
		Name:     "everRun-E2E",
		Platform: "everrun",
		Nodes: []config.NodeConfig{
			{Name: "node0", IP: "10.0.0.11"},
			{Name: "node1", IP: "10.0.0.12"},
		},
	}
	st := NewClusterState(cfg, 10)

	st.setNodes([]avcli.NodeInfo{
		{Name: pStr("node0"), State: "running", StandingState: pStr("normal"), Mode: pStr("normal")},
		{Name: pStr("node1"), State: "running", StandingState: pStr("normal"), Mode: pStr("normal")},
	})
	st.setVMs([]avcli.VMInfo{{
		Name:  pStr("vm1"),
		State: "running",
		Volumes: []avcli.VMVolume{{
			Name:    pStr("vol1"),
			IsCdrom: false,
			DiskImages: []avcli.DiskImage{
				{Name: pStr("img0"), Enabled: true},
				{Name: pStr("img1"), Enabled: true},
			},
		}},
	}})
	st.setNodeOS("10.0.0.11", map[string]any{
		"ip": "10.0.0.11", "name": "node0", "reachable": true, "source": "ssh", "cpu_pct": 15.0,
	}, nil)
	st.setNodeOS("10.0.0.12", map[string]any{
		"ip": "10.0.0.12", "name": "node1", "reachable": true, "source": "ssh", "cpu_pct": 18.0,
	}, nil)

	view1 := clusterView(st)
	if got := DeriveStatus(view1); got != "op" {
		t.Fatalf("Step 1 status=%s want op", got)
	}
	if got := deriveSync(view1, "op"); got != "sync" {
		t.Fatalf("Step 1 sync=%s want sync", got)
	}

	st.setNodes([]avcli.NodeInfo{
		{Name: pStr("node0"), State: "running", StandingState: pStr("normal"), Mode: pStr("normal")},
		{Name: pStr("node1"), State: "stopped", StandingState: pStr("broken"), Mode: pStr("offline")},
	})
	st.setVMs([]avcli.VMInfo{{
		Name:  pStr("vm1"),
		State: "running",
		Volumes: []avcli.VMVolume{{
			Name:    pStr("vol1"),
			IsCdrom: false,
			DiskImages: []avcli.DiskImage{
				{Name: pStr("img0"), Enabled: true},
				{Name: pStr("img1"), Enabled: false},
			},
		}},
	}})
	st.failNodeOS("10.0.0.12", "node1")

	view2 := clusterView(st)
	if got := DeriveStatus(view2); got != "deg" {
		t.Fatalf("Step 2 status=%s want deg", got)
	}
	if got := deriveSync(view2, "deg"); got != "simplex" {
		t.Fatalf("Step 2 sync=%s want simplex", got)
	}
	if reach := st.OSReachable(); reach["10.0.0.12"] {
		t.Fatalf("Step 2 node1 SSH should be unreachable: %v", reach)
	}

	st.snmpNodeOS("10.0.0.12", "node1", map[string]any{
		"reachable": true, "cpu_pct": 5.0, "mem_pct": 20.0,
	})
	if src := st.snapshot().nodeOS["10.0.0.12"]["source"]; src != "snmp" {
		t.Fatalf("Step 3 source=%v want snmp", src)
	}

	st.setNodes([]avcli.NodeInfo{
		{Name: pStr("node0"), State: "running", StandingState: pStr("normal"), Mode: pStr("normal")},
		{Name: pStr("node1"), State: "running", StandingState: pStr("normal"), Mode: pStr("normal")},
	})
	st.setVMs([]avcli.VMInfo{{
		Name:  pStr("vm1"),
		State: "running",
		Volumes: []avcli.VMVolume{{
			Name:    pStr("vol1"),
			IsCdrom: false,
			DiskImages: []avcli.DiskImage{
				{Name: pStr("img0"), Enabled: true},
				{Name: pStr("img1"), Enabled: true},
			},
		}},
	}})
	st.setNodeOS("10.0.0.12", map[string]any{
		"ip": "10.0.0.12", "name": "node1", "reachable": true, "source": "ssh", "cpu_pct": 19.0,
	}, nil)

	view4 := clusterView(st)
	if got := DeriveStatus(view4); got != "op" {
		t.Fatalf("Step 4 status=%s want op", got)
	}
	if got := deriveSync(view4, "op"); got != "sync" {
		t.Fatalf("Step 4 sync=%s want sync", got)
	}
	if src := st.snapshot().nodeOS["10.0.0.12"]["source"]; src != "ssh" {
		t.Fatalf("Step 4 source=%v want ssh", src)
	}
}
