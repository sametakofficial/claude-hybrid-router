import { api } from './api.js';

export const state = {
  status: null, providers: [], agents: [], rules: [], presets: null, meta: null,
  logs: [], lastLogId: 0, logsLoaded: false, paused: false, logFilter: 'all', openErr: new Set()
};

export const ROUTE_DEFAULT = { provider: '', model: '', max_tokens: 0, temperature: null, reasoning_effort: '', system_mode: 'keep', system_prompt: '' };
export const COLORS = { red: '#e5484d', blue: '#3e7bfa', green: '#30a46c', yellow: '#e2b308', purple: '#8e4ec6', orange: '#f76b15', pink: '#d6409f', cyan: '#05a2c2' };
const PROVIDER_TYPES = [
  { name: 'openai', label: 'OpenAI-compatible', help: 'Most services (OpenAI, OpenRouter, Ollama, OpenCode…). Requests are translated.' },
  { name: 'anthropic', label: 'Anthropic-compatible', help: 'Speaks the Claude API. Requests are sent as they are.' }
];

/* /api/meta is the only source of the pick lists. If it fails, callers show an error. */
export async function loadMeta() {
  if (state.meta) return state.meta;
  let m;
  try { m = await api('GET', '/api/meta'); }
  catch (e) { throw new Error('Could not load the panel choices (/api/meta): ' + (e.message || e)); }
  if (!m || typeof m !== 'object') throw new Error('Could not load the panel choices (/api/meta): empty reply');
  const arr = k => (Array.isArray(m[k]) ? m[k] : []);
  return (state.meta = {
    tools: arr('tools'), claude_models: arr('claude_models'), colors: arr('colors'),
    rule_patterns: arr('rule_patterns'), agent_templates: arr('agent_templates'),
    provider_types: arr('provider_types').length ? arr('provider_types') : PROVIDER_TYPES
  });
}
export const providerType = (name) => {
  const list = (state.meta && state.meta.provider_types) || PROVIDER_TYPES;
  return list.find(t => t.name === name) || { name, label: name || 'OpenAI-compatible', help: '' };
};

export const loadProviders = async () => (state.providers = (await api('GET', '/api/providers')) || []);
export const loadAgents = async () => (state.agents = (await api('GET', '/api/agents')) || []);
export const loadRules = async () => (state.rules = (await api('GET', '/api/rules')) || []);
export const loadStatus = async () => {
  const prev = state.status;
  state.status = await api('GET', '/api/status');
  // Server restarted: log ids start over, so reset the cursor and cached logs.
  if (prev && state.status && Number(state.status.uptime_s) < Number(prev.uptime_s)) {
    state.lastLogId = 0; state.logs = []; state.openErr.clear(); state.logsLoaded = false;
  }
  const ver = document.getElementById('ver');
  if (state.status && ver) ver.textContent = state.status.version ? 'v' + String(state.status.version).replace(/^v/, '') : '';
  return state.status;
};
export async function fetchLogs() {
  const r = await api('GET', '/api/logs?since=' + state.lastLogId);
  state.logsLoaded = true;
  if (Array.isArray(r) && r.length) {
    for (const e of r) if (e.id > state.lastLogId) state.logs.push(e);
    state.lastLogId = Math.max(state.lastLogId, ...r.map(e => e.id || 0));
    if (state.logs.length > 500) state.logs.splice(0, state.logs.length - 500);
    return true;
  }
  return false;
}

/* ---- unsaved changes ----
   Each editor registers a check that returns a short label when it has unsaved changes. */
const dirtyChecks = new Set();
export function addDirtyCheck(fn) { dirtyChecks.add(fn); return () => dirtyChecks.delete(fn); }
export function unsavedLabel() {
  for (const fn of dirtyChecks) { const l = fn(); if (l) return l; }
  return '';
}
/* Ask before losing changes. true = go ahead. */
export function confirmDiscard() {
  const l = unsavedLabel();
  return !l || confirm(`You have unsaved changes in ${l}. Discard them?`);
}
