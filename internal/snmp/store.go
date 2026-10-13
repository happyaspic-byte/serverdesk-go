package snmp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	if err := ValidateTrapPath(s.path); err != nil {
		log.Print("snmp: trap persistence load refused")
		return out
	}
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
			if t.OID == "" {
				var legacy struct {
					OID string `json:"trap_oid"`
				}
				if json.Unmarshal(ln, &legacy) == nil {
					t.OID = legacy.OID
				}
			}
			t.Community = ""
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
// 실패는 비밀 없는 경고로 보고하고 이전 링/파일을 유지한다.
func (s *TrapStore) Add(t Trap) {
	if err := s.add(t); err != nil {
		log.Print("snmp: trap persistence write refused or failed")
	}
}

func (s *TrapStore) add(t Trap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateTrapPath(s.path); err != nil {
		return err
	}
	t.Community = ""
	next := append(append([]Trap(nil), s.buf...), t)
	if len(next) > s.ring {
		next = next[len(next)-s.ring:]
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, tr := range next {
		tr.Community = ""
		if err := enc.Encode(tr); err != nil {
			return err
		}
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := ValidateTrapPath(s.path); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".traps-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := ValidateTrapPath(s.path); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.buf = next
	return nil
}

// ValidateTrapPath rejects symlink components and unsafe existing objects.
// Missing private subdirectories are allowed; Add creates them with 0700.
func ValidateTrapPath(path string) error {
	if path != filepath.Clean(path) {
		return fmt.Errorf("snmp: trap path must be canonical")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	// Walk outward: validate every existing ancestor before creating anything.
	for cur := dir; ; cur = filepath.Dir(cur) {
		info, err := os.Lstat(cur)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("snmp: trap directory must not be a symlink")
			}
			if err := validateTrapDirectory(info, cur == dir); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if cur == filepath.Dir(cur) {
			break
		}
	}
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("snmp: trap path must be a regular file")
	}
	return validateTrapFile(info)
}

// ResolveTrapPath preserves absolute configured locations, but confines relative
// locations to runtimeDir. Both paths still require protected directory checks.
func ResolveTrapPath(runtimeDir, persist string) (string, error) {
	if persist == "" {
		persist = "traps.jsonl"
	}
	if persist != filepath.Clean(persist) {
		return "", fmt.Errorf("snmp: trap path must be canonical")
	}
	if !filepath.IsAbs(persist) {
		if runtimeDir != "" && runtimeDir != filepath.Clean(runtimeDir) {
			return "", fmt.Errorf("snmp: runtime directory must be canonical")
		}
		clean := filepath.Clean(persist)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("snmp: relative trap path escapes runtime directory")
		}
		persist = filepath.Join(runtimeDir, clean)
	}
	if err := ValidateTrapPath(persist); err != nil {
		return "", err
	}
	return persist, nil
}

// Snapshot 은 현재 링 내용을 복사해 돌려준다(오래된 것이 앞). 뷰 병합용.
func (s *TrapStore) Snapshot() []Trap {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Trap(nil), s.buf...)
}
