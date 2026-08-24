/* =============================================================================
 * js/screens/manage.js — 장비 관리 (Manage)
 * REBUILD-SPEC §1.8 / §5.1 / §5.5
 *
 * 회사 ▸ 공장 ▸ 장비 3단 접이식 트리 + 추가 위저드(4단계) + 수정 모달(4탭) + 삭제 확인.
 * 실장비는 /api/clusters를 통해 config에 저장한다.
 *
 * CSS 접두사: sc-mng-   /   data 훅: data-mng-*
 * 의존: js/model/*, js/util/* 만 (다른 screens 미import)
 * ========================================================================== */

import { TYPE_KEYS } from '../model/data.js';

const DASH = '—';

function isSampleState(state) {
  const st = state || {};
  return st.sampleMode === true || st.demoMode === true
    || st.source === 'sample' || st.source === 'demo';
}

/* ── 타입 카드(위저드 ①) 정의 — Vigil ManageScreen TYPES 이식 ────────────── */
function typeDefs(L) {
  return [
    ['EV', 'everRun'], ['EDGE', 'ztC Edge'], ['END', 'ztC Endurance'], ['FTS', 'ftServer'],
    ['SRV', L('General server', '일반 서버')], ['PLC', 'PLC'],
    ['PC', L('PC / Workstation', 'PC / 워크스테이션')], ['NAS', 'Synology NAS'],
    ['WIN', L('Windows Server', 'Windows 서버')], ['PI', 'Raspberry Pi'],
    ['PRN', L('Printer', '프린터')],
  ];
}
function platName(t, L) {
  const d = typeDefs(L).find((x) => x[0] === t);
  return d ? d[1] : 'everRun';
}
const FT_SET = { EV: 1, EDGE: 1, END: 1, FTS: 1 };
const isFTType = (t) => !!FT_SET[t];

const ICON_BY_TYPE = {
  EV: 'box', EDGE: 'link', END: 'db', FTS: 'ssd', SRV: 'db',
  PLC: 'crop', PC: 'dash', NAS: 'ssd', WIN: 'checklist', PI: 'bolt',
};
const KEY_PREFIX = {
  EV: 'ev', EDGE: 'edge', END: 'end', FTS: 'fts', SRV: 'srv',
  PLC: 'plc', PC: 'pc', NAS: 'nas', WIN: 'win', PI: 'pi', PRN: 'prn',
};

/* '필수' 배지가 달리는 인증 필드 — authGroup 의 req:true 조건과 동일 조건을 검증에도 쓴다
   (배지를 고칠 때 이 목록도 함께 고칠 것). [form 키, 비밀번호형 여부, 비었을 때 오류(en, ko)] */
function reqCredFields(t, platform) {
  const F = [];
  if (isFTType(t)) F.push(
    ['admin_user', 0, 'AC admin user is required', 'AC 관리자 ID는 필수입니다'],
    ['admin_pass', 1, 'AC password is required', 'AC 비밀번호는 필수입니다'],
  );
  if (t === 'WIN') F.push(
    ['admin_user', 0, 'Windows admin user is required', 'Windows 관리자 ID는 필수입니다'],
    ['admin_pass', 1, 'Windows password is required', 'Windows 비밀번호는 필수입니다'],
  );
  if (t === 'PI') F.push(
    ['admin_user', 0, 'SSH user is required', 'SSH 사용자는 필수입니다'],
    ['root_pass', 1, 'SSH password is required', 'SSH 비밀번호는 필수입니다'],
  );
  if (t === 'SRV' && platform === 'proxmox') F.push(
    ['admin_user', 0, 'PVE user is required', 'PVE 계정은 필수입니다'],
    ['admin_pass', 1, 'PVE password is required', 'PVE 비밀번호는 필수입니다'],
  );
  // 연결 탭 'BMC / iLO IP *' 배지 조건(connGroup 의 redfish/both 분기)과 동일 조건.
  if (t === 'SRV' && (platform === 'redfish' || platform === 'both')) F.push(
    ['bmc_ip', 0, 'BMC / iLO IP is required', 'BMC / iLO IP는 필수입니다'],
  );
  if (t === 'NAS' || (t === 'SRV' && !platform)) F.push(
    ['community', 0, 'SNMP community is required', 'SNMP 커뮤니티는 필수입니다'],
  );
  return F;
}

// 모달 포커스 트랩용 — 화면에 보이는(offsetParent 존재) 포커스 가능 요소만 수집한다.
// (요구사항: 장비 추가/수정/삭제 모달 role=dialog 접근성 — Tab 순환·포커스 이동·Esc 취소)
const FOCUSABLE_SEL = 'a[href], button:not([disabled]), input:not([disabled]), '
  + 'select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
function getFocusable(container) {
  if (!container) return [];
  return Array.from(container.querySelectorAll(FOCUSABLE_SEL))
    .filter((n) => n.offsetParent !== null || n === document.activeElement);
}

/* "Name=라벨(단위)" 토큰 파싱 (PLC 공정 변수) */
function parseTags(s) {
  return String(s || '').split(',').map((t) => t.trim()).filter(Boolean).map((tok) => {
    const mm = tok.match(/^([^=]+?)\s*(?:=\s*([^(]*?)\s*(?:\(([^)]*)\))?)?$/);
    return mm ? { name: mm[1].trim(), label: (mm[2] || '').trim(), unit: (mm[3] || '').trim() } : null;
  }).filter((t) => t && t.name);
}

/** Pure helpers used by the destructive confirmation and Node regression tests. */
export function deleteImpact(state, id) {
  const st = state || {};
  const target = String(id || '');
  const prefix = target + '\u0001';
  return {
    target,
    acknowledgements: Object.keys(st.ackedAlerts || {}).filter((key) => key === target || key.indexOf(prefix) === 0).length,
    maintenanceWindow: !!(st.maint && st.maint[target]),
    note: !!(st.notes && st.notes[target]),
  };
}

export function validateDeleteConfirmation(form, L = (en) => en) {
  const f = form || {};
  const reason = String(f.delete_reason || '').trim();
  if (!reason) {
    return { field: 'delete_reason', message: L('Enter a reason.', '사유를 입력하세요.') };
  }
  if (Array.from(reason).length > 500) {
    return { field: 'delete_reason', message: L('Use 500 characters or fewer.', '사유는 500자 이하로 입력하세요.') };
  }
  if (String(f.delete_phrase || '').trim() !== String(f.origKey || '')) {
    return { field: 'delete_phrase', message: L('Type the exact device ID to confirm.', '확인을 위해 장비 ID를 정확히 입력하세요.') };
  }
  return null;
}

function emptyForm() {
  return {
    mode: 'add', origKey: '', _rev: 0,
    key: '', label: '', type: 'EV', company: '', factory: '', site: 'LAN', mgmt: '',
    node0: '', node1: '', node0_user: 'root', node0_pass: '', node1_user: 'root', node1_pass: '',
    admin_user: 'admin', admin_pass: '', community: 'public', root_pass: '',
    vms: [{ name: '', ip: '' }],
    vendor: '', bmc_ip: '', model: '', asset_tag: '', floor_pos: '', api: '', api_orig: '', tags_text: '',
    win_user: '', win_pass: '',
    tab: 'basic', showPw: {}, err: null, fieldErrors: {},
    testing: false, testResult: null, testErr: null, busy: false,
  };
}

/* ============================================================================
 * 화면 모듈
 * ========================================================================== */
const screen = {
  key: 'manage',
  title: { en: 'Manage', ko: '장비 관리' },
  icon: 'box',

  /* ── init: DOM 1회 생성 + 위임 리스너 등록 ─────────────────────────────── */
  init(root, ctx) {
    this.ctx = ctx;
    this.root = root;
    this.rowRefs = new Map();
    this.grpRefs = new Map();
    this.treeSig = '';
    this.modalSig = '';
    root.classList.add('sc-mng');

    const { el } = ctx.util.dom;
    const L = ctx.L;

    // 헤더
    this.elTitle = el('h1', { class: 'sc-mng-h1', text: L('Devices', '장비') });
    this.elSub = el('p', { class: 'sc-mng-sub' });
    this.elAdd = el('button', {
      class: 'btn btn--primary', type: 'button', 'data-mng-add': '',
    }, [el('span', { class: 'sc-mng-plus', text: '+' }), el('span', { text: L('Add device', '장비 추가') })]);

    const head = el('div', { class: 'sc-mng-head' }, [
      el('div', { class: 'u-flex' }, [this.elTitle, this.elSub]),
      this.elAdd,
    ]);

    // 폴러 배너
    this.elBanner = el('div', { class: 'banner banner--neg sc-mng-banner', hidden: true }, [
      el('span', { class: 'banner-icon' }, [ctx.util.icon('warningCircle', { size: 16 })]),
      this.elBannerText = el('span', { class: 'banner-text' }),
    ]);

    // 트리
    this.elTree = el('div', { class: 'sc-mng-tree' });

    // ※ 하단 '요약 스트립' 제거(minor#3) — 등록 대수는 리스트에서 자명하고, 갱신 시각은 LIVE
    //   인디케이터가 표시한다(clusters 와 동일 중복/저정보 패턴).

    // 모달 호스트
    this.elModalHost = el('div', { class: 'sc-mng-modal-host' });

    root.appendChild(head);
    root.appendChild(this.elBanner);
    root.appendChild(this.elTree);
    root.appendChild(this.elModalHost);

    this._onClick = (e) => this.onClick(e);
    this._onInput = (e) => this.onInput(e);
    // Escape → 모달 취소(삭제 확인 모달도 확정이 아니라 항상 취소로 닫는다).
    // Tab / Shift+Tab → 열린 모달 안에서 포커스를 순환시키는 트랩(모달 밖으로 못 나가게).
    this._onKey = (e) => {
      const f = this.form();
      if (!f) return;
      // IME 조합 중(isComposing) 키는 모달 닫기·트랩을 건어뛰고 IME 에 양보한다(#569) —
      // 한글 라벨 등을 조합하다 조합만 취소하려는 Escape 가 document 까지 전파돼
      // 모달을 통째로 닫아 폼을 소실시켰다. #471 datalist 가드는 list 연결 입력만
      // 덮었고, app.js:943(#560)과 같은 계열의 입력 소비 존중이다.
      if (e.isComposing) return;
      if (e.key === 'Escape') {
        if (f.busy || e.defaultPrevented) return;
        // datalist 자동완성을 닫는 Escape 는 주요 브라우저에서 document 까지 전파된다 —
        // list 연결 입력에서의 첫 Escape 는 드롭다운 닫기로 보고 모달 닫기를 건너뛰고,
        // 같은 입력에서의 연속(두 번째) Escape 부터 모달을 닫는다(#471).
        const t = e.target;
        const listInput = (t && typeof t.getAttribute === 'function' && t.getAttribute('list')) ? t : null;
        if (listInput && this._escListSkip !== listInput) { this._escListSkip = listInput; return; }
        this._escListSkip = null;
        e.preventDefault(); this.closeModal();
        return;
      }
      this._escListSkip = null;
      if (e.key === 'Tab') {
        const dialog = this.elModalHost && this.elModalHost.querySelector('[role="dialog"]');
        if (!dialog) return;
        const items = getFocusable(dialog);
        if (!items.length) { e.preventDefault(); dialog.focus(); return; }
        const first = items[0], last = items[items.length - 1];
        const active = document.activeElement;
        // 모달 남쪽의 비포커스 영역(힌트 문구·라벨 등)을 클릭하면 activeElement 가 body 로 빠진다.
        // 이 상태의 Tab 은 첫/마지막 요소 조건에 걸리지 않아 aria-modal 다이얼로그가 열린 채
        // 셸(레일·헤더)로 포커스가 이탈했다(#517) — 활성 요소가 다이얼로그 밖이면 표준 처리대로
        // 안쪽으로 강제 복귀(Shift+Tab 이면 마지막, 아니면 첫 포커스 가능 요소).
        if (!active || !dialog.contains(active)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
        else if (e.shiftKey && active === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus(); }
      }
    };
    // 회사/공장 접기 헤더(role=button tabindex=0)의 키보드 활성화 — Enter/Space 로 클릭과 동일하게 접기 토글.
    // 장비 행/카드(role=button tabindex=0)도 같은 패턴으로 상세 진입(clusters #24·nodes 전례).
    // Space 는 preventDefault 로 페이지 스크롤을 막는다. 행 안의 수정·제거 등 네이티브
    // 버튼/입력에서는 동작하지 않는다(그 컨트롤 고유의 클릭이 담당 — 이중 동작·Space 활성화 방해 금지).
    this._onKeydown = (e) => {
      const tab = e.target.closest && e.target.closest('[role="tab"][data-mng-tab]');
      if (tab && ['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) {
        const tabs = Array.from(tab.parentElement.querySelectorAll('[role="tab"]'));
        const here = tabs.indexOf(tab);
        let next = e.key === 'Home' ? 0 : e.key === 'End' ? tabs.length - 1
          : (here + (e.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
        e.preventDefault();
        const key = tabs[next].getAttribute('data-mng-tab');
        this.setForm({ tab: key, err: null, fieldErrors: {} });
        queueMicrotask(() => {
          const target = this.elModalHost && this.elModalHost.querySelector('#mng-tab-' + key);
          if (target) target.focus();
        });
        return;
      }
      if (e.key !== 'Enter' && e.key !== ' ' && e.key !== 'Spacebar') return;
      const toggle = e.target.closest && e.target.closest('[data-mng-toggle]');
      if (toggle && this.root.contains(toggle)) {
        e.preventDefault();
        this.toggleGroup(toggle.getAttribute('data-mng-toggle'));
        return;
      }
      if (e.target.closest && e.target.closest('button, a, input, select, textarea')) return;
      const rowEl = e.target.closest && e.target.closest('[data-mng-row]');
      if (rowEl && this.root.contains(rowEl)) {
        e.preventDefault();
        this.ctx.goDetail(rowEl.getAttribute('data-mng-row'));
      }
    };
    root.addEventListener('click', this._onClick);
    root.addEventListener('input', this._onInput);
    root.addEventListener('change', this._onInput);
    root.addEventListener('keydown', this._onKeydown);
    document.addEventListener('keydown', this._onKey);
  },

  destroy() {
    if (this.root) {
      this.root.removeEventListener('click', this._onClick);
      this.root.removeEventListener('input', this._onInput);
      this.root.removeEventListener('change', this._onInput);
      this.root.removeEventListener('keydown', this._onKeydown);
      this.root.classList.remove('sc-mng');
    }
    document.removeEventListener('keydown', this._onKey);
    // 화면 라우팅으로 모달이 열린 채 언마운트되는 경우 — 배경 inert 흔적을 남기지 않는다
    // (포커스 복원은 시도하지 않음: 이미 다른 화면으로 이동 중이라 대상 요소가 사라졌을 수 있다).
    if (this._modalOpen) this._closeModalA11y(false);
    this._modalOpen = false; this._lastFocus = null;
    this.rowRefs && this.rowRefs.clear();
    this.grpRefs && this.grpRefs.clear();
    this.ctx = null; this.root = null;
    this.elModalHost = null; this.elTree = null;
  },

  /* ── 헬퍼 ──────────────────────────────────────────────────────────────── */
  form() {
    const st = this.ctx && this.ctx.store.getState();
    return (st && st.form) || null;
  },
  setForm(patch) {
    const f = this.form();
    if (!f) return;
    Object.assign(f, patch);
    f._rev = (f._rev || 0) + 1;
    this.ctx.store.setState({ form: f });
  },
  // 회사/공장 그룹 접기 토글 — 클릭/키보드(Enter·Space) 공용 활성화 로직.
  toggleGroup(k) {
    if (!k || !this.ctx) return;
    const cur = this.ctx.store.getState().manageCollapsed || {};
    this.ctx.store.setState({ manageCollapsed: Object.assign({}, cur, { [k]: !cur[k] }) });
  },

  /* ── render: 값만 patch ───────────────────────────────────────────────── */
  render(state, ctx) {
    const L = ctx.L;
    const model = (ctx.getModel && ctx.getModel(state)) || null;
    const tree = (model && model.tree)
      || (ctx.model && ctx.model.buildManageTree ? ctx.model.buildManageTree(state) : [])
      || [];

    // detail 화면의 "설정 수정" 핸드오프 — editKey가 오면 수정 모달을 1회 연다.
    // 열린 폼이 있어도 교체한다: editKey는 detail 에서만 세팅되고 곧바로 이 화면으로 이동하므로,
    // 그 시점에 남아 있는 폼은 이전 방문에서 닫히지 않은 stale 폼뿐이다. (!form 조건이면
    // stale 폼 뒤로 핸드오프가 유실되고, 그 폼을 닫을 때 closeModal 이 editKey 까지 지워
    // 요청한 장비의 수정 모달이 영구히 열리지 않는다.)
    if (state.editKey) { this.openEdit(state.editKey); return; }

    this.elTitle.textContent = L('Devices', '장비');
    this.elAdd.lastChild.textContent = L('Add device', '장비 추가');
    const sampleMode = isSampleState(state);
    this.elAdd.disabled = sampleMode;
    this.elAdd.title = sampleMode ? L('Sample data is read-only', '샘플 데이터는 조회만 가능합니다') : '';
    this.elSub.textContent = sampleMode
      ? L('Read-only sample devices — configuration changes are disabled', '읽기 전용 샘플 장비 — 설정 변경이 비활성화됩니다')
      : L(
        'Company ▸ factory ▸ device — name · IP · status, add / edit / remove',
        '회사 ▸ 공장 ▸ 장비 — 이름·IP·상태, 추가/수정/제거'
      );

    // 폴러 연결 배너
    const liveErr = state.liveError || state.stale;
    this.elBanner.hidden = !liveErr;
    if (liveErr) {
      this.elBannerText.textContent = L('Poller unreachable', '폴러 연결 실패')
        + (state.liveError ? ': ' + state.liveError : '')
        + ' · /api/fleet';
    }

    this.renderTree(tree, state, ctx);
    this.renderModal(state, ctx);
  },

  /* ── 트리 ──────────────────────────────────────────────────────────────── */
  /**
   * 트리 렌더 방식 분류 — 그룹 수가 1개뿐이면 그 레벨의 헤더(캐럿·이름·카운트 배지)를
   * 접어 평탄화한다(장비 행/카드를 바로 노출). 회사·공장이 둘 다 1개뿐인 전형적인
   * 소규모 배치(실장비 2대 등)는 완전 평탄화 + 카드 밀도 상향까지 간다.
   *   flat   — 회사 1개 · 공장 ≤1개 → 헤더 없이 장비 카드 그리드
   *   flatfa — 회사 1개 · 공장 2개↑ → 회사 헤더만 생략, 공장 헤더는 최상위로 승격
   *   tree   — 회사 2개↑ → 기존 3단 트리(단, 공장이 1개뿐인 회사는 그 공장 헤더만 생략
   *            해 "같은 대수를 두 번 세는" 중복 카운트 배지를 없앤다)
   */
  classifyTree(tree) {
    if (!tree.length) return { kind: 'empty' };
    if (tree.length === 1) {
      const co = tree[0];
      if (co.factories.length <= 1) {
        return { kind: 'flat', devices: co.factories.length ? co.factories[0].devices : [] };
      }
      return { kind: 'flatfa', factories: co.factories };
    }
    return { kind: 'tree' };
  },

  renderTree(tree, state, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const mode = this.classifyTree(tree);
    // 시그니처는 '구조'(언어·모드·회사/공장/장비 구성)만 반영한다 — 접힘(collapsed) 상태는
    // 제외한다. 접기 토글은 아래 patchGroup 이 is-collapsed 클래스·aria-expanded·body.hidden
    // 으로 처리하므로 트리 재빌드가 불필요하다. 접힘을 시그니처에 넣으면 토글마다 elTree 를
    // 통째로 재생성(textContent='')해 포커스된 헤더 요소가 사라지고, 키보드 사용자가 첫 토글
    // 직후 포커스를 잃어 연속 Enter/Space 조작이 깨진다(P7 회귀). 재생성 비용도 낭비.
    const sig = (state.lang || 'ko') + '|' + mode.kind + '|' + tree.map((c) => c.key + '+' + c.count
      + ':' + c.factories.map((f) => f.key
        + ':' + f.devices.map((d) => d.id + (d.sample ? ':sample' : '')).join(',')).join(';')).join('|');

    if (sig !== this.treeSig) {
      this.treeSig = sig;
      this.rowRefs.clear();
      this.grpRefs.clear();
      this.elTree.textContent = '';
      this.elTree.classList.toggle('sc-mng-tree--cards', mode.kind === 'flat');

      if (mode.kind === 'empty') {
        this.elTree.appendChild(el('div', { class: 'empty' }, [
          el('div', { class: 'empty-icon' }, [ctx.util.icon('box', { size: 20 })]),
          el('div', { class: 'empty-title', text: L('No devices', '장비 없음') }),
          el('div', { class: 'empty-sub', text: L('Add a device to start monitoring.', '장비를 추가하면 모니터링이 시작됩니다.') }),
        ]));
      } else if (mode.kind === 'flat') {
        // 소규모 배치(실장비 2대 등) — 얇은 2행이 뷰포트 하단을 텅 비게 남기던 문제(major#2)를 해소하기
        // 위해 밀도 있는 '관리 카드'로 렌더한다. 실시간 CPU/MEM 그래프(개요·상세 소유)는 넣지 않고,
        // 관리에 필요한 메타(가동시간·VM 수·가용성·동기화·버전)만 담아 중복 없이 밀도를 채운다.
        const grid = el('div', { class: 'sc-mng-cards' });
        mode.devices.forEach((d) => grid.appendChild(this.buildCard(d, ctx)));
        this.elTree.appendChild(grid);
      } else if (mode.kind === 'flatfa') {
        mode.factories.forEach((fa) => {
          const w = this.buildFactory(fa, ctx);
          w.classList.add('sc-mng-fa--top');
          this.elTree.appendChild(w);
        });
      } else {
        tree.forEach((co) => this.elTree.appendChild(this.buildCompany(co, ctx)));
      }
    }

    // 값 patch
    if (mode.kind === 'flat') {
      mode.devices.forEach((d) => this.patchRow(d, ctx));
    } else if (mode.kind === 'flatfa') {
      mode.factories.forEach((fa) => {
        this.patchGroup(fa.key, fa);
        fa.devices.forEach((d) => this.patchRow(d, ctx));
      });
    } else if (mode.kind === 'tree') {
      tree.forEach((co) => {
        this.patchGroup(co.key, co);
        if (co.factories.length === 1) {
          co.factories[0].devices.forEach((d) => this.patchRow(d, ctx));
        } else {
          co.factories.forEach((fa) => {
            this.patchGroup(fa.key, fa);
            fa.devices.forEach((d) => this.patchRow(d, ctx));
          });
        }
      });
    }
  },

  buildCompany(co, ctx) {
    const { el } = ctx.util.dom;
    const caret = el('span', { class: 'sc-mng-caret' }, [ctx.util.icon('chevronDown', { size: 14 })]);
    const dot = el('span', { class: 'u-dot' });
    const count = el('span', { class: 'u-badge is-mono' });
    const head = el('div', {
      class: 'sc-mng-co-head', 'data-mng-toggle': co.key, role: 'button', tabindex: '0', 'aria-expanded': 'true',
    }, [
      caret,
      el('span', { class: 'sc-mng-co-ico' }, [ctx.util.icon('building', { size: 16 })]),
      el('span', { class: 'sc-mng-co-name', text: co.name }),
      dot, count,
    ]);
    const body = el('div', { class: 'sc-mng-co-body' });
    // 공장이 1개뿐이면 그 공장의 카운트는 회사 카운트와 항상 같다(같은 장비를 두 번 세는
    // 중복 배지) — 공장 헤더는 생략하고 장비 행을 회사 바디에 바로 노출한다.
    if (co.factories.length === 1) {
      co.factories[0].devices.forEach((d) => body.appendChild(this.buildRow(d, ctx)));
    } else {
      co.factories.forEach((fa) => body.appendChild(this.buildFactory(fa, ctx)));
    }
    const wrap = el('div', { class: 'sc-mng-co' }, [head, body]);
    this.grpRefs.set(co.key, { wrap, head, body, caret, dot, count });
    return wrap;
  },

  buildFactory(fa, ctx) {
    const { el } = ctx.util.dom;
    const caret = el('span', { class: 'sc-mng-caret' }, [ctx.util.icon('chevronDown', { size: 13 })]);
    const dot = el('span', { class: 'u-dot' });
    const count = el('span', { class: 'u-badge is-mono' });
    const head = el('div', {
      class: 'sc-mng-fa-head', 'data-mng-toggle': fa.key, role: 'button', tabindex: '0', 'aria-expanded': 'true',
    }, [
      caret,
      el('span', { class: 'sc-mng-fa-ico' }, [ctx.util.icon('crop', { size: 14 })]),
      el('span', { class: 'sc-mng-fa-name', text: fa.name }),
      dot, count,
    ]);
    const body = el('div', { class: 'sc-mng-fa-body' });
    fa.devices.forEach((d) => body.appendChild(this.buildRow(d, ctx)));
    const wrap = el('div', { class: 'sc-mng-fa' }, [head, body]);
    this.grpRefs.set(fa.key, { wrap, head, body, caret, dot, count });
    return wrap;
  },

  // 관리 목록은 "얇은 행"만 — 이름 / IP / 상태 + 편집·삭제 액션.
  // CPU/MEM/스파크라인/가용성/가동/VM 본문은 nodes·detail 화면이 소유하므로 여기서 재표현하지 않는다(중복 제거).
  buildRow(d, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const mark = el('span', { class: 'sc-mng-mark' }, [
      ctx.util.icon(d.typeIcon || ICON_BY_TYPE[d.type] || 'box', { size: 17 }),
    ]);
    const dot = el('span', { class: 'sc-mng-mark-dot' });
    mark.appendChild(dot);

    const name = el('span', { class: 'sc-mng-name u-mono' });
    const sampleBadge = el('span', { class: 'u-badge is-warn sc-mng-sample', text: 'SAMPLE', hidden: !d.sample });
    const meta = el('span', { class: 'sc-mng-meta u-mono' });
    // 상태 뱃지는 전 화면 공통 매핑(저하=앰버 is-warn)으로 통일한다(렌즈3 상태색 일관).
    const stat = el('span', { class: 'u-badge sc-mng-stat' });

    const btnEdit = el('button', {
      class: 'sc-mng-act', type: 'button', 'data-mng-edit': d.id,
      title: d.sample ? L('Sample data is read-only', '샘플 데이터는 조회만 가능합니다')
        : (d.type === 'END' ? L('Endurance editing is not supported', 'Endurance 수정은 현재 지원되지 않습니다') : L('Edit', '수정')),
      'aria-label': d.sample ? L('Sample data is read-only', '샘플 데이터는 조회만 가능합니다')
        : (d.type === 'END' ? L('Endurance editing is not supported', 'Endurance 수정은 현재 지원되지 않습니다') : L('Edit', '수정')),
      disabled: d.type === 'END' || d.sample,
    }, [ctx.util.icon('pencil', { size: 14 })]);
    const btnDel = el('button', {
      class: 'sc-mng-act sc-mng-act--del', type: 'button', 'data-mng-del': d.id,
      title: d.sample ? L('Sample data is read-only', '샘플 데이터는 조회만 가능합니다')
        : (d.type === 'END' ? L('Legacy Endurance records are display-only', '레거시 Endurance 레코드는 조회 전용입니다') : L('Remove', '제거')),
      'aria-label': d.sample ? L('Sample data is read-only', '샘플 데이터는 조회만 가능합니다')
        : (d.type === 'END' ? L('Legacy Endurance records are display-only', '레거시 Endurance 레코드는 조회 전용입니다') : L('Remove', '제거')),
      disabled: d.type === 'END' || d.sample,
    }, [ctx.util.icon('close', { size: 14 })]);

    const row = el('div', { class: 'sc-mng-row', 'data-mng-row': d.id, role: 'button', tabindex: '0' }, [
      mark,
      el('div', { class: 'sc-mng-idb' }, [name, sampleBadge, meta]),
      stat,
      el('div', { class: 'sc-mng-acts' }, [btnEdit, btnDel]),
    ]);
    this.rowRefs.set(d.id, { row, dot, name, sampleBadge, meta, stat });
    return row;
  },

  // 소규모 배치용 '관리 카드' — 얇은 행 대신 밀도 있는 카드로 하단 공백을 채운다(major#2).
  // 실시간 CPU/MEM 은 개요·상세가 소유하므로 넣지 않는다. 관리 메타(가동시간·VM·가용성·동기화)만.
  buildCard(d, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const mark = el('span', { class: 'sc-mng-mark' }, [
      ctx.util.icon(d.typeIcon || ICON_BY_TYPE[d.type] || 'box', { size: 18 }),
    ]);
    const dot = el('span', { class: 'sc-mng-mark-dot' });
    mark.appendChild(dot);
    const name = el('span', { class: 'sc-mng-name u-mono' });
    const sampleBadge = el('span', { class: 'u-badge is-warn sc-mng-sample', text: 'SAMPLE', hidden: !d.sample });
    const meta = el('span', { class: 'sc-mng-meta u-mono' });
    const stat = el('span', { class: 'u-badge sc-mng-stat' });
    const top = el('div', { class: 'sc-mng-card-top' }, [
      mark,
      el('div', { class: 'sc-mng-idb' }, [name, sampleBadge, meta]),
      stat,
    ]);

    const mk = (en, ko) => {
      const lab = el('span', { class: 'sc-mng-mv-lab u-muted' });
      lab.textContent = L(en, ko);
      const val = el('span', { class: 'sc-mng-mv-val u-mono' });
      return { wrap: el('div', { class: 'sc-mng-mv' }, [lab, val]), val };
    };
    const mUp = mk('Uptime', '가동시간');
    const mVm = mk('VMs', '가상 머신');
    const mAvail = mk('Availability', '가용성');
    const mSync = mk('Sync', '동기화');
    const metrics = el('div', { class: 'sc-mng-card-metrics' }, [mUp.wrap, mVm.wrap, mAvail.wrap, mSync.wrap]);

    const ver = el('span', { class: 'sc-mng-chip u-mono' });
    const btnEdit = el('button', {
      class: 'sc-mng-act', type: 'button', 'data-mng-edit': d.id,
      title: d.sample ? L('Sample data is read-only', '샘플 데이터는 조회만 가능합니다')
        : (d.type === 'END' ? L('Endurance editing is not supported', 'Endurance 수정은 현재 지원되지 않습니다') : L('Edit', '수정')),
      'aria-label': d.sample ? L('Sample data is read-only', '샘플 데이터는 조회만 가능합니다')
        : (d.type === 'END' ? L('Endurance editing is not supported', 'Endurance 수정은 현재 지원되지 않습니다') : L('Edit', '수정')),
      disabled: d.type === 'END' || d.sample,
    }, [ctx.util.icon('pencil', { size: 14 })]);
    const btnDel = el('button', {
      class: 'sc-mng-act sc-mng-act--del', type: 'button', 'data-mng-del': d.id,
      title: d.sample ? L('Sample data is read-only', '샘플 데이터는 조회만 가능합니다')
        : (d.type === 'END' ? L('Legacy Endurance records are display-only', '레거시 Endurance 레코드는 조회 전용입니다') : L('Remove', '제거')),
      'aria-label': d.sample ? L('Sample data is read-only', '샘플 데이터는 조회만 가능합니다')
        : (d.type === 'END' ? L('Legacy Endurance records are display-only', '레거시 Endurance 레코드는 조회 전용입니다') : L('Remove', '제거')),
      disabled: d.type === 'END' || d.sample,
    }, [ctx.util.icon('close', { size: 14 })]);
    const foot = el('div', { class: 'sc-mng-card-foot' }, [
      ver,
      el('div', { class: 'sc-mng-acts sc-mng-card-acts' }, [btnEdit, btnDel]),
    ]);

    const card = el('div', { class: 'sc-mng-card', 'data-mng-row': d.id, role: 'button', tabindex: '0' }, [top, metrics, foot]);
    this.rowRefs.set(d.id, {
      row: card, dot, name, sampleBadge, meta, stat,
      mUp: mUp.val, mVm: mVm.val, mAvail: mAvail.val, mSync: mSync.val, ver,
    });
    return card;
  },

  patchGroup(key, g) {
    const r = this.grpRefs.get(key);
    if (!r) return;
    r.wrap.classList.toggle('is-collapsed', !!g.collapsed);
    r.head.setAttribute('aria-expanded', g.collapsed ? 'false' : 'true');
    r.body.hidden = !!g.collapsed;
    r.count.textContent = String(g.count);
    const tone = g.worst === 'down' ? 'is-neg' : (g.worst === 'deg' ? 'is-warn' : 'is-pos');
    r.dot.className = 'u-dot ' + tone;
  },

  patchRow(d, ctx) {
    const r = this.rowRefs.get(d.id);
    if (!r) return;
    const L = ctx.L;
    // 회사 그룹 헤더와 중복되지 않도록 행에는 회사 프리픽스를 벗긴 장비코드만 표시한다.
    const nmFull = d.label || d.host || DASH;
    r.name.textContent = (d.company && nmFull.indexOf(d.company) === 0)
      ? (nmFull.slice(d.company.length).trim() || nmFull) : nmFull;
    if (r.sampleBadge) r.sampleBadge.hidden = !d.sample;
    r.meta.textContent = [d.typeLabel, d.mgmt || d.site].filter(Boolean).join(' · ');

    const tone = d.statusTone === 'neg' ? 'is-neg' : (d.statusTone === 'warn' ? 'is-warn' : 'is-pos');
    // 상태는 한 번만 진술한다(이중 인코딩 제거): 아이콘 도트는 숨기고 상태 필만,
    // 그마저도 정상은 무배지 · 편차(저하/오프라인/점검/로딩)일 때만 노출.
    r.dot.style.display = 'none';
    const inMaintenance = !!(d.maint || d.maintWin);
    const devi = inMaintenance ? 'is-warn' : tone;
    const showStat = d.pending || inMaintenance || d.statusTone === 'neg' || d.statusTone === 'warn';
    r.stat.style.display = showStat ? '' : 'none';
    r.stat.textContent = d.pending ? L('Loading…', '불러오는 중')
      : (inMaintenance
        ? (d.maintWin && !d.maint ? L('Maintenance window', '점검 창') : (d.maintLabel || L('Maintenance', '점검 중')))
        : d.statusLabel);
    // G23: 운영 화면(개요/노드/클러스터)은 결과('저하'), 관리 화면은 원인('점검')을 쓴다 —
    //     같은 장비가 다른 상태로 읽히는 혼선을 title 로 연결(어휘 인과 명시).
    r.stat.title = d.maintWin && !d.maint
      ? L('In a maintenance window — alerts are muted on operational screens',
        '점검 창 — 운영 화면에서 경보가 묵음 처리됨')
      : d.maint
        ? L('In maintenance mode — shown as "degraded" on operational screens',
          '점검 모드로 인한 저하 — 운영 화면에는 \'저하\'로 표시')
      : '';
    r.stat.className = 'u-badge sc-mng-stat ' + devi;

    // 관리 카드(flat 모드) 전용 메타 — 존재할 때만 patch.
    if (r.mUp) {
      const noTel = d.noTel || d.status === 'down';
      r.mUp.textContent = d.uptime || DASH;
      r.mVm.textContent = noTel ? DASH : ((d.vmRunning || 0) + ' / ' + (d.vms || 0));
      r.mAvail.textContent = d.avail || DASH;
      r.mSync.textContent = d.syncLabel || DASH;
      r.ver.textContent = d.version ? ('v' + d.version) : (d.mgmt || d.site || '');
    }
  },


  /* ── 모달 ──────────────────────────────────────────────────────────────── */
  renderModal(state, ctx) {
    const f = state.form;
    if (!f) {
      if (this.modalSig) { this.elModalHost.textContent = ''; this.modalSig = ''; }
      if (this._modalOpen) this._closeModalA11y();
      return;
    }
    if (!this._modalOpen) this._openModalA11y();
    const step = state.wizardStep || 0;
    // 구조 시그니처 — 텍스트 입력값은 제외해 포커스/캐럿을 보존한다.
    const sig = [
      f.mode, f.origKey, f.type, f.platform || '', step, f.tab, state.lang,
      Object.keys(f.showPw || {}).sort().join(','),
      f.busy ? 1 : 0, f.testing ? 1 : 0,
      f.testResult ? 'R' + (f._testRev || 0) : '-',
      (f.vms || []).length, f._rev,
    ].join('|');
    if (sig === this.modalSig) { this.patchModalErr(f); return; }
    this.modalSig = sig;
    // platform/type 변경처럼 모달을 재빌드하면 포커스된 요소가 파괴(포커스 유실)된다 —
    // 재빌드 전에 활성 필드의 data-mng-f 를 기억해 새 DOM의 같은 필드로 복원한다.
    const prevF = (document.activeElement && this.elModalHost.contains(document.activeElement))
      ? document.activeElement.getAttribute('data-mng-f') : null;
    const prevTab = (document.activeElement && this.elModalHost.contains(document.activeElement)
      && document.activeElement.getAttribute('data-mng-tab')) ? f.tab : null;
    this.elModalHost.textContent = '';
    this.elModalHost.appendChild(
      f.mode === 'delete' ? this.buildDeleteModal(f, ctx) : this.buildFormModal(f, step, ctx)
    );
    this.patchModalErr(f);
    const restored = prevF && this.elModalHost.querySelector('[data-mng-f="' + prevF + '"]');
    if (restored) restored.focus();
    else if (prevTab && this.elModalHost.querySelector('#mng-tab-' + prevTab)) this.elModalHost.querySelector('#mng-tab-' + prevTab).focus();
    else this._focusIntoModal();
  },

  // 모달 열림 진입 — 이전 포커스를 기억하고, 화면의 나머지 콘텐츠(모달 호스트를 제외한
  // this.root 의 형제들)를 aria-hidden으로 인어트 처리해 스크린리더/탭 순환에서 제외한다.
  _openModalA11y() {
    this._modalOpen = true;
    this._lastFocus = document.activeElement;
    const targets = [];
    if (this.root) Array.from(this.root.children).forEach((c) => { if (c !== this.elModalHost) targets.push(c); });
    ['.hd', '.rail', '.hd-banner', '.toast'].forEach((sel) => {
      const node = document.querySelector(sel); if (node) targets.push(node);
    });
    this._inerted = targets.map((node) => ({
      node,
      inert: !!node.inert,
      ariaHidden: node.getAttribute('aria-hidden'),
    }));
    this._inerted.forEach(({ node }) => {
      node.inert = true;
      node.setAttribute('aria-hidden', 'true');
    });
  },

  // 모달 닫힘 — 인어트 해제 + 이전 포커스 복원.
  _closeModalA11y(restoreFocus = true) {
    this._modalOpen = false;
    (this._inerted || []).forEach(({ node, inert, ariaHidden }) => {
      node.inert = inert;
      if (ariaHidden == null) node.removeAttribute('aria-hidden');
      else node.setAttribute('aria-hidden', ariaHidden);
    });
    this._inerted = [];
    const prev = this._lastFocus;
    this._lastFocus = null;
    if (restoreFocus && prev && document.body.contains(prev) && typeof prev.focus === 'function') prev.focus();
  },

  // 새로 빌드된 다이얼로그 내부로 포커스 이동 — 첫 폼 입력(data-mng-f), 없으면 다이얼로그 자체
  // (tabindex=-1). 위저드 단계 전환처럼 재빌드될 때도 매번 호출되어 파괴된 이전 포커스를 복구한다.
  // ※ DOM 상 첫 포커스 가능 요소는 modal-head 의 X(닫기) 버튼이라, 그쪽에 착지하면 Enter/Space
  //   한 번에 모달이 닫혀 작성 중인 폼이 소실된다(#470) — 안전한 착지 지점만 고른다.
  _focusIntoModal() {
    const dialog = this.elModalHost && this.elModalHost.querySelector('[role="dialog"]');
    if (!dialog) return;
    const field = dialog.querySelector('[data-mng-f]');
    (field || dialog).focus();
  },

  patchModalErr(f) {
    if (!this.elErr) return;
    this.elErr.hidden = !f.err;
    this.elErr.textContent = f.err || '';
    if (this.elTestErr) {
      this.elTestErr.hidden = !f.testErr;
      this.elTestErr.textContent = f.testErr || '';
    }
  },

  buildDeleteModal(f, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    this.elErr = el('div', { class: 'sc-mng-err', role: 'alert', hidden: true });
    this.elTestErr = null;
    const reasonErr = f.fieldErrors && f.fieldErrors.delete_reason;
    const phraseErr = f.fieldErrors && f.fieldErrors.delete_phrase;
    const impact = f.impact || { acknowledgements: 0, maintenanceWindow: false, note: false };
    const modal = el('div', {
      class: 'modal modal--narrow', role: 'dialog', 'aria-modal': 'true',
      'aria-labelledby': 'mng-modal-title-del', 'aria-describedby': 'mng-delete-impact', tabindex: '-1',
    }, [
      el('div', { class: 'modal-head' }, [
        el('h2', { class: 'modal-title', id: 'mng-modal-title-del', text: L('Remove device', '장비 제거') }),
        el('button', { class: 'modal-close', type: 'button', 'data-mng-close': '', 'aria-label': L('Close', '닫기') },
          [ctx.util.icon('close', { size: 15 })]),
      ]),
      el('div', { class: 'modal-body' }, [
        el('div', { class: 'confirm-danger', id: 'mng-delete-impact' }, [
          el('strong', { text: f.label || f.origKey }),
          document.createTextNode(' '),
          document.createTextNode(L('will be removed from monitoring. This cannot be undone.',
            '을(를) 모니터링에서 제거합니다. 되돌릴 수 없습니다.')),
        ]),
        el('dl', { class: 'sc-mng-delete-impact' }, [
          el('dt', { text: L('Target ID', '대상 ID') }), el('dd', { class: 'u-mono', text: f.origKey }),
          el('dt', { text: L('Acknowledgements removed', '삭제되는 경보 확인') }), el('dd', { text: String(impact.acknowledgements || 0) }),
          el('dt', { text: L('Maintenance window removed', '삭제되는 점검 창') }), el('dd', { text: impact.maintenanceWindow ? L('Yes', '예') : L('No', '아니요') }),
          el('dt', { text: L('Device note removed', '삭제되는 장비 메모') }), el('dd', { text: impact.note ? L('Yes', '예') : L('No', '아니요') }),
          el('dt', { text: L('Operator', '운영자') }), el('dd', { text: f.operator || ctx.operator || 'admin' }),
        ]),
        el('div', { class: 'field' }, [
          el('label', { class: 'field-label', for: 'mng-f-delete_reason', text: L('Confirmation reason (required)', '확인 사유 (필수)') }),
          el('textarea', {
            id: 'mng-f-delete_reason', class: 'field-input', rows: '3', required: true,
            maxlength: '500',
            'data-mng-f': 'delete_reason', value: f.delete_reason || '',
            'aria-invalid': reasonErr ? 'true' : null,
            'aria-describedby': 'mng-delete-reason-help' + (reasonErr ? ' mng-f-delete_reason-error' : ''),
          }),
          el('div', { class: 'field-hint', id: 'mng-delete-reason-help', text: L(
            'Stored in the server audit trail after the device is removed.',
            '장비 제거 후 서버 감사 기록에 영구 저장됩니다.'
          ) }),
          reasonErr ? el('div', { class: 'field-error', id: 'mng-f-delete_reason-error', text: reasonErr }) : null,
        ]),
        el('div', { class: 'field' }, [
          el('label', { class: 'field-label', for: 'mng-f-delete_phrase', text: L('Type device ID to confirm', '확인을 위해 장비 ID 입력') }),
          el('div', { class: 'field-hint u-mono', id: 'mng-delete-phrase-help', text: f.origKey }),
          el('input', {
            id: 'mng-f-delete_phrase', class: 'field-input', type: 'text', required: true, autocomplete: 'off',
            'data-mng-f': 'delete_phrase', value: f.delete_phrase || '',
            'aria-invalid': phraseErr ? 'true' : null,
            'aria-describedby': 'mng-delete-phrase-help' + (phraseErr ? ' mng-f-delete_phrase-error' : ''),
          }),
          phraseErr ? el('div', { class: 'field-error', id: 'mng-f-delete_phrase-error', text: phraseErr }) : null,
        ]),
        this.elErr,
      ]),
      el('div', { class: 'modal-foot' }, [
        el('button', { class: 'btn btn--outline', type: 'button', 'data-mng-close': '', text: L('Cancel', '취소') }),
        el('button', {
          class: 'btn btn--danger', type: 'button', 'data-mng-confirm-del': '', disabled: !!f.busy,
          text: f.busy ? L('Removing…', '제거 중…') : L('Remove', '제거'),
        }),
      ]),
    ]);
    return el('div', { class: 'modal-overlay', 'data-mng-overlay': '' }, [modal]);
  },

  buildFormModal(f, step, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const add = f.mode === 'add';

    this.elErr = el('div', { class: 'sc-mng-err', role: 'alert', hidden: true });
    this.elTestErr = el('div', { class: 'sc-mng-err', role: 'alert', hidden: true });

    const grid = el('div', { class: 'sc-mng-grid' });
    if (add) {
      if (step === 0) { grid.appendChild(this.typeCards(f, ctx)); this.basicFields(f, grid, ctx); }
      else if (step === 1) this.connGroup(f, grid, ctx);
      else if (step === 2) this.authGroup(f, grid, ctx);
      else this.summary(f, grid, ctx);
    } else {
      if (f.tab === 'basic') { grid.appendChild(this.typeChip(f, ctx)); this.basicFields(f, grid, ctx); }
      else if (f.tab === 'conn') this.connGroup(f, grid, ctx);
      else if (f.tab === 'auth') this.authGroup(f, grid, ctx);
      else this.vmGroup(f, grid, ctx);
      grid.id = 'mng-tabpanel';
      grid.setAttribute('role', 'tabpanel');
      grid.setAttribute('aria-labelledby', 'mng-tab-' + f.tab);
      grid.tabIndex = 0;
    }

    const modalNav = add ? this.stepBar(step, ctx) : this.tabBar(f, ctx);

    const body = el('div', { class: 'modal-body' }, [
      el('p', { class: 'sc-mng-hint', text: add
        ? L('Credentials are stored only in the poller config on the host.', '자격증명은 호스트의 폴러 설정에만 저장됩니다.')
        : L('Leave a password blank to keep the current one.', '비밀번호를 비우면 기존 값이 유지됩니다.') }),
      modalNav,
      grid,
      // datalist 는 grid(단계·탭 전환 시 통째로 교체)가 아니라 모달 공통 영역에 둔다 —
      // company·factory(기본 단계)와 vendor(연결 단계)처럼 list 참조 필드가 어느 단계에
      // 있든 참조 대상이 DOM에 살아 있게 한다(#354).
      this.datalists(ctx),
    ]);
    if (!add || step === 3) {
      body.appendChild(this.elTestErr);
      body.appendChild(this.testArea(f, ctx));
    }
    body.appendChild(this.elErr);

    // 푸터
    const left = add
      ? el('button', { class: 'btn btn--outline', type: 'button', 'data-mng-back': '' },
        [step === 0 ? L('Cancel', '취소') : ('← ' + L('Back', '뒤로'))])
      : this.testBtn(f, ctx);
    const right = el('div', { class: 'btn-group' });
    if (add && step < 3) {
      right.appendChild(el('button', {
        class: 'btn btn--primary', type: 'button', 'data-mng-next': '',
      }, [L('Next', '다음') + ' →']));
    } else if (add) {
      right.appendChild(this.testBtn(f, ctx));
      right.appendChild(el('button', {
        class: 'btn btn--primary', type: 'button', 'data-mng-save': '', disabled: !!f.busy,
      }, [f.busy ? L('Saving…', '저장 중…') : L('Add device', '장비 추가')]));
    } else {
      right.appendChild(el('button', { class: 'btn btn--outline', type: 'button', 'data-mng-close': '', text: L('Cancel', '취소') }));
      right.appendChild(el('button', {
        class: 'btn btn--primary', type: 'button', 'data-mng-save': '', disabled: !!f.busy,
      }, [f.busy ? L('Saving…', '저장 중…') : L('Save', '저장')]));
    }

    const modal = el('div', {
      class: 'modal modal--wide sc-mng-modal', role: 'dialog', 'aria-modal': 'true',
      'aria-labelledby': 'mng-modal-title-form', tabindex: '-1',
    }, [
      el('div', { class: 'modal-head' }, [
        el('h2', {
          class: 'modal-title', id: 'mng-modal-title-form',
          text: add ? L('Add device', '장비 추가') : L('Edit device', '장비 수정'),
        }),
        el('button', { class: 'modal-close', type: 'button', 'data-mng-close': '', 'aria-label': L('Close', '닫기') },
          [ctx.util.icon('close', { size: 15 })]),
      ]),
      body,
      el('div', { class: 'modal-foot modal-foot--between' }, [left, right]),
    ]);
    return el('div', { class: 'modal-overlay', 'data-mng-overlay': '' }, [modal]);
  },

  /* ── 모달 조각들 ───────────────────────────────────────────────────────── */
  stepBar(step, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const names = [L('Type', '종류'), L('Connection', '연결'), L('Credentials', '인증'), L('Confirm', '확인')];
    const bar = el('div', { class: 'modal-steps sc-mng-steps' });
    names.forEach((nm, i) => {
      const done = i < step; const cur = i === step;
      bar.appendChild(el('button', {
        class: 'sc-mng-step', type: 'button', 'data-mng-step': String(i), disabled: i > step,
        'aria-label': nm, 'aria-current': cur ? 'step' : null,
      }, [
        el('span', { class: 'modal-step' + (cur ? ' is-active' : (done ? ' is-done' : '')) },
          [done ? ctx.util.icon('check', { size: 13 }) : String(i + 1)]),
        el('span', { class: 'sc-mng-step-lab' + (cur ? ' is-cur' : ''), text: nm }),
      ]));
      if (i < names.length - 1) bar.appendChild(el('span', { class: 'modal-step-line' + (done ? ' sc-mng-line-done' : '') }));
    });
    return bar;
  },

  tabBar(f, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const tabs = [['basic', L('Basic', '기본')], ['conn', L('Connection', '연결')], ['auth', L('Credentials', '인증')]];
    if (isFTType(f.type)) tabs.push(['vm', 'VM']);
    return el('div', { class: 'modal-tabs', role: 'tablist' }, tabs.map(([k, lab]) => el('button', {
      class: 'modal-tab' + (f.tab === k ? ' is-active' : ''), type: 'button', role: 'tab',
      id: 'mng-tab-' + k, 'aria-controls': 'mng-tabpanel',
      'aria-selected': f.tab === k ? 'true' : 'false', tabindex: f.tab === k ? '0' : '-1',
      'data-mng-tab': k, text: lab,
    })));
  },

  typeCards(f, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const grid = el('div', { class: 'sc-mng-types' });
    typeDefs(L).forEach(([val, lab]) => {
      const sel = f.type === val;
      const unsupported = val === 'END';
      grid.appendChild(el('button', {
        class: 'sc-mng-type' + (sel ? ' is-sel' : ''), type: 'button', 'data-mng-type': val,
        'aria-pressed': sel ? 'true' : 'false',
        'aria-disabled': unsupported ? 'true' : null,
        disabled: unsupported,
      }, [
        el('span', { class: 'sc-mng-type-ico' }, [ctx.util.icon(ICON_BY_TYPE[val] || 'box', { size: 17 })]),
        el('span', { class: 'sc-mng-type-lab', text: lab }),
        unsupported ? el('span', { class: 'sc-mng-type-plan', text: L('Planned · not supported', '계획됨 · 현재 미지원') }) : null,
      ]));
    });
    return el('div', { class: 'sc-mng-full' }, [
      el('label', { class: 'field-label', text: L('Platform', '종류') }), grid,
    ]);
  },

  typeChip(f, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    return el('div', { class: 'sc-mng-full field' }, [
      el('label', { class: 'field-label', text: L('Platform', '종류') }),
      el('div', { class: 'u-row' }, [
        el('span', { class: 'sc-mng-typechip' }, [
          el('span', { class: 'sc-mng-type-ico is-sel' }, [ctx.util.icon(ICON_BY_TYPE[f.type] || 'box', { size: 15 })]),
          el('span', { text: platName(f.type, L) }),
        ]),
        el('span', { class: 'field-hint', text: L('Type is fixed — remove & re-add to change.', '종류 고정 — 변경하려면 제거 후 재추가') }),
      ]),
    ]);
  },

  field(f, k, label, ph, ctx, opts) {
    const { el } = ctx.util.dom;
    const o = opts || {};
    // 라벨↔입력 프로그래매틱 연결 — data-mng-f 와 같은 키로 안정 id 를 만든다
    // (renderModal 포커스 복원은 data-mng-f 기반이라 이 id 와 무관하게 동작한다).
    const fid = 'mng-f-' + k;
    const hintId = fid + '-hint';
    const errId = fid + '-error';
    const error = f.fieldErrors && f.fieldErrors[k];
    const described = [o.hint ? hintId : '', error ? errId : ''].filter(Boolean).join(' ');
    const kids = [el('label', { class: 'field-label', for: fid }, [label, o.req === true ? el('span', { class: 'sc-mng-req', text: ctx.L('required', '필수') }) : (o.req === false ? el('span', { class: 'sc-mng-opt', text: ctx.L('optional', '선택') }) : null)])];
    kids.push(el('input', {
      id: fid,
      class: 'field-input', type: o.type || 'text', 'data-mng-f': k,
      value: f[k] == null ? '' : String(f[k]), placeholder: ph || '',
      list: o.list || null, disabled: !!o.disabled, autocomplete: 'off',
      required: o.req === true, 'aria-required': o.req === true ? 'true' : null,
      'aria-invalid': error ? 'true' : null, 'aria-describedby': described || null,
    }));
    if (o.hint) kids.push(el('div', { class: 'field-hint', id: hintId, text: o.hint }));
    if (error) kids.push(el('div', { class: 'field-error', id: errId, text: error }));
    return el('div', { class: 'field' + (o.full ? ' sc-mng-full' : '') }, kids);
  },

  pwField(f, k, label, ctx, opts) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const o = opts || {};
    const fid = 'mng-f-' + k;
    const errId = fid + '-error';
    const error = f.fieldErrors && f.fieldErrors[k];
    // edit '기존 유지' 행에는 입력이 없다 — for 는 실제 입력을 렌더할 때만 단다.
    const keep = f.mode === 'edit' && !f.showPw[k];
    const head = el('label', keep ? { class: 'field-label' } : { class: 'field-label', for: fid }, [label,
      o.req === true ? el('span', { class: 'sc-mng-req', text: L('required', '필수') })
        : (o.req === false ? el('span', { class: 'sc-mng-opt', text: L('optional', '선택') }) : null)]);
    if (keep) {
      return el('div', { class: 'field' }, [head, el('div', { class: 'u-row' }, [
        el('div', { class: 'sc-mng-keep u-flex', text: L('•••••• keep current', '•••••• 기존 유지') }),
        el('button', { class: 'btn btn--outline btn--sm', type: 'button', 'data-mng-pw': k, text: L('Change', '변경') }),
      ])]);
    }
    return el('div', { class: 'field' }, [head, el('input', {
      id: fid,
      class: 'field-input', type: 'password', 'data-mng-f': k, autocomplete: 'new-password',
      value: f[k] == null ? '' : String(f[k]), placeholder: o.ph || '••••••',
      required: o.req === true, 'aria-required': o.req === true ? 'true' : null,
      'aria-invalid': error ? 'true' : null, 'aria-describedby': error ? errId : null,
    }), error ? el('div', { class: 'field-error', id: errId, text: error }) : null]);
  },

  basicFields(f, grid, ctx) {
    const L = ctx.L;
    grid.appendChild(this.field(f, 'label', L('Device name', '장비 이름'), f.type === 'PLC' ? 'PLC 250.1' : 'LGV', ctx, { req: true }));
    grid.appendChild(this.field(f, 'company', L('Company', '회사'), '루비컴', ctx, { list: 'mng-companies' }));
    grid.appendChild(this.field(f, 'factory', L('Factory', '공장'), '1번공장', ctx, { list: 'mng-factories' }));
    grid.appendChild(this.field(f, 'asset_tag', L('Asset tag', '자산 태그'), 'RB-2024-001', ctx));
    // 플로어 맵 실배치 좌표 — 서버룸 실제 위치(행,열). 비우면 자동 배치.
    grid.appendChild(this.field(f, 'floor_pos', L('Floor position (row,col)', '플로어 위치 (행,열)'), L('e.g. 1,3 — blank = auto', '예: 1,3 — 비우면 자동'), ctx));
  },

  datalists(ctx) {
    const { el } = ctx.util.dom;
    const fleet = ctx.store.getState().fleet || [];
    const uniq = (fn) => Array.from(new Set(fleet.map((d) => (d.meta && fn(d.meta)) || '').filter(Boolean))).sort();
    const mk = (id, list) => el('datalist', { id }, list.map((v) => el('option', { value: v })));
    return el('div', { class: 'u-hide' }, [
      mk('mng-companies', uniq((m) => m.company)),
      mk('mng-factories', uniq((m) => m.factory)),
      mk('mng-vendors', ['Lenovo', 'Dell', 'HPE', 'Supermicro', 'Cisco', 'Fujitsu',
        'Omron', 'Mitsubishi', 'Siemens', 'LS ELECTRIC', 'Keyence', 'Rockwell', 'Schneider']),
    ]);
  },

  connGroup(f, grid, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const t = f.type;
    const mgmtLabel = t === 'PLC' ? 'PLC IP'
      : t === 'SRV' ? L('Server IP', '서버 IP')
        : t === 'NAS' ? 'NAS IP'
          : t === 'WIN' ? L('Windows Server IP', 'Windows 서버 IP')
            : t === 'PI' ? 'Raspberry Pi IP'
              : t === 'PC' ? 'PC IP'
                : t === 'PRN' ? L('Printer IP', '프린터 IP')
                  : L('Management IP (AC)', '관리 IP (AC)');
    const ph = t === 'PLC' ? '192.168.250.1' : t === 'NAS' ? '172.30.1.99' : t === 'PI' ? '172.30.1.79' : '172.30.1.30';
    grid.appendChild(this.field(f, 'mgmt', mgmtLabel, ph, ctx, { req: true }));
    grid.appendChild(this.field(f, 'site', L('Site', '사이트'), 'LAN', ctx));

    if (isFTType(t)) {
      const kids = [
        el('label', { class: 'field-label', for: 'mng-f-api', text: L('Transport', '수집 방식') }),
        el('select', { id: 'mng-f-api', class: 'field-select', 'data-mng-f': 'api' }, [
          ['', L('avcli (auto)', 'avcli (자동)')], ['rest', L('REST API', 'REST API')], ['snmp', L('SNMP only', 'SNMP 전용')],
        ].map(([v, lab]) => el('option', { value: v, selected: f.api === v, text: lab }))),
      ];
      // edit + 비자동 복원값일 때만 — 'avcli (자동)' 선택이 '자동으로 되돌리기'(빈 값 명시
      // 전송, buildBody #392)임을 안내한다. 자동 장비에서는 이 선택이 no-op 이라 숨긴다.
      if (f.mode === 'edit' && f.api) {
        kids.push(el('div', { class: 'field-hint', text: L('Select "avcli (auto)" to revert to automatic collection.', '"avcli (자동)"을 선택하면 자동 수집으로 되돌아갑니다.') }));
      }
      grid.appendChild(el('div', { class: 'field' }, kids));
    }
    if (t === 'SRV' || t === 'PLC') {
      grid.appendChild(this.field(f, 'vendor',
        t === 'PLC' ? L('Maker (auto-detected)', '제조사 (자동 감지)') : L('Vendor', '제조사'),
        t === 'PLC' ? 'Omron / Mitsubishi / Siemens' : 'Lenovo / Dell / HPE', ctx, { list: 'mng-vendors', req: false }));
      if (t === 'SRV') {
        // 일반 서버 수집 방식 — SNMP(OS)/Redfish(BMC) 조합 또는 Proxmox API.
        grid.appendChild(el('div', { class: 'field' }, [
          el('label', { class: 'field-label', for: 'mng-f-platform', text: L('Collection', '수집 방식') }),
          el('select', { id: 'mng-f-platform', class: 'field-select', 'data-mng-f': 'platform' }, [
            ['', L('SNMP (OS agent)', 'SNMP (OS 에이전트)')],
            ['redfish', L('Redfish (BMC: iLO/iDRAC/XCC)', 'Redfish (BMC: iLO/iDRAC/XCC)')],
            ['both', L('SNMP + Redfish', 'SNMP + Redfish')],
            ['proxmox', L('Proxmox VE API', 'Proxmox VE API')],
          ].map(([v, lab]) => el('option', { value: v, selected: (f.platform || '') === v, text: lab }))),
        ]));
        if (f.platform === 'redfish' || f.platform === 'both') {
          grid.appendChild(this.field(f, 'bmc_ip', L('BMC / iLO IP', 'BMC / iLO IP'), '172.30.1.30', ctx, { req: true }));
          grid.appendChild(this.field(f, 'bmc_user', L('BMC user', 'BMC 계정'), f.mode === 'edit' ? L('keep current', '기존 유지') : 'Administrator', ctx));
          grid.appendChild(this.pwField(f, 'bmc_pass', L('BMC password', 'BMC 비밀번호'), ctx));
        }
      }
      if (t === 'PLC') grid.appendChild(this.field(f, 'model', L('Model (auto-detected)', '모델 (자동 감지)'), 'NX1P2-1040DT', ctx, { req: false }));
    }
    if (t === 'PLC') {
      grid.appendChild(this.field(f, 'tags_text',
        L('Process variables (Name=Label(unit))', '공정 변수 (변수명=라벨(단위))'),
        'ProductCount=생산량(ea), CycleTime=사이클타임(s)', ctx, {
          full: true, req: false,
          hint: L('Only variables with Network Publish set in Sysmac Studio are readable.',
            'Sysmac Studio에서 Network Publish(네트워크 공개) 설정된 변수만 읽힙니다.'),
        }));
    }
    if (isFTType(t)) {
      const wrap = el('div', { class: 'sc-mng-full sc-mng-nodes' }, [
        el('h3', { class: 'sc-mng-secttl', text: L('Nodes (IP · SSH)', '노드 (IP · SSH)') }),
      ]);
      [0, 1].forEach((n) => {
        wrap.appendChild(el('div', { class: 'sc-mng-node' }, [
          this.field(f, 'node' + n, 'node' + n + ' IP', n === 0 ? '172.30.1.31' : '172.30.1.32', ctx),
          this.field(f, 'node' + n + '_user', L('SSH user', '아이디'), f.mode === 'edit' ? L('keep current', '기존 유지') : 'root', ctx),
          this.pwField(f, 'node' + n + '_pass', L('SSH password', '비밀번호'), ctx,
            { ph: n === 1 ? L('blank = same as node0', '비우면 node0와 동일') : '••••••' }),
        ]));
      });
      grid.appendChild(wrap);
    }
  },

  authGroup(f, grid, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const t = f.type;
    if (isFTType(t)) {
      grid.appendChild(this.field(f, 'admin_user', L('AC admin user', 'AC 관리자 ID'), f.mode === 'edit' ? L('keep current', '기존 유지') : 'admin', ctx, { req: true }));
      grid.appendChild(this.pwField(f, 'admin_pass', L('AC password', 'AC 비밀번호'), ctx, { req: true }));
      grid.appendChild(this.field(f, 'win_user', L('Windows guest user (WinRM)', 'Windows 게스트 ID (WinRM)'), f.mode === 'edit' ? L('keep current', '기존 유지') : 'administrator', ctx, { req: false }));
      grid.appendChild(this.pwField(f, 'win_pass', L('Windows guest password', 'Windows 게스트 비밀번호'), ctx, { req: false }));
    }
    if (t === 'WIN') {
      grid.appendChild(this.field(f, 'admin_user', L('Windows admin user', 'Windows 관리자 ID'), f.mode === 'edit' ? L('keep current', '기존 유지') : 'administrator', ctx, { req: true }));
      grid.appendChild(this.pwField(f, 'admin_pass', L('Windows password (WinRM 5985)', 'Windows 비밀번호 (WinRM 5985)'), ctx, { req: true }));
    }
    if (t === 'PI') {
      grid.appendChild(this.field(f, 'admin_user', L('SSH user', 'SSH 사용자'), f.mode === 'edit' ? L('keep current', '기존 유지') : 'pi', ctx, { req: true }));
      grid.appendChild(this.pwField(f, 'root_pass', L('SSH password', 'SSH 비밀번호'), ctx, { req: true }));
    }
    if (t === 'SRV' && f.platform === 'proxmox') {
      grid.appendChild(this.field(f, 'admin_user', L('PVE user (realm)', 'PVE 계정 (realm)'), f.mode === 'edit' ? L('keep current', '기존 유지') : 'root@pam', ctx, { req: true }));
      grid.appendChild(this.pwField(f, 'admin_pass', L('PVE password', 'PVE 비밀번호'), ctx, { req: true }));
    }
    if (t !== 'PLC' && t !== 'WIN' && t !== 'PI' && !(t === 'SRV' && (f.platform === 'proxmox' || f.platform === 'redfish'))) {
      grid.appendChild(this.field(f, 'community',
        t === 'PC' ? L('SNMP community (optional)', 'SNMP 커뮤니티 (선택)') : L('SNMP community', 'SNMP 커뮤니티'),
        f.mode === 'edit' ? L('keep current', '기존 유지') : 'public', ctx, { req: t === 'NAS' || (t === 'SRV' && !f.platform) }));
    }
    if (t === 'PLC') {
      grid.appendChild(el('div', { class: 'sc-mng-full sc-mng-note', text:
        L('PLC devices need no credentials — polling is unauthenticated.',
          'PLC는 자격증명이 필요 없습니다 — 인증 없이 폴링합니다.') }));
    }
  },

  vmGroup(f, grid, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const wrap = el('div', { class: 'sc-mng-full field' }, [
      el('label', { class: 'field-label', text: L('VMs (name · IP)', 'VM (이름 · IP)') }),
    ]);
    (f.vms || []).forEach((vm, i) => {
      const nameId = 'mng-vm-name-' + i;
      const ipId = 'mng-vm-ip-' + i;
      wrap.appendChild(el('div', { class: 'sc-mng-vmrow' }, [
        el('label', { class: 'u-sr-only', for: nameId, text: L('VM name ' + (i + 1), 'VM ' + (i + 1) + ' 이름') }),
        el('input', { id: nameId, class: 'field-input', 'data-mng-vm': String(i), 'data-mng-vmk': 'name', value: vm.name || '', placeholder: L('VM name', 'VM 이름') }),
        el('label', { class: 'u-sr-only', for: ipId, text: L('VM IP ' + (i + 1), 'VM ' + (i + 1) + ' IP') }),
        el('input', { id: ipId, class: 'field-input', 'data-mng-vm': String(i), 'data-mng-vmk': 'ip', value: vm.ip || '', placeholder: '172.30.1.51', inputmode: 'url' }),
        el('button', { class: 'sc-mng-act', type: 'button', 'data-mng-vm-del': String(i), title: L('Remove', '삭제'), 'aria-label': L('Remove VM ' + (i + 1), 'VM ' + (i + 1) + ' 삭제') },
          [ctx.util.icon('close', { size: 13 })]),
      ]));
    });
    wrap.appendChild(el('button', { class: 'btn btn--outline btn--sm sc-mng-vmadd', type: 'button', 'data-mng-vm-add': '', text: '+ ' + L('Add VM', 'VM 추가') }));
    grid.appendChild(wrap);
  },

  summary(f, grid, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const rows = [
      [L('Type', '종류'), platName(f.type, L), false],
      [L('Name', '이름'), f.label || DASH, false],
      [L('Management IP', '관리 IP'), f.mgmt || DASH, true],
      [L('Location', '위치'), [f.company, f.factory].filter(Boolean).join(' · ') || DASH, false],
      [L('Site', '사이트'), f.site || DASH, false],
    ];
    const box = el('div', { class: 'sc-mng-summary' });
    rows.forEach(([k, v, mono]) => {
      box.appendChild(el('span', { class: 'u-muted', text: k }));
      box.appendChild(el('span', { class: mono ? 'u-mono' : '', text: v }));
    });
    grid.appendChild(el('div', { class: 'sc-mng-full u-col u-gap-md' }, [
      el('p', { class: 'sc-mng-hint', text: L('Review the device, then run a connection test before saving.', '설정을 확인하고 저장 전에 연결을 테스트하세요.') }),
      box,
    ]));
  },

  testBtn(f, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    return el('button', {
      class: 'btn btn--outline', type: 'button', 'data-mng-test': '', disabled: !!(f.testing || f.busy),
    }, [ctx.util.icon('link', { size: 14 }), f.testing ? L('Testing…', '테스트 중…') : L('Test connection', '연결 테스트')]);
  },

  testArea(f, ctx) {
    const { el } = ctx.util.dom;
    const L = ctx.L;
    const r = f.testResult;
    if (!r) return el('div', { class: 'u-hide' });
    const badge = (ok, lab, sub) => el('span', {
      class: 'u-badge ' + (ok ? 'is-pos' : 'is-neg'),
    }, [ctx.util.icon(ok ? 'check' : 'close', { size: 11 }), lab + (sub ? ' · ' + sub : '')]);

    const head = el('div', { class: 'u-row sc-mng-testrow' }, [
      badge(!!r.reachable, L('Reachable', '도달')),
      badge(!!(r.auth && r.auth.ok), L('Auth', '인증'), r.auth && !r.auth.ok ? r.auth.error : ''),
      r.version ? el('span', { class: 'u-mono u-muted', text: L('version', '버전') + ' ' + r.version }) : null,
      r.transport ? el('span', { class: 'u-badge is-mono', text: r.transport }) : null,
    ]);
    const kids = [head];

    if (r.nodes && r.nodes.length) {
      kids.push(el('h3', { class: 'sc-mng-secttl', text: L('Nodes', '노드') }));
      kids.push(el('div', { class: 'sc-mng-testgrid' }, r.nodes.map((n) => el('div', { class: 'sc-mng-testline' }, [
        el('span', { class: 'u-mono', text: n.name }),
        el('span', { class: 'u-muted', text: n.state }),
        el('span', { class: 'u-muted', text: n.standing || '' }),
        el('span', { text: n.primary ? L('primary', '주') : '' }),
      ]))));
    }
    if (r.vms && r.vms.length) {
      kids.push(el('div', { class: 'u-row--between' }, [
        el('h3', { class: 'sc-mng-secttl', text: L('VMs', 'VM') }),
        // add 위저드에는 VM 입력 행(vmGroup — edit 탭 전용)이 없어 채운 매핑을 확인할 수 없다.
        // 묵반응 버튼으로 확인 불가 매핑이 vm_ips 로 그대로 저장되는 경로를 차단하고,
        // 매핑 확인·수정이 가능한 edit 모드에서만 버튼을 노출한다(#565).
        f.mode === 'edit'
          ? el('button', { class: 'btn btn--ghost btn--sm', type: 'button', 'data-mng-fillvm': '', text: L('Fill VM rows', 'VM 매핑칸 채우기') })
          : null,
      ]));
      kids.push(el('div', { class: 'sc-mng-testgrid' }, r.vms.map((v) => el('div', { class: 'sc-mng-testline' }, [
        el('span', { class: 'u-mono', text: v.name }),
        el('span', { class: 'u-muted', text: v.state }),
        el('span', { text: v.ft ? 'FT' : '' }),
      ]))));
    }
    if (r.warnings && r.warnings.length) {
      kids.push(el('div', { class: 'sc-mng-warn' }, r.warnings.map((w) => el('div', { text: '⚠ ' + w }))));
    }
    return el('div', { class: 'sc-mng-test' }, kids);
  },

  /* ── 이벤트 ────────────────────────────────────────────────────────────── */
  onInput(e) {
    const t = e.target;
    if (!t || !t.getAttribute) return;
    const f = this.form();
    if (!f) return;
    const name = t.getAttribute('data-mng-f');
    if (name) {
      f[name] = t.value;
      if (f.fieldErrors && f.fieldErrors[name]) {
        delete f.fieldErrors[name];
        t.removeAttribute('aria-invalid');
        const err = document.getElementById('mng-f-' + name + '-error');
        if (err) err.remove();
        f.err = null;
        this.patchModalErr(f);
      }
      // key 자동 생성(add 모드) — 재렌더 없이 내부 값만 갱신
      if (f.mode === 'add' && name === 'mgmt') this.autoKey(f);
      // 수집 방식(platform)은 보이는 자격증명 필드가 갈리므로 모달 재구성 필요.
      if (name === 'platform') this.setForm({ err: null, testResult: null, testErr: null });
      return;
    }
    const vmIdx = t.getAttribute('data-mng-vm');
    if (vmIdx != null) {
      const k = t.getAttribute('data-mng-vmk') || 'name';
      const row = (f.vms || [])[Number(vmIdx)];
      if (row) row[k] = t.value;
    }
  },

  autoKey(f) {
    // IPv4는 점을 대시로('ev-172-30-1-30'), 호스트명은 소문자 슬러그('srv-myserver',
    // 'nas-nas01-local')로 파생한다. 종전 [^\d.] 제거는 호스트명 입력 시 key=''
    // (원인 불명의 '식별자 필수' 오류)나 'nas01.local'→'nas-01-' 같은 기형 key를 만들었다.
    const slug = String(f.mgmt || '').trim().toLowerCase()
      .replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
    f.key = slug ? (KEY_PREFIX[f.type] || 'dev') + '-' + slug : '';
  },

  onClick(e) {
    const ctx = this.ctx;
    if (!ctx) return;
    const hit = (sel) => e.target.closest && e.target.closest(sel);
    const st = () => ctx.store.getState();

    // 오버레이 클릭 → 닫기 (Escape 와 동일하게 진행 중(busy)에는 닫지 않는다)
    if (e.target.hasAttribute && e.target.hasAttribute('data-mng-overlay')) {
      const f = this.form();
      if (!f || !f.busy) this.closeModal();
      return;
    }

    let n;
    if ((n = hit('[data-mng-add]'))) { this.openAdd(); return; }
    // X·취소 버튼도 Escape·오버레이와 같은 계약 — 진행 중(busy) 저장·삭제는 닫지 않는다(#319).
    // 닫히면 그 요청의 비동기 완료가 이후에 열린 무관한 모달에 오류를 주입하거나 닫아 버렸다.
    if ((n = hit('[data-mng-close]'))) {
      const f = this.form();
      if (!f || !f.busy) this.closeModal();
      return;
    }
    if ((n = hit('[data-mng-confirm-del]'))) { this.doDelete(); return; }
    if ((n = hit('[data-mng-edit]'))) { e.stopPropagation(); this.openEdit(n.getAttribute('data-mng-edit')); return; }
    if ((n = hit('[data-mng-del]'))) { e.stopPropagation(); this.openDelete(n.getAttribute('data-mng-del')); return; }
    if ((n = hit('[data-mng-toggle]'))) { this.toggleGroup(n.getAttribute('data-mng-toggle')); return; }
    if ((n = hit('[data-mng-type]'))) {
      const f = this.form(); if (!f) return;
      f.type = n.getAttribute('data-mng-type');
      if (!isFTType(f.type) && f.tab === 'vm') f.tab = 'basic';
      this.autoKey(f);
      this.setForm({ err: null, testResult: null, testErr: null });
      return;
    }
    if ((n = hit('[data-mng-tab]'))) { this.setForm({ tab: n.getAttribute('data-mng-tab'), err: null }); return; }
    if ((n = hit('[data-mng-step]'))) {
      const i = Number(n.getAttribute('data-mng-step'));
      if (i <= (st().wizardStep || 0)) { this.setForm({ err: null }); ctx.store.setState({ wizardStep: i }); }
      return;
    }
    if ((n = hit('[data-mng-next]'))) { this.next(); return; }
    if ((n = hit('[data-mng-back]'))) {
      const step = st().wizardStep || 0;
      // step 0 의 '이전'은 닫기와 같다 — busy 가드도 X·Escape 와 맞춘다(#319).
      if (step === 0) { const f = this.form(); if (!f || !f.busy) this.closeModal(); }
      else { this.setForm({ err: null }); ctx.store.setState({ wizardStep: step - 1 }); }
      return;
    }
    if ((n = hit('[data-mng-save]'))) { this.submit(); return; }
    if ((n = hit('[data-mng-test]'))) { this.testConnection(); return; }
    if ((n = hit('[data-mng-fillvm]'))) { this.fillVms(); return; }
    if ((n = hit('[data-mng-pw]'))) {
      const f = this.form(); if (!f) return;
      const k = n.getAttribute('data-mng-pw');
      f.showPw = Object.assign({}, f.showPw, { [k]: true });
      this.setForm({});
      return;
    }
    if ((n = hit('[data-mng-vm-add]'))) {
      const f = this.form(); if (!f) return;
      f.vms = (f.vms || []).concat([{ name: '', ip: '' }]);
      this.setForm({});
      return;
    }
    if ((n = hit('[data-mng-vm-del]'))) {
      const f = this.form(); if (!f) return;
      const i = Number(n.getAttribute('data-mng-vm-del'));
      if ((f.vms || []).length > 1) f.vms = f.vms.filter((_, j) => j !== i);
      this.setForm({});
      return;
    }
    if ((n = hit('[data-mng-row]'))) { ctx.goDetail(n.getAttribute('data-mng-row')); }
  },

  /* ── 모달 열기/닫기 ───────────────────────────────────────────────────── */
  openAdd() {
    if (isSampleState(this.ctx && this.ctx.store.getState())) {
      this.ctx.showToast(this.ctx.L('Sample data is read-only.', '샘플 데이터는 조회만 가능합니다.'));
      return;
    }
    const f = emptyForm();
    this.modalSig = '';
    this.ctx.store.setState({ form: f, wizardStep: 0, editKey: null });
  },

  openEdit(id) {
    const ctx = this.ctx;
    if (isSampleState(ctx && ctx.store.getState())) {
      ctx.store.setState({ editKey: null });
      ctx.showToast(ctx.L('Sample data is read-only.', '샘플 데이터는 조회만 가능합니다.'));
      return;
    }
    const dev = (ctx.store.getState().fleet || []).find((d) => d.id === id);
    // 못 찾으면 editKey를 지워야 한다 — 그대로 두면 render()의 editKey 핸드오프가 매 렌더마다
    // openEdit을 다시 호출해 토스트가 무한 반복된다.
    if (!dev) {
      ctx.store.setState({ editKey: null });
      ctx.showToast(ctx.L('Device not found', '장비를 찾을 수 없습니다'));
      return;
    }
    if (dev.type === 'END') {
      ctx.store.setState({ editKey: null });
      ctx.showToast(ctx.L(
        'Endurance is display-only for imported legacy data; collection and editing are not supported.',
        'Endurance는 가져온 레거시 데이터 조회만 가능하며 수집·수정은 현재 지원되지 않습니다.'
      ));
      return;
    }
    const m = dev.meta || {};
    const nodes = (m.nodes || []).map((x) => x.ip || '');
    const vms = (m.vmList || []).map((v) => ({ name: v.name || '', ip: v.ip || '' }));
    const f = Object.assign(emptyForm(), {
      mode: 'edit', origKey: dev.id, key: dev.id,
      label: m.label || dev.host || '', type: TYPE_KEYS.indexOf(dev.type) >= 0 ? dev.type : 'EV',
      company: m.company || '', factory: m.factory || '', site: dev.site === DASH ? '' : (dev.site || ''),
      mgmt: m.mgmt || '', node0: nodes[0] || '', node1: nodes[1] || '',
      // SRV 수집 방식 — 복원하지 않으면 Redfish/Proxmox 장비가 SNMP 로 렌더돼
      // 연결·인증 탭이 엉뚱한 필드(community 등)를 보여준다.
      platform: m.platform || '',
      // FT 수집 방식(api)도 복원 — 미복원이면 rest/snmp 장비가 항상 'avcli (자동)'으로
      // 렌더된다(#392). api_orig 는 buildBody 가 '자동으로 되돌리기'(빈 값 명시 전송)와
      // '기존 유지'(미전송)를 구분하는 기준이다. meta.api가 응답에 있으면 그 값을 복원한다.
      api: m.api || '', api_orig: m.api || '',
      vendor: m.vendor || '', bmc_ip: (m.bmc && m.bmc.ip) || '',
      model: (m.plc && m.plc.model) || '', asset_tag: m.assetTag || '', floor_pos: m.floorPos || '',
      // 자격증명 계정명은 폴리 meta 에 노출되지 않아 복원 불가 — 디폴트('admin'/'root')를
      // 채워 두면 저장 시 그대로 전송돼 실제 계정을 덮어쓴다. 빈 값 + '기존 유지' placeholder
      // 로 열고, buildBody 가 edit 모드의 빈 자격증명을 빼는 계약과 맞춘다.
      community: '', admin_user: '', node0_user: '', node1_user: '', win_user: '',
      admin_pass: '', root_pass: '', win_pass: '',
      node0_pass: '', node1_pass: '',
      tags_text: ((m.plc && m.plc.procVars) || []).map((t) => t.name + (t.label ? '=' + t.label : '') + (t.unit ? '(' + t.unit + ')' : '')).join(', '),
      vms: vms.length ? vms : [{ name: '', ip: '' }],
      tab: 'basic',
    });
    this.modalSig = '';
    ctx.store.setState({ form: f, editKey: null });
  },

  openDelete(id) {
    const ctx = this.ctx;
    if (isSampleState(ctx && ctx.store.getState())) {
      ctx.showToast(ctx.L('Sample data is read-only.', '샘플 데이터는 조회만 가능합니다.'));
      return;
    }
    const dev = (ctx.store.getState().fleet || []).find((d) => d.id === id);
    if (!dev) return;
    if (dev.type === 'END') {
      ctx.showToast(ctx.L('Legacy Endurance records are display-only.', '레거시 Endurance 레코드는 조회 전용입니다.'));
      return;
    }
    const st = ctx.store.getState();
    this.modalSig = '';
    ctx.store.setState({
      form: {
        mode: 'delete', origKey: dev.id, label: (dev.meta && dev.meta.label) || dev.host,
        operator: ctx.operator || 'admin', impact: deleteImpact(st, dev.id),
        delete_reason: '', delete_phrase: '',
        _rev: 0, err: null, fieldErrors: {}, showPw: {}, vms: [],
      },
    });
  },

  closeModal() {
    if (!this.ctx || !this.form()) return;
    // modalSig을 여기서 미리 비우면 renderModal()의 "!f" 분기가 `if (this.modalSig)` 가드에서
    // 항상 거짓으로 걸려 elModalHost를 절대 비우지 못한다(오버레이가 닫히지 않고 남는 버그) —
    // 이전 sig는 renderModal 쪽에서 스스로 지우도록 그대로 둔다.
    this.ctx.store.setState({ form: null, wizardStep: 0, editKey: null });
  },

  /* ── 위저드 진행/검증 ─────────────────────────────────────────────────── */
  setValidationError(field, message) {
    const f = this.form(); if (!f) return;
    const errors = Object.assign({}, f.fieldErrors || {}, { [field]: message });
    this.setForm({ err: message, fieldErrors: errors });
    queueMicrotask(() => {
      const target = this.elModalHost && this.elModalHost.querySelector('[data-mng-f="' + field + '"]');
      if (target) {
        target.setAttribute('aria-invalid', 'true');
        target.focus();
      }
    });
  },

  next() {
    const ctx = this.ctx;
    const f = this.form(); if (!f) return;
    const L = ctx.L;
    const step = ctx.store.getState().wizardStep || 0;
    if (step === 0 && !String(f.label || '').trim()) { this.setValidationError('label', L('Device name is required', '장비 이름은 필수입니다')); return; }
    if (step === 1 && !String(f.mgmt || '').trim()) { this.setValidationError('mgmt', L('Management IP is required', '관리 IP는 필수입니다')); return; }
    if (step === 2) { const issue = this.reqCredIssue(f, L); if (issue) { this.setValidationError(issue.field, issue.message); return; } }
    this.setForm({ err: null, fieldErrors: {} });
    ctx.store.setState({ wizardStep: Math.min(3, step + 1) });
  },

  /* '필수'(req:true 배지) 인증 필드 검증 — 배지 조건(reqCredFields)과 동일 조건.
     edit 모드는 '비우면 기존 유지' 계약(buildBody)이라, add 모드와 edit 에서 '변경'으로
     입력을 연(showPw) 비밀번호 필드만 검사한다. 비어 있는 첫 필드의 오류 문구 또는 null. */
  reqCredIssue(f, L) {
    const add = f.mode === 'add';
    for (const [k, pw, en, ko] of reqCredFields(f.type, f.platform)) {
      // edit 의 미검사는 '기존 유지' placeholder 를 단 필드(비밀번호 미개방·복원 불가
      // 계정명)에만 해당한다. bmc_ip 는 자격증명이 아니라 openEdit 이 meta.bmc.ip 를
      // 항상 복원하는 연결 필드라 placeholder 가 없다 — edit 에서 SNMP→redfish/both 로
      // 바꾸면(SNMP 장비는 복원값 없음) 또는 복원값을 지우면, * 배지만 달린 채 검증 없이
      // BMC 주소 없는 장비가 저장됐다(#318). 따라서 edit 에서도 항상 검사한다.
      if (!add && !(k === 'bmc_ip' || (pw && f.showPw && f.showPw[k]))) continue;
      if (!String(f[k] || '').trim()) return { field: k, message: L(en, ko) };
    }
    return null;
  },

  reqCredError(f, L) {
    const issue = this.reqCredIssue(f, L);
    return issue && issue.message;
  },

  /* ── 저장 / 삭제 (REST) ─────────────────────────────────────────────── */
  buildBody(f) {
    const vmips = (f.vms || [])
      .map((v) => ({ name: String(v.name || '').trim(), ip: String(v.ip || '').trim() }))
      .filter((v) => v.ip);
    const nodes = [f.node0, f.node1].map((x) => String(x || '').trim()).filter(Boolean);
    const body = Object.assign({}, f, { nodes, vm_ips: vmips, tags: f.type === 'PLC' ? parseTags(f.tags_text) : [] });
    ['tags_text', 'vms', 'showPw', 'err', 'fieldErrors', 'testing', 'testResult', 'testErr', 'busy', '_rev', 'mode', 'origKey', 'api_orig', 'tab'].forEach((k) => { delete body[k]; });
    // 비밀번호를 비워 두면 "기존 값 유지" 의도(수정 모달 힌트 문구와 동일) — 빈 문자열을 전송하면
    // 서버가 기존 자격증명을 빈 값으로 덮어쓸 수 있으므로 body에서 제외한다.
    ['node0_pass', 'node1_pass', 'admin_pass', 'root_pass', 'win_pass', 'bmc_pass'].forEach((k) => {
      if (body[k] === '') delete body[k];
    });
    // edit 모드에서는 계정명·community·수집 방식(api)·bmc_ip 도 같은 계약 — 폴리가 머지
    // 의미론이면 빈 값 전송이 기존 설정을 지우므로, 비어 있으면 아예 싣지 않는다(복원된 값·
    // 새 입력은 전송). bmc_ip 의 빈 값은 '비우면 유지'다 — redfish/both 의 빈 값은
    // reqCredError(#318)가 이미 차단하므로 여기 도달하는 '' 는 BMC 무관 플랫폼뿐이다.
    // 예외: api 는 복원값(api_orig)이 있는데 사용자가 'avcli (자동)'을 골라 '' 가 된 경우
    // '자동으로 되돌리기' 의도다 — 빈 값을 명시 전송해 리셋한다(#392). 이 경우가 아니면
    // (원래 자동) 종전대로 미전송 = 기존 유지.
    if (f.mode === 'edit') {
      ['admin_user', 'node0_user', 'node1_user', 'win_user', 'bmc_user', 'community', 'api', 'bmc_ip'].forEach((k) => {
        if (body[k] === '' && !(k === 'api' && f.api_orig)) delete body[k];
      });
    }
    return body;
  },

  async api(method, url, body) {
    if (String(method || 'GET').toUpperCase() !== 'GET'
      && isSampleState(this.ctx && this.ctx.store.getState())) {
      return { ok: false, error: this.ctx.L('Sample data is read-only.', '샘플 데이터는 조회만 가능합니다.') };
    }
    try {
      const headers = {};
      if (body) headers['Content-Type'] = 'application/json';
      const r = await fetch(url, {
        method,
        headers: Object.keys(headers).length ? headers : undefined,
        body: body ? JSON.stringify(body) : undefined,
        cache: 'no-store',
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) return { ok: false, error: j.error || ('HTTP ' + r.status) };
      return Object.assign({ ok: true }, j);
    } catch (err) {
      return { ok: false, error: String((err && err.message) || err) };
    }
  },

  async submit() {
    const ctx = this.ctx;
    const f = this.form(); if (!f || f.busy) return;
    const L = ctx.L;
    if (!String(f.mgmt || '').trim()) { this.setValidationError('mgmt', L('Management IP is required', '관리 IP는 필수입니다')); return; }
    if (!String(f.label || '').trim()) { this.setValidationError('label', L('Device name is required', '장비 이름은 필수입니다')); return; }
    const reqIssue = this.reqCredIssue(f, L);
    if (reqIssue) { this.setValidationError(reqIssue.field, reqIssue.message); return; }
    const fp = String(f.floor_pos || '').trim();
    if (fp && !/^\d{1,3}\s*[,\-]\s*\d{1,3}$/.test(fp)) { this.setValidationError('floor_pos', L('Floor position format: row,col (e.g. 1,3)', '플로어 위치 형식: 행,열 (예: 1,3)')); return; }
    if (f.mode === 'add') this.autoKey(f);
    // key 입력 UI는 없고 mgmt 에서만 파생된다 — 오류는 사용자가 고칠 수 있는 mgmt 필드로 연결한다.
    if (f.mode === 'add' && !String(f.key || '').trim()) {
      this.setValidationError('mgmt', L('Management IP/hostname must contain letters or digits (device key is derived from it)', '관리 IP·호스트명에 영문 또는 숫자가 있어야 합니다(장비 식별자가 여기서 만들어집니다)'));
      return;
    }
    // 중복 key는 서버 POST 전에 막는다 — POST 후에 검사하면 서버에는 등록되고 로컬만
    // 실패해 상태가 어긋난다(사전 검증 블록으로 이동).
    if (f.mode === 'add' && (ctx.store.getState().fleet || []).some((d) => d.id === f.key)) {
      this.setValidationError('mgmt', L('Duplicate key', '이미 존재하는 식별자입니다')); return;
    }

    this.setForm({ busy: true, err: null });
    const body = this.buildBody(f);
    const url = f.mode === 'edit' ? '/api/clusters/' + encodeURIComponent(f.origKey) : '/api/clusters';
    const res = await this.api(f.mode === 'edit' ? 'PUT' : 'POST', url, body);
    // await 사이에 폼이 교체·폐기됐을 수 있다(busy 중 UI 닫기는 막지만, 프로그램 경로 —
    // editKey 핸드오프·다른 open* — 는 남아 있다). 시작한 폼과 다륩다면 현재 폼에
    // 오류를 주입하거나 닫지 않는다(#319). 서버에 성공한 반영(fleet 갱신)과 토스트는
    // 폼과 무관하게 적용한다. setForm 이 같은 객체를 패치하므로 동일성은 참조 비교로 충분하다.
    const sameForm = this.form() === f;
    if (!res.ok) {
      if (sameForm) this.setForm({ busy: false, err: res.error });
      else ctx.showToast(L('Save failed', '저장 실패') + ': ' + res.error);
      return;
    }

    const st = ctx.store.getState();
    const fleet = (st.fleet || []).slice();
    if (f.mode === 'add') {
      ctx.store.setState(sameForm ? { form: null, wizardStep: 0 } : {});
      ctx.showToast(res.restart_required
        ? L('Saved — polling starts after poller restart', '저장됨 — 폴러 재시작 후 수집 시작') + ': ' + f.label
        : (L('Device added', '장비를 추가했습니다') + ': ' + f.label));
    } else {
      const i = fleet.findIndex((d) => d.id === f.origKey);
      if (i >= 0) {
        const dev = Object.assign({}, fleet[i]);
        dev.site = String(f.site || '').trim() || DASH;
        dev.meta = Object.assign({}, dev.meta, {
          label: f.label, company: f.company, factory: f.factory,
          mgmt: f.mgmt, assetTag: f.asset_tag, floorPos: String(f.floor_pos || '').trim(),
          vendor: f.vendor || (dev.meta && dev.meta.vendor) || '',
        });
        if (f.bmc_ip && dev.meta.bmc) dev.meta.bmc = Object.assign({}, dev.meta.bmc, { ip: f.bmc_ip });
        // PUT 성공 직후 폴러가 변경을 반영하기 전까지 표시 필드를 유지한다.
        // data.pullPatch(mergeLocalWrites)가 서버 스냅샷 위에 다시 적용한다.
        dev.meta.localEdit = {
          ts: Date.now(),
          site: dev.site,
          label: dev.meta.label, company: dev.meta.company, factory: dev.meta.factory,
          mgmt: dev.meta.mgmt, assetTag: dev.meta.assetTag, floorPos: dev.meta.floorPos,
          vendor: dev.meta.vendor, bmcIp: String(f.bmc_ip || '').trim(),
        };
        fleet[i] = dev;
      }
      // 표시 필드 수정은 lastPoll도 갱신해 다음 폴링 전 화면에 즉시 반영한다.
      ctx.store.setState(sameForm
        ? { fleet, form: null, wizardStep: 0, lastPoll: Date.now() }
        : { fleet, lastPoll: Date.now() });
      ctx.showToast(L('Device saved', '장비를 저장했습니다') + ': ' + f.label);
    }
    this.modalSig = '';
    this.treeSig = '';
  },

  async doDelete() {
    const ctx = this.ctx;
    const f = this.form(); if (!f || f.busy) return;
    const L = ctx.L;
    const issue = validateDeleteConfirmation(f, L);
    if (issue) { this.setValidationError(issue.field, issue.message); return; }
    this.setForm({ busy: true, err: null });
    const res = await this.api('DELETE', '/api/clusters/' + encodeURIComponent(f.origKey), {
      reason: String(f.delete_reason || '').trim(),
    });
    // submit 과 동형의 동일성 검사(#319) — 시작한 삭제 확인 폼이 아니면 현재 폼에
    // 오류를 주입하거나 닫지 않는다. 서버 성공분의 fleet 반영·토스트는 그대로 적용한다.
    const sameForm = this.form() === f;
    if (!res.ok) {
      if (sameForm) this.setForm({ busy: false, err: L('Remove failed', '제거 실패') + ': ' + res.error });
      else ctx.showToast(L('Remove failed', '제거 실패') + ': ' + res.error);
      return;
    }
    const st = ctx.store.getState();
    const fleet = (st.fleet || []).filter((d) => d.id !== f.origKey);
    // 장비 삭제는 화면의 fleet 행만 제거하는 것이 아니라 장비 id를 키로 쓰는 로컬·공유
    // 상태도 함께 정리해야 한다. 남겨 두면 같은 id로 재등록할 때 예전 메모·점검 창·확인
    // 상태가 되살아나고, 영속 저장소에는 다시는 사용되지 않을 고아 레코드가 쌓인다(#129).
    const id = String(f.origKey || '');
    const ackPrefix = id + '\u0001';
    const ackedAlerts = Object.assign({}, st.ackedAlerts || {});
    Object.keys(ackedAlerts).forEach((key) => {
      if (key === id || key.indexOf(ackPrefix) === 0) delete ackedAlerts[key];
    });
    const maint = Object.assign({}, st.maint || {});
    const notes = Object.assign({}, st.notes || {});
    delete maint[id];
    delete notes[id];
    const patch = { fleet, ackedAlerts, maint, notes };
    if (sameForm) patch.form = null;
    if (st.selected === f.origKey) patch.selected = null;
    ctx.store.setState(patch);
    ctx.showToast(L('Device removed', '장비를 제거했습니다') + ': ' + f.label);
    this.modalSig = '';
    this.treeSig = '';
  },

  /* ── 연결 테스트 ──────────────────────────────────────────────────────── */
  async testConnection() {
    const ctx = this.ctx;
    const f = this.form(); if (!f || f.testing) return;
    const L = ctx.L;
    if (!String(f.mgmt || '').trim()) { this.setForm({ testErr: L('Management IP is required', '관리 IP는 필수입니다'), testResult: null }); return; }
    this.setForm({ testing: true, testErr: null, testResult: null });

    let result = null; let error = null;
    const res = await this.api('POST', '/api/clusters/test', this.buildBody(f));
    if (res.ok) result = res; else error = res.error;
    // await 사이에 폼이 교체·폐기됐을 수 있다 — 테스트 중엔 busy=false라 닫기가 허용되고
    // (닫기 계약은 busy 만 본다), 닫은 뒤 다른 모달을 열면 form 객체가 바뀐다. 시작한 폼
    // f와 다륩다면 결과·에러를 현재 폼에 주입하지 않는다(submit·doDelete 의 #319 와 동형).
    // setForm 이 같은 객체를 패치하므로 동일성은 참조 비교로 충분하다(#353).
    const cur = this.form();
    if (cur !== f) return;
    cur._testRev = (cur._testRev || 0) + 1;
    this.setForm({ testing: false, testResult: result, testErr: error });
  },


  fillVms() {
    const f = this.form(); if (!f || !f.testResult || !f.testResult.vms) return;
    const existing = Object.create(null);
    (f.vms || []).forEach((v) => { if (v.name) existing[v.name] = v.ip; });
    f.vms = f.testResult.vms.map((v) => ({ name: v.name, ip: existing[v.name] || '' }));
    if (f.mode === 'edit') f.tab = 'vm';
    this.setForm({});
  },
};

export default screen;
