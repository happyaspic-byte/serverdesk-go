// js/screens/overview.js — S1: 개요(Overview) 화면
// REBUILD-SPEC.md §1.1(14블록) / §5.1(모듈 인터페이스) / §5.5(sc-ov- · data-ov-* 접두) 준수.
// - init: DOM 1회 생성 + root 클릭 위임 1개 등록
// - render: 값만 patch(트랜지션/스파크라인 리셋 방지). 리스트는 key 기반 재조정.
// - destroy: 리스너 해제 + 참조 정리
// 의존: js/model/compute.js(ctx.getModel), js/util/{dom,svg,fmt,icon}.js — 다른 screens는 import 금지.

const KEY = 'overview';

/* ===========================================================================
 * 모듈 로컬 상태
 * ======================================================================== */
let C = null;        // ctx
let ROOT = null;     // section.screen
let N = {};          // 노드 참조 맵
let TEXTS = [];      // [node, en, ko] 정적 라벨(언어 전환 시 갱신)
let onClick = null;  // 위임 핸들러
let onKeydown = null; // 키보드 위임 핸들러(라이브 로그 접이식 헤더)
let lastLang = null;
let logOpen = false; // 라이브 로그 tail 접이식 — 기본 접힘(minor#2)

/* ===========================================================================
 * 소소한 헬퍼
 * ======================================================================== */
const DASH = '—';

function el(tag, attrs, children) {
  if (C && C.util && C.util.dom && typeof C.util.dom.el === 'function') return C.util.dom.el(tag, attrs || {}, children || []);
  // dom.js 로드 실패 시 최소 폴백
  const node = document.createElement(tag);
  Object.entries(attrs || {}).forEach(([k, v]) => {
    if (v == null || v === false) return;
    if (k === 'class') node.className = v;
    else if (k === 'text') node.textContent = v;
    else node.setAttribute(k, v === true ? '' : v);
  });
  (Array.isArray(children) ? children : [children]).forEach((c) => {
    if (c == null || c === false) return;
    node.appendChild(typeof c === 'object' ? c : document.createTextNode(String(c)));
  });
  return node;
}

function L(en, ko) {
  return C && typeof C.L === 'function' ? C.L(en, ko) : ko;
}

/** 정적 라벨 등록 — 언어 전환 시 render에서 일괄 갱신 */
function t(node, en, ko) {
  TEXTS.push([node, en, ko]);
  node.textContent = L(en, ko);
  return node;
}

function ic(name, size) {
  const f = C && C.util && C.util.icon;
  if (typeof f !== 'function') return el('span', { class: 'sc-ov-ic-fb' });
  try { return f(name, { size: size || 14 }); } catch (e) { return el('span', { class: 'sc-ov-ic-fb' }); }
}

/** 아이콘 슬롯 교체(값 patch 대상) */
function setIcon(slot, name, size) {
  if (!slot) return;
  if (slot.dataset.ovIcon === String(name || '')) return;
  slot.dataset.ovIcon = String(name || '');
  while (slot.firstChild) slot.removeChild(slot.firstChild);
  if (name) slot.appendChild(ic(name, size));
}

const TONE_CLS = { pos: 'is-pos', warn: 'is-warn', neg: 'is-neg', info: 'is-info', mut: 'u-muted' };
function toneCls(tone) { return TONE_CLS[tone] || 'u-muted'; }

/** 상태/톤 클래스만 교체(다른 클래스 보존) */
function setToneClass(node, base, tone) {
  if (!node) return;
  const cls = base + ' ' + toneCls(tone);
  if (node.className !== cls) node.className = cls;
}

function setText(node, v) {
  if (!node) return;
  const s = v == null ? '' : String(v);
  if (node.textContent !== s) node.textContent = s;
}

function show(node, on) {
  if (!node) return;
  if (node.hidden === !on) return;
  node.hidden = !on;
}

/**
 * key 기반 리스트 재조정. 노드를 재사용하므로 트랜지션/hover 상태가 유지된다.
 * create(item,key) → {node, …refs}, update(rec, item, i)
 */
function syncList(container, items, keyOf, create, update) {
  if (!container) return;
  const map = container.__ovRecs || (container.__ovRecs = new Map());
  const seen = new Set();
  let prev = null;
  (items || []).forEach((item, i) => {
    const key = String(keyOf(item, i));
    seen.add(key);
    let rec = map.get(key);
    if (!rec) { rec = create(item, key); map.set(key, rec); }
    update(rec, item, i);
    const ref = prev ? prev.nextSibling : container.firstChild;
    if (rec.node !== ref) container.insertBefore(rec.node, ref);
    prev = rec.node;
  });
  map.forEach((rec, key) => {
    if (seen.has(key)) return;
    if (rec.node && rec.node.parentNode) rec.node.parentNode.removeChild(rec.node);
    map.delete(key);
  });
}

/* ===========================================================================
 * 공용 조각 빌더
 * ======================================================================== */
function cardHead(titleEn, titleKo, iconName, extra) {
  const head = el('div', { class: 'card-head' }, [
    iconName ? el('span', { class: 'sc-ov-hd-ic u-muted' }, [ic(iconName, 15)]) : null,
    // 카드 제목은 h2 — 실측상 문서에 h1 1개뿐이고 h2/h3 가 0개라 스크린리더가
    // 개요의 구조를 전혀 못 읽었다. 시각 스타일은 .card-title 이 그대로 운반한다.
    t(el('h2', { class: 'card-title' }), titleEn, titleKo),
    extra || null,
  ]);
  return head;
}

function linkMore(view, en, ko) {
  return el('button', { class: 'sc-ov-more', type: 'button', 'data-ov-goto': view }, [
    t(el('span', {}), en, ko),
    ic('chevronDown', 12),
  ]);
}

/** 라이브 로그 토글의 키보드 활성화 판정 — 토글(role=button, data-ov-logtoggle) 자신에 포커스가
 *  있을 때의 Enter/Space 만 토글한다. '콘솔 열기' 버튼(linkMore)은 #582 이후 토글의 형제라 그
 *  Enter/Space 는 네이티브 클릭(data-ov-goto)이 담당한다 — closest 는 자신도 매칭하므로
 *  결과가 target 자신일 때만 토글에 포커스가 있는 것으로 본다. */
export function isLogToggleKey(e) {
  if (!e || (e.key !== 'Enter' && e.key !== ' ' && e.key !== 'Spacebar')) return false;
  const t = e.target;
  return !!(t && typeof t.closest === 'function' && t.closest('[data-ov-logtoggle]') === t);
}

/** 라이브 로그 tail 행의 메시지 텍스트 — 모델(compute.js collectTraps)이 desc 에 '⚡ ' 를
 *  구워 납품하므로 뷰가 trap 행에 또 붙이면 '⚡ ⚡ …' 가 된다(#13). incidents 로그 뷰처럼
 *  모델 msg 를 그대로 쓴다(모델 접두를 빼면 incidents 로그 뷰·툴팁까지 바뀌므로 뷰 쪽만 수정). */
export function logRowMsg(g) {
  return (g && g.msg) || '';
}

/** 라이브 로그 tail 행의 syncList 키 — decorate 가 납품하는 경보·이벤트별 고유키(ackKey)를 쓴다.
 *  g.id 는 '장비 id' 라 트랩·경보(compute.js collectTraps/collectAlerts 의 id: s.id)가
 *  같은 장비에서 여러 건 들어오면 전부 같은 키로 붕괴해 1행만 남는다(#12).
 *  인덱스 혼합 키는 금지 — 새 로그 유입 때 전 행 키가 밀려 매 폴 전량 재생성되는 회귀(아래 주석). */
export function logRowKey(g) {
  return String((g && (g.ackKey || g.id)) || '');
}

/** 라이브 로그 tail 펼침/접힘 — 클릭·키보드 공용. aria-expanded 를 펼침 상태와 동기화한다. */
function toggleLog() {
  logOpen = !logOpen;
  if (N.console) N.console.classList.toggle('is-collapsed', !logOpen);
  if (N.logCaret) N.logCaret.classList.toggle('is-open', logOpen);
  if (N.logToggle) N.logToggle.setAttribute('aria-expanded', logOpen ? 'true' : 'false');
}

/* ===========================================================================
 * init — DOM 1회 생성
 * ======================================================================== */
function init(root, ctx) {
  C = ctx;
  ROOT = root;
  N = {};
  TEXTS = [];
  lastLang = null;

  const wrap = el('div', { class: 'sc-ov' });

  /* ── 0. 페이지 헤더 ── */
  // 플릿 총평 배지 — 폴러가 내려주는 최상위 verdict 를 UI 가 통째로 버리고 있었다(감사 P0).
  // compute.buildModel 이 같은 신호를 재도출한 fleetVerdict 를 페이지 제목 옆에 상시 노출한다.
  // '지금 이 플릿이 정상인가'에 대한 단일 앵커 — 카드 하나가 아니라 화면 전체의 판정이다.
  N.verdict = el('span', { class: 'u-badge sc-ov-verdict' });
  N.verdictWhy = el('span', { class: 'sc-ov-verdict-why u-muted' });
  wrap.appendChild(el('header', { class: 'sc-ov-head' }, [
    el('div', { class: 'sc-ov-head-top' }, [
      t(el('h1', { class: 'sc-ov-title' }), 'Overview', '개요'),
      N.verdict,
    ]),
    t(el('p', { class: 'sc-ov-sub u-muted' }),
      'Fault-tolerant fleet at a glance', '이중화 플릿 현황 요약'),
    N.verdictWhy,
  ]));

  /* ── 1행: 주의 필요 / 히트맵 / FT 이중화 ── */
  const row1 = el('div', { class: 'sc-ov-row1' });

  // (1) 주의 필요
  // 카운트는 중립(수량은 심각도가 아님) — 심각도 신호는 행별 상태 도트/칩이 운반.
  // 위계 교정(감사 P0): 개요에서 가장 큰 활자가 '동기화 2/2'·'라이선스 D-59' 같은 안전 정보였고
  // 심각 경보 본문은 12px/400 으로 화면 하단에 깔려 있었다 — 크기·굵기·위치 3축 모두 역전.
  // 위험 수치를 히어로 슬롯으로 올려 '가장 큰 숫자 = 지금 가장 위험한 것'이 되게 한다.
  N.attHero = el('div', { class: 'sc-ov-big sc-ov-att-hero u-mono' });
  N.attHeroLbl = t(el('span', { class: 'sc-ov-att-hero-lbl u-muted' }), '', '');
  N.attHeroTop = el('div', { class: 'sc-ov-att-hero-top' }, [N.attHero, N.attHeroLbl]);
  N.attCount = el('span', { class: 'u-badge is-mono' });
  N.attList = el('div', { class: 'sc-ov-att-list' });
  N.attMore = el('button', { class: 'sc-ov-more', type: 'button', 'data-ov-goto': 'nodes' });
  N.attEmptyIco = el('span', { class: 'empty-icon' });
  N.attEmptyTitle = el('div', { class: 'empty-title' });
  N.attEmptySub = el('div', { class: 'empty-sub' });
  N.attEmpty = el('div', { class: 'empty sc-ov-att-empty' }, [N.attEmptyIco, N.attEmptyTitle, N.attEmptySub]);
  N.availVal = el('span', { class: 'sc-ov-avail-val u-mono' });
  N.availDelta = el('span', { class: 'sc-ov-avail-delta u-muted u-mono', hidden: true });
  N.availBar = el('i', { class: 'sc-ov-avail-fill' });
  const attCard = el('section', { class: 'card sc-ov-card sc-ov-att' }, [
    cardHead('Needs attention', '주의 필요', 'warningCircle', el('span', { class: 'sc-ov-hd-right' }, [N.attCount])),
    el('div', { class: 'card-body sc-ov-att-body' }, [N.attHeroTop, N.attList, N.attMore, N.attEmpty]),
    el('div', { class: 'card-foot sc-ov-avail' }, [
      (N.availLbl = t(el('span', { class: 'sc-ov-avail-lbl u-muted' }), 'Fleet availability · 30d', '플릿 가용성 · 30일')),
      N.availVal,
      N.availDelta,
      t(el('span', { class: 'sc-ov-avail-target u-muted u-mono' }), '/ target 99.99', '/ 목표 99.99'),
      el('span', { class: 'sc-ov-avail-track' }, [N.availBar]),
    ]),
  ]);
  row1.appendChild(attCard);

  // (2) 플릿 히트맵
  N.heatCount = el('span', { class: 'card-sub u-mono' });
  N.heatGrid = el('div', { class: 'sc-ov-heat' });
  N.heatLegend = el('div', { class: 'legend-row sc-ov-heat-legend' });
  row1.appendChild(el('section', { class: 'card sc-ov-card sc-ov-heat-card' }, [
    cardHead('Fleet status', '플릿 상태', 'checklist', N.heatCount),
    el('div', { class: 'card-body sc-ov-heat-body' }, [N.heatGrid]),
    el('div', { class: 'card-foot' }, [N.heatLegend]),
  ]));

  // (3) FT 이중화
  N.ftBig = el('div', { class: 'sc-ov-big u-mono' });
  N.ftEmpty = el('div', { class: 'sc-ov-ft-empty u-muted' });
  N.ftRows = el('div', { class: 'sc-ov-ft-rows' });
  const ftRow = (cls, en, ko) => {
    const v = el('b', { class: 'u-mono' });
    const r = el('div', { class: 'sc-ov-ft-row' }, [
      el('i', { class: 'sc-ov-sq ' + cls }),
      t(el('span', { class: 'u-ink2' }), en, ko),
      v,
    ]);
    return { row: r, val: v };
  };
  const ftMir = ftRow('is-pos', 'Mirrored', '미러링');
  // 재동기화(회복 중)와 심플렉스(저하)는 둘 다 앰버라 색만으로는 구분 불가 → 새 hue 추가 없이 형태 채널로 3분한다:
  //   재동기화=전환 상태 → 링(도넛) 스와치(sc-ov-sq--resync) · 심플렉스=solid 스와치. 순수 red 는 오프라인 전용 예약.
  // 표기는 다른 전 화면(compute.js syncInfo 등)과 동일한 '심플렉스'(Stratus 관례) — 이 화면만 다르면 같은 상태가 두 이름을 갖는다.
  const ftRes = ftRow('is-warn sc-ov-sq--resync', 'Resyncing', '재동기화');
  const ftSim = ftRow('is-warn', 'Simplex', '심플렉스');
  // #257: sync='offline'(다운) 장비는 #48 에서 kpi.offline 으로 분리 납품되는데 화면이 미소비라
  // 3행 합계가 ftTotal 과 불일치했다(오프라인 장비가 카운트에서 증발). 4번째 행으로 소비한다 —
  // 위 예약대로 순수 red(is-neg) 스와치는 이 행 전용. 표기는 카드 하단 클러스터 리스트의
  // syncLabel '오프라인'(compute.js syncInfo)과 같은 이름을 쓴다.
  const ftOff = ftRow('is-neg', 'Offline', '오프라인');
  N.ftMir = ftMir.val; N.ftRes = ftRes.val; N.ftSim = ftSim.val; N.ftOff = ftOff.val;
  N.ftRows.appendChild(ftMir.row); N.ftRows.appendChild(ftRes.row); N.ftRows.appendChild(ftSim.row);
  N.ftRows.appendChild(ftOff.row);
  // 클러스터별 동기화 리스트 — 카운트 4행 아래 상시 공백을 실데이터로 앵커링(심사 반영).
  N.ftList = el('div', { class: 'sc-ov-ft-list' });
  row1.appendChild(el('section', { class: 'card sc-ov-card sc-ov-ft' }, [
    cardHead('Sync health', 'FT 이중화', 'cycle'),
    el('div', { class: 'card-body' }, [
      el('div', { class: 'sc-ov-ft-top' }, [
        N.ftBig,
        t(el('span', { class: 'u-muted sc-ov-ft-unit' }), 'pairs in sync', '쌍 동기화'),
      ]),
      N.ftEmpty,
      N.ftRows,
      N.ftList,
    ]),
  ]));
  wrap.appendChild(row1);

  /* ── 2행: 자원 할당 · 라이선스 만료 (2카드) — 단일수치 카드(VM·수집)는 아래 stat strip 으로 통합(A2) ── */
  const row2 = el('div', { class: 'sc-ov-row2' });

  // (4) 자원 할당 — 숫자 요약(도넛은 capacity 화면 전용, 중복 제거 minor#5)
  N.resCount = el('span', { class: 'card-sub u-mono' });
  N.resCpuVal = el('span', { class: 'sc-ov-res-v u-mono' });
  N.resMemVal = el('span', { class: 'sc-ov-res-v u-mono' });
  N.resCpuPct = el('span', { class: 'sc-ov-res-pct u-mono' });
  N.resMemPct = el('span', { class: 'sc-ov-res-pct u-mono' });
  // 할당(커밋) 바 — capacity 커밋 도넛과 같은 잉크 2-톤(fmt.allocFill). 카드 하단 사고성 공백을 채운다(K2).
  N.resCpuBar = el('i', { class: 'sc-ov-bar-fill' });
  N.resMemBar = el('i', { class: 'sc-ov-bar-fill' });
  N.resSummary = el('div', { class: 'sc-ov-res-sum' }, [
    el('div', { class: 'sc-ov-res-block' }, [
      el('div', { class: 'sc-ov-res-row' }, [
        el('span', { class: 'sc-ov-res-k', text: 'vCPU' }), N.resCpuVal, N.resCpuPct,
      ]),
      el('span', { class: 'sc-ov-bar' }, [N.resCpuBar]),
    ]),
    el('div', { class: 'sc-ov-res-block' }, [
      el('div', { class: 'sc-ov-res-row' }, [
        t(el('span', { class: 'sc-ov-res-k' }), 'MEM', '메모리'), N.resMemVal, N.resMemPct,
      ]),
      el('span', { class: 'sc-ov-bar' }, [N.resMemBar]),
    ]),
  ]);
  N.resEmpty = el('div', { class: 'sc-ov-none u-muted' });
  // 개요=요약 · 용량=상세(A2): 커밋 게이지·헤드룸 상세는 용량 화면 소유 → 요약 카드에서 상세로 잇는 링크.
  N.resMore = el('button', { class: 'sc-ov-more sc-ov-res-more', type: 'button', 'data-ov-goto': 'capacity' }, [
    t(el('span', {}), 'Capacity detail', '용량에서 자세히'),
    ic('chevronDown', 12),
  ]);
  row2.appendChild(el('section', { class: 'card sc-ov-card sc-ov-mini sc-ov-res-card' }, [
    cardHead('Resources', '자원 할당', 'db', N.resCount),
    el('div', { class: 'card-body sc-ov-mini-body' }, [N.resSummary, N.resEmpty, N.resMore]),
  ]));

  // (6) 라이선스 만료 — 목록형 카드라 strip 이 아닌 카드 유지
  // A1: hero 를 '가장 임박' 요약(라벨+D-day+장비)으로 명시하고, 아래 목록은 그 다음 만료건부터
  //     시작(=hero 와 동일 장비 첫 행을 제외)해 헤드라인↔목록 첫행 값 중복을 제거.
  N.licLead = t(el('span', { class: 'u-muted sc-ov-lic-lead' }), 'Soonest expiry', '가장 임박');
  N.licBig = el('div', { class: 'sc-ov-big u-mono' });
  N.licLeadHost = el('span', { class: 'u-muted u-mono u-nowrap sc-ov-lic-lead-host' });
  N.licList = el('div', { class: 'sc-ov-lic-list' });
  // #258: 카드는 hero 1 + 목록(만료 slice(0,3)의 나머지 + 영구 slice(0,2))만 보여주고 나머지는
  // 무표기 절단이었다 — '주의 필요' 카드의 '외 N대 — 장비 보기'(attMore) 관례처럼 절단 건수를
  // 표기하고 전체 목록으로 잇는다. 라이선스 관점 목록 화면은 clusters 가 소유(nodes.js 헤더 주석).
  // 문구는 render 에서 매 폴 patch(언어 전환 추종), goto 는 기존 data-ov-goto 위임이 처리.
  N.licMore = el('button', { class: 'sc-ov-more sc-ov-lic-more', type: 'button', 'data-ov-goto': 'clusters' });
  row2.appendChild(el('section', { class: 'card sc-ov-card sc-ov-mini sc-ov-lic' }, [
    cardHead('License expiry', '라이선스 만료', 'bookmark'),
    el('div', { class: 'card-body sc-ov-mini-body' }, [
      el('div', { class: 'sc-ov-lic-hero' }, [
        N.licLead,
        el('div', { class: 'sc-ov-ft-top' }, [N.licBig, N.licLeadHost]),
      ]),
      N.licList,
      N.licMore,
    ]),
  ]));

  // (8) 스토리지 — FT 클러스터 장비별 스토리지그룹 사용률. topo.storage 있는 실장비만 행 노출.
  //     목록은 실제 데이터가 늘어날 수 있으므로 CSS가 스크롤 가드와 line2 서피스를 맡는다.
  N.stoCount = el('span', { class: 'card-sub u-mono' });
  N.stoList = el('div', { class: 'sc-ov-sto-list' });
  N.stoCard = el('section', { class: 'card sc-ov-card sc-ov-mini sc-ov-sto-card' }, [
    cardHead('Storage', '스토리지', 'ssd', N.stoCount),
    el('div', { class: 'card-body sc-ov-mini-body' }, [N.stoList]),
  ]);
  row2.appendChild(N.stoCard);
  wrap.appendChild(row2);

  /* ── stat strip: 단일수치(가상 머신·수집 상태·마지막 폴)를 얇은 한 줄로 통합(A2) ──
     VM 실행수·수집 상태는 각각 넓은 여백의 개별 카드였다 → 카드 수·여백을 줄여 한 줄 stat band 로 묶는다. */
  N.vmBig = el('span', { class: 'sc-ov-strip-num u-mono' });
  N.vmSub = el('span', { class: 'sc-ov-strip-sub u-muted' });
  N.colBig = el('span', { class: 'sc-ov-strip-status' });
  N.colErr = el('span', { class: 'sc-ov-strip-note is-warn' });
  N.colMaint = el('span', { class: 'sc-ov-strip-note is-warn' });
  N.colPoll = el('span', { class: 'sc-ov-strip-num u-mono' });
  // 폴러 캐시까지 더한 실제 데이터 나이 — 수신 시각과 유의미하게 벌어질 때만 뜬다.
  N.colStale = el('span', { class: 'sc-ov-strip-note is-warn' });
  const stripItem = (iconName, en, ko, valNodes) => el('div', { class: 'sc-ov-strip-item' }, [
    el('span', { class: 'sc-ov-strip-ico u-muted' }, [ic(iconName, 14)]),
    t(el('span', { class: 'sc-ov-strip-lbl u-muted' }), en, ko),
    el('span', { class: 'sc-ov-strip-val' }, valNodes),
  ]);
  N.strip = el('section', { class: 'card sc-ov-card sc-ov-strip' }, [
    stripItem('box', 'Virtual machines', '가상 머신', [N.vmBig, N.vmSub]),
    el('span', { class: 'sc-ov-strip-div' }),
    stripItem('bolt', 'Collection', '수집 상태', [N.colBig, N.colErr, N.colMaint]),
    el('span', { class: 'sc-ov-strip-div' }),
    stripItem('clock', 'Last poll', '마지막 폴', [N.colPoll, N.colStale]),
  ]);
  wrap.appendChild(N.strip);

  /* ── 3행: 장비 카드 그리드 + 우측 컬럼 ── */
  const row3 = el('div', { class: 'sc-ov-row3' });

  // 이벤트 피드 일원화: 이 화면의 상태는 히트맵(게슈탈트)·주의 필요(액션 큐)·실시간 경보(알림)가
  //   각기 다른 역할로 이미 소유한다. '최근 변동' 타임라인은 실시간 경보의 하위집합(같은 오프라인
  //   장비를 시간순으로 재나열)이라 걷어냈다 — 전체 인벤토리 브라우즈는 #/nodes 가 소유한다.
  const side = el('div', { class: 'sc-ov-side sc-ov-side--full' });

  // (9) 실시간 경보 — 총합 뱃지는 중립(잉크), red 는 '심각 N' 칩에만(minor#1).
  N.alertCount = el('span', { class: 'u-badge is-mono' });
  N.alertCrit = el('span', { class: 'u-badge is-neg is-mono sc-ov-alert-crit', hidden: true });
  N.alertList = el('div', { class: 'sc-ov-alerts' });
  N.alertEmpty = el('div', { class: 'sc-ov-none u-muted' });
  side.appendChild(el('section', { class: 'card sc-ov-card' }, [
    el('div', { class: 'card-head' }, [
      el('span', { class: 'sc-ov-hd-ic u-muted' }, [ic('bell', 15)]),
      t(el('h2', { class: 'card-title' }), 'Active incidents', '실시간 경보'),
      N.alertCount, N.alertCrit,
      el('span', { class: 'sc-ov-hd-right' }, [linkMore('incidents', 'View all', '전체 보기')]),
    ]),
    el('div', { class: 'card-body' }, [N.alertList, N.alertEmpty]),
  ]));

  // ※ '상위 부하'(Top load) 카드 제거 — 소규모 플릿(실장비 2대)에서 top-load 랭킹이 바로 위
  //   '장비' 카드의 CPU% 를 순서만 바꿔 재나열해 새 정보가 0인 중복표현이었다(major#1). 부하 비교는
  //   '장비' 카드가 소유한다.
  // ※ '플랫폼별' 카드 제거 — everRun 1/1·ztC Edge 1/1 같은 자명한 저정보이며 'FT 이중화'와도 겹쳤다(minor#1).

  row3.appendChild(side);
  // 위계 교정: '실시간 경보'는 y≈811 로 뷰포트 하단에 깔려 있어 심각 경보가 스크롤 없이는
  // 거의 안 보였다. 위험 정보를 1행(주의 필요·히트맵·FT) 바로 다음으로 끌어올리고
  // 자원·라이선스·스토리지(안전 정보)는 그 아래로 민다.
  wrap.insertBefore(row3, row2);

  // (13) 라이브 로그 — 다크 필 금지 → --line2 서피스 + mono
  N.logLines = el('div', { class: 'sc-ov-log-lines u-mono' });
  N.logDot = el('i', { class: 'sc-ov-log-dot' });
  N.logEmpty = el('div', { class: 'sc-ov-none u-muted' });
  // 라이브 로그 tail — 접이식(기본 접힘). 헤더 클릭으로 펼침/접힘(minor#2, 과길이 축소).
  N.logCaret = el('span', { class: 'sc-ov-log-caret' }, [ic('chevronDown', 13)]);
  // 헤더는 '콘솔 열기' 버튼을 품고 있어 통째 <button>으로는 못 바꾼다. role=button 을 헤더
  // 통째로 두면 ① 자손 h2 의 헤딩 세맨틱이 children-presentational 규칙으로 소멸하고
  // ② '콘솔 열기' 네이티브 버튼이 인터랙티브 중첩이 된다(#582). role=button 은 토글 영역
  // (캐럿+아이콘+제목)으로 한정하고, 헤딩 탐색용 h2 는 시각 숨김으로 role=button 바깥에 둔다
  // (#488 clusters 전례). 시각 제목은 span.card-title — CSS 가 클래스 선택자라 픽셀 불변.
  // 헤더 잔여 영역 클릭 토글은 data-ov-loghead 위임이, Enter/Space 활성화는 keydown 위임
  // (isLogToggleKey)이 클릭과 같은 토글로 연결한다.
  N.logToggle = el('span', {
    class: 'sc-ov-log-toggle', 'data-ov-logtoggle': '1',
    role: 'button', tabindex: '0', 'aria-expanded': 'false',
    // 부모 .card-head(flex, gap 8px)의 플렉스 배치를 그대로 계승 — CSS 파일 불변.
    style: { display: 'flex', alignItems: 'center', gap: 'inherit' },
  }, [
    N.logCaret,
    el('span', { class: 'sc-ov-hd-ic u-muted' }, [ic('clock', 15)]),
    t(el('span', { class: 'card-title' }), 'Live log', '라이브 로그'),
  ]);
  N.logHead = el('div', { class: 'card-head sc-ov-log-head', 'data-ov-loghead': '1' }, [
    N.logToggle,
    el('span', { class: 'sc-ov-log-tail u-mono u-muted' }, [N.logDot, 'tail -f /var/log/fleet']),
    el('span', { class: 'sc-ov-hd-right' }, [linkMore('logs', 'Open console', '콘솔 열기')]),
  ]);
  N.console = el('section', { class: 'card sc-ov-card sc-ov-console is-collapsed' }, [
    // 헤딩 탐색용 h2 — role=button 후손은 presentational 처리되어 헤딩이 소멸하므로 바깥에
    // 둔다(#582). position:absolute+clip 시각 숨김(#488·nodes.js 캡션 전례, CSS 파일 불변).
    t(el('h2', {
      style: {
        position: 'absolute', width: '1px', height: '1px', padding: '0', margin: '-1px',
        overflow: 'hidden', clip: 'rect(0,0,0,0)', whiteSpace: 'nowrap', border: '0',
      },
    }), 'Live log', '라이브 로그'),
    N.logHead,
    el('div', { class: 'card-body sc-ov-log-body' }, [N.logLines, N.logEmpty]),
  ]);
  wrap.appendChild(N.console);

  // (14) 푸터
  wrap.appendChild(t(el('div', { class: 'sc-ov-foot u-muted' }),
    'serverdesk · fault-tolerant fleet console', 'serverdesk · 이중화 플릿 콘솔'));

  root.appendChild(wrap);

  /* ── 로컬 이벤트 위임 1개 ── */
  onClick = (e) => {
    const goto = e.target.closest && e.target.closest('[data-ov-goto]');
    if (goto && ROOT.contains(goto)) {
      const v = goto.getAttribute('data-ov-goto');
      // '콘솔 열기'는 통합된 경보·로그 화면의 로그(원시 tail) 뷰로 바로 연다.
      if (v === 'logs') C.store.setState({ alertView: 'log' });
      // '전체 보기'는 경보 '카드' 탭으로 연다 — parseHash 의 #/incidents = cards 계약과 동일하게
      // 목적 탭을 명시 패치한다(직전 log/stats 탭의 stale 상태로 착지하는 비대칭 누락 방지).
      if (v === 'incidents') C.store.setState({ alertView: 'cards' });
      // '주의 필요' 카드의 '외 N대 — 장비 보기'(attMore)는 nodes 를 attention 필터로 연다
      // (전체 102대가 아니라 down/deg/maint 집합만 — 개요 attentionAll 과 동일).
      if (goto === N.attMore) C.store.setState({ nodesFilter: 'attention' });
      if (typeof C.goView === 'function') C.goView(v);
      return;
    }
    // 라이브 로그 헤더 클릭 → 펼침/접힘(위 goto '콘솔 열기' 링크가 먼저 매칭되므로 링크 클릭과 분리됨).
    // role=button 토글 영역(data-ov-logtoggle)뿐 아니라 헤더 잔여 영역 클릭도 토글로 연결한다.
    const logtoggle = e.target.closest && e.target.closest('[data-ov-loghead]');
    if (logtoggle && ROOT.contains(logtoggle)) {
      toggleLog();
      return;
    }
    // 경보 행의 [확인] 퀵 버튼 — 행 네비게이션보다 먼저 매칭해 상세 이동을 막는다.
    // store 뮤테이션 형태는 incidents.js 의 확인과 동일(값=ISO 시각) — 서버 동기화는
    // app.js 의 pushAck 구독이 알아서 처리한다.
    const ackBtn = e.target.closest && e.target.closest('.sc-ov-alert-ack');
    if (ackBtn && ROOT.contains(ackBtn)) {
      const key = ackBtn.dataset.ackKey || '';
      if (key && C && C.store) {
        C.store.setState((st) => {
          const next = Object.assign({}, st.ackedAlerts);
          next[key] = new Date().toISOString();
          return { ackedAlerts: next };
        });
        if (typeof C.showToast === 'function') C.showToast(L('Alert acknowledged', '경보를 확인했습니다'));
      }
      return;
    }
    const dev = e.target.closest && e.target.closest('[data-ov-dev]');
    if (dev && ROOT.contains(dev)) {
      const id = dev.getAttribute('data-ov-dev');
      if (id && typeof C.goDetail === 'function') C.goDetail(id);
    }
  };
  // 라이브 로그 토글(role=button)의 키보드 활성화 — Enter/Space 를 클릭과 같은 토글로 연결한다.
  // Space 는 preventDefault 로 페이지 스크롤을 막는다(manage.js 접기 헤더와 같은 패턴).
  onKeydown = (e) => {
    if (isLogToggleKey(e) && ROOT.contains(e.target)) {
      e.preventDefault();
      toggleLog();
      return;
    }
    // 경보 행(role=button)의 Enter/Space — 행 네비게이션(클릭과 동일). 행이 div 라
    // 네이티브 활성화가 없어 여기서 연결한다. ack 버튼 위의 키 입력은 네이티브 클릭이 담당.
    if (!e || (e.key !== 'Enter' && e.key !== ' ' && e.key !== 'Spacebar')) return;
    const t = e.target;
    if (!t || typeof t.closest !== 'function' || t.closest('.sc-ov-alert-ack')) return;
    const row = t.closest('.sc-ov-alert');
    if (row && row === t && ROOT.contains(row)) {
      e.preventDefault();
      const id = row.getAttribute('data-ov-dev');
      if (id && typeof C.goDetail === 'function') C.goDetail(id);
    }
  };
  root.addEventListener('click', onClick);
  root.addEventListener('keydown', onKeydown);
}

/* ===========================================================================
 * 리스트 렌더러들
 * ======================================================================== */

// (1) 주의 필요 행
function renderAttention(m) {
  // #538: 행이 '주의 필요' 포함 사유를 드러내야 한다. 포함 술어(compute.js isAttn)는
  //   down/deg/장비측 점검(maint)/활성 심각 경보(critOf>0) 4가지인데, 행은 statusLabel 만
  //   찍어 폴러(poller)가 op 으로 고정하는 FT 장비가 심각 경보를 달고도 초록 '가동' 배지로 렌더됐다
  //   (카드의 존재 이유인 위험 트리아지와 정반대 신호). 모델이 사유 필드를 납품하지 않으므로
  //   (rowOf 그대로) 화면에서 유도한다 — 심각 건수는 activeAlerts(미확인·비점검창,
  //   critOf 와 같은 모집단)를 hostId 로 센다.
  const critNOf = (id) => (m.activeAlerts || [])
    .filter((a) => a.hostId === id && a.sev === 'critical').length;
  syncList(N.attList, m.attention, (r) => r.id, () => {
    const dot = el('i', { class: 'u-dot' });
    const host = el('span', { class: 'sc-ov-att-host u-mono u-nowrap' });
    const type = el('span', { class: 'sc-ov-att-type u-muted' });
    const why = el('span', { class: 'u-badge is-mono' });
    const badge = el('span', { class: 'u-badge is-mono' });
    const node = el('button', { class: 'sc-ov-att-row', type: 'button' }, [dot, host, type, why, badge]);
    return { node, dot, host, type, why, badge };
  }, (rec, r) => {
    rec.node.setAttribute('data-ov-dev', r.id);
    setToneClass(rec.dot, 'u-dot', r.statusTone);
    // E3: 1차 식별자를 친숙명(사이트+장비, m.label)으로 통일 — '실시간 경보' 패널과 동일 스킴이 되게 한다
    //     (예전엔 raw hostname 'sj-edge-sim23'을 써 두 패널 간 같은 장비 매핑 비용이 컸다). raw host 는 보조(툴팁).
    setText(rec.host, r.label || r.host);
    rec.host.title = r.host;
    setText(rec.type, r.typeLabel);
    // 포함 사유 배지 — 배지(상태)가 이미 드러내는 down/deg 외의 숨은 사유만 병기한다.
    // down 행의 심각 경보는 down 과 같은 사실의 합성(DEVICE_STATE)이라 중복 병기하지 않는다.
    const why = [];
    if (r.maint) why.push(r.maintLabel || L('MAINT', '점검'));
    if (r.status !== 'down') {
      const critN = critNOf(r.id);
      if (critN > 0) why.push(L(critN === 1 ? '1 critical alert' : `${critN} critical alerts`, `심각 경보 ${critN}건`));
    }
    show(rec.why, why.length > 0);
    if (why.length) {
      setToneClass(rec.why, 'u-badge is-mono', r.maint && why.length === 1 ? 'warn' : 'neg');
      setText(rec.why, why.join(' · '));
    }
    setToneClass(rec.badge, 'u-badge is-mono', r.statusTone);
    setText(rec.badge, r.statusLabel);
  });
}

// (2) 히트맵 셀
function renderHeat(m) {
  // 장비 수에 따라 타일 크기를 조정한다 — 적을 때(라이브 2대) 대형화·중앙정렬해
  // 넓은 span-6 카드가 비어 보이지 않게, 많을 때(시뮬 27대)는 축소해 한눈에 담는다.
  const hn = (m.heat || []).length;
  // ≤24대는 라벨드 타일 — 장비 코드·타입이 타일 안에 박혀 툴팁 없이 식별된다.
  // 검토 판정(운영자·고객사 심사 만장일치): 예상 플릿 7~15대 전 구간에서 이름이 유지되어야 하며,
  // 장비 추가 순간 익명 잔디로 강등되는 8-임계는 결함 — 잔디는 진짜 대형(25대+)에서만 발동.
  const labeled = hn > 0 && hn <= 24;
  N.heatGrid.classList.toggle('is-labeled', labeled);
  const cell = hn <= 4 ? 56 : hn <= 8 ? 46 : hn <= 16 ? 34 : hn <= 40 ? 26 : 20;
  if (N.heatGrid.__ovCell !== cell) {
    N.heatGrid.__ovCell = cell;
    N.heatGrid.style.setProperty('--ov-cell', cell + 'px');
  }
  syncList(N.heatGrid, m.heat, (c) => c.id, () => {
    const dot = el('i', { class: 'sc-ov-cell-dot' });
    const type = el('span', { class: 'sc-ov-cell-type' });
    const code = el('b', { class: 'sc-ov-cell-code' });
    // span type=button 은 클릭만 되고 키보드가 안 먹는 가짜 버튼이었다 — 실제 <button>으로
    // (role/tabindex/Enter·Space 활성화는 네이티브가 담당, 기존 data-ov-dev 위임 클릭 핸들러 그대로).
    const node = el('button', { class: 'sc-ov-cell', type: 'button' }, [
      el('span', { class: 'sc-ov-cell-top' }, [dot, type]), code,
    ]);
    return { node, type, code };
  }, (rec, c) => {
    rec.node.setAttribute('data-ov-dev', c.id);
    rec.node.title = c.title;
    setText(rec.type, c.typeShort || '');
    setText(rec.code, c.code || c.label || c.host);
    // 히트맵 셀은 애니메이션하지 않는다(C2) — 오프라인(neg)은 흰 인셋 링, 저하(warn)는 우상단 노치로
    // '형태 토큰'까지 얹어 정적으로 3분 구분하므로 blink/pulse 장식 모션은 emit 하지 않는다.
    const cls = 'sc-ov-cell ' + toneCls(c.tone);
    if (rec.node.className !== cls) rec.node.className = cls;
  });
  // 범례는 색 스와치 + 라벨만 — 개수는 아래 필터탭(정상/저하/오프라인 카운트)이 이미 소유하므로
  // 여기서 숫자를 다시 찍지 않는다(카운트 중복 제거, minor#2).
  syncList(N.heatLegend, m.heatLegend, (g) => g.key, () => {
    const dot = el('i', { class: 'legend-key' });
    const lbl = el('span', {});
    return { node: el('span', { class: 'legend-item' }, [dot, lbl]), dot, lbl };
  }, (rec, g) => {
    setToneClass(rec.dot, 'legend-key sc-ov-legend-key', g.tone);
    setText(rec.lbl, g.label);
  });
}

// (6) 라이선스 행
function renderLicenses(m) {
  const lic = m.licenses || {};
  // A1: hero(가장 임박)가 이미 최임박 1건을 표시하므로 목록에선 첫 만료건을 제외(값 중복 제거).
  // 다만 '영구 라이선스는 스코프 밖'이라며 빼버리니, 실장비 2대 중 1대가 영구인 현장에서는
  // 목록이 통째로 비어 카드가 4자/10k 짜리 죽은 공간이 됐다(실측). 운영자가 알고 싶은 건
  // '만료 임박한 것' 뿐 아니라 '내 라이선스 전부가 어떤 상태인가'다 → 영구분도 함께 나열한다.
  const expTail = lic.has ? (lic.list || []).slice(1) : (lic.list || []);
  const perpRows = (lic.perp || []).map((l) => Object.assign({}, l, {
    txt: L('Perpetual', '영구'), tone: 'mut',
    tip: L('Perpetual license — no expiry', '영구 라이선스 — 만료 없음'),
  }));
  // #602: 미상(만료형인데 만료일 결측·파싱 불가, #593의 na/naAll)도 보고된 라이선스다 —
  // 목록에서 빼면 미상 장비가 카드에서 증발한다(상세 카드·클러스터 행의 '미상' 표기와 모순).
  const naRows = (lic.na || []).map((l) => Object.assign({}, l, {
    txt: L('Unknown', '미상'), tone: 'mut',
    tip: L('Expiry unknown — reported as expiring but no valid date', '만료일 미상 — 만료형으로 보고됐으나 유효한 만료일 없음'),
  }));
  const list = expTail.concat(perpRows, naRows);
  syncList(N.licList, list, (l, i) => (l.id || '') + ':' + i, () => {
    const name = el('span', { class: 'sc-ov-lic-name u-mono u-nowrap' });
    const host = el('span', { class: 'sc-ov-lic-host u-muted u-nowrap' });
    const dd = el('span', { class: 'sc-ov-lic-dd u-mono' });
    const node = el('button', { class: 'sc-ov-lic-row', type: 'button' }, [
      el('span', { class: 'sc-ov-lic-body' }, [name, host]), dd,
    ]);
    return { node, name, host, dd };
  }, (rec, l) => {
    rec.node.setAttribute('data-ov-dev', l.id || '');
    setText(rec.name, l.lic || l.host);
    setText(rec.host, l.lic ? l.host : '');
    show(rec.host, !!l.lic);
    rec.node.title = l.tip || '';
    setToneClass(rec.dd, 'sc-ov-lic-dd u-mono', l.tone);
    setText(rec.dd, l.txt);
  });
}

// (8) 스토리지그룹 행 — [장비 코드][그룹명] …[%] + 하단 수평 게이지. 임계 초과만 상태 톤.
function renderStorage(m) {
  const sto = (m && m.storage) || { rows: [] };
  syncList(N.stoList, sto.rows, (g) => g.key, () => {
    const dev = el('span', { class: 'sc-ov-sto-dev u-nowrap' });
    const grp = el('span', { class: 'sc-ov-sto-grp u-muted u-nowrap' });
    const pct = el('span', { class: 'sc-ov-sto-pct u-mono' });
    const fill = el('i', { class: 'sc-ov-sto-fill' });
    const node = el('button', { class: 'sc-ov-sto-row', type: 'button' }, [
      el('div', { class: 'sc-ov-sto-head' }, [dev, grp, pct]),
      el('span', { class: 'sc-ov-sto-track' }, [fill]),
    ]);
    return { node, dev, grp, pct, fill };
  }, (rec, g) => {
    rec.node.setAttribute('data-ov-dev', g.id);   // 클릭 시 기존 위임 핸들러가 #/detail/<id> 로 이동
    rec.node.title = g.tip || '';
    setText(rec.dev, g.dev);
    setText(rec.grp, g.group);
    // 중립(임계 미만)은 상태 클래스 없이 잉크 톤 유지 — warn/neg 행만 % 텍스트·게이지 필에 상태색.
    const cls = g.tone === 'neg' ? ' is-neg' : (g.tone === 'warn' ? ' is-warn' : '');
    setText(rec.pct, g.pctText);
    const pctCls = 'sc-ov-sto-pct u-mono' + cls;
    if (rec.pct.className !== pctCls) rec.pct.className = pctCls;
    const fillCls = 'sc-ov-sto-fill' + cls;
    if (rec.fill.className !== fillCls) rec.fill.className = fillCls;
    rec.fill.style.width = g.width;
  });
}


/** 실시간 경보 행 클래스 — 확인(ack)된 경보는 is-acked 를 얹어 활성과 구분한다(#14).
 *  미리보기 모집단은 active+acked 합성(compute.js: 확인분을 뒤에 둠)인데 행 모습이 같으면
 *  운영자가 이미 손댄 경보를 미조치 경보로 오독한다. */
export function alertRowCls(a) {
  return 'sc-ov-alert ' + toneCls(a && a.sevTone) + ((a && a.acked) ? ' is-acked' : '');
}

/** 히어로 숫자 옆 라벨은 flex gap이 간격을 맡으므로 영문 선행 공백을 넣지 않는다. */
export function attentionHeroLabel(heroCrit, attentionCount, L) {
  const crit = Number(heroCrit) || 0;
  const att = Number(attentionCount) || 0;
  if (crit) return L(crit === 1 ? 'active critical alert' : 'active critical alerts', '건 활성 심각 경보');
  if (att) return L(att === 1 ? 'device needs attention' : 'devices need attention', '대 주의 필요');
  return L('— nothing needs attention', '건 — 조치 불필요');
}

// (9) 경보 행
function renderAlerts(m) {
  // syncList 키가 a.id 였는데 a.id 는 '장비 id' 라 같은 장비의 경보가 전부 같은 키로 붕괴했다.
  // 실측: 경보 7건을 넘겨도 2행만 렌더됐다(everRun 1 + ztC Edge 1). 경보별 고유키(ackKey)를 쓴다.
  syncList(N.alertList, m.alerts, (a, i) => a.ackKey || (a.host + ':' + a.time + ':' + i), () => {
    const ico = el('span', { class: 'sc-ov-alert-ico' });
    const host = el('span', { class: 'sc-ov-alert-host u-mono u-nowrap' });
    const sev = el('span', { class: 'u-badge is-mono' });
    // 확인됨 배지 — acked 행에만 노출(확인분은 디밍+배지로 활성과 구분, #14).
    const st = el('span', { class: 'u-badge is-mono sc-ov-alert-st', hidden: true });
    const msg = el('div', { class: 'sc-ov-alert-msg u-ink2' });
    const ago = el('span', { class: 'sc-ov-alert-ago u-mono u-muted' });
    // 행은 button 이 아니라 div[role=button] — [확인] 퀵 버튼을 중첩해야 해서
    // (button > button 은 무효 HTML). 키보드 활성화는 onKeydown 이 담당한다.
    const ack = el('button', { class: 'sc-ov-alert-ack', type: 'button' });
    const node = el('div', { class: 'sc-ov-alert', role: 'button', tabindex: '0' }, [
      ico,
      el('div', { class: 'sc-ov-alert-body' }, [
        el('div', { class: 'u-row' }, [host, sev, st]), msg,
      ]),
      ago, ack,
    ]);
    return { node, ico, host, sev, st, msg, ago, ack };
  }, (rec, a) => {
    rec.node.setAttribute('data-ov-dev', a.hostId || '');
    const cls = alertRowCls(a);
    if (rec.node.className !== cls) rec.node.className = cls;
    setIcon(rec.ico, a.sevIcon, 15);
    setText(rec.host, a.host);
    rec.host.title = a.host;
    setToneClass(rec.sev, 'u-badge is-mono', a.sevTone);
    setText(rec.sev, a.sevLabel);
    show(rec.st, !!a.acked);
    if (a.acked) setText(rec.st, a.stLabel || L('Acknowledged', '확인됨'));
    // 실시간 경보 카드 본문은 한글 유형 요약(msgLocal)으로 — incidents 카드탭과 동일 카탈로그(compute
    // alertMsgKo). 원문(영문 desc)은 툴팁에 보존해 정보 손실 없음(minor#1, i18n 정합).
    setText(rec.msg, a.msgLocal || a.msg);
    rec.msg.title = (a.msgLocal && a.msgLocal !== a.msg) ? a.msg : '';
    setText(rec.ago, a.ago);
    rec.ago.title = a.agoFull || '';
    // [확인] 퀵 버튼 — 미확인 행에만. 이미 확인된 행은 is-acked 배지가 상태를 말한다.
    setText(rec.ack, L('Ack', '확인'));
    rec.ack.dataset.ackKey = a.ackKey || '';
    show(rec.ack, !a.acked && !!a.ackKey);
  });
  show(N.alertList, m.alerts.length > 0);
  show(N.alertEmpty, m.alerts.length === 0);
  setText(N.alertEmpty, L('No active incidents', '활성 경보 없음'));
}

// (13) 라이브 로그
function renderLogs(m) {
  const rows = (m.logs || []).slice(0, 7);
  // 키에 인덱스를 섞으면 새 로그가 들어올 때마다 전 행 키가 밀려 매 폴 전량 재생성됐다 —
  // 로그 고유키(ackKey) 단독 키로 노드를 재사용한다. g.id 단독 키는 금지: 트랩·경보의
  // id 는 장비 id 라 같은 장비의 로그 행이 1행으로 붕괴한다(#12).
  syncList(N.logLines, rows, logRowKey, () => {
    const time = el('span', { class: 'sc-ov-log-t u-muted' });
    const host = el('span', { class: 'sc-ov-log-h u-muted u-nowrap' });
    const lvl = el('span', { class: 'sc-ov-log-l' });
    const msg = el('span', { class: 'sc-ov-log-m u-nowrap' });
    return { node: el('div', { class: 'sc-ov-log-line' }, [time, host, lvl, msg]), time, host, lvl, msg };
  }, (rec, g) => {
    setText(rec.time, g.timeShort || g.time || '');
    setText(rec.host, g.host);
    setToneClass(rec.lvl, 'sc-ov-log-l', g.sevTone);
    setText(rec.lvl, g.level || '');
    setText(rec.msg, logRowMsg(g));
  });
  show(N.logLines, rows.length > 0);
  show(N.logEmpty, rows.length === 0);
  setText(N.logEmpty, L('No events yet', '이벤트 없음'));
}

/* ===========================================================================
 * render — 값만 patch
 * ======================================================================== */
function render(state, ctx) {
  C = ctx || C;
  // 모델 빌드 예외가 화면 전체를 죽이지 않게(nodes.js 와 동일 패턴).
  let m = null;
  try { m = (C && typeof C.getModel === 'function') ? C.getModel(state) : null; } catch (e) { m = null; }
  if (!m) return;

  // 언어 전환 시 정적 라벨 일괄 갱신
  if (lastLang !== state.lang) {
    lastLang = state.lang;
    TEXTS.forEach(([node, en, ko]) => { node.textContent = L(en, ko); });
  }

  const kpi = m.kpi || {};
  const poll = m.pollStat || {};
  const pollerDown = !!(poll.live && (poll.liveError || poll.stale));

  /* (0) 플릿 총평 — 폴러 verdict 재도출값을 제목 옆 배지 + 사유 한 줄로 노출 */
  const fv = m.fleetVerdict || { key: 'ok', label: '', tone: 'pos', reasons: [] };
  setToneClass(N.verdict, 'u-badge sc-ov-verdict', fv.tone);
  setText(N.verdict, fv.label || '');
  setText(N.verdictWhy, (fv.reasons || []).join(' · '));

  /* (1) 주의 필요 */
  const attN = (m.attentionAll || []).length;
  show(N.attCount, attN > 0);
  setText(N.attCount, attN);

  // 히어로 위험 수치 — 활성 심각 경보가 있으면 그 수를, 없으면 주의 필요 대수를 띄운다.
  // 둘 다 0 일 때만 0(초록). 이 카드가 화면에서 가장 큰 활자를 갖는다.
  // `||` 는 0 을 falsy 로 흘려보낸다 — 전체 확인 후 critical 이 0 이 되면 폴백이 발동해
  // 확인된 것까지 다시 세는 버그가 있었다(실측: 전체 확인 후에도 히어로가 9로 남음).
  // 값이 '있는지'를 숫자 여부로 판정한다.
  const heroCrit = Number.isFinite(m.incStats && m.incStats.critical)
    ? m.incStats.critical
    : (m.activeAlerts || m.alertsAll || []).filter((a) => a.sev === 'critical').length;
  const heroN = heroCrit || attN;
  const heroTone = heroCrit ? 'neg' : (attN ? 'warn' : 'pos');
  setToneClass(N.attHero, 'sc-ov-big sc-ov-att-hero u-mono', heroTone);
  setText(N.attHero, String(heroN));
  // 숫자와 라벨이 별도 엘리먼트라 영문은 앞에 공백이 필요하다 —
  // 한국어는 '9건'처럼 붙여 쓰지만 영문은 '9active…'가 되어 버린다(실측 발견).
  setText(N.attHeroLbl, attentionHeroLabel(heroCrit, attN, L));

  const emptyMode = pollerDown ? 'poller' : (m.total === 0 ? 'none' : (attN === 0 ? 'ok' : ''));
  show(N.attList, emptyMode === '');
  show(N.attEmpty, emptyMode !== '');
  if (emptyMode === 'poller') {
    setIcon(N.attEmptyIco, 'warningCircle', 24);
    N.attEmptyIco.className = 'empty-icon is-neg';
    setToneClass(N.attEmptyTitle, 'empty-title', 'neg');
    setText(N.attEmptyTitle, L('Poller unreachable', '폴러 연결 실패'));
    setText(N.attEmptySub, L('Fleet status unknown', '상태 알 수 없음'));
  } else if (emptyMode === 'none') {
    setIcon(N.attEmptyIco, 'box', 24);
    N.attEmptyIco.className = 'empty-icon';
    N.attEmptyTitle.className = 'empty-title';
    setText(N.attEmptyTitle, poll.lastPoll ? L('No devices', '장비 없음') : L('Loading…', '로딩 중'));
    setText(N.attEmptySub, poll.lastPoll ? L('No devices registered', '등록된 장비 없음') : L('Fetching status…', '상태 확인 중'));
  } else if (emptyMode === 'ok') {
    setIcon(N.attEmptyIco, 'check', 24);
    N.attEmptyIco.className = 'empty-icon is-pos';
    N.attEmptyTitle.className = 'empty-title';
    setText(N.attEmptyTitle, L('All systems normal', '모든 장비 정상'));
    setText(N.attEmptySub, L(`all ${m.total} devices operational`, `${m.total}대 모두 가동 중`));
  } else {
    renderAttention(m);
  }
  show(N.attMore, m.attentionMore > 0);
  if (m.attentionMore > 0) {
    setText(N.attMore, L(`${m.attentionMore} more — view devices`, `외 ${m.attentionMore}대 — 장비 보기`));
  }
  // 감사 지적: 이 슬롯이 절대치가 아니라 목표 대비 편차(-0.056%p)만 띄우고 있었다.
  // 운영자가 필요한 건 '지금 가용성이 얼마인가'다 — 절대치를 주 수치로 되돌리고
  // 편차는 뒤에 보조로 병기한다(레일 배지 11px 만으로는 판독 불가였다).
  // #314: availIsMeasured===false(한 대라도 availDays=0인 혼합 플릿)면 kpi.availPct 는
  // 실측+명목 혼합 평균이다 — '측정 N일' 라벨과 함께 나가면 명목 혼합값을 실측으로 오표기.
  // 명목 단계에서는 명목값(상태 기반 평균, kpi.availNominal)을 '명목' 라벨과 함께 보인다
  // (clusters.js 타일의 '· 명목' 꼬리표와 같은 계약 — ARCHITECTURE.md availIsMeasured 항).
  const availNominalPhase = kpi.availIsMeasured === false;
  const availPct = Number(kpi.availPct);
  const availShown = availNominalPhase
    ? Number(String(kpi.availNominal).replace(/%$/, ''))
    : availPct;
  if (m.total > 0 && Number.isFinite(availShown)) {
    const dv = availShown - 99.99;
    const sign = dv >= 0 ? '+' : '-';
    setText(N.availVal, availShown.toFixed(3) + '%');
    setText(N.availDelta, sign + Math.abs(dv).toFixed(3) + '%p');
    show(N.availDelta, Math.abs(dv) >= 0.0005);
  } else {
    setText(N.availVal, DASH);
    show(N.availDelta, false);
  }
  // 표기 출처 툴팁 — availMeasuredLabel 계약 소비('실측'/'명목값(상태 기반)'). 명목일 때만 단다
  // (clusters.js 의 명목 툴팁 전례와 동일).
  N.availVal.title = availNominalPhase ? (kpi.availMeasuredLabel || '') : '';
  // 바는 표시값과 같은 수량으로 — 명목 단계에 혼합 평균 기반 availBarW 를 쓰면 값과 바가 갈라진다.
  const clampFn = C && C.util && C.util.fmt ? C.util.fmt.clamp : null;
  N.availBar.style.width = (m.total > 0 && Number.isFinite(availShown) && clampFn)
    ? (clampFn((availShown - 99.9) / 0.1, 0, 1) * 100).toFixed(0) + '%'
    : (kpi.availBarW || '0%');
  // 라벨 정직화: 명목 단계(실측 전·혼합)면 '명목', 실측 관측 기간이 30일 미만이면 '측정 N일'.
  const ad = Number(kpi.availDays) || 0;
  setText(N.availLbl, availNominalPhase ? L('Fleet availability · nominal', '플릿 가용성 · 명목')
    : (ad >= 30 ? L('Fleet availability · 30d', '플릿 가용성 · 30일')
      : L('Fleet availability · ' + Math.max(1, Math.floor(ad)) + 'd measured',
        '플릿 가용성 · 측정 ' + Math.max(1, Math.floor(ad)) + '일')));

  /* (2) 히트맵 */
  setText(N.heatCount, m.total + L(' nodes', '대'));
  renderHeat(m);

  /* (3) FT 이중화 */
  const hasFT = (kpi.ftTotal || 0) > 0;
  show(N.ftBig, hasFT);
  show(N.ftRows, hasFT);
  show(N.ftEmpty, !hasFT);
  setText(N.ftEmpty, L('No FT devices', 'FT 장비 없음'));
  setText(N.ftBig, hasFT ? kpi.synced + '/' + kpi.ftTotal : DASH);
  setText(N.ftMir, kpi.synced || 0);
  setText(N.ftRes, kpi.resync || 0);
  setText(N.ftSim, kpi.simplex || 0);
  // #257 — 다른 3행과 같은 관례: 0 이면 0 으로 패치(행을 숨기지 않는다). 숨기면 다시 나타날 때
  // 행 수가 출렁이고, '합계=총수' 검산도 깨진다.
  setText(N.ftOff, kpi.offline || 0);
  show(N.ftList, hasFT);
  syncList(N.ftList, hasFT ? (m.ftClusters || []) : [], (c) => c.id, () => {
    const code = el('b', { class: 'sc-ov-ft-code' });
    const badge = el('span', { class: 'u-badge' });
    const node = el('button', { class: 'sc-ov-ft-item', type: 'button' }, [code, badge]);
    return { node, code, badge };
  }, (rec, c) => {
    rec.node.setAttribute('data-ov-dev', c.id);
    setText(rec.code, c.code);
    setToneClass(rec.badge, 'u-badge', c.syncTone);
    setText(rec.badge, c.syncLabel);
  });

  /* (4) 자원 — 숫자 요약(도넛 없음) */
  const res = m.resAgg || {};
  show(N.resSummary, !!res.has);
  show(N.resEmpty, !res.has);
  show(N.resMore, !!res.has);
  setText(N.resEmpty, L('No allocation data', '할당 정보 없음'));
  show(N.resCount, !!res.has);
  if (res.has) {
    setText(N.resCount, res.counted + '/' + res.total + L(' dev', '대'));
    // 메트릭별 NA(#313): vcpuHas/memHas===false 면 그 메트릭만 '—' — 한쪽 결측 집단이
    // '0 / 0 vCPU · 0%' 로 실측 0% 인 양 오표기되던 경로 차단(§1.6). capacity.js(#262)와 같은
    // 계약: 플래그 미납품(구형 모델)은 종전대로 실측 취급(=== false 검사).
    const cpuNA = res.vcpuHas === false;
    const memNA = res.memHas === false;
    setText(N.resCpuVal, cpuNA ? DASH : `${res.vcpuUsed} / ${res.vcpuTot} vCPU`);
    // 정상 구간 % 텍스트는 중립(바=블루와 결) — 그린 텍스트는 상태 토큰 전용(재심사 반영).
    setToneClass(N.resCpuPct, 'sc-ov-res-pct u-mono', cpuNA ? 'mut' : (res.vcpuTone === 'pos' ? 'mut' : res.vcpuTone));
    setText(N.resCpuPct, cpuNA ? DASH : res.vcpuPct + '%');
    setText(N.resMemVal, memNA ? DASH : `${res.memUsed} / ${res.memTot} GiB`);
    setToneClass(N.resMemPct, 'sc-ov-res-pct u-mono', memNA ? 'mut' : (res.memTone === 'pos' ? 'mut' : res.memTone));
    setText(N.resMemPct, memNA ? DASH : res.memPct + '%');
    // 게이지 색 단일 규칙(TDS 재심사): 바=텍스트 동일 임계(정상 블루/78+ 앰버/90+ 레드).
    // 예전 '바는 중립, 텍스트만 톤' 이원화가 한 행 안에서 상태를 다르게 말한다는 지적을 수정.
    const bc = C && C.util && C.util.fmt ? (C.util.fmt.barFill || C.util.fmt.barColor) : null;
    const cpuPct = cpuNA ? 0 : res.vcpuPct;
    const memPct = memNA ? 0 : res.memPct;
    N.resCpuBar.style.width = cpuPct + '%';
    N.resMemBar.style.width = memPct + '%';
    if (bc) {
      N.resCpuBar.style.background = bc(cpuPct);
      N.resMemBar.style.background = bc(memPct);
    }
  }

  /* (5) VM */
  const vm = m.vmAgg || { running: 0, total: 0 };
  setText(N.vmBig, vm.running);
  setText(N.vmSub, '/ ' + vm.total + L(' running', ' 실행 중'));

  /* (6) 라이선스 */
  const lic = m.licenses || {};
  if (lic.has) {
    setToneClass(N.licBig, 'sc-ov-big u-mono', lic.minTone);
    setText(N.licBig, lic.minTxt);
    N.licBig.title = (lic.minDate || '') + L(' expiry', ' 만료') + (lic.minHost ? ' · ' + lic.minHost : '');
    setText(N.licLeadHost, lic.minHost || '');
    show(N.licLead, true);
    show(N.licLeadHost, !!lic.minHost);
  } else {
    setToneClass(N.licBig, 'sc-ov-big u-mono', 'mut');
    // #315: 장비는 있지만 어느 장비도 meta.license 를 보고하지 않는 플릿(만료 0 + 영구 0)을
    // '영구'로 표기하면 거짓 안전 신호다 — 데이터 없음은 total===0(lic.empty)과 같은 '—' 로
    // 내린다. 모델(compute.js)의 lic.empty 조건을 넓히는 게 정본이나, 화면 측 최소 방어로
    // all/perpAll/naAll 합계 0 을 판정한다(전부 영구 플릿은 perpAll>0 이라 '영구' 유지).
    // #602: 미상(naAll, #593)도 보고된 라이선스다 — 만료일 미상 장비가 있는데 '영구'를
    // 표기하면 #315 가 차단한 거짓 안전 신호의 재유입이므로, 만료 0건에서 미상이 1건이라도
    // 있으면 hero 는 '미상' 을 표기한다(영구·미상 혼합이면 미상이 우선 — 확인 필요 신호).
    const licNone = ((lic.all || []).length + (lic.perpAll || []).length + (lic.naAll || []).length) === 0;
    setText(N.licBig, (lic.empty || licNone) ? DASH
      : ((lic.naAll || []).length ? L('Unknown', '미상') : L('Perpetual', '영구')));
    N.licBig.title = '';
    setText(N.licLeadHost, '');
    show(N.licLead, false);
    show(N.licLeadHost, false);
  }
  renderLicenses(m);
  // #258: 절단 건수 표기 + 전체 목록 경로. 카드가 보여주는 건수는 hero+목록 = lic.list 길이
  // (hero 가 첫 건을 빼가므로 목록은 list.slice(1))와 lic.perp·lic.na 뿐 — 전체(all/perpAll/
  // naAll)와의 차가 곧 절단 건수다(#602: 미상 절단분도 건수에 포함). attMore 와 같은 관례:
  // 0 이면 버튼 자체를 숨긴다.
  const licMoreN = ((lic.all || []).length - (lic.list || []).length)
    + ((lic.perpAll || []).length - (lic.perp || []).length)
    + ((lic.naAll || []).length - (lic.na || []).length);
  show(N.licMore, licMoreN > 0);
  if (licMoreN > 0) {
    setText(N.licMore, L(`${licMoreN} more — view clusters`, `외 ${licMoreN}건 — 클러스터 보기`));
  }

  /* (8) 스토리지 — topo.storage 있는 FT 실장비만. 데이터 0건이면 카드 자체를 숨긴다(빈 카드 금지). */
  const sto = m.storage || { has: false, rows: [] };
  show(N.stoCard, sto.has);
  show(N.stoCount, sto.has && sto.rows.length > 0);
  if (sto.has) {
    const n = sto.rows.length;
    setText(N.stoCount, n + L(n === 1 ? ' group' : ' groups', '개'));
  }
  renderStorage(m);

  /* (7) 수집 상태 + 마지막 폴 (stat strip 인라인) */
  const col = m.collect || {};
  setToneClass(N.colBig, 'sc-ov-strip-status', col.empty ? 'mut' : (col.ok ? 'pos' : 'warn'));
  setText(N.colBig, col.empty ? DASH : (col.ok ? L('Healthy', '정상') : L('Attention', '확인 필요')));
  show(N.colErr, !!col.errCnt);
  if (col.errCnt) {
    const hosts = (col.errHosts || []).join(', ');
    const more = Number(col.errHostMore) > 0
      ? L(` · +${col.errHostMore} more`, ` · 외 ${col.errHostMore}대`) : '';
    setText(N.colErr, L(`· ${col.errCnt} err`, `· 오류 ${col.errCnt}`));
    N.colErr.title = L(`Collection errors: ${col.errCnt}`, `수집 오류 ${col.errCnt}건`)
      + (hosts ? ' · ' + hosts : '') + more;
  }
  show(N.colMaint, !!col.maintCnt);
  if (col.maintCnt) setText(N.colMaint, L(`· ${col.maintCnt} maint`, `· 점검 ${col.maintCnt}`));
  // '마지막 폴'은 클라이언트 수신 시각이라, 폴러 수집이 밀리면 '방금'이라 말하면서
  // 실제로는 몇 분 낡은 값을 보여준다. 둘 차이가 유의미할 때(10초 이상)만 실제 나이를 병기한다.
  const gap = (poll.dataAgeSec != null && poll.agoSec != null) ? poll.dataAgeSec - poll.agoSec : 0;
  setText(N.colPoll, poll.ago || DASH);
  N.colPoll.title = gap >= 10
    ? L('Data is ' + poll.dataAge + ' old (poller cache ' + poll.cacheAgeSec + 's)',
      '실제 데이터 나이 ' + poll.dataAge + ' (폴러 캐시 ' + poll.cacheAgeSec + '초)')
    : '';
  show(N.colStale, gap >= 10);
  if (gap >= 10) setText(N.colStale, L('· data ' + poll.dataAge, '· 데이터 ' + poll.dataAge));

  /* (9)~(13) */
  setText(N.alertCount, m.alertCount || 0);
  show(N.alertCount, (m.alertCount || 0) > 0);
  // '심각 N' 칩만 red — 총합 뱃지는 중립(minor#1).
  const critN = (m.incStats && m.incStats.critical) || 0;
  setText(N.alertCrit, L('critical ', '심각 ') + critN);
  show(N.alertCrit, critN > 0);
  renderAlerts(m);
  renderLogs(m);

  const dotCls = 'sc-ov-log-dot' + ((state.logPaused ?? state.paused) ? ' is-paused' : '') + (poll.live ? ' is-pos' : ' is-info');
  if (N.logDot.className !== dotCls) N.logDot.className = dotCls;
}

/* ===========================================================================
 * destroy
 * ======================================================================== */
function destroy() {
  if (ROOT && onClick) ROOT.removeEventListener('click', onClick);
  if (ROOT && onKeydown) ROOT.removeEventListener('keydown', onKeydown);
  onClick = null;
  onKeydown = null;
  ROOT = null;
  N = {};
  TEXTS = [];
  lastLang = null;
  logOpen = false;
}

export default {
  key: KEY,
  title: { en: 'Overview', ko: '개요' },
  icon: 'dash',
  init,
  render,
  destroy,
};
