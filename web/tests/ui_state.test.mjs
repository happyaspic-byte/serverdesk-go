import test from 'node:test';
import assert from 'node:assert/strict';

import {
  collectionState, isInitialLoading, formatConsoleTime, timestampKey, restoreImpact,
} from '../js/util/ui_state.js';

test('collection status never advertises LIVE before a successful poll', () => {
  assert.equal(collectionState({ pollPending: true, lastPoll: 0 }).key, 'connecting');
  assert.equal(collectionState({ pollPending: false, lastPoll: 0 }).key, 'offline');
  assert.equal(collectionState({ pollPending: false, lastPoll: 1000, liveError: 'timeout' }).key, 'stale');
  assert.equal(collectionState({ pollPending: false, lastPoll: 1000, stale: true }).key, 'stale');
  assert.equal(collectionState({ pollPending: false, lastPoll: 9_000, refreshSec: 30 }, 10_000).key, 'live');
});

test('one failed refresh becomes STALE and a later success recovers to LIVE', () => {
  const failed = { pollPending: false, lastPoll: 9_000, lastAttempt: 10_000, liveError: 'timeout', stale: true, refreshSec: 30 };
  assert.equal(collectionState(failed, 10_000).key, 'stale');
  const recovered = { ...failed, lastPoll: 11_000, lastAttempt: 11_000, liveError: null, stale: false };
  assert.equal(collectionState(recovered, 11_100).key, 'live');
});

test('initial loading differs from a successful empty fleet', () => {
  assert.equal(isInitialLoading({ pollPending: true, lastPoll: 0 }), true);
  assert.equal(isInitialLoading({ pollPending: false, lastPoll: 1234, fleet: [] }), false);
  assert.equal(isInitialLoading({ pollPending: false, lastPoll: 0, liveError: 'offline' }), false);
});

test('console timestamps are consistently KST and naive timestamps parse as KST', () => {
  assert.equal(formatConsoleTime('2026-08-24T00:00:00Z'), '2026-08-24 09:00:00 KST');
  assert.equal(formatConsoleTime('2026-08-24 00:00:00'), '2026-08-24 00:00:00 KST');
  assert.equal(timestampKey('2026-08-24 09:00:00'), Date.parse('2026-08-24T00:00:00Z'));
  assert.equal(timestampKey('2026-08-24T00:00:00Z'), Date.parse('2026-08-24T00:00:00Z'));
});

test('restore impact counts only persisted device sections', () => {
  assert.deepEqual(restoreImpact({ config: { clusters: [{}, {}], edge_devices: [{}] }, ui: {} }, 5), {
    incomingDevices: 3, currentDevices: 5, restoresUIState: true,
  });
});
