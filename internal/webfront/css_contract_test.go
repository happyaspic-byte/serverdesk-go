package webfront

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// findStylesCSSPath는 테스트 실행 위치에 관계없이 web/css/styles.css 경로를 탐색한다.
func findStylesCSSPath(t *testing.T) string {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "..", "web", "css", "styles.css"),
		filepath.Join("web", "css", "styles.css"),
		filepath.Join(".", "web", "css", "styles.css"),
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// 상위 디렉터리 5단계까지 재귀 탐색
	dir, err := os.Getwd()
	if err == nil {
		for i := 0; i < 5; i++ {
			p := filepath.Join(dir, "web", "css", "styles.css")
			if _, err := os.Stat(p); err == nil {
				return p
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	t.Fatalf("styles.css 파일을 찾을 수 없습니다. 탐색 후보: %v", candidates)
	return ""
}

// stripCSSComments는 CSS 주석(/* ... */)을 제거한다.
func stripCSSComments(css string) string {
	re := regexp.MustCompile(`(?s)/\*.*?\*/`)
	return re.ReplaceAllString(css, "")
}

// extractCSSTokens는 CSS 블록 본문 문자열에서 `--변수명: 값;` 쌍을 파싱해 맵으로 반환한다.
func extractCSSTokens(blockBody string) map[string]string {
	tokens := make(map[string]string)
	clean := stripCSSComments(blockBody)

	// 세미콜론 단위로 분리하거나 정규식으로 --name: value 추출
	re := regexp.MustCompile(`(--[a-zA-Z0-9_-]+)\s*:\s*([^;]+);`)
	matches := re.FindAllStringSubmatch(clean, -1)
	for _, m := range matches {
		if len(m) >= 3 {
			key := strings.TrimSpace(m[1])
			val := strings.TrimSpace(m[2])
			// 연속 공백 단일화
			val = regexp.MustCompile(`\s+`).ReplaceAllString(val, " ")
			tokens[key] = val
		}
	}
	return tokens
}

// TestCSSDarkTokensContract는 styles.css 내 두 다크 블록
// (@media (prefers-color-scheme: dark) 및 :root[data-theme="dark"])의
// 공유 토큰 세트 값이 완전 일치하는지 검사한다 (이중 수동 동기화 자동 강제).
func TestCSSDarkTokensContract(t *testing.T) {
	path := findStylesCSSPath(t)
	contentBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("styles.css 읽기 실패 (%s): %v", path, err)
	}
	css := string(contentBytes)

	// 1. @media (prefers-color-scheme: dark) 블록 추출
	mediaRegex := regexp.MustCompile(`(?s)@media\s*\(\s*prefers-color-scheme\s*:\s*dark\s*\)\s*\{([^}]+(?:\{[^}]+\}[^}]*)*)\}`)
	mediaMatch := mediaRegex.FindStringSubmatch(css)
	if len(mediaMatch) < 2 {
		t.Fatalf("@media (prefers-color-scheme: dark) 블록을 styles.css에서 찾을 수 없습니다.")
	}
	mediaDarkBody := mediaMatch[1]

	// 2. :root[data-theme="dark"] 블록 추출
	themeDarkRegex := regexp.MustCompile(`(?s):root\[\s*data-theme\s*=\s*["']?dark["']?\s*\]\s*\{([^}]+)\}`)
	themeDarkMatch := themeDarkRegex.FindStringSubmatch(css)
	if len(themeDarkMatch) < 2 {
		t.Fatalf(":root[data-theme=\"dark\"] 블록을 styles.css에서 찾을 수 없습니다.")
	}
	themeDarkBody := themeDarkMatch[1]

	// 3. 각 블록에서 CSS 변수 토큰 파싱
	mediaTokens := extractCSSTokens(mediaDarkBody)
	themeTokens := extractCSSTokens(themeDarkBody)

	// 4. 파싱된 토큰 개수 최소 검증 (파싱 실패 감지)
	minExpectedTokens := 20
	if len(mediaTokens) < minExpectedTokens {
		t.Errorf("@media dark 블록에서 파싱된 토큰 수가 너무 적습니다 (발견: %d개, 기대: >=%d)", len(mediaTokens), minExpectedTokens)
	}
	if len(themeTokens) < minExpectedTokens {
		t.Errorf(":root[data-theme=dark] 블록에서 파싱된 토큰 수가 너무 적습니다 (발견: %d개, 기대: >=%d)", len(themeTokens), minExpectedTokens)
	}

	// 5. 키 집합 및 값 완전 일치 비교
	allKeys := make(map[string]bool)
	for k := range mediaTokens {
		allKeys[k] = true
	}
	for k := range themeTokens {
		allKeys[k] = true
	}

	var missingInMedia []string
	var missingInTheme []string
	var mismatchedValues []string

	for k := range allKeys {
		vMedia, okMedia := mediaTokens[k]
		vTheme, okTheme := themeTokens[k]

		if !okMedia {
			missingInMedia = append(missingInMedia, k)
			continue
		}
		if !okTheme {
			missingInTheme = append(missingInTheme, k)
			continue
		}
		if vMedia != vTheme {
			mismatchedValues = append(mismatchedValues,
				"토큰 "+k+" 불일치:\n  @media dark:       "+vMedia+"\n  [data-theme=dark]: "+vTheme)
		}
	}

	if len(missingInMedia) > 0 {
		t.Errorf("@media (prefers-color-scheme: dark) 블록에 누락된 토큰: %v", missingInMedia)
	}
	if len(missingInTheme) > 0 {
		t.Errorf(":root[data-theme=\"dark\"] 블록에 누락된 토큰: %v", missingInTheme)
	}
	if len(mismatchedValues) > 0 {
		t.Errorf("두 다크 블록 간 토큰 값 불일치 (%d건):\n%s", len(mismatchedValues), strings.Join(mismatchedValues, "\n"))
	}

	t.Logf("다크 토큰 동기화 검증 성공: 총 %d개 토큰 완전 일치", len(mediaTokens))
}

// TestCSSRequiredDesignTokensContract는 필수 핵심 토큰들이 선언되어 있는지 검사한다.
func TestCSSRequiredDesignTokensContract(t *testing.T) {
	path := findStylesCSSPath(t)
	contentBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("styles.css 읽기 실패 (%s): %v", path, err)
	}
	css := string(contentBytes)

	requiredTokens := []string{
		"--shell",
		"--bg",
		"--surface",
		"--accent",
		"--accent-deep",
		"--ink",
		"--ink2",
		"--muted",
		"--pos",
		"--warn",
		"--neg",
		"--ink-fill",
	}

	for _, token := range requiredTokens {
		if !strings.Contains(css, token+":") {
			t.Errorf("필수 디자인 토큰 누락: %s", token)
		}
	}
}

func TestCSSCommercialAccessibilitySurfaces(t *testing.T) {
	data, err := os.ReadFile(findStylesCSSPath(t))
	if err != nil {
		t.Fatal(err)
	}
	css := string(data)
	for _, required := range []string{
		"--ink-fill:#9A4A28", "--warn:#7A5200", ".confirm-dialog", ".hd-banner-meta",
		".rail-live.is-stale", ".rail-live.is-offline",
		"@media (min-width:1025px) and (max-width:1279px)", "@media (pointer:coarse)",
	} {
		if !strings.Contains(css, required) {
			t.Errorf("commercial accessibility CSS contract missing %q", required)
		}
	}
}
