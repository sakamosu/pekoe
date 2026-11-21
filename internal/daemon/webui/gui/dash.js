// The local GUI's Dashboard (docs/gui-design.md の「Dashboard」): the
// inbox cards, the Overview tile, the Pipeline table and the Requests, from
// GET /api/inbox and GET /api/overview. Only what pekoed sends is drawn:
// no number is made up for what the API does not carry (merges per day,
// tokens). Every string goes in with textContent (lib/dom.js).

import { el, txt, colored, link } from "../lib/dom.js";
import { ago, short, questionId, reportId, requestId } from "../lib/fmt.js";
import { canAnswer } from "../lib/inbox.js";
import { parentLabel, STEP_LOOK, STEP_NAMES } from "../lib/overview.js";

export const cap = (s) => (s ? String(s).charAt(0).toUpperCase() + String(s).slice(1) : "");

// diff: +a −d, the counts in ok / err (data, not decoration)
export function diffBits(d) {
  if (!d || d.added + d.deleted <= 0) return null;
  const sp = el("span", "diff");
  sp.append(colored("ok", "+" + d.added), txt(" "), colored("err", "−" + d.deleted));
  return sp;
}

// ---- Inbox ----

const KINDS = {
  decision: ["?", "secondary", "decision"],
  approve: ["!", "warn", "approve"],
  login: ["@", "primary", "login"],
  fyi: ["i", "dim", "fyi"],
};

// TITLES: a card's title for an agent's item, in pekoe's words (the
// agent's question comes after the facts, labelled as its words)
const TITLES = { decision: "A decision asked by ", approve: "An approval asked by ", login: "A login asked by ", fyi: "A note from " };

// gate: one line of what pekoe read, "symbol name detail"; role "" is a
// fact with no state (a dim dot)
function gate(role, icon, name, ...detail) {
  const g = el("div", "gate");
  g.append(role ? colored(role, icon, "sym") : el("span", "sym r-dim", "·"), el("span", "g-name", name));
  const d = el("span", "g-detail");
  d.append(...detail);
  g.append(d);
  return g;
}

const TEST_ROLE = { pass: ["ok", "✓"], fail: ["err", "✗"], error: ["warn", "!"], blocked: ["warn", "!"], running: ["dim", "◌"] };

function gates(it) {
  const box = el("div", "gates");
  const f = it.facts;
  if (f && !f.gone) {
    for (const c of (f.merge && f.merge.conds) || []) box.append(gate(c.ok ? "ok" : "err", c.ok ? "✓" : "✗", c.name, txt(c.detail)));
    const d = f.diffstat || { added: 0, deleted: 0, files: 0 };
    const dd = diffBits(d);
    box.append(gate("", "", "diff", ...(dd ? [dd, txt(" · " + d.files + (d.files === 1 ? " file" : " files"))] : [txt("no changes")])));
    if (f.pr) {
      const ps = f.pr_status;
      const n = ["#" + f.pr];
      if (ps) {
        n.push(ps.state || "?", "CI " + ps.pass + " pass " + ps.fail + " fail " + ps.pending + " pending");
        if (ps.review_decision) n.push(ps.review_decision);
        if (ps.merge_state) n.push(ps.merge_state);
        if (ps.unresolved) n.push(ps.unresolved + " unresolved");
      }
      box.append(gate(ps && ps.fail ? "err" : "", "✗", "PR", txt(n.join(" · "))));
    }
    if (f.pr_repo) box.append(gate("warn", "!", "fork", txt("PR in " + f.pr_repo + " · pushed to " + f.push_repo + " (origin)")));
    const ts = f.test && f.test.status;
    if (ts && ts !== "none") {
      const [role, icon] = TEST_ROLE[ts] || ["dim", "·"];
      box.append(gate(role, icon, "test", txt(ts)));
    }
    for (const o of f.overlaps || []) box.append(gate(o.conflict ? "err" : "warn", o.conflict ? "✗" : "!", "forecast", txt((o.conflict ? "conflicts" : "overlaps") + " with " + o.slug)));
    if (f.report) box.append(gate("", "", "report", txt(reportId(f.report.id) + " " + f.report.status + " · " + ago(f.report.at) + " ago")));
    if (f.git_blocked) box.append(gate("warn", "!", "git", txt(".git changed: git stopped")));
  }
  for (const l of it.links || []) box.append(gate("", "", "link", link(l)));
  return box.childNodes.length ? box : null;
}

// targetLine: what the answer acts on, sunk into bg, monospace, no color
function targetLine(f) {
  const parts = [f.slug];
  if (f.branch && f.branch !== f.slug) parts.push(f.branch + (f.base ? " → " + f.base : ""));
  else if (f.base) parts.push("→ " + f.base);
  if (f.head) parts.push("HEAD " + short(f.head));
  return el("div", "target", parts.join(" · "));
}

// inboxCard: one open item. pekoe's facts first (the target, the gates),
// the agent's words after, labelled as its words; the options never come
// chosen (the recommendation is the agent's opinion). The answer goes
// through the confirm sheet (on.answer). focus: the first card, the
// page's one attention focus (the gradient frame). on: {busy(), focus(),
// blur(), answer(it)}.
export function inboxCard(it, d, focus, on) {
  const card = el("article", "icard" + (focus ? " focus-card" : ""));
  const [icon, role, word] = KINDS[it.kind] || ["?", "dim", it.kind];
  const f = it.facts;
  const pekoed = it.from === "pekoed";
  const head = el("div", "ic-head");
  head.append(el("h3", "ic-title", pekoed ? it.question : (TITLES[it.kind] || "A question from ") + it.from));
  const st = el("span", "ic-state");
  if (f && f.merge && f.merge.ready) st.append(colored("secondary", "✓ merge ready"));
  else st.append(colored(role, icon, "sym"), txt(" " + word));
  head.append(st);
  const from = el("span", "ic-from", (pekoed ? "pekoed asks" : "from " + it.from) + " · " + questionId(it.id) + " · " + ago(it.created_at) + " ago");
  if (it.claimed) from.append(txt(" "), colored("warn", "⚠ claims to be " + it.claimed));
  head.append(from);
  card.append(head);

  if (f && f.gone) {
    const w = el("div", "ic-line");
    w.append(colored("warn", "!"), txt(" " + f.slug + " is gone"));
    card.append(w);
  } else if (f) card.append(targetLine(f));
  else card.append(el("div", "ic-line dim", "Not about a session"));
  if (d.changed) {
    const w = el("div", "ic-line");
    w.append(colored("warn", "⚠"), txt(" Changed while you looked: check the facts again"));
    card.append(w);
  }
  const g = gates(it);
  if (g) card.append(g);

  if (!pekoed || it.context) {
    const says = el("div", "says");
    if (!pekoed) {
      says.append(el("div", "says-cap", "The agent says (its words, not pekoe's)"));
      says.append(el("div", "says-q", it.question));
    }
    if (it.context) says.append(el("div", "says-ctx", it.context));
    card.append(says);
  }

  const opts = el("div", "opts");
  opts.setAttribute("role", "radiogroup");
  (it.options || []).forEach((o, i) => {
    const b = el("button", "opt");
    b.type = "button";
    b.setAttribute("role", "radio");
    b.append(el("span", "k", String(i + 1)), el("span", "o-text", o));
    if (it.recommend === i) b.append(el("span", "o-rec", "recommended (agent)"));
    b.addEventListener("click", () => {
      d.choice = d.choice === i ? null : i;
      d.changed = false;
      paint();
      update();
    });
    opts.append(b);
  });
  if (opts.childNodes.length) card.append(opts);

  const ta = el("textarea", "note");
  ta.rows = 2;
  ta.maxLength = 4000;
  ta.placeholder = !(it.options || []).length ? "answer in words" : pekoed && it.kind === "approve" ? "note (optional; send back needs one)" : "note (optional; or, with nothing chosen, the answer on its own: none of these)";
  ta.value = d.note;
  ta.addEventListener("input", () => {
    d.note = ta.value;
    update();
  });
  ta.addEventListener("focus", on.focus);
  ta.addEventListener("blur", on.blur);
  card.append(ta);

  const row = el("div", "actions");
  const btn = el("button", "btn lg primary");
  btn.type = "button";
  btn.addEventListener("click", () => on.answer(it));
  row.append(btn);
  if (f && f.pr && f.pr_url) {
    const a = link(f.pr_url, "Open PR #" + f.pr);
    if (a.tagName === "A") {
      a.className = "btn lg";
      row.append(a);
    }
  }
  card.append(row);

  function paint() {
    opts.querySelectorAll(".opt").forEach((b, i) => {
      b.classList.toggle("sel", d.choice === i);
      b.setAttribute("aria-checked", String(d.choice === i));
    });
  }
  function update() {
    btn.textContent = d.choice !== null && it.options[d.choice] ? cap(it.options[d.choice]) + "…" : "Answer…";
    btn.disabled = !canAnswer(it, d) || on.busy();
  }
  paint();
  update();
  return card;
}

const MERGE_LOOK = { running: ["◌", "dim", "merging"], merged: ["✓", "ok", "merged"], failed: ["✗", "err", "not merged"] };

// mergeRow: pekoed's merge after a "merge" answer (pekoed's words only)
export function mergeRow(it) {
  const r = it.answer.merge;
  const [icon, role, word] = MERGE_LOOK[r.state] || ["?", "dim", r.state];
  const row = el("div", "lrow");
  row.append(colored(role, icon, "sym"), el("span", "l-id", "PR #" + r.pr), el("span", "l-text", word + " · " + (it.about || "") + " · HEAD " + (r.head || "").slice(0, 7) + (r.method ? " · " + r.method : "") + (r.detail ? " · " + r.detail : "")), el("span", "l-age", ago(r.at)));
  return row;
}

// ---- Overview: the counts pekoed has (no merges per day, no tokens) ----

export function renderTiles(box, c) {
  box.replaceChildren();
  const t = el("div", "tile");
  t.append(el("span", "tile-name", "Right now"));
  const nums = el("div", "tile-nums");
  const num = (n, word, hot) => {
    const s = el("span", "num");
    s.append(el("span", "n" + (!n ? " zero" : hot ? " hot" : ""), String(n || 0)), txt(" " + word));
    nums.append(s);
  };
  num(c.running, "running");
  if (c.queued) num(c.queued, "queued");
  num(c.waiting, "waiting");
  num(c.ready, "to merge", true);
  num(c.review, "to review");
  num(c.open_prs, "PR open");
  num(c.inbox_open, "inbox");
  num(c.ledger_open, "requests open");
  t.append(nums);
  box.append(t);
}

// ---- Pipeline: one row per session, the steps as "symbol word" ----

export const STEPS = ["impl", "review", "pr", "ci", "merge"];
const STEP_WORD = { done: "done", now: "now", fail: "fail", todo: "" };

function stepCell(f, name) {
  const td = el("td", "col-step");
  const step = (f.steps || []).find((x) => x.name === name);
  if (!step) {
    td.append(el("span", "r-dim", "·"));
    return td;
  }
  if (step.detail) td.title = (STEP_NAMES[name] || name) + ": " + step.detail;
  if (name === "merge" && f.merge && f.merge.ready && f.state !== "landed") {
    td.append(colored("secondary", "◉ you"));
    return td;
  }
  const [icon, role] = STEP_LOOK[step.status] || ["○", "dim"];
  const word = name === "pr" && f.pr ? "#" + f.pr : STEP_WORD[step.status] ?? "";
  td.append(colored(role, icon, "sym"));
  if (word) td.append(txt(" "), el("span", "s-word", word));
  return td;
}

function statusCell(f) {
  const td = el("td", "col-status");
  const top = el("div", "st-top");
  if (f.merge && f.merge.ready && f.state !== "landed") top.append(colored("secondary", "◉ Ready to merge"));
  else {
    const lk = f.look || { icon: "○", role: "dim", word: f.state };
    top.append(colored(lk.role, lk.icon, "sym"), txt(" " + cap(lk.word)), el("span", "s-age", f.state === "idle" ? "" : " · " + ago(f.state_since)));
  }
  td.append(top);
  const miss = [];
  if (f.merge) {
    for (const c of f.merge.conds || []) if (!c.ok) miss.push(["✗", c.name + ": " + c.detail]);
  } else {
    for (const s of f.steps || []) if (s.status === "fail" && s.detail) miss.push(["✗", (STEP_NAMES[s.name] || s.name) + ": " + s.detail]);
    if (f.after && f.after.length) miss.push(["◌", "after " + f.after.join(", ")]);
  }
  for (const [icon, text] of miss) {
    const m = el("div", "st-miss");
    m.append(colored(icon === "✗" ? "err" : "dim", icon, "sym"), txt(" " + text));
    td.append(m);
  }
  return td;
}

// renderPipeline fills the table body; open(slug) opens a session's tab.
// Returns how many rows are still moving (not landed).
export function renderPipeline(body, groups, open) {
  body.replaceChildren();
  let moving = 0;
  for (const g of groups) {
    for (const f of g.rows || []) {
      const past = f.state === "landed";
      if (!past) moving++;
      const tr = el("tr", past ? "past" : null);
      const td = el("td", "col-branch");
      const b = el("button", "flow-name", f.branch || f.slug);
      b.type = "button";
      b.addEventListener("click", () => open(f.slug));
      const sub = [parentLabel(g)];
      if (g.request) sub.push("request " + requestId(g.request.id));
      sub.push(f.repo + (f.slug !== f.branch ? " · " + f.slug : ""));
      td.append(b, el("div", "flow-sub", sub.join(" · ")));
      tr.append(td, ...STEPS.map((n) => stepCell(f, n)), statusCell(f));
      body.append(tr);
    }
  }
  return moving;
}

// ---- Requests: the ledger entries the sessions carry, open first, then
// the last five closed (the API has no ledger list of its own) ----

export function renderRequests(box, groups, open) {
  box.replaceChildren();
  const seen = new Map();
  for (const g of groups) {
    if (!g.request) continue;
    const r = seen.get(g.request.id) || { req: g.request, slugs: [] };
    for (const f of g.rows || []) r.slugs.push(f.slug);
    seen.set(g.request.id, r);
  }
  const all = [...seen.values()].sort((a, b) => b.req.id - a.req.id);
  const openOnes = all.filter((r) => r.req.status === "open");
  const closed = all.filter((r) => r.req.status !== "open").slice(0, 5);
  for (const r of [...openOnes, ...closed]) {
    const isOpen = r.req.status === "open";
    const row = el("div", "lrow" + (isOpen ? "" : " past"));
    row.append(isOpen ? colored("ok", "◉", "sym") : el("span", "sym", "✓"), el("span", "l-id", requestId(r.req.id)), el("span", "l-text", r.req.text || ""));
    const to = el("span", "l-to");
    to.append(txt(r.req.status + " → "));
    const b = el("button", "l-link", r.slugs[0] || "");
    b.type = "button";
    b.addEventListener("click", () => open(r.slugs[0]));
    to.append(b);
    if (r.slugs.length > 1) to.append(txt(" +" + (r.slugs.length - 1)));
    row.append(to);
    box.append(row);
  }
  if (!box.childNodes.length) box.append(el("div", "empty", "No requests on the sessions here"));
  return [openOnes.length, closed.length];
}
