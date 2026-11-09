import { tokenAsked } from './api.js';
import { currentView, guard } from './router.js';

/* Every 2s, let the visible view refresh itself (dashboard, logs). */
export function startPoller() {
  let busy = false;
  setInterval(async () => {
    if (document.hidden || busy || tokenAsked()) return;
    const v = currentView();
    if (!v || !v.tick) return;
    busy = true;
    try { await v.tick(guard()); }
    catch (e) { /* the connection dot shows it */ }
    finally { busy = false; }
  }, 2000);
}
