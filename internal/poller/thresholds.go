package poller

import "sync/atomic"

// 사용률 임계값 라이브 홀더 — PUT /api/admin/thresholds 로 런타임 변경된다.
// config.Config 를 직접 뮤테이션하지 않는다(폴리 워커와 데이터 레이스).
var threshLive atomic.Value // [2]float64{warn, crit}

func init() { threshLive.Store([2]float64{78, 90}) }

// SetThresholds 는 사용률 임계값을 갱신한다(0<warn<crit<=100 검증은 호출자 책임).
func SetThresholds(warn, crit float64) { threshLive.Store([2]float64{warn, crit}) }

// UsageThresholds 는 현재 사용률 임계값(warn, crit %)을 돌려준다.
func UsageThresholds() (float64, float64) {
	v := threshLive.Load().([2]float64)
	return v[0], v[1]
}
