import { $, $$, h } from '../lib/dom.js';
import { state, ROUTE_DEFAULT, providerType } from '../state.js';
import { createCombobox } from './combobox.js';

/* One combobox value encodes provider + model. */
export const rv = (p, m) => JSON.stringify([p || '', p ? m || '' : '']);
export function parseRv(v) {
  try { const a = JSON.parse(v); return { provider: a[0] || '', model: a[0] ? a[1] || '' : '' }; }
  catch (e) { return { provider: '', model: '' }; }
}
const SUB = 'Anthropic — your Claude subscription';
const provName = id => { const p = state.providers.find(x => x.id === id); return p ? p.name || p.id : id; };

/* Short text for a route: "OpenCode Go · glm-4.6". */
export function routeText(r) {
  if (!r || !r.provider) return SUB;
  return `${provName(r.provider)} · ${r.model || 'same model as request'}`;
}

/* Options for "Runs on": the subscription, then every enabled provider and its models.
   The current choice is always present, even if its provider is disabled or gone. */
export function routeOptions(cur) {
  const c = cur || {};
  const out = [{ value: rv('', ''), label: SUB, sub: 'Normal Claude Code. Nothing changes.', text: SUB }];
  const groups = state.providers.filter(p => p.enabled || p.id === c.provider)
    .map(p => ({ id: p.id, name: (p.name || p.id) + (p.enabled ? '' : ' (disabled)'), models: p.models || [] }));
  if (c.provider && !groups.some(g => g.id === c.provider)) groups.push({ id: c.provider, name: c.provider + ' (missing provider)', models: [] });
  for (const g of groups) {
    const models = g.models.slice();
    if (g.id === c.provider && c.model && !models.includes(c.model)) models.push(c.model);
    out.push({ value: rv(g.id, ''), group: g.name, label: 'Same model as request', sub: g.name, text: `${g.name} · same model as request` });
    for (const m of models) {
      out.push({ value: rv(g.id, m), group: g.name, label: m, sub: g.name + (g.models.includes(m) ? '' : ' · not in list'), text: `${g.name} · ${m}` });
    }
  }
  return out;
}

/* Combobox for picking where something runs. */
export function routeCombobox({ route, onChange, compact, ariaLabel, inputId }) {
  const r = route || {};
  const cb = createCombobox({
    value: rv(r.provider, r.model),
    getOptions: () => routeOptions(Object.assign({}, r, parseRv(cb.value))),
    textFor: v => routeText(parseRv(v)),
    onChange, compact, ariaLabel: ariaLabel || 'Runs on', inputId,
    placeholder: 'Search provider or model…'
  });
  return cb;
}

let n = 0;
/* Full route editor: combobox + advanced settings. Returns { get() }. */
export function mountRoute(el, route) {
  const r = Object.assign({}, ROUTE_DEFAULT, route || {});
  const advOpen = r.max_tokens || r.temperature != null || r.reasoning_effort || (r.system_mode && r.system_mode !== 'keep');
  const id = 'rt-' + (++n);
  el.innerHTML = `<div class="route">
    <div class="f"><span><label for="${id}">Runs on</label></span><div class="rt-cb"></div><small class="rt-provhint"></small></div>
    <details class="rt-on"${advOpen ? ' open' : ''}><summary>Advanced</summary>
      <div class="grid3">
        <label class="f"><span>Max tokens cap</span><input class="rt-max" type="number" min="0" step="1" value="${r.max_tokens ? h(r.max_tokens) : ''}" placeholder="no cap"></label>
        <label class="f"><span>Temperature</span><input class="rt-temp" type="number" min="0" max="2" step="0.05" value="${r.temperature == null ? '' : h(r.temperature)}" placeholder="keep"></label>
        <label class="f"><span>Reasoning effort</span><select class="rt-eff">
          ${['', 'low', 'medium', 'high'].map(x => `<option value="${x}"${x === r.reasoning_effort ? ' selected' : ''}>${x || 'default'}</option>`).join('')}
          ${r.reasoning_effort && !['low', 'medium', 'high'].includes(r.reasoning_effort) ? `<option selected value="${h(r.reasoning_effort)}">${h(r.reasoning_effort)}</option>` : ''}
        </select></label>
      </div>
      <label class="f"><span>System prompt</span><select class="rt-mode">
        <option value="keep"${r.system_mode === 'replace' || r.system_mode === 'append' ? '' : ' selected'}>Keep original</option>
        <option value="replace"${r.system_mode === 'replace' ? ' selected' : ''}>Replace with text below</option>
        <option value="append"${r.system_mode === 'append' ? ' selected' : ''}>Append text below</option>
      </select></label>
      <label class="f rt-sysw"><span class="sr">System prompt text</span><textarea class="rt-sys mono" rows="5" placeholder="System prompt text">${h(r.system_prompt)}</textarea></label>
    </details></div>`;
  const mode = $('.rt-mode', el);
  const cb = routeCombobox({ route: r, inputId: id, onChange: () => sync() });
  $('.rt-cb', el).appendChild(cb.el);
  const sync = () => {
    const c = parseRv(cb.value);
    const p = state.providers.find(x => x.id === c.provider);
    $$('.rt-on', el).forEach(x => (x.style.display = c.provider ? '' : 'none'));
    $('.rt-provhint', el).textContent = c.provider
      ? (p ? `${p.name || p.id} · ${providerType(p.type).label}${c.model ? '' : ' · uses the model Claude Code asks for'}` : 'Provider not found')
      : 'Normal Claude Code. Nothing changes.';
    $('.rt-sysw', el).style.display = mode.value === 'keep' ? 'none' : '';
  };
  mode.addEventListener('change', sync);
  sync();
  return {
    get() {
      const c = parseRv(cb.value);
      if (!c.provider) return Object.assign({}, ROUTE_DEFAULT);
      const t = $('.rt-temp', el).value.trim();
      const tv = t === '' ? null : parseFloat(t);
      return {
        provider: c.provider,
        model: c.model,
        max_tokens: Math.max(0, parseInt($('.rt-max', el).value, 10) || 0),
        temperature: tv == null || isNaN(tv) ? null : tv,
        reasoning_effort: $('.rt-eff', el).value,
        system_mode: mode.value,
        system_prompt: $('.rt-sys', el).value
      };
    }
  };
}
