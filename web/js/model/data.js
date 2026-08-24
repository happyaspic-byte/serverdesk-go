import { setUsageThresholds } from '../util/fmt.js';
// js/model/data.js — 실장비 데이터 모델
// ---------------------------------------------------------------------------
// TYPES 레지스트리 + /api/devices 정규화·폴링.
// 서버가 반환한 장비만 화면에 제공하며 클라이언트 자체 샘플·시뮬레이션 데이터는 만들지 않는다.
// ---------------------------------------------------------------------------

/* ===========================================================================
 * 1. 타입 레지스트리
 * ======================================================================== */

// Vigil src/model/data.ts 그대로 이식.
// 스키마: label 은 기본 표기(고유명사는 원어, 일반명사는 한국어), labelEN 은 선택 — 있으면
// 소비처(compute.js typeInfo)가 L(labelEN, label) 로 언어 전환한다(SRV/PRN 만 보유).
export const TYPES = {
  EV:   { label: 'everRun',        short: 'EV',   kindEN: 'Datacenter FT',          kindKO: '데이터센터 FT',     icon: 'ph-stack' },
  EDGE: { label: 'ztC Edge',       short: 'EDGE', kindEN: 'Edge FT',                kindKO: '엣지 FT',          icon: 'ph-cpu' },
  // 레거시 import 표시 전용. 현재 수집기에는 Endurance ingestion/persistence 계약이 없다.
  END:  { label: 'ztC Endurance',  short: 'END',  kindEN: 'Legacy imported record', kindKO: '레거시 가져오기 레코드', icon: 'ph-hard-drives', supported: false },
  FTS:  { label: 'ftServer',       short: 'FTS',  kindEN: 'Fault-tolerant server',  kindKO: '무정지 서버',       icon: 'ph-shield-check' },
  SRV:  { label: '서버', labelEN: 'Server', short: 'SRV',  kindEN: 'General server',         kindKO: '일반 서버',        icon: 'ph-hard-drive' },
  PLC:  { label: 'PLC',            short: 'PLC',  kindEN: 'Controller',             kindKO: '제어기',           icon: 'ph-squares-four' },
  PC:   { label: 'PC',             short: 'PC',   kindEN: 'PC / Workstation',       kindKO: 'PC / 워크스테이션', icon: 'ph-desktop' },
  NAS:  { label: 'NAS',            short: 'NAS',  kindEN: 'Network storage',        kindKO: '네트워크 스토리지', icon: 'ph-hard-drives' },
  WIN:  { label: 'Windows Server', short: 'WIN',  kindEN: 'Windows Server',         kindKO: 'Windows 서버',     icon: 'ph-windows-logo' },
  PI:   { label: 'Raspberry Pi',   short: 'PI',   kindEN: 'Single-board computer',  kindKO: '싱글보드 컴퓨터',   icon: 'ph-raspberry-pi' },
  PRN:  { label: '프린터', labelEN: 'Printer', short: 'PRN', kindEN: 'Network printer', kindKO: '네트워크 프린터', icon: 'ph-printer' },
};

/** FT(무정지) 계열 — 이중화/sync/VM/라이선스 카드를 갖는 타입. */
export const FT_TYPES = { EV: true, EDGE: true, END: true, FTS: true };

export const TYPE_KEYS = Object.keys(TYPES);
export const STATUS_KEYS = ['op', 'deg', 'down'];
export const SYNC_KEYS = ['sync', 'simplex', 'offline'];

export const isFT = (t) => !!FT_TYPES[t];
/** CPU/MEM 텔레메트리가 원천적으로 없는 타입(항상 대시로 표기). */
export const isNoTel = (t) => t === 'PLC' || t === 'PRN';

/* ===========================================================================
 * 2. 실장비 상태 파생
 * ======================================================================== */
export const clamp = (v, a, b) => (v < a ? a : v > b ? b : v);

const _CRIT = /offline|unreachable|failed|fault|lost|cannot|expired|broken|\bdown\b|simplex/i;
const _WARN = /warning|no link|disconnect|maintenance|\bsync|pressure|capacity|temporary|degrade|reboot|unexpected/i;

export function classify(text) {
  const value = String(text || '');
  if (_CRIT.test(value)) return 'critical';
  if (_WARN.test(value)) return 'warning';
  return 'info';
}

/** 노드 상태 배열 + SNMP 노드 배열로 클러스터 상태 도출. */
export function deriveStatus(nodes, snmpNodes) {
  const ns = Array.isArray(nodes) ? nodes : [];
  if (ns.length) {
    const running = ns.filter((n) => n && n.state === 'running').length;
    if (running === 0) return 'down';
    const abnormal = ns.some((n) => n && n.standing && n.standing !== 'normal');
    if (running < ns.length || abnormal) return 'deg';
    return 'op';
  }
  const sn = Array.isArray(snmpNodes) ? snmpNodes : [];
  const reachNodes = sn.filter((n) => n && n.reachable);
  if (reachNodes.length === 0) return 'down';
  if (reachNodes.length < sn.length) return 'deg';
  // 단일/다중 SNMP 노드가 전부 도달 가능하더라도, 노드가 저하(degraded) 플래그를 달고 있으면 deg.
  return reachNodes.some((n) => n.degraded) ? 'deg' : 'op';
}

export function deriveSync(nodes, status) {
  const ns = Array.isArray(nodes) ? nodes : [];
  if (!ns.length) return status === 'op' ? 'sync' : (status === 'deg' ? 'simplex' : 'offline');
  const runningAny = ns.filter((n) => n && n.state === 'running').length;
  // 이중화(sync)는 '정상 대기' 상태의 running 노드만 기여한다. syncing/maintenance 등 비정상 standing
  // 노드는 페일오버 준비가 안 됐으므로 이중화로 세지 않는다(status=deg 인데 sync=sync 로 표기되던 모순 제거).
  const healthy = ns.filter((n) => n && n.state === 'running' && (!n.standing || n.standing === 'normal')).length;
  return runningAny === 0 ? 'offline' : (healthy >= 2 ? 'sync' : 'simplex');
}

/** 상태 → 명목 가용성(실측 SLA 아님, poller.py 와 동일 매핑). */
export function availN(status) {
  return status === 'op' ? 99.99 : (status === 'deg' ? 99.9 : 99.0);
}

/* ===========================================================================
 * 3. /api/devices 폴링 계약
 * ======================================================================== */

/**
 * 실 poller 응답을 내부 스키마로 정규화(Vigil devices.ts::normalize 이식 + meta 보존).
 * @param {Object|Array} apiJson `{devices:[…]}` 또는 device 배열
 * @returns {Array<Object>} device[]
 */
export function normalize(apiJson) {
  const raw = Array.isArray(apiJson) ? apiJson
    : (apiJson && Array.isArray(apiJson.devices) ? apiJson.devices : []);
  return raw.map((device, index) => normalizeDevice(device, index)).filter(Boolean);
}

export function normalizeDevice(d, index = null) {
  if (!d || typeof d !== 'object') return null;
  const host = String(d.host || d.id || '').trim();
  if (!host || TYPE_KEYS.indexOf(d.type) < 0 || STATUS_KEYS.indexOf(d.status) < 0) return null;
  const num = (v, dflt) => { const n = Number(v); return Number.isFinite(n) ? n : dflt; };
  const type = d.type;
  const status = d.status;
  const rawCpu = num(d.cpu0, -1);
  const rawMem = num(d.mem0, -1);
  const meta = (d.meta && typeof d.meta === 'object') ? d.meta : {};
  // PLC 는 원천적으로 텔레메트리가 없다 — 폴러가 0을 보내도 NA 로 고정.
  const noTel = isNoTel(type);
  const cpuNA = noTel || rawCpu < 0;
  const memNA = noTel || rawMem < 0;
  // vmList 는 필터 후 배열을 로컬에 둔다 — meta.vms 생략 시 쓰는 기본값이 필터 전
  // meta.vmList.length 면 null·비객체 요소만큼 총수가 부풀어 개요 VM 집계와
  // 상세 목록이 어긋난다(#606, #586 잔여). 기본값은 필터 후 길이를 써야 완결이다.
  const vmList = Array.isArray(meta.vmList) ? meta.vmList.filter((v) => v && typeof v === 'object') : [];
  return {
    id: host,
    host,
    type,
    site: (d.site != null && String(d.site)) ? String(d.site) : '—',
    status,
    availN: num(d.availN, availN(status)),
    availDays: num(d.availDays, 0),
    cpu0: cpuNA ? -1 : clamp(Math.round(rawCpu), 0, 100),
    mem0: memNA ? -1 : clamp(Math.round(rawMem), 0, 100),
    cpuNA,
    memNA,
    sync: SYNC_KEYS.indexOf(d.sync) >= 0 ? d.sync : 'sync',
    uptime: Math.round(num(d.uptime, 0)),
    updatedAt: num(d.updatedAt, 0),
    live: true,
    meta: {
      label: String(meta.label || host),
      company: String(meta.company || ''),
      factory: String(meta.factory || ''),
      mgmt: meta.mgmt ? String(meta.mgmt) : '',
      version: meta.version ? String(meta.version) : '',
      assetTag: meta.assetTag ? String(meta.assetTag) : '',
      // 플로어 맵 실배치 좌표('행,열') — 정규화 화이트리스트에 없으면 여기서 증발한다.
      floorPos: meta.floorPos ? String(meta.floorPos) : '',
      vendor: meta.vendor ? String(meta.vendor) : '',
      // 서버가 명시적으로 제공한 읽기 전용 샘플 표시. `demo`는 과도기 응답 호환이며
      // 어느 쪽이 와도 두 별칭을 함께 보존해 화면이 실제 장비로 오인하지 않게 한다.
      sample: meta.sample === true || meta.demo === true,
      demo: meta.sample === true || meta.demo === true,
      error: meta.error != null ? meta.error : null,
      pending: !!meta.pending,
      // #398: 폴리가 보는 상태(down/deg) 진입 시각 — 합성 DEVICE_STATE 경보의 세션 무관
      // ack 키 원천(compute.js collectAlerts). 화이트리스트에 없으면 여기서 증발해
      // 프런트가 세션 onset 폴핵으로 떨어지고 리로드 시 확인이 소실된다.
      downSince: meta.downSince ? String(meta.downSince) : '',
      issueSince: meta.issueSince ? String(meta.issueSince) : '',
      unit: meta.unit && typeof meta.unit === 'object' ? meta.unit : {},
      // nodes/snmp/vmList 는 요소별 방어가 없으면 null/비객체 요소가 그대로 남아
      // compute 의 buildModel/buildDetail/buildTopo 가 요소 필드를 읽다 throw 한다(#586).
      // alerts/traps 의 normalizeAlert/normalizeTrap 요소별 정규화와 같은 결로 걸러낸다
      // (정상 요소와 그 순서는 보존).
      nodes: Array.isArray(meta.nodes) ? meta.nodes.filter((n) => n && typeof n === 'object') : [],
      snmp: Array.isArray(meta.snmp) ? meta.snmp.filter((n) => n && typeof n === 'object') : [],
      vmList,
      vms: num(meta.vms, vmList.length),
      // vmRunning 도 #606(vms)과 같은 결 — 폴리가 vmRunning 만 생략하고 vmList 를 납품하면
      // 필터 후 vmList 의 state:'running' 개수로 도출 가능한데 상수 0 이면 개요 VM 집계·
      // 클러스터 행·상세 타일이 전부 '0 / N' 으로 표기돼 상세 목록(실행 중 VM 나열)과
      // 어긋난다(#617, #606 잔여). 기본값은 필터 후 vmList 의 running 카운트를 쓴다.
      vmRunning: num(meta.vmRunning, vmList.filter((v) => v.state === 'running').length),
      // alerts/traps 도 #586(nodes/snmp/vmList)과 같은 결로 null·비객체 요소를 걸러낸다 —
      // 두지 않으면 normalizeAlert/normalizeTrap 이 null 을 '빈 desc 의 info 경보'/
      // 'desc=SNMP trap 트랩' 유령 객체로 둔갑시켜 카운트·목록에 유입된다(#592).
      alerts: Array.isArray(meta.alerts) ? meta.alerts.slice(0, 25).filter((a) => a && typeof a === 'object').map(normalizeAlert) : [],
      traps: Array.isArray(meta.traps) ? meta.traps.slice(0, 40).filter((t) => t && typeof t === 'object').map(normalizeTrap) : [],
      license: meta.license || null,
      lastVmSwitch: meta.lastVmSwitch || null,
      lastNodeSwitch: meta.lastNodeSwitch || null,
      lastReboot: meta.lastReboot || null,
      bmc: meta.bmc || null,
      // events 도 #586(nodes/snmp/vmList)과 같은 결로 null·비객체 요소를 걸러낸다 —
      // 두지 않으면 compute 의 이벤트 소비(e.kind 읽기)가 null 에서 throw 한다(#594).
      events: Array.isArray(meta.events) ? meta.events.filter((e) => e && typeof e === 'object') : [],
      // 실 폴러(everrun-poller /api/devices) 전용 확장 —
      // topo: 토폴로지 실관계(네트워크/스토리지 미러). buildTopo 가 있으면 우선 쓴다.
      // storage 배열 요소도 #586(nodes/snmp/vmList)과 같은 결로 null·비객체를 걸러낸다 —
      // 두지 않으면 compute 의 스토리지그룹 집계(g.pct 읽기)가 null 에서 throw 한다(#604).
      topo: _normTopo(meta.topo),
      platform: meta.platform ? String(meta.platform) : '',
      healthLevel: meta.healthLevel ? String(meta.healthLevel) : '',
      healthReasons: Array.isArray(meta.healthReasons) ? meta.healthReasons : [],
      alertCounts: (meta.alertCounts && typeof meta.alertCounts === 'object') ? meta.alertCounts : {},
      collection: (meta.collection && typeof meta.collection === 'object') ? meta.collection : null,
      stale: !!meta.stale,
      uuid: meta.uuid ? String(meta.uuid) : '',
      plc: _normPlc(meta.plc) || undefined,
      // Proxmox 등 일반 서버(SRV) 확장 — 폴러 poll_proxmox 산출. 화이트리스트에
      // 없으면 여기서 증발하므로(§floorPos 주석 참조) 반드시 명시한다.
      srv: (meta.srv && typeof meta.srv === 'object') ? meta.srv : undefined,
      // srvNet/srvDisks/srvStorage 도 같은 결로 걸러낸다 — null 요소가 그대로 통과하면
      // buildDetail 이 dk.health/nt.kind/sp.name 을 읽다 throw 한다(#594, #586 동종).
      srvNet: Array.isArray(meta.srvNet) ? meta.srvNet.filter((n) => n && typeof n === 'object') : [],
      srvDisks: Array.isArray(meta.srvDisks) ? meta.srvDisks.filter((d) => d && typeof d === 'object') : [],
      srvStorage: Array.isArray(meta.srvStorage) ? meta.srvStorage.filter((s) => s && typeof s === 'object') : [],
      srvThermal: (meta.srvThermal && typeof meta.srvThermal === 'object') ? meta.srvThermal : null,
      pc: meta.pc || undefined,
      nas: meta.nas || undefined,
      win: meta.win || undefined,
      pi: meta.pi || undefined,
      printer: meta.printer || undefined,
      // ztC Endurance 세부 이중화 구조(CM-A/B 역할·IP 플랜·모듈 이중화) — 토폴로지
      // 단일 서버 카드의 END 행 재료. 화이트리스트에 없으면 여기서 증발한다.
      endurance: (meta.endurance && typeof meta.endurance === 'object') ? meta.endurance : undefined,
    },
    // 폴러가 노드 링버퍼에서 만든 실측 이력. compute.histOf 가 state.hist 가 비면
    // 이 배열로 폴백하므로, 실 모드에서도 스파크라인이 그려진다.
    histCpu: _numArr(d.histCpu),
    histMem: _numArr(d.histMem),
    histRtt: _numArr(d.histRtt),
  };
}

/** 숫자 배열만 남기고 최근 48개로 자른다(폴러 이력 → 스파크라인). */
/** plc 중첩 배열(sysDiag.modules/procVars/errHistory)의 null·비객체 요소를 걸러낸다(#605). */
function _normPlc(plc) {
  if (!plc || typeof plc !== 'object') return plc;
  const out = Object.assign({}, plc);
  if (plc.sysDiag && typeof plc.sysDiag === 'object' && Array.isArray(plc.sysDiag.modules)) {
    out.sysDiag = Object.assign({}, plc.sysDiag, {
      modules: plc.sysDiag.modules.filter((m) => m && typeof m === 'object'),
    });
  }
  if (Array.isArray(plc.procVars)) out.procVars = plc.procVars.filter((v) => v && typeof v === 'object');
  if (Array.isArray(plc.errHistory)) out.errHistory = plc.errHistory.filter((h) => h && typeof h === 'object');
  return out;
}

/** topo 객체는 통째 패스스루하되 storage 배열의 null·비객체 요소만 걸러낸다(#604). */
function _normTopo(topo) {
  if (!topo || typeof topo !== 'object') return null;
  if (!Array.isArray(topo.storage)) return topo;
  return Object.assign({}, topo, { storage: topo.storage.filter((g) => g && typeof g === 'object') });
}

function _numArr(v) {
  if (!Array.isArray(v)) return [];
  const out = [];
  for (let i = 0; i < v.length; i++) {
    const n = Number(v[i]);
    if (Number.isFinite(n)) out.push(n);
  }
  return out.slice(-48);
}

function normalizeAlert(a) {
  const desc = String((a && (a.desc || a.message || a.name)) || '');
  return {
    name: String((a && a.name) || ''),
    desc,
    time: String((a && a.time) || ''),
    severity: String((a && a.severity) || ''),
    sev: (a && a.sev) || classify(((a && a.name) || '') + ' ' + desc),
  };
}

function normalizeTrap(t) {
  const desc = String((t && (t.desc || t.oid)) || 'SNMP trap');
  return {
    desc,
    oid: String((t && t.oid) || ''),
    time: String((t && t.time) || ''),
    src: String((t && t.src) || ''),
    sev: (t && t.sev) || classify(desc),
  };
}

/**
 * 폴러 엔드포인트. everrun-poller 는 클러스터 원본을 `/api/fleet`(clusters[]) 로,
 * 이 프런트가 먹는 평면 device[] 를 `/api/devices` 로 낸다(= /api/fleet?format=devices).
 * serve.py 가 6001 에서 `/api/*` 를 폴러(9890)로 중계한다.
 */
export const API_URL = '/api/devices';

const CLUSTER_ACTIONS_DEFAULT_REASON = 'Server did not advertise cluster action support.';
const CLUSTER_ACTIONS_DEFAULT_REASON_KO = '서버가 클러스터 제어 지원을 알리지 않았습니다.';

/**
 * 서버 capability 응답을 fail-closed 형태로 정규화한다.
 * supported=true 뿐 아니라 개별 action allowlist에도 있어야 실행 가능하다.
 */
export function normalizeCapabilities(raw) {
  const root = raw && typeof raw === 'object' ? raw : {};
  const src = root.cluster_actions && typeof root.cluster_actions === 'object'
    ? root.cluster_actions : {};
  const actions = Array.isArray(src.actions)
    ? Array.from(new Set(src.actions.filter((v) => typeof v === 'string' && v.trim()).map((v) => v.trim())))
    : [];
  const supported = src.supported === true;
  return {
    cluster_actions: {
      supported,
      // 빈 allowlist를 "모두 허용"으로 해석하지 않는다. 광고되지 않은 action은 항상 차단한다.
      actions: supported ? actions : [],
      endpoint: typeof src.endpoint === 'string' ? src.endpoint : '/api/clusters/{id}/action',
      reason: typeof src.reason === 'string' && src.reason.trim()
        ? src.reason.trim() : CLUSTER_ACTIONS_DEFAULT_REASON,
      reason_ko: typeof src.reason_ko === 'string' && src.reason_ko.trim()
        ? src.reason_ko.trim() : CLUSTER_ACTIONS_DEFAULT_REASON_KO,
    },
  };
}

/** 특정 cluster action의 실행 가능 여부. 누락·오염·부분 광고 모두 기본 거절한다. */
export function clusterActionAvailability(capabilities, action) {
  const cap = normalizeCapabilities(capabilities).cluster_actions;
  const key = String(action || '');
  if (!cap.supported) {
    return { supported: false, reason: cap.reason, reason_ko: cap.reason_ko };
  }
  if (!cap.actions.includes(key)) {
    return {
      supported: false,
      reason: 'Server did not advertise support for action: ' + key,
      reason_ko: '서버가 이 제어 작업의 지원을 알리지 않았습니다: ' + key,
    };
  }
  return { supported: true, reason: '', reason_ko: '' };
}

function redirectToLogin() {
  if (typeof window === 'undefined' || !window.location) return;
  const next = window.location.pathname + window.location.search + window.location.hash;
  window.location.replace('/login?next=' + encodeURIComponent(next));
}

/**
 * device[] 폴링. 장비가 없는 빈 배열도 정상적인 기초 상태다.
 * @param {string} [url='/api/devices']
 * @param {number} [timeoutMs=2500]
 * @returns {Promise<{ok:boolean, devices:Array, error:(string|null), refreshSec:number, polledAt:number, stale:boolean}>}
 */
export async function pull(url, timeoutMs) {
  const target = url || API_URL;
  const ms = typeof timeoutMs === 'number' ? timeoutMs : 2500;
  const fail = (msg) => ({
    ok: false, devices: [], error: msg, refreshSec: 30, polledAt: 0, stale: false,
    source: 'live', sampleMode: false, demoMode: false,
  });

  if (typeof fetch !== 'function') return fail('fetch unavailable');

  let ctrl = null;
  let timer = null;
  try {
    if (typeof AbortController === 'function') {
      ctrl = new AbortController();
      timer = setTimeout(() => { try { ctrl.abort(); } catch (_) { /* noop */ } }, ms);
    }
    const opt = { cache: 'no-store' };
    if (ctrl) opt.signal = ctrl.signal;
    const r = await fetch(target, opt);
    if (r && r.status === 401) {
      redirectToLogin();
      return fail('authentication required');
    }
    if (!r || !r.ok) return fail('HTTP ' + (r ? r.status : '?'));
    const j = await r.json();
    const responseSource = String((j && j.source) || '').toLowerCase();
    const sampleMode = !!(j && (j.sample === true || j.demo === true
      || responseSource === 'sample' || responseSource === 'demo'));
    const devices = normalize(j).map((device) => {
      if (!sampleMode) return device;
      return Object.assign({}, device, {
        meta: Object.assign({}, device.meta, { sample: true, demo: true }),
      });
    });
    return {
      ok: true,
      devices,
      source: sampleMode ? 'sample' : 'live',
      sampleMode,
      demoMode: sampleMode,
      // 이벤트 이력 — 문자열 필드만 통과(폴러 EventLog 계약: ts/host/label/kind/sev/desc).
      events: Array.isArray(j && j.events) ? j.events.slice(0, 200).map((e) => ({
        time: String((e && e.ts) || ''), host: String((e && e.host) || ''),
        label: String((e && e.label) || ''), kind: String((e && e.kind) || ''),
        sev: String((e && e.sev) || 'info'), desc: String((e && e.desc) || ''),
      })) : [],
      error: null,
      refreshSec: (j && typeof j.refreshSec === 'number') ? j.refreshSec : 30,
      polledAt: Date.now(),
      // 폴러 자신이 "수집이 밀렸다"고 보고하면 HTTP 가 200 이어도 stale 로 다룬다.
      stale: !!(j && j.stale),
      // 폴러가 계산한 플릿 총평(ok|warning|critical). 이전 정규화에서 통째로 버려져
      // UI 가 백엔드의 판정을 전혀 몰랐다(감사 P0) — 화이트리스트로 통과시킨다.
      overall: (j && ['ok', 'warning', 'critical'].includes(j.overall)) ? j.overall : null,
      // 사용률 임계값 — 서버(config.thresholds) 정본. pullPatch 가 라이브 반영한다.
      thresholds: (j && j.thresholds && typeof j.thresholds.warn === 'number' && typeof j.thresholds.crit === 'number')
        ? { warn: j.thresholds.warn, crit: j.thresholds.crit } : null,
      // 변경 기능은 서버 광고만 신뢰한다. 필드가 없는 구버전 서버는 안전하게 미지원 처리.
      capabilities: normalizeCapabilities(j && j.capabilities),
      // 폴러가 실장비에서 읽은 뒤 흐른 시간. 클라이언트 수신 시각(lastPoll)과 다르다 —
      // 수집이 밀리면 '방금 받았지만 값은 몇 분 낡은' 상태가 되고, lastPoll 만 보면 그걸 못 본다.
      cacheAgeSec: (j && typeof j.cache_age_secs === 'number' && j.cache_age_secs >= 0)
        ? j.cache_age_secs : null,
    };
  } catch (e) {
    return fail((e && (e.name === 'AbortError' ? 'timeout' : e.message)) || 'network error');
  } finally {
    if (timer) clearTimeout(timer);
  }
}


/* ---- 실 모드: 관리 화면의 표시 메타 수정 보존 머지 ----
   PUT 직후 폴러가 수정값을 반영하기 전까지 meta.localEdit 필드를 서버 스냅샷에
   다시 적용한다. 서버가 같은 값을 반환하기 시작하면 마커를 제거한다. */
const LOCAL_EDIT_FIELDS = ['label', 'company', 'factory', 'mgmt', 'assetTag', 'floorPos', 'vendor'];

function mergeLocalWrites(localFleet, serverDevices) {
  const local = Array.isArray(localFleet) ? localFleet : [];
  const server = Array.isArray(serverDevices) ? serverDevices : [];
  if (!local.length) return server;
  const localById = new Map();
  for (const d of local) {
    if (d && d.id != null && !localById.has(d.id)) localById.set(d.id, d);
  }
  const merged = server.map((srv) => {
    if (!srv || srv.id == null) return srv;
    const ld = localById.get(srv.id);
    const edit = ld && ld.meta && ld.meta.localEdit;
    if (!edit || typeof edit !== 'object') return srv;
    const m = srv.meta || {};
    // 폴리가 PUT 을 반영해 같은 값을 내리기 시작하면 오버라이드 종료.
    const reflected = String(srv.site || '') === String(edit.site || '')
      && LOCAL_EDIT_FIELDS.every((k) => String(m[k] || '') === String(edit[k] || ''))
      && (!edit.bmcIp || !!(m.bmc && String(m.bmc.ip || '') === String(edit.bmcIp)));
    if (reflected) return srv;
    const dev = Object.assign({}, srv, { site: edit.site || srv.site });
    const meta = Object.assign({}, m);
    for (const k of LOCAL_EDIT_FIELDS) meta[k] = String(edit[k] || '');
    if (edit.bmcIp && meta.bmc) meta.bmc = Object.assign({}, meta.bmc, { ip: String(edit.bmcIp) });
    meta.localEdit = edit;
    dev.meta = meta;
    return dev;
  });
  return merged;
}

/**
 * 스펙 §4.4 의 state 패치 형태로 감싼 헬퍼. app.js 가 그대로 setState 에 넘길 수 있다.
 */
export async function pullPatch(state, url, timeoutMs) {
  const st = state || {};
  const r = await pull(url, timeoutMs);

  if (r.ok) {
    if (r.thresholds) setUsageThresholds(r.thresholds.warn, r.thresholds.crit);
    return {
      // 샘플 응답에는 운영 장비의 로컬 보류 수정을 절대 합치지 않는다.
      fleet: r.sampleMode ? r.devices : mergeLocalWrites(st.fleet, r.devices),
      source: r.source,
      sampleMode: !!r.sampleMode,
      demoMode: !!r.demoMode,
      refreshSec: r.refreshSec,
      lastPoll: r.polledAt,
      lastAttempt: Date.now(),
      pollPending: false,
      // 직전 실패의 stale=true를 정상 응답에서 반드시 지운다. 폴러 자체가 캐시
      // 지연을 보고한 성공 응답만 stale로 유지한다.
      stale: !!r.stale,
      // 이벤트 이력(폴러 events[]) — 로그 화면 tail 의 정본. 활성 경보 스냅샷과 달리
      // 해소된 경보·상태 전이가 이력으로 남는다.
      liveEventLog: r.events,
      // 폴러 수집이 밀린 상태면 배너를 띄운다(데이터는 그대로 보여 준다).
      liveError: r.stale ? 'poller stale' : null,
      // 백엔드 총평 — compute 가 자체 재도출값과 대조해 더 나쁜 쪽을 택한다.
      pollerOverall: r.overall || null,
      thresholds: r.thresholds || null,
      capabilities: r.capabilities,
      cacheAgeSec: r.cacheAgeSec,
    };
  }

  const wasSample = st.sampleMode === true || st.demoMode === true
    || st.source === 'sample' || st.source === 'demo';
  return {
    // 직전 샘플 플릿을 화면에 유지하는 동안에는 실패 후에도 LIVE로 위장하지 않는다.
    source: wasSample ? 'sample' : 'live',
    sampleMode: wasSample,
    demoMode: wasSample,
    liveError: r.error || 'unreachable',
    stale: true,
    lastAttempt: Date.now(),
    pollPending: false,
  };
}


export default {
  TYPES, FT_TYPES, TYPE_KEYS, STATUS_KEYS, SYNC_KEYS,
  isFT, isNoTel, deriveStatus, deriveSync, availN,
  normalize, normalizeDevice, pull, pullPatch,
};

/* ===========================================================================
 * 경보 확인(ack) 공유 상태 — serve.py 의 GET/PUT /ack
 *
 * 왜 서버에 두나: 폴러는 읽기 전용 원천이라 경보 해제 API 가 없다(GET /api/ack → 404 실측).
 * 확인 상태를 localStorage 에만 두면 운영자 A 가 확인해도 B 는 그대로 '심각 9'를 본다 —
 * 다중 운영자 NOC 에서는 사실상 무용지물이다. 폴러(실장비 폴링 프로세스)는 건드리지 않고
 * 정적 서버가 작은 공유 상태를 들고 있게 한다.
 * 서버가 없거나 실패하면 조용히 localStorage 만으로 동작한다(폐쇄망 단독 실행 호환).
 * ======================================================================== */

const ACK_URL = '/ack';
const MAINT_URL = '/maint';   // 유지보수(점검) 창 공유 상태 — serve.py. ack 와 같은 모양.
const NOTES_URL = '/notes';   // 장비 메모(인수인계) 공유 상태 — serve.py.
const RETRY_DELAY_MS = 400;   // 재전송 전 대기 — 서버가 일시적으로 밀린 경우를 흡수한다.

/** 서버의 공유 상태(ack·maint 공통)를 읽는다. 실패하면 null(=서버 없음, 로컬만 쓴다). */
async function pullState(url, timeoutMs) {
  if (typeof fetch !== 'function') return null;
  let ctrl = null; let timer = null;
  try {
    if (typeof AbortController === 'function') {
      ctrl = new AbortController();
      timer = setTimeout(() => { try { ctrl.abort(); } catch (_) { /* noop */ } }, timeoutMs || 2000);
    }
    const opt = { cache: 'no-store' };
    if (ctrl) opt.signal = ctrl.signal;
    const r = await fetch(url, opt);
    if (!r || !r.ok) return null;
    const j = await r.json();
    return (j && typeof j === 'object' && !Array.isArray(j)) ? j : null;
  } catch (e) {
    return null;
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/** 서버의 확인 상태를 읽는다. 실패하면 null(=서버 없음, 로컬만 쓴다). */
export function pullAck(timeoutMs) { return pullState(ACK_URL, timeoutMs); }

/** 서버의 점검 창 상태를 읽는다. 실패하면 null(=서버 없음, 로컬만 쓴다). */
export function pullMaint(timeoutMs) { return pullState(MAINT_URL, timeoutMs); }

/** 서버의 장비 메모를 읽는다. 실패하면 null(=서버 없음, 로컬만 쓴다). */
export function pullNotes(timeoutMs) { return pullState(NOTES_URL, timeoutMs); }

/**
 * 확인 상태 델타를 서버에 보낸다. 실패해도 throw 하지 않는다(로컬은 이미 저장됨).
 *
 * 왜 전체 맵이 아니라 델타인가(실측 버그): 전체 교체로 보내면 운영자 A 가 확인한 뒤
 * 그보다 먼저 화면을 연 B 의 PUT 이 A 의 확인을 통째로 덮어썼다. 병합은 서버가 락 안에서 한다.
 * @param {{set?:object, del?:string[], replace?:object}} delta
 */
async function pushState(url, delta, timeoutMs) {
  if (typeof fetch !== 'function') return false;
  let ctrl = null; let timer = null;
  try {
    if (typeof AbortController === 'function') {
      ctrl = new AbortController();
      timer = setTimeout(() => { try { ctrl.abort(); } catch (_) { /* noop */ } }, timeoutMs || 2500);
    }
    const opt = {
      method: 'PUT', cache: 'no-store',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(delta || { set: {} }),
    };
    if (ctrl) opt.signal = ctrl.signal;
    const r = await fetch(url, opt);
    if (r && r.ok) return true;
    // 서버가 일시적으로 밀렸을 수 있다(연결 거부·5xx). 델타라 재전송이 안전하다
    // — 같은 set/del 을 두 번 병합해도 결과가 같다(멱등).
    return await retryOnce(url, opt, timeoutMs);
  } catch (e) {
    return await retryOnce(url, {
      method: 'PUT', cache: 'no-store',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(delta || { set: {} }),
    }, timeoutMs);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/**
 * 델타는 멱등이라(같은 set/del 을 두 번 병합해도 결과가 같다) 1회 재전송이 안전하다.
 * 재시도에도 **자체 타임아웃**을 준다 — 첫 시도의 signal 은 이미 abort 됐을 수 있어 재사용하면
 * 즉시 실패하고, 그렇다고 signal 없이 보내면 서버가 멈췄을 때 영원히 매달린다(실측 전 결함).
 */
async function retryOnce(url, opt, timeoutMs) {
  let ctrl = null; let timer = null;
  try {
    await new Promise((r) => setTimeout(r, RETRY_DELAY_MS));
    const o = Object.assign({}, opt);
    delete o.signal;
    if (typeof AbortController === 'function') {
      ctrl = new AbortController();
      timer = setTimeout(() => { try { ctrl.abort(); } catch (_) { /* noop */ } }, timeoutMs || 2500);
      o.signal = ctrl.signal;
    }
    const r2 = await fetch(url, o);
    return !!(r2 && r2.ok);
  } catch (e) {
    return false;
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/**
 * 확인 상태 델타를 서버에 본다. 실패해도 throw 하지 않는다(로컬은 이미 저장됨).
 *
 * 왜 전체 맵이 아니라 델타인가(실측 버그): 전체 교체로 보낼 운영자 A 가 확인한 뒤
 * 그보다 먼저 화면을 연 B 의 PUT 이 A 의 확인을 통째로 덮어썼다. 병합은 서버가 락 안에서 한다.
 * @param {{set?:object, del?:string[], replace?:object}} delta
 */
export function pushAck(delta, timeoutMs) { return pushState(ACK_URL, delta, timeoutMs); }

/** 점검 창 델타를 서버에 본다(pushAck 과 같은 계약·재시도). */
export function pushMaint(delta, timeoutMs) { return pushState(MAINT_URL, delta, timeoutMs); }

/** 장비 메모 델타를 서버에 본다(pushAck 과 같은 계약·재시도). */
export function pushNotes(delta, timeoutMs) { return pushState(NOTES_URL, delta, timeoutMs); }

/** Server response replaces local fallback; null means the server was unreachable. */
export function authoritativeSharedState(localValue, remoteValue) {
  return (remoteValue && typeof remoteValue === 'object' && !Array.isArray(remoteValue))
    ? remoteValue : (localValue || {});
}

/** 이전 맵 → 다음 맵의 차이를 델타로 계산한다(순수 · 테스트 가능). */
export function ackDelta(prev, next) {
  const a = prev || {}; const b = next || {};
  const set = {};
  const del = [];
  for (const k of Object.keys(b)) if (a[k] !== b[k]) set[k] = b[k];
  for (const k of Object.keys(a)) if (!(k in b)) del.push(k);
  return { set, del };
}
