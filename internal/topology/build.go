package topology

// ---------------------------------------------------------------------------
// 토폴로지 빌더 진입점 및 클러스터 통합 빌드 조율
// ---------------------------------------------------------------------------

// clusterBuild 는 클러스터 1대를 그래프에 싣는 동안 쓰는 조회 맵 모음이다.
type clusterBuild struct {
	g             *graph
	c             *ClusterInput
	cid           string
	clusterGID    string
	syncing       bool
	nodeByName    map[string]string
	netByName     map[string]string
	netRoleByName map[string]string
	sgByRawID     map[string]string
	volGIDByRawID map[string]string
	volNameIndex  map[string][]string
	volNameOrder  []string // volNameIndex 삽입 순서 (이름 접두 매칭의 결정성용)
	nicKeys       []nicKeyGID
}

// nicKeyGID 는 (노드이름, 인터페이스이름) -> NIC 그래프 id 매핑 하나다.
// 알림 대상 해석 시 인터페이스 이름으로 첫 번째 NIC 을 찾는 데 쓴다.
type nicKeyGID struct {
	node, ifname, gid string
}

// BuildClusterTopology 는 정규화된 클러스터 1대를 토폴로지 그래프로 만든다.
// roots 는 클러스터 노드 하나다.
func BuildClusterTopology(c ClusterInput) *FullTopology {
	g := newGraph()
	buildClusterInto(g, &c, nil)
	finalizeGraph(g)
	return emitGraph(g, []ClusterInput{c})
}

// BuildFleetTopology 는 여러 클러스터를 fleet 루트 하나로 통합한다.
// ClusterInput.Site 가 있으면 사이트 계층(level 1)이 생성된다.
// fleetLabel 이 빈 문자열이면 "전체 플릿" 을 쓴다.
func BuildFleetTopology(clusters []ClusterInput, fleetLabel string) *FullTopology {
	if fleetLabel == "" {
		fleetLabel = "전체 플릿"
	}
	g := newGraph()
	fleetID := "fleet:root"
	g.addNode(fleetID, nodeInit{
		Type:  "fleet",
		Label: ptrOrNil(fleetLabel),
		Level: levels["fleet"],
		Meta:  omap{{"cluster_count", len(clusters)}},
	})

	siteSeen := map[string]bool{}
	for i := range clusters {
		c := &clusters[i]
		parent := fleetID
		if c.Site != nil {
			sid := c.Site.ID
			if sid == "" {
				sid = "site:default"
			}
			if !siteSeen[sid] {
				label := c.Site.Label
				if label == "" {
					label = sid
				}
				g.addNode(sid, nodeInit{
					Type:   "site",
					Label:  ptrOrNil(label),
					Level:  levels["site"],
					Parent: &fleetID,
					Meta:   omap{},
				})
				g.addEdge(fleetID, sid, "contains", "ok")
				siteSeen[sid] = true
			}
			parent = sid
		}
		buildClusterInto(g, c, &parent)
	}

	finalizeGraph(g)
	return emitGraph(g, clusters)
}

// buildClusterInto 는 정규화 클러스터 1대를 기존 그래프에 이어 붙인다.
// parentID 가 nil 이면 루트로 둔다(단독 빌드).
func buildClusterInto(g *graph, c *ClusterInput, parentID *string) {
	cid := c.ClusterID
	if cid == "" {
		cid = c.Unit.Address
	}
	if cid == "" {
		cid = "cluster"
	}
	platform := c.Platform
	if platform == "" {
		platform = "everrun"
	}
	syncing := c.Unit.Syncing

	b := &clusterBuild{
		g:             g,
		c:             c,
		cid:           cid,
		syncing:       syncing,
		nodeByName:    map[string]string{},
		netByName:     map[string]string{},
		netRoleByName: map[string]string{},
		sgByRawID:     map[string]string{},
		volGIDByRawID: map[string]string{},
		volNameIndex:  map[string][]string{},
	}

	b.buildUnit(parentID, platform)
	b.buildNodes()
	b.buildNetworks()
	b.buildStorageGroups()
	b.buildNICs()
	b.buildVMs()
	b.buildStandaloneVolumes()
	b.buildImageContainers()
	b.applyAlerts()
}

// buildUnit 은 클러스터 노드(level 2)를 만든다 (R4 의 클러스터 측).
func (b *clusterBuild) buildUnit(parentID *string, platform string) {
	c := b.c
	unitRawID := c.Unit.ID
	if unitRawID == "" {
		unitRawID = "supernova:o0"
	}
	b.clusterGID = gid(b.cid, unitRawID)
	totalMem := ParseSize(c.Unit.TotalMemory)
	usedMem := ParseSize(c.Unit.UsedMemory)
	label := c.Unit.Name
	if label == "" {
		label = b.cid
	}
	var license any
	if c.License != nil {
		license = c.License
	}
	b.g.addNode(b.clusterGID, nodeInit{
		Type:    "cluster",
		Label:   ptrOrNil(label),
		Status:  "ok",
		Level:   levels["cluster"],
		Parent:  parentID,
		Cluster: ptrOrNil(b.cid),
		Meta: omap{
			{"cluster_id", b.cid},
			{"platform", platform},
			{"version", strOrNil(c.Unit.Version)},
			{"uuid", strOrNil(c.Unit.UUID)},
			{"address", strOrNil(c.Unit.Address)},
			{"netmask", strOrNil(c.Unit.Netmask)},
			{"configured", boolOrNil(c.Unit.Configured)},
			{"syncing", b.syncing},
			{"total_vcpus", strOrNil(c.Unit.TotalVCPUs)},
			{"used_vcpus", strOrNil(c.Unit.UsedVCPUs)},
			{"total_memory_bytes", intPtrToAny(totalMem)},
			{"used_memory_bytes", intPtrToAny(usedMem)},
			{"memory_used_pct", floatPtrToAny(Pct(usedMem, totalMem))},
			{"license", license},
			{"raw_id", strOrNil(c.Unit.ID)},
		},
	})
	if parentID != nil {
		b.g.addEdge(*parentID, b.clusterGID, "contains", "ok")
	}
	if b.syncing {
		cn := b.g.get(b.clusterGID)
		cn.Reasons = append(cn.Reasons, "유닛 동기화(syncing) 진행 중")
		cn.Status = StatusMax(cn.Status, "warning")
	}
}

// ---------------------------------------------------------------------------
// 알림 오버레이 (R11)
// ---------------------------------------------------------------------------

// applyAlerts 는 알림을 분류해 대상 노드에 누적한다.
// 권위 상태(status)는 건드리지 않고 alert_status 채널에만 쌓는다 (§5.3).
func (b *clusterBuild) applyAlerts() {
	g := b.g
	for _, a := range b.c.Alerts {
		sev, why := ClassifyAlert(a)
		targets := ExtractAlertTargets(a)
		var resolved []string
		for _, t := range targets {
			tgid := ""
			switch t.Type {
			case "node":
				tgid = b.nodeByName[t.Name]
			case "sharednetwork":
				tgid = b.netByName[t.Name]
			case "vm":
				tgid = b.findByLabel("vm", t.Name)
			case "volume":
				if gids := b.volNameIndex[t.Name]; len(gids) > 0 {
					tgid = gids[0]
				}
			case "nic":
				for _, k := range b.nicKeys {
					if k.ifname == t.Name {
						tgid = k.gid
						break
					}
				}
			case "quorum":
				tgid = b.ensureQuorum(t.Name)
			}
			if tgid != "" {
				resolved = append(resolved, tgid)
			}
		}
		targetEvidence := "alert-text"
		if len(targets) == 0 {
			targetEvidence = "cluster-fallback"
		}
		if len(resolved) == 0 {
			resolved = []string{b.clusterGID}
		}

		rec := &AlertRecord{
			ID:             ptrOrNil(a.ID),
			Name:           ptrOrNil(a.Name),
			Description:    ptrOrNil(a.Description),
			Time:           ptrOrNil(a.Time),
			RawSeverity:    ptrOrNil(a.Severity),
			Severity:       sev,
			ClassifiedBy:   why,
			Targets:        resolved,
			TargetEvidence: targetEvidence,
		}
		g.alertRecords = append(g.alertRecords, rec)
		for _, rgid := range resolved {
			gn := g.get(rgid)
			if gn == nil {
				continue
			}
			gn.Alerts = append(gn.Alerts, rec.ID)
			if sev == "critical" || sev == "degraded" || sev == "warning" {
				// 권위 상태(status)는 건드리지 않는다. 알림은 별도 채널에 누적한다.
				gn.AlertStatus = ptrOrNil(StatusMax(derefStr(gn.AlertStatus), sev))
				desc := a.Description
				if desc == "" {
					desc = a.Name
				}
				gn.Reasons = append(gn.Reasons, "알림: "+desc)
			}
		}
	}
}

// findByLabel 은 타입+라벨로 노드 id 를 찾는다 (알림 대상 해석용).
func (b *clusterBuild) findByLabel(typ, label string) string {
	for _, n := range b.g.nodes {
		if n.Type == typ && n.Label != nil && *n.Label == label {
			return n.ID
		}
	}
	return ""
}

// ensureQuorum 은 쿼럼 노드를 없으면 만든다.
// avcli 에 쿼럼 조회 명령이 없어 알림에서만 발견된다.
func (b *clusterBuild) ensureQuorum(addr string) string {
	qgid := gid(b.cid, "quorum:"+addr)
	if !b.g.has(qgid) {
		b.g.addNode(qgid, nodeInit{
			Type:    "quorum",
			Label:   ptrOrNil("쿼럼 " + addr),
			Status:  "unknown",
			Level:   levels["quorum"],
			Parent:  &b.clusterGID,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"address", addr},
				{"source", "alert-text"},
				{"note", "avcli 에 쿼럼 조회 명령이 없어 알림에서만 발견됨"},
			},
		})
		b.g.addEdge(b.clusterGID, qgid, "quorum", "unknown", kv{"evidence", "alert-text"})
	}
	return qgid
}

// ---------------------------------------------------------------------------
// nil 가능 값 헬퍼 (빌더 전용)
// ---------------------------------------------------------------------------

func intPtrToAny(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func floatPtrToAny(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func strPtrToAny(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func stringSliceOrEmpty(s []string) any {
	if s == nil {
		return []string{}
	}
	return s
}

func metricsCPUPct(m *NodeOSMetrics) any {
	if m == nil {
		return nil
	}
	return floatPtrToAny(m.CPUPct)
}

func metricsMemPct(m *NodeOSMetrics) any {
	if m == nil {
		return nil
	}
	return floatPtrToAny(m.MemPct)
}

func metricsUptime(m *NodeOSMetrics) any {
	if m == nil {
		return nil
	}
	return floatPtrToAny(m.UptimeS)
}

func metricsTemps(m *NodeOSMetrics) any {
	if m == nil || m.TempsC == nil {
		return map[string]float64{}
	}
	return m.TempsC
}

func metricsSource(m *NodeOSMetrics) any {
	if m == nil {
		return nil
	}
	return strOrNil(m.Source)
}

func vmALinksToJSON(links []VMALinkInput) any {
	if links == nil {
		return []any{}
	}
	out := make([]any, 0, len(links))
	for _, a := range links {
		out = append(out, omap{
			{"network", strOrNil(a.Network)},
			{"role", strOrNil(a.Role)},
			{"bandwidth", strOrNil(a.Bandwidth)},
		})
	}
	return out
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func orShared(lane string) string {
	if lane == "" {
		return LaneShared
	}
	return lane
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
