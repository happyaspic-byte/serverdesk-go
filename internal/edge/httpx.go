package edge

import (
	"context"
	"crypto/tls"
	"net/http"
	"strconv"
	"time"
)

// httpStatusError — HTTP 4xx/5xx 응답. Python urllib.error.HTTPError 에 해당.
type httpStatusError struct{ Code int }

func (e *httpStatusError) Error() string { return "HTTP status " + strconv.Itoa(e.Code) }

// authFailed — Python 의 `exc.code in (401, 403)` 판정.
func authFailed(err error) bool {
	se, ok := err.(*httpStatusError)
	return ok && (se.Code == 401 || se.Code == 403)
}

// errClass — Python `type(exc).__name__` 에 해당하는 짧은 오류 분류명.
// meta.error 에 들어가 프런트가 그대로 보여주므로 Python 측 이름 관행을 따른다.
func errClass(err error) string {
	switch err.(type) {
	case *httpStatusError:
		return "HTTPError"
	case *jsonSyntaxError:
		return "JSONDecodeError"
	case *valueError:
		return "ValueError"
	}
	return "URLError"
}

// jsonSyntaxError — JSON 디코딩 실패 래퍼.
type jsonSyntaxError struct{ Err error }

func (e *jsonSyntaxError) Error() string { return e.Err.Error() }

// valueError — Python ValueError 에 해당(숫자 변환 실패 등).
type valueError struct{ Msg string }

func (e *valueError) Error() string { return e.Msg }

// insecureClient — 자가서명 장비 전용 HTTPS 클라이언트.
// 폐쇄망 낮부 장비(프린터·PVE·BMC)는 전부 자체서명이라 검증을 생략한다.
// 대신 용도를 이 파일로 한정해 외부망 호출에 재사용되지 않게 한다.
func insecureClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, // 폐쇄망 자체서명 전용
			MaxIdleConnsPerHost: 2,
		},
	}
}

// pollCtx — 한 라운드의 공유 환경.
type pollCtx struct {
	ctx     context.Context
	snmp    SNMPGetFunc
	now     float64 // epoch 초(float) — Python time.time() 과 같은 스케일
	refresh bool    // 이번 라운드에 정적 정보 재조회 여부
	sws     *http.Client
	pve     *http.Client
	rf      *http.Client
}
