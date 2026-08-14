// js/screens/nodes.js — S2 화면: 전체 노드 (All nodes)
// REBUILD-SPEC.md §1.2(화면 명세) / §5.1(모듈 인터페이스) / §5.5(sc-nd- / data-nd-*)
//
// 관점: "플랫폼/OS" — 노드 단위 CPU/MEM 추이·가동시간·가용성·플랫폼 버전 중심.
// (FT 이중화·sync·VM·라이선스 관점은 clusters 화면이 담당 — 두 화면 중복 제거.)
//
// 소수 장비 적응 레이아웃:
//  - 전체 노드가 6대 미만이면 테이블 대신 카드형(노드당 큰 카드: 상태 배지 + 물리 노드(node0/node1)
//    블록 — 코어/RAM·부하·온도·디스크·VM 타일)으로 전환해 화면을 의미 있게 채운다.
//  - 6대 이상이면 정렬 테이블. 모드 판정은 필터 이전 전체 대수 기준이라
//    필터 조작 시 레이아웃이 흔들리지 않는다.
//
// 원칙
//  - 다른 js/screens/* 는 import 하지 않는다. js/model/*, js/util/* 만 사용(ctx 경유).
//  - init에서 DOM을 1회 생성 + 로컬 이벤트 위임 1개 등록. render는 값만 patch.
//  - 리스트(tbody 행 / 카드)는 데이터 참조(필터/정렬 결과로 만들어지는 id 순서)가 바뀔 때만 재생성한다.

const CARD_MAX = 6; // 전체 노드가 이 값 미만이면 카드형, 이상이면 테이블

// E2: 이 화면의 52 관리단위 = '장비'(manage 기준으로 통일). '노드'는 물리 FT 노드(90)에만 쓴다.
// 'all' 칩만 단위를 명시('전체 장비 52')해 topology '90 물리 노드'와의 오버로드를 끊는다.
const FILTERS = [
  { key: 'all', en: 'All devices', ko: '전체 장비' },
  { key: 'op', en: 'Healthy', ko: '정상' },
  { key: 'deg', en: 'Degraded', ko: '저하' },
  { key: 'down', en: 'Offline', ko: '오프라인' },
  // 개요 '주의 필요' 카드의 '장비 보기' 링크가 여는 필터(down||deg||maint) — compute.js nodesFilters 와 동일 키.
  { key: 'attention', en: 'Attention', ko: '주의 필요' },
];

// sortRows()(compute.js §5)가 이해하는 키만 사용: host|type|site|cpu|mem|sync|avail|uptime
const COLS = [
  { key: 'host', en: 'Device', ko: '장비', num: false },
  { key: 'type', en: 'Platform', ko: '플랫폼', num: false },
  { key: 'site', en: 'Site', ko: '사이트', num: false },
  { key: 'cpu', en: 'CPU', ko: 'CPU', num: true },
  { key: 'mem', en: 'MEM', ko: 'MEM', num: true },
  { key: 'sync', en: 'Sync', ko: 'Sync', num: false },
  { key: 'avail', en: 'Avail', ko: '가용성', num: true },
  { key: 'uptime', en: 'Uptime', ko: 'Uptime', num: true },
];

// tone('pos'|'warn'|'neg'|'info'|'mut') → 공용 텍스트 상태색 클래스. 'mut'만 u-muted로 매핑
// (공용 클래스 재정의 없음, styles.css의 .is-pos/.is-warn/.is-neg/.is-info/.u-muted 재사용).
function toneCls(tone) {
  return tone === 'pos' || tone === 'warn' || tone === 'neg' || tone === 'info' ? 'is-' + tone : 'u-muted';
}

// 사용률(CPU/MEM) 텍스트 톤 → 클래스: 정상(pos)은 상태색이 아니라 잉크로 둔다(정상값 녹색 장식 제거,
// minor#4). 임계 초과만 앰버(warn 78%+)/레드(neg 90%+)로 태워 신호 대비를 살린다.
function usageToneCls(tone) {
  return tone === 'warn' || tone === 'neg' ? 'is-' + tone : (tone === 'mut' ? 'u-muted' : '');
}

/** 물리 노드 API state를 화면 계약으로 정규화한다 — 알 수 없는 원문값을 그대로 노출하지 않는다. */
export function physicalNodeStateLabel(state, L) {
  const key = String(state == null ? '' : state).trim().toLowerCase();
  if (['running', 'online', 'up', 'active', 'ready', 'normal'].includes(key)) return L('Running', '실행 중');
  if (['stopped', 'shutdown', 'offline', 'down', 'halted'].includes(key)) return L('Stopped', '정지');
  if (['starting', 'booting', 'initializing'].includes(key)) return L('Starting', '시작 중');
  if (['stopping', 'shutting-down'].includes(key)) return L('Stopping', '정지 중');
  if (['failed', 'fault', 'error'].includes(key)) return L('Fault', '장애');
  return key ? L('Unknown', '알 수 없음') : '—';
}

/* 장비 → 물리 노드(node0/node1) 표시 행. FT 어플라이언스는 meta.nodes를 펼치고,
 * 그 외 단일 장비는 장비 자체를 노드 1개로 폴백 표기한다.
 * (clusters=FT 이중화 프레이밍, nodes=물리 노드 하드웨어/부하 — 두 화면 역할 분리) */
// 사용률 임계 — ctx.util.fmt 에서 스냅(미주입 시 78/90 폴리).
let _fmtRef = null;
const _uthr = () => (_fmtRef && typeof _fmtRef.usageThresholds === 'function' ? _fmtRef.usageThresholds() : { warn: 78, crit: 90 });

function physNodes(device, r, L) {
  const list = (device && device.meta && Array.isArray(device.meta.nodes)) ? device.meta.nodes : [];
  if (list.length) {
    return list.map((n, i) => {
      const load = Array.isArray(n.loadAvg) && n.loadAvg.length && Number.isFinite(n.loadAvg[0]) ? n.loadAvg[0] : null;
      const reach = n.reachable !== false;
      const maint = /mainten/i.test((n.standing || '') + ' ' + (n.mode || ''));
      const stTone = !reach ? 'neg' : (maint || n.state !== 'running' ? 'warn' : 'pos');
      const temp = Number.isFinite(n.tempMaxC) ? n.tempMaxC : null;
      const fs = Number.isFinite(n.fsMaxPct) ? n.fsMaxPct : null;
      return {
        key: n.name || ('node' + i),
        name: n.name || ('node' + i),
        role: n.primary ? L('Primary', '주 노드') : L('Secondary', '보조 노드'),
        primary: !!n.primary,
        stTone,
        stLabel: !reach ? L('Unreachable', '연결 끊김')
          : (maint ? L('Maintenance', '점검 중')
            : physicalNodeStateLabel(n.state, L)),
        ip: n.ip || '—',
        model: [n.manufacturer, n.model].filter(Boolean).join(' ') || '—',
        cores: n.cores != null ? String(n.cores) : (n.cpus != null ? String(n.cpus) : '—'),
        mem: n.memGiB != null ? n.memGiB + ' GiB' : (n.memory || '—'),
        load: load != null ? load.toFixed(2) : '—',
        temp: temp != null ? temp + '°C' : '—',
        tempTone: temp != null && temp >= 75 ? 'warn' : 'mut',
        fs: fs != null ? fs + '%' : '—',
        // 디스크 사용률 톤 — 게이지 색 단일 규칙(DESIGN-TOKENS §1.35, fmt.js 62-63·compute.js 97-98:
        // ≥78 warn / ≥90 neg)과 정합. 컴포넌트별 임계 하드코딩(85) 금지. 정상은 녹색 장식 대신 mut(minor#4).
        fsTone: fs != null ? (fs >= _uthr().crit ? 'neg' : (fs >= _uthr().warn ? 'warn' : 'mut')) : 'mut',
        vms: n.vmCount != null ? String(n.vmCount) : '—',
      };
    });
  }
  // 폴백: 단일 장비 = 노드 1개
  return [{
    key: r.id, name: r.host, role: '', primary: false,
    stTone: r.statusTone, stLabel: r.statusLabel,
    ip: r.mgmt || r.site || '—', model: '—',
    cores: '—', mem: '—', load: '—', temp: '—', tempTone: 'mut', fs: '—', fsTone: 'mut',
    vms: r.vms != null ? String(r.vms) : '—',
  }];
}

// 물리노드 타일 스펙 [key, EN, KO] — 라벨은 언어 전환 시 _patchCard 에서 다시 써야 하므로
// 모듈 스코프에 두어 _nodeBlock(생성)·_patchCard(갱신)이 같은 정의를 공유한다.
const NODE_TILE_SPECS = [['cores', 'vCPU', 'vCPU'], ['mem', 'RAM', 'RAM'], ['load', 'Load', '부하'],
  ['temp', 'Temp', '온도'], ['fs', 'Disk', '디스크'], ['vms', 'VMs', 'VM']];

export default {
  key: 'nodes',
  title: { en: 'All devices', ko: '전체 장비' },
  icon: 'ops',

  init(root, ctx) {
    const { dom, icon } = ctx.util;
    _fmtRef = ctx.util.fmt;
    const el = dom.el;

    this._ctx = ctx;
    this._root = root;
    this._rowRefs = new Map(); // id -> refs (테이블)
    this._groupRefs = new Map(); // 'co:..'/'fa:..' -> 그룹 헤더 행 refs (G24 트리)
    this._orderKey = null;     // 마지막으로 tbody를 만들 때 사용한 id 순서(join)
    this._cardRefs = new Map(); // id -> refs (카드)
    this._cardOrderKey = null;
    this._mode = null;         // 'cards' | 'table'

    // ---- 헤더(타이틀 + 필터 칩) --------------------------------------------
    this._title = el('h1', { class: 'sc-nd-title' });
    this._sub = el('p', { class: 'sc-nd-sub u-muted' });

    this._filterEls = {};
    const filtersWrap = el('div', { class: 'sc-nd-filters' });
    FILTERS.forEach((f) => {
      const label = el('span', { class: 'sc-nd-chip-label' });
      const count = el('span', { class: 'chip-count' });
      // 토글 칩 — #389 전례대로 aria-pressed 를 init 에 굽고 render 에서 is-active 와 함께 갱신(#430).
      const chip = el('button', { class: 'chip', type: 'button', 'data-nd-filter': f.key, 'aria-pressed': 'false' }, [label, count]);
      this._filterEls[f.key] = { chip, label, count };
      filtersWrap.appendChild(chip);
    });

    const head = el('div', { class: 'sc-nd-head' }, [
      el('div', {}, [this._title, this._sub]),
      filtersWrap,
    ]);

    // ---- 테이블(6대 이상) --------------------------------------------------
    this._sortEls = {};
    const trHead = el('tr', {});
    COLS.forEach((c) => {
      const label = el('span', { class: 'sc-nd-th-label' });
      const icoSlot = el('span', { class: 'table-sort-ico sc-nd-sort-ico' }, [icon('chevronDown', { size: 10 })]);
      // APG sortable table 패턴: th 는 columnheader role 을 유지(aria-sort 는 columnheader 에서만
      // 유효 — role=button 을 th 에 직접 주면 덮여 정렬 상태가 스크린리더에 노출되지 않는다).
      // 조작은 납부의 네이티브 button 이 담당(키보드 Enter/Space 는 click 으로 자연 변환).
      const btn = el('button', {
        type: 'button', class: 'sc-nd-sort-btn', 'data-nd-sort': c.key,
      }, [label, icoSlot]);
      const th = el('th', {
        class: 'is-sortable' + (c.num ? ' is-num' : ''), scope: 'col',
      }, [btn]);
      this._sortEls[c.key] = { th, label, ico: icoSlot };
      trHead.appendChild(th);
    });
    const thead = el('thead', {}, [trHead]);
    this._tbody = el('tbody', {}, []);
    // 캡션은 시각적으로 숨긴다(u-sr-only 류 CSS 클래스가 아직 없어 인라인 clip 기법 사용 — nodes.js
    // 단독 소유 범위 내에서만 처리, CSS 파일은 건드리지 않는다).
    const caption = el('caption', {
      text: ctx.L('Devices and their physical nodes — sortable by column', '장비 및 물리 노드 — 열 기준 정렬 가능'),
      style: {
        position: 'absolute', width: '1px', height: '1px', padding: '0', margin: '-1px',
        overflow: 'hidden', clip: 'rect(0,0,0,0)', whiteSpace: 'nowrap', border: '0',
      },
    });
    // render 의 정적 텍스트 갱신 경로에서 언어 전환마다 다시 써 준다(타일 라벨 self-heal 과 같은 패턴).
    this._caption = caption;
    const table = el('table', { class: 'table sc-nd-table' }, [caption, thead, this._tbody]);
    this._tableWrap = el('div', { class: 'table-wrap sc-nd-table-wrap' }, [table]);

    // ---- 카드(6대 미만) ----------------------------------------------------
    this._cardsWrap = el('div', { class: 'sc-nd-cards' });
    this._cardsEmpty = el('div', { class: 'sc-nd-empty', hidden: true });

    const wrap = el('div', { class: 'sc-nd-wrap' }, [head, this._tableWrap, this._cardsWrap, this._cardsEmpty]);
    dom.clear(root);
    root.appendChild(wrap);

    // ---- 로컬 이벤트 위임(1개) ---------------------------------------------
    this._onClick = (e) => {
      const filterBtn = e.target.closest('[data-nd-filter]');
      if (filterBtn && root.contains(filterBtn)) {
        ctx.store.setState({ nodesFilter: filterBtn.dataset.ndFilter });
        return;
      }
      // G24: 회사/공장 그룹 헤더 행 — 접기/펼치기(스토어 저장 = 화면 이동 후에도 유지).
      const grp = e.target.closest('[data-nd-group]');
      if (grp && root.contains(grp)) {
        const key = grp.dataset.ndGroup;
        const cur = ctx.store.getState().nodesCollapsed || {};
        const next = Object.assign({}, cur);
        next[key] = !next[key];
        ctx.store.setState({ nodesCollapsed: next });
        return;
      }
      const sortTh = e.target.closest('[data-nd-sort]');
      if (sortTh && root.contains(sortTh)) {
        const key = sortTh.dataset.ndSort;
        const cur = (ctx.store.getState().nodesSort) || { key: 'host', dir: 'asc' };
        const dir = cur.key === key && cur.dir === 'asc' ? 'desc' : 'asc';
        ctx.store.setState({ nodesSort: { key, dir } });
        return;
      }
      const card = e.target.closest('[data-nd-card]');
      if (card && root.contains(card)) {
        ctx.goDetail(card.dataset.id);
        return;
      }
      const row = e.target.closest('[data-nd-row]');
      if (row && root.contains(row)) {
        ctx.goDetail(row.dataset.id);
      }
    };
    root.addEventListener('click', this._onClick);

    // 카드(role=button tabindex=0)의 키보드 활성화 — Enter/Space 로 클릭과 동일 동작(상세 진입).
    // 테이블 행(data-nd-row, role=button tabindex=0)도 동일 패턴 — 키보드 사용자가 정렬 테이블에서
    // 마우스 없이 상세로 진입할 수 있게 한다(카드 구현을 그대로 미러링).
    // Space 는 preventDefault 로 페이지 스크롤을 막는다.
    // 그룹 헤더(data-nd-group)도 같은 패턴으로 클릭과 동일 동작을 준다
    // (manage.js 의 data-mng-toggle 키보드 처리와 동일 구조).
    // 정렬 헤더는 th 안의 네이티브 button 이라 Enter/Space 가 click 으로 자연 변환되므로
    // 여기서 처리하지 않는다(처리하면 keydown + click 이중 토글).
    this._onKeydown = (e) => {
      if (e.key !== 'Enter' && e.key !== ' ' && e.key !== 'Spacebar') return;
      const card = e.target.closest('[data-nd-card]');
      if (card && root.contains(card)) {
        e.preventDefault();
        ctx.goDetail(card.dataset.id);
        return;
      }
      const row = e.target.closest('[data-nd-row]');
      if (row && root.contains(row)) {
        e.preventDefault();
        ctx.goDetail(row.dataset.id);
        return;
      }
      const grp = e.target.closest('[data-nd-group]');
      if (grp && root.contains(grp)) {
        e.preventDefault();
        const key = grp.dataset.ndGroup;
        const cur = ctx.store.getState().nodesCollapsed || {};
        const next = Object.assign({}, cur);
        next[key] = !next[key];
        ctx.store.setState({ nodesCollapsed: next });
      }
    };
    root.addEventListener('keydown', this._onKeydown);
  },

  render(state, ctx) {
    if (!this._root) return;
    const L = ctx.L;
    let model = null;
    try {
      model = (ctx.getModel && ctx.getModel(state)) || (ctx.model && ctx.model.buildModel && ctx.model.buildModel(state)) || null;
    } catch (e) { model = null; }

    const fleetRows = (model && model.fleetRows) || [];
    const nodesFilters = (model && model.nodesFilters) || [];
    const nodesSort = (model && model.nodesSort) || state.nodesSort || { key: 'host', dir: 'asc' };
    const nodesFilter = state.nodesFilter || 'all';
    // 카드 모드에서 물리 노드(node0/node1)를 그리기 위한 원천 장비 맵.
    this._devById = new Map((state.fleet || []).map((d) => [String(d.id), d]));

    // 모드 판정: 필터 이전 전체 대수(all count) 기준(필터 조작 시 흔들림 방지)
    const allF = nodesFilters.find((x) => x.key === 'all');
    const total = allF ? allF.count : fleetRows.length;
    const mode = total < CARD_MAX ? 'cards' : 'table';

    // ---- 정적 텍스트(언어 전환마다 갱신) ----
    this._title.textContent = L('All devices', '전체 장비');
    this._sub.textContent = mode === 'cards'
      ? L('Physical nodes (node0 / node1) — load, temperature, disk & VM placement.', '물리 노드(node0 / node1) — 부하·온도·디스크·VM 배치.')
      : L('Sortable inventory of every managed device.', '전체 관리 장비의 정렬 가능한 목록입니다.');
    // 스크린리더용 caption 도 같은 경로에서 갱신 — init 1회 고정이면 진입 시점 언어로 남는다.
    this._caption.textContent = L('Devices and their physical nodes — sortable by column', '장비 및 물리 노드 — 열 기준 정렬 가능');

    FILTERS.forEach((f) => {
      const refs = this._filterEls[f.key];
      const m = nodesFilters.find((x) => x.key === f.key);
      refs.label.textContent = L(f.en, f.ko);
      refs.count.textContent = String(m ? m.count : 0);
      refs.chip.classList.toggle('is-active', m ? !!m.active : nodesFilter === f.key);
      refs.chip.setAttribute('aria-pressed', (m ? !!m.active : nodesFilter === f.key) ? 'true' : 'false');
    });

    COLS.forEach((c) => {
      const refs = this._sortEls[c.key];
      refs.label.textContent = L(c.en, c.ko);
      // G24: 가용성 해석 규칙은 헤더 툴팁 1곳에서 — 행 병기는 편차 행 한정.
      if (c.key === 'avail') refs.th.title = L('Annualized downtime is shown only on degraded/offline rows', '연환산 다운타임은 저하·오프라인 행에만 병기');
      const sorted = nodesSort.key === c.key;
      refs.th.classList.toggle('is-sorted', sorted);
      refs.th.setAttribute('aria-sort', sorted ? (nodesSort.dir === 'desc' ? 'descending' : 'ascending') : 'none');
      refs.ico.classList.toggle('sc-nd-sort-desc', sorted && nodesSort.dir === 'desc');
    });

    // ---- 모드 전환(표시/숨김) ----
    this._tableWrap.hidden = mode !== 'table';
    this._cardsWrap.hidden = mode !== 'cards';
    this._mode = mode;

    if (mode === 'table') {
      this._cardsEmpty.hidden = true;
      this._renderRows(fleetRows, ctx, L, state);
      if (this._emptyTd) this._emptyTd.textContent = L('No devices to display.', '표시할 장비가 없습니다.');
    } else {
      this._renderCards(fleetRows, ctx, L);
    }
  },

  // ======================= 카드 렌더 (물리 노드 관점) =======================
  _renderCards(rowsIn, ctx, L) {
    // 카드 모드 정렬: 타입 위계(FT 쌍 → NAS → 서버류 → 프린터 → PLC) 후 라벨 —
    // 알파벳(내부 key) 순서는 FT 쌍을 찢어놓아 배치가 무의미해진다(사용자 지적).
    const PRIO = { EV: 0, EDGE: 1, END: 2, FTS: 3, NAS: 10, SRV: 20, WIN: 21, PC: 22, PI: 23, PRN: 30, PLC: 40 };
    const rows = rowsIn.slice().sort((a, b) => {
      const p = (PRIO[a.type] != null ? PRIO[a.type] : 50) - (PRIO[b.type] != null ? PRIO[b.type] : 50);
      return p || String(a.label || a.host).localeCompare(String(b.label || b.host), 'ko');
    });
    // 물리 노드 수까지 시그니처에 포함(노드 추가/제거 시 재생성).
    const orderKey = 'C|' + rows.map((r) => {
      const dev = this._devById && this._devById.get(String(r.id));
      return r.id + 'x' + physNodes(dev, r, L).length;
    }).join('|');
    if (orderKey !== this._cardOrderKey) {
      this._rebuildCards(rows, ctx, L);
      this._cardOrderKey = orderKey;
    }
    const empty = !rows.length;
    this._cardsEmpty.hidden = !empty;
    this._cardsWrap.hidden = empty;
    if (empty) {
      this._cardsEmpty.textContent = L('No devices match this filter.', '이 필터에 해당하는 장비가 없습니다.');
      return;
    }
    rows.forEach((r) => {
      const refs = this._cardRefs.get(r.id);
      if (refs) this._patchCard(refs, r, ctx, L);
    });
  },

  _nodeBlock(ctx) {
    const { dom } = ctx.util;
    const el = dom.el;
    const L = ctx.L;
    const name = el('span', { class: 'u-mono sc-nd-node-name' });
    const role = el('span', { class: 'u-badge sc-nd-node-role' });
    const stDot = el('span', { class: 'u-dot' });
    const stLbl = el('span', { class: 'sc-nd-node-stlbl' });
    const sub = el('span', { class: 'u-mono sc-nd-node-sub' });
    const head = el('div', { class: 'sc-nd-node-head' }, [
      name, role,
      el('span', { class: 'u-row sc-nd-node-state' }, [stDot, stLbl]),
    ]);

    const tiles = {};
    const labs = {};
    const grid = el('div', { class: 'sc-nd-node-grid' });
    NODE_TILE_SPECS.forEach(([k, en, ko]) => {
      const v = el('span', { class: 'u-mono sc-nd-tile-val' });
      const lab = el('span', { class: 'sc-nd-tile-lab', text: L(en, ko) });
      tiles[k] = v;
      labs[k] = lab;
      grid.appendChild(el('div', { class: 'sc-nd-tile' }, [lab, v]));
    });

    const block = el('div', { class: 'sc-nd-node' }, [head, sub, grid]);
    return { block, name, role, stDot, stLbl, sub, tiles, labs };
  },

  _rebuildCards(rows, ctx, L) {
    const { dom, icon } = ctx.util;
    const el = dom.el;
    // 테이블 행 재빌드(_rebuildRows, #347)와 같은 계약 — 카드 구성(대수·노드 수) 변화로
    // 카드가 통짜 재생성되면 포커스된 카드도 파괴 activeElement 가 body 로 떨어진다.
    // 재빌드 전 포커스 카드의 data-id 를 기억해 새 DOM 의 같은 카드로 복원한다(#431).
    const prevActive = (document.activeElement && this._cardsWrap.contains(document.activeElement))
      ? document.activeElement : null;
    const prevCardId = (prevActive && prevActive.getAttribute('data-nd-card') != null)
      ? prevActive.getAttribute('data-id') : null;
    dom.clear(this._cardsWrap);
    this._cardRefs.clear();
    if (!rows.length) return;

    rows.forEach((r) => {
      const dot = el('span', { class: 'u-dot' });
      const host = el('span', { class: 'u-mono sc-nd-card-host' });
      const platIco = el('span', { class: 'sc-nd-card-plat-ico' });
      const platLabel = el('span', { class: 'sc-nd-card-plat-label' });
      const stBadge = el('span', { class: 'u-badge sc-nd-card-st' });

      const dev = this._devById && this._devById.get(String(r.id));
      const phys = physNodes(dev, r, L);
      const nodesWrap = el('div', { class: 'sc-nd-nodes' });
      const nodeRefs = phys.map(() => {
        const nb = this._nodeBlock(ctx);
        nodesWrap.appendChild(nb.block);
        return nb;
      });

      const card = el('div', {
        class: 'card sc-nd-card', 'data-nd-card': true, 'data-id': r.id,
        role: 'button', tabindex: '0',
      }, [
        el('div', { class: 'sc-nd-card-head' }, [
          dot,
          el('div', { class: 'sc-nd-card-id u-col' }, [
            host,
            el('div', { class: 'u-row sc-nd-card-plat' }, [platIco, platLabel]),
          ]),
          stBadge,
        ]),
        nodesWrap,
      ]);
      this._cardsWrap.appendChild(card);
      this._cardRefs.set(r.id, {
        card, dot, host, platIco, platLabel, stBadge, nodeRefs, _platIcon: null,
      });
    });

    // 파괴 전 포커스됐던 카드가 새 DOM 에도 있으면 포커스 복원(_rebuildRows 와 같은 계약).
    if (prevCardId) {
      const restored = this._cardRefs.get(prevCardId);
      if (restored) restored.card.focus();
    }
  },

  _patchCard(refs, r, ctx, L) {
    const { icon } = ctx.util;
    refs.card.classList.toggle('is-sel', !!r.sel);

    refs.dot.className = 'u-dot is-' + r.statusTone;
    refs.dot.setAttribute('title', r.host + ' · ' + r.statusLabel);
    // 제목은 내부 key(host)가 아니라 장비 라벨 — 다른 화면과 호칭 통일. 원 key 는 툴팁으로.
    refs.host.textContent = r.label || r.host;
    refs.host.title = r.host;

    if (refs._platIcon !== r.typeIcon) {
      refs.platIco.innerHTML = '';
      refs.platIco.appendChild(icon(r.typeIcon, { size: 13, color: 'var(--muted)' }));
      refs._platIcon = r.typeIcon;
    }
    refs.platLabel.textContent = r.typeLabel + (r.site ? ' · ' + r.site : '');

    refs.stBadge.className = 'u-badge sc-nd-card-st ' + toneCls(r.statusTone);
    refs.stBadge.textContent = r.statusLabel;
    // 콘솔 점검 창(maintWin) — 상태 배지를 '점검 중'으로 대체한다. 장비측 FT 점검과 별개로,
    // 이 창은 운영자가 건 묵음 창이라 상태보다 우선해 보여야 덮인 장비를 다시 만지지 않는다.
    if (r.maintWin) {
      refs.stBadge.className = 'u-badge sc-nd-card-st is-warn';
      refs.stBadge.textContent = L('Maintenance', '점검 중');
    }
    // title은 매 렌더 덮어써야 한다 — 창 해제(maintWin→false) 시 이전 점검 메모가 툴팁에 잔존하지 않게
    // (clusters.js/topology.js와 같은 규약).
    refs.stBadge.title = r.maintWin ? ((r.maintWinInfo && r.maintWinInfo.note) || '') : '';

    const dev = this._devById && this._devById.get(String(r.id));
    const phys = physNodes(dev, r, L);
    (refs.nodeRefs || []).forEach((nb, i) => {
      const p = phys[i];
      if (!p) return;
      // 타일 라벨(부하/온도/디스크/VM…)은 생성 시 1회 기록되므로, 언어 전환 시 여기서 다시 써 준다
      // (카드 재생성은 노드 수 변화에만 일어나 lang 토글로는 orderKey 불변 → self-heal 안 됨).
      if (nb.labs) NODE_TILE_SPECS.forEach(([k, en, ko]) => { if (nb.labs[k]) nb.labs[k].textContent = L(en, ko); });
      nb.name.textContent = p.name;
      nb.role.textContent = p.role || '';
      nb.role.className = 'u-badge sc-nd-node-role' + (p.primary ? ' is-active' : '');
      nb.role.hidden = !p.role;
      nb.stDot.className = 'u-dot ' + toneCls(p.stTone);
      nb.stLbl.textContent = p.stLabel;
      nb.stLbl.className = 'sc-nd-node-stlbl ' + toneCls(p.stTone);
      nb.sub.textContent = [p.ip, p.model].filter((x) => x && x !== '—').join('  ·  ') || '—';
      nb.tiles.cores.textContent = p.cores;
      nb.tiles.mem.textContent = p.mem;
      nb.tiles.load.textContent = p.load;
      nb.tiles.temp.textContent = p.temp;
      nb.tiles.temp.className = 'u-mono sc-nd-tile-val ' + toneCls(p.tempTone);
      nb.tiles.fs.textContent = p.fs;
      nb.tiles.fs.className = 'u-mono sc-nd-tile-val ' + toneCls(p.fsTone);
      nb.tiles.vms.textContent = p.vms;
    });
  },

  // ======================= 테이블 렌더 (6대 이상) =======================
  // G24: 평면 테이블 → 회사 → 공장 → 장비 트리(manage/topology 와 동일 위계, 사용자 지시).
  // 필터·정렬은 장비 행에 그대로 적용(그룹 안 순서 = 현재 정렬), 빈 그룹은 자연 소멸.
  _renderRows(rows, ctx, L, state) {
    const cmp = (ctx.model && ctx.model.cmpKo) || ((a, b) => String(a).localeCompare(String(b)));
    const collapsed = (state && state.nodesCollapsed) || {};
    const UN_CO = L('Unassigned', '미분류');
    const UN_FA = L('Unassigned', '미지정');
    const coMap = new Map();
    rows.forEach((r) => {
      const c = r.company || UN_CO;
      const f = r.factory || UN_FA;
      if (!coMap.has(c)) coMap.set(c, new Map());
      const fm = coMap.get(c);
      if (!fm.has(f)) fm.set(f, []);
      fm.get(f).push(r);
    });
    // 실장비(폴러 meta.topo) 회사 최상단 — 토폴로지(G3)와 동일 규칙.
    const hasReal = (c) => Array.from(coMap.get(c).values()).some((arr2) => arr2.some((r) => {
      const dev = this._devById && this._devById.get(String(r.id));
      return !!(dev && dev.meta && dev.meta.topo);
    }));
    const cos = Array.from(coMap.keys()).sort((a, b) => {
      const ra = hasReal(a) ? 0 : 1;
      const rb = hasReal(b) ? 0 : 1;
      return ra !== rb ? ra - rb : cmp(a, b);
    });
    const worstOf = (arr2) => (arr2.some((r) => r.statusTone === 'neg') ? 'neg'
      : (arr2.some((r) => r.statusTone === 'warn') ? 'warn' : 'pos'));
    const worstLabel = (tone) => tone === 'neg' ? L('Offline', '오프라인')
      : (tone === 'warn' ? L('Degraded', '저하') : L('Healthy', '정상'));
    const entries = [];
    cos.forEach((c) => {
      const fm = coMap.get(c);
      const all = Array.from(fm.values()).reduce((a, b) => a.concat(b), []);
      const coKey = 'co:' + c;
      const coCol = !!collapsed[coKey];
      const coWorst = worstOf(all);
      entries.push({ g: 'co', key: coKey, label: c, count: all.length, worst: coWorst,
        statusLabel: worstLabel(coWorst), open: !coCol });
      if (coCol) return;
      Array.from(fm.keys()).sort(cmp).forEach((f) => {
        const devs = fm.get(f);
        const faKey = 'fa:' + c + '/' + f;
        const faCol = !!collapsed[faKey];
        const faWorst = worstOf(devs);
        entries.push({ g: 'fa', key: faKey, label: f, count: devs.length, worst: faWorst,
          statusLabel: worstLabel(faWorst), open: !faCol });
        if (!faCol) devs.forEach((r) => entries.push({ r }));
      });
    });

    const orderKey = 'T|' + entries.map((e) => (e.g ? e.key + (e.open ? '+' : '-') : e.r.id)).join('|');
    if (orderKey !== this._orderKey) {
      this._rebuildRows(entries, ctx);
      this._orderKey = orderKey;
    }
    entries.forEach((e) => {
      if (e.g) {
        const gr = this._groupRefs.get(e.key);
        if (gr) this._patchGroup(gr, e);
      } else {
        const refs = this._rowRefs.get(e.r.id);
        if (refs) this._patchRow(refs, e.r, ctx, L);
      }
    });
  },

  _patchGroup(gr, e) {
    gr.caret.textContent = e.open ? '▾' : '▸';
    // role=button 토글이므로 펼침 상태를 AT 에 노출한다(manage.js patchGroup 전례).
    gr.tr.setAttribute('aria-expanded', e.open ? 'true' : 'false');
    gr.label.textContent = e.label;
    gr.count.textContent = String(e.count);
    gr.dot.className = 'u-dot is-' + e.worst;
    if (gr.status) gr.status.textContent = e.statusLabel || '';
    gr.tr.setAttribute('aria-label', [e.label, e.statusLabel, e.count].filter(Boolean).join(' — '));
  },

  _rebuildRows(entries, ctx) {
    // 아이콘은 여기서 채우지 않는다 — rebuild 직후 _renderRows가 항상 _patchRow를 호출해
    // 아이콘/텍스트를 포함한 모든 값을 채워 넣는다(중복 방지).
    const { dom } = ctx.util;
    const el = dom.el;
    // 그룹 헤더 토글·CPU/MEM 정렬(틱마다 순서 변동)로 orderKey 가 바뀌어 tbody 가 통짜 재생성되고, 포커스된 tr 도
    // 함께 파괴되어 activeElement 가 body 로 떨어진다 — 재빌드 전 포커스 대상(그룹 헤더 data-nd-group 키, 장비 행 data-id)을
    // 기억해 새 DOM 의 같은 행으로 포커스를 복원한다(manage.js 모달 재빌드 전례).
    const prevActive = (document.activeElement && this._tbody.contains(document.activeElement))
      ? document.activeElement : null;
    const prevKey = prevActive ? prevActive.getAttribute('data-nd-group') : null;
    const prevRowId = (prevActive && prevActive.getAttribute('data-nd-row') != null)
      ? prevActive.getAttribute('data-id') : null;
    dom.clear(this._tbody);
    this._rowRefs.clear();
    this._groupRefs.clear();

    if (!entries.length) {
      const td = el('td', { colspan: String(COLS.length) });
      const tr = el('tr', { class: 'table-empty' }, [td]);
      this._tbody.appendChild(tr);
      this._emptyTd = td;
      return;
    }
    this._emptyTd = null;

    // 그룹 헤더 행(회사/공장) — 클릭 = 접기/펼치기.
    const makeGroup = (e) => {
      const caret = el('span', { class: 'sc-nd-g-caret' });
      const dot = el('span', { class: 'u-dot' });
      const label = el('span', { class: 'sc-nd-g-label' });
      const status = el('span', { class: 'sc-nd-g-status' });
      const count = el('span', { class: 'u-mono sc-nd-g-count' });
      const td = el('td', { colspan: String(COLS.length) }, [
        el('span', { class: 'u-row sc-nd-g-row' }, [caret, dot, label, status, count]),
      ]);
      const tr = el('tr', {
        class: 'sc-nd-group' + (e.g === 'fa' ? ' sc-nd-group--fa' : ''),
        'data-nd-group': e.key,
        // 접기/펼치기 헤더 — 키보드 활성화(_onKeydown 이 Enter/Space 처리, manage.js 토글 패턴).
        // aria-expanded 는 생성 시에도 현재 펼침 상태로 부여하고, 이후 _patchGroup 이 갱신한다.
        role: 'button', tabindex: '0', 'aria-expanded': e.open ? 'true' : 'false',
      }, [td]);
      this._tbody.appendChild(tr);
      this._groupRefs.set(e.key, { tr, caret, dot, label, status, count });
    };

    entries.forEach((e2) => {
      if (e2.g) { makeGroup(e2); return; }
      this._makeDeviceRow(e2.r, el);
    });

    // 파괴 전 포커스됐던 그룹 헤더/장비 행이 새 DOM 에도 있으면 포커스 복원.
    if (prevKey) {
      const restored = this._groupRefs.get(prevKey);
      if (restored) restored.tr.focus();
    } else if (prevRowId) {
      const restoredRow = this._rowRefs.get(prevRowId);
      if (restoredRow) restoredRow.tr.focus();
    }
  },

  _makeDeviceRow(r, el) {
    {
      const dot = el('span', { class: 'u-dot' });
      const host = el('span', { class: 'u-mono sc-nd-host' });
      // 저하/오프라인 행에 상태 텍스트 칩 상시 노출 — 닷 색 단독 구분 제거(색맹 대응, minor#8).
      const stChip = el('span', { class: 'u-badge sc-nd-status', hidden: true });
      const tdNode = el('td', {}, [el('span', { class: 'u-row' }, [dot, host, stChip])]);

      const typeIcoSlot = el('span', { class: 'sc-nd-type-ico' });
      const typeLabel = el('span', { class: 'sc-nd-type-label' });
      const tdType = el('td', {}, [el('span', { class: 'u-row sc-nd-platform' }, [typeIcoSlot, typeLabel])]);

      const site = el('span', { class: 'sc-nd-site' });
      const tdSite = el('td', {}, [site]);

      const cpu = el('td', { class: 'is-num u-mono' });
      const mem = el('td', { class: 'is-num u-mono' });

      const syncIcoSlot = el('span', { class: 'sc-nd-sync-ico' });
      const syncLabel = el('span', { class: 'sc-nd-sync-label' });
      const tdSync = el('td', {}, [el('span', { class: 'u-row sc-nd-sync' }, [syncIcoSlot, syncLabel])]);

      // 가용성 셀: 값(99.99%) + SLA 편차 = 연간 다운타임 예산(연 53분/8.8시간/3.7일). 등급 톤으로
      // 저하/오프라인 행이 도드라지게 한다(E5 — 상태의 순수 함수라 '동일값'처럼 보이던 열에 의미 부여).
      const availVal = el('span', { class: 'u-mono sc-nd-avail-val' });
      const availSub = el('span', { class: 'u-mono sc-nd-avail-sub' });
      const avail = el('td', { class: 'is-num sc-nd-avail' }, [
        el('span', { class: 'sc-nd-avail-cell' }, [availVal, availSub]),
      ]);
      const uptime = el('td', { class: 'is-num u-mono' });

      const tr = el('tr', { 'data-nd-row': true, 'data-id': r.id, role: 'button', tabindex: '0' }, [
        tdNode, tdType, tdSite, cpu, mem, tdSync, avail, uptime,
      ]);
      this._tbody.appendChild(tr);
      this._rowRefs.set(r.id, {
        tr, dot, host, stChip, typeIcoSlot, typeLabel, site, cpu, mem, syncIcoSlot, syncLabel,
        availVal, availSub, uptime,
        _typeIcon: null, _syncIcon: null,
      });
    }
  },

  _patchRow(refs, r, ctx, L) {
    const { icon } = ctx.util;
    refs.tr.classList.toggle('is-sel', !!r.sel);

    refs.dot.className = 'u-dot is-' + r.statusTone;
    refs.dot.setAttribute('title', r.host + ' · ' + r.statusLabel);
    // 키보드/스크린리더용 — 행 활성화(Enter/Space) 시 이동할 대상과 현재 상태를 이름으로 제공
    // (카드 뷰의 role=button tabindex=0 패턴을 테이블 행에도 그대로 미러링).
    refs.tr.setAttribute('aria-label', (r.label || r.host || '') + ' — ' + (r.statusLabel || ''));
    // G24: 그룹 헤더가 회사를 말하므로 행 라벨은 프리픽스를 벗긴 장비코드(manage G8·topo G5 동일 규칙).
    const nmFull = r.label || r.host || '';
    refs.host.textContent = (r.company && nmFull.indexOf(r.company) === 0)
      ? (nmFull.slice(r.company.length).trim() || nmFull) : nmFull;
    refs.host.title = nmFull;

    // 저하(warn)/오프라인(neg) 행은 상태 텍스트 칩을 상시 노출(오프라인 행과 동일 원칙, minor#8).
    // 콘솔 점검 창(maintWin) 행도 상시 노출 — 묵음 창이 걸린 장비를 상태만 보고 건드리지 않게.
    const showChip = r.statusTone === 'warn' || r.statusTone === 'neg' || !!r.maintWin;
    refs.stChip.hidden = !showChip;
    if (showChip) {
      refs.stChip.textContent = r.maintWin ? L('Maintenance', '점검 중') : r.statusLabel;
      refs.stChip.className = 'u-badge sc-nd-status ' + (r.maintWin ? 'is-warn' : toneCls(r.statusTone));
      // title은 매 렌더 덮어써야 한다 — 창 해제(maintWin→false) 시 이전 점검 메모가 툴팁에 잔존하지 않게.
      refs.stChip.title = r.maintWin ? ((r.maintWinInfo && r.maintWinInfo.note) || '') : '';
    }

    if (refs._typeIcon !== r.typeIcon) {
      refs.typeIcoSlot.innerHTML = '';
      refs.typeIcoSlot.appendChild(icon(r.typeIcon, { size: 13, color: 'var(--muted)' }));
      refs._typeIcon = r.typeIcon;
    }
    refs.typeLabel.textContent = r.typeLabel;
    refs.site.textContent = r.site;

    refs.cpu.textContent = r.cpuText;
    refs.cpu.className = ('is-num u-mono ' + usageToneCls(r.cpu ? r.cpu.tone : 'mut')).trim();
    refs.mem.textContent = r.memText;
    refs.mem.className = ('is-num u-mono ' + usageToneCls(r.mem ? r.mem.tone : 'mut')).trim();

    if (refs._syncIcon !== r.syncIcon) {
      refs.syncIcoSlot.innerHTML = '';
      refs.syncIcoSlot.appendChild(icon(r.syncIcon, { size: 12 }));
      refs._syncIcon = r.syncIcon;
    }
    // 색은 예외에만: 정상 sync(pos)는 중립 텍스트 + 아이콘만 색 유지, 이상(simplex/끊김)만 라벨 채색.
    refs.syncIcoSlot.className = 'sc-nd-sync-ico ' + toneCls(r.syncTone);
    refs.syncLabel.textContent = r.syncLabel;
    refs.syncLabel.className = 'sc-nd-sync-label' + (r.syncTone === 'pos' ? '' : ' ' + toneCls(r.syncTone));

    // 값은 등급 톤(정상=중립, 저하=앰버, 오프라인=레드).
    refs.availVal.textContent = r.avail;
    refs.availVal.className = ('u-mono sc-nd-avail-val ' + usageToneCls(r.availTone)).trim();
    // G24(판정 minor): 연환산 다운타임 병기는 값이 달라지는 편차 행(저하/오프라인)에만 —
    //     정상 40여 행의 동일값 반복은 잡음. 해석 규칙은 열 헤더 툴팁이 담당.
    const abnormal = r.statusTone === 'warn' || r.statusTone === 'neg';
    refs.availSub.textContent = abnormal ? (r.availDown || '') : '';
    refs.uptime.textContent = r.uptime;
  },

  destroy() {
    if (this._root && this._onClick) this._root.removeEventListener('click', this._onClick);
    if (this._root && this._onKeydown) this._root.removeEventListener('keydown', this._onKeydown);
    if (this._root) this._root.innerHTML = '';
    this._root = null;
    this._ctx = null;
    this._rowRefs = new Map();
    this._groupRefs = new Map();
    this._orderKey = null;
    this._cardRefs = new Map();
    this._cardOrderKey = null;
    this._mode = null;
    this._title = this._sub = this._tbody = null;
    this._tableWrap = this._cardsWrap = this._cardsEmpty = null;
    this._filterEls = {};
    this._sortEls = {};
    this._emptyTd = null;
  },
};
