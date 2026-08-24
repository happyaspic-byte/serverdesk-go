import test from 'node:test';
import assert from 'node:assert/strict';

import {
  createDurableSharedOutbox,
  createSharedSyncCoordinator,
  SHARED_OUTBOX_KEY,
} from '../js/util/shared_sync.js';

function memoryStorage(seed = {}) {
  const values = new Map(Object.entries(seed));
  return {
    values,
    getItem: (key) => values.has(key) ? values.get(key) : null,
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
  };
}

test('shared writes are FIFO and never overlap within a kind', async () => {
  const calls = [];
  let releaseFirst;
  const firstGate = new Promise((resolve) => { releaseFirst = resolve; });
  let active = 0;
  let maxActive = 0;
  const sync = createSharedSyncCoordinator({
    ack: {
      push: async (delta) => {
        active += 1;
        maxActive = Math.max(maxActive, active);
        calls.push(delta.id);
        if (delta.id === 'set') await firstGate;
        active -= 1;
        return true;
      },
      pull: async () => ({}),
    },
  });
  const first = sync.enqueue('ack', { id: 'set' });
  const second = sync.enqueue('ack', { id: 'delete' });
  await Promise.resolve();
  assert.deepEqual(calls, ['set']);
  releaseFirst();
  await Promise.all([first, second]);
  assert.deepEqual(calls, ['set', 'delete']);
  assert.equal(maxActive, 1);
});

test('failed queue head is retained and Retry sends writes before authoritative reads', async () => {
  const events = [];
  let online = false;
  let releaseFailure;
  const failureGate = new Promise((resolve) => { releaseFailure = resolve; });
  let firstAttempt = true;
  const sync = createSharedSyncCoordinator({
    maintenance: {
      push: async (delta) => {
        events.push('push:' + delta.id);
        if (firstAttempt) { firstAttempt = false; await failureGate; }
        return online;
      },
      pull: async () => { events.push('pull'); return {}; },
    },
  });
  const first = sync.enqueue('maintenance', { id: 'set' });
  const following = sync.enqueue('maintenance', { id: 'delete' });
  releaseFailure();
  assert.deepEqual(await Promise.all([first, following]), [false, false]);
  assert.equal(sync.snapshot().maintenance.queued, 2);
  online = true;
  events.length = 0;
  await sync.retryAll();
  assert.equal(events[0], 'push:set');
  assert.equal(events[1], 'push:delete');
  assert.ok(events.includes('pull'));
  assert.equal(sync.snapshot().maintenance.queued, 0);
});

test('pending writes block remote overwrite and endpoint read failures stay independent', async () => {
  const applied = [];
  const reads = [];
  let ackPulls = 0;
  const pendingSync = createSharedSyncCoordinator({
    ack: { push: async () => false, pull: async () => { ackPulls += 1; return {}; } },
  }, { applyRemote: (kind) => applied.push(kind) });
  await pendingSync.enqueue('ack', { id: 'local-change' });
  const skipped = await pendingSync.refresh();
  assert.equal(skipped.ack.reason, 'pending-write');
  assert.equal(ackPulls, 0);
  assert.deepEqual(applied, []);

  const partialSync = createSharedSyncCoordinator({
    ack: { push: async () => true, pull: async () => ({ server: true }) },
    maintenance: { push: async () => true, pull: async () => null },
  }, {
    onReadStatus: (kind, status) => reads.push([kind, status.ok]),
  });
  const result = await partialSync.refresh();
  assert.equal(result.ack.ok, true);
  assert.equal(result.maintenance.ok, false);
  assert.deepEqual(reads.sort(), [['ack', true], ['maintenance', false]]);
});

test('a read started before a local write is discarded and drain reconciles fresh server truth', async () => {
  let releaseOldRead;
  const oldReadGate = new Promise((resolve) => { releaseOldRead = resolve; });
  let pullCount = 0;
  const applied = [];
  const sync = createSharedSyncCoordinator({
    ack: {
      push: async () => true,
      pull: async () => {
        pullCount += 1;
        if (pullCount === 1) { await oldReadGate; return { old: true }; }
        return { fresh: true };
      },
    },
  }, { applyRemote: (_kind, value) => applied.push(value) });

  const oldRead = sync.refresh();
  await Promise.resolve();
  const write = sync.enqueue('ack', { id: 'local-change' });
  releaseOldRead();
  await Promise.all([oldRead, write]);
  assert.deepEqual(applied, [{ fresh: true }]);
  assert.equal(pullCount, 2);
});

test('failed writes survive reload and replay FIFO before the first authoritative read', async () => {
  const storage = memoryStorage();
  const outbox = createDurableSharedOutbox(storage);
  const first = createSharedSyncCoordinator({
    ack: { push: async () => false, pull: async () => ({ server: true }) },
  }, { outbox });

  assert.equal(await first.enqueue('ack', { set: { alertA: { ts: 't1', by: 'op', reason: 'checked' } }, del: [] }), false);
  assert.equal(await first.enqueue('ack', { set: {}, del: ['alertA'] }), false);
  const persisted = storage.getItem(SHARED_OUTBOX_KEY);
  assert.ok(persisted);
  assert.deepEqual(JSON.parse(persisted).queues.ack.map((d) => d.del.length ? 'delete' : 'set'), ['set', 'delete']);

  const events = [];
  const applied = [];
  const afterReload = createSharedSyncCoordinator({
    ack: {
      push: async (delta) => { events.push(delta.del.length ? 'push:delete' : 'push:set'); return true; },
      pull: async () => { events.push('pull'); return { server: true }; },
    },
  }, { outbox, applyRemote: (_kind, value) => applied.push(value) });
  await afterReload.retryAll();

  assert.deepEqual(events.slice(0, 3), ['push:set', 'push:delete', 'pull']);
  assert.deepEqual(applied[0], { server: true });
  assert.equal(storage.getItem(SHARED_OUTBOX_KEY), null);
  assert.equal(afterReload.snapshot().ack.queued, 0);
});

test('corrupt, legacy, oversized, and secret-bearing outboxes fail closed', async () => {
  const badDocs = [
    '',
    '{bad json',
    JSON.stringify({ queues: { ack: [] } }),
    'x'.repeat(256 * 1024 + 1),
  ];
  for (const raw of badDocs) {
    const storage = memoryStorage({ [SHARED_OUTBOX_KEY]: raw });
    let pushes = 0;
    let pulls = 0;
    const statuses = [];
    const sync = createSharedSyncCoordinator({
      ack: {
        push: async () => { pushes += 1; return true; },
        pull: async () => { pulls += 1; return {}; },
      },
    }, {
      outbox: createDurableSharedOutbox(storage),
      onOutboxStatus: (status) => statuses.push(status),
    });
    await sync.retryAll();
    assert.equal(pushes, 0);
    assert.equal(pulls, 0);
    assert.equal(sync.snapshot().ack.blocked, 'load');
    assert.equal(statuses[0].ok, false);
    assert.equal(storage.getItem(SHARED_OUTBOX_KEY), raw);
  }

  const secretStorage = memoryStorage();
  const secretOutbox = createDurableSharedOutbox(secretStorage);
  assert.equal(secretOutbox.save({
    ack: [{ set: { alertA: { ts: 't', webhook_url: 'https://secret.invalid/token' } }, del: [] }],
  }, ['ack']), false);
  assert.equal(secretStorage.getItem(SHARED_OUTBOX_KEY), null);
});
