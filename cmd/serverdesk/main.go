// serverdesk — everRun / ztC Edge 통합 폴리 + 프런트 정적 서버의 단일 바이너리.
//
// Python everrun-poller(poller.py :9890)와 server-monitoring serve.py(:6001)를
// 하나로 대체한다. 수집 코어는 internal/poller, HTTP 표면은 internal/httpapi,
// 정적+콘솔 상태는 internal/webfront 가 담당한다.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"net/http"

	"serverdesk/internal/alerting"
	"serverdesk/internal/avcli"
	"serverdesk/internal/config"
	demodata "serverdesk/internal/demo"
	"serverdesk/internal/deviceview"
	"serverdesk/internal/edge"
	"serverdesk/internal/httpapi"
	"serverdesk/internal/poller"
	"serverdesk/internal/sshmetrics"
	"serverdesk/internal/webauth"
	"serverdesk/internal/webfront"
	"serverdesk/web"
)

// 로그 레벨(poller.py _LEVELS).
var logLevels = map[string]int{"debug": 10, "info": 20, "warn": 30, "error": 40}

var logLevel = 20

// version is replaced by the release workflow with -ldflags "-X main.version=...".
var version = "dev"

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
		cfgPath         string
		authPath        string
		initAuth        bool
		setAuthPassword bool
		checkAuth       bool
		listen          string
		tlsCert         string
		tlsKey          string
		allowHTTP       bool
		migrateSecrets  string
		setDeviceSecret string
		credentialsDir  string
		logLevelFlag    string
		once            bool
		demoMode        bool
		allowArgv       bool
		showVersion     bool
	)
	flag.StringVar(&cfgPath, "c", "config.json", "설정 JSON 경로")
	flag.StringVar(&cfgPath, "config", "config.json", "설정 JSON 경로")
	flag.StringVar(&listen, "l", "", "바인드 주소 (host:port), 설정보다 우선")
	flag.StringVar(&authPath, "auth", "auth.json", "웹 관리자 인증 JSON 경로")
	flag.BoolVar(&initAuth, "init-auth", false, "웹 관리자 인증을 초기화")
	flag.BoolVar(&setAuthPassword, "set-auth-password", false, "stdin에서 웹 관리자 암호를 설정")
	flag.BoolVar(&checkAuth, "check-auth", false, "웹 관리자 인증 파일을 엄격히 검증")
	flag.StringVar(&listen, "listen", "", "바인드 주소 (host:port), 설정보다 우선")
	flag.StringVar(&tlsCert, "tls-cert", "", "직접 HTTPS 리스너 인증서 PEM 경로 (tls_cert_file보다 우선)")
	flag.StringVar(&tlsKey, "tls-key", "", "직접 HTTPS 리스너 개인키 PEM 경로 (tls_key_file보다 우선)")
	flag.BoolVar(&allowHTTP, "allow-insecure-http", false,
		"비루프백 평문 HTTP 호환 모드를 명시 승인(break-glass, 운영 비권장)")
	flag.StringVar(&migrateSecrets, "migrate-secrets", "",
		"평문 장비 자격증명을 secret:// 참조로 바꾸고 지정 디렉터리에 안전하게 저장한 뒤 종료")
	flag.StringVar(&setDeviceSecret, "set-device-secret", "", "stdin의 장비 credential을 지정 이름으로 생성 후 종료")
	flag.StringVar(&credentialsDir, "credentials-dir", "", "-set-device-secret 대상 디렉터리")
	flag.StringVar(&logLevelFlag, "log-level", "", "로그 레벨(debug/info/warn/error)")
	flag.BoolVar(&once, "once", false, "1회 수집 후 fleet JSON 을 stdout 에 출력하고 종료(진단용)")
	flag.BoolVar(&demoMode, "demo", false, "루프백 전용 읽기 전용 샘플 장비 3대 표시(실장비 수집 없음)")
	flag.BoolVar(&allowArgv, "allow-argv-exposure", false,
		"avcli 암호가 ps 에 노출되는 환경(/proc hidepid 미적용 + 다른 로그인 계정 존재)에서도 강제로 기동한다")
	flag.BoolVar(&showVersion, "version", false, "버전 출력 후 종료")
	flag.Parse()
	if showVersion {
		fmt.Printf("serverdesk %s\n", version)
		return
	}
	authOperations := 0
	for _, enabled := range []bool{initAuth, setAuthPassword, checkAuth, migrateSecrets != "", setDeviceSecret != ""} {
		if enabled {
			authOperations++
		}
	}
	if authOperations > 1 {
		fmt.Fprintln(os.Stderr, "credential maintenance operations are mutually exclusive")
		os.Exit(1)
	}
	if setDeviceSecret != "" {
		secret, err := readPassword(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "장비 credential 입력 실패: %v\n", err)
			os.Exit(1)
		}
		if err := config.StoreCredential(credentialsDir, setDeviceSecret, secret); err != nil {
			fmt.Fprintf(os.Stderr, "장비 credential 저장 실패: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("CREDENTIAL=%s\nREFERENCE=secret://%s\n", setDeviceSecret, setDeviceSecret)
		return
	}
	if migrateSecrets != "" {
		if _, err := os.Stat(cfgPath); err != nil && cfgPath == "config.json" {
			if _, fallbackErr := os.Stat("config.local.json"); fallbackErr == nil {
				cfgPath = "config.local.json"
			}
		}
		result, err := config.MigratePlaintextSecrets(cfgPath, migrateSecrets)
		if err != nil {
			fmt.Fprintf(os.Stderr, "자격증명 마이그레이션 실패: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Migrated %d credentials.\n", result.Count)
		for _, name := range result.Names {
			fmt.Printf("CREDENTIAL=%s\n", name)
		}
		fmt.Printf("Set SERVERDESK_CREDENTIALS_DIRECTORY=%s (or map these names with systemd LoadCredential).\n", migrateSecrets)
		return
	}
	if initAuth {
		password, err := webauth.InitializeCredentials(authPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "인증 초기화 실패: %v\n", err)
			os.Exit(1)
		}
		if _, err := fmt.Printf("ADMIN_USERNAME=admin\nADMIN_PASSWORD=%s\n", password); err != nil {
			removeErr := os.Remove(authPath)
			fmt.Fprintf(os.Stderr, "인증 초기 자격증명 출력 실패: %v\n", err)
			if removeErr != nil {
				fmt.Fprintf(os.Stderr, "불완전한 인증 파일 제거 실패: %v\n", removeErr)
			}
			os.Exit(1)
		}
		return
	}
	if setAuthPassword {
		password, err := readPassword(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "암호 입력 실패: %v\n", err)
			os.Exit(1)
		}
		if err := webauth.SetPassword(authPath, password); err != nil {
			fmt.Fprintf(os.Stderr, "암호 설정 실패: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Administrator password updated.")
		return
	}
	if checkAuth {
		if _, err := webauth.LoadCredentials(authPath); err != nil {
			fmt.Fprintf(os.Stderr, "인증 파일 검증 실패: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Administrator credential store is valid.")
		return
	}
	authSrv, err := webauth.NewFromFile(authPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "인증 파일 로드 실패: %v\n", err)
		os.Exit(1)
	}

	// 패키지 로거 연결 — 전부 같은 마스킹 로거로 모은다.
	avcli.Logf = func(level, cluster, msg string) { logMsg(normalizeLevel(level), cluster, msg) }
	poller.Logf = func(level, cluster, msg string) { logMsg(normalizeLevel(level), cluster, msg) }
	httpapi.Logf = func(level, cluster, msg string) { logMsg(normalizeLevel(level), cluster, msg) }
	config.Warnf = func(format string, args ...any) { logMsg("warn", "config", fmt.Sprintf(format, args...)) }
	auditLog := func(message string) { logMsg("info", "audit", message) }
	authSrv.SetAuditLogger(auditLog)

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
	cfg, err := config.LoadSecure(cfgPath)
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
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !demoMode && needsArgvExposureCheck(cfg) {
		warn, err := config.CheckArgvExposure(allowArgv)
		if err != nil {
			fmt.Fprintln(os.Stderr, config.Mask(err.Error()))
			os.Exit(1)
		}
		if warn != "" {
			logMsg("warn", "-", warn)
		}
	}

	if listen == "" {
		listen = cfg.Listen
	}
	if tlsCert == "" {
		tlsCert = cfg.TLSCertFile
	}
	if tlsKey == "" {
		tlsKey = cfg.TLSKeyFile
	}
	transport := listenerTransport{
		addr: listen, certFile: tlsCert, keyFile: tlsKey,
		allowInsecureHTTP: allowHTTP || cfg.AllowInsecureHTTP,
	}
	if err := validateDemoMode(demoMode, once, cfg, transport); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !once {
		if err := transport.validate(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if !once && !transport.tlsEnabled() && transport.allowInsecureHTTP && !transport.loopback() {
		logMsg("warn", "-", "비루프백 평문 HTTP break-glass 모드입니다; forwarded header를 신뢰하지 않으므로 운영에서는 직접 TLS 또는 루프백 프록시를 사용하십시오")
	}
	runtimeDir := poller.ExpandUser(cfg.RuntimeDir)
	if demoMode {
		runtimeDir = demoRuntimeDir(runtimeDir)
		logMsg("info", "demo", "읽기 전용 샘플 모드 활성화 — 실장비 수집과 외부 알림이 비활성화됩니다")
	}

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
		cli.PrefixArgs = append([]string(nil), cfg.AvcliArgs...)
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
			cli.PrefixArgs = append([]string(nil), cfg.AvcliArgs...)
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
		AllowWrites: !demoMode,
		NotifyHosts: nil,
	})

	// 사용률 임계값 라이브 홀더 — PUT /api/admin/thresholds 가 이 값을 갱신한다.
	poller.SetThresholds(cfg.Thresholds.Warn, cfg.Thresholds.Crit)

	store := config.NewStore(cfg.Path)
	overlay := httpapi.NewDisplayOverlay(cfg)
	apiSrv := httpapi.New(cache, states, cfg, store, eventLog, avail, edgeMgr, webSrv,
		cfg.CORSAllowedOrigins, overlay)
	if demoMode {
		apiSrv.DemoMode = true
		apiSrv.SampleDevices = demodata.Devices
	}

	// Server-resident critical delivery. The source is the same fleet+edge
	// snapshot consumed by the UI, but the engine has its own persisted queue and
	// therefore continues when every browser is closed.
	notifier, err := alerting.New(alerting.Options{
		StateDir: runtimeDir,
		Config:   cfg.Notifications,
		Sender:   webSrv,
		Snapshot: func() ([]alerting.Signal, map[string]bool, bool) {
			fleet, _, _ := cache.Snapshot()
			devices := []map[string]any{}
			if fleet != nil {
				view := deviceview.BuildDevices(fleet, apiSrv.DisplayCfg(), 30)
				if raw, ok := view["devices"].([]any); ok {
					for _, item := range raw {
						if device, ok := item.(map[string]any); ok {
							devices = append(devices, device)
						}
					}
				}
			}
			edgeStatus := edgeMgr.CollectorStatus()
			devices = append(devices, edgeMgr.Latest()...)
			// nil fleet means Cache.Update has not run yet; an empty non-nil
			// fleet is the valid zero-cluster deployment. Configured edge
			// workers likewise need one completed round before reconciliation.
			ftReady, ready := notificationSourceReadiness(fleet, states, edgeStatus)
			edgeReady := edgeNotificationSourceReady(edgeStatus)
			edgeHosts := make(map[string]bool, edgeStatus.Configured)
			for _, device := range edgeMgr.Devices() {
				edgeHosts[device.Key] = true
				ftReady[device.Key] = edgeReady
			}
			ftReady[edgeCollectorConditionHost] = edgeReady
			signals := notificationSignals(devices)
			for i := range signals {
				switch {
				case edgeHosts[signals[i].Host]:
					signals[i].SourceUnready = !edgeReady
				case ftReady[signals[i].Host]:
					// This cluster's current fast snapshot is authoritative.
				default:
					// Unknown and individually unready sources fail closed. The
					// engine still ingests signals from other ready hosts.
					signals[i].SourceUnready = true
				}
			}
			// A current collector error is itself an authoritative positive
			// condition even though that host cannot authorize recoveries until
			// a later successful snapshot. Never label stale device data as down.
			signals = append(signals, notificationCollectorSignals(fleet, states)...)
			signals = append(signals, notificationEdgeCollectorSignals(edgeStatus)...)
			return signals, ftReady, ready
		},
		SilenceWithError: func() (map[string]bool, map[string]bool, error) {
			state, err := webSrv.ExportUIStateWithError()
			if err != nil {
				return nil, nil, err
			}
			acked, maintenance := notificationSilence(state, time.Now())
			return acked, maintenance, nil
		},
		Logf: func(level, component, message string) {
			logMsg(normalizeLevel(level), component, message)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "notification engine initialization failed: %v\n", err)
		os.Exit(1)
	}
	apiSrv.Notifier = notifier
	start(notifier.Start)
	logMsg("info", "notify", fmt.Sprintf("server notification engine started enabled=%t", cfg.Notifications.Enabled))

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
	srv := &http.Server{Addr: listen, Handler: authSrv.Handler(webauth.AuditMutations(root, auditLog))}
	transport.configureServerTLS(srv)
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

	var serveErr error
	if transport.tlsEnabled() {
		logMsg("info", "-", fmt.Sprintf("HTTPS 리스닝 https://%s (fleet/topology/health + webfront)", listen))
		serveErr = srv.ListenAndServeTLS(transport.certFile, transport.keyFile)
	} else {
		logMsg("info", "-", fmt.Sprintf("HTTP 리스닝 http://%s (fleet/topology/health + webfront)", listen))
		serveErr = srv.ListenAndServe()
	}
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

func readPassword(in io.Reader) (string, error) {
	reader := bufio.NewReader(in)
	password := make([]byte, 0, 128)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", errors.New("암호는 줄바꿈으로 끝나야 합니다")
			}
			return "", err
		}
		if b == '\n' {
			break
		}
		if len(password) == 1024 {
			return "", errors.New("암호는 1024바이트 이하여야 합니다")
		}
		password = append(password, b)
	}
	if len(password) > 0 && password[len(password)-1] == '\r' {
		password = password[:len(password)-1]
	}
	for {
		r, _, err := reader.ReadRune()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		if !unicode.IsSpace(r) {
			return "", errors.New("암호 뒤에 허용되지 않는 입력이 있습니다")
		}
	}
	return string(password), nil
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
			ExtraIPs:       append([]string{}, d.ExtraIPs...),
			FinsPort:       d.FinsPort,
			FinsSrcNode:    d.FinsSrcNode,
			User:           d.User,
			Password:       d.Password,
			BMCIP:          d.BmcIP,
			BMCUser:        d.BmcUser,
			BMCPassword:    d.BmcPassword,
			TLSFingerprint: d.TLSFingerprint,
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

func needsArgvExposureCheck(cfg *config.Config) bool {
	return cfg != nil && len(cfg.Clusters) > 0
}

func edgeNotificationSourceReady(status poller.EdgeCollectorStatus) bool {
	return status.Configured == 0 || (!status.LastRoundAt.IsZero() && status.LastError == "")
}

// notificationSourceReadiness returns per-FT-source trust as well as fleet-wide
// readiness. The alert engine uses the former to keep accepting critical
// signals from healthy collectors while the latter conservatively pauses all
// recovery reconciliation whenever any source is uncertain.
func notificationSourceReadiness(fleet map[string]any, states []*poller.ClusterState, edgeStatus poller.EdgeCollectorStatus) (map[string]bool, bool) {
	trusted := make(map[string]bool, len(states))
	if fleet == nil {
		return trusted, false
	}
	views := make(map[string]map[string]any, len(states))
	for _, cluster := range fleetClusters(fleet) {
		if key := mapStr(cluster, "key"); key != "" {
			views[key] = cluster
		}
	}
	allReady := true
	for _, state := range states {
		if state == nil {
			allReady = false
			continue
		}
		view := views[state.Key]
		collection, ok := view["collection"].(map[string]any)
		ready := state.Age("fast") != nil && ok && hasCollectionAge(collection["fast_age_secs"]) && hasSuccessfulFastSnapshot(collection)
		trusted[state.Key] = ready
		if !ready {
			// Mark("fast") can race ahead of the cache refresher. Do not treat
			// that gap as ready: notificationSignals must describe the same
			// post-success snapshot whose readiness we are proving.
			allReady = false
		}
	}
	return trusted, allReady && edgeNotificationSourceReady(edgeStatus)
}

func notificationSourceReady(fleet map[string]any, states []*poller.ClusterState, edgeStatus poller.EdgeCollectorStatus) bool {
	_, ready := notificationSourceReadiness(fleet, states, edgeStatus)
	return ready
}

func notificationCollectorSignals(fleet map[string]any, states []*poller.ClusterState) []alerting.Signal {
	if fleet == nil {
		return nil
	}
	views := make(map[string]map[string]any, len(states))
	for _, cluster := range fleetClusters(fleet) {
		if key := mapStr(cluster, "key"); key != "" {
			views[key] = cluster
		}
	}
	out := make([]alerting.Signal, 0)
	for _, state := range states {
		if state == nil {
			continue
		}
		collection, _ := views[state.Key]["collection"].(map[string]any)
		errorsByTier, _ := collection["errors"].(map[string]any)
		message, _ := errorsByTier["fast"].(string)
		if strings.TrimSpace(message) == "" {
			continue
		}
		out = append(out, alerting.Signal{
			Key: "collector:" + state.Key + ":fast", Host: state.Key,
			Description:        "FT collector unavailable — current AVCLI fast collection failed",
			SuppressEscalation: true,
		})
	}
	return out
}

const edgeCollectorConditionHost = "serverdesk:edge-collector"

func notificationEdgeCollectorSignals(status poller.EdgeCollectorStatus) []alerting.Signal {
	if status.Configured == 0 || strings.TrimSpace(status.LastError) == "" {
		return nil
	}
	return []alerting.Signal{{
		Key: "collector:edge:round", Host: edgeCollectorConditionHost,
		Description:        "Edge collector unavailable — current collection round failed",
		SuppressEscalation: true,
	}}
}

func hasSuccessfulFastSnapshot(collection map[string]any) bool {
	lastSuccess, ok := collection["last_success"].(map[string]any)
	if !ok {
		return false
	}
	if _, ok := lastSuccess["fast"]; !ok {
		return false
	}
	errorsByTier, ok := collection["errors"].(map[string]any)
	if !ok {
		return false
	}
	if raw, exists := errorsByTier["fast"]; exists {
		message, ok := raw.(string)
		return ok && strings.TrimSpace(message) == ""
	}
	return true
}

func hasCollectionAge(value any) bool {
	switch age := value.(type) {
	case *float64:
		return age != nil
	case float64, float32, int, int64, json.Number:
		return true
	default:
		return false
	}
}

func notificationSignals(devices []map[string]any) []alerting.Signal {
	var out []alerting.Signal
	for _, device := range devices {
		host := mapStr(device, "id")
		if host == "" {
			host = mapStr(device, "host")
		}
		if host == "" {
			continue
		}
		meta, _ := device["meta"].(map[string]any)
		if strings.EqualFold(mapStr(device, "status"), "down") {
			description := "Device offline — no node responded to the collector"
			downSince := mapStr(meta, "downSince")
			ackTime := normalizeNotificationAckTime(downSince)
			out = append(out, alerting.Signal{
				Key: "state:" + host + ":down", Host: host,
				Description: description,
				AckKey:      strings.Join([]string{host, "DEVICE_STATE", description, ackTime}, "\x01"),
				StartedAt:   parseNotificationTime(downSince),
			})
		}
		alerts, _ := meta["alerts"].([]any)
		for _, raw := range alerts {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			severity := strings.ToLower(mapStr(item, "sev"))
			if severity == "" {
				severity = strings.ToLower(mapStr(item, "severity"))
			}
			name := mapStr(item, "name")
			if severity != "critical" || name == "DEVICE_STATE" {
				continue
			}
			description := mapStr(item, "desc")
			if description == "" {
				description = mapStr(item, "description")
			}
			if description == "" {
				description = name
			}
			rawTime := mapStr(item, "time")
			ackTime := normalizeNotificationAckTime(rawTime)
			material := strings.Join([]string{host, name, description, ackTime}, "\x00")
			digest := sha256.Sum256([]byte(material))
			out = append(out, alerting.Signal{
				Key: "alert:" + fmt.Sprintf("%x", digest[:16]), Host: host,
				Description: description, AckKey: strings.Join([]string{host, name, description, ackTime}, "\x01"),
				StartedAt: parseNotificationTime(rawTime),
			})
		}
	}
	return out
}

func normalizeNotificationAckTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "no-time"
	}
	value = strings.Replace(value, "T", " ", 1)
	return strings.TrimSuffix(value, "Z")
}

func parseNotificationTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	kst := time.FixedZone("KST", 9*60*60)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, kst); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func notificationSilence(ui map[string]any, now time.Time) (map[string]bool, map[string]bool) {
	acked := map[string]bool{}
	maintenance := map[string]bool{}
	if raw, ok := ui["ack"].(map[string]any); ok {
		for key := range raw {
			acked[key] = true
		}
	}
	if raw, ok := ui["maint"].(map[string]any); ok {
		for host, value := range raw {
			entry, _ := value.(map[string]any)
			until, err := time.Parse(time.RFC3339Nano, mapStr(entry, "until"))
			if err != nil {
				until, err = time.Parse(time.RFC3339, mapStr(entry, "until"))
			}
			if err == nil && until.After(now) {
				maintenance[host] = true
			}
		}
	}
	return acked, maintenance
}
