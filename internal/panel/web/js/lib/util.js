import { toast } from '../components/toast.js';

export const enc = encodeURIComponent;
export const clone = o => JSON.parse(JSON.stringify(o == null ? null : o));
export const sanitizeName = s => String(s || '').toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9_.-]/g, '');
export const splitTools = s => String(s || '').split(',').map(x => x.trim()).filter(Boolean);

/* Run an async action, disable the button meanwhile, toast errors. */
export async function run(fn, btn) {
  if (btn) btn.disabled = true;
  try { return await fn(); }
  catch (e) { toast(e.message || String(e), 'err'); }
  finally { if (btn) btn.disabled = false; }
}

export async function copyText(text) {
  try { await navigator.clipboard.writeText(text); }
  catch (e) {
    const ta = document.createElement('textarea');
    ta.value = text; document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); } catch (e2) { /* ignore */ }
    ta.remove();
  }
  toast('Copied', 'ok');
}
