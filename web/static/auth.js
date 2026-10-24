'use strict';
const get = id => document.getElementById(id);
let invite = '', userID = '', resetting = false;
function message(text, danger = true) {
  get('auth-notice').textContent = text;
  get('auth-notice').className = 'alert ' + (danger ? 'alert-danger' : 'alert-success');
}
function show(section) {
  for (const id of ['login-section', 'enroll-section', 'recovery-section', 'security-section']) get(id).classList.toggle('d-none', id !== section);
}
async function request(path, body) {
  const response = await fetch(path, { method: body ? 'POST' : 'GET', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, ...(body ? { body: JSON.stringify(body) } : {}) });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error?.message || 'Не удалось выполнить запрос. Повторите позже.');
  return data;
}
function formBody(form) {
  const values = Object.fromEntries(new FormData(form));
  if (values.code) values.code = values.code.trim().toLowerCase();
  return values;
}
function onSubmit(id, action) {
  get(id).onsubmit = async event => {
    event.preventDefault();
    const button = get(id).querySelector('button[type="submit"]');
    button.disabled = true;
    get('auth-notice').classList.add('d-none');
    try { await action(formBody(get(id))); }
    catch (error) { message(error.message); }
    finally { button.disabled = false; }
  };
}
function setup(data) {
  get('enroll-user').value = userID;
  get('totp-qr').src = data.qr;
  get('totp-secret').value = data.secret;
  get('new-password-group').classList.toggle('d-none', resetting);
  get('enroll-password').required = !resetting;
  get('enroll-code').value = '';
  show('enroll-section');
}
function recovery(data) {
  invite = '';
  get('totp-secret').value = '';
  get('totp-qr').removeAttribute('src');
  get('enroll-password').value = '';
  get('recovery-codes').value = data.recovery_codes.join('\n');
  get('recovery-login').href = '/auth?user=' + encodeURIComponent(userID);
  show('recovery-section');
  message('Настройка подтверждена. Сохраните резервные коды перед выходом.', false);
}
onSubmit('login-form', async body => {
  await request('/auth/login', body);
  get('login-password').value = '';
  location.replace('/');
});
onSubmit('enroll-form', async body => {
  const data = resetting
    ? await request('/auth/mfa/confirm', { code: body.code })
    : await request('/auth/enroll', { ...body, user_id: userID, invite });
  recovery(data);
});
onSubmit('password-form', async body => {
  await request('/auth/password', body);
  get('password-form').reset();
  show('login-section');
  message('Пароль изменён, все сеансы завершены. Войдите с новым паролем и следующим кодом аутентификатора.', false);
});
onSubmit('reset-mfa-form', async body => {
  const data = await request('/auth/mfa/start', body);
  get('reset-mfa-form').reset();
  resetting = true;
  setup(data);
});
get('download-recovery').onclick = () => {
  const url = URL.createObjectURL(new Blob(['Family expenses — аккаунт ' + userID + ' — резервные коды\n' + get('recovery-codes').value], { type: 'text/plain;charset=utf-8' }));
  const link = document.createElement('a');
  link.href = url; link.download = 'family-expenses-recovery.txt'; link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
};
(async () => {
  const fragment = new URLSearchParams(location.hash.slice(1));
  invite = fragment.get('invite') || '';
  userID = fragment.get('user') || '';
  get('login-user').value = userID || new URLSearchParams(location.search).get('user') || '';
  // Keep enrollment tokens out of subsequent URLs and browser storage.
  if (location.hash) history.replaceState(null, '', location.pathname);
  try {
    if (invite && userID) {
      get('login-user').value = userID;
      setup(await request('/auth/invite', { user_id: userID, invite }));
    } else if (location.pathname === '/auth/security') {
      const account = await request('/auth/me');
      userID = account.user_id;
      get('account-id').textContent = account.user_id;
      show('security-section');
    }
  } catch (error) { show('login-section'); message(error.message); }
})();
