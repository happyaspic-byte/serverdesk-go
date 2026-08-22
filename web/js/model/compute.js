// js/model/compute.js — F3 파생 계산 (파사드 모듈)
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
//  - compute/ 하위 도메인 모듈(base, format, time, node, alert, kpi, cluster, detail, topo)을
//    re-export하여 기존 import 경로와의 100% 호환성을 보장한다.
// ---------------------------------------------------------------------------

import {
  TYPES, FT_TYPES, TYPE_KEYS, STATUS_KEYS, SYNC_KEYS,
  isFT, isNoTel, deriveStatus, deriveSync, availN,
} from './data.js';
import { COLOR } from '../util/fmt.js';

/* 재수출 — 화면/다른 모듈이 compute 하나만 import 해도 되도록. */
export {
  TYPES, FT_TYPES, TYPE_KEYS, STATUS_KEYS, SYNC_KEYS,
  isFT, isNoTel, deriveStatus, deriveSync, availN, COLOR,
};

/* 도메인 모듈 재수출 (compute/*) */
export {
  clamp, cmpKo, langOf, makeL,
  SEV_RANK, STALE_ALERT_DAYS, sevInfo, histOf,
} from './compute/base.js';

export {
  statusTone, statusLabel, statusAnim, pctTone, pctToneAlloc,
  typeIconOf, typeInfo, usageOf, syncInfo, isMaint,
  fmtAvailN, fmtDowntimeYr, fmtUptimeD,
} from './compute/format.js';

export {
  tsNorm, tsKey, agoSec, agoText, shortTime,
  parseLicDate, fmtLicDate, ddayText, licTone,
} from './compute/time.js';

export {
  sortRows,
} from './compute/node.js';

export {
  alertAckKey, autoAckDue, toCsv, activeMaint,
  escalDue, expiredMaint,
} from './compute/alert.js';

export {
  buildModel,
} from './compute/kpi.js';

export {
  worstOf, buildClusters, buildCapacity, buildManageTree, buildSearch,
} from './compute/cluster.js';

export {
  buildDetail, flowOf,
} from './compute/detail.js';

export {
  COMPANY_PALETTE, stackH, protoShort, tonerSummary, licInfo,
  typeRows, devSort, rvBlock, realLanes, vmGrid, vmBlock,
  r1, orthPath, polyMid, nodeStatus, grpWorst,
  buildTopo, buildCompanyColors,
} from './compute/topo.js';

/* Import for default export */
import { clamp, cmpKo, langOf, makeL, SEV_RANK, sevInfo, histOf } from './compute/base.js';
import {
  statusTone, statusLabel, statusAnim, pctTone,
  typeIconOf, typeInfo, usageOf, syncInfo, isMaint,
  fmtAvailN, fmtUptimeD,
} from './compute/format.js';
import {
  tsNorm, tsKey, agoSec, agoText, shortTime,
  parseLicDate, fmtLicDate, ddayText, licTone,
} from './compute/time.js';
import { sortRows } from './compute/node.js';
import { buildModel } from './compute/kpi.js';
import {
  buildClusters, buildCapacity, buildManageTree, buildSearch,
} from './compute/cluster.js';
import { buildDetail } from './compute/detail.js';
import {
  COMPANY_PALETTE, buildTopo, buildCompanyColors,
} from './compute/topo.js';

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
