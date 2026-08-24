// js/util/fmt.js
// 공용 포맷/색 유틸: i18n(L), clamp, 부하 바 색 매핑.
// 색 상수는 serverdesk 토큰(pos/warn/neg/info)만 사용한다. Vigil 원색(#e8625a 등) 금지.
// 시그니처(REBUILD-SPEC.md §5.3): setLang, L, COLOR, clamp, barColor, barFill, allocFill

// ---------------------------------------------------------------------------
// i18n — store.lang을 모듈 스코프에 주입(app.js가 언어 변경 시 setLang 호출)
// ---------------------------------------------------------------------------
let currentLang = 'ko';

// app.js/각 화면 모듈이 store 변경 구독에서 호출: setLang(state.lang)
export function setLang(lang) {
  if (lang === 'en' || lang === 'ko') currentLang = lang;
}

// Vigil L(en, ko) 인라인 삼항 패턴 이식. 현재 setLang()으로 주입된 언어 기준.
export function L(en, ko) {
  return currentLang === 'en' ? en : ko;
}

// ---------------------------------------------------------------------------
// 색 토큰 (serverdesk 디자인 언어 고정값 — css/styles.css :root 변수와 동일한 값)
// ---------------------------------------------------------------------------
export const COLOR = Object.freeze({
  pos: '#3A7A4E',
  posBright: '#5FA86F',
  warn: '#7A5200',
  warnBright: '#D89A2B',
  neg: '#C0453E',
  info: '#B85C33',
  ink: '#28251F',
  muted: '#6E6656',
  softTrack: '#D8D1C1',
});

// ---------------------------------------------------------------------------
// 숫자 포맷
// ---------------------------------------------------------------------------

// 0~1 사이 클램프가 아니라 범용 [a,b] 클램프.
export function clamp(v, a, b) {
  const n = Number(v);
  if (!Number.isFinite(n)) return a;
  return Math.min(b, Math.max(a, n));
}

// ---------------------------------------------------------------------------
// 부하 바 색 매핑 — 임계값: warning 78 / critical 90 (settings §1.9와 동일 기준)
// ---------------------------------------------------------------------------

// 사용률 임계값 라이브 값 — 서버(config.thresholds)가 정본. /api/devices 폴 성공 시
// data.js 가 setUsageThresholds 로 갱신하고, 미수신 시 78/90(종전 하드코딩과 동일)이다.
let USAGE_THRESH = { warn: 78, crit: 90 };
export function setUsageThresholds(warn, crit) {
  const w = Number(warn), c = Number(crit);
  if (w > 0 && w < c && c <= 100) USAGE_THRESH = { warn: w, crit: c };
}
export function usageThresholds() { return USAGE_THRESH; }

// 부하값(0~100) → 진한 색(하드 대비). ≥crit neg, ≥warn warn, 그 외 토스 블루.
// (TDS 심사 반영: 중립 사용률 채움은 잉크가 아니라 모노블루 — 임계 초과만 앰버/레드.
//  다크에서도 블루가 살아 있어 저대비 문제까지 함께 해소.)
// CSS 변수 문자열을 반환한다(hex 아님) — 인라인 style 이 라이트 hex 로 고정되면 다크에서
// 클래스 기반 필(var 경유)과 밝기 규칙이 갈린다(3R 심사 지적). var() 는 style 속성에서
// 테마를 자동 추종한다. (SVG presentation attribute 에는 못 쓰므로 소비처는 style.* 로 지정.)
export function barColor(v) {
  const n = Number(v) || 0;
  // 이 반환값은 소비처(capacity/clusters)에서 element.style.color 로 들어간다 — 즉 '텍스트'다.
  // 원색 토큰(--neg/--warn/--blue)은 채도 우선이라 평범한 배경에서 3.4~3.7 로 AA 미달이었다.
  // 텍스트 경로는 AA 를 넘긴 --*-on-tint 변형을 쓴다(막대·아크 채움은 barFill 이 원색 유지).
  if (n >= USAGE_THRESH.crit) return 'var(--neg-on-tint)';
  if (n >= USAGE_THRESH.warn) return 'var(--warn-on-tint)';
  return 'var(--blue-on-tint)';
}

// 채움(필) 전용 — §1.4 base/bright 이원 원칙: 텍스트는 base(barColor), 필/아크는 bright.
// 앰버가 텍스트용 base 와 필용 bright 두 톤으로 갈려 보인다는 재심사 지적을 '필=bright 단일'로 정리.
export function barFill(v) {
  const n = Number(v) || 0;
  if (n >= USAGE_THRESH.crit) return 'var(--neg)';
  if (n >= USAGE_THRESH.warn) return 'var(--warn-bright)';
  return 'var(--accent)';
}
// (구 2-톤 그라디언트 폐기 — 단색 임계 규칙으로 수렴. 채움 소비처는 전부 이 별칭 사용.)
export function allocFill(v) {
  return barFill(v);
}
