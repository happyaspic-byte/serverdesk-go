import test from 'node:test';
import assert from 'node:assert/strict';
import screen from '../js/screens/manage.js';
import { pullPatch } from '../js/model/data.js';

const L = (en) => en;
function fixture(form, response = { ok: true, applied: ['label'] }) {
  const original = { id: 'edge-01', type: 'SRV', site: 'old site', meta: {
    label: 'old name', company: 'old company', mgmt: '127.0.0.1', vendor: 'Dell',
    bmc: { ip: '127.0.0.2' }, assetTag: 'old tag', floorPos: '1,1',
  } };
  const state = { form, fleet: [original], lastPoll: 123 };
  const toasts = [];
  const calls = [];
  const s = Object.assign({}, screen, { ctx: { L, showToast: (msg) => toasts.push(msg),
    store: { getState: () => state, setState: (patch) => Object.assign(state, patch) },
  }, api: async (...args) => { calls.push(args); return response; } });
  return { s, state, original, calls, toasts };
}
const editForm = () => ({ mode: 'edit', origKey: 'edge-01', key: 'edge-01', type: 'SRV',
  label: ' new name ', company: ' unconfirmed company ', factory: '', site: ' new site ',
  asset_tag: ' new tag ', floor_pos: '2,3', vendor: 'HPE', mgmt: '127.0.0.9',
  platform: 'redfish', bmc_ip: '127.0.0.99', bmc_user: 'admin', bmc_pass: ' secret ',
  admin_pass: ' other secret ', community: 'private', api: 'rest', model: 'model',
  nodes: ['127.0.0.4'], node0: '127.0.0.4', vms: [{ name: 'vm', ip: '127.0.0.5' }],
  showPw: {}, tags_text: 'count=Count(ea)', fieldErrors: {},
});

// Lightweight element builder: executes production render methods without a browser.
function el(tag, attrs = {}, children = []) {
  return { tag, attrs, children: children.filter(Boolean),
    appendChild(child) { this.children.push(child); return child; },
    setAttribute(key, value) { this.attrs[key] = value; },
  };
}
function all(root) { return [root, ...(root.children || []).flatMap((n) => typeof n === 'object' ? all(n) : [])]; }
const renderCtx = { L, util: { dom: { el }, icon: () => el('svg') } };

test('edit payload contains only supported metadata, including explicit clears', () => {
  const body = screen.buildBody(editForm());
  assert.deepEqual(body, { label: 'new name', company: 'unconfirmed company', factory: '',
    site: 'new site', asset_tag: 'new tag', floor_pos: '2,3', vendor: 'HPE' });
  assert.equal(Object.hasOwn(screen.buildBody({ ...editForm(), type: 'EV' }), 'vendor'), false);
});

test('edit connection and credential controls are disabled; metadata remains editable', () => {
  for (const type of ['EV', 'SRV', 'PLC', 'NAS']) {
    const f = { ...editForm(), type, showPw: { bmc_pass: true, admin_pass: true, node0_pass: true } };
    const grid = el('div');
    screen.connGroup(f, grid, renderCtx);
    screen.authGroup(f, grid, renderCtx);
    screen.vmGroup(f, grid, renderCtx);
    for (const control of all(grid).filter((n) => ['input', 'select', 'button'].includes(n.tag))) {
      const key = control.attrs['data-mng-f'];
      if (['site', 'vendor'].includes(key)) assert.equal(!!control.attrs.disabled, false, key);
      else assert.equal(control.attrs.disabled, true, key || JSON.stringify(control.attrs));
    }
  }
});

test('edit password has no change button even with showPw state', () => {
  for (const showPw of [{}, { admin_pass: true }]) {
    const rendered = screen.pwField({ ...editForm(), showPw }, 'admin_pass', 'Password', renderCtx);
    assert.equal(all(rendered).some((n) => Object.hasOwn(n.attrs, 'data-mng-pw')), false);
    assert.equal(all(rendered).some((n) => n.tag === 'input' && !n.attrs.disabled), false);
  }
});

test('metadata success overlays only applied fields; never BMC or management target', async () => {
  const { s, state, calls, toasts } = fixture(editForm(), { ok: true, applied: ['label', 'floor_pos'] });
  await s.submit();
  assert.equal(calls[0][0], 'PUT');
  const d = state.fleet[0];
  assert.equal(d.meta.label, 'new name');
  assert.equal(d.meta.floorPos, '2,3');
  assert.equal(d.meta.company, 'old company');
  assert.equal(d.meta.mgmt, '127.0.0.1');
  assert.equal(d.meta.bmc.ip, '127.0.0.2');
  assert.equal(d.site, 'old site');
  assert.deepEqual(Object.keys(d.meta.localEdit).sort(), ['floorPos', 'label', 'ts']);
  assert.equal(state.lastPoll, 123); // Metadata edits are not observations.
  assert.match(toasts[0], /metadata/i);
});

test('edit response without applied metadata cannot claim successful save', async () => {
  const { s, state, original, toasts } = fixture(editForm(), { ok: true, applied: [] });
  await s.submit();
  assert.equal(state.fleet[0], original);
  assert.notEqual(state.form, null);
  assert.equal(state.form.busy, false);
  assert.match(state.form.err, /metadata/i);
  assert.equal(toasts.length, 0);
});

test('metadata edit does not validate disabled operational requirements or probe credentials', async () => {
  const { s, state, calls } = fixture({ ...editForm(), mgmt: '', bmc_ip: '' });
  await s.testConnection();
  assert.equal(calls.length, 0);
  await s.submit();
  assert.equal(calls.length, 1);
  assert.equal(state.form, null);
});

test('add preserves credential whitespace, connection payload and enabled controls', async () => {
  const f = { ...editForm(), mode: 'add', platform: 'proxmox', admin_user: 'admin', admin_pass: ' raw password ', label: 'node' };
  const body = screen.buildBody(f);
  assert.equal(body.admin_pass, ' raw password ');
  assert.equal(body.mgmt, '127.0.0.9');
  assert.equal(body.type, 'SRV');
  const grid = el('div');
  screen.connGroup(f, grid, renderCtx);
  screen.authGroup(f, grid, renderCtx);
  for (const n of all(grid).filter((n) => ['input', 'select'].includes(n.tag))) assert.equal(!!n.attrs.disabled, false);
  const { s, calls, state } = fixture(f, { ok: true });
  await s.testConnection();
  assert.equal(calls[0][2].admin_pass, ' raw password ');
  await s.submit();
  assert.equal(calls[1][0], 'POST');
  assert.equal(calls[1][2].admin_pass, ' raw password ');
  assert.equal(state.form, null);
});

test('add accepts whitespace-only password as a nonempty raw credential', () => {
  assert.equal(screen.reqCredIssue({ mode: 'add', type: 'SRV', platform: 'proxmox', admin_user: 'admin', admin_pass: '   ' }, L), null);
});

test('pullPatch applies sparse metadata markers without clearing authoritative fields; retires echoed marker', async () => {
  const previousFetch = globalThis.fetch;
  const server = { id: 'edge-01', host: 'edge-01', type: 'SRV', site: 'server site', status: 'op',
    meta: { label: 'old name', company: 'server company', factory: 'server factory',
      mgmt: '127.0.0.1', bmc: { ip: '127.0.0.2' }, assetTag: 'tag', floorPos: '1,1', vendor: 'Dell' } };
  const local = { ...server, meta: { ...server.meta, localEdit: { ts: 1, label: 'new name' } } };
  globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => ({ devices: [server] }) });
  try {
    let patch = await pullPatch({ fleet: [local] });
    assert.equal(patch.fleet[0].meta.label, 'new name');
    assert.equal(patch.fleet[0].site, 'server site');
    assert.equal(patch.fleet[0].meta.company, 'server company');
    assert.equal(patch.fleet[0].meta.mgmt, '127.0.0.1');
    assert.equal(patch.fleet[0].meta.bmc.ip, '127.0.0.2');
    assert.equal(patch.fleet[0].meta.assetTag, 'tag');
    server.meta.label = 'new name';
    patch = await pullPatch({ fleet: patch.fleet });
    assert.equal(Object.hasOwn(patch.fleet[0].meta, 'localEdit'), false);

    local.meta.localEdit = { ts: 2, company: '', floorPos: '', site: '—' };
    patch = await pullPatch({ fleet: [local] });
    assert.equal(patch.fleet[0].meta.company, '');
    assert.equal(patch.fleet[0].meta.floorPos, '');
    assert.equal(patch.fleet[0].site, '—');
    assert.equal(patch.fleet[0].meta.mgmt, '127.0.0.1');
    server.meta.company = ''; server.meta.floorPos = ''; server.site = '—';
    patch = await pullPatch({ fleet: patch.fleet });
    assert.equal(Object.hasOwn(patch.fleet[0].meta, 'localEdit'), false);
  } finally { globalThis.fetch = previousFetch; }
});

test('pullPatch retains compatibility with complete legacy metadata markers', async () => {
  const previousFetch = globalThis.fetch;
  const marker = { label: 'new', company: 'corp', factory: 'plant', mgmt: '127.0.0.1',
    assetTag: '', floorPos: '2,2', vendor: 'HPE', site: 'site', bmcIp: '127.0.0.3', ts: 1 };
  const server = { id: 'edge-01', host: 'edge-01', type: 'SRV', site: 'old', status: 'op',
    meta: { label: 'old', bmc: { ip: '127.0.0.2' } } };
  globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => ({ devices: [server] }) });
  try {
    const patch = await pullPatch({ fleet: [{ ...server, meta: { localEdit: marker } }] });
    assert.equal(patch.fleet[0].meta.label, 'new');
    assert.equal(patch.fleet[0].site, 'site');
    assert.equal(patch.fleet[0].meta.mgmt, '127.0.0.1');
    assert.equal(patch.fleet[0].meta.bmc.ip, '127.0.0.3');
  } finally { globalThis.fetch = previousFetch; }
});
