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

// prLink: a PR's number ("#4") as a link to the PR on GitHub when pekoed
// sent its page (pr_url), else as text. Its click stays with the link: a
// row around it that opens or selects on a click (asButton) does not do
// that too. Its keys go on to the page (the GUI's keys work with the link
// focused); asButton takes only keys on the row itself.
export function prLink(url, text) {
  if (!url) return txt(text);
  const a = link(url, text);
  a.addEventListener("click", (e) => e.stopPropagation());
  return a;
}

// asButton makes e (a div) act as a button: fn on a click, Enter or Space,
// in the tab order. For a row that holds a link (prLink): an <a> inside a
// <button> is not allowed, and its click would go to the button.
export function asButton(e, fn) {
  e.setAttribute("role", "button");
  e.tabIndex = 0;
  e.addEventListener("click", fn);
  e.addEventListener("keydown", (ev) => {
    if (ev.target !== e || (ev.key !== "Enter" && ev.key !== " ")) return;
    ev.preventDefault();
    fn(ev);
  });
  return e;
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
