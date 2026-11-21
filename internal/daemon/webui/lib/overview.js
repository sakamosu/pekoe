// The overview: the counts, tapi, and every session's flow
// (impl → review → PR → CI → merge), read only (GET /api/overview).

import { el, txt, colored } from "./dom.js";
import { ago } from "./fmt.js";

export const STEP_NAMES = { impl: "impl", pr: "PR", ci: "CI", review: "review", merge: "merge" };
export const STEP_LOOK = { done: ["✓", "ok"], now: ["●", "warn"], fail: ["✗", "err"], todo: ["○", "dim"] };

// renderCounts: one tile per count; a tile with a hash is a link
export function renderCounts(box, c) {
  box.replaceChildren();
  const tile = (role, icon, n, word, hash) => {
    const t = el(hash ? "a" : "div", "tile");
    if (hash) t.href = hash;
    t.append(colored(n ? role : "dim", icon), el("span", "n", String(n)), el("span", "w", word));
    box.append(t);
  };
  tile("ok", "●", c.running, "running");
  if (c.queued) tile("dim", "◌", c.queued, "queued");
  tile("warn", "●", c.waiting, "waiting");
  tile("secondary", "✓", c.review, "to review");
  tile("primary", "#", c.open_prs, "open PRs");
  tile("ok", "✓", c.ready, "ready to merge");
  tile("warn", "?", c.inbox_open, "inbox", "#/inbox");
  tile("secondary", "☰", c.ledger_open, "requests open");
}

// tapiLine: icon  tapi  state  age, as a session's line1 (tapi: /api/tapi)
export function tapiLine(tapi) {
  const l1 = el("div", "line1");
  if (!tapi) {
    l1.append(colored("dim", "○", "ic"), el("span", "name r-text", "tapi"), colored("dim", "not readable", "word"));
    return l1;
  }
  const lk = tapi.look || { icon: "○", role: "dim", word: tapi.state };
  let word = lk.word, role = lk.role;
  if (!tapi.started) [word, role] = ["not started", "dim"];
  else if (!tapi.alive) [word, role] = ["exited", "dim"];
  l1.append(colored(role, lk.icon, "ic"), el("span", "name r-text", "tapi"), colored(role, word, "word"),
    el("span", "age", tapi.started && tapi.state !== "idle" ? ago(tapi.state_since) : ""));
  return l1;
}

// renderTapi: tapi's state, and what it waits on, in box
export function renderTapi(box, tapi) {
  box.replaceChildren();
  if (!tapi) {
    box.append(el("div", "empty", "(tapi is not readable)"));
    return;
  }
  box.append(tapiLine(tapi));
  if (tapi.started && tapi.answerable) box.append(el("div", "also r-warn", "⚠ tapi waits for a permission: answer it at the terminal"));
  else if (tapi.started && tapi.state === "waiting") box.append(el("div", "also r-warn", "? tapi asks: open it to answer"));
  if (tapi.last_message) box.append(el("div", "msg last", tapi.last_message));
}

export function parentLabel(g) {
  switch (g.parent) {
    case "+tapi": return "tapi";
    case "": return "opened directly";
    case "human": return "you";
    case "unknown": return "unknown";
  }
  return "from " + g.parent;
}

// stepCell: one step as icon + name, in its status's role
export function stepCell(s, withName) {
  const [icon, role] = STEP_LOOK[s.status] || ["○", "dim"];
  const sp = el("span", "step st-" + (STEP_LOOK[s.status] ? s.status : "todo"));
  sp.append(colored(role, icon));
  if (withName) sp.append(txt(" " + (STEP_NAMES[s.name] || s.name)));
  return sp;
}

// missing: what stands between a row and its merge, as lines: the merge
// conditions not met (with a PR), else the failed steps' details; and
// what it waits after. ready: the evidence is all there.
export function missing(f) {
  const out = [];
  if (f.merge) {
    for (const c of f.merge.conds || []) {
      if (c.ok) continue;
      const line = el("div");
      line.append(colored("err", "✗"), txt(" " + c.name + ": " + c.detail));
      out.push(line);
    }
  } else {
    for (const s of f.steps) {
      if (s.status !== "fail" || !s.detail) continue;
      const line = el("div");
      line.append(colored("err", "✗"), txt(" " + (STEP_NAMES[s.name] || s.name) + ": " + s.detail));
      out.push(line);
    }
  }
  if (!f.merge && f.after && f.after.length) out.push(el("div", null, "after " + f.after.join(", "))); // with a PR, merge.check's after says it
  return out;
}
