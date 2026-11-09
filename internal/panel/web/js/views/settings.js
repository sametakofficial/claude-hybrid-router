import { $, h } from '../lib/dom.js';
import { run } from '../lib/util.js';
import { store } from '../lib/storage.js';
import { api } from '../api.js';
import { addDirtyCheck } from '../state.js';
import { toast } from '../components/toast.js';

let unreg = null, dirty = false;
export function leave() { if (unreg) unreg(); unreg = null; dirty = false; }

export async function render(v, alive) {
  const s = await api('GET', '/api/settings');
  if (!alive()) return;
  leave();
  const fld = (k, label, hint, attrs) => `<label class="f"><span>${label}</span><input id="st-${k}" class="mono" value="${h(s[k])}" spellcheck="false" ${attrs || ''}>${hint ? `<small>${hint}</small>` : ''}</label>`;
  v.innerHTML = `<div class="bar"><h2>Settings</h2><div class="grow"></div><button class="btn primary" type="button" id="saveSet">Save</button></div>
    <div id="st-note"></div>
    <form class="panel" id="st-form">
      <div class="grid2">
        ${fld('proxy_listen', 'Proxy listen', 'Address Claude Code connects to. Restart needed.')}
        ${fld('panel_listen', 'Panel listen', 'Address of this panel. Restart needed.')}
      </div>
      <div class="grid2">
        ${fld('upstream', 'Anthropic upstream', 'Where pass-through requests go.')}
        ${fld('header_timeout', 'Header timeout (seconds)', 'How long to wait for response headers.', 'type="number" min="0" step="1"')}
      </div>
      <div class="grid2">
        ${fld('agents_dir', 'Agents directory', 'Folder with agent .md files.')}
        ${fld('log_file', 'Log file', 'Empty = no log file.')}
      </div>
      <label class="f"><span>Panel token</span><input id="st-tok" type="password" autocomplete="new-password" placeholder="${h(s.panel_token ? s.panel_token : 'not set')}">
        <small>${s.panel_token ? 'A token is set. Leave blank to keep it.' : 'Optional. Protects this panel.'}</small></label>
    </form>`;
  unreg = addDirtyCheck(() => (dirty ? 'Settings' : ''));
  const form = $('#st-form');
  form.addEventListener('input', () => (dirty = true));
  form.addEventListener('submit', e => { e.preventDefault(); $('#saveSet').click(); });
  $('#saveSet').addEventListener('click', e => run(async () => {
    const body = Object.assign({}, s);
    ['proxy_listen', 'panel_listen', 'upstream', 'agents_dir', 'log_file'].forEach(k => (body[k] = $('#st-' + k).value.trim()));
    body.header_timeout = Math.max(0, parseInt($('#st-header_timeout').value, 10) || 0);
    const tok = $('#st-tok').value.trim();
    body.panel_token = tok || s.panel_token || '';
    const r = await api('PUT', '/api/settings', body);
    if (tok) store.set('chr_token', tok);
    if (r && r.settings) Object.assign(s, r.settings);
    dirty = false;
    if (!alive()) return;
    $('#st-tok').value = '';
    $('#st-note').innerHTML = r && r.restart_required ? '<div class="note warn" role="status">Saved. Restart claude-hybrid to apply the new listen address.</div>' : '';
    toast('Settings saved', 'ok');
  }, e.currentTarget));
}
