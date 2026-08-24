package edge

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// ── 삼성 SyncThru 웹(읽기 전용 GET) ────────────────────────────────────────
// 자체서명 프린터는 명시적 SPKI SHA-256 피닝이 설정된 경우에만 연결한다.

var (
	// SyncThru 는 키 미인용 JS 리터럴을 낸다 — 표준 JSON 으로 완화 변환.
	reLenientKey   = regexp.MustCompile(`([{,]\s*)([A-Za-z_][A-Za-z0-9_]*)\s*:`)
	reLenientTrail = regexp.MustCompile(`,\s*([}\]])`)
)

// lenientJSON — 미인용 키에 따옴표를 씌우고 트레일링 콤마를 제거한다.
// Python _sws_get 의 정규식 2단과 동일 규칙.
func lenientJSON(t string) string {
	t = reLenientKey.ReplaceAllString(t, `${1}"${2}":`)
	t = reLenientTrail.ReplaceAllString(t, `$1`)
	return t
}

// swsGet — SyncThru JSON 1건 GET (gzip 수동 해제 포함).
// Go http 클라이언트는 Accept-Encoding 을 수동으로 설정하면 자동 해제를
// 하지 않으므로 매직바이트(1f 8b)를 보고 직접 푼다 — Python 과 같은 방식.
func swsGet(ctx context.Context, cl *http.Client, ip, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ip+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &httpStatusError{Code: resp.StatusCode}
	}
	b, err := readLimitedBody(resp.Body, maxCompressedDeviceResponseBytes)
	if err != nil {
		return nil, err
	}
	if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(strings.NewReader(string(b)))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		if b, err = readLimitedBody(zr, maxDeviceResponseBytes); err != nil {
			return nil, err
		}
	}
	var v any
	if err := json.Unmarshal([]byte(lenientJSON(strings.ToValidUTF8(string(b), "�"))), &v); err != nil {
		return nil, err
	}
	m := jm(v)
	if m == nil {
		return nil, fmt.Errorf("edge: syncthru %s: 최상위가 객체가 아님", path)
	}
	return m, nil
}

// syncThruData — home.json + counters.json 에서 뽑은 표시용 요약.
type syncThruData struct {
	StatusText string
	WebModel   string
	ProductNum string
	HostName   string
	MAC        string
	Location   string
	MonoTotal  int64
	ColorTotal int64
	TonerCnt   map[string]int64
}

// mapSyncThru — 디코딩된 home/counters 객체를 요약으로. 순수 함수(테스트 대상).
// billing 카운터는 심플렉스+듀플렉스 합산이 실제 소비 매수다.
func mapSyncThru(home, cnt map[string]any) syncThruData {
	ident := jm(home["identity"])
	st := jm(home["status"])
	numOr0 := func(v any) int64 {
		n, _ := ji(v)
		return n
	}
	bw := numOr0(cnt["GXI_BILLING_SIMPLEX_BW_TOTAL_CNT"]) +
		numOr0(cnt["GXI_BILLING_DUPLEX_BW_TOTAL_CNT"])
	col := numOr0(cnt["GXI_BILLING_SIMPLEX_COLOR_TOTAL_CNT"]) +
		numOr0(cnt["GXI_BILLING_DUPLEX_COLOR_TOTAL_CNT"])
	tonerCnt := map[string]int64{}
	for _, c := range []string{"black", "cyan", "magenta", "yellow"} {
		if v, ok := ji(jm(home["toner_"+c])["cnt"]); ok {
			tonerCnt[c] = v
		}
	}
	return syncThruData{
		StatusText: strings.Trim(js(st["status1"]), ". "),
		WebModel:   js(ident["model_name"]),
		ProductNum: js(ident["Product_num"]),
		HostName:   js(ident["host_name"]),
		MAC:        js(ident["mac_addr"]),
		Location:   js(ident["location"]),
		MonoTotal:  bw,
		ColorTotal: col,
		TonerCnt:   tonerCnt,
	}
}

// fetchSyncThru — home + counters 모두 성공해야 유효. 실패 시 nil.
// (하나만 되면 표시 일관성이 깨져서 Python 도 통째로 버린다.)
func fetchSyncThru(ctx context.Context, cl *http.Client, ip string) *syncThruData {
	home, err := swsGet(ctx, cl, ip, "/sws/app/information/home/home.json")
	if err != nil {
		return nil
	}
	cnt, err := swsGet(ctx, cl, ip, "/sws/app/information/counters/counters.json")
	if err != nil {
		return nil
	}
	d := mapSyncThru(home, cnt)
	return &d
}
