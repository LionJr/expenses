'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

async function authPage({ hash = '', pathname = '/auth', respond }) {
  const nodes = new Map(), events = [], requests = [], redirects = [];
  const get = id => {
    if (!nodes.has(id)) nodes.set(id, {
      value: '', values: {}, disabled: false, required: false, textContent: '', className: '',
      classList: { add() {}, toggle() {} }, querySelector: () => get(id + '-submit'),
      removeAttribute(name) { delete this[name]; }, reset() { this.values = {}; },
    });
    return nodes.get(id);
  };
  const context = vm.createContext({
    document: { getElementById: get },
    location: { hash, pathname, search: '', replace: path => redirects.push(path) },
    history: { replaceState: (...args) => events.push(['history', ...args]) },
    URLSearchParams, FormData: class { constructor(form) { this.values = form.values; } *[Symbol.iterator]() { yield* Object.entries(this.values); } },
    setTimeout,
    fetch: async (path, options) => {
      events.push(['fetch', path]);
      const request = { path, options, body: options.body ? JSON.parse(options.body) : undefined };
      requests.push(request);
      const result = await respond(request);
      return { ok: (result.status || 200) < 400, status: result.status || 200, json: async () => result.body };
    },
  });
  vm.runInContext(readFileSync('web/static/auth.js', 'utf8'), context);
  await new Promise(resolve => setImmediate(resolve));
  return { get, requests, events, redirects, submit: id => get(id).onsubmit({ preventDefault() {} }) };
}

test('personal invitation is removed from the URL before POST and never placed in storage', async () => {
  const token = 'a'.repeat(64), codes = ['b'.repeat(32), 'c'.repeat(32)];
  const p = await authPage({ hash: '#user=42&invite=' + token, respond: async request => ({ body: request.path === '/auth/invite' ? { secret: 'BASE32SECRET', qr: 'data:image/png;base64,test' } : { recovery_codes: codes } }) });
  assert.equal(p.events[0][0], 'history');
  assert.equal(p.requests[0].path, '/auth/invite');
  assert.deepEqual(p.requests[0].body, { user_id: '42', invite: token });
  assert.equal(p.get('enroll-user').value, '42');
  assert.equal(p.get('totp-secret').value, 'BASE32SECRET');
  p.get('enroll-form').values = { password: 'A personal strong family passphrase', code: '123456' };
  await p.submit('enroll-form');
  assert.deepEqual(p.requests[1].body, { password: 'A personal strong family passphrase', code: '123456', user_id: '42', invite: token });
  assert.equal(p.get('recovery-codes').value, codes.join('\n'));
  assert.equal(p.get('totp-secret').value, '');
  assert.equal(p.get('totp-qr').src, undefined);
  assert.equal(p.get('recovery-login').href, '/auth?user=42');
  for (const request of p.requests) {
    assert.equal(request.options.credentials, 'same-origin');
    assert.ok(request.path.startsWith('/auth/'));
    assert.ok(!request.path.includes(token));
  }
});

test('login sends password and factor, reports rejection and redirects only after success', async () => {
  let reject = true;
  const p = await authPage({ respond: async () => reject ? { status: 401, body: { error: { message: 'Неверный код' } } } : { body: { ok: true } } });
  p.get('login-form').values = { user_id: '42', password: 'A personal strong family passphrase', code: ' ABCDEF0123456789ABCDEF0123456789 ' };
  await p.submit('login-form');
  assert.equal(p.requests[0].body.code, 'abcdef0123456789abcdef0123456789');
  assert.equal(p.get('auth-notice').textContent, 'Неверный код');
  assert.deepEqual(p.redirects, []);
  assert.equal(p.get('login-form-submit').disabled, false);
  reject = false;
  await p.submit('login-form');
  assert.deepEqual(p.redirects, ['/']);
  assert.equal(p.get('login-password').value, '');
});

test('MFA replacement confirms a new factor and presents replacement backup codes', async () => {
  const p = await authPage({ pathname: '/auth/security', respond: async request => ({ body: request.path === '/auth/me' ? { user_id: '42' } : request.path === '/auth/mfa/start' ? { secret: 'NEWSECRET', qr: 'data:image/png;base64,new' } : { recovery_codes: ['a'.repeat(32)] } }) });
  assert.equal(p.get('account-id').textContent, '42');
  p.get('reset-mfa-form').values = { password: 'A personal strong family passphrase', code: '123456' };
  await p.submit('reset-mfa-form');
  assert.equal(p.get('enroll-password').required, false);
  assert.equal(p.get('totp-secret').value, 'NEWSECRET');
  p.get('enroll-form').values = { password: '', code: '654321' };
  await p.submit('enroll-form');
  assert.equal(p.requests.at(-1).path, '/auth/mfa/confirm');
  assert.deepEqual(p.requests.at(-1).body, { code: '654321' });
  assert.equal(p.get('recovery-login').href, '/auth?user=42');
});
