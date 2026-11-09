import { $ } from '../lib/dom.js';
import { run } from '../lib/util.js';
import { api } from '../api.js';
import { state, addDirtyCheck } from '../state.js';
import { toast } from '../components/toast.js';

let unreg = null;
export function leave() { if (unreg) unreg(); unreg = null; }

export async function render(v, alive) {
  const r = await api('GET', '/api/config/raw');
  if (!alive()) return;
  leave();
  v.innerHTML = `<div class="bar"><h2>Raw config</h2><div class="grow"></div>
    <button class="btn" type="button" id="rawReload" title="Re-read config and agents from disk">Reload</button><button class="btn primary" type="button" id="rawSave">Save</button></div>
    <div class="note warn">API keys are shown masked. Masked values are kept as they are when you save. Invalid YAML is rejected.</div>
    <label class="sr" for="rawText">Config file (YAML)</label>
    <textarea id="rawText" class="yaml" spellcheck="false"></textarea>`;
  const ta = $('#rawText');
  let saved = (r && r.yaml) || '';
  ta.value = saved;
  unreg = addDirtyCheck(() => (ta.isConnected && ta.value !== saved ? 'Raw config' : ''));
  ta.addEventListener('keydown', e => {
    if (e.key === 'Tab' && !e.shiftKey) { e.preventDefault(); ta.setRangeText('  ', ta.selectionStart, ta.selectionEnd, 'end'); }
    if ((e.ctrlKey || e.metaKey) && e.key === 's') { e.preventDefault(); $('#rawSave').click(); }
  });
  $('#rawSave').addEventListener('click', e => run(async () => {
    const text = ta.value;
    await api('PUT', '/api/config/raw', { yaml: text });
    saved = text;
    state.meta = null;
    toast('Config saved', 'ok');
  }, e.currentTarget));
  $('#rawReload').addEventListener('click', e => run(async () => {
    if (ta.value !== saved && !confirm('Reload from disk? Your unsaved changes here will be lost.')) return;
    await api('POST', '/api/reload');
    const r2 = await api('GET', '/api/config/raw');
    if (!alive()) return;
    saved = (r2 && r2.yaml) || '';
    ta.value = saved;
    toast('Reloaded from disk', 'ok');
  }, e.currentTarget));
}
