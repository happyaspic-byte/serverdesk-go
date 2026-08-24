import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

import { validateDeleteConfirmation } from '../js/screens/manage.js';

test('device removal reason is required and bounded by Unicode characters', () => {
  assert.equal(validateDeleteConfirmation({
    origKey: 'edge-01', delete_phrase: 'edge-01', delete_reason: '  ',
  }).field, 'delete_reason');
  assert.equal(validateDeleteConfirmation({
    origKey: 'edge-01', delete_phrase: 'edge-01', delete_reason: '🙂'.repeat(501),
  }).field, 'delete_reason');
  assert.equal(validateDeleteConfirmation({
    origKey: 'edge-01', delete_phrase: 'edge-01', delete_reason: 'retired after approved change',
  }), null);
});

test('destructive requests carry the confirmed reason to the server audit API', async () => {
  const [manage, settings, app] = await Promise.all([
    readFile(new URL('../js/screens/manage.js', import.meta.url), 'utf8'),
    readFile(new URL('../js/screens/settings.js', import.meta.url), 'utf8'),
    readFile(new URL('../js/app.js', import.meta.url), 'utf8'),
  ]);

  assert.match(manage, /this\.api\('DELETE',[\s\S]*?reason: String\(f\.delete_reason/);
  assert.match(manage, /Stored in the server audit trail/);
  assert.doesNotMatch(manage, /does not persist an audit reason/);
  assert.match(settings, /Object\.assign\(\{\}, doc, \{ reason: String\(approved\.reason/);
  assert.match(settings, /body: JSON\.stringify\(payload\)/);
  assert.match(app, /reason\.maxLength = 500/);
});
