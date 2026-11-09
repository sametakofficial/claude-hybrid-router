import { $, h } from '../lib/dom.js';

const LIMIT = 400; // rows rendered before "Show all"

/* Model checklist for the provider editor.
   onPick(list) runs after every change of the checked set; onEdit() when the user changed it. */
export function mountModelChecklist(root, { models, onPick, onEdit }) {
  let all = (models || []).slice();
  const picked = new Set(all), manual = new Set();
  let live = null, touched = false, showAll = false;
  root.innerHTML = `<div class="fetchbox">
      <div class="row" data-nodirty><label class="sr" for="mc-q">Search models</label><input class="mc-q" id="mc-q" type="search" placeholder="Search models" spellcheck="false" autocomplete="off">
        <button class="btn sm mc-all" type="button">Select all</button><button class="btn sm mc-none" type="button">None</button></div>
      <div class="mlist" role="group" aria-label="Models"></div>
      <div class="small muted mc-count" aria-live="polite"></div>
    </div>
    <details class="adv mc-manual"><summary>Add model manually</summary>
      <div class="row" data-nodirty><label class="sr" for="mc-add">Model id</label><input class="mc-add mono" id="mc-add" placeholder="model id" spellcheck="false"><button class="btn mc-addb" type="button">Add</button></div>
      <small class="muted">Only if the list above does not load.</small>
    </details>`;
  const q = $('.mc-q', root), box = $('.mlist', root);
  const selected = () => all.filter(x => picked.has(x));
  const matching = () => { const qq = q.value.trim().toLowerCase(); return all.filter(x => !qq || x.toLowerCase().includes(qq)); };
  const changed = user => { if (user) { touched = true; if (onEdit) onEdit(); } paint(); if (onPick) onPick(selected()); };
  function paint() {
    $('.fetchbox', root).style.display = all.length ? '' : 'none';
    const list = matching();
    const rows = showAll ? list : list.slice(0, LIMIT);
    box.innerHTML = rows.map(x =>
      `<label><input type="checkbox" value="${h(x)}"${picked.has(x) ? ' checked' : ''}>${h(x)}${manual.has(x) ? ' <span class="tag">(manual)</span>' : live && !live.has(x) ? ' <span class="tag">(not in live list)</span>' : ''}</label>`).join('')
      + (rows.length < list.length ? `<div class="more"><button class="btn sm mc-more" type="button">Show all ${list.length}</button></div>` : '')
      || '<span class="muted small">No match.</span>';
    $('.mc-count', root).textContent = `${selected().length} of ${all.length} selected`;
  }
  box.addEventListener('change', e => {
    const c = e.target; if (!c.matches('input[type=checkbox]')) return;
    if (c.checked) picked.add(c.value); else picked.delete(c.value);
    changed(true);
  });
  box.addEventListener('click', e => { if (e.target.closest('.mc-more')) { showAll = true; paint(); } });
  q.addEventListener('input', () => { showAll = false; paint(); });
  $('.mc-all', root).addEventListener('click', () => { matching().forEach(x => picked.add(x)); changed(true); });
  $('.mc-none', root).addEventListener('click', () => { matching().forEach(x => picked.delete(x)); changed(true); });
  const addManual = () => {
    const inp = $('.mc-add', root);
    const ids = inp.value.split(/[\s,]+/).map(s => s.trim()).filter(Boolean);
    if (!ids.length) return;
    ids.forEach(x => { if (!all.includes(x)) { all.push(x); manual.add(x); } picked.add(x); });
    inp.value = '';
    changed(true);
  };
  $('.mc-addb', root).addEventListener('click', addManual);
  $('.mc-add', root).addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addManual(); } });
  paint();

  return {
    all: () => all.slice(),
    selected,
    openManual() { $('.mc-manual', root).open = true; },
    /* Merge a live model list. For a new provider that the user has not touched, check everything found. */
    setFound(found, { isNew, saved }) {
      const keep = all.filter(x => manual.has(x) || (!isNew && picked.has(x) && !found.includes(x)));
      if (isNew && !touched) { picked.clear(); found.forEach(x => picked.add(x)); manual.forEach(x => picked.add(x)); }
      all = found.concat(keep.filter(x => !found.includes(x)));
      live = new Set(found);
      for (const x of Array.from(picked)) if (!all.includes(x)) picked.delete(x);
      changed(false);
      return isNew ? 0 : found.filter(x => !(saved || []).includes(x)).length;
    }
  };
}
