import { store } from './lib/storage.js';

/* Move ?token=… from the URL into storage. */
(function takeUrlToken() {
  try {
    const u = new URL(location.href);
    const t = u.searchParams.get('token');
    if (t) {
      store.set('chr_token', t);
      u.searchParams.delete('token');
      history.replaceState(null, '', u.pathname + u.search + u.hash);
    }
  } catch (e) { /* ignore */ }
})();

export function setOnline(on) {
  const d = document.getElementById('online');
  if (!d) return;
  d.className = 'dot ' + (on ? 'on' : 'off');
  d.title = on ? 'Connected' : 'Not reachable';
}

let tokenPromise = null;
export const tokenAsked = () => !!tokenPromise;

function askToken() {
  if (tokenPromise) return tokenPromise;
  tokenPromise = new Promise(resolve => {
    const dlg = document.createElement('dialog');
    dlg.className = 'modal tokdlg';
    dlg.setAttribute('aria-labelledby', 'tok-title');
    dlg.innerHTML = `<form method="dialog">
      <h3 id="tok-title" style="margin-bottom:6px">Panel token required</h3>
      <p class="hint">This panel is protected. Enter the panel token from your config.</p>
      <label class="sr" for="tok-in">Token</label>
      <input type="password" id="tok-in" autocomplete="current-password" placeholder="Token" required>
      <div class="row" style="margin-top:12px;justify-content:flex-end"><button class="btn primary" type="submit">Continue</button></div>
    </form>`;
    document.body.appendChild(dlg);
    dlg.addEventListener('cancel', e => e.preventDefault()); // the token is required
    dlg.querySelector('form').addEventListener('submit', e => {
      e.preventDefault();
      const v = dlg.querySelector('#tok-in').value.trim();
      if (!v) return;
      store.set('chr_token', v);
      dlg.close(); dlg.remove();
      tokenPromise = null;
      resolve();
    });
    dlg.showModal();
  });
  return tokenPromise;
}

export async function api(method, path, body) {
  const headers = { 'X-CHR': '1' };
  const tok = store.get('chr_token');
  if (tok) headers['X-CHR-Token'] = tok;
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  let res;
  try {
    res = await fetch(path, { method, headers, body: body !== undefined ? JSON.stringify(body) : undefined, cache: 'no-store' });
  } catch (e) {
    setOnline(false);
    throw new Error('Cannot reach claude-hybrid (' + e.message + ')');
  }
  setOnline(true);
  if (res.status === 401) { await askToken(); return api(method, path, body); }
  const txt = await res.text();
  let data = null;
  if (txt) { try { data = JSON.parse(txt); } catch (e) { data = res.ok ? txt : { error: txt.slice(0, 300) }; } }
  if (!res.ok) throw new Error((data && data.error) || ('HTTP ' + res.status));
  return data;
}
