import { h, nextId } from '../lib/dom.js';

/* Searchable single-select combobox (WAI-ARIA 1.2 combobox + listbox pattern).
   Options: [{ value, label, sub, group, text }]
     label = main line, sub = second line, group = header shown when it changes,
     text  = what the input shows when the option is chosen (defaults to label).
   getOptions() is called on every open, so long lists cost nothing until used.
   Matches are rendered in chunks of CHUNK as the list scrolls / the active row moves.
   On a real choice it calls onChange(value, option) and fires a bubbling "change"
   event on the wrapper. Typing in the input does not count as an edit (data-nodirty). */
const CHUNK = 150;

export function createCombobox({ getOptions, textFor, value = '', onChange, compact, ariaLabel, inputId, placeholder }) {
  const lid = nextId('cbl');
  const wrap = document.createElement('div');
  wrap.className = 'cb' + (compact ? ' compact' : '');
  wrap.innerHTML = `<input class="cb-in" type="text" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="${lid}"
      autocomplete="off" spellcheck="false" data-nodirty${inputId ? ` id="${h(inputId)}"` : ''}${ariaLabel ? ` aria-label="${h(ariaLabel)}"` : ''} placeholder="${h(placeholder || 'Type to search…')}">
    <button class="cb-btn" type="button" tabindex="-1" aria-label="Show options" data-nodirty>▾</button>
    <ul class="cb-list" id="${lid}" role="listbox"${ariaLabel ? ` aria-label="${h(ariaLabel)}"` : ''} hidden></ul>
    <span class="sr" aria-live="polite"></span>`;
  const input = wrap.querySelector('input'), list = wrap.querySelector('ul'), live = wrap.querySelector('.sr'), btn = wrap.querySelector('button');

  let cur = value, opts = [], matches = [], shown = 0, active = -1, isOpen = false, typed = false;

  /* textFor(value) lets the caller name the current value without building every option. */
  const curText = () => {
    if (textFor) return textFor(cur);
    const o = (opts.length ? opts : (opts = getOptions())).find(x => x.value === cur);
    return o ? o.text || o.label : cur;
  };
  const showText = () => { input.value = curText(); input.title = input.value; };

  function filter(q) {
    const words = q.toLowerCase().split(/\s+/).filter(Boolean);
    if (!words.length) return opts;
    return opts.filter(o => {
      const s = o._s || (o._s = [o.label, o.sub, o.group, o.text].join(' ').toLowerCase());
      return words.every(w => s.includes(w));
    });
  }
  function renderMore(upTo) {
    const end = Math.min(matches.length, Math.max(shown + CHUNK, upTo == null ? 0 : upTo + 1));
    if (end <= shown) return;
    const more = list.querySelector('.cb-more'); if (more) more.remove();
    let html = '';
    let prevGroup = shown > 0 ? matches[shown - 1].group : undefined;
    for (let i = shown; i < end; i++) {
      const o = matches[i];
      if (o.group && o.group !== prevGroup) html += `<li class="cb-group" role="presentation">${h(o.group)}</li>`;
      prevGroup = o.group;
      html += `<li role="option" id="${lid}-${i}" data-i="${i}" aria-selected="${o.value === cur}"><b>${h(o.label)}</b>${o.sub ? `<small>${h(o.sub)}</small>` : ''}</li>`;
    }
    if (end < matches.length) html += `<li class="cb-more" role="presentation">${matches.length - end} more · scroll or type to narrow</li>`;
    list.insertAdjacentHTML('beforeend', html);
    shown = end;
  }
  function paint(q) {
    matches = filter(q);
    shown = 0; list.innerHTML = '';
    if (!matches.length) list.innerHTML = '<li class="cb-more" role="presentation">No match</li>';
    else renderMore();
    live.textContent = matches.length + (matches.length === 1 ? ' option' : ' options');
  }
  function setActive(i, scroll) {
    const prev = list.querySelector('li.active'); if (prev) prev.classList.remove('active');
    active = i;
    if (i < 0 || i >= matches.length) { active = -1; input.removeAttribute('aria-activedescendant'); return; }
    if (i >= shown) renderMore(i);
    const li = document.getElementById(`${lid}-${i}`);
    if (li) { li.classList.add('active'); if (scroll !== false) li.scrollIntoView({ block: 'nearest' }); }
    input.setAttribute('aria-activedescendant', `${lid}-${i}`);
  }
  function place() {
    const r = input.getBoundingClientRect();
    const vw = document.documentElement.clientWidth, vh = window.innerHeight;
    const width = Math.min(Math.max(r.width, 280), vw - 16);
    const left = Math.max(8, Math.min(r.left, vw - width - 8));
    const below = vh - r.bottom - 8, above = r.top - 8;
    const up = below < 200 && above > below;
    list.style.left = left + 'px';
    list.style.width = width + 'px';
    list.style.maxHeight = Math.max(120, Math.min(320, up ? above : below)) + 'px';
    if (up) { list.style.top = ''; list.style.bottom = (vh - r.top + 2) + 'px'; }
    else { list.style.bottom = ''; list.style.top = (r.bottom + 2) + 'px'; }
  }
  const onScrollOut = e => { if (e.target !== list && isOpen) place(); };
  function open() {
    if (isOpen || input.disabled) return;
    opts = getOptions();
    typed = false;
    paint('');
    isOpen = true;
    list.hidden = false;
    input.setAttribute('aria-expanded', 'true');
    place();
    const ci = matches.findIndex(o => o.value === cur);
    setActive(ci, true);
    window.addEventListener('scroll', onScrollOut, true);
    window.addEventListener('resize', place);
  }
  function close(restore) {
    if (!isOpen) { if (restore) showText(); return; }
    isOpen = false;
    list.hidden = true;
    input.setAttribute('aria-expanded', 'false');
    input.removeAttribute('aria-activedescendant');
    window.removeEventListener('scroll', onScrollOut, true);
    window.removeEventListener('resize', place);
    if (restore) showText();
  }
  function choose(i) {
    const o = matches[i]; if (!o) return;
    const changed = o.value !== cur;
    cur = o.value;
    close(true);
    if (changed) {
      if (onChange) onChange(cur, o);
      wrap.dispatchEvent(new Event('change', { bubbles: true }));
    }
  }

  input.addEventListener('focus', () => { input.select(); });
  input.addEventListener('click', () => { if (!isOpen) open(); });
  input.addEventListener('input', () => {
    if (!isOpen) open();
    typed = true;
    paint(input.value.trim());
    list.scrollTop = 0;
    setActive(matches.length ? 0 : -1);
  });
  input.addEventListener('keydown', e => {
    const k = e.key;
    if (k === 'ArrowDown' || k === 'ArrowUp') {
      e.preventDefault();
      if (!isOpen) { open(); return; }
      if (e.altKey && k === 'ArrowUp') { choose(active); return; }
      const n = matches.length; if (!n) return;
      setActive(active < 0 ? (k === 'ArrowDown' ? 0 : n - 1) : Math.max(0, Math.min(n - 1, active + (k === 'ArrowDown' ? 1 : -1))));
    } else if (k === 'PageDown' || k === 'PageUp') {
      if (!isOpen) return;
      e.preventDefault();
      setActive(Math.max(0, Math.min(matches.length - 1, (active < 0 ? 0 : active) + (k === 'PageDown' ? 10 : -10))));
    } else if ((k === 'Home' || k === 'End') && isOpen && !typed) {
      e.preventDefault();
      setActive(k === 'Home' ? 0 : matches.length - 1);
    } else if (k === 'Enter') {
      if (isOpen) { e.preventDefault(); if (active >= 0) choose(active); else close(true); }
    } else if (k === 'Escape') {
      if (isOpen) { e.preventDefault(); e.stopPropagation(); close(true); input.select(); }
      else if (input.value !== curText()) { e.preventDefault(); e.stopPropagation(); showText(); input.select(); }
    } else if (k === 'Tab') {
      close(true);
    }
  });
  input.addEventListener('blur', () => { setTimeout(() => { if (!wrap.contains(document.activeElement)) close(true); }, 0); });
  btn.addEventListener('mousedown', e => e.preventDefault());
  btn.addEventListener('click', () => { if (isOpen) close(true); else { input.focus(); open(); } });
  list.addEventListener('mousedown', e => e.preventDefault()); // keep focus in the input
  list.addEventListener('click', e => { const li = e.target.closest('li[role=option]'); if (li) choose(Number(li.dataset.i)); });
  list.addEventListener('mousemove', e => { const li = e.target.closest('li[role=option]'); if (li && Number(li.dataset.i) !== active) setActive(Number(li.dataset.i), false); });
  list.addEventListener('scroll', () => { if (list.scrollTop + list.clientHeight > list.scrollHeight - 80) renderMore(); });

  showText();
  opts = []; // rebuild on next open (provider list may change)
  return {
    el: wrap, input,
    get value() { return cur; },
    set value(v) { cur = v; opts = []; showText(); opts = []; },
    set disabled(b) { input.disabled = !!b; btn.disabled = !!b; if (b) close(true); },
    refresh() { opts = []; showText(); opts = []; }
  };
}
