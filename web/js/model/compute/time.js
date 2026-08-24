// js/model/compute/time.js — 시간 파싱/포맷
// ---------------------------------------------------------------------------
// 순수 함수만. DOM 접근 0건.
// ---------------------------------------------------------------------------

import { _num, DASH, makeL } from './base.js';
import { formatConsoleTime, timestampKey, CONSOLE_TIME_ZONE } from '../../util/ui_state.js';

/* ===========================================================================
 * 2. 시간 파싱/포맷 (Vigil buildModel.ts 이식)
 * ======================================================================== */

/** 'YYYY-MM-DDTHH:MM:SSZ' 등 → 'YYYY-MM-DD HH:MM:SS'. */
export function tsNorm(t) {
  const raw = String(t == null ? '' : t).trim();
  if (!raw) return '';
  // 명시적 UTC/offset 값은 콘솔 표준(KST)으로 변환한다. 시간대 없는 레거시 값은
  // 수집기 계약상 KST이므로 텍스트를 보존하고 KST 라벨만 붙인다.
  if (/(?:Z|[+\-]\d{2}:?\d{2})$/i.test(raw)) return formatConsoleTime(raw);
  return raw.replace('T', ' ') + ' KST';
}

/**
 * 경보 확인 키 전용 레거시 정규화. 표시 시간대 변환과 의도적으로 분리한다.
 * 서버 /ack에 이미 저장된 키 계약(T→공백, 끝 Z 제거, 결측 no-time)을 바꾸면 안 된다.
 */
export function ackTimeNorm(t) {
  const raw = String(t == null ? '' : t).trim();
  if (!raw) return 'no-time';
  return raw.replace('T', ' ').replace(/Z$/i, '');
}

/** ACK value may be a legacy ISO string or the durable `{ts, by, reason}` record. */
export function ackTimestampKey(value) {
  const raw = value && typeof value === 'object' ? value.ts : value;
  const parsed = Date.parse(String(raw || ''));
  return Number.isFinite(parsed) ? parsed : 0;
}

export function _nowStamp() {
  return formatConsoleTime(new Date());
}

/** 타임스탬프 문자열 → epoch ms (없으면 0). data.js tsOf 는 로컬 시각을 쓰므로 로컬로 파싱한다. */
export function tsKey(ts) {
  return timestampKey(ts);
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

export function _todayStr() {
  return formatConsoleTime(new Date(), false).slice(0, 10);
}

export { CONSOLE_TIME_ZONE };

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
