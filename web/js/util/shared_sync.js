// Ordered synchronization for operator-owned shared maps (ACK, maintenance, notes).
// The server is authoritative when reachable, while localStorage remains an offline fallback.

function isMap(value) {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

export const SHARED_OUTBOX_VERSION = 1;
export const SHARED_OUTBOX_KEY = 'sd.sharedOutbox.v1';
const OUTBOX_MAX_BYTES = 256 * 1024;
const OUTBOX_MAX_ITEMS_PER_KIND = 256;
const OUTBOX_MAX_OPS_PER_DELTA = 512;
const OUTBOX_MAX_KEY_LENGTH = 1024;
const OUTBOX_MAX_VALUE_BYTES = 16 * 1024;
const OUTBOX_KINDS = ['ack', 'maintenance', 'notes'];
const SECRET_FIELD = /(?:webhook|secret|token|password|passwd|credential)/i;
const SECRET_URL = /https?:\/\/[^\s]*(?:\/api\/webhooks?\/|hooks\.|webhook\.)/i;

function safeJSON(value) {
  try { return JSON.stringify(value); } catch (_) { return null; }
}

function containsSecretField(value, depth = 0) {
  if (depth > 8) return true;
  if (value == null || typeof value !== 'object') return false;
  if (Array.isArray(value)) return value.some((item) => containsSecretField(item, depth + 1));
  return Object.entries(value).some(([key, item]) => SECRET_FIELD.test(key) || containsSecretField(item, depth + 1));
}

function boundedString(value, max, required = false) {
  if (typeof value !== 'string' || value.length > max || SECRET_URL.test(value)) return null;
  if (required && !value.trim()) return null;
  return value;
}

function cleanSharedValue(kind, item) {
  if (kind === 'ack' && typeof item === 'string') return boundedString(item, 40, true);
  if (!isMap(item) || containsSecretField(item)) return null;
  const contracts = {
    ack: { required: 'ts', fields: { ts: 40, by: 80, reason: 500 } },
    maintenance: { required: 'until', fields: { until: 40, note: 200, by: 40, ts: 40 } },
    notes: { required: 'text', fields: { text: 1000, ts: 40, by: 40 } },
  };
  const contract = contracts[kind];
  if (!contract || Object.keys(item).some((key) => !Object.hasOwn(contract.fields, key))) return null;
  const out = Object.create(null);
  for (const [field, max] of Object.entries(contract.fields)) {
    if (!Object.hasOwn(item, field)) continue;
    const text = boundedString(item[field], max, field === contract.required);
    if (text == null) return null;
    out[field] = text;
  }
  return Object.hasOwn(out, contract.required) ? out : null;
}

function cleanDelta(value, kind) {
  if (!isMap(value)) return null;
  const keys = Object.keys(value);
  if (keys.some((key) => key !== 'set' && key !== 'del')) return null;
  const sourceSet = value.set == null ? {} : value.set;
  const sourceDel = value.del == null ? [] : value.del;
  if (!isMap(sourceSet) || !Array.isArray(sourceDel)) return null;
  const setKeys = Object.keys(sourceSet);
  if (setKeys.length + sourceDel.length > OUTBOX_MAX_OPS_PER_DELTA) return null;
  const set = Object.create(null);
  for (const key of setKeys) {
    if (!key || key.length > OUTBOX_MAX_KEY_LENGTH) return null;
    const item = cleanSharedValue(kind, sourceSet[key]);
    if (item == null) return null;
    const encoded = safeJSON(item);
    if (encoded == null || encoded.length > OUTBOX_MAX_VALUE_BYTES) return null;
    set[key] = item;
  }
  const del = [];
  for (const key of sourceDel) {
    if (typeof key !== 'string' || !key || key.length > OUTBOX_MAX_KEY_LENGTH) return null;
    del.push(key);
  }
  return { set, del };
}

function cleanQueues(value, kinds) {
  if (!isMap(value)) return null;
  const allowed = new Set(kinds);
  if (Object.keys(value).some((kind) => !allowed.has(kind))) return null;
  const queues = Object.create(null);
  for (const kind of kinds) {
    const source = value[kind] == null ? [] : value[kind];
    if (!Array.isArray(source) || source.length > OUTBOX_MAX_ITEMS_PER_KIND) return null;
    queues[kind] = [];
    for (const item of source) {
      const delta = cleanDelta(item, kind);
      if (!delta) return null;
      queues[kind].push(delta);
    }
  }
  return queues;
}

/**
 * Versioned, bounded localStorage adapter for pending operator-state deltas.
 * Invalid/legacy/oversized payloads are reported as unusable and are never
 * executed or overwritten automatically. Notification credentials are outside
 * this schema, and secret-looking fields are rejected before serialization.
 */
export function createDurableSharedOutbox(storage = globalThis.localStorage, key = SHARED_OUTBOX_KEY) {
  function load(kinds = OUTBOX_KINDS) {
    let raw = null;
    try { raw = storage && storage.getItem(key); } catch (error) {
      return { ok: false, error: 'outbox-read-failed', queues: Object.create(null) };
    }
    if (raw == null) return { ok: true, queues: cleanQueues({}, kinds) };
    if (raw.length > OUTBOX_MAX_BYTES) return { ok: false, error: 'outbox-too-large', queues: Object.create(null) };
    let doc;
    try { doc = JSON.parse(raw); } catch (_) {
      return { ok: false, error: 'outbox-invalid-json', queues: Object.create(null) };
    }
    if (!isMap(doc) || doc.version !== SHARED_OUTBOX_VERSION || !isMap(doc.queues)) {
      return { ok: false, error: 'outbox-unsupported-version', queues: Object.create(null) };
    }
    const queues = cleanQueues(doc.queues, kinds);
    if (!queues) return { ok: false, error: 'outbox-invalid-shape', queues: Object.create(null) };
    return { ok: true, queues };
  }

  function save(queues, kinds = OUTBOX_KINDS) {
    const cleaned = cleanQueues(queues, kinds);
    if (!cleaned) return false;
    const hasItems = kinds.some((kind) => cleaned[kind].length);
    try {
      if (!hasItems) {
        storage.removeItem(key);
        return true;
      }
      const raw = safeJSON({ version: SHARED_OUTBOX_VERSION, queues: cleaned });
      if (raw == null || raw.length > OUTBOX_MAX_BYTES) return false;
      storage.setItem(key, raw);
      return true;
    } catch (_) {
      return false;
    }
  }

  return { load, save, key };
}

/**
 * Build a per-kind FIFO write coordinator.
 *
 * Each definition may provide `push(delta)` and `pull()`. Failed writes remain at the
 * head of their queue. A pull never applies while a write is queued/in flight, nor may
 * a pull that started before a later write overwrite that write when it eventually
 * resolves.
 */
export function createSharedSyncCoordinator(definitions, hooks = {}) {
  const defs = definitions || {};
  const kinds = Object.keys(defs);
  const slots = Object.create(null);
  const outbox = hooks.outbox || null;
  const restored = outbox && typeof outbox.load === 'function'
    ? outbox.load(kinds) : { ok: true, queues: Object.create(null) };
  kinds.forEach((kind) => {
    slots[kind] = {
      queue: restored.ok && Array.isArray(restored.queues[kind]) ? restored.queues[kind].slice() : [],
      inFlight: false, drainPromise: null, readPromise: null, version: 0,
      blocked: restored.ok ? '' : 'load',
    };
  });

  const queueSnapshot = () => Object.fromEntries(kinds.map((kind) => [kind, slots[kind].queue]));
  const persistQueues = () => !outbox || typeof outbox.save !== 'function' || outbox.save(queueSnapshot(), kinds);

  const writeStatus = (kind, ok) => {
    if (typeof hooks.onWriteStatus === 'function') {
      hooks.onWriteStatus(kind, { ok, pending: pending(kind) });
    }
  };
  const readStatus = (kind, ok) => {
    if (typeof hooks.onReadStatus === 'function') hooks.onReadStatus(kind, { ok });
  };

  function pending(kind) {
    const slot = slots[kind];
    return !!(slot && (slot.blocked || slot.inFlight || slot.queue.length));
  }

  async function refreshKind(kind) {
    const slot = slots[kind];
    const def = defs[kind];
    if (!slot || !def || typeof def.pull !== 'function') {
      return { kind, ok: true, skipped: true, reason: 'unsupported' };
    }
    if (pending(kind)) return { kind, ok: true, skipped: true, reason: 'pending-write' };
    if (slot.readPromise) return slot.readPromise;

    const version = slot.version;
    const task = (async () => {
      let remote = null;
      try { remote = await def.pull(); } catch (_) { remote = null; }
      // A write queued after this read began makes the response stale, even when the
      // write has already drained by the time the response arrives.
      if (pending(kind) || slot.version !== version) {
        return { kind, ok: true, skipped: true, reason: 'superseded-by-write' };
      }
      if (!isMap(remote)) {
        readStatus(kind, false);
        return { kind, ok: false, skipped: false };
      }
      try {
        if (typeof hooks.applyRemote === 'function') hooks.applyRemote(kind, remote);
      } catch (_) {
        readStatus(kind, false);
        return { kind, ok: false, skipped: false };
      }
      readStatus(kind, true);
      return { kind, ok: true, skipped: false, value: remote };
    })();
    slot.readPromise = task;
    try { return await task; } finally {
      if (slot.readPromise === task) slot.readPromise = null;
    }
  }

  async function runDrain(kind) {
    const slot = slots[kind];
    const def = defs[kind];
    if (slot.blocked === 'load') {
      writeStatus(kind, false);
      return false;
    }
    if (slot.blocked === 'save') {
      if (!persistQueues()) {
        writeStatus(kind, false);
        return false;
      }
      slot.blocked = '';
    }
    slot.inFlight = true;
    let ok = true;
    try {
      while (slot.queue.length) {
        let pushed = false;
        try { pushed = !!(await def.push(slot.queue[0])); } catch (_) { pushed = false; }
        if (!pushed) {
          ok = false;
          writeStatus(kind, false);
          break;
        }
        const accepted = slot.queue.shift();
        // Persist removal only after the server accepted the idempotent delta. If
        // storage fails, restore the item so a reload/retry can safely replay it.
        if (!persistQueues()) {
          slot.queue.unshift(accepted);
          slot.blocked = 'save';
          ok = false;
          writeStatus(kind, false);
          break;
        }
      }
    } finally {
      slot.inFlight = false;
      slot.drainPromise = null;
    }
    if (ok && !slot.queue.length) {
      writeStatus(kind, true);
      // If an older read was already in flight, it is deliberately skipped by the
      // version guard. Run one fresh reconciliation after that read finishes.
      const first = await refreshKind(kind);
      if (first && first.skipped && !pending(kind)) await refreshKind(kind);
    }
    // An enqueue may race with the reconciliation window above. It starts its own
    // drain, but this guard also guarantees no item is stranded.
    if (slot.queue.length && !slot.inFlight && !slot.drainPromise && ok) drain(kind);
    return ok;
  }

  function drain(kind) {
    const slot = slots[kind];
    const def = defs[kind];
    if (!slot || !def || typeof def.push !== 'function') return Promise.resolve(false);
    if (slot.drainPromise) return slot.drainPromise;
    if (!slot.queue.length) return Promise.resolve(true);
    const task = runDrain(kind);
    slot.drainPromise = task;
    return task;
  }

  function enqueue(kind, delta) {
    const slot = slots[kind];
    if (!slot) return Promise.resolve(false);
    if (slot.blocked === 'load') {
      writeStatus(kind, false);
      return Promise.resolve(false);
    }
    // The production durable adapter accepts only the bounded set/del contract.
    // Keeping the coordinator transport-agnostic without an adapter preserves its
    // use as a small FIFO primitive in isolated tests/consumers.
    const cleaned = outbox ? cleanDelta(delta || { set: {}, del: [] }, kind) : (delta || { set: {}, del: [] });
    if (!cleaned || slot.queue.length >= OUTBOX_MAX_ITEMS_PER_KIND) {
      slot.blocked = 'save';
      writeStatus(kind, false);
      return Promise.resolve(false);
    }
    slot.queue.push(cleaned);
    slot.version += 1;
    // A mutation must be durable before any network attempt. On quota/storage
    // failure the local UI value remains, while writes and authoritative pulls
    // fail closed until persistence succeeds.
    if (!persistQueues()) {
      slot.blocked = 'save';
      writeStatus(kind, false);
      return Promise.resolve(false);
    }
    return drain(kind);
  }

  async function refresh(selectedKinds) {
    const list = Array.isArray(selectedKinds) ? selectedKinds : kinds;
    const rows = await Promise.all(list.map(refreshKind));
    return Object.fromEntries(rows.map((row) => [row.kind, row]));
  }

  async function retryAll() {
    // Pending local mutations are always sent before authoritative reads.
    await Promise.all(kinds.map((kind) => drain(kind)));
    return refresh();
  }

  function snapshot() {
    const out = {};
    kinds.forEach((kind) => {
      const slot = slots[kind];
      out[kind] = { queued: slot.queue.length, inFlight: slot.inFlight, version: slot.version, blocked: slot.blocked };
    });
    return out;
  }

  if (!restored.ok && typeof hooks.onOutboxStatus === 'function') {
    hooks.onOutboxStatus({ ok: false, error: restored.error || 'outbox-invalid' });
  } else if (kinds.some((kind) => slots[kind].queue.length) && typeof hooks.onOutboxStatus === 'function') {
    hooks.onOutboxStatus({ ok: true, restored: true });
  }

  return { enqueue, drain, refresh, retryAll, pending, snapshot };
}
