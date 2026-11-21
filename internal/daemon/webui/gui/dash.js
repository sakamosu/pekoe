// The local GUI's Dashboard (docs/gui-design.md の「Dashboard」): the
// inbox cards, the Overview tiles, the Pipeline table and the Requests, from
// GET /api/inbox, GET /api/overview, GET /api/stats (the merges per day and
// today's tokens, `pekoe --json stats`) and GET /api/ledger. Only what
// pekoed sends is drawn: no number is made up. Every string goes in with
// textContent (lib/dom.js); the bars are SVG built with createElementNS and
// numeric attributes (no style).

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
