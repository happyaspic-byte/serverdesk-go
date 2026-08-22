package topology

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 스토리지 계층 빌더 (스토리지 그룹 / 논리 디스크 / 볼륨 / 미러 디스크 이미지 / 이미지 컨테이너)
// ---------------------------------------------------------------------------

// buildStorageGroups 는 스토리지 그룹(level 5)과 노드별 논리 디스크(level 4)를 만든다 (R10).
func (b *clusterBuild) buildStorageGroups() {
	for _, sg := range b.c.StorageGroups {
		sgid := gid(b.cid, sg.ID)
		st, reasons, upct := sgStatus(sg)
		n := b.g.addNode(sgid, nodeInit{
			Type:    "storagegroup",
			Label:   ptrOrNil(sg.Name),
			Status:  st,
			Level:   levels["storagegroup"],
			Parent:  &b.clusterGID,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(sg.ID)},
				{"size_bytes", intPtrToAny(sg.SizeBytes)},
				{"used_bytes", intPtrToAny(sg.UsedBytes)},
				{"size_label", strPtrToAny(HumanSize(sg.SizeBytes))},
				{"used_label", strPtrToAny(HumanSize(sg.UsedBytes))},
				{"used_pct", floatPtrToAny(upct)},
				{"disk_type", strOrNil(sg.DiskType)},
				{"logical_sector_size", sg.LogicalSectorSize},
				{"physical_sector_size", sg.PhysicalSectorSize},
			},
		})
		n.Reasons = append(n.Reasons, reasons...)
		b.g.addEdge(b.clusterGID, sgid, "contains", "ok")
		b.sgByRawID[sg.ID] = sgid

		for _, d := range sg.Disks {
			dgid := gid(b.cid, d.ID)
			hostGID, hasHost := b.nodeByName[d.Node]
			dss := strings.ToLower(d.StandingState)
			dst := "ok"
			if dss != "" && dss != "normal" {
				dst = "degraded"
			}
			parent := &b.clusterGID
			if hasHost {
				parent = &hostGID
			}
			lane := d.Node
			if lane == "" {
				lane = LaneShared
			}
			dn := b.g.addNode(dgid, nodeInit{
				Type:    "disk",
				Label:   ptrOrNil(fmt.Sprintf("%s (%s)", d.Name, d.Node)),
				Status:  dst,
				Level:   levels["disk"],
				Parent:  parent,
				Lane:    lane,
				Cluster: ptrOrNil(b.cid),
				Meta: omap{
					{"raw_id", strOrNil(d.ID)},
					{"node", strOrNil(d.Node)},
					{"size_bytes", intPtrToAny(d.SizeBytes)},
					{"used_bytes", intPtrToAny(d.UsedBytes)},
					{"size_label", strPtrToAny(HumanSize(d.SizeBytes))},
					{"used_pct", floatPtrToAny(Pct(d.UsedBytes, d.SizeBytes))},
					{"standing_state", strOrNil(d.StandingState)},
				},
			})
			if dss != "" && dss != "normal" {
				dn.Reasons = append(dn.Reasons, fmt.Sprintf("논리디스크 standing-state=%s", dss))
			}
			if hasHost {
				b.g.addEdge(hostGID, dgid, "contains", "ok")
			}
			b.g.addEdge(dgid, sgid, "member-of", dst)
		}
	}
}

// buildVMVolumes 는 VM 소속 볼륨과 디스크 이미지(미러 조각)를 만든다 (R3/R4).
// id 없는 볼륨(cdrom 등)은 그래프 노드로 만들지 않고 VM 메타에만 남긴다.
func (b *clusterBuild) buildVMVolumes(vm VMInput, vgid string, vmNode *Node) {
	for _, vol := range vm.Volumes {
		if vol.ID == "" {
			vmNode.Meta.appendToList("removable_devices", omap{
				{"device", strOrNil(vol.Device)},
				{"device_id", strOrNil(vol.DeviceID)},
			})
			continue
		}
		volgid := gid(b.cid, vol.ID)
		mstate, mst, mreasons := volumeMirrorStatus(vol, b.syncing)
		vn := b.g.addNode(volgid, nodeInit{
			Type:    "volume",
			Label:   ptrOrNil(vol.Name),
			Status:  mst,
			Level:   levels["volume"],
			Parent:  &vgid,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(vol.ID)},
				{"device", strOrNil(vol.Device)},
				{"device_id", strOrNil(vol.DeviceID)},
				{"size_bytes", intPtrToAny(ParseSize(vol.Size))},
				{"size_label", strOrNil(vol.Size)},
				{"sector_size", strOrNil(vol.SectorSize)},
				{"mirror_state", mstate},
				{"bootable", nil}, // vm-info 쪽 볼륨에는 bootable 이 없다. volume-info 로 보강
				{"attached_to_vm", strOrNil(vm.Name)},
			},
		})
		vn.Reasons = append(vn.Reasons, mreasons...)
		b.registerVolume(vol.Name, vol.ID, volgid)
		b.g.addEdge(vgid, volgid, "attaches", mst,
			kv{"device", strOrNil(vol.Device)}, kv{"device_id", strOrNil(vol.DeviceID)})

		for _, img := range vol.DiskImages {
			igid := gid(b.cid, img.ID)
			en := strings.ToUpper(img.EnableStatus) == "ENABLED"
			ist := "ok"
			if !en {
				ist = "degraded"
			}
			imn := b.g.addNode(igid, nodeInit{
				Type:    "diskimage",
				Label:   ptrOrNil(fmt.Sprintf("%s@%s", vol.Name, img.Node)),
				Status:  ist,
				Level:   levels["diskimage"],
				Parent:  &volgid,
				Lane:    orShared(img.Node),
				Cluster: ptrOrNil(b.cid),
				Meta: omap{
					{"raw_id", strOrNil(img.ID)},
					{"node", strOrNil(img.Node)},
					{"enable_status", strOrNil(img.EnableStatus)},
					{"internal_name", strOrNil(img.Name)},
				},
			})
			if !en {
				imn.Reasons = append(imn.Reasons, "디스크 이미지 DISABLED — 미러 조각 오프라인")
			}
			mst2 := ist
			if en && b.syncing {
				mst2 = "warning"
			}
			syncState := "offline"
			if b.syncing {
				syncState = "syncing"
			} else if en {
				syncState = "in-sync"
			}
			b.g.addEdge(volgid, igid, "mirror", mst2,
				kv{"sync_state", syncState}, kv{"node", strOrNil(img.Node)})
			if hostGID, ok := b.nodeByName[img.Node]; ok {
				b.g.addEdge(igid, hostGID, "resides-on", ist, kv{"span", true})
			}
		}
	}
}

// registerVolume 은 볼륨 id/이름 색인을 삽입 순서와 함께 기록한다.
func (b *clusterBuild) registerVolume(name, rawID, volgid string) {
	b.volGIDByRawID[rawID] = volgid
	if _, ok := b.volNameIndex[name]; !ok {
		b.volNameOrder = append(b.volNameOrder, name)
	}
	b.volNameIndex[name] = append(b.volNameIndex[name], volgid)
}

// buildStandaloneVolumes 는 독립 볼륨(시스템 root/swap/diagdata 등)을 싣고,
// VM 소속 볼륨에는 volume-info 의 bootable/storage-group 을 보강한다.
func (b *clusterBuild) buildStandaloneVolumes() {
	for _, vol := range b.c.Volumes {
		raw := vol.ID
		volgid, exists := b.volGIDByRawID[raw]
		sgGID := ""
		if vol.StorageGroup != nil {
			sgGID = b.sgByRawID[vol.StorageGroup.ID]
		}
		if !exists {
			// VM 에 붙지 않은 시스템 볼륨: 스토리지 그룹 밑에 매단다
			volgid = gid(b.cid, raw)
			parent := b.clusterGID
			if sgGID != "" {
				parent = sgGID
			}
			b.g.addNode(volgid, nodeInit{
				Type:    "volume",
				Label:   ptrOrNil(vol.Name),
				Status:  "ok",
				Level:   levels["volume"],
				Parent:  &parent,
				Cluster: ptrOrNil(b.cid),
				Meta: omap{
					{"raw_id", strOrNil(raw)},
					{"size_bytes", intPtrToAny(ParseSize(vol.Size))},
					{"size_label", strOrNil(vol.Size)},
					{"bootable", boolOrNil(vol.Bootable)},
					{"system_volume", true},
					{"mirror_state", "unknown"},
					{"attached_to_vm", nil},
				},
			})
			b.registerVolume(vol.Name, raw, volgid)
		} else {
			// vm-info 쪽 볼륨에는 bootable/storage-group 이 없다. volume-info 로 보강.
			gv := b.g.get(volgid)
			if v, _ := gv.Meta.get("bootable"); v == nil {
				gv.Meta.set("bootable", boolOrNil(vol.Bootable))
			}
			var sgName any
			if vol.StorageGroup != nil {
				sgName = strOrNil(vol.StorageGroup.Name)
			}
			gv.Meta.set("storage_group", sgName)
		}
		if sgGID != "" {
			b.g.addEdge(volgid, sgGID, "stored-on", "ok", kv{"span", true})
		}
	}
}

// buildImageContainers 는 이미지 컨테이너(level 9, 실사용량)를 만든다.
// 볼륨 연결은 id 참조가 없어 이름 접두 매칭이 유일한 수단이다(조사 계약 참조).
func (b *clusterBuild) buildImageContainers() {
	for _, ic := range b.c.ImageContainers {
		icgid := gid(b.cid, ic.ID)
		sgGID := ""
		if ic.StorageGroup != nil {
			sgGID = b.sgByRawID[ic.StorageGroup.ID]
		}
		sizeB := ParseSize(ic.Size)
		usedB := ParseSize(ic.SizeUsed)
		parent := b.clusterGID
		if sgGID != "" {
			parent = sgGID
		}
		b.g.addNode(icgid, nodeInit{
			Type:    "imagecontainer",
			Label:   ptrOrNil(ic.Name),
			Status:  "ok",
			Level:   levels["imagecontainer"],
			Parent:  &parent,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(ic.ID)},
				{"size_bytes", intPtrToAny(sizeB)},
				{"used_bytes", intPtrToAny(usedB)},
				{"used_pct", floatPtrToAny(Pct(usedB, sizeB))},
				{"is_local", boolOrNil(ic.IsLocal)},
				{"has_filesystem", boolOrNil(ic.HasFilesystem)},
			},
		})
		target := b.matchContainer(ic.Name)
		if target != "" {
			b.g.addEdge(icgid, target, "backs", "ok",
				kv{"evidence", "name-prefix"}, kv{"confidence", 0.6}, kv{"span", true})
		} else if sgGID != "" {
			b.g.addEdge(icgid, sgGID, "stored-on", "ok")
		}
	}
}

// matchContainer 는 volNameIndex 삽입 순서대로 이름 접두 매칭한다.
func (b *clusterBuild) matchContainer(containerName string) string {
	if containerName == "" {
		return ""
	}
	cn := normName(containerName)
	best := ""
	bestLen := 0
	for _, vname := range b.volNameOrder {
		gids := b.volNameIndex[vname]
		if vname == "" || len(gids) != 1 {
			continue // 동명 볼륨이 여러 개면 조인 불가
		}
		vn := normName(vname)
		if vn == "" {
			continue
		}
		if cn == vn || strings.HasPrefix(cn, vn+"_") {
			if len(vn) > bestLen {
				best, bestLen = gids[0], len(vn)
			}
		}
	}
	return best
}
