// js/model/compute/format.js — 상태/동기화/타입 표기 헬퍼
// ---------------------------------------------------------------------------
// 순수 함수만. DOM 접근 0건.
// ---------------------------------------------------------------------------

import { TYPES, FT_TYPES } from '../data.js';
import { usageThresholds } from '../../util/fmt.js';
import { _num, _arr, DASH, makeL, clamp } from './base.js';

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

/** 장비 라벨에서 회사 프리픽스를 벗긴 고유 장비코드를 반환한다.
 * 프리픽스가 없으면 라벨을 그대로 유지한다. */
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

