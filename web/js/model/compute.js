// js/model/compute.js — F3 파생 계산
// ---------------------------------------------------------------------------
// Vigil `src/model/compute.ts` + `buildModel.ts` + `detail.ts` + `topo.ts` 이식.
// REBUILD-SPEC.md §4.5 계약.
//
// 원칙
//  - 순수 함수만. DOM 접근 0건. 콜백(onClick) 반환 금지 — 화면은 id/데이터만 받아 위임 처리한다.
//  - 데이터 조작 금지: 결측은 NA 플래그와 '—' 텍스트로만 표기한다(0으로 채우지 않는다).
//  - 색은 semantic tone 문자열('pos'|'warn'|'neg'|'info'|'mut') 또는 명시적인 categorical
//    company/custom color만 내보낸다. 화면이 tone을 CSS 변수로 해석해 paint한다.
//  - derive_status/derive_sync/availN 은 로직 분기를 막기 위해 model/data.js 의 것을 재사용한다.
// ---------------------------------------------------------------------------

import {
  TYPES, FT_TYPES, TYPE_KEYS, STATUS_KEYS, SYNC_KEYS,
  isFT, isNoTel, deriveStatus, deriveSync, availN,
} from './data.js';
import { COLOR, usageThresholds } from '../util/fmt.js';

/* 재수출 — 화면/다른 모듈이 compute 하나만 import 해도 되도록. */
export {
  TYPES, FT_TYPES, TYPE_KEYS, STATUS_KEYS, SYNC_KEYS,
  isFT, isNoTel, deriveStatus, deriveSync, availN, COLOR,
};

/* ===========================================================================
 * 0. 기본 소도구
 * ======================================================================== */

export function clamp(v, a, b) {
  const n = Number(v);
  if (!Number.isFinite(n)) return a;
  return Math.max(a, Math.min(b, n));
}

const _meta = (s) => (s && s.meta) || {};
const _arr = (v) => (Array.isArray(v) ? v : []);
const _num = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
const DASH = '—';

/** 지문용 간이 문자열 해시(djb2, 32bit) — 길이가 아니라 내용이 바뀌면 달라진다(#279). */
const _strHash = (s) => {
  let h = 5381;
  for (let i = 0; i < s.length; i += 1) h = (((h << 5) + h) + s.charCodeAt(i)) | 0;
  return (h >>> 0).toString(36);
};

/** 한글 우선 자연 정렬(스펙 §7: localeCompare(_, 'ko', {numeric:true})). */
export function cmpKo(a, b) {
  return String(a == null ? '' : a).localeCompare(String(b == null ? '' : b), 'ko', { numeric: true });
}

/** state.lang 판정. */
export function langOf(state) {
  return (state && state.lang) === 'en' ? 'en' : 'ko';
}

/** state 기준 L(en, ko) 생성기(모듈 전역 언어에 의존하지 않는 순수 버전). */
export function makeL(state) {
  const ko = langOf(state) === 'ko';
  return (en, k) => (ko ? k : en);
}

/* ===========================================================================
 * 1. 상태/동기화/타입 표기 헬퍼
 * ======================================================================== */

/** 상태 → 상태색 tone. */
export function statusTone(status) {
  return status === 'down' ? 'neg' : (status === 'deg' ? 'warn' : 'pos');
}


/** 상태 → 라벨(en/ko). */
export function statusLabel(status, L) {
  if (status === 'down') return L('Offline', '오프라인');
  if (status === 'deg') return L('Degraded', '저하');
  return L('Operational', '가동');
}

/** deg=pulse, down=blink (공용 애니 클래스명, 스펙 §5.5). */
export function statusAnim(status) {
  return status === 'down' ? 'blink' : (status === 'deg' ? 'pulse' : '');
}

/** 0~100 사용률 → 임계 tone (기본 78 warn / 90 neg — 서버 thresholds 로 갱신). */
export function pctTone(v) {
  const n = _num(v);
  if (n == null) return 'mut';
  const t = usageThresholds();
  if (n >= t.crit) return 'neg';
  if (n >= t.warn) return 'warn';
  return 'pos';
}

/** 커밋(할당) 비율 톤 — 게이지 색 단일 규칙(fmt.barColor 78/90)과 동일 임계로 통일(재심사 반영):
 *  한 행 안에서 % 텍스트와 바가 서로 다른 상태를 말하던 이원화를 제거한다.
 *  pctTone 과 임계가 완전히 같아 별칭으로 둔다(테스트가 import 하므로 export 유지). */
export const pctToneAlloc = pctTone;

/** icon.js 레지스트리에 실제 등록된 이름으로 타입 아이콘 매핑(ph-* 는 미등록이라 폴백 경고가 난다). */
const TYPE_ICON = {
  EV: 'box', EDGE: 'link', END: 'db', FTS: 'ssd', SRV: 'db',
  PLC: 'crop', PC: 'dash', NAS: 'ssd', WIN: 'checklist', PI: 'bolt', PRN: 'checklist',
};
export function typeIconOf(type) {
  return TYPE_ICON[type] || 'box';
}

/** 타입 메타(레지스트리 + 아이콘 보정). */
export function typeInfo(type, L) {
  const t = TYPES[type] || { label: type, short: type, kindEN: '', kindKO: '' };
  return {
    key: type,
    // 일반명사 타입(서버)만 i18n. everRun/ztC Edge/NAS/PLC 등은 고유명사라 label 고정.
    label: (L && t.labelEN) ? L(t.labelEN, t.label) : t.label,
    short: t.short,
    kind: L ? L(t.kindEN || '', t.kindKO || '') : (t.kindKO || t.kindEN || ''),
    icon: typeIconOf(type),
  };
}

/** 0%인데 소수 실부하 신호가 있으면 '<1%'로 구분(유휴 ≠ 미수집). PLC 응답시간 '<1 ms' 표기와
 *  동일 규약. 이 파일에서 '<1%' 문자열을 만드는 유일한 지점 — usageOf(장비 CPU/MEM)와
 *  nodeRow(노드별 cpu_pct, buildDetail §9)가 둘 다 이 함수를 거친다(이전엔 두 곳에 따로
 *  구현돼 같은 장비가 화면마다 다른 값을 보였다). */
function subPctText(val, sub) {
  return (val === 0 && _num(sub) > 0) ? '<1%' : val + '%';
}

/** 장비의 CPU/MEM 표기 계산(결측은 '—'). */
export function usageOf(dev, field) {
  const na = field === 'cpu'
    ? (dev.cpuNA || dev.cpu0 < 0 || dev.status === 'down' || isNoTel(dev.type))
    : (dev.memNA || dev.mem0 < 0 || dev.status === 'down' || isNoTel(dev.type));
  const raw = field === 'cpu' ? dev.cpu0 : dev.mem0;
  const val = na ? null : Math.round(clamp(raw, 0, 100));
  // CPU 만 소수 실부하 신호(meta.srv.cpuPct1, Proxmox 전용 poll_proxmox 산출)가 있다.
  const srv = _meta(dev).srv;
  const sub = field === 'cpu' && srv ? srv.cpuPct1 : null;
  return {
    na,
    val,
    text: na ? DASH : subPctText(val, sub),
    width: (na ? 0 : val) + '%',
    tone: na ? 'mut' : pctTone(val),
  };
}

/** sync 표기 — FT 타입은 sync/simplex/offline, 그 외는 online/offline. */
export function syncInfo(dev, L) {
  if (!isFT(dev.type)) {
    return dev.status === 'down'
      ? { key: 'offline', label: L('Offline', '오프라인'), tone: 'neg', icon: 'link' }
      : { key: 'online', label: L('Online', '온라인'), tone: 'pos', icon: 'check' };
  }
  if (dev.sync === 'sync') return { key: 'sync', label: L('In sync', '동기화됨'), tone: 'pos', icon: 'cycle' };
  if (dev.sync === 'simplex') return { key: 'simplex', label: L('Simplex', '심플렉스'), tone: 'warn', icon: 'warningCircle' };
  return { key: 'offline', label: L('Offline', '오프라인'), tone: 'neg', icon: 'link' };
}

/** 노드 하나가 점검(maintenance) 중인지 — standing/mode 두 필드를 본다.
 *  이 판정은 아래 7곳(isMaint, maintCnt, buildDetail·토폴로지 노드 행 등)이 공유한다. */
function _nodeMaint(n) {
  return /mainten/i.test(((n && n.standing) || '') + ' ' + ((n && n.mode) || ''));
}

/** 점검(maintenance) 중인 노드가 하나라도 있는지. */
export function isMaint(dev) {
  return _arr(_meta(dev).nodes).some(_nodeMaint);
}

/** 가용성 숫자 → 문자열(99.990%). */
export function fmtAvailN(a) {
  const n = Number(a);
  if (!Number.isFinite(n)) return DASH;
  // 소수 3자리 고정(자릿수 축 통일, 심사 반영) + 내림 — 99.9996 같은 값이 반올림으로
  // "100.000%"가 되면 불완전 장비가 만점으로 보인다(리뷰 지적). 100 미만은 절대 100 표기 금지.
  if (n < 100) return (Math.floor(n * 1000) / 1000).toFixed(3) + '%';
  return '100.000%';
}

/** 가용성(%) → SLA 편차 = 연간 허용 다운타임 예산. 99.990%/99.900%/99.000% 처럼 소수점만 다른
 *  '거의 같아 보이는' 숫자를, 실제 의미(연 53분 / 8.8시간 / 3.7일)로 풀어 등급 간 차이를 드러낸다(E5). */
export function fmtDowntimeYr(a, L) {
  const n = Number(a);
  const l = typeof L === 'function' ? L : ((en) => en);
  if (!Number.isFinite(n) || n >= 100) return l('0/yr', '연 0분');
  const mins = (100 - n) / 100 * 525960; // 1년 = 525,960분
  if (mins < 90) return l(Math.round(mins) + 'm/yr', '연 ' + Math.round(mins) + '분');
  const hrs = mins / 60;
  if (hrs < 48) return l(hrs.toFixed(1) + 'h/yr', '연 ' + hrs.toFixed(1) + '시간');
  return l((hrs / 24).toFixed(1) + 'd/yr', '연 ' + (hrs / 24).toFixed(1) + '일');
}

/** 장비 라벨('대원정밀 EDGE-27')에서 회사 프리픽스를 벗긴 고유 장비코드('EDGE-27'). 프리픽스가
 *  없으면(everRun 8.1.0.2-19 등) 라벨을 그대로 쓴다 — 절단에서 살아남아야 할 토큰을 우선한다(E1). */
function deviceCode(label, company) {
  let s = String(label == null ? '' : label).trim();
  const co = String(company == null ? '' : company).trim();
  if (co && s.indexOf(co) === 0) s = s.slice(co.length).trim();
  return s || String(label == null ? '' : label).trim();
}

/** 업타임(일) → '0d'|'<1d'|'34d'. */
export function fmtUptimeD(d) {
  const n = Number(d);
  if (!Number.isFinite(n) || n < 0) return DASH;
  if (n === 0) return '0d';
  // 0~1일 구간을 실제와 무관한 '12h' 고정으로 표기하던 것을 정직한 근사로 고친다.
  if (n < 1) return '<1d';
  return Math.floor(n) + 'd';
}

/* ===========================================================================
 * 2. 시간 파싱/포맷 (Vigil buildModel.ts 이식)
 * ======================================================================== */

const _pad2 = (n) => String(n).padStart(2, '0');

/** 'YYYY-MM-DDTHH:MM:SSZ' 등 → 'YYYY-MM-DD HH:MM:SS'. */
export function tsNorm(t) {
  return String(t == null ? '' : t).replace('T', ' ').replace('Z', '');
}

function _nowStamp() {
  const d = new Date();
  return d.getFullYear() + '-' + _pad2(d.getMonth() + 1) + '-' + _pad2(d.getDate()) +
    ' ' + _pad2(d.getHours()) + ':' + _pad2(d.getMinutes()) + ':' + _pad2(d.getSeconds());
}

/** 타임스탬프 문자열 → epoch ms (없으면 0). data.js tsOf 는 로컬 시각을 쓰므로 로컬로 파싱한다. */
export function tsKey(ts) {
  const s = String(ts == null ? '' : ts).trim().replace(' ', 'T');
  if (!s) return 0;
  const d = new Date(s);
  return isNaN(d.getTime()) ? 0 : d.getTime();
}

/** 경과 초(미래/파싱실패는 null). */
export function agoSec(ts) {
  const k = tsKey(ts);
  if (!k) return null;
  const sec = Math.floor((Date.now() - k) / 1000);
  return sec < 0 ? null : sec;
}

/** 경과 초 → '3분 전' / '3m ago'. */
export function agoText(sec, L, ko) {
  if (sec == null) return '';
  if (sec < 60) return L('just now', '방금');
  const mi = Math.floor(sec / 60);
  if (mi < 60) return ko ? mi + '분 전' : mi + 'm ago';
  const h = Math.floor(mi / 60);
  if (h < 24) return ko ? h + '시간 전' : h + 'h ago';
  return ko ? Math.floor(h / 24) + '일 전' : Math.floor(h / 24) + 'd ago';
}

/** 로그 콘솔용 짧은 시각(오늘은 HH:MM:SS, 그 외 MM-DD HH:MM). */
export function shortTime(t, todayStr) {
  const s = String(t == null ? '' : t);
  const dm = /(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2}):(\d{2})/.exec(s);
  if (!dm) {
    const m = /(\d{2}):(\d{2}):(\d{2})/.exec(s);
    return m ? m[0] : s.slice(-8);
  }
  const day = dm[1] + '-' + dm[2] + '-' + dm[3];
  return day === todayStr ? (dm[4] + ':' + dm[5] + ':' + dm[6]) : (dm[2] + '-' + dm[3] + ' ' + dm[4] + ':' + dm[5]);
}

function _todayStr() {
  const d = new Date();
  return d.getFullYear() + '-' + _pad2(d.getMonth() + 1) + '-' + _pad2(d.getDate());
}

/* ---- 라이선스 날짜 ('Mon Jun 29 17:01:47 KST 2026') ---- */
const _MO = { Jan: '01', Feb: '02', Mar: '03', Apr: '04', May: '05', Jun: '06', Jul: '07', Aug: '08', Sep: '09', Oct: '10', Nov: '11', Dec: '12' };

/** avcli 라이선스 날짜 → Date (실패 null). */
export function parseLicDate(str) {
  if (!str) return null;
  const mm = String(str).match(/(\w{3})\s+(\d+)\s+[\d:]+\s+\w+\s+(\d{4})/);
  const d = mm
    ? new Date(mm[3] + '-' + (_MO[mm[1]] || '01') + '-' + String(mm[2]).padStart(2, '0') + 'T00:00:00Z')
    : new Date(str);
  return isNaN(d.getTime()) ? null : d;
}

/** avcli 라이선스 날짜 → 'YYYY-MM-DD'. */
export function fmtLicDate(str) {
  if (!str) return '';
  const mm = String(str).match(/(\w{3})\s+(\d+)\s+[\d:]+\s+\w+\s+(\d{4})/);
  if (!mm) return String(str);
  return mm[3] + '-' + (_MO[mm[1]] || '01') + '-' + String(mm[2]).padStart(2, '0');
}

/** 남은 일수 → 'D-12' / '만료 D+3'. */
export function ddayText(days, L, ko) {
  if (days == null) return '';
  return days < 0 ? (ko ? '만료 D+' + (-days) : 'D+' + (-days) + ' overdue') : ('D-' + days);
}

export function licTone(days) {
  // red는 실장애 전용 — 만료 임박(D-N)은 앰버, 실제 만료(D+)만 neg. 정상은 중립(장식 색 금지).
  if (days == null) return 'mut';
  if (days < 0) return 'neg';
  if (days <= 60) return 'warn';
  return 'mut';
}

/* ===========================================================================
 * 3. 심각도 메타
 * ======================================================================== */

export const SEV_RANK = { critical: 0, warning: 1, info: 2 };

/** 심각도 → {tone, level, label, icon}. */
/** 이 일수 이상 미확인이면 '장기 방치'로 본다 — 실측상 활성 경보 31건이 전부 10~41일이었다. */
export const STALE_ALERT_DAYS = 7;

export function sevInfo(sev, L) {
  const k = sev === 'critical' || sev === 'warning' ? sev : 'info';
  if (k === 'critical') return { key: 'critical', tone: 'neg', level: 'ERROR', label: L('CRITICAL', '심각'), icon: 'warningCircle' };
  if (k === 'warning') return { key: 'warning', tone: 'warn', level: 'WARN', label: L('WARNING', '경고'), icon: 'warningCircle' };
  return { key: 'info', tone: 'info', level: 'INFO', label: L('INFO', '정보'), icon: 'infoCircle' };
}

/* ===========================================================================
 * 4. 이력(hist) 주입
 * ======================================================================== */

/** state.hist 우선, 없으면 device.histCpu/… 로 폴백. NA 샘플은 애초에 push되지 않는다. */
export function histOf(state, dev, field) {
  const h = (state && state.hist && state.hist[dev.id]) || null;
  if (h && Array.isArray(h[field]) && h[field].length) return h[field];
  const own = field === 'cpu' ? dev.histCpu : (field === 'mem' ? dev.histMem : dev.histRtt);
  return _arr(own);
}

/** 스파크라인용: 표본이 2개 미만이면 현재값으로 평평하게 채운다(선이 안 그려지는 문제 방지). */
function _sparkHist(list, cur) {
  if (Array.isArray(list) && list.length > 1) return list;
  const v = _num(cur);
  return v == null ? [] : [v, v];
}

/* ===========================================================================
 * 5. 정렬 유틸 (nodes 테이블)
 * ======================================================================== */

/** fleetRows 정렬. key = host|type|site|cpu|mem|sync|avail|uptime */
const STATUS_RANK = { down: 0, deg: 1, op: 2 };
export function sortRows(rows, key, dir) {
  const k = key || 'host';
  const sign = dir === 'desc' ? -1 : 1;
  const numCmp = (a, b) => {
    if (a == null && b == null) return 0;
    if (a == null) return -1;
    if (b == null) return 1;
    return a - b;
  };
  const out = rows.slice();
  out.sort((a, b) => {
    let r = 0;
    switch (k) {
      case 'cpu': r = numCmp(a.cpuVal, b.cpuVal); break;
      case 'mem': r = numCmp(a.memVal, b.memVal); break;
      case 'avail': r = numCmp(a.availN, b.availN); break;
      case 'uptime': r = numCmp(a.uptimeDays, b.uptimeDays); break;
      case 'type': r = cmpKo(a.typeLabel, b.typeLabel); break;
      case 'site': r = cmpKo(a.site, b.site); break;
      case 'sync': r = cmpKo(a.syncLabel, b.syncLabel); break;
      // 장비 상태(op/deg/down) 순위 — SEV_RANK(critical/warning/info)는 키가 안 맞아 전부 0으로
      // 평준화돼 정렬이 사실상 물력했다(알 수 없는 키는 뒤로).
      case 'status': r = (STATUS_RANK[a.status] == null ? 3 : STATUS_RANK[a.status]) -
        (STATUS_RANK[b.status] == null ? 3 : STATUS_RANK[b.status]); break;
      default: r = cmpKo(a.host, b.host);
    }
    if (r === 0) r = cmpKo(a.host, b.host);
    return r * sign;
  });
  return out;
}

/* ===========================================================================
 * 6. 경보/트랩 수집 (Vigil buildModel.ts liveAlerts/liveTraps)
 * ======================================================================== */

// 경보 유형(name) → 한글 요약. 실장비 폴러(everRun)와 시뮬 템플릿의 경보 desc 는 전부 영문이라,
// 라벨/필터/상태가 한글인 카드 본문만 영문으로 남아 판독 마찰이 생겼다(E3). 유형 키(name)로 한글
// 요약을 매핑하고, 원문(desc)은 카드 툴팁·로그 tail 에 그대로 보존한다(정보 손실 없음). 매핑에 없는
// 유형(동적 포트/노드명이 섞인 원시 트랩 등)은 원문을 그대로 노출한다.
const ALERT_KO = {
  // data.js 시뮬 템플릿(name)
  DISK_PRESSURE: '공유 볼륨 그룹 저장 용량 부족',
  NIC_NO_LINK: '네트워크 인터페이스 링크 없음',
  VM_MIGRATED: 'VM 이 피어 노드로 이전됨',
  FIRMWARE_AVAILABLE: '펌웨어 업데이트 사용 가능',
  SYNC_STARTED: '미러 재동기화 시작',
  PSU_REDUNDANCY: '전원 이중화 일시 상실',
  NTP_DRIFT: 'NTP 시계 편차 감지',
  NODE_MAINTENANCE: '노드 점검 모드 진입',
  // 실장비(everRun) 경보 name
  'Node Maintenance': '노드 점검 중',
  'Node Unreachable': '노드 연결 불가',
  'Node rebooted unexpectedly': '노드 비정상 재부팅',
  'unit_isSyncing': '유닛 미러 동기화 중',
  'unit_tooFew10GSyncLinks': '10G 동기화 A-Link 부족',
  'unit_testAlert': '테스트 경보',
  'E-Alert Notification Not Enabled': 'E-Alert 알림 미설정',
  'Call Home Not Enabled': 'Call Home 미설정',
  'The quorum server is offline': '쿼럼 서버 오프라인',
  'Detection of Disconnected Network': '네트워크 연결 끊김 감지',
  'Detection of Bad Network': '네트워크 불량 감지',
  'Detection of Degraded Network': '네트워크 저하 감지',
  'Detection of Slow Business Network': '업무 네트워크 속도 저하 감지',
  'A Shared Network is miswired': '공유 네트워크 배선 오류',
  'VM failed to start': 'VM 시작 실패',
  'Disk problem': '디스크 문제',
  'License Outdated': '라이선스 갱신 필요',
  'Single PM Detected': '단일 PM 감지 — 이중화 불가',
};

/** 경보 유형 → 한글 요약(없으면 null → 호출부가 원문 유지). ko 일 때만 쓴다. */
function alertMsgKo(name, desc) {
  const key = String(name || '').trim();
  // DEVICE_STATE(합성 상태 경보)는 offline/degraded 두 변형을 desc 로 가른다.
  if (key === 'DEVICE_STATE') {
    return /offline|not responding|오프라인|응답/i.test(String(desc || ''))
      ? '장비 오프라인 — 응답하는 노드 없음'
      : '장비 저하 — 이중화 상실 또는 노드 비정상';
  }
  return ALERT_KO[key] || null;
}

// 검증용 임시 픽스처 호스트(예: srv-evt-test) — 실장비가 아니다. 하드 삭제 대신 플래그만
// 세워 화면이 숨길지 고르게 한다(데이터 손실 금지). buildModel·autoAckDue 가 공용.
const TEST_FIXTURE_RE = /^srv-evt-test$/i;

// 최초 발생 시각 보존(폴링마다 '방금'으로 리셋되지 않도록). 복구된 항목은 매 실행마다 정리.
// 키: 합성 상태 경보는 `장비id|down` / `장비id|deg`, time 결측 폴리 경보는
// `장비id|alert|name|desc`(#50 — 매 빌드 now 대체 시 ack 키가 폴리마다 바뀌어 확인이 풀렸다).
const _onsetMap = Object.create(null);

/* time 결측 폴리 경보의 안정 onset 키 재료('alert|name|desc') — collectAlerts 의 표시용
   time(onset 맵) 전용. ack 키는 #365 이후 onset 이 아니라 ACK_TIME_MISSING 을 쓴다(아래). */
const _alertOnsetKind = (a) => 'alert|' + (a.name || '') + '|' + (a.desc || a.name || '');

/* #365: time 결측 폴리 경보의 ack 키 time 자리 고정값 — 키에 세션 로컬 onset(첫 목격 시각)을
   싣던 자리다. 세션마다 다른 시각이라 리로드 시 확인이 풀리고 /ack 공유가 성립하지 않았다.
   표시용 time 은 onset 을 유지하되, 키 재료는 세션 무관 고정 문자열로 분리한다.
   (tsNorm 은 실 시각을 보존하므로 이 값과 충돌하지 않는다.) */
const ACK_TIME_MISSING = 'no-time';

function collectAlerts(fleet, L) {
  const now = _nowStamp();
  const seen = Object.create(null);
  // 키는 장비id + 상태·경보 식별자까지 포함한다(위 설계 주석). 단일 매개변수로 선언된 채
  // 호출부만 2인자 호출이던 동안은 두 번째 인자가 소실돼 같은 장비의 무관한 경보(다른 name 의
  // 폴리 경보, 합성 down/deg)가 서로의 낡은 onset 을 상속했다(#278 — #50 수정 회귀).
  const onset = (id, kind) => {
    const key = id + '|' + kind;
    seen[key] = 1;
    if (!_onsetMap[key]) _onsetMap[key] = now;
    return _onsetMap[key];
  };

  const alerts = [];
  fleet.forEach((s) => {
    const m = _meta(s);
    // G15: 자산코드 없는 라벨(예: 실장비 'ztC Edge')은 관리 IP 를 병기 — 같은 장비의 심각
    //     이벤트 6건이 전부 동일 문자열로 읽혀 코드 표기 행(EDGE-23 등)과 식별성이 갈렸다.
    //     숫자(코드/버전)가 이미 든 라벨은 그대로 둔다(everRun 8.1.0.2-19, EV-03 …).
    const label = (m.label || s.host)
      + ((!/\d/.test(m.label || s.host) && m.mgmt) ? ' · ' + m.mgmt : '');
    const al = _arr(m.alerts);
    al.forEach((a) => {
      const tNorm = tsNorm(a.time);
      // #398: 합성 DEVICE_STATE(data.js::_syncSyntheticAlert 가 세션 시각으로 기록)은 a.time 이
      // 세션마다 갈려 그대로 키 재료가 되면 확인이 리로드에 소실된다 — 키 재료는 폴리 안정 원천
      // (meta.downSince/issueSince)을 우선 쓰고, 없으면 고정값으로 내린다(표시 time 은 그대로).
      const synState = a.name === 'DEVICE_STATE';
      alerts.push({
        host: label, hostId: s.id, id: s.id,
        sev: a.sev || a.severity || 'info',
        // name 을 보존해 화면 카드에서 한글 유형 요약(alertMsgKo)으로 매핑한다(E3). desc(원문)는 그대로.
        name: a.name || '',
        desc: a.desc || a.name || '',
        // #50: time 결측(실 폴리 경보)을 매 빌드 now 로 대체하면 ack 복합키(…+time)가
        // 폴리마다 바뀌어 '확인'이 다음 빌드에 풀린다 — 장비·경병만 안정 onset 을 쓴다.
        // autoAckDue·escalDue 도 같은 collectAlerts 를 타므로 함께 안정화된다.
        time: tNorm || onset(s.id, _alertOnsetKind(a)),
        // #365: ack 키의 시각 재료는 표시용 time 과 분리한다 — time 결측이면 세션 로컬
        // onset 대신 고정값(ACK_TIME_MISSING)이라 리로드·다중 콘솔 간에도 키가 같다.
        ackTime: synState
          ? (tsNorm(m.downSince) || tsNorm(m.issueSince) || ACK_TIME_MISSING)
          : (tNorm || ACK_TIME_MISSING),
        // 검증용 임시 픽스처 호스트는 플래그만 세운다(#31) — decorate(::testFixture)를 거쳐
        // 화면 카드 목록·CSV 가 걸러내고, 카운트·판정 파생(realAlerts)도 같은 플래그로 뺀다(#265).
        testFixture: TEST_FIXTURE_RE.test(s.id),
      });
    });
    // data.js::_syncSyntheticAlert 가 down/deg 장비의 meta.alerts 맨 앞에 이미 합성 경보
    // (name:'DEVICE_STATE')를 넣어둔다. 위 al.forEach 가 그걸 이미 push 했으므로
    // 여기서 또 만들면 같은 내용이 2건이 되어 incStats/배지/로그가 전부 부풀려진다.
    const hasSynth = al.some((a) => a && a.name === 'DEVICE_STATE');
    if (s.status === 'down' && !hasSynth) {
      alerts.push({
        host: label, hostId: s.id, id: s.id, sev: 'critical',
        // ack 키 재료(host+name+desc+time)는 언어 중립이어야 한다 — L() 로 만든 desc 는
        // 언어 전환 순간 키가 달라져 확인(ack)·자동확인 상태가 풀렸다(#49). data.js::
        // _syncSyntheticAlert 와 같은 중립 문구 + name 을 싣고, 한글 표기는
        // alertMsgKo('DEVICE_STATE') 가 담당한다(표시 한글화는 변함없음).
        name: 'DEVICE_STATE',
        desc: 'Device offline — no node responded to the collector',
        time: tsNorm(m.downSince) || onset(s.id, 'down'),
        // #398: ack 키 재료 — 폴리 안정 원천(downSince)이 있으면 그것을 써 다음 번 다운과
        // 사건이 구분되고, 없으면 고정값으로 낸다(세션 onset 폴핵은 리로드 시 확인 소실).
        ackTime: tsNorm(m.downSince) || ACK_TIME_MISSING,
        testFixture: TEST_FIXTURE_RE.test(s.id),
      });
    } else if (s.status === 'deg' && !al.length) {
      alerts.push({
        host: label, hostId: s.id, id: s.id, sev: 'warning',
        name: 'DEVICE_STATE',
        desc: 'Device degraded — redundancy lost or a node is not normal',
        time: tsNorm(m.issueSince) || onset(s.id, 'deg'),
        // #398: down 분기와 같은 계약 — issueSince 우선, 없으면 고정값.
        ackTime: tsNorm(m.issueSince) || ACK_TIME_MISSING,
        testFixture: TEST_FIXTURE_RE.test(s.id),
      });
    }
  });
  Object.keys(_onsetMap).forEach((k) => { if (!seen[k]) delete _onsetMap[k]; });

  // 심각도 우선 → 같은 심각도 내 최신순 (CRITICAL 이 신규 INFO 밑에 숨지 않도록).
  alerts.sort((a, b) =>
    ((SEV_RANK[a.sev] == null ? 3 : SEV_RANK[a.sev]) - (SEV_RANK[b.sev] == null ? 3 : SEV_RANK[b.sev])) ||
    String(b.time || '').localeCompare(String(a.time || '')));
  return alerts;
}

/* 경보 확인(ack) 복합 키 — buildModel 안 장식(decorate)과 같은 규약. host+name+desc+time.
   모듈 수준으로 둬 자동 확인(autoAckDue)과 화면 장식이 키 생성을 한 곳에서만 하게 한다. */
export function alertAckKey(hostId, name, desc, time) {
  return [hostId || '', name || '', desc || '', time || ''].join('\u0001');
}

/**
 * 오래된 미확인 경보의 자동 확인(아카이브) 대상 키 목록 — 순수 함수(DOM/상태 불변).
 * setg.ackAutoDays(0=해제 · 7 · 30 일)보다 오래 미확인으로 남은 경보를 골라낸다.
 * 배지/카운트가 수십 일 된 미확인으로 영구 고정돼 '늘 9'가 되는 것을 막는다.
 * 확인은 표시일 뿐 원본 경보는 지우지 않는다(설정에서 해제하거나 '확인 해제'로 되돌릴 수 있다).
 * 검증용 임시 픽스처 호스트(TEST_FIXTURE_RE)는 화면 목록과 마찬가지로 제외한다.
 */
export function autoAckDue(state) {
  const days = Number(state && state.setg && state.setg.ackAutoDays);
  if (days !== 7 && days !== 30) return [];
  const ackMap = (state && state.ackedAlerts) || {};
  const cutoff = days * 86400;
  const due = [];
  collectAlerts(_arr(state && state.fleet), makeL(state || {})).forEach((a) => {
    if (TEST_FIXTURE_RE.test(a.hostId || '')) return;
    // #365: 키의 시각 재료는 ackTime(세션 무관) — 나이(agoSec)는 표시와 같은 onset 기준을 유지.
    const key = alertAckKey(a.hostId, a.name, a.desc, a.ackTime || a.time);
    if (ackMap[key]) return;
    const sec = agoSec(a.time);
    if (sec != null && sec >= cutoff) due.push(key);
  });
  return due;
}

/** RFC4180 최소 CSV — 셀은 항상 따옴표로 감싸고 내부 따옴표는 두 겹으로(순수 · 테스트 가능). */
export function toCsv(headers, rows) {
  const cell = (v) => '"' + String(v == null ? '' : v).replace(/"/g, '""') + '"';
  return [headers.map(cell).join(',')]
    .concat((rows || []).map((r) => r.map(cell).join(',')))
    .join('\r\n');
}

/**
 * 활성 점검 창 맵(deviceId → {until,note,by,ts}) — 순수 함수(DOM/상태 불변).
 * 만료(until ≤ now)·파싱 불가 창은 걸러낸다. 확인(ack)과는 다른 개념이다:
 * ack 는 '봤다'는 표시, 점검 창은 '지금은 이 장비를 만지는 중'이라 그 장비의
 * 경보·상태를 활성 판정(배지·카운트·주의 필요)에서 빼는 묵음 창. 원본은 지우지 않는다.
 */
export function activeMaint(state, nowMs) {

  const out = {};
  const now = (nowMs == null ? Date.now() : nowMs);
  const src = (state && state.maint) || {};
  Object.keys(src).forEach((id) => {
    const w = src[id] || {};
    const t = Date.parse(w.until || '');
    if (!isNaN(t) && t > now) out[id] = w;
  });
  return out;
}

/**
 * 에스컬레이션 대상 — setg.escHours(0=해제 · 4 · 24 시간) 이상 미확인으로 방치된 critical 경보.
 * 확인분·점검 창 묵음·픽스처는 제외한다. 반환은 {key, host, desc, time, ageSec} — 키로 클레임하고
 * 내용으로 웹훅 문구를 만든다. 순수 함수(DOM/상태 불변).
 */
export function escalDue(state, nowMs) {
  const hours = Number(state && state.setg && state.setg.escHours);
  if (hours !== 4 && hours !== 24) return [];
  const ackMap = (state && state.ackedAlerts) || {};
  const maintMap = activeMaint(state, nowMs);
  const cutoff = hours * 3600;
  const due = [];
  collectAlerts(_arr(state && state.fleet), makeL(state || {})).forEach((a) => {
    if ((a.sev || '') !== 'critical') return;
    if (TEST_FIXTURE_RE.test(a.hostId || '')) return;
    if (Object.prototype.hasOwnProperty.call(maintMap, a.hostId)) return;
    // #365: 클레임 키도 같은 계약 — 시각 재료는 ackTime(세션 무관)이라 리로드 후에도 같은 클레임.
    const key = alertAckKey(a.hostId, a.name, a.desc, a.ackTime || a.time);
    if (ackMap[key]) return;
    const sec = agoSec(a.time);
    if (sec != null && sec >= cutoff) due.push({ key, host: a.host, desc: a.desc, time: a.time, ageSec: sec });
  });
  return due;
}

/** 만료된 점검 창 id 목록 — app.js 가 주기적으로 del 델타를 보내 청소한다(순수). */
export function expiredMaint(state, nowMs) {
  const out = [];
  const now = (nowMs == null ? Date.now() : nowMs);
  const src = (state && state.maint) || {};
  Object.keys(src).forEach((id) => {
    const t = Date.parse((src[id] || {}).until || '');
    if (isNaN(t) || t <= now) out.push(id);
  });
  return out;
}

function collectTraps(fleet) {
  const traps = [];
  fleet.forEach((s) => {
    const m = _meta(s);
    // G15: 알림과 동일 규칙 — 코드 없는 라벨엔 관리 IP 병기.
    const label = (m.label || s.host)
      + ((!/\d/.test(m.label || s.host) && m.mgmt) ? ' · ' + m.mgmt : '');
    _arr(m.traps).forEach((t) => {
      traps.push({
        host: label, hostId: s.id, id: s.id,
        sev: t.sev || 'info',
        desc: '⚡ ' + (t.desc || t.oid || 'SNMP trap'),
        time: tsNorm(t.time),
        trap: true,
      });
    });
  });
  return traps;
}

/* ===========================================================================
 * 7. buildModel
 * ======================================================================== */

/** 인자 유연 처리: buildModel(state) / buildModel(fleet, state) 둘 다 허용. */
function _resolve(a, b) {
  if (Array.isArray(a)) return { fleet: a, state: b || {} };
  const st = a || {};
  return { fleet: _arr(st.fleet), state: st };
}

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
  // #398: DEVICE_STATE(합성)도 collectAlerts 와 같은 재료(downSince/issueSince 우선, 없으면
  //   고정값) — 시뮬이 기록한 세션 시각 a.time 을 쓰면 카드 키와 갈라진다.
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

  // 로그: 이벤트 이력(폴러 events[] — 전이·발생·해제) + trap 병합(최신순) + 레벨/키워드 필터.
  // 이력이 오면 그것이 정본 — 활성 경보 스냅샷 병합(liveEvents)은 이력 없는 구폴러/시뮬 폴백.
  // (스냅샷 방식은 경보가 해소되는 순간 로그에서도 증발했다 — 실사용 지적.)
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
     FT 장비(EV/EDGE/END/FTS)의 meta.topo.storage[] 그룹별 사용률(%)을 한 목록으로 모은다.
     topo(폴러 실관계)는 실장비 응답에만 담기고 시뮬 장비엔 없다 → topo.storage 가 없으면 그 장비는
     생략한다(방어). 사용률(pct) 결측 그룹도 건너뛴다. 임계 톤은 barColor 규칙(78 warn/90 neg)을
     재사용하되, 중립은 pos(녹색)가 아니라 잉크 톤이라 tone=''(빈 문자열)로 내보낸다.
     실장비 ztC Edge 의 'Initial Storage Group'(97%)이 이 카드의 존재 이유다. */
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
    source: S.source || 'sim',
    live: S.source === 'live',
    sourceLabel: S.source === 'live' ? L('Live', '실시간') : L('Simulation', '시뮬레이션'),
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
  // 관리 트리(manage 화면 전용 소비)는 config CRUD 대상만 — 시뮬 장비(meta.sim)는
  // config 에 없어 수정/삭제가 실패하므로 트리에서 뺀다. 다른 화면 표시에는 그대로 포함.
  const tree = buildTree(allRows.filter((r) => !(devById[r.id] && _meta(devById[r.id]).sim)), S, L);
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
  // 확인 시간 — 경보의 실제 onset(a.time 벽시계)과 확인 시각(ISO)의 차. 키의 시각 재료
  // (parts[3]=ackTime)는 쓰지 않는다 — #543 이후 DEVICE_STATE 의 ackTime 은 키 안정화용
  // 에피소드 스탬프(data.js::_episodeStamp, 최대 ~300일 과거의 결정적 가짜 시각)라 그걸
  // onset 으로 파싱하면 시뮬에서 '평균 확인 수천 시간'이 나온다(#585). 이미 해소돼 목록에
  // 없는 경보의 고아 키는 onset 조회 실패로 자연 배제된다.
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
      // E2: 노드 부제 스킴 통일 — 시뮬은 'sj-edge-sim23-node0'(장비 raw id 접두), 실장비는 'node0'로
      //     서로 달랐고, 접두 raw id 는 상단 친숙 장비명(host)과도 충돌했다. 접두를 벗겨 'node0'로 정규화해
      //     전 행이 '{장비명} · node{N}' 동일 스킴이 되게 한다(그룹핑 키는 원문 n 유지).
      //     nodeKey(원문 n)는 화면 syncList 키 재료 — 같은 nShort로 정규화되는 원문 다른 두 노드
      //     (예: node0와 foo-node0)가 표시명만으로 키를 만들면 충돌해 행이 소실된다.
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
  // 장비 관리는 config CRUD 화면 — 폴섭이 생성한 시뮬 장비(sim_devices)는 config 에
  // 없어서 수정/삭제가 반드시 실패하므로 목록에서 숨긴다(삭제 시 '장비 없음' 오류 방지).
  const m = buildModel(_arr(fleet).filter((s) => !(s && s.meta && s.meta.sim)), state);
  return m.tree;
}
export function buildSearch(a, b) {
  const { fleet, state } = _resolve(a, b);
  const m = buildModel(fleet, state);
  return m.searchResults;
}

/* ===========================================================================
 * 9. buildDetail — Vigil detail.ts 이식 (variant 8종)
 * ======================================================================== */

/** 인자 유연 처리: (state) / (state,id) / (fleet,id,state) */
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

/* ===========================================================================
 * 10. buildTopo — Vigil topo.ts 이식(회사→공장→장비→노드→EAC→VM)
 * ======================================================================== */

/** 회사 색 팔레트 — 차분한 브랜드 8색(채도 낮은 라인/보더 용도).
 * 상태색과 충돌하는 hue(초록=pos·앰버=warn·주황/빨강=neg)는 팔레트에서 제외한다(C4):
 * red/앰버/초록은 '실장애·상태' 전용 예약이므로, 회사 구분색은 blue/teal/violet/plum/brown 계열만 쓴다.
 * (제거: #BD8526 앰버·#2E9E73 초록·#D96C36 주황 → #4C63A6 인디고·#A76C93 플럼·#3F8CA8 세룰리안으로 대체) */
// G10: 4번째 색 #4C63A6 ↔ 7번째 #8A6A4F 스왑 — 1번째 #5677A8 과 ΔRGB≈22 근접 블루가
//     실플릿(5개 회사)에서 인접 배정돼 범주 구분이 어려웠다. 앞 5칸을 블루·틸·바이올렛·브라운·모브로
//     상호 판별되게 재배치(색 자체는 기존 팔레트 내 재사용, 신규색 없음).
export const COMPANY_PALETTE = [
  '#5677A8', '#4E8E96', '#7E72B5', '#8A6A4F',
  '#A76C93', '#3F8CA8', '#4C63A6', '#6A7C8C',
];

/* 회사 기본색 단일 산출(#52) — 토폴로지 자동 배정(buildTopo)과 설정 화면 미리보기
 * (buildCompanyColors)가 같은 회사에 다른 팔레트 인덱스를 매기던 것을 한 함수로 통일한다.
 * 정렬은 토폴로지 화면 순서(실장비 회사 우선 → cmpKo)를 따른다 — 사용자가 색을 직접 고르지
 * 않은 회사도 설정 미리보기가 토폴로지에 실제 보이는 색과 항상 일치하게 하기 위함이다. */
function coDefaultColorMap(coNames, hasReal) {
  const ordered = coNames.slice().sort((a, b) => {
    const ra = hasReal(a) ? 0 : 1; const rb = hasReal(b) ? 0 : 1;
    return ra !== rb ? ra - rb : cmpKo(a, b);
  });
  const map = Object.create(null);
  ordered.forEach((co, i) => { map[co] = COMPANY_PALETTE[i % COMPANY_PALETTE.length]; });
  return map;
}

/* ---------------------------------------------------------------------------
 * 레이아웃 상수 — 좌→우 계층 흐름 + 공장 밴드 안 "장비 카드 그리드" + 캔버스 신문단.
 *
 * G32(전면 리디자인, 사용자 지시): G19 이후 접힌 장비가 1대=1행으로 굳어 라이브 102대가
 * 666×10400(1:15.6) 세로 스트립이 됐다(88% 잘림, 실측 G002-TOPO-MEASURE). 2축으로 공간을 쓴다:
 *   ① 공장 밴드 내부 — 접힌 장비를 **컬럼-메이저** D열로 랩(위→아래 채우고 다음 컬럼).
 *      열마다 자기 스파인(공장 우변→열 세로선→카드 좌변 중앙)을 둔다. 예전 그리드 패킹(행당
 *      D개, 행-메이저)은 형제가 우측으로 흘러 직렬 체인으로 오독됐다(G19) — 컬럼-메이저는
 *      같은 행의 이웃이 서로 다른 열(= 다른 스파인)에 속해 그 오독을 원천 차단한다.
 *   ② 최상위 — 회사 블록(원자적, 컬럼 간 분할 금지)을 캔버스 C열로 신문단 배치.
 * G34: D·C 는 planCompany(회사 1개 공장 밴드 계획)와 packFor(D·C 조합 패킹)가 전체 종횡비
 * (TARGET_RATIO)에 가장 가깝도록 자동 탐색한다(주석 정정 — 예전 이름 'planFor'는 리팩터로
 * planCompany/packFor 로 분리됐다). 데이터가
 * 작으면(회사 포커스 등) 자연히 C=1·D=1 로 수렴한다(열을 늘려도 종횡비 이득이 없으므로).
 * ------------------------------------------------------------------------ */
const PAD = 24;
// T1→G1: 회사 박스 폭 150→164 — 150 은 이름 폭 87px 로 '동아반도체'(800 웨이트 실측 ~86px)가
//     여유 1px 에 걸려 렌더러에 따라 '동아반…'으로 절단됐다. 폭을 최장명 +14px 여유로 키우고
//     오른쪽 컬럼(FA.x)·그리드 기준선(GRID_X)을 같이 +18 밀어 간선 최소 수평런(≥20px)을 지킨다.
// G33: CO.w 164→176 — --fs-19 확대(19→23px, FIT_MIN 0.40 페어)로 '동아반도체' 실측 105px 가
//     내부 101px 를 4px 넘겨 '동아반…' 절단 재발(G1 회귀). +12 로 여유 8px 복원.
const CO = { x: 24, w: 176, h: 46 };
// FA.w 152→196: 실장비 공장 라벨(서브넷 '172.30.1.0/24')이 fit 라벨 확대(--fs-19)에서
// 실측 127px 를 요구 — 188(내부 125px)은 2px 모자라 '/24'가 잘렸다. +8 여유로 절단 제거.
// GRID_X 를 같이 밀어 각 컬럼 스파인(그리드 열 x - 22)이 FA 우변보다 오른쪽에 오게 유지한다.
// G33: FA.w 196→204 — 같은 폰트 확대로 '172.30.1.0/24' 실측 156px 가 내부 154px 를 2px 초과
//     ('/…' 절단, 기존 +8 여유 계약 재발). CO 이동(+12)과 함께 FA.x 208→220, GRID_X 438→458.
const FA = { x: 220, w: 204, h: 40 };
const GRID_X = 458;                 // 장비 그리드 좌측 기준선(열 0). 열 j 는 +j*(CARD.w+CARD.gx).
// G32: 카드 204×84→150×62, gx/gy 16→8/6 — 컬럼-메이저 랩으로 카드가 가로로도 쌓이므로 카드 1장의
// 면적을 줄여야 같은 스테이지에 더 많은 열/행이 들어온다(실측: 라이브 102대 기준 fit 0.42,
// 스테이지 점유 57%, 잘림 0). 기본(미줌) 배율에선 sub/bars/foot 를 CSS 가 이미 숨기고 head(아이콘+
// 라벨) 1줄만 남기므로(§styles :not(.is-zoomed)) 높이를 줄여도 기본 뷰 판독성은 그대로다.
// G33: CARD.w 150→164 — 실클러스터 컴팩트 타일 라벨 'everRun 8.1.0.2-19' 실측 114px 가
//     내부 107px 에서 7px 절단. +14 로 내부 121px(여유 7px) 확보 — 시뮬 코드(EV-05)는 원래 무사.
// G34(레드팀 P2-1 정정): CARD.gx 8→26 — 이전 값(8px)에서는 열 간 스파인(spineX = colX-22)이
// 이전 열 카드의 우변(prevColX+CARD.w)보다 왼쪽에 와 스파인 선이 이전 카드 밑에 깔려 가려졌다.
// gx 를 26 으로 넓히고 spineX 를 colX-13 으로 당겨 접기 손잡이(카드 우변 중앙, ±11px)와 스파인
// 사이에 2px 이격을 확보한다(prevColX+CARD.w+11 손잡이 우측 끝 vs prevColX+CARD.w+13 스파인).
// gy 6→12: 62px 컴팩트 카드가 6px 간격으로 닿을 듯 붙어 '한 덩어리'로 읽혔다(사용자 지적).
const CARD = { w: 164, h: 62, gx: 26, gy: 12 };
const BAND_GAP = 24;                // 같은 회사 안 공장 밴드 사이 (18→24, 동일 지적)
// 풀카드(140~176px) 행 사이 간격 — 컴팩트 gy 를 그대로 쓰면 큰 박스끼리 6px 로 밀착돼
// 카드 경계가 뭉개졌다(Proxmox 풀카드 전환 후 두드러짐). 행 어느 쪽이든 full 이면 이 값.
const ROW_GAP_FULL = 22;
const CO_GAP = 34;                  // 회사 사이(같은 캔버스 컬럼 안)
// G32: 캔버스 컬럼(신문단) 사이 여백 — 회사 간 CO_GAP 보다 넓게 잡아 "같은 회사 안 밴드 간격"
//     과 다른(더 큰) 위계임을 보여준다. 열 폭이 이미 GRID_X+카드 랩 만큼 넓어 과한 여백은 낭비다.
const CANVAS_COL_GAP = 48;
const MAX_D = 14;                   // 공장 밴드 내부 랩 열 수 탐색 상한
const MAX_C = 5;                    // 캔버스 신문단 열 수 탐색 상한
/* 펼친 장비 행: 장비 → 노드 → EAC → VM 이 좌→우로 흐른다(상대 x). */
const XD = { w: 252, h: 140 };
const ENDU = { w: 595 };            // ztC Endurance 단일 섀시 카드 — 흐름 그룹 4열(BMC→STANDBY→MGMT→HOST) + 상태점/ms 실측 수용 폭
const XN = { x: 288, w: 208, h: 74, gy: 14 };
const XE = { x: 532, w: 142, h: 106 };
// 시뮬·비FT 호스팅 VM 은 오른쪽으로 열을 늘리지 않고 한 열에서 아래로 계속 쌓는다.
// 사용자가 장비→VM 흐름을 좌→우 새 열이 아니라 위→아래 형제 목록으로 읽도록 폭을 고정한다.
const XV = { x: 718, w: 212, h: 92, gy: 12 };
const TARGET_RATIO = 1.62;

/* ---------------------------------------------------------------------------
 * 실데이터(everrun-poller) 전용 확장 레인 — meta.topo 가 있을 때만 쓴다.
 *
 * 주 계층 체인을 좌→우 직렬로 편다(사용자 요구: Stratus→everRun→관리화면→node→VM).
 * 관리화면(EAC)을 장비와 노드 사이에 세워 everRun→관리화면→노드→VM 이 한 줄로
 * 읽히게 한다. 네트워크·스토리지 상세는 장비 상세 화면이 담당한다.
 *
 *   [장비]──[관리화면]──[노드0]──┐
 *                        [노드1]──┴─[VM col0][VM col1]
 * ------------------------------------------------------------------------ */
// 주 체인(장비→관리→노드→그룹→VM)을 좌→우로 압축해 기본 fit 배율을 0.75 이상으로 끌어올린다.
// (예전 RV.x=886·w=212 는 다열 그리드로 캔버스가 1500px 까지 늘어 fit 이 좌우로 잘렸다.)
// G21: RE.h 92→118 — EAC 콘텐츠 실높이(크롬 20 + 아이콘 28 + 제목/IP/상태필 ~60 + 패딩)가
//     114px 인데 92px + overflow:hidden 으로 '정상 연결' 상태필이 통째로 잘렸다(사용자 지적).
const RE = { x: 274, w: 140, h: 118 };                  // 관리화면(EAC) — 장비 다음(주 체인 2단)
// G21: 실측 scrollHeight 기준 높이 보정 — node 76·VM그룹 52·개별VM 45·스토리지 100 인데
//     박스가 각각 74/50/44/96 이라 하단 수 px 이 넘쳐 접혔다(관리화면과 동일 계열 결함).
const RN = { x: 430, w: 182, h: 82, gy: 14 };           // node0/node1 — 관리화면 다음(3단)
const RG = { x: 628, w: 96, h: 58 };                    // VM 그룹 노드 — 노드 다음(4단, 노드별 1개)
// 개별 VM — VM 그룹 오른쪽 세로 스택 카드(이름+IP/사양+▸배치노드).
// 개수와 무관하게 한 열로 내려가며, IP 한 줄을 숨기지 않도록 높이를 확보한다.
// w=188 은 22자 VM 명(텍스트폭 132px)이 말줄임 없이 들어오는 최소치(표시폭 ~136px).
// G20: RV.w 188→212 — 실 VM 이름(winsrv_2022_en_EVAL_01, 실측 154px)이 이름 폭 138px 에서
//     절단됐다(사용자 지적). 22자 mono 가 여유 8px 로 들어오는 폭.
const RV = { x: 740, w: 212, h: 86, gy: 9 };  // 개별 VM — VM 그룹 다음(5단, IP 2줄+하단 여유 수용)
const LANE_GAP = 16;                                    // 노드 레인(노드+VM그룹+VM) 사이 세로 간격
/** meta.topo(폴러 실관계)가 쓸 만한지. 없으면 예전 레이아웃으로 간다. */
function topoOf(s) {
  const t = _meta(s).topo;
  if (!t || typeof t !== 'object') return null;
  const nets = _arr(t.networks);
  const stos = _arr(t.storage);
  return (nets.length || stos.length) ? t : null;
}

/** 실데이터 VM 블록도 우측 열을 만들지 않고 한 열에서 아래로 쌓는다. */
/* ===========================================================================
 * 토폴로지 순수 유틸 — buildTopo(962줄)에서 승격.
 * 이 함수들은 buildTopo 의 지역 상태를 하나도 닫지 않는 순수 함수라 밖으로 뺄 수 있고,
 * 밖으로 빼야 tests/topo-geometry.test.mjs 가 좌표 계약을 고정할 수 있다.
 * (계획 패스와 렌더 패스가 같은 함수를 써야 링크가 안 뜬다 — 그 '같은 함수'가 여기 있다.)
 * ======================================================================== */

/** 세로 스택 높이 — n개를 h높이·gy간격으로 쌓았을 때. 0개면 0(빈 스택이 여백을 만들면 안 된다). */
export function stackH(n, h, gy) { return n > 0 ? n * h + (n - 1) * gy : 0; }

/** 산업 프로토콜 라벨 축약 — 좁은 토폴로지 박스용. */
export function protoShort(p) {
  return p === 'EtherNet/IP' ? 'E/IP' : (p === 'Modbus TCP' ? 'Modbus' : (p || ''));
}

/**
 * 프린터 토너 요약 — 최저 잔량(%)과 총 페이지 카운터.
 * buildDetail 안의 인라인 IIFE 였던 것을 승격(순수). CMYK/토너 이름을 가진 소모품만 센다.
 * @param {object} printerMeta 장비 meta.printer
 * @returns {{minPct:(number|null), pages:(number|null)}}
 */
export function tonerSummary(printerMeta) {
  const pr = printerMeta || {};
  const toners = _arr(pr.supplies)
    .filter((x) => x && /cyan|magenta|yellow|black|toner/i.test(String(x.name || '')) && Number(x.pct) >= 0);
  return {
    minPct: toners.length ? Math.min.apply(null, toners.map((x) => Number(x.pct))) : null,
    pages: pr.pages != null ? Number(pr.pages) : null,
  };
}

/**
 * 라이선스 요약 — 토폴로지 장비 카드의 LIC 행. buildTopo 에서 승격(순수).
 * 만료형이면 D-day, 영구형이면 '영구', 라이선스가 없으면 대시.
 * 만료형인데 만료일이 결측이거나 파싱 불가면 '영구'가 아니라 미상(#603 — 개요·클리스터·상세 #593 계약과 정합).
 * @param {object} m 장비 meta  @param {(en:string,ko:string)=>string} L
 */
export function licInfo(m, L) {
  const lic = m.license;
  if (!lic) return { label: DASH, dTxt: '', tone: 'mut' };
  const label = lic.name || lic.edition || L('License', '라이선스');
  if (lic.expires) {
    const d = lic.expire ? parseLicDate(lic.expire) : null;
    if (d) {
      const days = Math.round((d.getTime() - Date.now()) / 86400000);
      return { label, dTxt: days < 0 ? ('D+' + (-days)) : ('D-' + days), tone: licTone(days) };
    }
    return { label, dTxt: L('Unknown', '미상'), tone: 'mut' };
  }
  return { label, dTxt: L('Perp', '영구'), tone: 'mut' };
}

/** Endurance 카드의 서브시스템 흐름 — 단일 2U 섀시 안에서 관리 경로가 흐르는 방향:
 *  BMC A/B → Standby OS A/B → Management UI 1/2 + Windows Host.
 *  ACTIVE 박스는 역할 표시(CM-A/B 는 물리 식별자, Active/Standby 는 현재 역할). */
function enduFlow(s, m) {
  const e = m.endurance || {};
  const nodes = _arr(m.nodes);
  const act = nodes.find((n) => n && n.primary) || {};
  const stby = nodes.find((n) => n && !n.primary) || {};
  const mgmt = _arr(e.managementIPs);
  const bmc = (n) => (n && n.bmc) ? [n.bmc.eth0, n.bmc.eth1].filter(Boolean) : [];
  const stn = (n) => (n && n.standbyNic) ? [n.standbyNic.eno1, n.standbyNic.eno2].filter(Boolean) : [];
  const reach = (e && e.reach) || {};
  // rk = meta.endurance.reach 의 조회 키 — 박스에 상태 점·응답시간을 달기 위한 재료.
  const bx = (k, v, rk) => {
    const r = reach[rk] || {};
    return { k, v, state: r.state || '', ms: (typeof r.ms === 'number') ? r.ms : null };
  };
  return {
    active: { k: 'ACTIVE', v: [(act.name || '—') + (stby.name ? '  ·  STBY ' + stby.name : '')], tone: s.status === 'down' ? 'neg' : 'pos' },
    // 그룹별 cols — 열 안에서 박스는 위→아래. HOST(WINDOWS)는 MGMT 우측의 별도 그룹이다
    // (사용자 지적: 'MGMT · HOST' 합성 제목 오류 — HOST 는 WINDOWS 박스 위의 제목).
    groups: [
      { title: 'BMC', cols: [[bx('BMC A', bmc(act), 'bmcA'), bx('BMC B', bmc(stby), 'bmcB')]] },
      { title: 'STANDBY OS', cols: [[bx('STANDBY A', stn(act), 'stbyA'), bx('STANDBY B', stn(stby), 'stbyB')]] },
      { title: 'MGMT', cols: [[bx('MGMT UI 1', mgmt[0] ? [mgmt[0]] : [], 'mgmt1'), bx('MGMT UI 2', mgmt[1] ? [mgmt[1]] : [], 'mgmt2')]] },
      { title: 'HOST', cols: [[bx('WINDOWS', e.windowsHost ? [e.windowsHost] : [], 'windows')]] },
    ],
  };
}

/**
 * 비FT 장비(PLC/NAS/프린터/서버/PC 등)의 타입별 메타 행 — 토폴로지 카드 본문.
 * buildTopo(955줄)에서 승격: L 외에는 모듈 스코프 심볼(DASH/_arr/_num/protoShort)만 쓰는
 * 순수 함수라 밖으로 뺄 수 있고, 빼야 tests 가 타입별 행 계약을 고정할 수 있다.
 * @param {object} s 장비  @param {object} m 장비 meta  @param {(en:string,ko:string)=>string} L
 * @returns {Array<{k:string,v:string,tone:string}>|null} 행 목록, 해당 타입이 없으면 null
 */
export function typeRows(s, m, L) {
  const down = s.status === 'down';
  if (s.type === 'END') {
    // ztC Endurance — 토폴로지에서는 '2U 단일 섀시' 카드 한 장으로 표현한다
    // (사용자 지시: CM-A/CM-B 체인으로 펼치지 않음). 행 내용은 IP 플랜 11개와
    // 현재 Active/Standby 역할 — A/B 는 물리 모듈 식별자, Active/Standby 는 역할.
    const e = m.endurance || {};
    const nodes = _arr(m.nodes);
    const act = nodes.find((n) => n && n.primary) || {};
    const stby = nodes.find((n) => n && !n.primary) || {};
    const bmcIps = nodes.flatMap((n) => (n && n.bmc) ? [n.bmc.eth0, n.bmc.eth1] : []).filter(Boolean);
    const stbyIps = nodes.flatMap((n) => (n && n.standbyNic) ? [n.standbyNic.eno1, n.standbyNic.eno2] : []).filter(Boolean);
    const mgmt = _arr(e.managementIPs);
    // 카드 폭(~226px)에 4개 IP 풀나열은 잘린다 — 마지막 옥텟 범위 축약(예: 10.10.30.11~14 · 4개).
    const ipRange = (ips) => {
      if (!ips.length) return '';
      const last = (s) => String(s).split('.').pop();
      const prefix = String(ips[0]).split('.').slice(0, 3).join('.');
      return prefix + '.' + last(ips[0]) + (ips.length > 1 ? '~' + last(ips[ips.length - 1]) : '') + ' · ' + ips.length + L(' IPs', '개');
    };
    const osShort = (s) => String(s || '').split('(')[0].trim().replace('Windows Server', 'Win Srv');
    return [
      { k: L('ACTIVE', '액티브'), v: (act.name || DASH) + (stby.name ? ' · STBY ' + stby.name : ''), tone: down ? 'neg' : 'pos' },
      { k: L('MGMT UI', '관리 UI'), v: mgmt.length ? mgmt.join(' / ') : (m.mgmt || DASH), tone: 'mut' },
      { k: L('WINDOWS', '윈도우'), v: e.windowsHost || DASH, tone: 'mut' },
      { k: 'BMC', v: ipRange(bmcIps) || DASH, tone: 'mut' },
      { k: L('STANDBY OS', '스탠바이 OS'), v: ipRange(stbyIps) || DASH, tone: 'mut' },
      { k: 'OS', v: osShort(act.os) + (stby.os ? ' · STBY Ubuntu' : ''), tone: 'mut' },
    ];
  }
  if (s.type === 'PLC') {
    const p = m.plc || {};
    const run = String(p.runState || '');
    const goRun = run === 'RUN' || run === 'MONITOR';
    const sev = String(p.errSev || '');
    const bad = !!p.hasError || sev === 'major' || sev === 'partial';
    const warn = !bad && (sev === 'observation' || sev === 'minor');
    const rows = [
      { k: L('RUN', '운전'), v: down ? L('offline', '오프라인') : (run === 'PROGRAM' ? L('STOP', '정지') : (run || L('online', '온라인'))), tone: down ? 'neg' : (goRun ? 'pos' : (run ? 'warn' : 'mut')) },
      { k: L('NET', '통신'), v: (protoShort(p.protocol || '') || DASH) + (p.port ? ':' + p.port : ''), tone: 'mut' },
      { k: L('DIAG', '진단'), v: bad ? L('error', '에러') : (warn ? (sev === 'minor' ? L('minor', '경미') : L('observation', '관찰')) : L('normal', '정상')), tone: bad ? 'neg' : (warn ? 'warn' : 'pos') },
    ];
    _arr(p.procVars).slice(0, 2).forEach((v) => rows.push({
      k: String(v.label || v.name || ''),
      v: (v.value != null ? String(v.value) : DASH) + (v.unit ? ' ' + v.unit : ''), tone: 'mut', kw: 62,
    }));
    return rows;
  }
  if (s.type === 'PRN') {
    const p = m.printer || {};
    const sup = _arr(p.supplies);
    const isToner = (n) => /cyan|magenta|yellow|black/i.test(String(n || ''));
    const toners = sup.filter((x) => isToner(x.name) && x.pct >= 0);
    const minT = toners.length ? Math.min.apply(null, toners.map((x) => x.pct)) : null;
    const tray = _arr(p.trays)[0] || null;
    const errs = _arr(p.errors);
    return [
      { k: L('STATE', '상태'), v: down ? L('offline', '오프라인') : (errs.length ? errs[0] : ({ printing: L('Printing', '인쇄 중'), warmup: L('Warming up', '예열 중') }[p.status] || L('Idle', '대기'))), tone: down ? 'neg' : (errs.length ? 'warn' : 'pos') },
      { k: L('TONER', '토너'), v: minT != null ? (L('min ', '최저 ') + minT + '%') : DASH, tone: minT == null ? 'mut' : (minT <= 10 ? 'neg' : (minT <= 25 ? 'warn' : 'pos')) },
      { k: L('PAPER', '용지'), v: tray ? (tray.level === 0 ? L('empty', '없음') : (tray.level > 0 && tray.max ? tray.level + '/' + tray.max : L('present', '있음'))) : DASH, tone: tray && tray.level === 0 ? 'warn' : 'mut' },
      { k: L('PAGES', '페이지'), v: p.pages != null ? Number(p.pages).toLocaleString() : DASH, tone: 'mut' },
    ];
  }
  if (s.type === 'SRV') {
    // 일반 서버(Proxmox 등) — 폴러 meta.srv({node,load,pve}) + nodes[0] 지표로 kv 행.
    // (이전엔 SRV 분기가 없어 typeRows=null → 펼침 불가 compact 강등 → NAS/프린터와 달리
    //  가로 batch 로 튀던 배치 문제의 근원. 행이 생기면 형제와 같은 세로 풀카드가 된다.)
    const sv = m.srv || {};
    const n0 = _arr(m.nodes)[0] || {};
    const cu = usageOf(s, 'cpu');
    const mu = usageOf(s, 'mem');
    const load = _arr(sv.load).length ? _arr(sv.load) : _arr(n0.loadAvg);
    // 유휴 하이퍼바이저의 0% 는 '죽은 값'처럼 읽힌다 — subPctText(단일 구현, §1)가 '<1%'로 구분한다.
    const cpuTxt = cu.text;
    const rows = [
      { k: L('STATE', '상태'), v: down ? L('offline', '오프라인') : L('online', '온라인'), tone: down ? 'neg' : 'pos' },
      { k: 'CPU', v: cpuTxt, tone: cu.na ? 'mut' : cu.tone },
      { k: 'MEM', v: (mu.na ? DASH : mu.val + '%') + (n0.memGiB ? ' · ' + n0.memGiB + ' GiB' : ''), tone: mu.na ? 'mut' : mu.tone },
      { k: L('LOAD', '부하'), v: load.length ? load.join(' ') : DASH, tone: 'mut' },
      { k: 'VM', v: n0.vmCount != null ? String(n0.vmCount) : (m.vms != null ? String(m.vms) : DASH), tone: 'mut' },
    ];
    // Proxmox 확장 행 — 데이터가 있을 때만(일반 SNMP 서버는 5행 그대로).
    const dks = _arr(m.srvDisks).filter((x) => x && x.kind !== 'usb');
    if (dks.length) {
      const ok = dks.filter((x) => ['PASSED', 'OK', 'UNKNOWN', ''].indexOf(String(x.health || '').toUpperCase()) >= 0).length;
      const wear = dks.map((x) => _num(x.wearout)).filter((x) => x != null);
      const wMin = wear.length ? Math.min.apply(null, wear) : null;
      rows.push({
        k: L('DISK', '디스크'),
        v: ok + '/' + dks.length + ' OK' + (wMin != null ? L(' · life ', ' · 수명 ') + wMin + '%' : ''),
        tone: ok < dks.length ? 'neg' : (wMin != null && wMin <= 10 ? 'warn' : 'pos'),
      });
    }
    const stor = _arr(m.srvStorage);
    if (stor.length) {
      const worst = stor.reduce((a, b) => ((b.pct || 0) > (a.pct || 0) ? b : a), stor[0]);
      rows.push({ k: L('STOR', '풀'), v: (worst.name || '') + ' ' + (worst.pct || 0) + '%', tone: pctTone(worst.pct || 0) });
    }
    const eths = _arr(m.srvNet).filter((x) => x && x.kind === 'eth');
    if (eths.length > 1) {
      // 미연결 포트는 결함이 아니라 정보(NAS LAN 행과 동일 규약, tone mut).
      rows.push({ k: 'LAN', v: eths.filter((x) => x.up).length + '/' + eths.length + L(' link', ' 링크'), tone: 'mut' });
    }
    if (sv.kernel) rows.push({ k: L('KERNEL', '커널'), v: String(sv.kernel), tone: 'mut', kw: 62 });
    return rows;
  }
  if (s.type === 'PC') {
    const p = m.pc || {}; const svc = _arr(p.services);
    return [
      { k: 'OS', v: p.os || L('Unknown', '미상'), tone: 'mut' },
      { k: L('HOST', '호스트'), v: (p.netbios || s.host || DASH) + (p.workgroup ? ' · ' + p.workgroup : ''), tone: 'mut' },
      { k: L('SVC', '서비스'), v: svc.length ? svc.join('·') : DASH, tone: 'mut' },
      { k: L('MODE', '수집'), v: p.snmp ? 'SNMP' : L('agentless', '에이전트리스'), tone: 'mut' },
    ];
  }
  if (s.type === 'NAS') {
    const n = m.nas || {}; const vol = _arr(n.volumes)[0] || null;
    const disks = _arr(n.disks);
    // #542: ok 결측은 OK 로 세지 않는다(detail #522 와 같은 NA 계약) — 확인된 ok=true 만
    // 정상 집계하고, 결측이 하나라도 있으면 녹색(pos) 대신 중립(mut)으로 표기한다.
    const badDisk = disks.filter((d) => d && d.ok === false).length;
    const okDisk = disks.filter((d) => d && d.ok === true).length;
    const naDisk = disks.length - badDisk - okDisk;
    const t = _num(n.tempC);
    const ports = _arr(n.lanPorts);
    const portsUp = ports.filter((x) => x && x.up).length;
    return [
      // vol.pct 결측 시 'undefined%' 대신 대시 — 결측 ≠ 0% 이므로 톤도 mut 로.
      { k: L('VOL', '볼륨'), v: (vol && vol.pct != null) ? ((vol.name ? vol.name + ' ' : '') + vol.pct + '%') : DASH, tone: (vol && vol.pct != null) ? pctTone(vol.pct) : 'mut' },
      { k: L('DISK', '디스크'), v: (okDisk + badDisk) ? (okDisk + '/' + disks.length + ' OK') : DASH, tone: badDisk ? 'neg' : (disks.length && !naDisk ? 'pos' : 'mut') },
      { k: L('TEMP', '온도'), v: t != null ? t + '°C' : DASH, tone: t == null ? 'mut' : (t >= 65 ? 'neg' : (t >= 55 ? 'warn' : 'pos')) },
      // 랜포트 2개 이상일 때만 — 미연결 포트는 결함이 아니라 정보(tone mut).
      ...(ports.length > 1 ? [{ k: 'LAN', v: portsUp + '/' + ports.length + L(' link', ' 링크'), tone: 'mut' }] : []),
      { k: 'DSM', v: String(n.dsmVersion || n.model || DASH), tone: n.upgradeAvailable ? 'warn' : 'mut' },
    ];
  }
  if (s.type === 'WIN') {
    const w = m.win || {}; const disk = _arr(w.disks)[0] || null;
    return [
      { k: 'OS', v: (w.os || L('Windows Server', 'Windows 서버')) + (w.build ? ' · ' + w.build : ''), tone: 'mut' },
      { k: L('DISK', '디스크'), v: (disk && disk.pct != null) ? (disk.drive + ' ' + disk.pct + '%') : DASH, tone: (disk && disk.pct != null) ? pctTone(disk.pct) : 'mut' },
      { k: L('SVC', '서비스'), v: w.svcRunning ? (w.svcRunning + L(' running', ' 실행')) : DASH, tone: 'mut' },
      { k: L('PATCH', '패치'), v: String(w.lastHotfix || DASH), tone: 'mut' },
    ];
  }
  if (s.type === 'PI') {
    const p = m.pi || {}; const t = _num(p.tempC);
    const thr = !!p.throttled; const thrW = !thr && (!!p.throttleThermal || !!p.throttleUnderVolt);
    return [
      { k: L('MODEL', '모델'), v: String(p.model || 'Raspberry Pi'), tone: 'mut' },
      { k: L('TEMP', '온도'), v: t != null ? t.toFixed(1) + '°C' : DASH, tone: t == null ? 'mut' : (t >= 75 ? 'neg' : (t >= 65 ? 'warn' : 'pos')) },
      { k: L('THR', '스로틀'), v: thr ? L('throttled', '스로틀') : (thrW ? L('warning', '경고') : L('normal', '정상')), tone: thr ? 'neg' : (thrW ? 'warn' : 'pos') },
      { k: L('KERNEL', '커널'), v: String(p.kernel || DASH), tone: 'mut' },
    ];
  }
  return null;
}

/** 장비 정렬 비교자 — 타입 순서(TYPE_KEYS) 우선, 같으면 라벨 한글 정렬.
    (buildTopo 안에서는 TORD 라는 별칭으로 쓰이지만 그건 지역 별칭이라 여기선 원본을 참조한다.) */
export function devSort(x, y) {
  const tx = TYPE_KEYS.indexOf(x.type); const tyy = TYPE_KEYS.indexOf(y.type);
  if (tx !== tyy) return (tx < 0 ? 99 : tx) - (tyy < 0 ? 99 : tyy);
  return cmpKo(_meta(x).label || x.host || '', _meta(y).label || y.host || '');
}

export function rvBlock(n) {
  if (n <= 0) return { cols: 0, rows: 0, w: 0, h: 0 };
  return { cols: 1, rows: n, w: RV.w, h: n * RV.h + (n - 1) * RV.gy };
}

/**
 * 실데이터 노드 레인 계획 — 각 노드가 [노드 → VM그룹 → 그 노드의 VM들] 한 레인을 차지한다.
 * 계획 패스(planCompany 의 xRowH/xRowW)와 렌더 패스(pushRealRow)가 **같은 함수**를 써서
 * 좌표가 어긋나지 않게 한다(이게 어긋나면 캔버스가 틱마다 재생성되거나 링크가 뜬다).
 * VM 은 실제 배치 노드(vm.node = vm_placements)로 묶고, 어느 노드와도 안 맞으면 첫 노드로 보낸다.
 * @param {(nodeName:string)=>boolean} [vgColOf] 그 노드의 VM 그룹이 접혔는지(접히면 VM블록을
 *        폭/높이 계산에서 빼 캔버스가 좁아지고, 개별 VM 은 렌더되지 않는다).
 */
export function realLanes(m, vgColOf) {
  const nodes = _arr(m.nodes).slice(0, 2);
  const vms = _arr(m.vmList);
  const byNode = nodes.map(() => []);
  vms.forEach((v) => {
    let idx = nodes.findIndex((n) => (n.name || '') === String(v.node || ''));
    if (idx < 0) idx = 0;
    if (byNode[idx]) byNode[idx].push(v);
  });
  const lanes = nodes.map((n, i) => {
    const list = byNode[i] || [];
    const collapsed = !!(list.length && vgColOf && vgColOf(n.name || ('node' + i)));
    const blk = collapsed ? { cols: 0, rows: 0, w: 0, h: 0 } : rvBlock(list.length);
    // 레인 높이 = 노드/그룹/VM블록 중 가장 큰 것(VM 없는 노드는 그룹 높이를 빼 노드 높이만,
    // 접힌 그룹은 그룹 박스 높이까지만).
    const h = Math.max(RN.h, list.length ? RG.h : 0, blk.h);
    return { node: n, i, vms: list, blk, collapsed, h };
  });
  const total = lanes.length
    ? lanes.reduce((a, l) => a + l.h, 0) + LANE_GAP * (lanes.length - 1)
    : 0;
  return { lanes, total };
}

/** 시뮬·비FT 호스팅 VM 은 개수와 무관하게 한 열에서 아래로 쌓는다. */
export function vmGrid(n) {
  if (n <= 0) return { cols: 0, rows: 0 };
  return { cols: 1, rows: n };
}
export function vmBlock(n) {
  const g = vmGrid(n);
  return {
    cols: g.cols, rows: g.rows,
    w: g.cols ? XV.w : 0,
    h: g.rows ? g.rows * XV.h + (g.rows - 1) * XV.gy : 0,
  };
}

export function r1(n) { return Math.round(n * 10) / 10; }

/** 축정렬 폴리라인 → 모서리가 둥근 SVG path(직교 라우팅). */
export function orthPath(pts, r) {
  const p = [];
  pts.forEach((q) => {
    const last = p[p.length - 1];
    if (!last || Math.abs(last.x - q.x) > 0.01 || Math.abs(last.y - q.y) > 0.01) p.push(q);
  });
  if (p.length < 2) return 'M' + r1(pts[0].x) + ' ' + r1(pts[0].y);
  let d = 'M' + r1(p[0].x) + ' ' + r1(p[0].y);
  for (let i = 1; i < p.length - 1; i++) {
    const a = p[i - 1]; const c = p[i]; const b = p[i + 1];
    const l1 = Math.abs(c.x - a.x) + Math.abs(c.y - a.y);
    const l2 = Math.abs(b.x - c.x) + Math.abs(b.y - c.y);
    const rr = Math.max(0, Math.min(r, l1 / 2, l2 / 2));
    const u1x = (c.x - a.x) / (l1 || 1); const u1y = (c.y - a.y) / (l1 || 1);
    const u2x = (b.x - c.x) / (l2 || 1); const u2y = (b.y - c.y) / (l2 || 1);
    d += ' L' + r1(c.x - u1x * rr) + ' ' + r1(c.y - u1y * rr);
    d += ' Q' + r1(c.x) + ' ' + r1(c.y) + ' ' + r1(c.x + u2x * rr) + ' ' + r1(c.y + u2y * rr);
  }
  d += ' L' + r1(p[p.length - 1].x) + ' ' + r1(p[p.length - 1].y);
  return d;
}

/** 폴리라인 중점 — 단절(X) 마커 위치. */
export function polyMid(pts) {
  let tot = 0;
  for (let i = 1; i < pts.length; i++) tot += Math.abs(pts[i].x - pts[i - 1].x) + Math.abs(pts[i].y - pts[i - 1].y);
  let acc = 0;
  for (let i = 1; i < pts.length; i++) {
    const seg = Math.abs(pts[i].x - pts[i - 1].x) + Math.abs(pts[i].y - pts[i - 1].y);
    if (acc + seg >= tot / 2) {
      const t = seg ? (tot / 2 - acc) / seg : 0;
      return { x: pts[i - 1].x + (pts[i].x - pts[i - 1].x) * t, y: pts[i - 1].y + (pts[i].y - pts[i - 1].y) * t };
    }
    acc += seg;
  }
  return pts[pts.length - 1];
}

export function nodeStatus(n) {
  const run = n && /run/i.test(n.state || '');
  if (!run) return 'down';
  const sd = String((n && n.standing) || '').toLowerCase();
  const md = String((n && n.mode) || '').toLowerCase();
  // mode 는 poller/시뮬레이터에 따라 'normal' 또는 'production' 이 정상값이다.
  const modeBad = md && md !== 'normal' && md !== 'production';
  return ((sd && sd !== 'normal') || modeBad) ? 'deg' : 'op';
}

export function grpWorst(arr) {
  if (arr.some((s) => s.status === 'down')) return 'down';
  if (arr.some((s) => s.status === 'deg')) return 'deg';
  return 'op';
}

/**
 * 토폴로지 계층도 배치 계산(순수). 반환 boxes/links 는 좌표 + 데이터만 담고,
 * 클릭 처리는 화면이 box.id / box.deviceId 로 위임한다.
 */
export function buildTopo(a, b) {
  const { fleet, state } = _resolve(a, b);
  const S = state;
  const ko = langOf(S) === 'ko';
  const L = (en, k) => (ko ? k : en);
  const SERVERS = fleet;
  const collapsed = S.collapsed || {};
  const companyColors = S.companyColors || {};
  const topoFocus = S.topoFocus || null;
  // 콘솔 점검 창 — 활성 창 장비는 배지·요약 카운터에서 묵음(#19). overview 의 '활성'
  // 정의(ARCHITECTURE.md §4.1 '활성 창 장비는 경보·주의 필요·배지에서 묵음')와 같은 계약이다.
  const maintMap = activeMaint(S);
  const inMaint = (id) => Object.prototype.hasOwnProperty.call(maintMap, id);

  // 장비별 경보 집계 — 카드 배지와 우측 요약이 이 하나를 공유한다(#261).
  // 폴러 meta.alertCounts 가 있으면 캡(25건, data.js)된 meta.alerts 배열보다 우선한다.
  // 점검 창 장비는 묵음(#19) — overview '활성' 정의와 같은 계약(ARCHITECTURE.md §4.1).
  const devAlertTally = (s) => {
    const m = _meta(s);
    const maintWin = inMaint(s.id);
    const als = maintWin ? [] : _arr(m.alerts);
    const ac = (!maintWin && m.alertCounts && typeof m.alertCounts === 'object') ? m.alertCounts : null;
    const hasAC = !!ac && (ac.critical != null || ac.warning != null || ac.info != null);
    const crit = hasAC ? _num(ac.critical) : als.filter((a) => (a.sev || a.severity) === 'critical').length;
    const warn = hasAC ? _num(ac.warning) : als.filter((a) => (a.sev || a.severity) === 'warning').length;
    const info = hasAC ? _num(ac.info) : 0;
    return { n: hasAC ? (crit + warn + info) : als.length, crit, warn, info, maintWin };
  };

  const isColDev = (s) => !!collapsed['topo:' + s.id];
  const isColCo = (co) => !!collapsed['co:' + co];
  const isColFa = (co, fa) => !!collapsed['fa:' + co + '/' + fa];
  const isColNd = (s) => !!collapsed['nd:' + s.id];

  // #432: 미분류 그룹의 상태 키(접힘 'co:'·'fa:'…, topoFocus 매칭)는 언어 중립 슬러그로
  // 고정한다 — L() 표시 문자열을 키 재료로 쓰면 KO 에서 저장된 접힘·포커스가 EN 전환
  // 즉시 다른 키가 되어 소실(캔버스가 전체 보기로 점프)했다. 지역화는 표시 라벨만.
  const UNASSIGNED_CO = '(unassigned)';   // company 결측 그룹 슬러그(표시 문자열 아님)
  const UNASSIGNED_FA = '(no-factory)';   // factory 결측 그룹 슬러그
  const coOf = (s) => _meta(s).company || UNASSIGNED_CO;
  const faOf = (s) => _meta(s).factory || UNASSIGNED_FA;
  const coLabel = (co) => (co === UNASSIGNED_CO ? L('Unassigned', '미분류') : co);
  const faLabel = (fa) => (fa === UNASSIGNED_FA ? L('Unassigned', '미지정') : fa);
  // topoFocus 는 화면(topology.js)이 회사 박스의 표시 label 을 그대로 저장한다 — 슬러그와
  // KO/EN 표시 문자열 어느 쪽이 저장돼 있어도 미분류 그룹 하나로 정규화해 언어 전환에 견고하게.
  const focusCo = (topoFocus === UNASSIGNED_CO || topoFocus === 'Unassigned' || topoFocus === '미분류')
    ? UNASSIGNED_CO : topoFocus;
  const TORD = TYPE_KEYS;

  // 다른 장비의 게스트 VM으로 등록된 장비는 최상위 박스로 그리지 않는다.
  const hostedIps = new Set();
  SERVERS.forEach((s) => {
    if (!isFT(s.type)) _arr(_meta(s).vmList).forEach((vm) => { if (vm && vm.ip) hostedIps.add(vm.ip); });
  });

  const coMap = Object.create(null);
  SERVERS.forEach((s) => {
    if (hostedIps.has(_meta(s).mgmt)) return;
    const co = coOf(s); const fa = faOf(s);
    const c = coMap[co] || (coMap[co] = Object.create(null));
    (c[fa] || (c[fa] = [])).push(s);
  });
  Object.keys(coMap).forEach((co) => Object.keys(coMap[co]).forEach((fa) => coMap[co][fa].sort(devSort)));
  // G3: 실장비(폴러 meta.topo) 회사를 맨 위로 — 초기 fit 화면(상단부)에서 실제 인프라가
  //     먼저 읽힌다. 대형 시뮬 플릿에 실클러스터가 아래로 밀려 첫 화면 밖으로 잘리던 문제.
  const coHasReal = (co) => Object.keys(coMap[co]).some((fa) => coMap[co][fa].some((s) => !!topoOf(s)));
  const companies = Object.keys(coMap).sort((a, b) => {
    const ra = coHasReal(a) ? 0 : 1; const rb = coHasReal(b) ? 0 : 1;
    return ra !== rb ? ra - rb : cmpKo(coLabel(a), coLabel(b));
  });
  const realCos = companies.filter((c) => c !== UNASSIGNED_CO);
  // 기본색 산출은 모듈 단일 함수(buildCompanyColors 와 공용 — #52).
  const coDefaults = coDefaultColorMap(realCos, coHasReal);
  const coColor = (co) => companyColors[co]
    || (co === UNASSIGNED_CO ? null : coDefaults[co]);
  const shown = (focusCo && companies.indexOf(focusCo) >= 0) ? [focusCo] : companies;
  const sLabel = (st) => statusLabel(st, L);

  /* ---- 비FT 장비의 타입별 메타 행 ---- */

  const boxes = [];
  const links = [];
  let packetN = 0;
  // G32: 캔버스 신문단(회사 컬럼)이 배정되기 전까지는 모든 좌표가 "열 0" 로컬 기준이다.
  // pushBox/link 가 최종 삽입 순간에 curXOff 를 더해 절대좌표로 승격한다 — 하위 헬퍼
  // (pushDevice/pushRealRow/hLink 등) 는 전혀 손대지 않아도 된다.
  let curXOff = 0;
  const pushBox = (o) => { o.x = r1(o.x + curXOff); boxes.push(o); return o; };


  /** 폴리라인 링크 1개(상태색 · 패킷 애니 · 단절 X 마커 좌표 포함). */
  const link = (pts, st, col) => {
    const sp = curXOff ? pts.map((p) => ({ x: p.x + curXOff, y: p.y })) : pts;
    const m = polyMid(sp);
    const tone = statusTone(st);
    links.push({
      id: 'lk' + links.length,
      path: orthPath(sp, 12),
      midX: r1(m.x), midY: r1(m.y),
      // Operational paint is semantic; companyColor is independently categorical data.
      tone,
      strokeTone: tone,
      packetTone: tone,
      companyColor: col || null,
      // 흐름점(SMIL)은 '저하(deg) 엣지'에만 켠다 — 정상은 정적 라인, 단절(down)은 X 마커.
      // 상시 애니 밀도를 낮춰(정상 59링크 흐름 → 0) 움직임이 '주의가 필요한 곳'만 가리게 한다(minor T1).
      dashed: st === 'down', packet: st === 'deg',
      // 음수 begin — 양수면 시작 전까지 패킷 원이 (0,0) 에 멈춰 있어 캔버스 좌상단에
      // 정체불명의 점이 찍힌다. 음수는 "이미 그만큼 진행된 상태"로 즉시 시작한다.
      begin: (-((packetN++ % 5) * 0.5)).toFixed(2),
    });
  };
  /** 좌→우 단계 연결(수평 S자 엘보). */
  const hLink = (x1, y1, x2, y2, st, col) => {
    const mx = x1 + (x2 - x1) * 0.5;
    link([{ x: x1, y: y1 }, { x: mx, y: y1 }, { x: mx, y: y2 }, { x: x2, y: y2 }], st, col);
  };
  /* ---- 펼침 가능 여부 / 펼침 상태 ----
     기본값은 "접힘"이다. collapsed['topo:<id>'] === false 일 때만 가지를 펼친다.
     (27대 × 노드/EAC/VM 을 전부 펼치면 어떤 배치로도 한 화면에 읽히지 않는다) */
  const expandable = (s) => (isFT(s.type)
    ? _arr(_meta(s).nodes).length > 0
    // 비-FT(NAS/PLC/프린터)는 VM 이 없어도 타입 상세(kv rows)가 있으면 펼칠 수 있다.
    : (_arr(_meta(s).vmList).length > 0 || !!typeRows(s, _meta(s), L)));
  // (사용자 지시) 장비 박스는 접기 기능 없이 항상 풀 정보 — '−'로 작게 만드는 동작 자체를 제거.
  const isExp = (s) => expandable(s);

  const fullDevH = (s) => {
    if (isFT(s.type) && s.type !== 'END') return XD.h;
    if (s.type === 'END') {
      // 흐름 카드 예산: 고정 영역 ~120px + ACTIVE 행 ~48px + 그룹 타이틀 ~18px
      // + 가장 긴 열의 박스 수 × ~54px(열 안 세로 스택, 넓어진 간격 반영).
      const f = enduFlow(s, _meta(s));
      let maxCol = 1;
      f.groups.forEach((g) => g.cols.forEach((c) => { if (c.length > maxCol) maxCol = c.length; }));
      return Math.max(XD.h, 186 + maxCol * 54);
    }
    const rows = typeRows(s, _meta(s), L);
    // 비FT 풀카드 실측 예산: 고정 영역 88px + kv 행당 20px(18px line + 2px gap).
    // 브라우저/폰트별 반올림 여유 4px를 더한다. 구 96+16n은 NAS 4행을 160px로
    // 잡아 실제 scrollHeight 166px보다 작았고, overflow:hidden이 하단 VM 줄 여백을 잘랐다.
    return rows ? Math.max(XD.h, 92 + rows.length * 20) : XD.h;
  };
  // VM 그룹은 **기본 펼침**이다. 요구된 체인(노드 → VM그룹 → 개별VM)이 초기 fit 에서 좌→우로
  // 읽혀야 하고, 실측상 펼친 캔버스에서 node→VM그룹이 fit 안에 들어온다(맨 오른쪽 개별 VM 만
  // 소폭 팬으로 확인 — 예전 4단 배치도 VM 이 넘쳐 팬이 필요했다). 개수 배지는 펼침/접힘 모두 보인다.
  // collapsed['vg:<id>:<node>']===true 로 눌러 접으면 그 노드 VM 은 폭/높이 계산에서 빠져 캔버스가 좁아진다.
  const vgColOf = (s) => (nodeName) => collapsed['vg:' + s.id + ':' + nodeName] === true;
  // 장비 우측 가지(관리화면·노드·VM) 펼침 여부 — 기본 펼침, '−' 로 접음. 박스 자체는 불변.
  const branchOpenOf = (s) => collapsed['topo:' + s.id] !== true;
  /** 실데이터 주 체인 밴드(장비·관리화면·노드레인) 높이 — 인프라는 이 밑에 깐다.
      노드 레인은 [노드 → VM그룹 → 그 노드의 VM들]을 세로로 쌓은 realLanes.total 이다. */
  const realMainH = (s, m) => Math.max(XD.h, RE.h, realLanes(m, vgColOf(s)).total);
  // G31(사용자 지시): 네트워크/스토리지 보조 레인은 토폴로지에서 제거됐고,
  // 인프라 상세는 장비 상세 화면이 담당한다.
  const xRowH = (s) => {
    const m = _meta(s);
    // END(Endurance)는 체인 없는 단일 카드 — 행 높이는 곧 카드 높이(미니 박스 그리드 포함).
    // 이 계획값이 fullDevH 보다 작으면 다음 카드가 이 카드의 하단 박스를 덮는다(실측 겹침).
    if (s.type === 'END') return fullDevH(s);
    if (!isFT(s.type)) return Math.max(fullDevH(s), vmBlock(_arr(m.vmList).length).h);
    const t = topoOf(s);
    // 가지 접힘 — 행 높이는 장비 박스만.
    if (t && !branchOpenOf(s)) return XD.h;
    // 실데이터: 주 체인 밴드(기본) + 펼쳤을 때만 인프라 보조 레인.
    if (t) return realMainH(s, m);
    const ns = _arr(m.nodes).slice(0, 2);
    const nodesH = stackH(ns.length, XN.h, XN.gy);
    if (isColNd(s)) return Math.max(XD.h, nodesH);
    return Math.max(XD.h, nodesH, XE.h, vmBlock(_arr(m.vmList).length).h);
  };
  const xRowW = (s) => {
    const m = _meta(s);
    // END 단일 카드는 흐름 카드 폭(3열 그룹)만 차지한다(체인 예약 폭 없음).
    if (s.type === 'END') return ENDU.w;
    if (!isFT(s.type)) return XN.x + vmBlock(_arr(m.vmList).length).w;
    const t = topoOf(s);
    if (t && !branchOpenOf(s)) return XD.w;      // 가지 접힘 — 장비 박스 폭까지만
    if (t) {
      // 노드별로: VM 그룹 펼침 → VM블록 오른쪽 끝까지, 접힘 → 그룹 박스까지, VM 없음 → 노드까지.
      const { lanes } = realLanes(m, vgColOf(s));
      let mainRight = RN.x + RN.w;
      lanes.forEach((l) => {
        if (l.vms.length && !l.collapsed) mainRight = Math.max(mainRight, RV.x + l.blk.w);
        else if (l.vms.length) mainRight = Math.max(mainRight, RG.x + RG.w);
      });
      return mainRight;
    }
    if (isColNd(s)) return XN.x + XN.w;
    const vb = vmBlock(_arr(m.vmList).length);
    return vb.cols ? XV.x + vb.w : XE.x + XE.w;
  };

  /** 접힌 장비 n 대를 D열로 컬럼-메이저 랩(위→아래 채우고 다음 열)한 그리드 계획.
   *  G32: 예전 그리드 패킹(행-메이저, 행당 D개)은 형제가 우측으로 흘러 'A→B' 직렬 체인으로
   *  오독됐다(G19). 컬럼-메이저는 같은 행의 이웃이 서로 다른 열(=다른 스파인)에 속해
   *  좌→우 흐름으로 읽히지 않는다 — 각 열은 독립된 세로 스택(형제 나열)일 뿐이다. */
  const gridBlock = (items, D) => {
    const n = items.length;
    if (!n) return { rowsPerCol: 0, cols: 0, w: 0, h: 0, cells: [] };
    const rowsPerCol = Math.max(1, Math.ceil(n / D));
    const cols = Math.max(1, Math.ceil(n / rowsPerCol));
    const cells = items.map((s, i) => ({ s, col: Math.floor(i / rowsPerCol), row: i % rowsPerCol }));
    return {
      rowsPerCol, cols, cells,
      w: cols * CARD.w + (cols - 1) * CARD.gx,
      h: rowsPerCol * CARD.h + (rowsPerCol - 1) * CARD.gy,
    };
  };

  /** 회사 1개의 공장 밴드 계획(로컬 y=0 기준, 아직 캔버스 컬럼에 배정되지 않은 상태). */
  const planCompany = (co, D) => {
    // 렌더 패스·pRows 와 동일 기준(표시 라벨) — #432 슬러그 '(no-factory)' 도입 후
    // 원시 키 정렬(ASCII '(' 선두)과 표시 정렬('미지정' 위치)이 어긋나던 불일치 해소.
    const fas = Object.keys(coMap[co]).sort((a, b) => cmpKo(faLabel(a), faLabel(b)));
    const colCo = isColCo(co);
    let maxRowW = CARD.w;
    let y = 0;
    const bands = [];
    if (colCo) {
      y += CO.h;
    } else {
      fas.forEach((fa, fi) => {
        if (fi) y += BAND_GAP;
        const bandTop = y;
        const rows = [];
        if (isColFa(co, fa)) {
          y += FA.h;
        } else {
          // 접힌 장비는 연속 구간(batch)으로 모았다가 D열 그리드블록 하나로 랩한다.
          // 펼친 장비가 끼어들면 그 시점에 batch 를 그리드블록으로 확정하고, 펼친 장비는
          // 밴드 전폭 단독 행을 그대로 쓴다(체인 시맨틱 유지).
          let batch = [];
          const flush = () => {
            if (!batch.length) return;
            const block = gridBlock(batch, D);
            rows.push({ kind: 'gridblock', block, h: block.h });
            batch = [];
          };
          coMap[co][fa].forEach((s) => {
            if (isExp(s)) { flush(); rows.push({ kind: 'full', dev: s, h: xRowH(s) }); } else batch.push(s);
          });
          flush();
          if (!rows.length) rows.push({ kind: 'gridblock', block: gridBlock([], D), h: FA.h });
          let ry = y;
          rows.forEach((r, i) => {
            if (i) ry += (r.kind === 'full' || rows[i - 1].kind === 'full') ? ROW_GAP_FULL : CARD.gy;
            r.y = ry; ry += r.h;
            const rw = r.kind === 'full' ? xRowW(r.dev) : r.block.w;
            if (rw > maxRowW) maxRowW = rw;
          });
          y = ry;
        }
        bands.push({ fa, top: bandTop, h: y - bandTop, rows });
      });
    }
    return { co, h: y, collapsed: colCo, bands, w: GRID_X + maxRowW };
  };

  /** dy(세로) · xOff(가로, 캔버스 컬럼) 를 더한 절대좌표 사본(원본은 순수하게 재사용 가능하게 둔다). */
  const offsetCompany = (p, dy, xOff) => ({
    co: p.co, collapsed: p.collapsed, top: dy, h: p.h, xOff,
    bands: p.bands.map((bd) => ({
      fa: bd.fa, top: bd.top + dy, h: bd.h,
      rows: bd.rows.map((r) => Object.assign({}, r, { y: r.y + dy })),
    })),
  });

  /** 회사 블록(원자적)을 캔버스 C열로 신문단 배치 — 목표 열당 높이를 넘기면 다음 열로 넘어간다.
   *  C 보다 실제로 쓰인 열이 적으면(colsUsed) 데이터가 작다는 뜻 — 상위 D·C 탐색에서 중복 스킵. */
  const packFor = (D, C) => {
    const plans = shown.map((co) => planCompany(co, D));
    let totalH = 0;
    plans.forEach((p, i) => { totalH += p.h + (i ? CO_GAP : 0); });
    const target = C > 1 ? totalH / C : Infinity;
    const cols = [[]];
    const colH = [0];
    plans.forEach((p) => {
      let ci = cols.length - 1;
      const addH = cols[ci].length ? CO_GAP + p.h : p.h;
      if (cols[ci].length && cols.length < C && colH[ci] + addH > target) {
        cols.push([]); colH.push(0); ci += 1;
      }
      colH[ci] += cols[ci].length ? CO_GAP + p.h : p.h;
      cols[ci].push(p);
    });
    let x = 0;
    let maxH = 0;
    const laid = [];
    cols.forEach((list) => {
      const w = list.length ? Math.max.apply(null, list.map((p) => p.w)) : (GRID_X + CARD.w);
      let y = PAD;
      list.forEach((p, i) => {
        if (i) y += CO_GAP;
        laid.push(offsetCompany(p, y, x));
        y += p.h;
      });
      if (y + PAD > maxH) maxH = y + PAD;
      x += w + CANVAS_COL_GAP;
    });
    const usedW = cols.length ? x - CANVAS_COL_GAP : 0;
    return { cos: laid, w: usedW + PAD, h: maxH, colsUsed: cols.length };
  };

  // (D, C) 탐색: 종횡비가 "허용 범위"(TARGET_RATIO 의 로그 ±RATIO_TOL, ≈ 1.0~2.6 — Acceptance
  // 계약과 동일 창) 안에 드는 후보 중, 기준 뷰포트(REF_W×REF_H — 계층도 스테이지 기본 내부
  // 여백 실측치, topology.js FIT_PAD 와 동일 값)에 **가장 크게 맞춰지는(fit 배율 최대)** 조합을 고른다.
  // G32: 면적만 최소화하면 폭 3042×높이 1200(가로가 REF_W 를 훨씬 넘는) 같은 조합이 이겨 여전히
  // 좌우가 잘렸다(실측). fit 배율(=min(REF_W/w, REF_H/h))을 직접 목적함수로 삼아야 "스테이지 안에
  // 실제로 얼마나 크게 들어오는가"를 최적화한다 — 데이터가 작으면(회사 포커스 등) 열을 늘려도
  // fit 배율 이득이 없어 자연히 D=1·C=1 로 수렴한다.
  const REF_W = 926;   // FIT_PAD 좌우(14+56) 를 뺀 기본 스테이지 내부 폭 실측 근사치
  const REF_H = 618;   // FIT_PAD 상하(12+30) 를 뺀 기본 스테이지 내부 높이 실측 근사치
  const RATIO_TOL = Math.log(2.6 / TARGET_RATIO); // ≈ 0.474 (허용 비율 1.0~2.6 과 대칭)
  let best = null;      // 허용 비율 안, fit 배율 최대
  let fallback = null;  // 허용 비율 밖일 때의 fit 배율 최대(안전망)
  for (let D = 1; D <= MAX_D; D += 1) {
    for (let C = 1; C <= MAX_C; C += 1) {
      const pl = packFor(D, C);
      if (pl.colsUsed < C) continue; // 이미 더 작은 C 로 같은 결과가 나왔다 — 중복 스킵.
      const ratioDev = Math.abs(Math.log((pl.w / Math.max(1, pl.h)) / TARGET_RATIO));
      const fitS = Math.min(REF_W / pl.w, REF_H / pl.h);
      if (ratioDev <= RATIO_TOL && (!best || fitS > best.fitS)) best = { fitS, pl, D, C };
      if (!fallback || fitS > fallback.fitS) fallback = { fitS, pl, D, C };
    }
  }
  if (!best) best = fallback;
  const P = best.pl;

  /* ---- 장비 박스 1개(접힌 컴팩트 카드 / 펼친 전체 카드) ---- */
  const pushDevice = (s, x, y, full, hOverride) => {
    const m = _meta(s);
    const ti = typeInfo(s.type, L);
    const ftDev = isFT(s.type);
    const li = licInfo(m, L);
    const cu = usageOf(s, 'cpu');
    const mu = usageOf(s, 'mem');
    const rowsAll = (ftDev && s.type !== 'END') ? null : typeRows(s, m, L);
    const compact = !full;
    // 텔레메트리가 없는 장비(PLC 등)의 컴팩트 카드는 상태를 담은 행 1개만 보여 준다.
    const keyRow = rowsAll ? [(rowsAll.find((r) => r.tone && r.tone !== 'mut') || rowsAll[0])] : null;
    // 활성 경보 뱃지 — 폴러 meta.alertCounts(있으면 우선) 또는 meta.alerts 심각도 집계.
    // 점검 창(maintWin) 장비는 묵음(#19) — overview '활성' 정의와 같은 계약(ARCHITECTURE.md §4.1).
    const tally = devAlertTally(s);
    const maintWin = tally.maintWin;
    const aCrit = tally.crit;
    const aWarn = tally.warn;
    const alertN = tally.n;
    pushBox({
      kind: 'device', id: s.id, key: 'topo:' + s.id, deviceId: s.id,
      x, y, w: compact ? CARD.w : (s.type === 'END' ? ENDU.w : XD.w), h: compact ? CARD.h : (hOverride || XD.h),
      compact,
      alertN, alertCrit: aCrit, alertWarn: aWarn,
      alertTone: aCrit > 0 ? 'neg' : 'warn',
      // 점검 창 장비 표시(#19) — topology.js 가 상태 배지를 '점검 중'으로 대체하고 사유를 툴팁에 둔다.
      maintWin, maintNote: maintWin ? String((maintMap[s.id] || {}).note || '') : '',
      label: m.label || s.host, typeIcon: ti.icon, typeLabel: ti.short || ti.label, mgmt: m.mgmt || '',
      // E1: 플로어 맵 타일 라벨이 '대원정밀 EDGE-…' 처럼 회사 프리픽스에서 절단돼 고유 식별번호(EDGE-27)가
      //     사라졌다. 회사명을 벗긴 장비코드(code)와 회사(company)를 함께 내려, 타일이 코드를 우선 노출한다.
      company: m.company || '',
      code: deviceCode(m.label || s.host, m.company),
      // 플로어 맵 실배치('행,열', 장비 관리에서 편집) — 빈 값이면 플로어 뷰가 자동 배치.
      floorPos: String(m.floorPos || ''),
      statusLabel: sLabel(s.status), tone: statusTone(s.status),
      anim: statusAnim(s.status),
      version: String(m.version || (m.unit && m.unit.version) || ''),
      // 컴팩트 카드는 라이선스 그리드 대신 CPU/MEM 막대(또는 대표 1행)를 쓴다.
      // END(ztC Endurance)는 FT 라이선스 그리드가 아니라 서브시스템 미니 박스(enduBoxes)를
      // 쓴다 — 2U 단일 섀시 1대로 표현하는 사용자 지시에 따라 CM-A/B 체인도 펼치지 않는다.
      isFT: compact ? false : (ftDev && s.type !== 'END'),
      enduFlow: (!compact && s.type === 'END') ? enduFlow(s, m) : null,
      // G26: 렌더 분기(isFT)와 별개로 '실제 FT 여부'를 항상 내린다 — 컴팩트 FT 카드의 meta 가
      //     isFT=false 로 위장돼 비FT 용 'type · IP · 버전' 긴 문자열을 받아 맥(SF Mono, 광폭)에서
      //     잘리던 원인. meta 는 이 플래그로 판단한다.
      ftType: ftDev,
      rows: compact ? ((cu.na && mu.na && keyRow) ? keyRow : null) : rowsAll,
      licLabel: li.label, licDTxt: li.dTxt, licTone: li.tone, licExpLabel: L('EXP', '만료'),
      cpu: cu.val, cpuNA: cu.na, cpuTone: cu.tone,
      mem: mu.val, memNA: mu.na, memTone: mu.tone,
      vmLabel: (m.vms || 0) ? ((m.vmRunning != null ? m.vmRunning : 0) + '/' + (m.vms || 0)) : null,
      syncLabel: ftDev
        ? (s.sync === 'sync' ? L('in sync', '동기화') : (s.sync === 'simplex' ? L('simplex', '심플렉스') : L('offline', '오프라인')))
        : (s.status === 'down' ? L('offline', '오프라인') : L('online', '온라인')),
      syncTone: (ftDev ? s.sync === 'sync' : s.status !== 'down') ? 'pos' : (s.sync === 'simplex' ? 'warn' : 'neg'),
      // 장비 박스 자체는 상시 풀 정보(축소 없음). +/− 는 우측 가지(관리화면·노드·VM)만 접고 편다 —
      // 가지가 있는 장비(FT)에만 노출(사용자 지시: '필요한 것들만').
      collapsible: !compact && ftDev && !!topoOf(s),
      collapsed: collapsed['topo:' + s.id] === true,
    });
  };

  /**
   * 실데이터 펼침 행 — 주 계층 체인을 좌→우 직렬로 편다.
   *   장비 ─▶ 관리화면(EAC) ─▶ node0/node1(FT 페어) ─▶ VM(배치 노드에서 분기)
   *   - bandCy : 주 체인 세로 중앙   - rowTop : 행 상단   - mainH : 주 체인 밴드 높이
   * 장비 카드와 회사→공장 간선은 호출부에서 이미 그렸다.
   */
  const pushRealRow = (s, m, t, bandCy, rowTop, mainH, cc) => {
    const nodes = _arr(m.nodes).slice(0, 2);
    const snmp = _arr(m.snmp);
    const eacOk = s.status !== 'down' && !m.error;

    /* 1) 관리화면(EAC) — 장비 다음(주 체인 2단). 클러스터 관리 IP 를 보여 준다. */
    const eacX = GRID_X + RE.x;
    pushBox({
      kind: 'eac', id: s.id + ':eac', deviceId: s.id,
      x: eacX, y: r1(bandCy - RE.h / 2), w: RE.w, h: RE.h,
      mgmt: m.mgmt || '', ok: eacOk, tone: eacOk ? 'pos' : 'neg',
      okLabel: eacOk ? L('Connected', '정상 연결') : L('No response', '응답 없음'),
    });
    // 장비 → 관리화면
    link([{ x: GRID_X + XD.w, y: bandCy }, { x: eacX, y: bandCy }], eacOk ? 'op' : 'down', cc);

    /* 2) 노드 레인 — 각 노드가 [노드 → VM그룹 → 그 노드의 VM들] 한 레인을 차지한다(3~5단).
       레인들을 주 체인 세로 중앙(bandCy)에 맞춰 위→아래로 쌓는다(realLanes = 계획과 공통). */
    const { lanes, total } = realLanes(m, vgColOf(s));
    const nodeCy = [];
    const nodeSt = [];
    const nodeRight = GRID_X + RN.x + RN.w;
    const grpX = GRID_X + RG.x;
    let laneTop = bandCy - total / 2;
    lanes.forEach((lane, i) => {
      const n = lane.node;
      const st = nodeStatus(n);
      const nMaint = _nodeMaint(n);
      const eff = nMaint ? 'deg' : st;
      const laneCy = laneTop + lane.h / 2;
      nodeCy.push(laneCy); nodeSt.push(st);

      /* 노드 박스(레인 세로 중앙). G31(사용자 지시): 노드의 인프라(네트워크/스토리지) '+' 토글 제거 —
         펼치면 행 높이가 급팽창해 아래 형제들이 화면 밖으로 밀렸고, 노드/인프라 상세는 카드 클릭
         (data-topo-open → 상세 화면)이 이미 담당한다. 토폴로지는 주 체인(장비→관리→노드→VM)만. */
      pushBox({
        kind: 'node', id: s.id + ':' + (n.name || ('node' + i)), deviceId: s.id,
        x: GRID_X + RN.x, y: r1(laneCy - RN.h / 2), w: RN.w, h: RN.h,
        name: n.name || ('node' + i),
        role: n.primary === true ? L('PRIMARY', '주 노드') : (n.primary === false ? L('STANDBY', '보조 노드') : ''),
        roleTone: n.primary === true ? 'pos' : 'mut', maint: nMaint,
        ip: (snmp[i] && snmp[i].ip) ? snmp[i].ip : (n.ip || ''),
        cpu: (snmp[i] && snmp[i].cpu != null) ? snmp[i].cpu : null,
        mem: (snmp[i] && snmp[i].mem != null) ? snmp[i].mem : null,
        cpuTone: pctTone(snmp[i] && snmp[i].cpu),
        memTone: pctTone(snmp[i] && snmp[i].mem),
        tone: statusTone(eff),
        stateLabel: nMaint ? L('Maintenance', '점검 중') : sLabel(st),
        anim: statusAnim(eff),
        collapsible: false, collapsed: false,
      });
      // 관리화면 → 노드 (EAC 가 두 노드를 관리 = FT 페어 분기)
      hLink(eacX + RE.w, bandCy, GRID_X + RN.x, laneCy, eacOk ? st : 'down', cc);

      /* 3) VM 그룹 노드 + 그 노드에 배치된 VM들(4~5단). VM 이 없는 노드는 그룹을 만들지 않는다.
         VM 그룹은 기본 펼침 — collapsed['vg:<id>:<node>']===true 로 눌러야 접힌다.
         키는 노드별로 유니크(vg:<id>:<node>)라 인프라 토글 중복 버그를 재발시키지 않는다. */
      if (lane.vms.length) {
        const grpCy = laneCy;
        const runN = lane.vms.filter((v) => /run/i.test(v.state || '')).length;
        const grpWorstSt = lane.vms.some((v) => !v.state) ? 'down'
          : (lane.vms.every((v) => /run/i.test(v.state || '')) ? 'op' : 'deg');
        const grpTone = !eacOk ? 'neg' : (nodeSt[i] === 'op' ? statusTone(grpWorstSt) : 'warn');
        const vgKey = 'vg:' + s.id + ':' + (n.name || ('node' + i));
        const vgCollapsed = lane.collapsed;
        pushBox({
          kind: 'vmgroup', id: s.id + ':vg:' + (n.name || ('node' + i)), key: vgKey, deviceId: s.id,
          x: grpX, y: r1(grpCy - RG.h / 2), w: RG.w, h: RG.h,
          count: lane.vms.length, running: runN, node: n.name || ('node' + i),
          tone: grpTone,
          collapsible: true, collapsed: vgCollapsed,
        });
        // 노드 → VM그룹
        hLink(nodeRight, laneCy, grpX, grpCy, eacOk ? st : 'down', cc);

        if (!vgCollapsed) {
          const blk = lane.blk;
          const vTop = grpCy - blk.h / 2;
          lane.vms.forEach((v, vi) => {
            const run = /run/i.test(v.state || '');
            const vst = run ? 'op' : (v.state ? 'deg' : 'down');
            const vx = GRID_X + RV.x;
            const vy = vTop + vi * (RV.h + RV.gy);
            const cy = vy + RV.h / 2;
            const fr = String(v.ft || '').toLowerCase();
            // node = **현재 배치 노드**(node.vm_placements), standbyNodes = 대기 인스턴스.
            const standby = _arr(v.standbyNodes).join('·');
            pushBox({
              kind: 'vm', id: s.id + ':vm:' + v.name, deviceId: s.id,
              x: r1(vx), y: r1(vy), w: RV.w, h: RV.h,
              compact: true,                               // 세로 스택 컴팩트 카드(이름+상태점+▸노드)
              name: v.name || 'VM',
              // 실장비에서 게스트 IP가 오면 우선 표시하고, 없을 때만 자원 스펙으로 대체한다.
              ip: String(v.ip || '').trim()
                || [v.cpus ? (v.cpus + ' vCPU') : '', v.memory || ''].filter(Boolean).join(' · '),
              ipIsAddress: !!String(v.ip || '').trim(),
              ftL: fr === 'ft' ? 'FT' : (fr === 'ha' ? 'HA' : ''),
              ftTone: fr === 'ft' ? 'pos' : 'info',  // G25: HA=범주(info), warn 은 저하 전용
              node: String(v.node || ''),
              title: (v.name || 'VM')
                + (v.node ? ' · ' + L('on ', '배치 ') + v.node : '')
                + (standby ? ' · ' + L('standby ', '대기 ') + standby : ''),
              state: run ? L('Running', '실행 중') : (v.state || DASH),
              tone: statusTone(vst),
            });
            hLink(grpX + RG.w, grpCy, vx, cy, nodeSt[i] === 'op' ? vst : 'deg', cc);
          });
        }
      }
      laneTop += lane.h + LANE_GAP;
    });
  };

  /* ---- 2차 패스: 계획 좌표로 박스·링크를 만든다 ---- */
  P.cos.forEach((cp) => {
    curXOff = cp.xOff;    // G32: 이 회사가 배정된 캔버스 컬럼(신문단) 절대 x — 이후 pushBox/link 가 소비.
    const co = cp.co;
    const cc = coColor(co);
    const coCy = cp.top + cp.h / 2;
    const fas = Object.keys(coMap[co]).sort((a, b) => cmpKo(faLabel(a), faLabel(b)));
    const coWorst = grpWorst(fas.reduce((acc, fa) => acc.concat(coMap[co][fa]), []));
    pushBox({
      kind: 'company', id: 'co:' + co, key: 'co:' + co,
      x: CO.x, y: r1(coCy - CO.h / 2), w: CO.w, h: CO.h, label: coLabel(co),
      tone: statusTone(coWorst),
      color: cc, focused: focusCo === co,
      collapsible: true, collapsed: cp.collapsed,
    });

    cp.bands.forEach((bd) => {
      const devs = coMap[co][bd.fa];
      const faCy = bd.top + bd.h / 2;
      const fWorst = grpWorst(devs);
      pushBox({
        kind: 'factory', id: 'fa:' + co + '/' + bd.fa, key: 'fa:' + co + '/' + bd.fa,
        x: FA.x, y: r1(faCy - FA.h / 2), w: FA.w, h: FA.h, label: faLabel(bd.fa),
        tone: statusTone(fWorst),
        count: devs.length, color: cc, collapsible: true, collapsed: isColFa(co, bd.fa),
      });
      // 회사→공장 간선은 그 공장의 상태로 칠한다(회사 전체 최악 상태로 칠하면
      // 정상 공장 링크까지 전부 끊긴 것처럼 보인다).
      hLink(CO.x + CO.w, coCy, FA.x, faCy, fWorst, cc);

      bd.rows.forEach((row) => {
        /* --- 접힌 장비(1대 = 1행, G19 세로 스택): 공장 → 세로 스파인 → 카드 좌변 중앙 ---
           G18+G19: 상단 진입(레일)도 우측 흐름(그리드 패킹)도 폐기 — 공장 우변에서 나온 간선이
           스파인을 타고 각 형제 행의 세로 중앙까지 내려와 좌변 중앙으로 들어가는 고전 트리 문법. */
        if (row.kind === 'gridblock') {
          // G32: 컬럼-메이저 D열 그리드블록 — 열마다 자기 스파인(공장 우변→그 열 세로선→
          // 카드 좌변 중앙)을 둔다. 같은 행의 이웃이 다른 열(=다른 스파인)에 속해 좌→우
          // 체인으로 오독되지 않는다(G19 계승, 세로=형제 나열 시맨틱 유지).
          row.block.cells.forEach((cell) => {
            const colX = GRID_X + cell.col * (CARD.w + CARD.gx);
            // G34: colX-22 → colX-13 — CARD.gx 8→26 페어(거터 중앙 근접, 접기 손잡이와 2px 이격).
            const spineX = colX - 13;
            const cy = row.y + cell.row * (CARD.h + CARD.gy) + CARD.h / 2;
            pushDevice(cell.s, colX, row.y + cell.row * (CARD.h + CARD.gy), false);
            link([
              { x: FA.x + FA.w, y: faCy }, { x: spineX, y: faCy },
              { x: spineX, y: cy }, { x: colX, y: cy },
            ], cell.s.status, cc);
          });
          return;
        }

        /* --- 펼친 장비: 행 하나를 통째로 써서 노드/EAC/VM 을 좌→우로 흘린다 --- */
        const s = row.dev;
        const m = _meta(s);
        const ftDev = isFT(s.type);
        const snmp = _arr(m.snmp);
        const dh = fullDevH(s);
        // 실데이터 펼침 행은 주 체인(장비→관리→노드→VM)을 위쪽 밴드에 두고 인프라를 그 아래에 깐다.
        // 그래서 세로 중앙(devCy)은 '행 중앙'이 아니라 '주 체인 밴드 중앙'이다(그 외 경로는 행 중앙 그대로).
        const realTopo = ftDev ? topoOf(s) : null;
        // 가지(관리화면·노드·VM)는 '−' 로 접을 수 있다 — 장비 박스는 항상 풀 정보.
        const realExp = !!realTopo && branchOpenOf(s);
        const mainH = realExp ? realMainH(s, m) : row.h;
        const devCy = row.y + mainH / 2;
        pushDevice(s, GRID_X, r1(devCy - dh / 2), true, dh);
        hLink(FA.x + FA.w, faCy, GRID_X, devCy, s.status, cc);
        // END(ztC Endurance)는 단일 섀시 카드로 종결 — CM-A/B 체인·VM 가지를 펼치지 않는다.
        if (s.type === 'END') return;
        if (realTopo && !branchOpenOf(s)) return;                // 가지 접힘 — 우측 체인 생략

        if (!ftDev) {
          // 비FT 호스트(NAS 등)의 게스트 VM
          const hosted = _arr(m.vmList);
          const hb = vmBlock(hosted.length);
          const hTop = devCy - hb.h / 2;
          hosted.forEach((vm, vi) => {
            const g = vm.ip ? SERVERS.find((x) => _meta(x).mgmt === vm.ip) : null;
            const vmRun = /run/i.test(String(vm.state || ''));
            const gst = g ? g.status : (vm.state ? (vmRun ? 'op' : 'down') : 'op');
            const vx = GRID_X + XN.x;
            const vy = hTop + vi * (XV.h + XV.gy);
            const cy = vy + XV.h / 2;
            pushBox({
              kind: 'vm', id: s.id + ':vm:' + (vm.ip || vm.name), deviceId: g ? g.id : s.id,
              x: r1(vx), y: r1(vy), w: XV.w, h: XV.h,
              name: (g && (_meta(g).label || g.host)) || vm.name || 'VM', ip: String(vm.ip || ''),
              ftL: '',
              node: g ? ((TYPES[g.type] || {}).short || g.type) : (vm.vcpu ? vm.vcpu + ' vCPU' : ''),
              state: g ? sLabel(g.status) : (vm.state ? (vmRun ? L('Running', '실행 중') : L('Stopped', '정지')) : L('unmonitored', '미수집')),
              tone: statusTone(gst),
            });
            hLink(GRID_X + XD.w, devCy, vx, cy, gst, cc);
          });
          return;
        }

        // 실데이터(폴러 meta.topo)가 있으면 관리화면·노드·VM 주 체인을 편다.
        // 노드 가지를 접은 상태에서는 예전 경로(장비→노드만)를 그대로 쓴다.
        if (realExp) {
          pushRealRow(s, m, realTopo, devCy, row.y, mainH, cc);
          return;
        }

        const rawN = _arr(m.nodes).slice(0, 2);
        const nodeCol = isColNd(s);
        const eacCy = devCy;
        const eacOk = s.status !== 'down' && !m.error;
        if (!nodeCol) {
          pushBox({
            kind: 'eac', id: s.id + ':eac', deviceId: s.id,
            x: GRID_X + XE.x, y: r1(eacCy - XE.h / 2), w: XE.w, h: XE.h,
            mgmt: m.mgmt || '', ok: eacOk, tone: eacOk ? 'pos' : 'neg',
            okLabel: eacOk ? L('Connected', '정상 연결') : L('No response', '응답 없음'),
          });
        }
        let ny = devCy - stackH(rawN.length, XN.h, XN.gy) / 2;
        rawN.forEach((n, i) => {
          const st = nodeStatus(n);
          const nMaint = _nodeMaint(n);
          const cy = ny + XN.h / 2;
          const eff = nMaint ? 'deg' : st;
          pushBox({
            kind: 'node', id: s.id + ':' + (n.name || 'node' + i), key: 'nd:' + s.id, deviceId: s.id,
            x: GRID_X + XN.x, y: r1(ny), w: XN.w, h: XN.h,
            name: n.name || ('node' + i),
            role: n.primary === true ? L('PRIMARY', '주 노드') : (n.primary === false ? L('STANDBY', '보조 노드') : ''),
            roleTone: n.primary === true ? 'pos' : 'mut', maint: nMaint,
            ip: (snmp[i] && snmp[i].ip) ? snmp[i].ip : (n.ip || ''),
            cpu: (snmp[i] && snmp[i].cpu != null) ? snmp[i].cpu : null,
            mem: (snmp[i] && snmp[i].mem != null) ? snmp[i].mem : null,
            cpuTone: pctTone(snmp[i] && snmp[i].cpu),
            memTone: pctTone(snmp[i] && snmp[i].mem),
            tone: statusTone(eff),
            stateLabel: nMaint ? L('Maintenance', '점검 중') : sLabel(st),
            anim: statusAnim(eff),
            collapsible: true, collapsed: nodeCol,
          });
          hLink(GRID_X + XD.w, devCy, GRID_X + XN.x, cy, st, cc);
          if (!nodeCol) hLink(GRID_X + XN.x + XN.w, cy, GRID_X + XE.x, eacCy, st, cc);
          ny += XN.h + XN.gy;
        });
        if (!nodeCol) {
          const vms = _arr(m.vmList);
          const vb = vmBlock(vms.length);
          const vTop = devCy - vb.h / 2;
          vms.forEach((v, vi) => {
            const vst = /run/i.test(v.state || '') ? 'op' : (v.state ? 'deg' : 'down');
            const vx = GRID_X + XV.x;
            const vy = vTop + vi * (XV.h + XV.gy);
            const cy = vy + XV.h / 2;
            const fr = String(v.ft || '').toLowerCase();
            pushBox({
              kind: 'vm', id: s.id + ':vm:' + v.name, deviceId: s.id,
              x: r1(vx), y: r1(vy), w: XV.w, h: XV.h,
              name: v.name || 'VM', ip: String(v.ip || ''),
              ftL: fr === 'ft' ? 'FT' : (fr === 'ha' ? 'HA' : ''),
              ftTone: fr === 'ft' ? 'pos' : 'info',  // G25: HA=범주(info), warn 은 저하 전용
              node: String(v.node || ''),
              state: /run/i.test(v.state || '') ? L('Running', '실행 중') : (v.state || DASH),
              tone: statusTone(vst),
            });
            hLink(GRID_X + XE.x + XE.w, eacCy, vx, cy, vst, cc);
          });
        }
      });
    });
  });

  const TW = Math.round(P.w);
  const H = Math.round(P.h);

  /* ---- 우측 요약 + 공장별 행 ---- */
  let nodeTot = 0; let nodeUp = 0;
  SERVERS.forEach((s) => {
    const m = _meta(s);
    const ns = _arr(m.nodes);
    if (ns.length) ns.forEach((n) => { nodeTot++; if (nodeStatus(n) === 'op') nodeUp++; });
    else {
      // #440: 비FT 장비의 노드격 집계(SNMP 타깃)도 FT 노드와 같은 계약으로 — tot 에는
      // 전체를 세고 reachable 만 up 으로. 도달 불가뿐인(=다운) 장비가 r=0 으로 tot 에서
      // 증발해 attention 에 영구 0 기여하던 결함(개요 isAttn 정의와 화면 수치 발산).
      const sn = _arr(m.snmp);
      nodeTot += sn.length;
      nodeUp += sn.filter((x) => x.reachable).length;
    }
  });
  const pRows = [];
  companies.forEach((co) => Object.keys(coMap[co]).sort((a, b) => cmpKo(faLabel(a), faLabel(b))).forEach((fa) => {
    const arr = coMap[co][fa];
    const gw = grpWorst(arr);
    pRows.push({
      key: 'fa:' + co + '/' + fa, label: coLabel(co) + ' · ' + faLabel(fa), icon: 'building',
      count: arr.length, tone: statusTone(gw), statusLabel: sLabel(gw),
      collapsed: isColFa(co, fa),
    });
  }));
  let alertN = 0; let critN = 0;
  SERVERS.forEach((s) => {
    if (inMaint(s.id)) return;   // 점검 창 묵음(#19) — 카드 배지와 같은 계약
    // 배지와 같은 devAlertTally(#261) — alertCounts 가 있으면 캡(25건)된 alerts 배열이 아니라 그것으로 집계.
    const t = devAlertTally(s);
    alertN += t.n;
    critN += t.crit;
  });

  const focused = (focusCo && companies.indexOf(focusCo) >= 0) ? focusCo : null;
  // #350: hintTip 분기 재료 — 장비 + 와 VM 그룹 + 의 실존 여부를 박스 종류별로 따로 본다.
  const devPlus = boxes.some((b) => b.collapsible && b.kind === 'device');
  const vgPlus = boxes.some((b) => b.collapsible && b.kind === 'vmgroup');
  return {
    w: TW, h: H, wPx: TW + 'px', hPx: H + 'px', viewBox: '0 0 ' + TW + ' ' + H,
    // G32: 레이아웃 입력(랩 열 수 D · 캔버스 신문단 열 수 C) 시그니처 — topology.js treeSig 가
    // 박스 좌표와 별개로 소비한다. 좌표만으로도 대개 갈리지만, 접힌 장비가 0~1대라 D 변화가
    // 좌표에 반영되지 않는 극단 케이스까지 안전하게 잡기 위한 명시적 계약(사용자 지시).
    layoutSig: best.D + ':' + best.C,
    boxes, links,
    groupLabel: L('By factory', '공장별'),
    focus: focused ? coLabel(focused) : null, focusColor: focused ? coColor(focused) : null,
    focusLabel: L('show all', '전체 보기'),
    companies: companies.map((co) => ({ name: coLabel(co), color: coColor(co), custom: !!companyColors[co] })),
    palette: COMPANY_PALETTE,
    legend: [
      { key: 'op', label: L('Operational', '가동'), tone: 'pos' },
      { key: 'deg', label: L('Degraded', '저하'), tone: 'warn' },
      { key: 'down', label: L('Offline', '오프라인'), tone: 'neg' },
    ],
    summary: {
      nodes: nodeTot, healthy: nodeUp, attention: Math.max(0, nodeTot - nodeUp),
      alerts: alertN, criticals: critN,
    },
    pRows,
    // T2: 4줄 장황 안내문을 한 줄 요약으로 축약. 세부(무슨 +가 무엇을 펼치는지)는 hintTip(툴팁)으로 이관.
    hint: L('Click a card for detail · + expands each layer',
      '카드 클릭 → 상세 · + 로 하위 계층 펼침'),
    // #316: 장비 +·VM 그룹 + 도 실데이터(meta.topo) 펼침 행 전용 — 시뮬 뷰의 실존 + 는
    //     회사·공장·노드뿐이다. 화면에 실제로 그려진 + 박스 기준으로 문구를 분기해,
    //     없는 컨트롤을 가리키는 오안내를 막는다(실데이터 전용 컨트롤은 조걸부로만 안내).
    // #350: 분기를 박스 종류별로 나눈다 — 장비 + 와 VM 그룹 + 는 별개 컨트롤이라,
    //     meta.topo 는 있지만 VM 0대인 플릿(장비 + 만 존재)에서까지 'VM 그룹의 +' 를
    //     안내하면 같은 부류의 오안내다. 각 문구는 해당 컨트롤이 실존할 때만 포함한다.
    hintTip: devPlus
      ? (vgPlus
        ? L('Click a card to open its detail. The device + expands nodes / console; each VM group + toggles its VMs.',
          '카드를 클릭하면 상세로 이동합니다. 장비의 + 로 노드·관리 화면을, VM 그룹의 + 로 개별 VM 을 펼칩니다.')
        : L('Click a card to open its detail. The device + expands nodes / console.',
          '카드를 클릭하면 상세로 이동합니다. 장비의 + 로 노드·관리 화면을 펼칩니다.'))
      : L('Click a card to open its detail. + toggles the company/factory/node layers; with live poller data the device + expands nodes / console and each VM group + toggles its VMs.',
        '카드를 클릭하면 상세로 이동합니다. + 로 회사·공장·노드 계층을 접고 펼칩니다. 실데이터 장비에서는 장비의 + 가 노드·관리 화면을, VM 그룹의 + 가 개별 VM 을 펼칩니다.'),
    // G32: 키보드 단축키(+/−/0 or f) 추가 — 힌트 칩 한 줄에 마우스·키보드 조작을 함께 명시.
    zoomHint: L('Scroll to zoom · drag to pan · +/− zoom · 0/F fit',
      '스크롤 확대·축소 · 드래그 이동 · +/− 확대·축소 · 0/F 맞춤'),
  };
}

/** settings 화면용 회사 색상 관리 목록. */
export function buildCompanyColors(a, b) {
  const { fleet, state } = _resolve(a, b);
  const cc = state.companyColors || {};
  const names = Array.from(new Set(fleet.map((s) => _meta(s).company).filter(Boolean))).sort(cmpKo);
  // 기본색 산출은 buildTopo 와 같은 모듈 단일 함수(#52) — 실장비 회사 우선 정렬 기준도
  // 공유해, 설정 미리보기의 기본색이 토폴로지 자동 배정색과 항상 일치한다.
  const realCos = new Set(fleet.filter((s) => !!topoOf(s)).map((s) => _meta(s).company).filter(Boolean));
  const defaults = coDefaultColorMap(names, (co) => realCos.has(co));
  return {
    palette: COMPANY_PALETTE,
    list: names.map((co) => ({
      name: co,
      color: cc[co] || defaults[co],
      defaultColor: defaults[co],
      custom: !!cc[co],
    })),
  };
}

export default {
  buildModel, buildDetail, buildTopo,
  buildClusters, buildCapacity, buildManageTree, buildSearch, buildCompanyColors,
  clamp, cmpKo, langOf, makeL,
  statusTone, statusLabel, statusAnim, pctTone,
  typeIconOf, typeInfo, usageOf, syncInfo, isMaint,
  fmtAvailN, fmtUptimeD, tsNorm, tsKey, agoSec, agoText, shortTime,
  parseLicDate, fmtLicDate, ddayText, licTone,
  sevInfo, SEV_RANK, histOf, sortRows,
  COMPANY_PALETTE, TYPES, FT_TYPES, isFT, isNoTel,
  deriveStatus, deriveSync, availN, COLOR,
};
