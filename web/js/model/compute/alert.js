// js/model/compute/alert.js — 경보/트랩 수집 및 에스컬레이션/점검 계산
// ---------------------------------------------------------------------------
// 순수 함수만. DOM 접근 0건.
// ---------------------------------------------------------------------------

import { isFT, isNoTel, deriveStatus, deriveSync } from '../data.js';
import { _meta, _arr, _strHash, DASH, makeL, SEV_RANK, STALE_ALERT_DAYS, sevInfo } from './base.js';
import { tsNorm, ackTimeNorm, tsKey, agoSec, agoText, shortTime, _nowStamp, _todayStr } from './time.js';

/* ===========================================================================
 * 6. 경보/트랩 수집 (Vigil buildModel.ts liveAlerts/liveTraps)
 * ======================================================================== */

// 유형 키로 한글 요약을 매핑한다. 원문(desc)은 툴팁·로그에 보존하며, 매핑에 없는
// 동적 포트·노드 경보나 원시 트랩은 원문을 그대로 노출한다.
const ALERT_KO = {
  // 주요 수집 경보 유형
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
export const TEST_FIXTURE_RE = /^srv-evt-test$/i;

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
export const ACK_TIME_MISSING = 'no-time';

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
          ? ackTimeNorm(m.downSince || m.issueSince)
          : ackTimeNorm(a.time),
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
        ackTime: ackTimeNorm(m.downSince),
        testFixture: TEST_FIXTURE_RE.test(s.id),
      });
    } else if (s.status === 'deg' && !al.length) {
      alerts.push({
        host: label, hostId: s.id, id: s.id, sev: 'warning',
        name: 'DEVICE_STATE',
        desc: 'Device degraded — redundancy lost or a node is not normal',
        time: tsNorm(m.issueSince) || onset(s.id, 'deg'),
        // #398: down 분기와 같은 계약 — issueSince 우선, 없으면 고정값.
        ackTime: ackTimeNorm(m.issueSince),
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


export { collectAlerts, collectTraps, alertMsgKo };
