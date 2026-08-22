// js/model/compute/topo.js — buildTopo (토폴로지 뷰 모델)
// ---------------------------------------------------------------------------
// 순수 함수만. DOM 접근 0건.
// ---------------------------------------------------------------------------

import {
  TYPES, FT_TYPES, isFT, isNoTel, deriveStatus, deriveSync, availN,
} from '../data.js';
import { COLOR } from '../../util/fmt.js';
import {
  clamp, cmpKo, langOf, makeL, SEV_RANK, STALE_ALERT_DAYS,
  sevInfo, histOf, _meta, _arr, _num, DASH, _resolve,
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
// 회사명이 긴 환경에서도 라벨이 잘리지 않도록 카드 폭과 내부 여백을 확보한다.
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
// G33: CARD.w 150→164 — 긴 실클러스터 타일 라벨이 내부 폭에서 잘리지 않도록 여유를 확보한다.
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
// 비FT 호스팅 VM은 오른쪽으로 열을 늘리지 않고 한 열에서 아래로 계속 쌓는다.
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

/** 비FT 호스팅 VM은 개수와 무관하게 한 열에서 아래로 쌓는다. */
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
  // 수집원에 따라 mode 정상값은 'normal' 또는 'production'이다.
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
  // 상세 토폴로지가 있는 회사를 위에 두어 초기 fit 화면에서 FT 인프라를 먼저 읽게 한다.
  const coHasTopology = (co) => Object.keys(coMap[co]).some((fa) => coMap[co][fa].some((s) => !!topoOf(s)));
  const companies = Object.keys(coMap).sort((a, b) => {
    const ra = coHasTopology(a) ? 0 : 1; const rb = coHasTopology(b) ? 0 : 1;
    return ra !== rb ? ra - rb : cmpKo(coLabel(a), coLabel(b));
  });
  const realCos = companies.filter((c) => c !== UNASSIGNED_CO);
  // 기본색 산출은 모듈 단일 함수(buildCompanyColors 와 공용 — #52).
  const coDefaults = coDefaultColorMap(realCos, coHasTopology);
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
      // 회사명을 벗긴 장비코드(code)와 회사(company)를 함께 내려 타일이 코드를 우선 노출한다.
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
    // 장비 +와 VM 그룹 +는 상세 토폴로지 행에만 존재한다. 실제로 그려진 컨트롤을
    // 기준으로 문구를 분기해 없는 조작을 안내하지 않는다.
    // meta.topo가 있어도 VM이 0대면 장비 +만 존재하므로 각 문구를 독립적으로 고른다.
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
