// Collection/UI state helpers kept DOM-free so status truth and timezone
// formatting can be regression-tested with Node.

export const CONSOLE_TIME_ZONE = 'Asia/Seoul';

/** 서버가 명시적으로 광고한 샘플/데모 데이터인지 판별한다. */
export function isSampleMode(state) {
  const st = state || {};
  return st.sampleMode === true || st.demoMode === true
    || st.source === 'sample' || st.source === 'demo';
}

/**
 * Return the one collection state the shell is allowed to advertise.
 * lastPoll is the last *successful* response; lastAttempt may include failures.
 */
export function collectionState(state, now = Date.now()) {
  const st = state || {};
  const lastSuccess = Number(st.lastPoll) || 0;
  const refreshMs = Math.max(1, Number(st.refreshSec) || 30) * 3000;
  if (st.pollPending && !lastSuccess && !st.liveError && !st.uiError) {
    return { key: 'connecting', label: 'CONNECTING', tone: 'warn', lastSuccess };
  }
  if (st.uiError || (!lastSuccess && (st.liveError || st.pollPending === false))) {
    return { key: 'offline', label: 'OFFLINE', tone: 'neg', lastSuccess };
  }
  if (st.liveError || st.stale || (lastSuccess && now - lastSuccess > refreshMs)) {
    return { key: 'stale', label: 'STALE', tone: 'warn', lastSuccess };
  }
  if (isSampleMode(st) && lastSuccess) {
    return { key: 'sample', label: 'SAMPLE', tone: 'warn', lastSuccess };
  }
  return { key: 'live', label: 'LIVE', tone: 'pos', lastSuccess };
}

export function isInitialLoading(state) {
  const st = state || {};
  return !!st.pollPending && !st.lastPoll && !st.liveError && !st.uiError;
}

const KST_FORMATTER = new Intl.DateTimeFormat('sv-SE', {
  timeZone: CONSOLE_TIME_ZONE,
  year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
});

/** Format an epoch/ISO timestamp as an explicitly labelled console time. */
export function formatConsoleTime(value, includeZone = true) {
  let d;
  if (value instanceof Date) d = value;
  else if (typeof value === 'string' && /^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}/.test(value.trim())) {
    // Zoned inputs remain exact; naive collector/server timestamps are KST by contract.
    const key = timestampKey(value);
    d = key ? new Date(key) : new Date(value);
  } else d = new Date(value);
  if (Number.isNaN(d.getTime())) return '—';
  const text = KST_FORMATTER.format(d).replace('T', ' ');
  return includeZone ? text + ' KST' : text;
}

/** Naive collector timestamps are documented as KST; zoned values stay exact. */
export function timestampKey(value) {
  let raw = String(value == null ? '' : value).trim();
  if (!raw) return 0;
  raw = raw.replace(/\s+KST$/i, '+09:00');
  raw = raw.replace(' ', 'T');
  if (!/(?:Z|[+\-]\d{2}:?\d{2})$/i.test(raw)) raw += '+09:00';
  const d = new Date(raw);
  return Number.isNaN(d.getTime()) ? 0 : d.getTime();
}

export function currentOperator(doc = typeof document !== 'undefined' ? document : null) {
  if (!doc || typeof doc.querySelector !== 'function') return 'admin';
  const node = doc.querySelector('.hd-profile-name');
  return (node && String(node.textContent || '').trim()) || 'admin';
}

export function restoreImpact(documentValue, currentDevices = 0) {
  const doc = documentValue && typeof documentValue === 'object' ? documentValue : {};
  const cfg = doc.config && typeof doc.config === 'object' ? doc.config : {};
  const clusters = Array.isArray(cfg.clusters) ? cfg.clusters.length : 0;
  const edge = Array.isArray(cfg.edge_devices) ? cfg.edge_devices.length : 0;
  return {
    incomingDevices: clusters + edge,
    currentDevices: Math.max(0, Number(currentDevices) || 0),
    restoresUIState: !!(doc.ui && typeof doc.ui === 'object'),
  };
}

/** DOM-free validator shared by typed/reason confirmation dialogs. */
export function confirmationIssues(values, options) {
  const v = values || {};
  const o = options || {};
  const issues = [];
  if (o.requireReason !== false && !String(v.reason || '').trim()) issues.push('reason');
  if (o.typedPhrase && String(v.phrase || '').trim() !== String(o.typedPhrase)) issues.push('phrase');
  return issues;
}
