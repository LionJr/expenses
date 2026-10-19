'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

// Exercise the actual form handler with a small DOM/HTTP adapter. This checks
// submitted payloads and editor state; layout and browser APIs still need a UI run.
async function panel(patchResponse) {
  const nodes = new Map(), requests = [];
  const element = () => ({ value: '', disabled: false, textContent: '', className: '', open: false,
    classList: { add() {}, remove() {}, toggle() {} }, append() {}, replaceChildren() {},
    reset() {}, focus() {}, showModal() { this.open = true; }, close() { this.open = false; } });
  const get = id => { if (!nodes.has(id)) nodes.set(id, element()); return nodes.get(id); };
  const context = vm.createContext({
    document: { getElementById: get, querySelector: () => ({ content: 'Asia/Ashgabat' }), createElement: element },
    URLSearchParams, FormData: class { *[Symbol.iterator]() {} }, setTimeout,
    fetch: async (path, options = {}) => {
      if (options.method === 'PATCH' || options.method === 'POST') {
        requests.push({ path, method: options.method, body: JSON.parse(options.body) });
        if (patchResponse) return patchResponse(path, options);
      }
      return { ok: true, status: 200, json: async () => path === '/api/users' ? { items: [{ id: '101', name: 'А', username: '' }] } : { items: [], total: 0, total_amount: '0' } };
    },
  });
  vm.runInContext(readFileSync('web/static/app.js', 'utf8'), context);
  await new Promise(resolve => setImmediate(resolve));
  return { context, get, requests, submit: () => get('expense-form').onsubmit({ preventDefault() {} }) };
}

const original = { id: '14', user_id: '101', amount: '10000', category: 'жильё', spent_at: '2026-10-19', comment: 'старый', version: '7' };

test('editing a comment sends only that field and the original version', async () => {
  const p = await panel();
  p.context.openEditor(original);
  p.get('edit-comment').value = 'новый';
  await p.submit();
  assert.deepEqual(p.requests, [{ path: '/api/expenses/14', method: 'PATCH', body: { expected_version: '7', comment: 'новый' } }]);
  assert.equal(p.get('editor').open, false);
});

test('a conflict keeps the draft open without retrying or overwriting it', async () => {
  const p = await panel(async () => ({ ok: false, status: 409, json: async () => ({ error: { message: 'конфликт' } }) }));
  p.context.openEditor(original);
  p.get('edit-amount').value = '123,45';
  await p.submit();
  assert.equal(p.requests.length, 1);
  assert.deepEqual(p.requests[0].body, { expected_version: '7', amount: '12345' });
  assert.equal(p.get('editor').open, true);
  assert.equal(p.get('edit-amount').value, '123,45');
  assert.match(p.get('form-error').textContent, /уже изменён/);
  assert.equal(p.get('save').disabled, false);
});

test('unchanged editor does not send an empty PATCH', async () => {
  const p = await panel();
  p.context.openEditor(original);
  await p.submit();
  assert.equal(p.requests.length, 0);
  assert.equal(p.get('editor').open, false);
});

test('creating an expense sends all fields without an edit precondition', async () => {
  const p = await panel();
  p.context.openEditor();
  p.get('edit-user').value = '101'; p.get('edit-amount').value = '12,34';
  p.get('edit-category').value = 'еда'; p.get('edit-date').value = '2026-10-19';
  p.get('edit-comment').value = '';
  await p.submit();
  assert.deepEqual(p.requests, [{ path: '/api/expenses', method: 'POST', body: { amount: '1234', category: 'еда', spent_at: '2026-10-19', comment: '', user_id: '101' } }]);
});

test('completion of an old save does not close a newer editor', async () => {
  let resolveSave;
  const p = await panel(() => new Promise(resolve => { resolveSave = resolve; }));
  p.context.openEditor(original); p.get('edit-comment').value = 'old save';
  const saving = p.submit();
  p.context.openEditor({ ...original, id: '7', comment: 'new draft' });
  resolveSave({ ok: true, status: 200, json: async () => ({}) });
  await saving;
  assert.equal(p.get('editor').open, true);
  assert.equal(p.get('edit-comment').value, 'new draft');
});
