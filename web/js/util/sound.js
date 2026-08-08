// js/util/sound.js — 알림 사운드 (Web Audio oscillator 비프, 오디오 파일 없음)
// REBUILD-SPEC §1.9 '경고 등급 이상 경보에서만 재생' — settings 의 '알림 사운드'(setg.sound)
// 토글이 소비하는 유일한 소비처. 재생 시점 제어(새 경보 등장 시 1회)는 app.js 가 담당하고,
// 이 모듈은 '어떻게 소리를 내는가'만 담당한다.
//
// 브라우저 자동재생 정책 — 사용자 제스처 전에 만든 AudioContext 는 suspended 다.
// 최소 처리: 컨텍스트는 첫 사용 시 lazy 생성 + suspended 면 resume() 만 시도하고 그 틱의
// 재생은 건너뛴다(나중에 깨어나며 뒤늦게 울리는 혼란을 막기 위함). 이후 제스처에서
// resumeAlertAudio()(app.js 의 pointerdown/keydown 리스너)가 다시 재개를 건다.
//
// 어떤 실패에도 throw 하지 않는다(셸 생존 원칙 — 사운드는 부가 기능).

let _ctx = null;

/** 모듈 내 AudioContext 싱글턴(미지원·생성 실패 시 null). */
function audioCtx() {
  if (_ctx) return _ctx;
  const AC = globalThis.AudioContext || globalThis.webkitAudioContext;
  if (!AC) return null;
  try {
    _ctx = new AC();
  } catch (e) {
    return null;
  }
  return _ctx;
}

/**
 * 자동재생 정책 해제 — 사용자 제스처 핸들러에서 호출한다.
 * suspended 상태면 재개를 시도한다. ctx 를 넘기면 그 컨텍스트를 쓴다(테스트 주입용).
 */
export function resumeAlertAudio(ctx) {
  const c = ctx || audioCtx();
  if (!c || c.state !== 'suspended') return;
  try { c.resume().catch(() => {}); } catch (e) { /* noop */ }
}

/**
 * 경고음 1회 — 짧은 2음 차임(880 → 660Hz, 약 0.3초).
 * suspended 면 재개만 시도하고 이번 재생은 건너뛴다(false). ctx 주입 가능(테스트용).
 * @returns {boolean} 실제로 스케줄했으면 true
 */
export function playAlertBeep(ctx) {
  const c = ctx || audioCtx();
  if (!c) return false;
  try {
    if (c.state === 'suspended') {
      c.resume().catch(() => {});
      return false;
    }
    const t0 = c.currentTime;
    const osc = c.createOscillator();
    const gain = c.createGain();
    osc.type = 'sine';
    osc.frequency.setValueAtTime(880, t0);
    osc.frequency.setValueAtTime(660, t0 + 0.12);
    gain.gain.setValueAtTime(0.0001, t0);
    gain.gain.exponentialRampToValueAtTime(0.12, t0 + 0.02);
    gain.gain.exponentialRampToValueAtTime(0.0001, t0 + 0.3);
    osc.connect(gain);
    gain.connect(c.destination);
    osc.start(t0);
    osc.stop(t0 + 0.32);
    return true;
  } catch (e) {
    return false;
  }
}

/**
 * 사운드 대상 경보 키 집합 — buildModel 결과의 activeAlerts(미확인·비점검 창) 중
 * warning 이상(sev 'warning'|'critical')만 고른다. 에스컬레이션(escalDue)과 같은
 * 모집단이라 '화면에 실제로 뜬 새 경보'와 울리는 경보가 어긋나지 않는다.
 * 순수 함수(테스트 가능).
 */
export function alertSoundKeys(m) {
  const out = new Set();
  const list = (m && Array.isArray(m.activeAlerts)) ? m.activeAlerts : [];
  list.forEach((a) => {
    if (!a) return;
    if (a.sev !== 'warning' && a.sev !== 'critical') return;
    out.add(a.ackKey || [a.hostId, a.name, a.desc, a.time].join(''));
  });
  return out;
}
