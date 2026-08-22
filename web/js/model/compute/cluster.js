// js/model/compute/cluster.js — 클러스터/용량/트리/검색 모델
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
import { sortRows } from './node.js';
import {
  alertAckKey, autoAckDue, toCsv, activeMaint,
  escalDue, expiredMaint, collectAlerts, collectTraps, alertMsgKo,
} from './alert.js';
import { buildModel } from './kpi.js';

/* ===========================================================================
 * 8. clusters / capacity / manage-tree / search
 * ======================================================================== */

function buildClusterRows(rows, fleet, S, L, ko) {
  const filter = S.clustersFilter || 'all';
  const ftRows = rows.filter((r) => r.isFT);
  const list = (filter === 'all' ? ftRows : ftRows.filter((r) => r.status === filter)).map((r) => {
    const dev = fleet.find((s) => s.id === r.id);
    const m = _meta(dev);
    const lic = m.license;
    let licTxt = DASH;
    let lTone = 'mut';
    if (lic) {
      if (lic.expires) {
        const d = lic.expire ? parseLicDate(lic.expire) : null;
        if (d) {
          const days = Math.round((d.getTime() - Date.now()) / 86400000);
          licTxt = ddayText(days, L, ko);
          lTone = licTone(days);
        } else { licTxt = L('Unknown', '미상'); lTone = 'mut'; }   // #593: 만료일 결측·파싱 불가는 영구가 아니라 미상
      } else { licTxt = L('Perpetual', '영구'); lTone = 'mut'; }
    }
    return Object.assign({}, r, {
      nodeCount: _arr(m.nodes).length,
      vmText: (m.vmRunning || 0) + ' / ' + (m.vms || 0),
      licTxt, licTone: lTone,
    });
  });
  const counts = {
    all: ftRows.length,
    op: ftRows.filter((r) => r.status === 'op').length,
    deg: ftRows.filter((r) => r.status === 'deg').length,
    down: ftRows.filter((r) => r.status === 'down').length,
  };
  const filters = [['all', L('All', '전체')], ['op', L('Healthy', '정상')], ['deg', L('Degraded', '저하')], ['down', L('Offline', '오프라인')]]
    .map((f) => ({ key: f[0], label: f[1], count: counts[f[0]], active: filter === f[0] }));
  return { list, filters, counts, total: ftRows.length };
}

function buildCapacityModel(rows, fleet, resAgg, L, ko) {
  const usable = rows.filter((r) => !r.noTel);
  const cpuRank = usable.filter((r) => !r.cpu.na).sort((a, b) => b.cpuVal - a.cpuVal)
    .map((r) => ({ id: r.id, host: r.label, typeLabel: r.typeLabel, typeIcon: r.typeIcon, val: r.cpuVal, text: r.cpuText, width: r.cpu.width, tone: r.cpu.tone }));
  const memRank = usable.filter((r) => !r.mem.na).sort((a, b) => b.memVal - a.memVal)
    .map((r) => ({ id: r.id, host: r.label, typeLabel: r.typeLabel, typeIcon: r.typeIcon, val: r.memVal, text: r.memText, width: r.mem.width, tone: r.mem.tone }));

  const headroom = [];
  const vmByNode = [];
  fleet.forEach((s) => {
    const m = _meta(s);
    const u = m.unit;
    if (u && (u.totVcpu || u.totMem)) {
      const vp = u.totVcpu ? Math.round((u.usedVcpu || 0) / u.totVcpu * 100) : 0;
      const mp = u.totMem ? Math.round((u.usedMem || 0) / u.totMem * 100) : 0;
      headroom.push({
        id: s.id, host: m.label || s.host, typeLabel: (TYPES[s.type] || {}).label || s.type,
        vcpuUsed: Math.round(u.usedVcpu || 0), vcpuTot: Math.round(u.totVcpu || 0), vcpuPct: vp,
        vcpuWidth: vp + '%', vcpuTone: pctTone(vp),
        memUsed: Number((u.usedMem || 0).toFixed(1)), memTot: Number((u.totMem || 0).toFixed(1)), memPct: mp,
        memWidth: mp + '%', memTone: pctTone(mp),
        freeVcpu: Math.max(0, Math.round((u.totVcpu || 0) - (u.usedVcpu || 0))),
        freeMem: Number(Math.max(0, (u.totMem || 0) - (u.usedMem || 0)).toFixed(1)),
      });
    }
    const per = Object.create(null);
    _arr(m.vmList).forEach((v) => {
      const n = String(v.node || '');
      if (!n) return;
      // 노드 원문에 장비 접두사가 포함돼도 부제는 node{N}으로 정규화한다.
      // 그룹핑 키에는 원문을 유지해 서로 다른 노드의 표시명이 같아도 행이 소실되지 않게 한다.
      const nShort = n.replace(/^.*?-(node\d+)$/i, '$1');
      const cur = per[n] || (per[n] = { node: nShort, nodeKey: n, host: m.label || s.host, id: s.id, total: 0, running: 0 });
      cur.total++;
      if (v.state === 'running') cur.running++;
    });
    Object.keys(per).sort(cmpKo).forEach((k) => vmByNode.push(per[k]));
  });
  // tight 판정(아래 vcpuPct >= 78 || memPct >= 78)과 같은 기준으로 정렬 —
  // vCPU 결측(0)·MEM 만 포화인 클러스터가 리스트 최하단에 묻히지 않게.
  headroom.sort((a, b) => Math.max(b.vcpuPct, b.memPct) - Math.max(a.vcpuPct, a.memPct));
  // 바 길이는 사용률(running/total)로 인코딩 — 앱의 다른 CPU/MEM 바와 읽는 방향 일치.
  // (예전엔 총 슬롯수/최댓값이라 '0/2'가 꽉 찬 바, 100% 사용 '1/1'이 반쪽으로 오독됐다.)
  vmByNode.forEach((v) => { v.width = (v.total ? Math.round(v.running / v.total * 100) : 0) + '%'; });
  vmByNode.sort((a, b) => b.total - a.total);

  const warnT = usageThresholds().warn;
  return {
    cpuRank, memRank, headroom, vmByNode, resAgg,
    title: L('Capacity', '용량'),
    tight: headroom.filter((h) => h.vcpuPct >= warnT || h.memPct >= warnT).length,
    tightNames: headroom.filter((h) => h.vcpuPct >= warnT || h.memPct >= warnT).map((h) => h.host).filter(Boolean),
  };
}

/** manage 화면용 회사▸공장▸장비 3단 트리. */
function buildTree(rows, S, L) {
  const collapsed = (S && S.manageCollapsed) || {};
  // buildTopo 의 #432 와 같은 계약 — 그룹 키(접힘 상태 'co:'·'fa:'의 재료)는 언어 중립
  // 슬러그로 고정하고, 지역화는 표시 name 에만 적용한다. 리터럴 '미분류'를 키·표시
  // 겸용으로 쓰면 en 모드에서도 한국어로 그룹핑·정렬되고 화면(topo)마다 명칭이 갈린다.
  const UNASSIGNED_CO = '(unassigned)';   // company 결측 그룹 슬러그(표시 문자열 아님)
  const UNASSIGNED_FA = '(no-factory)';   // factory 결측 그룹 슬러그
  const coMap = Object.create(null);
  rows.forEach((r) => {
    const co = r.company || UNASSIGNED_CO;
    const fa = r.factory || UNASSIGNED_FA;
    const c = coMap[co] || (coMap[co] = Object.create(null));
    (c[fa] || (c[fa] = [])).push(r);
  });
  return Object.keys(coMap).sort(cmpKo).map((co) => {
    const facs = Object.keys(coMap[co]).sort(cmpKo).map((fa) => {
      const devs = coMap[co][fa].slice().sort((a, b) => cmpKo(a.label, b.label));
      return {
        key: 'fa:' + co + '/' + fa,
        name: fa === UNASSIGNED_FA ? L('Unassigned', '미지정') : fa,
        count: devs.length,
        collapsed: !!collapsed['fa:' + co + '/' + fa],
        worst: worstOf(devs), devices: devs,
      };
    });
    const all = facs.reduce((a, f) => a.concat(f.devices), []);
    return {
      key: 'co:' + co,
      name: co === UNASSIGNED_CO ? L('Unassigned', '미분류') : co,
      count: all.length,
      collapsed: !!collapsed['co:' + co],
      worst: worstOf(all), factories: facs,
    };
  });
}

export function worstOf(list) {
  if (list.some((r) => (r.status || r) === 'down')) return 'down';
  if (list.some((r) => (r.status || r) === 'deg')) return 'deg';
  return 'op';
}

/** 전역 검색 — 노드 + 경보 혼합. F1 계약: {kind,id,label,meta,status}. */
function buildSearchList(fleet, alertsAll, S, L, ko, today) {
  const q = String((S && S.search) || '').trim().toLowerCase();
  const out = [];
  if (!q) return out;
  fleet.forEach((s) => {
    const m = _meta(s);
    const ti = typeInfo(s.type, L);
    const vmNames = _arr(m.vmList).map((v) => v.name).filter(Boolean);
    // 시리얼·MAC·SNMP 시리얼·운영자 메모도 검색 대상 — 현장에서 장비는 이름보다
    // S/N 이나 MAC 으로 부르는 경우가 많고, 인수인계 메모 속 키워드도 찾아야 한다.
    const subs = [m.printer, m.nas, m.plc, m.win, m.pi, m.pc];
    const noteTxt = (((S || {}).notes || {})[s.id] || {}).text;
    const hay = [s.host, m.label, m.mgmt, m.assetTag, ti.label, s.site, noteTxt]
      .concat(_arr(m.nodes).map((n) => n.name))
      .concat(_arr(m.nodes).map((n) => n.ip))
      .concat(vmNames)
      .concat(subs.map((x) => x && x.serial))
      .concat(subs.map((x) => x && x.mac))
      .concat(_arr(m.snmp).map((x) => x && x.serial))
      .filter(Boolean).join(' ').toLowerCase();
    if (hay.indexOf(q) < 0) return;
    const vmHit = vmNames.find((nm) => String(nm).toLowerCase().indexOf(q) >= 0);
    out.push({
      kind: 'node', id: s.id, icon: ti.icon,
      label: m.label || s.host, title: m.label || s.host,
      meta: [ti.label, m.mgmt, vmHit ? ('VM: ' + vmHit) : null].filter(Boolean).join(' · '),
      sub: [ti.label, m.mgmt, vmHit ? ('VM: ' + vmHit) : null].filter(Boolean).join(' · '),
      status: s.status, tone: statusTone(s.status),
    });
  });
  alertsAll.forEach((a) => {
    if (String(a.msg || '').toLowerCase().indexOf(q) < 0 && String(a.host || '').toLowerCase().indexOf(q) < 0) return;
    out.push({
      kind: 'incident', id: a.hostId, icon: a.sevIcon,
      label: a.msg, title: a.msg,
      meta: [a.host, shortTime(a.time, today)].filter(Boolean).join(' · '),
      sub: [a.host, shortTime(a.time, today)].filter(Boolean).join(' · '),
      status: a.sev, tone: a.sevTone,
    });
  });
  return out.slice(0, 8);
}

/* 개별 호출용 래퍼(화면이 model 전체를 만들지 않고 쓰고 싶을 때). */
export function buildClusters(a, b) {
  const { fleet, state } = _resolve(a, b);
  const m = buildModel(fleet, state);
  return m.clusters;
}
export function buildCapacity(a, b) {
  const { fleet, state } = _resolve(a, b);
  const m = buildModel(fleet, state);
  return m.capacity;
}
export function buildManageTree(a, b) {
  const { fleet, state } = _resolve(a, b);
  const m = buildModel(fleet, state);
  return m.tree;
}
export function buildSearch(a, b) {
  const { fleet, state } = _resolve(a, b);
  const m = buildModel(fleet, state);
  return m.searchResults;
}

