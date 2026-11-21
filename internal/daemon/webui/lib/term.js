// The agent screen, with its colors.
// pekoed sends each line as runs {t, fg, bg, b, d, i, u, s} (internal/termstyle).
// The text goes in with textContent; a color is a theme role (a class from
// this fixed list) or #rrggbb, checked here and set through CSSOM.

import { el } from "./dom.js";

const TERM_ROLES = new Set(["border", "err", "ok", "warn", "primary-end", "primary", "secondary", "text", "dim", "bg"]);
const HEX = /^#[0-9a-f]{6}$/;

function termRun(run) {
  const sp = el("span", null, String(run.t ?? ""));
  const cls = [];
  for (const [key, prop, pre] of [["fg", "color", "f-"], ["bg", "backgroundColor", "k-"]]) {
    const c = run[key];
    if (typeof c !== "string" || !c) continue;
    if (TERM_ROLES.has(c)) cls.push(pre + c);
    else if (HEX.test(c)) sp.style[prop] = c;
  }
  if (run.b) cls.push("t-b");
  if (run.d) cls.push("t-d");
  if (run.i) cls.push("t-i");
  if (run.u) cls.push("t-u");
  if (run.s) cls.push("t-s");
  if (cls.length) sp.className = cls.join(" ");
  return sp;
}

// termLines builds the screen: one text node per plain run, a span per
// styled one, a newline between lines.
export function termLines(rows) {
  const frag = document.createDocumentFragment();
  rows.forEach((runs, i) => {
    if (i) frag.append("\n");
    for (const run of Array.isArray(runs) ? runs : []) {
      const plain = !run.fg && !run.bg && !run.b && !run.d && !run.i && !run.u && !run.s;
      frag.append(plain ? document.createTextNode(String(run.t ?? "")) : termRun(run));
    }
  });
  return frag;
}

// screenRows: rows from the API, or the plain lines from an older pekoed
export function screenRows(r) {
  if (Array.isArray(r.rows)) return r.rows;
  return (r.lines || []).map((l) => [{ t: l }]);
}
