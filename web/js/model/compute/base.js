// js/model/compute/base.js — 기본 소도구, 심각도 메타, 이력 유틸
// ---------------------------------------------------------------------------
// 순수 함수만. DOM 접근 0건.
// ---------------------------------------------------------------------------

/* ===========================================================================
 * 0. 기본 소도구
 * ======================================================================== */

export function clamp(v, a, b) {
  const n = Number(v);
  if (!Number.isFinite(n)) return a;
  return Math.max(a, Math.min(b, n));
}

const _meta = (s) => (s && s.meta) || {};
const _arr = (v) => (Array.isArray(v) ? v : []);
const _num = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
const DASH = '—';

/** 지문용 간이 문자열 해시(djb2, 32bit) — 길이가 아니라 내용이 바뀌면 달라진다(#279). */
const _strHash = (s) => {
  let h = 5381;
  for (let i = 0; i < s.length; i += 1) h = (((h << 5) + h) + s.charCodeAt(i)) | 0;
  return (h >>> 0).toString(36);
};

/** 한글 우선 자연 정렬(스펙 §7: localeCompare(_, 'ko', {numeric:true})). */
export function cmpKo(a, b) {
  return String(a == null ? '' : a).localeCompare(String(b == null ? '' : b), 'ko', { numeric: true });
}

/** state.lang 판정. */
export function langOf(state) {
  return (state && state.lang) === 'en' ? 'en' : 'ko';
}

/** state 기준 L(en, ko) 생성기(모듈 전역 언어에 의존하지 않는 순수 버전). */
export function makeL(state) {
  const ko = langOf(state) === 'ko';
  return (en, k) => (ko ? k : en);
}


/** 인자 유연 처리: buildModel/buildTopo 등에서 (state) / (fleet, state) 둘 다 허용. */
export function _resolve(a, b) {
  if (Array.isArray(a)) return { fleet: a, state: b || {} };
  const st = a || {};
  return { fleet: _arr(st.fleet), state: st };
}

/* ===========================================================================
 * 3. 심각도 메타
 * ======================================================================== */

export const SEV_RANK = { critical: 0, warning: 1, info: 2 };

/** 심각도 → {tone, level, label, icon}. */
/** 이 일수 이상 미확인이면 '장기 방치'로 본다 — 실측상 활성 경보 31건이 전부 10~41일이었다. */
export const STALE_ALERT_DAYS = 7;

export function sevInfo(sev, L) {
  const k = sev === 'critical' || sev === 'warning' ? sev : 'info';
  if (k === 'critical') return { key: 'critical', tone: 'neg', level: 'ERROR', label: L('CRITICAL', '심각'), icon: 'warningCircle' };
  if (k === 'warning') return { key: 'warning', tone: 'warn', level: 'WARN', label: L('WARNING', '경고'), icon: 'warningCircle' };
  return { key: 'info', tone: 'info', level: 'INFO', label: L('INFO', '정보'), icon: 'infoCircle' };
}


/* ===========================================================================
 * 4. 이력(hist) 주입
 * ======================================================================== */


/** state.hist 우선, 없으면 device.histCpu/… 로 폴백. NA 샘플은 애초에 push되지 않는다. */
export function histOf(state, dev, field) {
  const h = (state && state.hist && state.hist[dev.id]) || null;
  if (h && Array.isArray(h[field]) && h[field].length) return h[field];
  const own = field === 'cpu' ? dev.histCpu : (field === 'mem' ? dev.histMem : dev.histRtt);
  return _arr(own);
}

/** 스파크라인용: 표본이 2개 미만이면 현재값으로 평평하게 채운다(선이 안 그려지는 문제 방지). */
export function _sparkHist(list, cur) {
  if (Array.isArray(list) && list.length > 1) return list;
  const v = _num(cur);
  return v == null ? [] : [v, v];
}


export { _meta, _arr, _num, DASH, _strHash };
