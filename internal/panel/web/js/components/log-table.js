import { h } from '../lib/dom.js';
import { fmtNum, fmtTime } from '../lib/format.js';
import { state } from '../state.js';

export function statusBadge(st, err) {
  const cls = !st ? 'err' : st < 300 ? 'ok' : st < 500 ? 'warn' : 'err';
  return `<span class="badge ${err && st < 300 ? 'warn' : cls}">${st ? h(st) : 'ERR'}</span>`;
}
function logTarget(e) {
  if (e.kind !== 'routed') return '<span class="muted">Anthropic</span>' + (e.model && e.model !== e.req_model ? ' / ' + h(e.model) : '');
  return `<b>${h(e.provider)}</b> / ${h(e.model || e.req_model)}`;
}
export function logTable(list, full) {
  if (!list.length) return `<div class="empty">${state.logsLoaded ? 'No log entries.' : 'Loading…'}</div>`;
  return `<div class="tablewrap"><table><thead><tr><th>Time</th><th>Source</th><th>Request → target</th><th>Status</th><th class="num">Latency</th><th class="num">Tokens in/out</th>${full ? '<th>Path</th>' : ''}<th>Error</th></tr></thead><tbody>
  ${list.map(e => `<tr>
    <td title="${h(e.time)}">${fmtTime(e.time)}</td>
    <td>${e.source ? `<span class="badge${e.kind === 'routed' ? ' routed' : ''}">${h(e.source)}</span>` : '<span class="muted">–</span>'}</td>
    <td><span class="mono">${h(e.req_model)}</span> → ${logTarget(e)}${e.stream ? ' <span class="badge info" title="Streaming">SSE</span>' : ''}</td>
    <td>${statusBadge(e.status, e.error)}</td>
    <td class="num">${h(fmtNum(e.latency_ms))} ms</td>
    <td class="num">${h(fmtNum(e.input_tokens))} / ${h(fmtNum(e.output_tokens))}</td>
    ${full ? `<td class="mono muted">${h(e.path)}</td>` : ''}
    <td class="errc${state.openErr.has(e.id) ? ' open' : ''}" data-id="${h(e.id)}" title="${e.error ? 'Click to expand' : ''}"><span class="t">${h(e.error)}</span>${e.status === 429 && e.kind !== 'routed' ? '<small class="muted">Anthropic rate limit — Claude Code retries automatically.</small>' : ''}</td>
  </tr>`).join('')}</tbody></table></div>`;
}
/* Delegated: expand/collapse error cells. Safe to call more than once on the same root. */
export function bindLogTable(root) {
  if (!root || root._logBound) return;
  root._logBound = true;
  root.addEventListener('click', e => {
    const td = e.target.closest('td.errc'); if (!td || !root.contains(td)) return;
    const id = Number(td.dataset.id);
    if (state.openErr.has(id)) state.openErr.delete(id); else state.openErr.add(id);
    td.classList.toggle('open');
  });
}
