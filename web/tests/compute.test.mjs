// web/tests/compute.test.mjs — compute.js 순수 연산 모듈 회귀 테스트
// ---------------------------------------------------------------------------
// Node 내장 node:test 및 node:assert/strict 사용
// ---------------------------------------------------------------------------

import test from 'node:test';
import assert from 'node:assert/strict';

import compute, {
  clamp, cmpKo, langOf, makeL,
  statusTone, statusLabel, statusAnim, pctTone, pctToneAlloc,
  typeIconOf, typeInfo, usageOf, syncInfo, isMaint,
  fmtAvailN, fmtDowntimeYr, fmtUptimeD,
  tsNorm, tsKey, agoSec, agoText, shortTime,
  parseLicDate, fmtLicDate, ddayText, licTone,
  SEV_RANK, STALE_ALERT_DAYS, sevInfo, histOf,
  sortRows,
  alertAckKey, autoAckDue, toCsv, activeMaint,
  escalDue, expiredMaint,
  worstOf, buildClusters, buildCapacity, buildManageTree, buildSearch,
  COMPANY_PALETTE, buildCompanyColors, r1, orthPath, polyMid,
  deriveStatus, deriveSync, availN,
} from '../js/model/compute.js';

test('1. clamp: 숫자 범위 제한 및 유효하지 않은 입력 방어', () => {
  assert.equal(clamp(50, 0, 100), 50);
  assert.equal(clamp(-10, 0, 100), 0);
  assert.equal(clamp(150, 0, 100), 100);
  assert.equal(clamp('abc', 10, 20), 10);
  assert.equal(clamp(NaN, 10, 20), 10);
});

test('2. cmpKo: 한글 자연 정렬 (localeCompare ko numeric)', () => {
  const list = ['노드10', '노드2', '노드1', '서버A'];
  list.sort(cmpKo);
  assert.deepEqual(list, ['노드1', '노드2', '노드10', '서버A']);
});

test('3. langOf & makeL: 언어 판정 및 L 다국어 전환', () => {
  assert.equal(langOf({ lang: 'en' }), 'en');
  assert.equal(langOf({ lang: 'ko' }), 'ko');
  assert.equal(langOf(null), 'ko');

  const L_ko = makeL({ lang: 'ko' });
  const L_en = makeL({ lang: 'en' });
  assert.equal(L_ko('Host', '호스트'), '호스트');
  assert.equal(L_en('Host', '호스트'), 'Host');
});

test('4. statusTone / statusAnim / statusLabel: 상태 표기 헬퍼', () => {
  assert.equal(statusTone('down'), 'neg');
  assert.equal(statusTone('deg'), 'warn');
  assert.equal(statusTone('op'), 'pos');

  assert.equal(statusAnim('down'), 'blink');
  assert.equal(statusAnim('deg'), 'pulse');
  assert.equal(statusAnim('op'), '');

  const L = (en, ko) => ko;
  assert.equal(statusLabel('down', L), '오프라인');
  assert.equal(statusLabel('deg', L), '저하');
  assert.equal(statusLabel('op', L), '가동');
});

test('5. pctTone & pctToneAlloc: 임계치 기준 사용률 톤 산출', () => {
  assert.equal(pctTone(50), 'pos');
  assert.equal(pctTone(80), 'warn');
  assert.equal(pctTone(95), 'neg');
  assert.equal(pctTone(null), 'mut');
  assert.equal(pctToneAlloc(92), 'neg');
});

test('6. fmtAvailN & availN: 가용률 계산 및 포맷', () => {
  assert.equal(availN('op'), 99.99);
  assert.equal(availN('deg'), 99.9);
  assert.equal(availN('down'), 99.0);
  assert.equal(fmtAvailN(99.95), '99.950%');
  assert.equal(fmtAvailN(undefined), '—');
  assert.equal(fmtAvailN(100), '100.000%');
});

test('7. fmtUptimeD & fmtDowntimeYr: 가동/다운타임 시간 포맷', () => {
  const L = (en, ko) => ko;
  assert.equal(fmtUptimeD(1), '1d');
  assert.equal(fmtUptimeD(0.5), '<1d');
  assert.equal(fmtUptimeD(0), '0d');
  assert.equal(fmtUptimeD(NaN), '—');
  assert.equal(fmtDowntimeYr(100, L), '연 0분');
  assert.equal(fmtDowntimeYr(99.99, L), '연 53분');
});

test('8. tsNorm, tsKey, agoSec, agoText: 타임스탬프 파싱 및 경과 시간', () => {
  const L = (en, ko) => ko;
  const now = Date.now();
  const pastIso = new Date(now - 120 * 1000).toISOString();
  assert.equal(agoSec(pastIso), 120);
  assert.equal(agoText(120, L, true), '2분 전');
  assert.equal(agoText(120, L, false), '2m ago');
  assert.equal(tsNorm('2025-01-01T00:00:00Z'), '2025-01-01 00:00:00');
  assert.ok(tsKey('2025-01-01 00:00:00') > 0);
});

test('9. parseLicDate, fmtLicDate, ddayText, licTone: 라이선스 D-day 및 톤', () => {
  const L = (en, ko) => ko;
  const sample = 'Mon Jun 29 17:01:47 KST 2026';
  const parsed = parseLicDate(sample);
  assert.ok(parsed instanceof Date);
  assert.equal(fmtLicDate(sample), '2026-06-29');

  assert.equal(ddayText(10, L, true), 'D-10');
  assert.equal(ddayText(-3, L, true), '만료 D+3');
  assert.equal(ddayText(-3, L, false), 'D+3 overdue');

  assert.equal(licTone(-1), 'neg');
  assert.equal(licTone(30), 'warn');
  assert.equal(licTone(100), 'mut');
  assert.equal(licTone(null), 'mut');
});

test('10. SEV_RANK & sevInfo: 심각도 우선순위 및 메타', () => {
  const L = (en, ko) => ko;
  assert.ok(SEV_RANK.critical < SEV_RANK.warning);
  assert.ok(SEV_RANK.warning < SEV_RANK.info);

  const critInfo = sevInfo('critical', L);
  assert.equal(critInfo.tone, 'neg');
  assert.equal(critInfo.label, '심각');

  const warnInfo = sevInfo('warning', L);
  assert.equal(warnInfo.tone, 'warn');
  assert.equal(warnInfo.label, '경고');

  const infoMeta = sevInfo('info', L);
  assert.equal(infoMeta.tone, 'info');
  assert.equal(infoMeta.label, '정보');
});

test('11. sortRows: 노드 목록 다중 기준 정렬', () => {
  const rows = [
    { host: 'node-b', cpuVal: 80, memVal: 40, status: 'op', uptimeDays: 10 },
    { host: 'node-a', cpuVal: 90, memVal: 60, status: 'down', uptimeDays: 5 },
    { host: 'node-c', cpuVal: 30, memVal: 80, status: 'deg', uptimeDays: 20 },
  ];

  const sortedByHost = sortRows(rows, 'host', 'asc');
  assert.equal(sortedByHost[0].host, 'node-a');
  assert.equal(sortedByHost[2].host, 'node-c');

  const sortedByCpuDesc = sortRows(rows, 'cpu', 'desc');
  assert.equal(sortedByCpuDesc[0].host, 'node-a');
  assert.equal(sortedByCpuDesc[2].host, 'node-c');

  const sortedByStatusAsc = sortRows(rows, 'status', 'asc');
  assert.equal(sortedByStatusAsc[0].status, 'down');
  assert.equal(sortedByStatusAsc[2].status, 'op');
});

test('12. alertAckKey & autoAckDue: 경보 ack 키 및 자동 승인 기한 판정', () => {
  const alert = { hostId: 'dev1', name: 'NIC_DOWN', desc: 'No Link', time: '2025-01-01 00:00:00' };
  const key = alertAckKey(alert.hostId, alert.name, alert.desc, alert.time);
  assert.ok(typeof key === 'string' && key.length > 0);

  const oldTime = new Date(Date.now() - 8 * 86400 * 1000).toISOString();
  const state = {
    setg: { ackAutoDays: 7 },
    fleet: [
      {
        id: 's1',
        host: 'server-1',
        meta: {
          alerts: [
            { name: 'NIC_DOWN', desc: 'No Link', time: oldTime, sev: 'critical' },
          ],
        },
      },
    ],
    ackedAlerts: {},
  };
  const due = autoAckDue(state);
  assert.equal(due.length, 1);
  assert.ok(due[0].includes('s1'));
});

test('13. escalDue: 에스컬레이션 기한 초과 판정', () => {
  const now = Date.now();
  const oldTime = new Date(now - 5 * 3600 * 1000).toISOString();
  const state = {
    setg: { escHours: 4 },
    fleet: [
      {
        id: 's1',
        host: 'server-1',
        meta: {
          alerts: [
            { name: 'unit_testAlert', desc: 'Test Alert', time: oldTime, sev: 'critical' },
          ],
        },
      },
    ],
    ackedAlerts: {},
  };
  const due = escalDue(state, now);
  assert.equal(due.length, 1);
  assert.equal(due[0].host, 'server-1');
});

test('14. activeMaint & expiredMaint: 점검 일정 상태 및 만료 판정', () => {
  const now = Date.now();
  const futureIso = new Date(now + 100000).toISOString();
  const pastIso = new Date(now - 100000).toISOString();
  const state = {
    maint: {
      's1': { until: futureIso },
      's2': { until: pastIso },
    },
  };
  const active = activeMaint(state, now);
  assert.ok(active['s1']);
  assert.equal(active['s2'], undefined);

  const expired = expiredMaint(state, now);
  assert.deepEqual(expired, ['s2']);
});

test('15. toCsv: 경보/로그 데이터 CSV 문자열 변환', () => {
  const headers = ['시간', '장비', '메시지'];
  const rows = [
    ['2025-01-01', 'node-1', 'Alert, with comma'],
    ['2025-01-02', 'node-2', 'Normal "quoted"'],
  ];
  const csv = toCsv(headers, rows);
  assert.ok(csv.includes('"Alert, with comma"'));
  assert.ok(csv.includes('"Normal ""quoted"""'));
});

test('16. worstOf: 다중 상태 중 최우선 위험 상태 산출', () => {
  assert.equal(worstOf(['op', 'deg', 'down']), 'down');
  assert.equal(worstOf(['op', 'deg']), 'deg');
  assert.equal(worstOf(['op', 'op']), 'op');
  assert.equal(worstOf([]), 'op');
});

test('17. typeInfo & typeIconOf: 장비 타입 정보 및 아이콘', () => {
  const L = (en, ko) => ko;
  const srvInfo = typeInfo('SRV', L);
  assert.equal(srvInfo.key, 'SRV');
  assert.equal(srvInfo.label, '서버');

  assert.equal(typeIconOf('SRV'), 'db');
  assert.equal(typeIconOf('SW'), 'box');
});

test('18. COMPANY_PALETTE & buildCompanyColors: 회사별 토폴로지 색상 배정', () => {
  assert.ok(Array.isArray(COMPANY_PALETTE));
  assert.ok(COMPANY_PALETTE.length >= 8);

  const fleet = [
    { id: '1', meta: { company: '삼성전자' } },
    { id: '2', meta: { company: '현대자동차' } },
  ];
  const colors = buildCompanyColors(fleet, {});
  assert.equal(colors.list.length, 2);
  assert.ok(colors.list.find((c) => c.name === '삼성전자'));
  assert.ok(colors.list.find((c) => c.name === '현대자동차'));
});

test('19. default export 파사드 무결성 검증', () => {
  assert.equal(typeof compute.buildModel, 'function');
  assert.equal(typeof compute.buildDetail, 'function');
  assert.equal(typeof compute.buildTopo, 'function');
  assert.equal(typeof compute.clamp, 'function');
  assert.equal(typeof compute.statusTone, 'function');
  assert.equal(typeof compute.sortRows, 'function');
  assert.equal(typeof compute.deriveStatus, 'function');
  assert.equal(typeof compute.availN, 'function');
});

test('20. deriveStatus & deriveSync: 장비/노드 상태 파생 일관성', () => {
  const nodesOp = [{ state: 'running', standing: 'normal' }];
  assert.equal(deriveStatus(nodesOp), 'op');

  const nodesDeg = [{ state: 'running', standing: 'normal' }, { state: 'stopped', standing: 'normal' }];
  assert.equal(deriveStatus(nodesDeg), 'deg');

  const nodesDown = [{ state: 'stopped', standing: 'normal' }];
  assert.equal(deriveStatus(nodesDown), 'down');
});
