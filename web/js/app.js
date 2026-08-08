// js/app.js
// serverdesk — 셸 렌더 + 해시 라우터 + 화면 모듈 스왑 + tick 루프 + /api/fleet 폴링.
// REBUILD-SPEC.md §1(공통 셸) / §3(상태·라우터 계약) / §4.4(폴링) / §5.1(모듈 인터페이스)
//
// 원칙
//  - 화면 전용 DOM은 전혀 만들지 않는다. 각 js/screens/<key>.js 가 자기 DOM을 생성한다.
//  - 화면 모듈/유틸/모델은 개별 try-catch 동적 import. 실패해도 셸은 계속 동작한다.
//  - 색은 CSS 변수·공용 클래스로만. 인라인 hex 사용 금지.

import { initialState, createStore, loadPersisted, persist } from './store.js';

// ---------------------------------------------------------------------------
// 화면 레지스트리 (§1 표 · 레일 순서)
// ---------------------------------------------------------------------------
export const SCREENS = [
  { key: 'overview',  icon: 'dash',     en: 'Overview',   ko: '대시보드' },
  { key: 'nodes',     icon: 'ops',      en: 'All devices', ko: '전체 장비' },
  { key: 'topology',  icon: 'crop',     en: 'Topology',   ko: '토폴로지' },
  { key: 'capacity',  icon: 'db',       en: 'Capacity',   ko: '용량' },
  { key: 'clusters',  icon: 'link',     en: 'Clusters',   ko: '클러스터' },
  { key: 'incidents', icon: 'bell',     en: 'Alerts & log', ko: '경보 · 로그' },
  { key: 'manage',    icon: 'box',      en: 'Manage',     ko: '장비 관리' },
  { key: 'settings',  icon: 'settings', en: 'Settings',   ko: '설정' },
  { key: 'detail',    icon: null,       en: 'Server detail', ko: '서버 상세' },
];
const SCREEN_KEYS = SCREENS.map(s => s.key);
const screenMeta = key => SCREENS.find(s => s.key === key) || SCREENS[0];

// ---------------------------------------------------------------------------
// DOM 헬퍼 (util/dom.js 가 없어도 셸이 동작하도록 로컬 최소 구현)
// ---------------------------------------------------------------------------
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
function setField(name, value, root = document) {
  $$('[data-field="' + name + '"]', root).forEach(el => { el.textContent = value; });
}
function searchShortcutLabel() {
  const platform = (typeof navigator !== 'undefined' &&
    ((navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform)) || '';
  return /Mac|iPhone|iPad|iPod/i.test(platform) ? '⌘K' : 'Ctrl K';
}
function show(el, on) { if (el) el.hidden = !on; }

// 셸 고정 요소
const shell = {
  crumbRoot: null, crumbSep: null, crumbLeaf: null,
  searchWrap: null, searchInput: null, searchResults: null,
  clock: null, bellDot: null, banner: null, toast: null,
  screenRoot: null, sourceBadge: null,
  rail: null, railToggle: null, skipLink: null, langToggle: null, bell: null,
};
function cacheShell() {
  shell.rail = $('[data-rail]');
  shell.railToggle = $('[data-rail-toggle]');
  // 슬라이딩 액티브 인디케이터 — 마크업 오염 없이 여기서 1회 생성(.rail-indicator, styles.css §5).
  if (shell.rail && !shell.railInd) {
    shell.railInd = document.createElement('span');
    shell.railInd.className = 'rail-indicator';
    shell.railInd.setAttribute('aria-hidden', 'true');
    shell.rail.appendChild(shell.railInd);
    // 레일 펼침/접힘(width 트랜지션) 완료 시 아이콘 박스가 변하므로 재측정.
    shell.rail.addEventListener('transitionend', (e) => {
      if (e.propertyName === 'width') positionRailIndicator();
    });
  }
  shell.crumbRoot = $('[data-field="crumbRoot"]');
  shell.crumbSep = $('[data-crumb-sep]');
  shell.crumbLeaf = $('[data-field="crumbLeaf"]');
  shell.searchWrap = $('[data-search-wrap]');
  shell.searchInput = $('[data-search-input]');
  shell.searchResults = $('[data-search-results]');
  shell.clock = $('[data-clock]');
  shell.bellDot = $('[data-bell-dot]');
  shell.banner = $('[data-banner]');
  // 배너 닫기(×) — 아이콘 전용 버튼이라 renderBanner 가 언어 추적 aria-label 을 준다(#552).
  shell.bannerX = shell.banner ? shell.banner.querySelector('[data-action="dismissBanner"]') : null;
  shell.toast = $('[data-toast]');
  shell.screenRoot = $('[data-screen-root]');
  shell.sourceBadge = $('[data-source-badge]');
  shell.skipLink = $('.skip-link');
  shell.langToggle = $('[data-action="toggleLang"]');
  shell.bell = $('[data-action="goIncidents"]');
}

// ---------------------------------------------------------------------------
// store
// ---------------------------------------------------------------------------
const store = createStore({ ...initialState, ...loadPersisted() });
const { getState, setState, subscribe } = store;

// ---------------------------------------------------------------------------
// 지연 로드되는 공용 모듈 (개별 try-catch)
// ---------------------------------------------------------------------------
const util = { dom: null, svg: null, fmt: null, icon: null };
let model = null;   // model/compute.js 네임스페이스
let data = null;    // model/data.js 네임스페이스
let sound = null;   // util/sound.js 네임스페이스(없어도 셸은 동작 — 사운드만 무음)

async function tryImport(path, label) {
  try {
    return await import(path);
  } catch (e) {
    console.warn('[serverdesk] 모듈 로드 실패: ' + label + ' — 폴백 사용', e && e.message);
    return null;
  }
}

/** util/icon.js 가 없을 때의 안전 폴백 */
function fallbackIcon(name, opts) {
  const o = opts || {};
  const size = o.size || 20;
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('width', String(size));
  svg.setAttribute('height', String(size));
  svg.setAttribute('fill', 'currentColor');
  if (o.cls) svg.setAttribute('class', o.cls);
  const c = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
  c.setAttribute('cx', '12'); c.setAttribute('cy', '12'); c.setAttribute('r', '8');
  c.setAttribute('fill', 'none'); c.setAttribute('stroke', 'currentColor'); c.setAttribute('stroke-width', '1.5');
  svg.appendChild(c);
  svg.dataset.icon = name || '';
  return svg;
}

/** util/fmt.js 가 없을 때의 안전 폴백(셸이 쓰는 최소분) */
const fallbackFmt = {
  L: (en, ko) => (getState().lang === 'en' ? en : ko),
  clamp: (v, a, b) => Math.max(a, Math.min(b, v)),
  fmtAgo(ts) {
    const s = Math.max(0, Math.round((Date.now() - ts) / 1000));
    if (s < 60) return L(s + 's ago', s + '초 전');
    const m = Math.round(s / 60);
    if (m < 60) return L(m + 'm ago', m + '분 전');
    const h = Math.round(m / 60);
    return L(h + 'h ago', h + '시간 전');
  },
};

async function loadShared() {
  const [dom, svg, fmt, icon, compute, dat, snd] = await Promise.all([
    tryImport('./util/dom.js', 'util/dom.js'),
    tryImport('./util/svg.js', 'util/svg.js'),
    tryImport('./util/fmt.js', 'util/fmt.js'),
    tryImport('./util/icon.js', 'util/icon.js'),
    tryImport('./model/compute.js', 'model/compute.js'),
    tryImport('./model/data.js', 'model/data.js'),
    tryImport('./util/sound.js', 'util/sound.js'),
  ]);
  util.dom = dom || { $, $$, tpl: id => document.getElementById(id).content.firstElementChild.cloneNode(true), clear: n => { n.innerHTML = ''; } };
  util.svg = svg || {};
  util.fmt = fmt ? { ...fallbackFmt, ...fmt } : fallbackFmt;
  util.icon = (icon && (icon.icon || icon.default)) || fallbackIcon;
  model = compute || {};
  data = dat || null;
  sound = snd || null;
}

// ---------------------------------------------------------------------------
// i18n 헬퍼 (§0-5)
// ---------------------------------------------------------------------------
function L(en, ko) { return getState().lang === 'en' ? en : ko; }
function titleOf(key) { const m = screenMeta(key); return L(m.en, m.ko); }

// ---------------------------------------------------------------------------
// 라우팅 (§3.3)
// ---------------------------------------------------------------------------

/** '#/detail/abc' → {view:'detail', selected:'abc'} · 알 수 없으면 overview */
export function parseHash(hash) {
  const raw = String(hash == null ? location.hash : hash).replace(/^#\/?/, '');
  // malformed 퍼센트 인코딩(#/detail/% 등)에서 decodeURIComponent 가 throw 해 부팅이 통째로
  // 죽는 것을 막는다 — 디코딩 실패 세그먼트는 원문 그대로 둔다(알 수 없는 화면이면 아래에서 overview 폴백).
  const parts = raw.split('/').filter(Boolean).map((seg) => {
    try { return decodeURIComponent(seg); } catch (e) { return seg; }
  });
  if (!parts.length) return { view: 'overview', selected: null };
  // 레거시: logs 화면은 incidents(경보·로그)로 통합됐다. #/logs 는 '로그' 탭, #/incidents 는 '카드'(경보)
  // 탭을 기본 선택하도록 라우트→탭(alertView)을 바인딩한다(해시 유지 — hashOf 가 alertView 로 되돌린다).
  if (parts[0] === 'logs') return { view: 'incidents', selected: null, alertView: 'log' };
  const view = parts[0];
  if (!SCREEN_KEYS.includes(view)) return { view: 'overview', selected: null };
  if (view === 'detail') {
    if (!parts[1]) return { view: 'overview', selected: null };
    return { view: 'detail', selected: parts[1] };
  }
  // '통계'(stats) 탭도 해시로 복원된다(#/incidents/stats ↔ alertView 'stats', #520).
  // 없으면 stats 탭에서 새로고침·detail 왕복 Back 시 #/incidents 만 남아 cards 탭으로 착지했다.
  if (view === 'incidents') {
    return { view: 'incidents', selected: null, alertView: parts[1] === 'stats' ? 'stats' : 'cards' };
  }
  return { view, selected: null };
}

/** state → 해시 문자열 */
export function hashOf(state) {
  if (state.view === 'detail' && state.selected != null) {
    return '#/detail/' + encodeURIComponent(state.selected);
  }
  // incidents 통합 화면: '로그' 탭은 #/logs, '통계' 탭은 #/incidents/stats,
  // '카드'(경보) 탭은 #/incidents 로 해시 유지(라우트↔탭 바인딩).
  if (state.view === 'incidents' && state.alertView === 'log') return '#/logs';
  if (state.view === 'incidents' && state.alertView === 'stats') return '#/incidents/stats';
  return '#/' + state.view;
}

let hashLock = false;   // hashchange 처리 중 pushState 억제

function syncHash(st) {
  if (hashLock) return;
  const want = hashOf(st);
  if (location.hash !== want) {
    try { history.pushState(null, '', want); } catch (e) { location.hash = want; }
  }
}

/**
 * 딥링크 라우트 검증 — `#/detail/<id>`의 id가 fleet에 없으면 overview로 보낸다(§7).
 * 단, 첫 pull이 끝나 데이터 소스가 확정되기 전에는 검증하지 않는다 —
 * boot()가 채우는 placeholder 시뮬 플릿(27대)은 라이브 52대와 id 체계가 달라,
 * 여기서 검증하면 새로고침·URL 직접 접속 딥링크가 전부 overview로 튕긴다.
 * 확정 전 미존재 id는 detail의 빈 상태(EMPTY_DEV)가 받아주고, 확정 후
 * applyHash() 1회 재실행으로 진짜 없는 id만 정정한다.
 */
let dataSettled = false;
function resolveRoute(route, st) {
  if (!route || route.view !== 'detail') return route;
  if (!dataSettled) return route;
  const fleet = (st && st.fleet) || [];
  if (!fleet.length) return route;
  const ok = fleet.some(d => String(d.id) === String(route.selected));
  return ok ? route : { view: 'overview', selected: null };
}

function applyHash() {
  const parsed = parseHash();
  const st = getState();
  const route = resolveRoute(parsed, st);
  const rewritten = route.view !== parsed.view || route.selected !== parsed.selected;
  const tabChg = route.alertView != null && st.alertView !== route.alertView;
  if (st.view !== route.view || st.selected !== route.selected || tabChg) {
    hashLock = true;
    const patch = {
      view: route.view, selected: route.selected,
      originView: route.view === 'detail' ? st.originView : route.view,
    };
    if (route.alertView != null) patch.alertView = route.alertView;
    setState(patch);
    hashLock = false;
  }
  if (rewritten) {
    // 유효하지 않은 해시는 히스토리에 남기지 않고 제자리 정정한다.
    const want = hashOf(getState());
    if (location.hash !== want) {
      try { history.replaceState(null, '', want); } catch (e) { location.hash = want; }
    }
  }
  // B(딥링크): 존재하지 않는 detail id 는 '무음' overview 리다이렉트 대신 토스트로 알린다.
  //   resolveRoute 는 데이터 확정(dataSettled) 후 fleet 에 진짜 없는 id 만 overview 로 바꾸므로,
  //   유효 id 딥링크(데이터 도착 전 포함)는 detail 그대로 통과 → 여기 걸리지 않는다(회귀 금지).
  if (parsed.view === 'detail' && parsed.selected != null && route.view !== 'detail') {
    showToast(L(parsed.selected + ' not found — showing overview', parsed.selected + ' 장비 없음 — 개요로 이동'));
  }
}

/** 레일/일반 화면 이동 */
function goView(key) {
  if (key === 'logs') key = 'incidents';                 // logs → incidents 통합
  if (!SCREEN_KEYS.includes(key) || key === 'detail') key = 'overview';
  setState({ view: key, selected: null, originView: key });
}
/** 드릴다운 (§3.3) */
function goDetail(id) {
  if (id == null) return;
  const st = getState();
  setState({ view: 'detail', selected: String(id), originView: st.view === 'detail' ? st.originView : st.view });
}
/** 뒤로 (§3.3) */
function goBack() {
  const st = getState();
  const back = SCREEN_KEYS.includes(st.originView) && st.originView !== 'detail' ? st.originView : 'overview';
  setState({ view: back, selected: null });
}

// ---------------------------------------------------------------------------
// 토스트
// ---------------------------------------------------------------------------
let toastTimer = null;
function showToast(msg) {
  clearTimeout(toastTimer);
  setState({ toast: msg });
  // 연속 동일 문구 토스트(#616): 각 showToast 호출은 별개의 사용자 작업에 대한 확인이다.
  // announce 의 msg === lastAnnounce 디듑에 두 번째 호출이 삼켜지지 않도록 호출 시점에
  // 디듑 키를 비운다 — 배너(#583)와 달리 등장 전이 같은 리셋 트리거가 없기 때문이다.
  lastAnnounce = '';
  // 실시간 콘솔인데 aria-live 리전이 0개였다 — 상태 전이 토스트를 AT 에도 전달한다.
  announce(msg);
  // 같은 문구가 연속이면 토스트가 unhidden 상태로 잔류해 toastup 애니메이션이 재생되지
  // 않는다 — reflow 로 재생시켜 시각 확인 피드백도 복구한다(#616).
  if (shell.toast && !shell.toast.hidden) {
    shell.toast.style.animation = 'none';
    void shell.toast.offsetWidth; // reflow 강제 — 애니메이션 재시작
    shell.toast.style.animation = '';
  }
  toastTimer = setTimeout(() => setState({ toast: null }), 2400);
}

// ---------------------------------------------------------------------------
// 좌측 레일 펼침/접힘 (localStorage 'sd.railOpen')
// ---------------------------------------------------------------------------
function positionRailIndicator() {
  const ind = shell.railInd;
  if (!ind || !shell.rail) return;
  const act = shell.rail.querySelector('.rail-icon.is-active');
  if (!act) { ind.classList.remove('is-on'); return; }
  ind.style.top = act.offsetTop + 'px';
  ind.style.left = act.offsetLeft + 'px';
  ind.style.width = act.offsetWidth + 'px';
  ind.style.height = act.offsetHeight + 'px';
  ind.classList.add('is-on');
}

function toggleRail() {
  setState(s => ({ railOpen: !s.railOpen }));
  persist(getState());
  // 레일 폭 전환(.2s ease) 완료 후, resize 리스너를 가진 화면(예: 토폴로지)이
  // 새 폭에 맞게 스스로 refit 하도록 신호를 보낸다. js/screens/* 는 건드리지 않는다.
  setTimeout(() => { try { window.dispatchEvent(new Event('resize')); } catch (e) { /* noop */ } }, 230);
}

// ---------------------------------------------------------------------------
// 파생 모델 메모 (state 객체 동일성 기준 1회만 계산)
// ---------------------------------------------------------------------------
let memoState = null;
let memoModel = null;
function getModel(st) {
  if (memoState === st) return memoModel;
  let m = null;
  if (model && typeof model.buildModel === 'function') {
    try { m = model.buildModel(st); } catch (e) { m = null; }
  }
  memoState = st;
  memoModel = m;
  return m;
}

// ---------------------------------------------------------------------------
// 화면 모듈 로딩 · 스왑 (§3.3 / §5.1)
// ---------------------------------------------------------------------------
const moduleCache = new Map();

function placeholderModule(key) {
  return {
    key,
    title: { en: screenMeta(key).en, ko: screenMeta(key).ko },
    icon: screenMeta(key).icon,
    init(root) {
      root.innerHTML = '';
      const box = document.createElement('div');
      box.className = 'card u-empty';
      box.setAttribute('data-placeholder', key);
      const t = document.createElement('div');
      t.className = 'u-empty-title';
      t.textContent = titleOf(key);
      const s = document.createElement('div');
      s.className = 'u-empty-sub u-muted';
      s.textContent = L('This screen is not available yet.', '준비 중입니다.');
      box.appendChild(t); box.appendChild(s);
      root.appendChild(box);
    },
    render() {},
    destroy() {},
  };
}

async function loadScreen(key) {
  if (moduleCache.has(key)) return moduleCache.get(key);
  const mod = await tryImport('./screens/' + key + '.js', 'screens/' + key + '.js');
  const impl = mod && (mod.default || mod);
  const ok = impl && typeof impl.init === 'function';
  // Import 실패는 캐시하지 않는다. 네트워크/일시적 모듈 오류가 복구된 뒤
  // 다음 화면 전환에서 재시도할 수 있어야 한다.
  if (!ok) return placeholderModule(key);
  moduleCache.set(key, impl);
  return impl;
}

let wantKey = null;      // 요청된 화면 key
let mountedKey = null;   // 실제 마운트된 화면 key
let mountedMod = null;
let mountedRoot = null;
// 최초 화면 스왑 여부 — 첫 렌더에 포커스를 훔치면 skip link 가 키보드로 도달 불가해진다.
let swappedOnce = false;

async function swapView(key) {
  wantKey = key;
  const mod = await loadScreen(key);
  if (wantKey !== key) return;                 // 그 사이 다른 화면 요청 → 폐기
  if (mountedMod && typeof mountedMod.destroy === 'function') {
    try { mountedMod.destroy(); } catch (e) { console.warn('[serverdesk] destroy 실패: ' + mountedKey, e); }
  }
  shell.screenRoot.innerHTML = '';
  mountedRoot = document.createElement('section');
  mountedRoot.className = 'screen';
  mountedRoot.dataset.screen = key;
  shell.screenRoot.appendChild(mountedRoot);
  mountedMod = mod;
  mountedKey = key;
  try {
    mod.init(mountedRoot, ctx);
  } catch (e) {
    console.error('[serverdesk] init 실패: ' + key, e);
    // 화면 init 실패도 빈 main으로 남기지 않는다. 동일한 셸 안에서
    // 재시도 가능한 명시적 placeholder를 보여준다.
    mountedMod = placeholderModule(key);
    mountedMod.init(mountedRoot, ctx);
  }
  renderActive(getState());
  markEnterCards(mountedRoot);   // 화면 진입 카드 스태거: 스왑 시점 1회만 부여(재렌더 시 미호출 — 재생 방지)
  
  // A11y: 화면 '전환' 후에는 메인으로 포커스를 옮겨 스크린리더가 새 화면을 읽게 한다.
  // 단 최초 로드에서는 옮기면 안 된다 — 실측: 첫 렌더에 main 이 포커스를 가져가 버려
  // Tab 이 본문 한가운데서 시작했고, DOM 0번인 '본문 바로가기'(skip link)에 키보드로
  // 영영 도달할 수 없었다. 최초 1회는 문서 시작 지점을 그대로 둔다.
  if (shell.screenRoot && swappedOnce) {
    try { shell.screenRoot.focus(); } catch (e) { /* noop */ }
  }
  swappedOnce = true;
}

/**
 * 화면 진입 카드 스태거(CSS riseIn, styles.css `.screen .card`) — 스왑 직후 1회만 새 root의
 * .card 들에 순서 인덱스를 --enter-i로 부여한다. init()에서 바로 만드는 카드뿐 아니라 첫
 * render()에서 비로소 생성되는 카드(nodes/clusters 카드형, detail)까지 잡도록 renderActive
 * 이후에 호출한다 — 아직 페인트 전이라(동기 실행) 깜빡임 없이 첫 프레임부터 지연값이 반영된다.
 * 최대 10까지만 인덱스를 늘려 카드가 많은 화면에서 마지막 카드가 과도하게 늦게 뜨지 않게 한다.
 */
function markEnterCards(root) {
  const cards = root.querySelectorAll('.card');
  for (let i = 0; i < cards.length; i++) {
    cards[i].style.setProperty('--enter-i', String(Math.min(i, 10)));
  }
}

function renderActive(st) {
  if (!mountedMod || typeof mountedMod.render !== 'function') return;
  try { mountedMod.render(st, ctx); } catch (e) { console.error('[serverdesk] render 실패: ' + mountedKey, e); }
}

// ---------------------------------------------------------------------------
// ctx (화면 모듈에 전달, §5.1)
// ---------------------------------------------------------------------------
const ctx = {
  store,
  get model() { return model; },
  util,
  // 편의 API (화면 모듈 공통 소비)
  L,
  getModel,
  goView,
  goDetail,
  goBack,
  showToast,
  screens: SCREENS,
};

// ---------------------------------------------------------------------------
// 셸 렌더
// ---------------------------------------------------------------------------
let prevLang = null;
let prevColors = null;
let prevTheme = null;
let prevSetg = null;
let prevAck = null;
let prevMaint = null;
let prevNotes = null;
let prevNotify = null;

function renderShell(st) {
  const m = getModel(st);

  // 언어
  const langChanged = prevLang !== st.lang;
  if (langChanged) {
    document.documentElement.lang = st.lang === 'en' ? 'en' : 'ko';
    // util/fmt.js 의 L()은 모듈 스코프 currentLang 기준이라 셸이 동기화해줘야 한다(F4 계약).
    if (util.fmt && typeof util.fmt.setLang === 'function') util.fmt.setLang(st.lang);
    if (shell.skipLink) shell.skipLink.textContent = L('Skip to main content', '본문 바로가기');
    if (shell.langToggle) shell.langToggle.setAttribute('aria-label', L('Switch language', '언어 전환'));
    if (shell.bell) shell.bell.setAttribute('aria-label', L('Alerts', '알림'));
    prevLang = st.lang;
  }
  setField('langLabel', L('EN', '한국어'));
  setField('brandSub', 'FT Console');
  setField('profileRole', L('Admin', '관리자'));
  setField('searchKbd', searchShortcutLabel());
  // 검색 인풋 placeholder/aria 는 index.html 하드코딩이라 lang 전환 시 셸이 갱신해줘야 한다.
  if (langChanged && shell.searchInput) {
    shell.searchInput.setAttribute('placeholder', L('Search devices · alerts', '노드·경보 검색'));
    shell.searchInput.setAttribute('aria-label', L('Search', '검색'));
  }

  // 브레드크럼
  if (st.view === 'detail') {
    const rootKey = SCREEN_KEYS.includes(st.originView) && st.originView !== 'detail' ? st.originView : 'overview';
    shell.crumbRoot.textContent = titleOf(rootKey);
    show(shell.crumbSep, true);
    show(shell.crumbLeaf, true);
    const dev = (st.fleet || []).find(d => String(d.id) === String(st.selected));
    shell.crumbLeaf.textContent = dev ? (dev.host || dev.id) : String(st.selected || '');
  } else {
    shell.crumbRoot.textContent = titleOf(st.view);
    show(shell.crumbSep, false);
    show(shell.crumbLeaf, false);
    shell.crumbLeaf.textContent = '';
  }

  // 레일 펼침/접힘 상태
  if (shell.rail) shell.rail.classList.toggle('is-open', !!st.railOpen);
  if (shell.railToggle) {
    shell.railToggle.setAttribute('aria-expanded', st.railOpen ? 'true' : 'false');
    const tip = st.railOpen ? L('Collapse sidebar', '사이드바 접기') : L('Expand sidebar', '사이드바 펼치기');
    shell.railToggle.setAttribute('aria-label', tip);
    shell.railToggle.title = tip + ' ([)';
  }

  // 레일: 활성 + 툴팁 + 배지 + 펼침 라벨
  const badges = railBadges(st, m);
  $$('[data-rail-icon]').forEach(el => {
    const key = el.dataset.railIcon;
    const isActive = key === st.view;
    el.classList.toggle('is-active', isActive);
    if (isActive) el.setAttribute('aria-current', 'page');
    else el.removeAttribute('aria-current');
    const t = st.lang === 'en' ? el.dataset.titleEn : el.dataset.titleKo;
    if (t) { el.title = t; el.setAttribute('aria-label', t); }
    const lab = el.querySelector('[data-rail-label]');
    if (lab && t) lab.textContent = t;
    const b = el.querySelector('[data-rail-badge]');
    if (b) {
      let n = badges[key] || 0;
      // 경보(벨) 배지: 누적 알림 '총합'을 상시 red 로 태우면 과대표현(총합 110 → 99+ red)이라, 다른
      // 총합 배지처럼 중립으로 두고 red 는 '심각(critical) 건수'만 표기한다(C1).
      //   · critical>0 → 배지는 심각 건수(예: 11)만, red(기본).  ← 노드 down 배지와 같은 '실장애 카운트' 언어
      //   · critical=0 → 총합을 중립색(is-neutral)으로.          ← 다른 총합 배지와 통일
      // 노드(down) 배지 등 다른 배지는 기본 red 유지(오프라인=실장애 카운트).
      if (key === 'incidents') {
        const crit = (m && m.incStats && typeof m.incStats.critical === 'number') ? m.incStats.critical : 0;
        b.classList.remove('is-warn');
        if (crit > 0) { n = crit; b.classList.remove('is-neutral'); }
        else { b.classList.add('is-neutral'); }
      }
      b.textContent = n > 99 ? '99+' : String(n);
      b.hidden = !n;
    }
  });

  // 슬라이딩 인디케이터를 활성 레일 아이콘 위치로 — transform 이 아니라 top/left 를 쓰는 이유:
  // 펼침 모드에서 width/height 도 함께 변해 박스 4속성을 같은 토큰 전환으로 묶는 게 단순하다.
  positionRailIndicator();

  // 레일 하단: live/sim + fleet 가용성
  if (shell.sourceBadge) {
    shell.sourceBadge.dataset.source = st.source;
    shell.sourceBadge.classList.toggle('is-live', st.source === 'live');
    const dot = shell.sourceBadge.querySelector('.u-dot');
    if (dot) {
      dot.classList.toggle('is-pos', st.source === 'live');
      dot.classList.toggle('is-warn', st.source !== 'live');
      dot.classList.toggle('pulse', true);
    }
    setField('sourceLabel', st.source === 'live' ? 'LIVE' : 'SIM');
  }
  setField('fleetAvail', (m && m.kpi && m.kpi.avail) || '—');

  // 알림 종
  renderBell(st, m);

  // 폴러 단절 배너 (시뮬 모드는 표시 안 함)
  renderBanner(st);

  // 전역 검색
  renderSearch(st, m);

  // 토스트
  if (shell.toast) {
    setField('toastText', st.toast || '');
    shell.toast.hidden = !st.toast;
  }

  // 고밀도 모드
  document.body.classList.toggle('is-dense', !!(st.setg && st.setg.dense));

  // 영속 — setg(자동새로고침·사운드·고밀도)도 변경 감지 대상에 포함한다.
  // 이전엔 lang/colors/theme 만 봐서 스위치를 켜도 저장이 트리거되지 않았다.
  if (langChanged || prevColors !== st.companyColors || prevTheme !== st.theme || prevSetg !== st.setg || prevAck !== st.ackedAlerts || prevMaint !== st.maint || prevNotes !== st.notes || prevNotify !== st.notify) {
    persist(st);
    prevColors = st.companyColors;
    prevTheme = st.theme;
    prevSetg = st.setg;
    prevNotify = st.notify;
    // 확인 상태가 바뀌면 서버에도 밀어 넣는다(다중 운영자 공유).
    // 최초 1회(prevAck===null)는 boot 의 합집합 동기화가 이미 처리하므로 건너뛴다.
    // 실패해도 무시한다 — localStorage 에는 이미 저장됐고, 서버는 옵션이다.
    if (prevAck !== null && prevAck !== st.ackedAlerts && data && typeof data.pushAck === 'function') {
      // 전체 맵이 아니라 '무엇이 바뀌었는지'만 보낸다 — 동시 운영자 덮어쓰기 방지(실측 버그).
      data.pushAck(data.ackDelta(prevAck, st.ackedAlerts || {}), 2500);
    }
    prevAck = st.ackedAlerts;
    // 점검 창도 같은 경로로 공유한다(델타·락 병합은 serve.py /maint 가 ack 와 같이 처리).
    if (prevMaint !== null && prevMaint !== st.maint && data && typeof data.pushMaint === 'function') {
      data.pushMaint(data.ackDelta(prevMaint, st.maint || {}), 2500);
    }
    prevMaint = st.maint;
    // 장비 메모도 같은 경로로 공유한다.
    if (prevNotes !== null && prevNotes !== st.notes && data && typeof data.pushNotes === 'function') {
      data.pushNotes(data.ackDelta(prevNotes, st.notes || {}), 2500);
    }
    prevNotes = st.notes;
  }

  // 해시 동기화
  syncHash(st);
}

/** 레일 배지 카운트 — compute 모델이 없으면 fleet에서 직접 센다. */
function railBadges(st, m) {
  const out = {};
  if (m && m.incStats && typeof m.incStats.total === 'number') {
    out.incidents = m.incStats.total;
  } else {
    out.incidents = (st.fleet || []).reduce((n, d) => n + (((d.meta && d.meta.alerts) || []).length), 0);
  }
  const down = (st.fleet || []).filter(d => d.status === 'down').length;
  if (down) out.nodes = down;
  return out;
}

function renderBell(st, m) {
  if (!shell.bellDot) return;
  const list = (m && Array.isArray(m.alerts)) ? m.alerts : [];
  let sev = null;
  if (list.length) {
    const has = k => list.some(a => (a.sev || a.severity || '').toLowerCase().indexOf(k) === 0);
    sev = has('c') ? 'neg' : has('w') ? 'warn' : 'info';
  } else {
    const down = (st.fleet || []).some(d => d.status === 'down');
    const deg = (st.fleet || []).some(d => d.status === 'deg');
    sev = down ? 'neg' : deg ? 'warn' : null;
  }
  shell.bellDot.classList.remove('is-neg', 'is-warn', 'is-info');
  if (sev) shell.bellDot.classList.add('is-' + sev);
  shell.bellDot.hidden = !sev;
}

let bannerDismissedAt = 0;
// 시뮬 폴핵 에피소드 진입 시각. sim 소스에서는 tick 루프가 lastPoll 을 매번 현재 시각으로
// 갱신하므로(computeStale·LIVE 인디케이터가 쓰는 동작이라 건드리지 않는다) dismiss 비교를
// lastPoll 에 걸면 닫기 직후 다음 틱에 배너가 부활한다 — 에피소드 진입 시각으로 대체한다.
let simFallbackSince = 0;
// 등장 1회 통지용 — 직전 렌더의 배너 표시 상태(#577). 배너는 role=alert 가 아니므로
// textContent 갱신이 AT 에 재통지되지 않고, 등장 전이 때만 announce 로 알린다.
let bannerWasOn = false;

function renderBanner(st) {
  if (!shell.banner) return;
  // 닫기(×) 아이콘 전용 버튼의 접근 이름 — 언어 전환 추적은 renderShell 의 searchInput 과 같은
  // 패턴으로, 배너 표시 여부와 무관하게 매 렌더 갱신한다(#552, 모달 닫기의 L('Close','닫기') 관례).
  if (shell.bannerX) shell.bannerX.setAttribute('aria-label', L('Close', '닫기'));
  const liveStale = st.source === 'live' && st.stale;
  // 폴러 단절로 시뮬 fleet 으로 갈아탄 상태: 가짜 플릿이 정상처럼 보여도 배너 지속.
  const simFallback = st.source === 'sim' && st.simFallback;
  if (simFallback && !simFallbackSince) simFallbackSince = Date.now();
  else if (!simFallback) simFallbackSince = 0;   // live 복귀 — 다음 에피소드는 새 dismiss 단위
  const dismissBasis = simFallback ? simFallbackSince : st.lastPoll;
  const on = (liveStale || simFallback) && dismissBasis > bannerDismissedAt;
  if (!on) { shell.banner.hidden = true; bannerWasOn = false; return; }
  let text;
  if (simFallback) {
    text = L(
      'Collector disconnected — showing simulation data',
      '수집기 연결 끊김 — 시뮬레이션 데이터 표시 중'
    );
  } else {
    const secs = st.lastPoll ? Math.round((Date.now() - st.lastPoll) / 1000) : 0;
    const reason = st.liveError ? String(st.liveError) : L('no response from collector', '수집기 응답 없음');
    text = L(
      'Poller disconnected — last collected ' + secs + 's ago (' + reason + ')',
      '수집기 연결 끊김 — 마지막 수집 ' + secs + '초 전 (' + reason + ')'
    );
  }
  // 등장(숨김→표시) 시 1회만 통지 — 이후 카운트다운 갱신은 라이브 리전 밖에서 조용히(#577).
  // 새 에피소드 등장 전이에서 디듑 키를 비운다(#583) — 직전 에피소드와 문구가 같아도
  // (시뮬 폴핵은 언어별 상수 문자열이라 항상 동일) announce 의 lastAnnounce 디듑에
  // 삼켜지지 않고 재등장이 AT 에 통지된다. 에피소드 내 1회 통지는 bannerWasOn 이 보장.
  if (!bannerWasOn) lastAnnounce = '';
  if (!bannerWasOn) announce(text);
  bannerWasOn = true;
  setField('bannerText', text);
  shell.banner.hidden = false;
}

// ---------------------------------------------------------------------------
// 전역 검색 (§1 공통 셸)
// ---------------------------------------------------------------------------
let searchNodes = [];
let prevSearchKey = null;
let prevSearchQuery = null;  // 직전 검색어 — 변경 시 활성 인덱스 리셋
let searchActive = 0;       // Enter 대상 인덱스(기본 0 = 첫 결과)
let searchNavigated = false; // 화살표로 이동했는지 — false면 하이라이트 미표시

function applySearchActive() {
  searchNodes.forEach((n, i) => {
    const active = searchNavigated && i === searchActive;
    n.classList.toggle('is-active', active);
    n.setAttribute('aria-selected', active ? 'true' : 'false');
  });
}

function searchItems(st, m) {
  const q = (st.search || '').trim().toLowerCase();
  if (!q) return [];
  if (m && Array.isArray(m.searchResults)) return m.searchResults.slice(0, 8);
  // 폴백: fleet(노드) + alerts 혼합
  const out = [];
  (st.fleet || []).forEach(d => {
    const hay = [d.host, d.site, d.type, d.id].join(' ').toLowerCase();
    if (hay.indexOf(q) >= 0) out.push({ kind: 'node', id: d.id, label: d.host || d.id, meta: (d.site || '—') + ' · ' + (d.type || ''), status: d.status });
  });
  (st.fleet || []).forEach(d => {
    ((d.meta && d.meta.alerts) || []).forEach(a => {
      const txt = ((a.name || '') + ' ' + (a.desc || '')).trim();
      if (txt.toLowerCase().indexOf(q) >= 0) {
        // 모델 경로(compute.js)와 정합 — status 는 심각도 문자열, tone 은 sevTone(neg|warn|info).
        const sev = a.sev || 'info';
        out.push({ kind: 'alert', id: d.id, label: txt, meta: d.host || d.id,
          status: sev, tone: sev === 'critical' ? 'neg' : sev === 'warning' ? 'warn' : 'info' });
      }
    });
  });
  return out.slice(0, 8);
}

function renderSearch(st, m) {
  if (!shell.searchResults) return;
  if (shell.searchInput && shell.searchInput.value !== (st.search || '') && document.activeElement !== shell.searchInput) {
    shell.searchInput.value = st.search || '';
  }
  const items = searchItems(st, m);
  const open = !!(st.search || '').trim();
  // 검색어가 바뀌면(결과 목록이 그대로여도) 활성 인덱스 리셋.
  const q = st.search || '';
  if (q !== prevSearchQuery) { prevSearchQuery = q; searchActive = 0; searchNavigated = false; }
  // 재빌드 스킵 키에는 상태 재료(status/tone — 도트 색)와 meta(호스트·시각 표기)도 넣는다.
  // kind:id:label 만으로 만들면 검색어가 그대로인 채 배경 틱(1.2s)으로 장비 상태가
  // op→down 등으로 바뀌어도 열린 드롭다운의 도트 색·meta 가 갱신되지 않았다(#519).
  const key = (open ? 'q|' : '') + items.map(i =>
    i.kind + ':' + i.id + ':' + i.label + ':' + (i.status || '') + ':' + (i.tone || '') + ':' + (i.meta || '')
  ).join('|');
  if (key !== prevSearchKey) {
    prevSearchKey = key;
    // 결과 목록이 바뀌면 활성 인덱스 리셋(포커스 이동 중 배경 폴링으로 목록이 갈릴 때 방어).
    searchActive = 0;
    searchNavigated = false;
    shell.searchResults.innerHTML = '';
    if (open && !items.length) {
      const d = document.createElement('div');
      d.className = 'hd-search-empty u-muted';
      d.textContent = L('No results', '결과 없음');
      shell.searchResults.appendChild(d);
    }
    searchNodes = items.map(it => {
      const n = document.getElementById('tpl-search-item').content.firstElementChild.cloneNode(true);
      n.dataset.searchGo = it.id;
      n.setAttribute('role', 'option');
      n.setAttribute('aria-selected', 'false');
      const dot = n.querySelector('.u-dot');
      if (dot) {
        dot.classList.remove('is-pos', 'is-warn', 'is-neg');
        // node 항목의 status 는 장비 상태(op|deg|down)지만, incident/alert 항목은 compute 가
        // 심각도 문자열(critical|warning|info)을 status 로 납품한다(compute.js searchResults).
        // 그대로 장비 매핑에 넣으면 critical 경볼에도 is-pos(정상 녹색)이 붙으므로,
        // 경보 항목은 함께 납품되는 tone(sevTone: neg|warn|info)으로 매핑하고
        // info 는 중립(색 클래스 없음)으로 둔다(#518).
        const cls = it.kind === 'node'
          ? (it.status === 'down' ? 'is-neg' : it.status === 'deg' ? 'is-warn' : 'is-pos')
          : (it.tone === 'neg' ? 'is-neg' : it.tone === 'warn' ? 'is-warn' : null);
        if (cls) dot.classList.add(cls);
      }
      n.querySelector('.hd-search-item-name').textContent = it.label;
      n.querySelector('.hd-search-item-meta').textContent = it.meta || '';
      shell.searchResults.appendChild(n);
      return n;
    });
  }
  shell.searchResults.hidden = !open;
  if (shell.searchWrap) shell.searchWrap.classList.toggle('is-open', open);
  if (shell.searchInput) shell.searchInput.setAttribute('aria-expanded', open ? 'true' : 'false');
  applySearchActive();
}

// ---------------------------------------------------------------------------
// 메인 render (store 구독)
// ---------------------------------------------------------------------------
function render(st) {
  // 화면 모듈은 import/init/render/destroy 가 각각 try-catch 로 감싸져 있는데
  // 셸 자신(renderShell)만 무방비였다 — 셸 렌더 중 throw 가 setState 호출자(클릭 핸들러)까지
  // 거슬러 올라가 UI 를 통째로 멈출 수 있었다. 셸도 동일한 격리 계약을 적용한다.
  try {
    renderShell(st);
  } catch (e) {
    console.error('[app] renderShell failed', e);
  }
  if (st.view !== wantKey) { swapView(st.view); return; }
  if (mountedKey === st.view) renderActive(st);
}

/**
 * 스크린리더 라이브 리전에 상태를 알린다(§a11y).
 * index.html 의 #sd-live(aria-live=polite)가 없으면 조용히 무시한다.
 */
let lastAnnounce = '';
export function announce(msg) {
  if (!msg || msg === lastAnnounce) return;
  lastAnnounce = msg;
  const n = document.getElementById('sd-live');
  if (!n) return;
  // 같은 문자열 재대입은 AT 가 무시하므로 비웠다가 다음 프레임에 채운다.
  n.textContent = '';
  requestAnimationFrame(() => { n.textContent = msg; });
}

// ---------------------------------------------------------------------------
// 전역 data-action 위임 스위치 (§5.2)
// ---------------------------------------------------------------------------
function handleAction(action, el) {
  switch (action) {
    case 'toggleLang':
      setState(s => ({ lang: s.lang === 'ko' ? 'en' : 'ko' }));
      persist(getState());
      break;
    case 'toggleRail': toggleRail(); break;
    // #514: 벨 '알림' 버튼도 경보 카드 탭이 목적지다. 개요 '전체 보기'(#486)·detail
    // '전체 인시던트 보기'(#509)와 같은 패턴으로 목적 탭을 명시 패치한다 — 미패치 시
    // 직전 log/stats 탭으로 착지했다.
    case 'goIncidents': setState({ alertView: 'cards' }); goView('incidents'); break;
    case 'goBack': goBack(); break;
    case 'goto': goView(el && (el.dataset.view || el.dataset.goto)); break;
    case 'goDetail': goDetail(el && (el.dataset.id || el.dataset.detailId)); break;
    case 'dismissBanner': bannerDismissedAt = Date.now(); renderBanner(getState()); break;
    case 'togglePause': setState(s => ({ logPaused: !s.logPaused })); break;
    case 'clearSearch': setState({ search: '' }); break;
    case 'toast': showToast((el && el.dataset.toastMsg) || ''); break;
    // 경보 확인/해제 — 원본은 지우지 않고 확인 표시만 토글한다(폴러는 해제 API 가 없다).
    // 값은 확인 시각(ISO) — 나중에 '확인한 지 N일' 표기에 쓸 수 있게 boolean 대신 타임스탬프.
    // 점검 창 설정/해제 — data-maint-id + data-maint-hours(0이면 해제).
    case 'maintSet': {
      const id = el && el.dataset.maintId;
      if (!id) break;
      const hours = Number(el.dataset.maintHours || 0);
      setState((st) => {
        const next = Object.assign({}, st.maint);
        if (hours > 0) {
          next[id] = {
            until: new Date(Date.now() + hours * 3600 * 1000).toISOString(),
            note: el.dataset.maintNote || '',
            by: 'console',
            ts: new Date().toISOString(),
          };
        } else {
          delete next[id];
        }
        return { maint: next };
      });
      showToast(hours > 0
        ? L('Maintenance window set (' + hours + 'h)', '점검 모드 ' + hours + '시간 설정')
        : L('Maintenance window cleared', '점검 모드를 해제했습니다'));
      break;
    }
    case 'ackAlert': {
      const k = el && el.dataset.ackKey;
      if (!k) break;
      setState((s) => {
        const next = Object.assign({}, s.ackedAlerts);
        if (next[k]) delete next[k]; else next[k] = new Date().toISOString();
        return { ackedAlerts: next };
      });
      showToast(getState().ackedAlerts[k]
        ? L('Alert acknowledged', '경보를 확인 처리했습니다')
        : L('Acknowledgement removed', '확인을 해제했습니다'));
      break;
    }
    // 일괄 확인 — 31건을 한 건씩 누르게 두면 실제로는 아무도 안 쓴다.
    case 'ackAllVisible': {
      const keys = String((el && el.dataset.ackKeys) || '').split('\u0002').filter(Boolean);
      if (!keys.length) break;
      setState((s) => {
        const next = Object.assign({}, s.ackedAlerts);
        const now = new Date().toISOString();
        keys.forEach((k) => { next[k] = now; });
        return { ackedAlerts: next };
      });
      showToast(L(keys.length + ' alerts acknowledged', '경보 ' + keys.length + '건을 확인 처리했습니다'));
      break;
    }
    // 일괄 해제 — 버튼의 data-ack-keys(incidents.js bulkAckKeys, #27)에 실린 '현재 필터
    // 목록의 확인분'만 지운다. 전역 초기화(setState({ ackedAlerts: {} }))는 필터 밖 경보와
    // /ack 델타로 전파돼 타 운영자의 확인까지 되돌렸다. 키가 없으면 아무것도 하지 않는다.
    case 'ackClearAll': {
      const keys = String((el && el.dataset.ackKeys) || '').split('\u0002').filter(Boolean);
      if (!keys.length) break;
      setState((s) => {
        const next = Object.assign({}, s.ackedAlerts);
        keys.forEach((k) => { delete next[k]; });
        return { ackedAlerts: next };
      });
      showToast(L(keys.length + ' acknowledgements removed', '경보 ' + keys.length + '건의 확인을 해제했습니다'));
      break;
    }
    default: break;
  }
}

document.addEventListener('click', e => {
  const t = e.target;
  if (!(t instanceof Element)) return;
  let el;
  // 본문 바로가기(skip link): 기본 앵커 동작을 그대로 두면 location.hash 가 '#screen-root' 로
  // 오염돼 parseHash 가 미등록 세그먼트로 판정 → overview 강제 이동으로 현재 화면이 날아간다(#8).
  // 해시는 건드리지 않고 focus/scroll 만 이동한다. no-JS 에서는 href 앵커가 그대로 동작한다.
  if ((el = t.closest('.skip-link'))) {
    e.preventDefault();
    if (shell.screenRoot) {
      shell.screenRoot.focus({ preventScroll: true });
      shell.screenRoot.scrollIntoView();
    }
    return;
  }
  if ((el = t.closest('[data-search-go]'))) {
    goDetail(el.dataset.searchGo);
    setState({ search: '' });
    if (shell.searchInput) shell.searchInput.value = '';
    return;
  }
  // 검색 드롭다운 바깥 클릭 시 닫기 — 아래 내비게이션 분기들(data-rail-icon/data-action/data-goto)이
  // 조기 return 하므로 반드시 그보다 먼저 수행한다. 예전엔 이 분기가 맨 뒤라 레일 이동 시 건너뛰어져,
  // 팝오버가 새 화면 위에 그대로 남아 클릭을 가로챘다(실측: 설정 이동 후 '다크' 버튼 30초 인터셉트).
  // 입력창 문자열은 renderSearch 가 비포커스 시 state 로 역동기화하므로 state 만 비우면 충분하다.
  if (shell.searchWrap && !t.closest('[data-search-wrap]') && getState().search) {
    setState({ search: '' });
  }
  if ((el = t.closest('[data-rail-icon]'))) { goView(el.dataset.railIcon); return; }
  if ((el = t.closest('[data-action]'))) { handleAction(el.dataset.action, el); return; }
  if ((el = t.closest('[data-goto]'))) { goView(el.dataset.goto); return; }
});

// 검색 결과가 포커스를 받지 않는 상태에서 Tab 등으로 영역을 벗어나도
// 문자열과 결과 목록이 열린 채 남지 않게 닫는다. 결과 버튼으로 이동하는
// 경우에는 다음 포커스가 같은 searchWrap 안이므로 유지한다.
document.addEventListener('focusout', e => {
  if (!shell.searchWrap || !e.target || !shell.searchWrap.contains(e.target)) return;
  queueMicrotask(() => {
    if (!shell.searchWrap.contains(document.activeElement) && getState().search) setState({ search: '' });
  });
});

// 전역 검색 입력
document.addEventListener('input', e => {
  const t = e.target;
  if (t instanceof Element && t.matches('[data-search-input]')) {
    setState({ search: t.value });
  }
});

// ⌘K 포커스 · Esc 초기화 · Enter 진입
document.addEventListener('keydown', e => {
  // '[' 로 레일 토글(입력 중이 아닐 때만)
  if (e.key === '[' && !e.metaKey && !e.ctrlKey && !e.altKey) {
    const a = document.activeElement;
    const typing = a && (a.tagName === 'INPUT' || a.tagName === 'TEXTAREA' || a.isContentEditable);
    if (!typing) { e.preventDefault(); toggleRail(); return; }
  }
  if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
    e.preventDefault();
    // 열린 모달이 있으면 무시한다 — 모달 밖 검색창으로 포커스를 탈취하면 모달 계약
    // (manage.js Tab 트랩·detail.js 확인 입력)이 깨지고, 뒤에 가려진 검색 결과에서
    // Enter(goDetail)로 의도치 않은 화면 이탈이 일어난다(#390).
    // 두 모달 구현을 모두 커버: manage.js 는 열릴 때만 .modal-overlay 가 DOM 에 있고,
    // detail.js 는 상주하되 u-hide 토글로 가시를 바꾼다.
    if (document.querySelector('.modal-overlay:not(.u-hide)')) return;
    if (shell.searchInput) shell.searchInput.focus();
    return;
  }
  // IME 조합 중(isComposing)에는 아래 키들을 가로채지 않는다(#560) — 한글 IME 에서
  // 조합 확정 Enter 를 goDetail 로 오인해 화면 이탈 + 검색어 초기화가 일어났고,
  // 후보창 탐색 ArrowUp/Down 을 preventDefault 로 삼켰다. Escape 도 조합 취소 의도이므로
  // 같은 이유로 IME 에 양보한다(#471 datalist Esc 가드와 같은 계열의 입력 소비 존중).
  if (e.isComposing) return;
  if (e.key === 'Escape') {
    if (getState().search) { setState({ search: '' }); if (shell.searchInput) shell.searchInput.value = ''; }
    if (shell.searchInput) shell.searchInput.blur();
    return;
  }
  if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') &&
      document.activeElement === shell.searchInput && searchNodes.length) {
    e.preventDefault();
    searchNavigated = true;
    const last = searchNodes.length - 1;
    searchActive = e.key === 'ArrowDown'
      ? Math.min(searchActive + 1, last)
      : Math.max(searchActive - 1, 0);
    applySearchActive();
    return;
  }
  if (e.key === 'Enter' && document.activeElement === shell.searchInput) {
    // 화살표로 이동했으면 활성 항목, 아니면 기존대로 첫 결과.
    const target = (searchNavigated && searchNodes[searchActive]) ||
      (shell.searchResults && shell.searchResults.querySelector('[data-search-go]'));
    if (target) {
      goDetail(target.dataset.searchGo);
      setState({ search: '' });
      shell.searchInput.value = '';
    }
  }
});

window.addEventListener('hashchange', applyHash);
window.addEventListener('popstate', applyHash);

// ---------------------------------------------------------------------------
// KST 시계 (1초 자체 갱신 — store 리빌드 유발 금지)
// ---------------------------------------------------------------------------
function kstNow() {
  // UTC+9 고정. 언어 무관 'YYYY-MM-DD HH:MM:SS'
  const d = new Date(Date.now() + 9 * 3600 * 1000);
  const p = n => String(n).padStart(2, '0');
  return d.getUTCFullYear() + '-' + p(d.getUTCMonth() + 1) + '-' + p(d.getUTCDate()) +
    ' ' + p(d.getUTCHours()) + ':' + p(d.getUTCMinutes()) + ':' + p(d.getUTCSeconds());
}
function startClock() {
  const paint = () => { if (shell.clock) shell.clock.textContent = kstNow(); };
  paint();
  setInterval(paint, 1000);
}

// ---------------------------------------------------------------------------
// stale 판정 (§4.4)
// ---------------------------------------------------------------------------
function computeStale(st) {
  if (st.source !== 'live') return false;
  if (st.liveError) return true;
  if (!st.lastPoll) return false;
  return Date.now() - st.lastPoll > (st.refreshSec || 30) * 3 * 1000;
}

/**
 * 오래된 미확인 경보 자동 확인 — settings 의 setg.ackAutoDays(0=해제 · 7 · 30 일).
 * 확인은 '표시'일 뿐 원본 경보는 지우지 않는다. setState 이후 prevAck 감시자가
 * 서버(/ack)에 델타를 밀어 수동 확인과 같은 경로로 다중 운영자와 공유된다.
 * 무시할 수준의 비용(수십 장비 × 수십 경보)이라 tick/pull 완료마다 부른다.
 */
/** 만료된 점검 창 청소 — 로컬 정리가 곧바로 화면 복귀를 만들고, 감시자가 del 델타를 서버에 보낸다. */
/**
 * 에스컬레이션 — critical 이 setg.escHours(4/24시간) 넘게 미확인이면 웹훅 재통보.
 * 중복 발송 방지는 이중이다: 로컬 Set(틱마다 PUT 하지 않게) + 서버 /escal add-if-absent
 * (콘솔이 여러 개 열리든 한 쪽만 added 를 받는다). 클레임 실패(서버 다운)는 Set 에서
 * 되돌려 다음 틱에 재시도하고, 웹훅 발송 실패는 재시도하지 않는다(이미 클레임됨).
 * 타 콘솔이 선점한 키(added 에 안 든 것)도 Set 에서 뺀다 — 서버 클레임은 TTL(6h)로
 * 만료돼 재클레임 가능한데 Set 에 남기면 이 페이지 수명 동안 선점이 묘력화된다(#329).
 */
const _escalClaimed = new Set();
function applyEscalation() {
  if (!model || typeof model.escalDue !== 'function' || !data) return;
  const st = getState();
  if (!(st.setg && (st.setg.escHours === 4 || st.setg.escHours === 24))) return;
  const url = (st.notify && st.notify.enabled && st.notify.url) || '';
  if (!url) return;
  if (typeof data.claimEscal !== 'function' || typeof data.sendWebhook !== 'function') return;
  const due = model.escalDue(st).filter((d) => !_escalClaimed.has(d.key));
  if (!due.length) return;
  due.forEach((d) => _escalClaimed.add(d.key));     // 비동기 사이 중복 시도 방지
  data.claimEscal(due.map((d) => d.key), 2500).then((added) => {
    if (!added) { due.forEach((d) => _escalClaimed.delete(d.key)); return; }  // 서버 다운 — 재시도
    // added 에 안 든 키(타 콘솔 선점)는 Set 에서 지워 후속 틱에 재시도한다 — TTL 만료 뒤
    // 재클레임이 이 경로로 살아난다. 재PUT 은 add-if-absent 라 유효 클레임 기간 중엔 added 가
    // 비어 멱등하고, 만료 뒤엔 먼저 도착한 한 콘솔만 added 를 받아 웹훅도 1회다(중복 발송 없음).
    due.forEach((d) => { if (added.indexOf(d.key) < 0) _escalClaimed.delete(d.key); });
    due.filter((d) => added.indexOf(d.key) >= 0).forEach((d) => {
      const h = st.setg.escHours;
      const msg = (getState().lang === 'en')
        ? '🚨 [serverdesk] Critical unacknowledged ' + h + 'h+ — ' + d.host + ': ' + d.desc + ' (since ' + d.time + ')'
        : '🚨 [serverdesk] critical 미확인 ' + h + '시간 초과 — ' + d.host + ': ' + d.desc + ' (발생 ' + d.time + ')';
      data.sendWebhook(url, msg, 3000);
    });
  }).catch(() => {});
}

function sweepMaint() {
  if (!model || typeof model.expiredMaint !== 'function') return;
  const ids = model.expiredMaint(getState());
  if (!ids.length) return;
  setState((st) => {
    const next = Object.assign({}, st.maint);
    ids.forEach((id) => { delete next[id]; });
    return { maint: next };
  });
}

function applyAutoAck() {
  if (!model || typeof model.autoAckDue !== 'function') return;
  const due = model.autoAckDue(getState());
  if (!due.length) return;
  setState((s) => {
    const next = Object.assign({}, s.ackedAlerts);
    const iso = new Date().toISOString();
    due.forEach((k) => { if (!next[k]) next[k] = iso; });
    return { ackedAlerts: next };
  });
}

/**
 * 알림 사운드 — 새 경보(warning 이상) 등장 시 1회 비프(setg.sound 가 켜져 있을 때만).
 * 감지는 기존 경보 모집단을 재활용한다: 모델의 activeAlerts(미확인·비점검 창) 키를
 * 이전 스냅샷과 비교해 새로 나타난 키가 있으면 울린다(escalDue 와 같은 collectAlerts
 * 모집단 — 화면에 뜬 경보와 울리는 경보가 어긋나지 않게).
 * 첫 스냅샷은 기준선만 저장한다 — 페이지 로드 때 이미 떠 있던 경보가 일제히 울리지 않게.
 * 추적은 토글과 무관하게 항상 갱신한다 — 켜는 순간 기존 경보 전부가 '새 경보'로
 * 오인돼 울리는 것을 막기 위함.
 */
let _soundSeen = null;
function applyAlertSound() {
  if (!sound || typeof sound.alertSoundKeys !== 'function' || typeof sound.playAlertBeep !== 'function') return;
  const st = getState();
  const keys = sound.alertSoundKeys(getModel(st));
  if (_soundSeen === null) { _soundSeen = keys; return; }
  let fresh = false;
  keys.forEach((k) => { if (!_soundSeen.has(k)) fresh = true; });
  _soundSeen = keys;
  if (fresh && st.setg && st.setg.sound) sound.playAlertBeep();
}

// ---------------------------------------------------------------------------
// tick 루프 (1200ms) + /api/fleet 폴링 (3000ms)
// ---------------------------------------------------------------------------
function startTick() {
  setInterval(() => {
    const st = getState();
    if (!(st.setg && st.setg.refresh)) return;
    const patch = { tick: st.tick + 1 };
    if (st.source === 'sim' && data && typeof data.tickFleet === 'function') {
      try {
        // model/data.js 계약: {fleet, hist, lastPoll, changed} 반환 + fleet은 제자리 변경.
        // 구버전(배열 반환 / 반환 없음)도 그대로 받아들인다.
        const r = data.tickFleet(st.fleet, st);
        if (Array.isArray(r)) {
          patch.fleet = r;
          patch.lastPoll = Date.now();
        } else if (r && typeof r === 'object') {
          if (Array.isArray(r.fleet) && r.fleet !== st.fleet) patch.fleet = r.fleet;
          if (r.hist) patch.hist = r.hist;                 // ← 스파크라인 이력(누락 시 차트가 빈다)
          patch.lastPoll = r.lastPoll || Date.now();
          if (Array.isArray(r.changed) && r.changed.length) notifyTransitions(r.changed);
        } else {
          patch.lastPoll = Date.now();
        }
      } catch (e) {
        console.warn('[serverdesk] tickFleet 실패', e);
      }
    }
    const stale = computeStale(st);
    if (stale !== st.stale) patch.stale = stale;
    setState(patch);
    applyAutoAck();
    sweepMaint();
    applyEscalation();
    applyAlertSound();
  }, 1200);
}

function startPull() {
  // model/data.js 계약: pullPatch(state) → statePatch. (pull(url,timeoutMs)는 저수준 API라 쓰지 않는다)
  const pullPatch = data && typeof data.pullPatch === 'function' ? data.pullPatch : null;
  if (!pullPatch) return;

  let fails = 0;
  let timer = null;
  let isTabActive = !document.hidden;
  // 세대 토큰 — run() 이 await pullPatch 에 멈춘 사이(timer 는 아직 null) 탭 복귀
  // 핸들러가 run() 을 다시 기동하면, 깨어난 구 체인도 setTimeout(run) 을 걸어 체인이
  // 영구 이중화된다(반복 시 누적). 복귀 기동 때 세대를 올려 구 체인의 재예약을 무효화한다(#324).
  let generation = 0;
  const BASE_MS = 3000;
  const MAX_MS = 30000;   // 폐쇄망(백엔드 없음)에서 3초마다 404를 때리지 않도록 백오프

  const run = async () => {
    const gen = generation;
    if (!isTabActive) return; // 탭 비활성화 시 백그라운드 폴링 중단 (자원 절약)
    const st = getState();
    // 앱은 항상 sim 플레이스홀더로 부팅하므로 첫 요청도 source 와 무관하게 실행해야 한다.
    // 여기서 live 만 허용하면 sim → live 전환이 영원히 일어나지 않아, 폴러가 정상이어도
    // 27대 시뮬레이션 플릿만 보인다.
    let pulled = false;
    try {
      const patch = await pullPatch(st);
      if (patch && Object.keys(patch).length) {
        fails = patch.source === 'live' ? 0 : fails + 1;
        const merged = { ...getState(), ...patch };
        patch.stale = computeStale(merged);
        setState(patch);
        pulled = true;
      }
    } catch (e) {
      // pullPatch는 자체 폴백해야 하지만, 예외가 새어나와도 시뮬 유지
      // source 만 바꾸면 tickFleet 랜덤워크가 실장비 스냅샷을 흔드므로(pullPatch 폴백 분기와
      // 동일 사유) 시뮬 fleet 을 새로 만들어 통째로 교체하고 배너 플래그(simFallback)를 남긴다.
      fails += 1;
      const patch = { source: 'sim', liveError: null, simFallback: true };
      try {
        if (data && typeof data.buildFleet === 'function') patch.fleet = data.buildFleet();
      } catch (_) { /* noop */ }
      setState(patch);
    }
    if (pulled) {
      // 후처리는 pull 성공 확정 뒤 try 밖에서 실행한다 — 여기서 throw 되어도 위 catch 의
      // 시뮬 플릿 교체로 이어지면 안 된다(수집기는 멀쩡한데 라이브 플릿이 날아가는 결함).
      // 후처리 자체의 예외는 폴링 루프를 죽이지 않도록 따로 격리한다.
      try {
        applyAutoAck();
        // 점검 창 만료 청소도 자동확인과 같은 생존 조건을 갖는다 — tick 게이트(setg.refresh)
        // 와 무관하게 pull 완료 때도 만료 창을 지워 묵음이 until 이후로 지속되는 것을 막는다.
        // 재통보·사운드보다 먼저 청소해 막 만료된 창 장비의 경보가 그 사이클에 바로 반영되게 한다.
        sweepMaint();
        // 에스컬레이션도 자동확인과 같은 생존 조건을 갖는다 — tick 게이트(setg.refresh)
        // 와 무관하게 pull 완료 때도 재통보를 검사한다(자동새로고침 OFF 에서 중단되는 결함).
        applyEscalation();
        applyAlertSound();   // 실 모드에서 새 경보는 pull 로만 들어온다(tickFleet 은 시뮬 전용)
      } catch (e) {
        console.warn('[serverdesk] pull 후처리 실패', e);
      }
    }
    // 첫 pull 완료 = 데이터 소스 확정(live 또는 시뮬 폴백). 이제부터 딥링크 검증 활성화,
    // 현재 해시를 1회 재해석해 진짜 없는 id만 정정한다.
    if (!dataSettled) { dataSettled = true; try { applyHash(); } catch (_) { /* noop */ } }
    const wait = Math.min(MAX_MS, BASE_MS * Math.pow(2, Math.max(0, fails - 3)));
    // await 에 멈춘 사이 탭 복귀가 새 체인을 기동했으면(세대 증가) 이 체인은 여기서 끊는다
    // — 둘 다 재예약하면 폴링 루프가 영구 이중화된다(#324).
    if (gen !== generation) return;
    timer = setTimeout(run, wait);
  };

  // 탭 비활성화/복귀 스마트 폴링 바인딩
  document.addEventListener('visibilitychange', () => {
    isTabActive = !document.hidden;
    if (isTabActive) {
      clearTimeout(timer);
      generation += 1;  // in-flight run 이 깨어나도 재예약하지 못하게 세대를 올린다(#324)
      run(); // 탭 복귀 시 즉시 1회 수집 및 타이머 재가동
    } else {
      clearTimeout(timer); // 탭 이탈 시 타임아웃 해제
    }
  });

  clearTimeout(timer);
  run();
}

/** tickFleet이 보고한 상태 전이를 토스트로 알린다(최대 1건/틱). */
function notifyTransitions(changed) {
  const c = changed[changed.length - 1];
  if (!c || !c.to) return;
  const dev = (getState().fleet || []).find(d => String(d.id) === String(c.id));
  const host = dev ? (dev.host || dev.id) : c.id;
  const label = to => (to === 'down' ? L('offline', '오프라인')
    : to === 'deg' ? L('degraded', '저하')
      : L('operational', '정상'));
  showToast(host + ' → ' + label(c.to));
}

// ---------------------------------------------------------------------------
// 부트스트랩
// ---------------------------------------------------------------------------
async function boot() {
  cacheShell();
  await loadShared();

  // 초기 fleet(시뮬)
  if (data && typeof data.buildFleet === 'function') {
    try {
      const fleet = data.buildFleet(data.SIM_SEED);
      if (Array.isArray(fleet)) setState({ fleet, source: 'sim', lastPoll: Date.now() });
    } catch (e) {
      console.warn('[serverdesk] buildFleet 실패', e);
    }
  }

  // 세 원격 동기화는 서로 의존하지 않으므로 병렬로 시작한다. 각 작업은 자체 오류를
  // 삼켜 폐쇄망 폴백을 유지하고, 가장 느린 엔드포인트 하나만 첫 렌더를 지연시킨다(#154).
  const syncAck = async () => {
    // 경보 확인 상태 — 서버(serve.py /ack)가 정본, localStorage 는 오프라인 폴백.
    // 다중 운영자 환경에서 A 가 확인한 경보를 B 도 확인된 것으로 봐야 한다.
    if (!(data && typeof data.pullAck === 'function')) return;
    try {
      const remote = await data.pullAck(2000);
      if (remote) {
        // 서버 ↔ 로컬 합집합 — 오프라인 중 로컬에서 확인한 건이 서버 복귀 시 사라지면 안 된다.
        const merged = Object.assign({}, getState().ackedAlerts || {}, remote);
        setState({ ackedAlerts: merged });
        if (Object.keys(merged).length !== Object.keys(remote).length) {
          // 로컬에만 있던 건을 서버로 올린다 — 델타라 다른 운영자의 확인을 덮지 않는다.
          data.pushAck(data.ackDelta(remote, merged), 2500);
        }
      }
    } catch (e) {
      console.warn('[serverdesk] ack 동기화 생략', e);
    }
  };

  const syncMaint = async () => {
    // 점검 창 상태 — ack 와 같은 계약(/maint 가 정본, localStorage 는 오프라인 폴백).
    if (!(data && typeof data.pullMaint === 'function')) return;
    try {
      const remoteM = await data.pullMaint(2000);
      if (remoteM) {
        const mergedM = Object.assign({}, getState().maint || {}, remoteM);
        setState({ maint: mergedM });
        if (Object.keys(mergedM).length !== Object.keys(remoteM).length) {
          data.pushMaint(data.ackDelta(remoteM, mergedM), 2500);
        }
      }
    } catch (e) {
      console.warn('[serverdesk] maint 동기화 생략', e);
    }
  };

  const syncNotes = async () => {
    // 장비 메모 — ack/maint 와 같은 계약(/notes 가 정본).
    if (!(data && typeof data.pullNotes === 'function')) return;
    try {
      const remoteN = await data.pullNotes(2000);
      if (remoteN) {
        const mergedN = Object.assign({}, getState().notes || {}, remoteN);
        setState({ notes: mergedN });
        if (Object.keys(mergedN).length !== Object.keys(remoteN).length) {
          data.pushNotes(data.ackDelta(remoteN, mergedN), 2500);
        }
      }
    } catch (e) {
      console.warn('[serverdesk] notes 동기화 생략', e);
    }
  };

  await Promise.all([syncAck(), syncMaint(), syncNotes()]);

  // 해시 → 초기 view (존재하지 않는 detail id는 overview로)
  const route = resolveRoute(parseHash(), getState());
  hashLock = true;
  const bootPatch = { view: route.view, selected: route.selected, originView: route.view === 'detail' ? 'overview' : route.view };
  if (route.alertView != null) bootPatch.alertView = route.alertView;
  setState(bootPatch);
  hashLock = false;

  subscribe(render);
  render(getState());
  startClock();
  startTick();
  startPull();

  // 브라우저 자동재생 정책 — AudioContext 는 사용자 제스처 전까지 suspended 라
  // 첫 클릭/키 입력 때 재개를 걸어 둔다(알림 사운드, setg.sound).
  ['pointerdown', 'keydown'].forEach((ev) => {
    document.addEventListener(ev, () => {
      if (sound && typeof sound.resumeAlertAudio === 'function') sound.resumeAlertAudio();
    }, { passive: true });
  });
}

boot();

export { store, ctx, goView, goDetail, goBack, showToast };
