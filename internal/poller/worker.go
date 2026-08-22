package poller

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"serverdesk/internal/avcli"
	"serverdesk/internal/snmp"
	"serverdesk/internal/sshmetrics"
)

// AvcliWorker 는 클러스터 1개의 avcli 티어 스케줄러다(poller.py 의 AvcliWorker).
//
// 주기 설계 근거(poller.py 주석 인용): avcli 는 호출당 15~60초가 걸리고 매 호출이
// audit-info 에 로그인 레코드를 남긴다. 그래서 티어를 3단으로 나눈다.
//
//	fast   : node-info, alert-info              (2콜, 실측 상한 ~80s → 사실상 연속)
//	slow   : unit/vm/network/storage/volume/image-container (+Edge LED)
//	static : license-info (하루 1회)
//
// 티어 직렬화는 avcli.Client 가 클러스터(mgmt IP) 단위 락으로 보장한다.
type AvcliWorker struct {
	st     *ClusterState
	cli    *avcli.Client
	fast   time.Duration
	slow   time.Duration
	static time.Duration
}

// NewAvcliWorker 는 클러스터의 avcli 워커를 만든다.
func NewAvcliWorker(st *ClusterState, cli *avcli.Client) *AvcliWorker {
	iv := st.Cfg.Intervals
	return &AvcliWorker{
		st:     st,
		cli:    cli,
		fast:   time.Duration(iv.Fast) * time.Second,
		slow:   time.Duration(iv.Slow) * time.Second,
		static: time.Duration(iv.Static) * time.Second,
	}
}

// Start 는 ctx 가 끝날 때까지 due-times 우선순위 스케줄 루프를 돌린다.
// 우선순위는 static → slow → fast(느린 티어가 굶지 않도록 먼저 확인).
func (w *AvcliWorker) Start(ctx context.Context) {
	logf("info", w.st.Key, fmt.Sprintf("avcli 워커 시작 fast=%ds slow=%ds static=%ds",
		int(w.fast.Seconds()), int(w.slow.Seconds()), int(w.static.Seconds())))
	now := time.Now()
	// 콜드 스타트 순서: fast(노드/알림)를 먼저 채운다. 가장 중요한 데이터이고,
	// 여기서 결정되는 platform 값이 slow 티어의 LED-info 호출 여부를 가른다.
	due := map[string]time.Time{
		"fast":   {},
		"slow":   now.Add(1 * time.Second),
		"static": now.Add(2 * time.Second),
	}
	for ctx.Err() == nil {
		now = time.Now()
		for _, tier := range []struct {
			name string
			fn   func(context.Context)
			iv   time.Duration
		}{
			{"static", w.tierStatic, w.static},
			{"slow", w.tierSlow, w.slow},
			{"fast", w.tierFast, w.fast},
		} {
			if now.Before(due[tier.name]) {
				continue
			}
			// 반드시 다음 기한을 다시 잡는다(Python try/finally). 이게 없으면 티어
			// 함수가 한 번 패닉한 순간 영구히 '기한 초과'로 남아 1초마다 재실행되고
			// (장비에는 초당 로그인 감사 레코드) 우선순위 때문에 하위 티어가 굶는다.
			func() {
				defer func() {
					if r := recover(); r != nil {
						w.st.Mark(tier.name, fmt.Sprintf("tier crashed: %v", r))
						logf("error", w.st.Key, fmt.Sprintf("avcli 워커 예외(%s): %v\n%s",
							tier.name, r, debug.Stack()))
					}
				}()
				tier.fn(ctx)
			}()
			due[tier.name] = time.Now().Add(tier.iv)
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(1 * time.Second):
		}
	}
	logf("info", w.st.Key, "avcli 워커 종료")
}

// RunTiersOnce 는 fast/slow/static 티어를 순서대로 1회씩 실행한다(--once 진단 모드).
// Python 의 `w.tier_fast(); w.tier_slow(); w.tier_static()` 직렬 호출에 해당한다.
func (w *AvcliWorker) RunTiersOnce() {
	ctx := context.Background()
	w.tierFast(ctx)
	w.tierSlow(ctx)
	w.tierStatic(ctx)
}

// tierFast 는 node-info + alert-info 를 수집한다(poller.py tier_fast).
func (w *AvcliWorker) tierFast(ctx context.Context) {
	ok := true
	errMsg := ""
	root, e1, _ := w.cli.CallXML3(ctx, "node-info")
	if root != nil {
		nodes := avcli.ParseNodeInfo(root)
		// Python `if nodes:` — 빈 파싱 결과로 이전 성공분을 덮지 않는다.
		if len(nodes) > 0 {
			w.st.setNodes(nodes)
			if w.st.GetPlatform() == "" {
				p := avcli.DetectPlatform(nodes)
				w.st.setPlatform(p)
				logf("info", w.st.Key, "플랫폼 판별: "+p)
			}
		}
	} else {
		ok = false
		errMsg = errString(e1)
	}
	root, e2, _ := w.cli.CallXML3(ctx, "alert-info")
	if root != nil {
		w.st.setAlerts(avcli.ParseAlertInfo(root))
	} else {
		ok = false
		if errMsg == "" {
			errMsg = errString(e2)
		}
	}
	if !ok && errMsg == "" {
		errMsg = "partial failure"
	}
	w.st.Mark("fast", errMsg)
}

// tierSlow 는 unit/vm/network/storage/volume/image-container(+Edge LED)를 수집한다
// (poller.py tier_slow). 치명 오류(인증 실패·도달 불가)가 나오면 같은 이유로
// 실패할 나머지 콜을 건너뛰고 조기 종료한다(도달불가 클러스터에서 slow 7콜 = 수 분).
func (w *AvcliWorker) tierSlow(ctx context.Context) {
	ok := true
	lastErr := ""
	aborted := false

	grab := func(cmd string, set func(root *avcli.Element)) {
		if aborted || ctx.Err() != nil {
			return
		}
		root, err, fatal := w.cli.CallXML3(ctx, cmd)
		if root == nil {
			ok = false
			lastErr = errString(err)
			if fatal {
				aborted = true
				logf("warn", w.st.Key, "클러스터 접근 불가 — slow 티어 조기 종료: "+lastErr)
			}
			return
		}
		set(root)
	}

	grab("unit-info", func(r *avcli.Element) { w.st.setUnit(avcli.ParseUnitInfo(r)) })
	grab("vm-info", func(r *avcli.Element) { w.st.setVMs(avcli.ParseVMInfo(r)) })
	grab("network-info", func(r *avcli.Element) { w.st.setNetworks(avcli.ParseNetworkInfo(r)) })

	// storage-info-v2 가 노드별 논리디스크 + 볼륨까지 준다. 실패하면 구버전으로 폭백.
	if !aborted && ctx.Err() == nil {
		root, err, fatal := w.cli.CallXML3(ctx, "storage-info-v2 --disks --volumes")
		if root == nil && !fatal && ctx.Err() == nil {
			root, err, fatal = w.cli.CallXML3(ctx, "storage-info")
		}
		if root != nil {
			w.st.setStorageGroups(avcli.ParseStorageInfo(root))
		} else {
			ok = false
			lastErr = errString(err)
			if fatal {
				aborted = true
				logf("warn", w.st.Key, "클러스터 접근 불가 — slow 티어 조기 종료: "+lastErr)
			}
		}
	}
	grab("volume-info", func(r *avcli.Element) { w.st.setVolumes(avcli.ParseVolumeInfo(r)) })
	grab("image-container-info", func(r *avcli.Element) {
		w.st.setContainers(avcli.ParseImageContainerInfo(r))
	})

	// 이름 접두어 매칭으로 image-container <-> VM 조인.
	func() {
		defer func() {
			if r := recover(); r != nil {
				logf("warn", w.st.Key, fmt.Sprintf("container join 실패: %v", r))
			}
		}()
		w.st.joinImageContainers()
	}()

	// LED-info 는 ztC Edge 에서만. everRun 은 서버측 NPE 로 항상 실패한다.
	if w.st.GetPlatform() == "ztcedge" && !aborted && ctx.Err() == nil {
		if root, _ := w.cli.CallXML(ctx, "LED-info"); root != nil {
			w.st.setLED(avcli.ParseLEDInfo(root))
		}
	}

	if !ok && lastErr == "" {
		lastErr = "partial failure"
	}
	w.st.Mark("slow", lastErr)
}

// tierStatic 은 license-info 를 수집한다(poller.py tier_static).
func (w *AvcliWorker) tierStatic(ctx context.Context) {
	root, err := w.cli.CallXML(ctx, "license-info")
	if root == nil {
		w.st.Mark("static", errString(err))
		return
	}
	w.st.setLicense(avcli.ParseLicenseInfo(root))
	w.st.Mark("static", "")
}

// OsMetricsWorker 는 노드 OS 메트릭(SSH + /proc) 수집기다(poller.py OsMetricsWorker).
// 부분 실패를 허용한다 — 한 노드의 실패가 라운드를 죽이지 않는다.
// SSH ControlMaster 재사용 시 1회 0.01초라 부하가 무시 수준이라 10초 주기다.
type OsMetricsWorker struct {
	st       *ClusterState
	ssh      *sshmetrics.Runner
	interval time.Duration
}

// NewOsMetricsWorker 는 OS 메트릭 워커를 만든다.
func NewOsMetricsWorker(st *ClusterState, ssh *sshmetrics.Runner) *OsMetricsWorker {
	return &OsMetricsWorker{
		st:       st,
		ssh:      ssh,
		interval: time.Duration(st.Cfg.Intervals.OS) * time.Second,
	}
}

// Start 는 ctx 종료까지 interval 주기로 전 대상을 수집한다.
// 단일 노드 행/지연이 타 노드 수집을 지연시키지 않도록 sync.WaitGroup 기반으로
// 병렬 수집하며, 노드당 고루틴 상한은 세마포어(8)로 제한한다.
func (w *OsMetricsWorker) Start(ctx context.Context) {
	logf("info", w.st.Key, fmt.Sprintf("OS 메트릭 워커 시작 interval=%ds", int(w.interval.Seconds())))
	const maxConcurrency = 8
	sem := make(chan struct{}, maxConcurrency)

	for ctx.Err() == nil {
		func() {
			// 라운드 전체를 감싼다 — 대상 조회/개별 수집의 패닉이 워커를 죽이지 못하게.
			defer func() {
				if r := recover(); r != nil {
					logf("error", w.st.Key, fmt.Sprintf("OS 메트릭 라운드 예외: %v\n%s", r, debug.Stack()))
				}
			}()
			targets := w.st.NodeTargets()
			var wg sync.WaitGroup
			for _, tgt := range targets {
				if ctx.Err() != nil {
					break
				}
				wg.Add(1)
				go func(target NodeTarget) {
					defer wg.Done()
					defer func() {
						if r := recover(); r != nil {
							logf("error", w.st.Key, fmt.Sprintf("OS 노드 개별 수집 예외(%s): %v\n%s", target.IP, r, debug.Stack()))
						}
					}()
					select {
					case sem <- struct{}{}:
						defer func() { <-sem }()
					case <-ctx.Done():
						return
					}
					w.collect(ctx, target)
				}(tgt)
			}
			wg.Wait()
		}()
		select {
		case <-ctx.Done():
		case <-time.After(w.interval):
		}
	}
	logf("info", w.st.Key, "OS 메트릭 워커 종료")
}

// CollectOnce 는 대상 1대를 즉시 1회 수집한다(--once 진단 모드용 외부 노출).
func (w *OsMetricsWorker) CollectOnce(ctx context.Context, tgt NodeTarget) {
	w.collect(ctx, tgt)
}

// collect 는 노드 1대를 SSH 로 수집해 nodeOS 에 반영한다.
// 실패 시 파생 메트릭을 버리고 식별 필드만 남긴다(ClusterState.failNodeOS 주석 참조).
func (w *OsMetricsWorker) collect(ctx context.Context, tgt NodeTarget) {
	ip := tgt.IP
	user := tgt.User
	if user == "" {
		user = "root"
	}
	m, err := w.ssh.Collect(ctx, ip, 0, user, tgt.Password)
	if err != nil || m == nil {
		if err != nil {
			logf("debug", w.st.Key, fmt.Sprintf("OS 수집 실패 %s: %v", ip, err))
			if errors.Is(err, sshmetrics.ErrHostKeyChanged) {
				logf("error", w.st.Key, fmt.Sprintf("[SECURITY] OS 수집 호스트 키 불일치 %s: %v", ip, err))
			}
		}
		w.st.failNodeOS(ip, tgt.Name)
		return
	}
	// Metrics 구조체 → Python parse_metrics dict 와 같은 키의 맵.
	mm := toJSONMap(m)
	spine := m.Spine
	delete(mm, "spine") // spine 은 설정이라 nodeOS 와 수명이 다르다(별도 보관)
	mm["ip"] = ip
	mm["name"] = tgt.Name
	mm["reachable"] = true
	mm["source"] = "ssh"
	mm["stale_since"] = nil
	mm["last_ssh_ts"] = mm["ts"]
	w.st.setNodeOS(ip, mm, spine)
	// 히스토리 링 적립(nil cpu_pct — 첫 샘플 — 은 Ring.Push 가 걸러낸다).
	ts, _ := numVal(mm["ts"])
	w.st.RingFor(ip, "cpu").Push(int64(ts), mm["cpu_pct"])
	w.st.RingFor(ip, "mem").Push(int64(ts), mm["mem_pct"])
}

// SnmpWorker 는 생존 확인 폭백이다(poller.py SnmpWorker). SSH 가 죽어도 노드가
// 살아있는지 구분하기 위한 2차 신호. everRun 은 MIB view 제약으로 sysUpTime/sysName
// 만 나오고 ztC Edge 는 CPU/MEM 도 나온다.
type SnmpWorker struct {
	st        *ClusterState
	interval  time.Duration
	community string
}

// NewSnmpWorker 는 SNMP 워커를 만든다.
func NewSnmpWorker(st *ClusterState) *SnmpWorker {
	return &SnmpWorker{
		st:        st,
		interval:  time.Duration(st.Cfg.Intervals.SNMP) * time.Second,
		community: st.Cfg.SNMPCommunity,
	}
}

// Start 는 ctx 종료까지 interval 주기로 전 대상을 SNMP GET 한다.
func (w *SnmpWorker) Start(ctx context.Context) {
	if !w.st.Cfg.SNMPEnabled {
		logf("info", w.st.Key, "SNMP 비활성")
		return
	}
	logf("info", w.st.Key, fmt.Sprintf("SNMP 워커 시작 interval=%ds", int(w.interval.Seconds())))
	for ctx.Err() == nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logf("error", w.st.Key, fmt.Sprintf("SNMP 라운드 예외: %v\n%s", r, debug.Stack()))
				}
			}()
			for _, tgt := range w.st.NodeTargets() {
				if ctx.Err() != nil {
					break
				}
				w.pollOne(ctx, tgt)
			}
		}()
		select {
		case <-ctx.Done():
		case <-time.After(w.interval):
		}
	}
	logf("info", w.st.Key, "SNMP 워커 종료")
}

// pollOne 은 대상 1대를 GET 하고 결과를 nodeOS 에 반영한다.
// SSH 가 살아있는 노드의 주 메트릭은 덮지 않는다(snmpNodeOS 주석 참조).
func (w *SnmpWorker) pollOne(ctx context.Context, tgt NodeTarget) {
	ip := tgt.IP
	if ip == "" {
		return
	}
	res, err := snmp.Get(ctx, ip, 161, w.community, snmp.DefaultOIDs, 3*time.Second)
	info := map[string]any{"reachable": false}
	if err == nil && res != nil {
		info["reachable"] = true
		if v, ok := snmpInt(res[snmp.OIDSysUpTime]); ok {
			info["uptime_secs"] = float64(v / 100) // timeticks(1/100s) → 초
		}
		if v := res[snmp.OIDSysName]; v.Kind == snmp.KindString {
			info["sysname"] = v.Str
		}
		if idle, ok := snmpInt(res[snmp.OIDCPUIdle]); ok {
			info["cpu_pct"] = float64(max(0, min(100, 100-idle)))
		}
		tot, tok := snmpInt(res[snmp.OIDMemTotal])
		av, aok := snmpInt(res[snmp.OIDMemAvail])
		if tok && aok && tot > 0 {
			info["mem_pct"] = round1(float64(tot-av) / float64(tot) * 100)
		}
		if v := res[snmp.OIDLoad1]; v.Kind == snmp.KindString {
			if f := avcli.ParseFloat(v.Str); f != nil {
				info["load1"] = *f
			}
		}
	}
	w.st.snmpNodeOS(ip, tgt.Name, info)
}

// snmpInt 는 SNMP 값을 정수로 읽는다(Python isinstance(v, int) 분기에 해당).
func snmpInt(v snmp.Value) (int64, bool) {
	switch v.Kind {
	case snmp.KindInt, snmp.KindCounter, snmp.KindGauge, snmp.KindTimeticks, snmp.KindCounter64:
		return v.Int, true
	}
	return 0, false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
