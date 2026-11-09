import { h, nextId } from '../lib/dom.js';
import { addDirtyCheck } from '../state.js';

/* One modal at a time, built on native <dialog>.
   - showModal() gives focus trap, inert background and top layer.
   - Focus goes back to the element that opened the first modal of a chain.
   - Esc, ✕ and Cancel ask before discarding unsaved changes.
   Any input/change inside the dialog marks it dirty, except inside [data-nodirty]. */
let cur = null;

export function openModal({ title, body, foot, wide, label }) {
  const opener = cur ? cur.opener : document.activeElement;
  if (cur) cur.close(true);
  const id = nextId('mt');
  const dlg = document.createElement('dialog');
  dlg.className = 'modal' + (wide ? ' wide' : '');
  dlg.setAttribute('aria-labelledby', id);
  dlg.innerHTML = `<div class="mhead"><h3 id="${id}">${h(title)}</h3><button class="btn icon ghost" type="button" data-close aria-label="Close">✕</button></div>
    <div class="mbody">${body}</div>
    <div class="mfoot">${foot || '<div class="grow"></div><button class="btn" type="button" data-close>Close</button>'}</div>`;
  document.getElementById('modal-root').appendChild(dlg);

  let dirty = false, closed = false;
  const m = {
    el: dlg, opener,
    get dirty() { return dirty; },
    markDirty() { dirty = true; },
    clean() { dirty = false; },
    /* force=true skips the unsaved-changes question. Returns true when closed. */
    close(force) {
      if (closed) return true;
      if (!force && dirty && !confirm('Discard your unsaved changes?')) return false;
      closed = true;
      unreg();
      if (dlg.open) dlg.close();
      dlg.remove();
      if (cur === m) {
        cur = null;
        if (opener && opener.isConnected && typeof opener.focus === 'function') opener.focus();
      }
      return true;
    }
  };
  const unreg = addDirtyCheck(() => (dirty && !closed ? label || 'the open editor' : ''));
  const onEdit = e => { if (!e.target.closest('[data-nodirty]')) dirty = true; };
  dlg.addEventListener('input', onEdit);
  dlg.addEventListener('change', onEdit);
  dlg.addEventListener('click', e => { if (e.target.closest('[data-close]')) m.close(); });
  dlg.addEventListener('keydown', e => {
    if (e.key === 'Escape' && !e.defaultPrevented) { e.preventDefault(); m.close(); }
  });
  dlg.addEventListener('cancel', e => { e.preventDefault(); m.close(); });
  dlg.addEventListener('close', () => { if (!closed) m.close(true); });
  cur = m;
  dlg.showModal();
  return m;
}

export function closeModal(force) { return cur ? cur.close(force) : true; }
