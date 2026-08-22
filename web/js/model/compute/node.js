// js/model/compute/node.js — 노드 목록 정렬 유틸
// ---------------------------------------------------------------------------
// 순수 함수만. DOM 접근 0건.
// ---------------------------------------------------------------------------

import { deriveStatus, deriveSync } from '../data.js';
import { cmpKo, _num, _meta, _arr, SEV_RANK } from './base.js';
import { statusTone, syncInfo, isMaint, fmtAvailN, fmtUptimeD } from './format.js';

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

