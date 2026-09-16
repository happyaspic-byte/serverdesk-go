import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

import { tooltipRows } from '../js/screens/topology.js';

const css = await readFile(new URL('../css/screens/topology.css', import.meta.url), 'utf8');

function rule(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = css.match(new RegExp(escaped + '\\s*\\{([^}]*)\\}'));
  assert.ok(match, 'missing CSS rule: ' + selector);
  return match[1].replace(/\s+/g, ' ');
}

function hasDeclaration(body, property, value) {
  const escapedProperty = property.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const escapedValue = value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  assert.match(body, new RegExp('(?:^|;)\\s*' + escapedProperty + '\\s*:\\s*' + escapedValue + '\\s*(?:;|$)'));
}

test('topology node cards keep long names inside the fixed-height card', () => {
  const head = rule('.sc-topo-node-head');
  hasDeclaration(head, 'display', 'flex');
  hasDeclaration(head, 'min-width', '0');
  hasDeclaration(head, 'padding-right', '12px');

  const name = rule('.sc-topo-node-name');
  hasDeclaration(name, 'flex', '1');
  hasDeclaration(name, 'min-width', '0');
  hasDeclaration(name, 'white-space', 'nowrap');
  hasDeclaration(name, 'overflow', 'hidden');
  hasDeclaration(name, 'text-overflow', 'ellipsis');

  const role = rule('.sc-topo-node-role');
  hasDeclaration(role, 'flex-shrink', '0');
  hasDeclaration(role, 'white-space', 'nowrap');

  const ip = rule('.sc-topo-node-ip');
  hasDeclaration(ip, 'flex', '1');
  hasDeclaration(ip, 'min-width', '0');
  hasDeclaration(ip, 'white-space', 'nowrap');
  hasDeclaration(ip, 'overflow', 'hidden');
  hasDeclaration(ip, 'text-overflow', 'ellipsis');

  const state = rule('.sc-topo-node-state');
  hasDeclaration(state, 'flex-shrink', '0');
  hasDeclaration(state, 'white-space', 'nowrap');
});

test('topology tooltip preserves the full node name after visual truncation', () => {
  const longName = 'sample-everrun-node-with-an-extremely-long-name';
  const rows = tooltipRows({ kind: 'node', name: longName, stateLabel: '정상' });
  assert.deepEqual(rows[0], ['title', longName]);
});
