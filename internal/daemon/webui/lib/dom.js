// The DOM helpers both pages build with (the phone's app.js and the local
// GUI's gui/gui.js). Everything goes into the DOM through textContent and
// text nodes: agent output is untrusted text, and no HTML is ever built
// from a string (webinbox_test.go checks every file under webui/).

export const $ = (id) => document.getElementById(id);
export const ROLES = new Set(["ok", "warn", "err", "primary", "secondary", "dim", "text"]);

export function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

// colored: text in a theme role (a class, never a style from the wire)
export function colored(role, text, cls) {
  return el("span", (cls ? cls + " " : "") + "r-" + (ROLES.has(role) ? role : "dim"), text);
}

export const txt = (s) => document.createTextNode(String(s));
export const sep = () => el("span", "sep", "·");

// link: an anchor for an http(s) URL with nothing odd in it; anything else
// (javascript:, data:, a path) stays plain text
export function link(url, text) {
  const u = String(url ?? "");
  if (!/^https?:\/\//i.test(u) || /[\s"<>\\]/.test(u)) return el("span", "r-dim", text ?? u);
  const a = el("a", "lnk", text ?? u);
  a.href = u;
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  return a;
}

let toastTimer = 0;

// toast: a short line at the bottom (#toast): ✓ for done, ✗ for an error
export function toast(text, ok) {
  const t = $("toast");
  t.replaceChildren(el("span", "ic", ok ? "✓" : "✗"), document.createTextNode(text));
  t.className = "toast" + (ok ? " ok" : "");
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), ok ? 1800 : 4000);
}
