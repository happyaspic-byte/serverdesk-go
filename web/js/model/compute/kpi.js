// js/model/compute/kpi.js — buildModel (개요 KPI 및 집계 모델)
// ---------------------------------------------------------------------------
// 순수 함수만. DOM 접근 0건.
// ---------------------------------------------------------------------------

import {
  TYPES, FT_TYPES, TYPE_KEYS, STATUS_KEYS, SYNC_KEYS,
  isFT, isNoTel, deriveStatus, deriveSync, availN,
} from '../data.js';
import { COLOR } from '../../util/fmt.js';
import {
  clamp, cmpKo, langOf, makeL, SEV_RANK, STALE_ALERT_DAYS,
  sevInfo, histOf, _meta, _arr, _num, DASH, _strHash, _resolve,
} from './base.js';
import {
  statusTone, statusLabel, statusAnim, pctTone, pctToneAlloc,
  typeIconOf, typeInfo, usageOf, syncInfo, isMaint, fmtAvailN,
  fmtDowntimeYr, fmtUptimeD,
} from './format.js';
import {
  tsNorm, tsKey, agoSec, agoText, shortTime,
  parseLicDate, fmtLicDate, ddayText, licTone, _nowStamp, _todayStr,
} from './time.js';
import { sortRows } from './node.js';
import {
  alertAckKey, autoAckDue, toCsv, activeMaint,
  escalDue, expiredMaint, collectAlerts, collectTraps, alertMsgKo,
} from './alert.js';

/* ===========================================================================
 * 7. buildModel
 * ======================================================================== */


/* 메모이제이션 캐시 — buildModel 반복 호출 시 동일 데이터면 재연산 생략 */
let _memoKey = '';
let _memoResult = null;

/**
 * 전체 화면이 소비하는 파생 모델.
 * @param {Object|Array} a state 또는 fleet
 * @param {Object} [b] a가 fleet일 때의 state
 */
export function buildModel(a, b) {
  const { fleet, state } = _resolve(a, b);
  
  // 메모이제이션 핑거프린트 계산 (매 틱 173KB 전체 재연산 방지)
  const key = [
    (state && state.lastPoll) || 0,
    (state && state.lang) || 'ko',
    (state && state.view) || 'overview',
    (state && state.selected) || '',
    (state && state.ovFilter) || 'all',
    (state && state.alertFilter) || 'all',
    (state && state.logLevel) || 'all',
    (state && state.logQuery) || '',
    // FIX-2: 지문에 빠져 있던 UI 상태 — 이게 없으면 정렬/필터 클릭이 캐시에 먹혀
    // 다음 폴링 틱까지(최악: 자동새로고침 OFF·로그 Pause 시 영구히) 반영되지 않는다.
    (state && state.nodesSort && state.nodesSort.key) || 'host',
    (state && state.nodesSort && state.nodesSort.dir) || 'asc',
    (state && state.nodesFilter) || 'all',
    (state && state.clustersFilter) || 'all',
    // 전역 검색어 — buildSearchList 가 S.search 를 소비하므로 지문에 반드시 든다. 빠져 있던 동안엔
    // 타이핑 직후 이전 질의의 모델(searchResults)이 그대로 재사용돼 다음 폴링 틱까지(~3초, 자동
    // 새로고침 OFF 면 영구히) 허위 '결과 없음'이 보였다 — 위 FIX-2 가 경고한 결함 클래스의 재발.
    (state && state.search) || '',
    // 확인 처리 상태 — 빠지면 '확인' 클릭이 캐시에 먹혀 화면이 안 바뀐다(정렬 지문 누락과 같은 함정).
    // 개수만 세면 '하나 확인 + 하나 해제' 가 같은 길이라 놓친다 → 키 목록을 정렬해 그대로 넣는다.
    Object.keys((state && state.ackedAlerts) || {}).sort().join(','),
    // 점검 창 — 설정/해제가 캐시에 먹히면 '점검 모드' 클릭이 화면에 안 뜬다(같은 함정 클래스).
    // 만료 sweep 은 키 삭제로 잡히고, 기간 연장은 until 값 변경으로 잡힌다.
    Object.keys((state && state.maint) || {}).sort()
      .map((k) => k + ((state.maint[k] || {}).until || '')).join(','),
    // 장비 메모 — 검색 hay 가 note 텍스트를 소비한다(추가/수정 시 결과 갱신). 키+내용 해시로
    // 지문(#279): 길이만 본 데선 'aaaa'→'bbbb' 같은 길이 수정이 캐시를 통과해 검색이 낡았다.
    Object.keys((state && state.notes) || {}).sort()
      .map((k) => k + _strHash(String((((state.notes || {})[k] || {}).text || '')))).join(','),
    // manage 화면 접기/펼치기 — 아래(1329행 부근)에서 S.manageCollapsed 를 소비하는데 지문에
    // 없어 접기 토글이 캐시에 먹혔다. true 인 키만 정렬해 넣는다(길이만 세면 접기+펼치기 상쇄를 놓친다).
    Object.keys((state && state.manageCollapsed) || {})
      .filter((k) => state.manageCollapsed[k]).sort().join(','),
    // 로그 Pause — S.paused 를 개요 로그 도트(1179행 부근)가 소비하므로 토글 즉시 무효화돼야 한다.
    !!(state && (state.logPaused ?? state.paused)),
    // 폴리 수집 상태 — pollStat(1299행 부근)이 source/stale/liveError 를 소비한다. 유예 구간
    // (LIVE_GRACE)의 state 패치는 {liveError}뿐이라 lastPoll·fleet 이 불변이고, stale 도 단독
    // 패치로 바뀐다(app.js::computeStale) — 지문에 없으면 이 패치들이 캐시에 먹혀 개요 수집
    // 카드·incidents 로딩 판정이 백오프(최대 30초+) 동안 "실시간 정상"으로 고정된다(§4.1 함정).
    (state && state.source) || '',
    !!(state && state.stale),
    (state && state.liveError) || '',
    fleet.length,
    // 변경 감지 프록시 — 이전엔 fleet[0].updatedAt 하나만 봤다. 0번 장비가 재정렬되거나
    // 제거되거나 다른 장비보다 느리게 폴링되면 나머지 장비의 갱신을 통째로 놓친다(잠재 staleness).
    // 전 장비의 updatedAt 최댓값 + id 시그니처를 쓴다: 재정렬·추가·삭제까지 잡히고 비용은 O(n).
    fleet.reduce((mx, d) => (Number(d && d.updatedAt) > mx ? Number(d.updatedAt) : mx), 0),
    fleet.map((d) => (d && d.id) || '').join(','),
    (state && state.pollerOverall) || '',
    Math.round(Number((state && state.cacheAgeSec)) || 0),
  ].join('|');

  if (key === _memoKey && _memoResult) {
    return _memoResult;
  }

  const S = state;
  const ko = langOf(S) === 'ko';
  const L = (en, k) => (ko ? k : en);
  const today = _todayStr();
  const SERVERS = fleet;
  const total = SERVERS.length;

  /* ---- KPI ---- */
  const op = SERVERS.filter((s) => s.status === 'op').length;
  const deg = SERVERS.filter((s) => s.status === 'deg').length;
  const down = SERVERS.filter((s) => s.status === 'down').length;
  const ftDevs = SERVERS.filter((s) => isFT(s.type));
  const ftTotal = ftDevs.length;
  const syncedN = ftDevs.filter((s) => s.sync === 'sync').length;
  const simplexN = ftDevs.filter((s) => s.sync === 'simplex').length;
  // #48: sync='offline'(다운) 장비가 나머지 계산으로 '재동기화'에 흡수됐다 — 같은 카드의
  // 클러스터 리스트(syncLabel '오프라인')와 카운트가 모순됐다. 오프라인은 별도 카운트로
  // 분리하고, 재동기화는 전이 상태 장비만 세 정직한 집계로 둔다.
  const offlineN = ftDevs.filter((s) => s.sync === 'offline').length;
  const resyncN = ftTotal - syncedN - simplexN - offlineN;
  const availPct = total ? SERVERS.reduce((acc, s) => acc + (Number(s.availN) || 0), 0) / total : 100;
  const visual = clamp((availPct - 99.9) / 0.1, 0, 1);
  // E4: 폴러가 avail_tracker 로 실측 다운타임을 넣기 전에는 availN 이 상태의 순수 함수
  // (op 99.99/deg 99.9/down 99.0, data.js::availN)일 뿐이라 소수 3자리는 가짜 정밀도다.
  // availDays(관측 일수) > 0 인 장비만 '실측'으로 승격한다 — 0 인 동안은 명목값임을 화면에
  // 숨기지 않는다(명목/실측을 같은 3자리 문자열로 섞어 내면 실측인 척하는 거짓말이 된다).
  const availNominalPct = total ? SERVERS.reduce((acc, s) => acc + availN(s.status), 0) / total : 100;
  const minAvailDays = total ? Math.min.apply(null, SERVERS.map((s) => Number(s.availDays) || 0)) : 0;
  const availIsMeasured = minAvailDays > 0;
  const kpi = {
    total, operational: op, degraded: deg, down, ftTotal,
    availPct,
    // 실측 관측 일수(폴러 avail_tracker) — 0 이면 아직 명목값 단계.
    availDays: SERVERS.reduce((mx, s) => Math.max(mx, Number(s.availDays) || 0), 0),
    // E4: 절대 가용성을 1급 필드로 승격(헤드라인 후보) — 이전엔 목표(99.99) 대비
    // 근소 편차(±0.00x%p)만 화면에 나가 절대 수치가 어디서도 1급으로 안 보였다.
    avail: total ? availPct.toFixed(3) + '%' : DASH,
    availAbs: total ? availPct.toFixed(3) + '%' : DASH,
    // 명목값(상태만의 함수) — 실측이 아직 없을 때 화면이 "정밀해 보이는 가짜 값" 대신
    // 이 필드+availIsMeasured=false 조합으로 정직하게 보여줄 수 있게 분리해 낸다.
    availNominal: total ? availNominalPct.toFixed(3) + '%' : DASH,
    availIsMeasured,
    availMeasuredLabel: availIsMeasured ? L('Measured', '실측') : L('Nominal (status-derived)', '명목값(상태 기반)'),
    availBarW: total ? (visual * 100).toFixed(0) + '%' : '0%',
    synced: syncedN, resync: resyncN, simplex: simplexN,
    // #48: 오프라인(다운) FT 장비 수 — 예전엔 resync 에 흡수됐다. 화면(개요 FT 카드)이
    // 오프라인 행을 추가해 소비할 때까지 카드 표시는 후속 과제.
    offline: offlineN,
    healthyPct: total ? Math.round(op / total * 100) : 0,
  };

  /* ---- 유지보수(점검) 창 — 활성 창 맵. ack 블록보다 앞에 둔다: rowOf 가 클로저로
     참조하므로 SERVERS.map(rowOf) 실행 전에 선언돼 있어야 한다(TDZ). */
  const maintMap = activeMaint(S);
  const inMaint = (id) => Object.prototype.hasOwnProperty.call(maintMap, id);

  /* ---- 공통 행 빌더 ---- */
  const rowOf = (s) => {
    const m = _meta(s);
    const ti = typeInfo(s.type, L);
    const cpu = usageOf(s, 'cpu');
    const mem = usageOf(s, 'mem');
    const sy = syncInfo(s, L);
    const maint = isMaint(s);
    const maintWin = inMaint(s.id);       // 콘솔 점검 창(maint 는 장비측 FT 점검 — 별개)
    const noTel = isNoTel(s.type);
    const cpuHist = histOf(S, s, 'cpu');
    const memHist = histOf(S, s, 'mem');
    return {
      id: s.id, host: s.host, label: m.label || s.host,
      company: m.company || '', factory: m.factory || '', mgmt: m.mgmt || '',
      type: s.type, typeLabel: ti.label, typeShort: ti.short, typeIcon: ti.icon, typeKind: ti.kind,
      site: s.site || DASH,
      status: s.status, statusLabel: statusLabel(s.status, L), statusTone: statusTone(s.status), anim: statusAnim(s.status),
      cpu, mem,
      cpuVal: cpu.val, cpuText: cpu.text, memVal: mem.val, memText: mem.text,
      cpuHist: cpu.na ? [] : _sparkHist(cpuHist, cpu.val),
      memHist: mem.na ? [] : _sparkHist(memHist, mem.val),
      syncKey: sy.key, syncLabel: sy.label, syncTone: sy.tone, syncIcon: sy.icon,
      availN: Number(s.availN) || 0, avail: fmtAvailN(s.availN),
      // E4: 이 장비의 availN 이 실측(폴러 avail_tracker, availDays>0)인지 상태만의 명목값인지
      // 표기에서 구분한다 — 구분 없이 3자리 소수로만 보이면 명목값이 실측인 척하게 된다.
      availIsMeasured: (Number(s.availDays) || 0) > 0,
      availNominal: fmtAvailN(availN(s.status)),
      // E5: 가용성은 상태의 순수 함수(op 99.99/deg 99.9/down 99.0)라 열 전체가 사실상 동일값이었다.
      // SLA 편차(연간 다운타임 예산)와 등급 톤을 함께 내려, 등급 간 실질 차이를 드러낸다.
      availDown: fmtDowntimeYr(s.availN, L),
      availTone: (Number(s.availN) || 0) >= 99.99 ? 'mut' : ((Number(s.availN) || 0) >= 99.9 ? 'warn' : 'neg'),
      uptimeDays: (s.status === 'down' || noTel || s.uptime < 0) ? null : s.uptime,
      uptime: (s.status === 'down' || noTel || s.uptime < 0) ? DASH : fmtUptimeD(s.uptime),
      maint, maintLabel: L('MAINT', '점검'),
      maintWin, maintWinInfo: maintWin ? maintMap[s.id] : null,
      pending: !!m.pending, error: m.error || null,
      isFT: isFT(s.type), noTel,
      vms: m.vms || 0, vmRunning: m.vmRunning || 0,
      version: (m.version || (m.unit && m.unit.version) || '').toString(),
      sel: S.selected === s.id,
    };
  };

  const allRows = SERVERS.map(rowOf);
  const byId = Object.create(null);
  allRows.forEach((r) => { byId[r.id] = r; });
  // 원본 장비(SERVERS) 조회도 맵으로 — 행마다 SERVERS.find 를 돌리면 byId 를 만들고도
  // O(n²) 을 그대로 탄다(102대×매 틱의 servers 매핑·isAttn 경유 critOf 중첩 호출).
  const devById = Object.create(null);
  SERVERS.forEach((s) => { devById[s.id] = s; });

  /* ---- overview 장비 카드 (ovFilter) ---- */
  const ovFilter = S.ovFilter || 'all';
  const servers = (ovFilter === 'all' ? allRows : allRows.filter((r) => r.status === ovFilter)).map((r) => {
    const dev = devById[r.id];
    const m = _meta(dev);
    let lastEvt = null;
    if (m.lastNodeSwitch && m.lastNodeSwitch.ts) {
      lastEvt = { label: L('Node failover', '노드 전환'), when: String(m.lastNodeSwitch.ts).slice(5, 16) };
    } else if (_arr(m.alerts).length) {
      lastEvt = { label: String(m.alerts[0].desc || m.alerts[0].name || '').slice(0, 42), when: shortTime(m.alerts[0].time, today) };
    }
    return Object.assign({}, r, { lastEvt });
  });
  const shownLabel = ko ? (total + '대 중 ' + servers.length + '대') : (servers.length + ' of ' + total);
  const ovFilters = [['all', L('All', '전체')], ['op', L('Healthy', '정상')], ['deg', L('Degraded', '저하')], ['down', L('Offline', '오프라인')]]
    .map((f) => ({
      key: f[0], label: f[1], active: ovFilter === f[0],
      count: f[0] === 'all' ? total : allRows.filter((r) => r.status === f[0]).length,
    }));

  /* ---- 경보 확인(ack) 상태 — nodes '주의 필요' 필터/attention/verdict 보다 먼저 필요하다 ----
     폴러가 경보 해제 API 를 주지 않아(읽기 전용 원천) 클라이언트 '확인' 개념을 둔다.
     원본은 지우지 않고 확인 표시만 붙이며, 확인된 건 활성 카운트에서만 빠진다.
     경보 id 는 장비 id 라 식별 불가 → host+name+desc+time 복합 키를 쓴다.
     원문이 바뀌면 키가 달라져 자동 재활성 = 새 사건으로 다시 뜬다. */
  const ackMap = (S && S.ackedAlerts) || {};
  const ackKeyOf = alertAckKey;   // 키 생성은 모듈 수준 alertAckKey 하나로 — 자동 확인(autoAckDue)과 동일 규약
  // #325: 키의 시각 재료는 collectAlerts 의 time 정규화와 같은 규약을 써야 한다 — 원본
  //   a.time 을 그대로 쓰면 폴리 ISO(T/Z) 형식·time 결측 경보에서 카드 ack 키와 장비 큐
  //   (critOf) 키가 영구 불일치해, 확인한 심각 경보가 '주의 필요'에 남는다.
  // #365: time 결측의 폴핵은 onset 맵이 아니라 고정값(ACK_TIME_MISSING) — collectAlerts 가
  //   납품하는 ackTime 과 같은 재료라 카드 키와 세션 무관하게 일치한다.
  // #398: DEVICE_STATE(합성)도 collectAlerts와 같은 재료(downSince/issueSince 우선,
  //   없으면 고정값)를 써야 카드 키가 세션 시각과 무관하게 유지된다.
  const isAcked = (hostId, a, m) => !!ackMap[ackKeyOf(hostId, a.name, a.desc || a.name,
    (a && a.name === 'DEVICE_STATE')
      ? (tsNorm(m && m.downSince) || tsNorm(m && m.issueSince) || ACK_TIME_MISSING)
      : (tsNorm(a.time) || ACK_TIME_MISSING))];

  // FIX-1: status 만 보면 'op 인데 critical 경보를 든 장비'가 통째로 빠진다.
  // 백엔드 _derive_status 는 오보를 피하려 FT 장비를 의도적으로 op 로 고정하므로,
  // 프런트가 severity 를 보지 않으면 개요가 '모든 장비 정상'이라고 거짓말한다.
  // 확인(ack) 처리된 경보는 '주의 필요'에서도 빠진다 — 활성 심각만 장비를 큐에 올린다.
  const critOf = (id) => {
    if (inMaint(id)) return 0;   // 점검 창 중엔 심각 경보를 주의 필요에 올리지 않는다(묵음 창)
    const dev = devById[id];
    // 심각도는 sev 우선 — collectAlerts(:458)·incStats·buildTopo 요약과 같은 우선순위.
    // 여기만 severity 우선이면 같은 경보가 목록은 '경고', 장비 큐는 '심각'으로 갈린다(#51).
    return _arr(_meta(dev).alerts)
      .filter((a) => (a.sev || a.severity) === 'critical' && !isAcked(id, a, _meta(dev))).length;
  };

  /* ---- nodes 테이블 (필터 + 정렬) ---- */
  const nodesFilter = S.nodesFilter || 'all';
  const nSort = S.nodesSort || { key: 'host', dir: 'asc' };
  // FIX-3: '주의 필요' 필터 = 개요 attention 과 동일 집합 — 아래 attentionAll 이 이 술어(isAttn)를
  // 그대로 쓰도록 정의를 여기 한 곳에만 둔다. 예전엔 이 자리에서 down||deg||maint 만 보고
  // critOf(활성 심각 경보)를 빼먹어, 실플릿에서 개요 "주의 필요 2대" vs 노드 필터 칩 "0"
  // 불일치가 관측됐다(같은 집합을 두 곳에 복제한 대가). 이를 위해 ack/critOf 정의를 위로 올렸다.
  // 점검 창(maintWin) 장비는 주의 필요에서 뺀다 — 만지는 중이라는 게 운영자 합의.
  // 장비측 FT 점검(r.maint)은 계측 신호라 그대로 둔다(둘은 별개).
  const isAttn = (r) => !inMaint(r.id) && (r.status === 'down' || r.status === 'deg' || r.maint || critOf(r.id) > 0);
  const attnCount = allRows.filter(isAttn).length;
  const fleetRows = sortRows(
    nodesFilter === 'all' ? allRows
      : nodesFilter === 'attention' ? allRows.filter(isAttn)
        : allRows.filter((r) => r.status === nodesFilter),
    nSort.key, nSort.dir,
  );
  const nodesFilters = ovFilters.map((f) => ({ key: f.key, label: f.label, count: f.count, active: nodesFilter === f.key }));
  nodesFilters.push({ key: 'attention', label: L('Attention', '주의 필요'), count: attnCount, active: nodesFilter === 'attention' });

  /* ---- 히트맵(잔디) ---- */
  const heat = allRows.map((r) => ({
    id: r.id, host: r.host, label: r.label,
    // 라벨드 타일(소형 플릿 ≤8대)용 파생 — 짧은 코드(회사 프리픽스 제거)와 타입 축약.
    code: deviceCode(r.label || r.host, r.company), typeShort: r.typeShort,
    status: r.status, tone: r.statusTone, anim: r.anim,
    title: r.host + ' · ' + r.statusLabel,
  }));
  const fleetGrid = heat;
  // FT 이중화 카드 하단 클러스터별 동기화 상태 리스트(개요) — 카운트 3행 밑 상시 공백을
  // 실데이터(어느 클러스터가 어떤 sync 인지)로 채운다(심사 반영).
  const ftClusters = allRows.filter((r) => isFT(r.type)).map((r) => ({
    id: r.id, code: deviceCode(r.label || r.host, r.company),
    syncLabel: r.syncLabel, syncTone: r.syncTone,
  }));
  const heatLegend = [
    { key: 'op', label: L('Operational', '가동'), tone: 'pos', count: op },
    { key: 'deg', label: L('Degraded', '저하'), tone: 'warn', count: deg },
    { key: 'down', label: L('Offline', '오프라인'), tone: 'neg', count: down },
  ];

  /* ---- 주의 필요 ---- */
  // 위험도순 통합 뷰(minor#5): 오프라인(down) → 저하(deg) → 점검(maint) 을 한 목록에 위험도 내림차순으로.
  // 예전엔 오프라인을 경보 피드와 겹친다는 이유로 제외했으나, '주의 필요'는 '지금 봐야 할 장비'를 위험도
  // 순으로 모으는 단일 앵커가 더 유용하다 — 가장 위험한 오프라인이 목록 맨 위에 오게 한다.
  // (ack 상태·critOf·isAttn 정의는 nodes 섹션 앞으로 이동 — FIX-1/FIX-3 주석 참조.)
  const attnRank = (r) => (r.status === 'down' ? 0
    : (r.status === 'deg' ? 1 : (critOf(r.id) > 0 ? 2 : 3)));
  const attentionAll = allRows
    // FIX-3: nodes 'attention' 필터와 같은 술어(isAttn)를 그대로 써 두 화면의 모집단 불일치를 원천 차단.
    .filter(isAttn)
    .sort((x, y) => (attnRank(x) - attnRank(y)) || cmpKo(x.host, y.host));
  const attention = attentionAll.slice(0, 4);
  const attentionMore = Math.max(0, attentionAll.length - attention.length);

  /* ---- 경보/트랩/로그 ---- */
  const liveAlerts = collectAlerts(SERVERS, L);
  const liveTraps = collectTraps(SERVERS);
  const liveEvents = liveAlerts.concat(liveTraps)
    .sort((x, y) => String(y.time || '').localeCompare(String(x.time || '')));

  const decorate = (a) => {
    const sv = sevInfo(a.sev, L);
    const sec = agoSec(a.time);
    // 한글 모드에서만 유형 요약으로 치환하고, 원문(msg)은 그대로 둬 로그 tail·카드 툴팁에 보존한다(E3).
    const local = ko ? alertMsgKo(a.name, a.desc) : null;
    // 경보별 안정 키 — id 는 장비 id 라 못 쓴다. 원문이 바뀌면 키도 바뀌어 자동 재활성된다.
    // #365: 키의 시각 재료는 collectAlerts 의 ackTime — time 결측 경보는 세션 무관 고정값이라
    // 리로드·다중 콘솔(/ack 공유)에서도 같은 키다(트랩 등 ackTime 없는 행은 time 그대로).
    const ackKey = ackKeyOf(a.hostId, a.name, a.desc, a.ackTime || a.time);
    const acked = !!ackMap[ackKey];
    return {
      id: a.id, hostId: a.hostId, host: a.host,
      ackKey, acked,
      maintWin: inMaint(a.hostId),
      sev: sv.key, sevLabel: sv.label, sevTone: sv.tone, sevIcon: sv.icon, level: sv.level,
      msg: a.desc, desc: a.desc, msgLocal: local || a.desc,
      time: a.time, timeShort: shortTime(a.time, today),
      ago: agoText(sec, L, ko) || a.time, agoFull: a.time,
      stLabel: acked ? L('Acknowledged', '확인됨') : L('Active', '활성'),
      // 경보 나이 — 실측상 31건 전부 10일 이상이었는데 화면엔 '9일 전' 같은 상대시각만 있고
      // '이건 오래 방치된 것'이라는 신호가 없었다. 임계(STALE_ALERT_DAYS)를 넘으면 표시한다.
      ageDays: sec == null ? null : Math.floor(sec / 86400),
      stale: sec != null && sec >= STALE_ALERT_DAYS * 86400,
      trap: !!a.trap,
      // E2: collapseFlaps(아래)가 접은 행 — 펼치면 원본 전이 이력(members)을 그대로 볼 수 있다.
      // 접지 않은 일반 행은 flap=false/members=null 로 기존 계약과 동일하다(호환).
      flap: !!a.flap, flapCount: a.flapCount || 0,
      members: a.members ? a.members.map(decorate) : null,
      // 검증용 임시 픽스처 호스트(예: srv-evt-test) — 하드 삭제 대신 플래그만 세워
      // 화면이 숨길지 고르게 한다(데이터 손실 금지 원칙).
      testFixture: !!a.testFixture,
    };
  };

  const alertsAll = liveAlerts.map(decorate);
  // 표면이 보는 모집단 — 검증용 임시 픽스처(srv-evt-test 등)는 고객 표면 전체에서 뺀다(#265).
  // #31 로 목록·CSV·로그만 숨겨져 칩·레일 배지·개요 미리보기·알림 사운드(activeAlerts 소비)는
  // 픽스처를 계속 세는 3갈래가 됐다. logRows 처럼 모델이 한 곳에서 걸러 낸 파생을 쓰면
  // 모든 표면이 같은 모집단을 본다. alertsAll/incList 는 플래그를 단 원본을 유지한다
  // (화면 필터·검증 계약 — 데이터 손실 금지).
  const realAlerts = alertsAll.filter((x) => !x.testFixture);
  // '활성' = 확인되지 않았고 점검 창으로 묵음 처리되지도 않은 경보. 확인분·점검분은 목록엔 남지만 카운트·판정에서 빠진다.
  const activeAlerts = realAlerts.filter((x) => !x.acked && !x.maintWin);
  const ackedN = realAlerts.length - activeAlerts.length;
  // 개요 경보 미리보기 — 행이 1줄로 펴지며 높이가 줄어 같은 카드에 더 담을 수 있다.
  // 미확인을 앞에, 확인분을 뒤에 둔다(확인한 건 이미 손을 댄 것).
  const alerts = activeAlerts.concat(realAlerts.filter((x) => x.acked)).slice(0, 7);
  const critN = activeAlerts.filter((x) => x.sev === 'critical').length;
  const warnN = activeAlerts.filter((x) => x.sev === 'warning').length;
  const infoN = activeAlerts.filter((x) => x.sev === 'info').length;
  const alertTopSev = critN ? 'critical' : (warnN ? 'warning' : (activeAlerts.length ? 'info' : ''));

  const incStats = {
    critical: critN, warning: warnN, info: infoN, total: activeAlerts.length,
    acked: ackedN, totalAll: realAlerts.length,
    // '전체'(all 필터)를 맨 앞에 둔다 — 심각/경고/정보 3분할의 합계가 뒤에 파생표처럼 붙어 보이던
    //  중복표현을 해소하고(minor#4), 표준 필터 순서(전체→심각→경고→정보)로 현재 뷰 앵커로 읽히게 한다.
    cards: [
      { key: 'total', label: L('All alerts', '전체 알림'), value: activeAlerts.length, tone: 'mut', icon: 'bell', note: L('Live device alerts', '실시간 장비 알림') },
      { key: 'critical', label: L('Critical', '심각'), value: critN, tone: 'neg', icon: 'warningCircle', note: L('Across the fleet', '플릿 전체') },
      { key: 'warning', label: L('Warnings', '경고'), value: warnN, tone: 'warn', icon: 'warningCircle', note: L('Active', '활성') },
      { key: 'info', label: L('Info', '정보'), value: infoN, tone: 'info', icon: 'infoCircle', note: L('Notices', '공지') },
    ],
  };

  // 칩 카운트는 목록(incList)과 같은 모집단이어야 한다 — 이전엔 all 은 liveAlerts(확인분 포함),
  // 심각/경고/정보는 activeAlerts(확인분 제외)에서 세어 칩 숫자가 실제 렌더 행 수와 어긋났다.
  // 목록(아래 incList)은 화면(incidents.js)이 testFixture 를 거르고 렌더하므로 카운트도 같은
  // 픽스처 제외 모집단(realAlerts)에서 센다(#265 — 불일치 재발 금지).
  const incCounts = {
    all: realAlerts.length,
    critical: realAlerts.filter((x) => x.sev === 'critical').length,
    warning: realAlerts.filter((x) => x.sev === 'warning').length,
    info: realAlerts.filter((x) => x.sev === 'info').length,
  };
  const alertFilter = S.alertFilter || 'all';
  const incFilters = [['all', L('All', '전체')], ['critical', L('Critical', '심각')], ['warning', L('Warning', '경고')], ['info', L('Info', '정보')]]
    .map((f) => ({ key: f[0], label: f[1], count: incCounts[f[0]], active: alertFilter === f[0] }));
  const incList = (alertFilter === 'all' ? alertsAll : alertsAll.filter((x) => x.sev === alertFilter));

  /* ---- 플릿 총평(fleetVerdict) ----
     실 폴러 응답 최상위 overall 필드가 data.js::pull() 정규화에서 devices/events/refreshSec/stale
     만 통과시키느라 통째로 버려진다(§Handoff) — UI 전체가 이 값을 몰랐다. data.js 는 이 파일
     소유가 아니므로 여기서 같은 신호를 장비 상태 + 활성 심각 알림에서 재도출한다. */
  const critHostIds = Array.from(new Set(activeAlerts.filter((a) => a.sev === 'critical').map((a) => a.hostId)));
  const fvReasons = [];
  if (down > 0) fvReasons.push(L(down + ' device(s) offline', down + '대 오프라인'));
  if (critN > 0) {
    fvReasons.push(L(
      critN + ' active critical alert(s) on ' + critHostIds.length + ' device(s)',
      '활성 심각 알림 ' + critN + '건(장비 ' + critHostIds.length + '대)',
    ));
  }
  if (deg > 0) fvReasons.push(L(deg + ' device(s) degraded', deg + '대 성능 저하'));
  if (warnN > 0 && critN === 0 && down === 0) fvReasons.push(L(warnN + ' active warning(s)', '활성 경고 ' + warnN + '건'));
  // 폴러 자신의 판정(overall)이 이제 state 로 올라온다(data.js pullPatch → pollerOverall).
  // 프런트 재도출값과 백엔드 판정이 다르면 **더 나쁜 쪽**을 택한다 — 둘 중 하나만 위험을 봤다면
  // 그건 못 본 쪽의 누락이지 안전 신호가 아니다. 어느 쪽이 근거인지도 사유에 남긴다.
  const RANK = { ok: 0, warning: 1, critical: 2 };
  const derived = (down > 0 || critN > 0) ? 'critical' : ((deg > 0 || warnN > 0) ? 'warning' : 'ok');
  const backend = (S && S.pollerOverall) || null;
  const fvKey = (backend && RANK[backend] > RANK[derived]) ? backend : derived;
  if (backend && RANK[backend] > RANK[derived]) {
    fvReasons.push(L('Poller reports ' + backend, '폴러 판정: ' + backend));
  }
  const fleetVerdict = {
    key: fvKey,
    label: fvKey === 'critical' ? L('Critical', '심각') : (fvKey === 'warning' ? L('Warning', '경고') : L('Healthy', '정상')),
    tone: fvKey === 'critical' ? 'neg' : (fvKey === 'warning' ? 'warn' : 'pos'),
    reasons: fvReasons.length ? fvReasons : [L(total + ' device(s) operational', '전체 ' + total + '대 정상')],
  };

  // ---- 플래핑 억제 ----
  // 실측: 접촉불량 프린터(printer-c56x) 1대가 며칠간 분 단위로 가동↔오프라인을 오가며
  // events[] 42건 중 40건(95%)을 차지해 실제 사건 2건을 파묻었다. 같은 host 안에서
  // FLAP_WINDOW_MS 이내 간격으로 FLAP_MIN_COUNT 건 이상 연속 STATE_CHANGE 가 이어지면 1행으로
  // 접는다. 원본 이력은 버리지 않고 members[] 에 그대로 보존(펼치기용) — 하드 삭제 금지 원칙.
  // 실측(printer-c56x)의 최대 전이 간격은 ~8.1시간(장비가 몇 시간마다 발작하듯 오프라인→가동을
  // 반복) — 30분처럼 짧은 창은 발작 사이 '조용한 시간'에 끊겨 그룹이 20개로 쪼개진다. 하루 이내
  // 재발은 같은 사건으로 본다.
  const FLAP_WINDOW_MS = 12 * 60 * 60 * 1000; // 동일 host 연속 전이 간 최대 간격(12시간) — 넘으면 별개 사건.
  const FLAP_MIN_COUNT = 4;                // 이 이상 연속돼야 '플래핑'으로 접는다(가끔의 2~3회는 그대로 유지).
  // TEST_FIXTURE_RE 는 모듈 수준 정의를 쓴다(autoAckDue 와 공용 — 숨김 판정 일치).

  const collapseFlaps = (rows) => {
    const out = [];
    let i = 0;
    while (i < rows.length) {
      const r = rows[i];
      if (r.name !== 'STATE_CHANGE') { out.push(r); i += 1; continue; }
      const members = [r];
      let j = i + 1;
      while (j < rows.length && rows[j].name === 'STATE_CHANGE' && rows[j].hostId === r.hostId) {
        const gap = tsKey(members[members.length - 1].time) - tsKey(rows[j].time);
        if (gap > FLAP_WINDOW_MS || gap < 0) break;
        members.push(rows[j]);
        j += 1;
      }
      if (members.length >= FLAP_MIN_COUNT) {
        const worstSev = members.reduce((w, mm) => (SEV_RANK[mm.sev] < SEV_RANK[w] ? mm.sev : w), members[0].sev);
        const fromT = tsNorm(members[members.length - 1].time).slice(5, 10);
        const toT = tsNorm(members[0].time).slice(5, 10);
        out.push({
          id: 'flap|' + r.hostId + '|' + members[0].time + '|' + members.length,
          hostId: r.hostId, host: r.host, name: 'STATE_FLAP', trap: false,
          sev: worstSev, time: members[0].time,
          desc: L(
            r.host + ' state flapping ×' + members.length + ' (' + fromT + ' ~ ' + toT + ')',
            r.host + ' · 상태 플래핑 ×' + members.length + ' (' + fromT + ' ~ ' + toT + ')',
          ),
          flap: true, flapCount: members.length, members,
          testFixture: TEST_FIXTURE_RE.test(r.hostId || ''),
        });
        i = j;
      } else {
        out.push(r);
        i += 1;
      }
    }
    return out;
  };

  // 로그: 이벤트 이력(폴러 events[] — 전이·발생·해제)이 정본이며 trap을 최신순으로 병합한다.
  // 이벤트 이력이 없는 이전 폴러에서는 활성 경보 스냅샷을 보조 경로로 사용한다.
  const evLog = _arr(S.liveEventLog).map((e) => ({
    id: 'ev|' + e.host + '|' + e.time + '|' + e.desc,
    hostId: e.host, host: e.label || e.host,
    sev: e.sev, desc: e.desc, time: e.time,
    name: e.kind === 'state' ? 'STATE_CHANGE' : (e.kind || 'EVENT').toUpperCase(),
    trap: false,
    testFixture: TEST_FIXTURE_RE.test(e.host || ''),
  }));
  const logMerged = evLog.length
    ? evLog.concat(liveTraps).sort((x, y) => String(y.time || '').localeCompare(String(x.time || '')))
    : liveEvents;
  // §E2 플래핑 억제 — collapseFlaps 는 이 함수 앞부분에서 정의.
  const logSource = collapseFlaps(logMerged);
  const logLevel = S.logLevel || 'all';
  const logQuery = String(S.logQuery || '').trim().toLowerCase();
  // 검증용 임시 픽스처(srv-evt-test 등)는 고객이 보는 목록에서 뺀다.
  // **여기 한 곳에서만** 거른다 — 화면에서 따로 거르면 칩 카운트(같은 모집단에서 세는)와
  // 어긋난다(실제로 그렇게 만들었다가 '전체 3 / 렌더 1' 불일치가 났다).
  // 원본 events[] 는 state 에 그대로 남으므로 데이터 손실은 없다.
  const logRows = logSource.map(decorate).filter((r) => !r.testFixture);
  const logQueryMatch = (r) => {
    if (!logQuery) return true;
    // 결측 필드가 'undefined' 문자열로 haystack 에 유입되면 'undefined' 검색어에 오매칭된다.
    const hay = ((r.msg || '') + ' ' + (r.host || '') + ' ' + (r.time || '')).toLowerCase();
    return hay.indexOf(logQuery) >= 0;
  };
  const logFiltered = logRows.filter((r) => {
    if (logLevel !== 'all' && r.level !== String(logLevel).toUpperCase()) return false;
    return logQueryMatch(r);
  });
  const logsFull = logFiltered;
  const logs = logRows.slice(0, 7);
  // E1(칩/목록 모집단 불일치): 카운트는 반드시 logsFull 을 만드는 것과 같은 필터(레벨+쿼리)를
  // 통과해야 한다 — 레벨만 세면 검색 중엔 칩 숫자가 실제 렌더 행 수보다 부풀어 보였다(심사 지적).
  // incidents.js 로그 뷰는 심각도 칩(전체/심각/경고/정보)을 alertFilter 로 카드 뷰와 공유하는데,
  // 그 칩이 incStats(경보 모집단 meta.alerts, 실측 31건)에서 세어져 실제 렌더 대상인 이벤트
  // 로그(logsFull, events[] 기반)와 모집단이 달랐다 — 심사에서 지적된 4개 칩 전부 불일치의 근원.
  // incStats/incFilters(카드 뷰용)는 그대로 두고, 로그 뷰 전용으로 같은 모집단(logRows, 쿼리
  // 반영)에서 세는 칩을 별도로 낸다(§Handoff: incidents.js 로그 뷰는 이 필드로 바꿔 달아야 한다).
  const incLogFilters = [
    ['all', L('All', '전체'), 'mut'],
    ['critical', L('Critical', '심각'), 'neg'],
    ['warning', L('Warning', '경고'), 'warn'],
    ['info', L('Info', '정보'), 'info'],
  ].map(([key, label, tone]) => ({
    key, label, tone,
    count: logRows.filter((r) => (key === 'all' || r.sev === key) && logQueryMatch(r)).length,
    active: alertFilter === key,
  }));
  const logCountLabel = ko ? (logsFull.length + '행') : (logsFull.length + ' lines');
  const logStatusLabel = L('Events · alerts + SNMP traps', '이벤트 · 알림 + SNMP 트랩');

  /* ---- 자원 집계 / VM ---- */
  // 메트릭(vCPU/MEM)별 집계 — 한쪽만 결측(totVcpu=0 등)인 장비가 있어도 그 메트릭만 NA 여야
  // 한다(#262). totVcpu 가 0인 장비까지 vCPU 합산에 넣으면 집단 vcpuPct 가 0% 로 오표기된다.
  const acc = SERVERS.reduce((a2, s) => {
    const u = _meta(s).unit;
    if (u && (u.totVcpu || u.totMem)) {
      a2.counted++;
      if (u.totVcpu) { a2.vn++; a2.vu += u.usedVcpu || 0; a2.vt += u.totVcpu || 0; }
      if (u.totMem) { a2.mn++; a2.mu += u.usedMem || 0; a2.mt += u.totMem || 0; }
    }
    return a2;
  }, { counted: 0, vn: 0, vu: 0, vt: 0, mn: 0, mu: 0, mt: 0 });
  const resAgg = acc.counted ? {
    has: true, counted: acc.counted, total,
    // 메트릭별 has — 화면은 이 플래그로 메트릭별 NA('—')와 실측 0% 를 구분한다(#262).
    vcpuHas: acc.vn > 0, memHas: acc.mn > 0,
    vcpuUsed: Math.round(acc.vu), vcpuTot: Math.round(acc.vt),
    vcpuPct: acc.vt ? Math.round(acc.vu / acc.vt * 100) : 0,
    memUsed: Number(acc.mu.toFixed(1)), memTot: Number(acc.mt.toFixed(1)),
    memPct: acc.mt ? Math.round(acc.mu / acc.mt * 100) : 0,
  } : {
    has: false, counted: 0, total, vcpuHas: false, memHas: false,
    vcpuUsed: 0, vcpuTot: 0, vcpuPct: 0, memUsed: 0, memTot: 0, memPct: 0,
  };
  // 자원 '할당'(커밋) 도넛 — 할당은 결함이 아니라 헤드룸 정보. neg-red 대신 앰버로 캡(색-무게 일관).
  resAgg.vcpuTone = pctToneAlloc(resAgg.vcpuPct);
  resAgg.memTone = pctToneAlloc(resAgg.memPct);

  const vmAgg = SERVERS.reduce((a2, s) => {
    const m = _meta(s);
    a2.running += (m.vmRunning || 0); a2.total += (m.vms || 0);
    return a2;
  }, { running: 0, total: 0 });

  /* ---- 라이선스 ---- */
  const licExp = [];
  const licPerp = [];
  // #593: 만료형(expires)인데 만료일이 결측이거나 파싱 불가면 '영구'가 아니라 미상(NA) — 갱신 필요
  //   라이선스가 영구로 둔갑해 숨는 것을 막는다. 미상분은 별도 버킷(na/naAll)으로 납품한다.
  const licNa = [];
  SERVERS.forEach((s) => {
    const m = _meta(s);
    const lic = m.license;
    if (!lic) return;
    const host = m.label || s.host;
    if (lic.expires) {
      const d = lic.expire ? parseLicDate(lic.expire) : null;
      if (d) {
        const days = Math.round((d.getTime() - Date.now()) / 86400000);
        licExp.push({
          id: s.id, host, lic: String(lic.name || ''), days,
          expDate: d.toISOString().slice(0, 10),
          tone: licTone(days), txt: ddayText(days, L, ko),
        });
        return;
      }
      licNa.push({ id: s.id, host, lic: String(lic.name || ''), txt: L('Unknown', '미상'), tone: 'mut' });
      return;
    }
    licPerp.push({ id: s.id, host, lic: String(lic.name || ''), txt: L('Perpetual', '영구'), tone: 'mut' });
  });
  licExp.sort((x, y) => x.days - y.days);
  const licenses = {
    has: licExp.length > 0, empty: total === 0,
    minDays: licExp.length ? licExp[0].days : null,
    minTxt: licExp.length ? licExp[0].txt : '',
    minTone: licExp.length ? licExp[0].tone : 'pos',
    minDate: licExp.length ? licExp[0].expDate : '',
    minHost: licExp.length ? licExp[0].host : '',
    list: licExp.slice(0, 3).map((l) => Object.assign({}, l, { tip: (l.expDate || '') + L(' expiry', ' 만료') })),
    all: licExp,
    perp: licPerp.slice(0, 2),
    perpAll: licPerp,
    na: licNa.slice(0, 2),
    naAll: licNa,
  };

  /* ---- 스토리지그룹 사용률(FT 클러스터) ----
     FT 장비(EV/EDGE/END/FTS)의 meta.topo.storage[] 그룹별 사용률(%)을 모은다.
     topo.storage 또는 사용률이 없는 장비·그룹은 건너뛴다. 임계 톤은 barColor 규칙을
     재사용하되, 정상 중립값은 잉크 톤으로 표시한다. */
  const storageGroups = [];
  SERVERS.forEach((s) => {
    if (!isFT(s.type)) return;
    const topo = _meta(s).topo;
    if (!topo || !Array.isArray(topo.storage) || !topo.storage.length) return;
    const r = byId[s.id];
    const devShort = r ? deviceCode(r.label, r.company) : (s.host || DASH);
    topo.storage.forEach((g, i) => {
      const raw = _num(g.pct);
      if (raw == null) return;                       // 사용률 없는 그룹은 카드에서 생략(방어)
      const pct = Math.round(clamp(raw, 0, 100));
      // barColor(pct) 규칙 재사용: crit↑ neg · warn↑ warn · 그 외 중립(잉크). 임계 초과만 상태 톤.
      const _pt = pctTone(pct);
      const tone = (_pt === 'neg' || _pt === 'warn') ? _pt : '';
      const usage = (g.usedRaw && g.sizeRaw) ? (g.usedRaw + ' / ' + g.sizeRaw) : '';
      storageGroups.push({
        id: s.id, key: s.id + ':' + (g.name || i),
        dev: devShort, host: r ? r.label : (s.host || ''),
        group: String(g.name || DASH),
        pct, pctText: pct + '%', width: pct + '%', tone,
        usage, mirrored: !!g.mirrored,
        tip: [String(g.name || ''), usage, pct + '%'].filter(Boolean).join(' · '),
      });
    });
  });
  // 사용률 높은 순(가장 임박한 포화가 위로) — 개요의 '가장 급한 것 먼저' 관례와 일치.
  storageGroups.sort((x, y) => y.pct - x.pct);
  const storage = { has: storageGroups.length > 0, rows: storageGroups };

  /* ---- 최근 이벤트 ---- */
  const evts = [];
  SERVERS.forEach((s) => {
    const m = _meta(s);
    const host = m.label || s.host;
    if (m.lastNodeSwitch && m.lastNodeSwitch.ts) {
      evts.push({
        id: s.id, k: tsKey(m.lastNodeSwitch.ts), icon: 'cycle', tone: 'warn',
        label: L('Node failover', '노드 전환'), host,
        when: String(m.lastNodeSwitch.ts).slice(5, 16),
        flow: (m.lastNodeSwitch.from && m.lastNodeSwitch.to) ? (m.lastNodeSwitch.from + ' → ' + m.lastNodeSwitch.to) : '',
      });
    }
    if (m.lastVmSwitch && m.lastVmSwitch.ts) {
      evts.push({
        id: s.id, k: tsKey(m.lastVmSwitch.ts), icon: 'cycle', tone: 'info',
        label: L('VM failover', 'VM 전환'), host,
        when: String(m.lastVmSwitch.ts).slice(5, 16),
        flow: (m.lastVmSwitch.from && m.lastVmSwitch.to) ? (m.lastVmSwitch.from + ' → ' + m.lastVmSwitch.to) : (m.lastVmSwitch.vm || ''),
      });
    }
    if (m.lastReboot && m.lastReboot.agoSecs != null) {
      evts.push({
        id: s.id,
        k: (m.lastReboot.at ? m.lastReboot.at * 1000 : Date.now() - m.lastReboot.agoSecs * 1000),
        icon: 'cycle', tone: 'mut', label: L('Reboot', '재부팅'), host,
        when: agoText(m.lastReboot.agoSecs, L, ko), flow: m.lastReboot.node || '',
      });
    }
    // data.js 추가 계약: meta.events[] (상태 전이/재부팅 기록)
    // 실 폴러는 상태 전이 이력을 못 만든다 -> kind:'alert' 로 실제 장비 알림을 넘긴다.
    _arr(m.events).forEach((e) => {
      const kind = e.kind === 'reboot' ? 'reboot' : (e.kind === 'alert' ? 'alert' : 'status');
      evts.push({
        id: s.id, k: e.at || tsKey(e.ts),
        icon: kind === 'reboot' ? 'cycle' : (kind === 'alert' ? 'bell' : 'bolt'),
        tone: sevInfo(e.sev, L).tone,
        label: kind === 'reboot' ? L('Reboot', '재부팅')
          : (kind === 'alert' ? L('Alert', '장비 알림') : L('Status change', '상태 변경')),
        host, when: shortTime(e.ts, today), flow: e.text || '',
      });
    });
  });
  evts.sort((x, y) => y.k - x.k);
  const eventsTop = evts.slice(0, 6);

  /* ---- 최근 변동(recent changes) — 개요 하단 활동 타임라인(A1) ----
     히트맵(52 게슈탈트)·주의 필요(위험도 트리아지)와 역할을 분리한다: 여기서는 '무엇이 언제 바뀌었나'를
     장비별 1건(최신)씩 · 최신순으로 소수만 보여준다. evts 는 이미 최신순 정렬 → 장비별 첫 등장만 취해
     중복을 접는다. 상세 인벤토리·지표(CPU/MEM)는 #/nodes 소유(장비 브라우즈 삼중 노출/12행 캡 제거).
     이벤트가 없는 현재 이상(down/deg) 장비는 상태 자체를 변동으로 보아 뒤에 덧붙여 항상 노출한다. */
  const rcSeen = Object.create(null);
  const recentChangesAll = [];
  evts.forEach((e) => {
    if (rcSeen[e.id]) return;
    rcSeen[e.id] = 1;
    const r = byId[e.id];
    // detail 은 전환 흐름(node1 → node0)만 남긴다 — 원시 알림 메시지(flow=e.text)는 '실시간 경보' 카드가
    // 소유하므로 여기 붙이면 중복이다. 변동 타입(라벨)+시각만으로 활동을 요약한다.
    const flow = String(e.flow || '');
    recentChangesAll.push({
      id: e.id, host: e.host, icon: e.icon, changeLabel: e.label, changeTone: e.tone,
      when: e.when || '', detail: flow.indexOf('→') >= 0 ? flow : '',
      statusTone: r ? r.statusTone : 'mut', statusLabel: r ? r.statusLabel : '',
    });
  });
  allRows.forEach((r) => {
    if (rcSeen[r.id]) return;
    if (r.status !== 'down' && r.status !== 'deg') return;
    rcSeen[r.id] = 1;
    recentChangesAll.push({
      id: r.id, host: r.label, icon: r.status === 'down' ? 'link' : 'warningCircle',
      changeLabel: r.status === 'down' ? L('Offline', '오프라인') : L('Degraded', '상태 저하'),
      changeTone: r.statusTone, when: '', detail: '',
      statusTone: r.statusTone, statusLabel: r.statusLabel,
    });
  });
  const recentChanges = recentChangesAll.slice(0, 6);

  /* ---- 수집 상태 ---- */
  // 계층별 수집 실패도 '수집 상태'에 반영한다.
  // 폴러는 meta.collection.errors 에 fast/slow/static 티어별 실패를 담아 보내는데(data.js 가
  // state 까지 통과시킨다) 판정이 meta.error 만 보고 있었다 — 특정 티어가 죽어도 '정상'으로 뜨고
  // 그 티어가 채우던 값이 낡은 채 최신인 척 렌더됐다. VM 보호 상실을 숨기던 것과 같은 부류.
  const tierErrOf = (s) => {
    const c = _meta(s).collection;
    const e = (c && typeof c.errors === 'object' && c.errors) ? c.errors : null;
    if (!e) return [];
    return Object.keys(e).filter((k) => e[k]);
  };
  const tierErrHosts = SERVERS
    .filter((s) => tierErrOf(s).length > 0)
    .map((s) => ({ host: _meta(s).label || s.host, tiers: tierErrOf(s) }));
  const errHostList = SERVERS
    .filter((s) => _meta(s).error || tierErrOf(s).length > 0)
    .map((s) => _meta(s).label || s.host);
  const maintCnt = SERVERS.reduce((n, s) => n + _arr(_meta(s).nodes)
    .filter((x) => _nodeMaint(x)).length, 0);
  const lastPoll = Number(S.lastPoll) || 0;
  const pollAgoSec = lastPoll ? Math.max(0, Math.floor((Date.now() - lastPoll) / 1000)) : null;
  // 데이터의 '진짜' 나이 = 클라이언트가 받은 지 경과 + 폴러가 실장비에서 읽은 뒤 경과.
  // lastPoll 만 보면 수집이 밀렸을 때 '방금'이라고 말하면서 몇 분 낡은 값을 보여준다.
  const cacheAgeSec = Number(S.cacheAgeSec);
  const dataAgeSec = (pollAgoSec != null && Number.isFinite(cacheAgeSec))
    ? pollAgoSec + Math.round(cacheAgeSec)
    : pollAgoSec;
  const collect = {
    empty: total === 0,
    ok: errHostList.length === 0 && maintCnt === 0,
    errCnt: errHostList.length, errHosts: errHostList.slice(0, 2),
    errHostMore: Math.max(0, errHostList.length - 2), maintCnt,
    // 어느 장비의 어느 수집 티어가 죽었는지 — 화면이 '수집 실패'만 말하고 끝내지 않게 한다.
    tierErrs: tierErrHosts.slice(0, 3),
  };
  const pollStat = Object.assign({}, collect, {
    source: 'live',
    live: true,
    sourceLabel: L('Live', '실시간'),
    lastPoll,
    ago: agoText(pollAgoSec, L, ko),
    agoSec: pollAgoSec,
    // 폴러 캐시까지 더한 실제 데이터 나이. 화면은 둘 차이가 유의미할 때만 병기하면 된다.
    dataAgeSec,
    dataAge: agoText(dataAgeSec, L, ko),
    cacheAgeSec: Number.isFinite(cacheAgeSec) ? Math.round(cacheAgeSec) : null,
    refreshSec: Number(S.refreshSec) || 30,
    stale: !!S.stale, liveError: S.liveError || null,
    paused: !!(S.logPaused ?? S.paused),
  });

  /* ---- 상위 부하 / 플랫폼별 ---- */
  const topConsumers = allRows
    .filter((r) => !r.cpu.na && !r.noTel)
    .sort((x, y) => y.cpuVal - x.cpuVal)
    .slice(0, 5)
    .map((r) => ({
      id: r.id, host: r.label, typeLabel: r.typeLabel, typeIcon: r.typeIcon,
      cpu: r.cpuText, cpuVal: r.cpuVal, mem: r.memText, memVal: r.memVal,
      tone: r.cpu.tone, width: r.cpu.width,
    }));

  const platMap = Object.create(null);
  SERVERS.forEach((s) => {
    const ti = typeInfo(s.type, L);
    const cur = platMap[ti.label] || (platMap[ti.label] = { key: s.type, label: ti.label, icon: ti.icon, total: 0, up: 0 });
    cur.total++;
    if (s.status !== 'down') cur.up++;
  });
  const platforms = Object.keys(platMap).sort(cmpKo).map((k) => {
    const p = platMap[k];
    const pct = Math.round(p.up / Math.max(1, p.total) * 100);
    return Object.assign({}, p, { pct, width: pct + '%', tone: pct === 100 ? 'pos' : (pct >= 50 ? 'warn' : 'neg') });
  });

  /* ---- 클러스터 / 용량 / 관리 트리 / 검색 ---- */
  const clusters = buildClusterRows(allRows, SERVERS, S, L, ko);
  const capacity = buildCapacityModel(allRows, SERVERS, resAgg, L, ko);
  const tree = buildTree(allRows, S, L);
  const searchResults = buildSearchList(SERVERS, realAlerts, S, L, ko, today);

  /* ---- 경보 인사이트(통계 탭) — 새 수집 없이 기존 모집단에서 파생한다 ---- */
  // 반복 경보 Top-N: 카드(중복 병합 가중) + 로그 tail 을 같은 키로 센다.
  const _cnt = {};
  const _bump = (k, w) => { if (k) _cnt[k] = (_cnt[k] || 0) + (w || 1); };
  realAlerts.forEach((r) => _bump(r.msgLocal || r.msg, r.dupCount || 1));
  logRows.forEach((r) => _bump(r.msgLocal || r.msg, 1));
  const topAlerts = Object.keys(_cnt).map((k) => ({ label: k, count: _cnt[k] }))
    .sort((a, b) => b.count - a.count).slice(0, 5);
  // 장비별 활성 경보 Top-N — 심각 많은 순, 같으면 총 건수 순.
  const _devCnt = {};
  activeAlerts.forEach((r) => {
    const d = _devCnt[r.hostId] || (_devCnt[r.hostId] = { host: r.host, hostId: r.hostId, count: 0, critN: 0 });
    d.count += 1;
    if (r.sev === 'critical') d.critN += 1;
  });
  const topDevices = Object.keys(_devCnt).map((k) => _devCnt[k])
    .sort((a, b) => (b.critN - a.critN) || (b.count - a.count)).slice(0, 5);
  // 확인 시간은 경보의 실제 onset(a.time)과 확인 시각의 차다. 키 안정화용 ackTime은
  // 실제 발생 시각이 아닐 수 있으므로 계산에 쓰지 않는다. 이미 해소돼 목록에 없는 경보의
  // 고아 키는 onset 조회 실패로 자연 배제된다.
  const _onsetByAckKey = Object.create(null);
  realAlerts.forEach((r) => {
    if (r.ackKey && _onsetByAckKey[r.ackKey] == null) _onsetByAckKey[r.ackKey] = tsKey(r.time);
  });
  const _lat = [];
  Object.keys(ackMap).forEach((k) => {
    const t0 = _onsetByAckKey[k] || 0;
    const t1 = Date.parse(ackMap[k] || '');
    if (t0 && !isNaN(t1) && t1 >= t0) _lat.push((t1 - t0) / 3600000);
  });
  _lat.sort((a, b) => a - b);
  const _med = _lat.length % 2
    ? _lat[Math.floor(_lat.length / 2)]
    : (_lat[_lat.length / 2 - 1] + _lat[_lat.length / 2]) / 2;
  const ackStats = _lat.length ? {
    n: _lat.length,
    avgH: Math.round(_lat.reduce((x, y) => x + y, 0) / _lat.length * 10) / 10,
    medH: Math.round(_med * 10) / 10,
  } : null;
  // 미확인 최장기 — 가장 오래 묵은 활성 경보 1건.
  let oldestUnacked = null;
  activeAlerts.forEach((r) => {
    const sec = agoSec(r.time);
    if (sec != null && (!oldestUnacked || sec > oldestUnacked.ageSec)) {
      oldestUnacked = { host: r.host, msg: r.msgLocal || r.msg, ageSec: sec, ageDays: r.ageDays, time: r.time, sev: r.sev };
    }
  });
  const incInsights = { topAlerts, topDevices, ackStats, oldestUnacked };

  const out = {
    lang: ko ? 'ko' : 'en', ko, total,
    kpi, fleetVerdict,
    rows: allRows, byId,
    servers, shownLabel, ovFilters,
    fleetRows, nodesFilters, nodesSort: nSort,
    heat, fleetGrid, heatLegend, ftClusters,
    attention, attentionAll, attentionMore,
    alerts, alertsAll, activeAlerts, alertCount: activeAlerts.length, ackedN, alertTopSev,

    incStats, incFilters, incList, incInsights,
    logs, logsFull, logRows, incLogFilters, logCountLabel, logStatusLabel,
    resAgg, vmAgg, licenses, storage, eventsTop, recentChanges, collect, pollStat,
    topConsumers, platforms,
    clusters, capacity, tree,
    searchResults,
  };
  
  _memoKey = key;
  _memoResult = out;
  return out;
}

