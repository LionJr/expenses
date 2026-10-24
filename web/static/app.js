'use strict';
const $ = id => document.getElementById(id);
const categories = ['еда', 'транспорт', 'жильё', 'развлечения', 'здоровье', 'покупки', 'другое'];
const currency = document.querySelector('meta[name="app-currency"]').content;
const timezone = document.querySelector('meta[name="app-timezone"]').content;
let users = [], items = [], offset = 0, total = 0, editing = null, original = null, generation = 0, editorGeneration = 0;
const limit = 20;
$('timezone').textContent = timezone;
function money(value) {
  const n = BigInt(value), whole = (n / 100n).toLocaleString('ru-RU'), fraction = (n % 100n).toString().padStart(2, '0');
  return `${whole},${fraction} ${currency}`;
}
function amountToMinorUnits(value) {
  const match = value.trim().match(/^(\d+)(?:[.,](\d{1,2}))?$/);
  if (!match) throw new Error('Введите положительную сумму, не более двух знаков после запятой.');
  const amount = BigInt(match[1]) * 100n + BigInt((match[2] || '').padEnd(2, '0'));
  if (amount <= 0n || amount > 9223372036854775807n) throw new Error('Сумма вне допустимого диапазона.');
  return amount.toString();
}
function name(id) { const u = users.find(u => u.id === id); return u?.name.trim() || (u?.username ? '@' + u.username : id); }
function option(select, value, text) { const o = document.createElement('option'); o.value = value; o.textContent = text; select.append(o); }
function notice(message, danger = false) { $('notice').textContent = message; $('notice').className = 'alert ' + (danger ? 'alert-danger' : 'alert-success'); }
async function api(path, options = {}) {
  const response = await fetch(path, { ...options, headers: { 'Content-Type': 'application/json', ...options.headers } });
  if (response.status === 401) { location.replace('/auth'); throw new Error('Сеанс завершён. Войдите снова.'); }
  if (response.status === 204) return null;
  const result = await response.json();
  if (!response.ok) { const error = new Error(result.error?.message || 'Не удалось выполнить запрос.'); error.status = response.status; throw error; }
  return result;
}
function today() { const parts = new Intl.DateTimeFormat('en-CA', { timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(new Date()); return ['year','month','day'].map(k => parts.find(p => p.type === k).value).join('-'); }
function render() {
  $('rows').replaceChildren();
  for (const item of items) {
    const tr = document.createElement('tr');
    const values = [item.spent_at.split('-').reverse().join('.'), name(item.user_id), item.category, item.comment || '—', money(item.amount)];
    values.forEach((value, i) => {
      const td = document.createElement('td');
      if (i === 2) { const span = document.createElement('span'); span.className = 'category'; span.dataset.category = value; span.textContent = value; td.append(span); }
      else td.textContent = value;
      if (i === 3) td.className = 'comment';
      if (i === 4) td.className = 'text-end amount';
      tr.append(td);
    });
    const td = document.createElement('td'); td.className = 'actions text-end';
    const edit = document.createElement('button'); edit.textContent = 'Изменить'; edit.onclick = () => openEditor(item);
    const remove = document.createElement('button'); remove.textContent = 'Удалить'; remove.onclick = () => deleteItem(item, remove);
    td.append(edit, remove); tr.append(td); $('rows').append(tr);
  }
  $('empty').classList.toggle('d-none', items.length !== 0);
  $('prev').disabled = offset === 0; $('next').disabled = offset + limit >= total;
  $('page-label').textContent = total ? `${offset + 1}–${offset + items.length} из ${total}` : 'Нет записей';
}
async function load() {
  const request = ++generation;
  const query = new URLSearchParams(new FormData($('filters'))); query.set('limit', String(limit)); query.set('offset', String(offset));
  try {
    const result = await api('/api/expenses?' + query);
    if (request !== generation) return;
    items = result.items; total = result.total;
    if (offset > 0 && items.length === 0) { offset = Math.max(0, Math.floor((total - 1) / limit) * limit); return load(); }
    $('total-amount').textContent = money(result.total_amount); $('total-count').textContent = total.toLocaleString('ru-RU'); render();
  } catch (error) { if (request === generation) notice(error.message, true); }
}
function openEditor(item = null) {
  editorGeneration++;
  editing = item?.id || null; $('expense-form').reset(); $('form-error').classList.add('d-none');
  original = item ? { ...item } : null;
  $('editor-title').textContent = item ? 'Изменить расход' : 'Новый расход'; $('edit-user').disabled = !!item;
  if (item) {
    $('edit-user').value = item.user_id; const amount = BigInt(item.amount); $('edit-amount').value = `${amount / 100n},${(amount % 100n).toString().padStart(2,'0')}`;
    $('edit-date').value = item.spent_at; $('edit-category').value = item.category; $('edit-comment').value = item.comment;
  } else { $('edit-date').value = today(); }
  $('editor').showModal(); $('edit-amount').focus();
}
async function deleteItem(item, button) {
  if (!confirm(`Удалить расход «${item.comment || item.category}» на ${money(item.amount)}?`)) return;
  button.disabled = true;
  try { await api('/api/expenses/' + item.id, { method: 'DELETE' }); notice('Расход удалён.'); await load(); }
  catch (error) { notice(error.message, true); button.disabled = false; }
}
$('expense-form').onsubmit = async event => {
  event.preventDefault(); $('save').disabled = true;
  const editingID = editing, snapshot = original, editorRequest = editorGeneration;
  try {
    const values = { amount: amountToMinorUnits($('edit-amount').value), category: $('edit-category').value, spent_at: $('edit-date').value, comment: $('edit-comment').value };
    const body = editingID ? { expected_version: snapshot.version } : { ...values, user_id: $('edit-user').value };
    if (editingID) {
      for (const [key, value] of Object.entries(values)) if (value !== snapshot[key]) body[key] = value;
      if (Object.keys(body).length === 1) { $('editor').close(); notice('Изменений нет.'); return; }
    }
    await api('/api/expenses' + (editingID ? '/' + editingID : ''), { method: editingID ? 'PATCH' : 'POST', body: JSON.stringify(body) });
    if (editorRequest === editorGeneration) $('editor').close();
    notice(editingID ? 'Расход изменён.' : 'Расход добавлен.'); await load();
  } catch (error) {
    if (error.status === 409) await load();
    if (editorRequest === editorGeneration) { $('form-error').textContent = error.status === 409 ? 'Расход уже изменён. Закройте форму и откройте запись заново — ваши данные не перезаписаны.' : error.message; $('form-error').classList.remove('d-none'); }
  }
  finally { $('save').disabled = false; }
};
$('add').onclick = () => openEditor(); $('close').onclick = $('cancel').onclick = () => $('editor').close();
$('logout').onclick = async () => {
  try { await api('/auth/logout', { method: 'POST', body: '{}' }); location.replace('/auth'); }
  catch (error) { notice(error.message, true); }
};
$('filters').onsubmit = event => { event.preventDefault(); offset = 0; load(); };
$('filters').onreset = () => { offset = 0; setTimeout(load, 0); };
$('prev').onclick = () => { offset = Math.max(0, offset - limit); load(); }; $('next').onclick = () => { offset += limit; load(); };
(async () => {
  try {
    for (const category of categories) { option($('filter-category'), category, category); option($('edit-category'), category, category); }
    users = (await api('/api/users')).items;
    for (const user of users) { option($('filter-user'), user.id, name(user.id)); option($('edit-user'), user.id, name(user.id)); }
    $('add').disabled = users.length === 0; await load();
  } catch (error) { notice(error.message, true); }
})();
