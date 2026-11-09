import { $, $$, h } from '../lib/dom.js';
import { clone, run } from '../lib/util.js';
import { api } from '../api.js';
import { state, ROUTE_DEFAULT, loadProviders, loadRules, loadMeta, addDirtyCheck } from '../state.js';
import { toast } from '../components/toast.js';
import { mountRoute } from '../components/route-picker.js';

const CUSTOM = '__custom__';
let draft = [], editors = [], dirty = false, unreg = null;

export function leave() { if (unreg) unreg(); unreg = null; dirty = false; }

export async function render(v, alive) {
  await Promise.all([loadProviders(), loadRules(), loadMeta()]);
  if (!alive()) return;
  leave();
  draft = clone(state.rules) || [];
  v.innerHTML = `<div class="bar"><h2>Rules</h2><div class="grow"></div>
    <button class="btn" type="button" id="addRule">+ Add rule</button><button class="btn primary" type="button" id="saveRules">Save all</button></div>
    <p class="hint">For requests that are not from a routed agent. Pick which requests, then where they run. First match wins. No match = your subscription.</p>
    <div id="rulelist"></div>`;
  const box = $('#rulelist');
  unreg = addDirtyCheck(() => (dirty ? 'Rules' : ''));
  box.addEventListener('input', e => { if (!e.target.closest('[data-nodirty]')) dirty = true; });
  box.addEventListener('change', e => { if (!e.target.closest('[data-nodirty]')) dirty = true; });
  box.addEventListener('change', e => {
    const sel = e.target.closest('.r-msel'); if (!sel) return;
    const card = sel.closest('.rule'), c = sel.value === CUSTOM;
    $('.r-cw', card).style.display = c ? '' : 'none';
    if (c) $('.r-match', card).focus();
  });
  box.addEventListener('click', e => {
    const b = e.target.closest('button[data-act]'); if (!b) return;
    const i = Number(b.closest('.rule').dataset.i);
    sync();
    if (b.dataset.act === 'up' && i > 0) draft.splice(i - 1, 0, draft.splice(i, 1)[0]);
    else if (b.dataset.act === 'down' && i < draft.length - 1) draft.splice(i + 1, 0, draft.splice(i, 1)[0]);
    else if (b.dataset.act === 'del') { if (!confirm(`Delete rule #${i + 1}?`)) return; draft.splice(i, 1); }
    else return;
    dirty = true;
    paint();
    const again = $(`#rulelist .rule[data-i="${b.dataset.act === 'up' ? i - 1 : b.dataset.act === 'down' ? i + 1 : Math.min(i, draft.length - 1)}"] button[data-act="${b.dataset.act}"]`);
    if (again && !again.disabled) again.focus();
  });
  paint();
  $('#addRule').addEventListener('click', () => {
    sync();
    const pats = state.meta.rule_patterns;
    // Default to the first enabled provider: a rule that runs on the subscription does nothing.
    const first = state.providers.find(p => p.enabled);
    const route = Object.assign({}, ROUTE_DEFAULT, first ? { provider: first.id } : {});
    draft.push({ match: pats.length ? pats[0].match : '', enabled: true, note: '', route });
    dirty = true;
    paint();
    const sels = $$('#rulelist .r-msel'); if (sels.length) sels[sels.length - 1].focus();
  });
  $('#saveRules').addEventListener('click', e => run(async () => {
    sync();
    draft.forEach((r, i) => { if (!r.match) throw new Error(`Rule #${i + 1} needs a pattern`); });
    const saved = (await api('PUT', '/api/rules', draft)) || draft;
    state.rules = saved;
    dirty = false;
    if (!alive()) return;
    draft = clone(saved);
    paint();
    toast('Rules saved', 'ok');
  }, e.currentTarget));
}

/* Copy the form back into draft. */
function sync() {
  $$('#rulelist .rule').forEach(card => {
    const i = Number(card.dataset.i), r = draft[i];
    if (!r) return;
    const sv = $('.r-msel', card).value;
    r.match = sv === CUSTOM ? $('.r-match', card).value.trim() : sv;
    r.enabled = $('.r-en', card).checked;
    r.note = $('.r-note', card).value;
    r.route = editors[i].get();
  });
}

function paint() {
  const box = $('#rulelist'); if (!box) return;
  editors = [];
  if (!draft.length) { box.innerHTML = '<div class="empty">No rules: everything not from a routed agent uses your subscription.</div>'; return; }
  const pats = state.meta.rule_patterns;
  box.innerHTML = draft.map((r, i) => {
    const known = pats.some(p => p.match === r.match);
    return `<div class="card rule" data-i="${i}" role="group" aria-label="Rule ${i + 1}">
    <div class="rule-head"><span class="num">#${i + 1}</span>
      <label class="switch" title="Enabled"><input type="checkbox" class="r-en"${r.enabled ? ' checked' : ''} aria-label="Rule ${i + 1} enabled"><span></span></label>
      <select class="r-msel" aria-label="Which requests">${pats.map(p => `<option value="${h(p.match)}"${p.match === r.match ? ' selected' : ''}>${h(p.label)}</option>`).join('')}
        <option value="${h(CUSTOM)}"${known ? '' : ' selected'}>Custom pattern…</option></select>
      <button class="btn icon" type="button" data-act="up" aria-label="Move up"${i === 0 ? ' disabled' : ''}>↑</button>
      <button class="btn icon" type="button" data-act="down" aria-label="Move down"${i === draft.length - 1 ? ' disabled' : ''}>↓</button>
      <button class="btn icon danger" type="button" data-act="del" aria-label="Delete rule ${i + 1}">✕</button></div>
    <label class="f r-cw"${known ? ' style="display:none"' : ''}><span class="sr">Custom pattern</span><input class="r-match mono" value="${h(known ? '' : r.match)}" placeholder="claude-*-latest" spellcheck="false">
      <small>Glob on the requested model name. <code>*</code> = anything.</small></label>
    <label class="f"><span class="sr">Note</span><input class="r-note" value="${h(r.note)}" placeholder="Note (optional)"></label>
    <div class="r-route"></div></div>`;
  }).join('');
  $$('.rule', box).forEach(card => { editors[Number(card.dataset.i)] = mountRoute($('.r-route', card), draft[Number(card.dataset.i)].route); });
}
