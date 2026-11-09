export function toast(msg, kind) {
  const box = document.getElementById('toasts');
  if (!box) return;
  const t = document.createElement('div');
  t.className = 'toast ' + (kind || '');
  t.setAttribute('role', kind === 'err' ? 'alert' : 'status');
  t.textContent = msg;
  box.appendChild(t);
  setTimeout(() => t.remove(), kind === 'err' ? 6000 : 3000);
}
