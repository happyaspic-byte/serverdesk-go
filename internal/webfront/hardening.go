package webfront

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ApplyHardening 은 통합자의 http.Server 에 slowloris 방지 deadline 을 설정한다.
// Python 은 accept 직후 소켓에 30초 타임아웃을 걸었다(cron 판은 헤더+본문 한 요청의
// 전체 absolute deadline 30초) — 헤더/바디를 무한히 천천히 보내는 연결이 워커를
// 붙잡지 못하게. Go 에서는 핸들러가 아니라 http.Server 에서만 강제할 수 있으므로
// 헬퍼로 둔다. keep-alive 유휴 연결도 30초 뒤 정리된다(Python settimeout 과 같다).
func ApplyHardening(srv *http.Server) {
	srv.ReadHeaderTimeout = 30 * time.Second
	srv.ReadTimeout = 30 * time.Second
	srv.WriteTimeout = 30 * time.Second
	srv.IdleTimeout = 30 * time.Second
	srv.MaxHeaderBytes = 64 << 10
}

// CheckSameOrigin 은 쓰기 요청의 Origin/Referer 가 Host 와 같은지 확인한다
// (CSRF 성격의 LAN 요청 방지). 둘 다 netloc(host:port)만 뽑아 비교한다.
// 헤더가 있는데 netloc 을 못 뽑으면('Origin: null'·'file://'·파싱 불가) 교차출처로
// 간주해 거부한다(fail-close). 헤더가 아예 없으면(curl·서버 간 호출) 통과시킨다.
func CheckSameOrigin(r *http.Request) bool {
	own := strings.ToLower(r.Host)
	for _, hdr := range []string{"Origin", "Referer"} {
		v := r.Header.Get(hdr)
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || u.Host == "" || u.User != nil {
			return false // Python netloc 에는 userinfo 가 포함되므로 userinfo 있으면 불일치
		}
		if strings.ToLower(u.Host) != own {
			return false
		}
	}
	return true
}

// GateWrite는 통합자가 /api 쓰기 mux에 마운트하는 게이트다.
// 로그인 미들웨어가 인증을 끝낸 뒤 호출되며, 여기서는 쓰기 허용 여부와 동일 출처만 확인한다.
func (s *Server) GateWrite(w http.ResponseWriter, r *http.Request) bool {
	if !s.allowWrites {
		writeJSONError(w, http.StatusForbidden,
			"writes are disabled on this server; restart with --allow-writes to enable "+
				"mutating /api requests")
		return false
	}
	if !CheckSameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "cross-origin write rejected")
		return false
	}
	return true
}

// readCappedBody 는 Python _read_capped_body 포트다.
//
// Content-Length 를 모르는 바디(청크 인코딩)는 캡을 적용할 수 없으므로 411 로 명시 거부 —
// 무시하고 넘어가면 keep-alive 소켓의 다음 요청 파싱이 어긋난다. 캡 초과는 413.
// Python 의 '클라이언트가 쓰기를 마칠 때까지 버려가며 읽는' drain 춤(413 응답이 EPIPE 로
// 끊기지 않게)은 Go net/http 가 keep-alive 재사용 전에 잔여 바디를 스스로 버리므로
// 필요 없다. 반환 bool 이 false 면 이미 오류 응답을 보냈으니 호출부는 즉시 리턴한다.
func readCappedBody(w http.ResponseWriter, r *http.Request, cap int64, tooLargeMsg string) ([]byte, bool) {
	if r.ContentLength < 0 {
		writeJSONError(w, http.StatusLengthRequired,
			"Content-Length required (chunked bodies not supported)")
		return nil, false
	}
	if r.ContentLength > cap {
		writeJSONError(w, http.StatusRequestEntityTooLarge, tooLargeMsg)
		return nil, false
	}
	if r.ContentLength == 0 {
		return nil, true // 바디 없음 — Python 의 None 반환에 해당
	}
	// 헤더가 캡 안이라도 실제 바디가 더 올 수 있으므로(거짓 Content-Length) 한 번 더 묶는다.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cap))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, tooLargeMsg)
		} else {
			writeJSONError(w, http.StatusBadRequest, "failed to read request body")
		}
		return nil, false
	}
	return body, true
}

// writeJSON 은 상태 엔드포인트 응답 공통이다: JSON + Cache-Control no-store.
// no-store — 공유 상태는 캐시되면 다른 운영자의 변경이 안 보인다.
func writeJSON(w http.ResponseWriter, status int, v any) {
	data := marshalJSON(v)
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	w.Write(data)
}

// writeJSONError 는 Python _send_json_error 에 해당한다: {"error": msg} + no-store.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// marshalJSON 은 Python json.dumps(ensure_ascii=False)에 맞춰 HTML 이스케이프를 끈다.
// Go 기본 json.Marshal 은 <>& 를 \u003c 등으로 이스케이프해 파일/응답 바이트가 Python 과
// 달라진다 — 같은 파일을 두 구현이 번갈아 써도 내용이 흔들리지 않게 맞춘다.
func marshalJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("null") // map[string]any 만 다루므로 도달 불가
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}
