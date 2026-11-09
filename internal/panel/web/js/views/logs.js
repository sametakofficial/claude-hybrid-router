import { $, $$ } from '../lib/dom.js';
import { state, fetchLogs, loadStatus } from '../state.js';
import { logTable, bindLogTable } from '../components/log-table.js';

export async function render(v, alive) {
  if (!state.logsLoaded) await fetchLogs();
  if (!alive()) return;
  v.innerHTML = `<div class="bar"><h2>Logs</h2><div class="grow"></div>
    <div class="seg" id="lfilter" role="group" aria-label="Show">${[['all', 'All'], ['routed', 'Routed'], ['errors', 'Errors']].map(([k, l]) => `<button type="button" data-f="${k}" class="${state.logFilter === k ? 'on' : ''}" aria-pressed="${state.logFilter === k}">${l}</button>`).join('')}</div>
    <button class="btn" type="button" id="lpause">${state.paused ? '▶ Resume' : '❚❚ Pause'}</button></div>
    <p class="hint">Newest first. Up to 500 entries kept in this page. Updates every 2s.</p>
    <div id="logbody"></div>`;
  $('#lfilter').addEventListener('click', e => {
    const b = e.target.closest('button[data-f]'); if (!b) return;
    state.logFilter = b.dataset.f;
    $$('#lfilter button').forEach(x => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); });
    paint();
  });
  $('#lpause').addEventListener('click', e => {
    state.paused = !state.paused;
    e.currentTarget.textContent = state.paused ? '▶ Resume' : '❚❚ Pause';
    if (!state.paused) paint();
  });
  bindLogTable($('#logbody'));
  paint();
}

function paint() {
  const b = $('#logbody'); if (!b) return;
  let list = state.logs.slice().reverse();
  if (state.logFilter === 'routed') list = list.filter(e => e.kind === 'routed');
  if (state.logFilter === 'errors') list = list.filter(e => e.error || !e.status || e.status >= 400);
  b.innerHTML = logTable(list, true);
}

let tickN = 0;
export async function tick(alive) {
  let changed = await fetchLogs();
  if (!alive()) return;
  if (++tickN % 3 === 0) {
    const before = state.lastLogId;
    await loadStatus();
    if (state.lastLogId < before) { await fetchLogs(); changed = true; }
  }
  if (changed && !state.paused && alive()) paint();
}
