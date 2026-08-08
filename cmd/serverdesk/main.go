// serverdesk — everRun / ztC Edge 통합 폴리 + 프런트 정적 서버의 단일 바이너리.
//
// Python everrun-poller(poller.py :9890)와 server-monitoring serve.py(:6001)를
// 하나로 대체한다. 수집 코어는 internal/poller, HTTP 표면은 internal/httpapi,
// 정적+콘솔 상태는 internal/webfront 가 담당한다.
package main

import (
	"context"
	"errors"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"net/http"

	"serverdesk/internal/avcli"
	"serverdesk/internal/config"
	"serverdesk/internal/edge"
	"serverdesk/internal/httpapi"
	"serverdesk/internal/poller"
	"serverdesk/internal/sshmetrics"
	"serverdesk/internal/webfront"
	"serverdesk/web"
)

// 로그 레벨(poller.py _LEVELS).
var logLevels = map[string]int{"debug": 10, "info": 20, "warn": 30, "error": 40}

var logLevel = 20

// logMsg 는 poller.py log() 포트다: ISO 타임스탬프 + 레벨 + 클러스터키.
// 어떤 경로로든 비밀이 섞일 수 있는 문자열은 config.Mask 를 반드시 통과시킨다.
func logMsg(level, cluster, msg string) {
	if logLevels[level] < logLevel {
		return
	}
	lv := logLevels[level]
	if lv == 0 {
		lv = 20
	}
	tag := strings.ToUpper(level)
	for len(tag) < 5 {
		tag += " "
	}
	ts := time.Now().Format("2006-01-02T15:04:05-0700")
	if cluster == "" {
		cluster = "-"
	}
	fmt.Fprintf(os.Stderr, "%s [%-5s] [%s] %s\n", ts, tag, cluster, config.Mask(msg))
}

// normalizeLevel 은 패키지마다 다른 레벨 표기("ERROR"/"warn")를 소문자로 통일한다.
func normalizeLevel(lv string) string {
	lv = strings.ToLower(lv)
	if _, ok := logLevels[lv]; !ok {
		return "info"
	}
	return lv
}

func main() {
	var (
		cfgPath      string
		listen       string
		logLevelFlag string
		once         bool
		allowArgv    bool
	)
	flag.StringVar(&cfgPath, "c", "config.json", "설정 JSON 경로")
	flag.StringVar(&cfgPath, "config", "config.json", "설정 JSON 경로")
	flag.StringVar(&listen, "l", "", "바인드 주소 (host:port), 설정보다 우선")
	flag.StringVar(&listen, "listen", "", "바인드 주소 (host:port), 설정보다 우선")
	flag.StringVar(&logLevelFlag, "log-level", "", "로그 레벨(debug/info/warn/error)")
	flag.BoolVar(&once, "once", false, "1회 수집 후 fleet JSON 을 stdout 에 출력하고 종료(진단용)")
	flag.BoolVar(&allowArgv, "allow-argv-exposure", false,
		"avcli 암호가 ps 에 노출되는 환경(/proc hidepid 미적용 + 다른 로그인 계정 존재)에서도 강제로 기동한다")
	flag.Parse()

	// 패키지 로거 연결 — 전부 같은 마스킹 로거로 모은다.
	avcli.Logf = func(level, cluster, msg string) { logMsg(normalizeLevel(level), cluster, msg) }
	poller.Logf = func(level, cluster, msg string) { logMsg(normalizeLevel(level), cluster, msg) }
	httpapi.Logf = func(level, cluster, msg string) { logMsg(normalizeLevel(level), cluster, msg) }
	config.Warnf = func(format string, args ...any) { logMsg("warn", "config", fmt.Sprintf(format, args...)) }

	if _, err := os.Stat(cfgPath); err != nil {
		// 기본값(config.json)이 없을 때 config.local.json 이 있으면 그것으로 폴리 —
		// 배포 패키지 설치본의 파일명이 config.local.json 이라, 아무 인수 없이 실행하면
		// '설정 파일이 없습니다' 로 죽는 첫인상 결함을 막는다.
		if cfgPath == "config.json" {
			if _, err2 := os.Stat("config.local.json"); err2 == nil {
				cfgPath = "config.local.json"
			}
		}
	}
	if _, err := os.Stat(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "설정 파일이 없습니다: %s\n", cfgPath)
		os.Exit(1)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if logLevelFlag == "" {
		logLevelFlag = cfg.LogLevel
	}
	if v, ok := logLevels[strings.ToLower(logLevelFlag)]; ok {
		logLevel = v
	}
	if err := config.CheckPerms(cfgPath); err != nil {
		logMsg("warn", "-", err.Error())
	}
	warn, err := config.CheckArgvExposure(allowArgv)
	if err != nil {
		fmt.Fprintln(os.Stderr, config.Mask(err.Error()))
		os.Exit(1)
	}
	if warn != "" {
		logMsg("warn", "-", warn)
	}

	if listen == "" {
		listen = cfg.Listen
	}
	runtimeDir := poller.ExpandUser(cfg.RuntimeDir)

	sshRunner, err := sshmetrics.NewRunner(runtimeDir, time.Duration(cfg.SSHTimeout)*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SSH 러너 준비 실패: %v\n", err)
		os.Exit(1)
	}

	// 클러스터 상태 + 워커 구성.
	states := make([]*poller.ClusterState, 0, len(cfg.Clusters))
	type workerSet struct {
		av  *poller.AvcliWorker
		osm *poller.OsMetricsWorker
		snm *poller.SnmpWorker
	}
	sets := make([]workerSet, 0, len(cfg.Clusters))
	for i := range cfg.Clusters {
		c := &cfg.Clusters[i]
		st := poller.NewClusterState(c, cfg.Trap.ViewMax)
		cli := avcli.NewClient(c.Key, c.MgmtIP, c.AdminUser, c.AdminPassword)
		cli.Bin = cfg.AvcliBin
		cli.Timeout = time.Duration(cfg.AvcliTimeout) * time.Second
		states = append(states, st)
		sets = append(sets, workerSet{
			av:  poller.NewAvcliWorker(st, cli),
			osm: poller.NewOsMetricsWorker(st, sshRunner),
			snm: poller.NewSnmpWorker(st),
		})
		logMsg("info", c.Key, "클러스터 등록 mgmt="+c.MgmtIP)
	}

	cache := poller.NewFleetCache()

	// --once: 진단 모드. fast+slow+static 을 한 번씩 돌리고 결과를 찍는다.
	if once {
		for i := range cfg.Clusters {
			c := &cfg.Clusters[i]
			st := states[i]
			cli := avcli.NewClient(c.Key, c.MgmtIP, c.AdminUser, c.AdminPassword)
			cli.Bin = cfg.AvcliBin
			cli.Timeout = time.Duration(cfg.AvcliTimeout) * time.Second
			w := poller.NewAvcliWorker(st, cli)
			w.RunTiersOnce()
			osw := poller.NewOsMetricsWorker(st, sshRunner)
			for _, t := range st.NodeTargets() {
				osw.CollectOnce(context.Background(), t)
			}
			time.Sleep(2 * time.Second)
			for _, t := range st.NodeTargets() { // CPU 델타를 위해 2회 수집
				osw.CollectOnce(context.Background(), t)
			}
		}
		cache.Update(states)
		fleet, _, _ := cache.Snapshot()
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(fleet)
		fmt.Println(buf.String())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	start := func(f func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f(ctx)
		}()
	}
	for _, ws := range sets {
		start(ws.av.Start)
		start(ws.osm.Start)
		start(ws.snm.Start)
	}

	// 엣지 디바이스(프린터/NAS/PLC/PVE/서버) — 읽기전용 폴리.
	edgeMgr := poller.NewEdgeManager(ctx, convertEdgeDevices(cfg.EdgeDevices),
		func(level, comp, msg string) { logMsg(normalizeLevel(level), comp, msg) })
	if n := len(edgeMgr.Devices()); n > 0 {
		logMsg("info", "edge", fmt.Sprintf("엣지 디바이스 %d대 등록", n))
	}

	// 실측 가용성 트래커 — FT 클러스터 + 엣지 장비 상태를 60초 샘플링해 영속.
	avail := poller.NewAvailTracker(runtimeDir, func() [][2]string {
		var out [][2]string
		fleet, _, _ := cache.Snapshot()
		if fleet != nil {
			for _, cv := range fleetClusters(fleet) {
				out = append(out, [2]string{mapStr(cv, "key"), poller.DeriveStatus(cv)})
			}
		}
		for _, e := range edgeMgr.Latest() {
			out = append(out, [2]string{mapStr(e, "id"), mapStr(e, "status")})
		}
		return out
	})
	start(avail.Start)
	logMsg("info", "avail", "실측 가용성 트래커 시작 경로="+avail.Path())

	// 이벤트 이력(라이브 로그) — 상태 전이·경보 발생/해제를 10초 diff 로 기록.
	eventLog := poller.NewEventLog(runtimeDir+string(os.PathSeparator)+"events.jsonl", 500)

	// SNMP 트랩 수신기(바인드 실패해도 폴리 본체는 계속 동작한다).
	trapCommunity := ""
	if cfg.Trap.Community != nil {
		trapCommunity = *cfg.Trap.Community
	}
	trapRx := poller.StartTrapReceiver(ctx, cfg.Trap.Enabled, cfg.Trap.Bind, cfg.Trap.Port,
		trapCommunity, cfg.Trap.Persist, cfg.Trap.Ring, poller.ResolveMibDir(cfg.Trap.MibDir),
		runtimeDir, states)

	// 웹 프런트(정적 + 콘솔 공유 상태). StateDir 는 런타임 디렉터리 — 라이브 상태는
	// 배포 전에 ack-state.json 을 여기로 이관한다.
	webSrv := webfront.New(web.FS, webfront.Options{
		StateDir:    runtimeDir,
		AllowWrites: true,
		NotifyHosts: nil,
	})

	// 사용률 임계값 라이브 홀더 — PUT /api/admin/thresholds 가 이 값을 갱신한다.
	poller.SetThresholds(cfg.Thresholds.Warn, cfg.Thresholds.Crit)

	store := config.NewStore(cfg.Path)
	overlay := httpapi.NewDisplayOverlay(cfg)
	apiSrv := httpapi.New(cache, states, cfg, store, eventLog, avail, edgeMgr, webSrv,
		cfg.CORSAllowedOrigins, overlay)

	// 이벤트 워처(표시 라벨은 오버레이를 따른다 — PUT 즉시 반영).
	watcher := poller.NewEventWatcher(eventLog, cache, states, edgeMgr.Latest,
		func(key string) string { return apiSrv.DisplayCfg()[key].Label })
	start(watcher.Start)
	logMsg("info", "events", fmt.Sprintf("이벤트 이력 시작 (복원 %d건)", eventLog.Len()))

	// 캐시 리프레셔: 워커가 갱신한 상태를 주기적으로 뷰로 굽는다.
	refresh := time.Duration(cfg.CacheRefresh) * time.Second
	if refresh <= 0 {
		refresh = 5 * time.Second
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logMsg("error", "-", fmt.Sprintf("캐시 갱신 예외: %v", r))
					}
				}()
				cache.Update(states)
			}()
			select {
			case <-ctx.Done():
			case <-time.After(refresh):
			}
		}
	}()

	// 최상위 mux: /api/* 는 httpapi, 나머지는 webfront(정적 + 콘솔 상태).
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/api" || strings.HasPrefix(p, "/api/") {
			apiSrv.ServeHTTP(w, r)
			return
		}
		webSrv.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: listen, Handler: root}
	webfront.ApplyHardening(srv)

	// 그레이스풀 셧다운: 워커 정지 → avail flush → 트랩 수신기 닫기.
	sigCh := make(chan os.Signal, 2)
	notifyStop(sigCh) // 플랫폼별 시그널 연결은 signal_*.go(Windows 는 Interrupt 만)
	go func() {
		sig := <-sigCh
		logMsg("info", "-", fmt.Sprintf("시그널 %v 수신 — graceful shutdown", sig))
		cancel()
		_ = srv.Shutdown(context.Background())
	}()

	logMsg("info", "-", fmt.Sprintf("HTTP 리스닝 http://%s (fleet/topology/health + webfront)", listen))
	serveErr := srv.ListenAndServe()
	if serveErr != nil && serveErr != http.ErrServerClosed {
		if errors.Is(serveErr, syscall.EADDRINUSE) || strings.Contains(serveErr.Error(), "Only one usage of each socket address") {
			logMsg("error", "-", "HTTP 서버 실패: 포트가 이미 사용 중입니다 — 실행 중인 serverdesk 인스턴스를 확인하세요 (Windows: schtasks /End /TN serverdesk 후 재실행): "+serveErr.Error())
		} else {
			logMsg("error", "-", "HTTP 서버 실패: "+serveErr.Error())
		}
		os.Exit(1)
	}
	cancel()
	// 워커 종료 대기는 짧게 — avcli 콜은 최대 90초 블록될 수 있어 기다리지 않고
	// 끊는다(Python 도 daemon 스레드라 프로세스 종료 시 잘린다).
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	avail.Flush()
	if trapRx != nil {
		trapRx.Receiver.Close()
	}
	logMsg("info", "-", "종료 완료")
}

// convertEdgeDevices 는 config 의 엣지 설정을 edge 패키지 계약으로 옮긴다.
// (두 구조체는 같은 JSON 키를 쓰지만 읽기 전용 뷰와 수집 설정으로 분리돼 있다.)
func convertEdgeDevices(in []config.EdgeDevice) []edge.DeviceConfig {
	out := make([]edge.DeviceConfig, 0, len(in))
	for _, d := range in {
		out = append(out, edge.DeviceConfig{
			Key: d.Key, Kind: d.Kind,
			Name: d.Name, IP: d.IP, Community: d.Community,
			Vendor: d.Vendor, Company: d.Company, Factory: d.Factory,
			Site: d.Site, AssetTag: d.AssetTag, FloorPos: d.FloorPos,
			ExtraIPs:    append([]string{}, d.ExtraIPs...),
			FinsPort:    d.FinsPort,
			FinsSrcNode: d.FinsSrcNode,
			User:        d.User,
			Password:    d.Password,
			BMCIP:       d.BmcIP,
			BMCUser:     d.BmcUser,
			BMCPassword: d.BmcPassword,
		})
	}
	return out
}

// fleetClusters 는 fleet 맵의 clusters[] 를 맵 슬라이스로 꺼낸다.
func fleetClusters(fleet map[string]any) []map[string]any {
	var out []map[string]any
	if l, ok := fleet["clusters"].([]any); ok {
		for _, cv := range l {
			if cm, ok := cv.(map[string]any); ok {
				out = append(out, cm)
			}
		}
	}
	return out
}

func mapStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
