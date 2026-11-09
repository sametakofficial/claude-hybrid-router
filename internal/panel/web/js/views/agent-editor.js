import { $, $$, h } from '../lib/dom.js';
import { enc, clone, run, sanitizeName, splitTools } from '../lib/util.js';
import { api } from '../api.js';
import { state, COLORS, loadMeta } from '../state.js';
import { openModal } from '../components/modal.js';
import { toast } from '../components/toast.js';
import { mountRoute } from '../components/route-picker.js';
import { rerender } from '../router.js';

const fileAgents = () => state.agents.filter(a => !a.missing);

function uniqueName(base) {
  base = sanitizeName(base);
  if (!base || !state.agents.some(x => x.name === base)) return base;
  let n = 2; while (state.agents.some(x => x.name === base + '-' + n)) n++;
  return base + '-' + n;
}

export async function openTemplatePicker() {
  await run(async () => {
    const meta = await loadMeta();
    const tpls = meta.agent_templates.slice().sort((a, b) => (a.id === 'blank') - (b.id === 'blank'));
    const m = openModal({
      title: 'New agent', wide: true,
      body: `<p class="hint">Pick a starting point. You can change everything on the next screen.</p><div class="presets">
        ${tpls.map((t, i) => `<button type="button" class="preset" data-i="${i}"><b>${h(t.id === 'blank' ? 'Blank' : t.name || t.id)}</b><span>${h(t.id === 'blank' ? 'Empty form' : t.description)}</span>${t.id !== 'blank' ? `<span>Tools: ${h(t.tools || 'all')}</span>` : ''}</button>`).join('')}
      </div>`
    });
    $('.presets', m.el).addEventListener('click', e => {
      const b = e.target.closest('.preset'); if (!b) return;
      const t = tpls[Number(b.dataset.i)] || {};
      const blank = t.id === 'blank';
      openAgentEditor(null, { name: blank ? '' : uniqueName(t.name || t.id || ''), description: t.description || '', tools: t.tools || '', prompt: t.prompt || '' });
    });
  });
}

/* orig = existing agent, or null for a new one. tpl prefills a new agent.
   opts.recreate: the route exists in config but the .md file is missing. */
export async function openAgentEditor(orig, tpl, opts) {
  const meta = await loadMeta().catch(e => { toast(e.message, 'err'); return null; });
  if (!meta) return;
  const recreate = !!(opts && opts.recreate);
  const isNew = !orig;
  const a = Object.assign({ name: '', description: '', tools: '', model: '', color: '', prompt: '', route: {} }, clone(orig) || {}, isNew ? tpl || {} : {});
  const modelVal = a.model === 'inherit' ? '' : a.model;
  const mopts = meta.claude_models.slice();
  if (!mopts.some(x => x.value === '')) mopts.unshift({ value: '', label: 'Default (inherit)' });
  if (modelVal && !mopts.some(x => x.value === modelVal)) mopts.push({ value: modelVal, label: modelVal });
  const cur = splitTools(a.tools);
  const toolList = meta.tools.map(t => ({ name: t.name, desc: t.desc }));
  cur.forEach(n => { if (!toolList.some(t => t.name === n)) toolList.push({ name: n, desc: 'from file' }); });
  const allTools = !a.tools.trim();
  const colors = [''].concat(meta.colors);
  if (a.color && !colors.includes(a.color)) colors.push(a.color);
  const title = recreate ? 'Recreate agent file: ' + a.name : isNew ? 'New agent' : 'Edit agent: ' + a.name;
  const m = openModal({
    title, wide: true, label: 'the agent editor',
    body: `${recreate ? '<p class="note warn">This agent has a route in the config, but its file is missing. Saving creates the file again.</p>' : ''}
    <div class="grid2">
      <label class="f"><span>Name</span><input id="ag-name" value="${h(a.name)}" placeholder="my-agent" spellcheck="false" autocomplete="off"${recreate ? ' readonly' : ''}><small>Lowercase, no spaces. Also the file name.</small></label>
      <label class="f"><span>Claude model</span><select id="ag-model">${mopts.map(x => `<option value="${h(x.value)}"${x.value === modelVal ? ' selected' : ''}>${h(x.label)}</option>`).join('')}</select>
        <small>What Claude Code asks for. “Runs on” below can send it elsewhere.</small></label>
    </div>
    <label class="f"><span>Description</span><textarea id="ag-desc" rows="2">${h(a.description)}</textarea><small>Claude reads this to decide when to use the agent.</small></label>
    <div class="where"><b>Where does this agent run?</b><div id="ag-route"></div></div>
    <fieldset class="f" style="border:0;padding:0;margin:0 0 12px"><legend class="sr">Tools</legend><span>Tools</span>
      <label class="check"><input type="checkbox" id="ag-all"${allTools ? ' checked' : ''}> <b>All tools</b></label>
      <div class="toolgrid${allTools ? ' off' : ''}" id="ag-tools">${toolList.map(t => `<label><input type="checkbox" value="${h(t.name)}"${allTools || cur.includes(t.name) ? ' checked' : ''}${allTools ? ' disabled' : ''}><div>${h(t.name)}<small>${h(t.desc || '')}</small></div></label>`).join('')}</div>
    </fieldset>
    <div class="f"><span id="ag-color-l">Color</span><div class="swatches" id="ag-color" role="radiogroup" aria-labelledby="ag-color-l">${colors.map(c => c
      ? `<button type="button" class="sw${c === a.color ? ' on' : ''}" role="radio" aria-checked="${c === a.color}" data-c="${h(c)}" title="${h(c)}" aria-label="${h(c)}" style="background:${COLORS[c] || 'var(--panel2)'}">${COLORS[c] ? '' : h(c.slice(0, 2))}</button>`
      : `<button type="button" class="sw none${!a.color ? ' on' : ''}" role="radio" aria-checked="${!a.color}" data-c="" title="No color">none</button>`).join('')}</div></div>
    ${a.file ? `<p class="muted small" style="margin-top:0">File: <code>${h(a.file)}</code></p>` : ''}
    <label class="f"><span>Instructions (system prompt)</span><textarea id="ag-prompt" class="mono" rows="12" spellcheck="false" placeholder="You are …">${h(a.prompt)}</textarea></label>`,
    foot: `${isNew ? '' : '<button class="btn danger" type="button" id="ag-del">Delete</button>'}<div class="grow"></div>
      <button class="btn" type="button" data-close>Cancel</button><button class="btn primary" type="button" id="ag-save">Save</button>`
  });
  const el = m.el;
  const re = mountRoute($('#ag-route', el), a.route);
  const nameIn = $('#ag-name', el);
  nameIn.addEventListener('input', () => {
    const pos = nameIn.selectionStart, before = nameIn.value, after = sanitizeName(before);
    if (after !== before) { nameIn.value = after; const p = Math.max(0, pos - (before.length - after.length)); nameIn.setSelectionRange(p, p); }
  });
  if (isNew && !recreate) nameIn.focus();
  const allCb = $('#ag-all', el), grid = $('#ag-tools', el);
  allCb.addEventListener('change', () => {
    grid.classList.toggle('off', allCb.checked);
    $$('input', grid).forEach(c => { c.disabled = allCb.checked; if (allCb.checked) c.checked = true; });
  });
  let color = a.color || '';
  $('#ag-color', el).addEventListener('click', e => {
    const b = e.target.closest('.sw'); if (!b) return;
    if (b.dataset.c !== color) m.markDirty();
    color = b.dataset.c;
    $$('.sw', el).forEach(x => { x.classList.toggle('on', x === b); x.setAttribute('aria-checked', x === b); });
  });
  $('#ag-save', el).addEventListener('click', e => run(async () => {
    const name = nameIn.value.trim();
    if (!name) throw new Error('Please enter a name');
    if (!/^[A-Za-z0-9_.-]+$/.test(name)) throw new Error('Name may only use letters, digits, . _ -');
    let tools = '';
    if (!allCb.checked) {
      const picked = $$('input:checked', grid).map(c => c.value);
      if (!picked.length) throw new Error('Pick at least one tool, or check “All tools”');
      tools = picked.join(', ');
    }
    let model = $('#ag-model', el).value;
    if (!model && a.model === 'inherit') model = 'inherit';
    const body = Object.assign({}, a, {
      name, description: $('#ag-desc', el).value.trim(), tools, model, color,
      prompt: $('#ag-prompt', el).value, route: re.get()
    });
    delete body.missing; delete body.name_mismatch;
    if ((isNew || name !== orig.name) && fileAgents().some(x => x.name === name)) throw new Error('An agent with this name already exists');
    await api('PUT', '/api/agents/' + enc(isNew ? name : orig.name), body);
    m.close(true);
    toast(recreate ? 'Agent file created' : 'Agent saved', 'ok');
    rerender();
  }, e.currentTarget));
  const del = $('#ag-del', el);
  if (del) del.addEventListener('click', e => run(async () => {
    if (!confirm(`Move agent "${orig.name}" to trash?`)) return;
    const r = await api('DELETE', '/api/agents/' + enc(orig.name));
    m.close(true);
    toast('Moved to trash' + (r && r.trashed_to ? ': ' + r.trashed_to : ''), 'ok');
    rerender();
  }, e.currentTarget));
}
