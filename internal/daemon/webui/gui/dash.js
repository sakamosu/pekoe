// The local GUI's Dashboard (docs/gui-design.md の「Dashboard」): the
// inbox cards, the Overview tiles, the Pipeline tree and the Requests, from
// GET /api/inbox, GET /api/overview, GET /api/stats (the merges per day and
// today's tokens, `pekoe --json stats`) and GET /api/ledger. Only what
// pekoed sends is drawn: no number is made up. Every string goes in with
// textContent (lib/dom.js); the bars are SVG built with createElementNS and
// numeric attributes (no style).

import { el, txt, colored, link } from "../lib/dom.js";
import { ago, short, questionId, reportId, requestId } from "../lib/fmt.js";
import { canAnswer } from "../lib/inbox.js";
import { STEP_NAMES } from "../lib/overview.js";

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

// ---- Overview: the counts right now, the merges per day, today's tokens ----

// shortTokens shortens a count as pekoed's state.ShortTokens does (pekoe
// stats, the TUI): 950, 12.3k, 4.56M, 1.23G. Go rounds the quotient's exact
// value half to even; toFixed rounds an exact half up, so a tie is settled
// here (TestGUIShortTokensMatchesPekoed).
export function shortTokens(n) {
  n = Math.trunc(Number(n) || 0);
  if (n < 1000) return String(n);
  for (const [div, prec, unit] of [[1e3, 1, "k"], [1e6, 2, "M"]]) {
    const s = fixed(n / div, prec);
    if (parseFloat(s) < 1000) return s + unit;
  }
  return fixed(n / 1e9, 2) + "G";
}

function fixed(x, prec) {
  const exact = x.toFixed(60); // a double's whole expansion, for x < 1e21
  const dot = exact.indexOf(".");
  const rest = exact.slice(dot + 1 + prec);
  if (!/^50*$/.test(rest)) return x.toFixed(prec);
  // an exact half: down to the even digit (toFixed went up)
  const kept = exact.slice(0, dot + 1 + prec).replace(/\.$/, "");
  const last = Number(kept.replace(".", "").slice(-1));
  return last % 2 === 0 ? kept : x.toFixed(prec);
}

// mergeDays: the stats' days, oldest first (from … to, pekoed's local
// dates), each with its merges (every repository, tapi's row too).
export function mergeDays(st) {
  const by = new Map();
  for (const r of (st && st.rows) || []) by.set(r.day, (by.get(r.day) || 0) + (r.merged || 0));
  // counted as UTC dates: the page's time zone never moves them (a day
  // that starts with a DST jump at 0:00 has no local 0:00)
  const parse = (s) => {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s || "");
    return m ? new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]))) : null;
  };
  const from = parse(st && st.from), to = parse(st && st.to);
  const days = [];
  if (!from || !to) return days;
  const pad = (n) => String(n).padStart(2, "0");
  for (const t = from; t <= to && days.length < 31; t.setUTCDate(t.getUTCDate() + 1)) {
    const day = t.getUTCFullYear() + "-" + pad(t.getUTCMonth() + 1) + "-" + pad(t.getUTCDate());
    days.push({ day, merged: by.get(day) || 0, today: day === st.to });
  }
  return days;
}

// todayTokens: today's tokens by kind, every repository and tapi.
export function todayTokens(st) {
  const u = { input: 0, output: 0, cache_creation: 0, cache_read: 0 };
  for (const r of (st && st.rows) || []) {
    if (r.day !== st.to || !r.tokens) continue;
    for (const k of Object.keys(u)) u[k] += r.tokens[k] || 0;
  }
  return u;
}

const BAR_W = 8, BAR_GAP = 3, BAR_H = 28;

// barHeights: a bar's height in px for each day, the busiest day full; a
// day with a merge is never shorter than 4 px, a day without one is a
// 2 px stub (the day is there, nothing was merged).
export function barHeights(days) {
  const most = Math.max(0, ...days.map((d) => d.merged));
  return days.map((d) => (d.merged > 0 ? Math.max(4, Math.round((BAR_H * d.merged) / most)) : 2));
}

const SVG = "http://www.w3.org/2000/svg";
const WEEKDAY = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

function dayWord(d) {
  const [y, m, dd] = d.day.split("-").map(Number);
  return (d.today ? "today" : WEEKDAY[new Date(y, m - 1, dd).getDay()]) + " " + d.day;
}

// mergeBars: one bar a day, past days dim, today secondary (docs/
// gui-design.md「棒グラフ」). Each bar's number is in its <title> and the
// whole row in the label, so it reads without the picture.
function mergeBars(days) {
  const svg = document.createElementNS(SVG, "svg");
  const w = days.length * BAR_W + Math.max(0, days.length - 1) * BAR_GAP;
  svg.setAttribute("class", "bars");
  svg.setAttribute("width", String(w));
  svg.setAttribute("height", String(BAR_H));
  svg.setAttribute("viewBox", "0 0 " + w + " " + BAR_H);
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", "Merges per day: " + days.map((d) => dayWord(d) + " " + d.merged).join(", "));
  const hs = barHeights(days);
  days.forEach((d, i) => {
    const r = document.createElementNS(SVG, "rect");
    r.setAttribute("class", "bar" + (d.today ? " today" : "") + (d.merged ? "" : " zero"));
    r.setAttribute("x", String(i * (BAR_W + BAR_GAP)));
    r.setAttribute("y", String(BAR_H - hs[i]));
    r.setAttribute("width", String(BAR_W));
    r.setAttribute("height", String(hs[i]));
    r.setAttribute("rx", "2");
    const t = document.createElementNS(SVG, "title");
    t.textContent = dayWord(d) + " · " + d.merged + " merged";
    r.append(t);
    svg.append(r);
  });
  return svg;
}

// big: a large number and its word ("4 today")
function big(n, word) {
  const s = el("span", "big");
  s.append(el("span", "big-n" + (n ? "" : " zero"), String(n)), txt(" "), el("span", "big-w", word));
  return s;
}

// statsTile: a tile for what stats says; until it came back, or when it
// failed, the tile says so instead of numbers.
function statsTile(name, stats) {
  const t = el("div", "tile");
  t.append(el("span", "tile-name", name));
  if (stats.err) {
    const l = el("div", "tile-note");
    l.append(colored("err", "✗", "sym"), txt(" stats unavailable · " + stats.err));
    t.append(l);
    return [t, null];
  }
  if (!stats.st) {
    t.append(el("div", "tile-note", "loading…"));
    return [t, null];
  }
  return [t, stats.st];
}

// renderTiles: Right now (the counts of /api/overview), Merged (today, the
// days and their bars) and Tokens today (by kind). stats: {st, err}, the
// last good /api/stats and why the last read failed.
export function renderTiles(box, c, stats = {}) {
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

  const [mt, st] = statsTile("Merged", stats);
  if (st) {
    const days = mergeDays(st);
    const today = days.length ? days[days.length - 1].merged : 0;
    const row = el("div", "tile-merged");
    row.append(big(today, "today"), big(st.merged || 0, days.length + (days.length === 1 ? " day" : " days")));
    if (days.length) row.append(mergeBars(days));
    mt.append(row);
  }
  box.append(mt);

  const [tt, ts] = statsTile("Tokens today", stats);
  if (ts) {
    const u = todayTokens(ts);
    const grid = el("div", "tile-tokens");
    for (const [word, k] of [["out", "output"], ["in", "input"], ["cache write", "cache_creation"], ["cache read", "cache_read"]]) {
      const kv = el("span", "kv");
      const v = el("span", "kv-v" + (u[k] ? "" : " zero"), shortTokens(u[k]));
      v.title = u[k].toLocaleString("en-US") + " tokens";
      kv.append(el("span", "kv-k", word), v);
      grid.append(kv);
    }
    tt.append(grid);
  }
  box.append(tt);
}

// ---- Pipeline: a tree of requests and branches, each branch on a rail
// of five step boxes (docs/gui-design.md の「Pipeline」) ----

export const STEPS = ["impl", "review", "pr", "ci", "merge"];
// the rows shown before "Show N more"
export const PIPE_ROWS = 8;

// stepLook: one step box as [kind, symbol, word]. kind is the box's look
// (done · now · todo · fail · you, docs/ui-guidelines.md「状態の表現」):
// done ✓ secondary, now ◉ ok (the agent works), you ◉ warn (a person's
// turn: the agent waits at the step it is on, or the merge is theirs;
// never CI, which runs on GitHub), fail ✗ err, todo ○ dim.
export function stepLook(f, name) {
  const step = (f.steps || []).find((x) => x.name === name);
  if (!step) return ["todo", "○", ""];
  const live = f.state !== "landed";
  if (name === "merge" && live && f.merge && f.merge.ready && step.status !== "done") return ["you", "◉", "you"];
  switch (step.status) {
    case "done":
      return ["done", "✓", name === "pr" && f.pr ? "#" + f.pr : "done"];
    case "fail":
      return ["fail", "✗", "fail"];
    case "now":
      if (name !== "ci" && f.state === "waiting") return ["you", "◉", "you"];
      if (name === "ci" && f.ci && f.ci.pass + f.ci.fail + f.ci.pending > 0) return ["now", "◉", f.ci.pass + "/" + (f.ci.pass + f.ci.fail + f.ci.pending)];
      return ["now", "◉", "now"];
  }
  return ["todo", "○", ""];
}

// rank: the overview's order of states (web.go's webRank)
const RANK = { waiting: 0, running: 1, creating: 1, queued: 1, done: 2, failed: 2, landed: 4 };
const rank = (f) => RANK[f.state] ?? 3;

const FROM = { "+tapi": "tapi", human: "you", unknown: "unknown", "": "direct" };

// pipelineModel: the overview's groups as the Pipeline's tree. A branch
// hangs under the request that made it (origin) and stays there when a
// later request comes in (that one is a badge); a branch no request made
// is a row of its own, after the requests. Requests with rows still
// moving first, newest on top; inside one, the overview's order of states.
export function pipelineModel(groups) {
  const byOrigin = new Map();
  const solo = [];
  for (const g of groups || []) {
    for (const f of g.rows || []) {
      const parent = f.parent ?? g.parent ?? "";
      const row = { ...f, parent };
      if (!f.origin) {
        solo.push(row);
        continue;
      }
      let t = byOrigin.get(f.origin);
      if (!t) byOrigin.set(f.origin, (t = { id: f.origin, from: FROM[parent] ?? parent, rows: [] }));
      t.rows.push(row);
    }
  }
  const moving = (t) => t.rows.some((f) => f.state !== "landed");
  const trees = [...byOrigin.values()].sort((a, b) => moving(b) - moving(a) || b.id - a.id);
  // rows of one request come from different overview groups when a later
  // request moved some of them: order them again as the overview does
  for (const t of trees) t.rows.sort((a, b) => rank(a) - rank(b));
  return { trees, solo };
}

// badges: the requests that came in after the branch was made, as
// [text, now]. The one at work now first (◉ rN), then the newest done
// (+rN); with more than two, the rest as one count (+K).
export function badges(later) {
  const ls = later || [];
  const now = ls.filter((l) => l.now);
  const past = ls.filter((l) => !l.now).reverse();
  const out = [...now.map((l) => ["◉ " + requestId(l.id), true]), ...past.map((l) => ["+" + requestId(l.id), false])];
  if (out.length <= 2) return out;
  return [out[0], ["+" + (out.length - 1), false]];
}

// waitPhrase: one unmet merge condition in a few words. merge_state
// UNSTABLE or BLOCKED is left out while CI is not through (it says the
// same); BEHIND, DIRTY and the rest are shown. ci with no checks at all
// is not a wait (none will come): statusNote says it on its own.
function waitPhrase(c, conds) {
  switch (c.name) {
    case "land":
      return /not run/.test(c.detail) ? "pekoe land" : "";
    case "review":
      return /needs a new review|differs/.test(c.detail) ? "re-review" : "review";
    case "ci":
      return /not read/.test(c.detail) ? "PR status" : /failed/.test(c.detail) ? "CI " + c.detail : "CI";
    case "merge_state": {
      const ci = conds.find((x) => x.name === "ci");
      if (/not read/.test(c.detail) || (ci && !ci.ok && /UNSTABLE|BLOCKED/.test(c.detail))) return "";
      return "merge state " + c.detail.replace(/^merge_state is /, "").toLowerCase();
    }
    case "conflicts":
      return "conflicts";
    case "after":
      return "earlier sessions";
    case "settled":
      return c.detail.replace(/^agent is /, "agent ");
  }
  return c.name;
}

// statusNote: the Status column's second line, dim: why the row went back
// to impl (a later request), what failed, what the merge waits on.
export function statusNote(f) {
  if (f.state === "landed") return "";
  const now = (f.later || []).find((l) => l.now);
  const impl = (f.steps || []).find((s) => s.name === "impl");
  // a branch a request made and a later one is at work on: sent back to
  // impl by it. A branch no request made only works on it.
  if (now && impl && impl.status !== "done") return (f.origin ? "reopened by " : "working on ") + requestId(now.id);
  const failed = (f.steps || []).find((s) => s.status === "fail");
  if (failed) return (STEP_NAMES[failed.name] || failed.name) + (failed.detail ? ": " + failed.detail : " failed");
  if (f.merge && f.merge.ready) return "ready · your merge";
  if (f.merge) {
    const conds = f.merge.conds || [];
    const noCI = conds.some((c) => c.name === "ci" && !c.ok && c.detail === "no CI checks");
    const unmet = conds.filter((c) => !c.ok && !(noCI && c.name === "ci"));
    const ws = [...new Set(unmet.map((c) => waitPhrase(c, conds)).filter(Boolean))];
    const parts = [...(noCI ? ["no CI checks"] : []), ...(ws.length ? ["merge waits on " + ws.join(" · ")] : [])];
    if (parts.length) return parts.join(" · ");
  }
  if (f.after && f.after.length) return "after " + f.after.join(", ");
  return "";
}

function stepCell(f, name) {
  const [kind, icon, word] = stepLook(f, name);
  const cell = el("div", "pl-step");
  cell.setAttribute("role", "cell");
  cell.append(el("span", "pl-link " + (kind === "todo" ? "todo" : "reached")));
  const box = el("span", "pl-box b-" + kind);
  const step = (f.steps || []).find((x) => x.name === name);
  box.title = (STEP_NAMES[name] || name) + ": " + (step && step.detail ? step.detail : kind);
  box.append(el("span", "pl-sym", icon));
  if (word) box.append(el("span", "pl-word", word));
  cell.append(box);
  return cell;
}

function statusCell(f) {
  const cell = el("div", "pl-status");
  cell.setAttribute("role", "cell");
  const top = el("div", "st-top");
  const lk = f.look || { icon: "○", role: "dim", word: f.state };
  top.append(colored(lk.role, lk.icon, "sym"), txt(" " + String(lk.word || "").toLowerCase()));
  if (f.state !== "idle" && f.state_since) top.append(el("span", "s-age", " · " + ago(f.state_since)));
  cell.append(top);
  const note = statusNote(f);
  if (note) {
    const n = el("div", "st-note", note);
    n.title = note;
    cell.append(n);
  }
  return cell;
}

// branchRow: one branch: the tree's piece (the corner into the box, or
// the straight line for a request's first branch), the branch box with
// its badges, the rail, the status. first: the request's first row.
// shown: how many of the request's rows are on screen (fewer than all
// when "Show N more" cuts it).
function branchRow(f, tree, first, open, shown) {
  const past = f.state === "landed";
  const row = el("div", "pl-row" + (past ? " past" : ""));
  row.setAttribute("role", "row");
  const rq = el("div", "pl-req");
  rq.setAttribute("role", "cell");
  if (tree && first) {
    const box = el("div", "pl-rbox");
    const n = tree.rows.length;
    const count = shown < n ? shown + " of " + n : String(n);
    box.append(el("span", "pl-rid", requestId(tree.id)), el("span", "pl-rsub", tree.from + " · " + count + (n === 1 ? " branch" : " branches")));
    box.title = requestId(tree.id) + " · from " + tree.from;
    rq.append(box);
  } else if (!tree) rq.append(el("span", "pl-none", "no request"));
  const tr = el("div", "pl-tree");
  tr.setAttribute("aria-hidden", "true");
  if (tree) tr.append(el("span", first ? "pl-flat" : "pl-corner"));
  const bc = el("div", "pl-bcell");
  bc.setAttribute("role", "cell");
  const b = el("button", "pl-branch");
  b.type = "button";
  b.title = (f.branch || f.slug) + (f.repo ? " · " + f.repo : "") + (f.slug !== f.branch ? " · " + f.slug : "");
  b.append(el("span", "pl-bname", f.branch || f.slug));
  for (const [text, now] of badges(f.later)) {
    const bd = el("span", "pl-badge" + (now ? " now" : ""));
    if (now) bd.append(el("span", "pl-bsym", text.slice(0, 1)), txt(text.slice(1)));
    else bd.append(txt(text));
    b.append(bd);
  }
  b.addEventListener("click", () => open(f.slug));
  bc.append(b);
  row.append(rq, tr, bc, ...STEPS.map((n) => stepCell(f, n)), statusCell(f));
  return row;
}

// renderPipeline fills box with the tree; open(slug) opens a session's
// tab. more: {expanded, toggle()}: past PIPE_ROWS rows, a "Show N more"
// button (toggle) unless expanded. Returns how many rows are still moving
// (not landed).
export function renderPipeline(box, groups, open, more = {}) {
  box.replaceChildren();
  const { trees, solo } = pipelineModel(groups);
  const all = [...trees.flatMap((t) => t.rows), ...solo];
  const moving = all.filter((f) => f.state !== "landed").length;
  if (!all.length) return 0;
  const head = el("div", "pl-row pl-head");
  head.setAttribute("role", "row");
  for (const [cls, word] of [["pl-req", "Request"], ["pl-tree", ""], ["pl-bcell", "Branch"], ...STEPS.map((n) => ["pl-step", STEP_NAMES[n] || n]), ["pl-status", "Status"]]) {
    const h = el("div", cls, word);
    // the tree's column is drawing only, hidden in the body rows too: the
    // header has as many columns as they do
    if (cls === "pl-tree") h.setAttribute("aria-hidden", "true");
    else h.setAttribute("role", "columnheader");
    head.append(h);
  }
  box.append(head);
  let left = more.expanded ? Infinity : PIPE_ROWS;
  for (const t of trees) {
    if (left <= 0) break;
    const rows = t.rows.slice(0, left);
    left -= rows.length;
    const g = el("div", "pl-group");
    g.setAttribute("role", "rowgroup");
    // the trunk: one line for the whole request, from the middle of the
    // first row down to where the last branch turns off it
    if (rows.length > 1) g.append(el("span", "pl-trunk"));
    rows.forEach((f, i) => g.append(branchRow(f, t, i === 0, open, rows.length)));
    box.append(g);
  }
  for (const f of solo) {
    if (left-- <= 0) break;
    box.append(branchRow(f, null, false, open));
  }
  const hidden = all.length - Math.min(all.length, more.expanded ? Infinity : PIPE_ROWS);
  if (hidden > 0 || (more.expanded && all.length > PIPE_ROWS)) {
    const b = el("button", "pl-more", more.expanded ? "Show less" : "Show " + hidden + " more");
    b.type = "button";
    b.addEventListener("click", () => more.toggle && more.toggle());
    box.append(b);
  }
  return moving;
}

// ---- Requests: the ledger (GET /api/ledger), open first (oldest first,
// as `pekoe ledger`), then the closed ones closed last ----

// closedWord: how a closed request ended, in pekoe ledger's words; what
// pekoed closed itself as no request is "closed by pekoe"
const closedWord = (e) => (e.status === "chat" && e.auto ? "closed by pekoe" : e.status);

// renderRequests fills the list from lv ({open, closed, closed_total}).
// on: {open(slug), has(slug)}: a session tied to the request opens its tab
// when it is still here, and is a plain name when it is gone. Returns
// [open, closed in all].
export function renderRequests(box, lv, on) {
  box.replaceChildren();
  const opens = (lv && lv.open) || [];
  const closed = (lv && lv.closed) || [];
  for (const e of opens) {
    const row = el("div", "lrow");
    row.append(colored("ok", "◉", "sym"), el("span", "l-id", requestId(e.id)), el("span", "l-text", e.text || ""));
    row.lastChild.title = e.text || "";
    const to = el("span", "l-to");
    to.append(txt("open"));
    const slugs = e.sessions || [];
    if (slugs.length) {
      to.append(txt(" → "));
      const s = slugs[slugs.length - 1];
      if (on.has(s)) {
        const b = el("button", "l-link", s);
        b.type = "button";
        b.addEventListener("click", () => on.open(s));
        to.append(b);
      } else to.append(txt(s));
      if (slugs.length > 1) to.append(txt(" +" + (slugs.length - 1)));
    }
    row.append(to, el("span", "l-age", ago(e.at)));
    box.append(row);
  }
  for (const e of closed) {
    const row = el("div", "lrow past");
    row.append(el("span", "sym", "✓"), el("span", "l-id", requestId(e.id)), el("span", "l-text", e.text || ""));
    row.lastChild.title = e.text || "";
    row.append(el("span", "l-to", closedWord(e) + " · " + ago(e.closed_at || e.at)));
    box.append(row);
  }
  const total = (lv && lv.closed_total) || 0;
  if (!opens.length && !closed.length) box.append(el("div", "empty", "No requests yet"));
  else if (total > closed.length && on.expanded) box.append(el("div", "lrow more", "+" + (total - closed.length) + " older · pekoe ledger --all"));
  return [opens.length, total];
}
