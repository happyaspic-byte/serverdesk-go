// Package webfront 는 Python serve.py(정적 서버 + 콘솔 공유 상태)의 Go 포트다.
//
// 왜 이 패키지가 있는가
// --------------------
// 프런트는 빌드 도구도 CDN 도 없는 순수 정적 ES 모듈이라 정적 서빙이면 충분하다.
// 그런데 경보 확인(ack)·점검 창(maint)·인수인계 메모(notes)·에스컬레이션 클레임(escal)은
// NOC 여러 운영자가 같은 상태를 봐야 하는 '콘솔 상태'다. 실장비 폴러는 읽기 전용 원천이라
// 이 상태를 저장할 API 가 없고(폴러의 확인 해제는 404 실측), 클라이언트 localStorage 에
// 두면 운영자 A 가 확인해도 B 는 못 본다. 그래서 정적 서버가 작은 공유 상태를 JSON 파일로
// 들고 있는다 — 장비 설정이 아니라 콘솔 상태라 /api 쓰기 게이트(AllowWrites)와는
// 의도적으로 분리돼 있다.
//
// /api/* 리버스 프록시는 Go 병합에서 별도 패키지가 인프로세스로 처리하므로 여기서는 다루지
// 않는다. 통합자는 최상위 mux 에서 /api/* 를 다른 핸들러로 보내고 나머지를 이 Server 에
// 넘긴다. /api 쓰기 게이트에는 GateWrite 를 마운트하고, 리스닝 http.Server 에는
// ApplyHardening 을 적용한다.
//
// Linux 전용: 상태 파일의 프로세스 간 직렬화에 flock(syscall.Flock)을 쓴다.
package webfront

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MaxBodyBytes는 인증된 요청이라도 과도한 본문으로 메모리를 소모하지 못하게 하는 상한(1 MiB)이다.
// 이 패키지의 상태 엔드포인트는 각자 더 작은 캡(ack 256KiB, maint/notes/escal 128KiB,
// notify 64KiB)을 쓰므로 여기선 발동하지 않는다 — /api 프록시를 처리하는 패키지가
// 같은 이유로 이 값을 참조하라고 노출한다(Python MAX_BODY_BYTES).
const MaxBodyBytes = 1 << 20

const (
	maxAckBytes   = 256 * 1024 // 확인 키 수천 건이면 충분 — 그 이상은 오용으로 본다.
	maxAckKeys    = 5000
	maxMaintBytes = 128 * 1024
	maxMaintKeys  = 2000 // 장비 수보다 훨씬 크다(장비당 창 1개).
	maxNotesBytes = 128 * 1024
	maxNotesKeys  = 2000
	maxEscalBytes = 128 * 1024
	maxEscalKeys  = 5000
	maxNotifyBody = 64 * 1024

	// 동시 처리 상한. Python 은 64-스레드 세마포어로 연결 플러드 시 스레드 무한 생성을
	// 막았고, cron 판은 상한 초과 연결을 즉시 닫는다(슬롯이 Slowloris 에 점유돼도
	// accept 루프가 멈추지 않게). Go 는 고루틴이 싸지만 무제한 큐잉은 메모리를 먹으므로
	// 슬롯이 없으면 기다리지 않고 503 으로 즉시 거부한다.
	maxConcurrent = 64

	// 정적 파일을 통째로 메모리에 올려 gzip 여부를 판단하는 상한. 이보다 크면
	// gzip 없이 스트리밍한다(일반적인 웹 번들 크기에서는 절대 닿지 않는 안전판).
	maxStaticBuffer = 16 << 20
)

// inlineThemeScriptSHA256 은 web/index.html 의 유일한 인라인 <script>(테마 조기 스탬프,
// FOUC 방지)의 정확한 텍스트에 대한 SHA-256 해시다. CSP script-src 에 이 해시를 넣으면
// 'unsafe-inline' 없이 이 스크립트 하나만 허용할 수 있다 — index.html 을 이 패키지가
// 소유하지 않으므로(다른 담당) nonce 재작성 대신 정적 해시를 택했다. index.html 의 해당
// 스크립트 내용이 바뀌면 이 해시도 같이 바뀌어야 한다(어기면 브라우저 콘솔에 CSP 위반
// 로그가 뜨므로 즉시 드러난다).
const inlineThemeScriptSHA256 = "sha256-GCipB54NxdgUyNZ9JwFUEBaH/9BXyicTQ2/9SihDpDk="

// cspHeader 는 Python CSP_HEADER 와 같은 값이다.
//
// style-src 에 'unsafe-inline' 이 필요한 이유(Python 주석의 실측 정정 그대로):
// 앞선 판단은 "el.style.width = ... 는 CSP 대상이 아니다" 였으나 틀렸다. Chrome 은
// CSSOM 을 통한 style 속성 변경도 style-src 로 강제하며, 완화 없이는 게이지/진행바가
// 그려지지 않았다(js/ 전역 수십 개소가 el.style.* 를 쓴다). XSS 실행 경로인
// script-src 는 여전히 'self' + 해시로 잠겨 있어 위험도가 질적으로 다르다. 인라인
// 스타일 대입을 CSS 변수 + 클래스 토글로 전부 걷어내야 이 완화를 되돌릴 수 있다.
const cspHeader = "default-src 'self'; " +
	"script-src 'self' '" + inlineThemeScriptSHA256 + "'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'"

// 정적 서빙에서 블랙리스트로 막을 디렉터리 세그먼트(루트 기준, 대소문자 그대로 비교).
// tests/·tools/ 는 개발 트리 전용 — 배포 번들에선 빠지지만 저장소에서 직접 서빙할 때도
// /tests/serve.test.mjs 등이 200 으로 노출되던 실측 구멍을 막는다.
var blockedDirSegments = map[string]bool{
	"systemd": true, "legacy-v3": true, "legacy-v4-preskin-css": true,
	"design": true, "__pycache__": true, "tests": true, "tools": true,
}

// mimeByExt 는 Python Handler.extensions_map 에 해당한다. ES 모듈은 MIME 이 틀리면
// 브라우저가 실행을 거부(strict MIME checking)하므로 명시적으로 고정한다.
var mimeByExt = map[string]string{
	".html":  "text/html",
	".htm":   "text/html",
	".js":    "text/javascript",
	".mjs":   "text/javascript",
	".css":   "text/css",
	".json":  "application/json",
	".map":   "application/json",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
	".woff":  "font/woff",
	".ttf":   "font/ttf",
	".txt":   "text/plain",
	".png":   "image/png",
	".gif":   "image/gif",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".ico":   "image/x-icon",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".wasm":  "application/wasm",
}

// Options 는 New 의 설정이다.
type Options struct {
	// StateDir 은 *-state.json 공유 상태 파일을 둘 디렉터리다. 비어 있으면 바이너리가
	// 있는 디렉터리(Python 의 '스크립트 폴더' 기본값에 해당)를 쓰고, 그마저 알 수
	// 없으면 작업 디렉터리다.
	StateDir string

	// AllowWrites는 /api 계열 쓰기 게이트(GateWrite)가 참조한다. 꺼져 있으면
	// 모든 장비 설정 변경을 403으로 거부한다. 콘솔 상태 쓰기는 로그인 미들웨어 인증 후
	// 각 핸들러의 동일 출처 검사를 거친다.
	AllowWrites bool

	// NotifyHosts 는 /notify 웹훅 릴리가 추가로 허용할 대상 호스트다(서브도메인 포함).
	// 기본 허용 hooks.slack.com·discord.com·discordapp.com 과 SERVERDESK_NOTIFY_HOSTS
	// 환경변수(쉼표 구분)에 더해진다. 로그인 이후에도 SSRF 방어는 별도로 유지하므로
	// LAN 주소를 올릴 때는 신중해야 한다.
	NotifyHosts []string
}

// Server 는 정적 자산과 공유 상태 엔드포인트를 한 핸들러로 묶은 http.Handler 다.
// 처리하는 경로는 정적 파일 + /ack·/maint·/notes·/escal·/notify·/notify/test 뿐이고,
// /api/* 는 통합자의 최상위 mux 가 다른 패키지로 라우팅한다.
type Server struct {
	static fs.FS

	allowWrites bool
	notifyHosts []string

	// 동시 처리 상한 세마포어(maxConcurrent 슬롯). ServeHTTP 진입 시 비차단으로 얻고,
	// 없으면 503 — cron 판의 non-blocking 세마포어에 해당한다.
	sem chan struct{}

	ack, maint, notes, escal *stateFile

	notifyClient *http.Client
	notifyLookup func(context.Context, string) ([]net.IPAddr, error)
}

// New 는 static(통합자가 web/ 에 뿌리를 둔 embed.FS 를 넘긴다)과 opts 로 Server 를 만든다.
// static 이 nil 이면 정적 요청은 전부 404 다(상태 엔드포인트는 정상 동작).
func New(static fs.FS, opts Options) *Server {
	dir := opts.StateDir
	if dir == "" {
		dir = defaultStateDir()
	}
	hosts := append([]string{}, defaultNotifyHosts...)
	hosts = append(hosts, splitHosts(os.Getenv("SERVERDESK_NOTIFY_HOSTS"))...)
	for _, h := range opts.NotifyHosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts = append(hosts, h)
		}
	}
	s := &Server{
		static:       static,
		allowWrites:  opts.AllowWrites,
		notifyHosts:  hosts,
		sem:          make(chan struct{}, maxConcurrent),
		notifyLookup: net.DefaultResolver.LookupIPAddr,
	}
	s.notifyClient = &http.Client{
		Timeout: notifyTimeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           s.dialNotifyContext,
			ForceAttemptHTTP2:     true,
			DisableKeepAlives:     true, // resolve and validate on every delivery
			TLSHandshakeTimeout:   notifyTimeout,
			ResponseHeaderTimeout: notifyTimeout,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// 리다이렉트 추종 금지 — 허용된 웹훅 호스트가 302 로 루프백/사설망을
			// 가리키면 /notify 의 호스트 허용 목록이 우회된다. 웹훅 정상 응답은
			// 200/204 라 끊어도 무해하다.
			return http.ErrUseLastResponse
		},
	}
	s.ack = &stateFile{path: filepath.Join(dir, "ack-state.json"), maxBytes: maxAckBytes, maxKeys: maxAckKeys}
	s.maint = &stateFile{path: filepath.Join(dir, "maint-state.json"), maxBytes: maxMaintBytes, maxKeys: maxMaintKeys}
	s.notes = &stateFile{path: filepath.Join(dir, "notes-state.json"), maxBytes: maxNotesBytes, maxKeys: maxNotesKeys}
	s.escal = &stateFile{path: filepath.Join(dir, "escal-state.json"), maxBytes: maxEscalBytes, maxKeys: maxEscalKeys}
	return s
}

// defaultStateDir 은 Python '스크립트 폴더' 기본값에 해당하는 디렉터리를 고른다:
// 실행 바이너리가 있는 디렉터리, 실패 시 작업 디렉터리.
func defaultStateDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// ServeHTTP 는 정적 파일과 공유 상태 엔드포인트만 처리한다. /api/* 는 여기 오지 않는다
// (통합자의 mux 가 먼저 가로챈다).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 동시 처리 상한 — 슬롯이 없으면 즉시 503(cron 판의 non-blocking 세마포어).
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		writeJSONError(w, http.StatusServiceUnavailable, "too many concurrent requests")
		return
	}

	// Python 의 end_headers 에 해당: 정적/상태/오류 응답 모두에 최소 보안 헤더 세트.
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	// 로그아웃 form POST가 실제 Origin을 보내도록 same-origin을 쓴다.
	// no-referrer는 navigate-mode POST의 Origin을 null로 만들어 정상 로그아웃까지 거부한다.
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", cspHeader)

	p := r.URL.Path
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if r.Method == http.MethodGet {
			switch strings.TrimRight(p, "/") {
			case "/ack":
				s.handleStateGet(w, s.ack)
				return
			case "/maint":
				s.handleStateGet(w, s.maint)
				return
			case "/notes":
				s.handleStateGet(w, s.notes)
				return
			case "/escal":
				s.handleEscalGet(w)
				return
			}
		}
		s.serveStatic(w, r)
	case http.MethodPut:
		switch strings.TrimRight(p, "/") {
		case "/ack":
			s.handleDeltaPut(w, r, s.ackEndpoint())
		case "/maint":
			s.handleDeltaPut(w, r, s.maintEndpoint())
		case "/notes":
			s.handleDeltaPut(w, r, s.notesEndpoint())
		case "/escal":
			s.handleEscalPut(w, r)
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	case http.MethodPost:
		switch strings.TrimRight(p, "/") {
		case "/notify", "/notify/test":
			s.handleNotify(w, r)
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	case http.MethodDelete:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	case http.MethodOptions:
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Unsupported method ('"+r.Method+"')", http.StatusNotImplemented)
	}
}

// serveStatic 은 Python SimpleHTTPRequestHandler 의 do_GET/do_HEAD 에 해당한다:
// 블랙리스트 → 디렉터리(index.html 만, 목록 생성 절대 금지) → 파일 서빙.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}

	// 캐시 정책: 폰트·아이콘(svg)만 1시간 캐시하고, css/js 는 매번 재검증한다.
	// 빌드 단계가 없어 파일명에 콘텐츠 해시가 없으므로, css/js 를 max-age 로 묶으면
	// 배포 후 브라우저가 구버전을 보여주는 혼선이 생긴다(실제 보고된 사례). 사낸망이라
	// 재검증(304) 왕복 비용은 무시할 수준이고, css/js 자체는 gzip 으로 수십 KB 다.
	if hasAssetExt(p) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	} else {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	}

	if s.static == nil || isBlockedPath(p) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	name := strings.TrimPrefix(path.Clean(p), "/")
	if name == "" {
		name = "."
	}
	f, err := s.static.Open(name)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if st.IsDir() {
		// 디렉터리 인덱스 생성 금지(Python list_directory 오버라이드) — 레거시 폴더
		// 파일명 열거 노출 차단. index.html 이 있으면 그것만 서빙한다.
		if !strings.HasSuffix(p, "/") {
			loc := p + "/"
			if r.URL.RawQuery != "" {
				loc += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, loc, http.StatusMovedPermanently)
			return
		}
		name = path.Join(name, "index.html")
		f2, err := s.static.Open(name)
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		defer f2.Close()
		st2, err := f2.Stat()
		if err != nil || st2.IsDir() {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		f, st = f2, st2
	}

	ctype := mimeByExt[strings.ToLower(path.Ext(name))]
	if ctype == "" {
		ctype = mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	if !st.ModTime().IsZero() {
		// embed.FS 의 ModTime 은 zero 라 이 헤더는 OS 파일시스템을 쓸 때만 나간다.
		h.Set("Last-Modified", st.ModTime().UTC().Format(http.TimeFormat))
	}

	if st.Size() > maxStaticBuffer {
		// 대형 파일은 gzip 없이 스트리밍한다(메모리 보호).
		h.Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			io.Copy(w, f)
		}
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	body := data
	if len(data) > 1024 {
		// gzip 은 클라이언트가 받아들이고 바디가 1KB 를 넘을 때만.
		h.Set("Vary", "Accept-Encoding")
		if acceptsGzip(r.Header.Get("Accept-Encoding")) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			if _, err := gz.Write(data); err == nil {
				err = gz.Close()
			}
			if err == nil {
				body = buf.Bytes()
				h.Set("Content-Encoding", "gzip")
			}
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

// isBlockedPath 는 정적 파일 요청 경로가 노출 금지 목록에 해당하면 true.
// Python _is_blocked_path 의 그대로의 포트다.
//
// r.URL.Path 는 net/http 가 이미 퍼센트 디코딩한 값이다(Python 의 unquote 에 해당).
// 트레일링 슬래시·.. 우회가 안 통하도록 path.Clean(posixpath.normpath 해당)으로
// 정규화한 뒤 세그먼트 단위로 검사한다.
func isBlockedPath(p string) bool {
	if strings.ContainsRune(p, 0) {
		return true // cron 판 하드닝: NUL 바이트 경로 차단
	}
	norm := path.Clean(p)
	var segs []string
	for _, seg := range strings.Split(norm, "/") {
		if seg != "" && seg != "." {
			segs = append(segs, seg)
		}
	}
	if len(segs) == 0 {
		return false // "/" -> index.html
	}
	for _, seg := range segs {
		// 닷파일/닷디렉터리 전부 차단: .git, .gitignore, .env 등
		if strings.HasPrefix(seg, ".") {
			return true
		}
		if blockedDirSegments[seg] {
			return true
		}
	}
	last := strings.ToLower(segs[len(segs)-1])
	if last == "serve.py" {
		return true
	}
	// *-state.json(+원자적 쓰기의 .tmp, flock 의 .lock)은 공유 상태 파일이다.
	// 이름 열거 대신 접미 규칙으로 둔다 — 새 상태 엔드포인트가 추가돼도 차단 목록
	// 갱신을 잊는 사고가 재발하지 않게. (.lock 차단은 cron 판 하드닝)
	if strings.HasSuffix(last, "-state.json") ||
		strings.HasSuffix(last, "-state.json.tmp") ||
		strings.HasSuffix(last, "-state.json.lock") {
		return true
	}
	if strings.HasSuffix(last, ".md") || strings.HasSuffix(last, ".pyc") ||
		strings.HasSuffix(last, ".sh") { // run-checks.sh / make-dist.sh 등 개발 스크립트
		return true
	}
	if strings.HasSuffix(last, ".log") || strings.Contains(last, ".log.") { // *.log, *.log.*
		return true
	}
	return false
}

// hasAssetExt 는 캐시 정책 판정용 확장자 목록(Python end_headers 의 목록 그대로)이다.
// hasAssetExt 는 장기 캐시 대상(폰트·svg 아이콘)만 고른다. css/js 는 해시 없는 파일명이라
// 여기 넣으면 배포 후 브라우저가 구버전을 1시간 동안 보여준다 — css/js 는 no-cache 경로로.
func hasAssetExt(p string) bool {
	for _, ext := range []string{".woff2", ".svg"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

// acceptsGzip 은 Accept-Encoding 헤더에 gzip(q=0 제외)이 있으면 true.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		qzero := false
		for _, prm := range strings.Split(params, ";") {
			prm = strings.TrimSpace(prm)
			if qv, ok := strings.CutPrefix(prm, "q="); ok {
				if q, err := strconv.ParseFloat(qv, 64); err == nil && q == 0 {
					qzero = true
				}
			}
		}
		if !qzero {
			return true
		}
	}
	return false
}
