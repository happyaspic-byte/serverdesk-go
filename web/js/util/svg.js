// js/util/svg.js
// 공용 SVG 차트 헬퍼: 라인/영역 경로, 도넛 게이지(Ring), 스파크라인, 토폴로지 베지어 링크.
// Vigil src/ui/Ring.tsx, Sparkline.tsx의 시각 동작(-90deg 회전, dashoffset 애니 .5s, hover 스냅 툴팁)을
// vanilla DOM으로 이식. 색은 fmt.js 토큰(COLOR 등) 경유 — 하드코딩 원색 금지.
// 시그니처(REBUILD-SPEC.md §5.3): linePts, linePath, areaPath, sparkline, donutPair, bezierLink

import { clamp } from './fmt.js';

const SVG_NS = 'http://www.w3.org/2000/svg';

// 중립 차트색은 CSS 변수로 두 테마를 직접 따른다. Chromium은 presentation attribute 안의
// var()를 무시하므로 아래 값은 반드시 element.style로 CSS 변수를 적용한다(svg-var-attr 계약).
const TRACK = 'var(--track)';
const AREA_FILL = 'rgba(var(--ink-rgb),.08)';
const TIP_SHADOW = 'var(--shadow-sm)';

function svgEl(tag, attrs = {}) {
  const node = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null) continue;
    node.setAttribute(k, v);
  }
  return node;
}

// vals[] → [[x,y], …] 스케일 좌표. NaN/undefined 샘플은 제외(NA 스파크라인 왜곡 방지).
// lo/hi 미지정 시 남은 값들의 min/max로 자동 스케일.
function scalePoints(vals, w, h, pad, lo, hi) {
  const list = Array.isArray(vals) ? vals.filter((v) => Number.isFinite(v)) : [];
  if (!list.length) return [];
  let lo2 = lo, hi2 = hi;
  if (lo2 == null || hi2 == null) {
    const mn = Math.min(...list), mx = Math.max(...list);
    if (lo2 == null) lo2 = mn;
    if (hi2 == null) hi2 = mx;
  }
  if (hi2 === lo2) hi2 = lo2 + 1;
  const n = list.length;
  const innerW = Math.max(1, w - pad * 2);
  const innerH = Math.max(1, h - pad * 2);
  return list.map((v, i) => {
    const x = n === 1 ? pad + innerW / 2 : pad + (i * innerW) / (n - 1);
    const t = clamp((v - lo2) / (hi2 - lo2), 0, 1);
    const y = pad + innerH * (1 - t);
    return [x, y];
  });
}

// 폴리라인 좌표 문자열 "x,y x,y …"
export function linePts(vals, w, h, pad = 2, lo, hi) {
  return scalePoints(vals, w, h, pad, lo, hi)
    .map(([x, y]) => `${x.toFixed(2)},${y.toFixed(2)}`)
    .join(' ');
}

// SVG path d = "M… L…"
export function linePath(vals, w, h, pad = 2, lo, hi) {
  const pts = scalePoints(vals, w, h, pad, lo, hi);
  if (!pts.length) return '';
  return pts.map(([x, y], i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(2)},${y.toFixed(2)}`).join(' ');
}

// 하단을 닫은 면적 path d = "M… L… Z"
export function areaPath(vals, w, h, pad = 2, lo, hi) {
  const pts = scalePoints(vals, w, h, pad, lo, hi);
  if (!pts.length) return '';
  const bottom = (h - pad).toFixed(2);
  const first = pts[0];
  const last = pts[pts.length - 1];
  const mid = pts.map(([x, y]) => `L${x.toFixed(2)},${y.toFixed(2)}`).join(' ');
  return `M${first[0].toFixed(2)},${bottom} ${mid} L${last[0].toFixed(2)},${bottom} Z`;
}

// 툴팁 수평 위치 — translateX(-50%) 중심이 래퍼 폭 밖으로 넘치지 않게 툴팁 반폭만큼
// 안쪽으로 클램프(#176): fx≈0/1 에서 툴팁 절반이 잘려 보이던 문제.
export function clampTipPx(fx, wrapW, tipW) {
  const half = tipW / 2;
  return clamp(fx * wrapW, half, Math.max(half, wrapW - half));
}

// 툴팁 표기 — compute.js subPctText('<1%') 규약과 정합(#175): 0 < v < 1 은 반올림(0%)
// 대신 '<1' + 단위로 읽는다(유휴 ≠ 미수집). 비유한값(NaN)은 '—' — 'NaN%' 오표기 차단.
export function tipText(v, unit = '%') {
  if (!Number.isFinite(v)) return '—';
  if (v > 0 && v < 1) return '<1' + unit;
  return Math.round(v) + unit;
}

// 면적+선 미니 스파크라인. area/line은 areaPath()/linePath()로 미리 계산해 전달하고,
// hist(원본 값 배열)는 hover 스냅 툴팁 계산에만 쓰인다. hist.length<2면 hover 비활성.
// 반환: {el: HTMLElement, attachHover(fn)} — attachHover는 (value, index)를 받는 콜백을 추가 등록한다
// (leave 시 (null,-1) 호출). 화면 모듈이 자체 동기화가 필요할 때 사용, 내부 툴팁은 항상 동작.
export function sparkline({ hist = [], w = 120, h = 30, fill, stroke, area, line, unit = '%' } = {}) {
  const wrap = document.createElement('div');
  wrap.style.position = 'relative';
  wrap.style.flex = '1';
  wrap.style.minWidth = '0';

  const svg = svgEl('svg', {
    width: '100%', height: h, viewBox: `0 0 ${w} ${h}`, preserveAspectRatio: 'none',
  });
  svg.style.display = 'block';

  const areaEl = svgEl('path', { d: area || '' });
  areaEl.style.fill = fill || AREA_FILL;
  // line 인자는 linePath()("M… L…") / linePts()("x,y x,y") 어느 쪽이 와도 그려지도록 형태를 판별한다.
  // (스펙 §5.3은 linePath를 명시하지만 호출부가 혼용하므로 통합 단계에서 양쪽 모두 수용)
  const lineIsPath = /^\s*[Mm]/.test(String(line || ''));
  const lineEl = lineIsPath
    ? svgEl('path', {
      d: line, fill: 'none',
      'stroke-width': 1.5, 'vector-effect': 'non-scaling-stroke', 'stroke-linejoin': 'round',
    })
    : svgEl('polyline', {
      points: line || '', fill: 'none',
      'stroke-width': 1.5, 'vector-effect': 'non-scaling-stroke', 'stroke-linejoin': 'round',
    });
  lineEl.style.stroke = stroke || 'currentColor';
  const cursor = svgEl('line', {
    x1: 0, y1: 0, x2: 0, y2: h, 'stroke-width': 1,
    'stroke-dasharray': '2 2', 'vector-effect': 'non-scaling-stroke',
  });
  cursor.style.stroke = 'var(--muted)';   /* 테마 색은 style로 CSS 변수를 적용한다. */
  cursor.style.display = 'none';

  svg.appendChild(areaEl);
  svg.appendChild(lineEl);
  svg.appendChild(cursor);
  wrap.appendChild(svg);

  const tip = document.createElement('div');
  tip.className = 'u-mono';
  Object.assign(tip.style, {
    position: 'absolute', bottom: 'calc(100% + 5px)', left: '0%', transform: 'translateX(-50%)',
    // 라이트 전용·다크 필 금지 규약: 툴팁도 화이트 서피스 + 잉크 텍스트 + 보더
    background: 'var(--surface)', color: 'var(--ink)', border: '1px solid var(--line)',
    // 콘텐츠 램프 밖 유일 리터럴 10px 였던 툴팁 폰트를 램프 토큰(--fs-11)으로 라우팅(minor).
    fontSize: 'var(--fs-11)', fontWeight: '700',
    padding: '3px 7px', borderRadius: '6px', whiteSpace: 'nowrap', pointerEvents: 'none',
    zIndex: '70', boxShadow: TIP_SHADOW, display: 'none',
  });
  wrap.appendChild(tip);

  const active = Array.isArray(hist) && hist.length >= 2;
  const hoverFns = new Set();

  function onMove(e) {
    const rect = wrap.getBoundingClientRect();
    if (!rect.width) return;
    const fx = clamp((e.clientX - rect.left) / rect.width, 0, 1);
    const i = Math.round(fx * (hist.length - 1));
    const v = Number(hist[i]);
    if (!Number.isFinite(v)) {
      cursor.style.display = 'none';
      tip.style.display = 'none';
      hoverFns.forEach((fn) => { try { fn(null, -1); } catch (err) { /* noop */ } });
      return;
    }
    cursor.style.display = '';
    cursor.setAttribute('x1', (fx * w).toFixed(2));
    cursor.setAttribute('x2', (fx * w).toFixed(2));
    tip.style.display = '';
    tip.style.left = `${(clampTipPx(fx, rect.width, tip.offsetWidth) / rect.width) * 100}%`;
    // 단위는 호출부 지정(기본 '%'). RTT(ms) 등 %가 아닌 지표에서 '20%'로 오표기되던 문제 해결.
    tip.textContent = tipText(v, unit);
    hoverFns.forEach((fn) => { try { fn(v, i); } catch (err) { /* 화면 콜백 오류가 차트를 깨지 않도록 무시 */ } });
  }
  function onLeave() {
    cursor.style.display = 'none';
    tip.style.display = 'none';
    hoverFns.forEach((fn) => { try { fn(null, -1); } catch (err) { /* noop */ } });
  }
  if (active) {
    wrap.addEventListener('mousemove', onMove);
    wrap.addEventListener('mouseleave', onLeave);
  }

  return {
    el: wrap,
    attachHover(fn) {
      if (typeof fn === 'function') hoverFns.add(fn);
      return () => hoverFns.delete(fn);
    },
  };
}

// overview/capacity 공용 vCPU·MEM 2링. resAgg = {vcpuUsed,vcpuTot,vcpuPct,memUsed,memTot,memPct}(compute.js 스키마).
// 게이지 색 단일 규칙(재심사 반영): 도넛도 바와 같은 임계 1종 — 정상 블루 / ≥78 앰버(bright) / ≥90 레드.
// 예전 2-톤(잉크 base + 78 초과분만 앰버, 95 에서 레드) 아크는 '동일 화면 92%=앰버 vs 90%=레드' 모순을
// 만들었다. 이제 값 전체를 단일 색 아크로 칠한다(over 아크는 미사용·숨김 — DOM 구조는 호환 유지).
// donutPair(최초 생성)와 capacity.patchGauge(틱 갱신)가 동일 계산을 공유하도록 여기서 한 번만 정의한다.
export function allocArcParams(pct, c) {
  const p = clamp(pct, 0, 100);
  return {
    baseOffset: c * (1 - p / 100),                          // 단일 아크: 0→값
    baseStroke: p >= 90 ? 'var(--neg)' : (p >= 78 ? 'var(--warn-bright)' : 'var(--accent)'),
    overShow: false,
    overDash: 0,
    overGap: c,
    overOffset: c,
    overStroke: 'var(--warn-bright)',
  };
}

export function donutPair(resAgg = {}, opts = {}) {
  const vcpuPct = clamp(Number(resAgg.vcpuPct) || 0, 0, 100);
  const memPct = clamp(Number(resAgg.memPct) || 0, 0, 100);
  const size = 76, stroke = 8, gap = 22;
  const w = size * 2 + gap;
  const h = size + 18;

  const svg = svgEl('svg', {
    width: w, height: h, viewBox: `0 0 ${w} ${h}`,
    role: 'img', 'aria-label': opts.ariaLabel || 'vCPU and memory allocation chart',
  });
  svg.style.display = 'block';

  const cy = size / 2;
  const r = (size - stroke) / 2;
  const c = 2 * Math.PI * r;
  const items = [
    { pct: vcpuPct, label: 'vCPU', cx: size / 2 },
    { pct: memPct, label: 'MEM', cx: size + gap + size / 2 },
  ];

  items.forEach(({ pct, label, cx }) => {
    const arc = allocArcParams(pct, c);
    const rot = svgEl('g', { transform: `rotate(-90 ${cx} ${cy})` });
    const track = svgEl('circle', { cx, cy, r, fill: 'none', 'stroke-width': stroke });
    track.style.stroke = TRACK;
    rot.appendChild(track);
    // 단일 아크 — 0→값, 색은 임계 규칙(allocArcParams.baseStroke)
    const base = svgEl('circle', {
      cx, cy, r, fill: 'none', 'stroke-width': stroke,
      'stroke-linecap': 'round', 'stroke-dasharray': c.toFixed(2), 'stroke-dashoffset': arc.baseOffset.toFixed(2),
    });
    base.style.stroke = arc.baseStroke;   /* baseStroke는 테마 CSS 변수일 수 있다. */
    base.style.transition = 'stroke-dashoffset var(--dur) var(--ease)';
    rot.appendChild(base);
    // overflow(앰버/neg) 아크 — 78→값(초과분만). 값≤78 이면 숨김.
    const over = svgEl('circle', {
      cx, cy, r, fill: 'none', 'stroke-width': stroke, 'stroke-linecap': 'round',
      'stroke-dasharray': arc.overDash.toFixed(2) + ' ' + arc.overGap.toFixed(2),
      'stroke-dashoffset': arc.overOffset.toFixed(2),
    });
    over.style.stroke = arc.overStroke;
    over.style.display = arc.overShow ? '' : 'none';
    rot.appendChild(over);
    svg.appendChild(rot);

    // 도넛 중심 % — 히어로 숫자 '보조 대형' 티어 단일 규칙 20px/700 로 통일(§7 웨이트 상한 700 — 800/900 금지, M1).
    const text = svgEl('text', {
      x: cx, y: cy, 'text-anchor': 'middle', 'dominant-baseline': 'central',
      'font-size': 20, 'font-weight': 700,
    });
    text.style.fill = 'var(--ink)';   /* 테마 색은 style로 CSS 변수를 적용한다. */
    text.textContent = `${Math.round(pct)}%`;
    svg.appendChild(text);

    const labelEl = svgEl('text', {
      x: cx, y: h - 4, 'text-anchor': 'middle', 'font-size': 11, 'font-weight': 600,
    });
    labelEl.style.fill = 'var(--muted)';   /* 테마 색은 style로 CSS 변수를 적용한다. */
    labelEl.textContent = label;
    svg.appendChild(labelEl);
  });

  return svg;
}

// 토폴로지 링크 path d 문자열(수평 S자 베지어). down=true면 곡률을 살짝 낮춘다(선 스타일/색은 호출부 담당:
// 정상 실선+animateMotion 패킷, down 빨강 점선+X).
export function bezierLink(x1, y1, x2, y2, opts = {}) {
  const { down = false } = opts;
  const bend = down ? 0.35 : 0.5;
  const dx = (x2 - x1) * bend;
  const c1x = x1 + dx, c1y = y1;
  const c2x = x2 - dx, c2y = y2;
  return `M${x1},${y1} C${c1x},${c1y} ${c2x},${c2y} ${x2},${y2}`;
}
