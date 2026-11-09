import { $, h } from '../lib/dom.js';
import { fmtNum } from '../lib/format.js';
import { enc, run } from '../lib/util.js';
import { api } from '../api.js';
import { openModal } from './modal.js';

function resultHTML(r) {
  return r && r.ok
    ? `<p><span class="badge ok">OK</span> <span class="muted small">${h(fmtNum(r.latency_ms))} ms</span></p>${r.reply ? `<pre class="reply">${h(r.reply)}</pre>` : ''}`
    : `<p><span class="badge err">Failed</span>${r && r.latency_ms ? ` <span class="muted small">${h(fmtNum(r.latency_ms))} ms</span>` : ''}</p><pre class="reply">${h((r && r.error) || 'Unknown error')}</pre>`;
}

/* "Test" box: pick a model, send a tiny request with the saved settings.
   setModels() only refreshes the model list; the last result stays. */
export function mountTest(box, provider, models) {
  box.setAttribute('data-nodirty', '');
  box.innerHTML = `<div class="t-has"><div class="row"><label class="sr" for="t-model-${h(provider.id)}">Model to test</label>
      <select class="t-model mono" id="t-model-${h(provider.id)}"></select><button class="btn" type="button">Test</button></div>
    <small class="muted">Sends a tiny request using the saved settings.</small></div>
    <p class="muted small t-none">No models saved yet. Load models first.</p>
    <div class="t-out" aria-live="polite"></div>`;
  const sel = $('.t-model', box), out = $('.t-out', box);
  const setModels = list => {
    const keep = sel.value;
    sel.innerHTML = list.map(x => `<option value="${h(x)}">${h(x)}</option>`).join('');
    if (list.includes(keep)) sel.value = keep;
    $('.t-has', box).style.display = list.length ? '' : 'none';
    $('.t-none', box).style.display = list.length ? 'none' : '';
  };
  setModels(models || []);
  $('button', box).addEventListener('click', e => run(async () => {
    out.innerHTML = '<p class="muted small">Testing…</p>';
    try { out.innerHTML = resultHTML(await api('POST', `/api/providers/${enc(provider.id)}/test`, { model: sel.value })); }
    catch (err) { out.innerHTML = `<pre class="reply err">${h(err.message)}</pre>`; }
  }, e.currentTarget));
  return { setModels };
}

export function openTestModal(p) {
  const m = openModal({ title: 'Test: ' + (p.name || p.id), body: '<div class="tm-box"></div>' });
  mountTest($('.tm-box', m.el), p, p.models || []);
}
