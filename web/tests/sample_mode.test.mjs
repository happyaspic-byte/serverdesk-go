import test from 'node:test';
import assert from 'node:assert/strict';

import { normalizeDevice, pullPatch } from '../js/model/data.js';
import compute from '../js/model/compute.js';
import { controlActionGate } from '../js/screens/detail.js';
import { collectionState, isSampleMode } from '../js/util/ui_state.js';

function rawDevice(meta = {}) {
  return {
    id: 'sample-everrun-01', host: 'sample-everrun-01', type: 'EV', site: 'SAMPLE',
    status: 'op', sync: 'sync', cpu0: 21, mem0: 43, uptime: 12,
    meta: Object.assign({
      label: '[샘플] everRun 정상', company: '샘플 고객사', factory: '데모 공장',
      mgmt: '192.0.2.10', nodes: [], alerts: [], traps: [],
    }, meta),
  };
}

test('device normalization preserves explicit sample/demo provenance', () => {
  const fromSample = normalizeDevice(rawDevice({ sample: true }));
  assert.equal(fromSample.meta.sample, true);
  assert.equal(fromSample.meta.demo, true);

  const fromDemoAlias = normalizeDevice(rawDevice({ demo: true }));
  assert.equal(fromDemoAlias.meta.sample, true);
  assert.equal(fromDemoAlias.meta.demo, true);
});

test('sample API response propagates source and replaces local live edits', async () => {
  const previousFetch = globalThis.fetch;
  globalThis.fetch = async () => ({
    ok: true,
    status: 200,
    json: async () => ({ source: 'sample', sample: true, devices: [rawDevice()] }),
  });
  try {
    const patch = await pullPatch({
      fleet: [{
        id: 'sample-everrun-01', host: 'sample-everrun-01', type: 'EV', status: 'op',
        meta: { label: '운영 로컬 수정', localEdit: { label: '운영 로컬 수정' } },
      }],
    });
    assert.equal(patch.source, 'sample');
    assert.equal(patch.sampleMode, true);
    assert.equal(patch.demoMode, true);
    assert.equal(patch.fleet[0].meta.label, '[샘플] everRun 정상');
    assert.equal(patch.fleet[0].meta.sample, true);
  } finally {
    globalThis.fetch = previousFetch;
  }
});

test('sample collection is first-class and a failed refresh is STALE, never LIVE', () => {
  const sample = { source: 'sample', sampleMode: true, lastPoll: 9_000, refreshSec: 30, pollPending: false };
  assert.equal(isSampleMode(sample), true);
  assert.equal(collectionState(sample, 10_000).key, 'sample');
  assert.equal(collectionState({ ...sample, stale: true, liveError: 'timeout' }, 10_000).key, 'stale');
  assert.equal(collectionState({ source: 'sample', sampleMode: true, lastPoll: 0, pollPending: false }, 10_000).key, 'offline');
});

test('sample detail disables cluster controls even if a capability is advertised', () => {
  const device = normalizeDevice(rawDevice({
    sample: true,
    nodes: [{ name: 'node0', state: 'running' }, { name: 'node1', state: 'running' }],
  }));
  const state = {
    fleet: [device], selected: device.id, lang: 'ko', sampleMode: true, source: 'sample',
    hist: {}, ackedAlerts: {}, maint: {}, notes: {}, collapsed: {}, companyColors: {}, setg: {},
    capabilities: { cluster_actions: { supported: true, actions: ['node-reboot'] } },
  };
  const detail = compute.buildDetail(state, device.id);
  const model = compute.buildModel(state);
  assert.equal(model.pollStat.source, 'sample');
  assert.equal(model.pollStat.live, false);
  assert.equal(model.pollStat.sourceLabel, 'SAMPLE');
  assert.equal(detail.sample, true);
  assert.equal(detail.control.sample, true);
  assert.equal(controlActionGate(detail.control, 'node-reboot').supported, false);
});
