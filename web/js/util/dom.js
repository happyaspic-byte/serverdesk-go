// js/util/dom.js
// 공용 DOM 헬퍼: 셀렉터, 엘리먼트 생성, data-field 바인딩.
// 특정 화면(screens/*)에 의존하지 않는다. 다른 js/util/*만 참조 가능(현재는 무의존).
// 공개 헬퍼: $(sel,root?), $$(sel,root?), el(tag,attrs,children), clear(node)

export function $(sel, root = document) {
  return root.querySelector(sel);
}

export function $$(sel, root = document) {
  return Array.from(root.querySelectorAll(sel));
}

// 엘리먼트 생성 헬퍼.
// attrs: class/className → node.className, text → textContent,
//        style(object) → Object.assign(node.style,…), onClick 등 on<Event>(함수) → addEventListener,
//        그 외는 setAttribute(true면 빈 값 속성).
// 신뢰 경계가 불분명한 문자열을 주입할 수 있는 html/innerHTML 경로는 제공하지 않는다.
// children: 문자열/숫자/노드 또는 그 배열(중첩 배열 허용).
export function el(tag, attrs = {}, children = []) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class' || k === 'className') node.className = v;
    else if (k === 'text') node.textContent = v;
    else if (k === 'style' && typeof v === 'object') Object.assign(node.style, v);
    else if (/^on[A-Z]/.test(k) && typeof v === 'function') node.addEventListener(k.slice(2).toLowerCase(), v);
    else if (v === true) node.setAttribute(k, '');
    else node.setAttribute(k, v);
  }
  const append = (c) => {
    if (c == null || c === false || c === true) return;
    if (Array.isArray(c)) { c.forEach(append); return; }
    node.appendChild(typeof c === 'string' || typeof c === 'number' ? document.createTextNode(String(c)) : c);
  };
  append(children);
  return node;
}

// node의 자식을 전부 제거한다(리스트 데이터 참조가 바뀌어 통짜 재생성이 필요할 때만 사용).
export function clear(node) {
  if (!node) return;
  while (node.firstChild) node.removeChild(node.firstChild);
}
