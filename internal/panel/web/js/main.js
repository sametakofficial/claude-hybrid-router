import { $ } from './lib/dom.js';
import { run } from './lib/util.js';
import { api } from './api.js';
import { state, loadStatus, loadMeta, loadProviders, loadAgents, confirmDiscard } from './state.js';
import { toast } from './components/toast.js';
import { initTheme } from './theme.js';
import { initRouter, render } from './router.js';
import { startPoller } from './poller.js';
import * as dashboard from './views/dashboard.js';
import * as agents from './views/agents.js';
import * as providers from './views/providers.js';
import * as rules from './views/rules.js';
import * as logs from './views/logs.js';
import * as settings from './views/settings.js';
import * as raw from './views/raw.js';

initTheme($('#themeBtn'));
initRouter({ dashboard, agents, providers, rules, logs, settings, raw });

$('#reloadBtn').addEventListener('click', e => run(async () => {
  if (!confirmDiscard()) return;
  await api('POST', '/api/reload');
  state.meta = null;
  toast('Reloaded config and agents', 'ok');
  render();
}, e.currentTarget));

startPoller();
loadStatus().catch(() => {});
Promise.all([loadMeta(), loadProviders(), loadAgents()]).catch(() => {}).finally(() => render());
