import { $, $$, h } from '../lib/dom.js';
import { store } from '../lib/storage.js';
import { fmtNum, fmtDur, fmtCompact, fmtMoney, fmtPct, fmtPts, tokSum, dayLabel, niceStep } from '../lib/format.js';
import { copyText } from '../lib/util.js';
import { api } from '../api.js';
import { state, COLORS, loadStatus, loadAgents, fetchLogs } from '../state.js';
import { toast } from '../components/toast.js';
import { logTable, bindLogTable } from '../components/log-table.js';
import { curTab } from '../router.js';

/* Dashboard: subscription savings. Each section repaints only when its data key
   changes, so polling never steals focus or resets scroll. */
const dash = {
  usage: null, usageErr: '', usageAt: 0, usageLoading: null, keys: {},
  period: ['today', '7d', '30d'].includes(store.get('chr_dash_period')) ? store.get('chr_dash_period') : '30d',
  chartMode: store.get('chr_dash_chart') === 'tokens' ? 'tokens' : 'usd',
  customOpen: false, savingPlan: false
};
const PERIOD_LABEL = { today: 'Today', '7d': '7 days', '30d': '30 days' };
async function loadUsage() {
  if (dash.usageLoading) return dash.usageLoading;
  dash.usageLoading = (async () => {
    try { dash.usage = await api('GET', '/api/usage'); dash.usageErr = ''; }
    catch (e) { dash.usageErr = e.message || String(e); }
    finally { dash.usageAt = Date.now(); dash.usageLoading = null; }
  })();
  return dash.usageLoading;
}
function claimWindow(claim) {
  const c = String(claim || '');
  if (c.includes('seven_day')) return '7d';
  if (c.includes('five_hour')) return '5h';
  return c;
}
function limitStatusBadge(st) {
  if (!st) return '';
  if (st === 'allowed') return '<span class="badge ok">✓ Allowed</span>';
  if (st === 'allowed_warning') return '<span class="badge warn">! Near limit</span>';
  if (st === 'rejected') return '<span class="badge err">✕ Rate limited</span>';
  return `<span class="badge">${h(st)}</span>`;
}
function resetText(ts) {
  if (!ts) return '';
  const s = ts - Date.now() / 1000;
  return s > 0 ? 'resets in ' + fmtDur(s) : 'reset due';
}
function sourceLabel(src) {
  const s = String(src || '');
  const i = s.indexOf(':');
  const kind = i > 0 ? s.slice(0, i) : '', name = i > 0 ? s.slice(i + 1) : s;
  if (kind === 'agent') return `<span class="badge routed">agent</span> <b>${h(name)}</b>`;
  if (kind === 'rule') return `<span class="badge">rule</span> <b>#${h(name)}</b>`;
  return `<b>${h(s || 'unknown')}</b>`;
}
const ROUTE_CTA = '<a href="#agents">Route an agent to another provider</a> to start saving.';

export async function render(v, alive) {
  dash.keys = {};
  const usageP = loadUsage();
  await Promise.all([loadStatus(), state.logsLoaded ? null : fetchLogs().catch(() => null), loadAgents().catch(() => null)]);
  if (!alive()) return;
  v.innerHTML = `<div id="dash">
    <div class="dgrid top2">
      <section class="panel hero" id="d-hero" aria-label="Subscription limit"></section>
      <section class="panel" id="d-plan" aria-label="Your plan"></section>
    </div>
    <section id="d-kpi" aria-label="Offload"></section>
    <section class="panel" id="d-chart" aria-label="Last 30 days"></section>
    <section id="d-src" aria-label="Top routed agents and rules"></section>
    <div class="dgrid half">
      <section class="panel" id="d-status" aria-label="Proxy"></section>
      <section class="panel" id="d-agents" aria-label="Routed agents"></section>
    </div>
    <section id="d-logs" aria-label="Recent requests"></section>
    <section id="d-how"></section>
  </div>`;
  bindDash($('#dash'));
  paintDash();
  await usageP;
  if (alive()) paintDash();
}

/* Repaint one section if its key changed. Skips while the user is typing in it; keeps <details data-k> open state. */
function paintSec(id, key, fn) {
  const el = document.getElementById(id);
  if (!el || dash.keys[id] === key) return;
  const ae = document.activeElement;
  if (dash.keys[id] !== undefined && ae && el.contains(ae) && ae.matches('input,textarea,select')) return;
  const open = {};
  $$('details[data-k]', el).forEach(d => (open[d.dataset.k] = d.open));
  el.innerHTML = fn();
  $$('details[data-k]', el).forEach(d => { if (d.dataset.k in open) d.open = open[d.dataset.k]; });
  dash.keys[id] = key;
}
function paintDash() {
  if (!$('#dash')) return;
  const u = dash.usage;
  const err = dash.usageErr;
  if (!u) {
    const msg = err ? `<div class="errbox">Usage report not available: ${h(err)}</div>` : '<div class="loading">Loading usage…</div>';
    paintSec('d-hero', 'x' + err, () => msg);
  } else {
    paintSec('d-hero', JSON.stringify([u.windows, u.limits]), () => heroHTML(u));
  }
  paintSec('d-plan', JSON.stringify([u && u.plan, u && u.plans, u && u.periods, u && u.plan_value_ratio, dash.customOpen, dash.savingPlan, !!u]), () => planHTML(u));
  paintSec('d-kpi', JSON.stringify([u && u.periods, dash.period]), () => kpiHTML(u));
  const cw = Math.round(($('#d-chart') || {}).clientWidth || 0);
  paintSec('d-chart', JSON.stringify([u && u.days, dash.chartMode, cw]), () => chartHTML(u, cw));
  paintSec('d-src', JSON.stringify(u && u.sources), () => sourcesHTML(u));
  const s = state.status || {};
  paintSec('d-status', JSON.stringify([s.launch_cmd, s.proxy_listen, s.panel_listen, s.config_path, s.agents_dir]), () => statusHTML(s));
  liveStatus();
  paintSec('d-agents', JSON.stringify([state.agents.map(a => [a.name, a.color, a.route]), state.providers.map(p => [p.id, p.name])]), routedAgentsHTML);
  const recent = state.logs.slice(-8).reverse();
  paintSec('d-logs', JSON.stringify([state.logsLoaded, recent.map(e => [e.id, e.status])]), () =>
    `<div class="bar dhead"><h3>Recent requests</h3><div class="grow"></div><a href="#logs" class="small">All logs →</a></div>
     <div class="compact">${logTable(recent, false)}</div>`);
  if (dash.keys['d-logs-bound'] !== dash.keys['d-logs']) { bindLogTable($('#d-logs')); dash.keys['d-logs-bound'] = dash.keys['d-logs']; }
  paintSec('d-how', JSON.stringify([u && u.assumptions, u && u.cache_hit_rate]), () => howHTML(u));
}

/* Live text inside the status card (no repaint). */
function liveStatus() {
  const s = state.status; if (!s) return;
  const st = s.stats || {};
  const set = (k, v) => { const el = $(`[data-live="${k}"]`); if (el && el.textContent !== v) el.textContent = v; };
  $$('[data-reset]').forEach(el => { const t = resetText(Number(el.dataset.reset)); if (el.textContent !== t) el.textContent = t; });
  set('uptime', fmtDur(s.uptime_s));
  set('req', fmtNum(st.requests)); set('routed', fmtNum(st.routed)); set('errors', fmtNum(st.errors));
  const e = $('[data-live="errors"]'); if (e) e.classList.toggle('bad', !!st.errors);
}

/* ---- hero: subscription limit ---- */
function heroHTML(u) {
  const wins = (u.windows || []).slice();
  const claim = claimWindow(u.limits && u.limits.claim);
  const status = (u.limits && u.limits.status) || '';
  if (!wins.length) {
    return `<div class="eyebrow">Subscription limit</div>
      <div class="hero-fig"><span class="big muted">—</span></div>
      <p class="hero-sub">No limit data yet. Anthropic reports your usage with each reply, so it shows up after your next Claude Code request through <code>claude-hybrid</code>.</p>`;
  }
  wins.sort((a, b) => (b.name === claim) - (a.name === claim));
  const main = wins[0];
  const anyRouted = (u.periods || []).some(p => p.routed && p.routed.requests);
  let fig, sub;
  if (!main.calibrated) {
    const prog = Math.round(Math.min(1, Math.max(0, Number(main.calibration) || 0)) * 100);
    fig = '<span class="big muted">—</span><span class="unit">pts saved</span>';
    sub = `${h(main.note || 'Not enough data to estimate savings yet.')}
      <span class="calib" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${prog}" aria-label="Calibration">
        <span class="calib-bar"><span style="width:${prog}%"></span></span><span class="small muted">Calibrating · ${prog}%</span></span>`;
  } else {
    fig = `<span class="big">${h(fmtPts(main.saved))}</span><span class="unit">pts saved</span>`;
    sub = main.saved > 0
      ? `of your ${h((main.label || main.name).toLowerCase())}, by routing work to other providers.`
      : anyRouted ? `on your ${h((main.label || main.name).toLowerCase())} so far.` : `Nothing routed yet. ${ROUTE_CTA}`;
  }
  return `<div class="hero-head"><div class="eyebrow">Subscription limit</div><div class="grow"></div>${limitStatusBadge(status)}</div>
    <div class="hero-fig">${fig}</div>
    <p class="hero-sub">${sub}</p>
    <div class="meters">${wins.map(w => meterHTML(w, w.name === claim)).join('')}</div>
    <div class="legend small"><span><i class="sw-sub"></i>Used now</span><span><i class="sw-ghost"></i>Would be used without routing</span></div>`;
}
function meterHTML(w, binding) {
  const used = Math.max(0, Number(w.utilization) || 0);
  const est = w.calibrated ? Math.max(used, Number(w.without) || 0) : used;
  const dom = Math.max(1, est);
  const pu = Math.min(100, used / dom * 100), pe = Math.min(100, est / dom * 100);
  const sev = used >= 1 ? ' crit' : used >= 0.9 ? ' warn' : '';
  const ghostW = pe - pu;
  const label = w.label || w.name;
  const caption = w.calibrated
    ? `<b>${h(fmtPct(used))} used</b>${est > used ? ` · would be ${h(fmtPct(est))} without routing` : ''}`
    : `<b>${h(fmtPct(used))} used</b> · <span class="muted">${w.calibration !== undefined && !w.calibrated && /^Calibrating/.test(w.note || '') ? 'estimate after calibration' : h(w.note || 'No estimate yet')}</span>`;
  const aria = `${label}: ${fmtPct(used)} used` + (w.calibrated && est > used ? `, would be ${fmtPct(est)} without routing` : '');
  return `<div class="mrow">
    <div class="mtop"><span class="mname">${h(label)}</span>${binding ? '<span class="badge info" title="This window decides when you get rate limited">binding</span>' : ''}
      <div class="grow"></div><span class="muted small" data-reset="${h(w.reset_at || '')}">${h(resetText(w.reset_at))}</span></div>
    <div class="meter${sev}" role="img" aria-label="${h(aria)}">
      <div class="m-used" style="width:${pu.toFixed(2)}%"></div>
      ${ghostW > 0.3 ? `<div class="m-ghost" style="left:calc(${pu.toFixed(2)}% + 2px);width:calc(${ghostW.toFixed(2)}% - 2px)"></div>` : ''}
      ${dom > 1 ? `<div class="m-100" style="left:${(100 / dom).toFixed(2)}%" title="100%"></div>` : ''}
    </div>
    <div class="mcap"><span>${caption}</span><div class="grow"></div>${w.calibrated ? `<span class="msave">${h(fmtPts(w.saved))} pts saved</span>` : ''}</div>
  </div>`;
}

/* ---- plan ---- */
function planHTML(u) {
  if (!u) return '<div class="eyebrow">Your plan</div><div class="loading">…</div>';
  const plans = u.plans || [];
  const cur = u.plan ? u.plan.id : '';
  const showCustom = dash.customOpen || cur === 'custom';
  const p30 = (u.periods || []).find(p => p.name === '30d');
  const val = p30 && p30.routed ? p30.routed.cost_usd || 0 : 0;
  const ratio = Number(u.plan_value_ratio) || 0;
  let worth;
  if (!val) worth = `<p class="pworth muted">No routed work in the last 30 days yet. ${ROUTE_CTA}</p>`;
  else {
    const r = ratio >= 10 ? Math.round(ratio) : ratio >= 1 ? ratio.toFixed(1).replace(/\.0$/, '') : ratio.toFixed(2);
    worth = `<p class="pworth">Routed work worth <b>${h(fmtMoney(val))}</b> of API usage in the last 30 days${u.plan && u.plan.price_usd ? ` — <b>${h(r)}×</b> your plan price.` : '.'}</p>`;
  }
  const subVal = p30 && p30.anthropic ? p30.anthropic.cost_usd || 0 : 0;
  if (subVal && u.plan && u.plan.price_usd) {
    const sr = subVal / u.plan.price_usd;
    worth += `<p class="muted small" style="margin:6px 0 0">Work on your subscription in the same 30 days: ${h(fmtMoney(subVal))} of API usage (${h(sr >= 10 ? Math.round(sr) : sr.toFixed(1).replace(/\.0$/, ''))}× plan price).</p>`;
  }
  return `<div class="eyebrow">Your plan</div>
    ${cur ? '' : '<p class="note" style="margin:8px 0">Pick your Claude plan so savings can be shown against it.</p>'}
    <div class="seg plan-seg" role="group" aria-label="Claude plan">${plans.map(p => `<button type="button" data-plan="${h(p.id)}" class="${(p.id === cur && !(dash.customOpen && cur !== 'custom')) || (p.id === 'custom' && showCustom) ? 'on' : ''}" aria-pressed="${p.id === cur}"${dash.savingPlan ? ' disabled' : ''}>${h(p.name)}</button>`).join('')}</div>
    ${showCustom ? `<form class="row pcustom" id="planCustom"><label class="sr" for="planPrice">Monthly price in USD</label>
      <span class="muted">$</span><input id="planPrice" type="number" min="0" step="1" inputmode="decimal" placeholder="e.g. 50" value="${u.plan && u.plan.id === 'custom' && u.plan.price_usd ? h(u.plan.price_usd) : ''}">
      <span class="muted small">/ month</span><button class="btn primary sm" type="submit"${dash.savingPlan ? ' disabled' : ''}>Save</button></form>` : ''}
    ${u.plan ? `<p class="muted small" style="margin:8px 0 0">${h(u.plan.name)}${u.plan.price_usd ? ' · ' + h(fmtMoney(u.plan.price_usd)) + '/month' : ''}${u.plan.multiple ? ' · ' + h(u.plan.multiple) + '× Pro usage' : ''}</p>` : ''}
    ${worth}`;
}
async function savePlan(id, price) {
  if (dash.savingPlan) return;
  dash.savingPlan = true; paintDash();
  try {
    const s = await api('GET', '/api/settings');
    const body = Object.assign({}, s, { plan: id });
    if (id === 'custom') body.plan_price = price;
    await api('PUT', '/api/settings', body);
    dash.customOpen = false;
    toast('Plan saved', 'ok');
    await loadUsage();
  } catch (e) { toast(e.message || String(e), 'err'); }
  finally { dash.savingPlan = false; paintDash(); }
}

/* ---- offload KPIs ---- */
function kpiHTML(u) {
  const seg = `<div class="seg" role="group" aria-label="Period">${['today', '7d', '30d'].map(k => `<button type="button" data-period="${k}" class="${dash.period === k ? 'on' : ''}" aria-pressed="${dash.period === k}">${PERIOD_LABEL[k]}</button>`).join('')}</div>`;
  const head = `<div class="bar dhead"><h3>Offload</h3><span class="muted small">work sent to other providers instead of your subscription</span><div class="grow"></div>${seg}</div>`;
  if (!u) return head + '<div class="loading">…</div>';
  const p = (u.periods || []).find(x => x.name === dash.period) || { anthropic: {}, routed: {}, offload: 0 };
  const a = p.anthropic || {}, r = p.routed || {};
  const at = tokSum(a.tokens), rt = tokSum(r.tokens);
  const reqTot = (a.requests || 0) + (r.requests || 0);
  const tile = (k, v, sub) => `<div class="stat"><div class="k">${k}</div><div class="v">${v}</div><div class="sub">${sub}</div></div>`;
  return head + `<div class="stats">
      ${tile('Share offloaded', h(fmtPct(p.offload, true)), 'of API-equivalent value')}
      ${tile('Routed tokens', h(fmtCompact(rt)), `vs ${h(fmtCompact(at))} on subscription`)}
      ${tile('Requests routed', h(fmtNum(r.requests || 0)), `of ${h(fmtNum(reqTot))} total`)}
      ${tile('Value routed', h(fmtMoney(r.cost_usd)), `vs ${h(fmtMoney(a.cost_usd))} on subscription`)}
    </div>
    <div class="panel split">
      <div class="bar" style="margin-bottom:8px"><b>Subscription vs routed</b><span class="muted small">${h(PERIOD_LABEL[dash.period].toLowerCase())}</span><div class="grow"></div>
        <div class="legend small"><span><i class="sw-sub"></i>Subscription</span><span><i class="sw-routed"></i>Routed</span></div></div>
      ${splitBar('Value', a.cost_usd || 0, r.cost_usd || 0, fmtMoney)}
      ${splitBar('Tokens', at, rt, fmtCompact)}
      ${!r.requests ? `<p class="muted small" style="margin:6px 0 0">Nothing routed in this period. ${ROUTE_CTA}</p>` : ''}
    </div>`;
}
function splitBar(label, a, r, fmt) {
  const t = a + r;
  const pa = t ? a / t * 100 : 0, pr = t ? 100 - pa : 0;
  const aria = t ? `${label}: subscription ${fmt(a)} (${Math.round(pa)}%), routed ${fmt(r)} (${Math.round(pr)}%)` : `${label}: no traffic`;
  return `<div class="sbar-row"><span class="sbar-k">${h(label)}</span>
    <div class="sbar" role="img" aria-label="${h(aria)}">${t ? `${pa > 0 ? `<div class="s-sub" style="flex-basis:${pa}%" title="Subscription ${h(fmt(a))}"></div>` : ''}${pr > 0 ? `<div class="s-routed" style="flex-basis:${pr}%" title="Routed ${h(fmt(r))}"></div>` : ''}` : ''}</div>
    <span class="sbar-v">${t ? `${h(Math.round(pa))}% · <b>${h(pr > 0 && pr < 1 ? pr.toFixed(1) : Math.round(pr))}% routed</b>` : '<span class="muted">no traffic</span>'}</span></div>`;
}

function chartHTML(u, cw) {
  const toggle = `<div class="seg" role="group" aria-label="Measure">${[['usd', 'Value'], ['tokens', 'Tokens']].map(([k, l]) => `<button type="button" data-cmode="${k}" class="${dash.chartMode === k ? 'on' : ''}" aria-pressed="${dash.chartMode === k}">${l}</button>`).join('')}</div>`;
  const head = `<div class="bar" style="margin-bottom:4px"><h3>Last 30 days</h3><div class="grow"></div>${toggle}</div>
    <div class="legend small" style="margin-bottom:6px"><span><i class="sw-sub"></i>Subscription</span><span><i class="sw-routed"></i>Routed</span>
      <span class="muted">${dash.chartMode === 'usd' ? 'API-equivalent value per day' : 'tokens per day, incl. cache'}</span></div>`;
  if (!u) return head + '<div class="loading">…</div>';
  const days = u.days || [];
  const tok = dash.chartMode === 'tokens';
  const fmt = tok ? fmtCompact : fmtMoney;
  const rows = days.map(d => ({ date: d.date, a: Number(tok ? d.anthropic_tokens : d.anthropic_usd) || 0, r: Number(tok ? d.routed_tokens : d.routed_usd) || 0 }));
  const W = Math.max(300, (cw || 700) - 30), H = 210;
  const ml = 46, mr = 6, mt = 10, mb = 24;
  const iw = W - ml - mr, ih = H - mt - mb;
  const peak = Math.max(0, ...rows.map(x => x.a + x.r));
  const stp = niceStep(peak), nt = stp ? Math.ceil(peak / stp) : 0, max = stp * nt;
  const n = rows.length || 30, slot = iw / n;
  const bw = Math.max(2, Math.min(18, slot - 2));
  const y = v => mt + ih - (max ? v / max * ih : 0);
  const axisFmt = v => (tok ? fmtCompact(v) : v === 0 ? '$0' : '$' + (v < 1 ? (+v.toFixed(3)).toString() : v < 10 ? (+v.toFixed(1)).toString() : fmtCompact(v)));
  let g = '';
  if (max) for (let i = 0; i <= nt; i++) {
    const v = stp * i, yy = y(v).toFixed(1);
    g += `<line class="${i ? 'grid' : 'base'}" x1="${ml}" x2="${W - mr}" y1="${yy}" y2="${yy}"/><text class="tick" x="${ml - 6}" y="${yy}" dy="0.32em" text-anchor="end">${h(axisFmt(v))}</text>`;
  } else g += `<line class="base" x1="${ml}" x2="${W - mr}" y1="${mt + ih}" y2="${mt + ih}"/>`;
  const topPath = (x, yt, w, hh) => {
    const r = Math.min(4, w / 2, hh);
    return `M${x},${yt + hh}V${yt + r}Q${x},${yt} ${x + r},${yt}H${x + w - r}Q${x + w},${yt} ${x + w},${yt + r}V${yt + hh}Z`;
  };
  let bars = '', hits = '', labels = '';
  const step = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(iw / 60))));
  rows.forEach((d, i) => {
    const x = ml + i * slot + (slot - bw) / 2;
    const base = mt + ih;
    if (max && d.a + d.r > 0) {
      const ha = d.a / max * ih, hr = d.r / max * ih;
      if (ha > 0) bars += hr > 0 ? `<rect class="b-sub" x="${x.toFixed(1)}" y="${(base - ha).toFixed(1)}" width="${bw.toFixed(1)}" height="${ha.toFixed(1)}"/>`
        : `<path class="b-sub" d="${topPath(x, base - Math.max(ha, 1), bw, Math.max(ha, 1))}"/>`;
      if (hr > 0) {
        const gap = ha > 0 ? 2 : 0;
        const hh = Math.max(1, hr - gap);
        bars += `<path class="b-routed" d="${topPath(x, base - ha - gap - hh, bw, hh)}"/>`;
      }
    }
    hits += `<rect class="hit" data-i="${i}" x="${(ml + i * slot).toFixed(1)}" y="${mt}" width="${slot.toFixed(1)}" height="${ih}" aria-label="${h(dayLabel(d.date, true))}: subscription ${h(fmt(d.a))}, routed ${h(fmt(d.r))}"/>`;
    if ((n - 1 - i) % step === 0) labels += `<text class="tick" x="${(i === n - 1 ? W - mr : ml + i * slot + slot / 2).toFixed(1)}" y="${H - 6}" text-anchor="${i === n - 1 ? 'end' : 'middle'}">${h(i === n - 1 ? 'Today' : dayLabel(d.date))}</text>`;
  });
  const empty = !max ? `<text class="emptyt" x="${ml + iw / 2}" y="${mt + ih / 2}" text-anchor="middle">No traffic in the last 30 days</text>` : '';
  const totA = rows.reduce((s, x) => s + x.a, 0), totR = rows.reduce((s, x) => s + x.r, 0);
  const desc = `Stacked daily bars for the last 30 days. Subscription total ${fmt(totA)}, routed total ${fmt(totR)}.`;
  dash.chartRows = { rows, fmt };
  return head + `<div class="chartwrap">
    <svg class="chart" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" role="img" aria-labelledby="chT chD">
      <title id="chT">Subscription vs routed, last 30 days</title><desc id="chD">${h(desc)}</desc>
      ${g}${bars}${labels}${empty}<g class="hits">${hits}</g></svg>
    <div class="tip" id="chTip" hidden></div></div>
    <details class="adv" data-k="chtable" style="margin:6px 0 0"><summary>Show as table</summary>
      <div class="tablewrap" style="max-height:260px;overflow:auto"><table><thead><tr><th>Day</th><th class="num">Subscription</th><th class="num">Routed</th><th class="num">Routed share</th></tr></thead><tbody>
      ${rows.slice().reverse().map(d => `<tr><td>${h(dayLabel(d.date, true))}</td><td class="num">${h(fmt(d.a))}</td><td class="num">${h(fmt(d.r))}</td><td class="num">${d.a + d.r ? h(Math.round(d.r / (d.a + d.r) * 100)) + '%' : '–'}</td></tr>`).join('')}
      </tbody></table></div></details>`;
}
function chartHover(e) {
  const tip = $('#chTip'); if (!tip || !dash.chartRows) return;
  const hit = e.target.closest && e.target.closest('rect.hit');
  $$('#d-chart rect.hit.on').forEach(r => r !== hit && r.classList.remove('on'));
  if (!hit) { tip.hidden = true; return; }
  hit.classList.add('on');
  const d = dash.chartRows.rows[Number(hit.dataset.i)], fmt = dash.chartRows.fmt;
  if (!d) return;
  const t = d.a + d.r;
  tip.innerHTML = `<b>${h(dayLabel(d.date, true))}</b>
    <div><i class="sw-sub"></i>Subscription <span>${h(fmt(d.a))}</span></div>
    <div><i class="sw-routed"></i>Routed <span>${h(fmt(d.r))}</span></div>
    ${t ? `<div class="muted">${h(Math.round(d.r / t * 100))}% routed</div>` : ''}`;
  tip.hidden = false;
  const wrap = tip.parentElement.getBoundingClientRect(), hr = hit.getBoundingClientRect();
  const tw = tip.offsetWidth;
  let left = hr.left - wrap.left + hr.width / 2 - tw / 2;
  left = Math.max(0, Math.min(wrap.width - tw, left));
  tip.style.left = left + 'px';
  tip.style.top = '4px';
}

/* ---- top sources ---- */
function sourcesHTML(u) {
  const head = '<div class="bar dhead"><h3>Top routed agents and rules</h3><span class="muted small">last 30 days</span></div>';
  if (!u) return head + '<div class="loading">…</div>';
  const src = (u.sources || []).slice(0, 10);
  if (!src.length) return head + `<div class="empty">Nothing routed in the last 30 days. ${ROUTE_CTA}</div>`;
  const tot = src.reduce((s, x) => s + (x.cost_usd || 0), 0) || 1;
  return head + `<div class="tablewrap"><table class="srct"><thead><tr><th>Source</th><th>Runs on</th><th class="num">Requests</th><th class="num">Tokens</th><th class="num">Value</th><th>Share of value</th></tr></thead><tbody>
    ${src.map(x => {
      const sh = (x.cost_usd || 0) / tot * 100;
      return `<tr><td>${sourceLabel(x.source)}</td><td class="mono">${h(x.target)}</td><td class="num">${h(fmtNum(x.requests))}</td><td class="num">${h(fmtCompact(x.tokens))}</td><td class="num">${h(fmtMoney(x.cost_usd))}</td>
        <td><div class="share"><div class="sharebar"><i style="width:${sh.toFixed(1)}%"></i></div><span>${h(Math.round(sh))}%</span></div></td></tr>`;
    }).join('')}</tbody></table></div>`;
}

/* ---- status + agents ---- */
function statusHTML(s) {
  const cmd = s.launch_cmd || 'claude-hybrid';
  return `<div class="bar dhead"><h3>Proxy</h3><span class="badge ok">● Running</span><div class="grow"></div><span class="muted small">up <span data-live="uptime"></span></span></div>
    <div class="mini"><div><span data-live="req">0</span><small>requests</small></div><div><span data-live="routed">0</span><small>routed</small></div><div><span data-live="errors">0</span><small>errors</small></div></div>
    <p class="muted small" style="margin:0 0 8px">Since the proxy started.</p>
    <div class="kv"><div>Proxy</div><div><code>${h(s.proxy_listen)}</code></div><div>Panel</div><div><code>${h(s.panel_listen)}</code></div></div>
    <details class="adv" data-k="howstart" style="margin:10px 0 0"><summary>How to start</summary>
      <div class="cmd"><code>${h(cmd)}</code><button class="btn sm" type="button" data-copy>Copy</button></div>
      <p class="hint" style="margin:0 0 8px">Run <code>${h(cmd)}</code> instead of <code>claude</code>. It starts Claude Code with <code>ANTHROPIC_BASE_URL</code> pointing at this proxy.
        Normal requests still use your subscription. Routed agents and rules go to your providers.</p>
      <div class="kv small"><div>Config</div><div><code>${h(s.config_path)}</code></div><div>Agents dir</div><div><code>${h(s.agents_dir)}</code></div></div>
    </details>`;
}
function routedAgentsHTML() {
  const head = '<div class="bar dhead"><h3>Routed agents</h3><div class="grow"></div><a href="#agents" class="small">Agents →</a></div>';
  const ra = state.agents.filter(a => !a.missing && a.route && a.route.provider).sort((a, b) => a.name.localeCompare(b.name));
  if (!ra.length) return head + `<div class="empty">All agents use your subscription. <a href="#agents">Pick where agents run →</a></div>`;
  return head + `<ul class="alist">${ra.map(a => {
    const p = state.providers.find(x => x.id === a.route.provider);
    return `<li><span class="cdot" style="background:${COLORS[a.color] || 'transparent'}"></span><b>${h(a.name)}</b><span class="grow"></span>
      <span class="muted small">${h(p ? p.name || p.id : a.route.provider)} / <span class="mono">${h(a.route.model || 'same model')}</span></span></li>`;
  }).join('')}</ul>`;
}
function howHTML(u) {
  if (!u) return '';
  const as = u.assumptions || [];
  return `<details class="adv how" data-k="how"><summary>How is this calculated?</summary>
    <ul>${as.map(x => `<li>${h(x)}</li>`).join('')}
    ${u.cache_hit_rate != null ? `<li>Cache-hit rate on your subscription: ${h(fmtPct(u.cache_hit_rate))}.</li>` : ''}
    <li>“pts saved” = estimated limit use without routing minus current use, in percentage points.</li></ul></details>`;
}

/* Delegated handlers, bound once per dashboard mount. */
function bindDash(root) {
  root.addEventListener('click', e => {
    const b = e.target.closest('button');
    if (!b || b.disabled) return;
    if (b.dataset.plan) {
      const id = b.dataset.plan;
      if (id === 'custom') {
        dash.customOpen = true; paintDash();
        const i = $('#planPrice'); if (i) i.focus();
        return;
      }
      dash.customOpen = false;
      if (!dash.usage || !dash.usage.plan || dash.usage.plan.id !== id) savePlan(id); else paintDash();
    } else if (b.dataset.period) {
      dash.period = b.dataset.period; store.set('chr_dash_period', dash.period); paintDash();
    } else if (b.dataset.cmode) {
      dash.chartMode = b.dataset.cmode; store.set('chr_dash_chart', dash.chartMode); paintDash();
    } else if ('copy' in b.dataset) {
      copyText((state.status && state.status.launch_cmd) || 'claude-hybrid');
    }
  });
  root.addEventListener('submit', e => {
    if (e.target.id !== 'planCustom') return;
    e.preventDefault();
    const v = parseFloat($('#planPrice').value);
    if (!(v > 0)) { toast('Enter your monthly price in USD', 'err'); return; }
    savePlan('custom', v);
  });
  root.addEventListener('mousemove', chartHover);
  root.addEventListener('mouseleave', () => { const t = $('#chTip'); if (t) t.hidden = true; }, true);
  root.addEventListener('pointerleave', e => { if (e.target.matches && e.target.matches('svg.chart')) { const t = $('#chTip'); if (t) t.hidden = true; $$('#d-chart rect.hit.on').forEach(r => r.classList.remove('on')); } }, true);
}
let dashResizeT = null;
window.addEventListener('resize', () => { clearTimeout(dashResizeT); dashResizeT = setTimeout(() => { if (curTab() === 'dashboard') paintDash(); }, 150); });

/* Poll: new logs every 2s, status every 6s (or on new logs), usage every 30s. */
let tickN = 0;
export async function tick(alive) {
  const fresh = await fetchLogs();
  if (!alive()) return;
  if (++tickN % 3 === 0 || fresh) {
    const before = state.lastLogId;
    await loadStatus();
    if (state.lastLogId < before) await fetchLogs();
  }
  if (Date.now() - dash.usageAt > 30000) await loadUsage();
  if (alive()) paintDash();
}
