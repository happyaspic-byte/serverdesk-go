// js/screens/topology.js
// S3 — 토폴로지 화면 (REBUILD-SPEC.md §1.3 / §5.1 / §5.5)
//
// 구성: SVG 계층도(company→factory→device→node/VM→EAC→VM) + 플로어 맵 보완 뷰.
//  - 계층도: compute.buildTopo(state) 가 내보내는 boxes/links 좌표를 그대로 배치.
//    팬/줌/핏(휠 0.25~6, 드래그, 더블클릭 리셋, +/−/Fit), 회사·공장·노드꼬리 접기,
//    회사 포커스, 회사 색상(state.companyColors), 박스 클릭 → goDetail.
//  - 플로어 맵: legacy-v3 의 map- 마크업/CSS 를 이식해 랙=장비, 핑=down/deg 로 재해석.
//    2D/3D + 줌(state.view3d / state.zoom) 재사용.
//
// 렌더 전략(스펙 §0-4): init 에서 DOM 1회 생성, render 는 값만 patch.
//  캔버스 통짜 재생성은 **지오메트리(좌표·접힘·구조)** 가 바뀔 때만 한다. 상태색/사용률/
//  라벨 변화는 전부 patch 이므로 틱마다 링크 패킷 애니메이션과 바 트랜지션이 리셋되지 않는다.
//
// 규약
//  - 다른 js/screens/* 를 import 하지 않는다. js/model/* + js/util/* 만 사용.
//  - CSS 는 sc-topo- 접두사 + legacy map- 세트만 사용(공용 클래스 재정의 없음).
//  - 상태 SVG 페인트는 semantic tone을 style 프로퍼티로 적용하고, 회사 색은 categorical data로만 쓴다.

import { buildTopo } from '../model/compute.js';

/* ========================================================================
 * 0. 작은 헬퍼
 * ==================================================================== */

const SVGNS = 'http://www.w3.org/2000/svg';
const XLINK = 'http://www.w3.org/1999/xlink';

/** tone('pos'|'warn'|'neg'|'info'|'mut') → 이 화면 전용 modifier 클래스. */
function tcls(tone) {
  const t = tone === 'pos' || tone === 'warn' || tone === 'neg' || tone === 'info' ? tone : 'mut';
  return 'sc-topo-t-' + t;
}
function tonePaint(tone) {
  return tone === 'pos' ? 'var(--pos)'
    : (tone === 'warn' ? 'var(--warn)'
      : (tone === 'neg' ? 'var(--neg)'
        : (tone === 'info' ? 'var(--blue)' : 'var(--muted)')));
}


function px(n) { return (Math.round(n * 10) / 10) + 'px'; }

function pct(v) { return Math.max(3, Math.min(100, Number(v) || 0)) + '%'; } // 최소 3% 클램프 의도적 — 0~2%도 링크가 '보이게'(가시성 확보)

function reduceMotion() {
  try {
    return !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches);
  } catch (e) { return false; }
}

/* ========================================================================
 * 1. 모듈 로컬 상태 (init 에서 채우고 destroy 에서 비운다)
 * ==================================================================== */

let root = null;
let ctx = null;
let els = null;

let onClick = null;
let onWheel = null;
let onPointerDown = null;
let onPointerMove = null;
let onPointerUp = null;
let onDblClick = null;
let onResize = null;
let onKeyDown = null;
let onBoxOver = null;
let onBoxOut = null;

// 팬/줌 뷰포트 상태
let view = null;            // {s, tx, ty}
let userZoomed = false;
let dragging = false;
let dragId = null;
let pressed = false;
let pressOnBtn = false;
let movedFar = false;
let pressX = 0;
let pressY = 0;
let lastX = 0;
let lastY = 0;
let dims = { w: 1, h: 1 };
let fitTimer = 0;

// 렌더 캐시
let lastTreeSig = '';
let boxRefs = new Map();    // boxId → {node, kind, base, refs}
let linkRefs = new Map();   // linkId → {path, packetG, xG, xCirc, xPath}
let svgEl = null;           // 현재 캔버스 SVG(흐름점을 patch 단계에서 동적 append/remove)
let lastFloorSig = '';
let lastPaneSig = '';
let lastRowsSig = '';
let rowRefs = [];
// 접기 손잡이 참조({node, collapsed}) — collapse 키당 1개. 손잡이는 글리프만 있어 title 이
// 유일한 접근명인데 빌드 시점 언어에 구워지면 언어 전환(render → patch 만 다시 일어남)을
// 따라가지 못한다(#349). patch 단계에서 현재 언어로 갱신할 수 있게 참조를 보관한다.
let knobRefs = [];
// G32: 최신 박스 데이터(boxId → box) — patch 주기와 무관하게 호버 툴팁이 항상 최신값을 읽는다.
let boxDataMap = new Map();
// G32: 상태 필터 딤 — legend 칩 클릭으로 토글. 모듈 로컬 상태(영속화 불필요), tick 마다 유지.
let statusFilter = null;   // null | 'pos' | 'warn' | 'neg'
// 툴팁 재렌더 캐시 — mousemove 마다 툴팁 DOM 전체를 다시 만들지 않기 위해 마지막으로
// 렌더한 box 를 기억한다. boxDataMap 은 render 마다 새 객체로 재구축되므로(boxId → 새 box)
// 객체 동일성이 곧 데이터 버전이다. 언어(행 텍스트)도 키에 포함한다.
let ttCacheBox = null;
let ttCacheKey = '';

/* ========================================================================
 * 2. DOM 생성 헬퍼
 * ==================================================================== */

function E(tag, cls, attrs) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (attrs) for (const k in attrs) { if (attrs[k] != null) n.setAttribute(k, attrs[k]); }
  return n;
}

function S(tag, attrs) {
  const n = document.createElementNS(SVGNS, tag);
  if (attrs) for (const k in attrs) { if (attrs[k] != null) n.setAttribute(k, attrs[k]); }
  return n;
}

/** ctx.util.icon 안전 래퍼(미등록/로드실패에도 레이아웃을 깨지 않는다). */
function ico(name, size, cls) {
  try {
    const f = ctx && ctx.util && ctx.util.icon;
    if (typeof f === 'function') {
      const svg = f(name, { size: size || 14, cls: cls || '' });
      if (svg) return svg;
    }
  } catch (e) { /* 폴백 */ }
  return E('span', cls || '');
}

function L(en, ko) {
  if (ctx && typeof ctx.L === 'function') return ctx.L(en, ko);
  const st = ctx && ctx.store ? ctx.store.getState() : null;
  return (st && st.lang) === 'en' ? en : ko;
}

/** textContent 를 값이 바뀔 때만 쓴다(불필요한 리페인트 방지). */
function txt(node, v) {
  const s = v == null ? '' : String(v);
  if (node && node.textContent !== s) node.textContent = s;
}

function cls(node, v) {
  if (node && node.className !== v) node.className = v;
}

/** 장비 라벨의 하이픈(EDGE-24·everRun 8.1.0.2-19 등)을 비분리 하이픈(U+2011)으로 치환한다.
 *  컴팩트 카드 2줄 클램프에서 줄바꿈 지점을 회사명↔장비ID '공백' 하나로 고정해, 하이픈 뒤에서
 *  '동아반도체 EDGE-' / '24' 처럼 장비ID 가 쪼개지지 않게 한다(E4). 글리프는 하이픈과 동일. */
function labelNB(v) {
  return String(v == null ? '' : v).replace(/-/g, '‑');
}

/* ========================================================================
 * 3. 셸 DOM (init 1회)
 * ==================================================================== */

function buildShell() {
  const wrap = E('div', 'sc-topo');

  /* ---- 헤더 ---- */
  const head = E('div', 'sc-topo-head');
  const hl = E('div', 'sc-topo-head-l');
  const title = E('h1', 'sc-topo-title');
  const sub = E('p', 'sc-topo-sub');
  hl.appendChild(title); hl.appendChild(sub);

  const hr = E('div', 'sc-topo-head-r');
  const legend = E('div', 'sc-topo-legend');
  const sep = E('span', 'sc-topo-vsep');
  // ('라이브 링크 · Mbps' 범례는 플로어 맵 가짜 Mbps 배지 전용이었다 — 배지 제거와 함께 삭제.)

  // 플로어 맵 전환은 비활성화되어 있으므로 단일 버튼 토글을 렌더하지 않는다.
  hr.appendChild(legend); hr.appendChild(sep);
  head.appendChild(hl); head.appendChild(hr);

  /* ---- 본문 그리드 ---- */
  const body = E('div', 'sc-topo-body');

  // 계층도 패널
  const stageWrap = E('div', 'sc-topo-stagewrap', { 'data-topo-pane': 'tree' });
  const fit = E('div', 'sc-topo-fit', {
    'data-topo-fit': '1',
    tabindex: '0',
    role: 'region',
    'aria-keyshortcuts': 'ArrowLeft ArrowRight ArrowUp ArrowDown + - 0 F',
  });
  const canvas = E('div', 'sc-topo-canvas', { 'data-topo-canvas': '1' });
  fit.appendChild(canvas);

  const focusWrap = E('div', 'sc-topo-focuswrap');
  focusWrap.hidden = true;
  const focusBtn = E('button', 'sc-topo-focusbtn', { type: 'button', 'data-topo-clearfocus': '1' });
  const focusDot = E('span', 'sc-topo-focusdot');
  const focusName = E('span', 'sc-topo-focusname');
  const focusHint = E('span', 'sc-topo-focushint');
  focusBtn.appendChild(focusDot); focusBtn.appendChild(focusName); focusBtn.appendChild(focusHint);
  focusWrap.appendChild(focusBtn);
  fit.appendChild(focusWrap);

  const zoomHint = E('span', 'sc-topo-zoomhint');
  fit.appendChild(zoomHint);

  const zoomBox = E('div', 'sc-topo-zoom');
  const zIn = E('button', 'sc-topo-zoombtn', { type: 'button', 'data-topo-zoom': 'in', title: L('Zoom in', '확대') });
  zIn.textContent = '+';
  const zOut = E('button', 'sc-topo-zoombtn', { type: 'button', 'data-topo-zoom': 'out', title: L('Zoom out', '축소') });
  zOut.textContent = '−';
  const zFit = E('button', 'sc-topo-zoombtn sc-topo-zoombtn--fit', { type: 'button', 'data-topo-zoom': 'fit', title: L('Fit', '맞춤') });
  zFit.textContent = 'Fit';
  zoomBox.appendChild(zIn); zoomBox.appendChild(zOut); zoomBox.appendChild(zFit);

  fit.appendChild(zoomBox);

  // G32: 호버 툴팁 — 스테이지(fit) 안쪽에 절대배치, 위임 mouseover 1개가 채운다(destroy 에서 정리).
  const tooltip = E('div', 'sc-topo-tooltip');
  tooltip.hidden = true;
  fit.appendChild(tooltip);
  stageWrap.appendChild(fit);

  // 플로어 맵 패널 (legacy-v3 map- 자산 이식)
  const floor = E('div', 'sc-topo-floorwrap', { 'data-topo-pane': 'floor' });
  floor.hidden = true;
  const mapBar = E('div', 'map-titlebar');
  const mapTitle = E('span', 'map-title');
  const mapTitleTxt = E('span', '');
  mapTitle.appendChild(mapTitleTxt);
  function stat(iconName, extraCls) {
    const s = E('span', 'map-stat' + (extraCls ? ' ' + extraCls : ''));
    s.appendChild(ico(iconName, 13));
    const t = E('span', '');
    s.appendChild(t);
    return { el: s, txt: t };
  }
  const stNodes = stat('db');
  const stHealthy = stat('check');
  const stAlert = stat('warningCircle', 'map-stat--alert');
  const mapLive = E('span', 'map-live');
  const mapLiveTxt = E('span', '');
  mapLive.appendChild(E('span', 'map-live-dot')); mapLive.appendChild(mapLiveTxt);
  mapBar.appendChild(mapTitle); mapBar.appendChild(E('div', 'map-spacer'));
  mapBar.appendChild(stNodes.el); mapBar.appendChild(stHealthy.el); mapBar.appendChild(stAlert.el); mapBar.appendChild(mapLive);

  const mapCanvas = E('div', 'map-canvas');
  mapCanvas.appendChild(E('div', 'map-grid'));
  const mapStage = E('div', 'map-stage', { 'data-topo-mapstage': '1' });
  // 케이블 = 실배선(장비 mgmt IP 의 /24 관리망별 체인) — rebuildFloor 가 랙 좌표에서 그린다.
  // (legacy 하드코딩 장식 경로 4개는 가짜라 제거 — 사용자 지시로 실데이터 전환.)
  const cables = S('svg', { class: 'map-paths', viewBox: '0 0 1000 620', preserveAspectRatio: 'none' });
  mapStage.appendChild(cables);
  const rackLayer = E('div', 'map-layer sc-topo-racks');
  mapStage.appendChild(rackLayer);
  // 장식용 obstacle(방 기둥/벽) 회색 유령 박스는 rest 상태 노이즈(EDGE-21 우측·EV-18 하단에 잔존)라
  // 렌더에서 제거한다(M1). 랙 타일·ping·링크 배지가 정보 계층을 온전히 차지한다.
  // (legacy-v3 의 '라이브 링크 Mbps' 배지 스테이션은 하드코딩 가짜 수치였다 — 실콘솔 오해 소지로
  //  레이어째 제거. 실측 트래픽을 붙이게 되면 폴러 지표 기반으로 다시 만든다.)
  mapCanvas.appendChild(mapStage);

  const mapToggle = E('div', 'map-view-toggle');
  const b2d = E('button', 'map-view-btn', { type: 'button', 'data-topo-3d': '0' });
  b2d.textContent = '2D';
  const b3d = E('button', 'map-view-btn', { type: 'button', 'data-topo-3d': '1' });
  b3d.textContent = '3D';
  mapToggle.appendChild(b2d); mapToggle.appendChild(b3d);
  mapCanvas.appendChild(mapToggle);

  const mapZoom = E('div', 'map-zoom');
  const mzIn = E('button', 'map-zoom-btn', { type: 'button', 'data-topo-mzoom': 'in', title: 'Zoom in' });
  mzIn.textContent = '+';
  const mzOut = E('button', 'map-zoom-btn', { type: 'button', 'data-topo-mzoom': 'out', title: 'Zoom out' });
  mzOut.textContent = '−';
  const mzR = E('button', 'map-zoom-btn', { type: 'button', 'data-topo-mzoom': 'reset', title: 'Reset' });
  mzR.appendChild(ico('crop', 13));
  mapZoom.appendChild(mzIn); mapZoom.appendChild(mzOut); mapZoom.appendChild(mzR);
  mapCanvas.appendChild(mapZoom);

  floor.appendChild(mapBar); floor.appendChild(mapCanvas);

  /* ---- 우측 요약 ---- */
  const side = E('aside', 'sc-topo-side');
  const cnt = E('div', 'sc-topo-counters');
  function counter(toneCls) {
    const c = E('div', 'sc-topo-counter');
    const v = E('div', 'sc-topo-counter-val u-mono' + (toneCls ? ' ' + toneCls : ''));
    const l = E('div', 'sc-topo-counter-lbl');
    c.appendChild(v); c.appendChild(l);
    cnt.appendChild(c);
    return { val: v, lbl: l, node: c };
  }
  const cNodes = counter('');
  // 정상 집계는 중립(총합/집계는 색 예외 아님) — green 은 상태 도트/엣지에만 예약.
  const cHealthy = counter('');
  const cAlerts = counter('');
  // 경보 총합은 중립(잉크)으로 두고, '심각 N' 만 red 로 분리 표기(minor#1) — 총량을 red 로 태워
  // 과대표현하던 문제 해소. 심각 0 이면 칩을 숨긴다.
  const cAlertCrit = E('div', 'sc-topo-counter-crit sc-topo-t-neg u-mono');
  cAlertCrit.hidden = true;
  cAlerts.node.appendChild(cAlertCrit);
  side.appendChild(cnt);

  // 우측 요약 패널의 섹션 제목 — 시각은 그대로, 의미만 h2 로 승격한다.
  // 실측상 이 화면은 h1 1개뿐이라 스크린리더가 '공장별' 목록의 소속을 못 읽었다.
  const grpLbl = E('h2', 'sc-topo-grouplbl u-mono');
  side.appendChild(grpLbl);
  const rowsWrap = E('div', 'sc-topo-rows');
  side.appendChild(rowsWrap);

  const hint = E('div', 'sc-topo-hintbox');
  hint.appendChild(ico('infoCircle', 15, 'sc-topo-hintico'));
  const hintTxt = E('span', 'sc-topo-hinttxt');
  hint.appendChild(hintTxt);
  side.appendChild(hint);

  body.appendChild(stageWrap);
  body.appendChild(floor);
  body.appendChild(side);
  wrap.appendChild(head);
  wrap.appendChild(body);

  return {
    wrap, title, sub, legend,
    fit, canvas, focusWrap, focusBtn, focusDot, focusName, focusHint, zoomHint, zIn, zOut, tooltip,
    floor, stageWrap, mapTitleTxt, stNodes, stHealthy, stAlert, mapLiveTxt,
    mapStage, rackLayer, cableSvg: cables, b2d, b3d,
    cNodes, cHealthy, cAlerts, cAlertCrit, grpLbl, rowsWrap, hintTxt,
  };
}

/* ========================================================================
 * 4. 계층도 박스 — build(구조) + patch(값)
 * ==================================================================== */

/** 진행바 1개. NA 표시용 대시와 바를 함께 만들고 patch 에서 토글한다. */
function makeBar() {
  const wrapEl = E('span', 'sc-topo-barwrap');
  const track = E('span', 'sc-topo-bar');
  const fill = E('i', 'sc-topo-bar-fill');
  track.appendChild(fill);
  const na = E('span', 'sc-topo-bar-na u-mono');
  na.textContent = '—';
  na.hidden = true;
  wrapEl.appendChild(track); wrapEl.appendChild(na);
  return { el: wrapEl, track, fill, na };
}

function patchBar(b, val, isNA, tone) {
  b.track.hidden = !!isNA;
  b.na.hidden = !isNA;
  if (!isNA) {
    b.fill.style.width = pct(val);
    cls(b.fill, 'sc-topo-bar-fill ' + tcls(tone));
  }
}

function makeKv(kw) {
  const r = E('span', 'sc-topo-kv');
  const k = E('span', 'sc-topo-kv-k u-mono');
  if (kw) k.style.width = kw + 'px';
  const v = E('span', 'sc-topo-kv-v u-mono');
  r.appendChild(k); r.appendChild(v);
  return { el: r, k, v };
}

function patchKv(kv, key, value, tone) {
  txt(kv.k, key);
  txt(kv.v, value);
  cls(kv.v, 'sc-topo-kv-v u-mono ' + tcls(tone));
}

function placeBox(node, b) {
  node.style.left = px(b.x);
  node.style.top = px(b.y);
  node.style.width = px(b.w);
  node.style.height = px(b.h);
  // G32: 호버 툴팁 위임이 이 id 로 최신 박스 데이터(boxDataMap)를 찾는다 — patch 주기와
  // 무관하게 항상 최신값을 보여주기 위해 DOM 이 아니라 별도 맵에서 값을 읽는다.
  node.dataset.topoBox = b.id;
}

/* ---- company ---- */
function buildCompany(b) {
  const node = E('button', '', { type: 'button', 'data-topo-focus': b.label });
  const icoWrap = E('span', 'sc-topo-co-ico');
  icoWrap.appendChild(ico('building', 14));
  const name = E('span', 'sc-topo-co-name');
  const dot = E('span', 'sc-topo-dot');
  node.appendChild(icoWrap); node.appendChild(name); node.appendChild(dot);
  return { node, base: 'sc-topo-box sc-topo-co', refs: { name, dot } };
}
function patchCompany(rec, b) {
  const r = rec.refs;
  txt(r.name, b.label);
  cls(r.dot, 'sc-topo-dot ' + tcls(b.tone));
  cls(rec.node, rec.base + ' ' + tcls(b.tone) + (b.focused ? ' is-focused' : ''));
  rec.node.style.setProperty('--sc-topo-co', b.color || 'var(--muted)');
}

/* ---- factory ---- */
function buildFactory() {
  const node = E('div', '');
  const icoWrap = E('span', 'sc-topo-fa-ico');
  icoWrap.appendChild(ico('building', 13));
  const name = E('span', 'sc-topo-fa-name');
  const count = E('span', 'sc-topo-fa-count u-mono');
  const dot = E('span', 'sc-topo-dot');
  node.appendChild(icoWrap); node.appendChild(name); node.appendChild(count); node.appendChild(dot);
  return { node, base: 'sc-topo-box sc-topo-fa', refs: { name, count, dot } };
}
function patchFactory(rec, b) {
  const r = rec.refs;
  txt(r.name, b.label);
  txt(r.count, b.count != null ? String(b.count) : '');
  cls(r.dot, 'sc-topo-dot ' + tcls(b.tone));
  cls(rec.node, rec.base + ' ' + tcls(b.tone));
  rec.node.style.setProperty('--sc-topo-co', b.color || 'var(--line)');
}

/* ---- device ---- */
function buildDevice(b) {
  const node = E('button', '', { type: 'button', 'data-topo-open': b.deviceId || b.id });
  const refs = {};

  refs.dot = E('span', 'sc-topo-dev-dot sc-topo-dot');
  node.appendChild(refs.dot);
  refs.compact = !!b.compact;

  const head = E('span', 'sc-topo-dev-head');
  head.appendChild(ico(b.typeIcon || 'db', b.compact ? 14 : 16, 'sc-topo-dev-tico'));
  refs.label = E('span', 'sc-topo-dev-label');
  head.appendChild(refs.label);
  node.appendChild(head);

  const sub = E('span', 'sc-topo-dev-sub');
  refs.status = E('span', 'sc-topo-dev-status');
  refs.meta = E('span', 'sc-topo-dev-meta u-mono');
  // 활성 경보 뱃지(작은 pill) — 카드 클릭 위임(data-topo-open)으로 상세 이동.
  refs.alert = E('span', 'sc-topo-dev-alert');
  refs.alert.appendChild(ico('bell', 9, 'sc-topo-dev-alert-ico'));
  refs.alertN = E('span', 'sc-topo-dev-alert-n u-mono');
  refs.alert.appendChild(refs.alertN);
  sub.appendChild(refs.status); sub.appendChild(refs.meta); sub.appendChild(refs.alert);
  node.appendChild(sub);

  if (b.isFT) {
    const gridWrap = E('span', 'sc-topo-dev-grid');
    refs.lic = makeKv();
    gridWrap.appendChild(refs.lic.el);
    // G26: IP(11자 mono)는 전용 전폭 행 — 예전 VER·IP 2열 동거는 열당 ~79px 로,
    //     맥(SF Mono, 글리프 광폭)에서 'v8.1.0.2-…'/'172.30.1.…' 절단을 만들었다.
    refs.ip = makeKv();
    gridWrap.appendChild(refs.ip.el);
    const g2 = E('span', 'sc-topo-dev-grid2');
    refs.ver = makeKv();
    refs.exp = makeKv();
    g2.appendChild(refs.ver.el); g2.appendChild(refs.exp.el);
    gridWrap.appendChild(g2);
    node.appendChild(gridWrap);
  } else if (b.enduFlow) {
    // ztC Endurance — 단일 섀시 안의 관리 경로 흐름: BMC → Standby OS → MGMT·HOST.
    const flow0 = b.enduFlow;
    const enduWrap = E('span', 'sc-topo-endu');
    const actBox = E('span', 'sc-topo-endu-box sc-topo-endu-box--wide');
    const actK = E('span', 'sc-topo-endu-k u-mono');
    const actV = E('span', 'sc-topo-endu-v u-mono');
    actBox.appendChild(actK); actBox.appendChild(actV);
    enduWrap.appendChild(actBox);
    refs.enduActive = { k: actK, v: actV };
    const flowEl = E('span', 'sc-topo-endu-flow');
    refs.enduGroups = flow0.groups.map((g, gi) => {
      if (gi) {
        const arrow = E('span', 'sc-topo-endu-arrow');
        arrow.textContent = '→'; // E() 의 3번째 인자는 attrs 객처이므로 텍스트는 직접 대입
        flowEl.appendChild(arrow);
      }
      // 그룹 = 타이틀 + 열(col)들의 가로 나열, 열 안에서 박스는 위→아래.
      // (MGMT·HOST 는 UI 1/2 열 + WINDOWS 열 — 사용자 지시의 우측 HOST 배치)
      const grp = E('span', 'sc-topo-endu-group');
      const ttl = E('span', 'sc-topo-endu-gtitle u-mono');
      ttl.textContent = g.title;
      const row = E('span', 'sc-topo-endu-row');
      const boxes = [];
      g.cols.forEach((colBoxes) => {
        const col = E('span', 'sc-topo-endu-col');
        colBoxes.forEach((bx) => {
          const box = E('span', 'sc-topo-endu-box');
          const head = E('span', 'sc-topo-endu-head');
          const dot = E('i', 'sc-topo-endu-dot');
          dot.setAttribute('aria-hidden', 'true');
          const k = E('span', 'sc-topo-endu-k u-mono');
          const ms = E('span', 'sc-topo-endu-ms u-mono');
          head.appendChild(dot); head.appendChild(k); head.appendChild(ms);
          const v = E('span', 'sc-topo-endu-v u-mono');
          box.appendChild(head); box.appendChild(v);
          col.appendChild(box);
          boxes.push({ k, v, dot, ms });
        });
        row.appendChild(col);
      });
      grp.appendChild(ttl); grp.appendChild(row);
      flowEl.appendChild(grp);
      return boxes;
    });
    enduWrap.appendChild(flowEl);
    node.appendChild(enduWrap);
  } else if (b.rows && b.rows.length) {
    const rowsWrap = E('span', 'sc-topo-dev-rows');
    refs.rows = b.rows.map((r) => {
      const kv = makeKv(r.kw);
      rowsWrap.appendChild(kv.el);
      return kv;
    });
    node.appendChild(rowsWrap);
  } else {
    const bars = E('span', 'sc-topo-dev-bars');
    const kc = E('span', 'sc-topo-bar-k u-mono'); kc.textContent = 'CPU';
    refs.cpu = makeBar();
    const km = E('span', 'sc-topo-bar-k u-mono'); km.textContent = 'MEM';
    refs.mem = makeBar();
    bars.appendChild(kc); bars.appendChild(refs.cpu.el);
    bars.appendChild(km); bars.appendChild(refs.mem.el);
    node.appendChild(bars);
  }

  const foot = E('span', 'sc-topo-dev-foot');
  refs.vmWrap = E('span', 'sc-topo-dev-vm');
  refs.vmWrap.appendChild(ico('box', 11, 'sc-topo-dev-vmico'));
  refs.vmTxt = E('span', 'u-mono');
  refs.vmWrap.appendChild(refs.vmTxt);
  refs.sync = E('span', 'sc-topo-dev-sync');
  foot.appendChild(refs.vmWrap); foot.appendChild(refs.sync);
  node.appendChild(foot);

  return { node, base: 'sc-topo-box sc-topo-dev' + (b.compact ? ' sc-topo-dev--compact' : ''), refs };
}
function patchDevice(rec, b) {
  const r = rec.refs;
  cls(rec.node, rec.base + ' ' + tcls(b.tone));
  cls(r.dot, 'sc-topo-dev-dot sc-topo-dot ' + tcls(b.tone) + (b.anim ? ' ' + b.anim : ''));
  // G5: 컴팩트 카드는 회사 프리픽스를 벗긴 장비코드(code)만 보여 준다 — 회사·공장은 이미
  //     왼쪽 계층 칩이 말하고 있어 '대원정밀 EV-03' 52회 반복은 순수 노이즈였다.
  //     전체 이름은 카드 title(툴팁)과 상세 화면에 그대로 남는다.
  txt(r.label, labelNB((r.compact && b.code) ? b.code : b.label));
  // 콘솔 점검 창(maintWin, #19) — 상태 배지를 '점검 중'으로 대체(nodes.js 와 같은 규약).
  // 실제 장비 상태는 카드 title·상세 화면에 그대로 남고, 창 사유(note)는 배지 툴팁에 둔다.
  txt(r.status, b.maintWin ? L('Maintenance', '점검 중') : b.statusLabel);
  cls(r.status, 'sc-topo-dev-status ' + tcls(b.tone));
  r.status.title = b.maintWin ? (b.maintNote || '') : '';
  // G26: meta 는 실제 FT 여부(ftType)로 판단 — FT 는 type 만(IP·버전은 kv 그리드/상세 담당).
  //     비FT 컴팩트는 type·IP 까지만(버전은 줌 세부·상세) — 맥 광폭 폰트에서도 안 넘치는 구조 예산.
  const metaTxt = (b.typeLabel || '')
    + (!b.ftType && b.mgmt ? ' · ' + b.mgmt : '')
    + (!b.ftType && !b.compact && b.version ? ' · v' + b.version : '');
  txt(r.meta, metaTxt);
  // 툴팁엔 항상 전체(type · IP · 버전) — 화면에서 생략된 값 복구 경로.
  r.meta.title = (b.typeLabel || '')
    + (b.mgmt ? ' · ' + b.mgmt : '')
    + (b.version ? ' · v' + b.version : '');

  if (r.alert) {
    const an = b.alertN || 0;
    r.alert.hidden = an <= 0;
    if (an > 0) {
      txt(r.alertN, String(an));
      cls(r.alert, 'sc-topo-dev-alert ' + tcls(b.alertTone || 'warn'));
      r.alert.title = L('Active alerts', '활성 경보') + ': ' + an
        + (b.alertCrit ? ' · ' + L('critical ', '심각 ') + b.alertCrit : '')
        + (b.alertWarn ? ' · ' + L('warning ', '경고 ') + b.alertWarn : '');
    }
  }

  if (r.lic) {
    patchKv(r.lic, 'LIC', b.licLabel || '—', 'mut');
    patchKv(r.ver, 'VER', b.version ? 'v' + b.version : '—', 'mut');
    patchKv(r.ip, 'IP', b.mgmt || '—', 'mut');
    patchKv(r.exp, b.licExpLabel || 'EXP', b.licDTxt || '—', b.licTone);
  } else if (r.enduGroups && b.enduFlow) {
    txt(r.enduActive.k, b.enduFlow.active.k);
    txt(r.enduActive.v, (b.enduFlow.active.v || []).join('\n'));
    cls(r.enduActive.v, 'sc-topo-endu-v u-mono ' + tcls(b.enduFlow.active.tone || 'mut'));
    b.enduFlow.groups.forEach((g, gi) => {
      // refs 는 build 때 열 순서대로 평탄하게 쌓인다 — shift() 로 소모하면 다음 patch 주기에
      // 참조가 사라지므로 인덱스 커서로 읽는다.
      let bi = 0;
      g.cols.forEach((col) => col.forEach((bx) => {
        const rr = (r.enduGroups[gi] || [])[bi++];
        if (!rr) return;
        txt(rr.k, bx.k);
        txt(rr.v, (bx.v || []).join('\n'));
        cls(rr.v, 'sc-topo-endu-v u-mono ' + tcls(bx.tone || 'mut'));
        // 도달 상태 점 + 상태 텍스트 — ms 숫자는 표시하지 않는다(사용자 지시: 불필요).
        // ok 는 점만, slow/down 은 텍스트를 병행(색각 의존 금지 계약).
        if (rr.dot) {
          const st = bx.state || '';
          cls(rr.dot, 'sc-topo-endu-dot' + (st ? ' is-' + st : ''));
          rr.dot.title = { ok: L('responding', '응답 정상'), slow: L('slow response', '응답 느림'), down: L('no response', '무응답') }[st] || '';
          txt(rr.ms, st === 'slow' ? L('slow', '느림') : (st === 'down' ? L('down', '끊김') : ''));
        }
      }));
    });
  } else if (r.rows && b.rows) {
    b.rows.forEach((row, i) => { if (r.rows[i]) patchKv(r.rows[i], row.k, row.v, row.tone); });
  } else if (r.cpu) {
    patchBar(r.cpu, b.cpu, b.cpuNA, b.cpuTone);
    patchBar(r.mem, b.mem, b.memNA, b.memTone);
  }

  r.vmWrap.hidden = b.vmLabel == null;
  if (b.vmLabel != null) txt(r.vmTxt, b.vmLabel + ' VM');
  txt(r.sync, b.syncLabel);
  cls(r.sync, 'sc-topo-dev-sync ' + tcls(b.syncTone));
}

/* ---- node ---- */
function buildNode(b) {
  const node = E('button', '', { type: 'button', 'data-topo-open': b.deviceId || b.id });
  const refs = {};
  refs.dot = E('span', 'sc-topo-dev-dot sc-topo-dot');
  node.appendChild(refs.dot);

  const head = E('div', 'sc-topo-node-head');
  refs.name = E('span', 'sc-topo-node-name u-mono');
  refs.role = E('span', 'sc-topo-node-role');
  head.appendChild(refs.name); head.appendChild(refs.role);
  node.appendChild(head);

  const sub = E('div', 'sc-topo-node-sub');
  refs.ip = E('span', 'sc-topo-node-ip u-mono');
  refs.state = E('span', 'sc-topo-node-state');
  sub.appendChild(refs.ip); sub.appendChild(refs.state);
  node.appendChild(sub);

  const bars = E('div', 'sc-topo-node-bars');
  function mk(label) {
    const g = E('div', 'sc-topo-node-bar');
    const k = E('span', 'sc-topo-bar-k u-mono'); k.textContent = label;
    const bar = makeBar();
    const v = E('span', 'sc-topo-node-pct u-mono');
    g.appendChild(k); g.appendChild(bar.el); g.appendChild(v);
    bars.appendChild(g);
    return { bar, v };
  }
  refs.cpu = mk('CPU');
  refs.mem = mk('MEM');
  node.appendChild(bars);

  return { node, base: 'sc-topo-box sc-topo-node', refs };
}
function patchNode(rec, b) {
  const r = rec.refs;
  cls(rec.node, rec.base + ' ' + tcls(b.tone));
  cls(r.dot, 'sc-topo-dev-dot sc-topo-dot ' + tcls(b.tone) + (b.anim ? ' ' + b.anim : ''));
  txt(r.name, b.name);
  txt(r.role, b.role || '');
  cls(r.role, 'sc-topo-node-role ' + tcls(b.roleTone));
  txt(r.ip, b.ip || '—');
  txt(r.state, b.stateLabel);
  cls(r.state, 'sc-topo-node-state ' + tcls(b.tone));
  patchBar(r.cpu.bar, b.cpu, b.cpu == null, b.cpuTone);
  txt(r.cpu.v, b.cpu == null ? '—' : b.cpu + '%');
  patchBar(r.mem.bar, b.mem, b.mem == null, b.memTone);
  txt(r.mem.v, b.mem == null ? '—' : b.mem + '%');
}

/* ---- eac ---- */
function buildEac(b) {
  const node = E('button', '', { type: 'button', 'data-topo-open': b.deviceId || b.id });
  const chrome = E('div', 'sc-topo-eac-chrome');
  // 맥OS 신호등 3점(빨/노/초)은 실제 상태와 무관한 순수 장식이었다 — 실 헬스 도트 1개로 대체(minor#5).
  const tl = E('span', 'sc-topo-eac-tl');
  chrome.appendChild(tl);
  chrome.appendChild(E('span', 'sc-topo-eac-urlbar'));
  node.appendChild(chrome);
  const body = E('div', 'sc-topo-eac-body');
  const circ = E('span', 'sc-topo-eac-circ');
  circ.appendChild(ico('ops', 15));
  const title = E('span', 'sc-topo-eac-title');
  const ip = E('span', 'sc-topo-eac-ip u-mono');
  const pill = E('span', 'sc-topo-eac-pill');
  const pdot = E('span', 'sc-topo-dot');
  const ptxt = E('span', '');
  pill.appendChild(pdot); pill.appendChild(ptxt);
  body.appendChild(circ); body.appendChild(title); body.appendChild(ip); body.appendChild(pill);
  node.appendChild(body);
  return { node, base: 'sc-topo-box sc-topo-eac', refs: { title, ip, pill, pdot, ptxt, tl } };
}
function patchEac(rec, b) {
  const r = rec.refs;
  cls(rec.node, rec.base + ' ' + tcls(b.tone));
  if (r.tl) cls(r.tl, 'sc-topo-eac-tl ' + tcls(b.tone));  // 창 크롬 도트 = 실 헬스 상태
  rec.node.title = L('Management console', '관리 화면');
  txt(r.title, L('Console', '관리 화면'));
  txt(r.ip, b.mgmt || '—');
  txt(r.ptxt, b.okLabel);
  cls(r.pill, 'sc-topo-eac-pill ' + tcls(b.tone));
  cls(r.pdot, 'sc-topo-dot ' + tcls(b.tone));
}

/* ---- vm ---- */
function buildVm(b) {
  const node = E('button', '', { type: 'button', 'data-topo-open': b.deviceId || b.id });
  const head = E('div', 'sc-topo-vm-head');
  // 상태점 — 컴팩트(실데이터 세로 스택)에서 아이콘 대신 노출, 상태색을 담는다.
  const dot = E('span', 'sc-topo-vm-dot sc-topo-dot');
  head.appendChild(dot);
  head.appendChild(ico('box', 14, 'sc-topo-vm-ico'));
  const name = E('span', 'sc-topo-vm-name u-mono');
  const ft = E('span', 'sc-topo-vm-ft');
  head.appendChild(name);
  // 비컴팩트(시뮬)는 이름 옆에, 컴팩트(실데이터)는 이름이 한 줄을 다 쓰도록 FT/HA 를 노드 줄로 내린다.
  if (!b.compact) head.appendChild(ft);
  node.appendChild(head);
  const ip = E('div', 'sc-topo-vm-ip u-mono');
  node.appendChild(ip);
  const foot = E('div', 'sc-topo-vm-foot');
  const state = E('span', 'sc-topo-vm-state');
  const nd = E('span', 'sc-topo-vm-node u-mono');
  foot.appendChild(state); foot.appendChild(nd);
  if (b.compact) foot.appendChild(ft);
  node.appendChild(foot);
  return { node, base: 'sc-topo-box sc-topo-vm' + (b.compact ? ' sc-topo-vm--compact' : ''), refs: { dot, name, ft, ip, state, nd } };
}
function patchVm(rec, b) {
  const r = rec.refs;
  cls(rec.node, rec.base + ' ' + tcls(b.tone));
  // #487: 클릭 위임 속성도 patch 갱신 대상이다. 비FT 호스팅 VM 의 deviceId 는 게스트 IP↔mgmt
  // 매칭에 의존하는데, mgmt 편집으로 매칭이 바뀌어도 박스 id·좌표는 그대로라 treeSig(지오메트리)
  // 가 불변 → 재생성 없이 patch 만 일어난다. 여기서 갱신하지 않으면 옛 장비 상세로 이동한다.
  rec.node.setAttribute('data-topo-open', b.deviceId || b.id);
  cls(r.dot, 'sc-topo-vm-dot sc-topo-dot ' + tcls(b.tone));
  // 실데이터에서는 배치/대기 노드까지 툴팁에 담는다(카드 폭이 좁아 본문엔 안 들어간다).
  txt(r.name, b.name || 'VM');
  txt(r.ft, b.ftL || '');
  cls(r.ft, 'sc-topo-vm-ft ' + tcls(b.ftTone));
  r.ft.hidden = !b.ftL;
  txt(r.ip, b.ip || '—');
  txt(r.state, b.state);
  cls(r.state, 'sc-topo-vm-state ' + tcls(b.tone));
  txt(r.nd, b.node ? '▸ ' + b.node : '');
}

/* ---- vmgroup (노드별 VM 묶음 노드 — 실장비 전용) ----
   노드와 개별 VM 사이 4단째. 개수 배지 + 실행/전체를 보여 주고, 오른쪽 손잡이로 개별 VM 을 펼침/접힘. */
function buildVmgroup(b) {
  const node = E('button', '', { type: 'button', 'data-topo-open': b.deviceId || b.id });
  const head = E('div', 'sc-topo-vmgroup-head');
  head.appendChild(ico('box', 14, 'sc-topo-vmgroup-ico'));
  const title = E('span', 'sc-topo-vmgroup-title');
  title.textContent = 'VM';
  const badge = E('span', 'sc-topo-vmgroup-badge u-mono');
  head.appendChild(title); head.appendChild(badge);
  node.appendChild(head);
  const sub = E('div', 'sc-topo-vmgroup-sub u-mono');
  node.appendChild(sub);
  return { node, base: 'sc-topo-box sc-topo-vmgroup', refs: { badge, sub } };
}
function patchVmgroup(rec, b) {
  const r = rec.refs;
  cls(rec.node, rec.base + ' ' + tcls(b.tone));
  txt(r.badge, String(b.count || 0));
  txt(r.sub, (b.running != null ? b.running : 0) + '/' + (b.count || 0) + ' ' + L('run', '실행'));
}

const BUILDERS = {
  company: buildCompany, factory: buildFactory, device: buildDevice,
  node: buildNode, eac: buildEac, vm: buildVm, vmgroup: buildVmgroup,
};
const PATCHERS = {
  company: patchCompany, factory: patchFactory, device: patchDevice,
  node: patchNode, eac: patchEac, vm: patchVm, vmgroup: patchVmgroup,
};

/** 접기 손잡이 title — build(buildKnob)와 patch(손잡이 언어 추적, #349)가 같은 문구를
 *  쓰도록 한 곳에 둔다. 순수 함수라 named export 로 회귀 게이트를 둔다(fitScale 전례). */
export function knobTitle(collapsed) {
  return collapsed ? L('Expand', '펼치기') : L('Collapse', '접기');
}

/** 접기 손잡이(+/−). 박스 오른쪽 가장자리 중앙. */
function buildKnob(b) {
  const k = E('button', 'sc-topo-knob' + (b.collapsed ? ' is-collapsed' : ''), {
    type: 'button', 'data-topo-toggle': b.key, 'data-topo-next': b.collapsed ? '0' : '1',
    title: knobTitle(!!b.collapsed),
  });
  k.style.left = px(b.x + b.w - 11);
  k.style.top = px(b.y + b.h / 2 - 11);
  // 글리프(−/+)는 CSS ::before/::after 고정 아이콘 바로 그린다(is-collapsed 클래스로 분기) — textContent 없음(T).
  return k;
}

/* ========================================================================
 * 5. 캔버스 재생성 / patch
 * ==================================================================== */

/**
 * 지오메트리 시그니처. 좌표·접힘·구조(FT 여부, 메타행 개수)만 담는다.
 * 상태색/사용률/라벨은 여기 넣지 않는다 → 틱마다 재생성되지 않고 patch 로만 갱신된다.
 * 언어도 넣지 않는다(#349) — 언어 전환에 캔버스를 통째로 다시 만들지 않고, 손잡이 title
 * 같은 언어 의존 속성은 patch 단계가 갱신한다. named export 로 그 계약을 게이트한다.
 */
export function treeSig(topo) {
  const bs = topo.boxes.map((b) => b.id + '|' + b.kind
    + '|' + b.x + ',' + b.y + ',' + b.w + ',' + b.h
    + '|' + (b.collapsed ? 1 : 0) + '|' + (b.collapsible ? 1 : 0)
    + '|' + (b.isFT ? 1 : 0) + '|' + (b.compact ? 1 : 0)
    + '|' + (b.rows ? b.rows.length : -1)).join(';');
  const ls = topo.links.map((k) => k.id + '|' + k.path).join(';');
  // G32: 레이아웃 입력(D/C) 시그니처를 명시적으로 포함 — 접힌 장비가 0~1대라 D 변화가 좌표에
  // 반영되지 않는 극단 케이스까지 안전하게 잡는다(사용자 지시).
  return topo.viewBox + '#' + (topo.layoutSig || '') + '#' + bs + '#' + ls;
}

// 동시 흐름점(SMIL) 상한 — 저하 엣지가 여러 개여도 '최악 링크' 3개까지만 흐름을 켠다(C1).
// 상시 모션을 앱 전역 steady-state 로 조여, 움직임이 '가장 주의가 필요한 소수'만 가리키게 한다.
const MAX_FLOW = 3;

/** 흐름점 1개(단일 원 + animateMotion). 저하 엣지에만, patch 단계에서 필요할 때만 만든다. */
function makePacket(k) {
  const g = S('g', { class: 'sc-topo-packet' });
  const circ = S('circle', { r: '3', opacity: '0.95' });
  const am = S('animateMotion', { dur: '2.4s', repeatCount: 'indefinite', begin: k.begin + 's' });
  const mp = S('mpath');
  try { mp.setAttributeNS(XLINK, 'href', '#sd-topo-' + k.id); } catch (e) { /* 구형 폴백 */ }
  mp.setAttribute('href', '#sd-topo-' + k.id);
  am.appendChild(mp);
  circ.appendChild(am);
  g.appendChild(circ);
  return { g, circ };
}

function rebuildCanvas(topo) {
  const cv = els.canvas;
  while (cv.firstChild) cv.removeChild(cv.firstChild);
  boxRefs.clear();
  linkRefs.clear();

  cv.style.width = topo.wPx;
  cv.style.height = topo.hPx;

  const svg = S('svg', {
    class: 'sc-topo-svg', width: String(topo.w), height: String(topo.h), viewBox: topo.viewBox,
  });
  svgEl = svg;
  const defs = S('defs');
  topo.links.forEach((k) => {
    defs.appendChild(S('path', { id: 'sd-topo-' + k.id, d: k.path, fill: 'none' }));
  });
  svg.appendChild(defs);

  // 링크 선
  topo.links.forEach((k) => {
    const p = S('path', { class: 'sc-topo-link', d: k.path, fill: 'none', 'stroke-linecap': 'round', 'stroke-linejoin': 'round' });
    svg.appendChild(p);
    linkRefs.set(k.id, { path: p, packetG: null, xG: null, xCirc: null, xPath: null });
  });

  // 흐름점(패킷)은 여기서 미리 만들지 않는다 — 저하(deg) 엣지에만, patchCanvas 가 필요한 순간에만
  // 동적으로 생성/제거한다(정상 59링크에 상시 SMIL 을 두던 문제 제거, T1). packetG 는 일단 null.

  // 단절 X 마커
  topo.links.forEach((k) => {
    const g = S('g', { class: 'sc-topo-xmark', transform: 'translate(' + k.midX + ',' + k.midY + ')' });
    const c = S('circle', { r: '8', 'stroke-width': '1.4' });
    c.style.fill = 'var(--surface)';
    const p = S('path', { d: 'M-3 -3 L3 3 M3 -3 L-3 3', 'stroke-width': '1.5', 'stroke-linecap': 'round' });
    g.appendChild(c); g.appendChild(p);
    svg.appendChild(g);
    const rec = linkRefs.get(k.id);
    if (rec) { rec.xG = g; rec.xCirc = c; rec.xPath = p; }
  });
  cv.appendChild(svg);

  topo.boxes.forEach((b) => {
    const build = BUILDERS[b.kind] || buildVm;
    const built = build(b);
    placeBox(built.node, b);
    cv.appendChild(built.node);
    boxRefs.set(b.id, { node: built.node, kind: b.kind, base: built.base, refs: built.refs });
  });
  // 접기 손잡이는 collapse 키당 **한 개만** 렌더한다. 한 클러스터의 두 노드가 같은 인프라
  // 토글 키('ii:<id>')를 달던 중복 렌더 버그(같은 키 손잡이 2개 → 상태 꼬임)를 원천 차단한다.
  const knobSeen = new Set();
  knobRefs = [];
  topo.boxes.filter((b) => b.collapsible).forEach((b) => {
    if (b.key == null || knobSeen.has(b.key)) return;
    knobSeen.add(b.key);
    const knob = buildKnob(b);
    // #349: title 은 유일한 접근명 — collapsed 는 treeSig 입력이라 patch 사이에 변하지
    // 않으므로 여기서 찍어 두고, patchCanvas 가 언어 전환 시 title 만 다시 굽는다.
    knobRefs.push({ node: knob, collapsed: !!b.collapsed });
    cv.appendChild(knob);
  });

  dims = { w: topo.w, h: topo.h };
}

/** 지오메트리가 그대로일 때 값만 patch(트랜지션/패킷 애니 리셋 방지). */
function patchCanvas(topo) {
  topo.boxes.forEach((b) => {
    const rec = boxRefs.get(b.id);
    if (!rec) return;
    const fn = PATCHERS[rec.kind];
    if (fn) fn(rec, b);
  });
  // #349: 접기 손잡이 title 은 patch 대상이다 — 언어 전환은 render 만 다시 부르고
  // treeSig 에 lang 을 넣지 않으므로(캔버스 통짜 재생성 방지) 여기서 현재 언어로 갱신한다.
  knobRefs.forEach((r) => {
    const t = knobTitle(r.collapsed);
    if (r.node.title !== t) r.node.title = t;
  });
  const noMotion = reduceMotion();
  let flow = 0;   // 이번 틱에 실제로 켠 흐름점 수(상한 MAX_FLOW)
  topo.links.forEach((k) => {
    const rec = linkRefs.get(k.id);
    if (!rec) return;
    rec.path.style.stroke = tonePaint(k.strokeTone);
    rec.path.setAttribute('stroke-width', k.dashed ? '1.4' : '1.8');
    rec.path.setAttribute('opacity', k.dashed ? '0.75' : '0.34');
    if (k.dashed) rec.path.setAttribute('stroke-dasharray', '5 6');
    else rec.path.removeAttribute('stroke-dasharray');
    // 흐름점: 저하 엣지(k.packet)에만 + 총량 상한. 필요할 때만 SMIL 을 DOM 에 만들고, 아니면 제거한다.
    const wantFlow = !noMotion && k.packet && flow < MAX_FLOW;
    if (wantFlow) {
      flow += 1;
      if (!rec.packetG && svgEl) {
        const mk = makePacket(k);
        svgEl.appendChild(mk.g);
        rec.packetG = mk.g; rec.circles = [mk.circ];
      }
      if (rec.packetG) {
        rec.packetG.style.display = '';
        if (rec.circles) rec.circles.forEach((c) => { c.style.fill = tonePaint(k.packetTone); });
      }
    } else if (rec.packetG) {
      if (svgEl && rec.packetG.parentNode === svgEl) svgEl.removeChild(rec.packetG);
      rec.packetG = null; rec.circles = null;
    }
    if (rec.xG) {
      rec.xG.style.display = k.dashed ? '' : 'none';
      if (k.dashed) {
        const paint = tonePaint(k.strokeTone);
        rec.xCirc.style.stroke = paint;
        rec.xPath.style.stroke = paint;
      }
    }
  });
}

/* ========================================================================
 * 6. 팬 / 줌 / 핏 (Vigil useTopoZoom 이식)
 * ==================================================================== */

// 스테이지 안쪽 여백. 우측/하단은 줌 버튼·힌트 칩이 앉는 자리라 더 크게 잡는다.
const FIT_PAD = { l: 14, t: 12, r: 56, b: 30 };
const MAX_SCALE = 6;
const FIT_MAX = 2.2;     // 내용이 작을 때(회사 포커스 등) 확대 상한
const FIT_READABLE = 0.9; // 자동 핏 판독 하한(5R 심사 반영) — 이 아래로 축소하면 보조 라벨 유효 픽셀이
                          // 9.5px 미만으로 떨어진다. 초기/자동 핏 중 **넓은 스테이지에만** 적용,
                          // Fit 버튼은 완전 수용(full). 협폭 스테이지 예외는 G35(아래 FIT_NARROW_W).
// G35(외부 평가 2/3인 독립 실측 정정): 모바일 390×844 에서 자동 핏 하한 0.9 가 첫 화면을 통째로
// 비웠다 — 스테이지 실측 320×574 vs 캔버스 1434×1556, s=0.9 강제 + 세로 오버플로 좌상단 앵커(G19)
// 조합이 콘텐츠 없는 좌상단 슬라이스만 보여줘 가시 박스 0/24(장비 0/7, Fit 을 눌러야 비로소 보임).
// 첫 페인트에 구조가 보여야 한다 — 판독 하한의 취지(라벨 판독성)보다 우선하는 운영자 첫 동선 결함.
// 하한 0.9 는 원래 1280×800(스테이지 실측 816px)까지 튜닝된 값이므로, 스테이지 실폭이 이 값 미만이면
// 하한을 버리고 자연 배율(완전 수용 — Fit 버튼과 동일)로 수렴한다. 분기 기준이 뷰포트가 아니라
// **스테이지 실폭**인 이유: 사이드패널 접힘·분할 창 등 뷰포트와 무관하게 스테이지만 좁아지는 경우까지
// 전부 커버한다. 경계 700 은 실측 816(하한을 유지해야 할 최소 데스크톱)과 320(모바일) 사이 — 어느
// 쪽 오차로도 안전. 협폭 자연 배율(≈0.17)은 TINY_HIDE 미만이라 카드가 '상태색 블록 + 역배율
// 라벨(G22)' 요약 뷰로 자동 전환된다(좁은 화면 = 요약 뷰라는 G34 설계와 합류).
const FIT_NARROW_W = 700;
// G34(레드팀 P2-3 정정): 계층도 기본(Fit) 하한을 없앤다. G32 하한 0.40 은 컬럼-메이저 랩 이후
// 좁은 뷰포트(1280×800)에서 자연 fit(≈0.31)보다 큰 값으로 clamp 돼 콘텐츠가 스테이지보다 커지고
// 우측이 잘렸다(레드팀 재현: resize-roundtrip clippedRight=true, G002-RT-report.json). fit 은
// 항상 콘텐츠를 완전 수용하는 자연 배율을 그대로 쓴다(하한 상수 없음, 상한 FIT_MAX 만 유지) —
// 판독성은 하한 clamp 가 아니라 아래 LOD(TINY_HIDE)가 전담한다: 좋은 뷰포트(1600×1000)에선
// 자연 fit≈0.394 로 그룹/장비 라벨(--fs-19, 23px)이 렌더 ≥9px 를 지키고, 좁은 뷰포트(1280×800)
// 에선 자연 fit≈0.31 로 TINY_HIDE(0.32) 아래로 내려가 카드가 '상태색 블록 + 역배율 보정 라벨'
// 모드로 자동 전환된다(의도된 설계 — 좁은 화면은 요약 뷰, 하한 clamp 로 잘리는 것보다 낫다).
// 줌 배율이 이 값 이상이면 캔버스에 .is-zoomed 를 붙여 카드 세부(CPU/MEM 바·VM 수·sync 뱃지)를
// 노출한다. 기본 배율(≈0.40~0.78)에선 상태색+이름 중심으로 단순화(minor#3/#4). CSS 가 토글을 소비.
const ZOOM_DETAIL = 0.85;
// 이 배율 미만(줌 아웃 극단)에선 .is-tiny 를 붙여 카드 라벨을 전부 숨기고 '상태색 블록'만 남긴다(T2).
// G34: 이전엔 FIT_MIN(0.40) 페어로 비례 산정했으나 하한 제거로 더는 FIT_MIN 에 종속되지 않는다.
// 실측 기준(1600×1000 자연 fit≈0.394)이 여전히 이 값보다 커 기본 뷰에서 라벨이 유지되고,
// 좁은 뷰포트(1280×800, 자연 fit≈0.31)에선 이 값 아래로 내려가 라벨이 상태색 블록으로 전환된다.
const TINY_HIDE = 0.32;

/** 자동 핏 배율 결정(순수 — tests/topo-geometry 게이트용 export).
 *  raw = 자연(콘텐츠 완전 수용) 배율, stageW = 스테이지 실폭(els.fit.clientWidth).
 *  Fit 버튼(full)과 협폭 스테이지(G35: stageW < FIT_NARROW_W)는 자연 배율 그대로,
 *  넓은 스테이지의 자동 핏만 판독 하한(FIT_READABLE)을 지킨다. 확대 상한(FIT_MAX)은 공통. */
export function fitScale(raw, stageW, full) {
  const keepReadable = !full && stageW >= FIT_NARROW_W;
  return Math.min(FIT_MAX, keepReadable ? Math.max(raw, FIT_READABLE) : raw);
}

/**
 * 전체 맞춤 배율/오프셋. 예전 구현은 `Math.min(1, ...)` 로 확대를 막아 두어
 * 내용이 작을 때 캔버스가 스테이지를 못 채웠고, 오프셋도 max(0,·) 로 잘려
 * 세로로 긴 그래프가 왼쪽 위에 몰렸다. 지금은 항상 중앙 정렬한다.
 */
function fitView(full) {
  const f = els && els.fit;
  if (!f) return null;
  const W = f.clientWidth || 0;
  const H = f.clientHeight || 0;
  if (!W || !H || !dims.w || !dims.h) return null;
  const iw = Math.max(40, W - FIT_PAD.l - FIT_PAD.r);
  const ih = Math.max(40, H - FIT_PAD.t - FIT_PAD.b);
  const raw = Math.min(iw / dims.w, ih / dims.h);
  // 넓은 스테이지의 자동 핏만 판독 하한(FIT_READABLE)을 지킨다 — 넘친 영역은 팬/줌으로(힌트바 상시).
  // Fit 버튼(full=true)은 G34 완전 수용, 협폭 스테이지(W < FIT_NARROW_W)도 G35 완전 수용 —
  // 완전 수용이면 dims·s ≤ iw/ih 가 보장돼 아래 G19 좌상단 앵커 분기를 타지 않고 항상 중앙 정렬이다.
  const s = fitScale(raw, W, full);
  // 콘텐츠가 스테이지 안에 들어오면 중앙 정렬, 넘치면 좌/상단 여백에 붙인다.
  // 이렇게 하지 않으면 (iw - dims.w*s) 가 음수가 되며 중앙정렬이 왼쪽(회사 박스)을 잘랐다.
  // G19: 세로 스택 트리(폭은 들어오는데 높이가 넘치는 스크롤 리스트)는 수평 중앙 정렬이
  //     좌측 대공백을 만들었다 — 세로 오버플로 시 좌측 앵커(+24)로 트리 시작점을 붙인다.
  return {
    s,
    tx: (dims.h * s > ih + 1)
      ? FIT_PAD.l + 24
      : Math.max(FIT_PAD.l, FIT_PAD.l + (iw - dims.w * s) / 2),
    ty: Math.max(FIT_PAD.t, FIT_PAD.t + (ih - dims.h * s) / 2),
  };
}

function applyView() {
  const cv = els && els.canvas;
  if (!cv || !view) return;
  cv.style.left = view.tx.toFixed(1) + 'px';
  cv.style.top = view.ty.toFixed(1) + 'px';
  cv.style.transformOrigin = '0 0';
  cv.style.transform = 'scale(' + view.s.toFixed(4) + ')';
  // G22: 역배율 라벨 보정 계수(지도 엔진식 LOD) — 극단 줌아웃(.is-tiny)에서 라벨을 숨기는 대신
  //     식별 라벨 1개를 캔버스 좌표계에서 1/s 배로 키워 화면 렌더 크기를 일정하게 유지한다.
  //     상한 3.2: 그 아래(배율<0.2)는 박스 자체가 수 px 라 색 블록 미니맵이 자연스럽다.
  cv.style.setProperty('--zi', Math.min(1 / view.s, 3.2).toFixed(3));
  // 스케일 감지 → 세부 노출 클래스 토글(minor#3/#4). 기본 배율은 단순화, 줌 인 시 세부 노출.
  cv.classList.toggle('is-zoomed', view.s >= ZOOM_DETAIL);
  // 극단 줌아웃(<0.45): 라벨 전부 숨김 → 상태색 블록만(판독 불가한 <8px 텍스트 제거, T2).
  cv.classList.toggle('is-tiny', view.s < TINY_HIDE);
  // 최소 배율(핏이 더 작으면 그 값)에 도달하면 축소(−) 버튼 비활성화.
  if (els && els.zOut) {
    const atMin = view.s <= minScale() + 0.001;
    els.zOut.disabled = atMin;
    els.zOut.classList.toggle('is-disabled', atMin);
  }
  if (els && els.zIn) {
    const atMax = view.s >= FIT_MAX - 0.001;
    els.zIn.disabled = atMax;
    els.zIn.classList.toggle('is-disabled', atMax);
  }
}

/**
 * 확대 하한. 스펙은 0.25 고정이지만 플릿이 커지면 "핏" 배율이 0.25 보다 작아져
 * +/− 를 누르는 순간 화면이 튄다. 핏 배율이 더 작으면 그 값을 하한으로 쓴다.
 */
function minScale() {
  const f = fitView();
  return f && f.s < 0.25 ? f.s : 0.25;
}

function zoomAtPoint(mx, my, factor) {
  const v = view || fitView();
  if (!v) return;
  const ns = Math.max(minScale(), Math.min(MAX_SCALE, v.s * factor));
  const cx = (mx - v.tx) / v.s;
  const cy = (my - v.ty) / v.s;
  view = { s: ns, tx: mx - cx * ns, ty: my - cy * ns };
  userZoomed = true;
  applyView();
}

function zoomCenter(factor) {
  const f = els && els.fit;
  if (!f) return;
  const r = f.getBoundingClientRect();
  // 기준점은 '지금 실제로 보이는 콘텐츠 영역'(콘텐츠 ∩ 스테이지)의 중앙 — 콘텐츠가 좌/상단에
  // 앵커된 채 스테이지 중앙 기준으로 확대하면 보고 있던 카드가 화면 밖으로 밀려나
  // 버튼이 고장 난 것처럼 느껴진다.
  let ax = r.width / 2, ay = r.height / 2;
  const v = view || fitView();
  if (v && dims.w && dims.h) {
    const ix0 = Math.max(0, v.tx), iy0 = Math.max(0, v.ty);
    const ix1 = Math.min(r.width, v.tx + dims.w * v.s);
    const iy1 = Math.min(r.height, v.ty + dims.h * v.s);
    if (ix1 > ix0 && iy1 > iy0) { ax = (ix0 + ix1) / 2; ay = (iy0 + iy1) / 2; }
  }
  zoomAtPoint(ax, ay, factor);
}

/** 키보드 포커스를 받은 스테이지를 일정한 화면 픽셀만큼 이동한다. */
function panBy(dx, dy) {
  const v = view || fitView();
  if (!v) return;
  view = { s: v.s, tx: v.tx + dx, ty: v.ty + dy };
  userZoomed = true;
  applyView();
}

function resetView() {
  userZoomed = false;
  const v = fitView(true);   /* Fit 버튼 = 완전 수용 */
  if (v) { view = v; applyView(); }
}

/** 화면이 아직 레이아웃되지 않았을 때(clientWidth=0) 프레임을 두고 재시도. */
function scheduleFit(n) {
  if (n > 40) return;
  clearTimeout(fitTimer);
  fitTimer = setTimeout(() => {
    fitTimer = 0;
    if (!els) return;
    const v = fitView();
    if (v) { view = v; applyView(); } else scheduleFit(n + 1);
  }, 45);
}

function ensureFit() {
  if (!userZoomed || !view) {
    const v = fitView();
    if (v) { view = v; applyView(); } else scheduleFit(0);
  } else {
    applyView();
  }
}

/* ========================================================================
 * 7. 플로어 맵
 * ==================================================================== */

const FLOOR_COLS = 6;

/** 장비 수에 맞는 그리드. ≤30대는 기존 6열 레이아웃 그대로(레거시 look 보존),
 *  그 이상은 열을 늘리고 행 높이를 줄여 100% 안에 전부 담는다(107대 검증 — 71랙 이탈 결함 수정). */
function floorGrid(n) {
  const cols = n <= 30 ? FLOOR_COLS : Math.min(16, Math.ceil(Math.sqrt(n * 1.7)));
  return { cols, rows: Math.max(1, Math.ceil(n / cols)) };
}

/** '행,열'(1-base) 파스 — 장비 관리에서 입력한 실배치 좌표. 형식 밖이면 null(자동 배치). */
function parseFloorPos(s) {
  const m = /^\s*(\d{1,3})\s*[,\-]\s*(\d{1,3})\s*$/.exec(String(s || ''));
  if (!m) return null;
  const r = parseInt(m[1], 10);
  const c = parseInt(m[2], 10);
  return (r >= 1 && c >= 1) ? { r, c } : null;
}

/** 실배치 레이아웃 — 위치 지정 장비는 그 (행,열)에, 미지정 장비는 남는 칸에 자동 채움.
 *  같은 칸 중복 지정은 다음 빈 칸으로 밀어낸다(결정적). 반환 place[i] = {r,c} (1-base). */
function floorLayout(devs) {
  const posed = [];
  const rest = [];
  devs.forEach((d, i) => {
    const p = parseFloorPos(d.floorPos);
    if (p) posed.push({ i, r: p.r, c: p.c }); else rest.push(i);
  });
  const auto = floorGrid(devs.length);
  let cols = auto.cols;
  posed.forEach((p) => { if (p.c > cols) cols = Math.min(16, p.c); });
  const taken = new Set();
  const place = [];
  const clampC = (c) => Math.min(c, cols);
  posed.forEach((p) => {
    let r = p.r;
    let c = clampC(p.c);
    while (taken.has(r + ',' + c)) { c += 1; if (c > cols) { c = 1; r += 1; } }
    taken.add(r + ',' + c);
    place[p.i] = { r, c, pinned: true };
  });
  let fr = 1;
  let fc = 1;
  const nextFree = () => { while (taken.has(fr + ',' + fc)) { fc += 1; if (fc > cols) { fc = 1; fr += 1; } } };
  rest.forEach((i) => {
    nextFree();
    taken.add(fr + ',' + fc);
    place[i] = { r: fr, c: fc, pinned: false };
  });
  let rows = 1;
  place.forEach((p) => { if (p.r > rows) rows = p.r; });
  return { cols, rows, place };
}

/** (행,열) 셀 → % 좌표. 6열 이하는 레거시 스텝(15%/17%)과 동일. */
function floorCellPos(cell, g) {
  const cw = 90 / g.cols;
  const rh = Math.min(17, 84 / g.rows);
  const w = Math.min(10.5, cw - 1.2);
  const h = Math.min(12, rh - 1.5);
  return {
    left: (5 + (cell.c - 1) * cw), top: (8 + (cell.r - 1) * rh),
    w: w + '%', h: h + '%', wN: w, hN: h,
  };
}

// 실배치 좌표(floorPos)가 바뀌면 랙을 다시 놓아야 한다 — id 만 보던 시그니처에 위치 포함.
function floorSig(devs) {
  return devs.map((d) => d.id + '@' + (d.floorPos || '')).join(';');
}

/** 실배선 — 장비 mgmt IP 의 /24 프리픽스(=관리망)별로 랙 센터를 직교 체인으로 잇는다.
 *  경로는 랙 레이어 **밑**(z-index)에 깔려 교차 구간이 랙 뒤로 숨는다(바닥 배선 은유).
 *  스타일은 중립 팔레트 순환 — 상태색(앰버/레드)은 배선에 쓰지 않는다(상태는 랙·핑 소유). */
const CABLE_PALETTE = ['var(--accent)', 'var(--pos)', 'var(--accent-deep)', 'var(--muted)'];

function rebuildCables(devs, grid, centers) {
  const svg = els.cableSvg;
  if (!svg) return;
  while (svg.firstChild) svg.removeChild(svg.firstChild);
  const rh = Math.min(17, 84 / grid.rows);
  const X = (pct) => Math.round(pct * 10);     // % → viewBox 1000
  const Y = (pct) => Math.round(pct * 6.2);    // % → viewBox 620
  const groups = new Map();            // '172.30.1.0/24' → [rack index…] (그리드 순서 유지)
  devs.forEach((d, i) => {
    const ip = String(d.mgmt || '');
    if (!/^\d+\.\d+\.\d+\.\d+$/.test(ip)) return;
    const key = ip.replace(/\.\d+$/, '.0/24');
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(i);
  });
  let gi = 0;
  groups.forEach((idxs, key) => {
    if (idxs.length < 2) { gi += 1; return; }   // 단독 장비 망은 선 없음(색 배정만 소비)
    const col = CABLE_PALETTE[gi % CABLE_PALETTE.length];
    gi += 1;
    let dAttr = '';
    for (let k = 0; k + 1 < idxs.length; k += 1) {
      const a = centers[idxs[k]];
      const b = centers[idxs[k + 1]];
      if (a.row === b.row) {
        dAttr += 'M' + X(a.x) + ' ' + Y(a.y) + 'L' + X(b.x) + ' ' + Y(b.y);
      } else {
        // 출발 랙 행 아래 '통로' 로 내려가 목적 열까지 간 뒤 올라간다(직교 Z-라우팅).
        const lowRow = Math.min(a.row, b.row);
        const cy = 8 + lowRow * rh + (rh + (centers[idxs[k]].h)) / 2;
        dAttr += 'M' + X(a.x) + ' ' + Y(a.y) + 'L' + X(a.x) + ' ' + Y(cy)
          + 'L' + X(b.x) + ' ' + Y(cy) + 'L' + X(b.x) + ' ' + Y(b.y);
      }
    }
    const p = S('path', {
      d: dAttr, fill: 'none', 'stroke-width': '2',
      'stroke-linejoin': 'round', 'stroke-linecap': 'round', opacity: '.5',
    });
    p.style.stroke = col;
    const tt = S('title');
    tt.textContent = key;
    p.appendChild(tt);
    svg.appendChild(p);
  });
}

function rebuildFloor(devs) {
  const layer = els.rackLayer;
  while (layer.firstChild) layer.removeChild(layer.firstChild);
  // 실배치 우선 레이아웃 — 장비 관리의 (행,열) 지정을 그대로 쓰고, 미지정은 자동 채움.
  const layout = floorLayout(devs);
  const grid = { cols: layout.cols, rows: layout.rows };
  // 밀집 그리드(열>8)는 랙이 작아진다 — 회사 보조줄 숨김·코드 축소는 CSS(is-dense)가 담당.
  layer.classList.toggle('is-dense', grid.cols > 8);
  const centers = [];
  els.rackRefs = devs.map((d, i) => {
    const cell = layout.place[i];
    const p = floorCellPos(cell, grid);
    centers.push({ x: p.left + p.wN / 2, y: p.top + p.hN / 2, row: cell.r - 1, h: p.hN });
    const rack = E('button', 'map-rack sc-topo-rack', {
      type: 'button', 'data-topo-open': d.deviceId || d.id,
    });
    rack.style.left = p.left + '%'; rack.style.top = p.top + '%';
    rack.style.width = p.w; rack.style.height = p.h;
    // E1: 코드(EDGE-27) 우선 노출 + 회사명 보조 줄. 코드는 절단 금지(고유 번호가 살아남게), 회사는 말줄임.
    const lb = E('span', 'sc-topo-rack-label');
    const code = E('span', 'sc-topo-rack-code u-mono');
    const co = E('span', 'sc-topo-rack-co');
    lb.appendChild(code); lb.appendChild(co);
    rack.appendChild(lb);
    layer.appendChild(rack);

    const ping = E('span', 'map-ping sc-topo-ping');
    // 핑은 랙 중심 — 셀 크기가 그리드에 따라 변하므로 고정 오프셋(5.2/6) 대신 절반 좌표.
    ping.style.left = (p.left + p.wN / 2) + '%';
    ping.style.top = (p.top + p.hN / 2) + '%';
    const dot = E('span', 'map-ping-dot');
    dot.appendChild(ico('warningCircle', 13));
    ping.appendChild(dot);
    ping.hidden = true;
    layer.appendChild(ping);

    return { rack, label: lb, code, co, ping, dot, cell };
  });
  rebuildCables(devs, grid, centers);
}

function patchFloor(devs) {
  const refs = els.rackRefs || [];
  devs.forEach((d, i) => {
    const r = refs[i];
    if (!r) return;
    cls(r.rack, 'map-rack sc-topo-rack ' + tcls(d.tone) + (d.tone === 'neg' ? ' is-warn' : ''));
    // 툴팁에 배치 출처 명시 — 지정 좌표(R행-C열)인지 자동 채움인지 사용자가 구분한다.
    r.rack.title = (d.label || '') + ' · ' + (d.statusLabel || '')
      + (r.cell && r.cell.pinned
        ? ' · R' + r.cell.r + '-C' + r.cell.c
        : ' · ' + L('auto placement', '자동 배치'));
    // 코드(EDGE-27) 우선 노출, 회사명은 보조 줄(중복/공백이면 숨김). 툴팁엔 full 라벨 유지.
    const code = d.code || d.label || '';
    txt(r.code, code);
    txt(r.co, d.company || '');
    r.co.hidden = !d.company || d.company === code;
    const alert = d.tone === 'neg' || d.tone === 'warn';
    r.ping.hidden = !alert;
    if (alert) cls(r.dot, 'map-ping-dot ' + tcls(d.tone));
  });
}

function mapTransform(view3d, zoom) {
  const z = Number(zoom) || 1;
  return view3d
    ? 'perspective(1100px) rotateX(42deg) rotate(-6deg) scale(' + (z * 0.82) + ')'
    : 'scale(' + z + ')';
}

/* ========================================================================
 * 8. 우측 요약 행
 * ==================================================================== */

function rowsSig(pRows) {
  return pRows.map((p) => p.key + '|' + p.label).join(';');
}

function rebuildRows(pRows) {
  const c = els.rowsWrap;
  while (c.firstChild) c.removeChild(c.firstChild);
  rowRefs = pRows.map((p) => {
    const b = E('button', 'sc-topo-row', { type: 'button', 'data-topo-row': p.key });
    const ic = E('span', 'sc-topo-row-ico');
    ic.appendChild(ico(p.icon || 'building', 16));
    const bd = E('span', 'sc-topo-row-body');
    const t = E('span', 'sc-topo-row-title');
    t.textContent = p.label;
    const s = E('span', 'sc-topo-row-sub');
    bd.appendChild(t); bd.appendChild(s);
    const dot = E('span', 'sc-topo-dot');
    const cnt = E('span', 'sc-topo-row-count u-mono');
    b.appendChild(ic); b.appendChild(bd); b.appendChild(dot); b.appendChild(cnt);
    c.appendChild(b);
    return { node: b, sub: s, dot, count: cnt };
  });
}

/* ========================================================================
 * 8b. 상태 필터 딤 · 호버 툴팁 · 키보드 단축키 (G32)
 * ==================================================================== */

/** legend 칩 클릭으로 토글된 statusFilter 를 device 카드에 반영(비매칭 딤). 모든 render tick 에서
 *  재호출해도 안전 — DOM 재생성 없이 클래스만 토글하므로 패킷 애니/트랜지션을 건드리지 않는다. */
function applyStatusFilter() {
  if (!boxRefs) return;
  boxRefs.forEach((rec, id) => {
    if (rec.kind !== 'device') return;
    const box = boxDataMap.get(id);
    const dim = !!statusFilter && !!box && box.tone !== statusFilter;
    rec.node.classList.toggle('is-dim', dim);
  });
}

function updateLegendActive() {
  if (!els || !els.legend) return;
  Array.prototype.forEach.call(els.legend.children, (n) => {
    if (n.hasAttribute && n.hasAttribute('data-topo-legend')) {
      const active = n.getAttribute('data-topo-legend') === statusFilter;
      n.classList.toggle('is-active', active);
      n.setAttribute('aria-pressed', active ? 'true' : 'false');
    }
  });
}

function toneLabelText(tone) {
  return tone === 'pos' ? L('Operational', '가동')
    : (tone === 'warn' ? L('Degraded', '저하') : (tone === 'neg' ? L('Offline', '오프라인') : ''));
}

/** kind 별 툴팁 행 — 이름/상태·CPU·MEM·VM 수·경보 등 핵심만(REBUILD-SPEC 요구). box 는
 *  boxDataMap 의 최신값이라 patch 주기와 무관하게 항상 맞다. 지원 안 하는 kind 는 null.
 *  DOM 을 만지지 않는 순수 함수 — named export 로 회귀 게이트를 둔다(fitScale 전례). */
export function tooltipRows(box) {
  const rows = [];
  if (box.kind === 'device') {
    rows.push(['title', box.label || box.code || '']);
    rows.push(['sub', box.statusLabel || '']);
    if (box.cpu != null && !box.cpuNA) rows.push(['kv', 'CPU', box.cpu + '%']);
    if (box.mem != null && !box.memNA) rows.push(['kv', 'MEM', box.mem + '%']);
    if (box.vmLabel != null) rows.push(['kv', L('VMs', 'VM'), box.vmLabel]);
    if (box.alertN) rows.push(['kv', L('Alerts', '경보'), String(box.alertN)]);
  } else if (box.kind === 'node') {
    rows.push(['title', box.name || '']);
    rows.push(['sub', box.stateLabel || '']);
    if (box.cpu != null) rows.push(['kv', 'CPU', box.cpu + '%']);
    if (box.mem != null) rows.push(['kv', 'MEM', box.mem + '%']);
  } else if (box.kind === 'vm') {
    rows.push(['title', box.name || 'VM']);
    rows.push(['sub', box.state || '']);
    // #348: VM ip/node 칸의 의미는 출처 경로마다 다르다 — 라벨을 값 의미에 맞춰 분기한다.
    //  · 실데이터(avcli poller meta.topo) 경로는 compact=true. 게스트 IP가 있으면 IP를,
    //    없으면 ip 칸에 '8 vCPU · 16 GB' 자원 스펙을 싣는다(compute.js pushRealRow).
    //  · 시뮬·호스팅(비FT 게스트) VM 은 ip=실제 IP → 'IP'. node 는 배치 노드/서버 유형이
    //    정상이지만, 호스팅 VM 이 매칭 서버 없이 vcpu 만 있으면 node 칸에 '8 vCPU' 스펙이
    //    들어온다 → 이 경우만 '사양'.
    const specIp = !!box.compact && !box.ipIsAddress;
    const nodeIsSpec = !specIp && /vCPU/.test(box.node || '');
    if (box.node) rows.push(['kv', nodeIsSpec ? L('Spec', '사양') : L('Node', '노드'), box.node]);
    if (box.ip) rows.push(['kv', specIp ? L('Spec', '사양') : 'IP', box.ip]);
  } else if (box.kind === 'vmgroup') {
    rows.push(['title', 'VM ' + (box.count || 0)]);
    rows.push(['sub', (box.running != null ? box.running : 0) + '/' + (box.count || 0) + ' ' + L('running', '실행 중')]);
  } else if (box.kind === 'company') {
    rows.push(['title', box.label || '']);
    rows.push(['sub', toneLabelText(box.tone)]);
  } else if (box.kind === 'factory') {
    rows.push(['title', box.label || '']);
    rows.push(['sub', toneLabelText(box.tone)]);
    if (box.count != null) rows.push(['kv', L('Devices', '장비'), String(box.count)]);
  } else {
    return null;
  }
  return rows;
}

/** 툴팁 본문을 채운다. 지원 안 하는 kind 면 false 를 돌려 호출부가 숨기게 한다. */
function renderTooltip(box) {
  const rows = box && tooltipRows(box);
  if (!rows || !rows.length) return false;
  const tt = els.tooltip;
  while (tt.firstChild) tt.removeChild(tt.firstChild);
  rows.forEach((r) => {
    if (r[0] === 'title') {
      const d = E('div', 'sc-topo-tooltip-title'); d.textContent = r[1]; tt.appendChild(d);
    } else if (r[0] === 'sub') {
      if (!r[1]) return;
      const d = E('div', 'sc-topo-tooltip-sub'); d.textContent = r[1]; tt.appendChild(d);
    } else {
      const d = E('div', 'sc-topo-tooltip-kv');
      const k = E('span', 'sc-topo-tooltip-k'); k.textContent = r[1];
      const v = E('span', 'sc-topo-tooltip-v'); v.textContent = r[2];
      d.appendChild(k); d.appendChild(v); tt.appendChild(d);
    }
  });
  return true;
}

/** G34(레드팀 i18n): hidden 만 토글하면 이전 hover 시점 언어로 렌더된 tooltip 자식 텍스트가 DOM 에
 *  남아, 언어 전환 스캔이 '잔존 한글'로 오탐한다(예: 저하 tone 장비를 KO 로 호버한 뒤 EN 전환).
 *  hide 시 내용도 비워 다음 호버가 항상 현재 언어로 새로 렌더하게 한다. */
function hideTooltip() {
  if (!els || !els.tooltip) return;
  els.tooltip.hidden = true;
  while (els.tooltip.firstChild) els.tooltip.removeChild(els.tooltip.firstChild);
  // 내용을 비웠으니 재렌더 캐시도 무효화 — 다음 호버는 항상 새로 렌더한다.
  ttCacheBox = null; ttCacheKey = '';
}

/** 스테이지 경계 안으로 클램프 — 커서 오른쪽 아래에 붙이되 넘치면 반대편으로 뒤집는다. */
function positionTooltip(clientX, clientY) {
  const f = els.fit;
  const r = f.getBoundingClientRect();
  const tw = els.tooltip.offsetWidth || 180;
  const th = els.tooltip.offsetHeight || 56;
  let x = clientX - r.left + 16;
  let y = clientY - r.top + 16;
  if (x + tw > r.width - 6) x = clientX - r.left - tw - 16;
  if (y + th > r.height - 6) y = clientY - r.top - th - 16;
  x = Math.max(6, Math.min(r.width - tw - 6, x));
  y = Math.max(6, Math.min(r.height - th - 6, y));
  els.tooltip.style.left = x + 'px';
  els.tooltip.style.top = y + 'px';
}

function bindTooltip() {
  const f = els.fit;
  onBoxOver = (e) => {
    if (dragging || pressed) { hideTooltip(); return; }
    const n = e.target && e.target.closest ? e.target.closest('.sc-topo-box') : null;
    if (!n || !f.contains(n)) { hideTooltip(); return; }
    const id = n.dataset ? n.dataset.topoBox : null;
    const box = id ? boxDataMap.get(id) : null;
    if (!box) { hideTooltip(); return; }
    // 같은 box(id + 데이터 버전 + 언어)면 DOM 재생성 없이 위치만 따라간다.
    const lang = (ctx.store && ctx.store.getState().lang) || 'ko';
    const key = id + '|' + lang;
    if (ttCacheBox !== box || ttCacheKey !== key) {
      if (!renderTooltip(box)) { hideTooltip(); return; }
      ttCacheBox = box; ttCacheKey = key;
    }
    els.tooltip.hidden = false;
    positionTooltip(e.clientX, e.clientY);
  };
  onBoxOut = (e) => {
    if (!e.relatedTarget || !f.contains(e.relatedTarget)) hideTooltip();
  };
  f.addEventListener('mousemove', onBoxOver);
  f.addEventListener('mouseleave', onBoxOut);
}

/** +/− 확대·축소, 0 또는 F 로 맞춤. 입력창 포커스 중이면 무시(검색창 등과 충돌 방지).
 *  G34(레드팀 P2-4): 브라우저 단축키(Ctrl/Cmd + '+'/'-' = 페이지 확대·축소, Alt+화살표 등)와
 *  충돌하지 않도록 수정자 키가 눌린 입력은 그대로 브라우저에 넘긴다(캔버스 줌 불변). */
function bindKeyboard() {
  onKeyDown = (e) => {
    if (!els || (els.stageWrap && els.stageWrap.hidden)) return;
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    const t = e.target;
    const tag = t && t.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (t && t.isContentEditable)) return;
    if (e.key === '+' || e.key === '=') { e.preventDefault(); zoomCenter(1.2); }
    else if (e.key === '-' || e.key === '_') { e.preventDefault(); zoomCenter(1 / 1.2); }
    else if (e.key === '0' || e.key === 'f' || e.key === 'F') { e.preventDefault(); resetView(); }
    else if (t === els.fit && e.key === 'ArrowLeft') { e.preventDefault(); panBy(64, 0); }
    else if (t === els.fit && e.key === 'ArrowRight') { e.preventDefault(); panBy(-64, 0); }
    else if (t === els.fit && e.key === 'ArrowUp') { e.preventDefault(); panBy(0, 64); }
    else if (t === els.fit && e.key === 'ArrowDown') { e.preventDefault(); panBy(0, -64); }
  };
  window.addEventListener('keydown', onKeyDown);
}
/* ========================================================================
 * 9. 이벤트 위임
 * ==================================================================== */

/**
 * 접힘 맵은 3-상태다(없음=기본값 / true=접힘 / false=펼침).
 * 장비 가지는 기본이 "접힘"이라 토글이 값을 지우면 안 되고, 반드시 명시값을 써야 한다.
 * 그래서 눌린 손잡이가 알려 준 다음 상태(data-topo-next)를 그대로 저장한다.
 */
function setCollapsed(key, next) {
  const st = ctx.store.getState();
  const map = Object.assign({}, st.collapsed || {});
  map[key] = !!next;
  ctx.store.setState({ collapsed: map });
}

function toggleFrom(node, key) {
  const a = node && node.getAttribute('data-topo-next');
  if (a === '0' || a === '1') { setCollapsed(key, a === '1'); return; }
  const st = ctx.store.getState();
  setCollapsed(key, !(st.collapsed || {})[key]);
}

function hit(target, sel) {
  const n = target && target.closest ? target.closest(sel) : null;
  return n && root && root.contains(n) ? n : null;
}

function handleClick(ev) {
  // 팬 드래그 끝에 따라오는 click 은 무시한다(끌다 놓았는데 상세로 튀지 않도록).
  if (movedFar) { movedFar = false; return; }
  const t = ev.target;
  if (!t) return;
  let n;

  if ((n = hit(t, '[data-topo-view]'))) {
    const v = n.getAttribute('data-topo-view');
    if (v !== ctx.store.getState().topoView) ctx.store.setState({ topoView: v });
    return;
  }
  if ((n = hit(t, '[data-topo-legend]'))) {
    const tone = n.getAttribute('data-topo-legend');
    statusFilter = statusFilter === tone ? null : tone;
    applyStatusFilter();
    updateLegendActive();
    return;
  }
  if ((n = hit(t, '[data-topo-zoom]'))) {
    const a = n.getAttribute('data-topo-zoom');
    if (a === 'in') zoomCenter(1.2);
    else if (a === 'out') zoomCenter(1 / 1.2);
    else resetView();
    return;
  }
  if ((n = hit(t, '[data-topo-toggle]'))) {
    ev.stopPropagation();
    toggleFrom(n, n.getAttribute('data-topo-toggle'));
    return;
  }
  if (hit(t, '[data-topo-clearfocus]')) { ctx.store.setState({ topoFocus: null }); return; }
  if ((n = hit(t, '[data-topo-focus]'))) {
    const co = n.getAttribute('data-topo-focus');
    const cur = ctx.store.getState().topoFocus;
    ctx.store.setState({ topoFocus: cur === co ? null : co });
    return;
  }
  if ((n = hit(t, '[data-topo-row]'))) { toggleFrom(n, n.getAttribute('data-topo-row')); return; }
  if ((n = hit(t, '[data-topo-3d]'))) {
    ctx.store.setState({ view3d: n.getAttribute('data-topo-3d') === '1' });
    return;
  }
  if ((n = hit(t, '[data-topo-mzoom]'))) {
    const a = n.getAttribute('data-topo-mzoom');
    ctx.store.setState((s) => ({
      zoom: a === 'reset' ? 1
        : (a === 'in' ? Math.min(1.5, (s.zoom || 1) + 0.15) : Math.max(0.7, (s.zoom || 1) - 0.15)),
    }));
    return;
  }
  if ((n = hit(t, '[data-topo-open]'))) {
    const id = n.getAttribute('data-topo-open');
    if (id && typeof ctx.goDetail === 'function') ctx.goDetail(id);
  }
}

function bindPanZoom() {
  const f = els.fit;

  onWheel = (e) => {
    if (els && els.stageWrap && els.stageWrap.hidden) return;
    e.preventDefault();
    hideTooltip();
    const r = f.getBoundingClientRect();
    zoomAtPoint(e.clientX - r.left, e.clientY - r.top, e.deltaY < 0 ? 1.12 : 1 / 1.12);
  };
  /* 팬은 배경이든 박스 위든 어디서 시작해도 되게 한다. 4px 넘게 움직인 뒤에야
     드래그로 승격하고(그 전엔 그냥 클릭), 드래그였다면 뒤따르는 click 을 삼킨다.
     단, 작은 컨트롤 버튼(줌 +/−/Fit·접기·범례 칩) 위에서 시작한 제스처는 드래그로
     승격하지 않는다 — 승격되면 수 px 손떨림에도 클릭이 팬으로 바뀌고 뒤따르는 click 이
     삼켜져 '버튼이 안 눌리는' 결함이 된다(실측 재현: 5px 흔들림 클릭 시 배율 불변).
     장비 카드(data-topo-open 버튼)는 컨트롤이 아니라 캔버스의 일부로 취급해
     카드 위에서도 팬을 시작할 수 있게 둔다. */
  onPointerDown = (e) => {
    pressed = true; movedFar = false; pressOnBtn = false;
    dragging = false;
    f.classList.remove('is-grabbing');
    pressX = lastX = e.clientX; pressY = lastY = e.clientY;
    dragId = e.pointerId != null ? e.pointerId : null;
    const btn = e.target && e.target.closest ? e.target.closest('button') : null;
    if (btn && !btn.hasAttribute('data-topo-open')) pressOnBtn = true;
  };
  onPointerMove = (e) => {
    if (!pressed || pressOnBtn) return;
    if (!dragging) {
      if (Math.abs(e.clientX - pressX) + Math.abs(e.clientY - pressY) < 4) return;
      dragging = true; movedFar = true;
      hideTooltip();
      f.classList.add('is-grabbing');
      if (dragId != null) {
        try { if (f.setPointerCapture) f.setPointerCapture(dragId); } catch (err) { /* noop */ }
      }
    }
    if (!view) { const v = fitView(); if (!v) return; view = v; }
    view.tx += e.clientX - lastX;
    view.ty += e.clientY - lastY;
    lastX = e.clientX; lastY = e.clientY;
    userZoomed = true;
    applyView();
  };
  onPointerUp = (e) => {
    pressed = false;
    if (!dragging) { dragId = null; return; }
    dragging = false;
    try {
      const id = (e && e.pointerId != null) ? e.pointerId : dragId;
      if (f.releasePointerCapture && id != null) f.releasePointerCapture(id);
    } catch (err) { /* noop */ }
    dragId = null;
    f.classList.remove('is-grabbing');
    // 드래그 뒤 따라오는 click 은 handleClick 이 movedFar 로 삼키는데, click 이 안 오는
    // 경로(캡처 미적용 상태로 f 밖에서 놓는 경우 등)에선 movedFar 가 남아 다음 클릭
    // 1회가 삼켜진다 — 짧은 타임아웃 뒤 해제해 두 경로 모두 안전하게 한다.
    // (click 은 pointerup 과 같은 태스크에서 먼저 디스패치되므로 정상 경로의 click 삼키기는 유지됨)
    setTimeout(() => { movedFar = false; }, 0);
  };
  onDblClick = (e) => {
    // 버튼 위 더블클릭은 리셋이 아니다 — '+/−' 연타가 dblclick 으로 인식돼 Fit 으로
    // 튀는 결함(실측 재현: Fit → + → 빠른 + 가 배율 0.404 → 0.281 로 역행) 차단.
    // 리셋은 배경 더블클릭에만 적용한다.
    if (e.target && e.target.closest && e.target.closest('button')) return;
    resetView();
  };
  onResize = () => { if (!userZoomed) ensureFit(); };

  f.addEventListener('wheel', onWheel, { passive: false });
  f.addEventListener('pointerdown', onPointerDown);
  f.addEventListener('pointermove', onPointerMove);
  f.addEventListener('pointerup', onPointerUp);
  f.addEventListener('pointercancel', onPointerUp);
  f.addEventListener('dblclick', onDblClick);
  window.addEventListener('resize', onResize);
}

/* ========================================================================
 * 10. 모듈 export
 * ==================================================================== */

export default {
  key: 'topology',
  title: { en: 'Topology', ko: '토폴로지' },
  icon: 'crop',

  init(rootEl, context) {
    root = rootEl;
    ctx = context;
    view = null; userZoomed = false;
    dragging = false; pressed = false; movedFar = false; pressOnBtn = false; dragId = null;
    lastTreeSig = ''; lastFloorSig = ''; lastPaneSig = ''; lastRowsSig = '';
    boxRefs = new Map(); linkRefs = new Map(); rowRefs = []; svgEl = null;
    boxDataMap = new Map(); statusFilter = null; knobRefs = [];

    els = buildShell();
    els.rackRefs = [];
    root.appendChild(els.wrap);

    onClick = handleClick;
    root.addEventListener('click', onClick);
    bindPanZoom();
    bindTooltip();
    bindKeyboard();
  },

  render(state, context) {
    if (!els) return;
    if (context) ctx = context;

    let topo = null;
    try {
      const fn = (ctx.model && ctx.model.buildTopo) || buildTopo;
      topo = fn(state);
    } catch (e) {
      console.warn('[topology] buildTopo failed:', e);
      return;
    }
    if (!topo || !Array.isArray(topo.boxes) || !Array.isArray(topo.links)) return;

    // G32: 최신 박스 데이터 캐시 — 호버 툴팁이 patch 주기와 무관하게 이걸 읽는다.
    boxDataMap = new Map();
    topo.boxes.forEach((b) => boxDataMap.set(b.id, b));

    /* ---- 헤더 ---- */
    txt(els.title, L('Topology', '토폴로지'));
    txt(els.sub, L('Company → factory → cluster → node hierarchy',
      '회사 → 공장 → 클러스터 → 노드 계층 구조'));

    const legendSig = (state.lang || 'ko') + '|' + (topo.legend || []).length;
    if (els.legend.dataset.sig !== legendSig) {
      els.legend.dataset.sig = legendSig;
      while (els.legend.firstChild) els.legend.removeChild(els.legend.firstChild);
      // G32: 상태 필터 — legend 를 클릭 가능 칩으로. 클릭한 상태만 선명, 나머지 장비 카드는 딤.
      (topo.legend || []).forEach((g) => {
        // 토글 칩 — #389 전례대로 aria-pressed 를 생성 시 굽고 updateLegendActive 에서 갱신(#430).
        const it = E('button', 'sc-topo-legend-item', { type: 'button', 'data-topo-legend': g.tone, 'aria-pressed': 'false' });
        it.appendChild(E('span', 'sc-topo-dot ' + tcls(g.tone)));
        const tx = E('span', 'sc-topo-legend-txt');
        tx.textContent = g.label;
        it.appendChild(tx);
        it.title = L('Filter cards by status', '상태별 카드 필터');
        els.legend.appendChild(it);
      });
    }
    updateLegendActive();
    const pane = state.topoView === 'floor' ? 'floor' : 'tree';
    els.stageWrap.hidden = pane !== 'tree';
    els.floor.hidden = pane !== 'floor';

    /* ---- 계층도 ---- */
    if (pane === 'tree') {
      const sig = treeSig(topo);
      if (sig !== lastTreeSig) {
        lastTreeSig = sig;
        rebuildCanvas(topo);
        patchCanvas(topo);
        ensureFit();
      } else {
        patchCanvas(topo);
        // G35: `|| !view` — scheduleFit 이 40프레임(≈1.8s) 재시도 후 포기하면 view=null 이
        //     고착돼 무변환(scale 1) 캔버스가 남을 수 있다(늦은 레이아웃·숨김 탭 복귀 부류).
        //     지오메트리가 안 바뀌어도 view 가 없으면 다음 tick 렌더에서 핏을 재시도해 복구한다.
        if (lastPaneSig !== pane || !view) ensureFit();
      }
      // G32: 상태 필터 딤 — patch 주기마다(값이 바뀔 수 있으므로) 재적용해 tick 에도 생존한다.
      applyStatusFilter();

      if (topo.focus) {
        els.focusWrap.hidden = false;
        els.focusDot.style.background = topo.focusColor || 'var(--muted)';
        els.focusBtn.style.setProperty('--sc-topo-co', topo.focusColor || 'var(--line)');
        txt(els.focusName, topo.focus);
        txt(els.focusHint, topo.focusLabel);
        els.focusBtn.title = topo.focusLabel || '';
      } else {
        els.focusWrap.hidden = true;
        // G34(레드팀 i18n): 숨긴 뒤에도 focusName/focusHint 텍스트 노드가 DOM 에 남아 있으면
        // 언어 전환 스캔(TreeWalker 는 hidden 여부와 무관하게 텍스트를 순회)이 이전 언어 문구를
        // '잔존'으로 오탐한다. hidden 전환 시 텍스트도 함께 비운다.
        txt(els.focusName, '');
        txt(els.focusHint, '');
      }
      txt(els.zoomHint, topo.zoomHint);
    }

    /* ---- 플로어 맵 ---- */
    if (pane === 'floor') {
      const devs = topo.boxes.filter((b) => b.kind === 'device');
      const fsig = floorSig(devs);
      if (fsig !== lastFloorSig) { lastFloorSig = fsig; rebuildFloor(devs); }
      patchFloor(devs);
      els.mapStage.style.transform = mapTransform(!!state.view3d, state.zoom);
      els.b2d.classList.toggle('is-active', !state.view3d);
      els.b3d.classList.toggle('is-active', !!state.view3d);
      txt(els.mapTitleTxt, topo.focus || L('Server room · floor view', '서버룸 · 플로어 뷰'));
      // 플로어 칩은 '장비(rack)' granularity — 우측 사이드바 '물리 노드'(90)와 다른 총계임을 단위 라벨로
      // 못박아 '어느 총계가 진짜' 마찰을 해소한다(기기 뷰 52 vs 물리노드 90, minor). 칩 시각비중은 CSS 로 종속.
      txt(els.stNodes.txt, String(devs.length) + L(' dev', ' 장비'));
      txt(els.stHealthy.txt, String(devs.filter((d) => d.tone === 'pos').length));
      txt(els.stAlert.txt, String(devs.filter((d) => d.tone !== 'pos').length));
      txt(els.mapLiveTxt, state.source === 'live' ? 'Live' : L('Sim', '시뮬'));
    }
    lastPaneSig = pane;

    /* ---- 우측 요약 ---- */
    const sum = topo.summary || { nodes: 0, healthy: 0, alerts: 0, criticals: 0 };
    txt(els.cNodes.val, String(sum.nodes));
    // E2: 이 값은 물리 FT 노드(node0/node1) 합계(≈90)다. #/nodes(장비 52)의 '노드'와 겹치지 않게
    //     단위를 '물리 노드'로 못박는다 — 90(물리 노드) vs 52(장비)가 모순처럼 읽히던 오버로드 해소.
    txt(els.cNodes.lbl, L('Nodes', '물리 노드'));
    txt(els.cHealthy.val, String(sum.healthy));
    txt(els.cHealthy.lbl, L('Healthy', '정상'));
    txt(els.cAlerts.val, String(sum.alerts));
    txt(els.cAlerts.lbl, L('Alerts', '경보'));
    // 총합은 중립(잉크) — red 는 '심각 N' 칩에만(minor#1).
    cls(els.cAlerts.val, 'sc-topo-counter-val u-mono');
    const critN = sum.criticals || 0;
    els.cAlertCrit.hidden = critN <= 0;
    if (critN > 0) txt(els.cAlertCrit, L('critical ', '심각 ') + critN);

    txt(els.grpLbl, topo.groupLabel);
    const pRows = topo.pRows || [];
    const rsig = rowsSig(pRows);
    if (rsig !== lastRowsSig) { lastRowsSig = rsig; rebuildRows(pRows); }
    pRows.forEach((p, i) => {
      const r = rowRefs[i];
      if (!r) return;
      txt(r.sub, p.statusLabel + (p.collapsed ? ' · ' + L('collapsed', '접힘') : ''));
      cls(r.dot, 'sc-topo-dot ' + tcls(p.tone));
      // E1: 공장별 합계는 '장비' 수(∑=52)라 단위를 명시한다 — 상단 카운터 '노드'(물리 노드, 90)와
      //     같은 패널에서 단위가 섞여 90↔52 가 모순처럼 읽히던 문제 해소('90 노드' vs 'N 장비/대').
      txt(r.count, p.count + L(' dev', '대'));
      r.node.classList.toggle('is-collapsed', !!p.collapsed);
      r.node.setAttribute('data-topo-next', p.collapsed ? '0' : '1');
    });
    txt(els.hintTxt, topo.hint);
    els.fit.setAttribute('aria-label', L(
      'Topology canvas. Use arrow keys to pan, plus and minus to zoom, and 0 or F to fit.',
      '토폴로지 캔버스. 방향키로 이동하고 +·-로 확대·축소하며 0 또는 F로 맞춥니다.',
    ));
    // T2: 한 줄 요약을 표시하되 상세 안내는 hover 툴팁으로 보존(정보 손실 없이 밀도만 축소).
    if (topo.hintTip) els.hintTxt.title = topo.hintTip;
  },

  destroy() {
    if (root && onClick) root.removeEventListener('click', onClick);
    if (els && els.fit) {
      const f = els.fit;
      if (onWheel) f.removeEventListener('wheel', onWheel);
      if (onPointerDown) f.removeEventListener('pointerdown', onPointerDown);
      if (onPointerMove) f.removeEventListener('pointermove', onPointerMove);
      if (onPointerUp) { f.removeEventListener('pointerup', onPointerUp); f.removeEventListener('pointercancel', onPointerUp); }
      if (onDblClick) f.removeEventListener('dblclick', onDblClick);
      if (onBoxOver) f.removeEventListener('mousemove', onBoxOver);
      if (onBoxOut) f.removeEventListener('mouseleave', onBoxOut);
    }
    if (onResize) window.removeEventListener('resize', onResize);
    if (onKeyDown) window.removeEventListener('keydown', onKeyDown);
    if (fitTimer) { clearTimeout(fitTimer); fitTimer = 0; }
    onClick = onWheel = onPointerDown = onPointerMove = onPointerUp = onDblClick = onResize = null;
    onKeyDown = onBoxOver = onBoxOut = null;
    boxRefs.clear(); linkRefs.clear(); rowRefs = []; svgEl = null;
    boxDataMap = new Map(); statusFilter = null; knobRefs = [];
    if (root) { while (root.firstChild) root.removeChild(root.firstChild); }
    root = null; ctx = null; els = null; view = null; userZoomed = false;
    dragging = false; pressed = false; movedFar = false; pressOnBtn = false; dragId = null;
    lastTreeSig = ''; lastFloorSig = ''; lastPaneSig = ''; lastRowsSig = '';
  },
};
