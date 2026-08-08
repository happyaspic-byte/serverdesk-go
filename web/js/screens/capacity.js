// js/screens/capacity.js
// 화면: capacity (용량) — REBUILD-SPEC.md §1.4
// 자원 할당(fleet 전체 vCPU/MEM 게이지) · FT 클러스터별 헤드룸 예측 · CPU/MEM 상위 소비자 랭킹 · 노드별 VM 분포.
// 다른 js/screens/*는 import하지 않는다. ctx.util(dom/svg/fmt/icon), ctx.model(compute 네임스페이스)만 사용.
// CSS는 css/screens/capacity.css의 sc-cap- 접두 클래스만 사용(공용 클래스는 재정의하지 않고 그대로 재사용).

let CTX = null;
let handler = null;
let keyHandler = null;
let mounted = false;

// 리스트 DOM 참조(1회 생성 + patch)
let els = null;      // 구조체: init에서 채움
let rankMap = null;  // key -> {el,...}
let hdMap = null;
let vmMap = null;

// ---------------------------------------------------------------------------
// 리스트 재조정 헬퍼: key 기준으로 엘리먼트를 재사용/재정렬, 빠진 항목만 제거(값 patch, 통짜 재생성 없음)
// ---------------------------------------------------------------------------
function syncList(container, map, items, keyFn, buildFn, updateFn) {
  const seen = new Set();
  items.forEach((item, idx) => {
    const key = keyFn(item);
    seen.add(key);
    let entry = map.get(key);
    if (!entry) {
      entry = buildFn(item);
      map.set(key, entry);
    }
    updateFn(entry, item, idx);
    // 이미 올바른 위치에 있는 행은 건드리지 않는다. 순서가 바뀐 경우에만
    // insertBefore 로 이동해 틱마다 detach/reattach 및 포커스 손실을 피한다.
    if (!container.children || container.children[idx] !== entry.el) {
      if (typeof container.insertBefore === 'function') {
        container.insertBefore(entry.el, container.children[idx] || null);
      } else {
        // 최소 DOM 테스트 더블은 appendChild만 제공한다. 실제 브라우저에서는
        // 위 insertBefore 경로가 사용되어 불필요한 이동을 막는다.
        container.appendChild(entry.el);
      }
    }
  });
  map.forEach((entry, key) => {
    if (!seen.has(key)) {
      entry.el.remove();
      map.delete(key);
    }
  });
}

// ---------------------------------------------------------------------------
// 게이지(donutPair) — 전체 재생성 대신 circle/text 속성만 patch(트랜지션 유지)
// 색은 fmt.barColor(pct) 단일 규칙(78%+=warn·90%+=neg·그 외 ink)만 사용 — donutPair() 최초 생성 시와
// 동일한 함수를 그대로 재사용해 이후 patch 때도 어긋나지 않는다(자체 톤 매핑 없음).
// ---------------------------------------------------------------------------
function patchGauge(resAgg) {
  if (!els.gaugeSvg) return;
  const fmt = CTX.util.fmt;
  const size = 76, stroke = 8;
  const r = (size - stroke) / 2;
  const c = 2 * Math.PI * r;
  // 할당 데이터 없음(resAgg.has===false)은 0% 가 아니라 NA 다 — 중심을 '—' 로 두고 아크는 비운다.
  // '커밋률 0%'(실측)와 '데이터 없음'(결측)이 같은 얼굴이면 §1.6(결측은 NA 로만 표기) 위반.
  // 메트릭별 결측(#262): vcpuHas/memHas 가 false 면 그 메트릭만 NA — 한쪽 결측 집단이
  // 다른 쪽까지 '0%' 로 오표기하던 경로('0 / 0 vCPU')를 막는다.
  const isNA = resAgg.has === false;
  const vcpuNA = isNA || resAgg.vcpuHas === false;
  const memNA = isNA || resAgg.memHas === false;
  const vcpuPct = vcpuNA ? 0 : fmt.clamp(Number(resAgg.vcpuPct) || 0, 0, 100);
  const memPct = memNA ? 0 : fmt.clamp(Number(resAgg.memPct) || 0, 0, 100);
  // 도넛 아크 구성: [track, base, over] × 2(vCPU/MEM) → 인덱스 0..5.
  const circles = els.gaugeSvg.querySelectorAll('circle');
  const texts = els.gaugeSvg.querySelectorAll('text');
  // 커밋(할당) 도넛은 fmt.barColor의 단일 78/90 임계 규칙을 사용한다.
  const setDonut = (base, over, pct) => {
    const arc = CTX.util.svg.allocArcParams(pct, c);
    if (base) {
      base.setAttribute('stroke-dashoffset', arc.baseOffset.toFixed(2));
      base.style.stroke = arc.baseStroke;   /* var() — 다크 자동 */
    }
    if (over) {
      over.style.stroke = arc.overStroke;
      over.setAttribute('stroke-dasharray', arc.overDash.toFixed(2) + ' ' + arc.overGap.toFixed(2));
      over.setAttribute('stroke-dashoffset', arc.overOffset.toFixed(2));
      over.style.display = arc.overShow ? '' : 'none';
    }
  };
  setDonut(circles[1], circles[2], vcpuPct);
  setDonut(circles[4], circles[5], memPct);
  if (texts[0]) texts[0].textContent = vcpuNA ? '—' : Math.round(vcpuPct) + '%';
  if (texts[2]) texts[2].textContent = memNA ? '—' : Math.round(memPct) + '%';
}

// ---------------------------------------------------------------------------
// 랭킹 행(CPU/MEM 부하 상위 소비자)
// ---------------------------------------------------------------------------
function buildRankRow() {
  const dom = CTX.util.dom;
  const icoWrap = dom.el('span', { class: 'sc-cap-rank-ico' });
  const hostEl = dom.el('div', { class: 'sc-cap-rank-host u-nowrap' });
  const typeEl = dom.el('div', { class: 'sc-cap-rank-type u-muted' });
  const numEl = dom.el('span', { class: 'sc-cap-rank-num u-mono u-muted' });
  const fillEl = dom.el('div', { class: 'sc-cap-fill' });
  const valEl = dom.el('span', { class: 'sc-cap-rank-val u-mono' });
  const el = dom.el('div', { class: 'sc-cap-rank-row', 'data-cap-row': '', role: 'button', tabindex: '0' }, [
    numEl,
    icoWrap,
    dom.el('div', { class: 'sc-cap-rank-name' }, [hostEl, typeEl]),
    dom.el('div', { class: 'sc-cap-bar' }, [fillEl]),
    valEl,
  ]);
  return { el, icoWrap, hostEl, typeEl, numEl, fillEl, valEl, iconKey: null };
}

function updateRankRow(entry, item, idx) {
  entry.el.dataset.id = item.id;
  entry.numEl.textContent = String(idx + 1);
  if (entry.iconKey !== item.typeIcon) {
    entry.icoWrap.innerHTML = '';
    entry.icoWrap.appendChild(CTX.util.icon(item.typeIcon, { size: 14 }));
    entry.iconKey = item.typeIcon;
  }
  entry.hostEl.textContent = item.host;
  entry.hostEl.title = item.host;
  entry.typeEl.textContent = item.typeLabel;
  entry.fillEl.style.width = item.width;
  // 임계색 단일 규칙 — 필은 bright(barFill), 텍스트는 base(barColor): §1.4 base/bright 이원.
  entry.fillEl.style.background = CTX.util.fmt.barFill(item.val);
  entry.valEl.textContent = item.text;
  entry.valEl.style.color = CTX.util.fmt.barColor(item.val);
}

// ---------------------------------------------------------------------------
// 헤드룸 행(FT 클러스터 vCPU/MEM 커밋 vs 총량)
// ---------------------------------------------------------------------------
function buildMetricLine(labelText) {
  const dom = CTX.util.dom;
  const valEl = dom.el('span', { class: 'sc-cap-metric-val u-mono' });
  const fillEl = dom.el('div', { class: 'sc-cap-fill' });
  const wrap = dom.el('div', { class: 'sc-cap-metric' }, [
    dom.el('div', { class: 'sc-cap-metric-top' }, [
      dom.el('span', { class: 'sc-cap-metric-label u-muted', text: labelText }),
      valEl,
    ]),
    dom.el('div', { class: 'sc-cap-bar sc-cap-bar--sm' }, [fillEl]),
  ]);
  return { wrap, valEl, fillEl };
}

function buildHeadroomRow() {
  const dom = CTX.util.dom;
  const hostEl = dom.el('div', { class: 'sc-cap-hd-host u-nowrap' });
  const typeEl = dom.el('div', { class: 'sc-cap-hd-type u-muted' });
  const vcpu = buildMetricLine('vCPU');
  const mem = buildMetricLine('MEM');
  const el = dom.el('div', { class: 'sc-cap-hd-row', 'data-cap-row': '', role: 'button', tabindex: '0' }, [
    dom.el('div', { class: 'sc-cap-hd-head' }, [hostEl, typeEl]),
    vcpu.wrap,
    mem.wrap,
  ]);
  return { el, hostEl, typeEl, vcpu, mem };
}

function updateHeadroomRow(entry, item) {
  const fmt = CTX.util.fmt;
  entry.el.dataset.id = item.id;
  // 헤드룸(커밋 vs 총량)도 allocFill의 공용 78/90 임계 규칙으로 도넛·랭킹과 색을 맞춘다.
  const vcpuColor = fmt.allocFill(item.vcpuPct);
  const memColor = fmt.allocFill(item.memPct);
  // A2: 좌측 red 코너아크(is-tight 스트라이프) 제거 — ≥95% 포화는 바의 red 팁(allocFill neg 구간)이
  //     이미 같은 정보를 표기하므로 이중 인코딩을 없애고 바 팁으로 일원화한다.
  entry.hostEl.textContent = item.host;
  entry.hostEl.title = item.host;
  entry.typeEl.textContent = item.typeLabel;
  // 메트릭별 NA(#433): 모델은 u.totVcpu/u.totMem 이 0·결측이면 pct=0 폴핵을 싣는다
  // (compute.js buildCapacityModel) — '0 / 0 (0%)' 은 실측 0% 와 구분되지 않는 오표기다.
  // 결측 쪽만 '—' 로 표기한다(요약 도넛·캡션의 메트릭별 NA(#262)와 같은 계약).
  entry.vcpu.valEl.textContent = item.vcpuTot > 0
    ? item.vcpuUsed + ' / ' + item.vcpuTot + ' (' + item.vcpuPct + '%)'
    : '—';
  entry.vcpu.fillEl.style.width = item.vcpuWidth;
  entry.vcpu.fillEl.style.background = vcpuColor;
  entry.mem.valEl.textContent = item.memTot > 0
    ? item.memUsed + ' / ' + item.memTot + ' GiB (' + item.memPct + '%)'
    : '—';
  entry.mem.fillEl.style.width = item.memWidth;
  entry.mem.fillEl.style.background = memColor;
}

// ---------------------------------------------------------------------------
// VM 분포 행(노드별 실행 VM 수)
// ---------------------------------------------------------------------------
function buildVmRow() {
  const dom = CTX.util.dom;
  const hostEl = dom.el('div', { class: 'sc-cap-vm-host u-nowrap' });
  const nodeEl = dom.el('div', { class: 'sc-cap-vm-node u-muted' });
  // VM 분포 바 — 게이지 기본 블루(§1.35), 부분 실행 앰버·전부 정지 레드는 클래스 오버라이드.
  const fillEl = dom.el('div', { class: 'sc-cap-fill sc-cap-fill--vm' });
  const countEl = dom.el('span', { class: 'u-badge is-mono' });
  const el = dom.el('div', { class: 'sc-cap-vm-row', 'data-cap-row': '', role: 'button', tabindex: '0' }, [
    dom.el('div', { class: 'sc-cap-vm-name' }, [hostEl, nodeEl]),
    dom.el('div', { class: 'sc-cap-bar' }, [fillEl]),
    countEl,
  ]);
  return { el, hostEl, nodeEl, fillEl, countEl };
}

function updateVmRow(entry, item) {
  entry.el.dataset.id = item.id;
  entry.hostEl.textContent = item.host;
  entry.hostEl.title = item.host;
  entry.nodeEl.textContent = item.node;
  // 전 행 동일 문법(재심사 반영): 트랙+좌측 기준 채움을 항상 그린다 — 행마다 바가 있다/없다
  // 갈리던 것(C3/G14 접기)이 '부유 세그먼트'로 오독됐다. 톤: 전부 실행=기본 블루,
  // 부분 실행=앰버, 전부 정지=레드(배지 톤과 동일).
  const uniform = Number(item.running) === Number(item.total);
  entry.fillEl.style.width = item.width;
  const tone = uniform ? '' : (Number(item.running) === 0 ? ' is-neg' : ' is-warn');
  entry.fillEl.className = 'sc-cap-fill sc-cap-fill--vm' + tone;
  entry.countEl.className = 'u-badge is-mono' + tone;
  entry.countEl.textContent = item.running + ' / ' + item.total;
}

// ---------------------------------------------------------------------------
// 카드 뼈대(제목/부제/본문/빈상태) 빌더 — sc-cap- 전용 클래스만 사용, .card는 공용 재사용
// ---------------------------------------------------------------------------
function buildCard(extraClass) {
  const dom = CTX.util.dom;
  const titleEl = dom.el('h2', { class: 'card-title' });
  const subEl = dom.el('span', { class: 'card-sub' });
  const bodyEl = dom.el('div', { class: 'card-body sc-cap-card-body' });
  const emptyIcon = dom.el('div', { class: 'empty-icon' }, [CTX.util.icon('infoCircle', { size: 20 })]);
  const emptyTitle = dom.el('div', { class: 'empty-title' });
  const emptySub = dom.el('div', { class: 'empty-sub' });
  const emptyEl = dom.el('div', { class: 'empty', hidden: true }, [emptyIcon, emptyTitle, emptySub]);
  const card = dom.el('section', { class: 'card' + (extraClass ? ' ' + extraClass : '') }, [
    dom.el('div', { class: 'card-head' }, [titleEl, subEl]),
    bodyEl,
    emptyEl,
  ]);
  return { card, titleEl, subEl, bodyEl, emptyEl, emptyIcon, emptyTitle, emptySub };
}

// ---------------------------------------------------------------------------
// mountScreen — DOM 최초 1회 생성 + 이벤트 위임 1개 등록 (모듈 계약의 init 본체)
// ---------------------------------------------------------------------------
let rootEl = null;

function mountScreen(root, ctx) {
  CTX = ctx;
  rootEl = root;
  const dom = ctx.util.dom;
  const svg = ctx.util.svg;

  rankMap = new Map();
  hdMap = new Map();
  vmMap = new Map();

  // 헤더
  const titleEl = dom.el('h1', { class: 'sc-cap-title' });
  const subEl = dom.el('p', { class: 'sc-cap-sub' });
  const head = dom.el('div', { class: 'sc-cap-head' }, [
    dom.el('div', { class: 'sc-cap-head-text' }, [titleEl, subEl]),
  ]);

  // 요약(게이지 + KPI) 카드
  const gaugeSvg = svg.donutPair(
    { vcpuUsed: 0, vcpuTot: 0, vcpuPct: 0, memUsed: 0, memTot: 0, memPct: 0 },
    { ariaLabel: ctx.L('vCPU and memory allocation chart', 'vCPU 및 메모리 커밋률 차트') },
  );
  // 최초 생성은 값 없음 상태 — donutPair 가 중심을 '0%' 로 그리므로 patchGauge 와 같은 NA 얼굴('—')로
  // 바꿔 둔다(첫 render 의 patchGauge 가 실값 또는 NA 를 다시 기록한다).
  const gaugeTexts = gaugeSvg.querySelectorAll('text');
  if (gaugeTexts[0]) gaugeTexts[0].textContent = '—';
  if (gaugeTexts[2]) gaugeTexts[2].textContent = '—';
  const gaugeCaption = dom.el('div', { class: 'sc-cap-gauge-caption u-mono u-muted' });
  const gaugeWrap = dom.el('div', { class: 'sc-cap-gauge' }, [gaugeSvg, gaugeCaption]);

  // KPI는 헤드룸 중심 2종만: 개요 화면과 겹치는 "실행 VM"(도넛과 함께 개요에 이미 있음 +
  // 이 화면 하단 VM 분포 리스트와도 중복)은 제거하고, 이 화면에서만 의미 있는 지표만 남긴다.
  const kpiTightVal = dom.el('div', { class: 'kpi-val' });
  const kpiTightFoot = dom.el('div', { class: 'kpi-foot' });
  const kpiTightLabel = dom.el('div', { class: 'kpi-label' });
  // '원격측정 커버리지'(2대 플릿에서 2/2 100% 자명)는 대형 KPI 타일에서 헤더 소형 뱃지로 강등한다(minor#7).
  const kpis = dom.el('div', { class: 'kpi-grid sc-cap-kpis' }, [
    dom.el('div', { class: 'kpi' }, [kpiTightLabel, kpiTightVal, kpiTightFoot]),
  ]);

  const summaryTitle = dom.el('h2', { class: 'card-title' });
  // 도넛이 '플릿 합계'임을 명시 — 아래 자원 헤드룸(클러스터별 분해)의 합계로 읽히게 한다(요약→상세).
  const summarySub = dom.el('span', { class: 'card-sub' });
  const covBadge = dom.el('span', { class: 'u-badge is-mono sc-cap-cov' });
  const summaryCard = dom.el('section', { class: 'card sc-cap-summary' }, [
    dom.el('div', { class: 'card-head' }, [summaryTitle, summarySub, covBadge]),
    dom.el('div', { class: 'card-body sc-cap-summary-body' }, [gaugeWrap, kpis]),
  ]);

  // 랭킹 카드(CPU/MEM 탭)
  const rankCard = buildCard('sc-cap-rankcard');
  // 토글 세그먼트 — is-active 만으로는 스크린리더에 눌림 상태가 전달되지 않는다(#430).
  // #389 전례대로 aria-pressed 를 init 에 굽고 render 에서 is-active 와 함께 갱신한다.
  const tabCpu = dom.el('button', { class: 'seg-btn is-active', type: 'button', 'data-cap-tab': 'cpu', 'aria-pressed': 'false', text: 'CPU' });
  const tabMem = dom.el('button', { class: 'seg-btn', type: 'button', 'data-cap-tab': 'mem', 'aria-pressed': 'false', text: 'MEM' });
  const tabs = dom.el('div', { class: 'seg sc-cap-tabs' }, [tabCpu, tabMem]);
  rankCard.card.querySelector('.card-head').appendChild(tabs);
  const rankList = dom.el('div', { class: 'sc-cap-ranklist' });
  rankCard.bodyEl.appendChild(rankList);

  // 헤드룸 카드
  const hdCard = buildCard('sc-cap-hdcard');
  const hdList = dom.el('div', { class: 'sc-cap-hdlist' });
  hdCard.bodyEl.appendChild(hdList);

  // 랭킹 + 헤드룸 2열 그리드
  const grid = dom.el('div', { class: 'sc-cap-grid' }, [rankCard.card, hdCard.card]);

  // VM 분포 카드(전체 폭)
  const vmCard = buildCard('sc-cap-vmcard');
  const vmList = dom.el('div', { class: 'sc-cap-vmlist' });
  vmCard.bodyEl.appendChild(vmList);

  const wrap = dom.el('div', { class: 'sc-cap' }, [head, summaryCard, grid, vmCard.card]);
  root.appendChild(wrap);

  els = {
    titleEl, subEl,
    gaugeSvg, gaugeCaption,
    summaryTitle, summarySub, covBadge,
    kpiTightVal, kpiTightFoot, kpiTightLabel,
    rankCard, tabCpu, tabMem, rankList,
    hdCard, hdList,
    vmCard, vmList,
  };

  handler = (e) => {
    const tabBtn = e.target.closest('[data-cap-tab]');
    if (tabBtn) {
      const val = tabBtn.getAttribute('data-cap-tab');
      const cur = (CTX.store.getState().capMetric === 'mem') ? 'mem' : 'cpu';
      if (val && val !== cur) CTX.store.setState({ capMetric: val });
      return;
    }
    const row = e.target.closest('[data-cap-row]');
    if (row && row.dataset.id) {
      CTX.goDetail(row.dataset.id);
    }
  };
  root.addEventListener('click', handler);

  // 클릭 가능 행(data-cap-row, role=button tabindex=0)의 키보드 활성화 — Enter/Space 로 클릭과
  // 동일 동작(상세 진입). Space 는 preventDefault 로 페이지 스크롤을 막는다(clusters.js 와 동일 패턴).
  keyHandler = (e) => {
    if (e.key !== 'Enter' && e.key !== ' ' && e.key !== 'Spacebar') return;
    const row = e.target.closest && e.target.closest('[data-cap-row]');
    if (row && root.contains(row) && row.dataset.id) {
      e.preventDefault();
      CTX.goDetail(row.dataset.id);
    }
  };
  root.addEventListener('keydown', keyHandler);

  mounted = true;
}

// ---------------------------------------------------------------------------
// patchScreen — store 변경/틱마다 값만 patch (모듈 계약의 render 본체)
// ---------------------------------------------------------------------------
function patchScreen(state, ctx) {
  if (!mounted || !els) return;
  CTX = ctx;
  const L = ctx.L;
  const m = ctx.getModel(state) || {};
  const cap = m.capacity || {};
  const resAgg = m.resAgg || cap.resAgg || {
    has: false, counted: 0, total: 0, vcpuUsed: 0, vcpuTot: 0, vcpuPct: 0, vcpuTone: 'mut',
    memUsed: 0, memTot: 0, memPct: 0, memTone: 'mut',
  };
  const headroom = cap.headroom || [];

  // 도넛은 role=img 이므로 언어 전환 시 접근 이름도 같은 틱에 갱신한다.
  els.gaugeSvg.setAttribute('aria-label', L('vCPU and memory allocation chart', 'vCPU 및 메모리 커밋률 차트'));

  els.titleEl.textContent = L('Capacity', '용량');
  els.subEl.textContent = L('Resource allocation, headroom forecast, and top consumers', '자원 할당 · 헤드룸 예측 · 상위 소비자 랭킹');
  // "리소스 개요"는 개요 화면의 자원/VM 카드와 그대로 겹치던 이름 — 이 화면만의 역할(커밋률·헤드룸)을 이름부터 구분한다.
  els.summaryTitle.textContent = L('Committed capacity', '커밋 용량');
  els.summarySub.textContent = L('Fleet total', '플릿 합계');

  // 게이지 — 개요 화면과 동일한 fleet 합산치지만, 아래 헤드룸 리스트의 합계를 미리 보여주는 앵커 역할로 축소.
  patchGauge(resAgg);
  // 캡션도 메트릭별(#262) — 결측 메트릭은 '0 / 0' 이 아니라 '—'(§1.6: 결측은 NA 로만 표기).
  // vcpuHas/memHas 미납품(구형 모델)은 종전대로 실측 취급(=== false 검사).
  els.gaugeCaption.textContent = resAgg.has
    ? ((resAgg.vcpuHas === false ? '—' : (resAgg.vcpuUsed + ' / ' + resAgg.vcpuTot)) + ' vCPU · '
      + (resAgg.memHas === false ? '—' : (resAgg.memUsed + ' / ' + resAgg.memTot)) + ' GiB')
    : L('No allocation data', '할당 데이터 없음');

  // KPI 2종(헤드룸 중심) — "실행 VM"은 개요 화면·이 화면의 VM 분포 리스트와 이중 중복이라 제거.
  const tight = cap.tight || 0;
  // '긴장'은 사람에게 쓰는 말이라 시스템 상태로는 어색하다 — 자원 여유가 없는 상태는 '포화'다.
  els.kpiTightLabel.textContent = L('Tight clusters', '포화 클러스터');
  // 헤드룸 데이터 전무(0개 클러스터 보고)는 결측이다 — '0' 은 "포화 0건 = 전량 정상" 신호를
  // 낸다(#469). 도넛·캡션의 NA 표기와 맞춰 '—' + 중립 톤으로 둔다(§1.6: 결측은 NA 로만).
  const hasHd = headroom.length > 0;
  els.kpiTightVal.textContent = hasHd ? (tight + ' / ' + headroom.length) : '—';
  els.kpiTightVal.className = 'kpi-val' + (hasHd ? '' : ' u-muted');
  // 어느 클러스터가 포화인지 이름을 병기 — '2/2' 숫자 옆 빈 공간을 실제 대상 정보로 채운다(심사 반영).
  const tn = (cap.tightNames || []).join(' · ');
  els.kpiTightFoot.textContent = tight > 0 && tn
    ? tn + ' — ' + L('≥ 78% committed', '커밋률 78% 이상')
    : L('≥ 78% committed', '커밋률 78% 이상');
  els.kpiTightFoot.className = 'kpi-foot' + (hasHd ? (tight > 0 ? ' is-warn' : ' is-pos') : '');

  // 원격측정 커버리지 — 소형 헤더 뱃지로 강등(minor#7).
  // G11: '41/52 · 79%' 병기 제거 — 79%는 41/52 의 재진술(파생값)이라 분수만 남긴다.
  //     (nodes 의 '연 53분'과 달리 다른 단위로의 유의미 변환이 아님.)
  els.covBadge.textContent = L('Coverage', '커버리지') + ' ' + resAgg.counted + '/' + resAgg.total;

  // 랭킹 카드
  const metric = (state.capMetric === 'mem') ? 'mem' : 'cpu';
  els.tabCpu.classList.toggle('is-active', metric === 'cpu');
  els.tabMem.classList.toggle('is-active', metric === 'mem');
  els.tabCpu.setAttribute('aria-pressed', metric === 'cpu' ? 'true' : 'false');
  els.tabMem.setAttribute('aria-pressed', metric === 'mem' ? 'true' : 'false');
  els.rankCard.titleEl.textContent = L('Load ranking', '부하 랭킹');
  els.rankCard.subEl.textContent = L('Excl. PLC / no telemetry', 'PLC·원격측정 제외');
  const rankRows = metric === 'mem' ? (cap.memRank || []) : (cap.cpuRank || []);
  syncList(els.rankList, rankMap, rankRows, (r) => 'rk:' + r.id, buildRankRow, updateRankRow);
  const rankEmpty = rankRows.length === 0;
  els.rankList.hidden = rankEmpty;
  els.rankCard.emptyEl.hidden = !rankEmpty;
  if (rankEmpty) {
    els.rankCard.emptyTitle.textContent = L('No load data', '부하 데이터 없음');
    els.rankCard.emptySub.textContent = L('All devices are PLC or lack telemetry.', '모든 장비가 PLC이거나 원격측정 데이터가 없습니다.');
  }

  // 헤드룸 카드
  els.hdCard.titleEl.textContent = L('Resource headroom', '자원 헤드룸');
  els.hdCard.subEl.textContent = headroom.length
    ? L(headroom.length + ' clusters', headroom.length + '개 클러스터')
    : '';
  syncList(els.hdList, hdMap, headroom, (r) => 'hd:' + r.id, buildHeadroomRow, updateHeadroomRow);
  const hdEmpty = headroom.length === 0;
  els.hdList.hidden = hdEmpty;
  els.hdCard.emptyEl.hidden = !hdEmpty;
  if (hdEmpty) {
    els.hdCard.emptyTitle.textContent = L('No FT capacity data', 'FT 용량 데이터 없음');
    els.hdCard.emptySub.textContent = L('No FT clusters report unit vCPU/memory allocation.', '유닛 vCPU/메모리 할당을 보고하는 FT 클러스터가 없습니다.');
  }

  // VM 분포 카드 — 비정상(부분 실행) 노드를 먼저 정렬해 위로 올린다(균일 3/3 다수를 아래 카운트 라인으로, C3).
  const vmByNode = (cap.vmByNode || []).slice().sort((a, b) => {
    const ap = (Number(a.running) < Number(a.total)) ? 0 : 1;
    const bp = (Number(b.running) < Number(b.total)) ? 0 : 1;
    return ap - bp;
  });
  els.vmCard.titleEl.textContent = L('VM distribution', 'VM 분포');
  els.vmCard.subEl.textContent = L('Per node', '노드별');
  // 키 재료는 표시명(node)이 아니라 원문 노드명(nodeKey) — 같은 nShort로 정규화되는 원문 다른
  // 두 노드(예: node0와 foo-node0)가 표시명으로 키를 만들면 충돌해 한 행이 소실된다.
  syncList(els.vmList, vmMap, vmByNode, (r) => 'vm:' + r.id + ':' + (r.nodeKey || r.node), buildVmRow, updateVmRow);
  const vmEmpty = vmByNode.length === 0;
  els.vmList.hidden = vmEmpty;
  els.vmCard.emptyEl.hidden = !vmEmpty;
  if (vmEmpty) {
    els.vmCard.emptyTitle.textContent = L('No VM data', 'VM 데이터 없음');
    els.vmCard.emptySub.textContent = L('No node reports running VMs.', 'VM을 보고하는 노드가 없습니다.');
  }
}

// ---------------------------------------------------------------------------
// unmountScreen — 로컬 리스너 해제, 참조 정리 (모듈 계약의 destroy 본체)
// ---------------------------------------------------------------------------
function unmountScreen() {
  if (rootEl && handler) {
    try { rootEl.removeEventListener('click', handler); } catch (e) { /* noop */ }
  }
  if (rootEl && keyHandler) {
    try { rootEl.removeEventListener('keydown', keyHandler); } catch (e) { /* noop */ }
  }
  rootEl = null;
  handler = null;
  keyHandler = null;
  els = null;
  rankMap = null;
  hdMap = null;
  vmMap = null;
  mounted = false;
  CTX = null;
}

export default {
  key: 'capacity',
  title: { en: 'Capacity', ko: '용량' },
  icon: 'db',
  init(root, ctx) { mountScreen(root, ctx); },
  render(state, ctx) { patchScreen(state, ctx); },
  destroy() { unmountScreen(); },
};
