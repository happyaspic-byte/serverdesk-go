// js/screens/incidents.js — S6: 경보 · 로그 (Alerts & log) — 통합 화면
// REBUILD-SPEC.md §1.6/§1.7 / §5.1 / §5.5.
//
// 통합 배경: incidents(경보 카드)와 logs(로그 tail)는 같은 이벤트 스트림을 표현 형식만 바꿔
//   두 개의 사이드바 진입점으로 분리돼 있었다(같은 심각도 카운트·같은 31건). 하나의 화면으로 합치고
//   "카드(경보 요약) ↔ 로그(원시 tail)" 뷰 토글 + 심각도 필터(KPI 카드)를 제공한다.
//   · 카드 뷰  = 장비 알림을 심각도별로(연속 중복 병합) 보여주는 요약.
//   · 로그 뷰  = 알림 + SNMP 트랩의 원시 tail(시각순) + 키워드 검색 + Live/Pause.
//   심각도 KPI 카드가 두 뷰 공통 필터다(alertFilter → logLevel 매핑).
//
// 규칙: js/model/*, js/util/* 만 ctx 경유. CSS 접두사 sc-inc-(+ 로그 콘솔은 공용 sc-log-* 재사용).

// 심각도 필터(critical/warning/info/all) → 로그 레벨(ERROR/WARN/INFO/all) 매핑.
const SEV_TO_LEVEL = { all: 'all', critical: 'ERROR', warning: 'WARN', info: 'INFO' };

// 렌더 상한(화면에만 적용, 모델/compute.js 는 그대로): 로그가 수백~수천 건 쌓이면 시그니처가
// 바뀔 때마다 전 행을 통짜 재생성해 폴 주기마다 화면이 버벅였다. 최신 LOG_CAP 건까지만 그리고
// 생략된 이전 로그는 하단 안내 행으로 대체한다.
// logsFull 은 최신순(desc — 개요 미리보기 logs = logRows.slice(0,7) 와 같은 방향)이라
// **앞에서** 잘라야 최신이 남는다. slice(-LOG_CAP) 은 배열 끝, 즉 가장 오래된 N건을 남겨
// 안내 문구('최신 200건 표시')와 정반대였다(실결함) — 순수 함수로 빼 두 방향을 테스트에 고정한다.
export const LOG_CAP = 200;
export function capLogRows(logsFull, cap) {
  const rows = Array.isArray(logsFull) ? logsFull : [];
  const limit = cap > 0 ? cap : LOG_CAP;
  const omitted = Math.max(0, rows.length - limit);
  return { shown: omitted ? rows.slice(0, limit) : rows, omitted };
}

/** 로그 행(data-log-row, role=button tabindex=0)의 키보드 활성화 판정(#32) —
 *  행은 div 라 Enter/Space 가 네이티브로는 눌리지 않는다. 행 위에서의 Enter/Space 만
 *  상세 진입으로 인식한다(판정을 순수 함수로 빼 테스트에 고정 — overview.js 의
 *  isLogToggleKey 와 같은 패턴). */
export function isLogRowKey(e) {
  if (!e || (e.key !== 'Enter' && e.key !== ' ' && e.key !== 'Spacebar')) return false;
  const t = e.target;
  return !!(t && typeof t.closest === 'function' && t.closest('[data-log-row]'));
}

// 카드 뷰 빈 상태 카피(#30) — 필터링 결과 0건과 전체 무경보는 다른 상태다.
// '정보' 필터에서 정보 경보만 없어도 '활성 경보 없음 — 전체 정상'으로 뜨면, 다른 심각도에
// 심각 경보가 쌓여 있는데도 전체 정상으로 오인한다. 로그 뷰가 이미 쓰는 필터-빈 카피 계열로 분기한다.
// 분기 판정을 순수 함수로 빼 테스트에 고정한다(capLogRows 와 같은 패턴).
export function emptyCardTitle(alertFilter, L) {
  return (alertFilter && alertFilter !== 'all')
    ? L('No alerts match this filter.', '이 필터에 해당하는 경보가 없습니다.')
    : L('No active alerts — all clear', '활성 경보 없음 — 전체 정상');
}

// 심각도 칩 모집단 선택(#28) — 칩 카운트는 목록과 같은 모집단에서 세야 한다.
// 카드 뷰 목록은 incList(= alertsAll, 확인·점검 포함)인데 칩만 incStats.cards(= activeAlerts,
// 확인·점검 제외)에서 세어 '전체 26' 칩 클릭 시 31행이 나오는 불일치가 있었다. compute.js 가
// 원칙 주석과 함께 만들어 둔 incFilters(= incList 와 동일 모집단)를 카드 뷰 칩이 소비한다.
// incStats 자체는 app.js·overview.js 의 '활성' 의미 소비처가 있으므로 건드리지 않고,
// 로그 뷰는 기존 계약대로 incLogFilters(logRows 모집단)를 쓴다.
// renderKpis 가 기대하는 필드(key/label/value/tone/icon)에 맞춰 반환한다 — incFilters 에는
// tone 이 없으므로 incStats.cards 와 같은 키→톤 매핑을 여기서 입힌다.
export function chipCardsFor(view, m) {
  const src = view === 'log' ? ((m && m.incLogFilters) || []) : ((m && m.incFilters) || []);
  return src.map((f) => ({
    key: f.key,
    label: f.label,
    tone: f.tone || (f.key === 'critical' ? 'neg' : (f.key === 'warning' ? 'warn' : (f.key === 'info' ? 'info' : 'mut'))),
    value: f.count,
    count: f.count,
    icon: f.key === 'info' ? 'infoCircle' : (f.key === 'all' ? 'bell' : 'warningCircle'),
  }));
}

export default {
  key: 'incidents',
  title: { en: 'Alerts & log', ko: '경보 · 로그' },
  icon: 'bell',

  init(root, ctx) {
    const dom = ctx.util.dom;
    const icon = ctx.util.icon;
    const el = dom.el;

    // ---- 헤더 -------------------------------------------------------------
    const titleEl = el('h1', { class: 'sc-inc-title' });
    const subEl = el('p', { class: 'sc-inc-sub' });
    const head = el('div', { class: 'sc-inc-head' }, [titleEl, subEl]);

    // ---- 뷰 토글(세그먼트) + 로그 도구(검색 · Live/Pause) --------------------
    const mkSeg = (view, ico) => {
      const lbl = el('span', { class: 'sc-inc-view-lbl' });
      // 토글 버튼 — is-active 클래스만으로는 스크린리더에 눌림 상태가 전달되지 않는다(#389).
      // 같은 화면 심각도 칩(아래 renderKpis)·settings 세그먼트 전례대로 aria-pressed 를 굽고
      // render 에서 매번 갱신한다.
      const btn = el('button', {
        type: 'button', class: 'seg-btn sc-inc-view', 'data-inc-view': view,
        'aria-pressed': 'false',
      }, [el('span', { class: 'sc-inc-view-ico' }, [icon(ico, { size: 13 })]), lbl]);
      return { btn, lbl };
    };
    const segCards = mkSeg('cards', 'bell');
    const segLog = mkSeg('log', 'clock');
    const segStats = mkSeg('stats', 'checklist');
    const viewToggle = el('div', { class: 'seg sc-inc-views' }, [segCards.btn, segLog.btn, segStats.btn]);

    const searchInput = el('input', {
      type: 'text', class: 'sc-log-search-input', 'data-inc-search': true,
      autocomplete: 'off', spellcheck: 'false', 'aria-label': ctx.L('Search logs', '로그 검색'),
    });
    // × 만으로는 스크린리더에 접근명이 없다(#351, #323 과 동일 류) — aria-label 을 굽고,
    // 언어 전환 추적은 renderLogView 의 placeholder 갱신과 같은 자리에서 한다.
    const searchClear = el('button', {
      type: 'button', class: 'sc-log-search-clear', 'data-inc-clear': true, hidden: true, text: '×',
      'aria-label': ctx.L('Clear search', '검색 지우기'),
    });
    const searchBox = el('div', { class: 'sc-log-search sc-inc-search' }, [searchInput, searchClear]);

    const liveIco = el('span', { class: 'sc-log-live-ico' }, [icon('bolt', { size: 14 })]);
    const liveLabel = el('span', {});
    const liveBtn = el('button', {
      type: 'button', class: 'sc-log-live-btn', 'data-action': 'togglePause',
    }, [liveIco, liveLabel]);

    const logTools = el('div', { class: 'sc-inc-logtools' }, [searchBox, liveBtn]);
    const toolbar = el('div', { class: 'sc-inc-toolbar' }, [viewToggle, logTools]);

    // ---- KPI 4카드 = 심각도 필터(두 뷰 공통) --------------------------------
    const kpiGrid = el('div', { class: 'kpi-grid sc-inc-kpis' });

    // ---- 카드 뷰(경보 요약) ------------------------------------------------
    const banner = el('div', { class: 'sc-inc-banner', hidden: true }, [
      el('span', { class: 'sc-inc-banner-ico' }, [icon('warningCircle', { size: 18 })]),
      el('span', { class: 'sc-inc-banner-text' }),
    ]);
    const skeleton = el('div', { class: 'sc-inc-skeleton', hidden: true }, [
      el('div', { class: 'sc-inc-skel-row' }),
      el('div', { class: 'sc-inc-skel-row' }),
      el('div', { class: 'sc-inc-skel-row' }),
    ]);
    const emptyIco = el('span', { class: 'sc-inc-empty-ico' }, [icon('check', { size: 24 })]);
    const emptyTitle = el('div', { class: 'sc-inc-empty-title' });
    const emptySub = el('div', { class: 'sc-inc-empty-sub u-muted' });
    const emptyAdd = el('button', { class: 'btn btn--primary sc-inc-empty-add', type: 'button', 'data-goto': 'manage', hidden: true });
    const emptyBox = el('div', { class: 'card sc-inc-empty', hidden: true }, [emptyIco, emptyTitle, emptySub, emptyAdd]);
    const list = el('div', { class: 'sc-inc-list', hidden: true });
    // '전체' 필터 기본 뷰는 상위 N건만 — 나머지는 이 버튼으로 펼친다(minor#4, 무한 스크롤 높이 방지).
    const moreBtn = el('button', { class: 'sc-inc-more', type: 'button', 'data-inc-more': true, hidden: true });
    const lessBtn = el('button', { class: 'sc-inc-less', type: 'button', 'data-inc-less': true, hidden: true });
    // 일괄 조작 — 31건을 한 건씩 누르게 두면 실제로는 아무도 안 쓴다.
    const ackAllBtn = el('button', { class: 'sc-inc-bulk', type: 'button', 'data-action': 'ackAllVisible' });
    const ackNoneBtn = el('button', { class: 'sc-inc-bulk', type: 'button', 'data-action': 'ackClearAll' });
    const bulkBar = el('div', { class: 'sc-inc-bulkbar' }, [ackAllBtn, ackNoneBtn]);
    const cardWrap = el('div', { class: 'sc-inc-cardwrap' }, [banner, skeleton, emptyBox, bulkBar, list, moreBtn, lessBtn]);

    // ---- 로그 뷰(원시 tail 콘솔) — 공용 sc-log-* 재사용 ---------------------
    const consoleDot = el('span', { class: 'sc-log-live-dot' });
    const consoleStatus = el('span', { class: 'sc-log-status' });
    const consoleCount = el('span', { class: 'sc-log-count u-mono' });
    const consoleHead = el('div', { class: 'sc-log-console-head' }, [consoleDot, consoleStatus, consoleCount]);
    const logList = el('div', { class: 'sc-log-list' });
    const logEmptyTitle = el('div', { class: 'empty-title' });
    const logEmptySub = el('div', { class: 'empty-sub' });
    const logEmpty = el('div', { class: 'empty sc-log-empty', hidden: true }, [
      el('div', { class: 'empty-icon' }, [icon('infoCircle', { size: 18 })]), logEmptyTitle, logEmptySub,
    ]);
    const consolePanel = el('div', { class: 'sc-log-console' }, [consoleHead, logList, logEmpty]);
    const logWrap = el('div', { class: 'sc-inc-logwrap', hidden: true }, [consolePanel]);

    // ---- 통계 뷰(인사이트 + CSV 납품 + 인쇄) --------------------------------
    const csvAlertsBtn = el('button', { class: 'sc-inc-bulk', type: 'button', 'data-inc-csv': 'alerts' });
    const csvLogBtn = el('button', { class: 'sc-inc-bulk', type: 'button', 'data-inc-csv': 'log' });
    const csvDevsBtn = el('button', { class: 'sc-inc-bulk', type: 'button', 'data-inc-csv': 'devices' });
    const printBtn = el('button', { class: 'sc-inc-bulk', type: 'button', 'data-inc-print': true });
    const statsTools = el('div', { class: 'sc-inc-stattools' }, [csvAlertsBtn, csvLogBtn, csvDevsBtn, printBtn]);
    const statsBody = el('div', { class: 'sc-inc-stats' });
    const statsWrap = el('div', { class: 'sc-inc-statswrap', hidden: true }, [statsTools, statsBody]);

    const body = el('div', { class: 'sc-inc-body' }, [cardWrap, logWrap, statsWrap]);
    const page = el('div', { class: 'sc-inc-page' }, [head, toolbar, kpiGrid, body]);
    root.appendChild(page);

    const S = {
      root, dom, icon, ctx,
      titleEl, subEl, kpiGrid,
      segCards, segLog, segStats, viewToggle, logTools, searchInput, searchClear, liveIco, liveLabel, liveBtn,
      statsWrap, statsBody, statsTools, csvAlertsBtn, csvLogBtn, csvDevsBtn, printBtn,
      banner, skeleton, emptyBox, emptyTitle, emptySub, emptyAdd, list, moreBtn, lessBtn, cardWrap, bulkBar, ackAllBtn, ackNoneBtn,
      consoleDot, consoleStatus, consoleCount, logList, logEmpty, logEmptyTitle, logEmptySub, logWrap,
      kpiRefs: [], rowRefs: [], rowSig: null, logSig: null, kpiSig: null,
    };
    this._S = S;

    // ---- 이벤트 위임(클릭 1개) --------------------------------------------
    S.onClick = async (ev) => {
      // 병합(×N) 행의 확인 버튼 — 멤버 전체 ackKey(data-ack-keys, ␂ 구분)를 일괄 처리한다(#29).
      // app.js 의 ackAlert 위임은 단일 키(data-ack-key)만 알므로, 복수 키는 여기서 처리하고 전파를
      // 막는다(막지 않으면 document 위임이 대표 키를 한 번 더 토글해 방금 확인이 취소된다).
      // 토글 기준은 병합 행 표시와 같은 '전원 확인' — 전원 확인 상태면 전체 해제, 아니면 전체 확인.
      // ackedAlerts 변경분은 app.js 의 store 구독이 pushAck 로 서버에 그대로 동기화한다.
      const ackBtn = ev.target.closest('.sc-inc-ack');
      if (ackBtn && root.contains(ackBtn)) {
        const keys = String(ackBtn.dataset.ackKeys || '').split('\u0002').filter(Boolean);
        if (keys.length > 1) {
          ev.stopPropagation();
          const cur = ctx.store.getState().ackedAlerts || {};
          const unack = keys.every((k) => cur[k]);
          const approved = ctx.confirmAction ? await ctx.confirmAction({
            title: unack ? ctx.L('Remove grouped acknowledgements', '묶음 경보 확인 해제') : ctx.L('Acknowledge grouped alerts', '묶음 경보 확인'),
            impact: ctx.L(
              keys.length + ' acknowledgement markers will be changed. Monitoring and source alerts remain unchanged.',
              keys.length + '건의 확인 표시를 변경합니다. 모니터링과 원본 경보는 변경되지 않습니다.'
            ),
            confirmLabel: unack ? ctx.L('Remove ' + keys.length, keys.length + '건 해제') : ctx.L('Acknowledge ' + keys.length, keys.length + '건 확인'),
            requireReason: true,
          }) : { confirmed: true };
          if (!approved) return;
          ctx.store.setState((s) => {
            const next = Object.assign({}, s.ackedAlerts);
            const now = new Date().toISOString();
            keys.forEach((k) => {
              if (unack) delete next[k];
              else next[k] = { ts: now, by: approved.operator || ctx.operator || 'admin', reason: approved.reason || '' };
            });
            return { ackedAlerts: next };
          });
          ctx.showToast(unack
            ? ctx.L('Acknowledgement removed (' + keys.length + ' alerts)', '경보 ' + keys.length + '건의 확인을 해제했습니다')
            : ctx.L(keys.length + ' alerts acknowledged', '경보 ' + keys.length + '건을 확인 처리했습니다'));
          return;
        }
      }
      const view = ev.target.closest('[data-inc-view]');
      if (view && root.contains(view)) {
        ctx.store.setState({ alertView: view.getAttribute('data-inc-view') });
        return;
      }
      const filterBtn = ev.target.closest('[data-inc-filter]');
      if (filterBtn && root.contains(filterBtn)) {
        const key = filterBtn.getAttribute('data-inc-filter');
        // 하나의 심각도 필터로 두 뷰(카드/로그)를 동시에 구동한다. 필터를 바꾸면 '전체 펼침'은 접는다.
        ctx.store.setState({ alertFilter: key, logLevel: SEV_TO_LEVEL[key] || 'all', alertExpanded: false });
        return;
      }
      const csvBtn = ev.target.closest('[data-inc-csv]');
      if (csvBtn && root.contains(csvBtn)) { exportCsv(S, csvBtn.getAttribute('data-inc-csv')); return; }
      const printB = ev.target.closest('[data-inc-print]');
      if (printB && root.contains(printB)) { window.print(); return; }
      const moreB = ev.target.closest('[data-inc-more]');
      if (moreB && root.contains(moreB)) {
        ctx.store.setState({ alertExpanded: true });
        return;
      }
      const lessB = ev.target.closest('[data-inc-less]');
      if (lessB && root.contains(lessB)) {
        ctx.store.setState({ alertExpanded: false });
        return;
      }
      const clearBtn = ev.target.closest('[data-inc-clear]');
      if (clearBtn && root.contains(clearBtn)) { ctx.store.setState({ logQuery: '' }); return; }
      const cardRow = ev.target.closest('[data-inc-row]');
      if (cardRow && root.contains(cardRow)) {
        const id = cardRow.getAttribute('data-id');
        if (id) ctx.goDetail(id);
        return;
      }
      const logRow = ev.target.closest('[data-log-row]');
      if (logRow && root.contains(logRow)) ctx.goDetail(logRow.dataset.logRow);
    };
    root.addEventListener('click', S.onClick);

    // 로그 행(role=button tabindex=0)의 키보드 활성화(#32) — 클릭하면 장비 상세로 가는 행인데
    // div 라 키보드·스크린리더는 도달 자체가 불가했다(카드 뷰 행은 <button>이라 비대칭).
    // Enter/Space 를 클릭과 같은 상세 진입에 연결한다. Space 는 preventDefault 로 페이지
    // 스크롤을 막는다(nodes.js 의 data-nd-row 처리와 같은 패턴).
    S.onKeydown = (ev) => {
      if (!isLogRowKey(ev)) return;
      const logRow = ev.target.closest('[data-log-row]');
      if (!root.contains(logRow)) return;
      ev.preventDefault();
      ctx.goDetail(logRow.dataset.logRow);
    };
    root.addEventListener('keydown', S.onKeydown);

    // 키워드 검색(로그 뷰) — input 이벤트
    S.onInput = () => { ctx.store.setState({ logQuery: S.searchInput.value }); };
    S.searchInput.addEventListener('input', S.onInput);
  },

  render(state, ctx) {
    const S = this._S;
    if (!S) return;
    const L = ctx.L;
    const view = state.alertView === 'log' ? 'log' : (state.alertView === 'stats' ? 'stats' : 'cards');

    S.titleEl.textContent = L('Alerts & log', '경보 · 로그');
    S.subEl.textContent = view === 'log'
      ? L('Raw tail of fleet alerts and SNMP traps.', '플릿 경보와 SNMP 트랩의 원시 로그 tail.')
      : (view === 'stats'
        ? L('Recurring alerts, per-device counts, ack latency and availability — exportable.', '반복 경보·장비별 집계·확인 시간·가용성 요약 — CSV/인쇄 납품.')
        : L('Live view of active alerts across the fleet.', '플릿 전역의 활성 경보를 실시간으로 확인합니다.'));

    // 뷰 토글 라벨/활성 — is-active 와 aria-pressed 를 함께 갱신한다(#389,
    // settings.js 세그먼트·아래 renderKpis 칩과 같은 계약).
    S.segCards.lbl.textContent = L('Cards', '카드');
    S.segLog.lbl.textContent = L('Log', '로그');
    S.segStats.lbl.textContent = L('Stats', '통계');
    [[S.segCards.btn, 'cards'], [S.segLog.btn, 'log'], [S.segStats.btn, 'stats']].forEach(([b, v]) => {
      b.classList.toggle('is-active', view === v);
      b.setAttribute('aria-pressed', view === v ? 'true' : 'false');
    });
    S.logTools.hidden = view !== 'log';
    S.cardWrap.hidden = view !== 'cards';
    S.logWrap.hidden = view !== 'log';
    S.statsWrap.hidden = view !== 'stats';
    S.kpiGrid.hidden = view === 'stats';   // 심각도 필터는 카드/로그 뷰용 — 통계엔 무관
    // 통계 뷰 도구 라벨(i18n) — 매 렌더 덮어써도 텍스트뿐이라 안전하다.
    S.csvAlertsBtn.textContent = L('Alerts CSV', '경보 CSV');
    S.csvLogBtn.textContent = L('Log CSV', '로그 CSV');
    S.csvDevsBtn.textContent = L('Devices CSV', '장비 CSV');
    S.printBtn.textContent = L('Print', '인쇄');

    const m = (ctx.getModel && ctx.getModel(state)) || null;
    if (!m) { showModelUnavailable(S, L); return; }

    // A1/A2: 심각도 필터는 두 뷰(카드/로그) 공통이므로, 대형 KPI 4카드(다색 과분할) 대신
    // 콤팩트 세그먼트 필터바(칩) 한 줄로 축약해 탭 상단에 1회만 노출한다(카드/로그 양 탭 공유).
    // 카드 뷰는 각 경보 행 배지가, 로그 뷰는 레벨 배지가 심각도를 이미 인코딩하므로 대형 카드는 중복.
    S.kpiGrid.classList.add('sc-inc-kpis--slim');
    // 칩/목록 모집단 일치(감사 P1): 심각도 칩이 항상 incStats(= meta.alerts[], 31건)에서
    // 세어져 실제 렌더 대상이 events[] 인 로그 뷰와 어긋났다(전체 31→3행, 정보 0→2행 등 4/4 불일치).
    // 뷰별로 자기 모집단에서 센 칩을 쓴다: 카드 뷰=incFilters(incList 와 같은 alertsAll 모집단,
    // #28 — incStats.cards 는 확인·점검을 빼 목록보다 적게 셌다), 로그 뷰=incLogFilters.
    renderKpis(S, chipCardsFor(view, m), state.alertFilter || 'all');

    S._m = m;   // exportCsv 가 같은 모델을 쓴다(클릭 시점 재계산 방지)
    if (view === 'log') { renderLogView(S, ctx, state, m); return; }
    if (view === 'stats') { renderStatsView(S, ctx, state, m); return; }
    renderCardView(S, ctx, state, m);
  },

  destroy() {
    const S = this._S;
    if (!S) return;
    S.root.removeEventListener('click', S.onClick);
    if (S.onKeydown) S.root.removeEventListener('keydown', S.onKeydown);
    if (S.searchInput && S.onInput) S.searchInput.removeEventListener('input', S.onInput);
    this._S = null;
  },
};

// ---------------------------------------------------------------------------
// 내부 헬퍼
// ---------------------------------------------------------------------------

function show(node, on) { if (node) node.hidden = !on; }
function toneCls(tone) { return tone === 'mut' ? 'u-muted' : ('is-' + (tone || 'mut')); }
function filterKeyForCard(key) { return key === 'total' ? 'all' : key; }
// 필터바 색 규율(A1): 총합·경고·정보는 중립, '심각'만(값>0) red — 개요 '총합 중립·심각만 red'와 일치.
function chipTone(c) { return (c.key === 'critical' && (c.value || 0) > 0) ? 'neg' : 'mut'; }

function renderKpis(S, cards, alertFilter) {
  const { dom, icon, kpiGrid } = S;
  const el = dom.el;
  // 재생성 판단을 길이 비교에서 키 시그니처 비교로 — 뷰 전환(카드 incStats.cards ↔ 로그 incLogFilters) 시
  // 칩 개수가 같아도 filter key/아이콘이 달라질 수 있어, 길이만 볼면 data-inc-filter/아이콘이 stale 로 남았다.
  const sig = cards.map((c) => c.key).join('|');
  if (S.kpiSig !== sig) {
    S.kpiSig = sig;
    dom.clear(kpiGrid);
    S.kpiRefs = cards.map((c) => {
      const iconWrap = el('span', { class: 'sc-inc-kpi-ico ' + toneCls(c.tone) }, [icon(c.icon || 'bell', { size: 14 })]);
      const labelText = el('span', {});
      const label = el('div', { class: 'kpi-label u-row' }, [iconWrap, labelText]);
      const valEl = el('div', { class: 'kpi-val u-mono' });
      const footEl = el('div', { class: 'kpi-foot' });
      const filterKey = filterKeyForCard(c.key);
      const node = el('button', {
        class: 'kpi sc-inc-kpi', type: 'button',
        'data-inc-filter': filterKey, 'aria-pressed': 'false',
      }, [label, valEl, footEl]);
      kpiGrid.appendChild(node);
      return { node, iconWrap, labelText, valEl, footEl, filterKey };
    });
  }
  cards.forEach((c, i) => {
    const r = S.kpiRefs[i];
    if (!r) return;
    const active = r.filterKey === (alertFilter || 'all');
    const ct = chipTone(c);
    r.labelText.textContent = c.label;
    r.valEl.textContent = String(c.value);
    r.valEl.className = 'kpi-val u-mono ' + toneCls(ct);
    r.footEl.textContent = c.note || '';
    r.iconWrap.className = 'sc-inc-kpi-ico ' + toneCls(ct);
    r.node.className = 'kpi sc-inc-kpi' + (active ? ' is-active' : '');
    r.node.setAttribute('aria-pressed', active ? 'true' : 'false');
  });
}

// 일괄 바 두 버튼('전체 확인'/'확인 해제')이 같은 계약으로 싣는 키 목록 — 현재 필터 목록에서
// acked 상태가 일치하는 행의 ackKey 만 ␂(U+0002) 구분으로 잇는다. app.js 의 ackAllVisible·
// ackClearAll 위임은 이 목록 범위만 일괄 처리한다(#27 — 해제 버튼의 N 은 목록의 확인 건수인데
// 전역 ackedAlerts 를 비워 필터 밖·타 운영자 확인분까지 날렸다). 병합 행 data-ack-keys(#29)와
// 같은 구분자라 app.js 는 두 경로를 같은 코드로 파싱한다.
export function bulkAckKeys(list, acked) {
  return (list || []).filter((r) => !!r.acked === acked).map((r) => r.ackKey).join('\u0002');
}

// 카드 뷰가 실제로 보여주는 행 — 검증용 임시 픽스처 호스트(srv-evt-test 등)는 실장비가
// 아니므로 고객이 보는 목록에서 뺀다. 모델은 하드 삭제 대신 testFixture 플래그만 세우므로
// 숨김 결정은 화면이 한다(데이터 손실 금지). 빈 상태 판정보다 먼저 적용해야 픽스처만
// 남은 뷰가 공백 화면 대신 빈 카피를 띄운다(#265).
export function visibleCardAlerts(list) {
  return (list || []).filter((r) => !r.testFixture);
}

/* ---- 모델 실패(!m) ---- */
// buildModel 예외 등으로 모델이 없을 때의 안내 표면(#317). renderCardView 의 세 분기(#267)와
// 같은 계약으로 일괄 바·'더 보기'도 진입 즉시 내린다 — 내리지 않으면 직전 성공 렌더의
// stale '전체 확인 (N)'·data-ack-keys 가 '모델을 불러올 수 없습니다' 위에 클릭 가능한 채 남아
// 보이지 않는 경보가 일괄 확인되고 서버 /ack 으로 동기화될 수 있었다(app.js ackAllVisible).
// 재노출은 성공 경로(renderCardView 가 라벨·키를 다시 실은 뒤)뿐이다.
export function showModelUnavailable(S, L) {
  S.emptyTitle.textContent = L('Model unavailable', '모델을 불러올 수 없습니다');
  show(S.banner, false); show(S.skeleton, false); show(S.list, false); show(S.emptyBox, true);
  show(S.moreBtn, false); show(S.lessBtn, false); show(S.bulkBar, false);
  // 로그 뷰에도 동일 안내 — 카드 뷰만 처리하고 로그 콘솔은 빈 화면으로 남는 회귀가 있었다.
  S.logEmptyTitle.textContent = L('Model unavailable', '모델을 불러올 수 없습니다');
  S.logEmptySub.textContent = '';
  show(S.logList, false); show(S.logEmpty, true);
  // 통계 뷰에도 동일 안내(#388) — render 는 !m 이어도 view 에 맞는 wrap 을 보이게 두고
  // early return 하므로, stats 뷰에서는 statsWrap 이 그대로 노출된다. 여기를 비우지 않으면
  // 직전 성공 렌더의 낡은 통계가 오류 표시 없이 현재값으로 오인된다(카드 안내는 cardWrap 이
  // 숨겨져 보이지 않는다). 안내로 교체하고 서명을 리셋해, 모델 복구 뒤 같은 서명이어도
  // 통계가 다시 그려지게 한다(로그 뷰의 S.logSig = '' 리셋과 같은 계약).
  S.dom.clear(S.statsBody);
  S.statsBody.appendChild(S.dom.el('div', {
    class: 'u-muted sc-inc-stat-empty',
    text: L('Model unavailable', '모델을 불러올 수 없습니다'),
  }));
  S.statsSig = null;
  // 통계 도구바(CSV 3종·인쇄)도 같은 계약으로 내린다(#489) — 본문(#388)만 안내로 교체하고
  // 도구바를 두면, 모델 불가 상태에서도 버튼이 살아 있어 exportCsv 가 직전 성공 렌더의
  // stale S._m 을 그대로 소비해 낡은 데이터가 'CSV 납품: …' 성공 토스트와 함께 저장된다
  // (일괄 바 #267·본문 #388 과 같은 오류 표면의 stale 조작 경로). 재노출은 성공 경로
  // (renderStatsView)뿐이다. S._m 도 비워, 도구바를 우회한 호출에서도 exportCsv 의
  // !m 가드가 stale 모델 소비를 막게 한다.
  show(S.statsTools, false);
  S._m = null;
}

/* ---- 카드 뷰(경보 요약) ---- */
// 세 분기(오류·로딩·빈)의 일괄 바 숨김 계약(#267)을 테스트가 직접 검증할 수 있게 export 한다.
export function renderCardView(S, ctx, state, m) {
  const L = ctx.L;
  const pollStat = m.pollStat || {};
  const liveError = pollStat.liveError || null;
  // pollPending/lastPoll이 최초 로딩의 정본이다. 장비 0대는 성공 응답일 수 있으므로
  // 모델 total=0만으로 스켈레톤을 계속 보여주지 않는다.
  const loading = !liveError && !!state.pollPending && !state.lastPoll;
  // 픽스처 필터는 빈 상태 판정보다 먼저 — 픽스처만 남은 뷰도 빈 카피가 떠야 한다(#265).
  let list = visibleCardAlerts(m.incList);
  // 일괄 바·'더 보기'의 포커스를 맨 먼저 기억한다(#564) — 아래 show(…, false) 가 매 렌더마다
  // bulkBar·moreBtn 을 일시 내리고(성공 경로에서만 재노출), 포커스된 요소가 display:none 되는
  // 순간 브라우저는 포커스를 body 로 떨군다. #405 의 복원(renderList)은 listEl 남쪽만 커버해
  // cardWrap 형제인 이 버튼들에는 닿지 않으므로, 여기서 잡아두고 성공 경로 말미에 복원한다.
  const prevActive = (typeof document !== 'undefined' && document.activeElement) || null;
  show(S.moreBtn, false); show(S.lessBtn, false);   // 배너/스켈레톤/빈 상태에선 펼침 컨트롤 숨김
  // 일괄 바도 같은 이유로 여기서 내린다(#267) — 세 분기가 갱신 없이 return 합니다
  // 이전 성공 렌더의 stale N·data-ack-keys 가 남아 보이지 않는 경보가 일괄 처리될 수 있었다.
  // 재노출은 아래 성공 경로(라벨·키를 먼저 다시 싣는 뒤)뿐이다.
  show(S.bulkBar, false);
  show(S.emptyAdd, false);
  S.emptySub.textContent = '';

  if (liveError) {
    S.banner.querySelector('.sc-inc-banner-text').textContent =
      L('Poller unreachable — cannot load alerts: ', '폴러 연결 실패 — 경보를 불러올 수 없습니다: ') + liveError;
    show(S.banner, true); show(S.skeleton, false); show(S.emptyBox, false); show(S.list, false);
    return;
  }
  if (loading) {
    show(S.banner, false); show(S.skeleton, true); show(S.emptyBox, false); show(S.list, false);
    return;
  }
  if (!list.length) {
    const noDevices = !Array.isArray(state.fleet) || state.fleet.length === 0;
    if (noDevices) {
      S.emptyTitle.textContent = L('No devices configured', '등록된 장비가 없습니다');
      S.emptySub.textContent = L(
        'Add a supported device to begin monitoring.',
        '지원되는 장비를 등록하면 모니터링을 시작할 수 있습니다.'
      );
      S.emptyAdd.textContent = L('Add device', '장비 추가');
      show(S.emptyAdd, true);
    } else {
      // 필터 0건은 '전체 정상'이 아니다(#30) — 필터 종류에 따라 카피를 분기한다(판정은 emptyCardTitle).
      S.emptyTitle.textContent = emptyCardTitle(state.alertFilter, L);
      S.emptySub.textContent = state.alertFilter && state.alertFilter !== 'all'
        ? L('Choose another severity filter to review the remaining alerts.', '다른 심각도 필터를 선택해 나머지 경보를 확인하세요.')
        : L('All configured devices are currently clear.', '등록된 모든 장비에 현재 활성 경보가 없습니다.');
    }
    show(S.banner, false); show(S.skeleton, false); show(S.emptyBox, true); show(S.list, false);
    return;
  }
  show(S.banner, false); show(S.skeleton, false); show(S.emptyBox, false); show(S.list, true);

  // '전체' 필터 기본 뷰 상한(minor#4): 심각+경고 우선 최대 30건만, 나머지는 '전체 N건 보기'로 펼침.
  // 심각도 필터(critical/warning/info)를 고르면 이미 좁혀지므로 상한을 적용하지 않는다.
  const CAP = 30;
  const isAll = (state.alertFilter || 'all') === 'all';
  const expanded = !!state.alertExpanded;
  let shown = list;
  let moreN = 0;
  if (isAll && !expanded) {
    const pri = list.filter((r) => r.sevTone === 'neg' || r.sevTone === 'warn');
    shown = (pri.length ? pri : list).slice(0, CAP);
    moreN = list.length - shown.length;
  }
  // 일괄 바 — 미확인이 있으면 '전체 확인', 확인분이 있으면 '확인 해제'를 노출한다.
  // 두 버튼 모두 data-ack-keys 에 '현재 필터 목록'의 해당 키만 싣는다(bulkAckKeys, #27) —
  // app.js 위임은 그 범위만 일괄 처리하므로 라벨의 N 과 실제 처리 건수가 항상 일치한다.
  const unackedN = list.filter((r) => !r.acked).length;
  const ackedN = list.length - unackedN;
  S.ackAllBtn.textContent = L('Acknowledge all (' + unackedN + ')', '전체 확인 (' + unackedN + ')');
  S.ackAllBtn.dataset.ackKeys = bulkAckKeys(list, false);
  S.ackNoneBtn.textContent = L('Undo all (' + ackedN + ')', '확인 해제 (' + ackedN + ')');
  S.ackNoneBtn.dataset.ackKeys = bulkAckKeys(list, true);
  show(S.ackAllBtn, unackedN > 0);
  show(S.ackNoneBtn, ackedN > 0);
  show(S.bulkBar, list.length > 0);
  renderList(S, ctx, shown);
  if (moreN > 0 && !expanded) {
    S.moreBtn.textContent = L('View all ' + list.length + ' alerts', '전체 ' + list.length + '건 보기');
    S.moreBtn.setAttribute('aria-expanded', 'false');
  }
  S.lessBtn.textContent = L('Show fewer alerts', '경보 접기');
  S.lessBtn.setAttribute('aria-expanded', expanded ? 'true' : 'false');
  show(S.moreBtn, isAll && !expanded && moreN > 0);
  show(S.lessBtn, isAll && expanded);
  // 포커스 복원(#564) — '전체 확인'·'확인 해제'·'더 보기'는 자기 동작이 성공하면 누른 버튼
  // 자신이 hidden 처리되고, 설령 계속 보이는 버튼이라도 위의 일시 숨김(show(…, false) → 재노출)으로
  // 포커스가 body 로 떨어진다. 계속 보이면 같은 버튼으로 되돌리고, 숨겨졌으면 안전한 인접 요소
  // (목록 첫 행)로 옮긴다.
  if (prevActive === S.ackAllBtn || prevActive === S.ackNoneBtn || prevActive === S.moreBtn || prevActive === S.lessBtn) {
    if (!prevActive.hidden) {
      prevActive.focus();
    } else {
      const first = S.rowRefs && S.rowRefs[0];
      if (first) first.row.focus();
    }
  }
}

// 확인 상태를 서명에 포함 — 빠지면 '확인' 눌러도 행이 재구성되지 않는다.
function rowKey(r) { return (r.hostId || r.id) + '|' + r.sev + '|' + r.time + '|' + (r.dupCount || 1) + '|' + (r.acked ? 'a' : '') + '|' + (r.maintWin ? 'm' : ''); }

// 연속(이웃) 동일 경보만 병합 — 장비+심각도+메시지+표시 상대시각이 전부 같아 화면상 똑같은 카드만 접는다.
// 병합 시 멤버 전체의 ackKey 를 ackKeys 배열로 보존한다(#29) — 대표 1건의 키만 남기면 ×N 행의 '확인'이
// 나머지 N-1건을 미확인으로 남겨 칩 카운트·일괄 바 건수·개요 배지가 줄지 않았다.
// 병합 행의 acked 표시는 '전원 확인' 기준 — 멤버 중 하나라도 미확인이면 미확인으로 둔다.
// renderList·클릭 위임·테스트가 같은 계약을 볼 수 있게 export 한다(capLogRows 와 같은 순수 헬퍼).
export function mergeDupAlerts(list) {
  const merged = [];
  list.forEach((r) => {
    const key = (r.hostId || r.id) + '|' + r.sev + '|' + r.msg + '|' + r.ago;
    const prev = merged[merged.length - 1];
    if (prev && prev._dupKey === key) {
      prev.dupCount += 1;
      prev.ackKeys.push(r.ackKey);
      const allAcked = prev._allAcked && !!r.acked;
      if (String(r.time || '') > String(prev.time || '')) {
        Object.assign(prev, r, { dupCount: prev.dupCount, _dupKey: key, ackKeys: prev.ackKeys });
      }
      prev._allAcked = allAcked;
      prev.acked = allAcked;
    } else {
      merged.push(Object.assign({}, r, { dupCount: 1, _dupKey: key, ackKeys: [r.ackKey], _allAcked: !!r.acked }));
    }
  });
  return merged;
}

function buildRow(S, r) {
  const { dom, icon } = S;
  const el = dom.el;
  const iconWrap = el('span', { class: 'sc-inc-row-ico is-' + (r.sevTone || 'mut') }, [icon(r.sevIcon || 'infoCircle', { size: 17 })]);
  const hostEl = el('span', { class: 'sc-inc-row-host u-mono' });
  const sevBadge = el('span', { class: 'u-badge is-' + (r.sevTone || 'mut') });
  const dupBadge = el('span', { class: 'u-badge sc-inc-row-dup', hidden: true });
  // 장기 방치 배지 — 실측상 활성 경보 31건이 전부 10일 이상이었는데 화면엔 신호가 없었다.
  const staleBadge = el('span', { class: 'u-badge is-warn sc-inc-row-stale', hidden: true });
  // 점검 창 배지 — 이 경보의 장비에 점검 모드가 걸려 있으면 표시(카운트·배지에서는 이미 빠짐).
  const maintBadge = el('span', { class: 'u-badge is-warn sc-inc-row-maint', hidden: true });
  const clockIco = icon('clock', { size: 12 });
  const timeEl = el('span', { class: 'sc-inc-row-time-txt' });
  // 시각을 헤더 우측으로 — 풀폭 행의 중앙 대공백을 정보로 상쇄(재심사 반영, rowFoot 제거).
  const timeWrap = el('span', { class: 'sc-inc-row-time u-muted' }, [clockIco, timeEl]);
  const rowHead = el('div', { class: 'sc-inc-row-head' }, [hostEl, sevBadge, dupBadge, staleBadge, maintBadge, timeWrap]);
  const msgEl = el('div', { class: 'sc-inc-row-msg' });
  const body = el('div', { class: 'sc-inc-row-body' }, [rowHead, msgEl]);
  const arrow = el('span', { class: 'sc-inc-row-arrow' }, [icon('chevronDown', { size: 14 })]);
  const row = el('button', { class: 'sc-inc-row', type: 'button', 'data-inc-row': true, 'data-id': r.hostId || r.id }, [iconWrap, body, arrow]);
  // 경보 확인 버튼 — 감사 지적: 경보 31건이 10~41일째 쌓여만 있고 제품 안에서 지울 방법이 없었다.
  // 행(button) 안에 button 을 넣으면 잘못된 마크업이라 형제로 두고 flex 로 나란히 붙인다.
  const ackBtn = el('button', {
    class: 'sc-inc-ack', type: 'button', 'data-action': 'ackAlert', 'data-ack-key': r.ackKey || '',
  });
  const node = el('div', { class: 'sc-inc-row-wrap' }, [row, ackBtn]);
  return { node, row, ackBtn, iconWrap, hostEl, sevBadge, dupBadge, staleBadge, maintBadge, msgEl, timeEl, timeWrap };
}

function renderList(S, ctx, rawList) {
  const { dom, list: listEl } = S;
  const list = mergeDupAlerts(rawList);
  const sig = list.map(rowKey).join('~');
  if (sig !== S.rowSig || S.rowRefs.length !== list.length) {
    // 전량 재생성 전 포커스 기억(#405, nodes.js #347 패턴) — 확인 버튼 클릭은 rowKey 의
    // acked 비트로 서명이 달라져 목록이 통짜 재생성되고, 누르던 버튼이 파괴돼 포커스가
    // body 로 낙하했다. 확인 버튼은 data-ack-key, 행은 data-id 를 기억해 새 DOM 의 같은
    // 대상으로 복원한다(재생성 결과에 대상이 없으면 생략).
    const prevActive = (document.activeElement && listEl.contains(document.activeElement))
      ? document.activeElement : null;
    const prevAckKey = (prevActive && prevActive.getAttribute('data-action') === 'ackAlert')
      ? prevActive.getAttribute('data-ack-key') : null;
    const prevRowId = (prevActive && prevActive.getAttribute('data-inc-row') != null)
      ? prevActive.getAttribute('data-id') : null;
    dom.clear(listEl);
    S.rowRefs = list.map((r) => {
      const row = buildRow(S, r);
      listEl.appendChild(row.node);
      return row;
    });
    S.rowSig = sig;
    if (prevAckKey) {
      const hit = S.rowRefs.find((row) => row.ackBtn.getAttribute('data-ack-key') === prevAckKey);
      if (hit) hit.ackBtn.focus();
    } else if (prevRowId) {
      const hit = S.rowRefs.find((row) => row.row.getAttribute('data-id') === prevRowId);
      if (hit) hit.row.focus();
    }
  }
  list.forEach((r, i) => {
    const row = S.rowRefs[i];
    if (!row) return;
    row.row.setAttribute('data-id', r.hostId || r.id);
    row.iconWrap.className = 'sc-inc-row-ico is-' + (r.sevTone || 'mut');
    row.hostEl.textContent = r.host;
    row.sevBadge.className = 'u-badge is-' + (r.sevTone || 'mut');
    row.sevBadge.textContent = r.sevLabel;
    if (r.dupCount > 1) {
      row.dupBadge.hidden = false;
      row.dupBadge.textContent = '×' + r.dupCount;
      row.dupBadge.title = ctx.L('Same alert reported ' + r.dupCount + ' times — merged', '동일 경보 ' + r.dupCount + '건 병합됨');
    } else {
      row.dupBadge.hidden = true;
    }
    // 카드 본문은 한글 유형 요약(msgLocal)으로, 원문(영문 desc)은 툴팁에 보존한다(E3).
    row.msgEl.textContent = r.msgLocal || r.msg;
    row.msgEl.title = (r.msgLocal && r.msgLocal !== r.msg) ? r.msg : '';
    row.timeEl.textContent = r.ago;
    row.timeWrap.title = r.agoFull || r.time || '';
    // 확인 상태 반영 — 확인된 행은 눌러서 해제할 수 있고, 시각적으로도 가라앉힌다.
    // 병합(×N) 행의 acked 는 mergeDupAlerts 가 '전원 확인' 기준으로 매긴 값이다(#29).
    row.node.classList.toggle('is-acked', !!r.acked);
    row.ackBtn.dataset.ackKey = r.ackKey || '';
    // 병합 행은 멤버 전체 키를 싣는다(␂ 구분, 일괄 바와 같은 계약) — 클릭 위임이 복수 키를 일괄 처리.
    row.ackBtn.dataset.ackKeys = (r.ackKeys || [r.ackKey]).filter(Boolean).join('\u0002');
    row.ackBtn.textContent = r.acked ? ctx.L('Undo', '확인 해제') : ctx.L('Ack', '확인');
    row.ackBtn.setAttribute('aria-label', (r.acked
      ? ctx.L('Remove acknowledgement for ', '확인 해제 — ')
      : ctx.L('Acknowledge alert on ', '경보 확인 — ')) + (r.host || ''));
    row.ackBtn.classList.toggle('is-on', !!r.acked);
    // 오래 방치된 미확인 경보만 표시 — 확인 처리된 건 이미 손을 댄 것이므로 뺀다.
    const showStale = !!r.stale && !r.acked && r.ageDays != null;
    row.staleBadge.hidden = !showStale;
    if (showStale) {
      row.staleBadge.textContent = ctx.L(r.ageDays + 'd unattended', r.ageDays + '일 미확인');
      row.staleBadge.title = ctx.L('No acknowledgement for ' + r.ageDays + ' days',
        r.ageDays + '일째 확인 처리되지 않았습니다');
    }
    // 점검 창 경보 — 배지 + 행 가라앉힘(확인 처리된 행과 같은 취급이지만 의미는 다르다).
    row.maintBadge.hidden = !r.maintWin;
    if (r.maintWin) {
      row.maintBadge.textContent = ctx.L('Maintenance', '점검 중');
      row.maintBadge.title = ctx.L('Device under maintenance — alerts are muted from counts and badges',
        '점검 모드 — 카운트·배지에서 묵음 처리 중입니다');
    }
    row.node.classList.toggle('is-maint', !!r.maintWin);
  });
}

/* ---- 로그 뷰(원시 tail) ---- */
function renderLogView(S, ctx, state, m) {
  const { dom } = S;
  const L = ctx.L;
  // 픽스처 숨김은 모델(compute.js logRows)에서 한 번만 한다 — 여기서 또 거르면
  // 칩 카운트와 목록이 어긋난다(그렇게 만들었다가 '전체 3 / 렌더 1' 불일치가 났다).
  const logsFull = m.logsFull || [];
  const logCountLabel = m.logCountLabel || '';
  const logStatusLabel = m.logStatusLabel || L('Events · alerts + SNMP traps', '이벤트 · 알림 + SNMP 트랩');
  const paused = !!(state.logPaused ?? state.paused);

  // 검색 입력값(포커스 중이면 커서 유지)
  const ph = L('Filter by keyword…', '키워드로 필터링…');
  if (S.searchInput.getAttribute('placeholder') !== ph) S.searchInput.setAttribute('placeholder', ph);
  S.searchInput.setAttribute('aria-label', L('Search logs', '로그 검색'));
  if (document.activeElement !== S.searchInput && S.searchInput.value !== (state.logQuery || '')) {
    S.searchInput.value = state.logQuery || '';
  }
  S.searchClear.hidden = !(state.logQuery && state.logQuery.length);
  // 지우기(×) 접근명 — init 이 구운 값은 그 시점 언어라, 전환 뒤엔 여기서 따라간다(#351, #323 과 같은 배선).
  S.searchClear.setAttribute('aria-label', L('Clear search', '검색 지우기'));

  // Live/Pause
  S.liveIco.classList.toggle('is-pos', !paused);
  S.liveIco.classList.toggle('is-warn', paused);
  S.liveLabel.textContent = paused ? L('Paused', '일시정지') : L('Live', '실시간');
  S.liveBtn.classList.toggle('is-paused', paused);

  // 콘솔 헤더
  S.consoleDot.classList.toggle('is-live', !paused);
  S.consoleDot.classList.toggle('is-paused', paused);
  S.consoleStatus.textContent = paused ? L('Paused', '일시정지됨') + ' · ' + logStatusLabel : logStatusLabel;
  S.consoleCount.textContent = logCountLabel;

  if (!logsFull.length) {
    S.logEmpty.hidden = false;
    S.logList.hidden = true;
    S.logEmptyTitle.textContent = L('No log entries', '로그 항목 없음');
    S.logEmptySub.textContent = (state.logQuery || (state.logLevel && state.logLevel !== 'all'))
      ? L('No entries match this filter.', '이 필터에 해당하는 로그가 없습니다.')
      : L('All quiet on the fleet.', '현재 발생한 이벤트가 없습니다.');
    S.logSig = '';
    return;
  }
  S.logEmpty.hidden = true;
  S.logList.hidden = false;

  const sig = state.lang + '|' + state.logLevel + '|' + state.logQuery + '|' +
    logsFull.map((r) => r.id + '@' + r.time + '@' + (r.trap ? 1 : 0) + '@' + r.msg).join(',');
  if (sig === S.logSig) return;
  S.logSig = sig;

  // 렌더 상한 적용 — 최신 LOG_CAP 건 + 생략 안내 행(계산은 모듈 상단 capLogRows, 순수).
  const { shown, omitted } = capLogRows(logsFull);

  // 행 전량 재생성 전 포커스 기억(#405, 카드 뷰 renderList·nodes.js #347 과 같은 패턴) —
  // 새 로그 유입으로 서명이 달라지면 행이 통짜 재생성돼 포커스가 body 로 낙하했다.
  // 행 키(data-log-row = 장비)를 기억해 새 DOM 의 같은 행으로 복원한다.
  const prevActive = (document.activeElement && S.logList.contains(document.activeElement))
    ? document.activeElement : null;
  const prevLogRow = (prevActive && prevActive.getAttribute('data-log-row') != null)
    ? prevActive.getAttribute('data-log-row') : null;
  dom.clear(S.logList);
  let logFocus = null;
  shown.forEach((r) => {
    // 클릭 시 장비 상세로 가는 행 — div 라 키보드 도달이 불가했다(#32). role=button + tabindex
    // 로 탭 순서에 넣고, Enter/Space 활성화는 init 의 keydown 위임(isLogRowKey)이 담당한다.
    // 스크린리더 이름은 행 텍스트(시각·호스트·레벨·메시지)가 그대로 제공한다.
    const row = dom.el('div', {
      class: 'sc-log-row' + (r.trap ? ' is-trap' : ''),
      'data-log-row': r.hostId || r.id, title: r.agoFull || r.time,
      role: 'button', tabindex: '0',
    }, [
      dom.el('span', { class: 'sc-log-time u-mono', text: r.timeShort || r.time || '' }),
      // G29: 절단 시 복구 경로 — msg 열과 동일하게 title 원문 보존.
      dom.el('span', { class: 'sc-log-host u-mono', text: r.host || '', title: r.host || '' }),
      // 레벨 배지 색 규율(A2): ERROR(neg)만 red, WARN/INFO 는 중립톤(레벨 텍스트로 구분) — '심각만 red' 일관.
      dom.el('span', { class: 'sc-log-level u-mono ' + (r.sevTone === 'neg' ? 'is-neg' : (r.sevTone === 'warn' ? 'is-warn' : 'u-muted')), text: r.level || '' }),
      // G13: 긴 메시지는 1줄 ellipsis 절단 — title 툴팁으로 전문 확인 어포던스 제공.
      dom.el('span', { class: 'sc-log-msg', text: r.msg || r.desc || '', title: r.msg || r.desc || '' }),
    ]);
    if (!logFocus && prevLogRow != null && String(r.hostId || r.id) === prevLogRow) logFocus = row;
    S.logList.appendChild(row);
  });
  if (omitted) {
    S.logList.appendChild(dom.el('div', { class: 'sc-log-row u-muted' }, [
      dom.el('span', {
        class: 'sc-log-msg',
        text: L('… ' + omitted + ' earlier log entries omitted (showing latest ' + LOG_CAP + ')',
          '… 이전 로그 ' + omitted + '건 생략 (최신 ' + LOG_CAP + '건 표시)'),
      }),
    ]));
  }
  if (logFocus) logFocus.focus();
}

// ---------------------------------------------------------------------------
// 통계 뷰(인사이트) + CSV 납품
// ---------------------------------------------------------------------------

export function availBarFrac(availN, worstAvail) {
  const worstDowntime = 100 - worstAvail;
  return worstDowntime > 0 ? (100 - availN) / worstDowntime : 0;
}

/** 반복 경보 Top-N — 계산은 compute.incInsights(순수), 여기선 막대만 그린다. */
function renderStatsView(S, ctx, state, m) {
  const L = ctx.L;
  // 모델 불가 분기(#489)에서 낸 도구바를 성공 경로에서 다시 올린다 — 내리기만 하면
  // 모델 복구 뒤에도 CSV/인쇄가 영구 숨김으로 고착된다(일괄 바 #267 재노출 계약과 동일).
  show(S.statsTools, true);
  const ins = (m && m.incInsights) || {};
  // 서명이 같으면 재생성 생략(틱마다 DOM 을 갈지 않는다 — patch 렌더 계약).
  // 단, oldestUnacked.ageSec 은 벽시계(compute.agoSec)라 매 틱 커져, 미확인 경보 1걸만
  // 있어도 서명이 매 틱 달라져 통계 탭이 폴 주기마다 전량 재생성됐다(#352). 표시는
  // ageDays·host·msg 뿐이라 서명에서만 ageSec 을 제외한다(compute.js 는 그대로 —
  // ageSec 자체는 정렬 판정에 쓰이는 정상 필드다). 모델 객체는 공유 참조라 얕은 복사로 벗긴다.
  const oldSig = ins.oldestUnacked;
  const sigIns = oldSig
    ? Object.assign({}, ins, { oldestUnacked: Object.assign({}, oldSig, { ageSec: 0 }) })
    : ins;
  // c4(SLA 카드)는 rows 의 availN 값을 소비하는데 서명이 length 만 봐서, 장비 수 불변인 채
  // availN 만 drift 하면 재생성이 생략돼 납품 표면에 낡은 SLA 가 고정됐다(#387). 값 기반 요약을
  // 서명에 포함한다 — availN 은 상태의 순수 함수(명목)이거나 poller 실측값이라 ageSec 같은
  // 벽시계 오염이 없어 틱마다 서명이 달라지지는 않는다(#352 계약 유지).
  // c4 는 라벨(r.label || r.host)도 표시하고 availN 동률은 roster 순서(안정 정렬)로 잘라내는데,
  // 서명이 availN 값만 봐서 라벨 변경·동률 순서 변경에도 재생성이 생략돼 낡은 라벨·순서가
  // 고정됐다(#435). 표시 재료인 라벨을 행 순서대로 함께 싣는다 — join 순서가 곧 roster 순서라
  // 순서 변경도 서명을 바꾼다. 라벨·순서 모두 벽시계가 아니라 #352 계약과 공존한다.
  // 모집단은 m.rows(플릿 전체)다 — fleetRows 는 nodesFilter 부분집합이라, 필터가 걸린 채
  // availN 이 drift 하면 서명이 그대로인 허수가 남았다(#434, 카드가 읽는 집합과 동일해야 함).
  const availSig = ((m && m.rows) || []).map((r) => (r.label || r.host) + '=' + r.availN).join(',');
  const sig = JSON.stringify([sigIns, m.rows && m.rows.length, availSig, state.lang]);
  if (S.statsSig === sig) return;
  S.statsSig = sig;

  const dom = S.dom;
  const el = dom.el;
  dom.clear(S.statsBody);

  const card = (title) => {
    const h = el('div', { class: 'sc-inc-stat-h', text: title });
    const body = el('div', { class: 'card-body' }, [h]);
    return el('section', { class: 'card sc-inc-statcard' }, [body]);
  };
  const barRow = (label, value, max, sub, frac) => {
    const pct = frac != null
      ? Math.max(3, Math.round(frac * 100))
      : (max > 0 ? Math.max(3, Math.round(value * 100 / max)) : 0);
    const fill = el('div', { class: 'sc-inc-stat-fill' });
    fill.style.width = pct + '%';
    const lab = el('div', { class: 'sc-inc-stat-lab', text: label });
    lab.title = label;
    const val = el('div', { class: 'u-mono sc-inc-stat-val', text: String(value) + (sub || '') });
    return el('div', { class: 'sc-inc-stat-row' }, [
      el('div', { class: 'sc-inc-stat-top' }, [lab, val]),
      el('div', { class: 'sc-inc-stat-track' }, [fill]),
    ]);
  };
  const emptyNote = (txt) => el('div', { class: 'u-muted sc-inc-stat-empty', text: txt });

  // 1) 반복 경보 Top 5
  const c1 = card(L('Top recurring alerts', '반복 경보 Top 5'));
  const tops = ins.topAlerts || [];
  if (!tops.length) c1.querySelector('.card-body').appendChild(emptyNote(L('No alerts yet', '경보가 없습니다')));
  else tops.forEach((t) => c1.querySelector('.card-body').appendChild(barRow(t.label, t.count, tops[0].count,
    L(' hits', '건'))));

  // 2) 장비별 활성 경보 Top 5
  const c2 = card(L('Top devices by active alerts', '장비별 활성 경보 Top 5'));
  const devs = ins.topDevices || [];
  if (!devs.length) c2.querySelector('.card-body').appendChild(emptyNote(L('No active alerts', '활성 경보가 없습니다')));
  else devs.forEach((d) => c2.querySelector('.card-body').appendChild(
    barRow(d.host + (d.critN ? ' · ' + L('crit ', '심각 ') + d.critN : ''), d.count, devs[0].count, L('', '건'))));

  // 3) 확인 시간(ack latency) + 미확인 최장기
  const c3 = card(L('Acknowledgement & oldest unacked', '확인 시간 · 미확인 최장기'));
  const c3b = c3.querySelector('.card-body');
  const ack = ins.ackStats;
  if (ack) {
    c3b.appendChild(barRow(L('Avg time to ack (' + ack.n + ')', '평균 확인 시간(' + ack.n + '건)'),
      ack.avgH, Math.max(ack.avgH, ack.medH), L('h', '시간')));
    c3b.appendChild(barRow(L('Median time to ack', '중간 확인 시간'), ack.medH, Math.max(ack.avgH, ack.medH), L('h', '시간')));
  } else {
    c3b.appendChild(emptyNote(L('No acknowledged alerts yet — ack some to build latency stats.',
      '아직 확인 처리된 경보가 없습니다 — 확인하면 통계가 쌓입니다.')));
  }
  const old1 = ins.oldestUnacked;
  if (old1) {
    const ageTxt = old1.ageDays != null && old1.ageDays >= 1 ? (old1.ageDays + L('d', '일')) : L('today', '오늘');
    c3b.appendChild(el('div', { class: 'sc-inc-stat-oldest' }, [
      el('span', { class: 'u-badge is-neg', text: L('Oldest unacked', '미확인 최장기') }),
      el('span', { class: 'sc-inc-stat-oldestmsg', text: old1.host + ' — ' + old1.msg + ' (' + ageTxt + ')' }),
    ]));
  }

  // 4) 가용성 하위 5(SLA) — availN 이 가장 낮은 장비부터.
  // 모집단은 m.rows(플릿 전체)다 — fleetRows 는 노드 화면 필터(nodesFilter)가 걸린 부분집합이라,
  // '주의 필요' 등 필터 상태에서 SLA 통계가 플릿 일부만으로 계산돼 침묵 왜곡됐다(#434).
  const c4 = card(L('Lowest availability (SLA)', '가용성 하위 5 (SLA)'));
  const rows = ((m && m.rows) || []).filter((r) => typeof r.availN === 'number')
    .slice().sort((a, b) => a.availN - b.availN).slice(0, 5);
  if (!rows.length) c4.querySelector('.card-body').appendChild(emptyNote(L('No availability data', '가용성 데이터가 없습니다')));
  else {
    const worstAvail = rows[0].availN;
    rows.forEach((r) => c4.querySelector('.card-body').appendChild(
      barRow(r.label || r.host, r.availN, 100, '%', availBarFrac(r.availN, worstAvail))));
  }

  [c1, c2, c3, c4].forEach((c) => S.statsBody.appendChild(c));
}

// CSV 수식(formula) 인젝션 방어(#266) — 네트워크 유래 문자열(장비 라벨·경보·트랩 메시지)이
// =,+,-,@ 로 시작하면 엑셀이 셀을 수식으로 해석한다. BOM 으로 엑셀 소비를 전제하는 납품이므로
// 표준 방어(선행 ' 접두 — 엑셀은 이를 숨기고 텍스트로 읽는다)를 문자열 셀에만 적용한다.
// 숫자 셀(음수 cpu 등)은 수식이 아니라 값으로 파싱되므로 건드리지 않는다.
export function csvSafeCell(v) {
  return (typeof v === 'string' && /^[=+\-@]/.test(v)) ? "'" + v : v;
}

/** CSV 납품 — 계산/이스케이프는 compute.toCsv(순수), 여기선 행 매핑과 다운로드만. */
function exportCsv(S, kind) {
  const m = S._m;
  const ctx = S.ctx;
  if (!m || !ctx || !ctx.model || typeof ctx.model.toCsv !== 'function') return;
  const L = ctx.L;
  let headers = []; let rows = []; let name = 'alerts';
  if (kind === 'alerts') {
    headers = ['host', 'severity', 'message', 'time', 'acked', 'maintenance'];
    rows = (m.alertsAll || []).filter((r) => !r.testFixture)
      .map((r) => [r.host, r.sev, r.msg, r.time, r.acked ? 'Y' : '', r.maintWin ? 'Y' : '']);
  } else if (kind === 'log') {
    name = 'log';
    headers = ['time', 'host', 'kind', 'severity', 'message'];
    rows = (m.logRows || []).filter((r) => !r.testFixture)
      .map((r) => [r.time || r.ts || '', r.host, r.kind || '', r.sev || '', r.msg || r.desc || '']);
  } else if (kind === 'devices') {
    name = 'devices';
    headers = ['id', 'label', 'type', 'site', 'status', 'cpu', 'mem', 'availability'];
    // 납품 모집단은 m.rows(플릿 전체) — fleetRows 는 nodesFilter 부분집합이라, 필터가 걸린 채
    // 납품하면 CSV 가 플릿 일부만 담는 침묵 누락이 생겼다(#434).
    rows = (m.rows || [])
      .map((r) => [r.id, r.label || r.host, r.type, r.site || '', r.status,
        r.cpuNA ? '' : r.cpu0, r.memNA ? '' : r.mem0, r.availN]);
  } else {
    return;
  }
  // 수식 인젝션 중화는 매핑 완료 후 한 곳에서 — 세 종류(alerts/log/devices) 공통(#266).
  rows = rows.map((r) => r.map(csvSafeCell));
  const csv = ctx.model.toCsv(headers, rows);
  const d = new Date();
  const pad = (n) => String(n).padStart(2, '0');
  const fname = 'serverdesk-' + name + '-' + d.getFullYear() + pad(d.getMonth() + 1) + pad(d.getDate())
    + '-' + pad(d.getHours()) + pad(d.getMinutes()) + '.csv';
  // BOM — 엑셀이 한글을 UTF-8 로 바로 읽게 한다.
  const blob = new Blob(['﻿' + csv], { type: 'text/csv;charset=utf-8' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = fname;
  document.body.appendChild(a);
  a.click();
  setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 0);
  try { ctx.showToast(L('CSV exported: ' + fname, 'CSV 납품: ' + fname)); } catch (e) { /* noop */ }
}
