package snmp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// TrapStore 는 traps.jsonl 링버퍼 영속 저장소다(파이썬 TrapStore 포팅).
// Add 시 버퍼 전체를 tmp 에 쓰고 rename 으로 원자 교체한다. 트랩은 저빈도
// 이벤트라 매 트랩 재작성(수백 줄) 비용은 무시할 만하고, 재시작 시 마지막
// 트랩들을 뷰에 다시 실을 수 있어 운영 가치가 크다.
type TrapStore struct {
	path string
	ring int
	mu   sync.Mutex
	buf  []Trap // 오래된 것이 앞 (append 순)
}

// NewTrapStore 는 path 를 ring 개 유지하는 JSONL 링 저장소로 연다.
// ring <= 0 이면 500(파이썬 기본값 maxlen=500).
func NewTrapStore(path string, ring int) *TrapStore {
	if ring <= 0 {
		ring = 500
	}
	return &TrapStore{path: path, ring: ring}
}

// Load 는 기존 traps.jsonl 을 읽어 버퍼를 채우고 목록을 돌려준다(재시작 재분배용).
// 깨진 줄은 걸러낸다 — 손상 한 줄이 전체 이력 로드를 막아서는 안 된다.
func (s *TrapStore) Load() []Trap {
	out := []Trap{}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20) // varbind 많은 트랩도 한 줄에 수용
	for sc.Scan() {
		ln := bytes.TrimSpace(sc.Bytes())
		if len(ln) == 0 {
			continue
		}
		var t Trap
		if json.Unmarshal(ln, &t) == nil {
			out = append(out, t)
		}
	}
	if len(out) > s.ring {
		out = out[len(out)-s.ring:]
	}
	s.mu.Lock()
	s.buf = append([]Trap(nil), out...)
	s.mu.Unlock()
	return append([]Trap(nil), out...)
}

// Add 는 트랩 1건을 링에 넣고 파일을 원자적으로 다시 쓴다.
// 쓰기 실패(디스크 풀 등)는 조용히 넘긴다 — 저장 실패가 수신 루프를 죽이면 안 된다.
func (s *TrapStore) Add(t Trap) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, t)
	if len(s.buf) > s.ring {
		s.buf = append([]Trap(nil), s.buf[len(s.buf)-s.ring:]...)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 파이썬 json.dumps(ensure_ascii=False) 와 같은 가독성
	for _, tr := range s.buf {
		if err := enc.Encode(tr); err != nil {
			return
		}
	}
	if d := filepath.Dir(s.path); d != "" && d != "." {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

// Snapshot 은 현재 링 내용을 복사해 돌려준다(오래된 것이 앞). 뷰 병합용.
func (s *TrapStore) Snapshot() []Trap {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Trap(nil), s.buf...)
}
