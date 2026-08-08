// js/screens/detail.js
// serverdesk — 서버 상세(드릴다운) 화면. REBUILD-SPEC §1.10 / §5.1 / §5.4 / §5.5
//
// 원칙
//  - default export = {key,title,icon,init,render,destroy}
//  - init에서 고정 골격(헤더/타일/알림/카드영역/확인모달) 1회 생성, 로컬 위임 리스너 1개 등록
//  - render는 "구조 시그니처"가 바뀔 때만 카드 영역을 재생성하고, 평소에는 값만 patch
//    (진행바 width 트랜지션·모달 입력값이 tick마다 리셋되지 않게)
//  - 색은 CSS 변수 / 공용 상태 클래스(.is-pos/.is-warn/.is-neg/.is-info)로만.
//    인라인 hex는 SVG stroke 등 불가피한 곳에서만 fmt.COLOR 상수를 경유한다.
//  - 다른 js/screens/* 를 import하지 않는다. js/util/*, js/model/* 만 ctx로 사용.

const DASH = '—';

const KST_NOTE_FORMATTER = new Intl.DateTimeFormat('sv-SE', {
  timeZone: 'Asia/Seoul', year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
});

export function formatNoteTimestamp(ts) {
  const d = new Date(ts);
  if (!ts || Number.isNaN(d.getTime())) return String(ts || '').replace('T', ' ').slice(0, 16);
  return KST_NOTE_FORMATTER.format(d);
}

/* ── 모듈 로컬 상태 ───────────────────────────────────────────────────── */
let U = null;            // ctx.util
let CTX = null;          // ctx
let rootEl = null;
let fix = {};            // 고정 골격 참조
let patchFixed = [];     // 헤더 등 고정 영역 patch 함수
let patchCards = [];     // 재생성되는 영역(타일/알림/카드) patch 함수
let sig = '';            // 구조 시그니처
let onClick = null;
let onKeydown = null;
let onInput = null;
let confirmState = null; // {action,target,label,destructive,consequence,triggerEl,devId}
let busy = false;
let resultMsg = null;    // {ok,msg}
let lastDetail = null;
let noteBoundId = null;  // 메모 textarea 값이 바인딩된 장비 id(#473)

/* ── 소형 유틸 ───────────────────────────────────────────────────────── */
function E(tag, attrs, children) {
  if (U && U.dom && typeof U.dom.el === 'function') return U.dom.el(tag, attrs || {}, children || []);
  // util/dom.js 로드 실패 시 최소 폴백
  const n = document.createElement(tag);
  Object.entries(attrs || {}).forEach(([k, v]) => {
    if (v == null || v === false) return;
    if (k === 'class') n.className = v;
    else if (k === 'text') n.textContent = v;
    else n.setAttribute(k, v);
  });
  const add = (c) => {
    if (c == null || c === false) return;
    if (Array.isArray(c)) { c.forEach(add); return; }
    n.appendChild(typeof c === 'object' ? c : document.createTextNode(String(c)));
  };
  add(children || []);
  return n;
}

function ico(name, size) {
  try {
    const fn = U && U.icon;
    if (typeof fn === 'function') return fn(name, { size: size || 15 });
  } catch (e) { /* 아이콘 실패는 화면을 깨지 않는다 */ }
  return E('span', { class: 'sc-dtl-ico-x' });
}

function iconInto(host, name, size) {
  if (!host) return;
  if (host._icoName === name) return;
  host._icoName = name;
  host.textContent = '';
  if (name) host.appendChild(ico(name, size));
}

const TONE_CLS = { pos: 'is-pos', warn: 'is-warn', neg: 'is-neg', info: 'is-info', mut: 'u-muted' };
function toneCls(t) { return TONE_CLS[t] || 'u-muted'; }
function toneKey(t) { return (t === 'pos' || t === 'warn' || t === 'neg' || t === 'info') ? t : 'mut'; }

// 사용률 임계 톤 — 서버 thresholds(ctx.util.fmt.usageThresholds), 폴리 78/90.
function _usageTone(p) {
  const f = U && U.fmt;
  const t = (f && typeof f.usageThresholds === 'function') ? f.usageThresholds() : { warn: 78, crit: 90 };
  const n = Number(p) || 0;
  return n >= t.crit ? 'neg' : (n >= t.warn ? 'warn' : 'pos');
}

function setText(node, v) {
  if (!node) return;
  const s = (v == null) ? '' : String(v);
  if (node.textContent !== s) node.textContent = s;
}
function setCls(node, base, extra) {
  if (!node) return;
  const s = extra ? base + ' ' + extra : base;
  if (node.className !== s) node.className = s;
}
function show(node, on) { if (node) node.classList.toggle('u-hide', !on); }

// SVG 테마 페인트는 presentation attribute가 아닌 style 프로퍼티로 지정한다.
function tonePaint(tone) {
  return tone === 'pos' ? 'var(--pos)'
    : (tone === 'warn' ? 'var(--warn)'
      : (tone === 'neg' ? 'var(--neg)'
        : (tone === 'info' ? 'var(--blue)' : 'var(--muted)')));
}

function arr(v) { return Array.isArray(v) ? v : []; }
function num(v) { const n = Number(v); return Number.isFinite(n) ? n : null; }
function pct(v) { const n = num(v); return n == null ? 0 : Math.max(0, Math.min(100, n)); }

/* fetch + 타임아웃 — settings.js fetchTimeout 과 동일 패턴(제어 액션이 응답 없이 멈추는 경우 방어) */
async function fetchTimeout(url, opts, ms) {
  const hasAbort = typeof AbortController !== 'undefined';
  const ctrl = hasAbort ? new AbortController() : null;
  const id = ctrl ? setTimeout(() => ctrl.abort(), ms) : null;
  try {
    return await fetch(url, Object.assign({}, opts, ctrl ? { signal: ctrl.signal } : {}));
  } finally {
    if (id) clearTimeout(id);
  }
}

/* patch 등록 */
function P(fn) { patchCards.push(fn); }
function PF(fn) { patchFixed.push(fn); }

/* i18n — rebuild 시점의 언어로 고정 라벨을 굽는다(언어 변경은 sig에 포함되어 재생성됨) */
let ko = true;
function L(en, kr) { return ko ? kr : en; }

/* ── 공용 조각 빌더 ──────────────────────────────────────────────────── */

function cardBox(extraCls) {
  return E('div', { class: 'card' + (extraCls ? ' ' + extraCls : '') });
}

function cardHead(title, right, iconName) {
  const h = E('div', { class: 'card-head' });
  if (iconName) h.appendChild(E('span', { class: 'sc-dtl-hico' }, [ico(iconName, 16)]));
  h.appendChild(E('h2', { class: 'card-title', text: title }));
  if (right) h.appendChild(right);
  return h;
}

/** 우측 상단 상태 배지(값 patch 가능) */
function toneBadge(getLabel, getTone, iconGet) {
  const ic = E('span', { class: 'sc-dtl-bico' });
  const tx = E('span', {});
  const b = E('span', { class: 'u-badge sc-dtl-rbadge' }, [ic, tx]);
  P((d) => {
    setText(tx, getLabel(d));
    setCls(b, 'u-badge sc-dtl-rbadge', toneCls(getTone(d)));
    if (iconGet) iconInto(ic, iconGet(d), 12);
    show(ic, !!iconGet);
  });
  return b;
}

/** key/value 한 줄 */
function kvRow(label, get, opt) {
  const v = E('span', { class: 'u-mono sc-dtl-kv-v' });
  const row = E('div', { class: 'sc-dtl-kv' }, [
    E('span', { class: 'sc-dtl-kv-k u-muted' }, [
      (opt && opt.icon) ? E('span', { class: 'sc-dtl-kico' }, [ico(opt.icon, 14)]) : null,
      E('span', { text: label }),
    ]),
    v,
  ]);
  P((d) => {
    const val = get(d);
    setText(v, val);
    const t = (opt && opt.tone) ? opt.tone(d) : '';
    setCls(v, 'u-mono sc-dtl-kv-v', t ? toneCls(t) : '');
  });
  return row;
}

/** 라벨 + 진행바 + 값 */
function barRow(label, getN, getText, getTone, opt) {
  const val = E('span', { class: 'u-mono sc-dtl-bar-val' });
  const fill = E('div', { class: 'sc-dtl-bar-fill' });
  const track = E('div', { class: 'sc-dtl-bar-track' }, [fill]);
  const row = E('div', { class: 'sc-dtl-barrow' + (opt && opt.inline ? ' is-inline' : '') }, [
    E('div', { class: 'sc-dtl-bar-head' }, [
      E('span', { class: 'u-muted sc-dtl-bar-k', text: label }),
      val,
    ]),
    track,
  ]);
  P((d) => {
    setText(val, getText(d));
    const n = getN(d);
    fill.style.width = (n == null ? 0 : pct(n)) + '%';
    let key = toneKey(getTone ? getTone(d) : 'pos');
    // 사용률 바는 건강상태가 아님 — 정상(pos) 채움은 블루(is-usage, §1.35 단일 규칙).
    // green 은 '가동/보호됨' 상태 토큰 전용. 임계 초과만 warn/neg.
    if (opt && opt.usage && key === 'pos') key = 'usage';
    setCls(fill, 'sc-dtl-bar-fill', 'is-' + key);
    // % 텍스트도 바와 같은 임계 톤(§1.35 텍스트=바 동일 임계, 재심사 반영) — 정상은 중립.
    setCls(val, 'u-mono sc-dtl-bar-val', (key === 'warn' || key === 'neg') ? 'is-' + key : '');
    track.classList.toggle('is-empty', n == null);
  });
  return row;
}

/**
 * 스파크라인 — §0-4/§5.1 "최초 1회 생성 + 값 patch".
 * tick마다 SVG를 새로 만들면 hover 스냅 툴팁이 매 틱 사라지므로(§5.3),
 * 최초 1회(또는 hist가 1개→2개 이상으로 늘어 hover가 활성화될 때)만 생성하고
 * 이후에는 area(d/fill) · line(points/stroke) 속성만 갱신한다.
 * hist 배열은 제자리 갱신해 sparkline 내부 hover 클로저가 최신 값을 보게 한다.
 */
function sparkHost(getHist, getHi, getTone, w, h, getUnit) {
  const host = E('div', { class: 'sc-dtl-spark' });
  const W = w || 200, H = h || 30;
  const sp = { hist: [], el: null, area: null, line: null, active: false, unit: null };
  P((d) => {
    const vals = arr(getHist(d)).map(Number).filter((v) => Number.isFinite(v));
    sp.hist.length = 0;
    vals.forEach((v) => sp.hist.push(v));

    if (!sp.hist.length) {
      // 데이터가 사라지면 비운다(다음에 값이 생기면 다시 생성).
      if (sp.el) { host.textContent = ''; sp.el = null; sp.area = null; sp.line = null; sp.active = false; sp.unit = null; }
      return;
    }
    const hi = getHi ? (num(getHi(d)) || 100) : 100;
    const tone = getTone ? getTone(d) : 'pos';
    // hover 툴팁 단위(기본 '%'). RTT(ms) 타일 등에서 올바른 단위를 표시한다.
    const unit = getUnit ? (getUnit(d) || '%') : '%';
    const svg = U && U.svg;
    if (!svg || typeof svg.sparkline !== 'function') return;

    const wantActive = sp.hist.length >= 2;   // sparkline은 hist<2면 hover 미부착
    // 단위가 바뀌면(장비 유형 전환 등) 툴팁은 생성 시 고정되므로 재생성해야 한다.
    // (!wantActive && sp.active): hist가 2→1로 줄면 기존 SVG에 hover가 잔존해
    // 규약(hist<2면 hover 미부착)과 어긋나므로 이 경우에도 재생성한다.
    if (!sp.el || (wantActive && !sp.active) || (!wantActive && sp.active) || sp.unit !== unit) {
      try {
        const made = svg.sparkline({ hist: sp.hist, w: W, h: H, area: '', line: '', unit });
        host.textContent = '';
        host.appendChild(made.el);
        sp.el = made.el;
        sp.active = wantActive;
        sp.unit = unit;
        sp.area = made.el.querySelector('path');
        sp.line = made.el.querySelector('polyline');
      } catch (e) { return; /* svg 유틸 실패 시 스파크라인 생략 */ }
    }
    if (!sp.area || !sp.line) return;
    sp.area.setAttribute('d', svg.areaPath(sp.hist, W, H, 3, 0, hi));
    sp.area.style.fill = tonePaint(tone);
    sp.area.style.fillOpacity = '0.14';
    sp.line.setAttribute('points', svg.linePts(sp.hist, W, H, 3, 0, hi));
    sp.line.style.stroke = tonePaint(tone);
  });
  return host;
}

/* ── 헤더(고정 골격) ─────────────────────────────────────────────────── */
function buildHeader() {
  const back = E('button', {
    class: 'btn btn--outline btn--sm sc-dtl-back', 'data-action': 'goBack', type: 'button',
  }, [E('span', { class: 'sc-dtl-bico' }, [ico('chevronDown', 13)]), E('span', { 'data-f': 'backLabel' })]);
  const edit = E('button', {
    class: 'btn btn--outline btn--sm', 'data-dtl-edit': '', type: 'button',
  }, [E('span', { class: 'sc-dtl-bico' }, [ico('pencil', 13)]), E('span', { 'data-f': 'editLabel' })]);
  const maintBtn = E('button', {
    class: 'btn btn--outline btn--sm sc-dtl-maintbtn', 'data-dtl-maint': '', type: 'button',
  }, [E('span', { class: 'sc-dtl-bico' }, [ico('점검', 13)]), E('span', { 'data-f': 'maintLabel' })]);
  // 점검 창 시간 선택 — data-action="maintSet" 은 app.js 전역 위임이 처리(로컬 핸들러 불필요).
  // data-maint-id 는 render 에서 현재 장비 id 로 채운다.
  const maintMenu = E('div', { class: 'sc-dtl-maintmenu', hidden: true });
  [1, 4, 8, 24].forEach((h) => {
    const b = E('button', {
      class: 'sc-dtl-maintopt', type: 'button',
      'data-action': 'maintSet', 'data-maint-hours': String(h), 'data-f': 'h' + h,
    });
    maintMenu.appendChild(b);
  });
  const maintClearBtn = E('button', {
    class: 'sc-dtl-maintopt sc-dtl-maintopt--clear', type: 'button',
    'data-action': 'maintSet', 'data-maint-hours': '0', 'data-f': 'clear',
  });
  maintMenu.appendChild(maintClearBtn);
  const maintWrap = E('div', { class: 'sc-dtl-maintwrap' }, [maintBtn, maintMenu]);
  const bar = E('div', { class: 'sc-dtl-actions' }, [back, maintWrap, edit]);

  const typeIcoBox = E('div', { class: 'sc-dtl-avatar' });
  // 앱 전역 H1은 Pretendard sans(다른 9개 화면과 통일). 제품명+버전 문자열도 sans로 렌더한다.
  const host = E('h1', { class: 'sc-dtl-host' });
  const stBadge = E('span', { class: 'u-badge' });
  const stDot = E('span', { class: 'u-dot' });
  const stTxt = E('span', {});
  const stReason = E('span', { class: 'sc-dtl-streason' });
  stBadge.appendChild(stDot); stBadge.appendChild(stTxt);

  const mType = E('span', { class: 'sc-dtl-mitem' });
  const mTypeIco = E('span', { class: 'sc-dtl-kico' });
  const mTypeTx = E('span', {});
  mType.appendChild(mTypeIco); mType.appendChild(mTypeTx);
  const mKind = E('span', { class: 'sc-dtl-mitem' });
  const mSite = E('span', { class: 'sc-dtl-mitem' });
  const mSiteIco = E('span', { class: 'sc-dtl-kico' });
  const mSiteTx = E('span', {});
  mSite.appendChild(mSiteIco); mSite.appendChild(mSiteTx);
  const mAsset = E('span', { class: 'u-badge is-mono' });
  const mEac = E('span', { class: 'sc-dtl-mitem sc-dtl-link' });
  const mEacDot = E('span', { class: 'u-dot' });
  const mEacTx = E('span', {});
  mEac.appendChild(mEacDot); mEac.appendChild(mEacTx);
  const mBmc = E('a', { class: 'sc-dtl-mitem sc-dtl-link', target: '_blank', rel: 'noreferrer' });
  const mBmcDot = E('span', { class: 'u-dot' });
  const mBmcTx = E('span', {});
  mBmc.appendChild(mBmcDot); mBmc.appendChild(mBmcTx);

  const sep = () => E('span', { class: 'sc-dtl-msep', text: '·' });
  const s1 = sep(), s2 = sep(), s3 = sep(), s4 = sep(), s5 = sep();

  const metaLine = E('div', { class: 'sc-dtl-meta' }, [
    mType, s1, mKind, s2, mSite, s3, mAsset, s4, mEac, s5, mBmc,
  ]);

  // 우상단 요약은 가용성(SLA 헤드라인) 단일 칩만 둔다.
  // 가동시간은 아래 KPI 타일이 소유 — 두 곳에 같은 값을 인접 중복 표기하지 않는다(중복 제거).
  const availV = E('div', { class: 'u-mono sc-dtl-stat-v' });
  const availK = E('div', { class: 'sc-dtl-stat-k u-muted' });

  const head = E('header', { class: 'sc-dtl-head' }, [
    E('div', { class: 'sc-dtl-headmain' }, [
      typeIcoBox,
      E('div', { class: 'sc-dtl-headtxt' }, [
        E('div', { class: 'sc-dtl-titlerow' }, [host, stBadge, stReason]),
        metaLine,
      ]),
    ]),
    E('div', { class: 'sc-dtl-stats' }, [
      E('div', { class: 'sc-dtl-stat' }, [availV, availK]),
    ]),
  ]);

  PF((d) => {
    setText(back.querySelector('[data-f="backLabel"]'), L('Back to fleet', '목록으로'));
    setText(edit.querySelector('[data-f="editLabel"]'), L('Edit config', '설정 수정'));
    iconInto(typeIcoBox, d.typeIcon || 'box', 26);
    setText(host, d.host || DASH);
    setText(stTxt, d.statusLabel || DASH);
    setText(stReason, d.statusReason || '');
    show(stReason, !!d.statusReason);
    setCls(stBadge, 'u-badge sc-dtl-stbadge', toneCls(d.statusTone));
    setCls(stDot, 'u-dot', 'is-' + toneKey(d.statusTone));

    iconInto(mTypeIco, d.typeIcon || 'box', 13);
    setText(mTypeTx, d.typeLabel || DASH);
    setText(mKind, d.kind || DASH);
    iconInto(mSiteIco, 'bookmark', 13);
    setText(mSiteTx, d.site || DASH);

    const hasAsset = !!d.assetTag;
    setText(mAsset, d.assetTag || '');
    show(mAsset, hasAsset); show(s3, hasAsset);

    const eac = d.eac;
    show(mEac, !!eac); show(s4, !!eac);
    if (eac) {
      setCls(mEacDot, 'u-dot', eac.up ? 'is-pos' : '');
      setText(mEacTx, 'EAC ' + (eac.ip || DASH));
      setCls(mEac, 'sc-dtl-mitem sc-dtl-link', eac.up ? 'is-pos' : 'u-muted');
      mEac.title = L('Edge appliance console', 'Edge 관리 콘솔 (EAC)');
    }
    const bmc = d.bmc;
    show(mBmc, !!bmc); show(s5, !!bmc);
    if (bmc) {
      setCls(mBmcDot, 'u-dot', bmc.up ? 'is-pos' : '');
      setText(mBmcTx, (bmc.name || 'BMC') + ' ' + (bmc.ip || ''));
      setCls(mBmc, 'sc-dtl-mitem sc-dtl-link', bmc.up ? 'is-pos' : 'u-muted');
      if (mBmc.getAttribute('href') !== (bmc.url || '#')) mBmc.setAttribute('href', bmc.url || '#');
      mBmc.title = L('Open out-of-band console', '원격 관리 콘솔 열기');
    }

    setText(availV, d.avail || DASH);
    setText(availK, L('availability', '가용성'));
  });

  return { bar, head, back, edit, maintBtn, maintMenu, maintWrap, maintClearBtn };
}

/* ── 타일 4개 ────────────────────────────────────────────────────────── */
function buildTiles(d) {
  const wrap = E('div', { class: 'sc-dtl-tiles' });
  arr(d.tiles).forEach((_, i) => {
    const get = (dd) => arr(dd.tiles)[i] || {};
    const labIco = E('span', { class: 'sc-dtl-kico' });
    const lab = E('span', {});
    const delta = E('span', { class: 'u-mono sc-dtl-tile-delta u-nowrap' });
    const val = E('span', { class: 'sc-dtl-tile-num' });
    const unit = E('span', { class: 'sc-dtl-tile-unit u-muted' });
    const valBox = E('div', { class: 'u-mono sc-dtl-tile-val' }, [val, unit]);
    const spark = sparkHost(
      (dd) => (get(dd).hasSpark ? get(dd).hist : []),
      (dd) => get(dd).histHi || 100,
      (dd) => get(dd).valueTone || 'pos',
      200, 30,
      (dd) => get(dd).unit,
    );
    const tile = E('div', { class: 'card sc-dtl-tile' }, [
      E('div', { class: 'u-row--between' }, [
        E('span', { class: 'u-row sc-dtl-tile-lab u-muted' }, [labIco, lab]),
        delta,
      ]),
      valBox,
      spark,
    ]);
    P((dd) => {
      const t = get(dd);
      iconInto(labIco, t.icon || 'box', 15);
      setText(lab, t.label || '');
      setText(delta, t.delta || '');
      if (delta.title !== (t.delta || '')) delta.title = t.delta || '';
      setCls(delta, 'u-mono sc-dtl-tile-delta u-nowrap', toneCls(t.deltaTone));
      setText(val, t.value == null ? DASH : t.value);
      setText(unit, t.unit || '');
      setCls(valBox, 'u-mono sc-dtl-tile-val' + (t.hasSpark ? ' is-spark' : ''), toneCls(t.valueTone || ''));
      tile.classList.toggle('is-long', String(t.value || '').length > 12);
      show(spark, !!t.hasSpark);
    });
    wrap.appendChild(tile);
  });
  return wrap;
}

/* ── 알림 배너 ───────────────────────────────────────────────────────── */
function buildNotices(d) {
  const wrap = E('div', { class: 'sc-dtl-notices' });
  arr(d.notices).forEach((_, i) => {
    const get = (dd) => arr(dd.notices)[i] || {};
    const icoBox = E('span', { class: 'sc-dtl-nico' });
    const strong = E('b', {});
    const text = E('span', {});
    const row = E('div', { class: 'sc-dtl-notice' }, [
      icoBox,
      E('span', { class: 'sc-dtl-ntext' }, [strong, E('span', { class: 'sc-dtl-msep', text: ' · ' }), text]),
    ]);
    P((dd) => {
      const n = get(dd);
      iconInto(icoBox, n.icon || 'warningCircle', 15);
      setText(strong, n.strong || '');
      setText(text, n.text || '');
      setCls(row, 'sc-dtl-notice', 'is-' + toneKey(n.tone || 'warn'));
    });
    wrap.appendChild(row);
  });
  return wrap;
}

/* ── PairCard (everRun 계열) ─────────────────────────────────────────── */
function nodePanel(idx) {
  const get = (d) => arr(d.nodes)[idx] || null;
  const name = E('span', { class: 'u-mono sc-dtl-nname' });
  const badge = E('span', { class: 'u-badge sc-dtl-nbadge' });
  const badgeIco = E('span', { class: 'sc-dtl-bico' });
  const badgeTx = E('span', {});
  badge.appendChild(badgeIco); badge.appendChild(badgeTx);
  const reboot = E('span', { class: 'u-badge is-warn' });
  const dot = E('span', { class: 'u-dot' });
  const role = E('div', { class: 'sc-dtl-nrole u-muted u-nowrap' });
  const state = E('div', { class: 'sc-dtl-nstate' });
  const soloChip = E('div', { class: 'u-badge is-warn sc-dtl-solo' });

  const cpuBar = barRow('CPU', (d) => (get(d) || {}).cpuN, (d) => (get(d) || {}).cpu || DASH, (d) => (get(d) || {}).cpuTone, { usage: true });
  const memBar = barRow(L('Memory', '메모리'), (d) => (get(d) || {}).memN, (d) => (get(d) || {}).mem || DASH, (d) => (get(d) || {}).memTone, { usage: true });
  // 가동시간은 상단 KPI 타일이 정본 — 노드 카드에서 중복 표기 제거(minor#1). 재부팅은 위 reboot 뱃지가 담당.

  const panel = E('div', { class: 'sc-dtl-node' }, [
    E('div', { class: 'u-row--between' }, [
      E('span', { class: 'u-row sc-dtl-nhead' }, [name, badge, reboot]),
      dot,
    ]),
    role, state, soloChip,
    E('div', { class: 'sc-dtl-nmetrics' }, [cpuBar, memBar]),
  ]);

  P((d) => {
    const n = get(d);
    const tone = n ? (n.tone || 'pos') : 'neg';
    setCls(panel, 'sc-dtl-node', 'is-' + toneKey(tone));
    setText(name, n ? n.name : DASH);
    setText(badgeTx, n ? (n.badge || L('NODE', '노드')) : L('NODE', '노드'));
    setCls(badge, 'u-badge sc-dtl-nbadge', toneCls(n && n.badgeTone ? n.badgeTone : 'mut'));
    iconInto(badgeIco, n && n.primary ? 'check' : 'infoCircle', 10);
    show(reboot, !!(n && n.rebooted));
    setText(reboot, n && n.rebooted ? (n.rebootLabel || L('Rebooted', '재부팅')) : '');
    setCls(dot, 'u-dot', 'is-' + toneKey(tone));
    setText(role, n ? (n.role || '') : L('Standing by', '대기'));
    setText(state, n ? (n.state || DASH) : L('OFFLINE', '오프라인'));
    setCls(state, 'sc-dtl-nstate', toneCls(tone));
    const nodes = arr(d.nodes);
    const downCnt = nodes.filter((x) => !x || x.tone === 'neg').length;
    const simplex = downCnt > 0 && downCnt < nodes.length;
    const solo = !!(n && n.primary && simplex);
    show(soloChip, solo);
    setText(soloChip, solo ? L('Carrying all workloads alone', '모든 워크로드 단독 처리 중') : '');
  });
  return panel;
}

function switchoverRow() {
  const wrap = E('div', { class: 'sc-dtl-switches' });
  [0, 1].forEach((i) => {
    const get = (d) => arr(d.switchovers)[i] || {};
    const icoBox = E('span', { class: 'sc-dtl-kico' });
    const lab = E('span', { class: 'u-nowrap' });
    const flow = E('span', { class: 'u-mono sc-dtl-flow is-pos' });
    const ts = E('div', { class: 'u-mono sc-dtl-swts' });
    const box = E('div', { class: 'sc-dtl-switch' }, [
      E('div', { class: 'u-row--between' }, [
        E('span', { class: 'u-row u-muted sc-dtl-swlab' }, [icoBox, lab]),
        flow,
      ]),
      ts,
    ]);
    P((d) => {
      const s = get(d);
      iconInto(icoBox, s.icon || 'cycle', 13);
      setText(lab, s.label || '');
      setText(flow, s.flow || '');
      show(flow, !!s.flow);
      setText(ts, s.ts || s.none || DASH);
      setCls(ts, 'u-mono sc-dtl-swts', s.ts ? '' : 'u-muted');
    });
    wrap.appendChild(box);
  });
  return wrap;
}

/**
 * PairCard 상태 계약(#356) — nodes 가 0건이면 장애가 아니라 데이터 없음(nodata)이다.
 * total===0 이면 allDown·simplex 가 모두 false 라 종전에는 'duplex'(칩 '듀플렉스 · 보호됨'
 * + 히어로 '보호 활성')로 떨어졌다 — 노드 패널의 '오프라인' 표기와 자기모순인 거짓 안전 신호.
 * 데이터 없음을 별도 상태로 내리는 것은 #315 라이선스('영구' 오표기 금지)와 같은 원칙이다.
 * #395: total===1(단일 노드 가동)도 보호가 아니다 — 페일오버 상대가 없으므로 simplex 계열.
 * deriveSync(data.js)도 healthy>=2 아니면 'simplex' 라 개요/플릿은 '심플렉스'인데
 * 상세만 '듀플렉스 · 보호됨'으로 갈리는 모순이었다.
 * 반환: 'duplex' | 'simplex' | 'offline' | 'nodata'.
 */
export function pairState(nodes) {
  const list = arr(nodes);
  const total = list.length;
  if (total === 0) return 'nodata';
  const downCnt = list.filter((x) => !x || x.tone === 'neg').length;
  if (downCnt === total) return 'offline';
  if (total === 1) return 'simplex'; // #395 — 단독 가동은 보호 아님(deriveSync 와 정합)
  return downCnt > 0 ? 'simplex' : 'duplex';
}

/** PairCard 상태 → 톤(#356) — nodata 는 pos/warn/neg 어느 것도 아닌 mut(중립). */
export function pairTone(st) {
  // 심플렉스(보호 상실, 회복 가능)=앰버, 오프라인(실장애)=red, 듀플렉스=녹색 (topology 정합, minor#7).
  return st === 'duplex' ? 'pos' : (st === 'simplex' ? 'warn' : (st === 'offline' ? 'neg' : 'mut'));
}

function pairCard() {
  const chip = E('span', { class: 'u-badge sc-dtl-rbadge' });
  const chipDot = E('span', { class: 'u-dot' });
  const chipTx = E('span', {});
  const chipSub = E('span', { class: 'u-muted' });
  chip.appendChild(chipDot); chip.appendChild(chipTx); chip.appendChild(chipSub);

  const heroIco = E('span', { class: 'sc-dtl-heroico' });
  const heroT = E('div', { class: 'sc-dtl-herot' });
  const heroS = E('div', { class: 'sc-dtl-heros u-muted' });
  const hero = E('div', { class: 'sc-dtl-hero' }, [heroIco, E('div', { class: 'u-flex' }, [heroT, heroS])]);

  const linkIco = E('span', { class: 'sc-dtl-linkico' });
  const linkLab = E('div', { class: 'sc-dtl-linklab' });
  const linkLine = E('div', { class: 'sc-dtl-linkline' });
  const linkCol = E('div', { class: 'sc-dtl-linkcol' }, [linkLine, E('div', { class: 'sc-dtl-linkdot' }, [linkIco]), linkLab]);

  const pair = E('div', { class: 'sc-dtl-pair' }, [nodePanel(0), linkCol, nodePanel(1)]);
  const sw = switchoverRow();

  const c = cardBox('sc-dtl-c-pair');
  c.appendChild(cardHead(L('Fault tolerance', '무정지 이중화'), chip));
  c.appendChild(E('div', { class: 'card-body' }, [hero, pair, sw]));

  P((d) => {
    const nodes = arr(d.nodes);
    const total = nodes.length;
    const st = pairState(nodes);
    const tone = pairTone(st);
    const endu = !!(d && d.endurance); // ztC Endurance — Active/Standby 모델(미러·듀플렉스 용어 대체)
    const up = nodes.find((x) => x && x.tone !== 'neg');
    const dn = nodes.find((x) => !x || x.tone === 'neg');

    setCls(chip, 'u-badge sc-dtl-rbadge', toneCls(tone));
    setCls(chipDot, 'u-dot', 'is-' + toneKey(tone));
    setText(chipTx, st === 'duplex' ? (endu ? 'Active/Standby' : L('Duplex', '듀플렉스')) : (st === 'simplex' ? L('Simplex', '심플렉스') : (st === 'offline' ? L('Offline', '오프라인') : L('No data', '데이터 없음'))));
    setText(chipSub, st === 'duplex' ? ('· ' + L('protected', '보호됨')) : (st === 'simplex' ? ('· ' + L('exposed', '보호 상실')) : ''));

    iconInto(heroIco, st === 'duplex' ? 'check' : (st === 'nodata' ? 'infoCircle' : 'warningCircle'), 22);
    setCls(hero, 'sc-dtl-hero', 'is-' + toneKey(tone));
    setText(heroT, st === 'duplex' ? L('Fully protected', '이중화 보호 활성')
      : (st === 'simplex'
        ? L('Redundancy lost — ' + ((up && up.name) || 'one node') + ' is running alone',
          '이중화 상실 — ' + ((up && up.name) || '단일 노드') + ' 단독 운영 중')
        : (st === 'offline' ? L('Cluster offline', '클러스터 오프라인') : L('No node data', '노드 데이터 없음'))));
    setCls(heroT, 'sc-dtl-herot', toneCls(tone));
    // 중복 진술 해소(minor#2 + G17): 배지('듀플렉스/오프라인/데이터 없음')가 이미 말하는 상태는 배너 제목을 숨긴다.
    // 듀플렉스 → 제목 숨김(설명만), 오프라인·nodata → 제목이 배지의 인접 재진술이라 숨기고 설명만 남긴다.
    // simplex 만 제목 유지 — '어느 노드가 단독 운영인지' 배지에 없는 정보를 담는다.
    show(heroT, st === 'simplex');
    setText(heroS, st === 'duplex' ? (endu ? L('Smart Exchange — service continuity without reboot', 'Smart Exchange — 재부팅 없는 서비스 연속성') : L('Both nodes mirrored · automatic failover ready', '두 노드 이중화 미러링 · 자동 페일오버 준비'))
      : (st === 'simplex'
        ? L('No failover protection until ' + ((dn && dn.name) || 'the peer') + ' recovers',
          ((dn && dn.name) || '피어') + ' 복구 전까지 페일오버 보호 없음')
        : (st === 'offline' ? L('No nodes are reachable', '응답하는 노드가 없습니다')
          : L('The poller is not reporting node information', '폴리가 노드 정보를 보고하지 않고 있습니다'))));

    iconInto(linkIco, st === 'duplex' ? 'link' : (st === 'nodata' ? 'infoCircle' : 'close'), 18);
    setCls(linkCol, 'sc-dtl-linkcol', 'is-' + toneKey(tone));
    setText(linkLab, st === 'duplex' ? (endu ? 'FABRIC' : L('MIRROR', '미러')) : (st === 'nodata' ? DASH : L('SEVERED', '끊김')));
    show(pair.children[1], total > 1);
    show(pair.children[2], total > 1);
    show(sw, arr(d.switchovers).length > 0);
  });
  return c;
}

/* ── ChartCard ───────────────────────────────────────────────────────── */
// 노드별 현재값(CPU/MEM)은 PairCard/EnclosureCard/ServerCard가 이미 보여주고,
// 집계 현재값은 상단 KPI 타일(CPU/메모리)이 이미 보여준다 — 이 카드는 그 값들과
// 겹치지 않도록 "현재값"을 다시 표기하지 않고, 최근 추이(라인차트)와 최대치(peak)만 보여준다.
function chartCard(d) {
  const sub = E('div', { class: 'u-muted sc-dtl-chsub' });

  const W = 460, H = 150;
  const svgNS = 'http://www.w3.org/2000/svg';
  const mkSvg = (tag, attrs) => {
    const n = document.createElementNS(svgNS, tag);
    Object.entries(attrs || {}).forEach(([k, v]) => n.setAttribute(k, v));
    return n;
  };
  const lineSvg = mkSvg('svg', { width: '100%', height: '120', viewBox: '0 0 ' + W + ' ' + H, preserveAspectRatio: 'none' });
  const baseline = mkSvg('line', { x1: 8, y1: H - 8, x2: W - 8, y2: H - 8, 'stroke-width': 1, 'vector-effect': 'non-scaling-stroke' });
  baseline.style.stroke = 'var(--line)';
  lineSvg.appendChild(baseline);
  const memArea = mkSvg('path', { d: '' });
  memArea.style.fill = tonePaint('mut');
  memArea.style.fillOpacity = '0.1';
  const cpuArea = mkSvg('path', { d: '' });
  cpuArea.style.fill = tonePaint('pos');
  cpuArea.style.fillOpacity = '0.12';
  const memLine = mkSvg('path', { fill: 'none', 'stroke-width': 1.5, 'vector-effect': 'non-scaling-stroke', d: '' });
  memLine.style.stroke = tonePaint('mut');
  const cpuLine = mkSvg('path', { fill: 'none', 'stroke-width': 1.5, 'vector-effect': 'non-scaling-stroke', d: '' });
  cpuLine.style.stroke = tonePaint('pos');
  [memArea, cpuArea, memLine, cpuLine].forEach((n) => lineSvg.appendChild(n));
  const legend = E('div', { class: 'sc-dtl-chlegend' }, [
    E('span', { class: 'u-row' }, [E('span', { class: 'sc-dtl-lseg is-pos' }), E('span', { class: 'u-muted', text: 'CPU' })]),
    E('span', { class: 'u-row' }, [E('span', { class: 'sc-dtl-lseg' }), E('span', { class: 'u-muted', text: L('Memory', '메모리') })]),
    E('span', { class: 'u-muted sc-dtl-chtrend', text: L('recent trend', '최근 추이') }),
  ]);
  const lineBox = E('div', { class: 'sc-dtl-chline' }, [legend, lineSvg]);
  const emptyBox = E('div', { class: 'u-muted sc-dtl-chempty', text: L('No trend data yet', '추이 데이터 없음') });

  // 자원 할당(unit) — 사용률이 아닌 '할당량'이라 상단 타일과 겹치지 않는다.
  const resBox = E('div', { class: 'sc-dtl-res' });
  const resV = barRow(L('vCPU allocated', 'vCPU 할당'),
    (dd) => (dd.resource || {}).vcpuPct,
    (dd) => {
      const r = dd.resource || {};
      return r.has ? (r.vcpuUsed + ' / ' + r.vcpuTot + ' vCPU') : DASH;
    },
    (dd) => (dd.resource || {}).vcpuTone, { usage: true });
  const resM = barRow(L('Memory allocated', '메모리 할당'),
    (dd) => (dd.resource || {}).memPct,
    (dd) => {
      const r = dd.resource || {};
      return r.has ? (r.memUsed + ' / ' + r.memTot + ' GiB') : DASH;
    },
    (dd) => (dd.resource || {}).memTone, { usage: true });
  resBox.appendChild(resV); resBox.appendChild(resM);

  const c = cardBox('sc-dtl-c-chart');
  const head = E('div', { class: 'card-head' }, [
    E('div', { class: 'u-col' }, [E('h2', { class: 'card-title', text: L('Usage trend', 'CPU · 메모리 추이') }), sub]),
  ]);
  c.appendChild(head);
  c.appendChild(E('div', { class: 'card-body' }, [lineBox, emptyBox, resBox]));

  P((dd) => {
    const hasRes = !!(dd.resource && dd.resource.has);
    const ch = arr(dd.cpuHist), mh = arr(dd.memHist);
    const hasLine = ch.length > 1 || mh.length > 1;

    const base = hasRes ? L('Allocation · capacity', '자원 할당 · 용량') : L('Recent trend', '최근 추이');
    const peakTxt = hasLine
      ? (' · CPU ' + L('peak', '최대') + ' ' + (dd.cpuPeak || DASH)
        + ' · ' + L('Memory', '메모리') + ' ' + L('peak', '최대') + ' ' + (dd.memPeak || DASH))
      : '';
    setText(sub, base + peakTxt);

    show(lineBox, hasLine);
    show(emptyBox, !hasLine);
    if (hasLine) {
      try {
        const s = U.svg;
        cpuArea.setAttribute('d', ch.length > 1 ? s.areaPath(ch, W, H, 8, 0, 100) : '');
        memArea.setAttribute('d', mh.length > 1 ? s.areaPath(mh, W, H, 8, 0, 100) : '');
        cpuLine.setAttribute('d', ch.length > 1 ? s.linePath(ch, W, H, 8, 0, 100) : '');
        memLine.setAttribute('d', mh.length > 1 ? s.linePath(mh, W, H, 8, 0, 100) : '');
      } catch (e) { /* noop */ }
    }
    show(resBox, hasRes);
  });
  return c;
}

/* ── VmTable ─────────────────────────────────────────────────────────── */
/**
 * VM hover 인라인 제어 버튼셋 — 실행 상태가 유일한 기준이다.
 * 과거 비교 키('run:<이름>')를 그대로 'run' 과 비교해 영구 false 가 돼 실행 중 VM에도
 * '시작'만 뜬 회귀가 있었다. 키 문자열이 아니라 p.running 에서 직접 파생시켜
 * 갱신 키와 분기가 다시는 어긋나지 않게 한다 — export 해서 게이트로 묶는다.
 */
export function vmActionPlan(running) {
  return running
    ? [{ en: 'Shutdown', ko: '종료', act: 'vm-shutdown', destructive: false, danger: false },
       { en: 'Power off', ko: '강제 종료', act: 'vm-poweroff', destructive: true, danger: true }]
    : [{ en: 'Power on', ko: '시작', act: 'vm-poweron', destructive: false, danger: false }];
}

function vmTable(d) {
  // VM 실행 수는 상단 KPI 타일(VM n/n)이 정본 — 카드 헤더에 같은 수치를 다시 달지 않는다(중복 제거).
  const body = E('div', { class: 'sc-dtl-vmgrid' });
  const headRow = E('div', { class: 'u-mono sc-dtl-vmhead' }, [
    E('span', { text: L('Name', '이름') }),
    E('span', { text: L('NODE', '노드') }),
    E('span', { class: 'is-num', text: 'CPU' }),
    E('span', { class: 'is-num', text: 'MEM' }),
    E('span', { class: 'is-num', text: L('Status', '상태') }),
  ]);
  body.appendChild(headRow);

  arr(d.procs).forEach((_, i) => {
    const get = (dd) => arr(dd.procs)[i] || {};
    const nm = E('span', { class: 'u-mono sc-dtl-vmname u-nowrap' });
    const ftB = E('span', { class: 'u-badge' });
    // 보호 저하는 톤 변화만으로 전달하면 안 된다(색맹·빠른 스캔). 어느 다리가 끊겼는지 문자로 쓴다.
    const protB = E('span', { class: 'u-badge is-neg sc-dtl-vmprot' });
    const ipB = E('span', { class: 'u-badge is-mono' });
    const wrB = E('span', { class: 'u-badge is-pos', text: 'WinRM' });
    const os = E('div', { class: 'u-muted sc-dtl-vmos u-nowrap' });
    const node = E('span', { class: 'u-mono u-nowrap sc-dtl-vmnode' });
    const cpu = E('span', { class: 'u-mono is-num u-ink2' });
    const mem = E('span', { class: 'u-mono is-num u-ink2' });
    const stB = E('span', { class: 'u-badge' });
    // 제어 액션을 VM 행에 인라인으로 흡수(hover 노출) — '제어' 카드의 VM 이중 나열 제거(minor#1).
    // 기존 위임 핸들러(data-dtl-act)를 그대로 재사용해 확인 모달·토스트가 동일하게 동작한다.
    const acts = E('div', { class: 'sc-dtl-vmacts' });
    const row = E('div', { class: 'sc-dtl-vmrow' }, [
      E('div', { class: 'sc-dtl-vmcell' }, [E('div', { class: 'u-row' }, [nm, ftB, protB, ipB, wrB]), os]),
      node, cpu, mem, E('span', { class: 'is-num sc-dtl-vmst' }, [stB]),
      acts,
    ]);
    let vmMode = '';
    P((dd) => {
      const p = get(dd);
      setText(nm, p.name || DASH);
      show(ftB, !!p.ft);
      setText(ftB, p.ft || '');
      setCls(ftB, 'u-badge', toneCls(p.ftTone));
      ftB.title = p.ftTitle || '';
      // 보호 상실 — 폴러의 diskMirrored/nicRedundant 가 false 일 때만 뜬다.
      show(protB, !!p.protDegraded);
      setText(protB, p.protDegraded ? (arr(p.protLegs).join(' · ') + ' ' + L('lost', '상실')) : '');
      protB.title = p.ftTitle || '';
      show(ipB, !!p.ip);
      setText(ipB, p.ip || '');
      show(wrB, !!p.winrm);
      const sub = [p.guestHost, p.os].filter(Boolean).join(' · ');
      show(os, !!sub);
      setText(os, sub);
      setText(node, p.node || DASH);
      // G29: 배치 노드명(sj-edge-…-node0)이 광폭 mono 에서 절단될 수 있어 원문 툴팁 보존.
      node.title = p.node || '';
      // D2: CPU·MEM 은 '할당량'(부하 아님)이라 상태색(green)으로 태우지 않는다 — 두 열 모두 중립 잉크로
      //     통일하고, 정지 VM 만 비활성(회색)으로 낮춘다(예전 CPU 열 일괄 green ↔ MEM 회색 불일치 해소).
      const vmDim = !p.running;
      setText(cpu, p.cpu || DASH);
      setCls(cpu, 'u-mono is-num', vmDim ? 'u-muted' : 'u-ink2');
      setText(mem, p.mem || DASH);
      setCls(mem, 'u-mono is-num', vmDim ? 'u-muted' : 'u-ink2');
      setText(stB, p.st || DASH);
      setCls(stB, 'u-badge', toneCls(p.stTone));
      // 인라인 제어(제어 액션이 있는 FT 계열만) — 실행/정지에 따라 버튼셋을 갱신.
      if (d.control) {
        // 비교 키에 VM 이름 포함 — 행 인덱스는 그대로인데 대상 VM이 바뀌면(같은 실행상태)
        // 버튼의 data-dtl-target 이 구 이름으로 남는 부실 갱신 방지.
        const nextMode = (p.running ? 'run' : 'stop') + ':' + (p.name || '');
        if (nextMode !== vmMode) {
          vmMode = nextMode;
          acts.textContent = '';
          const nm2 = p.name || '';
          vmActionPlan(p.running).forEach((a) => {
            acts.appendChild(actionBtn(L(a.en, a.ko), a.act, nm2, a.destructive, a.danger));
          });
        }
      }
    });
    body.appendChild(row);
  });
  if (!arr(d.procs).length) {
    body.appendChild(E('div', { class: 'u-muted sc-dtl-listempty', text: L('No virtual machines', '가상 머신 없음') }));
  }

  const c = cardBox('sc-dtl-c-vm');
  c.appendChild(cardHead(d.procTitle || L('Virtual machines', '가상 머신')));
  c.appendChild(E('div', { class: 'card-body u-scroll sc-dtl-vmwrap' }, [body]));
  return c;
}

/* ── LicenseCard ─────────────────────────────────────────────────────── */
function licenseCard() {
  const nameB = E('span', { class: 'u-badge is-mono' });
  const stB = toneBadge((d) => (d.license || {}).statusLabel || '', (d) => (d.license || {}).statusTone || 'mut');
  const grid = E('div', { class: 'sc-dtl-licgrid' });
  const fields = [
    [L('Edition', '에디션'), (d) => (d.license || {}).edition || DASH, null],
    [L('Type', '유형'), (d) => (d.license || {}).typeLabel || DASH, null],
    [null, (d) => (d.license || {}).expiry || DASH, (d) => (d.license || {}).dayTone],
    [L('Installed', '설치일'), (d) => (d.license || {}).install || DASH, null],
  ];
  const kNodes = [];
  fields.forEach(([k, get, tone]) => {
    const kn = E('div', { class: 'sc-dtl-lick u-muted', text: k || '' });
    const vn = E('div', { class: 'u-mono sc-dtl-licv u-nowrap' });
    kNodes.push(kn);
    P((d) => {
      setText(vn, get(d));
      setCls(vn, 'u-mono sc-dtl-licv u-nowrap', tone ? toneCls(tone(d)) : '');
    });
    grid.appendChild(E('div', {}, [kn, vn]));
  });
  P((d) => {
    setText(kNodes[2], (d.license || {}).expiryLabel || L('Expiry', '만료'));
    const nm = (d.license || {}).name || '';
    show(nameB, !!nm);
    setText(nameB, nm);
  });

  const c = cardBox('sc-dtl-c-lic');
  const head = cardHead(L('License', '라이선스'), stB, 'checklist');
  head.insertBefore(nameB, stB);
  c.appendChild(head);
  c.appendChild(E('div', { class: 'card-body' }, [grid]));
  return c;
}

/* ── HwCard ──────────────────────────────────────────────────────────── */
function hwCard(d) {
  const list = E('div', { class: 'sc-dtl-hwlist' });
  arr(d.hw).forEach((_, i) => {
    const get = (dd) => arr(dd.hw)[i] || {};
    const icoBox = E('span', { class: 'sc-dtl-hwico' });
    const lab = E('div', { class: 'sc-dtl-hwlab' });
    const det = E('div', { class: 'u-muted sc-dtl-hwdet' });
    const val = E('span', { class: 'u-mono sc-dtl-hwval' });
    const stB = E('span', { class: 'u-badge' });
    const dot = E('span', { class: 'u-dot' });
    const stTx = E('span', {});
    stB.appendChild(dot); stB.appendChild(stTx);
    const row = E('div', { class: 'sc-dtl-hwrow' }, [
      icoBox, E('div', { class: 'u-flex' }, [lab, det]), val, stB,
    ]);
    P((dd) => {
      const h = get(dd);
      iconInto(icoBox, h.icon || 'ops', 17);
      setText(lab, h.label || DASH);
      setText(det, h.detail || '');
      show(det, !!h.detail);
      // 실행 상태(running/정상)는 FT 에선 비워온다(minor#6) — 값·뱃지를 숨겨 모델·용량만 남긴다.
      setText(val, h.value || '');
      show(val, !!h.value);
      setText(stTx, h.stat || '');
      setCls(stB, 'u-badge', toneCls(h.statTone));
      setCls(dot, 'u-dot', 'is-' + toneKey(h.tone));
      show(stB, !!h.stat);   // setCls 가 className 을 덮으므로 숨김은 반드시 마지막에.
    });
    list.appendChild(row);
  });
  if (!arr(d.hw).length) {
    list.appendChild(E('div', { class: 'u-muted sc-dtl-listempty', text: L('No hardware data', '하드웨어 정보 없음') }));
  }
  const c = cardBox('sc-dtl-c-hw');
  c.appendChild(cardHead(L('Hardware health', '하드웨어 상태')));
  c.appendChild(E('div', { class: 'card-body' }, [list]));
  return c;
}

/* ── AlertsCard ──────────────────────────────────────────────────────── */
function alertsCard(d) {
  const more = E('button', {
    class: 'btn btn--ghost btn--sm sc-dtl-more', type: 'button', 'data-dtl-incidents': '',
  }, [E('span', { text: L('All incidents', '전체 인시던트 보기') })]);
  const list = E('div', { class: 'sc-dtl-alist' });
  const n = arr(d.alertsList).length;
  if (!n) {
    list.appendChild(E('div', { class: 'u-muted sc-dtl-listempty', text: L('No recent alerts', '최근 알림 없음') }));
  }
  for (let i = 0; i < n; i += 1) {
    const get = (dd) => arr(dd.alertsList)[i] || {};
    const dot = E('span', { class: 'u-dot' });
    const desc = E('div', { class: 'sc-dtl-adesc u-nowrap' });
    const ago = E('div', { class: 'u-mono u-muted sc-dtl-aago' });
    const row = E('div', { class: 'sc-dtl-arow' }, [dot, E('div', { class: 'u-flex' }, [desc, ago])]);
    P((dd) => {
      const a = get(dd);
      setCls(dot, 'u-dot', 'is-' + toneKey(a.tone));
      setText(desc, a.desc || '');
      desc.title = a.desc || '';
      setText(ago, a.ago || a.time || '');
      ago.title = a.time || '';
    });
    list.appendChild(row);
  }
  const c = cardBox('sc-dtl-c-alerts');
  c.appendChild(cardHead(L('Recent alerts', '최근 알림'), more, 'bell'));
  c.appendChild(E('div', { class: 'card-body' }, [list]));
  return c;
}

/* ── TrapsCard ───────────────────────────────────────────────────────── */
function trapsCard(d) {
  const list = E('div', { class: 'sc-dtl-alist' });
  arr(d.trapsList).forEach((_, i) => {
    const get = (dd) => arr(dd.trapsList)[i] || {};
    const dot = E('span', { class: 'u-dot' });
    // 심각도 텍스트 라벨(ERROR/WARN/INFO) — 색맹 대응 + 로그 화면과 부호화 통일(minor).
    // 색은 앞 도트(형태/색 토큰)가 운반하므로 텍스트는 중립(ink2)으로 둔다(로그 sc-log-level 규칙과 동일).
    const lvl = E('span', { class: 'u-mono sc-dtl-traplvl' });
    const desc = E('div', { class: 'sc-dtl-adesc u-nowrap' });
    const meta = E('div', { class: 'u-mono u-muted sc-dtl-aago' });
    const row = E('div', { class: 'sc-dtl-arow' }, [dot, lvl, E('div', { class: 'u-flex' }, [desc, meta])]);
    P((dd) => {
      const t = get(dd);
      setCls(dot, 'u-dot', 'is-' + toneKey(t.tone));
      setText(lvl, t.level || '');
      setText(desc, t.desc || '');
      desc.title = t.desc || '';
      setText(meta, (t.time || '') + (t.src ? ' · ' + t.src : ''));
    });
    list.appendChild(row);
  });
  const c = cardBox('sc-dtl-c-traps');
  c.appendChild(cardHead(L('SNMP traps (live)', 'SNMP 트랩 (실시간)'), E('span', { class: 'u-mono card-sub', text: 'udp/162' }), 'bolt'));
  c.appendChild(E('div', { class: 'card-body' }, [list]));
  return c;
}

/* ── ControlsCard (everRun 계열만) ──────────────────────────────────── */
function actionBtn(label, action, target, destructive, danger) {
  return E('button', {
    class: 'btn btn--sm ' + (danger ? 'btn--danger' : 'btn--outline'),
    type: 'button',
    'data-dtl-act': action,
    'data-dtl-target': target,
    'data-dtl-label': label,
    'data-dtl-destructive': destructive ? '1' : '',
  }, [E('span', { text: label })]);
}

function controlsCard(d) {
  const c0 = d.control || {};
  const rows = E('div', { class: 'sc-dtl-ctlrows' });

  arr(c0.nodes).forEach((n, i) => {
    const get = (dd) => arr((dd.control || {}).nodes)[i] || {};
    const nm = E('div', { class: 'u-mono sc-dtl-ctlname' });
    const sub = E('div', { class: 'u-muted sc-dtl-ctlsub' });
    const btns = E('div', { class: 'btn-group sc-dtl-ctlbtns' });
    const row = E('div', { class: 'sc-dtl-ctlrow' }, [E('div', { class: 'u-flex' }, [nm, sub]), btns]);
    let mode = '';
    P((dd) => {
      const x = get(dd);
      setText(nm, x.name || n.name || DASH);
      setText(sub, x.maint ? L('in maintenance', '점검 중')
        : (x.down ? L('offline', '오프라인')
          : ((dd && dd.endurance)
            ? (x.primary ? 'ACTIVE' : 'STANDBY')
            : (x.primary ? L('primary', '주 노드') : L('standby', '보조 노드')))));
      const nextMode = x.maint ? 'maint' : (x.down ? 'down' : 'up');
      if (nextMode === mode) return;
      mode = nextMode;
      btns.textContent = '';
      const nm2 = x.name || n.name || '';
      if (nextMode === 'maint') {
        btns.appendChild(actionBtn(L('Exit maintenance', '점검 해제'), 'node-workoff', nm2, false, false));
      } else if (nextMode === 'down') {
        btns.appendChild(actionBtn(L('Recover', '복구'), 'node-recover', nm2, false, false));
      } else {
        btns.appendChild(actionBtn(L('Maintenance', '점검 모드'), 'node-workon', nm2, true, false));
        btns.appendChild(actionBtn(L('Reboot', '재부팅'), 'node-reboot', nm2, true, true));
        btns.appendChild(actionBtn(L('Shutdown', '종료'), 'node-shutdown', nm2, true, true));
      }
    });
    rows.appendChild(row);
  });

  // VM 제어는 위 '가상 머신' 카드 행에 인라인(hover)으로 흡수했다 — 여기선 노드 전용으로 축소(minor#1).

  const res = E('div', { class: 'sc-dtl-ctlres u-hide' });
  const note = E('p', { class: 'u-muted sc-dtl-ctlnote' });
  const c = cardBox('sc-dtl-c-ctl');
  c.appendChild(cardHead(L('Node controls', '노드 제어'), null, 'ops'));
  c.appendChild(E('div', { class: 'card-body' }, [note, rows, res]));
  P((d) => {
    // Endurance 는 클러스터가 아니라 단일 섀시 안의 컴퓨트 모듈 쌍이다(용어 정정).
    setText(note, (d && d.endurance)
      ? L('Module actions run on the compute modules (simulated when the poller is offline).', '모듈 액션을 컴퓨트 모듈에 실행합니다 (시뮬레이션 모드에서는 로컬 상태로 흉내).')
      : L('Node actions run on the cluster (simulated when the poller is offline). Control VMs inline in the list above.', '노드 액션을 클러스터에 실행합니다 (시뮬레이션 모드에서는 로컬 상태로 흉내). VM 제어는 위 가상 머신 목록에서 실행하세요.'));
    show(res, !!resultMsg);
    if (resultMsg) {
      setText(res, (resultMsg.ok ? '✓ ' : '✕ ') + resultMsg.msg);
      setCls(res, 'sc-dtl-ctlres', resultMsg.ok ? 'is-pos' : 'is-neg');
    }
  });
  return c;
}

/* ── EnclosureCard (fts) ─────────────────────────────────────────────── */
function enclosureCard(d) {
  const badge = toneBadge((dd) => dd.syncLabel || '', (dd) => dd.syncTone || 'mut', (dd) => dd.syncIcon || 'cycle');
  const box = E('div', { class: 'sc-dtl-encl' });
  arr(d.nodes).forEach((_, i) => {
    const get = (dd) => arr(dd.nodes)[i] || {};
    const nm = E('span', { class: 'u-mono sc-dtl-nname' });
    const bg = E('span', { class: 'u-badge' });
    const dot = E('span', { class: 'u-dot' });
    const role = E('div', { class: 'u-muted sc-dtl-nrole' });
    const state = E('div', { class: 'u-mono sc-dtl-nstate' });
    const cpu = barRow('CPU', (dd) => get(dd).cpuN, (dd) => get(dd).cpu || DASH, (dd) => get(dd).cpuTone, { usage: true });
    const mem = barRow(L('Memory', '메모리'), (dd) => get(dd).memN, (dd) => get(dd).mem || DASH, (dd) => get(dd).memTone, { usage: true });
    const upV = E('span', { class: 'u-mono sc-dtl-kv-v' });
    const upRow = E('div', { class: 'sc-dtl-kv' }, [E('span', { class: 'u-muted', text: L('Uptime', '가동시간') }), upV]);
    const mods = E('div', { class: 'sc-dtl-mods' });
    const panel = E('div', { class: 'sc-dtl-node' }, [
      E('div', { class: 'u-row--between' }, [E('span', { class: 'u-row sc-dtl-nhead' }, [nm, bg]), dot]),
      role, state,
      E('div', { class: 'sc-dtl-nmetrics' }, [cpu, mem, upRow]),
      mods,
    ]);
    let modSig = '';
    P((dd) => {
      const n = get(dd);
      const tone = n.tone || 'pos';
      setCls(panel, 'sc-dtl-node', 'is-' + toneKey(tone));
      setText(nm, n.name || DASH);
      setText(bg, n.badge || 'LOCKSTEP');
      setCls(bg, 'u-badge', toneCls(n.badgeTone));
      setCls(dot, 'u-dot', 'is-' + toneKey(tone));
      setText(role, n.role || '');
      setText(state, n.state || DASH);
      setCls(state, 'u-mono sc-dtl-nstate', toneCls(tone));
      setText(upV, n.uptime || DASH);
      setCls(upV, 'u-mono sc-dtl-kv-v', n.rebooted ? 'is-warn' : '');
      const ml = arr(n.modules);
      const s = ml.map((m) => (m.name || '') + ':' + (m.state || '')).join('|');
      if (s === modSig) return;
      modSig = s;
      mods.textContent = '';
      ml.forEach((m) => {
        const ok = m.state === 'normal';
        mods.appendChild(E('span', { class: 'u-row sc-dtl-mod ' + (ok ? 'u-ink2' : 'is-warn') }, [
          E('span', { class: 'u-dot ' + (ok ? 'is-pos' : 'is-warn') }),
          E('span', { text: (m.name || '') + (ok ? '' : L(' · warn', ' · 주의')) }),
        ]));
      });
    });
    box.appendChild(panel);
  });

  const c = cardBox('sc-dtl-c-encl');
  c.appendChild(cardHead(L('CPU-I/O enclosures', 'CPU-I/O 인클로저'), badge));
  c.appendChild(E('div', { class: 'card-body' }, [box, ftFoot()]));
  return c;
}

/* FT 요약 푸터(ftTitle/ftSub) */
function ftFoot() {
  const icoBox = E('span', { class: 'sc-dtl-heroico' });
  const t = E('div', { class: 'sc-dtl-fttitle' });
  const s = E('div', { class: 'u-muted sc-dtl-ftsub' });
  const box = E('div', { class: 'sc-dtl-ftfoot' }, [icoBox, E('div', { class: 'u-flex' }, [t, s])]);
  P((d) => {
    // G27(판정 minor): PLC 정상 상태의 recap 스트립은 히어로 타일(상태·프로토콜·링크)의
    //     3중 재진술이라 통째로 숨긴다 — 이상(다운/에러) 시에만 노출해 '확인 스트립'이 아니라
    //     '주의 스트립'으로 역할을 좁힌다. 다른 변형(FT/서버)은 기존 유지.
    const hideRecap = d.variant === 'plc' && d.ftTone === 'pos';
    box.hidden = hideRecap;
    if (hideRecap) return;
    iconInto(icoBox, d.ftTone === 'pos' ? 'check' : 'warningCircle', 19);
    setCls(icoBox, 'sc-dtl-heroico', toneCls(d.ftTone));
    setText(t, d.ftTitle || '');
    setText(s, d.ftSub || '');
  });
  return box;
}

/* ── ServerCard (srv/pc) ─────────────────────────────────────────────── */
function serverCard(d) {
  const badge = toneBadge((dd) => dd.syncLabel || '', (dd) => dd.syncTone || 'mut', (dd) => dd.syncIcon || 'check');
  const state = E('span', { class: 'u-mono sc-dtl-nstate' });
  const role = E('span', { class: 'u-muted sc-dtl-nrole' });
  const hwLine = E('div', { class: 'u-muted sc-dtl-hwline' });
  const pcGrid = E('div', { class: 'sc-dtl-pcrows' });
  arr(d.pcRows).forEach((_, i) => {
    const get = (dd) => arr(dd.pcRows)[i] || {};
    const k = E('span', { class: 'u-muted' });
    const v = E('span', { class: 'u-mono sc-dtl-pcv' });
    P((dd) => { setText(k, get(dd).k || ''); setText(v, get(dd).v || ''); });
    pcGrid.appendChild(k); pcGrid.appendChild(v);
  });
  const n0 = (dd) => arr(dd.nodes)[0] || {};
  const cpu = barRow('CPU', (dd) => n0(dd).cpuN, (dd) => n0(dd).cpu || DASH, (dd) => n0(dd).cpuTone, { usage: true });
  const mem = barRow(L('Memory', '메모리'), (dd) => n0(dd).memN, (dd) => n0(dd).mem || DASH, (dd) => n0(dd).memTone, { usage: true });
  // 가동시간은 상단 KPI 타일(tUp)이 정본 — 서버 카드에서 중복 표기 제거(A1, everRun 노드 카드와 동일 규약).

  const c = cardBox('sc-dtl-c-srv');
  c.appendChild(cardHead(d.pairTitle || L('Server', '서버'), badge));
  c.appendChild(E('div', { class: 'card-body' }, [
    E('div', { class: 'u-row sc-dtl-srvstate' }, [state, role]),
    hwLine, pcGrid,
    E('div', { class: 'sc-dtl-srvbars' }, [cpu, mem]),
    ftFoot(),
  ]));
  P((dd) => {
    const n = n0(dd);
    setText(state, n.state || DASH);
    setCls(state, 'u-mono sc-dtl-nstate', toneCls(n.tone));
    setText(role, n.role || '');
    setText(hwLine, dd.hwLine || '');
    show(hwLine, !!dd.hwLine);
    show(pcGrid, arr(dd.pcRows).length > 0);
  });
  return c;
}

/* ── BmcCard ─────────────────────────────────────────────────────────── */
function bmcCard() {
  const title = E('h2', { class: 'card-title' });
  const badge = E('span', { class: 'u-badge' });
  const dot = E('span', { class: 'u-dot' });
  const btx = E('span', {});
  badge.appendChild(dot); badge.appendChild(btx);
  const ip = E('div', { class: 'u-mono sc-dtl-bmcip' });
  const desc = E('div', { class: 'u-muted sc-dtl-bmcdesc', text: L('Out-of-band management (power · console · sensors)', '대역외 원격 관리 (전원·콘솔·센서)') });
  const link = E('a', { class: 'btn btn--primary btn--sm', target: '_blank', rel: 'noreferrer' }, [
    E('span', { text: L('Open console', '콘솔 열기') }),
  ]);
  const head = E('div', { class: 'card-head' }, [E('span', { class: 'sc-dtl-hico' }, [ico('link', 16)]), title, badge]);
  const c = cardBox('sc-dtl-c-bmc');
  c.appendChild(head);
  c.appendChild(E('div', { class: 'card-body' }, [ip, desc, link]));
  P((d) => {
    const b = d.bmc || {};
    setText(title, b.name || 'BMC');
    setText(btx, b.up ? L('Online', '온라인') : L('Offline', '오프라인'));
    setCls(badge, 'u-badge', b.up ? 'is-pos' : '');
    setCls(dot, 'u-dot', b.up ? 'is-pos' : '');
    setText(ip, b.ip || DASH);
    if (link.getAttribute('href') !== (b.url || '#')) link.setAttribute('href', b.url || '#');
  });
  return c;
}

/**
 * NAS 볼륨 바 텍스트 계약(#355) — usedGiB/sizeGiB/pct 중 폴리가 빼먹은 필드는
 * 'undefined' 문자열로 굽지 않고 DASH(—)로 표기한다(§1.6 결측 표기).
 * winCard freeGB 방어(compute.js:1835-1839) 전례: 결측은 조작하지 않고 '—'.
 */
export function nasVolText(v) {
  const x = v || {};
  const u = num(x.usedGiB), s = num(x.sizeGiB), p = num(x.pct);
  return (u != null ? u : DASH) + ' / ' + (s != null ? s : DASH) + ' GiB · ' + (p != null ? p : DASH) + '%';
}

/** NAS 랜포트 텍스트 계약(#355) — ip 결측은 'undefined' 대신 DASH 표기.
    #530: up 결측(null/undefined)은 falsy 분기('미연결')로 떨어뜨리지 않고
    #508(팬·전원)·#522(디스크·RAID ok)와 같은 계약으로 DASH(NA) 중립 표기한다. */
export function nasLanText(p) {
  const x = p || {};
  return (x.ip || DASH) + ' ' + (x.up == null ? DASH : (x.up ? 'UP' : L('down', '미연결')));
}

/* ── NasCard / NasVmCard ─────────────────────────────────────────────── */
function nasCard(d) {
  const nas = d.nas || {};
  const badge = E('span', { class: 'u-badge' });
  const rows = E('div', { class: 'sc-dtl-kvlist' });
  const lab = (s) => (s === 'normal' ? L('Normal', '정상') : (s || DASH));
  const g = (dd) => dd.nas || {};

  if (nas.model) rows.appendChild(kvRow(L('Model', '모델'), (dd) => 'Synology ' + (g(dd).model || ''), { icon: 'ssd' }));
  if (nas.dsm || nas.dsmVersion) rows.appendChild(kvRow('DSM', (dd) => g(dd).dsm || g(dd).dsmVersion || DASH, { icon: 'checklist' }));
  if (nas.serial) rows.appendChild(kvRow(L('Serial', '시리얼'), (dd) => g(dd).serial || DASH, { icon: 'bookmark' }));
  if (arr(nas.lanPorts).length > 1) {
    rows.appendChild(kvRow(L('LAN ports', '랜 포트'), (dd) => arr(g(dd).lanPorts)
      .map(nasLanText).join(' · '), { icon: 'link' }));
  }
  if (nas.tempC != null) {
    rows.appendChild(kvRow(L('Temperature', '온도'),
      (dd) => (num(g(dd).tempC) != null ? g(dd).tempC + '°C' : DASH),
      { icon: 'bolt', tone: (dd) => (num(g(dd).tempC) >= 60 ? 'neg' : (num(g(dd).tempC) >= 50 ? 'warn' : '')) }));
  }
  // #508: fansOk/powerStatus 결측은 falsy 분기로 떨어뜨려 적색 경보('점검'+is-neg)를 내지 않고
  // #397(systemStatus 결측→mut)과 같은 계약으로 NA(DASH)+mut 중립 표기한다.
  rows.appendChild(kvRow(L('Fans', '팬'),
    (dd) => (g(dd).fansOk == null ? DASH : (g(dd).fansOk ? L('Normal', '정상') : L('Check', '점검'))),
    { icon: 'cycle', tone: (dd) => (g(dd).fansOk == null ? 'mut' : (g(dd).fansOk ? '' : 'neg')) }));
  rows.appendChild(kvRow(L('Power', '전원'), (dd) => lab(g(dd).powerStatus),
    { icon: 'bolt', tone: (dd) => (g(dd).powerStatus == null ? 'mut' : (g(dd).powerStatus === 'normal' ? '' : 'neg')) }));
  // #531: upgrade/upgradeAvailable 모두 결측이면 '최신'(거짓 안전)으로 굳지 않고
  // #508·#522와 같은 계약으로 NA(DASH)+mut 중립 표기한다. 명시적 false(=업데이트 없음 확인)만 '최신'.
  const nasUpd = (dd) => {
    const n = g(dd), u = n.upgrade, a = n.upgradeAvailable;
    if (u || a) return true;
    return (u == null && a == null) ? null : false;
  };
  rows.appendChild(kvRow(L('DSM update', 'DSM 업데이트'),
    (dd) => (nasUpd(dd) == null ? DASH : (nasUpd(dd) ? L('Available', '있음') : L('Up to date', '최신'))),
    { icon: '패치', tone: (dd) => (nasUpd(dd) == null ? 'mut' : (nasUpd(dd) ? 'warn' : '')) }));

  const volBox = E('div', { class: 'sc-dtl-sub' });
  const vols = arr(nas.volumes);
  if (vols.length) {
    volBox.appendChild(E('div', { class: 'sc-dtl-subtitle', text: L('Volumes', '볼륨') }));
    vols.forEach((_, i) => {
      const gv = (dd) => arr(g(dd).volumes)[i] || {};
      const row = barRow('',
        (dd) => gv(dd).pct,
        (dd) => nasVolText(gv(dd)),
        (dd) => _usageTone(gv(dd).pct),
        { usage: true });
      const nameNode = row.querySelector('.sc-dtl-bar-k');
      if (nameNode) {
        nameNode.classList.add('u-mono');
        P((dd) => setText(nameNode, gv(dd).name || ''));
      }
      volBox.appendChild(row);
    });
  }

  const diskBox = E('div', { class: 'sc-dtl-sub' });
  const disks = arr(nas.disks);
  if (disks.length) {
    diskBox.appendChild(E('div', { class: 'sc-dtl-subtitle', text: L('Disks', '디스크') }));
    disks.forEach((_, i) => {
      const gd = (dd) => arr(g(dd).disks)[i] || {};
      const dot = E('span', { class: 'u-dot' });
      const nm = E('span', { class: 'u-mono' });
      const md = E('span', { class: 'u-muted' });
      const st = E('span', {});
      const row = E('div', { class: 'sc-dtl-kv' }, [
        E('span', { class: 'u-row' }, [dot, nm, md]), st,
      ]);
      P((dd) => {
        const x = gd(dd);
        // #522: ok 결측은 falsy 분기로 떨어뜨려 적색 경보를 내지 않고
        // #508(팬·전원 행)과 같은 계약으로 NA(중립 도트+u-muted 텍스트) 표기한다.
        setCls(dot, 'u-dot', x.ok == null ? '' : (x.ok ? 'is-pos' : 'is-neg'));
        setText(nm, x.name || '');
        setText(md, x.model || '');
        setText(st, lab(x.status) + (x.tempC != null ? ' · ' + x.tempC + '°C' : ''));
        setCls(st, '', x.ok == null ? 'u-muted' : (x.ok ? 'is-pos' : 'is-neg'));
      });
      diskBox.appendChild(row);
    });
  }

  const raidBox = E('div', { class: 'sc-dtl-sub' });
  const raids = arr(nas.raid);
  if (raids.length) {
    raidBox.appendChild(E('div', { class: 'sc-dtl-subtitle', text: L('Storage pool / RAID', '스토리지 풀 / RAID') }));
    raids.forEach((_, i) => {
      const gr = (dd) => arr(g(dd).raid)[i] || {};
      const dot = E('span', { class: 'u-dot' });
      const nm = E('span', { class: 'u-mono' });
      const st = E('span', {});
      const row = E('div', { class: 'sc-dtl-kv' }, [E('span', { class: 'u-row' }, [dot, nm]), st]);
      P((dd) => {
        const x = gr(dd);
        // #522: ok 결측은 디스크 행과 같은 계약 — 중립 도트 + u-muted 텍스트(NA).
        setCls(dot, 'u-dot', x.ok == null ? '' : (x.ok ? 'is-pos' : 'is-neg'));
        setText(nm, x.name || '');
        setText(st, lab(x.status));
        setCls(st, '', x.ok == null ? 'u-muted' : (x.ok ? 'is-pos' : 'is-neg'));
      });
      raidBox.appendChild(row);
    });
  }

  const c = cardBox('sc-dtl-c-nas');
  c.appendChild(cardHead(L('Storage system', '스토리지 시스템'), badge));
  c.appendChild(E('div', { class: 'card-body' }, [rows, volBox, diskBox, raidBox]));
  P((dd) => {
    const x = g(dd);
    // #397: systemStatus 결측은 실패(red)가 아니라 NA(mut) — is-neg 는 명시적 비정상 보고만.
    // Health 타일(compute.js)은 같은 결측을 pos 로 봐 한 화면에서 녹/적으로 갈렸다.
    const st = x.systemStatus;
    setText(badge, lab(st));
    setCls(badge, 'u-badge', st ? (st === 'normal' ? 'is-pos' : 'is-neg') : 'u-muted');
  });
  return c;
}

function nasVmCard(d) {
  const cnt = E('span', { class: 'u-mono card-sub' });
  const list = E('div', { class: 'sc-dtl-alist' });
  arr(d.nasVms).forEach((_, i) => {
    const get = (dd) => arr(dd.nasVms)[i] || {};
    const nm = E('span', { class: 'u-mono u-flex u-nowrap' });
    const ip = E('span', { class: 'u-badge is-mono' });
    const vcpu = E('span', { class: 'u-mono u-muted' });
    const disk = E('span', { class: 'u-mono u-muted' });
    const st = E('span', { class: 'u-badge' });
    const row = E('div', { class: 'u-row sc-dtl-nvmrow' }, [
      E('span', { class: 'sc-dtl-kico' }, [ico('box', 15)]), nm, ip, vcpu, disk, st,
    ]);
    P((dd) => {
      const v = get(dd);
      setText(nm, v.name || DASH);
      show(ip, !!v.ip); setText(ip, v.ip || '');
      show(vcpu, v.vcpu != null); setText(vcpu, v.vcpu != null ? v.vcpu + ' vCPU' : '');
      show(disk, v.diskGB != null); setText(disk, v.diskGB != null ? v.diskGB + ' GB' : '');
      setText(st, v.stLabel || DASH);
      setCls(st, 'u-badge', toneCls(v.stTone));
    });
    list.appendChild(row);
  });
  const c = cardBox('sc-dtl-c-nasvm');
  c.appendChild(cardHead(L('Virtual Machines', '가상 머신'), cnt));
  c.appendChild(E('div', { class: 'card-body' }, [list]));
  P((dd) => setText(cnt, arr(dd.nasVms).length + ' VM'));
  return c;
}

/**
 * 소모품 게이지 채움 계약(#276) — pctText 와 짝을 맞춘다.
 * pct 결측(비수치)이나 음수 센티널은 텍스트가 DASH 인데 게이지가 100% 로 채워지는
 * 자기모순이 있었다 → 결측 음수는 비움(null, barRow is-empty). 단 -3 은 'OK(있음)'
 * 센티널이라 100 유지(트레이의 '용지 있음' 표기와 같은 의미). 소진(0)은 유효값.
 * #539: null 도 결측이다 — Number(null)===0 이라 num 만 거치면 0%(소진)으로 둔갑한다.
 */
export function supplyGaugeN(p) {
  if (p == null) return null;
  const n = num(p);
  if (n == null) return null;
  if (n < 0) return n === -3 ? 100 : null;
  return n;
}

/* ── PrinterCard ─────────────────────────────────────────────────────── */
// 토너/소모품 분류(프린터 관례 색상명) — printerCard 의 행 생성과 sigOf 의 구조 비트(#396)가
// 같은 분류를 써야 한다. 분류가 갈라지면 sig 가 행 존재를 오판해 재생성이 빠진다.
function isToner(n) { return /cyan|magenta|yellow|black/i.test(String(n || '')); }

function printerCard(d) {
  const pr = d.printer || {};
  const badge = E('span', { class: 'u-badge' });
  const info = E('div', { class: 'u-row sc-dtl-prinfo' });
  const errs = E('div', { class: 'u-row sc-dtl-prerrs' });
  const clean = (n) => String(n || '').split(' S/N:')[0].trim();
  const pctText = (p) => {
    // #539: null 도 결측 — Number(null)===0 이라 num 만으로는 '0%' 거짓 소진 표기가 된다
    if (p == null) return DASH;
    const n = num(p);
    // pct 결측 시 'undefined%' 대신 대시(결측=게이지 비움, barRow is-empty 와 일치)
    if (n == null) return DASH;
    return n < 0 ? (n === -3 ? 'OK' : DASH) : n + '%';
  };
  const supplies = arr(pr.supplies);
  const gp = (dd) => dd.printer || {};

  const pages = E('span', {});
  const serial = E('span', {});
  const cnts = E('span', {});
  const modelEl = E('span', {});
  const macEl = E('span', { class: 'u-mono' });
  info.appendChild(pages); info.appendChild(cnts); info.appendChild(modelEl);
  info.appendChild(macEl); info.appendChild(serial);

  const supBox = (title, filterFn) => {
    const box = E('div', { class: 'sc-dtl-sub' });
    const items = supplies.map((s, i) => [s, i]).filter(([s]) => filterFn(s.name));
    if (!items.length) return null;
    box.appendChild(E('div', { class: 'sc-dtl-subtitle', text: title }));
    items.forEach(([s, idx]) => {
      const gs = (dd) => arr(gp(dd).supplies)[idx] || {};
      const row = barRow(clean(s.name),
        (dd) => supplyGaugeN(gs(dd).pct),
        (dd) => pctText(gs(dd).pct),
        (dd) => {
          const p = gs(dd).pct;
          // #539: pct 결측(null/undefined)은 NA 중립 — null>=0 이 true 라 'neg'(소진)
          // 적색 거짓 경보로 떨어지던 것을 막는다. -3(OK 센티널) 등 음수는 기존 'pos' 유지.
          if (p == null) return '';
          if (p >= 0 && p <= 10) return 'neg';
          if (p >= 0 && p <= 25) return 'warn';
          return 'pos';
        });
      // 토너 게이지는 해당 토너 실색으로(프린터 관례). 임계(warn/neg)는 신호색이 우선.
      const nm = String(s.name || '').toLowerCase();
      const tcls = nm.includes('cyan') ? 'c' : nm.includes('magenta') ? 'm'
        : nm.includes('yellow') ? 'y' : nm.includes('black') ? 'k' : '';
      if (tcls) row.classList.add('sc-dtl-toner--' + tcls);
      // SyncThru: 교체 후 인쇄 매수 툴팁.
      const ckey = { c: 'cyan', m: 'magenta', y: 'yellow', k: 'black' }[tcls];
      const cnt = ckey && pr.tonerCnt ? pr.tonerCnt[ckey] : null;
      if (cnt != null) row.title = L('Printed since replacement: ', '교체 후 인쇄: ') + Number(cnt).toLocaleString() + L(' pages', '매');
      box.appendChild(row);
    });
    return box;
  };
  const toners = supBox(L('Toner', '토너'), isToner);
  const consum = supBox(L('Consumables', '소모품'), (n) => !isToner(n));

  const trayBox = E('div', { class: 'sc-dtl-sub' });
  const trays = arr(pr.trays);
  if (trays.length) {
    trayBox.appendChild(E('div', { class: 'sc-dtl-subtitle', text: L('Paper trays', '용지함') }));
    trays.forEach((_, i) => {
      const gt = (dd) => arr(gp(dd).trays)[i] || {};
      const nm = E('span', { class: 'u-mono' });
      const lv = E('span', { class: 'u-muted' });
      P((dd) => {
        const t = gt(dd);
        setText(nm, t.name || '');
        setText(lv, t.level == null ? DASH
          : (t.level === -3 ? L('Paper present', '용지 있음')
            : (t.level < 0 ? DASH : (t.level + (t.max ? ' / ' + t.max : '')))));
      });
      trayBox.appendChild(E('div', { class: 'sc-dtl-kv' }, [nm, lv]));
    });
  }

  const c = cardBox('sc-dtl-c-printer');
  c.appendChild(cardHead(L('Printer', '프린터'), badge));
  c.appendChild(E('div', { class: 'card-body' }, [info, errs, toners, consum, trayBox]));

  let errSig = '';
  P((dd) => {
    const p = gp(dd);
    const errList = arr(p.errors);
    const ok = errList.length === 0;
    const map = { idle: L('Idle', '대기'), printing: L('Printing', '인쇄 중'), warmup: L('Warming up', '예열 중') };
    // SyncThru 웹 상태 텍스트(Sleeping 등)가 있으면 그게 더 구체적 — 우선.
    setText(badge, p.statusText || map[p.status] || p.status || DASH);
    setCls(badge, 'u-badge', ok ? 'is-pos' : 'is-neg');
    show(pages, p.pages != null);
    setText(pages, p.pages != null ? (L('Pages', '누적 페이지') + ' ' + Number(p.pages).toLocaleString()) : '');
    show(cnts, p.monoTotal != null || p.colorTotal != null);
    setText(cnts, (p.monoTotal != null || p.colorTotal != null)
      ? (L('Mono ', '흑백 ') + Number(p.monoTotal || 0).toLocaleString()
        + L(' · Color ', ' · 컬러 ') + Number(p.colorTotal || 0).toLocaleString()) : '');
    show(modelEl, !!(p.webModel || p.productNum));
    setText(modelEl, [p.webModel, p.productNum].filter(Boolean).join(' · '));
    show(macEl, !!p.mac);
    setText(macEl, p.mac ? ('MAC ' + p.mac) : '');
    show(serial, !!p.serial);
    setText(serial, p.serial ? ('S/N ' + p.serial) : '');
    const s = errList.join('|');
    if (s !== errSig) {
      errSig = s;
      errs.textContent = '';
      errList.forEach((e) => errs.appendChild(E('span', { class: 'u-badge is-neg', text: String(e) })));
    }
    show(errs, errList.length > 0);
  });
  return c;
}

/* ── WinCard ─────────────────────────────────────────────────────────── */
function upFmt(sec) {
  const n = num(sec);
  if (n == null) return DASH;
  const dd = Math.floor(n / 86400), h = Math.floor((n % 86400) / 3600);
  return dd > 0 ? (dd + 'd ' + h + 'h') : (h + 'h');
}

function winCard(d) {
  const w = d.win || {};
  const gw = (dd) => dd.win || {};
  const rows = E('div', { class: 'sc-dtl-kvlist' });
  const add = (label, icon, get) => {
    const v = get(d);
    if (v == null || v === '') return;
    rows.appendChild(kvRow(label, get, { icon }));
  };
  add('OS', 'checklist', (dd) => (gw(dd).os ? gw(dd).os + (gw(dd).build ? ' · build ' + gw(dd).build : '') : ''));
  add(L('Hostname', '호스트명'), 'bookmark', (dd) => (gw(dd).host ? gw(dd).host + (gw(dd).domain ? ' · ' + gw(dd).domain : '') : ''));
  add(L('Hardware', '하드웨어'), 'ops', (dd) => [gw(dd).make, gw(dd).model].filter(Boolean).join(' '));
  add(L('Serial', '시리얼'), 'bookmark', (dd) => gw(dd).serial || '');
  add(L('CPU cores', 'CPU 코어'), 'ops', (dd) => (gw(dd).cores ? gw(dd).cores + L(' cores', ' 코어') : ''));
  add(L('Memory', '메모리'), 'ssd', (dd) => (gw(dd).memTotMB ? (Math.round(gw(dd).memTotMB / 1024) + ' GB · ' + (gw(dd).memPct != null ? gw(dd).memPct + '%' : '')) : ''));
  add(L('Uptime', '가동시간'), 'clock', (dd) => (gw(dd).uptimeSec ? upFmt(gw(dd).uptimeSec) : ''));
  add(L('Running services', '실행 서비스'), 'ops', (dd) => (gw(dd).svcRunning ? String(gw(dd).svcRunning) : ''));
  add(L('Latest hotfix', '최신 핫픽스'), '패치', (dd) => (gw(dd).lastHotfix ? gw(dd).lastHotfix + (gw(dd).lastHotfixOn ? ' · ' + gw(dd).lastHotfixOn : '') : ''));

  const diskBox = E('div', { class: 'sc-dtl-sub' });
  const disks = arr(w.disks);
  if (disks.length) {
    diskBox.appendChild(E('div', { class: 'sc-dtl-subtitle', text: L('Disks', '디스크') }));
    disks.forEach((_, i) => {
      const gd = (dd) => arr(gw(dd).disks)[i] || {};
      const row = barRow('',
        (dd) => gd(dd).pct,
        (dd) => {
          const x = gd(dd);
          const used = (num(x.sizeGB) != null && num(x.freeGB) != null) ? (x.sizeGB - x.freeGB).toFixed(1) : DASH;
          return used + ' / ' + (x.sizeGB != null ? x.sizeGB : DASH) + ' GB · ' + (x.pct != null ? x.pct + '%' : DASH);
        },
        (dd) => _usageTone(gd(dd).pct),
        { usage: true });
      const k = row.querySelector('.sc-dtl-bar-k');
      if (k) k.classList.add('u-mono');
      P((dd) => setText(k, gd(dd).drive || ''));
      diskBox.appendChild(row);
    });
  }

  const c = cardBox('sc-dtl-c-win');
  c.appendChild(cardHead(L('System', '시스템'), E('span', { class: 'u-badge is-pos', text: 'WinRM' })));
  c.appendChild(E('div', { class: 'card-body' }, [rows, diskBox]));
  return c;
}

/* ── PiCard ──────────────────────────────────────────────────────────── */
function piCard(d) {
  const p = d.pi || {};
  const gpi = (dd) => dd.pi || {};
  const rows = E('div', { class: 'sc-dtl-kvlist' });
  const add = (label, icon, get, tone) => {
    const v = get(d);
    if (v == null || v === '') return;
    rows.appendChild(kvRow(label, get, { icon, tone }));
  };
  add(L('Model', '모델'), 'ops', (dd) => gpi(dd).model || '');
  add(L('Kernel', '커널'), 'ops', (dd) => gpi(dd).kernel || '');
  add('IP · MAC', 'link', (dd) => [dd.mgmt || '', gpi(dd).mac || ''].filter(Boolean).join(' · '));
  add(L('OS (SSH)', 'OS (SSH)'), 'checklist', (dd) => (gpi(dd).sshBanner ? String(gpi(dd).sshBanner).replace('SSH-2.0-', '') : ''));
  add(L('Memory', '메모리'), 'ssd', (dd) => (gpi(dd).memTotMB ? (Math.round(gpi(dd).memTotMB / 1024) + ' GB' + (gpi(dd).memPct != null ? ' · ' + gpi(dd).memPct + '%' : '')) : ''));
  add(L('SoC temp', 'SoC 온도'), 'bolt', (dd) => (num(gpi(dd).tempC) != null ? num(gpi(dd).tempC).toFixed(1) + '°C' : ''),
    (dd) => (num(gpi(dd).tempC) >= 75 ? 'neg' : (num(gpi(dd).tempC) >= 65 ? 'warn' : '')));
  add(L('Core voltage', '코어 전압'), 'bolt', (dd) => (gpi(dd).coreVolt ? gpi(dd).coreVolt + 'V' : ''));
  if (p.ssh) {
    rows.appendChild(kvRow(L('Throttle', '스로틀'), (dd) => {
      const x = gpi(dd);
      if (!x.throttled) return L('none', '없음');
      const t = [x.throttleUnderVolt ? L('under-voltage', '저전압') : null, x.throttleThermal ? L('thermal', '과열') : null].filter(Boolean).join(' · ');
      return t || L('throttled', '스로틀');
    }, { icon: 'warningCircle', tone: (dd) => (gpi(dd).throttled ? 'neg' : '') }));
  }
  add(L('Uptime', '가동시간'), 'clock', (dd) => (gpi(dd).uptimeSec ? upFmt(gpi(dd).uptimeSec) : ''));

  const hint = E('div', { class: 'u-muted sc-dtl-hint', text: L('Set SSH credentials to collect CPU, memory, temperature and throttle status.', 'SSH 자격증명을 설정하면 CPU·메모리·온도·스로틀 상태를 수집합니다.') });

  const c = cardBox('sc-dtl-c-pi');
  c.appendChild(cardHead(L('Board', '보드'), E('span', { class: 'u-badge is-pos', text: p.ssh ? 'SSH' : L('agentless', '에이전트리스') })));
  c.appendChild(E('div', { class: 'card-body' }, [rows, hint]));
  P((dd) => show(hint, !gpi(dd).ssh));
  return c;
}

/* ── PLC 카드 7종 ────────────────────────────────────────────────────── */
function plcControllerCard() {
  const badge = E('span', { class: 'u-badge' });
  const bico = E('span', { class: 'sc-dtl-bico' });
  const btx = E('span', {});
  badge.appendChild(bico); badge.appendChild(btx);
  const run = E('div', { class: 'u-mono sc-dtl-plcrun' });
  const sub = E('div', { class: 'u-muted sc-dtl-plcsub' });
  const rows = E('div', { class: 'sc-dtl-kvlist' }, [
    kvRow(L('Maker', '제조사'), (d) => (d.plcMain || {}).maker || DASH),
    kvRow(L('Model', '모델'), (d) => (d.plcMain || {}).model || DASH),
    kvRow('IP', (d) => (d.plcMain || {}).ip || DASH),
  ]);
  const c = cardBox('sc-dtl-c-plcmain');
  c.appendChild(cardHead(L('Controller (PLC)', '제어기 (PLC)'), badge));
  c.appendChild(E('div', { class: 'card-body' }, [run, sub, rows, ftFoot()]));
  P((d) => {
    const p = d.plcMain || {};
    setText(run, p.run || DASH);
    setCls(run, 'u-mono sc-dtl-plcrun', toneCls(p.runTone));
    setText(sub, p.sub || '');
    if (p.err) {
      setText(btx, p.errLabel || '');
      setCls(badge, 'u-badge', 'is-warn'); iconInto(bico, 'warningCircle', 12);
    } else if (p.obs) {
      setText(btx, p.obsLabel || '');
      setCls(badge, 'u-badge', 'is-warn'); iconInto(bico, 'infoCircle', 12);
    } else {
      setText(btx, d.syncLabel || '');
      setCls(badge, 'u-badge', toneCls(d.syncTone)); iconInto(bico, d.syncIcon || 'check', 12);
    }
  });
  return c;
}

function kvListCard(title, icon, getRows, n, opts) {
  const rows = E('div', { class: 'sc-dtl-kvlist' });
  for (let i = 0; i < n; i += 1) {
    const get = (d) => arr(getRows(d))[i] || {};
    const k = E('span', { class: 'sc-dtl-kv-k u-muted' });
    const v = E('span', { class: 'u-mono sc-dtl-kv-v' });
    P((d) => {
      const r = get(d);
      setText(k, r.k || '');
      setText(v, r.v || DASH);
      const tone = r.tone ? r.tone : (r.warn ? 'warn' : (r.dim ? 'mut' : ''));
      setCls(v, 'u-mono sc-dtl-kv-v', tone ? toneCls(tone) : '');
    });
    rows.appendChild(E('div', { class: 'sc-dtl-kv' }, [k, v]));
  }
  const c = cardBox((opts && opts.cls) || '');
  c.appendChild(cardHead(title, (opts && opts.right) || null, icon));
  c.appendChild(E('div', { class: 'card-body' }, [rows]));
  return c;
}

function plcClockCard() {
  const dot = E('span', { class: 'u-dot' });
  const tx = E('span', { class: 'sc-dtl-plcclock' });
  const c = cardBox('sc-dtl-c-plcclock');
  c.appendChild(cardHead(L('Clock (RTC)', '내장 시계 (RTC)'), null, 'clock'));
  c.appendChild(E('div', { class: 'card-body' }, [E('div', { class: 'u-row' }, [dot, tx])]));
  P((d) => {
    const k = d.plcClock || {};
    setCls(dot, 'u-dot', k.bad ? 'is-warn' : 'is-pos');
    setText(tx, k.text || '');
    setCls(tx, 'sc-dtl-plcclock', k.bad ? 'is-warn' : 'u-ink2');
    c.classList.toggle('is-warnbg', !!k.bad);
  });
  return c;
}

function plcDiagCard(d) {
  const g0 = d.plcDiag || {};
  const badge = E('span', { class: 'u-badge' });
  const bico = E('span', { class: 'sc-dtl-bico' });
  const btx = E('span', {});
  badge.appendChild(bico); badge.appendChild(btx);

  const list = E('div', { class: 'sc-dtl-kvlist' });
  arr(g0.modules).forEach((_, i) => {
    const get = (dd) => arr((dd.plcDiag || {}).modules)[i] || {};
    const dot = E('span', { class: 'u-dot' });
    const lab = E('span', {});
    const code = E('span', { class: 'u-mono sc-dtl-kv-v' });
    P((dd) => {
      const mo = get(dd);
      setCls(dot, 'u-dot', 'is-' + toneKey(mo.tone || 'pos'));
      setText(lab, mo.label || '');
      setText(code, (mo.code || '') + (mo.sevLabel ? ' · ' + mo.sevLabel : ''));
      setCls(code, 'u-mono sc-dtl-kv-v', toneCls(mo.tone));
    });
    list.appendChild(E('div', { class: 'sc-dtl-kv' }, [E('span', { class: 'u-row u-ink2' }, [dot, lab]), code]));
  });
  arr(g0.extras).forEach((_, i) => {
    const get = (dd) => arr((dd.plcDiag || {}).extras)[i] || {};
    const k = E('span', { class: 'sc-dtl-kv-k u-muted' });
    const v = E('span', { class: 'u-mono sc-dtl-kv-v' });
    P((dd) => {
      const r = get(dd);
      setText(k, r.k || ''); setText(v, r.v || DASH);
      setCls(v, 'u-mono sc-dtl-kv-v', r.warn ? 'is-warn' : '');
    });
    list.appendChild(E('div', { class: 'sc-dtl-kv' }, [k, v]));
  });

  const foot = E('div', { class: 'sc-dtl-sub' });
  const sinceRow = E('div', { class: 'sc-dtl-kv' });
  const sinceK = E('span', { class: 'u-muted' });
  const sinceV = E('span', { class: 'u-mono is-warn' });
  sinceRow.appendChild(sinceK); sinceRow.appendChild(sinceV);
  const poRow = E('div', { class: 'sc-dtl-kv' });
  const poK = E('span', { class: 'u-muted' });
  const poV = E('span', { class: 'u-mono' });
  poRow.appendChild(poK); poRow.appendChild(poV);
  const histTitle = E('div', { class: 'u-muted sc-dtl-subtitle' });
  foot.appendChild(sinceRow); foot.appendChild(poRow); foot.appendChild(histTitle);
  arr(g0.history).forEach((_, i) => {
    const get = (dd) => arr((dd.plcDiag || {}).history)[i] || {};
    const at = E('span', { class: 'u-mono u-muted' });
    const lb = E('span', {});
    P((dd) => {
      const h = get(dd);
      setText(at, h.at || '');
      setText(lb, h.label || '');
      setCls(lb, '', h.warn ? 'is-warn' : 'is-pos');
    });
    foot.appendChild(E('div', { class: 'sc-dtl-kv' }, [at, lb]));
  });

  const c = cardBox('sc-dtl-c-plcdiag');
  c.appendChild(cardHead(L('Module diagnostics', '모듈 진단'), badge, 'crop'));
  c.appendChild(E('div', { class: 'card-body' }, [list, foot]));
  P((dd) => {
    const g = dd.plcDiag || {};
    const st = g.state || (g.err ? 'err' : 'ok');
    const map = {
      ok: { tone: 'pos', icon: 'check', label: L('All clear', '이상 없음') },
      warn: { tone: 'warn', icon: 'warningCircle', label: L('Attention', '주의') },
      err: { tone: 'neg', icon: 'warningCircle', label: L('Error', '오류 있음') },
    };
    const b = map[st] || map.ok;
    setText(btx, b.label);
    setCls(badge, 'u-badge', toneCls(b.tone));
    iconInto(bico, b.icon, 12);
    show(sinceRow, !!g.since);
    setText(sinceK, g.sinceLabel || ''); setText(sinceV, g.since || '');
    show(poRow, g.powerOn != null);
    setText(poK, L('Power-on count', '전원 투입 횟수'));
    setText(poV, g.powerOn != null ? (g.powerOn + (ko ? '회' : '')) : '');
    show(histTitle, arr(g.history).length > 0);
    setText(histTitle, g.historyLabel || '');
    show(foot, !!g.since || g.powerOn != null || arr(g.history).length > 0);
  });
  return c;
}

function plcSdCard(d) {
  const rows = arr((d.plcSd || {}).rows).length;
  const c = kvListCard(L('SD card', 'SD 카드'), 'ssd', (dd) => (dd.plcSd || {}).rows, rows, { cls: 'sc-dtl-c-plcsd' });
  P((dd) => c.classList.toggle('is-warnbg', !!(dd.plcSd || {}).bad));
  return c;
}

function plcNetCard(d) {
  const net = d.plcNet || {};
  const nSvc = arr(net.svc).length;
  const nCtr = arr(net.ctr).length;
  const rows = E('div', { class: 'sc-dtl-kvlist' });
  const mk = (getList, i) => {
    const get = (dd) => arr(getList(dd))[i] || {};
    const k = E('span', { class: 'sc-dtl-kv-k u-muted' });
    const v = E('span', { class: 'u-mono sc-dtl-kv-v' });
    P((dd) => {
      const r = get(dd);
      setText(k, r.k || ''); setText(v, r.v || DASH);
      const tone = r.tone ? r.tone : (r.warn ? 'warn' : '');
      setCls(v, 'u-mono sc-dtl-kv-v', tone ? toneCls(tone) : '');
    });
    return E('div', { class: 'sc-dtl-kv' }, [k, v]);
  };
  for (let i = 0; i < nSvc; i += 1) rows.appendChild(mk((dd) => (dd.plcNet || {}).svc, i));
  for (let i = 0; i < nCtr; i += 1) rows.appendChild(mk((dd) => (dd.plcNet || {}).ctr, i));
  const c = cardBox('sc-dtl-c-plcnet');
  c.appendChild(cardHead(L('Network health', '네트워크 헬스'), null, 'link'));
  c.appendChild(E('div', { class: 'card-body' }, [rows]));
  return c;
}

function plcVarsCard(d) {
  const grid = E('div', { class: 'sc-dtl-vargrid' });
  arr(d.plcVars).forEach((_, i) => {
    const get = (dd) => arr(dd.plcVars)[i] || {};
    const lab = E('span', { class: 'u-muted' });
    const nm = E('span', { class: 'u-mono sc-dtl-varname' });
    const val = E('span', { class: 'u-mono sc-dtl-varval' });
    const unit = E('span', { class: 'u-muted sc-dtl-varunit' });
    const box = E('div', { class: 'sc-dtl-var' }, [
      E('div', { class: 'sc-dtl-varlab' }, [lab, nm]),
      E('div', {}, [val, unit]),
    ]);
    P((dd) => {
      const v = get(dd);
      setText(lab, v.label || '');
      setText(nm, v.label !== v.name ? v.name : '');
      show(nm, v.label !== v.name);
      setText(val, v.value == null ? DASH : v.value);
      setText(unit, v.unit || '');
      show(unit, !!v.unit);
    });
    grid.appendChild(box);
  });
  const c = cardBox('sc-dtl-c-plcvars');
  c.appendChild(cardHead(L('Process variables', '공정 변수'), null, 'db'));
  c.appendChild(E('div', { class: 'card-body' }, [grid]));
  return c;
}

/* ── variant별 카드 배치 (§5.4) ─────────────────────────────────────── */
function g2(a, b) { return E('div', { class: 'sc-dtl-g2' }, [a, b].filter(Boolean)); }
function g11(a, b) { return E('div', { class: 'sc-dtl-g11' }, [a, b].filter(Boolean)); }
function stack(list) { return E('div', { class: 'sc-dtl-stack' }, list.filter(Boolean)); }

function buildCards(d) {
  const v = d.variant;
  const out = E('div', { class: 'sc-dtl-cards' });

  if (v === 'plc') {
    out.appendChild(g11(plcControllerCard(), stack([
      arr(d.plcComm).length ? kvListCard(L('Communication', '통신'), 'link', (dd) => dd.plcComm, arr(d.plcComm).length, { cls: 'sc-dtl-c-plccomm' }) : null,
      d.plcClock ? plcClockCard() : null,
    ])));
    if (d.plcDiag || d.plcNet || d.plcSd) {
      out.appendChild(g11(
        d.plcDiag ? plcDiagCard(d) : E('div', {}),
        stack([d.plcNet ? plcNetCard(d) : null, d.plcSd ? plcSdCard(d) : null]),
      ));
    }
    if (arr(d.plcVars).length) out.appendChild(plcVarsCard(d));
    out.appendChild(alertsCard(d));
    return out;
  }

  if (v === 'pi') {
    out.appendChild(g2(piCard(d), chartCard(d)));
    out.appendChild(alertsCard(d));
    return out;
  }

  if (v === 'win') {
    out.appendChild(g2(winCard(d), chartCard(d)));
    out.appendChild(alertsCard(d));
    return out;
  }

  if (v === 'nas') {
    out.appendChild(g2(nasCard(d), stack([
      arr(d.nasVms).length ? nasVmCard(d) : null,
      alertsCard(d),
    ])));
    return out;
  }

  if (v === 'srv' && d.printer) {
    out.appendChild(g2(printerCard(d), alertsCard(d)));
    return out;
  }

  if (v === 'srv') {
    out.appendChild(g2(stack([serverCard(d), d.bmc ? bmcCard() : null]), chartCard(d)));
    // Proxmox 게스트 VM 이 생기면 everRun 과 같은 VM 테이블로 나열.
    if (arr(d.procs).length) out.appendChild(vmTable(d));
    out.appendChild(g2(hwCard(d), alertsCard(d)));
    return out;
  }

  if (v === 'fts') {
    out.appendChild(g2(enclosureCard(d), chartCard(d)));
    out.appendChild(g2(hwCard(d), alertsCard(d)));
    return out;
  }

  // everRun 계열(기본)
  out.appendChild(pairCard());
  // ztC Endurance — 단일 2U 섀시 구성 카드(IP 플랜 11 + 서브시스템 이중화)
  if (d.endurance) out.appendChild(enduranceCard());
  // Endurance 는 가상머신 레이어가 없다(베어메탈 OS) — VM 테이블을 달지 않는다.
  out.appendChild(g2(chartCard(d), d.endurance ? null : vmTable(d)));
  if (d.license && d.license.has) out.appendChild(licenseCard());
  out.appendChild(g2(hwCard(d), alertsCard(d)));
  if (d.control) out.appendChild(controlsCard(d));
  if (arr(d.trapsList).length) out.appendChild(trapsCard(d));
  return out;
}

/* ── EnduranceCard (END) ─────────────────────────────────────────────── */
/* ztC Endurance 단일 2U 섀시 구성 — A/B 는 물리 모듈 식별자, Active/Standby 는 현재
 * 역할이다. everRun 용어(node0/node1·미러·Checkpoint)는 쓰지 않는다.
 *  - 역할 행: 모듈별 현재 역할 · 실행 OS · 부팅 디바이스
 *  - IP 플랜: BMC 4 + Standby OS 4 + Management UI 2 + Windows Host 1 = 11
 *    (Gateway/DNS 는 설정값으로 11개에 포함하지 않음). 상태는 색+텍스트 병행, ms 미표기.
 *  - 서브시스템: Compute=Active/Standby(Smart Exchange), Storage/I/O/PSU=Active/Active. */
function enduranceCard() {
  const rolesBox = E('div', { class: 'sc-dtl-endu-roles' });
  const ipBody = E('div', { class: 'sc-dtl-endu-ips' });
  const ipSum = E('div', { class: 'u-muted sc-dtl-endu-sum' });
  const subBox = E('div', { class: 'sc-dtl-endu-subs' });
  const foot = E('div', { class: 'u-muted sc-dtl-endu-foot' });

  const c = cardBox('sc-dtl-c-endu');
  c.appendChild(cardHead(L('ztC Endurance layout', 'ztC Endurance 구성'), null, 'building'));
  c.appendChild(E('div', { class: 'card-body' }, [
    rolesBox,
    E('div', { class: 'sc-dtl-endu-sect', text: L('IP plan', 'IP 플랜') }),
    ipBody, ipSum,
    E('div', { class: 'sc-dtl-endu-sect', text: L('Subsystem redundancy', '서브시스템 이중화') }),
    subBox, foot,
  ]));

  let sig = '';
  P((d) => {
    const e = (d && d.endurance) || {};
    const nodes = arr(d && d.nodes);
    const reach = e.reach || {};
    const rstate = (rk) => (reach[rk] || {}).state || '';

    /* -- IP 플랜 11개 -- */
    const rows = [];
    // 모듈 문자는 폴리가 보내는 module 필드가 정본 — 이름 끝글자 추측은 실장비 명명이
    // 다르면 BMC/Standby 그룹을 조용히 오표기한다(셀프 리뷰 🟡 정정). 폴백은 이름 접미.
    const letter = (n) => ((n && n.module) || (String((n && n.name) || '').slice(-1) === 'B' ? 'B' : 'A'));
    const pushPair = (mod, iface0, ip0, iface1, ip1, rk) => {
      if (ip0) rows.push({ grp: mod, iface: iface0, ip: ip0, st: rstate(rk) });
      if (ip1) rows.push({ grp: mod, iface: iface1, ip: ip1, st: rstate(rk) });
    };
    let bmcN = 0, stbN = 0, mgmN = 0, winN = 0;
    nodes.forEach((n) => {
      if (!n) return;
      const lt = letter(n);
      if (n.bmc) {
        const b4 = rows.length;
        pushPair('BMC ' + lt, 'eth0', n.bmc.eth0, 'eth1', n.bmc.eth1, 'bmc' + lt);
        bmcN += rows.length - b4;
      }
      if (n.standbyNic) {
        const b4 = rows.length;
        pushPair('Standby OS ' + lt, 'eno1', n.standbyNic.eno1, 'eno2', n.standbyNic.eno2, 'stby' + lt);
        stbN += rows.length - b4;
      }
    });
    const mgmt = arr(e.managementIPs);
    mgmt.forEach((ip, i) => {
      if (!ip) return;
      rows.push({ grp: 'Management UI ' + (i + 1), iface: '', ip, st: rstate('mgmt' + (i + 1)) });
      mgmN++;
    });
    if (e.windowsHost) {
      rows.push({ grp: 'Windows Host', iface: '', ip: e.windowsHost, st: rstate('windows') });
      winN++;
    }

    const subs = [
      { label: L('Compute', '컴퓨트') + ' A/B', o: e.compute, desc: e.failover || '' },
      { label: 'Storage A/B', o: e.storage, desc: (e.storage && e.storage.redundancy) || '' },
      { label: 'I/O A/B', o: e.io, desc: (e.io && e.io.redundancy) || '' },
      { label: 'PSU A/B', o: e.psu, desc: (e.psu && e.psu.note) || '' },
    ];
    const roleRows = nodes.filter(Boolean).map((n) => [n.name, n.badge, n.badgeTone, n.osName, n.bootDevice]);
    const nsig = JSON.stringify([roleRows, rows, subs.map((x) => [(x.o && x.o.mode) || '', x.desc]),
      e.chassis || '', e.midplane || '', langSigLang()]);
    if (nsig === sig) return;
    sig = nsig;

    /* -- 역할 행: 모듈(물리)과 역할(현재)을 분리 표기 -- */
    rolesBox.textContent = '';
    nodes.forEach((n) => {
      if (!n) return;
      rolesBox.appendChild(E('div', { class: 'sc-dtl-endu-role' }, [
        E('span', { class: 'u-mono sc-dtl-nname', text: n.name || '' }),
        E('span', { class: 'u-badge ' + toneCls(n.badgeTone), text: n.badge || '' }),
        E('span', { class: 'u-muted sc-dtl-endu-rd', text: [n.osName, n.bootDevice].filter(Boolean).join(' · ') }),
      ]));
    });

    ipBody.textContent = '';
    rows.forEach((r) => {
      const tone = r.st === 'ok' ? 'pos' : (r.st === 'slow' ? 'warn' : 'mut');
      const stTxt = r.st === 'ok' ? L('OK', '정상') : (r.st === 'slow' ? L('slow', '느림') : DASH);
      ipBody.appendChild(E('div', { class: 'sc-dtl-endu-ip' }, [
        E('span', { class: 'sc-dtl-endu-g', text: r.grp }),
        E('span', { class: 'u-muted u-mono', text: r.iface || '·' }),
        E('span', { class: 'u-mono sc-dtl-endu-a', text: r.ip }),
        E('span', { class: 'u-row sc-dtl-endu-s' }, [
          E('span', { class: 'u-dot is-' + tone }),
          E('span', { class: 'u-muted', text: stTxt }),
        ]),
      ]));
    });
    setText(ipSum, 'BMC ' + bmcN + ' + Standby OS ' + stbN + ' + Management UI ' + mgmN
      + ' + Windows Host ' + winN + ' = ' + rows.length
      + L(' — Gateway/DNS are settings, not counted in the plan', ' — Gateway/DNS 는 설정값으로 IP 플랜에 포함하지 않음'));

    subBox.textContent = '';
    subs.forEach((x) => {
      const mode = (x.o && x.o.mode) || '';
      subBox.appendChild(E('div', { class: 'sc-dtl-endu-sub' }, [
        E('span', { class: 'sc-dtl-endu-g', text: x.label }),
        E('span', { class: 'u-badge u-mono', text: mode ? mode.toUpperCase() : DASH }),
        E('span', { class: 'u-muted', text: x.desc }),
      ]));
    });
    setText(foot, [e.chassis, e.midplane].filter(Boolean).join(' · '));
  });
  return c;
}

/** sig 재빌드용 현재 언어 식별자 — L 이 클로저 언어를 굽기 때문에 언어 전환 시 행을 다시 구운다. */
function langSigLang() { return ko ? 'ko' : 'en'; }

/* ── 구조 시그니처 ───────────────────────────────────────────────────── */
function sigOf(d, lang) {
  return [
    lang, d.id, d.variant,
    arr(d.tiles).length, arr(d.notices).map((n) => n.strong || '').join(','),
    arr(d.nodes).length, arr(d.hw).length, arr(d.procs).length,
    arr(d.alertsList).length, arr(d.trapsList).length, arr(d.nasVms).length,
    arr(d.pcRows).length, arr(d.plcVars).length, arr(d.plcComm).length,
    (d.plcDiag ? arr(d.plcDiag.modules).length + '/' + arr(d.plcDiag.extras).length + '/' + arr(d.plcDiag.history).length : '-'),
    (d.plcSd ? arr(d.plcSd.rows).length : '-'),
    (d.plcNet ? arr(d.plcNet.svc).length + '/' + arr(d.plcNet.ctr).length : '-'),
    d.plcClock ? '1' : '0', d.plcMain ? '1' : '0',
    (d.license && d.license.has) ? '1' : '0',
    (d.resource && d.resource.has) ? '1' : '0',
    d.control ? arr(d.control.nodes).length + '/' + arr(d.control.vms).length : '-',
    d.bmc ? '1' : '0', d.eac ? '1' : '0', d.endurance ? '1' : '0',
    // #396: 조걶부 행은 빌드 시 필드 존재로만 만들어진다 — sig 에 존재 비트가 없으면 세션 중
    // 폴리가 필드를 새로 보고핸도 재생성이 안 돼 행이 영구 결측된다(§4.1 지문 함정 동류).
    d.printer
      ? arr(d.printer.supplies).map((s) => (isToner(s && s.name) ? 't' : 'c')).join('') + '/' + arr(d.printer.trays).length
      : '-',
    d.nas ? arr(d.nas.volumes).length + '/' + arr(d.nas.disks).length + '/' + arr(d.nas.raid).length
      + '/' + (d.nas.model ? 1 : 0) + ((d.nas.dsm || d.nas.dsmVersion) ? 1 : 0) + (d.nas.serial ? 1 : 0)
      + (arr(d.nas.lanPorts).length > 1 ? 1 : 0) + (d.nas.tempC != null ? 1 : 0) : '-',
    d.win ? arr(d.win.disks).length + '/' + [d.win.os, d.win.host, d.win.make || d.win.model,
      d.win.serial, d.win.cores, d.win.memTotMB, d.win.uptimeSec, d.win.svcRunning, d.win.lastHotfix,
    ].map((v) => (v ? 1 : 0)).join('') : '-',
    d.pi ? (d.pi.ssh ? '1' : '0') + '/' + [d.pi.model, d.pi.kernel, d.mgmt || d.pi.mac,
      d.pi.sshBanner, d.pi.memTotMB, num(d.pi.tempC) != null, d.pi.coreVolt, d.pi.uptimeSec,
    ].map((v) => (v ? 1 : 0)).join('') : '-',
    arr(d.nodes).filter((n) => n && (n.cpuN != null || n.memN != null)).length,
  ].join('|');
}

/* ── 확인 모달 ───────────────────────────────────────────────────────── */
// 모달 id 들 — 화면당 모달은 1개라 정적 id 로 label/for·aria-labelledby 를 연결한다(#43).
const MODAL_TITLE_ID = 'sc-dtl-modal-title';
const MODAL_TYPED_ID = 'sc-dtl-modal-typed';
// 메모 카드 제목 id — detail 화면은 동시에 1개라 정적 id 로 textarea aria-labelledby 를 연결한다(#492).
const NOTE_TITLE_ID = 'sc-dtl-note-title';
export function buildModal() {
  const title = E('div', { class: 'modal-title', id: MODAL_TITLE_ID });
  const conseq = E('p', { class: 'u-ink2 sc-dtl-conseq' });
  // label 은 형제로만 두면 프로그램적 연결이 없다 — for/id 로 입력과 묶는다(#43).
  const typedLab = E('label', { class: 'field-label', for: MODAL_TYPED_ID });
  const typedIn = E('input', { class: 'field-input u-mono', type: 'text', id: MODAL_TYPED_ID, 'data-dtl-typed': '', autocomplete: 'off' });
  const typedWrap = E('div', { class: 'field' }, [typedLab, typedIn]);
  const cancel = E('button', { class: 'btn btn--outline', type: 'button', 'data-dtl-cancel': '' });
  const runBtn = E('button', { class: 'btn btn--danger', type: 'button', 'data-dtl-run': '' });
  const closeX = E('button', { class: 'modal-close', type: 'button', 'data-dtl-cancel': '', 'aria-label': L('Close', '닫기') }, [ico('close', 16)]);
  // role=dialog 에 이름이 없어 스크린리더가 모달 제목을 읽지 못했다 — aria-labelledby 연결(#43).
  const modal = E('div', {
    class: 'modal modal--narrow', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': MODAL_TITLE_ID,
  }, [
    E('div', { class: 'modal-head' }, [title, closeX]),
    E('div', { class: 'modal-body' }, [conseq, typedWrap]),
    E('div', { class: 'modal-foot' }, [cancel, runBtn]),
  ]);
  const overlay = E('div', { class: 'modal-overlay u-hide', 'data-dtl-modal': '' }, [modal]);
  return { overlay, modal, title, conseq, typedLab, typedIn, typedWrap, cancel, runBtn, closeX };
}

// Tab 순환 트랩의 다음 포커스 계산(순수) — 모달이 떠 있는 동안 Tab/Shift+Tab 이 배경으로
// 빠지지 않고 모달 안을 순환한다(#43). 포커스가 목록 밖이면 끝으로 모셔 온다.
export function nextModalFocus(items, current, shiftKey) {
  if (!items || !items.length) return null;
  const idx = items.indexOf(current);
  if (idx < 0) return shiftKey ? items[items.length - 1] : items[0];
  return items[(idx + (shiftKey ? -1 : 1) + items.length) % items.length];
}

/**
 * 파괴적 제어의 결과 설명 — 확인 모달 본문.
 * 핵심은 **피어 건강도 인지**다: 페어의 다른 노드가 정상이면 "VM 은 계속 돈다",
 * 아니면 "VM 이 멈춘다"로 문구가 갈린다. 이 분기가 뒤집히면 운영자가 안심하고
 * 서비스를 내린다 — export 해서 게이트로 묶는다.
 */
export function consequenceOf(action, target, nodes) {
  const peer = arr(nodes).find((n) => n && n.name !== target);
  if (action === 'node-workon' || action === 'node-reboot' || action === 'node-shutdown') {
    if (!peer) return L('This is the only node — VMs will stop.', '단일 노드 — VM이 중지됩니다.');
    if (peer.down || peer.maint) {
      return L('Peer ' + peer.name + ' is not healthy — this would stop the VMs.',
        '피어 ' + peer.name + '가 정상 아님 — VM 중지 위험이 있습니다.');
    }
    return L('VMs keep running on ' + peer.name + '; ' + target + ' briefly runs simplex.',
      'VM은 ' + peer.name + '에서 계속 실행, ' + target + '는 잠시 단일노드(심플렉스).');
  }
  if (action === 'vm-poweroff') return L('Hard power-off — unsaved guest state is lost.', '강제 종료 — 게스트의 저장 안 된 상태는 유실됩니다.');
  if (action === 'vm-shutdown') return L('Graceful guest shutdown.', '게스트 정상 종료 요청.');
  if (action === 'node-workoff') return L('Bring the node back into service (triggers a resync).', '노드를 서비스로 복귀 (재동기화 시작).');
  if (action === 'node-recover') return L('Recover a failed node.', '실패한 노드를 복구합니다.');
  if (action === 'vm-poweron') return L('Start the VM.', 'VM을 시작합니다.');
  return '';
}

function openConfirm(action, target, label, destructive) {
  const d = lastDetail || {};
  confirmState = {
    action, target, label, destructive,
    // 모달을 연 시점의 장비 id — 같은 detail view 안의 뒤로가기는 destroy 없이 render 만
    // 돌아 모달이 잔존하므로, runConfirm 은 이 값과 현재 화면 장비를 대조해 오실행을 막는다(#438).
    devId: d.id || null,
    consequence: consequenceOf(action, target, (d.control || {}).nodes),
    // 모달을 연 트리거 — 닫힐 때 포커스를 돌려준다(#43, 키보드 사용자의 위치 상실 방지).
    triggerEl: (typeof document !== 'undefined' && document.activeElement) || null,
  };
  resultMsg = null;
  const m = fix.modal;
  setText(m.title, label + ' — ' + target);
  setText(m.conseq, confirmState.consequence);
  show(m.typedWrap, !!destructive);
  setText(m.typedLab, L('Type ' + target + ' to confirm', target + ' 를 입력하면 활성화'));
  m.typedIn.value = '';
  setText(m.cancel, L('Cancel', '취소'));
  setText(m.runBtn, L('Run', '실행'));
  // 닫기 X 접근명 — buildModal 이 구운 값은 init 시점 언어라, 전환 후엔 여기서 따라간다(#323).
  m.closeX.setAttribute('aria-label', L('Close', '닫기'));
  m.runBtn.disabled = !!destructive;
  show(m.overlay, true);
  // 모든 액션에서 포커스를 모달 안으로 옮긴다(#43) — 파괴적이면 확인 입력으로,
  // 비파괴는 입력이 없으므로 실행 버튼으로(기존엔 오버레이 뒤 트리거에 그대로였다).
  const focusTo = destructive ? m.typedIn : m.runBtn;
  try { focusTo.focus(); } catch (e) { /* noop */ }
}

/* 속성 선택자 값 이스케이프 — CSS.escape 가 없는 환경(Node 테스트 셰임)도 커버한다. */
function cssAttr(v) {
  const s = String(v == null ? '' : v);
  return (typeof CSS !== 'undefined' && CSS.escape) ? CSS.escape(s) : s.replace(/["\\]/g, '\\$&');
}

/**
 * 모달 닫힘 시 포커스 복귀 앵커를 고른다(#275).
 * 실행 경로는 setState 동기 렌더(store.js::setState → 구독자 렌더)로 카드가 재생성돼
 * 모달을 연 트리거가 문서에서 떨어져 있다 — 떨어진 노드의 focus() 는 무력이라
 * 포커스가 body 로 유실됐다. 취소/Esc 경로(재렌더 없음)는 트리거가 그대로 살아 있다.
 * 순서: 트리거(문서에 있을 때) → 같은 액션·대상의 새 버튼 → 화면 루트.
 */
export function focusReturnAnchor(trigger, root, action, target) {
  if (trigger && trigger.isConnected !== false) return trigger;
  if (root && typeof root.querySelector === 'function') {
    try {
      const btn = root.querySelector(
        '[data-dtl-act="' + cssAttr(action) + '"][data-dtl-target="' + cssAttr(target) + '"]');
      if (btn) return btn;
    } catch (e) { /* noop */ }
  }
  return root || null;
}

function closeConfirm() {
  const cs = confirmState;
  confirmState = null;
  show(fix.modal.overlay, false);
  // 포커스 복귀 — 모달을 연 트리거 버튼으로 돌려준다(#43). 단 실행 경로는 트리거가
  // 문서에서 떨어져 있을 수 있으므로 대체 앵커를 고른다(#275, focusReturnAnchor 참조).
  const to = focusReturnAnchor(cs && cs.triggerEl, rootEl, cs && cs.action, cs && cs.target);
  if (to && to === rootEl && rootEl && typeof rootEl.getAttribute === 'function'
    && rootEl.getAttribute('tabindex') == null) {
    // 섹션(화면 루트)은 원래 포커스 불가 — main#screen-root 의 tabindex=-1 과 같은
    // 프로그램적 포커스 전용 부여(탭 순서에는 들어가지 않는다).
    try { rootEl.setAttribute('tabindex', '-1'); } catch (e) { /* noop */ }
  }
  if (to && typeof to.focus === 'function') {
    try { to.focus(); } catch (e) { /* noop */ }
  }
}

function syncRunBtn() {
  if (!confirmState) return;
  const m = fix.modal;
  const need = confirmState.destructive;
  m.runBtn.disabled = busy || (need && m.typedIn.value.trim() !== confirmState.target);
  setText(m.runBtn, busy ? L('Running…', '실행 중…') : L('Run', '실행'));
}

/* 시뮬레이션 CRUD: fleet 로컬 변경으로 액션을 흉내낸다 */
function simulateAction(action, target) {
  const st = CTX.store.getState();
  const id = (lastDetail || {}).id;
  const fleet = arr(st.fleet);
  const idx = fleet.findIndex((s) => s && s.id === id);
  if (idx < 0) return { ok: false, msg: L('Device not found', '장비를 찾을 수 없습니다') };
  const dev = fleet[idx];
  const meta = Object.assign({}, dev.meta || {});
  const nodes = arr(meta.nodes).map((n) => Object.assign({}, n));
  const vms = arr(meta.vmList).map((v) => Object.assign({}, v));
  const snmp = arr(meta.snmp).map((s) => Object.assign({}, s));
  const n = nodes.find((x) => x.name === target);
  const vm = vms.find((x) => x.name === target);

  if (action === 'node-workon' && n) { n.standing = 'maintenance'; n.mode = 'maintenance'; }
  else if (action === 'node-workoff' && n) { n.standing = 'normal'; n.mode = 'production'; n.state = 'running'; }
  else if (action === 'node-reboot' && n) {
    // #541: mode 도 함께 리셋 — _nodeMaint(compute.js)는 standing+mode 둘 다 보기에
    // 점검 중 재부팅 후 mode='maintenance' 가 잔류하면 유령 '점검 중' 으로 고정된다(#532 동급).
    n.state = 'running'; n.standing = 'normal'; n.mode = 'production';
    // #551: at 은 epoch 초 계약 — 시뮬 폴 러(data.js)는 Math.floor(now/1000)로 기록하고
    // compute.js 는 at*1000 으로 읽는다. ms 로 기록하면 이벤트 정렬 키가 ~1.7e15 가 되어
    // 개요 '최근 이벤트' 최상단에 세션 종료까지 고정된다.
    const atSec = Math.floor(Date.now() / 1000);
    meta.lastReboot = { ip: n.ip || '', node: n.name, at: atSec, agoSecs: 0 };
    // agoSecs 노화 경로(data.js)는 snmp 노드의 rebooted_at 이 있을 때만 돈다 — 함께 심어
    // '재부팅 감지됨 · 방금' 이 세션이 끝날 때까지 식지 않는 가짜 신선도 신호가 되는 것을 막는다.
    const sn = snmp.find((s) => s && s.ip && s.ip === n.ip);
    if (sn) { sn.rebooted_at = atSec; sn.reboot_ago = 0; }
  } else if (action === 'node-shutdown' && n) {
    // #576: reboot(#541)와 동일 — mode 도 함께 리셋. _nodeMaint(compute.js)는 standing+mode 를
    // 모두 보기에 점검 중 종료 후 mode='maintenance' 가 잔류하면 멈춘 노드가 유령 '점검 중' 으로 고정된다.
    n.state = 'stopped'; n.standing = 'normal'; n.mode = 'production';
  }
  else if (action === 'node-recover' && n) { n.state = 'running'; n.standing = 'normal'; n.mode = 'production'; }
  else if (action === 'vm-poweron' && vm) { vm.state = 'running'; }
  else if ((action === 'vm-shutdown' || action === 'vm-poweroff') && vm) { vm.state = 'shutdown'; }
  else return { ok: false, msg: L('Target not found', '대상을 찾을 수 없습니다') };

  meta.nodes = nodes;
  meta.vmList = vms;
  meta.snmp = snmp;
  meta.vms = vms.length;
  meta.vmRunning = vms.filter((x) => x.state === 'running').length;

  const next = Object.assign({}, dev, { meta });
  const M = CTX.model || {};
  try {
    if (typeof M.deriveStatus === 'function') next.status = M.deriveStatus(nodes, snmp);
    if (typeof M.deriveSync === 'function') next.sync = M.deriveSync(nodes, next.status);
    if (typeof M.availN === 'function') next.availN = M.availN(next.status);
  } catch (e) { /* 파생 실패는 무시 */ }

  const nf = fleet.slice();
  nf[idx] = next;
  CTX.store.setState({ fleet: nf });
  return { ok: true, msg: L('Simulated: ', '시뮬레이션 실행: ') + action + ' ' + target };
}

/* #615: 제어 액션 POST. manage.js api() 전례 — serve.py 를 --token 으로 띄운 운영
 * 모드에서는 모든 쓰기 요청이 X-Serverdesk-Token 헤더 일치를 요구한다(_mutating_denied,
 * 불일치·누락 → 403). 토큰은 설정 화면에서 입력받아 setg.token 에 영속한다.
 * fetchImpl 주입 가능 — export 해서 게이트로 묶는다. */
export async function postAction(id, action, target, setg, fetchImpl) {
  const headers = { 'Content-Type': 'application/json' };
  const tok = (setg && typeof setg.token === 'string') ? setg.token : '';
  if (tok) headers['X-Serverdesk-Token'] = tok;
  const doFetch = fetchImpl || ((url, opts) => fetchTimeout(url, opts, 15000));
  return doFetch('/api/clusters/' + encodeURIComponent(id) + '/action', {
    method: 'POST',
    headers,
    body: JSON.stringify({ action, target }),
  });
}

async function runConfirm() {
  if (!confirmState || busy) return;
  const { action, target } = confirmState;
  // #438: 모달을 연 장비와 현재 화면 장비가 다를 때는 실행하지 않고 모달을 닫는다.
  // 같은 detail view 안의 뒤로가기는 destroy 없이 render 만 돌아 모달이 잔존하는데,
  // 종전엔 여기서 최신 lastDetail.id 를 써 엉뚱한 클러스터에 POST 가 나갔다(live 오실행).
  const id = confirmState.devId || (lastDetail || {}).id;
  if (confirmState.devId && lastDetail && confirmState.devId !== lastDetail.id) {
    closeConfirm();
    try {
      CTX.showToast(L('Device changed — action cancelled.', '장비가 바뀌어 실행을 취소했습니다.'));
    } catch (e) { /* noop */ }
    return;
  }
  busy = true;
  syncRunBtn();
  const st = CTX.store.getState();
  let res;
  if (st.source === 'live') {
    try {
      const r = await postAction(id, action, target, st.setg);
      let j = {};
      try { j = await r.json(); } catch (e) { j = {}; }
      res = r.ok ? { ok: true, msg: j.output || L('started', '실행 요청됨') }
        : { ok: false, msg: j.error || ('HTTP ' + r.status) };
    } catch (e) {
      res = { ok: false, msg: String((e && e.message) || e) };
    }
  } else {
    res = simulateAction(action, target);
  }
  busy = false;
  // #510: fetch(최대 15초) 대기 중 render-only 전환(뒤로가기)으로 현재 장비가 바뀌었으면
  // resultMsg 를 대입하지 않는다. render 의 리셋(#491)은 전환 시점에만 일어나, 완료 시
  // 재대입이 B 의 새 controlsCard P 클로저에 A 결과 배지를 띄운다.
  // #438 confirmState.devId 전례처럼 실행을 시작한 장비(id)와 현재 장비를 대조한다.
  // 토스트는 앱 전역 계약(실행 사실 고지)이라 불일치 시에도 그대로 남긴다.
  const sameDev = !!(lastDetail && lastDetail.id === id);
  if (sameDev) resultMsg = res;
  // await(live fetch 최대 15초) 사이에 화면을 이탈하면 destroy() 가 fix={}·rootEl=null 로
  // 만든다 — fix.modal 접근이 TypeError 가 되므로 모달 닫기·패치는 화면 생존 시에만(#321).
  // 결과 토스트는 앱 전역(#sd-live)이라 이탈 후에도 남긴다(유실 방지).
  if (rootEl) closeConfirm();
  try { CTX.showToast((res.ok ? '✓ ' : '✕ ') + res.msg); } catch (e) { /* noop */ }
  // 결과 배지를 즉시 반영 — resultMsg 는 같은 장비일 때만 대입됐으므로(#510),
  // 전환이 있었어도 이 패치는 B 카드에 A 배지를 붙이지 않는다.
  if (rootEl && lastDetail) patchCards.forEach((fn) => { try { fn(lastDetail); } catch (err) { /* noop */ } });
}

/* ── 빈 상태 ─────────────────────────────────────────────────────────── */
// #394: 라벨을 init 시 1회만 구우면 빈 상태에서 언어 전환을 추적하지 못한다(구 언어 영구 잔존).
// 라벨 굽기를 patch 로 분리해 render(빈 상태)마다 현재 언어로 다시 굽는다(#357 전례).
let emptyRef = null;
function patchEmptyBox() {
  if (!emptyRef) return;
  setText(emptyRef.title, L('No device selected', '선택된 장비가 없습니다'));
  setText(emptyRef.sub, L('Pick a device from the fleet to see its detail.', '목록에서 장비를 선택하면 상세가 표시됩니다.'));
}
function emptyBox() {
  const title = E('div', { class: 'empty-title' });
  const sub = E('div', { class: 'empty-sub' });
  emptyRef = { title, sub };
  patchEmptyBox();
  return E('div', { class: 'empty' }, [
    E('div', { class: 'empty-icon' }, [ico('box', 22)]),
    title,
    sub,
  ]);
}

/* ── 모듈 ────────────────────────────────────────────────────────────── */
export default {
  key: 'detail',
  title: { en: 'Server detail', ko: '서버 상세' },
  icon: 'ops',

  init(root, ctx) {
    rootEl = root;
    CTX = ctx;
    U = ctx.util || {};
    patchFixed = [];
    patchCards = [];
    sig = '';
    confirmState = null;
    busy = false;
    resultMsg = null;
    lastDetail = null;
    noteBoundId = null;
    ko = ((ctx.store && ctx.store.getState().lang) || 'ko') !== 'en';

    const wrap = E('div', { class: 'sc-dtl' });
    const hd = buildHeader();
    const tiles = E('div', { 'data-dtl-tiles': '' });
    const notices = E('div', { 'data-dtl-notices': '' });
    // ---- 장비 메모(인수인계) — serve.py /notes 공유. '이 경보는 벤더 확인 중' 같은
    // 운영자 노트를 장비에 붙인다. 입력 중인 텍스트를 렌더가 덮어쓰지 않게 포커스 가드를 둔다.
    // #492: 화면 유일의 자유 텍스트 입력에 프로그램적 라벨이 없었다 — 제목 span 을 id 로
    // 묶어 aria-labelledby 로 연결한다(#43 모달 입력의 label/for 전례와 같은 계약.
    // placeholder 는 값 입력 시 소실되므로 라벨 대체재가 아니다).
    const noteTa = E('textarea', { class: 'sc-dtl-noteta', 'data-dtl-note': '', rows: 3, maxlength: 1000, 'aria-labelledby': NOTE_TITLE_ID });
    const noteSave = E('button', { class: 'btn btn--outline btn--sm', type: 'button', 'data-dtl-note-save': '' });
    const noteClear = E('button', { class: 'btn btn--outline btn--sm', type: 'button', 'data-dtl-note-clear': '' });
    const noteMeta = E('span', { class: 'u-muted sc-dtl-notemeta' });
    const notesBox = E('section', { class: 'card sc-dtl-notes' }, [
      E('div', { class: 'card-head sc-dtl-notehead' }, [
        E('span', { class: 'card-title', 'data-f': 'noteTitle', id: NOTE_TITLE_ID }), noteMeta,
      ]),
      E('div', { class: 'card-body' }, [
        noteTa,
        E('div', { class: 'sc-dtl-noteactions' }, [noteSave, noteClear]),
      ]),
    ]);
    const cards = E('div', { 'data-dtl-cards': '' });
    const empty = E('div', { class: 'u-hide', 'data-dtl-empty': '' }, [emptyBox()]);
    wrap.appendChild(hd.bar);
    wrap.appendChild(hd.head);
    wrap.appendChild(tiles);
    wrap.appendChild(notices);
    wrap.appendChild(notesBox);
    wrap.appendChild(cards);
    wrap.appendChild(empty);
    root.appendChild(wrap);

    fix = {
      wrap, head: hd.head, bar: hd.bar, back: hd.back, edit: hd.edit,
      tiles, notices, cards, empty,
      notesBox, noteTa, noteSave, noteClear, noteMeta,
      maintBtn: hd.maintBtn, maintMenu: hd.maintMenu, maintWrap: hd.maintWrap,
      maintClearBtn: hd.maintClearBtn,
      modal: buildModal(),
    };
    root.appendChild(fix.modal.overlay);

    onClick = (ev) => {
      const t = ev.target;
      let el;
      // 점검 모드 메뉴 토글 + 바깥 클릭 닫기 — 시간 선택(maintSet)은 app.js 전역 위임이 처리하고,
      // 선택 후엔 여기서 닫는다(아래 maint-hours 분기).
      if (t.closest('[data-dtl-maint]')) { fix.maintMenu.hidden = !fix.maintMenu.hidden; return; }
      if (t.closest('[data-maint-hours]')) { fix.maintMenu.hidden = true; return; }
      if (!t.closest('.sc-dtl-maintwrap') && !fix.maintMenu.hidden) fix.maintMenu.hidden = true;
      if (t.closest('[data-dtl-note-save]')) {
        const id = (lastDetail || {}).id;
        // #473: textarea 의 값이 바인딩된 장비와 현재 장비가 일치할 때만 저장한다(오저장 방지).
        // render 의 재바인딩이 선행하므로 정상 경로에선 항상 일치하지만, 방어적으로 대조한다.
        if (id && noteBoundId === id) {
          const text = String(fix.noteTa.value || '').slice(0, 1000);
          CTX.store.setState((st) => {
            const next = Object.assign({}, st.notes);
            if (text.trim()) next[id] = { text, ts: new Date().toISOString(), by: 'console' };
            else delete next[id];
            return { notes: next };
          });
          try { CTX.showToast(L('Note saved', '메모를 저장했습니다')); } catch (e) { /* noop */ }
        }
        return;
      }
      if (t.closest('[data-dtl-note-clear]')) {
        const id = (lastDetail || {}).id;
        if (id) {
          fix.noteTa.value = '';
          CTX.store.setState((st) => {
            const next = Object.assign({}, st.notes);
            delete next[id];
            return { notes: next };
          });
        }
        return;
      }
      if ((el = t.closest('[data-dtl-edit]'))) {
        const id = (lastDetail || {}).id;
        try { CTX.store.setState({ editKey: id || null }); } catch (e) { /* noop */ }
        try { CTX.goView('manage'); } catch (e) { /* noop */ }
        return;
      }
      if (t.closest('[data-dtl-incidents]')) {
        // #509: '전체 인시던트 보기'는 경보 '카드' 탭으로 연다 — #486(overview.js)과 같은 패턴으로
        // 목적 탭을 명시 패치한다(직전 log/stats 탭의 stale alertView 로 착지하는 비대칭 방지).
        try { CTX.store.setState({ alertView: 'cards' }); } catch (e) { /* noop */ }
        try { CTX.goView('incidents'); } catch (e) { /* noop */ }
        return;
      }
      if ((el = t.closest('[data-dtl-act]'))) {
        openConfirm(el.getAttribute('data-dtl-act'), el.getAttribute('data-dtl-target') || '',
          el.getAttribute('data-dtl-label') || '', el.getAttribute('data-dtl-destructive') === '1');
        return;
      }
      if (t.closest('[data-dtl-cancel]')) { if (!busy) closeConfirm(); return; }
      if (t.closest('[data-dtl-run]')) { runConfirm(); return; }
      if (t === fix.modal.overlay && !busy) closeConfirm();
    };
    root.addEventListener('click', onClick);

    onInput = (ev) => { if (ev.target.closest('[data-dtl-typed]')) syncRunBtn(); };
    root.addEventListener('input', onInput);

    onKeydown = (ev) => {
      if (!confirmState) return;
      // IME 조합 중(isComposing)에는 아래 키들을 가로채지 않는다(#568) — 파괴적 액션의
      // 확인 문자열을 한글 IME 로 입력할 때 조합 확정 Enter 가 runConfirm 실행으로
      // 오인됐고, Escape 는 조합 취소 의도가 모달 닫힘으로 오인됐다(#560 전역 검색
      // 가드와 같은 계열의 입력 소비 존중).
      if (ev.isComposing) return;
      if (ev.key === 'Escape' && !busy) { closeConfirm(); return; }
      // Tab 순환 트랩(#43) — 모달이 떠 있는 동안 Tab/Shift+Tab 이 배경으로 빠지지 않고
      // 모달 안(닫기 X → 확인 입력(파괴적) → 취소 → 실행)을 순환한다. 비활성 실행 버튼은
      // 포커스 불가라 목록에서 뺀다(활성화되면 다음 Tab 부터 자동 합류).
      if (ev.key === 'Tab') {
        const m = fix.modal;
        const items = [m.closeX];
        if (confirmState.destructive) items.push(m.typedIn);
        items.push(m.cancel);
        if (!m.runBtn.disabled) items.push(m.runBtn);
        const next = nextModalFocus(items, document.activeElement, ev.shiftKey);
        if (next) {
          ev.preventDefault();
          try { next.focus(); } catch (e) { /* noop */ }
        }
        return;
      }
      if (ev.key === 'Enter' && ev.target && ev.target.closest && ev.target.closest('[data-dtl-typed]')) {
        if (!fix.modal.runBtn.disabled) runConfirm();
      }
    };
    window.addEventListener('keydown', onKeydown);
  },

  render(state, ctx) {
    if (!rootEl) return;
    CTX = ctx || CTX;
    // #357: 언어는 render 진입부에서 먼저 반영한다. 아래 점검 메뉴·메모 카드 라벨(L 호출)이
    // sig 변경 블록(ko 갱신)보다 앞서 구워지므로, 여기서 갱신하지 않으면 setState({lang}) 직후
    // 첫 render 가 구 언어로 굽고 refresh OFF+paused 조합에서는 영구 잔존한다.
    const lang = (state && state.lang) === 'en' ? 'en' : 'ko';
    ko = lang !== 'en';
    const model = (ctx && ctx.model) || {};
    let d = null;
    try { if (typeof model.buildDetail === 'function') d = model.buildDetail(state); } catch (e) { d = null; }

    const nothing = !d || !d.id;
    show(fix.empty, nothing);
    show(fix.head, !nothing);
    // #145: bar 전체를 숨기지 않는다 — '목록으로'는 장비 소실(라이브→시뮬 폴 락 등) 시
    // 화면 내 유일한 복귀 수단이다. 장비가 없을 때는 점검·설정 수정만 숨긴다.
    show(fix.maintWrap, !nothing);
    show(fix.edit, !nothing);
    show(fix.tiles, !nothing);
    show(fix.notices, !nothing);
    show(fix.cards, !nothing);
    show(fix.notesBox, !nothing);
    if (nothing) {
      patchEmptyBox();
      // 뒤로가기 라벨은 patchFixed 가 안 도는(nothing 조기 return) 빈 상태에서도
      // 현재 언어로 구워야 한다(#394와 같은 계열).
      setText(fix.back.querySelector('[data-f="backLabel"]'), L('Back to fleet', '목록으로'));
      return;
    }

    lastDetail = d;

    // 점검 모드 버튼/메뉴 — 활성 창이 있으면 남은 시간과 해제 옵션을 보여 준다.
    // 창 판정은 compute.activeMaint 단일 소스(노드 배지·경보 억제와 같은 시계/규칙).
    try {
      const am = (ctx.model && typeof ctx.model.activeMaint === 'function') ? ctx.model.activeMaint(state) : {};
      const win = am[d.id];
      fix.maintMenu.querySelectorAll('[data-maint-hours]').forEach((b) => { b.dataset.maintId = d.id; });
      [1, 4, 8, 24].forEach((h) => {
        setText(fix.maintMenu.querySelector('[data-f="h' + h + '"]'), L(h + 'h', h + '시간'));
      });
      setText(fix.maintClearBtn, L('Clear maintenance', '점검 해제'));
      show(fix.maintClearBtn, !!win);
      fix.maintBtn.classList.toggle('is-on', !!win);
      let lbl = L('Maintenance', '점검 모드');
      if (win) {
        const leftMs = Math.max(0, Date.parse(win.until) - Date.now());
        const leftH = Math.floor(leftMs / 3600e3);
        const leftM = Math.max(1, Math.ceil(leftMs / 60e3));
        lbl = leftH >= 1
          ? L('In maintenance · ' + leftH + 'h left', '점검 중 · ' + leftH + '시간 남음')
          : L('In maintenance · ' + leftM + 'm left', '점검 중 · ' + leftM + '분 남음');
      }
      setText(fix.maintBtn.querySelector('[data-f="maintLabel"]'), lbl);
    } catch (e) { /* noop */ }

    // 장비 메모 — 서버(/notes)가 정본. 입력 중(포커스)엔 값을 덮어쓰지 않는다(타이핑 보호).
    try {
      const note = ((state && state.notes) || {})[d.id] || null;
      setText(fix.notesBox.querySelector('[data-f="noteTitle"]'), L('Operator note', '운영자 메모'));
      show(fix.notesBox, true);
      // #473: textarea 값을 어느 장비에 바인딩했는지 기억한다. 같은 detail view 의
      // render-only 전환(뒤로가기)은 destroy 없이 render 만 돌아, 포커스 가드가
      // A 장비 미저장 텍스트를 B 에 잔존시키고 저장(:note-save)은 lastDetail.id 기준이라
      // A 텍스트가 B 로 오저장됐다(#438 모달 devId 가드와 같은 계열). 장비가 바뀌면
      // 포커스와 무관하게 값을 현재 장비 메모로 교체하고, 미저장 분실은 토스트로 알린다.
      if (noteBoundId !== d.id) {
        const stale = String(fix.noteTa.value || '');
        // #496: 분실 판정은 전 장비의 저장값과 비교한다. 저장된 메모가 있는 장비에서
        // 이탈만 한 경우(textarea 값 == 저장값)는 잃은 것이 없는데, stale.trim() 단독
        // 판정은 이 경우에도 매번 '미저장 메모 분실' 오탐 토스트를 띄웠다(#473 부작용).
        const prevSaved = String((((state || {}).notes || {})[noteBoundId] || {}).text || '');
        fix.noteTa.value = (note && note.text) || '';
        noteBoundId = d.id;
        // #491: 제어 실행 결과 배지(resultMsg)도 전 장비 것이다. 리셋하지 않으면 sig 변경으로
        // 재생성된 새 장비의 controlsCard P 클로저가 잔존 resultMsg 를 읽어, 이 장비에서
        // 실행하지 않은 액션의 성공/실패 배지를 띄운다(리셋은 종전 openConfirm 뿐이었다).
        resultMsg = null;
        if (stale.trim() && stale !== prevSaved) {
          try {
            CTX.showToast(L('Device changed — unsaved note discarded.',
              '장비가 바뀌어 저장하지 않은 메모를 버렸습니다.'));
          } catch (e) { /* noop */ }
        }
      } else if (document.activeElement !== fix.noteTa && fix.noteTa.value !== ((note && note.text) || '')) {
        fix.noteTa.value = (note && note.text) || '';
      }
      fix.noteTa.placeholder = L('Handover note for this device (shared with other operators)',
        '이 장비에 대한 인수인계 메모(다른 운영자와 공유됨)');
      setText(fix.noteSave, L('Save note', '메모 저장'));
      setText(fix.noteClear, L('Clear', '지우기'));
      setText(fix.noteMeta, note && note.ts
        ? L('Saved ', '저장 ') + formatNoteTimestamp(note.ts)
        : L('No note', '메모 없음'));
    } catch (e) { /* noop */ }

    const nextSig = sigOf(d, lang);
    if (nextSig !== sig) {
      sig = nextSig;
      patchCards = [];
      fix.tiles.textContent = '';
      fix.notices.textContent = '';
      fix.cards.textContent = '';
      fix.tiles.appendChild(buildTiles(d));
      fix.notices.appendChild(buildNotices(d));
      fix.cards.appendChild(buildCards(d));
    }
    patchFixed.forEach((fn) => { try { fn(d); } catch (e) { /* noop */ } });
    patchCards.forEach((fn) => { try { fn(d); } catch (e) { /* noop */ } });
  },

  destroy() {
    if (rootEl && onClick) rootEl.removeEventListener('click', onClick);
    if (rootEl && onInput) rootEl.removeEventListener('input', onInput);
    if (onKeydown) window.removeEventListener('keydown', onKeydown);
    onClick = null; onInput = null; onKeydown = null;
    patchFixed = []; patchCards = [];
    fix = {}; sig = ''; rootEl = null; emptyRef = null;
    confirmState = null; busy = false; resultMsg = null; lastDetail = null;
    noteBoundId = null;
  },
};
