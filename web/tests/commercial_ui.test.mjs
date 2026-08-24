import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

import { confirmationIssues } from '../js/util/ui_state.js';
import { deleteImpact, validateDeleteConfirmation } from '../js/screens/manage.js';
import { escalationTransition, notificationDisplay, sanitizeNotificationConfig } from '../js/screens/settings.js';
import { loadPersisted, persist, LS_NOTIFY } from '../js/store.js';
import { authoritativeSharedState } from '../js/model/data.js';

test('typed destructive confirmation requires a reason and exact target', () => {
  assert.deepEqual(confirmationIssues({ reason: '', phrase: 'WRONG' }, {
    requireReason: true, typedPhrase: 'RESTORE',
  }), ['reason', 'phrase']);
  assert.deepEqual(confirmationIssues({ reason: 'approved change', phrase: 'RESTORE' }, {
    requireReason: true, typedPhrase: 'RESTORE',
  }), []);

  const form = { origKey: 'edge-01', delete_reason: 'retired', delete_phrase: 'edge-0' };
  assert.equal(validateDeleteConfirmation(form).field, 'delete_phrase');
  form.delete_phrase = 'edge-01';
  assert.equal(validateDeleteConfirmation(form), null);
});

test('device delete impact identifies target and dependent monitoring state', () => {
  assert.deepEqual(deleteImpact({
    ackedAlerts: { ['edge-01\u0001NIC\u0001down\u0001no-time']: { ts: 'x' }, other: 'x' },
    maint: { 'edge-01': { until: 'x' } }, notes: { 'edge-01': { text: 'handoff' } },
  }, 'edge-01'), {
    target: 'edge-01', acknowledgements: 1, maintenanceWindow: true, note: true,
  });
});

test('legacy browser webhook secret is removed before load and never persisted', () => {
  const values = new Map([[LS_NOTIFY, JSON.stringify({ enabled: true, url: 'https://secret.invalid/token' })]]);
  globalThis.localStorage = {
    getItem: (key) => values.has(key) ? values.get(key) : null,
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
  };
  const patch = loadPersisted();
  assert.equal(values.has(LS_NOTIFY), false);
  assert.equal(Object.hasOwn(patch, 'notify'), false);
  persist({ lang: 'ko', companyColors: {}, railOpen: false, theme: 'system', ackedAlerts: {}, maint: {}, notes: {}, setg: {} });
  assert.equal(values.has(LS_NOTIFY), false);
  delete globalThis.localStorage;
});

test('notifier status is green only when server runtime is healthy and never exposes its URL', () => {
  const starting = notificationDisplay({ loaded: true, enabled: true, configured: true, runtime: { source_ready: false, healthy: false } });
  assert.equal(starting.tone, 'warn');
  const degraded = notificationDisplay({ loaded: true, enabled: true, configured: true, runtime: { healthy: false, dead_letter: 2, last_error: 'redacted' }, webhook_url: 'https://secret.invalid/token' });
  assert.equal(degraded.tone, 'neg');
  assert.equal(JSON.stringify(degraded).includes('secret.invalid'), false);
  const active = notificationDisplay({ loaded: true, enabled: true, configured: true, runtime: { source_ready: true, healthy: true, pending: 1, last_success: '2025-01-01T00:00:00Z' } });
  assert.equal(active.tone, 'pos');
  assert.equal(active.pending, 1);
  assert.equal(active.lastSuccess, '2025-01-01T00:00:00Z');
  assert.equal(notificationDisplay({ loaded: true, enabled: true, configured: true, status: { source_ready: true, healthy: true } }).tone, 'pos');
  const sanitized = sanitizeNotificationConfig({
    enabled: true, configured: true, webhook_url: 'https://secret.invalid/token',
    runtime: { healthy: true, webhook_url: 'https://secret.invalid/nested' },
  });
  assert.equal(JSON.stringify(sanitized).includes('secret.invalid'), false);
});

test('failed optimistic escalation policy can restore the exact server value', () => {
  const server = { loaded: true, enabled: true, configured: true, escalation_hours: 4 };
  const transition = escalationTransition(server, 24);
  assert.equal(transition.optimistic.escalation_hours, 24);
  assert.deepEqual(transition.previous, server);
  assert.notEqual(transition.previous, server);
  assert.equal(transition.previous.escalation_hours, 4);
});

test('remote shared state is authoritative while unreachable server keeps local fallback', () => {
  const local = { stale: { ts: 'old' } };
  assert.deepEqual(authoritativeSharedState(local, {}), {});
  assert.equal(authoritativeSharedState(local, null), local);
});

test('commercial UI contracts keep notifier server-owned and style new accessibility surfaces', async () => {
  const [settings, overview, app, data, store, css] = await Promise.all([
    readFile(new URL('../js/screens/settings.js', import.meta.url), 'utf8'),
    readFile(new URL('../js/screens/overview.js', import.meta.url), 'utf8'),
    readFile(new URL('../js/app.js', import.meta.url), 'utf8'),
    readFile(new URL('../js/model/data.js', import.meta.url), 'utf8'),
    readFile(new URL('../js/store.js', import.meta.url), 'utf8'),
    readFile(new URL('../css/styles.css', import.meta.url), 'utf8'),
  ]);
  assert.match(settings, /\/api\/admin\/notifications/);
  assert.match(settings, /\/api\/admin\/notifications\/test/);
  assert.match(settings, /if \(rollbackConfig\) S\.notifyConfig = rollbackConfig/);
  assert.match(settings, /S\.dom\.hookInput\.value = ''/);
  assert.doesNotMatch(settings, /getState\(\)\.notify|setState\(\{ notify/);
  assert.doesNotMatch(store, /getItem\(LS_NOTIFY\)|setItem\(LS_NOTIFY/);
  assert.doesNotMatch(data, /sendWebhook|claimEscal|NOTIFY_URL|ESCAL_URL/);
  assert.match(overview, /C\.confirmAction/);
  assert.match(overview, /reason: approved\.reason/);
  assert.doesNotMatch(overview, /next\[key\] = new Date\(\)\.toISOString\(\)/);
  assert.match(overview, /const initialLoading = !!state\.pollPending/);
  assert.match(overview, /N\.attEmptyAdd/);
  assert.match(app, /createSharedSyncCoordinator/);
  assert.match(app, /function startSharedRefresh\(\)[\s\S]*?setInterval\([\s\S]*?sharedSync\.retryAll\(\)[\s\S]*?visibilitychange/);
  assert.doesNotMatch(app, /function applyEscalation/);
  assert.match(app, /case 'maintSet':[\s\S]*?requireReason: true[\s\S]*?note: approved\.reason[\s\S]*?by: approved\.operator/);
  assert.match(app, /case 'ackAlert':[\s\S]*?requireReason: true[\s\S]*?reason: approved\.reason/);
  for (const token of ['.confirm-dialog', '.hd-banner-meta', '.rail-live.is-stale', '.rail-live.is-offline', '@media (forced-colors: active)', 'max-width:1279px']) {
    assert.ok(css.includes(token), 'missing CSS contract: ' + token);
  }
});
