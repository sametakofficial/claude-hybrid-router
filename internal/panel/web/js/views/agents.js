import { $, $$, h } from '../lib/dom.js';
import { enc, run, splitTools } from '../lib/util.js';
import { api } from '../api.js';
import { state, ROUTE_DEFAULT, COLORS, loadProviders, loadAgents, loadMeta } from '../state.js';
import { toast } from '../components/toast.js';
import { routeCombobox, routeText, parseRv, rv } from '../components/route-picker.js';
import { openAgentEditor, openTemplatePicker } from './agent-editor.js';
import { rerender } from '../router.js';

/* Route-only save, so edits made to the .md file since the page loaded are never overwritten. */
async function putRoute(name, route) {
  const r = await api('PUT', '/api/agents/' + enc(name) + '/route', route);
  return (r && r.route) || route;
}

export async function render(v, alive) {
  await Promise.all([loadProviders(), loadAgents(), loadMeta()]);
  if (!alive()) return;
  const sorted = state.agents.slice().sort((a, b) => a.name.localeCompare(b.name));
  const list = sorted.filter(a => !a.missing), missing = sorted.filter(a => a.missing);
  v.innerHTML = `<div class="bar"><h2>Agents</h2><span class="muted">${list.length}</span><div class="grow"></div>
    <button class="btn primary" type="button" id="newAgent">+ New agent</button></div>
    <p class="hint">Claude Code subagents (<code>~/.claude/agents/*.md</code>). Pick where each agent runs. Changes save at once.</p>
    ${missing.length ? `<div class="note warn" role="status"><b>${missing.length === 1 ? 'One route has' : missing.length + ' routes have'} no agent file.</b> The config routes these agents, but their <code>.md</code> file is gone.</div>
      <div class="cards" style="margin-bottom:14px">${missing.map((a, i) => `<div class="card missing" data-m="${i}">
        <div class="title"><span class="badge warn">File missing</span>${h(a.name)}</div>
        <div class="desc">Runs on: ${h(routeText(a.route))}</div>
        <div class="row"><button class="btn sm" type="button" data-act="recreate">Recreate file</button><button class="btn sm danger" type="button" data-act="unroute">Remove route</button></div>
      </div>`).join('')}</div>` : ''}
    ${list.length ? `<div class="cards" id="agentCards">${list.map((a, i) => `<div class="card click" data-i="${i}" tabindex="0" aria-label="Edit agent ${h(a.name)}">
      <div class="title"><span class="cdot" style="background:${COLORS[a.color] || 'transparent'}"></span>${h(a.name)}${a.name_mismatch ? ' <span class="badge warn" title="The name inside the file differs from the file name. Claude Code uses the name inside the file.">Name ≠ file</span>' : ''}</div>
      <div class="desc">${h(a.description) || '<i>No description</i>'}</div>
      <div class="meta"><span class="badge" title="Claude model">${h(a.model || 'inherit')}</span><span class="badge" title="${h(a.tools || 'All tools')}">${a.tools ? h(splitTools(a.tools).length) + ' tools' : 'All tools'}</span></div>
      <div class="runs"><span id="runs-l-${i}">Runs on</span><div class="rt-slot"></div></div>
    </div>`).join('')}</div>` : '<div class="empty">No agents yet. Click “New agent” and pick a template.</div>'}`;
  $('#newAgent').addEventListener('click', openTemplatePicker);

  $$('.card[data-i]', v).forEach(card => {
    const a = list[Number(card.dataset.i)];
    const cb = routeCombobox({
      route: a.route, compact: true, ariaLabel: 'Runs on, agent ' + a.name,
      onChange: value => {
        const c = parseRv(value);
        const route = c.provider ? Object.assign({}, ROUTE_DEFAULT, a.route || {}, c) : Object.assign({}, ROUTE_DEFAULT);
        const prev = a.route;
        cb.disabled = true;
        putRoute(a.name, route)
          .then(saved => { a.route = saved; toast(`${a.name}: ${routeText(saved)}`, 'ok'); })
          .catch(e => { a.route = prev; cb.value = rv(prev && prev.provider, prev && prev.model); toast(e.message || String(e), 'err'); })
          .finally(() => { cb.disabled = false; });
      }
    });
    $('.rt-slot', card).appendChild(cb.el);
    card.addEventListener('click', e => { if (!e.target.closest('.runs')) openAgentEditor(a); });
    card.addEventListener('keydown', e => { if ((e.key === 'Enter' || e.key === ' ') && e.target === card) { e.preventDefault(); openAgentEditor(a); } });
  });

  $$('.card[data-m]', v).forEach(card => {
    const a = missing[Number(card.dataset.m)];
    card.addEventListener('click', e => {
      const b = e.target.closest('button[data-act]'); if (!b) return;
      if (b.dataset.act === 'recreate') openAgentEditor(null, { name: a.name, route: a.route }, { recreate: true });
      else run(async () => {
        if (!confirm(`Remove the route for "${a.name}"? Its requests will use your subscription again.`)) return;
        await putRoute(a.name, Object.assign({}, ROUTE_DEFAULT));
        toast('Route removed', 'ok');
        rerender();
      }, b);
    });
  });
}
