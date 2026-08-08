package avcli

import (
	"encoding/xml"
	"io"
	"regexp"
	"strings"
)

// ParseError 는 XML 이 비었거나 파싱 불가할 때다(avcli_parse.py 의 AvcliParseError).
//
// 실패 시 avcli 는 stdout 0바이트 + stderr 스택트레이스를 낸다(조사 계약).
// removable-disk-info 는 결과가 없으면 XML 선언조차 없이 0바이트다.
type ParseError struct {
	Msg string
}

func (e *ParseError) Error() string { return e.Msg }

// Element 는 avcli XML 을 담는 범용 트리 노드다.
//
// encoding/xml 의 구조체 태그 매핑 대신 범용 트리를 쓰는 이유:
//   - vm-info 의 a-links 처럼 **자식 태그명이 곧 네트워크 이름**인 동적 스키마가 있다.
//   - avcli 태그 명명이 불일치한다(shared-network 하이픈, hasFileSystem camelCase,
//     MAC/ID 대문자) — 경로 문자열 접근이 Python(ElementTree) 포팅과 1:1 로 대응한다.
type Element struct {
	Tag      string
	Text     string
	Children []*Element
}

// Find 는 path("a/b/c")를 따라가 첫 번째로 일치하는 자식을 반환한다(ElementTree find).
func (e *Element) Find(path string) *Element {
	if e == nil {
		return nil
	}
	cur := e
	for _, seg := range strings.Split(path, "/") {
		var next *Element
		for _, ch := range cur.Children {
			if ch.Tag == seg {
				next = ch
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// FindAll 은 path 의 마지막 구간에 일치하는 모든 자식을 반환한다(ElementTree findall).
func (e *Element) FindAll(path string) []*Element {
	if e == nil {
		return nil
	}
	segs := strings.Split(path, "/")
	parents := []*Element{e}
	for _, seg := range segs[:len(segs)-1] {
		var next []*Element
		for _, p := range parents {
			for _, ch := range p.Children {
				if ch.Tag == seg {
					next = append(next, ch)
				}
			}
		}
		parents = next
	}
	last := segs[len(segs)-1]
	var out []*Element
	for _, p := range parents {
		for _, ch := range p.Children {
			if ch.Tag == last {
				out = append(out, ch)
			}
		}
	}
	return out
}

// findDescendants 는 ElementTree 의 ".//tag" 에 해당 — 전체 하위에서 tag 를 모은다.
// (LED-info 의 node 검색용)
func (e *Element) findDescendants(tag string) []*Element {
	var out []*Element
	var walk func(x *Element)
	walk = func(x *Element) {
		for _, ch := range x.Children {
			if ch.Tag == tag {
				out = append(out, ch)
			}
			walk(ch)
		}
	}
	walk(e)
	return out
}

// ParseXML 은 avcli stdout 문자열을 트리로 파싱한다.
//
// 루트 태그가 <Error> 면 명시 실패로 돌린다 — 버전에 따라 오류가 stdout 으로
// 나올 경우 <Error> 를 정상 응답으로 오인해 "빈 결과"로 조용히 넘어가는 침묵 실패가
// 된다(avcli_parse.py 주석 인용).
func ParseXML(raw string) (*Element, error) {
	s := strings.ToValidUTF8(raw, "�")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "\uFEFF") // BOM/선행 잡음 제거
	if s == "" {
		return nil, &ParseError{Msg: "empty response (0 bytes)"}
	}
	dec := xml.NewDecoder(strings.NewReader(s))
	var root *Element
	var stack []*Element
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, &ParseError{Msg: "XML parse error: " + err.Error()}
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 && root != nil {
				return nil, &ParseError{Msg: "XML parse error: junk after document element"}
			}
			el := &Element{Tag: t.Name.Local}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, el)
			} else {
				root = el
			}
			stack = append(stack, el)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, &ParseError{Msg: "empty response (0 bytes)"}
	}
	if strings.EqualFold(root.Tag, "error") {
		msg := strings.TrimSpace(root.Text)
		if msg == "" {
			msg = "avcli error"
		}
		return nil, &ParseError{Msg: msg}
	}
	return root, nil
}

// --- Element 접근 헬퍼 ------------------------------------------------------
// 빈 태그(<sub-state/>)는 text 가 없으므로 모두 "없음" 으로 읽는다(ElementTree 의
// .text is None 방어와 동일). 값 계열 getter 는 없으면 nil 을 돌려준다 — Python 의
// None 과 같게 JSON null 로 직렬화되도록 포인터를 쓴다.

func getText(e *Element, path string) *string {
	f := e.Find(path)
	if f == nil {
		return nil
	}
	v := strings.TrimSpace(f.Text)
	if v == "" {
		return nil
	}
	return &v
}

// getLower — 상태 열거는 케이스 혼재(state=소문자/enable-status=대문자)라 소문자로 통일.
func getLower(e *Element, path string) *string {
	v := getText(e, path)
	if v == nil {
		return nil
	}
	l := strings.ToLower(*v)
	return &l
}

func getLowerDef(e *Element, path, def string) string {
	if v := getLower(e, path); v != nil {
		return *v
	}
	return def
}

func textStr(e *Element, path string) string {
	if v := getText(e, path); v != nil {
		return *v
	}
	return ""
}

func getInt(e *Element, path string) *int64     { return ParseInt(textStr(e, path)) }
func getFloat(e *Element, path string) *float64 { return ParseFloat(textStr(e, path)) }
func getBool(e *Element, path string) *bool     { return ParseBool(textStr(e, path)) }

// getTexts 는 반복 요소의 텍스트 리스트(빈 값 제외). JSON 이 null 이 아니라
// [] 가 되도록 항상 빈 슬라이스로 시작한다.
func getTexts(e *Element, path string) []string {
	out := []string{}
	for _, f := range e.FindAll(path) {
		if v := strings.TrimSpace(f.Text); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// sizePair 는 (bytes, raw) 쌍 — 바이트로 정규화하되 원문도 *_raw 로 보존한다.
func sizePair(e *Element, path string) (*int64, *string) {
	raw := getText(e, path)
	if raw == nil {
		return nil, nil
	}
	return ParseSize(*raw), raw
}

func boolOr(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func int64Val(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// textKVRe — 텍스트 모드(`-x` 없이) 출력의 `-> key : value` 라인.
var textKVRe = regexp.MustCompile(`^\s*(?:->)?\s*([^:]+?)\s*:\s*(.*)$`)

// ParseTextKV 는 텍스트 모드 출력을 map 으로 바꾼다.
// snmp-info 처럼 XML 생성이 avcli 내부 DOMException 으로 깨지는 명령의 폴백이다.
// 같은 key 가 여러 번 나오면 []string 으로 모은다(poller.py 와 동일).
func ParseTextKV(raw string) map[string]any {
	out := map[string]any{}
	for _, line := range strings.Split(raw, "\n") {
		m := textKVRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		k := strings.TrimSpace(m[1])
		v := strings.TrimSpace(m[2])
		if k == "" {
			continue
		}
		if prev, ok := out[k]; ok {
			switch p := prev.(type) {
			case []string:
				out[k] = append(p, v)
			default:
				out[k] = []string{prev.(string), v}
			}
		} else {
			out[k] = v
		}
	}
	return out
}
