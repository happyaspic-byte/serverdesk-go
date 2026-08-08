import { setUsageThresholds } from '../util/fmt.js';
// js/model/data.js — F2 데이터 모델
// ---------------------------------------------------------------------------
// TYPES 레지스트리 + Stratus fleet 시뮬레이션(buildFleet/tickFleet) + /api/fleet 폴백(normalize/pull).
// 근거: REBUILD-SPEC.md §4.1~§4.4, Vigil `server/poller.py::build_device/derive_*`,
//       Vigil `src/model/data.ts`(TYPES) / `devices.ts`(normalize) / `detail.ts`(meta 소비 필드).
//
// 원칙
//  - DOM 접근 없음. 순수 함수 중심(예외: 시뮬레이션 PRNG 커서·onset 맵만 모듈 로컬 상태).
//  - Math.random 금지. 시드 기반 결정적 의사난수(mulberry32)만 사용한다.
//  - tick 갱신은 랜덤워크(±3~6)로 자연스럽게 이어지게 하고 급변을 만들지 않는다.
//  - 결측은 조작하지 않고 -1 + NA 플래그로만 표기한다(PLC는 항상 NA).
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
  END:  { label: 'ztC Endurance',  short: 'END',  kindEN: 'Datacenter FT',          kindKO: '데이터센터 FT',     icon: 'ph-hard-drives' },
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
 * 2. 결정적 의사난수 (mulberry32)
 * ======================================================================== */

/** 32bit 시드 → 0..1 난수 함수. 동일 시드 = 동일 시퀀스. */
export function makeRng(seed) {
  let a = (seed >>> 0) || 0x9e3779b9;
  return function rng() {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** 문자열 → 32bit 해시(시드 파생용). */
export function hashSeed(str) {
  let h = 2166136261 >>> 0;
  const s = String(str);
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619) >>> 0;
  }
  return h >>> 0;
}

const mkHelpers = (rng) => ({
  rnd: rng,
  ri: (a, b) => a + Math.floor(rng() * (b - a + 1)),         // 정수 [a,b]
  rf: (a, b) => a + rng() * (b - a),                          // 실수 [a,b)
  pick: (arr) => arr[Math.floor(rng() * arr.length) % arr.length],
  chance: (p) => rng() < p,
  shuffle: (arr) => {
    const a = arr.slice();
    for (let i = a.length - 1; i > 0; i--) {
      const j = Math.floor(rng() * (i + 1));
      const t = a[i]; a[i] = a[j]; a[j] = t;
    }
    return a;
  },
});

/* ===========================================================================
 * 3. 공용 소도구
 * ======================================================================== */

export const clamp = (v, a, b) => (v < a ? a : v > b ? b : v);
const pad2 = (n) => String(n).padStart(2, '0');
const round1 = (v) => Math.round(v * 10) / 10;

/** 'YYYY-MM-DD HH:MM:SS' (Vigil audit/alert 타임스탬프 포맷). */
export function tsOf(ms) {
  const d = new Date(ms);
  return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) +
    ' ' + pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
}

const _MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
/** avcli license-info 스타일 날짜: 'Mon Jun 29 17:01:47 KST 2026' (compute의 _fmtDate가 파싱). */
export function licDate(ms) {
  const d = new Date(ms);
  const wd = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][d.getDay()];
  return wd + ' ' + _MON[d.getMonth()] + ' ' + pad2(d.getDate()) + ' ' +
    pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds()) + ' KST ' + d.getFullYear();
}

const DAY = 86400000;

/* ---- Vigil poller.py 의 심각도 분류기 이식 ---- */
const _CRIT = /offline|unreachable|failed|fault|lost|cannot|expired|broken|\bdown\b|simplex/i;
const _WARN = /warning|no link|disconnect|maintenance|\bsync|pressure|capacity|temporary|degrade|reboot|unexpected/i;

export function classify(text) {
  const t = String(text || '');
  if (_CRIT.test(t)) return 'critical';
  if (_WARN.test(t)) return 'warning';
  return 'info';
}

/* ---- poller.py derive_status / derive_sync / availN 이식 ---- */

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
  // (실 폴러는 헬스 기반으로 비FT deg 를 산출한다 — 시뮬도 배정된 deg 를 이 플래그로 보존한다.)
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
 * 4. 시뮬레이션 정적 재료
 * ======================================================================== */

const COMPANIES = [
  { name: '한빛전자',   short: 'HB', factories: ['수원 1공장', '수원 2공장', '평택공장'] },
  { name: '대원정밀',   short: 'DW', factories: ['창원공장', '김해공장'] },
  { name: '세종화학',   short: 'SJ', factories: ['울산공장', '여수공장', '대산공장'] },
  { name: '동아반도체', short: 'DA', factories: ['이천공장'] },
];

const SITES = ['수원', '평택', '창원', '김해', '울산', '여수', '대산', '이천'];

const VM_NAMES = ['MES-APP', 'SCADA', 'HISTORIAN', 'DB-PRIMARY', 'FILE-SVR', 'AD-DC', 'OPC-UA',
  'REPORT', 'BACKUP', 'GATEWAY', 'VISION', 'LINE-CTRL'];

const VM_OS = ['Microsoft Windows Server 2019', 'Microsoft Windows Server 2022', 'Red Hat Enterprise Linux 8', 'Ubuntu 22.04 LTS'];

const NODE_MODELS = ['R650', 'R750', 'DL380 Gen10', 'ThinkSystem SR650', 'ztC Edge 250i'];
const VENDORS = ['Dell Inc.', 'HPE', 'Lenovo', 'Supermicro', 'Stratus Technologies'];
const CPU_MODELS = [
  'Intel(R) Xeon(R) Silver 4310 CPU @ 2.10GHz',
  'Intel(R) Xeon(R) Gold 6338 CPU @ 2.00GHz',
  'Intel(R) Xeon(R) Gold 5218 CPU @ 2.30GHz',
];

const PLC_MAKERS = [
  { maker: 'OMRON', models: ['NX102-9000', 'NJ501-1300', 'CJ2M-CPU33'], protocol: 'FINS', port: 9600 },
  { maker: 'Rockwell', models: ['1756-L83E', '5069-L320ER'], protocol: 'EtherNet/IP', port: 44818 },
  { maker: 'Mitsubishi', models: ['R08CPU', 'Q06UDEH'], protocol: 'Modbus TCP', port: 502 },
];

const PLC_VARS = [
  { name: 'LineSpeed', label: '라인 속도', unit: 'm/min', lo: 8, hi: 24 },
  { name: 'OvenTemp', label: '오븐 온도', unit: '°C', lo: 180, hi: 235 },
  { name: 'AirPress', label: '에어 압력', unit: 'kPa', lo: 480, hi: 620 },
  { name: 'CycleCount', label: '사이클 카운트', unit: 'ea', lo: 10000, hi: 99000 },
  { name: 'RejectRate', label: '불량률', unit: '%', lo: 0, hi: 3 },
];

const ALERT_SEEDS = [
  { name: 'SYNC_STARTED', desc: 'Mirror resynchronization started on volume vol-data', severity: 'warning' },
  { name: 'NODE_MAINTENANCE', desc: 'Node entered maintenance mode', severity: 'warning' },
  { name: 'DISK_PRESSURE', desc: 'Storage capacity pressure on the shared volume group', severity: 'warning' },
  { name: 'NTP_DRIFT', desc: 'System clock drift detected against the NTP source', severity: 'info' },
  { name: 'FIRMWARE_AVAILABLE', desc: 'A firmware update is available for this unit', severity: 'info' },
  { name: 'VM_MIGRATED', desc: 'Virtual machine migrated to the peer node', severity: 'info' },
  { name: 'PSU_REDUNDANCY', desc: 'Power supply redundancy temporary loss', severity: 'warning' },
  // severity 는 classify()(Vigil poller.py 이식 분류기) 결과와 같아야 한다 — 어긋나면
  // 목록(sev 우선)과 장비 큐가 같은 경보를 다른 심각도로 읽는다(#51). 'no link' 는
  // 이중화 상실 = warning.
  { name: 'NIC_NO_LINK', desc: 'Network interface eth3 has no link', severity: 'warning' },
];

const TRAP_SEEDS = [
  { desc: 'linkDown on interface eth3', oid: '1.3.6.1.6.3.1.1.5.3' },
  { desc: 'linkUp on interface eth3', oid: '1.3.6.1.6.3.1.1.5.4' },
  { desc: 'coldStart notification received', oid: '1.3.6.1.6.3.1.1.5.1' },
  { desc: 'authenticationFailure from management station', oid: '1.3.6.1.6.3.1.1.5.5' },
  { desc: 'Fan speed threshold crossed', oid: '1.3.6.1.4.1.674.10892.5.3.2' },
  { desc: 'Temperature probe warning', oid: '1.3.6.1.4.1.674.10892.5.3.1' },
];

/** 상태별 CPU/MEM 기준선(랜덤워크의 평균 회귀 목표). */
const BASELINE = {
  op:   { cpuLo: 35, cpuHi: 65, memLo: 40, memHi: 68 },
  deg:  { cpuLo: 65, cpuHi: 85, memLo: 62, memHi: 88 },
  down: { cpuLo: 0,  cpuHi: 0,  memLo: 0,  memHi: 0 },
};

/* ===========================================================================
 * 5. 초기 생성 (buildFleet)
 * ======================================================================== */

// 시뮬레이션 커서(모듈 로컬). buildFleet 이 재설정한다.
// 시드는 이 상수 하나가 정본이다 — app.js boot(data.buildFleet(data.SIM_SEED))과
// pullPatch 폴백(buildFleet() 기본값)가 같은 fleet 을 만들도록 양쪽이 공유한다.
export const SIM_SEED = 20260720;
let _simRng = makeRng(SIM_SEED);
let _simSeed = SIM_SEED;
// 상태 최초 진입 시각 보존(합성 경보의 발생시각이 tick 마다 흔들리지 않게).
const _onsetMap = new Map();
// #543: 세션 내 down/deg 에피소드 순번(장비 id → n번째 에피소드). 회복 시에도 지우지
// 않아 재발 에피소드가 새 순번을 받고, buildFleet 이 리셋해 리로드 후 첫 에피소드는
// 다시 1번 — 시뮬 downSince 의 시드 결정적 생성 재료(_episodeStamp 참조).
const _episodeSeq = new Map();

/* #543: 시뮬 down/deg 의 downSince/issueSince 를 시드 결정적으로 만든다.
   #493 이 벽시계 onset(tsOf(now))을 기록하면서 상시 down 장비의 ack 키가 리로드마다
   갈렸다(#398 재발) — compute 의 키 재료가 meta.downSince 우선이기 때문이다.
   에피소드 식별은 (장비 id 해시 + 세션 내 에피소드 순번)으로 대신한다:
   · 같은 세션의 재발 에피소드는 순번이 달라 새 시각 → 새 ack 키(#493 계약 유지).
   · 리로드(buildFleet 리셋) 후 같은 n번째 에피소드는 같은 해시·같은 순번이라
     같은 시각 → 같은 ack 키(#398 계약 복구).
   UTC 직접 포맷이라 콘솔 시간대와 무관하게 /ack 공유 키가 같다(표시용 onset time 은
   기존 벽시계 tsOf 그대로 — 이 값은 키 재료 전용이다). */
const _EP_BASE_MS = Date.UTC(2026, 0, 1);
function _episodeStamp(id, seq) {
  let h = 2166136261; // FNV-1a — 같은 id 는 항상 같은 해시
  const s = String(id);
  for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619); }
  // 기준 시각에서 과거로 뺀다 — 미래 시각의 downSince 가 되지 않게(장비마다
  // 해시 오프셋, 에피소드마다 순번 1일씩 추가로 과거).
  const d = new Date(_EP_BASE_MS - (((h >>> 0) % (300 * 86400)) + seq * 86400) * 1000);
  return d.getUTCFullYear() + '-' + pad2(d.getUTCMonth() + 1) + '-' + pad2(d.getUTCDate()) +
    ' ' + pad2(d.getUTCHours()) + ':' + pad2(d.getUTCMinutes()) + ':' + pad2(d.getUTCSeconds());
}

const ipOf = (a, b, c, d) => a + '.' + b + '.' + c + '.' + d;

function makeSnmpNode(h, ip, status, reachable, uptimeDays) {
  const base = BASELINE[status] || BASELINE.op;
  const n = { ip, reachable: !!reachable };
  // 도달 가능하지만 상태가 deg 로 배정된 노드는 '저하' 플래그를 단다 —
  // deriveStatus 의 SNMP 분기가 이 플래그로 비FT(단일 노드) deg 를 op 로 뭉개지 않고 보존한다.
  if (reachable && status === 'deg') n.degraded = true;
  if (reachable) {
    n.cpu = Math.round(h.rf(base.cpuLo, base.cpuHi));
    n.mem = Math.round(h.rf(base.memLo, base.memHi));
    n.uptime_days = uptimeDays;
    n.uptime_secs = uptimeDays * 86400 + h.ri(0, 86399);
    n.fresh = n.uptime_secs < 3600;
    n.cpuModel = h.pick(CPU_MODELS);
    n.memGiB = h.pick([64, 128, 192, 256]);
    n.serial = 'SN' + h.ri(100000, 999999);
    n.bios = '2.' + h.ri(1, 18) + '.' + h.ri(0, 9);
  }
  return n;
}

function makeFtNodes(h, key, type, status, ipBase) {
  const count = 2;
  const nodes = [];
  const model = h.pick(NODE_MODELS);
  const cpuModel = h.pick(CPU_MODELS);
  const memGiB = h.pick([128, 192, 256, 384]);
  const cores = h.pick([16, 24, 32, 48]);
  for (let i = 0; i < count; i++) {
    // deg: 한 노드가 비정상(대기/점검), down: 전부 정지.
    const running = status === 'down' ? false : (status === 'deg' ? i === 0 : true);
    const standing = status === 'deg' && i === 1 ? h.pick(['syncing', 'maintenance']) : 'normal';
    nodes.push({
      name: key + '-node' + i,
      state: running ? 'running' : 'stopped',
      standing: running ? standing : 'unknown',
      mode: standing === 'maintenance' ? 'maintenance' : 'production',
      primary: i === 0 && running,
      manufacturer: h.pick(VENDORS),
      model: type === 'EDGE' ? 'ztC Edge 250i' : model,
      cpus: String(cores),
      memory: memGiB + ' GB',
      ip: ipOf(ipBase[0], ipBase[1], ipBase[2], ipBase[3] + 1 + i),
      serial: 'SN' + h.ri(100000, 999999),
      bios: '2.' + h.ri(1, 18) + '.' + h.ri(0, 9),
      cpuModel,
      cores,
      memGiB,
      modules: null,
    });
  }
  return nodes;
}

function makeVms(h, key, nodes, status) {
  const n = h.ri(2, 6);
  const names = h.shuffle(VM_NAMES).slice(0, n);
  const nodeNames = nodes.map((x) => x.name);
  return names.map((nm, i) => {
    const running = status === 'down' ? false : (status === 'deg' && i === n - 1 ? false : true);
    const ft = h.chance(0.65) ? 'ft' : (h.chance(0.5) ? 'ha' : '');
    const vcpu = h.pick([2, 4, 8, 16]);
    const memGB = h.pick([4, 8, 16, 32]);
    return {
      name: key + '-' + nm,
      state: running ? 'running' : 'shutdown',
      ft,
      cpus: String(vcpu),
      vcpu,
      memory: memGB + ' GB',
      diskMB: memGB * 1024 * h.ri(2, 8),
      standing: running ? 'normal' : '',
      node: nodeNames[i % nodeNames.length],
      ip: ipOf(10, 30, h.ri(10, 60), h.ri(20, 240)),
      guest: h.chance(0.4) ? {
        os: h.pick(VM_OS),
        host: nm.toLowerCase().replace(/[^a-z0-9]/g, '') + '01',
        cpuPct: Math.round(h.rf(5, 70)),
        memPct: Math.round(h.rf(25, 85)),
      } : null,
    };
  });
}

function makeAlerts(h, now, count) {
  const out = [];
  for (let i = 0; i < count; i++) {
    const s = h.pick(ALERT_SEEDS);
    const t = now - h.ri(60, 60 * 60 * 20) * 1000;
    out.push({ name: s.name, desc: s.desc, time: tsOf(t), severity: s.severity, sev: classify(s.name + ' ' + s.desc) });
  }
  return out.sort((a, b) => String(b.time).localeCompare(String(a.time)));
}

function makeTraps(h, now, mgmt, count) {
  const out = [];
  for (let i = 0; i < count; i++) {
    const s = h.pick(TRAP_SEEDS);
    const t = now - h.ri(30, 60 * 60 * 12) * 1000;
    out.push({ desc: s.desc, oid: s.oid, time: tsOf(t), src: mgmt, sev: classify(s.desc) });
  }
  return out.sort((a, b) => String(b.time).localeCompare(String(a.time)));
}

function makeLicense(h, now, idx) {
  // 영구 2건은 buildFleet 에서 idx 로 지정.
  const perpetual = idx < 2;
  const days = h.ri(30, 400) * (h.chance(0.18) ? 0.08 : 1); // 일부는 D-30 이내로 임박
  const expMs = now + Math.round(days) * DAY;
  return {
    name: 'everRun Enterprise Edition',
    type: h.chance(0.2) ? 'trial' : 'standard',
    edition: h.pick(['Enterprise', 'Standard', 'HA']),
    install: licDate(now - h.ri(200, 900) * DAY),
    expire: perpetual ? '' : licDate(expMs),
    expires: !perpetual,
    activated: h.chance(0.92),
  };
}

function makeUnit(h, type, status) {
  const totVcpu = h.pick([32, 48, 64, 96]);
  const totMem = h.pick([128, 192, 256, 384, 512]);
  const useR = h.rf(0.70, 0.90);
  return {
    name: 'unit0',
    version: (type === 'EDGE' ? '3.' : '7.') + h.ri(0, 9) + '.' + h.ri(0, 9) + '.0',
    syncing: status === 'deg' ? 'true' : 'false',
    totVcpu,
    usedVcpu: Math.round(totVcpu * useR),
    totMem,
    usedMem: round1(totMem * useR),
  };
}

function makePlcMeta(h, now, status, mgmt) {
  const fam = h.pick(PLC_MAKERS);
  const model = h.pick(fam.models);
  const hasError = status !== 'op' && h.chance(0.6);
  const obs = !hasError && h.chance(0.25);
  const sev = hasError ? h.pick(['minor', 'partial', 'major']) : (obs ? 'observation' : '');
  const runState = status === 'down' ? '' : (status === 'deg' && h.chance(0.4) ? 'PROGRAM' : h.pick(['RUN', 'RUN', 'MONITOR']));
  return {
    runState,
    hasError,
    errSev: sev,
    errSince: sev ? tsOf(now - h.ri(300, 60 * 60 * 30) * 1000) : '',
    errorMessage: hasError ? 'FAL ' + h.ri(1, 511) + ' — ' + h.pick(['I/O bus response timeout', 'Servo axis alarm', 'Safety relay open']) : '',
    protocol: fam.protocol,
    port: fam.port,
    maker: fam.maker,
    model,
    detectedModel: model,
    productName: fam.maker + ' ' + model,
    fwVersion: '1.' + h.ri(0, 30),
    unitRev: 'V' + h.ri(1, 4) + '.' + h.ri(0, 9),
    serial: 'PLC' + h.ri(10000, 99999),
    finsRttMs: status === 'down' ? null : round1(h.rf(0.4, 8)),
    // cipStatus 는 runState 와 커플링 — 독립 추첨이면 'RUN · 프로그램 실행 중' 옆에
    // 'Program mode' 가 병존하는 자기모순이 생긴다(#567). down(도달 불가)은 '' → 대시.
    cipStatus: fam.protocol === 'EtherNet/IP' ? (runState === 'PROGRAM' ? 'Program mode' : (runState ? 'Run mode' : '')) : '',
    cipMajorFault: fam.protocol === 'EtherNet/IP' ? hasError && h.chance(0.4) : false,
    cipMinorFault: fam.protocol === 'EtherNet/IP' ? h.chance(0.2) : false,
    finsFatalCode: fam.protocol === 'FINS' ? (hasError ? '0x' + h.ri(4096, 65535).toString(16) : '0x0000') : '',
    finsNonFatalCode: fam.protocol === 'FINS' ? '0x' + (h.chance(0.3) ? h.ri(1, 4095).toString(16).padStart(4, '0') : '0000') : '',
    linkSpeedMbps: h.pick([100, 1000]),
    linkFullDuplex: h.chance(0.9),
    clockSkewSec: h.chance(0.15) ? h.ri(86401, 86400 * 90) : h.ri(-45, 45),
    procVars: h.shuffle(PLC_VARS).slice(0, h.ri(3, 5)).map((v) => ({
      name: v.name, label: v.label, unit: v.unit,
      value: v.unit === 'ea' ? h.ri(v.lo, v.hi) : round1(h.rf(v.lo, v.hi)),
      _lo: v.lo, _hi: v.hi,
    })),
    sysDiag: {
      ctrlErr: hasError,
      ctrlSev: sev,
      modules: [
        { module: 'Controller', code: hasError ? '0x' + h.ri(4096, 65535).toString(16) : '0x0000', err: hasError, sev: hasError ? sev : '' },
        { module: 'I/O bus', code: '0x0000', err: false, sev: obs ? 'observation' : '' },
        { module: 'Motion', code: '0x0000', err: false, sev: '' },
      ],
      userAlarm: { active: hasError && h.chance(0.5), code: 'FAL ' + h.ri(1, 511) },
      outHoldCfg: h.chance(0.4),
      powerOnCount: h.ri(20, 4000),
      sdCard: {
        ready: h.chance(0.85),
        deteriorated: h.chance(0.1),
        err: h.chance(0.05),
        protected: h.chance(0.2),
        powerFail: h.chance(0.05),
      },
      eip: fam.protocol === 'EtherNet/IP' ? {
        online: status !== 'down',
        // down 시딩에서도 tagLinkRun 은 끊긴다 — h.chance 를 먼저 소비해 RNG 스트림은 유지(#566).
        tagLinkRun: h.chance(0.8) && status !== 'down',
        portErr: h.chance(0.1) ? '0x0021' : '0x0000',
        cipErr: '0x0000',
        tcpAppErr: '0x0000',
        identityErr: false,
        bootpErr: false,
        ntp: { ok: h.chance(0.85), last: tsOf(now - h.ri(60, 7200) * 1000) },
      } : null,
    },
    netCounters: {
      inOctets: h.ri(1e7, 8e9),
      outOctets: h.ri(1e7, 6e9),
      inErrors: h.chance(0.25) ? h.ri(1, 40) : 0,
      outErrors: h.chance(0.15) ? h.ri(1, 12) : 0,
      inDiscards: h.chance(0.2) ? h.ri(1, 25) : 0,
      outDiscards: 0,
    },
    errHistory: sev ? [
      { at: tsOf(now - h.ri(600, 60 * 60 * 10) * 1000), to: sev },
      { at: tsOf(now - h.ri(60 * 60 * 12, 60 * 60 * 60) * 1000), to: '' },
    ] : [],
    _mgmt: mgmt,
  };
}

function makeNasMeta(h, status) {
  const volTot = h.pick([4096, 8192, 16384, 32768]);
  const usedPct = Math.round(h.rf(35, 88));
  const bad = status !== 'op' && h.chance(0.6);
  const diskN = h.ri(2, 6);
  return {
    model: h.pick(['DS1621+', 'RS1221+', 'DS923+', 'RS3621xs+']),
    serial: '20' + h.ri(10, 24) + 'PDN' + h.ri(100000, 999999),
    dsmVersion: 'DSM 7.' + h.ri(0, 2) + '-' + h.ri(40000, 65000),
    tempC: h.ri(32, 52),
    systemStatus: bad ? h.pick(['degraded', 'attention']) : 'normal',
    powerStatus: 'normal',
    systemFan: 'normal',
    cpuFan: 'normal',
    fansOk: !bad,
    upgradeAvailable: h.chance(0.3),
    upgrade: h.chance(0.3),
    disks: Array.from({ length: diskN }, (_, i) => ({
      name: 'Drive ' + (i + 1),
      status: bad && i === diskN - 1 ? 'warning' : 'normal',
      ok: !(bad && i === diskN - 1),
      model: h.pick(['WD40EFRX', 'ST8000VN004', 'HAT5300-8T']),
      tempC: h.ri(30, 46),
    })),
    raid: [{ name: 'Storage Pool 1', status: bad ? 'degraded' : 'normal', ok: !bad }],
    volumes: [{ name: 'volume1', pct: usedPct, sizeGiB: volTot, usedGiB: Math.round(volTot * usedPct / 100) }],
  };
}

function makeWinMeta(h, key, status) {
  const memTotMB = h.pick([8, 16, 32, 64, 128]) * 1024;
  const dn = h.ri(1, 3);
  const drives = ['C:', 'D:', 'E:'];
  return {
    host: key.toUpperCase(),
    domain: h.pick(['CORP', 'PLANT', 'FACTORY']),
    os: h.pick(['Windows Server 2019 Standard', 'Windows Server 2022 Datacenter', 'Windows Server 2016 Standard']),
    build: String(h.ri(17763, 20348)),
    make: h.pick(VENDORS),
    model: h.pick(NODE_MODELS),
    serial: 'WS' + h.ri(100000, 999999),
    cores: h.pick([4, 8, 16, 32]),
    memTotMB,
    cpuPct: status === 'down' ? null : Math.round(h.rf(15, 70)),
    memPct: status === 'down' ? null : Math.round(h.rf(35, 80)),
    uptimeSec: h.ri(3600, 86400 * 300),
    svcRunning: h.ri(70, 180),
    lastHotfix: 'KB' + h.ri(5000000, 5099999),
    lastHotfixOn: tsOf(Date.now() - h.ri(3, 90) * DAY),
    disks: Array.from({ length: dn }, (_, i) => {
      const sizeGB = h.pick([256, 512, 1024, 2048]);
      const pct = Math.round(h.rf(30, 92));
      return { drive: drives[i], sizeGB, freeGB: Math.round(sizeGB * (100 - pct) / 100), pct };
    }),
  };
}

function makePiMeta(h, status) {
  // down 시딩은 tempC/throttled 도 cpu/mem(#596)과 같은 계약으로 해제한다 —
  // 두지 않으면 죽은 장비 상세에 'SoC 온도 77.5°C'(≥75°C neg)·'스로틀: 과열'이
  // 사실처럼 표기된다(#607). tempC=null 은 상세 온도 행을 숨기고 throttled=false 는
  // 스로틀 행을 '없음'으로 내린다. 복구는 틱 갱신(status !== 'down' 분기)이 자연 복원한다.
  const temp = status === 'down' ? null : round1(h.rf(42, 78));
  const mem = status === 'down' ? null : Math.round(h.rf(25, 72));
  return {
    model: h.pick(['Raspberry Pi 4 Model B Rev 1.4', 'Raspberry Pi 5 Model B Rev 1.0', 'Raspberry Pi 3 Model B+']),
    kernel: '6.' + h.ri(1, 6) + '.' + h.ri(0, 70) + '-v8+',
    os: h.pick(['Raspbian GNU/Linux 12 (bookworm)', 'Ubuntu 22.04.4 LTS']),
    mac: 'dc:a6:32:' + h.ri(16, 255).toString(16) + ':' + h.ri(16, 255).toString(16) + ':' + h.ri(16, 255).toString(16),
    macVendor: 'Raspberry Pi Trading Ltd',
    rttMs: status === 'down' ? null : round1(h.rf(0.3, 6)),
    tempC: temp,
    coreVolt: round1(h.rf(0.85, 1.35)) + '',
    memTotMB: h.pick([2048, 4096, 8192]),
    cpu: status === 'down' ? null : Math.round(h.rf(8, 60)),
    mem,
    // 시뮬 PI 는 SSH 로 수집한다(sshBanner·cpu·mem·temp·throttle 모두 SSH 경로 산출) —
    // ssh 키를 시딩하지 않으면 배지는 '에이전트리스'·힌트 표시·Throttle 행 영구 숨김인데
    // 부제(compute hwLine)는 cpu != null 기준 'SSH' 로 읽는 3갈래 모순이 된다(#595).
    // memPct 는 detail 카드 메모리 행('N GB · N%')의 소비 계약 — mem 과 같은 값/down-null
    // 계약으로 시딩하고 틱 갱신도 함께 미러한다.
    ssh: true,
    memPct: mem,
    uptimeSec: h.ri(3600, 86400 * 200),
    throttled: temp != null && temp >= 75,
    throttleThermal: temp != null && temp >= 75,
    // A down PI must not retain a synthetic under-voltage warning. The
    // topology card derives its warning from this field even when the main
    // throttled flag is false.
    throttleUnderVolt: status !== 'down' && h.chance(0.12),
    sshBanner: 'OpenSSH_9.2p1 Debian-2',
  };
}

function makePcMeta(h, key, status) {
  return {
    netbios: key.toUpperCase(),
    fqdn: key + '.plant.local',
    workgroup: h.pick(['WORKGROUP', 'PLANT']),
    os: h.pick(['Windows 10 Pro', 'Windows 11 Pro']),
    osVersion: h.pick(['22H2', '23H2', '21H2']),
    smbDialect: h.pick(['3.1.1', '3.0.2']),
    smbSigning: { enabled: h.chance(0.8), required: h.chance(0.3) },
    services: h.shuffle(['SMB', 'RDP', 'WinRM', 'HTTP']).slice(0, h.ri(1, 3)),
    loginUser: h.chance(0.6) ? h.pick(['operator', 'line01', 'admin']) : '',
    mac: '00:1a:2b:' + h.ri(16, 255).toString(16) + ':' + h.ri(16, 255).toString(16) + ':' + h.ri(16, 255).toString(16),
    macVendor: h.pick(['Dell Inc.', 'ASUSTek COMPUTER INC.', 'Hewlett Packard']),
    machineGuid: '',
    clockSkewSec: h.ri(-120, 120),
    rttMs: status === 'down' ? null : round1(h.rf(0.2, 5)),
    snmp: h.chance(0.5),
    fileSharing: h.chance(0.5),
    printer: null,
    certs: [],
  };
}

/**
 * 시뮬레이션 fleet 초기 생성.
 * @param {number|string} [seed=SIM_SEED] 시드(문자열이면 해시)
 * @returns {Array<Object>} device[] (§4.1 스키마)
 */
export function buildFleet(seed) {
  const s = typeof seed === 'string' ? hashSeed(seed) : (typeof seed === 'number' ? seed : SIM_SEED);
  _simSeed = s >>> 0;
  _simRng = makeRng(_simSeed);
  _onsetMap.clear();
  _episodeSeq.clear(); // #543 — 새 세션의 에피소드 순번은 1부터(리로드 안정)

  const rng = makeRng(_simSeed);
  const h = mkHelpers(rng);
  const now = Date.now();

  // 1) 배치 계획: 회사 3~4개 × 공장 1~3개, 총 27대.
  const companies = COMPANIES.slice(0, h.ri(3, 4));
  const slots = [];
  companies.forEach((co, ci) => {
    const facs = co.factories.slice(0, h.ri(1, co.factories.length));
    facs.forEach((fa, fi) => slots.push({ co, fa, ci, fi }));
  });

  // 2) 타입 배분 — FT 위주 + 기타 섞기(총 27대).
  const plan = [
    'EV', 'EV', 'EV', 'EV', 'EV', 'EV',
    'EDGE', 'EDGE', 'EDGE', 'EDGE',
    'END', 'END', 'END',
    'FTS', 'FTS',
    'SRV', 'SRV', 'SRV',
    'NAS', 'NAS',
    'WIN', 'WIN',
    'PLC', 'PLC', 'PLC',
    'PI', 'PC',
  ];

  // 3) 상태 배분: 대부분 op, deg 2, down 1.
  const statusPlan = new Array(plan.length).fill('op');
  const idxs = h.shuffle(plan.map((_, i) => i));
  statusPlan[idxs[0]] = 'down';
  statusPlan[idxs[1]] = 'deg';
  statusPlan[idxs[2]] = 'deg';

  const fleet = [];
  let licIdx = 0;

  plan.forEach((type, i) => {
    const slot = slots[i % slots.length];
    const status = statusPlan[i];
    const co = slot.co;
    const seq = String(i + 1).padStart(2, '0');
    const key = co.short.toLowerCase() + '-' + type.toLowerCase() + seq;
    const ipBase = [10, 20 + slot.ci, 10 + slot.fi, 10 + (i % 40) * 5];
    const mgmt = ipOf(ipBase[0], ipBase[1], ipBase[2], ipBase[3]);
    const site = SITES[(slot.ci * 3 + slot.fi) % SITES.length];
    const uptimeDays = status === 'down' ? 0 : h.ri(1, 420);

    const meta = {
      label: co.name + ' ' + TYPES[type].short + '-' + seq,
      company: co.name,
      factory: slot.fa,
      mgmt,
      assetTag: 'AST-' + co.short + '-' + h.ri(10000, 99999),
      vendor: h.pick(VENDORS),
      error: null,
      pending: false,
      version: '',
      alerts: [],
      traps: makeTraps(h, now, mgmt, h.ri(0, 6)),
      snmp: [],
      nodes: [],
      vmList: [],
      vms: 0,
      vmRunning: 0,
      unit: {},
      license: null,
      lastVmSwitch: null,
      lastNodeSwitch: null,
      lastReboot: null,
      bmc: null,
      events: [],
    };

    let cpu0 = -1;
    let mem0 = -1;
    let sync = 'sync';
    let stat = status;

    if (isFT(type)) {
      const nodes = makeFtNodes(h, key, type, status, ipBase);
      const snmp = nodes.map((n) => makeSnmpNode(h, n.ip, status, n.state === 'running', uptimeDays));
      const unit = makeUnit(h, type, status);
      const vms = makeVms(h, key, nodes, status);
      meta.nodes = nodes;
      meta.snmp = snmp;
      meta.unit = unit;
      meta.version = unit.version;
      meta.vmList = vms;
      meta.vms = vms.length;
      meta.vmRunning = vms.filter((v) => v.state === 'running').length;
      meta.license = makeLicense(h, now, licIdx++);
      meta.alerts = makeAlerts(h, now, h.ri(0, 4));
      if (h.chance(0.5)) {
        meta.lastNodeSwitch = {
          ts: tsOf(now - h.ri(3600, 86400 * 20) * 1000),
          node: nodes[0].name, from: nodes[1].name, to: nodes[0].name,
          desc: 'Physical machine "' + nodes[1].name + '" entered maintenance',
        };
      }
      if (h.chance(0.5) && vms.length) {
        meta.lastVmSwitch = {
          ts: tsOf(now - h.ri(3600, 86400 * 20) * 1000),
          vm: vms[0].name, from: nodes[1].name, to: nodes[0].name,
          desc: 'Move virtual machine "' + vms[0].name + '" to ' + nodes[0].name,
        };
      }
      stat = deriveStatus(nodes, snmp);
      sync = deriveSync(nodes, stat);
      const reach = snmp.filter((n) => n.reachable);
      cpu0 = reach.length ? Math.round(reach.reduce((a, n) => a + n.cpu, 0) / reach.length) : -1;
      mem0 = reach.length ? Math.round(reach.reduce((a, n) => a + n.mem, 0) / reach.length) : -1;
    } else if (type === 'PLC') {
      meta.plc = makePlcMeta(h, now, status, mgmt);
      meta.snmp = [];
      meta.alerts = meta.plc.hasError
        ? [{ name: 'PLC_ERROR', desc: meta.plc.errorMessage || 'Controller error flag set', time: meta.plc.errSince || tsOf(now), severity: 'critical', sev: 'critical' }]
        : [];
      stat = status;
      sync = status === 'down' ? 'offline' : 'sync';
      cpu0 = -1; mem0 = -1;   // PLC는 항상 NA
    } else {
      // SRV / NAS / WIN / PI / PC — 단일 노드 SNMP
      const snmp = [makeSnmpNode(h, mgmt, status, status !== 'down', uptimeDays)];
      meta.snmp = snmp;
      if (type === 'NAS') meta.nas = makeNasMeta(h, status);
      if (type === 'WIN') meta.win = makeWinMeta(h, key, status);
      if (type === 'PI') meta.pi = makePiMeta(h, status);
      if (type === 'PC') meta.pc = makePcMeta(h, key, status);
      if (type === 'SRV') meta.bmc = { ip: ipOf(ipBase[0], ipBase[1], ipBase[2] + 100, ipBase[3]), up: h.chance(0.8) };
      if (type === 'NAS' && h.chance(0.7)) {
        const vms = makeVms(h, key, [{ name: key }], status).slice(0, h.ri(1, 3));
        meta.vmList = vms;
        meta.vms = vms.length;
        meta.vmRunning = vms.filter((v) => v.state === 'running').length;
      }
      meta.alerts = makeAlerts(h, now, h.ri(0, 2));
      stat = deriveStatus([], snmp);
      sync = deriveSync([], stat);
      const reach = snmp.filter((n) => n.reachable);
      cpu0 = reach.length ? reach[0].cpu : -1;
      mem0 = reach.length ? reach[0].mem : -1;
    }

    // 상태별 합성 경보(down/deg) — onset 보존
    const dev = {
      id: key,
      host: key,
      type,
      site,
      status: stat,
      availN: availN(stat),
      cpu0: cpu0 < 0 ? -1 : clamp(cpu0, 0, 100),
      mem0: mem0 < 0 ? -1 : clamp(mem0, 0, 100),
      cpuNA: cpu0 < 0,
      memNA: mem0 < 0,
      sync,
      uptime: uptimeDays,
      live: true,
      meta,
      histCpu: [],
      histMem: [],
      histRtt: [],
    };
    _syncSyntheticAlert(dev, now);
    fleet.push(dev);
  });

  return fleet;
}

/* ---- 합성 경보(장비 오프라인/저하) 관리 ---- */
const SYN_NAME = 'DEVICE_STATE';

function _syncSyntheticAlert(dev, now) {
  const m = dev.meta || (dev.meta = {});
  const alerts = Array.isArray(m.alerts) ? m.alerts : (m.alerts = []);
  const rest = alerts.filter((a) => a && a.name !== SYN_NAME);
  if (dev.status === 'op') {
    _onsetMap.delete(dev.id);
    // #493: 회복 시 에피소드 식별 재료도 지운다 — 남겨 두면 다음 에피소드가
    // 새 onset 으로 덮어쓰기 전까지 지난 다운의 시각이 ack 키에 흘러든다.
    delete m.downSince;
    delete m.issueSince;
    m.alerts = rest;
    return;
  }
  let onset = _onsetMap.get(dev.id);
  if (!onset || onset.status !== dev.status) {
    onset = { status: dev.status, at: now };
    _onsetMap.set(dev.id, onset);
    // #493: 시뮬의 down/deg 진입 시각을 meta 에 기록한다. compute.js 가 합성
    // DEVICE_STATE 경보의 ack 키 원천으로 downSince/issueSince 를 우선 쓰므로,
    // 이 기록이 없으면 키 재료가 고정값('no-time')으로 떨어져 같은 장비의
    // 재발 다운 에피소드가 이전 확인(ack)을 그대로 상속했다(#493).
    // #543: 값은 벽시계 onset 이 아니라 _episodeStamp(장비 id + 에피소드 순번의
    // 시드 결정적 시각) — 벽시계는 리로드마다 갈려 상시 down 장비의 ack 키가
    // 세션마다 바뀌었다(#398 재발). 표시용 경보 time(onset.at)은 아래 그대로.
    const seq = (_episodeSeq.get(dev.id) || 0) + 1;
    _episodeSeq.set(dev.id, seq);
    if (dev.status === 'down') { m.downSince = _episodeStamp(dev.id, seq); delete m.issueSince; }
    else { m.issueSince = _episodeStamp(dev.id, seq); delete m.downSince; }
  }
  const desc = dev.status === 'down'
    ? 'Device offline — no node responded to the collector'
    : 'Device degraded — redundancy lost or a node is not normal';
  rest.unshift({
    name: SYN_NAME,
    desc,
    time: tsOf(onset.at),
    severity: dev.status === 'down' ? 'critical' : 'warning',
    sev: dev.status === 'down' ? 'critical' : 'warning',
  });
  m.alerts = rest;
}

function _pushEvent(dev, now, kind, text, sev) {
  const m = dev.meta || (dev.meta = {});
  if (!Array.isArray(m.events)) m.events = [];
  m.events.unshift({ ts: tsOf(now), at: now, kind, text, sev: sev || 'info', host: dev.host });
  if (m.events.length > 20) m.events.length = 20;
}

function _pushTrap(dev, now, desc, oid, sev) {
  const m = dev.meta || (dev.meta = {});
  if (!Array.isArray(m.traps)) m.traps = [];
  m.traps.unshift({ desc, oid: oid || '1.3.6.1.6.3.1.1.5.0', time: tsOf(now), src: m.mgmt || dev.host, sev: sev || classify(desc) });
  if (m.traps.length > 40) m.traps.length = 40;
}

/* ===========================================================================
 * 6. 틱 갱신 (tickFleet)
 * ======================================================================== */

/* 시뮬 추가 장비의 '불러오는 중'(meta.pending)을 몇 틱 보여 준 뒤 해제한다.
   실 모드에서는 폴리가 장비를 내리기 시작하면 mergeLocalWrites 의 key dedupe 로 자연
   소멸하지만, 시뮬에는 해제 경로가 없어 배지가 세션 끝까지 고정됐다(#436).
   텔레메트리는 첫 틱부터 정상 갱신되므로 거짓 상태 신호를 유한하게 둔다. */
const PENDING_TICKS = 3;

/** 목표 구간으로 부드럽게 회귀하는 랜덤워크(±3~6). */
function walk(prev, lo, hi, h) {
  const cur = (typeof prev === 'number' && prev >= 0) ? prev : (lo + hi) / 2;
  const step = h.rf(3, 6) * (h.chance(0.5) ? 1 : -1);
  let next = cur + step;
  // 구간 밖이면 되돌아오는 힘을 준다(급변 금지).
  if (next < lo) next = cur + Math.abs(step) * 0.7;
  if (next > hi) next = cur - Math.abs(step) * 0.7;
  return clamp(Math.round(next), 0, 100);
}

/**
 * fleet 한 틱 갱신. fleet 배열의 device 를 제자리에서 갱신하고,
 * 이력(state.hist)은 새 객체로 만들어 반환한다.
 * @param {Array} fleet buildFleet 결과
 * @param {Object} state store 상태({hist, tick})
 * @returns {{fleet:Array, hist:Object, lastPoll:number, changed:Array}}
 */
export function tickFleet(fleet, state) {
  const list = Array.isArray(fleet) ? fleet : [];
  const st = state || {};
  const now = Date.now();
  const h = mkHelpers(_simRng);
  const prevHist = st.hist || {};
  const hist = {};
  const changed = [];

  for (let i = 0; i < list.length; i++) {
    const dev = list[i];
    if (!dev || !dev.meta) continue;
    const m = dev.meta;

    /* --- 0) 시뮬 추가 장비의 pending 유한화 --- */
    if (m.pending === true) {
      m._pendingTicks = (m._pendingTicks || 0) + 1;
      if (m._pendingTicks >= PENDING_TICKS) {
        m.pending = false;
        delete m._pendingTicks;
      }
    }

    /* --- 4) 상태 전이(저확률) — 텔레메트리보다 먼저 적용 --- */
    let transitioned = null;
    // 확률은 틱 1200ms 기준 — fleet 전체에서 분당 0.5회 내외로만 흔들리게 잡았다.
    // op→deg 는 FT 만: FT 는 노드 standing 을 저하시켜 deg 를 정합하게 표현하지만, 비FT(단일 노드)는
    // 저하 내부(meta)를 동적으로 만들지 않으므로 deg 로 두면 유령 '저하' 이벤트/트랩/토스트만 남는다.
    if (dev.status === 'op' && isFT(dev.type) && h.chance(0.0004)) transitioned = 'deg';
    else if (dev.status === 'deg') {
      if (h.chance(0.003)) transitioned = 'down';
      else if (h.chance(0.015)) transitioned = 'op';
    } else if (dev.status === 'down' && h.chance(0.010)) transitioned = 'op';

    if (transitioned) _applyTransition(dev, transitioned, now, h, changed);

    const base = BASELINE[dev.status] || BASELINE.op;

    /* --- 1) 텔레메트리 지터 --- */
    if (isNoTel(dev.type)) {
      dev.cpu0 = -1; dev.mem0 = -1; dev.cpuNA = true; dev.memNA = true;
      if (m.plc) {
        if (typeof m.plc.finsRttMs === 'number') {
          m.plc.finsRttMs = dev.status === 'down' ? null : round1(clamp(m.plc.finsRttMs + h.rf(-0.6, 0.6), 0.2, 30));
        } else if (dev.status !== 'down') {
          m.plc.finsRttMs = round1(h.rf(0.4, 4));
        }
        // down 장비는 공정 변수 지터와 옥텟 카운터 증가를 건너뛴다 — 죽은 컨트롤러의
        // procVars/netCounters 가 살아 있는 것처럼 움직이는 모순 제거.
        if (dev.status !== 'down' && Array.isArray(m.plc.procVars)) {
          m.plc.procVars.forEach((v) => {
            const lo = typeof v._lo === 'number' ? v._lo : 0;
            const hi = typeof v._hi === 'number' ? v._hi : 100;
            const span = (hi - lo) || 1;
            const cur = Number(v.value) || (lo + hi) / 2;
            const nx = clamp(cur + h.rf(-span * 0.02, span * 0.02), lo, hi);
            v.value = v.unit === 'ea' ? Math.round(nx) : round1(nx);
          });
        }
        if (dev.status !== 'down' && m.plc.netCounters) {
          m.plc.netCounters.inOctets += h.ri(1000, 90000);
          m.plc.netCounters.outOctets += h.ri(800, 70000);
        }
      }
    } else {
      const snmp = Array.isArray(m.snmp) ? m.snmp : [];
      snmp.forEach((n, ni) => {
        const nodeDown = dev.status === 'down' ||
          (Array.isArray(m.nodes) && m.nodes[ni] && m.nodes[ni].state !== 'running');
        if (nodeDown) {
          n.reachable = false;
          delete n.cpu; delete n.mem;
          return;
        }
        n.reachable = true;
        n.cpu = walk(n.cpu, base.cpuLo, base.cpuHi, h);
        n.mem = walk(n.mem, base.memLo, base.memHi, h);
        // 6) uptime 증가 (틱 1.2s 단위)
        n.uptime_secs = (n.uptime_secs || 0) + 1.2;
        n.uptime_days = Math.floor(n.uptime_secs / 86400);
        n.fresh = n.uptime_secs < 3600;
        // 5) 재부팅 감지 흉내 — 매우 낮은 확률로 uptime 리셋
        if (h.chance(0.0006)) {
          n.uptime_secs = h.ri(30, 900);
          n.uptime_days = 0;
          n.fresh = true;
          n.rebooted_at = Math.floor(now / 1000);
          n.reboot_ago = 0;
          const nodeName = (Array.isArray(m.nodes) && m.nodes[ni] && m.nodes[ni].name) || n.ip;
          m.lastReboot = { ip: n.ip, node: nodeName, at: n.rebooted_at, agoSecs: 0 };
          _pushEvent(dev, now, 'reboot', nodeName + ' rebooted', 'warning');
          _pushTrap(dev, now, 'coldStart notification received', '1.3.6.1.6.3.1.1.5.1', 'warning');
        } else if (n.rebooted_at) {
          const ago = Math.floor(now / 1000) - n.rebooted_at;
          if (ago > 86400) { delete n.rebooted_at; delete n.reboot_ago; }
          else {
            n.reboot_ago = ago;
            if (m.lastReboot && m.lastReboot.ip === n.ip) m.lastReboot.agoSecs = ago;
          }
        }
      });

      /* --- 2) 클러스터 집계 --- */
      const reach = snmp.filter((n) => n && n.reachable && typeof n.cpu === 'number');
      if (reach.length) {
        dev.cpu0 = Math.round(reach.reduce((a, n) => a + n.cpu, 0) / reach.length);
        dev.mem0 = Math.round(reach.reduce((a, n) => a + (n.mem || 0), 0) / reach.length);
        dev.cpuNA = false; dev.memNA = false;
      } else {
        dev.cpu0 = -1; dev.mem0 = -1; dev.cpuNA = true; dev.memNA = true;
      }
      dev.uptime = snmp.reduce((mx, n) => Math.max(mx, n.uptime_days || 0), 0);

      // PC/PI 는 RTT 도 지터
      const rttHolder = m.pc || m.pi || null;
      if (rttHolder) {
        rttHolder.rttMs = dev.status === 'down'
          ? null
          : round1(clamp((typeof rttHolder.rttMs === 'number' ? rttHolder.rttMs : 1.2) + h.rf(-0.5, 0.5), 0.2, 40));
      }
      if (m.pi && dev.status !== 'down') {
        m.pi.tempC = round1(clamp(m.pi.tempC + h.rf(-0.8, 0.8), 38, 84));
        m.pi.throttleThermal = m.pi.tempC >= 75;
        m.pi.throttled = m.pi.throttleThermal || m.pi.throttleUnderVolt;
        m.pi.cpu = dev.cpu0 >= 0 ? dev.cpu0 : null;
        m.pi.mem = dev.mem0 >= 0 ? dev.mem0 : null;
        // memPct 는 makePiMeta(#595)가 mem 과 같은 계약으로 시딩한 소비 키 — 틱도 함께 미러한다.
        m.pi.memPct = m.pi.mem;
      }
      if (m.win && dev.status !== 'down') {
        m.win.cpuPct = dev.cpu0 >= 0 ? dev.cpu0 : null;
        m.win.memPct = dev.mem0 >= 0 ? dev.mem0 : null;
      }
      // down 장비는 볼륨 사용률 지터도 건너뛴다 — PLC procVars/netCounters 와 같은 이유로
      // 죽은 장비의 지표가 살아 있는 것처럼 움직이는 모순 제거.
      if (m.nas && dev.status !== 'down' && Array.isArray(m.nas.volumes) && m.nas.volumes[0] && h.chance(0.02)) {
        const v = m.nas.volumes[0];
        v.pct = clamp(v.pct + (h.chance(0.6) ? 1 : -1), 5, 99);
        v.usedGiB = Math.round(v.sizeGiB * v.pct / 100);
      }
      // FT unit used 자원도 아주 천천히 움직인다 — down 장비는 위와 같이 건너뛴다.
      if (m.unit && m.unit.totVcpu && dev.status !== 'down') {
        if (h.chance(0.03)) {
          m.unit.usedVcpu = clamp(m.unit.usedVcpu + (h.chance(0.5) ? 1 : -1), 1, m.unit.totVcpu);
          m.unit.usedMem = round1(clamp(m.unit.usedMem + h.rf(-2, 2), 1, m.unit.totMem));
        }
      }
    }

    /* --- 상태/sync/avail 일관성 재확인 --- */
    if (isFT(dev.type)) {
      const stt = deriveStatus(m.nodes, m.snmp);
      dev.status = stt;
      dev.sync = deriveSync(m.nodes, stt);
      if (m.unit) m.unit.syncing = stt === 'deg' ? 'true' : 'false';
    } else if (isNoTel(dev.type)) {
      dev.sync = dev.status === 'down' ? 'offline' : 'sync';
    } else {
      const stt = deriveStatus([], m.snmp);
      dev.status = stt;
      dev.sync = deriveSync([], stt);
    }
    dev.availN = availN(dev.status);
    _syncSyntheticAlert(dev, now);
    if (Array.isArray(m.alerts) && m.alerts.length > 25) m.alerts.length = 25;
    if (Array.isArray(m.traps) && m.traps.length > 40) m.traps.length = 40;

    /* --- 3) 이력 push (NA 샘플은 제외) --- */
    const ph = prevHist[dev.id] || {};
    const cpuArr = Array.isArray(ph.cpu) ? ph.cpu.slice() : [];
    const memArr = Array.isArray(ph.mem) ? ph.mem.slice() : [];
    const rttArr = Array.isArray(ph.rtt) ? ph.rtt.slice() : [];
    if (dev.cpu0 >= 0) cpuArr.push(dev.cpu0);
    if (dev.mem0 >= 0) memArr.push(dev.mem0);
    const rttNow = (m.pc && typeof m.pc.rttMs === 'number') ? m.pc.rttMs
      : (m.pi && typeof m.pi.rttMs === 'number') ? m.pi.rttMs
        : (m.plc && typeof m.plc.finsRttMs === 'number') ? m.plc.finsRttMs : null;
    if (rttNow != null) rttArr.push(rttNow);
    while (cpuArr.length > 48) cpuArr.shift();
    while (memArr.length > 48) memArr.shift();
    while (rttArr.length > 48) rttArr.shift();
    hist[dev.id] = { cpu: cpuArr, mem: memArr, rtt: rttArr };
  }

  // 사라진 장비의 이력은 정리(누수 방지)
  return { fleet: list, hist, lastPoll: now, changed };
}

/** 상태 전이 적용 — sync 재계산 · 노드 상태 조정 · 경보/트랩/이벤트 기록. */
function _applyTransition(dev, next, now, h, changed) {
  const m = dev.meta || (dev.meta = {});
  const from = dev.status;
  dev.status = next;
  changed.push({ id: dev.id, from, to: next });

  if (isFT(dev.type) && Array.isArray(m.nodes) && m.nodes.length) {
    if (next === 'down') {
      m.nodes.forEach((n) => { n.state = 'stopped'; n.standing = 'unknown'; n.mode = 'production'; n.primary = false; });
      (m.vmList || []).forEach((v) => { v.state = 'shutdown'; });
    } else if (next === 'deg') {
      m.nodes.forEach((n, i) => {
        if (i === 0) { n.state = 'running'; n.standing = 'normal'; n.mode = 'production'; n.primary = true; }
        else {
          n.state = 'running';
          n.standing = h.pick(['syncing', 'maintenance']);
          // mode 는 standing 과 커플링(makeFtNodes 와 동일 규약) — _nodeMaint 가 두 필드를 모두 본다.
          n.mode = n.standing === 'maintenance' ? 'maintenance' : 'production';
          n.primary = false;
        }
      });
      const peer = m.nodes[1];
      m.lastNodeSwitch = {
        ts: tsOf(now), node: m.nodes[0].name, from: peer ? peer.name : null, to: m.nodes[0].name,
        desc: 'Physical machine "' + (peer ? peer.name : '') + '" is no longer normal',
      };
      const vm = (m.vmList || [])[0];
      if (vm) {
        m.lastVmSwitch = {
          ts: tsOf(now), vm: vm.name, from: peer ? peer.name : null, to: m.nodes[0].name,
          desc: 'Move virtual machine "' + vm.name + '" to ' + m.nodes[0].name,
        };
        vm.node = m.nodes[0].name;
      }
    } else {
      // 복구(→op): standing 을 'normal' 로 되돌리면 mode 도 'production' 으로 정합해야 한다.
      // 그렇지 않으면 mode='maintenance' 가 남아 _nodeMaint 가 영구 true 가 된다(#532).
      m.nodes.forEach((n, i) => { n.state = 'running'; n.standing = 'normal'; n.mode = 'production'; n.primary = i === 0; });
      (m.vmList || []).forEach((v) => { v.state = 'running'; });
    }
    m.vmRunning = (m.vmList || []).filter((v) => v.state === 'running').length;
  }

  // 복구(→op) 시 타입별 에러 meta 도 함께 지운다 — #532(FT standing/mode)와 같은 결함 클래스.
  // 두지 않으면 deg+에러 PLC 의 PLC_ERROR(critical) 경보·hasError·runState:'PROGRAM' 과
  // down+배드 디스크 NAS 의 systemStatus:'degraded'·disk ok:false 가 회복 후에도 잔류한다(#553).
  if (next === 'op') {
    if (m.plc) _recoverPlcMeta(dev, now);
    if (m.nas) _recoverNasMeta(m.nas);
  }
  // down 전이 시 EIP 도달성도 끊는다 — 빌드 시딩(online: status !== 'down')과
  // 복구 경로(#553, online = true)의 계약을 전이에도 대칭 적용.
  // 두지 않으면 down 상세에 '장비 오프라인'과 'EtherNet/IP Online(녹색)'이 병존한다(#561).
  // tagLinkRun 도 같은 계약 — 죽은 컨트롤러의 태그 데이터링크가 '실행 중'일 수 없다(#566).
  if (next === 'down' && m.plc && m.plc.sysDiag && m.plc.sysDiag.eip) {
    m.plc.sysDiag.eip.online = false;
    m.plc.sysDiag.eip.tagLinkRun = false;
  }
  // runState·cipStatus·cipMinorFault 도 down 시딩 계약(runState='' → cipStatus='' → 대시)으로
  // 내린다 — 두지 않으면 deg→down 전이 PLC 상세에 '제어 포트 미응답(OFFLINE)'과 나란히
  // 'CIP 상태: Run mode · 폴트 없음'이 잔류한다(#578). 복구는 _recoverPlcMeta(#553)가 담당.
  if (next === 'down' && m.plc) {
    m.plc.runState = '';
    m.plc.cipStatus = '';
    m.plc.cipMinorFault = false;
  }
  // win.cpuPct/memPct 도 down 시딩 계약(null)으로 내린다 — 두지 않으면 deg→down 전이 WIN
  // 상세에 '장비 오프라인'과 나란히 마지막 실측 '메모리 N GB · N%'가 잔류한다(#584).
  // 복구는 틱 갱신(dev.status !== 'down' 분기)이 dev.cpu0/mem0 로 자연 복원한다.
  if (next === 'down' && m.win) {
    m.win.cpuPct = null;
    m.win.memPct = null;
  }
  // pi.cpu/mem 도 같은 down 시딩 계약(null, makePiMeta)으로 내린다 — #584(WIN)와
  // 같은 클래스. 두지 않으면 deg→down 전이 PI 는 틱 갱신(status !== 'down' 분기)이
  // 건드리지 않아 마지막 실측 cpu/mem 이 복구까지 잔류하고, 부제 'SSH' 판정
  // (compute hwLine, cpu != null 기준)이 죽은 장비를 살아 있는 것처럼 읽는다(#596).
  // memPct 도 같은 계약 키(#595)라 함께 내린다 — 아직 시딩 전 코드와 머지돼도 null 대입은 무해.
  // tempC/throttled/throttleThermal 도 같은 클래스(#607) — 두지 않으면 deg→down 전이 PI
  // 상세에 마지막 실측 'SoC 온도 N.N°C'(≥75°C neg)·'스로틀: 과열'이 복구까지 잔류한다.
  // 복구는 cpu/mem 과 마찬가지로 틱 갱신(status !== 'down' 분기)이 자연 복원한다.
  if (next === 'down' && m.pi) {
    m.pi.cpu = null;
    m.pi.mem = null;
    m.pi.memPct = null;
    m.pi.tempC = null;
    m.pi.throttled = false;
    m.pi.throttleThermal = false;
    m.pi.throttleUnderVolt = false;
  }

  if (Array.isArray(m.snmp)) {
    m.snmp.forEach((n, i) => {
      const nodeUp = next !== 'down' && (!Array.isArray(m.nodes) || !m.nodes[i] || m.nodes[i].state === 'running');
      n.reachable = nodeUp;
      if (!nodeUp) { delete n.cpu; delete n.mem; delete n.degraded; }
      // deg 로 전이한 도달 가능 노드는 저하 플래그 유지, 그 외(op)는 해제 —
      // deriveStatus SNMP 분기가 회복(deg→op)을 정확히 반영하도록 한다.
      else if (next === 'deg') n.degraded = true;
      else delete n.degraded;
    });
  }

  if (isFT(dev.type)) dev.sync = deriveSync(m.nodes, next);
  else dev.sync = next === 'down' ? 'offline' : (next === 'deg' ? 'simplex' : 'sync');
  dev.availN = availN(next);

  const label = next === 'down' ? 'went offline' : (next === 'deg' ? 'degraded' : 'recovered');
  const sev = next === 'down' ? 'critical' : (next === 'deg' ? 'warning' : 'info');
  _pushEvent(dev, now, 'status', dev.host + ' ' + label, sev);
  _pushTrap(dev, now,
    next === 'down' ? 'linkDown on the management interface'
      : (next === 'deg' ? 'Redundancy degraded — running simplex' : 'linkUp on the management interface'),
    next === 'op' ? '1.3.6.1.6.3.1.1.5.4' : '1.3.6.1.6.3.1.1.5.3', sev);
  _syncSyntheticAlert(dev, now);
}

/** op 회복 시 PLC 에러 meta 초기화 — makePlcMeta 의 op 경로와 동일한 값으로 되돌린다(#553). */
function _recoverPlcMeta(dev, now) {
  const m = dev.meta;
  const p = m.plc;
  const hadError = !!p.hasError;
  p.runState = 'RUN'; // down('')·deg('PROGRAM') 잔류 모두 정상 운전 상태로
  p.hasError = false;
  p.errSev = '';
  p.errSince = '';
  p.errorMessage = '';
  p.cipMajorFault = false;
  // cipStatus·cipMinorFault 도 runState='RUN' 복구와 정합시킨다 — 두지 않으면
  // 'RUN · 프로그램 실행 중' 옆에 'Program mode · 마이너 폴트' 잔류(#567).
  if (p.protocol === 'EtherNet/IP') { p.cipStatus = 'Run mode'; p.cipMinorFault = false; }
  if (p.finsFatalCode) p.finsFatalCode = '0x0000';
  const sd = p.sysDiag;
  if (sd) {
    sd.ctrlErr = false;
    sd.ctrlSev = '';
    if (Array.isArray(sd.modules) && sd.modules[0]) {
      sd.modules[0].code = '0x0000'; sd.modules[0].err = false; sd.modules[0].sev = '';
    }
    if (sd.userAlarm) sd.userAlarm.active = false;
    if (sd.eip) { sd.eip.online = true; sd.eip.tagLinkRun = true; } // #561·#566 down 전이와 대칭 복구
  }
  if (hadError) {
    // errHistory 는 {at,to} 쌍으로 해소('')를 기록하는 규약(makePlcMeta 시딩과 동일).
    if (Array.isArray(p.errHistory)) p.errHistory.push({ at: tsOf(now), to: '' });
    if (Array.isArray(m.alerts)) m.alerts = m.alerts.filter((a) => !a || a.name !== 'PLC_ERROR');
  }
}

/** op 회복 시 NAS 헬스 meta 초기화 — makeNasMeta 의 op 경로와 동일한 값으로 되돌린다(#553). */
function _recoverNasMeta(nas) {
  nas.systemStatus = 'normal';
  nas.fansOk = true;
  (nas.disks || []).forEach((d) => { d.status = 'normal'; d.ok = true; });
  (nas.raid || []).forEach((r) => { r.status = 'normal'; r.ok = true; });
}

/* ===========================================================================
 * 7. /api/fleet 폴링 계약
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
  const fallbackHost = index == null ? 'node' : 'node-' + (index + 1);
  const host = String(d.host || d.id || fallbackHost).trim() || fallbackHost;
  const num = (v, dflt) => { const n = Number(v); return Number.isFinite(n) ? n : dflt; };
  const type = TYPE_KEYS.indexOf(d.type) >= 0 ? d.type : 'SRV';
  const status = STATUS_KEYS.indexOf(d.status) >= 0 ? d.status : 'op';
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
      // 폴섭 생성 시뮬 장비 표시 — 장비 관리(config CRUD)에서 숨기는 판별자.
      sim: !!meta.sim,
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

/**
 * device[] 폴링. 폐쇄망에서는 실패가 기본이므로 조용히 폴백한다(콘솔 오염 금지).
 * @param {string} [url='/api/devices']
 * @param {number} [timeoutMs=2500]
 * @returns {Promise<{ok:boolean, devices:Array, error:(string|null), refreshSec:number, polledAt:number, stale:boolean}>}
 */
export async function pull(url, timeoutMs) {
  const target = url || API_URL;
  const ms = typeof timeoutMs === 'number' ? timeoutMs : 2500;
  const fail = (msg) => ({ ok: false, devices: [], error: msg, refreshSec: 30, polledAt: 0, stale: false });

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
    if (!r || !r.ok) return fail('HTTP ' + (r ? r.status : '?'));
    const j = await r.json();
    const devices = normalize(j);
    if (!devices.length) return fail('empty fleet');
    return {
      ok: true,
      devices,
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

/* ---- 실 모드 폴백 상태(모듈 로컬) ----
   폴러가 죽었을 때 곧바로 시뮬로 갈아타면 두 가지가 나쁘다.
     ① stale 배너(source==='live' && stale 일 때만 뜬다)가 한 번도 안 보인다.
     ② 마지막 실장비 스냅샷이 fleet 에 남은 채 source 만 'sim' 이 되어,
        tickFleet 랜덤워크가 **실장비 카드의 CPU/MEM 을 흔든다**(시뮬 오염).
   그래서 유예 구간(LIVE_GRACE 회)에는 실 모드 + liveError 로 버티며 배너를 띄우고,
   유예를 넘기면 시뮬 fleet 을 새로 만들어 통째로 갈아끼운다. */
const LIVE_GRACE = 4;
let _liveFails = 0;

/* ---- 실 모드: manage 낙관 반영 보존 머지 ----
   manage 의 추가·수정은 REST 성공 직후 로컬 fleet 에 먼저 반영하는데, 다음 pull 이
   서버 스냅샷으로 fleet 을 통째로 덮어쓰면 폴리가 아직 그 변경을 모르는 동안
   (restart_required 재시작 대기 · PUT 반영 전) 추가 행이 증발하고 수정분이 되돌아갔다.
   성공 분기에서 로컬 쓰기를 서버 목록 위에 머지한다.
     · meta.pending(추가 플레이스홀더) — 서버 목록에 같은 id 가 없을 때만 끝에 보존.
       폴리가 잡아 내리기 시작하면 서버 쪽이 이기므로 자연 소멸한다(key 기준 dedupe).
       단, 무기한 보존은 아니다: #436 의 유한화(PENDING_TICKS)는 tickFleet 안이라 시뮬
       전용이고, live 에서는 폴리 재시작까지(수 시간~수 일 가능) '불러오는 중' 배지가
       고정된 채 status:'op' 장비로 kpi.total/healthyPct/플랫폼 집계를 부풀렸다(#494).
       생성 시각(meta.pendingAt, makeDevice 가 기록)이 PENDING_TTL_MS 를 넘기면 머지에서
       제외해 소멸시킨다 — 폴리가 살아나 내리기 시작하면 서버 목록 쪽으로 다시 올라온다.
       pendingAt 결측(이 수정 이전에 만들어진 잔여)은 지금부터 유예를 시작한다(즉시 증발
       금지 — 추가 직후 행이 다음 폴에 사라지는 #33 회귀로 되돌아가지 않게).
     · meta.localEdit(수정 낙관 반영, manage.js 가 실 모드 PUT 성공 시 부착) — 서버가
       수정값을 반영하기까지 그 필드를 서버 스냅샷 위에 다시 입힌다.
       서버가 반영하면 마커를 버린다(자기 청소). */
const PENDING_TTL_MS = 10 * 60 * 1000;   // live pending 플레이스홀더 TTL(폴리 재시작 소요 상한)
const LOCAL_EDIT_FIELDS = ['label', 'company', 'factory', 'mgmt', 'assetTag', 'floorPos', 'vendor'];

function mergeLocalWrites(localFleet, serverDevices) {
  const local = Array.isArray(localFleet) ? localFleet : [];
  const server = Array.isArray(serverDevices) ? serverDevices : [];
  if (!local.length) return server;
  const localById = new Map();
  for (const d of local) {
    if (d && d.id != null && !localById.has(d.id)) localById.set(d.id, d);
  }
  const serverIds = new Set();
  const merged = server.map((srv) => {
    if (!srv || srv.id == null) return srv;
    serverIds.add(srv.id);
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
  for (const d of local) {
    if (!(d && d.id != null && d.meta && d.meta.pending === true) || serverIds.has(d.id)) continue;
    // #494: TTL 만료 검사 — pendingAt 이 없으면(구형 잔여) 지금을 기준으로 유예를 새로 연다.
    const at = Number(d.meta.pendingAt);
    if (!Number.isFinite(at)) d.meta.pendingAt = Date.now();
    else if (Date.now() - at > PENDING_TTL_MS) continue;
    merged.push(d);
  }
  return merged;
}

/**
 * 스펙 §4.4 의 state 패치 형태로 감싼 헬퍼. app.js 가 그대로 setState 에 넘길 수 있다.
 */
export async function pullPatch(state, url, timeoutMs) {
  const st = state || {};
  const r = await pull(url, timeoutMs);

  if (r.ok) {
    _liveFails = 0;
    if (r.thresholds) setUsageThresholds(r.thresholds.warn, r.thresholds.crit);
    return {
      // 시뮬→실 전환(st.source!=='live')일 때는 머지하지 않는다 — 시뮬에서 추가한 장비는
      // POST 된 적이 없으므로 실 플릿에 유령으로 남기면 안 된다(통째 교체가 정상 동작).
      fleet: st.source === 'live' ? mergeLocalWrites(st.fleet, r.devices) : r.devices,
      source: 'live',
      refreshSec: r.refreshSec,
      lastPoll: r.polledAt,
      // 이벤트 이력(폴러 events[]) — 로그 화면 tail 의 정본. 활성 경보 스냅샷과 달리
      // 해소된 경보·상태 전이가 이력으로 남는다.
      liveEventLog: r.events,
      // 폴러 수집이 밀린 상태면 배너를 띄운다(데이터는 그대로 보여 준다).
      liveError: r.stale ? 'poller stale' : null,
      // 백엔드 총평 — compute 가 자체 재도출값과 대조해 더 나쁜 쪽을 택한다.
      pollerOverall: r.overall || null,
      thresholds: r.thresholds || null,
      cacheAgeSec: r.cacheAgeSec,
      // 라이브 복귀 → 시뮬 폴백 배너 해제.
      simFallback: false,
    };
  }

  _liveFails += 1;

  if (st.source === 'live') {
    if (_liveFails <= LIVE_GRACE) {
      // 유예: 마지막 실 스냅샷 유지 + stale 배너.
      return { liveError: r.error || 'unreachable' };
    }
    // 유예 초과: 시뮬로 완전 복귀(실장비 스냅샷을 tickFleet 이 오염시키지 않게 교체).
    // simFallback 플래그를 남겨, 시뮬 fleet 이 정상처럼 보여도 배너가 지속되게 한다.
    _liveFails = 0;
    return {
      fleet: buildFleet(),
      source: 'sim',
      liveError: null,
      stale: false,
      lastPoll: Date.now(),
      simFallback: true,
    };
  }

  // 처음부터 백엔드가 없는 폐쇄망: 조용히 시뮬 유지(배너 억제).
  return { source: 'sim', liveError: null };
}

/* ===========================================================================
 * 8. 시뮬 모드 로컬 CRUD 보조 (manage 화면용)
 * ======================================================================== */

/** 시뮬 모드에서 장비 1대를 새로 만든다(위저드 결과 → device). */
export function makeDevice(input) {
  const inp = input || {};
  const key = String(inp.key || inp.host || ('dev-' + Date.now())).trim();
  const type = TYPE_KEYS.indexOf(inp.type) >= 0 ? inp.type : 'EV';
  const h = mkHelpers(makeRng(hashSeed(key)));
  const now = Date.now();
  const mgmt = String(inp.mgmt || ipOf(10, 20, 10, h.ri(2, 250)));
  const ipBase = mgmt.split('.').map((x) => parseInt(x, 10) || 0);
  const meta = {
    label: String(inp.label || key),
    company: String(inp.company || ''),
    factory: String(inp.factory || ''),
    mgmt,
    assetTag: String(inp.assetTag || ''),
    vendor: '',
    error: null,
    pending: true,
    // #494: pending 유한화(live)의 나이 재료 — mergeLocalWrites 의 TTL 이 읽는다.
    pendingAt: now,
    version: '',
    alerts: [], traps: [], snmp: [], nodes: [], vmList: [], vms: 0, vmRunning: 0,
    unit: {}, license: null, lastVmSwitch: null, lastNodeSwitch: null, lastReboot: null,
    bmc: null, events: [],
  };
  if (isFT(type)) {
    const nodes = makeFtNodes(h, key, type, 'op', ipBase);
    meta.nodes = nodes;
    meta.snmp = nodes.map((n) => makeSnmpNode(h, n.ip, 'op', true, 0));
    meta.unit = makeUnit(h, type, 'op');
    meta.version = meta.unit.version;
    meta.vmList = makeVms(h, key, nodes, 'op');
    meta.vms = meta.vmList.length;
    meta.vmRunning = meta.vms;
    meta.license = makeLicense(h, now, 5);
  } else if (type === 'PLC') {
    meta.plc = makePlcMeta(h, now, 'op', mgmt);
  } else {
    meta.snmp = [makeSnmpNode(h, mgmt, 'op', true, 0)];
    if (type === 'NAS') meta.nas = makeNasMeta(h, 'op');
    if (type === 'WIN') meta.win = makeWinMeta(h, key, 'op');
    if (type === 'PI') meta.pi = makePiMeta(h, 'op');
    if (type === 'PC') meta.pc = makePcMeta(h, key, 'op');
  }
  const noTel = isNoTel(type);
  const cpu = noTel ? -1 : (meta.snmp[0] ? meta.snmp[0].cpu : -1);
  const mem = noTel ? -1 : (meta.snmp[0] ? meta.snmp[0].mem : -1);
  return {
    id: key, host: key, type,
    site: String(inp.site || '—'),
    status: 'op', availN: 99.99,
    cpu0: cpu == null ? -1 : cpu, mem0: mem == null ? -1 : mem,
    cpuNA: cpu == null || cpu < 0, memNA: mem == null || mem < 0,
    sync: 'sync', uptime: 0, live: true, meta,
    histCpu: [], histMem: [], histRtt: [],
  };
}

/** 현재 시뮬 시드(디버그/재현용). */
export const currentSeed = () => _simSeed;

export default {
  TYPES, FT_TYPES, TYPE_KEYS, STATUS_KEYS, SYNC_KEYS,
  isFT, isNoTel, makeRng, hashSeed, clamp, tsOf, licDate, classify,
  deriveStatus, deriveSync, availN,
  buildFleet, tickFleet, normalize, normalizeDevice, pull, pullPatch,
  makeDevice, currentSeed, SIM_SEED,
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

const ESCAL_URL = '/escal';
const NOTIFY_URL = '/notify';

/**
 * 에스컬레이션 클레임 — 서버가 락 안에서 add-if-absent 로 병합하고, 실제로 새로 들어간
 * 키만 added 로 돌려준다. 콘솔이 여러 개 열리든 added 를 받은 한 쪽만 웹훅을 쏜다(중복 발송 방지).
 * 실패하면 null(다음 틱에 다시 시도).
 */
export async function claimEscal(keys, timeoutMs) {
  if (typeof fetch !== 'function' || !keys || !keys.length) return null;
  const set = {};
  const iso = new Date().toISOString();
  keys.forEach((k) => { set[k] = iso; });
  let timer = null;
  try {
    const opt = {
      method: 'PUT', cache: 'no-store',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ set }),
    };
    if (typeof AbortController === 'function') {
      const ctrl = new AbortController();
      timer = setTimeout(() => { try { ctrl.abort(); } catch (_) { /* noop */ } }, timeoutMs || 2500);
      opt.signal = ctrl.signal;
    }
    const r = await fetch(ESCAL_URL, opt);
    if (!r || !r.ok) return null;
    const j = await r.json();
    return (j && Array.isArray(j.added)) ? j.added : null;
  } catch (e) {
    return null;
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/** 웹훅 발송 — serve.py /notify 가 Slack(text)·Discord(content) 양식으로 중계한다. */
export async function sendWebhook(url, text, timeoutMs) {
  if (typeof fetch !== 'function' || !url || !text) return false;
  let timer = null;
  try {
    const opt = {
      method: 'POST', cache: 'no-store',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url, text }),
    };
    if (typeof AbortController === 'function') {
      const ctrl = new AbortController();
      timer = setTimeout(() => { try { ctrl.abort(); } catch (_) { /* noop */ } }, timeoutMs || 3000);
      opt.signal = ctrl.signal;
    }
    const r = await fetch(NOTIFY_URL, opt);
    if (!r || !r.ok) return false;
    const j = await r.json().catch(() => null);
    return !!(j && j.ok);
  } catch (e) {
    return false;
  } finally {
    if (timer) clearTimeout(timer);
  }
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
