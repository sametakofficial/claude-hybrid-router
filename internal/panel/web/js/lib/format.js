import { h } from './dom.js';

export const fmtNum = n => (Number(n) || 0).toLocaleString();
export function fmtDur(s) {
  s = Math.floor(Number(s) || 0);
  const d = Math.floor(s / 86400), hh = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60), sec = s % 60;
  if (d) return d + 'd ' + hh + 'h';
  if (hh) return hh + 'h ' + m + 'm';
  if (m) return m + 'm ' + sec + 's';
  return sec + 's';
}
export function fmtTime(t) {
  const d = new Date(t);
  if (isNaN(d)) return h(t);
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
}
export function fmtCompact(n) {
  n = Number(n) || 0;
  const a = Math.abs(n);
  const f = (x, s) => (x >= 100 ? Math.round(x) : x.toFixed(1).replace(/\.0$/, '')) + s;
  if (a >= 1e9) return f(n / 1e9, 'B');
  if (a >= 1e6) return f(n / 1e6, 'M');
  if (a >= 1e3) return f(n / 1e3, 'K');
  return String(Math.round(n));
}
export function fmtMoney(n) {
  n = Number(n) || 0;
  if (n === 0) return '$0';
  if (n < 0.01) return '<$0.01';
  if (n < 10) return '$' + n.toFixed(2);
  if (n < 1000) return '$' + Math.round(n);
  return '$' + fmtCompact(n);
}
export function fmtPct(f, digits) {
  const v = (Number(f) || 0) * 100;
  if (digits) return (v > 0 && v < 1 ? v.toFixed(1) : Math.round(v)) + '%';
  return Math.round(v) + '%';
}
export function fmtPts(saved) {
  const v = Math.max(0, (Number(saved) || 0) * 100);
  return v < 10 ? v.toFixed(1).replace(/\.0$/, '') : String(Math.round(v));
}
export const tokSum = t => (t ? (t.input || 0) + (t.output || 0) + (t.cache_read || 0) + (t.cache_write || 0) : 0);
export function dayLabel(ds, long) {
  const m = /^(\d+)-(\d+)-(\d+)$/.exec(ds || '');
  if (!m) return ds || '';
  const d = new Date(+m[1], +m[2] - 1, +m[3]);
  return d.toLocaleDateString('en-US', long ? { weekday: 'short', month: 'short', day: 'numeric' } : { month: 'short', day: 'numeric' });
}
/* 4 gridline steps on a 1-2-5 scale. */
export function niceStep(v) {
  if (v <= 0) return 0;
  const raw = v / 4, e = Math.pow(10, Math.floor(Math.log10(raw))), f = raw / e;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * e;
}
