package topology

import (
	"bytes"
	"encoding/json"
)

// kv 는 omap 의 키-값 한 쌍이다. 엣지의 kind 별 추가 속성을
// 호출 순서대로 넘기기 위해 addEdge 의 가변 인자로도 쓴다.
type kv struct {
	k string
	v any
}

// omap 은 삽입 순서를 보존하는 얇은 JSON 객체다.
//
// Python dict 의 삽입 순서가 곧 json.dumps 의 키 순서였다. 운영에서
// 토폴로지 스냅샷을 diff 로 비교하는 도구가 있으므로, Go 맵(알파벳 정렬
// 출력) 대신 삽입 순서를 유지해 Python 출력과 필드 배열을 맞춘다.
type omap []kv

// MarshalJSON 은 삽입 순서대로 객체를 직렬화한다.
func (o omap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, pair := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(pair.k)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(pair.v)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// set 은 키가 있으면 값을 바꾸고, 없으면 끝에 추가한다 (Python dict 대입과 동일).
func (o *omap) set(k string, v any) {
	for i := range *o {
		if (*o)[i].k == k {
			(*o)[i].v = v
			return
		}
	}
	*o = append(*o, kv{k, v})
}

// get 은 키의 값과 존재 여부를 반환한다. 값이 nil 이어도 키가 있으면 ok=true
// 다 (Python 의 `"bootable": None` 처럼 null 로 채워진 키를 구분해야 한다).
func (o omap) get(k string) (any, bool) {
	for i := range o {
		if o[i].k == k {
			return o[i].v, true
		}
	}
	return nil, false
}

// appendToList 는 리스트 값 키에 항목을 추가한다 (Python setdefault(...).append(...) 용).
func (o *omap) appendToList(k string, v any) {
	if cur, ok := o.get(k); ok {
		if lst, isList := cur.([]any); isList {
			o.set(k, append(lst, v))
			return
		}
	}
	o.set(k, []any{v})
}
