// The phone page's small graphics (docs/web.md 「画面の構成」): the counts
// as one band, a request's five steps as dots on a line, and pekoed's
// merge conditions and the PR's checks as bars. Web only (the local GUI
// draws its own); every width is the CSS's (a grid or flex of equal parts,
// app.css), nothing here writes a style.

import { el, colored } from "./lib/dom.js";
import { STEP_NAMES } from "./lib/overview.js";

// countBand: the counts in one band, each a link to its list. A 0 is dim
// (its mark too); queued has a cell only when it is not 0.
// c: /api/now's counts.
export function countBand(c) {
  const band = el("nav", "band");
  band.setAttribute("aria-label", "counts");
  const cell = (n, role, icon, word, hash) => {
    const a = el("a", "bc" + (n ? "" : " zero"));
    a.href = hash;
    const w = el("span", "bw");
    w.append(colored(n ? role : "dim", icon), " " + word);
    a.append(el("span", "bn", String(n)), w);
    band.append(a);
  };
  cell(c.waiting || 0, "warn", "◉", "waiting", "#/sessions/active");
  cell((c.questions || 0) + (c.ready || 0), "secondary", "?", "inbox", "#/inbox");
  cell(c.running || 0, "ok", "●", "running", "#/sessions/active");
  if (c.queued) cell(c.queued, "dim", "◌", "queued", "#/sessions/active");
  cell(c.review || 0, "secondary", "✓", "to review", "#/sessions/review");
  return band;
}

// stepLook: a step's mark: done ✓, under way ◉ (ok), the person's turn
// ◉ (warn: the session waits on a permission or a question, or the merge
// waits for a person; webinbox.go's merge step says "waiting for a
// person"), failed ✗, not reached ○ (an empty dashed dot).
// waiting: the request's session waits on the person.
export function stepLook(s, waiting) {
  switch (s.status) {
    case "done": return ["done", "✓"];
    case "fail": return ["fail", "✗"];
    case "now":
      if (s.name === "merge" ? /waiting for a person/.test(s.detail || "") : waiting && s.name !== "ci") return ["you", "◉"];
      return ["now", "◉"];
    default: return ["todo", ""];
  }
}

// stepper: the five steps as dots joined by a line, each step's name
// under its dot (the line into a step solid once it is reached, dashed
// before). A step's detail is its title.
export function stepper(steps, waiting) {
  const ol = el("ol", "stepper");
  for (const s of steps || []) {
    const [k, icon] = stepLook(s, waiting);
    const li = el("li", "sp st-" + k);
    if (s.detail) li.title = s.detail;
    li.append(el("span", "dot", icon), el("span", "sn", STEP_NAMES[s.name] || s.name));
    ol.append(li);
  }
  return ol;
}

// gateBars: pekoed's merge conditions (merge.check's conds) as one bar
// each, ok or err, and how many are met. null without conditions.
export function gateBars(m) {
  const conds = (m && m.conds) || [];
  if (!conds.length) return null;
  const met = conds.filter((c) => c.ok).length;
  const row = el("div", "meter gates");
  const bars = el("span", "bars");
  bars.setAttribute("role", "img");
  bars.setAttribute("aria-label", met + " of " + conds.length + " merge conditions met");
  for (const c of conds) {
    const b = el("i", "mseg " + (c.ok ? "b-ok" : "b-err"));
    b.title = c.name + (c.ok ? " ✓" : " ✗");
    bars.append(b);
  }
  row.append(bars, el("span", "mn", met + "/" + conds.length + " gates"));
  return row;
}

// ciBar: the PR's checks, one part a check (passed ok, failed err,
// running dim), and the counts. null when no check is counted.
// ps: the facts' pr_status.
export function ciBar(ps) {
  if (!ps) return null;
  const pass = ps.pass || 0, fail = ps.fail || 0, pend = ps.pending || 0;
  const n = pass + fail + pend;
  if (!n) return null;
  const row = el("div", "meter ci");
  const bars = el("span", "bars");
  bars.setAttribute("role", "img");
  bars.setAttribute("aria-label", "CI: " + pass + " of " + n + " checks passed, " + fail + " failed, " + pend + " running");
  const part = (k, times) => {
    for (let i = 0; i < times; i++) bars.append(el("i", "mseg " + k));
  };
  part("b-ok", pass);
  part("b-err", fail);
  part("b-pend", pend);
  const words = el("span", "mn");
  words.append("CI ", colored(fail ? "err" : pend ? "text" : "ok", String(pass)), "/" + n);
  if (fail) words.append(colored("err", " · " + fail + " failed"));
  if (pend) words.append(" · " + pend + " running");
  row.append(bars, words);
  return row;
}

// mergeMeters: the merge conditions' bars and the CI's, as one block
// (null when there is neither). f: an inbox item's facts.
export function mergeMeters(f) {
  if (!f) return null;
  const g = gateBars(f.merge), c = ciBar(f.pr_status);
  if (!g && !c) return null;
  const box = el("div", "meters");
  if (g) box.append(g);
  if (c) box.append(c);
  return box;
}
