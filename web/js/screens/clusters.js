// js/screens/clusters.js
// 화면: clusters — FT 클러스터(EV/EDGE/END/FTS) 읽기전용 목록.
// REBUILD-SPEC.md §1.5 / §5.1 / §5.5. 다른 js/screens/*를 import하지 않는다.
//
// 관점: "FT(내결함성)" — 이중화(노드 페어)·sync(락스텝)·VM 보호·라이선스 중심.
// (노드 단위 CPU/MEM 추이·플랫폼 버전 관점은 nodes 화면이 담당 — 두 화면 중복 제거.)
//
// 소수 장비 적응 레이아웃:
//  - FT 클러스터가 6대 미만이면 테이블 대신 카드형(클러스터당 큰 카드: 상태·이중화 노드 페어·sync·
//    VM(실행/전체)·라이선스·가용성, 하단에 CPU/MEM 미니바)으로 전환해 화면을 의미 있게 채운다.
//  - 6대 이상이면 기존 읽기전용 테이블(시뮬 15대 회귀 방지). 모드 판정은 필터 이전 total 기준.
//
//   clusters = { list[], filters[{key,label,count,active}], counts{all,op,deg,down}, total }
//   list[] 행 필드 = compute.js row 공통 필드 + { nodeCount, vmText, licTxt, licTone }

const CARD_MAX = 6; // FT 클러스터가 이 값 미만이면 카드형, 이상이면 테이블
const FILTER_KEYS = ['all', 'op', 'deg', 'down'];

// 컬럼 정의: [라벨 getter(L), 숫자정렬 여부]. 라벨은 render()에서 매번 다시 굽는다(i18n 토글).
const COLS = [
  [(L) => L('Status', '상태'), false],
  [(L) => L('Cluster', '클러스터'), false],
  [(L) => L('Site', '사이트'), false],
  [() => 'CPU', true],
  [() => 'MEM', true],
  [() => 'Sync', false],
  [() => 'VMs', true],
  // G9: '노드' 컬럼 제거 — FT 는 항상 2노드쌍이라 41행 전부 '2'인 정보량 0 상수 컬럼이었다.
  //     사실은 테이블 부제('2노드 FT쌍')에 1회만 진술한다.
  [(L) => L('Version', '버전'), false],
  [(L) => L('License', '라이선스'), false],
];

// 카드형 타일 라벨 정의: [en, ko]. 카드는 최초 1회 생성 + patch 렌더 계약이라
// 라벨도 COLS 처럼 patch 경로에서 매번 다시 굽는다(언어 토글 갱신 누락 방지, #25).
const TILE_LABELS = [
  ['VMs', '가상머신'],
  ['License', '라이선스'],
  ['Availability', '가용성'],
];

// 상태색 tone(pos/warn/neg/info)을 클래스에 매핑한다. 'mut'(결측) 등은 빈 문자열 → 각 요소의
// 기본(중립) 스타일로 자연스럽게 폴백한다(u-dot/u-badge 기본색이 이미 muted).
function toneClass(tone) {
  if (tone === 'neg') return 'is-neg';
  if (tone === 'warn') return 'is-warn';
  if (tone === 'pos') return 'is-pos';
  if (tone === 'info') return 'is-info';
  return '';
}

// FT 이중화 상태 문구(클러스터 status 기준).
function redunTitle(L, tone) {
  if (tone === 'neg') return L('Protection lost', '보호 중단');
  if (tone === 'warn') return L('Reduced protection', '이중화 저하');
  return L('Fully protected', '이중화 정상');
}

function typeAccessibleLabel(row) {
  return row.typeLabel || row.typeShort || row.type || 'Unknown';
}

// 사용률 warn 임계 — fmt.usageThresholds(서버 정본), 미주입 시 78 폴리.
const _uwarn = (fmt) => (fmt && typeof fmt.usageThresholds === 'function' ? fmt.usageThresholds().warn : 78);

export default {
  key: 'clusters',
  title: { en: 'Clusters', ko: '클러스터' },
  icon: 'link',

  init(root, ctx) {
    const dom = ctx.util.dom;
    const iconFn = ctx.util.icon;

    // 로컬 상태(patch 대상 DOM 참조 보관용, store와 무관한 렌더 캐시)
    const s = {
      root,
      rowMap: new Map(),      // id -> {tr, cells...} (테이블)
      rowOrderKey: '',        // id 순서 시그니처(재생성 판단용)
      cardMap: new Map(),     // id -> refs (카드)
      cardOrderKey: '',
      mode: null,             // 'cards' | 'table'
      filterBtns: new Map(),  // key -> button
    };
    this._s = s;

    const wrap = dom.el('div', { class: 'sc-cl-root' });

    // ── 헤더 ──
    // 공용 page-title 컴포넌트 채택 — 타이틀 시스템 단일 소스 유지(minor#13).
    const titleEl = dom.el('h1', { class: 'page-title sc-cl-title' });
    const head = dom.el('div', { class: 'sc-cl-head' }, [
      dom.el('div', { class: 'sc-cl-head-main' }, [
        titleEl,
        dom.el('div', { class: 'sc-cl-sub', 'data-cl-sub': true, text: '' }),
      ]),
      dom.el('div', { class: 'sc-cl-filters', 'data-cl-filters': true }),
    ]);
    wrap.appendChild(head);

    const filterWrap = head.querySelector('[data-cl-filters]');
    FILTER_KEYS.forEach((key) => {
      // 토글 칩 — #389 전례대로 aria-pressed 를 init 에 굽고 render 에서 is-active 와 함께 갱신(#430).
      const btn = dom.el('button', {
        type: 'button', class: 'chip sc-cl-chip', 'data-cl-filter': key, 'aria-pressed': 'false',
      }, [
        dom.el('span', { 'data-cl-chip-label': true, text: key }),
        dom.el('span', { class: 'chip-count', 'data-cl-chip-count': true, text: '0' }),
      ]);
      filterWrap.appendChild(btn);
      s.filterBtns.set(key, btn);
    });

    // ── 테이블(6대 이상) ──
    const tableWrap = dom.el('div', { class: 'table-wrap sc-cl-table-wrap' });
    const table = dom.el('table', { class: 'table sc-cl-table' });
    const thead = dom.el('thead', {}, [
      dom.el('tr', {}, COLS.map((c) => dom.el('th', { class: c[1] ? 'is-num' : '' }))),
    ]);
    s.thCells = Array.prototype.slice.call(thead.querySelectorAll('th'));
    const tbody = dom.el('tbody', { 'data-cl-tbody': true });
    table.appendChild(thead);
    table.appendChild(tbody);
    tableWrap.appendChild(table);
    wrap.appendChild(tableWrap);

    // ── 카드(6대 미만) ──
    const cardsWrap = dom.el('div', { class: 'sc-cl-cards' });
    wrap.appendChild(cardsWrap);

    // ── 빈 상태 ──
    const emptyTitle = dom.el('div', { class: 'empty-title' });
    const empty = dom.el('div', { class: 'empty sc-cl-empty', 'data-cl-empty': true, hidden: true }, [
      dom.el('div', { class: 'empty-icon' }, [iconFn('link', { size: 20 })]),
      emptyTitle,
      dom.el('div', { class: 'empty-sub', 'data-cl-empty-sub': true }),
    ]);
    wrap.appendChild(empty);

    // ※ 하단 '요약 스트립' 제거(minor#2) — 서브타이틀의 'FT 클러스터 N대'를 그대로 중복했고,
    //   갱신 시각은 좌하단 LIVE 인디케이터가 이미 표시한다.

    root.appendChild(wrap);
    s.tbody = tbody;
    s.tableWrap = tableWrap;
    s.cardsWrap = cardsWrap;
    s.emptyEl = empty;
    s.titleEl = titleEl;
    s.emptyTitle = emptyTitle;
    s.emptySub = empty.querySelector('[data-cl-empty-sub]');
    s.subEl = head.querySelector('[data-cl-sub]');

    // ── 로컬 이벤트 위임(1개) ──
    s.onClick = (e) => {
      const filterBtn = e.target.closest('[data-cl-filter]');
      if (filterBtn && root.contains(filterBtn)) {
        ctx.store.setState({ clustersFilter: filterBtn.dataset.clFilter });
        return;
      }
      const card = e.target.closest('[data-cl-card]');
      if (card && root.contains(card)) {
        ctx.goDetail(card.dataset.id);
        return;
      }
      const row = e.target.closest('[data-cl-row]');
      if (row && root.contains(row)) {
        ctx.goDetail(row.dataset.clRow);
      }
    };
    root.addEventListener('click', s.onClick);

    // 카드(role=button tabindex=0)의 키보드 활성화 — Enter/Space 로 클릭과 동일 동작(상세 진입).
    // 테이블 행(data-cl-row, role=button tabindex=0)도 동일 패턴 — nodes.js 와 같은
    // "행 클릭→상세" 화면 간 키보드 UX 를 맞춘다. Space 는 preventDefault 로 페이지 스크롤을 막는다.
    s.onKeydown = (e) => {
      if (e.key !== 'Enter' && e.key !== ' ' && e.key !== 'Spacebar') return;
      const card = e.target.closest('[data-cl-card]');
      if (card && root.contains(card)) {
        e.preventDefault();
        ctx.goDetail(card.dataset.id);
        return;
      }
      const row = e.target.closest('[data-cl-row]');
      if (row && root.contains(row)) {
        e.preventDefault();
        ctx.goDetail(row.dataset.clRow);
      }
    };
    root.addEventListener('keydown', s.onKeydown);
    // app.js가 init 직후 render를 1회 보장 호출하므로 여기서 별도 호출하지 않는다.
  },

  render(state, ctx) {
    const s = this._s;
    if (!s) return;
    const dom = ctx.util.dom;

    const model = ctx.getModel(state);
    const cl = (model && model.clusters) || { list: [], filters: [], counts: { all: 0, op: 0, deg: 0, down: 0 }, total: 0 };

    const list = cl.list || [];
    const total = cl.total || 0;
    const mode = total < CARD_MAX ? 'cards' : 'table';

    // ── 정적 라벨 i18n patch(언어 토글은 render만 호출되므로 매번 다시 굽는다) ──
    s.titleEl.textContent = ctx.L('Clusters', '클러스터');
    s.emptyTitle.textContent = ctx.L('No FT clusters', '클러스터가 없습니다');
    COLS.forEach((c, i) => {
      const th = s.thCells[i];
      if (!th) return;
      const label = c[0](ctx.L);
      if (th.textContent !== label) th.textContent = label;
    });

    // ── 헤더 서브텍스트 ──
    s.subEl.textContent = mode === 'cards'
      ? ctx.L(
        `${total} FT cluster${total === 1 ? '' : 's'} · redundancy, sync & VM protection`,
        `FT 클러스터 ${total}대 · 이중화·동기화·VM 보호`)
      : ctx.L(
        `${total} FT cluster${total === 1 ? '' : 's'} · 2-node FT pairs · read-only`,
        `FT 클러스터 ${total}대 · 2노드 FT쌍 · 읽기전용`);

    // ── 필터 칩(고정 4키, 값만 patch) ──
    const filterByKey = {};
    (cl.filters || []).forEach((f) => { filterByKey[f.key] = f; });
    FILTER_KEYS.forEach((key) => {
      const btn = s.filterBtns.get(key);
      if (!btn) return;
      const f = filterByKey[key] || { label: key, count: cl.counts ? (cl.counts[key] || 0) : 0, active: state.clustersFilter === key || (!state.clustersFilter && key === 'all') };
      btn.querySelector('[data-cl-chip-label]').textContent = f.label;
      btn.querySelector('[data-cl-chip-count]').textContent = String(f.count);
      btn.classList.toggle('is-active', !!f.active);
      btn.setAttribute('aria-pressed', f.active ? 'true' : 'false');
    });

    // ── 빈 상태 ──
    if (!list.length) {
      s.emptyEl.hidden = false;
      s.tableWrap.hidden = true;
      s.cardsWrap.hidden = true;
      s.emptySub.textContent = total === 0
        ? ctx.L('No fault-tolerant devices are configured.', 'FT 장비가 없습니다.')
        : ctx.L('No devices match this filter.', '이 필터에 해당하는 장비가 없습니다.');
      s.rowMap.forEach((r) => r.tr.remove());
      s.rowMap.clear();
      s.rowOrderKey = '';
      s.cardMap.clear();
      dom.clear(s.cardsWrap);
      s.cardOrderKey = '';
      s.mode = mode;
      return;
    }
    s.emptyEl.hidden = true;

    // ── 모드 전환(표시/숨김) ──
    s.tableWrap.hidden = mode !== 'table';
    s.cardsWrap.hidden = mode !== 'cards';
    s.mode = mode;

    if (mode === 'cards') this._renderCards(list, ctx);
    else this._renderTable(list, ctx);
  },

  // ======================= 카드 렌더 (FT 관점, 6대 미만) =======================
  _renderCards(list, ctx) {
    const s = this._s;
    const dom = ctx.util.dom;
    const iconFn = ctx.util.icon;
    const fmt = ctx.util.fmt;
    const el = dom.el;

    const orderKey = 'C|' + list.map((r) => r.id).join('|');
    if (orderKey !== s.cardOrderKey) {
      // 구성 변경으로 카드가 통짜 재생성되면 포커스된 카드(role=button)가 함께 파괴 activeElement 가
      // body 로 떨어진다 — 재빌드 전 포커스 카드의 data-id 를 기억해 새 DOM 의 같은 카드로 포커스를
      // 복원한다(nodes.js _rebuildRows 의 #347 패턴).
      const prevActive = (document.activeElement && s.cardsWrap.contains(document.activeElement))
        ? document.activeElement : null;
      const prevCardId = (prevActive && prevActive.getAttribute('data-cl-card') != null)
        ? prevActive.getAttribute('data-id') : null;
      dom.clear(s.cardsWrap);
      s.cardMap.clear();
      list.forEach((row) => {
        const ref = {};
        const dot = el('span', { class: 'u-dot' });
        const stBadge = el('span', { class: 'u-badge sc-cl-card-st' });
        const name = el('span', { class: 'sc-cl-card-name' });
        const host = el('span', { class: 'u-mono u-muted sc-cl-card-host' });
        const typeIco = el('span', { class: 'sc-cl-card-type-ico' });

        // FT 이중화 hero: 노드 페어 칩 + 이중화 상태 + sync
        const nodesWrap = el('div', { class: 'sc-cl-ft-nodes' });
        // 클러스터 카드 제목 — 카드가 role=button 이라 후손 h2 는 children-presentational 규칙으로
        // 헤딩 세맨틱이 소멸한다(#488). 시각 제목은 span 으로 둔다(CSS 가 클래스 선택자라 동일) —
        // 헤딩 탐색용 h2 는 카드 밖에 시각적으로 숨겨 따로 둔다(아래 srTitle).
        const ftTitle = el('span', { class: 'sc-cl-ft-title' });
        const ftNote = el('span', { class: 'sc-cl-ft-note' });
        const syncIco = el('span', { class: 'sc-cl-ft-sync-ico' });
        const syncLbl = el('span', { class: 'sc-cl-ft-sync-label' });
        const syncWrap = el('div', { class: 'sc-cl-ft-sync' }, [syncIco, syncLbl]);
        const ftPanel = el('div', { class: 'sc-cl-ft' }, [
          el('div', { class: 'sc-cl-ft-redun' }, [
            nodesWrap,
            el('div', { class: 'u-col sc-cl-ft-redun-txt' }, [ftTitle, ftNote]),
          ]),
          syncWrap,
        ]);

        // 타일: VM / 라이선스 / 가용성 — 라벨 노드는 tileLabs 에 모아 patch 에서 다시 굽는다.
        const vmVal = el('span', { class: 'sc-cl-tile-val u-mono' });
        const licBadge = el('span', { class: 'u-badge sc-cl-tile-badge' });
        const availVal = el('span', { class: 'sc-cl-tile-val u-mono' });
        const tileLabs = [];
        const tile = ([labEn, labKo], valNode) => {
          const lab = el('span', { class: 'sc-cl-tile-label', text: ctx.L(labEn, labKo) });
          tileLabs.push(lab);
          return el('div', { class: 'sc-cl-tile' }, [lab, valNode]);
        };
        const tiles = el('div', { class: 'sc-cl-tiles' }, [
          tile(TILE_LABELS[0], vmVal),
          tile(TILE_LABELS[1], licBadge),
          tile(TILE_LABELS[2], availVal),
        ]);

        // 하단 CPU/MEM 미니바(부차 — 이 화면의 주역은 FT 이중화)
        const cpuFill = el('div', { class: 'sc-cl-bar-fill' });
        const cpuVal = el('span', { class: 'u-mono sc-cl-bar-val' });
        const memFill = el('div', { class: 'sc-cl-bar-fill' });
        const memVal = el('span', { class: 'u-mono sc-cl-bar-val' });
        const loadItem = (labEn, labKo, fill, val) => el('div', { class: 'sc-cl-load-item' }, [
          el('span', { class: 'sc-cl-load-label', text: ctx.L(labEn, labKo) }),
          el('div', { class: 'sc-cl-bar' }, [fill]),
          val,
        ]);
        const load = el('div', { class: 'sc-cl-load' }, [
          loadItem('CPU', 'CPU', cpuFill, cpuVal),
          loadItem('MEM', 'MEM', memFill, memVal),
        ]);

        const card = el('div', {
          class: 'card sc-cl-card', 'data-cl-card': true, 'data-id': row.id,
          role: 'button', tabindex: '0',
        }, [
          el('div', { class: 'sc-cl-card-head' }, [
            dot,
            typeIco,
            el('div', { class: 'u-col sc-cl-card-id' }, [name, host]),
            stBadge,
          ]),
          ftPanel,
          tiles,
          load,
        ]);
        // 헤딩 탐색용 h2 — role=button 카드의 후손은 presentational 처리되어 헤딩이 소멸하므로
        // 카드 밖(cardsWrap 직계)에 둔다(#488). position:absolute 라 그리드 아이템에서 빠져
        // 레이아웃은 불변. 시각 숨김은 nodes.js 캡션 전례의 인라인 clip 기법(CSS 파일 불변).
        const srTitle = el('h2', {
          style: {
            position: 'absolute', width: '1px', height: '1px', padding: '0', margin: '-1px',
            overflow: 'hidden', clip: 'rect(0,0,0,0)', whiteSpace: 'nowrap', border: '0',
          },
        });
        s.cardsWrap.appendChild(srTitle);
        s.cardsWrap.appendChild(card);
        s.cardMap.set(row.id, {
          card, srTitle, dot, stBadge, name, host, typeIco,
          nodesWrap, ftTitle, ftNote, syncIco, syncLbl, syncWrap,
          vmVal, licBadge, availVal, tileLabs, cpuFill, cpuVal, memFill, memVal,
          _typeIcon: null, _syncIcon: null, _nodeCount: -1, _nodeTone: null,
        });
      });
      s.cardOrderKey = orderKey;
      // 파괴 전 포커스됐던 카드가 새 DOM 에도 있으면 포커스 복원.
      if (prevCardId) {
        const restored = s.cardMap.get(prevCardId);
        if (restored) restored.card.focus();
      }
    }

    // ── 값 patch ──
    list.forEach((row) => {
      const c = s.cardMap.get(row.id);
      if (!c) return;
      const tc = toneClass(row.statusTone);

      c.card.classList.toggle('is-sel', !!row.sel);
      // 키보드/스크린리더용 — 테이블 행(#24, _renderTable)과 같은 규약의 간결한 접근 이름.
      // aria-label 이 없으면 role=button 카드의 접근 이름이 카드 전 문구의 연결로 늘어진다(#488).
      c.card.setAttribute('aria-label', (row.label || row.host || '') + ' — ' + (row.statusLabel || ''));
      const typeName = typeAccessibleLabel(row);
      c.typeIco.setAttribute('title', typeName);
      c.typeIco.setAttribute('aria-label', typeName);
      // 카드 밖 h2 의 헤딩 텍스트 — 클러스터 경계 식별용으로 이름만(동적 상태는 aria-label 이 담당).
      c.srTitle.textContent = row.label || row.host || '';
      c.dot.className = 'u-dot ' + tc + (row.anim ? ' ' + row.anim : '');
      c.dot.setAttribute('title', row.host + ' · ' + row.statusLabel);

      if (c._typeIcon !== (row.typeIcon || 'link')) {
        c.typeIco.innerHTML = '';
        c.typeIco.appendChild(iconFn(row.typeIcon || 'link', { size: 16 }));
        c._typeIcon = row.typeIcon || 'link';
      }
      c.name.textContent = row.label;
      c.host.textContent = row.host + (row.site && row.site !== '—' ? ' · ' + row.site : '');

      // 콘솔 점검 창(maintWin) — 상태 배지를 '점검 중'으로 대체(nodes.js/topology.js 와 같은 규약).
      // 실제 장비 상태는 상세 화면에 그대로 남고, 창 사유(note)는 배지 툴팁에 둔다.
      c.stBadge.className = 'u-badge sc-cl-card-st ' + (row.maintWin ? 'is-warn' : tc);
      c.stBadge.textContent = row.maintWin ? ctx.L('Maintenance', '점검 중') : row.statusLabel;
      c.stBadge.title = row.maintWin ? ((row.maintWinInfo && row.maintWinInfo.note) || '') : '';

      // FT 이중화 노드 칩(개수/색 변할 때만 재생성)
      // nodeCount 결측(0/undefined)을 Math.max(1,…)로 '1 nodes'로 둔갑시키면 거짓 정보가 된다 —
      // 결측은 칩 0개 + '노드 정보 없음' 안내로 표기한다.
      const nc = Number.isFinite(row.nodeCount) && row.nodeCount > 0 ? row.nodeCount : 0;
      if (c._nodeCount !== nc || c._nodeTone !== row.statusTone) {
        c.nodesWrap.innerHTML = '';
        for (let i = 0; i < nc; i++) {
          c.nodesWrap.appendChild(el('span', { class: 'sc-cl-ft-node ' + tc }));
        }
        c._nodeCount = nc;
        c._nodeTone = row.statusTone;
      }
      c.ftTitle.className = 'sc-cl-ft-title ' + tc;
      c.ftTitle.textContent = redunTitle(ctx.L, row.statusTone);
      c.ftNote.textContent = nc
        ? nc + ctx.L(' nodes · zero-downtime FT', ' 노드 · 무정지 이중화')
        : ctx.L('No node info', '노드 정보 없음');

      if (c._syncIcon !== (row.syncIcon || 'link')) {
        c.syncIco.innerHTML = '';
        c.syncIco.appendChild(iconFn(row.syncIcon || 'link', { size: 14 }));
        c._syncIcon = row.syncIcon || 'link';
      }
      // 정상 sync 는 중립(좌측 상태 도트가 이미 pos), 이상(simplex/끊김)만 채색 — 색은 예외에만.
      c.syncWrap.className = 'sc-cl-ft-sync ' + (row.syncTone === 'pos' ? '' : toneClass(row.syncTone));
      c.syncLbl.textContent = row.syncLabel;

      c.vmVal.textContent = row.vmText;
      c.licBadge.className = 'u-badge sc-cl-tile-badge ' + toneClass(row.licTone);
      c.licBadge.textContent = row.licTxt;
      // 가용성 타일 — row.avail 은 availDays=0(시뮬·수집 초기)일 때 상태의 순수 함수인
      // 명목값이다(compute rowOf 가 availIsMeasured 로 구분해 납품). 실측처럼 3자리 소수만
      // 보이면 '정밀해 보이는 가짜 값'이 되므로 명목일 때는 꼬리표를 달아 출처를 밝힌다
      // (nodes.js 의 availDown 병기와 같은 취지 — 이 화면 타일은 병기 공간이 없어 꼬리표로).
      c.availVal.textContent = row.availIsMeasured === false
        ? row.avail + ' · ' + ctx.L('nominal', '명목')
        : row.avail;
      c.availVal.title = row.availIsMeasured === false
        ? ctx.L('Nominal value derived from device state — not measured', '장비 상태에서 유도한 명목값 — 실측 아님')
        : '';

      // 타일 라벨 i18n — 언어 토글은 카드 재생성 없이 render 만 다시 부르고(orderKey 불변)
      // 값 patch 에서 함께 다시 굽는다. 미변경 시 대입 생략.
      c.tileLabs.forEach((lab, i) => {
        const t = ctx.L(TILE_LABELS[i][0], TILE_LABELS[i][1]);
        if (lab.textContent !== t) lab.textContent = t;
      });

      // 부하 정량 바 — 잉크 2-톤(allocFill)으로 capacity 와 통일. 녹/앰버 상태 인코딩 제거(C5):
      // 정상값은 중립 잉크, 78%↑ 초과분만 앰버, 95%↑만 neg. 녹색은 상태 도트(u-dot)에만 남긴다.
      c.cpuFill.className = 'sc-cl-bar-fill';
      c.cpuFill.style.width = row.cpu.width;
      c.cpuFill.style.background = fmt.allocFill(row.cpu.val);
      if (c.cpuVal) c.cpuVal.style.color = row.cpu.val >= _uwarn(fmt) ? fmt.barColor(row.cpu.val) : '';
      c.cpuVal.textContent = row.cpuText;
      c.memFill.className = 'sc-cl-bar-fill';
      c.memFill.style.width = row.mem.width;
      c.memFill.style.background = fmt.allocFill(row.mem.val);
      if (c.memVal) c.memVal.style.color = row.mem.val >= _uwarn(fmt) ? fmt.barColor(row.mem.val) : '';
      c.memVal.textContent = row.memText;
    });
  },

  // ======================= 테이블 렌더 (6대 이상, 시뮬 15대) =======================
  _renderTable(list, ctx) {
    const s = this._s;
    const dom = ctx.util.dom;
    const iconFn = ctx.util.icon;
    const fmt = ctx.util.fmt;

    // ── 행 재생성 여부(순서/구성 변경 시에만 통짜 재생성) ──
    const orderKey = list.map((r) => r.id).join('|');
    if (orderKey !== s.rowOrderKey) {
      // 카드 경로와 같은 #347 패턴 — 재빌드로 파괴되는 포커스 행(role=button)의 data-cl-row 키를
      // 기억해 새 DOM 의 같은 행으로 포커스를 복원한다.
      const prevActive = (document.activeElement && s.tbody.contains(document.activeElement))
        ? document.activeElement : null;
      const prevRowId = prevActive ? prevActive.getAttribute('data-cl-row') : null;
      dom.clear(s.tbody);
      s.rowMap.clear();
      list.forEach((row) => {
        const cellsRef = {};
        // 행 클릭→상세 패턴은 nodes 테이블과 동일 — role/tabindex 도 맞춰 키보드 접근을 보장한다
        // (keydown 위임이 Enter/Space 를 클릭과 동일 동작으로 처리).
        const tr = dom.el('tr', { 'data-cl-row': row.id, role: 'button', tabindex: '0' });

        // 상태
        const tdStatus = dom.el('td', {}, [
          dom.el('span', { class: 'u-row u-gap-xs' }, [
            dom.el('span', { class: 'u-dot ' + toneClass(row.statusTone) + (row.anim ? ' ' + row.anim : '') }),
            dom.el('span', { class: 'sc-cl-status-label', text: row.statusLabel }),
          ]),
        ]);
        cellsRef.status = tdStatus.querySelector('.sc-cl-status-label');
        cellsRef.dot = tdStatus.querySelector('.u-dot');

        // 이름
        const typeName = typeAccessibleLabel(row);
        const typeIco = dom.el('span', {
          class: 'sc-cl-type-ico', title: typeName, 'aria-label': typeName,
        }, [iconFn(row.typeIcon || 'link', { size: 16 })]);
        const tdName = dom.el('td', {}, [
          dom.el('div', { class: 'u-row u-gap-sm' }, [
            typeIco,
            dom.el('div', { class: 'u-col' }, [
              dom.el('span', { class: 'sc-cl-name', text: row.label }),
              dom.el('span', { class: 'u-mono u-muted sc-cl-host', text: row.host }),
            ]),
          ]),
        ]);
        cellsRef.name = tdName.querySelector('.sc-cl-name');
        cellsRef.host = tdName.querySelector('.sc-cl-host');
        cellsRef.typeIco = typeIco;

        // 사이트
        const tdSite = dom.el('td', { class: 'sc-cl-site', text: row.site });
        cellsRef.site = tdSite;

        // CPU
        const tdCpu = dom.el('td', { class: 'is-num' }, [dom.el('div', { class: 'sc-cl-bar-wrap' }, [
          dom.el('div', { class: 'sc-cl-bar' }, [dom.el('div', { class: 'sc-cl-bar-fill ' + toneClass(row.cpu.tone) })]),
          dom.el('span', { class: 'u-mono sc-cl-bar-val', text: row.cpuText }),
        ])]);
        cellsRef.cpuFill = tdCpu.querySelector('.sc-cl-bar-fill');
        cellsRef.cpuVal = tdCpu.querySelector('.sc-cl-bar-val');

        // MEM
        const tdMem = dom.el('td', { class: 'is-num' }, [dom.el('div', { class: 'sc-cl-bar-wrap' }, [
          dom.el('div', { class: 'sc-cl-bar' }, [dom.el('div', { class: 'sc-cl-bar-fill ' + toneClass(row.mem.tone) })]),
          dom.el('span', { class: 'u-mono sc-cl-bar-val', text: row.memText }),
        ])]);
        cellsRef.memFill = tdMem.querySelector('.sc-cl-bar-fill');
        cellsRef.memVal = tdMem.querySelector('.sc-cl-bar-val');

        // Sync — 아이콘은 슬롯에 넣고 참조를 보관한다: tickFleet 이 매 틱 dev.sync 를
        // 재계산하므로 patch 경로에서도 아이콘을 갱신해야 라벨·색과 모순되지 않는다
        // (카드 경로·nodes.js 의 syncIcoSlot 패턴과 동일).
        const syncIcoSlot = dom.el('span');
        syncIcoSlot.appendChild(iconFn(row.syncIcon || 'link', { size: 14 }));
        const tdSync = dom.el('td', {}, [
          dom.el('span', { class: 'u-row u-gap-xs ' + (row.syncTone === 'pos' ? '' : toneClass(row.syncTone)) }, [
            syncIcoSlot,
            dom.el('span', { class: 'sc-cl-sync-label', text: row.syncLabel }),
          ]),
        ]);
        cellsRef.syncIco = syncIcoSlot;
        cellsRef._syncIcon = row.syncIcon || 'link';
        cellsRef.syncLabel = tdSync.querySelector('.sc-cl-sync-label');
        cellsRef.syncWrap = tdSync.querySelector('.u-row');

        // VMs
        const tdVms = dom.el('td', { class: 'is-num u-mono sc-cl-vms', text: row.vmText });
        cellsRef.vms = tdVms;

        // 버전
        const tdVer = dom.el('td', { class: 'u-mono sc-cl-ver', text: row.version || '—' });
        cellsRef.ver = tdVer;

        // 라이선스
        const tdLic = dom.el('td', {}, [
          dom.el('span', { class: 'u-badge ' + toneClass(row.licTone), text: row.licTxt }),
        ]);
        cellsRef.licBadge = tdLic.querySelector('.u-badge');

        tr.appendChild(tdStatus);
        tr.appendChild(tdName);
        tr.appendChild(tdSite);
        tr.appendChild(tdCpu);
        tr.appendChild(tdMem);
        tr.appendChild(tdSync);
        tr.appendChild(tdVms);
        tr.appendChild(tdVer);
        tr.appendChild(tdLic);

        s.tbody.appendChild(tr);
        cellsRef.tr = tr;
        s.rowMap.set(row.id, cellsRef);
      });
      s.rowOrderKey = orderKey;
      // 파괴 전 포커스됐던 행이 새 DOM 에도 있으면 포커스 복원.
      if (prevRowId) {
        const restoredRow = s.rowMap.get(prevRowId);
        if (restoredRow) restoredRow.tr.focus();
      }
    }

    // ── 값 patch(트랜지션 리셋 방지) ──
    list.forEach((row) => {
      const c = s.rowMap.get(row.id);
      if (!c) return;
      c.tr.classList.toggle('is-sel', !!row.sel);
      // 키보드/스크린리더용 — 행 활성화(Enter/Space) 시 이동할 대상과 현재 상태를 이름으로 제공
      // (nodes.js 테이블 행 패턴 미러링).
      c.tr.setAttribute('aria-label', (row.label || row.host || '') + ' — ' + (row.statusLabel || ''));
      const typeName = typeAccessibleLabel(row);
      c.typeIco.setAttribute('title', typeName);
      c.typeIco.setAttribute('aria-label', typeName);

      c.dot.className = 'u-dot ' + toneClass(row.statusTone) + (row.anim ? ' ' + row.anim : '');
      // 정상행은 상태 도트만(nodes 무배지 처리와 통일) — 저하/오프라인만 라벨 노출.
      // 콘솔 점검 창(maintWin) 행은 톤과 무관하게 '점검 중'으로 대체 노출(nodes.js/topology.js 규약).
      c.status.textContent = row.maintWin ? ctx.L('Maintenance', '점검 중')
        : (row.statusTone === 'pos' ? '' : row.statusLabel);
      c.status.title = row.maintWin ? ((row.maintWinInfo && row.maintWinInfo.note) || '') : '';

      c.name.textContent = row.label;
      c.host.textContent = row.host;
      c.site.textContent = row.site;

      // 부하 정량 바 — 잉크 2-톤(allocFill)으로 통일, 녹/앰버 상태 인코딩 제거(C5).
      c.cpuFill.className = 'sc-cl-bar-fill';
      c.cpuFill.style.width = row.cpu.width;
      c.cpuFill.style.background = fmt.allocFill(row.cpu.val);
      if (c.cpuVal) c.cpuVal.style.color = row.cpu.val >= _uwarn(fmt) ? fmt.barColor(row.cpu.val) : '';
      c.cpuVal.textContent = row.cpuText;

      c.memFill.className = 'sc-cl-bar-fill';
      c.memFill.style.width = row.mem.width;
      c.memFill.style.background = fmt.allocFill(row.mem.val);
      if (c.memVal) c.memVal.style.color = row.mem.val >= _uwarn(fmt) ? fmt.barColor(row.mem.val) : '';
      c.memVal.textContent = row.memText;

      if (c._syncIcon !== (row.syncIcon || 'link')) {
        c.syncIco.innerHTML = '';
        c.syncIco.appendChild(iconFn(row.syncIcon || 'link', { size: 14 }));
        c._syncIcon = row.syncIcon || 'link';
      }
      c.syncWrap.className = 'u-row u-gap-xs ' + (row.syncTone === 'pos' ? '' : toneClass(row.syncTone));
      c.syncLabel.textContent = row.syncLabel;

      c.vms.textContent = row.vmText;
      c.ver.textContent = row.version || '—';

      c.licBadge.className = 'u-badge ' + toneClass(row.licTone);
      c.licBadge.textContent = row.licTxt;
    });
  },

  destroy() {
    const s = this._s;
    if (s && s.root && s.onClick) {
      s.root.removeEventListener('click', s.onClick);
      if (s.onKeydown) s.root.removeEventListener('keydown', s.onKeydown);
    }
    if (s) {
      s.rowMap.clear();
      s.cardMap.clear();
      s.filterBtns.clear();
    }
    this._s = null;
  },
};
