// js/store.js
// 프레임워크 없는 상태 저장소. DOM 접근 없음.
// REBUILD-SPEC.md §3.1 / §3.2

// ---------------------------------------------------------------------------
// localStorage 키
// ---------------------------------------------------------------------------
export const LS_LANG = 'sd.lang';
export const LS_COMPANY_COLORS = 'sd.companyColors';
export const LS_RAIL_OPEN = 'sd.railOpen';
export const LS_THEME = 'sd.theme';
// 감사 지적: 설정 화면이 '자동 새로고침 · 알림 사운드 · 고밀도 레이아웃'을 스위치로 노출하는데
// persist() 가 이 셋을 저장하지 않아 새로고침하면 조용히 기본값으로 되돌아갔다.
// 사용자는 스위치를 sticky 로 기대한다 — 저장 대상에 편입한다.
export const LS_SETG = 'sd.setg';
export const LS_MAINT = 'sd.maint';
export const LS_NOTES = 'sd.notes';
// 구형 브라우저 전용 웹훅 키. 서버 notifier 전환 후에는 마이그레이션 삭제에만 사용한다.
export const LS_NOTIFY = 'sd.notify';
// 확인 처리된 경보 키 집합 — 폴러가 해제 API 를 주지 않으므로 확인 상태는 클라이언트가 보관한다.
export const LS_ACKED = 'sd.ackedAlerts';
const LS_DATA_SCHEMA = 'sd.dataSchema';
const DATA_SCHEMA = 'live-only-v1';

function migrateLocalState() {
  // webhook 토큰을 포함할 수 있는 구형 값은 애플리케이션 상태로 읽기 전에 무조건 제거한다.
  try { localStorage.removeItem(LS_NOTIFY); } catch (e) { /* private mode 등 무시 */ }
  try {
    if (localStorage.getItem(LS_DATA_SCHEMA) !== DATA_SCHEMA) {
      [LS_COMPANY_COLORS, LS_ACKED, LS_MAINT, LS_NOTES].forEach((key) => localStorage.removeItem(key));
      localStorage.setItem(LS_DATA_SCHEMA, DATA_SCHEMA);
    }
  } catch (e) { /* private mode 등 무시 */ }
  try {
    const raw = localStorage.getItem(LS_SETG);
    const saved = raw ? JSON.parse(raw) : null;
    if (saved && typeof saved === 'object' && Object.prototype.hasOwnProperty.call(saved, 'token')) {
      delete saved.token;
      localStorage.setItem(LS_SETG, JSON.stringify(saved));
    }
  } catch (e) { /* 구형 설정 정리 실패 무시 */ }
}

/**
 * 초기 상태 (REBUILD-SPEC.md §3.2 전문).
 */
export const initialState = {
  // ── 라우팅 ──
  view: 'overview',        // 화면 key. detail 진입 시 'detail'
  selected: null,          // detail 대상 device id
  originView: 'overview',  // detail 뒤로가기 복귀 지점
  // ── 전역 ──
  lang: 'ko',              // localStorage['sd.lang']
  theme: 'system',         // 'system'|'light'|'dark' — localStorage['sd.theme'] (index.html 조기 스탬프와 동기)
  tick: 0,                 // setInterval 증가 (setg.refresh && !paused 일 때만)
  paused: false,           // legacy compatibility flag; log stream uses logPaused
  logPaused: false,        // incidents log stream Live/Pause only
  toast: null,
  search: '',              // 전역 검색어
  railOpen: false,         // 좌측 레일 펼침 여부. localStorage['sd.railOpen']
  // ── 데이터 ──
  fleet: [],               // device[] (§4.1)
  source: 'live',
  // 서버가 명시적으로 광고한 읽기 전용 샘플 응답. 클라이언트는 샘플 장비를 만들지 않는다.
  sampleMode: false,
  // 구버전/과도기 서버의 `demo` 표기와도 상태 계약을 맞춘다.
  demoMode: false,
  lastPoll: 0,             // 마지막 성공 응답 epoch ms (실패 시 갱신 금지)
  lastAttempt: 0,          // 성공/실패를 포함한 마지막 폴 시도 epoch ms
  pollPending: true,       // 최초 요청 진행 중 — 성공한 0대와 로딩을 구분
  refreshSec: 30,          // stale 임계 계산용
  stale: false,
  liveError: null,
  uiError: null,           // 화면 init/render 실패 — 전역 오류 표면에서 재시도 가능
  syncError: null,         // ACK/점검 등 공유 상태 서버 반영 실패 — 전역 배너에서 명시
  liveEventLog: null,      // 폴러 events[] 이력(로그 tail 정본). 미수신 시 null — compute 는 배열로 방어
  pollerOverall: null,     // 폴러 계산 플릿 총평 'ok'|'warning'|'critical'|null
  cacheAgeSec: null,       // 폴러가 실장비를 읽은 뒤 흐른 초(null=미제공)
  // 서버가 명시적으로 광고한 변경 기능. 누락 시 cluster actions는 fail-closed.
  capabilities: {
    cluster_actions: {
      supported: false, actions: [], endpoint: '/api/clusters/{id}/action',
      reason: 'Server did not advertise cluster action support.',
      reason_ko: '서버가 클러스터 제어 지원을 알리지 않았습니다.',
    },
  },
  alertView: 'cards',      // incidents 화면 탭 'cards'(경보)|'log'(로그)|'stats'(통계). 해시(#/logs)와 바인딩
  alertExpanded: false,    // incidents 카드 '더 보기' 펼침 상태(§4.1 — setState 동적 추가 금지)
  hist: {},                // { [id]: {cpu:[], mem:[], rtt:[]} } 최대 48
  // ── 화면별 UI 상태 ──
  ovFilter: 'all',         // all|op|deg|down
  nodesSort: { key: 'host', dir: 'asc' },
  nodesFilter: 'all',
  nodesCollapsed: {},      // nodes 화면 그룹 접힘 상태(§4.1 필드 표 — setState 동적 추가 금지)
  topoView: 'tree',        // tree|floor
  topoFocus: null,
  collapsed: {},
  companyColors: {},       // localStorage['sd.companyColors']
  capMetric: 'cpu',        // capacity 부하 랭킹 탭 cpu|mem
  clustersFilter: 'all',
  alertFilter: 'all',      // all|critical|warning|info
  logLevel: 'all',         // all|ERROR|WARN|INFO (SEV_TO_LEVEL 대문자 체계 — compute 가 toUpperCase 비교)
  logQuery: '',
  // ── manage / settings ──
  manageCollapsed: {},
  editKey: null,
  wizardStep: 0,
  form: null,
  companyColorsDirty: false,
  ackedAlerts: {},
  thresholds: null,      // 서버 사용률 임계값 {warn,crit} — /api/devices 폴이 채움
  maint: {},
  notes: {},
  setg: { refresh: true, sound: false, dense: false, ackAutoDays: 0 },
  // ── floor map(topology 보완 뷰)이 재사용하는 기존 필드 ──
  view3d: false,
  zoom: 1,
};

// ---------------------------------------------------------------------------
// 영속 (localStorage)
// ---------------------------------------------------------------------------

/** 저장된 값을 읽어 초기 상태에 덮어쓸 patch를 만든다. 실패해도 절대 throw 하지 않는다. */
export function loadPersisted() {
  migrateLocalState();
  const patch = {};
  try {
    const lang = localStorage.getItem(LS_LANG);
    if (lang === 'ko' || lang === 'en') patch.lang = lang;
  } catch (e) { /* private mode 등 무시 */ }
  try {
    const raw = localStorage.getItem(LS_COMPANY_COLORS);
    if (raw) {
      const obj = JSON.parse(raw);
      if (obj && typeof obj === 'object') patch.companyColors = obj;
    }
  } catch (e) { /* 파싱 실패 무시 */ }
  try {
    const th = localStorage.getItem(LS_THEME);
    if (th === 'light' || th === 'dark') patch.theme = th;
    const ro = localStorage.getItem(LS_RAIL_OPEN);
    if (ro === '1' || ro === 'true') patch.railOpen = true;
    else if (ro === '0' || ro === 'false') patch.railOpen = false;
  } catch (e) { /* private mode 등 무시 */ }
  try {
    const raw = localStorage.getItem(LS_SETG);
    if (raw) {
      const o = JSON.parse(raw);
      if (o && typeof o === 'object') {
        // 알려진 키만 받아들인다 — 저장소가 오염돼도 상태 모양이 깨지지 않게.
        // ackAutoDays 는 7/30 일만 허용(그 외·없음은 0=해제).
        patch.setg = {
          refresh: typeof o.refresh === 'boolean' ? o.refresh : true,
          sound: typeof o.sound === 'boolean' ? o.sound : false,
          dense: typeof o.dense === 'boolean' ? o.dense : false,
          ackAutoDays: (o.ackAutoDays === 7 || o.ackAutoDays === 30) ? o.ackAutoDays : 0,
        };
      }
    }
  } catch (e) { /* 파싱 실패 무시 */ }
  try {
    const raw = localStorage.getItem(LS_ACKED);
    if (raw) {
      const o = JSON.parse(raw);
      // 값은 현재 {ts,by,reason}; 구형 ISO 문자열도 읽기 호환한다.
      if (o && typeof o === 'object') patch.ackedAlerts = o;
    }
  } catch (e) { /* 파싱 실패 무시 */ }
  try {
    const raw = localStorage.getItem(LS_NOTES);
    if (raw) {
      const o = JSON.parse(raw);
      // 값은 {text, ts, by} — 서버(/notes)가 정본, 로컬은 오프라인 폴백(ack 와 같은 패턴).
      if (o && typeof o === 'object') patch.notes = o;
    }
  } catch (e) { /* 파싱 실패 무시 */ }
  try {
    const raw = localStorage.getItem(LS_MAINT);
    if (raw) {
      const o = JSON.parse(raw);
      // 값은 {until, note, by, ts} — 서버(/maint)가 정본, 로컬은 오프라인 폴백(ack 와 같은 패턴).
      if (o && typeof o === 'object') patch.maint = o;
    }
  } catch (e) { /* 파싱 실패 무시 */ }
  return patch;
}

/** 민감하지 않은 콘솔 UI 상태만 localStorage에 저장한다. 알림 비밀은 서버 정본이다. */
export function persist(state) {
  try { localStorage.setItem(LS_LANG, state.lang); } catch (e) { /* noop */ }
  try { localStorage.setItem(LS_COMPANY_COLORS, JSON.stringify(state.companyColors || {})); } catch (e) { /* noop */ }
  try { localStorage.setItem(LS_RAIL_OPEN, state.railOpen ? '1' : '0'); } catch (e) { /* noop */ }
  try {
    // 'system' 은 키 자체를 지워 index.html 조기 스탬프가 속성을 안 붙이게 한다.
    if (state.theme === 'light' || state.theme === 'dark') localStorage.setItem(LS_THEME, state.theme);
    else localStorage.removeItem(LS_THEME);
  } catch (e) { /* noop */ }
  try { localStorage.setItem(LS_ACKED, JSON.stringify(state.ackedAlerts || {})); } catch (e) { /* noop */ }
  try { localStorage.setItem(LS_MAINT, JSON.stringify(state.maint || {})); } catch (e) { /* noop */ }
  try { localStorage.setItem(LS_NOTES, JSON.stringify(state.notes || {})); } catch (e) { /* noop */ }
  try { localStorage.removeItem(LS_NOTIFY); } catch (e) { /* noop */ }
  try {
    const g = state.setg || {};
    localStorage.setItem(LS_SETG, JSON.stringify({
      refresh: !!g.refresh, sound: !!g.sound, dense: !!g.dense,
      ackAutoDays: (g.ackAutoDays === 7 || g.ackAutoDays === 30) ? g.ackAutoDays : 0,
    }));
  } catch (e) { /* noop */ }
}

// ---------------------------------------------------------------------------
// store
// ---------------------------------------------------------------------------

/**
 * 상태 저장소를 생성한다.
 * @param {object} initState 초기 상태 객체 (얕은 복사되어 저장됨)
 * @returns {{getState: Function, setState: Function, subscribe: Function}}
 */
export function createStore(initState) {
  let state = { ...initState };
  const listeners = new Set();

  function getState() {
    return state;
  }

  /**
   * 상태를 갱신한다(얕은 병합).
   * @param {object|Function} patchOrFn 병합할 객체, 또는 (state) => 병합할 객체
   */
  function setState(patchOrFn) {
    const patch = typeof patchOrFn === 'function' ? patchOrFn(state) : patchOrFn;
    if (!patch) return;
    state = { ...state, ...patch };
    listeners.forEach(fn => fn(state));
  }

  /**
   * 상태 변경 구독. setState 호출 후 전체 구독자가 최신 state와 함께 호출된다.
   * @param {Function} fn (state) => void
   * @returns {Function} 구독 해제 함수
   */
  function subscribe(fn) {
    listeners.add(fn);
    return () => listeners.delete(fn);
  }

  return { getState, setState, subscribe };
}
