import { $, $$, h } from './lib/dom.js';
import { closeModal } from './components/modal.js';
import { confirmDiscard, unsavedLabel } from './state.js';

/* Tabs + render guard.
   Each view module exports render(el, alive) and optionally tick(alive) and leave().
   alive() turns false as soon as another render starts, so late async work never
   paints over a different tab. */
export const TABS = [['dashboard', 'Dashboard'], ['agents', 'Agents'], ['providers', 'Providers'], ['rules', 'Rules'], ['logs', 'Logs'], ['settings', 'Settings'], ['raw', 'Raw config']];
let views = {}, gen = 0, shownTab = null;

export function curTab() {
  const t = (location.hash || '').replace(/^#\/?/, '');
  return TABS.some(x => x[0] === t) ? t : 'dashboard';
}
/* Snapshot the current render generation. */
export function guard() { const g = gen; return () => g === gen; }
export const currentView = () => views[shownTab];

/* quiet=true keeps the old content until the new one is ready (in-place refresh). */
export async function render(quiet) {
  const tab = curTab();
  const prev = views[shownTab];
  if (prev && prev.leave && shownTab !== tab) prev.leave();
  shownTab = tab;
  $$('#tabs a').forEach(a => {
    const on = a.dataset.tab === tab;
    a.classList.toggle('active', on);
    if (on) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
  });
  const g = ++gen;
  const alive = () => g === gen;
  const v = $('#view');
  if (!quiet) v.innerHTML = '<div class="loading">Loading…</div>';
  closeModal(true);
  try { await views[tab].render(v, alive); }
  catch (e) {
    if (!alive()) return;
    v.innerHTML = `<div class="errbox" role="alert">${h(e.message || e)}</div><p><button class="btn" type="button" id="retry">Retry</button></p>`;
    $('#retry').addEventListener('click', () => render());
  }
}
export const rerender = () => render(true);

export function initRouter(viewMap) {
  views = viewMap;
  $('#tabs').innerHTML = TABS.map(([k, l]) => `<a href="#${k}" data-tab="${k}">${l}</a>`).join('');
  window.addEventListener('hashchange', e => {
    if (curTab() === shownTab) return;
    if (!confirmDiscard()) {
      // Stay: put the old tab back into the URL without another hashchange.
      history.replaceState(null, '', new URL(e.oldURL).hash || '#' + shownTab);
      return;
    }
    render();
  });
  window.addEventListener('beforeunload', e => {
    if (unsavedLabel()) { e.preventDefault(); e.returnValue = ''; }
  });
}
