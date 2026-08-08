// js/screens/settings.js
// serverdesk — 설정 화면(Settings). REBUILD-SPEC.md §1.9 / §5.1 / §5.5.
//
// 구성: 언어 토글 / 회사 색상 관리(팔레트+커스텀+리셋, 토폴로지 연동) /
//       Critical 웹훅(상태배지·URL·저장·테스트·해제) / 알림 임계값(읽기전용, Warning 78·Critical 90) /
//       환경설정 토글(자동 새로고침/알림 사운드/고밀도 레이아웃, 기존 store.setg) /
//       오래된 경보 자동 확인(해제/7일/30일 — store.setg.ackAutoDays, 처리는 app.js::applyAutoAck) /
//       제품 정보(개발사 Roobicom 연락처 — 웹사이트·이메일·전화, 정적 값 + 라벨만 i18n).
//
// 규칙 준수:
//  - js/screens/* 를 import하지 않는다(전혀 import 없음 — ctx로 전달받는 model/util만 사용).
//  - DOM은 init()에서 1회 생성, render()는 값만 patch(placeholder/입력값 재대입으로 트랜지션·타이핑 방해 금지).
//  - 로컬 클릭 위임 1개(root, 'click') + 컬러피커/입력 갱신을 위한 'input' 위임 1개(색상 input은 change가 아닌
//    input 이벤트로만 변경을 알리므로 클릭 위임 하나로는 커버되지 않아 부득이 두 번째 위임을 둔다 — §5.2 취지 내 예외).
//  - 색은 CSS 변수(var(--pos) 등)만 사용. 단, 회사 색상 스와치/팔레트 버튼의 배경색은 사용자가 고르는
//    데이터 값 자체(팔레트는 compute.js COMPANY_PALETTE, 커스텀은 컬러피커 결과)라 인라인 style이 불가피하다.

const THRESHOLDS = [
  { key: 'warn', tone: 'warn', pct: 78, labelEn: 'Warning (amber)', labelKo: '경고 (주황)' },
  { key: 'neg', tone: 'neg', pct: 90, labelEn: 'Critical (red)', labelKo: '심각 (빨강)' },
];

const SETG_ROWS = [
  // 설명 문구는 실제 데이터 소스에 따라 달라진다 — 실 폴러로 도는 중에 "시뮬레이션 갱신"이라고
  // 적혀 있으면 운영자에게 실장비를 시뮬레이션이라고 말하는 셈이다(실측 결함).
  { key: 'refresh',
    nameEn: 'Auto refresh', nameKo: '자동 새로고침',
    metaEn: 'Advances the live simulation every 1.2s', metaKo: '1.2초 간격으로 시뮬레이션 갱신',
    // 이 토글은 라이브 수신을 끄지 못한다 — 3초 pull 은 ARCHITECTURE.md §4.3 계약상 setg.refresh 와
    // 무관하게 돌고, 꺼지는 건 1.2초 tick 하트비트(화면 경과시간 갱신 등)뿐이다. 문구가 마치 수신
    // 주기를 제어하는 것처럼 읽히면 운영자가 데이터 신선도 제어를 오해한다 — 수신 지속을 명시한다.
    metaLiveEn: 'Off keeps the 3s snapshot fetch running (collector polls devices every %s) — only the 1.2s screen heartbeat pauses',
    metaLiveKo: '꺼도 3초 스냅샷 수신은 계속(장비 수집 주기 %s) · 1.2초 화면 하트비트만 멈춥니다' },
  { key: 'sound', nameEn: 'Alert sound', nameKo: '알림 사운드', metaEn: 'Plays a chime on warning-or-above alerts', metaKo: '경고 등급 이상 경보에서만 재생' },
  { key: 'dense', nameEn: 'Dense layout', nameKo: '고밀도 레이아웃', metaEn: 'Tighter row spacing across all screens', metaKo: '전체 화면의 행 간격을 좁게 표시' },
];

let S = null; // init~destroy 사이의 인스턴스 상태(단일 활성 화면 가정)

// ---------------------------------------------------------------------------
// 방어적 헬퍼 (ctx.util 로드 실패해도 화면이 죽지 않도록)
// ---------------------------------------------------------------------------
function mkIcon(ctx, name) {
  try {
    if (ctx && ctx.util && typeof ctx.util.icon === 'function') {
      const svg = ctx.util.icon(name, { size: 15 });
      if (svg) return svg;
    }
  } catch (e) { /* noop */ }
  return document.createElement('span');
}

function L(ctx, en, ko) {
  try {
    if (ctx && typeof ctx.L === 'function') return ctx.L(en, ko);
  } catch (e) { /* noop */ }
  return ko;
}

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}

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

// ---------------------------------------------------------------------------
// DOM 최초 1회 생성
// ---------------------------------------------------------------------------
function cardHead(ctx, iconName) {
  const head = el('div', 'card-head');
  if (iconName) {
    const ico = el('span', 'sc-set-card-ico');
    ico.appendChild(mkIcon(ctx, iconName));
    head.appendChild(ico);
  }
  const title = el('h2', 'card-title');
  const sub = el('span', 'card-sub');
  head.appendChild(title);
  head.appendChild(sub);
  return { head, title, sub };
}

function buildCoRow(name, palette) {
  const row = el('div', 'sc-set-co-row');
  row.setAttribute('data-set-co-row', '');
  row.dataset.co = name;

  const info = el('div', 'sc-set-co-info');
  const swatch = el('span', 'sc-set-co-swatch');
  swatch.setAttribute('data-set-co-swatch', '');
  const nameEl = el('span', 'sc-set-co-name', name);
  info.appendChild(swatch);
  info.appendChild(nameEl);

  const actions = el('div', 'sc-set-co-actions');

  // S: 40개 스와치 상시 노출 → 회사별 '현재 색 + 변경 어포던스'로 접고, 클릭 시 온디맨드로 팔레트를 펼친다.
  // 팔레트(8색)+커스텀+리셋은 이 래퍼에 담아 collapsed 기본에서 숨기고, is-expanded 에서만 노출한다.
  const paletteWrap = el('div', 'sc-set-co-palette');
  const palBtns = [];
  palette.forEach((p) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'sc-set-co-pal';
    b.setAttribute('data-set-co-pal', '');
    // 토글 스와치 — #389 전례대로 aria-pressed 를 생성 시 굽고 renderCoRows 에서 갱신(#430).
    b.setAttribute('aria-pressed', 'false');
    b.dataset.color = p;
    b.style.background = p; // 데이터(팔레트 색) 자체를 보여주는 스와치 — §5.5 예외
    b.title = p;
    paletteWrap.appendChild(b);
    palBtns.push(b);
  });

  // 커스텀 컬러피커 트리거. icon.js 레지스트리엔 pencil류 아이콘이 없어(등록 시 콘솔 경고 유발)
  // 점선 원 + 중앙 점만으로 "커스텀 색상" 트리거임을 표시한다(장식 텍스트, title로 의미 보강).
  const customLabel = el('label', 'sc-set-co-custom');
  const customDot = el('span', 'sc-set-co-custom-dot');
  customLabel.appendChild(customDot);
  const customInput = document.createElement('input');
  customInput.type = 'color';
  customInput.setAttribute('data-set-co-custom', '');
  customLabel.appendChild(customInput);
  paletteWrap.appendChild(customLabel);

  const resetBtn = document.createElement('button');
  resetBtn.type = 'button';
  resetBtn.className = 'sc-set-co-reset';
  resetBtn.setAttribute('data-set-co-reset', '');
  resetBtn.hidden = true;
  paletteWrap.appendChild(resetBtn);

  // '변경' 토글 — collapsed 에선 유일한 액션(펼치기), expanded 에선 '완료'(접기)로 라벨 전환.
  const changeBtn = document.createElement('button');
  changeBtn.type = 'button';
  changeBtn.className = 'sc-set-co-change';
  changeBtn.setAttribute('data-set-co-change', '');

  actions.appendChild(paletteWrap);
  actions.appendChild(changeBtn);

  row.appendChild(info);
  row.appendChild(actions);

  return { row, swatch, palBtns, customLabel, customInput, resetBtn, changeBtn };
}

function buildDom(ctx) {
  const root = el('div', 'sc-set-wrap');

  const head = el('div', 'sc-set-head');
  const title = el('h1', 'sc-set-title');
  const sub = el('p', 'sc-set-sub');
  head.appendChild(title);
  head.appendChild(sub);
  root.appendChild(head);

  // E4: 2열 CSS 그리드는 행 정렬 때문에 짧은 '알림' 카드 아래에 ~200px 세로 공백이 생겼다(우열 비대칭).
  //     카드를 masonry(멀티컬럼) 컨테이너에 담아 카드 단위로 흐르게 해, 열 간 세로 공백을 수렴시킨다.
  const cards = el('div', 'sc-set-cards');
  root.appendChild(cards);

  // 언어(표시 언어)는 단독 카드로 두면 토글 하나만 담고 카드 절반이 빈다(고립 빈 패널) —
  // 아래 '환경설정' 카드에 첫 행으로 병합한다. 세그먼트 컨트롤 refs 는 여기서 만들어 넘긴다.

  // ── 회사 색상 카드 ──
  const coCard = el('section', 'card sc-set-card');
  coCard.setAttribute('data-set-co-card', '');
  coCard.hidden = true;
  const coH = cardHead(ctx, null);
  coCard.appendChild(coH.head);
  const coBody = el('div', 'card-body');
  const coHint = el('p', 'sc-set-hint');
  const coList = el('div', 'sc-set-co-list');
  coList.setAttribute('data-set-co-list', '');
  coBody.appendChild(coHint);
  coBody.appendChild(coList);
  coCard.appendChild(coBody);
  /* 열 균형(심사 반영): 큰 환경설정 카드를 선두로 — 메이슨리가 [환경설정+회사색]|[알림+임계]로 나눠 우하단 공백 해소 */

  // ── 알림(Critical 웹훅) 카드 ──
  const hookCard = el('section', 'card sc-set-card');
  const hookH = cardHead(ctx, 'bell');
  hookCard.appendChild(hookH.head);
  const hookBody = el('div', 'card-body');
  const hookRow = el('div', 'sc-set-row');
  const hookLabel = el('div', 'sc-set-row-label');
  const hookName = el('div', 'sc-set-row-name');
  // 웹훅 URL 입력의 accessible name — 옆 라벨 텍스트를 id 로 연결한다(스위치 전례와 동일).
  // render() 가 언어 전환 시 textContent 만 갱신하므로(id 불변) 참조가 stale 해지지 않는다.
  hookName.id = 'sc-set-hook-name';
  const hookMeta = el('div', 'sc-set-row-meta');
  hookLabel.appendChild(hookName);
  hookLabel.appendChild(hookMeta);
  const hookStatus = el('span', 'u-badge');
  hookStatus.setAttribute('data-set-hook-status', '');
  hookRow.appendChild(hookLabel);
  hookRow.appendChild(hookStatus);
  hookBody.appendChild(hookRow);

  const hookForm = el('div', 'sc-set-hook-form');
  const hookInput = document.createElement('input');
  hookInput.className = 'field-input u-mono sc-set-hook-input';
  hookInput.type = 'url';
  hookInput.spellcheck = false;
  hookInput.autocomplete = 'off';
  hookInput.setAttribute('data-set-hook-input', '');
  hookInput.setAttribute('aria-labelledby', hookName.id);
  const hookBtns = el('div', 'btn-group');
  const hookSave = el('button', 'btn btn--primary btn--sm');
  hookSave.type = 'button';
  hookSave.setAttribute('data-set-hook-save', '');
  const hookTest = el('button', 'btn btn--outline btn--sm');
  hookTest.type = 'button';
  hookTest.setAttribute('data-set-hook-test', '');
  const hookClear = el('button', 'btn btn--danger btn--sm');
  hookClear.type = 'button';
  hookClear.setAttribute('data-set-hook-clear', '');
  hookClear.hidden = true;
  hookBtns.appendChild(hookSave);
  hookBtns.appendChild(hookTest);
  hookBtns.appendChild(hookClear);
  hookForm.appendChild(hookInput);
  hookForm.appendChild(hookBtns);
  hookBody.appendChild(hookForm);

  const hookMsg = el('div', 'sc-set-hook-msg');
  hookMsg.setAttribute('data-set-hook-msg', '');
  // 저장·테스트 발송 결과 모두 이 인라인 메시지로만 표시된다(테스트는 토스트도 없음) —
  // index.html 의 토스트와 같은 선언(role=status + aria-live=polite)으로 AT 에도 전달한다(#274).
  // showHookMsg 가 hidden 해제 후 textContent 를 바꾸므로 변경이 리전에 기록된다.
  hookMsg.setAttribute('role', 'status');
  hookMsg.setAttribute('aria-live', 'polite');
  hookMsg.hidden = true;
  hookBody.appendChild(hookMsg);
  hookCard.appendChild(hookBody);
  /* hookCard 배치는 하단 열 균형 블록에서 1회 (이중 append 제거) */

  // ── 알림 임계값 카드(읽기전용) ──
  const thCard = el('section', 'card sc-set-card');
  const thH = cardHead(ctx, 'warningCircle');
  thCard.appendChild(thH.head);
  const thBody = el('div', 'card-body');
  const thRefs = {};
  THRESHOLDS.forEach((t) => {
    const row = el('div', 'sc-set-thresh-row');
    const top = el('div', 'sc-set-thresh-top');
    const lbl = el('span', 'sc-set-thresh-label');
    const val = el('span', 'sc-set-thresh-val is-' + t.tone, t.pct + '%');
    top.appendChild(lbl);
    top.appendChild(val);
    const track = el('div', 'sc-set-thresh-track');
    // 채운 바가 아니라 임계 위치의 얇은 틱 마커(중립 트랙 위) — red 색면 최소화(settings.css 참조).
    const fill = el('div', 'sc-set-thresh-fill is-' + t.tone);
    fill.style.left = t.pct + '%';
    track.appendChild(fill);
    row.appendChild(top);
    row.appendChild(track);
    thBody.appendChild(row);
    thRefs[t.key] = { lbl, val, fill };
  });
  // 편집 폼 — 읽기전용 틱 아래에 warn/crit 입력 + 저장/기본값(서버 config.thresholds 가 정본).
  const thForm = el('div', 'sc-set-th-form');
  const thWarnIn = document.createElement('input');
  thWarnIn.className = 'field-input sc-set-th-in';
  thWarnIn.type = 'number'; thWarnIn.min = '1'; thWarnIn.max = '99'; thWarnIn.step = '1';
  thWarnIn.setAttribute('data-set-th-warn', '');
  const thCritIn = document.createElement('input');
  thCritIn.className = 'field-input sc-set-th-in';
  thCritIn.type = 'number'; thCritIn.min = '2'; thCritIn.max = '100'; thCritIn.step = '1';
  thCritIn.setAttribute('data-set-th-crit', '');
  const thWarnLb = el('span', 'sc-set-th-inlab');
  const thCritLb = el('span', 'sc-set-th-inlab');
  const thSave = el('button', 'btn btn--primary btn--sm');
  thSave.type = 'button';
  thSave.setAttribute('data-set-th-save', '');
  const thReset = el('button', 'btn btn--outline btn--sm');
  thReset.type = 'button';
  thReset.setAttribute('data-set-th-reset', '');
  const thMsg = el('span', 'sc-set-th-msg');
  thForm.appendChild(thWarnLb); thForm.appendChild(thWarnIn);
  thForm.appendChild(thCritLb); thForm.appendChild(thCritIn);
  thForm.appendChild(thSave); thForm.appendChild(thReset); thForm.appendChild(thMsg);
  thBody.appendChild(thForm);
  thCard.appendChild(thBody);
  /* thCard 배치도 하단 열 균형 블록에서 1회 */

  // ── 환경설정 카드(언어 병합) ──
  const setgCard = el('section', 'card sc-set-card');
  const setgH = cardHead(ctx, 'checklist');
  setgCard.appendChild(setgH.head);
  const setgBody = el('div', 'card-body toggle-list');
  const setgRefs = {};

  // 언어 행 — 토글 행과 같은 bordered box 로 두어 환경설정 목록에 자연스럽게 편입(고립 빈 패널 제거).
  const langRow = el('div', 'toggle-row sc-set-lang-row');
  const langBody = el('div', 'toggle-body');
  const langName = el('div', 'toggle-name');
  const langMeta = el('div', 'toggle-meta');
  langBody.appendChild(langName);
  langBody.appendChild(langMeta);
  const langSeg = el('div', 'sc-set-seg');
  const btnEn = el('button', 'sc-set-seg-btn', 'EN');
  btnEn.type = 'button';
  btnEn.setAttribute('data-set-lang', 'en');
  const btnKo = el('button', 'sc-set-seg-btn', '한국어');
  btnKo.type = 'button';
  btnKo.setAttribute('data-set-lang', 'ko');
  langSeg.appendChild(btnEn);
  langSeg.appendChild(btnKo);
  langRow.appendChild(langBody);
  langRow.appendChild(langSeg);
  setgBody.appendChild(langRow);

  // 테마 행 — 시스템/라이트/다크 3-세그. 'system' 은 data-theme 미부착(= OS prefers-color-scheme 자동).
  // 즉시 스탬프는 onClick 에서, 재방문 없는 첫 페인트는 index.html 조기 스탬프가 담당(FOUC 방지 쌍).
  const themeRow = el('div', 'toggle-row sc-set-lang-row');
  const themeBody = el('div', 'toggle-body');
  const themeName = el('div', 'toggle-name');
  const themeMeta = el('div', 'toggle-meta');
  themeBody.appendChild(themeName);
  themeBody.appendChild(themeMeta);
  const themeSeg = el('div', 'sc-set-seg');
  const mkThemeBtn = (val, txt) => {
    const b = el('button', 'sc-set-seg-btn', txt);
    b.type = 'button';
    b.setAttribute('data-set-theme', val);
    themeSeg.appendChild(b);
    return b;
  };
  const btnThS = mkThemeBtn('system', '');
  const btnThL = mkThemeBtn('light', '');
  const btnThD = mkThemeBtn('dark', '');
  themeRow.appendChild(themeBody);
  themeRow.appendChild(themeSeg);
  setgBody.appendChild(themeRow);

  // 오래된 경보 자동 확인 행 — 해제/7일/30일 3-세그. 미확인 방치 경보를 자동으로
  // 확인 처리(아카이브)해 배지·카운트가 수십 일 전 경보로 영구 고정되는 것을 막는다.
  // 원본 경보는 지우지 않는다(확인 해제로 되돌릴 수 있다). 실제 처리는 app.js::applyAutoAck.
  const ackRow = el('div', 'toggle-row sc-set-lang-row');
  const ackBody = el('div', 'toggle-body');
  const ackName = el('div', 'toggle-name');
  const ackMeta = el('div', 'toggle-meta');
  ackBody.appendChild(ackName);
  ackBody.appendChild(ackMeta);
  const ackSeg = el('div', 'sc-set-seg');
  const mkAckBtn = (val, txt) => {
    const b = el('button', 'sc-set-seg-btn', txt);
    b.type = 'button';
    b.setAttribute('data-set-ackauto', String(val));
    ackSeg.appendChild(b);
    return b;
  };
  const btnAck0 = mkAckBtn(0, '');
  const btnAck7 = mkAckBtn(7, '');
  const btnAck30 = mkAckBtn(30, '');
  ackRow.appendChild(ackBody);
  ackRow.appendChild(ackSeg);
  setgBody.appendChild(ackRow);

  // 에스컬레이션 행 — critical 미확인 방치 시 웹훅 재통보(해제/4시간/24시간).
  // 웹훅 URL 은 아래 Critical 웹훅 카드의 것을 쓴다(두 군데 설정 금지). 발송은 app.js::applyEscalation.
  const escRow = el('div', 'toggle-row sc-set-lang-row');
  const escBody = el('div', 'toggle-body');
  const escName = el('div', 'toggle-name');
  const escMeta = el('div', 'toggle-meta');
  escBody.appendChild(escName);
  escBody.appendChild(escMeta);
  const escSeg = el('div', 'sc-set-seg');
  const mkEscBtn = (val, txt) => {
    const b = el('button', 'sc-set-seg-btn', txt);
    b.type = 'button';
    b.setAttribute('data-set-eschours', String(val));
    escSeg.appendChild(b);
    return b;
  };
  const btnEsc0 = mkEscBtn(0, '');
  const btnEsc4 = mkEscBtn(4, '');
  const btnEsc24 = mkEscBtn(24, '');
  escRow.appendChild(escBody);
  escRow.appendChild(escSeg);
  setgBody.appendChild(escRow);

  SETG_ROWS.forEach((r) => {
    const row = el('div', 'toggle-row');
    const body = el('div', 'toggle-body');
    const name = el('div', 'toggle-name');
    // role=switch 의 accessible name — 옆 라벨 텍스트를 id 로 연결한다. render() 가 언어 전환 시
    // name.textContent 만 갱신하므로(id 불변) 참조가 stale 해지지 않는다.
    name.id = 'sc-set-sw-name-' + r.key;
    const meta = el('div', 'toggle-meta');
    body.appendChild(name);
    body.appendChild(meta);
    const sw = el('span', 'switch');
    sw.setAttribute('data-set-toggle', r.key);
    sw.setAttribute('role', 'switch');
    sw.setAttribute('aria-labelledby', name.id);
    sw.tabIndex = 0;
    const knob = el('span', 'switch-knob');
    sw.appendChild(knob);
    row.appendChild(body);
    row.appendChild(sw);
    setgBody.appendChild(row);
    setgRefs[r.key] = { name, meta, sw };
  });

  // 관리 API 토큰 행 — serve.py 를 --token 으로 띄운 운영 모드에서 manage 쓰기(장비 추가·수정·
  // 삭제·연결 테스트)는 X-Serverdesk-Token 헤더 일치를 요구한다. 값은 setg.token 에 영속하고
  // manage.js api() 가 헤더로 첨부한다. 평문 노출 방지: password 입력 + 저장값을 다시
  // 화면에 채우지 않는다 — 설정 여부는 placeholder·'해제' 버튼 노출로만 알린다(웹훅 카드 패턴).
  const tokRow = el('div', 'toggle-row sc-set-token-row');
  const tokBody = el('div', 'toggle-body');
  const tokName = el('div', 'toggle-name');
  // 관리 API 토큰 입력의 accessible name — 옆 라벨 텍스트를 id 로 연결한다(스위치 전례와 동일).
  // render() 가 언어 전환 시 textContent 만 갱신하므로(id 불변) 참조가 stale 해지지 않는다.
  tokName.id = 'sc-set-token-name';
  const tokMeta = el('div', 'toggle-meta');
  tokBody.appendChild(tokName);
  tokBody.appendChild(tokMeta);
  const tokForm = el('div', 'sc-set-token-form');
  const tokInput = document.createElement('input');
  tokInput.className = 'field-input u-mono sc-set-token-input';
  tokInput.type = 'password';
  tokInput.spellcheck = false;
  tokInput.autocomplete = 'off';
  tokInput.setAttribute('data-set-token-input', '');
  tokInput.setAttribute('aria-labelledby', tokName.id);
  const tokSave = el('button', 'btn btn--primary btn--sm');
  tokSave.type = 'button';
  tokSave.setAttribute('data-set-token-save', '');
  const tokClear = el('button', 'btn btn--outline btn--sm');
  tokClear.type = 'button';
  tokClear.setAttribute('data-set-token-clear', '');
  tokClear.hidden = true;
  tokForm.appendChild(tokInput);
  tokForm.appendChild(tokSave);
  tokForm.appendChild(tokClear);
  tokRow.appendChild(tokBody);
  tokRow.appendChild(tokForm);
  setgBody.appendChild(tokRow);
  setgCard.appendChild(setgBody);
  // ── 백업 / 복구 카드 — 설정 JSON(비밀 마스킹)·가용성 CSV 다운로드 + 파일 복구.
  const bkCard = el('section', 'card sc-set-card');
  const bkH = cardHead(ctx, 'db');
  bkCard.appendChild(bkH.head);
  const bkBody = el('div', 'card-body');
  const bkHint = el('p', 'sc-set-hint');
  const bkRow = el('div', 'btn-group sc-set-bk-btns');
  const bkExp = document.createElement('a');
  bkExp.className = 'btn btn--outline btn--sm';
  bkExp.href = '/api/admin/config/export';
  bkExp.setAttribute('download', '');
  const bkCsv = document.createElement('a');
  bkCsv.className = 'btn btn--outline btn--sm';
  bkCsv.href = '/api/availability.csv';
  bkCsv.setAttribute('download', '');
  const bkImp = el('button', 'btn btn--primary btn--sm');
  bkImp.type = 'button';
  bkImp.setAttribute('data-set-bk-import', '');
  const bkFile = document.createElement('input');
  bkFile.type = 'file';
  bkFile.accept = 'application/json,.json';
  bkFile.className = 'u-hide';
  bkFile.setAttribute('data-set-bk-file', '');
  bkFile.addEventListener('change', () => {
    const f = bkFile.files && bkFile.files[0];
    bkFile.value = ''; // 같은 파일을 연속 선택핸 change 가 다시 오게 초기화
    if (f) importConfig(ctx, f);
  });
  const bkMsg = el('div', 'sc-set-th-msg');
  bkRow.appendChild(bkExp); bkRow.appendChild(bkCsv); bkRow.appendChild(bkImp);
  bkBody.appendChild(bkHint); bkBody.appendChild(bkRow);
  bkBody.appendChild(bkFile); bkBody.appendChild(bkMsg);
  bkCard.appendChild(bkBody);

  // ── 제품 정보(About) 카드 — 개발사 연락처. 값(회사명·URL·메일·전화)은 언어 무관 정적,
  //    라벨만 render() 가 L() 로 갱신한다. 연락처는 고객 지원 채널과 동일해야 한다(roobicom.co.kr 기준).
  const aboutCard = el('section', 'card sc-set-card');
  const aboutH = cardHead(ctx, null);
  aboutCard.appendChild(aboutH.head);
  const aboutBody = el('div', 'card-body');
  const mkAboutRow = () => {
    const row = el('div', 'sc-set-row sc-set-about-row');
    const lbl = el('div', 'sc-set-row-label');
    const name = el('div', 'sc-set-row-name');
    lbl.appendChild(name);
    row.appendChild(lbl);
    aboutBody.appendChild(row);
    return { row, name };
  };
  const abCo = mkAboutRow();
  abCo.row.appendChild(el('div', 'sc-set-about-val', 'Roobicom (루비컴)'));
  const abWeb = mkAboutRow();
  const abWebA = el('a', 'sc-set-about-val sc-set-about-link', 'roobicom.co.kr');
  abWebA.href = 'https://roobicom.co.kr';
  abWebA.target = '_blank';
  abWebA.rel = 'noopener noreferrer'; // target=_blank 의 탭내핑 방지
  abWeb.row.appendChild(abWebA);
  const abMail = mkAboutRow();
  const abMailA = el('a', 'sc-set-about-val sc-set-about-link', 'contact@roobicom.co.kr');
  abMailA.href = 'mailto:contact@roobicom.co.kr';
  abMail.row.appendChild(abMailA);
  const abTel = mkAboutRow();
  abTel.row.appendChild(el('div', 'sc-set-about-val', '055-346-5868 (본사) · 031-636-3674 (지사)'));
  aboutCard.appendChild(aboutBody);

  // 열 균형 순서(심사 반영): 환경설정(대형) → 회사색 | 알림 → 임계 — appendChild 이동 의미론에
  // 기대지 않고 여기서 1회 명시 배치한다(리뷰 지적: 죽은 셀렉터·이중 append 제거).
  [setgCard, coCard, hookCard, thCard, bkCard, aboutCard].forEach((c) => cards.appendChild(c));

  return {
    root,
    title, sub,
    langName, langMeta, btnEn, btnKo,
    themeName, themeMeta, btnThS, btnThL, btnThD,
    ackName, ackMeta, btnAck0, btnAck7, btnAck30,
    escName, escMeta, btnEsc0, btnEsc4, btnEsc24,
    tokName, tokMeta, tokInput, tokSave, tokClear,
    coCard, coTitle: coH.title, coSub: coH.sub, coHint, coList,
    hookTitle: hookH.title, hookName, hookMeta, hookStatus,
    hookInput, hookSave, hookTest, hookClear, hookMsg,
    thTitle: thH.title, thSub: thH.sub, thRefs,
    thWarnIn, thCritIn, thWarnLb, thCritLb, thSave, thReset, thMsg,
    setgTitle: setgH.title, setgRefs,
    bkTitle: bkH.title, bkSub: bkH.sub, bkHint, bkExp, bkCsv, bkImp, bkFile, bkMsg,
    aboutTitle: aboutH.title, aboutSub: aboutH.sub,
    aboutNames: { co: abCo.name, web: abWeb.name, mail: abMail.name, tel: abTel.name },
  };
}

// ---------------------------------------------------------------------------
// 회사 색상: 데이터 참조(이름 목록)가 바뀔 때만 재생성, 그 외엔 값만 patch
// ---------------------------------------------------------------------------
function renderCompanyColors(state, ctx) {
  const dom = S.dom;
  let data = null;
  try {
    if (ctx.model && typeof ctx.model.buildCompanyColors === 'function') {
      data = ctx.model.buildCompanyColors(state);
    }
  } catch (e) { data = null; }
  const list = (data && data.list) || [];
  const palette = (data && data.palette) || [];

  dom.coTitle.textContent = L(ctx, 'Company colors', '회사 색상');
  dom.coSub.textContent = L(ctx, 'Synced with Topology', '토폴로지 연동');
  dom.coHint.textContent = L(
    ctx,
    'Colors that distinguish companies in the topology. Click a swatch or pick a custom color.',
    '토폴로지에서 회사를 시각적으로 구분하는 색입니다. 스와치를 누르거나 커스텀 색을 고르세요.'
  );

  dom.coCard.hidden = list.length === 0;
  if (list.length === 0) return;

  // 구분자 없는 join 은 서로 다른 목록이 같은 키로 붕괴할 수 있다('ab','c' vs 'a','bc')
  // — 회사명에 포함될 수 없는 \u0001 을 구분자로 쓴다.
  const namesKey = list.map((c) => c.name).join('\u0001');
  if (S.coNamesKey !== namesKey) {
    dom.coList.innerHTML = '';
    // 키가 서버 데이터(회사명) — __proto__ 등 프로토타입 키와 충돌하지 않도록 null-프로토타입 객체 사용.
    S.coRows = Object.create(null);
    list.forEach((c) => {
      const built = buildCoRow(c.name, palette);
      dom.coList.appendChild(built.row);
      S.coRows[c.name] = built;
    });
    S.coNamesKey = namesKey;
  }

  list.forEach((c) => {
    const row = S.coRows[c.name];
    if (!row) return;
    row.swatch.style.background = c.color; // 사용자가 고른/기본 배정된 실데이터 색 — §5.5 예외
    row.palBtns.forEach((b) => {
      const active = String(b.dataset.color).toLowerCase() === String(c.color).toLowerCase();
      b.classList.toggle('is-active', active);
      b.setAttribute('aria-pressed', active ? 'true' : 'false');
      // 접근명 — title 이 원시 hex('#3182F6')뿐이라 AT 가 "무엇을 고르는 버튼인지" 알 수 없었다(#135).
      // 회사명+색상을 포함한 aria-label 을 매 render 갱신해 언어 전환을 따라간다(커스텀 input 전례와 동일).
      b.setAttribute('aria-label', L(ctx,
        'Set ' + c.name + ' color to ' + b.dataset.color,
        c.name + ' 색상을 ' + b.dataset.color + ' 로 변경'));
    });
    if (/^#[0-9a-f]{6}$/i.test(c.color)) row.customInput.value = c.color;
    row.customLabel.title = L(ctx, 'Custom color', '커스텀 색');
    // input[type=color] 는 opacity:0 으로 덮여 있고 title 은 label 에 있어 input 의 접근명이
    // 없었다 — input 자체에 회사명 포함 aria-label 을 둔다. 매 render 갱신이라 언어 전환을 따라간다.
    row.customInput.setAttribute('aria-label', L(ctx, 'Custom color for ' + c.name, c.name + ' 커스텀 색'));
    row.resetBtn.hidden = !c.custom;
    row.resetBtn.textContent = L(ctx, 'Reset', '기본');
    row.resetBtn.title = L(ctx, 'Reset to default', '기본값으로');
    // S: 온디맨드 펼침 — 기본은 접힘(현재색+변경), '변경' 클릭 시 팔레트 노출.
    const expanded = !!(S.expanded && S.expanded[c.name]);
    row.row.classList.toggle('is-expanded', expanded);
    row.changeBtn.textContent = expanded ? L(ctx, 'Done', '완료') : L(ctx, 'Change', '변경');
    row.changeBtn.setAttribute('aria-expanded', expanded ? 'true' : 'false');
    row.changeBtn.title = expanded
      ? L(ctx, 'Collapse palette', '팔레트 접기')
      : L(ctx, 'Change color', '색 변경');
  });
}

function setCompanyColor(ctx, name, color) {
  if (!name || !color) return;
  const st = ctx.store.getState();
  const next = Object.assign({}, st.companyColors, { [name]: color });
  ctx.store.setState({ companyColors: next });
}

function resetCompanyColor(ctx, name) {
  if (!name) return;
  const st = ctx.store.getState();
  const next = Object.assign({}, st.companyColors);
  delete next[name];
  ctx.store.setState({ companyColors: next });
}

// ---------------------------------------------------------------------------
// Critical 웹훅
// ---------------------------------------------------------------------------
function updateHookButtons(ctx) {
  if (!S) return;
  const dom = S.dom;
  const st = ctx.store.getState();
  const val = (dom.hookInput.value || '').trim();
  const busy = !!S.busy;
  const on = !!(st.notify && st.notify.enabled);
  dom.hookSave.disabled = busy || !val;
  dom.hookTest.disabled = busy || (!val && !on);
  dom.hookClear.disabled = busy;
}

function showHookMsg(ctx, ok, text) {
  if (!S) return;
  const m = S.dom.hookMsg;
  clearTimeout(S.hookMsgTimer);
  m.hidden = false;
  m.textContent = (ok ? '✓ ' : '✕ ') + text;
  m.classList.toggle('is-pos', !!ok);
  m.classList.toggle('is-neg', !ok);
  S.hookMsgTimer = setTimeout(() => { if (S && S.dom && S.dom.hookMsg) S.dom.hookMsg.hidden = true; }, 5000);
}

// 웹훅 URL 형식 검증 — type=url 이지만 form 제출이 아니라 브라우저 제약 검증이 작동하지
// 않아 "hello" 같은 값도 저장되던 구멍이다(#138). 저장 직전 http(s) 만 통과시킨다.
function isHttpUrl(val) {
  try {
    const u = new URL(val);
    return u.protocol === 'http:' || u.protocol === 'https:';
  } catch (e) { return false; }
}

function saveHook(ctx, clear) {
  if (!S || S.busy) return;
  const val = clear ? '' : (S.dom.hookInput.value || '').trim();
  if (!clear && !val) return;
  if (!clear && !isHttpUrl(val)) {
    showHookMsg(ctx, false, L(ctx, 'Enter a valid http(s) webhook URL', '유효한 http(s) 웹훅 URL을 입력하세요'));
    return;
  }
  S.busy = clear ? 'clear' : 'save';
  render(ctx.store.getState(), ctx);
  // 서버 저장 경로는 없다 — serve.py 는 /notify·/notify/test 중계만 제공할 뿐
  // 폴러 /api/notify 는 404(실측)라 POST 마다 '서버 응답 실패'로 떨어졌다(#273, 사장 호출).
  // localStorage(store.js LS_NOTIFY)가 정본이므로 로컬 영속만 하고 문구도 그렇게 말한다.
  ctx.store.setState({ notify: { enabled: !!val, url: val } });
  if (!clear) S.dom.hookInput.value = '';
  S.busy = '';
  const msg = clear
    ? L(ctx, 'Cleared', '해제되었습니다')
    : L(ctx, 'Saved in this browser', '이 브라우저에 저장했습니다');
  showHookMsg(ctx, true, msg);
  try { ctx.showToast(msg); } catch (e) { /* noop */ }
  render(ctx.store.getState(), ctx);
}

// 설정 복구 — 파일 선택 → 확인 → POST /api/admin/config/import.
// 내보내기 문서의 빈 자격증명은 서버가 기존 값으로 머지한다. 장비·수집 변경은 재시작 후 적용.
async function importConfig(ctx, file) {
  if (!S || S.busy) return;
  let text = '';
  try {
    text = await file.text();
  } catch (e) {
    S.dom.bkMsg.textContent = String((e && e.message) || e);
    S.dom.bkMsg.className = 'sc-set-th-msg is-neg';
    return;
  }
  if (!window.confirm(L(ctx,
    'Overwrite current configuration with this file? (blank credentials keep current values)',
    '현재 설정을 이 파일 내용으로 덮어씁니다. 계속할까요? (빈 자격증명은 기존 값 유지)'))) {
    return;
  }
  S.busy = 'bk';
  render(ctx.store.getState(), ctx);
  try {
    const headers = { 'Content-Type': 'application/json' };
    const tok = (ctx.store.getState().setg || {}).token || '';
    if (tok) headers['X-Serverdesk-Token'] = tok;
    const r = await fetch('/api/admin/config/import', { method: 'POST', headers, body: text });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
    S.dom.bkMsg.textContent = j.message || L(ctx, 'Restored', '복구했습니다');
    S.dom.bkMsg.className = 'sc-set-th-msg is-pos';
  } catch (e) {
    S.dom.bkMsg.textContent = String((e && e.message) || e);
    S.dom.bkMsg.className = 'sc-set-th-msg is-neg';
  }
  S.busy = '';
  render(ctx.store.getState(), ctx);
}

// 임계값 저장 — PUT /api/admin/thresholds(파일 기록 + 폴리 라이브 반영).
// 성공하면 다음 폴(3초)부터 state.thresholds 도 갱신되지만 즉시 반영해 깜빡임을 없앤다.
async function saveThresholds(ctx, reset) {
  if (!S || S.busy) return;
  const warn = reset ? 78 : Number(S.dom.thWarnIn.value);
  const crit = reset ? 90 : Number(S.dom.thCritIn.value);
  if (!(warn > 0 && warn < crit && crit <= 100)) {
    S.dom.thMsg.textContent = L(ctx, 'Need 0 < warning < critical ≤ 100', '0 < 경고 < 심각 ≤ 100 이어야 합니다');
    S.dom.thMsg.className = 'sc-set-th-msg is-neg';
    return;
  }
  S.busy = 'th';
  render(ctx.store.getState(), ctx);
  try {
    const headers = { 'Content-Type': 'application/json' };
    const tok = (ctx.store.getState().setg || {}).token || '';
    if (tok) headers['X-Serverdesk-Token'] = tok;
    const r = await fetch('/api/admin/thresholds', { method: 'PUT', headers, body: JSON.stringify({ warn, crit }) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
    ctx.store.setState({ thresholds: { warn, crit } });
    S.dom.thMsg.textContent = L(ctx, 'Saved — applies fleet-wide', '저장했습니다 — 전체 장비에 반영됩니다');
    S.dom.thMsg.className = 'sc-set-th-msg is-pos';
  } catch (e) {
    S.dom.thMsg.textContent = String((e && e.message) || e);
    S.dom.thMsg.className = 'sc-set-th-msg is-neg';
  }
  S.busy = '';
  render(ctx.store.getState(), ctx);
}

async function testHook(ctx) {
  if (!S || S.busy) return;
  const st = ctx.store.getState();
  const val = (S.dom.hookInput.value || '').trim() || (st.notify && st.notify.url) || '';
  if (!val) return;
  S.busy = 'test';
  render(ctx.store.getState(), ctx);
  try {
    // serve.py /notify/test 가 Slack(text)·Discord(content) 양식으로 중계한다(실측:
    // 폴러 /api/notify 는 404, 브라우저 직발은 Slack CORS 에 걸린다 — 릴리가 정답).
    // 타임아웃은 중계(serve.py timeout=5)·실발송(app.js 3000)보다 길어야 한다 — 짧으면
    // 느린 웹훅이 실제 성공인데 테스트만 abort 로 실패한다(#320).
    const res = await fetchTimeout('/notify/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        url: val,
        text: L(ctx, 'serverdesk webhook test — if you see this, the relay works',
          'serverdesk 웹훅 테스트 — 이 메시지가 보이면 중계가 정상입니다'),
      }),
    }, 6000);
    const j = res ? await res.json().catch(() => null) : null;
    if (res && res.ok && j && j.ok) {
      showHookMsg(ctx, true, L(ctx, 'Sent · webhook HTTP ' + j.status, '테스트 발송됨 · 웹훅 HTTP ' + j.status));
    } else {
      // 비-2xx 웹훅 실패도 릴리는 HTTP 200 + {ok:false, status:<실제 코드>} 로 본낸다
      // (serve.py). 성공 분기와 같이 j.status 를 우선 표기하고, 없을 때만 릴리의
      // res.status 로 폴핵한다 — 안 그러면 '발송 실패 · HTTP 200' 모순 표기(#472).
      const code = res ? ' · HTTP ' + ((j && j.status) || res.status) : '';
      // 실패 사유 키 — 서버(serve.py _send_json_error)는 모든 실패 경로에서 {'error': message} 를
      // 본낸다. j.message 만 읽던 탓에 'notify target host not allowed' 같은 사유가 영구 미표시였다
      // (#437) — error 우선, message 는 폴핵.
      const why = (j && (j.error || j.message)) || '';
      showHookMsg(ctx, false, L(ctx, 'Send failed' + code, '발송 실패' + code) + (why ? ' — ' + why : ''));
    }
  } catch (e) {
    // abort(시간 초과)와 연결 실패(백엔드 없음)를 구분한다 —
    // 느린 웹훅을 '백엔드 없음'으로 오진단하지 않도록(#320).
    if (e && e.name === 'AbortError') {
      showHookMsg(ctx, false, L(ctx, 'Timed out — webhook is slow or unreachable', '시간 초과 — 웹훅 응답이 느리거나 도달할 수 없습니다'));
    } else {
      showHookMsg(ctx, false, L(ctx, 'No backend reachable (offline simulation)', '백엔드 없음(오프라인 시뮬레이션)'));
    }
  }
  // await 사이에 화면을 이탈하면 destroy() 가 S=null 로 만든다 — null 역참조 방지.
  if (!S) return;
  S.busy = '';
  render(ctx.store.getState(), ctx);
}

// ---------------------------------------------------------------------------
// 관리 API 토큰 (setg.token — manage 쓰기의 X-Serverdesk-Token)
// ---------------------------------------------------------------------------
function updateTokenButtons() {
  if (!S) return;
  const dom = S.dom;
  const val = (dom.tokInput.value || '').trim();
  dom.tokSave.disabled = !val;
}

function saveToken(ctx) {
  if (!S) return;
  const val = (S.dom.tokInput.value || '').trim();
  if (!val) return;
  ctx.store.setState((s) => ({ setg: Object.assign({}, s.setg, { token: val }) }));
  // 입력창은 항상 비운다 — 저장된 토큰을 평문으로 다시 보여주지 않기 위함.
  S.dom.tokInput.value = '';
  try { ctx.showToast(L(ctx, 'API token saved', 'API 토큰을 저장했습니다')); } catch (e) { /* noop */ }
  render(ctx.store.getState(), ctx);
}

function clearToken(ctx) {
  if (!S) return;
  ctx.store.setState((s) => ({ setg: Object.assign({}, s.setg, { token: '' }) }));
  S.dom.tokInput.value = '';
  try { ctx.showToast(L(ctx, 'API token cleared', 'API 토큰을 해제했습니다')); } catch (e) { /* noop */ }
  render(ctx.store.getState(), ctx);
}

// ---------------------------------------------------------------------------
// 이벤트 위임
// ---------------------------------------------------------------------------
function onClick(ev, ctx) {
  const t = ev.target;
  if (!(t instanceof Element)) return;
  let hit;
  if ((hit = t.closest('[data-set-theme]'))) {
    const val = hit.getAttribute('data-set-theme');
    if (val === 'system' || val === 'light' || val === 'dark') {
      // 즉시 스탬프(전 화면 반영) + 상태/영속(store.persist 가 sd.theme 기록, system 은 키 삭제).
      if (val === 'light' || val === 'dark') document.documentElement.setAttribute('data-theme', val);
      else document.documentElement.removeAttribute('data-theme');
      ctx.store.setState({ theme: val });
    }
    return;
  }
  if ((hit = t.closest('[data-set-lang]'))) {
    const val = hit.getAttribute('data-set-lang');
    if (val === 'en' || val === 'ko') ctx.store.setState({ lang: val });
    return;
  }
  if ((hit = t.closest('[data-set-eschours]'))) {
    const v = Number(hit.getAttribute('data-set-eschours'));
    const hours = (v === 4 || v === 24) ? v : 0;
    ctx.store.setState((st) => ({ setg: Object.assign({}, st.setg, { escHours: hours }) }));
    return;
  }
  if ((hit = t.closest('[data-set-ackauto]'))) {
    const v = Number(hit.getAttribute('data-set-ackauto'));
    const days = (v === 7 || v === 30) ? v : 0;
    ctx.store.setState((s) => ({ setg: Object.assign({}, s.setg, { ackAutoDays: days }) }));
    return;
  }
  if ((hit = t.closest('[data-set-co-pal]'))) {
    const row = t.closest('[data-set-co-row]');
    if (row) setCompanyColor(ctx, row.dataset.co, hit.dataset.color);
    return;
  }
  if ((hit = t.closest('[data-set-co-reset]'))) {
    const row = t.closest('[data-set-co-row]');
    if (row) resetCompanyColor(ctx, row.dataset.co);
    return;
  }
  if ((hit = t.closest('[data-set-co-change]'))) {
    const row = t.closest('[data-set-co-row]');
    if (row && S) {
      const nm = row.dataset.co;
      S.expanded[nm] = !S.expanded[nm];
      render(ctx.store.getState(), ctx);
    }
    return;
  }
  if ((hit = t.closest('[data-set-hook-save]'))) { saveHook(ctx, false); return; }
  if ((hit = t.closest('[data-set-hook-test]'))) { testHook(ctx); return; }
  if ((hit = t.closest('[data-set-hook-clear]'))) { saveHook(ctx, true); return; }
  if ((hit = t.closest('[data-set-th-save]'))) { saveThresholds(ctx, false); return; }
  if ((hit = t.closest('[data-set-th-reset]'))) { saveThresholds(ctx, true); return; }
  if ((hit = t.closest('[data-set-bk-import]'))) { if (S && S.dom.bkFile) S.dom.bkFile.click(); return; }
  if ((hit = t.closest('[data-set-token-save]'))) { saveToken(ctx); return; }
  if ((hit = t.closest('[data-set-token-clear]'))) { clearToken(ctx); return; }
  if ((hit = t.closest('[data-set-toggle]'))) {
    const key = hit.getAttribute('data-set-toggle');
    ctx.store.setState((s) => ({ setg: Object.assign({}, s.setg, { [key]: !(s.setg && s.setg[key]) }) }));
    return;
  }
}

// role="switch" span 은 버튼이 아니라 Space/Enter 로 click 이 발생하지 않는다 —
// 키보드/스크린리더 사용자를 위해 직접 토글한다(WCAG 2.1.1). onClick 의 토글과 동일 로직.
function onKeydown(ev, ctx) {
  const t = ev.target;
  if (!(t instanceof Element)) return;
  // 웹훅 입력창 Enter = 저장(type=url 이지만 form 이 아니라 브라우저 검증이 걸리지 않는다).
  // URL 검증은 saveHook 의 isHttpUrl 이 담당한다(#138).
  if (t.closest && t.closest('[data-set-hook-input]') && ev.key === 'Enter') {
    ev.preventDefault();
    saveHook(ctx, false);
    return;
  }
  const sw = t.closest('[data-set-toggle]');
  if (!sw) return;
  if (ev.key === ' ' || ev.key === 'Enter' || ev.key === 'Spacebar') {
    ev.preventDefault();
    const key = sw.getAttribute('data-set-toggle');
    ctx.store.setState((s) => ({ setg: Object.assign({}, s.setg, { [key]: !(s.setg && s.setg[key]) }) }));
  }
}

function onInput(ev, ctx) {
  const t = ev.target;
  if (!(t instanceof Element)) return;
  if (t.matches && t.matches('[data-set-co-custom]')) {
    const row = t.closest('[data-set-co-row]');
    if (row) setCompanyColor(ctx, row.dataset.co, t.value);
    return;
  }
  if (t.matches && t.matches('[data-set-hook-input]')) {
    updateHookButtons(ctx);
  }
  if (t.matches && t.matches('[data-set-token-input]')) {
    updateTokenButtons();
  }
}

// ---------------------------------------------------------------------------
// 모듈 인터페이스 (REBUILD-SPEC §5.1)
// ---------------------------------------------------------------------------
function init(root, ctx) {
  const dom = buildDom(ctx);
  root.appendChild(dom.root);

  S = {
    root,
    dom,
    busy: '',
    coNamesKey: null,
    coRows: Object.create(null),  // 키가 서버 데이터(회사명) — __proto__ 엣지 방지로 null-프로토타입
    expanded: Object.create(null), // S: 회사별 팔레트 펼침 상태(온디맨드) — 동일 사유
    hookMsgTimer: null,
  };
  S.onClick = (ev) => onClick(ev, ctx);
  S.onInput = (ev) => onInput(ev, ctx);
  S.onKeydown = (ev) => onKeydown(ev, ctx);
  root.addEventListener('click', S.onClick);
  root.addEventListener('input', S.onInput);
  root.addEventListener('keydown', S.onKeydown);
}

function render(state, ctx) {
  if (!S) return;
  const dom = S.dom;

  dom.title.textContent = L(ctx, 'Settings', '설정');
  dom.sub.textContent = L(
    ctx,
    'Console-wide preferences: language, company colors, alert webhook and thresholds.',
    '언어·회사 색상·경보 웹훅·임계값 등 콘솔 전역 환경설정입니다.'
  );

  // 언어(환경설정 카드 첫 행)
  dom.langName.textContent = L(ctx, 'Display language', '표시 언어');
  dom.langMeta.textContent = L(ctx, 'Applies to the entire console', '콘솔 전체에 적용됩니다');
  const isEn = state.lang === 'en';
  dom.btnEn.classList.toggle('is-active', isEn);
  dom.btnEn.setAttribute('aria-pressed', isEn ? 'true' : 'false');
  dom.btnKo.classList.toggle('is-active', !isEn);
  dom.btnKo.setAttribute('aria-pressed', !isEn ? 'true' : 'false');

  // 테마(환경설정 카드 둘째 행)
  dom.themeName.textContent = L(ctx, 'Theme', '테마');
  dom.themeMeta.textContent = L(ctx, 'System follows OS dark mode', '시스템은 OS 다크 모드를 따라갑니다');
  dom.btnThS.textContent = L(ctx, 'System', '시스템');
  dom.btnThL.textContent = L(ctx, 'Light', '라이트');
  dom.btnThD.textContent = L(ctx, 'Dark', '다크');
  const th = state.theme === 'light' || state.theme === 'dark' ? state.theme : 'system';
  [[dom.btnThS, 'system'], [dom.btnThL, 'light'], [dom.btnThD, 'dark']].forEach(([b, v]) => {
    b.classList.toggle('is-active', th === v);
    b.setAttribute('aria-pressed', th === v ? 'true' : 'false');
  });

  // 오래된 경보 자동 확인(환경설정 카드 셋째 행)
  dom.ackName.textContent = L(ctx, 'Auto-acknowledge stale alerts', '오래된 경보 자동 확인');
  dom.ackMeta.textContent = L(
    ctx,
    'Alerts left unacknowledged past the limit are marked acknowledged (originals are kept)',
    '설정한 기간을 넘긴 미확인 경보를 자동으로 확인 처리합니다(원본은 지우지 않습니다)'
  );
  dom.btnAck0.textContent = L(ctx, 'Off', '해제');
  dom.btnAck7.textContent = L(ctx, '7 days', '7일');
  dom.btnAck30.textContent = L(ctx, '30 days', '30일');
  const aad = state.setg && (state.setg.ackAutoDays === 7 || state.setg.ackAutoDays === 30) ? state.setg.ackAutoDays : 0;
  [[dom.btnAck0, 0], [dom.btnAck7, 7], [dom.btnAck30, 30]].forEach(([b, v]) => {
    b.classList.toggle('is-active', aad === v);
    b.setAttribute('aria-pressed', aad === v ? 'true' : 'false');
  });

  // 에스컬레이션(환경설정 카드 넷째 행)
  dom.escName.textContent = L(ctx, 'Critical escalation', 'Critical 에스컬레이션');
  dom.escMeta.textContent = L(
    ctx,
    'Re-notify the webhook when a critical alert stays unacknowledged past the limit',
    'critical 경보가 설정 시간을 넘겨 미확인이면 웹훅으로 재통보합니다'
  );
  dom.btnEsc0.textContent = L(ctx, 'Off', '해제');
  dom.btnEsc4.textContent = L(ctx, '4h', '4시간');
  dom.btnEsc24.textContent = L(ctx, '24h', '24시간');
  const esh = state.setg && (state.setg.escHours === 4 || state.setg.escHours === 24) ? state.setg.escHours : 0;
  [[dom.btnEsc0, 0], [dom.btnEsc4, 4], [dom.btnEsc24, 24]].forEach(([b, v]) => {
    b.classList.toggle('is-active', esh === v);
    b.setAttribute('aria-pressed', esh === v ? 'true' : 'false');
  });

  // 회사 색상
  renderCompanyColors(state, ctx);

  // Critical 웹훅
  dom.hookTitle.textContent = L(ctx, 'Notifications', '알림');
  dom.hookName.textContent = L(ctx, 'Critical webhook', 'Critical 웹훅');
  dom.hookMeta.textContent = L(
    ctx,
    'everRun E-Alert is off, so Vigil-style critical alerts post to this webhook. Slack and Discord URLs work.',
    'everRun E-Alert가 꺼져 있어, critical 발생 시 이 웹훅으로 통보합니다. Slack · Discord URL 지원합니다.'
  );
  const on = !!(state.notify && state.notify.enabled);
  dom.hookStatus.className = 'u-badge' + (on ? ' is-pos' : '');
  dom.hookStatus.textContent = on ? L(ctx, 'Configured', '설정됨') : L(ctx, 'Not set', '미설정');
  dom.hookInput.placeholder = on
    ? L(ctx, 'Enter a new URL to replace', '새 URL 입력 시 교체')
    : 'https://discord.com/api/webhooks/…';
  dom.hookSave.textContent = S.busy === 'save' ? '…' : L(ctx, 'Save', '저장');
  dom.hookTest.textContent = S.busy === 'test' ? '…' : L(ctx, 'Send test', '테스트 발송');
  dom.hookClear.textContent = S.busy === 'clear' ? '…' : L(ctx, 'Clear', '해제');
  dom.hookClear.hidden = !on;
  updateHookButtons(ctx);

  // 임계값 — 서버 thresholds(state.thresholds)가 정본, 미수신 시 78/90.
  dom.thTitle.textContent = L(ctx, 'Alert thresholds', '알림 임계값');
  dom.thSub.textContent = L(ctx, 'Applied fleet-wide · edit below', '전체 장비 적용 중 · 아래에서 수정');
  const liveTh = (state.thresholds && typeof state.thresholds.warn === 'number') ? state.thresholds : { warn: 78, crit: 90 };
  THRESHOLDS.forEach((t) => {
    const live = t.key === 'warn' ? liveTh.warn : liveTh.crit;
    dom.thRefs[t.key].lbl.textContent = L(ctx, t.labelEn, t.labelKo);
    dom.thRefs[t.key].val.textContent = live + '%';
    dom.thRefs[t.key].fill.style.left = live + '%';
  });
  dom.thWarnLb.textContent = L(ctx, 'Warning %', '경고 %');
  dom.thCritLb.textContent = L(ctx, 'Critical %', '심각 %');
  dom.thSave.textContent = S.busy === 'th' ? '…' : L(ctx, 'Save', '저장');
  dom.thReset.textContent = L(ctx, 'Reset to 78/90', '기본값(78/90)');
  dom.thSave.disabled = !!S.busy;
  dom.thReset.disabled = !!S.busy;
  // 편집 중인 입력은 덮어쓰지 않는다(포커스 보호 — 관제 콘솔 입력 관례).
  if (document.activeElement !== dom.thWarnIn) dom.thWarnIn.value = String(liveTh.warn);
  if (document.activeElement !== dom.thCritIn) dom.thCritIn.value = String(liveTh.crit);

  // 백업 / 복구
  dom.bkTitle.textContent = L(ctx, 'Backup & restore', '백업 / 복구');
  dom.bkSub.textContent = L(ctx, 'Config JSON · availability CSV', '설정 JSON · 가용성 CSV');
  dom.bkHint.textContent = L(ctx,
    'Export masks credentials (blank = keep current on restore). Device/collection changes apply after restart.',
    '내보내기는 자격증명을 마스킹합니다(빈 값 = 복구 시 기존 유지). 장비·수집 설정 변경은 재시작 후 적용됩니다.');
  dom.bkExp.textContent = L(ctx, 'Download config JSON', '설정 JSON 다운로드');
  dom.bkCsv.textContent = L(ctx, 'Availability CSV', '가용성 CSV');
  dom.bkImp.textContent = S.busy === 'bk' ? '…' : L(ctx, 'Restore from file…', '파일에서 복구…');
  dom.bkImp.disabled = !!S.busy;

  // 제품 정보(개발사 연락처) — 값은 정적, 라벨만 언어 전환을 따라간다.
  dom.aboutTitle.textContent = L(ctx, 'About', '제품 정보');
  dom.aboutSub.textContent = L(ctx, 'Developer & support contact', '개발 · 지원 연락처');
  dom.aboutNames.co.textContent = L(ctx, 'Developer', '개발사');
  dom.aboutNames.web.textContent = L(ctx, 'Website', '웹사이트');
  dom.aboutNames.mail.textContent = L(ctx, 'Email', '이메일');
  dom.aboutNames.tel.textContent = L(ctx, 'Phone', '전화');

  // 관리 API 토큰(환경설정 카드 마지막 행)
  dom.tokName.textContent = L(ctx, 'Manage API token', '관리 API 토큰');
  dom.tokMeta.textContent = L(
    ctx,
    'Sent as X-Serverdesk-Token on device add/edit/delete/test when serve.py runs with --token',
    'serve.py를 --token으로 띄운 경우 장비 추가·수정·삭제·연결 테스트 요청의 X-Serverdesk-Token 헤더로 전송됩니다'
  );
  const tokSet = !!(state.setg && state.setg.token);
  dom.tokInput.placeholder = tokSet
    ? L(ctx, 'Token set — enter a new one to replace', '토큰 설정됨 — 새 토큰 입력 시 교체')
    : L(ctx, 'Enter token (stored in this browser only)', '토큰 입력(이 브라우저에만 저장)');
  dom.tokSave.textContent = L(ctx, 'Save', '저장');
  dom.tokClear.textContent = L(ctx, 'Clear', '해제');
  dom.tokClear.hidden = !tokSet;
  updateTokenButtons();

  // 환경설정 토글
  dom.setgTitle.textContent = L(ctx, 'Preferences', '환경설정');
  SETG_ROWS.forEach((r) => {
    const ref = dom.setgRefs[r.key];
    ref.name.textContent = L(ctx, r.nameEn, r.nameKo);
    // 실 모드에서는 실제 폴링 주기를 보여 준다. 시뮬 모드에서만 '시뮬레이션'이라고 말한다.
    if (r.key === 'refresh' && state.source === 'live') {
      const sec = Number(state.refreshSec) > 0 ? Number(state.refreshSec) : 60;
      const every = L(ctx, sec + 's', sec + '초');
      ref.meta.textContent = L(ctx, r.metaLiveEn, r.metaLiveKo).replace('%s', every);
    } else {
      ref.meta.textContent = L(ctx, r.metaEn, r.metaKo);
    }
    const onv = !!(state.setg && state.setg[r.key]);
    ref.sw.classList.toggle('is-on', onv);
    ref.sw.setAttribute('aria-checked', onv ? 'true' : 'false');
  });
}

function destroy() {
  if (!S) return;
  clearTimeout(S.hookMsgTimer);
  if (S.root) {
    S.root.removeEventListener('click', S.onClick);
    S.root.removeEventListener('input', S.onInput);
    S.root.removeEventListener('keydown', S.onKeydown);
  }
  S = null;
}

export default {
  key: 'settings',
  title: { en: 'Settings', ko: '설정' },
  icon: 'bookmark', // ICONS 레지스트리 실제 키(F1/F4 확정) — 'settings'(기어)는 미등록이라 폴백+경고 발생
  init,
  render,
  destroy,
};
