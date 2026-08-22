// js/model/compute/detail.js — buildDetail (장비 상세 8종 뷰 모델)
// ---------------------------------------------------------------------------
// 순수 함수만. DOM 접근 0건.
// ---------------------------------------------------------------------------

import {
  TYPES, FT_TYPES, isFT, isNoTel, deriveStatus, deriveSync, availN,
} from '../data.js';
import {
  clamp, cmpKo, langOf, makeL, SEV_RANK, STALE_ALERT_DAYS,
  sevInfo, histOf, _meta, _arr, _num, DASH,
} from './base.js';
import {
  statusTone, statusLabel, statusAnim, pctTone, pctToneAlloc,
  typeIconOf, typeInfo, usageOf, syncInfo, isMaint, fmtAvailN,
  fmtDowntimeYr, fmtUptimeD,
} from './format.js';
import {
  tsNorm, tsKey, agoSec, agoText, shortTime,
  parseLicDate, fmtLicDate, ddayText, licTone,
} from './time.js';
import {
  alertAckKey, autoAckDue, toCsv, activeMaint,
  escalDue, expiredMaint, collectAlerts, collectTraps, alertMsgKo,
} from './alert.js';

/* ===========================================================================
 * 9. buildDetail — Vigil detail.ts 이식 (variant 8종)
 * ======================================================================== */

function _resolveDetail(a, b, c) {
  if (Array.isArray(a)) return { fleet: a, id: b, state: c || {} };
  const st = a || {};
  return { fleet: _arr(st.fleet), id: (b == null ? st.selected : b), state: st };
}

const EMPTY_DEV = {
  id: '', host: DASH, type: 'EV', site: DASH, status: 'op', availN: 0,
  cpu0: -1, mem0: -1, cpuNA: true, memNA: true, sync: 'offline', uptime: 0, meta: {},
};

export function buildDetail(a, b, c) {
  const { fleet, id, state } = _resolveDetail(a, b, c);
  const S = state;
  const ko = langOf(S) === 'ko';
  const L = (en, k) => (ko ? k : en);
  // id가 주어졌는데 매칭 실패면 fleet[0]로 떨어뜨리면 안 된다(엉뚱한 장비를 실데이터처럼 렌더).
  // → EMPTY_DEV(id:'')를 돌려 detail.js의 빈 상태로 유도한다(§7 '알 수 없는 해시' 계약).
  const dev = (id == null || id === '' ? fleet[0] : fleet.find((s) => s.id === id)) || EMPTY_DEV;
  const m = _meta(dev);
  const ti = typeInfo(dev.type, L);

  const isPLC = dev.type === 'PLC';
  const isNAS = dev.type === 'NAS';
  const isWIN = dev.type === 'WIN';
  const isPI = dev.type === 'PI';
  const isPC = dev.type === 'PC';
  const isSRV = dev.type === 'SRV' || dev.type === 'PRN';   // PRN 은 srv 변형 + printer 카드
  const isServer = isSRV || isPC;
  const isDown = dev.status === 'down';
  const ft = isFT(dev.type);
  const isEND = dev.type === 'END'; // ztC Endurance — Active/Standby 모델(everRun 미러/듀플렉스 용어와 분기)

  const _plc = m.plc || {};
  const _nas = m.nas || {};
  const _win = m.win || {};
  const _pi = m.pi || {};
  const _pc = m.pc || {};
  const vendor = m.vendor || '';
  const clV = (s) => String(s || '')
    .replace(/[,·]?\s*(Co\.,?\s*Ltd\.?|Company\s+Limited|Ltd\.?|Inc\.?|Incorporated|Corp\.?|Corporation|GmbH|LLC|S\.A\.|Pty\.?)\.?\s*$/i, '')
    .replace(/[\s,]+$/, '').trim();

  const variant = isPLC ? 'plc' : (isNAS ? 'nas' : (isWIN ? 'win' : (isPI ? 'pi'
    : (isServer ? 'srv' : (dev.type === 'FTS' ? 'fts' : 'ft')))));

  const maintNodes = _arr(m.nodes)
    .filter((n) => _nodeMaint(n))
    .map((n) => n.name || '').filter(Boolean);
  const maint = maintNodes.length > 0;

  const snmp = _arr(m.snmp);
  const cpuU = usageOf(dev, 'cpu');
  const memU = usageOf(dev, 'mem');
  const hc = _sparkHist(histOf(S, dev, 'cpu'), cpuU.val);
  const hm = _sparkHist(histOf(S, dev, 'mem'), memU.val);
  const hr = histOf(S, dev, 'rtt');
  const rttHist = (hr && hr.length > 1) ? hr : [];
  const rttHi = rttHist.length ? Math.max(2, ...rttHist.map((v) => Number(v) || 0)) : 2;

  /* ---- 자원(unit) ---- */
  const u = m.unit || {};
  const resource = (u.totVcpu || u.totMem) ? {
    has: true, version: u.version || '',
    vcpuUsed: Math.round(u.usedVcpu || 0), vcpuTot: Math.round(u.totVcpu || 0),
    vcpuPct: u.totVcpu ? Math.round(((u.usedVcpu || 0) / u.totVcpu) * 100) : 0,
    memUsed: Number((u.usedMem || 0).toFixed(1)), memTot: Number((u.totMem || 0).toFixed(1)),
    memPct: u.totMem ? Math.round(((u.usedMem || 0) / u.totMem) * 100) : 0,
    syncing: String(u.syncing) === 'true',
  } : { has: false };
  if (resource.has) {
    resource.vcpuTone = pctToneAlloc(resource.vcpuPct);
    resource.memTone = pctToneAlloc(resource.memPct);
  }

  /* ---- 타일 4개(타입별) ---- */
  const tile = (o) => Object.assign({ label: '', icon: 'box', value: DASH, unit: '', delta: '', deltaTone: 'mut', valueTone: '', hasSpark: false, hist: [], histHi: 100 }, o);
  const tCpu = tile({
    label: 'CPU', icon: 'ops', value: cpuU.na ? DASH : cpuU.val, unit: cpuU.na ? '' : '%',
    delta: m.mgmt || '', hasSpark: !cpuU.na, hist: cpuU.na ? [] : hc, valueTone: cpuU.tone,
  });
  const tMem = tile({
    label: L('Memory', '메모리'), icon: 'ssd', value: memU.na ? DASH : memU.val, unit: memU.na ? '' : '%',
    delta: L('used', '사용'), hasSpark: !memU.na, hist: memU.na ? [] : hm, valueTone: memU.tone,
  });
  const tVm = tile({
    label: L('VMs', 'VM'), icon: 'box',
    value: isDown ? DASH : String(m.vmRunning != null ? m.vmRunning : (m.vms || 0)),
    unit: m.vms != null ? (' / ' + m.vms) : '', delta: L('running', '실행 중'),
  });
  const tUp = tile({
    // uptime < 0 은 미수집 센티널 — 음수 일수를 그대로 보이지 않게 대시 처리.
    label: L('Uptime', '가동시간'), icon: 'clock', value: (isDown || Number(dev.uptime) < 0) ? DASH : String(dev.uptime), unit: 'd',
    delta: m.version || (u.version || ''),
  });
  const nodesRunning = _arr(m.nodes).filter((n) => /run/i.test(n.state || '')).length;
  const tEncl = tile({
    label: L('Enclosures', '인클로저'), icon: 'box',
    value: isDown ? DASH : (nodesRunning + '/' + Math.max(1, _arr(m.nodes).length)),
    delta: 'LOCKSTEP',
  });
  const tMod = tile({
    label: L('Compute modules', '컴퓨트 모듈'), icon: 'box',
    value: isDown ? DASH : (nodesRunning + '/' + Math.max(1, _arr(m.nodes).length)),
    delta: 'ACTIVE/STANDBY',
  });
  const tBmc = tile({
    label: 'BMC', icon: 'link',
    value: m.bmc ? (m.bmc.up ? L('Online', '온라인') : L('Offline', '오프라인')) : L('Not set', '미등록'),
    delta: m.bmc ? (m.bmc.ip || '') : '', valueTone: m.bmc ? (m.bmc.up ? 'pos' : 'neg') : 'mut',
  });

  const run = String(_plc.runState || '');
  const runGo = run === 'RUN' || run === 'MONITOR';
  const plcErr = !!_plc.hasError;
  const plcObs = !plcErr && ((_plc.errSev || (_plc.sysDiag || {}).ctrlSev || '') === 'observation');
  const proto = String(_plc.protocol || '');
  const protoShort = proto === 'EtherNet/IP' ? 'E/IP' : (proto === 'Modbus TCP' ? 'Modbus' : (proto || DASH));
  const fw = String(_plc.fwVersion || '');
  const detModel = String(_plc.detectedModel || '');
  const clockSkew = _num(_plc.clockSkewSec);
  const clockBad = (clockSkew != null) && (Math.abs(clockSkew) > 86400);
  const fmtBps = (v) => {
    const n = _num(v) || 0;
    if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB/s';
    if (n >= 1024) return (n / 1024).toFixed(1) + ' KB/s';
    return n + ' B/s';
  };
  const linkTxt = _num(_plc.linkSpeedMbps) != null
    ? (_plc.linkSpeedMbps + 'Mbps · ' + (_plc.linkFullDuplex ? L('Full', '전이중') : L('Half', '반이중')))
    : '';

  const tilesPLC = [
    tile({
      label: L('Status', '상태'), icon: 'bolt',
      value: isDown ? L('Offline', '오프라인') : (run ? (run === 'PROGRAM' ? L('STOP', '정지') : run) : L('Online', '온라인')),
      delta: isDown ? (m.mgmt || '') : (run
        ? (run === 'RUN' ? L('running program', '프로그램 실행')
          : (run === 'PROGRAM' ? L('program stopped', '프로그램 정지')
            : (run === 'MONITOR' ? L('monitor mode', '모니터 모드') : run)))
          + (plcErr ? L(' · error flag', ' · 에러') : (plcObs ? L(' · observation', ' · 관찰') : ''))
        : (m.mgmt || '')),
      deltaTone: (plcErr || plcObs) ? 'warn' : 'mut',
      valueTone: isDown ? 'neg' : ((run && !runGo) ? 'warn' : 'pos'),
    }),
    tile({ label: L('Protocol', '프로토콜'), icon: 'link', value: protoShort, delta: [proto || L('control port', '제어 포트'), linkTxt].filter(Boolean).join(' · ') }),
    tile({ label: L('Port', '포트'), icon: 'link', value: _plc.port ? String(_plc.port) : DASH, delta: proto === 'FINS' ? 'UDP' : 'TCP' }),
    tile({ label: L('Maker', '제조사'), icon: 'building', value: _plc.maker || DASH, delta: [detModel || _plc.model, fw ? ('FW ' + fw) : ''].filter(Boolean).join(' · ') }),
  ];

  const nasVol = (_arr(_nas.volumes)[0]) || null;
  // 볼륨 객체가 있어도 폴리가 pct/usedGiB/sizeGiB 를 빼먹을 수 있다(#393) —
  // nasVolText(#355, detail.js)와 같은 계약으로 필드별 결측은 'undefined' 대신 DASH 표기.
  const nasVolPct = _num(nasVol && nasVol.pct);
  const nasBad = !!(_nas.systemStatus && _nas.systemStatus !== 'normal');
  // #447: systemStatus 결측은 '—'를 녹색(pos)으로 칠하지 않는다 — detail.js 배지(#397,
  // 결측 u-muted)와 같은 화면에서 녹/회색으로 갈리던 거짓 안전 신호. 결측은 mut.
  const nasHealthTone = _nas.systemStatus ? (nasBad ? 'neg' : 'pos') : 'mut';
  const tilesNAS = [
    tCpu, tMem,
    tile({ label: L('Storage', '저장소'), icon: 'db', value: nasVolPct != null ? String(nasVolPct) : DASH, unit: nasVolPct != null ? '%' : '', delta: nasVol ? ((_num(nasVol.usedGiB) != null ? nasVol.usedGiB : DASH) + ' / ' + (_num(nasVol.sizeGiB) != null ? nasVol.sizeGiB : DASH) + ' GiB') : (_nas.model || ''), valueTone: pctTone(nasVolPct) }),
    tile({
      label: L('Health', '상태'), icon: 'checklist',
      value: _nas.systemStatus ? (_nas.systemStatus === 'normal' ? L('Normal', '정상') : _nas.systemStatus) : DASH,
      delta: (_num(_nas.tempC) != null ? (_nas.tempC + '°C') : '') + (_nas.upgradeAvailable ? L(' · update', ' · 업데이트') : ''),
      deltaTone: _nas.upgradeAvailable ? 'warn' : 'mut', valueTone: nasHealthTone,
    }),
  ];

  const winDisk = (_arr(_win.disks)[0]) || null;
  // 실 폴러(/api/devices)의 win.disks 는 freeGB 를 보내지 않는다(drive/sizeGB/pct 만) → sizeGB·pct 로 파생.
  // 파생도 불가하면 라벨에서 여유 용량을 아예 뺀다('undefined GB 여유' 방지).
  const winFreeGB = winDisk
    ? (_num(winDisk.freeGB) != null ? Math.round(winDisk.freeGB)
      : (_num(winDisk.sizeGB) != null && _num(winDisk.pct) != null
        ? Math.round(winDisk.sizeGB * (100 - winDisk.pct) / 100) : null))
    : null;
  // 디스크 객체가 있어도 폴리가 pct/drive 를 빼먹을 수 있다(#439) — #393(nasVolPct)과
  // 같은 필드별 방어로 결측은 'undefined%'/'undefined C:' 대신 DASH 표기.
  const winDiskPct = _num(winDisk && winDisk.pct);
  const tilesWIN = [
    Object.assign({}, tCpu, { delta: _win.cores ? (_win.cores + L(' cores', ' 코어')) : (m.mgmt || '') }),
    Object.assign({}, tMem, { delta: _win.memTotMB ? (Math.round(_win.memTotMB / 1024) + ' GB') : L('used', '사용') }),
    tile({ label: L('Disk', '디스크'), icon: 'ssd', value: winDiskPct != null ? String(winDiskPct) : DASH, unit: winDiskPct != null ? '%' : '', delta: winDisk ? ((winDisk.drive || DASH) + (winFreeGB != null ? ' ' + winFreeGB + L(' GB free', ' GB 여유') : '')) : '', valueTone: pctTone(winDiskPct) }),
    Object.assign({}, tUp, { delta: _win.svcRunning ? (_win.svcRunning + L(' services', ' 서비스')) : '' }),
  ];

  const piTemp = _num(_pi.tempC);
  const tilesPI = [
    Object.assign({}, tCpu, { delta: _pi.model || (m.mgmt || '') }),
    Object.assign({}, tMem, { delta: _pi.memTotMB ? (Math.round(_pi.memTotMB / 1024) + ' GB') : L('used', '사용') }),
    tile({
      label: L('Temperature', '온도'), icon: 'bolt',
      value: piTemp != null ? piTemp.toFixed(1) : DASH, unit: piTemp != null ? '°C' : '',
      delta: _pi.throttled ? L('throttled', '스로틀') : (_pi.coreVolt ? (_pi.coreVolt + 'V') : ''),
      deltaTone: _pi.throttled ? 'neg' : 'mut',
      valueTone: piTemp == null ? 'mut' : (piTemp >= 75 ? 'neg' : (piTemp >= 65 ? 'warn' : 'pos')),
    }),
    tile({
      label: L('Network', '네트워크'), icon: 'link',
      value: _num(_pi.rttMs) != null ? (_pi.rttMs < 1 ? '<1' : String(_pi.rttMs)) : DASH,
      unit: _num(_pi.rttMs) != null ? 'ms' : '', delta: L('ping · RTT', '핑 · 왕복'),
      hasSpark: !!(rttHist.length && !isDown), hist: rttHist, histHi: rttHi,
    }),
  ];

  const pcSvc = _arr(_pc.services);
  const pcSkew = _num(_pc.clockSkewSec);
  const pcSkewStr = pcSkew == null ? ''
    : (Math.abs(pcSkew) < 90 ? ((pcSkew > 0 ? '+' : '') + pcSkew + 's') : (Math.round(pcSkew / 60) + 'm'));
  const pcSign = _pc.smbSigning
    ? (_pc.smbSigning.required ? L('signing required', '서명 강제')
      : (_pc.smbSigning.enabled ? L('signing on', '서명 활성') : L('signing off', '서명 없음')))
    : '';
  const tilesPC = [
    tile({ label: L('Host', '호스트'), icon: 'bookmark', value: (_pc.netbios || m.label || dev.host || DASH), delta: [_pc.workgroup ? ('⌂ ' + _pc.workgroup) : (_pc.snmp ? 'SNMP' : L('agentless', '에이전트리스')), _pc.loginUser ? ('\u{1F464} ' + _pc.loginUser) : null].filter(Boolean).join(' · ') }),
    tile({ label: 'OS', icon: 'checklist', value: _pc.os || L('Unknown', '미상'), delta: [_pc.osVersion || null, _pc.smbDialect ? ('SMB ' + _pc.smbDialect) : null].filter(Boolean).join(' · ') }),
    tile({ label: L('Services', '노출 서비스'), icon: 'link', value: pcSvc.length ? pcSvc.join(' · ') : DASH, delta: [pcSign || null, pcSkewStr ? (L('clock ', '시계 ') + pcSkewStr) : null].filter(Boolean).join(' · ') }),
    tile({
      label: L('Network', '네트워크'), icon: 'link',
      value: _num(_pc.rttMs) != null ? (_pc.rttMs < 1 ? '<1' : String(_pc.rttMs)) : DASH,
      unit: _num(_pc.rttMs) != null ? 'ms' : '', delta: L('ping · RTT', '핑 · 왕복'),
      hasSpark: !!(rttHist.length && !isDown), hist: rttHist, histHi: rttHi,
    }),
  ];

  // 타일 선택 — 이전엔 7단 중첩 삼항이라 어느 변형이 무엇을 받는지 읽을 수 없었다.
  // 타입 전용 세트가 먼저, 그다음 변형별 규칙. 동작은 그대로이고 구조만 편다.
  //
  // 규칙: 하단의 노드/인클로저/서버 카드가 CPU·메모리의 정본이라, 그 카드를 가진 변형
  // (ft·fts·srv)의 상단 KPI 타일에서는 CPU·메모리를 빼고 글랜스 지표만 남긴다 —
  // 타일+카드 이중 출력(오프라인 시 양쪽 '—')을 해소(A1/minor#1).
  // 가동시간(tUp)은 타일이 정본. fts 인클로저 카드의 노드별 가동시간은 재부팅 신호라 유지한다.
  const tonerTile = () => {
    const t = tonerSummary(m.printer);
    return tile({
      label: L('Toner', '토너'), icon: 'db',
      value: t.minPct != null ? String(t.minPct) : DASH, unit: t.minPct != null ? '%' : '',
      delta: t.pages != null ? (L('pages ', '페이지 ') + Number(t.pages).toLocaleString()) : '',
      valueTone: t.minPct == null ? '' : (t.minPct <= 10 ? 'neg' : (t.minPct <= 25 ? 'warn' : '')),
    });
  };
  const byType = isPLC ? tilesPLC
    : isNAS ? tilesNAS
      : isWIN ? tilesWIN
        : isPI ? tilesPI
          : isPC ? tilesPC
            : null;
  let tiles;
  if (byType) {
    tiles = byType;
  } else if (variant === 'fts') {
    tiles = [tEncl, tUp];
  } else if (variant === 'srv' && dev.type === 'PRN') {
    // 프린터는 VM 개념이 없다 — 글랜스 타일은 토너 최저·페이지 카운터(사용자 지적).
    tiles = [tonerTile(), tUp];
  } else if (variant === 'srv') {
    tiles = [m.bmc ? tBmc : tVm, tUp];
  } else if (isEND) {
    // Endurance 는 가상머신이 없다(베어메탈 OS — 사용자 정정) — VM 타일 대신 모듈 상태.
    tiles = [tMod, tUp];
  } else {
    tiles = [tVm, tUp];
  }

  /* ---- 노드 행 ---- */
  const reachSnmp = snmp.filter((x) => x && x.reachable);
  const rawNodes = _arr(m.nodes).length
    ? m.nodes
    : (reachSnmp.length
      ? reachSnmp.map((x, i) => ({ name: 'node' + i, state: 'running', standing: 'normal', ip: x.ip, _sn: x, _snmpOnly: true }))
      // 수집 불가(다운) 시 자리표시자: FT만 2노드, 단일 장비는 1노드.
      : (ft ? [null, null] : [null]));

  const upFmt = (sec) => {
    if (sec == null) return DASH;
    const d = Math.floor(sec / 86400); const h = Math.floor((sec % 86400) / 3600); const mi = Math.floor((sec % 3600) / 60);
    if (d > 0) return d + 'd ' + h + 'h';
    if (h > 0) return h + 'h ' + mi + 'm';
    return mi + 'm';
  };

  const nodeRow = (n, i) => {
    const sn = (n && n._sn) || (n && n.ip ? (snmp.find((x) => x && x.ip === n.ip) || {}) : (snmp[i] || {}));
    const downN = n ? !/run/i.test(n.state || '') : isDown;
    const warn = !!(n && n.standing && String(n.standing).toLowerCase() !== 'normal');
    const prim = !!(n && n.primary);
    const sOnly = !!(n && n._snmpOnly);
    const nMaint = !!(n && _nodeMaint(n));
    return {
      name: (n && n.name) || (dev.host + (i === 0 ? '-A' : '-B')),
      primary: prim, maint: nMaint, maintLabel: L('Maintenance', '점검 중'),
      badge: variant === 'fts' ? 'LOCKSTEP'
        : (isEND ? (prim ? 'ACTIVE' : 'STANDBY')
          : (isPLC ? 'PLC' : (isPC ? 'PC' : (isSRV ? (dev.type === 'PRN' ? L('PRINTER', '프린터') : L('SERVER', '서버'))
            : (prim ? L('PRIMARY', '주 노드') : (n && !sOnly ? L('STANDBY', '보조 노드') : L('NODE', '노드'))))))),
      badgeTone: (variant === 'fts' || prim) ? 'pos' : 'mut',
      // G23: 역할어는 배지가 1회 진술 — 부제는 배지에 없는 '차별 정보'만 담는다.
      //     (예전 주 노드 부제 '주 노드'는 배지의 완전 재진술 = 무의미 중복, minor.)
      role: variant === 'fts' ? (L('Lockstepped enclosure', '락스텝 인클로저') + (n && n.ip ? ' · ' + n.ip : ''))
        : (isEND ? ((n && n.os) || DASH)
          : (prim ? L('Sync source', '동기 소스')
          : (isPLC ? ([(n && n.manufacturer) || _plc.maker, (n && n.model) || _plc.model].filter(Boolean).join(' ') + (n && n.ip ? ' · ' + n.ip : ''))
            : (isPC ? ((n && n.ip) || _pc.os || L('Windows PC', 'Windows PC'))
              : (isSRV ? ((vendor ? clV(vendor) + ' ' : '') + L('server', '서버') + (n && n.ip ? ' · ' + n.ip : ''))
                : (sOnly ? (L('SNMP only · FT role N/A', 'SNMP 전용 · FT 역할 미확인') + (n && n.ip ? ' · ' + n.ip : ''))
                  : (n ? L('Live mirror', '실시간 미러') : L('Standing by', '대기')))))))),
      // G27(실버그): 점검 모드 노드는 계획 정지 — red(실장애 전용)가 아니라 amber.
      //     헤더 필·점검 배너·심플렉스 배지(전부 amber)와 색 범주 일치.
      tone: nMaint ? 'warn' : (downN ? 'neg' : (warn ? 'warn' : 'pos')),
      state: nMaint ? L('MAINTENANCE', '점검 중')
        : (downN ? L('OFFLINE', '오프라인') : (isPLC ? L('ONLINE', '온라인') : String((n && n.state) || 'running').toUpperCase())),
      ip: (n && n.ip) || (sn && sn.ip) || '',
      // SNMP 미수집 노드(Proxmox 등)는 노드 객체 자체 지표(cpu_pct — PVE API)로 폴백.
      // '<1%' 판정은 subPctText(단일 구현, §1) 로 위임한다.
      cpu: sn.cpu != null ? sn.cpu + '%'
        : (n && n.cpu_pct != null ? subPctText(n.cpu_pct, n.cpu_pct1) : DASH),
      mem: sn.mem != null ? sn.mem + '%' : (n && n.mem_pct != null ? n.mem_pct + '%' : DASH),
      cpuN: sn.cpu != null ? Number(sn.cpu) : (n && n.cpu_pct != null ? Number(n.cpu_pct) : null),
      memN: sn.mem != null ? Number(sn.mem) : (n && n.mem_pct != null ? Number(n.mem_pct) : null),
      cpuTone: sn.cpu != null ? pctTone(sn.cpu) : (n && n.cpu_pct != null ? pctTone(n.cpu_pct) : 'mut'),
      memTone: sn.mem != null ? pctTone(sn.mem) : (n && n.mem_pct != null ? pctTone(n.mem_pct) : 'mut'),
      modules: (n && n.modules) || null,
      // END 전용(다른 타입은 null) — detail EnduranceCard 의 IP 플랜·연결 상태 재료.
      module: (n && n.module) || '',
      bmc: (n && n.bmc) || null,
      standbyNic: (n && n.standbyNic) || null,
      fabricConn: !!(n && n.fabricConn),
      bootDevice: (n && n.bootDevice) || '',
      osName: (n && n.os) || '',
      uptime: sn.uptime_secs != null ? upFmt(sn.uptime_secs) : DASH,
      rebooted: !!sn.rebooted_at, fresh: !!sn.fresh,
      rebootLabel: sn.rebooted_at ? (L('Rebooted', '재부팅') + ' · ' + agoText(sn.reboot_ago, L, ko)) : '',
    };
  };
  const nodes = rawNodes.map(nodeRow);

  /* ---- 하드웨어 ---- */
  const hw = rawNodes.filter(Boolean).map((n, i) => {
    const sn = n._sn || (n.ip ? (snmp.find((x) => x && x.ip === n.ip) || {}) : (snmp[i] || {}));
    const ok = (!n.standing || String(n.standing).toLowerCase() === 'normal');
    const detail = n._snmpOnly
      ? [n.ip, (sn.cpuModel || sn.manufacturer || null), (sn.memGiB ? sn.memGiB + ' GiB' : null),
        (sn.serial && sn.serial !== '00000000') ? ('SN ' + sn.serial) : null,
        sn.bios ? 'BIOS ' + sn.bios : null,
        sn.cpu != null ? 'CPU ' + sn.cpu + '%' : null,
        sn.mem != null ? 'MEM ' + sn.mem + '%' : null].filter(Boolean).join(' · ')
      : [n.cpuModel || n.model, (n.memGiB ? n.memGiB + ' GiB' : n.memory),
        (n.serial && n.serial !== '00000000') ? ('SN ' + n.serial) : null,
        n.bios ? ('BIOS ' + n.bios) : null].filter(Boolean).join(' · ');
    // FT(everRun/ztC) 는 노드 running/정상 을 '무정지 이중화' 카드가 담당하므로, 하드웨어 카드에선
    // 모델·용량만 남기고 running 값·정상 뱃지를 비워 이중 표기를 없앤다(minor#6). 서버류는 그 카드가
    // 없어 하드웨어 상태가 유일 표기이므로 그대로 둔다.
    const showState = isServer;
    return {
      icon: 'ops', label: (isServer ? (m.label || n.name) : n.name), detail,
      value: showState ? (n.state || 'running') : '',
      stat: showState ? (ok ? L('Normal', '정상') : (n.standing || '')) : '',
      statTone: ok ? 'pos' : 'warn',
      tone: /run/i.test(n.state || 'running') ? (ok ? 'pos' : 'warn') : 'neg',
    };
  });

  // Proxmox 호스트 — 물리 디스크(SMART)·NIC/브리지·스토리지 풀을 하드웨어 카드에 실측 나열.
  if (isServer && m.platform === 'proxmox') {
    _arr(m.srvDisks).forEach((dk) => {
      const h = String(dk.health || '').toUpperCase();
      const usb = dk.kind === 'usb';
      const bad = !usb && h && ['PASSED', 'OK', 'UNKNOWN'].indexOf(h) < 0;
      const lowLife = _num(dk.wearout) != null && dk.wearout <= 10;
      hw.push({
        icon: 'ssd',
        label: dk.model || dk.dev,
        detail: [dk.dev, dk.serial ? 'SN ' + dk.serial : null,
          (dk.kind || '').toUpperCase(), dk.sizeGB ? dk.sizeGB + ' GB' : null,
          dk.rpm ? dk.rpm + ' RPM' : null].filter(Boolean).join(' · '),
        value: _num(dk.wearout) != null ? L('life ', '수명 ') + dk.wearout + '%' : '',
        stat: bad ? dk.health : (usb || !h || h === 'UNKNOWN' ? '' : L('Normal', '정상')),
        statTone: bad ? 'neg' : 'pos',
        tone: bad ? 'neg' : (lowLife ? 'warn' : 'pos'),
      });
    });
    _arr(m.srvNet).forEach((nt) => {
      const br = nt.kind === 'bridge';
      hw.push({
        icon: 'link',
        label: nt.name + (br ? L(' (bridge)', ' (브리지)') : ''),
        detail: [nt.mac || null, nt.ip || null, nt.gw ? 'GW ' + nt.gw : null,
          (br && nt.ports) ? '← ' + nt.ports : null].filter(Boolean).join(' · '),
        value: '',
        stat: nt.up ? L('Link', '링크') : L('Unplugged', '미연결'),
        // 미연결 포트는 결함이 아니라 정보(토폴로지 LAN 행과 동일 규약).
        statTone: nt.up ? 'pos' : 'mut',
        tone: nt.up ? 'pos' : 'mut',
      });
    });
    _arr(m.srvStorage).forEach((sp) => {
      hw.push({
        icon: 'db',
        label: sp.name,
        detail: [sp.type, (sp.usedGiB != null && sp.totalGiB) ? sp.usedGiB + ' / ' + sp.totalGiB + ' GiB' : null]
          .filter(Boolean).join(' · '),
        value: (sp.pct != null ? sp.pct + '%' : ''),
        stat: '',
        statTone: 'pos',
        tone: sp.pct >= 90 ? 'neg' : (sp.pct >= 78 ? 'warn' : 'pos'),
      });
    });
  }

  /* ---- 라이선스 카드 ---- */
  const lic = m.license;
  let license = { has: false };
  if (lic) {
    const exp = (lic.expires && lic.expire) ? fmtLicDate(lic.expire) : null;
    let dLeft = null;
    if (exp) {
      const d = new Date(exp + 'T00:00:00Z');
      if (!isNaN(d.getTime())) dLeft = Math.round((d.getTime() - Date.now()) / 86400000);
    }
    // #593: 만료형인데 만료일이 결측이거나 파싱 불가(dLeft=null)면 '영구'가 아니라 미상(NA) —
    //   개요(buildModel)·클로스터 행과 같은 판정으로 세 소비 경로를 맞춘다.
    const expOk = exp != null && dLeft != null;
    license = {
      has: true, edition: lic.edition || DASH,
      typeLabel: lic.type === 'trial' ? L('Trial', '평가판') : (lic.type === 'standard' ? L('Standard', '정식') : (lic.type || DASH)),
      name: lic.name || '',
      statusLabel: lic.activated ? L('Activated', '정품 인증됨') : L('Not activated', '미인증'),
      statusTone: lic.activated ? 'pos' : 'neg',
      expiryLabel: expOk ? L('Expires', '만료일') : L('Expiry', '만료'),
      // #326: 만료분(dLeft<0)은 '0일 남음'으로 클램프하지 않고 개요 ddayText 와 같은
      //     '만료 D+N' 으로 표기 — 클램프는 오늘 만료처럼 읽혀 개요/클로스터와 자기모순이었다.
      expiry: expOk ? (exp + ' · ' + (dLeft < 0
        ? ddayText(dLeft, L, ko)
        : dLeft + (ko ? '일 남음' : 'd left'))) : (lic.expires ? L('Unknown', '미상') : L('Perpetual', '영구')),
      days: dLeft, dayTone: licTone(dLeft),
      install: fmtLicDate(lic.install),
    };
  }

  /* ---- 경보/트랩 ---- */
  const alertsList = _arr(m.alerts).slice()
    .sort((x, y) => String(y.time || '').localeCompare(String(x.time || '')))
    .slice(0, 6)
    .map((x) => {
      const sv = sevInfo(x.sev || x.severity, L);
      return {
        desc: String(x.desc || x.name || ''), time: String(x.time || ''),
        ago: agoText(agoSec(x.time), L, ko) || String(x.time || ''),
        sev: sv.key, sevLabel: sv.label, tone: sv.tone, icon: sv.icon,
      };
    });
  const trapsList = _arr(m.traps).slice(0, 8).map((t) => {
    const sv = sevInfo(t.sev, L);
    return {
      desc: String(t.desc || t.oid || 'SNMP trap'), time: String(t.time || ''),
      // level(ERROR/WARN/INFO): 트랩 심각도를 도트 색 단독이 아닌 텍스트로도 부호화해 로그 화면과 통일(minor).
      src: String(t.src || ''), sev: sv.key, tone: sv.tone, icon: sv.icon, level: sv.level,
    };
  });

  /* ---- 배너/알림 ---- */
  const avErr = m.error
    ? (/cert|ssl|NotAfter|handshake/i.test(m.error)
      ? L('Management console certificate error — cluster detail unavailable', '관리 콘솔 인증서 오류 — 클러스터 상세 수집 불가')
      : (L('Cluster detail unavailable', '클러스터 상세 수집 불가') + ': ' + String(m.error).slice(0, 70)))
    : null;
  const notices = [];
  if (maint) notices.push({ icon: '점검', tone: 'warn', strong: L('Maintenance mode', '점검 모드'), text: maintNodes.join(', ') + ' · ' + L('planned maintenance', '계획된 유지보수 중') });
  if (m.lastReboot) notices.push({ icon: 'cycle', tone: 'info', strong: L('Recent reboot detected', '재부팅 감지됨'), text: (m.lastReboot.node || '') + ' · ' + agoText(m.lastReboot.agoSecs, L, ko) });
  if (avErr) notices.push({ icon: 'warningCircle', tone: 'neg', strong: L('Data collection', '수집 상태'), text: avErr });
  if (isPLC && _plc.errorMessage) notices.push({ icon: 'warningCircle', tone: 'neg', strong: L('PLC message (FAL/FALS)', 'PLC 메시지 (FAL/FALS)'), text: String(_plc.errorMessage) });

  /* ---- PLC 카드들 ---- */
  const plcMain = isPLC ? {
    run: isDown ? L('OFFLINE', '오프라인') : (run === 'PROGRAM' ? L('STOP', '정지') : (run || L('ONLINE', '온라인'))),
    runTone: isDown ? 'neg' : (((run && !runGo) || plcErr) ? 'warn' : 'pos'),
    sub: isDown ? L('Control port not responding', '제어 포트 미응답')
      : (run ? (runGo ? L('Program running', '프로그램 실행 중') : L('Program stopped', '프로그램 정지됨')) : L('Reachable', '응답')),
    err: plcErr, errLabel: L('Error flag', '에러 플래그'),
    obs: plcObs, obsLabel: L('Observation', '관찰 이벤트'),
    maker: _plc.maker || DASH, model: detModel || _plc.model || DASH, ip: m.mgmt || '',
  } : null;

  const cipMajor = !!_plc.cipMajorFault;
  const cipMinor = !!_plc.cipMinorFault;
  const cipStatus = String(_plc.cipStatus || '');
  const plcComm = isPLC ? [
    { k: L('Protocol', '프로토콜'), v: proto || DASH },
    { k: L('Port', '포트'), v: _plc.port ? String(_plc.port) + ' · ' + (proto === 'FINS' ? 'UDP' : 'TCP') : DASH },
    { k: L('Link', '링크'), v: linkTxt || DASH },
    { k: L('Response time', '응답 시간'), v: _num(_plc.finsRttMs) != null ? (_plc.finsRttMs < 1 ? '<1 ms' : _plc.finsRttMs + ' ms') : DASH },
    { k: L('Detected model', '감지 모델'), v: detModel || DASH },
    { k: L('Firmware', '펌웨어'), v: fw || DASH },
    { k: L('Unit revision', '유닛 리비전'), v: String(_plc.unitRev || '') || DASH },
    { k: L('Serial', '시리얼'), v: String(_plc.serial || '') || DASH },
    { k: 'MAC', v: String(_plc.mac || '') || DASH },
    { k: L('Hostname', '호스트명'), v: String(_plc.hostname || '') || DASH },
    {
      k: L('IP config', 'IP 구성'),
      v: _plc.netMask ? (L('mask ', '마스크 ') + _plc.netMask + ' · GW '
        + (_plc.gateway || L('none', '없음'))) : DASH,
    },
    {
      k: L('Traffic', '트래픽'),
      v: _num(_plc.netInBps) != null
        ? ('↓ ' + fmtBps(_plc.netInBps) + ' · ↑ ' + fmtBps(_plc.netOutBps)) : DASH,
    },
    {
      k: L('Line errors', '회선 오류'),
      v: _num(_plc.netInErrors) != null
        ? (L('rx ', '수신 ') + _plc.netInErrors + L(' · tx ', ' · 송신 ') + (_plc.netOutErrors || 0)
          + (_num(_plc.netCrcErrors) != null ? ' · CRC ' + _plc.netCrcErrors : '')) : DASH,
      tone: ((_plc.netInErrors || 0) + (_plc.netOutErrors || 0) + (_plc.netCrcErrors || 0)) > 0 ? 'warn' : 'pos',
    },
    { k: L('Product name', '제품명'), v: String(_plc.productName || '') || DASH },
    {
      k: L('CIP status', 'CIP 상태'),
      v: cipStatus ? (cipStatus + (cipMajor ? L(' · MAJOR FAULT', ' · 메이저 폴트') : (cipMinor ? L(' · minor fault', ' · 마이너 폴트') : L(' · no fault', ' · 폴트 없음')))) : DASH,
    },
    (function () {
      const io = _num(_plc.ioConn);
      if (io == null) return { k: '', v: DASH };
      const m2 = { 2: [L('I/O connection fault', 'I/O 연결 오류'), 'neg'],
        3: [L('No data links', '데이터링크 없음'), 'mut'],
        6: [L('Data link running', '데이터링크 실행'), 'pos'],
        7: [L('Data link idle', '데이터링크 유휴'), 'warn'] };
      const e = m2[io] || [L('Standby', '대기'), 'mut'];
      return { k: L('EtherNet/IP link', 'EtherNet/IP 링크'), v: e[0], tone: e[1] };
    })(),
    { k: L('Fatal code', '치명 코드'), v: String(_plc.finsFatalCode || '') || DASH },
    { k: L('Non-fatal code', '비치명 코드'), v: String(_plc.finsNonFatalCode || '') || DASH },
  ].filter((r) => r.v !== DASH) : null;

  const plcClock = (isPLC && clockSkew != null) ? {
    bad: clockBad, days: Math.floor(Math.abs(clockSkew) / 86400),
    text: clockBad
      ? L('Off by ~' + Math.floor(Math.abs(clockSkew) / 86400) + ' days (' + (clockSkew > 0 ? 'behind' : 'ahead') + ') — check the RTC battery or reset the clock',
        '실제 시간과 약 ' + Math.floor(Math.abs(clockSkew) / 86400) + '일 차이 (' + (clockSkew > 0 ? '느림' : '빠름') + ') — RTC 배터리 점검 또는 시계 재설정 필요')
      : L('Within normal range (under 1 day)', '정상 범위 (1일 이내 오차)'),
  } : null;

  const sysD = (isPLC && _plc.sysDiag) || null;
  const MOD_KO = { Controller: '컨트롤러', 'I/O bus': 'I/O 버스', Motion: '모션' };
  const SEV_PLC = {
    observation: { en: 'Observation', ko: '관찰', tone: 'warn' },
    minor: { en: 'Minor fault', ko: '경미 결함', tone: 'warn' },
    partial: { en: 'Partial fault', ko: '부분 결함', tone: 'neg' },
    major: { en: 'Major fault', ko: '중대 결함', tone: 'neg' },
  };
  const SEV_ORD = { observation: 1, minor: 2, partial: 3, major: 4 };
  const worstSev = sysD
    ? (String(sysD.ctrlSev || '') || (_arr(sysD.modules).reduce((w, mo) => ((SEV_ORD[mo.sev] || 0) > (SEV_ORD[w] || 0) ? mo.sev : w), '')))
    : '';
  const plcDiag = (sysD && _arr(sysD.modules).length) ? {
    err: !!sysD.ctrlErr,
    state: worstSev ? ((worstSev === 'partial' || worstSev === 'major') ? 'err' : 'warn') : (sysD.ctrlErr ? 'err' : 'ok'),
    modules: sysD.modules.map((mo) => {
      const si = SEV_PLC[mo.sev] || null;
      return {
        label: ko ? (MOD_KO[mo.module] || mo.module) : mo.module,
        code: String(mo.code || ''), err: !!mo.err,
        sevLabel: si ? (ko ? si.ko : si.en) : (mo.err ? (ko ? '오류' : 'ERR') : ''),
        tone: si ? si.tone : (mo.err ? 'neg' : 'pos'),
      };
    }),
    extras: []
      .concat(sysD.userAlarm ? [{ k: L('User alarm (FAL)', '사용자 알람 (FAL)'), v: sysD.userAlarm.active ? String(sysD.userAlarm.code || '') : L('None', '없음'), warn: !!sysD.userAlarm.active }] : [])
      .concat(typeof sysD.outHoldCfg === 'boolean' ? [{ k: L('Output hold', '출력 유지 설정'), v: sysD.outHoldCfg ? L('Enabled', '사용') : L('Off', '미사용'), warn: false }] : []),
    powerOn: _num(sysD.powerOnCount),
    since: (_plc.errSince && worstSev) ? String(_plc.errSince) : '',
    sinceLabel: L('Observed since', '감지 시각'),
    history: _arr(_plc.errHistory).slice(0, 4).map((h) => {
      const toSev = SEV_PLC[h.to] || null;
      return {
        at: String(h.at || ''),
        label: h.to ? ((toSev ? (ko ? toSev.ko : toSev.en) : h.to) + L(' raised', ' 발생')) : L('cleared', '해제'),
        warn: !!h.to,
      };
    }),
    historyLabel: L('Recent transitions', '최근 이력'),
  } : null;

  const sd = (sysD && sysD.sdCard) || null;
  const plcSd = sd ? {
    bad: !!(sd.deteriorated || sd.powerFail || sd.err),
    rows: [{ k: L('Status', '상태'), v: sd.ready ? L('Ready', '정상 인식') : L('Not inserted', '미장착'), warn: false, dim: !sd.ready }]
      .concat(sd.ready ? [
        { k: L('Lifetime', '수명'), v: sd.deteriorated ? L('Deteriorated — replace', '수명 저하 — 교체 필요') : L('Good', '양호'), warn: !!sd.deteriorated },
        { k: L('Card error', '카드 오류'), v: sd.err ? L('Error', '오류') : L('None', '없음'), warn: !!sd.err },
        { k: L('Write protect', '쓰기 방지'), v: sd.protected ? L('On', '설정됨') : L('Off', '해제'), warn: false },
      ] : [])
      .concat(sd.powerFail ? [{ k: L('Power fail', '전원 이상'), v: L('Write interrupted by power loss', '전원 차단 중 쓰기 중단'), warn: true }] : []),
  } : null;

  const eip = (sysD && sysD.eip) || null;
  const nc = (isPLC && _plc.netCounters) || null;
  const fmtB = (n) => {
    if (n == null) return DASH;
    if (n >= 1073741824) return (n / 1073741824).toFixed(2) + ' GiB';
    if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MiB';
    if (n >= 1024) return (n / 1024).toFixed(1) + ' KiB';
    return n + ' B';
  };
  const portBad = !!(eip && eip.portErr && eip.portErr !== '0x0000');
  const plcNet = (eip || nc) ? {
    svc: eip ? [
      { k: L('EtherNet/IP service', 'EtherNet/IP 서비스'), v: eip.online ? L('Online', '온라인') : L('Offline', '오프라인'), tone: eip.online ? 'pos' : 'neg' },
      { k: L('Tag data links', '태그 데이터링크'), v: eip.tagLinkRun ? L('Running', '실행 중') : L('Not in use', '미사용'), tone: eip.tagLinkRun ? 'pos' : 'mut' },
      { k: L('LAN hardware', 'LAN 하드웨어'), v: eip.lanHwErr ? L('Error', '오류') : L('Normal', '정상'), tone: eip.lanHwErr ? 'neg' : 'pos' },
      { k: L('Port error', '포트 오류'), v: portBad ? eip.portErr : L('None', '없음'), tone: portBad ? 'neg' : 'pos' },
    ].concat((eip.tcpAppErr || eip.cipErr) ? [{
      k: L('EIP errors (TCP/CIP)', 'EIP 오류 (TCP/CIP)'),
      v: (eip.tcpAppErr || '0x0000') + ' / ' + (eip.cipErr || '0x0000'),
      tone: ((eip.tcpAppErr && eip.tcpAppErr !== '0x0000') || (eip.cipErr && eip.cipErr !== '0x0000')) ? 'neg' : 'pos',
    }] : []).concat((typeof eip.bootpErr === 'boolean' || typeof eip.identityErr === 'boolean') ? [{
      k: L('BOOTP / identity', 'BOOTP / 식별'),
      v: (eip.bootpErr || eip.identityErr) ? L('Error', '오류') : L('Normal', '정상'),
      tone: (eip.bootpErr || eip.identityErr) ? 'neg' : 'pos',
    }] : []).concat(eip.ntp ? [{
      k: L('NTP sync', 'NTP 동기화'),
      v: (eip.ntp.ok ? L('OK', '정상') : L('Failed', '실패')) + (eip.ntp.last ? ' · ' + String(eip.ntp.last).slice(0, 10) : ''),
      tone: eip.ntp.ok ? 'pos' : 'neg',
    }] : []) : [],
    ctr: nc ? [
      { k: L('Received (since boot)', '수신 누적'), v: fmtB(nc.inOctets) },
      { k: L('Sent (since boot)', '송신 누적'), v: fmtB(nc.outOctets) },
      { k: L('Errors (in / out)', '오류 (수신/송신)'), v: (nc.inErrors || 0) + ' / ' + (nc.outErrors || 0), warn: ((nc.inErrors || 0) + (nc.outErrors || 0)) > 0 },
      { k: L('Discards (in / out)', '폐기 (수신/송신)'), v: (nc.inDiscards || 0) + ' / ' + (nc.outDiscards || 0), warn: ((nc.inDiscards || 0) + (nc.outDiscards || 0)) > 0 },
    ] : [],
  } : null;

  const plcVars = (isPLC && _arr(_plc.procVars).length)
    ? _plc.procVars.map((v) => ({ name: String(v.name || ''), label: String(v.label || v.name || ''), unit: String(v.unit || ''), value: String(v.value != null ? v.value : DASH) }))
    : null;

  /* ---- 하드웨어 한 줄 / PC 행 ---- */
  const uniq = (list) => list.filter((v, i, arr) => arr.indexOf(v) === i);
  const hwLine = isPI
    ? [(_pi.model || 'Raspberry Pi'), (_pi.kernel ? ('kernel ' + _pi.kernel) : null), (_pi.mac || null), (_pi.cpu != null ? 'SSH' : L('agentless', '에이전트리스'))].filter(Boolean).join(' · ')
    : (isWIN
      ? [(_win.os || null), (_win.build ? ('build ' + _win.build) : null), (_win.domain ? ('⌂ ' + _win.domain) : null), (_win.serial ? ('S/N ' + _win.serial) : null), 'WinRM'].filter(Boolean).join(' · ')
      : (isNAS
        ? ['Synology' + (_nas.model ? ' ' + _nas.model : ''), (_nas.dsmVersion || null), (_nas.serial ? ('S/N ' + _nas.serial) : null), L('SNMP live', 'SNMP 수집')].filter(Boolean).join(' · ')
        : (isPC
          ? uniq([(_pc.fqdn || null), (clV(_pc.macVendor || vendor) || null), (_pc.mac || null), (_pc.snmp ? L('SNMP live', 'SNMP 수집') : L('agentless', '에이전트리스'))].filter(Boolean)).join(' · ')
          : (isSRV
            ? uniq([(vendor || null), (rawNodes[0] && (rawNodes[0].model || rawNodes[0].cpuModel)) || null, (snmp[0] && snmp[0].cpuModel) || null].filter(Boolean)).join(' · ')
            : ''))));

  const _srv = m.srv || null;
  const pcRows = isPC ? [
    _pc.loginUser ? { k: L('User', '로그인'), v: '\u{1F464} ' + _pc.loginUser } : null,
    _pc.fileSharing ? { k: L('File sharing', '파일 공유'), v: L('active', '활성') } : null,
    _pc.machineGuid ? { k: L('Machine GUID', '머신 GUID'), v: String(_pc.machineGuid) } : null,
  ].filter(Boolean).concat(_arr(_pc.certs).map((cert) => ({
    k: '\u{1F512} ' + String(cert.service || 'TLS'),
    v: String(cert.subject || '') + (cert.notAfter ? (' · ~' + String(cert.notAfter).slice(0, 10)) : '') + (cert.selfSigned ? L(' (self-signed)', '(자체서명)') : ''),
  })))
    // Proxmox 호스트 — 서버 카드 그리드에 시스템 제원(PVE API /nodes/<n>/status).
    : ((isServer && _srv) ? [
      _srv.cpuModel ? { k: 'CPU', v: _srv.cpuModel + (_srv.cores ? ' · ' + _srv.cores + 'C/' + (_srv.threads || _srv.cores) + 'T' : '') } : null,
      _srv.memGiB ? { k: L('Memory', '메모리'), v: _srv.memGiB + ' GiB' + (_srv.swapGiB ? L(' · swap ', ' · 스왑 ') + _srv.swapGiB + ' GiB' + (_srv.swapUsedPct ? ' (' + _srv.swapUsedPct + '%)' : '') : '') } : null,
      _srv.rootfsGiB ? { k: L('Root FS', '루트 FS'), v: (_srv.rootfsUsedGiB != null ? _srv.rootfsUsedGiB + ' / ' : '') + _srv.rootfsGiB + ' GiB' + (_srv.rootfsPct != null ? ' · ' + _srv.rootfsPct + '%' : '') } : null,
      _srv.kernel ? { k: L('Kernel', '커널'), v: String(_srv.kernel) } : null,
      _srv.boot ? { k: L('Boot', '부팅'), v: String(_srv.boot) } : null,
      (_srv.iowaitPct != null && _srv.iowaitPct >= 1) ? { k: 'I/O wait', v: _srv.iowaitPct + '%' } : null,
    ].filter(Boolean) : []);

  /* ---- 제어(ControlsCard) ---- */
  // 스펙 §5.4: ControlsCard 는 everRun 계열(FT)만. 그 외 타입은 null.
  const control = !ft ? null : {
    id: dev.id,
    nodes: _arr(m.nodes).map((n) => ({
      name: n.name,
      maint: _nodeMaint(n),
      down: !/run/i.test(n.state || ''),
      primary: n.primary === true,
    })),
    vms: _arr(m.vmList).map((v) => ({ name: v.name, running: v.state === 'running' })),
    actions: [
      { key: 'maint', label: L('Maintenance', '점검'), icon: '점검', danger: false },
      { key: 'reboot', label: L('Reboot', '재시작'), icon: '재시작', danger: true },
      { key: 'shutdown', label: L('Shutdown', '종료'), icon: '재시작', danger: true },
      { key: 'recover', label: L('Recover', '복구'), icon: 'cycle', danger: false },
    ],
  };

  /* ---- VM 목록 ---- */
  const procs = _arr(m.vmList).map((v) => {
    const fr = String(v.ft || '').toLowerCase();
    const g = v.guest;
    // 보호 '방식'(FT/HA)과 보호가 '지금 살아있는지'는 다른 축이다.
    // 폴러는 VM 별 diskMirrored/nicRedundant 를 주는데 UI 가 통째로 버리고 있었다(감사) —
    // 디스크 미러가 깨져도 초록 'FT' 배지가 그대로 떠서, 무정지 콘솔이 보호 상실을 숨겼다.
    // 명시적 false 일 때만 저하로 본다(비FT 장비는 이 필드가 아예 없다).
    const mirrorBroken = v.diskMirrored === false;
    const nicBroken = v.nicRedundant === false;
    // 페일오버 후보 노드 — instanceNodes 가 1개로 줄면 옮겨갈 곳이 없다. 디스크·NIC 이 멀쩡해도
    // 이 상태의 VM 은 노드 하나가 죽는 순간 같이 죽는다(FT 의 의미가 사라진다).
    // 필드가 아예 없는 장비(비FT)는 판정하지 않는다.
    const cand = _arr(v.instanceNodes);
    const noFailover = cand.length === 1;
    const legs = [];
    if (mirrorBroken) legs.push(L('disk mirror', '디스크 미러'));
    if (nicBroken) legs.push(L('NIC redundancy', 'NIC 이중화'));
    if (noFailover) legs.push(L('failover target', '페일오버 대상'));
    const degraded = legs.length > 0;
    return {
      name: v.name, ip: v.ip || '', node: String(v.node || ''),
      ft: fr === 'ft' ? 'FT' : (fr === 'ha' ? 'HA' : ''),
      // G25(판정 minor): HA 는 상태가 아니라 '보호 방식' 범주 — amber(저하 전용색) 오용을 끊고
      //     정보 남색(info)으로 분리해 stopped 옆에서 경고로 오독되지 않게 한다.
      //     단, 보호가 실제로 깨졌으면 방식과 무관하게 neg 로 떨어뜨린다.
      ftTone: degraded ? 'neg' : (fr === 'ft' ? 'pos' : (fr === 'ha' ? 'info' : 'mut')),
      ftTitle: degraded
        ? L('Protection degraded — ' + legs.join(', ') + ' lost',
          '보호 저하 — ' + legs.join(' · ') + ' 상실')
        : (fr === 'ft' ? L('Fault-tolerant · lockstep, zero-downtime', '무정지 FT · 락스텝 무중단')
          : (fr === 'ha' ? L('High availability · auto-restart on peer', '고가용성 HA · 피어 자동 재기동') : '')),
      // 화면이 배지 하나로 뭉개지 않게 개별 다리 상태도 넘긴다.
      protDegraded: degraded,
      protLegs: legs,
      diskMirrored: v.diskMirrored !== false,
      nicRedundant: v.nicRedundant !== false,
      // 후보 노드 수 — 1이면 페일오버 불가. 0(필드 없음)은 판정 대상이 아니다.
      failoverCandidates: cand.length,
      placedOn: _arr(v.placedOn).join(', '),
      // D3: 열 단위 통일 — CPU/MEM 모두 '할당 절대값'(vCPU·GB)로 전 행 일관.
      //     할당은 실행/정지 무관 전 VM 이 보유하지만 게스트 사용률(guest.cpuPct/memPct)은 일부만 있어
      //     한 열에 '16 vCPU'(할당)와 '67%'(사용률)가 섞여 축이 어긋났다 → 항상 할당값으로 통일.
      cpu: v.cpus ? (v.cpus + ' vCPU') : DASH,
      mem: v.memory || DASH,
      winrm: !!g, os: g ? String(g.os || '').replace('Microsoft ', '').trim() : '',
      guestHost: g ? String(g.host || '') : '',
      st: v.state === 'running' ? L('Running', '실행 중') : (v.state || DASH),
      stTone: v.state === 'running' ? 'pos' : 'mut',
      running: v.state === 'running',
    };
  });

  const nasVms = isNAS ? _arr(m.vmList).map((v) => {
    const running = v.state === 'running';
    return {
      name: v.name || 'VM', ip: v.ip || '', vcpu: v.vcpu != null ? v.vcpu : null,
      diskGB: v.diskMB ? Math.round(v.diskMB / 1024) : null,
      stLabel: v.state ? (running ? L('Running', '실행 중') : (v.state === 'shutdown' ? L('Stopped', '정지') : v.state)) : DASH,
      stTone: running ? 'pos' : 'mut',
    };
  }) : [];

  const simplex = dev.sync !== 'sync';
  const sy = syncInfo(dev, L);

  // 상단 상태 뱃지 사유(minor#15) — '저하'/'오프라인' 뱃지 옆에 근거를 병기해, 본문의 올그린
  // 이중화 패널과의 인지 충돌을 없앤다(심각 경보 수 > 메모리 임계 > 경고 수 순).
  let statusReason = '';
  if (!maint && dev.status !== 'op') {
    const ac = (m.alertCounts && typeof m.alertCounts === 'object') ? m.alertCounts : {};
    const critN = Number(ac.critical || ac.crit || 0);
    const warnN = Number(ac.warning || ac.warn || 0);
    const memP = (memU && !memU.na) ? memU.val : null;
    // G25(판정 minor): FT 는 무정지 배너 본문이 무응답 사유를 이미 진술 — 헤더칩 중복 제거.
    //     비FT(배너 없음)만 사유칩 유지.
    if (isDown) statusReason = ft ? '' : L('not responding', '응답 없음');
    else if (critN) statusReason = L(critN + (critN === 1 ? ' critical alert' : ' critical alerts'), '심각 경보 ' + critN);
    else if (memP != null && memP >= 78) statusReason = L('memory ' + memP + '%', '메모리 ' + memP + '%');
    else if (warnN) statusReason = L(warnN + (warnN === 1 ? ' warning' : ' warnings'), '경고 ' + warnN);
  }

  return {
    id: dev.id, host: m.label || dev.host, hostRaw: dev.host,
    type: dev.type, typeLabel: ti.label, typeShort: ti.short, typeIcon: ti.icon,
    variant, isFT: ft, isServer, isSRV, isPLC, isPC, isNAS, isWIN, isPI, isDown,
    statusReason,
    kind: isPLC ? ([_plc.maker, _plc.model].filter(Boolean).join(' · ') || L('Controller', '제어기'))
      : (isPI ? (_pi.model || 'Raspberry Pi')
        : (isWIN ? (_win.os || L('Windows Server', 'Windows 서버'))
          : (isNAS ? ['Synology', _nas.model].filter(Boolean).join(' · ')
            : (isPC ? (clV(_pc.macVendor || vendor) || L('PC / Workstation', 'PC / 워크스테이션'))
              : (isSRV ? (vendor || L('General server', '일반 서버')) : ti.kind))))),
    pairTitle: isPLC ? L('Controller (PLC)', '제어기 (PLC)')
      : (isPI ? 'Raspberry Pi'
        : (isWIN ? L('Windows Server', 'Windows 서버')
          : (isNAS ? L('Storage', '스토리지')
            : (isPC ? 'PC' : (isSRV ? L('Server', '서버') : L('Fault-tolerant pair', '무정지 페어')))))),
    site: dev.site || DASH,
    siteText: m.mgmt ? ((dev.site || DASH) + ' · ' + m.mgmt) : (dev.site || DASH),
    mgmt: m.mgmt || '', assetTag: String(m.assetTag || ''),
    company: m.company || '', factory: m.factory || '',
    status: dev.status, maint,
    statusLabel: maint ? L('Maintenance', '점검 중') : statusLabel(dev.status, L),
    statusTone: maint ? 'warn' : statusTone(dev.status),
    avail: fmtAvailN(dev.availN), availN: dev.availN,
    uptime: (isPLC || dev.uptime < 0) ? DASH : fmtUptimeD(dev.uptime),
    tiles, notices, nodes, hw, hwLine, pcRows,
    license, resource, control, alertsList, trapsList,
    procs, procTitle: L('Virtual machines', '가상 머신'),
    procCountLabel: ko ? ((m.vmRunning || 0) + '개 실행 중') : ((m.vmRunning || 0) + ' running'),
    nasVms,
    eac: dev.type === 'EDGE' ? { ip: m.mgmt || '', up: !m.error } : null,
    endurance: isEND ? (m.endurance || null) : null,
    bmc: m.bmc ? {
      ip: m.bmc.ip, up: !!m.bmc.up, url: 'https://' + m.bmc.ip,
      name: (function (v) {
        v = String(v || '').toLowerCase();
        return /hpe|hp\b|proliant/.test(v) ? 'iLO'
          : (/dell|idrac|poweredge/.test(v) ? 'iDRAC'
            : (/lenovo|xclarity|xcc|imm|thinksystem/.test(v) ? 'XCC'
              : (/supermicro/.test(v) ? 'IPMI'
                : (/cisco|cimc|ucs/.test(v) ? 'CIMC'
                  : (/fujitsu|irmc/.test(v) ? 'iRMC' : 'BMC')))));
      })(vendor),
    } : null,
    syncLabel: isEND ? (isDown ? L('Offline', '오프라인') : (dev.sync === 'simplex' ? L('Simplex', '심플렉스') : L('Protected', '보호됨')))
      : ((isServer || isPLC) ? (isDown ? L('Offline', '오프라인') : L('Online', '온라인'))
        : (dev.sync === 'sync' ? L('Mirrored', '미러링') : (dev.sync === 'simplex' ? L('Simplex', '심플렉스') : L('Offline', '오프라인')))),
    syncTone: ((isServer || isPLC) ? !isDown : dev.sync === 'sync') ? 'pos' : (dev.sync === 'simplex' ? 'warn' : 'neg'),
    syncIcon: sy.icon,
    ftTitle: isEND ? (simplex ? L('Fault tolerance degraded', '무정지 상태 저하') : L('Active/Standby protection', 'Active/Standby 보호 활성'))
      : (isPLC ? (isDown ? L('PLC offline', 'PLC 오프라인') : (run ? ('PLC · ' + (run === 'PROGRAM' ? L('STOP', '정지') : run)) : L('PLC online', 'PLC 온라인')))
      : (isPC ? (isDown ? L('PC offline', 'PC 오프라인') : (_pc.snmp ? L('SNMP monitored', 'SNMP 모니터링') : L('Agentless monitoring', '에이전트리스 모니터링')))
        : (isSRV ? (m.platform === 'proxmox' ? L('PVE API monitored', 'PVE API 모니터링') : L('SNMP monitored', 'SNMP 모니터링'))
          : (simplex ? L('Fault tolerance degraded', '무정지 상태 저하') : L('Fault tolerance active', '무정지 보호 활성'))))),
    ftSub: isPLC
      ? (isDown ? L('Control port not responding (EtherNet/IP 44818 · FINS 9600)', '제어 포트 미응답 (EtherNet/IP 44818 · FINS 9600)')
        // G25(판정 minor): 실행상태·프로토콜·이벤트는 히어로 타일이 이미 말한다(순수 recap 제거).
        //     여기엔 히어로에 없는 링크 속도·듀플렉스만 남긴다.
        : (linkTxt || L('Control link up', '제어 링크 정상')))
      : (isPC ? (isDown ? L('PC not responding to ping', 'PC 핑 미응답')
        : (_pc.snmp ? L('Reachable · CPU/memory via SNMP', '응답 · SNMP로 CPU·메모리 수집')
          : L('Reachable · agentless (ping · NetBIOS · MAC). Enable SNMP for CPU/memory.', '응답 · 에이전트리스 (핑·NetBIOS·MAC). CPU·메모리는 SNMP 필요.')))
        : (isSRV ? (m.platform === 'proxmox'
          ? L('Proxmox hypervisor — status, CPU, memory and VMs via PVE API', 'Proxmox 하이퍼바이저 — PVE API로 상태·CPU·메모리·VM 수집')
          : L('General server — status, CPU and memory via SNMP', '일반 서버 — SNMP로 상태·CPU·메모리 수집'))
          : (isEND ? L('Smart Exchange — service continuity without reboot', 'Smart Exchange — 재부팅 없는 서비스 연속성')
            : (simplex ? L('Running on a single node — redundancy lost until the peer recovers', '단일 노드로 동작 중 — 피어 복구 전까지 이중화 상실')
              : L('Both nodes mirrored — automatic failover ready', '두 노드 이중화 미러링 — 자동 페일오버 준비'))))),
    // FT/보호상실 톤: 심플렉스(한 노드 단독)는 앰버(회복 가능·경고), 순수 red 는 오프라인/무응답
    // 실장애 전용으로 예약(topology 정합, minor#7). 정상·SRV/PLC 는 pos.
    ftTone: isDown ? 'neg' : (dev.sync === 'simplex' ? 'warn' : 'pos'),
    cpuHist: cpuU.na ? [] : hc,
    memHist: memU.na ? [] : hm,
    rttHist, rttHi,
    cpu: cpuU, mem: memU,
    cpuNow: cpuU.text, memNow: memU.text,
    cpuPeak: cpuU.na ? DASH : String(hc.length ? Math.round(Math.max.apply(null, hc)) : cpuU.val) + '%',
    memPeak: memU.na ? DASH : String(hm.length ? Math.round(Math.max.apply(null, hm)) : memU.val) + '%',
    axis: ['', '', '', '', '', L('now', '현재')],
    switchovers: isEND ? [] : [
      {
        label: L('Last node failover', '마지막 노드 전환'), icon: 'cycle',
        ts: m.lastNodeSwitch ? m.lastNodeSwitch.ts : null,
        flow: flowOf(m.lastNodeSwitch), detail: m.lastNodeSwitch ? m.lastNodeSwitch.desc : '',
        none: L('No switchover on record', '전환 기록 없음'),
      },
      {
        label: L('Last VM failover', '마지막 VM 전환'), icon: 'cycle',
        ts: m.lastVmSwitch ? m.lastVmSwitch.ts : null,
        flow: flowOf(m.lastVmSwitch), detail: m.lastVmSwitch ? (m.lastVmSwitch.vm || m.lastVmSwitch.desc) : '',
        none: L('No switchover on record', '전환 기록 없음'),
      },
    ],
    plcMain, plcComm, plcClock, plcDiag, plcSd, plcNet, plcVars,
    pi: isPI ? _pi : null, win: isWIN ? _win : null, nas: isNAS ? _nas : null, pc: isPC ? _pc : null,
    // printer 키는 이 리터럴에 딱 하나여야 한다 — 예전엔 위쪽에 PC 분기 키가 한 번 더
    // 있어 뒤쪽(m.printer)이 이기면서 meta.pc.printer 경로가 사장됐다(detail.js 는
    // variant 'srv' + d.printer 로 PrinterCard 를 그린다 — PC 도 'srv').
    printer: isPC ? (_pc.printer || null) : (m.printer || null),
  };
}

export function flowOf(o) {
  if (!o) return '';
  if (o.from && o.to) return o.from + ' → ' + o.to;
  if (o.to) return '→ ' + o.to;
  if (o.from) return o.from + ' →';
  return '';
}

