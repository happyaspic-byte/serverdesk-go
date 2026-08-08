package edge

import (
	"context"
	"errors"
	"net/http"
	"time"

	"serverdesk/internal/snmp"
)

// fakeSNMP — 테스트용 SNMP 응답 고정구: ip → oid → 값.
// 등록되지 않은 ip 는 무응답(nil), 등록된 ip 에 없는 oid 는 미응답(키 없음).
type fakeSNMP map[string]map[string]snmp.Value

func (f fakeSNMP) get(_ context.Context, ip string, _ int, _ string, oids []string, _ time.Duration) (map[string]snmp.Value, error) {
	m, ok := f[ip]
	if !ok {
		return nil, nil
	}
	out := map[string]snmp.Value{}
	for _, o := range oids {
		if v, ok := m[o]; ok {
			out[o] = v
		}
	}
	return out, nil
}

func vint(n int64) snmp.Value    { return snmp.Value{Kind: snmp.KindInt, Int: n} }
func vticks(n int64) snmp.Value  { return snmp.Value{Kind: snmp.KindTimeticks, Int: n} }
func vstrv(s string) snmp.Value  { return snmp.Value{Kind: snmp.KindString, Str: s} }
func vbytes(b []byte) snmp.Value { return snmp.Value{Kind: snmp.KindBytes, Bytes: b} }

// errRoundTripper — 네트워크를 즉시 차단하는 Transport(테스트에서 실제 왕복 금지).
type errRoundTripper struct{}

func (errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("edge test: network blocked")
}

func blockedClient() *http.Client {
	return &http.Client{Transport: errRoundTripper{}, Timeout: time.Second}
}

// testWorker — 가짜 SNMP + 차단된 HTTP 로 만든 워커/라운드 환경.
func testWorker(sn fakeSNMP) (*Worker, *pollCtx) {
	w := NewWorker(nil)
	w.SNMPGet = sn.get
	pc := &pollCtx{
		ctx:  context.Background(),
		snmp: sn.get,
		now:  1_700_000_000.0,
		sws:  blockedClient(),
		pve:  blockedClient(),
		rf:   blockedClient(),
	}
	return w, pc
}
