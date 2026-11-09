import { $, $$, h } from '../lib/dom.js';
import { enc, clone, run } from '../lib/util.js';
import { api } from '../api.js';
import { state, loadProviders, loadMeta, providerType } from '../state.js';
import { openModal } from '../components/modal.js';
import { toast } from '../components/toast.js';
import { mountModelChecklist } from '../components/model-checklist.js';
import { mountTest } from '../components/test-box.js';
import { guard, rerender } from '../router.js';

function slugId(name, selfId) {
  const base = String(name || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40) || 'provider';
  const taken = id => id !== selfId && state.providers.some(x => x.id === id);
  if (!taken(base)) return base;
  let n = 2; while (taken(base + '-' + n)) n++;
  return base + '-' + n;
}

export async function openPresetPicker() {
  await run(async () => {
    if (!state.presets) state.presets = (await api('GET', '/api/presets')) || [];
    const m = openModal({
      title: 'Add provider', wide: true,
      body: `<p class="hint">Pick your service. The form is filled in for you.</p><div class="presets">
        ${state.presets.map((p, i) => `<button type="button" class="preset" data-i="${i}"><b>${h(p.name)}</b><span>${h(p.base_url)}</span>${p.note ? `<span>${h(p.note)}</span>` : ''}</button>`).join('')}
        <button type="button" class="preset" data-i="-1"><b>Other</b><span>Any OpenAI- or Anthropic-compatible API</span></button></div>`
    });
    $('.presets', m.el).addEventListener('click', e => {
      const b = e.target.closest('.preset'); if (!b) return;
      const pr = state.presets[Number(b.dataset.i)];
      openProviderEditor({
        id: '', name: pr ? pr.name : '', type: pr ? pr.type : 'openai', base_url: pr ? pr.base_url : '',
        api_key: '', has_api_key: false, api_key_env: pr ? pr.api_key_env || '' : '', headers: {},
        models: pr ? (pr.models || []).slice() : [], max_tokens_field: pr ? pr.max_tokens_field || '' : '', enabled: true
      }, true);
    });
  });
}

const hdrRow = (k, val) => `<div class="hrow"><input class="hk mono" placeholder="Header-Name" aria-label="Header name" value="${h(k)}" spellcheck="false"><input class="hv mono" placeholder="value" aria-label="Header value" value="${h(val)}" spellcheck="false"><button class="btn icon ghost" type="button" data-rm aria-label="Remove header">✕</button></div>`;

export async function openProviderEditor(orig, isNew) {
  const meta = await loadMeta().catch(() => null);
  const types = (meta && meta.provider_types) || [providerType('openai'), providerType('anthropic')];
  const alive = guard();
  const p = Object.assign({ id: '', name: '', type: 'openai', base_url: '', api_key: '', has_api_key: false, api_key_env: '', headers: {}, models: [], max_tokens_field: '', enabled: true }, clone(orig));
  const typeList = types.slice();
  if (p.type && !typeList.some(t => t.name === p.type)) typeList.push({ name: p.type, label: p.type, help: '' });
  const advOpen = !!(p.api_key_env || Object.keys(p.headers || {}).length || p.max_tokens_field);
  let clearKey = false;
  const m = openModal({
    title: isNew ? 'New provider' : 'Provider: ' + (p.name || p.id), wide: true, label: 'the provider editor',
    body: `<label class="f"><span>Name</span><input id="pv-name" value="${h(p.name)}" placeholder="e.g. OpenCode Go" autocomplete="off">
        <small class="idtxt">ID: <code id="pv-id">${h(isNew ? slugId(p.name) : p.id)}</code>${isNew ? ' (made from the name)' : ''}</small></label>
    <div class="grid2">
      <label class="f"><span>Type</span><select id="pv-type">${typeList.map(t => `<option value="${h(t.name)}"${t.name === (p.type || 'openai') ? ' selected' : ''}>${h(t.label || t.name)}</option>`).join('')}</select>
        <small id="pv-typehint"></small></label>
      <label class="f"><span>Base URL</span><input id="pv-url" class="mono" value="${h(p.base_url)}" placeholder="https://api.example.com/v1" spellcheck="false"><small id="pv-urlhint"></small></label>
    </div>
    <div class="f"><span><label for="pv-key">API key</label></span>
      <div class="keyrow"><input id="pv-key" type="password" autocomplete="new-password" placeholder="${h(p.has_api_key ? p.api_key || '••••' : 'Paste your key')}">
        ${p.has_api_key && p.api_key ? '<button class="btn sm danger" type="button" id="pv-clear">Remove key</button>' : ''}</div>
      <small id="pv-keyhint">${p.has_api_key ? (p.api_key ? 'Leave blank to keep the current key.' : 'The key comes from the environment variable below.') : 'Not needed for local servers.'}</small></div>
    <h4>Models</h4>
    <div class="row" style="margin-bottom:6px"><button class="btn" id="pv-load" type="button">↻ Load models</button><span class="stline" id="pv-st" role="status"></span></div>
    <div id="pv-models"></div>
    <details class="adv"${advOpen ? ' open' : ''}><summary>Advanced</summary>
      <div class="grid2">
        <label class="f" id="pv-mtfw"><span>Max tokens field</span><select id="pv-mtf">
          <option value=""${!p.max_tokens_field ? ' selected' : ''}>Default</option>
          <option value="max_tokens"${p.max_tokens_field === 'max_tokens' ? ' selected' : ''}>max_tokens</option>
          <option value="max_completion_tokens"${p.max_tokens_field === 'max_completion_tokens' ? ' selected' : ''}>max_completion_tokens</option>
        </select><small>Newer OpenAI models need max_completion_tokens.</small></label>
        <label class="f"><span>API key env var</span><input id="pv-env" class="mono" value="${h(p.api_key_env)}" placeholder="OPENAI_API_KEY" spellcheck="false"><small>Optional. Read the key from this environment variable.</small></label>
      </div>
      <div class="f"><span>Extra headers</span>
        <div id="pv-hdrs">${Object.entries(p.headers || {}).map(([k, val]) => hdrRow(k, val)).join('')}</div>
        <small>Secret values are shown masked. A masked value is kept when you save.</small>
        <div><button class="btn sm" id="pv-hadd" type="button">+ Header</button></div></div>
    </details>
    <label class="check" style="margin-bottom:8px"><input type="checkbox" id="pv-en"${p.enabled ? ' checked' : ''}> Enabled (show in “Runs on” lists)</label>
    ${isNew ? '' : '<h4>Test</h4><div id="pv-test"></div>'}`,
    foot: `${isNew ? '' : '<button class="btn danger" type="button" id="pv-del">Delete</button>'}<div class="grow"></div>
      <button class="btn" type="button" data-close>Cancel</button><button class="btn primary" type="button" id="pv-save">Save</button>`
  });
  const el = m.el;
  const typeSel = $('#pv-type', el), urlIn = $('#pv-url', el), keyIn = $('#pv-key', el), envIn = $('#pv-env', el), nameIn = $('#pv-name', el);
  const hdrs = $('#pv-hdrs', el), st = $('#pv-st', el);
  const readHeaders = () => { const o = {}; $$('.hrow', hdrs).forEach(r => { const k = $('.hk', r).value.trim(); if (k) o[k] = $('.hv', r).value; }); return o; };
  const curId = () => (isNew ? slugId(nameIn.value) : p.id);
  if (isNew) nameIn.addEventListener('input', () => ($('#pv-id', el).textContent = curId()));

  const test = isNew ? null : mountTest($('#pv-test', el), p, p.models || []);
  const models = mountModelChecklist($('#pv-models', el), {
    models: p.models,
    onPick: list => { if (test) test.setModels(list); },
    onEdit: () => m.markDirty()
  });

  const syncType = () => {
    const t = providerType(typeSel.value);
    const oa = typeSel.value !== 'anthropic';
    $('#pv-mtfw', el).style.display = oa ? '' : 'none';
    $('#pv-typehint', el).textContent = t.help || '';
    const base = (urlIn.value.trim() || '{base_url}').replace(/\/+$/, '');
    $('#pv-urlhint', el).textContent = 'Requests go to ' + base + (oa ? '/chat/completions' : '/v1/messages');
  };
  syncType();

  const clearBtn = $('#pv-clear', el);
  const syncKey = () => {
    if (!clearBtn) return;
    clearBtn.textContent = clearKey ? 'Undo' : 'Remove key';
    clearBtn.classList.toggle('danger', !clearKey);
    $('#pv-keyhint', el).textContent = clearKey ? 'The saved key will be removed when you save.' : 'Leave blank to keep the current key.';
    keyIn.placeholder = clearKey ? 'No key' : (p.api_key || '••••');
  };
  if (clearBtn) clearBtn.addEventListener('click', () => { clearKey = !clearKey; if (clearKey) keyIn.value = ''; m.markDirty(); syncKey(); });

  const setStatus = (txt, kind) => {
    txt = String(txt || '');
    st.textContent = txt.length > 300 ? txt.slice(0, 300) + '…' : txt;
    st.title = txt.length > 300 ? txt : '';
    st.className = 'stline' + (kind ? ' ' + kind : '');
  };
  // Live model discovery (does not save anything).
  let seq = 0, timer = null;
  const discover = async () => {
    clearTimeout(timer);
    const base = urlIn.value.trim();
    if (!base) { setStatus('Enter the base URL first.', 'err'); return; }
    const my = ++seq;
    setStatus('Loading models…');
    const btn = $('#pv-load', el); btn.disabled = true;
    try {
      const body = { type: typeSel.value, base_url: base, api_key: keyIn.value.trim(), api_key_env: envIn.value.trim(), headers: readHeaders() };
      if (!isNew && !clearKey) body.id = p.id;
      const r = await api('POST', '/api/discover', body);
      if (my !== seq || !el.isConnected) return;
      const found = Array.from(new Set((r && r.models) || [])).sort((a, b) => a.localeCompare(b));
      if (!found.length) { setStatus('The provider returned no models. Add one manually below.', 'err'); models.openManual(); return; }
      const fresh = models.setFound(found, { isNew, saved: p.models });
      setStatus(`${found.length} models found` + (fresh ? ` · ${fresh} new (unchecked)` : ''), 'ok');
    } catch (err) {
      if (my !== seq || !el.isConnected) return;
      setStatus(err.message || String(err), 'err');
      models.openManual();
    } finally { if (my === seq) btn.disabled = false; }
  };
  const soon = () => {
    clearTimeout(timer);
    if (!urlIn.value.trim()) return;
    if (!keyIn.value.trim() && !envIn.value.trim() && !(p.has_api_key && !clearKey)) return;
    timer = setTimeout(discover, 600);
  };
  $('#pv-load', el).addEventListener('click', discover);
  urlIn.addEventListener('input', () => { syncType(); soon(); });
  typeSel.addEventListener('change', () => { syncType(); soon(); });
  keyIn.addEventListener('input', () => { if (clearKey && keyIn.value) { clearKey = false; syncKey(); } soon(); });
  envIn.addEventListener('input', soon);
  hdrs.addEventListener('click', e => { const b = e.target.closest('[data-rm]'); if (b) { b.parentElement.remove(); m.markDirty(); } });
  $('#pv-hadd', el).addEventListener('click', () => { hdrs.insertAdjacentHTML('beforeend', hdrRow('', '')); $$('.hk', hdrs).pop().focus(); });
  if (!isNew && p.base_url) discover();
  else if (isNew && p.base_url) setStatus('Paste your key, the model list loads by itself.');

  $('#pv-save', el).addEventListener('click', e => run(async () => {
    const name = nameIn.value.trim();
    if (!name) throw new Error('Please enter a name');
    const id = curId();
    const base = urlIn.value.trim();
    if (!base) throw new Error('Base URL is required');
    const picked = models.selected();
    if (models.all().length && !picked.length) throw new Error('Pick at least one model');
    const key = keyIn.value.trim();
    const body = Object.assign({}, p, {
      id, name, type: typeSel.value, base_url: base,
      api_key: key, api_key_env: envIn.value.trim(),
      headers: readHeaders(), models: picked, max_tokens_field: typeSel.value !== 'anthropic' ? $('#pv-mtf', el).value : '',
      enabled: $('#pv-en', el).checked
    });
    delete body.has_api_key;
    if (clearKey && !key) body.clear_api_key = true;
    const saved = await api('PUT', '/api/providers/' + enc(id) + (isNew ? '?create=1' : ''), body);
    await loadProviders();
    m.close(true);
    toast(`Provider saved · ${((saved && saved.models) || picked).length} models`, 'ok');
    if (alive()) rerender();
  }, e.currentTarget));
  const del = $('#pv-del', el);
  if (del) del.addEventListener('click', e => run(async () => {
    if (!confirm(`Delete provider "${p.name || p.id}"?`)) return;
    await api('DELETE', '/api/providers/' + enc(p.id));
    m.close(true); toast('Provider deleted', 'ok');
    if (alive()) rerender();
  }, e.currentTarget));
}
