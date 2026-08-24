package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

// 저장 대상 섹션 이름(config JSON 최상위 배열 키).
const (
	SectionClusters    = "clusters"
	SectionEdgeDevices = "edge_devices"
)

// Warnf 는 백업 실패처럼 치명적이지 않은 경고를 호스트 로거에 연결하는 훅이다.
// 기본은 no-op — 폴패키지는 로깅 정책을 강제하지 않는다.
var Warnf = func(format string, args ...any) {}

// syncConfigParent is a fault-injection seam for proving the post-rename
// committed-with-durability-error path. Production uses syncParentDirectory.
var syncConfigParent = syncParentDirectory

// ErrConfigChanged reports that a compare-and-replace observed a newer document.
var ErrConfigChanged = errors.New("config changed concurrently")

// Store 는 config JSON 파일에 대한 원자적 RMW(read-modify-write) 저장소다.
//
// poller.py 의 _persist_display/_persist_add 규약을 그대로 따른다:
//   - 메모리 Config 를 덤프하지 않고 **원본 파일을 다시 읽어** 해당 항목만 고친다
//     (로드 시 채워진 기본값·파생값이 파일에 박제되는 것을 막기 위함).
//   - 문서를 map[string]json.RawMessage 오버레이로 들고 다녀 `_comment` 같은
//     미지의 키가 재작성 후에도 그대로 살아난다.
//   - 전체 RMW 는 하나의 뮤텍스로 직렬화한다.
//   - 예측 불가능한 same-directory temp를 생성 시점부터 0600으로 만들고 fsync한
//     뒤 플랫폼별 atomic replace를 사용한다. 고정 `.tmp` 이름은 사용하지 않는다.
//   - 직전본은 `.bak`에 같은 안전 경계로 남기며 symlink/non-regular 대상은 절대
//     따라가지 않는다.
type Store struct {
	mu            sync.Mutex
	path          string
	credentialDir string
}

// NewStore 는 path 의 config JSON 파일을 대상으로 하는 Store 를 만든다.
func NewStore(path string) *Store {
	return &Store{path: path, credentialDir: managedCredentialDirectory(path)}
}

// NewStoreWithCredentialDirectory is primarily for explicit deployments and
// tests. The directory is daemon-writable and must not be systemd's read-only
// CREDENTIALS_DIRECTORY.
func NewStoreWithCredentialDirectory(path, credentialDir string) *Store {
	credentialDir = strings.TrimSpace(credentialDir)
	if credentialDir != "" {
		credentialDir = filepath.Clean(credentialDir)
	}
	return &Store{path: path, credentialDir: credentialDir}
}

// Path 는 대상 파일 경로를 반환한다.
func (s *Store) Path() string { return s.path }

// CredentialDirectory returns the service-owned credential store paired with
// this config file. It never derives from CREDENTIALS_DIRECTORY.
func (s *Store) CredentialDirectory() string { return s.credentialDir }

const configStoreMaxSize = 16 << 20

func readRegularConfig(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("config must be a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, nil, errors.New("config changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, configStoreMaxSize+1))
	closeErr := file.Close()
	if err != nil {
		return nil, nil, err
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	if len(data) > configStoreMaxSize {
		return nil, nil, errors.New("config exceeds 16 MiB")
	}
	return data, info, nil
}

// writeAtomicFile0600 uses an unpredictable same-directory temporary, verifies
// that neither the destination nor temp is a symlink/non-regular object, fsyncs
// it, and atomically replaces the destination (including on Windows).
// committed is true once replacement occurred even if the parent fsync failed.
func writeAtomicFile0600(dst string, data []byte) (committed bool, err error) {
	dir := filepath.Dir(dst)
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return false, err
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("config directory must be a real directory")
	}
	if info, inspectErr := os.Lstat(dst); inspectErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("replacement target must be a regular non-symlink file")
		}
	} else if !errors.Is(inspectErr, os.ErrNotExist) {
		return false, inspectErr
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(dst)+"-*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return false, err
	}
	if _, err := f.Write(data); err != nil {
		return false, err
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	tmpInfo, err := f.Stat()
	if err != nil {
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	pathInfo, err := os.Lstat(tmp)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(tmpInfo, pathInfo) {
		return false, errors.New("config temporary file changed before replace")
	}
	if err := replaceConfigFile(tmp, dst); err != nil {
		return false, err
	}
	ok = true
	if err := syncConfigParent(dir); err != nil {
		return true, err
	}
	return true, nil
}

func writeFile0600(dst string, data []byte) error {
	committed, err := writeAtomicFile0600(dst, data)
	if committed && err != nil {
		Warnf("파일 교체는 완료됐지만 디렉터리 동기화에 실패했습니다: %v", err)
		return nil
	}
	return err
}

// rmw 는 잠금 하에 원본을 읽어 mutate 를 적용하고 원자 교체한다.
func (s *Store) rmw(mutate func(doc map[string]json.RawMessage) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	orig, _, err := readRegularConfig(s.path)
	if err != nil {
		return fmt.Errorf("config 저장 실패(읽기): %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(orig, &doc); err != nil {
		return fmt.Errorf("config 저장 실패(JSON 파싱): %w", err)
	}
	rollbackDoc := make(map[string]json.RawMessage, len(doc))
	for key, value := range doc {
		rollbackDoc[key] = value
	}
	if err := mutate(doc); err != nil {
		return err
	}
	// require-references 배포에서는 UI/API가 받은 평문 자격증명을 credential
	// provider에 먼저 저장하고 JSON에는 secret:// 참조만 남긴다.
	provisioned, err := protectRequiredRawDocumentAt(doc, s.credentialDir)
	if err != nil {
		removeManagedCredentialNames(s.credentialDir, provisioned.Names)
		return fmt.Errorf("config 저장 실패(자격증명 보호): %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// A serialization/fsync/rename failure must not leave an unreferenced
			// password behind. Only names created in this RMW are removed.
			removeManagedCredentialNames(s.credentialDir, provisioned.Names)
		}
	}()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 한글 등 비ASCII 보존 — Python 의 ensure_ascii=False 에 해당
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("config 저장 실패(직렬화): %w", err)
	}
	// 백업 실패는 저장을 막지 않는다(poller.py 와 동일 판단) — 경고만 남긴다.
	if _, err := writeAtomicFile0600(s.path+".bak", orig); err != nil {
		Warnf("config .bak 백업 실패: %v", err)
	}
	replaced, err := writeAtomicFile0600(s.path, buf.Bytes())
	if replaced {
		committed = true
	}
	if err != nil {
		if replaced {
			// The visible config already changed. Returning an error would leave
			// runtime state on the old generation, and deleting the new managed
			// secret would create a dangling secret:// reference. Continue as a
			// committed update, retain both credential generations for crash
			// fallback, and surface the durability fault through the host logger.
			Warnf("config 교체는 완료됐지만 디렉터리 동기화에 실패했습니다; 이전 credential 세대를 보존합니다: %v", err)
			return nil
		}
		return fmt.Errorf("config 저장 실패(원자 교체): %w", err)
	}
	if err := cleanupManagedCredentialStore(s.credentialDir, doc, rollbackDoc); err != nil {
		Warnf("managed credential 정리 실패: %v", err)
	}
	return nil
}

// ReadDoc 은 현재 파일의 최상위 문서를 읽어 돌려준다(쓰기 없음 — export 용).
func (s *Store) ReadDoc() (map[string]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orig, _, err := readRegularConfig(s.path)
	if err != nil {
		return nil, fmt.Errorf("config 읽기 실패: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(orig, &doc); err != nil {
		return nil, fmt.Errorf("config JSON 파싱 실패: %w", err)
	}
	return doc, nil
}

// ReplaceDoc 은 문서 전체를 교체한다(import 용) — rmw 와 같은 잠금·원자 교체·.bak 경로.
func (s *Store) ReplaceDoc(doc map[string]json.RawMessage) error {
	return s.rmw(func(cur map[string]json.RawMessage) error {
		for k := range cur {
			delete(cur, k)
		}
		for k, v := range doc {
			cur[k] = v
		}
		return nil
	})
}

func rawDocsEqual(left, right map[string]json.RawMessage) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftRaw := range left {
		rightRaw, ok := right[key]
		if !ok {
			return false
		}
		var leftValue, rightValue any
		if json.Unmarshal(leftRaw, &leftValue) != nil || json.Unmarshal(rightRaw, &rightValue) != nil ||
			!reflect.DeepEqual(leftValue, rightValue) {
			return false
		}
	}
	return true
}

// CompareAndReplaceDoc atomically replaces expected with doc or returns ErrConfigChanged.
func (s *Store) CompareAndReplaceDoc(expected, doc map[string]json.RawMessage) error {
	return s.rmw(func(cur map[string]json.RawMessage) error {
		if !rawDocsEqual(cur, expected) {
			return ErrConfigChanged
		}
		for k := range cur {
			delete(cur, k)
		}
		for k, v := range doc {
			cur[k] = v
		}
		return nil
	})
}

func sectionArray(doc map[string]json.RawMessage, section string) ([]json.RawMessage, error) {
	raw, ok := doc[section]
	if !ok {
		return []json.RawMessage{}, nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("config 저장 실패(%s 배열 파싱): %w", section, err)
	}
	return arr, nil
}

func setSectionArray(doc map[string]json.RawMessage, section string, arr []json.RawMessage) error {
	b, err := json.Marshal(arr)
	if err != nil {
		return err
	}
	doc[section] = b
	return nil
}

func entryKey(raw json.RawMessage) string {
	var obj struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(raw, &obj)
	return obj.Key
}

// SetSectionValue 는 최상위 키를 value 로 교체한다(객체/스칼라용 — 배열은 setSectionArray).
func (s *Store) SetSectionValue(section string, value any) error {
	return s.rmw(func(doc map[string]json.RawMessage) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		doc[section] = raw
		return nil
	})
}

// UpdateDisplayMeta 는 section( clusters | edge_devices )에서 key 항목을 찾아
// 표시 필드만 병합 저장한다. 값이 빈 문자열이면 해당 키를 지운다
// (poller.py 의 `v is None or v == "" → pop` 계약). "label" 은 config 키
// "name" 으로 변환된다(프런트 필드명 ↔ config 키명 매핑).
func (s *Store) UpdateDisplayMeta(section, key string, fields map[string]string) error {
	return s.rmw(func(doc map[string]json.RawMessage) error {
		arr, err := sectionArray(doc, section)
		if err != nil {
			return err
		}
		idx := -1
		for i, e := range arr {
			if entryKey(e) == key {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("config 에 항목 없음: %s", key)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(arr[idx], &obj); err != nil {
			return fmt.Errorf("config 저장 실패(항목 파싱): %w", err)
		}
		for k, v := range fields {
			cfgKey := k
			if k == "label" {
				cfgKey = "name"
			}
			v = strings.TrimSpace(v)
			if v == "" {
				delete(obj, cfgKey)
			} else {
				b, err := json.Marshal(v)
				if err != nil {
					return err
				}
				obj[cfgKey] = b
			}
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		arr[idx] = b
		return setSectionArray(doc, section, arr)
	})
}

// AddEntry 는 section 배열에 새 항목을 추가한다. entry 에 "key"(string)가
// 필수이며, 같은 key 가 이미 있으면 거절한다(poller.py 의 409 계약).
func (s *Store) AddEntry(section string, entry map[string]any) error {
	key, _ := entry["key"].(string)
	if key == "" {
		return errors.New("config 추가 실패: entry 에 key(string)가 필요합니다")
	}
	return s.rmw(func(doc map[string]json.RawMessage) error {
		if section != SectionClusters && section != SectionEdgeDevices {
			return fmt.Errorf("config 추가 실패: 지원하지 않는 section %q", section)
		}
		// Device keys are fleet-wide identities. Check both arrays while holding the
		// RMW mutex so concurrent cluster/edge additions cannot create an ambiguous
		// cross-type collision after their API pre-checks both observed "missing".
		for _, candidate := range []string{SectionClusters, SectionEdgeDevices} {
			existing, err := sectionArray(doc, candidate)
			if err != nil {
				return err
			}
			for _, e := range existing {
				if entryKey(e) == key {
					return fmt.Errorf("config 에 이미 존재: %s", key)
				}
			}
		}
		arr, err := sectionArray(doc, section)
		if err != nil {
			return err
		}
		b, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		arr = append(arr, b)
		return setSectionArray(doc, section, arr)
	})
}

// RemoveEdgeDevice 는 edge_devices 에서 key 항목을 제거한다.
// FT 클러스터 제거는 API 계약상 거절된다(config 파일 직접 수정 + 재시작) —
// 그래서 섹션을 인자로 받지 않고 edge_devices 로 고정한다.
func (s *Store) RemoveEdgeDevice(key string) error {
	return s.rmw(func(doc map[string]json.RawMessage) error {
		arr, err := sectionArray(doc, SectionEdgeDevices)
		if err != nil {
			return err
		}
		idx := -1
		for i, e := range arr {
			if entryKey(e) == key {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("config 에 항목 없음: %s", key)
		}
		arr = append(arr[:idx], arr[idx+1:]...)
		return setSectionArray(doc, SectionEdgeDevices, arr)
	})
}
