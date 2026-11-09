import { $, h } from '../lib/dom.js';
import { enc, run } from '../lib/util.js';
import { api } from '../api.js';
import { state, loadProviders, loadMeta, providerType } from '../state.js';
import { toast } from '../components/toast.js';
import { openTestModal } from '../components/test-box.js';
import { openProviderEditor, openPresetPicker } from './provider-editor.js';
import { rerender } from '../router.js';

function keyStatus(p) {
  if (p.has_api_key) return `<span class="badge ok" title="${h(p.api_key)}">Key set</span>`;
  if (p.api_key_env) return `<span class="badge info">Env: ${h(p.api_key_env)}</span>`;
  return '<span class="badge">No key</span>';
}

export async function render(v, alive) {
  await Promise.all([loadProviders(), loadMeta().catch(() => null)]);
  if (!alive()) return;
  const list = state.providers;
  v.innerHTML = `<div class="bar"><h2>Providers</h2><span class="muted">${list.length}</span><div class="grow"></div>
    <button class="btn primary" type="button" id="addProv">+ Add provider</button></div>
    <p class="hint">Other AI services your agents and rules can run on. Click a row to edit.</p>
    ${list.length ? `<div class="tablewrap"><table><thead><tr><th>On</th><th>Provider</th><th>Type</th><th class="num">Models</th><th>Key</th><th><span class="sr">Actions</span></th><th>Base URL</th></tr></thead><tbody>
      ${list.map((p, i) => `<tr class="click" data-i="${i}">
        <td><label class="switch" title="Enabled"><input type="checkbox" data-en="${i}"${p.enabled ? ' checked' : ''} aria-label="Enable ${h(p.name || p.id)}"><span></span></label></td>
        <td><button type="button" class="btn link" data-act="edit" data-i="${i}" style="font-size:13px;font-weight:700;color:var(--fg);text-decoration:none">${h(p.name || p.id)}</button><div class="muted small mono">${h(p.id)}</div></td>
        <td><span class="badge">${h(providerType(p.type).label.replace(/-compatible$/, ''))}</span></td>
        <td class="num">${(p.models || []).length}</td>
        <td>${keyStatus(p)}</td>
        <td><div class="ra"><button class="btn sm" type="button" data-act="refresh" data-i="${i}" title="Load the full model list from the provider">↻ Models</button><button class="btn sm" type="button" data-act="test" data-i="${i}">Test</button></div></td>
        <td class="mono small">${h(p.base_url)}</td></tr>`).join('')}
    </tbody></table></div>` : '<div class="empty">No providers yet. Click “Add provider” and pick one.</div>'}`;
  $('#addProv').addEventListener('click', openPresetPicker);
  const tbody = $('tbody', v);
  if (!tbody) return;
  tbody.addEventListener('click', e => {
    if (e.target.closest('.switch')) return;
    const b = e.target.closest('button[data-act]');
    const tr = e.target.closest('tr[data-i]'); if (!tr) return;
    const p = list[Number(tr.dataset.i)];
    if (!b || b.dataset.act === 'edit') return openProviderEditor(p, false);
    if (b.dataset.act === 'test') return openTestModal(p);
    run(async () => {
      const saved = await api('POST', `/api/providers/${enc(p.id)}/refresh`);
      toast(`${p.name || p.id}: ${((saved && saved.models) || []).length} models`, 'ok');
      if (alive()) rerender();
    }, b);
  });
  tbody.addEventListener('change', e => {
    const cb = e.target.closest('input[data-en]'); if (!cb) return;
    const p = list[Number(cb.dataset.en)];
    const want = cb.checked;
    cb.disabled = true;
    run(async () => {
      try {
        const saved = await api('PUT', '/api/providers/' + enc(p.id) + '/enabled', { enabled: want });
        Object.assign(p, saved && typeof saved === 'object' ? saved : { enabled: want });
        toast(`${p.name || p.id} ${want ? 'enabled' : 'disabled'}`, 'ok');
      } catch (err) { cb.checked = !want; throw err; }
      finally { cb.disabled = false; }
    });
  });
}
