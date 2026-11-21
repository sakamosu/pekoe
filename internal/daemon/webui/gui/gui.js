// pekoe's local GUI, the desktop page (docs/gui.md 段階 1; the look:
// docs/gui-design.md). The title bar; the sessions on the left (tapi's
// card, repositories with two or more sessions open, the rest folded under
// OTHER REPOSITORIES); the tabs (Dashboard, tapi, the sessions opened);
// the Dashboard (dash.js) with tapi's chat on the right; a session's view
// (header, the read-only screen, the input pinned to the bottom, its facts
// on the right); the status line.
// It draws with the phone's modules (lib/) and calls the same /api/, plus
// the GUI's own few (the Dashboard's stats and ledger; New session, tapi's
// Reset and Interrupt, actions.js). Everything goes into the DOM with textContent (agent
// output is untrusted text), and no style is set from here: every look is
// a class in gui.css (TestGUIStylesOnlyInCSS).

import { $, el, colored, txt, toast } from "../lib/dom.js";
import { api, guiSignIn, TAPI, apiBase } from "../lib/api.js";
import { ago, hm, look, cutNote, questionUp, requestId } from "../lib/fmt.js";
import { termLines, screenRows } from "../lib/term.js";
import { IMG_MAX, prepareImage, thumbs, sendBody, imagesNote } from "../lib/images.js";
import { draftOf } from "../lib/inbox.js";
import { parentLabel } from "../lib/overview.js";
import { SPIN, bubbleLine } from "../lib/chat.js";
import { answerSheet, inboxSheet, showSendSheet } from "../lib/sheets.js";
import { shellBang } from "./shell.js";
import { guardPageDrops } from "./drop.js";
import { THEME_WORDS, nextTheme, readTheme, saveTheme, applyTheme } from "./theme.js";
import { cap, diffBits, inboxCard, mergeRow, renderTiles, renderPipeline, renderRequests } from "./dash.js";
import { interruptWhy, interruptSheet, resetSheet, newWhy, newSheet } from "./actions.js";
import { chatPane, selectingIn } from "./chatpane.js";
import { picker } from "./picker.js";

let sessions = [];
let tapi = null; // /api/tapi
let overview = null; // the last /api/overview (the Dashboard; LINKED and "ready" elsewhere)
let view = "dashboard"; // dashboard | detail
let current = null; // the slug in the detail view, or TAPI
let busy = false; // a sheet is open: the cards it was opened from stay as they are
let sending = false, pressing = false, preparing = false;
let listTimer = 0, screenTimer = 0, chatTimer = 0, tickTimer = 0;
let listOk = 0, listFail = ""; // when the list last came, and why it did not
let filter = "all", query = "";
const openTabs = []; // the sessions opened in this tab, oldest first
const drafts = new Map(); // slug → unsent text: one session's words never ride along to another
const attachments = new Map(); // slug → images for its next send (same rule)
const deliveries = new Map(); // slug → {st: sending | delivered | failed, text, at, why}

// ---- theme: the OS's appearance, or this tab's choice (決まったこと 11) ----
// theme.js: system → dark → light → system on the title bar's button. The
// word is the choice in effect; with System it also says what the OS
// shows now.

const tabStore = () => window.sessionStorage;
let theme = "system";

function paintThemeButton() {
  const sys = window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
  const word = theme === "system" ? "System (" + sys + ")" : THEME_WORDS[theme];
  $("theme-word").textContent = THEME_WORDS[theme];
  const label = "theme: " + word + "; switch to " + THEME_WORDS[nextTheme(theme)];
  $("theme-btn").setAttribute("aria-label", label);
  $("theme-btn").title = label;
}

theme = applyTheme(document.documentElement, readTheme(tabStore));
paintThemeButton();
$("theme-btn").addEventListener("click", () => {
  theme = applyTheme(document.documentElement, nextTheme(theme));
  saveTheme(tabStore, theme);
  paintThemeButton();
});
if (window.matchMedia) window.matchMedia("(prefers-color-scheme: light)").addEventListener("change", paintThemeButton);

// ---- the sessions on the left ----

const flows = () => (overview ? (overview.groups || []).flatMap((g) => (g.rows || []).map((f) => ({ f, g }))) : []);
const readySet = () => new Set(flows().filter(({ f }) => f.merge && f.merge.ready && f.state !== "landed").map(({ f }) => f.slug));
const needsYou = (s, ready) => s.state === "waiting" || ready.has(s.slug);

function shown(s, ready) {
  if (filter === "needs" && !needsYou(s, ready)) return false;
  if (filter === "running" && s.state !== "running") return false;
  if (!query) return true;
  const q = query.toLowerCase();
  return [s.slug, s.branch, s.repo].some((x) => String(x || "").toLowerCase().includes(q));
}

// stateBits: symbol (its color), word (text; secondary only for the
// person's turn, "Ready to merge"). On the open row (raised) gui.css
// turns every word to text (決まったこと 12).
function stateBits(s, ready, withAge) {
  const out = [];
  if (ready.has(s.slug)) {
    out.push(colored("secondary", "◉", "sym"), el("span", "word r-secondary", "Ready to merge"));
  } else {
    const lk = look(s);
    let word = cap(lk.word);
    if (s.state === "waiting") word += s.answerable && s.pending_tool ? " · " + s.pending_tool.name : s.wait_kind === "question" ? " · question" : "";
    out.push(colored(lk.role, lk.icon, "sym"), el("span", "word" + (s.state === "waiting" ? " r-text" : ""), word));
  }
  if (withAge && s.state !== "idle") out.push(el("span", "word", " · " + ago(s.state_since)));
  return out;
}

function sideRow(s, ready) {
  const sel = s.slug === current;
  const b = el("button", "srow" + (sel ? " sel" : "") + (s.state === "landed" ? " past" : ""));
  b.type = "button";
  const l1 = el("span", "sr-1");
  l1.append(el("span", "sr-name", s.branch || s.slug), el("span", "sr-age", s.state === "idle" ? "" : ago(s.state_since)));
  const l2 = el("span", "sr-2");
  l2.append(...stateBits(s, ready, false));
  if (s.pr) {
    const pr = el("span", "sr-pr");
    if (s.pr_look) pr.append(colored(s.pr_look.role, s.pr_look.icon, "sym"), txt(" "));
    pr.append(txt("#" + s.pr));
    l2.append(pr);
  }
  const d = diffBits(s.diffstat);
  if (d) l2.append(d);
  b.append(l1, l2);
  b.addEventListener("click", () => go(s.slug));
  if (sel) b.setAttribute("aria-current", "true");
  return b;
}

// oneRow: a repository with one session, folded to one line
function oneRow(s, ready) {
  const sel = s.slug === current;
  const b = el("button", "srow one" + (sel ? " sel" : "") + (s.state === "landed" ? " past" : ""));
  b.type = "button";
  b.append(el("span", "sr-repo", s.repo));
  if (s.branch && s.branch !== s.repo) b.append(el("span", "sr-branch", s.branch));
  const d = diffBits(s.diffstat);
  if (d) b.append(d);
  const st = el("span", "sr-st");
  st.append(...stateBits(s, ready, true));
  b.append(st);
  b.addEventListener("click", () => go(s.slug));
  if (sel) b.setAttribute("aria-current", "true");
  return b;
}

function renderTapiCard() {
  const c = $("s-tapi");
  c.replaceChildren();
  c.classList.toggle("sel", current === TAPI);
  const l1 = el("span", "sr-1");
  l1.append(el("span", "sr-name", "tapi"), el("span", "sr-role", "orchestrator"));
  const st = el("span", "sr-st");
  st.append(...tapiState());
  l1.append(st);
  c.append(l1);
  if (tapi && tapi.last_message) c.append(el("span", "sr-last", tapi.last_message.split("\n")[0]));
  if (current === TAPI) c.setAttribute("aria-current", "true");
  else c.removeAttribute("aria-current");
}

// tapiState: tapi's symbol, word and age
function tapiState() {
  if (!tapi) return [colored("dim", "○", "sym"), el("span", "word", " Not readable")];
  if (!tapi.started) return [colored("dim", "○", "sym"), el("span", "word", " Not started")];
  if (!tapi.alive) return [colored("dim", "○", "sym"), el("span", "word", " Exited")];
  const lk = tapi.look || { icon: "○", role: "dim", word: tapi.state };
  return [colored(lk.role, lk.icon, "sym"), el("span", "word", " " + cap(lk.word) + (tapi.state === "idle" ? "" : " · " + ago(tapi.state_since)))];
}

function byRepo(list) {
  const groups = new Map();
  for (const s of list) {
    if (!groups.has(s.repo)) groups.set(s.repo, []);
    groups.get(s.repo).push(s);
  }
  return [...groups];
}

function renderSide() {
  const ready = readySet();
  const box = $("s-groups");
  box.replaceChildren();
  $("s-count").textContent = String(sessions.length);
  $("f-all").textContent = String(sessions.length);
  $("f-needs").textContent = String(sessions.filter((s) => needsYou(s, ready)).length);
  $("f-running").textContent = String(sessions.filter((s) => s.state === "running").length);
  for (const b of document.querySelectorAll(".chip")) b.setAttribute("aria-pressed", String(b.dataset.filter === filter));
  renderTapiCard();
  // /api/sessions comes waiting first; a repository is placed where its
  // first session is, so one with a session waiting comes on top
  const groups = byRepo(sessions.filter((s) => shown(s, ready)));
  const singles = [];
  for (const [repo, list] of groups) {
    if (list.length < 2 && sessions.filter((s) => s.repo === repo).length < 2) {
      singles.push(list[0]);
      continue;
    }
    const g = el("div", "sgroup");
    const h = el("div", "sgroup-head");
    h.append(el("span", "sg-name", repo), el("span", "sg-n", String(list.length)));
    g.append(h, ...list.map((s) => sideRow(s, ready)));
    box.append(g);
  }
  if (singles.length) {
    const g = el("div", "sgroup");
    g.append(el("div", "label sgroup-label", groups.length > singles.length ? "OTHER REPOSITORIES" : "REPOSITORIES"), ...singles.map((s) => oneRow(s, ready)));
    box.append(g);
  }
  if (!box.childNodes.length) box.append(el("div", "empty", sessions.length ? "Nothing matches" : "No sessions"));
  const waiting = sessions.filter((s) => s.state === "waiting").length;
  const done = sessions.filter((s) => s.state === "done").length;
  const foot = $("s-foot");
  foot.replaceChildren();
  if (waiting) foot.append(colored("warn", "◉", "sym"), txt(" " + waiting + " waiting"));
  if (done) {
    if (waiting) foot.append(el("span", "gap"));
    foot.append(colored("secondary", "✓", "sym"), txt(" " + done + " to review"));
  }
  foot.hidden = !foot.childNodes.length;
  document.title = waiting ? `(${waiting}) pekoe` : "pekoe";
}

for (const b of document.querySelectorAll(".chip")) {
  b.addEventListener("click", () => {
    filter = b.dataset.filter;
    renderSide();
  });
}
$("q").addEventListener("input", () => {
  query = $("q").value.trim();
  renderSide();
});
$("q").addEventListener("keydown", (e) => {
  if (e.key === "Escape") {
    $("q").value = "";
    query = "";
    renderSide();
    $("q").blur();
  }
});
$("s-tapi").addEventListener("click", () => go(TAPI));

// ---- the tabs: Dashboard, tapi, the sessions opened ----

function renderTabs() {
  const box = $("tabs");
  box.replaceChildren();
  const tab = (label, href, on, slug) => {
    const t = el("div", "tab" + (on ? " on" : "") + (slug ? " closable" : ""));
    const a = el("a", "tab-link", label);
    a.href = href;
    a.setAttribute("role", "tab");
    a.setAttribute("aria-selected", String(on));
    t.append(a);
    if (slug) {
      const x = el("button", "tab-x", "×");
      x.type = "button";
      x.setAttribute("aria-label", "close " + label);
      x.addEventListener("click", () => closeTab(slug));
      t.append(x);
    }
    box.append(t);
  };
  tab("Dashboard", "#/", view === "dashboard");
  tab("tapi", "#/tapi", current === TAPI);
  for (const slug of openTabs) {
    const s = sessions.find((x) => x.slug === slug);
    tab(s ? s.branch || slug : slug, "#/s/" + encodeURIComponent(slug), current === slug, slug);
  }
}

function closeTab(slug) {
  const i = openTabs.indexOf(slug);
  if (i >= 0) openTabs.splice(i, 1);
  drafts.delete(slug);
  if (current === slug) location.hash = "#/";
  else renderTabs();
}

// ---- refresh: the list, tapi, and what the view shows ----

async function refresh() {
  try {
    const [r, t] = await Promise.all([api("/api/sessions"), api("/api/tapi").catch(() => null)]);
    sessions = r.sessions || [];
    tapi = t;
    listOk = Date.now();
    listFail = "";
    if (view === "dashboard") await refreshDashboard();
    renderSide();
    renderTabs();
    if (view === "detail") renderDetail();
    renderChatHead();
  } catch (e) {
    listFail = e.message;
    toast(e.message);
  }
  renderDaemon();
  renderStatus();
}

function renderDaemon() {
  const d = $("daemon");
  d.replaceChildren();
  if (listFail && !listOk) d.append(colored("err", "✗", "sym"), txt(" pekoed unreachable"));
  else if (listFail) d.append(colored("err", "✗", "sym"), txt(" pekoed not answering · " + ago(new Date(listOk).toISOString())));
  else if (listOk) d.append(colored("ok", "◉", "sym"), txt(" pekoed running"));
}

// ---- Dashboard ----

let dashSig = "", inbox = [], merges = [], inboxSig = "", inboxHeld = false, inboxTyping = false;
const inboxDraft = new Map();
const isheet = inboxSheet(inboxDraft, {
  close: () => resumeInbox(),
  done: () => {
    inboxSig = "";
    refresh();
  },
});

// the Overview's stats (merges per day, today's tokens): read at most every
// STATS_EVERY (pekoed reads the transcripts' new lines for it, and a merge
// is seen at most once a minute), as the TUI's dashboard does
const STATS_EVERY = 30000;
const stats = { st: null, err: "", at: 0 };
// Requests: the last five closed, or the whole ledger (Open ledger: up to
// the 500 closed last, the rest is `pekoe ledger --all`)
let ledgerAll = false, ledger = null, ledgerErr = "";

async function refreshStats() {
  if (Date.now() - stats.at < STATS_EVERY) return;
  stats.at = Date.now();
  try {
    stats.st = await api("/api/stats");
    stats.err = "";
  } catch (e) {
    stats.err = e.message;
  }
}

async function refreshLedger() {
  try {
    ledger = await api("/api/ledger?closed=" + (ledgerAll ? 500 : 5));
    ledgerErr = "";
  } catch (e) {
    ledgerErr = e.message;
  }
}

async function refreshDashboard() {
  try {
    const [o, ib] = await Promise.all([api("/api/overview"), api("/api/inbox"), refreshStats(), refreshLedger()]);
    overview = o;
    if (view !== "dashboard") return;
    const has = (slug) => sessions.some((x) => x.slug === slug);
    const sig = JSON.stringify([o.counts, o.groups, stats.st, stats.err, ledger, ledgerErr, ledgerAll, (ledger && [...ledger.open, ...ledger.closed].flatMap((e) => e.sessions || []).map(has)) || []]);
    if (sig !== dashSig) {
      dashSig = sig;
      renderTiles($("o-tiles"), o.counts || {}, stats);
      const groups = o.groups || [];
      const moving = renderPipeline($("o-rows"), groups, go);
      $("o-empty").hidden = !!groups.length;
      $("o-flow").hidden = !groups.length;
      $("p-count").textContent = moving + " moving";
      renderLedger(has);
    }
    const items = ib.items || [];
    const isig = JSON.stringify([items, ib.merges || []]);
    if (isig !== inboxSig) {
      inbox = items;
      merges = ib.merges || [];
      inboxSig = isig;
      if (holdInbox()) inboxHeld = true;
      else renderInbox();
    }
  } catch (e) {
    toast(e.message);
  }
}

function renderLedger(has) {
  const box = $("r-rows");
  const btn = $("r-ledger");
  btn.textContent = ledgerAll ? "Close ledger" : "Open ledger";
  btn.setAttribute("aria-expanded", String(ledgerAll));
  if (!ledger) {
    box.replaceChildren();
    const l = el("div", "empty");
    if (ledgerErr) l.append(colored("err", "✗", "sym"), txt(" ledger unavailable · " + ledgerErr));
    else l.append(txt("loading…"));
    box.append(l);
    $("r-count").textContent = "";
    return;
  }
  const [op, cl] = renderRequests(box, ledger, { open: go, has, expanded: ledgerAll });
  $("r-count").textContent = op + " open · " + cl + " closed";
}

$("r-ledger").addEventListener("click", () => {
  ledgerAll = !ledgerAll;
  dashSig = "";
  refreshDashboard();
});

function renderInbox() {
  const rows = $("i-rows");
  rows.replaceChildren();
  const cnt = $("i-count");
  cnt.replaceChildren(el("b", "n", String(inbox.length)), txt(" open"));
  for (const id of [...inboxDraft.keys()]) if (!inbox.some((it) => it.id === id)) inboxDraft.delete(id);
  if (!inbox.length) rows.append(el("div", "empty", "Nothing waits for you"));
  inbox.forEach((it, i) => {
    rows.append(inboxCard(it, draftOf(inboxDraft, it), i === 0, {
      busy: () => isheet.isOpen(),
      focus: () => (inboxTyping = true),
      blur: () => {
        inboxTyping = false;
        setTimeout(resumeInbox, 300); // after a click on Answer… has landed
      },
      answer: (x) => isheet.open(x),
    }));
  });
  const m = $("i-merges");
  m.replaceChildren();
  if (merges.length) m.append(el("div", "label", "MERGES BY PEKOED (ON YOUR \"MERGE\")"), ...merges.map(mergeRow));
  m.hidden = !merges.length;
}

const holdInbox = () => inboxTyping || isheet.isOpen();

function resumeInbox() {
  if (inboxHeld && !holdInbox()) {
    inboxHeld = false;
    renderInbox();
  }
}

// ---- the detail view: a session, or tapi's screen ----

const tapiUp = () => !!tapi && tapi.started && tapi.alive;

function detailTarget() {
  if (current === TAPI) return tapiUp() ? tapi : null;
  return sessions.find((x) => x.slug === current);
}

const answers = answerSheet({
  close: () => (busy = false),
  done: () => refresh(),
});

function confirmAnswer(s, answer) {
  busy = true;
  answers.open(s, answer);
  $("sheet-ok").textContent = { allow: "Allow once", always: "Always allow", deny: "Deny" }[answer] || cap(answer);
  $("sheet-ok").classList.toggle("primary", answer !== "deny"); // a deny is never the main button
}

function renderDetail() {
  const isTapi = current === TAPI;
  const s = detailTarget();
  renderHead(s);
  renderInfo(s);
  renderTarget(s);
  renderKeypad(s);
  renderInterrupt(s);
  renderPane();
  renderStatus();
  if (busy) return; // the sheet is open: keep what it was opened from
  renderAsk(s, isTapi);
}

function renderHead(s) {
  const h = $("d-head");
  h.replaceChildren();
  if (current === TAPI) {
    h.append(el("span", "s-name", "tapi"), el("span", "s-role", "the orchestrator · claude"));
    const st = el("span", "s-state");
    st.append(...tapiState());
    h.append(st);
    return;
  }
  if (!s) {
    h.append(el("span", "s-name", current || ""));
    const st = el("span", "s-state");
    st.append(colored("err", "✗", "sym"), el("span", "word", " Gone"));
    h.append(st);
    return;
  }
  h.append(el("span", "s-name", s.branch || s.slug), el("span", "s-role", s.repo + " · " + s.agent));
  const st = el("span", "s-state");
  st.append(...stateBits(s, readySet(), true));
  h.append(st);
}

// renderInfo: what pekoed knows of it, as data (the right panel). Only
// what /api/sessions carries: usage, cost and the background shells are
// not in the API (docs/gui-design.md: 段階 2).
function renderInfo(s) {
  const box = $("d-info");
  box.replaceChildren();
  const sec = (label, ...rows) => {
    const rs = rows.filter(Boolean);
    if (!rs.length) return;
    const g = el("section", "info-sec");
    g.append(el("h2", "label", label), ...rs);
    box.append(g);
  };
  const row = (k, ...v) => {
    const r = el("div", "info-row");
    r.append(el("span", "ik", k));
    const vv = el("span", "iv");
    vv.append(...v);
    r.append(vv);
    return r;
  };
  if (current === TAPI) {
    sec("ABOUT", row("role", txt("orchestrator · reads every repository, starts sessions and hands work to them")));
    if (tapi) sec("WAITING ON YOU", row("inbox", txt(String(tapi.inbox_open))), row("requests", txt(tapi.ledger_open + " open")));
    if (tapi && tapi.answerable) sec("NOTICES", row("", colored("warn", "◉", "sym"), txt(" A permission prompt: answer it in the terminal (pekoe TUI)")));
    return;
  }
  if (!s) {
    sec("NOTICES", row("", colored("err", "✗", "sym"), txt(" " + current + " is gone")));
    return;
  }
  sec("SESSION", row("slug", txt(s.slug)), row("repo", txt(s.repo)), s.branch !== s.slug ? row("branch", txt(s.branch)) : null, row("agent", txt(s.agent)));
  const d = diffBits(s.diffstat);
  const test = {
    pass: [colored("ok", "✓", "sym"), txt(" pass")], fail: [colored("err", "✗", "sym"), txt(" fail")],
    running: [colored("dim", "◌", "sym"), txt(" running")], blocked: [colored("warn", "!", "sym"), txt(" not run: no sandbox")],
    error: [colored("warn", "!", "sym"), txt(" could not run")],
  }[s.test];
  sec("CHANGES", row("diff", d || txt("none")), test ? row("test", ...test) : null,
    s.pr ? row("PR", ...(s.pr_look ? [colored(s.pr_look.role, s.pr_look.icon, "sym"), txt(" ")] : []), txt("#" + s.pr + (s.pr_look ? " " + s.pr_look.word : ""))) : null,
    s.state === "running" && s.last_tool ? row("now", colored("ok", "◉", "sym"), txt(" " + s.last_tool)) : null);
  const fg = flows().find(({ f }) => f.slug === s.slug);
  if (fg) {
    const linked = [row("from", txt(parentLabel(fg.g)))];
    if (fg.g.request) {
      linked.push(row("request", txt(requestId(fg.g.request.id) + " · " + fg.g.request.status)));
      if (fg.g.request.text) linked.push(el("div", "info-text", fg.g.request.text));
    }
    sec("LINKED", ...linked);
  }
  sec("NOTICES",
    s.error ? row("", colored("err", "✗", "sym"), txt(" " + s.error.split("\n")[0])) : null,
    s.git_blocked ? row("", colored("warn", "!", "sym"), txt(" .git changed: git stopped")) : null,
    s.state === "queued" && s.queue ? row("", colored("dim", "◌", "sym"), txt(" " + s.queue)) : null,
    s.stopped ? row("", colored("dim", "◦", "sym"), txt(" stopped")) : null);
}

// renderAsk: the card that replaces the composer while the agent waits
// on a permission (決まったこと 10: it takes over the gradient frame).
// allow / always / deny each go through the confirm sheet.
function renderAsk(s, isTapi) {
  const ask = $("d-ask");
  ask.replaceChildren();
  const waiting = !!s && s.state === "waiting" && s.wait_kind !== "question";
  ask.hidden = !waiting;
  $("d-send").hidden = waiting && (isTapi ? tapi.answerable : s.answerable);
  // the composer stays for a question ("Type something else"), without
  // the frame: the card is the one thing to answer now
  $("d-send").classList.toggle("focus-card", !waiting && !questionUp(s));
  if (!waiting) return;
  const t = isTapi ? null : s.pending_tool;
  const head = el("div", "wc-head");
  head.append(colored("warn", "◉", "sym"), el("span", "wc-title", s.answerable || (isTapi && tapi.answerable) ? "Permission needed" : "Waiting"),
    el("span", "dim", (t ? t.name + " · " : "") + "wait " + ago(s.state_since)));
  ask.append(head);
  if (t && t.input) ask.append(el("pre", "target cmd", t.input));
  const cut = cutNote(t);
  if (cut) ask.append(warnLine(cut));
  if (t && t.also_in && t.also_in.length) ask.append(warnLine(t.touches + " also changed in " + t.also_in.join(", ")));
  if (s.last_message) ask.append(el("div", "wc-msg", s.last_message));
  if (isTapi) {
    if (tapi.answerable) ask.append(el("div", "wc-msg", "Answer it in the terminal (pekoe TUI): tapi's permissions are not answered here. Send and the keys are off until then."));
    return;
  }
  if (!s.answerable) return;
  const row = el("div", "actions");
  for (const [answer, key, word, cls] of [["allow", "y", "Allow once", "btn lg primary"], ["always", "A", "Always allow", "btn lg"], ["deny", "n", "Deny", "btn lg"]]) {
    const b = el("button", cls);
    b.type = "button";
    b.append(el("span", "k" + (answer === "deny" ? " r-err" : ""), key), txt(" " + word));
    if (answer === "always") b.title = "the agent's \"don't ask again\" (the TUI's A)";
    b.addEventListener("click", () => confirmAnswer(s, answer));
    row.append(b);
  }
  ask.append(row);
}

function warnLine(text) {
  const w = el("div", "wc-msg");
  w.append(colored("warn", "⚠"), txt(" " + text));
  return w;
}

// renderTarget: where send goes, on the composer: the same `current`
// doSend posts to.
function renderTarget(s) {
  const isTapi = current === TAPI;
  const to = $("d-to"), note = $("d-send-note");
  to.classList.toggle("gone", !s);
  let why = "";
  if (isTapi) {
    $("d-to-slug").textContent = "tapi";
    $("d-to-repo").textContent = s ? "orchestrator · all repositories" : tapi && tapi.started ? "has exited" : "is not started";
    if (tapi && !tapi.sendable) why = "pekoed does not take a send to tapi here";
    else if (s && s.answerable) why = "tapi waits for a permission: answer it in the terminal (pekoe TUI) before sending";
    $("d-text").placeholder = "ask tapi";
  } else {
    $("d-to-slug").textContent = current || "";
    $("d-to-repo").textContent = s ? [s.repo, s.branch !== s.slug ? s.branch : ""].filter(Boolean).join(" · ") : "is gone";
    // a permission menu takes the first character as its choice: pekoed
    // refuses a send until it is answered (409)
    if (s && s.answerable) why = "waits for a permission: answer it above first";
    $("d-text").placeholder = "send a short instruction";
  }
  note.textContent = why;
  note.hidden = !why;
  $("d-send-btn").disabled = !s || !!why || sending || preparing;
  $("d-shell").disabled = !s || !!why;
  // / and @ list the session's commands and files: not tapi's (its
  // directory is pekoe's, not a repository)
  for (const id of ["d-cmds", "d-files"]) {
    $(id).hidden = isTapi;
    $(id).disabled = !s || !!why;
  }
  $("d-attach").classList.toggle("off", preparing || attached(current).length >= IMG_MAX || !!why);
  renderDelivery($("d-delivery"), current);
}

// ---- the screen, kept still while text in it is selected or paused ----

let screenSig = "", screenPaused = false, screenHeld = false;
const scr = { ok: 0, fail: "" }; // freshness: when the screen last came, why not

// renderFresh: how fresh the screen is (決まったこと 8). The polling
// stays fixed (2.5 s), so there is no "idle · checking every …".
function renderFresh() {
  const f = $("d-fresh");
  f.replaceChildren();
  $("d-pause").textContent = screenPaused ? "Resume" : "Pause";
  $("d-pause").setAttribute("aria-pressed", String(screenPaused));
  if (view !== "detail") return;
  const since = (t) => ago(new Date(t).toISOString());
  if (screenPaused) f.append(colored("warn", "❚❚", "sym"), txt(" paused" + (scr.ok ? " · screen from " + since(scr.ok) + " ago" : "")));
  else if (screenHeld) f.append(colored("dim", "◦", "sym"), txt(" held while you select"));
  else if (scr.fail) f.append(colored("err", "✗", "sym"), txt(" stale · " + (scr.ok ? "last screen " + since(scr.ok) + " ago" : "no screen yet") + " · retrying"));
  else if (scr.ok) f.append(colored("ok", "◉", "sym"), txt(" live · updated " + since(scr.ok) + " ago"));
  if (scr.fail) f.title = scr.fail;
  else f.removeAttribute("title");
}

const screenBox = () => $("d-screen-scroll");
const atScreenBottom = () => {
  const b = screenBox();
  return b.scrollTop + b.clientHeight >= b.scrollHeight - 24;
};

async function refreshScreen() {
  if (!current || screenPaused || view !== "detail") return;
  const pre = $("d-screen");
  if (selectingIn(pre)) {
    if (!screenHeld) {
      screenHeld = true;
      renderFresh();
    }
    return;
  }
  const slug = current;
  try {
    const r = await api(apiBase(slug) + "/screen");
    if (slug !== current || screenPaused) return;
    if (selectingIn(pre)) return;
    scr.ok = Date.now();
    scr.fail = "";
    const sig = slug + "\n" + JSON.stringify([r.rows, r.lines, r.dead]);
    if (sig !== screenSig) {
      screenSig = sig;
      const stick = atScreenBottom();
      pre.replaceChildren(termLines(screenRows(r)));
      if (r.dead) pre.append("\n", el("span", "f-dim", "[agent exited]"));
      if (stick) screenBox().scrollTop = screenBox().scrollHeight;
    }
  } catch (e) {
    if (slug === current) {
      scr.fail = e.message;
      if (!scr.ok) pre.textContent = "(" + e.message + ")";
    }
  }
  renderFresh();
}

document.addEventListener("selectionchange", () => {
  if (screenHeld && !screenPaused && !selectingIn($("d-screen"))) {
    screenHeld = false;
    renderFresh();
    refreshScreen();
  }
  if (tchat.c.held && !selectingIn($("c-log"))) tchat.render(true);
  if (dchat.c.held && !selectingIn($("d-log"))) dchat.render(true);
});
screenBox().addEventListener("scroll", () => ($("d-follow").hidden = atScreenBottom()), { passive: true });
$("d-follow").addEventListener("click", () => {
  screenBox().scrollTop = screenBox().scrollHeight;
  $("d-follow").hidden = true;
});
$("d-pause").addEventListener("click", () => {
  screenPaused = !screenPaused;
  screenHeld = false;
  renderFresh();
  if (!screenPaused) refreshScreen();
});
$("d-refresh").addEventListener("click", () => {
  screenSig = "";
  refresh();
  refreshScreen();
});

// ---- Chat or Screen: the transcript as bubbles, or the agent's screen ----
// Screen is the default (docs/gui-design.md 「実装で決めたこと」): the
// permission and the question are judged on the screen, and the
// transcript has no tool results. Chat is a choice this tab remembers
// (sessionStorage, as the theme). While the agent waits for a permission
// or a question, the screen shows whatever was chosen; a chat that cannot
// be read (not Claude Code, no transcript yet) falls back to the screen
// with why, and comes back once it can be read.

const PANE_KEY = "pekoe-gui-pane";
let paneWant = "screen";
try {
  if (tabStore().getItem(PANE_KEY) === "chat") paneWant = "chat";
} catch {}

const dchat = chatPane({
  log: $("d-log"), head: $("d-earlier"), body: $("d-items"), jump: $("d-jump"),
  url: (k) => (k === TAPI ? "/api/tapi/transcript" : apiBase(k) + "/transcript"),
  name: () => (current === TAPI ? "tapi" : (detailTarget() || {}).agent || "agent"),
  shown: () => view === "detail" && paneMode().mode === "chat",
});

// paneMode: what the pane shows now, and why when it is not what was chosen
function paneMode() {
  if (paneWant !== "chat") return { mode: "screen", why: "" };
  const s = detailTarget();
  if (s && (s.answerable || questionUp(s))) return { mode: "screen", why: "the screen while the agent waits for you" };
  if (dchat.c.ok === false) return { mode: "screen", why: "chat unavailable · " + dchat.c.why };
  return { mode: "chat", why: "" };
}

function renderPane() {
  if (view !== "detail") return;
  const { mode, why } = paneMode();
  const chat = mode === "chat";
  $("d-mode-chat").setAttribute("aria-pressed", String(paneWant === "chat"));
  $("d-mode-screen").setAttribute("aria-pressed", String(paneWant !== "chat"));
  $("d-screen-scroll").hidden = chat;
  $("d-chat").hidden = !chat;
  $("d-pause").hidden = chat;
  if (chat) $("d-follow").hidden = true;
  const note = $("d-pane-note");
  note.textContent = chat ? "from the transcript · no tool output" : why || "last 80 lines";
  note.className = why ? "pane-why" : "dim";
  note.title = why;
  if (chat) dchat.render(false);
}

// refreshPaneChat: read even while the screen stands in for the chat
// (a wait, a chat that could not be read): back to it once it can be
async function refreshPaneChat() {
  if (view !== "detail" || paneWant !== "chat") return;
  await dchat.refresh(true);
  renderPane();
}

function setPane(want) {
  if (want === paneWant) return;
  paneWant = want;
  try {
    tabStore().setItem(PANE_KEY, want);
  } catch {}
  if (want === "chat") {
    dchat.reset(current);
    renderPane();
    dchat.refresh().then(() => {
      renderPane();
      dchat.toBottom();
    });
  } else {
    renderPane();
    screenSig = "";
    refreshScreen();
  }
  schedule();
}
$("d-mode-chat").addEventListener("click", () => setPane("chat"));
$("d-mode-screen").addEventListener("click", () => setPane("screen"));

// ---- the send box's / Commands and @ Files (picker.js): only write into it ----

const pick = picker({ ta: $("d-text"), box: $("d-pick"), slug: () => (current && current !== TAPI ? current : "") });
$("d-cmds").addEventListener("click", () => pick.start("cmd"));
$("d-files").addEventListener("click", () => pick.start("file"));

// ---- the question card (the keypad): only while a question's menu is up ----

function renderKeypad(s) {
  const pad = $("d-keys");
  const up = questionUp(s);
  pad.hidden = !up;
  if (up) {
    $("d-keys-age").textContent = "wait " + ago(s.state_since);
    $("d-keys-q").textContent = s.last_message || "";
    $("d-keys-q").hidden = !s.last_message;
  }
  for (const b of pad.querySelectorAll("button[data-key]")) b.disabled = pressing;
}

async function pressKey(key) {
  const slug = current;
  if (!slug || pressing) return;
  const s = detailTarget();
  if (!questionUp(s)) return;
  // one key at a time: no second Down (or Enter) before pekoed sent the first
  pressing = true;
  renderKeypad(s);
  try {
    await api(apiBase(slug) + "/keys", { keys: [key] });
    setTimeout(refreshScreen, 150);
    setTimeout(refreshScreen, 700);
    if (key === "Enter" || key === "Escape") setTimeout(refresh, 700);
  } catch (e) {
    toast(slug + ": " + e.message);
    refresh();
  }
  setTimeout(() => {
    pressing = false;
    if (current) renderKeypad(detailTarget());
  }, 200);
}

for (const b of $("d-keys").querySelectorAll("button[data-key]")) b.addEventListener("click", () => pressKey(b.dataset.key));
$("d-type-else").addEventListener("click", () => $("d-text").focus());

// ---- send: words and images; a send starting with ! through the shell sheet ----

const attached = (slug) => (slug && attachments.get(slug)) || [];

function dropAttached(slug, images) {
  const left = attached(slug).filter((im) => !images.includes(im));
  if (left.length) attachments.set(slug, left);
  else attachments.delete(slug);
  if (current === slug) renderThumbs();
}

function renderThumbs() {
  const box = $("d-thumbs");
  const images = attached(current);
  thumbs(box, images, (im) => dropAttached(current, [im]));
  if (preparing) {
    box.append(el("span", "thumb-note", "reading…"));
    box.hidden = false;
  } else if (images.length) {
    box.append(el("span", "thumb-note", images.length + "/" + IMG_MAX));
  }
  if (current) renderTarget(detailTarget());
}

async function addImages(files) {
  const slug = current;
  files = files.filter((f) => !f.type || f.type.startsWith("image/"));
  if (!slug || !files.length) return;
  const room = IMG_MAX - attached(slug).length;
  if (room <= 0) {
    toast("at most " + IMG_MAX + " images in one send");
    return;
  }
  if (files.length > room) toast("at most " + IMG_MAX + " images in one send: took the first " + room);
  preparing = true;
  renderThumbs();
  const got = [];
  for (const f of files.slice(0, room)) {
    try {
      got.push(await prepareImage(f));
    } catch (e) {
      toast(e.message);
    }
  }
  preparing = false;
  if (got.length) attachments.set(slug, [...attached(slug), ...got]);
  renderThumbs();
}

// renderDelivery: what was sent last to slug and how it went (決まったこと
// 8). "delivered" is pekoed having typed it into the agent's input box
// (send answered); whether the agent has taken it in yet is not something
// pekoed knows, so there is no "queued".
function renderDelivery(box, slug) {
  const d = slug && deliveries.get(slug);
  box.replaceChildren();
  box.hidden = !d;
  if (!d) return;
  if (d.st === "sending") box.append(colored("dim", "◌", "sym"), el("span", "dl-word", " sending"));
  else if (d.st === "delivered") box.append(colored("dim", "✓", "sym"), el("span", "dl-word", " delivered " + hm(new Date(d.at).toISOString())));
  else {
    box.append(colored("err", "✗", "sym"), el("span", "dl-word", " not sent · " + d.why));
    const b = el("button", "btn xs", "Retry");
    b.type = "button";
    b.addEventListener("click", () => doSend(slug === TAPI && view === "dashboard" ? "tapi" : "detail"));
    box.append(b);
  }
  if (d.text) box.append(el("span", "dl-text", d.text.split("\n")[0]));
}

let tsend = null; // {slug, text, images, ta} in the send or shell sheet

// doSend: from the session's composer ("detail") or the Dashboard's tapi
// panel ("tapi"). The target is fixed here, from what the label shows.
function doSend(from) {
  const isPanel = from === "tapi";
  if (!isPanel) pick.close();
  const ta = isPanel ? $("c-text") : $("d-text");
  const text = ta.value.trim();
  const slug = isPanel ? TAPI : current;
  const images = isPanel ? [] : attached(slug);
  const btn = isPanel ? $("c-send-btn") : $("d-send-btn");
  if ((!text && !images.length) || !slug || sending || preparing || btn.disabled) return;
  if (slug === TAPI && !tapiUp()) return toast("tapi is not running");
  if (slug !== TAPI && !sessions.some((x) => x.slug === slug)) return toast(slug + " is gone");
  if (shellBang(text)) return openShellSheet(slug, text, images, ta);
  if (slug === TAPI && tapi.confirm_send !== false) return openSendSheet(slug, text, images, ta);
  // images go through the sheet: the target, the text and each image seen
  // once more before they leave
  if (images.length) return openSendSheet(slug, text, images, ta);
  sendTo(slug, text, [], ta);
}

function targetLabel(slug) {
  if (slug === TAPI) return "tapi (orchestrator: every repository)";
  const s = sessions.find((x) => x.slug === slug);
  return slug + (s ? " · " + s.repo + (s.branch !== s.slug ? " · " + s.branch : "") : "");
}

function openSendSheet(slug, text, images, ta) {
  tsend = { slug, text, images, ta };
  busy = true;
  const s = sessions.find((x) => x.slug === slug);
  showSendSheet(slug === TAPI, slug, s && s.repo, text, images);
  $("tsheet-ok").textContent = slug === TAPI ? "Send to tapi" : "Send";
  $("tsheet-ok").classList.add("primary");
}

// openShellSheet: what runs and where, before a ! send leaves (the shell
// command runs right away, with no permission prompt)
function openShellSheet(slug, text, images, ta) {
  tsend = { slug, text, images, ta };
  busy = true;
  const where = slug === TAPI ? "tapi's" : slug + "'s";
  $("xsheet-warn").textContent = "⚠ This starts with !: Claude Code runs the rest as a shell command in " + where +
    " input box, right away and without a permission prompt. Read all of it before running it.";
  $("xsheet-to").textContent = targetLabel(slug);
  $("xsheet-cap").textContent = "text — all of it, as it will be typed (" + [...text].length + " chars, " + text.split("\n").length + " lines)";
  $("xsheet-text").textContent = text;
  $("xsheet-text").scrollTop = 0;
  $("xsheet-imgs").textContent = images.length ? images.length + " image" + (images.length > 1 ? "s" : "") + " go along (a line with each one's path is added after the text)" : "";
  $("xsheet-imgs").hidden = !images.length;
  $("xsheet-ok").textContent = "Run it in " + (slug === TAPI ? "tapi" : slug);
  $("xsheet-ok").className = "btn primary";
  $("xsheet-ok").disabled = false;
  $("xsheet").hidden = false;
  $("xsheet-cancel").focus();
}

function closeSheets() {
  $("tsheet").hidden = true;
  $("xsheet").hidden = true;
  tsend = null;
  busy = false;
}

async function sheetSend(okId) {
  if (!tsend || sending) return;
  const { slug, text, images, ta } = tsend;
  $(okId).disabled = true;
  await sendTo(slug, text, images, ta);
  closeSheets();
  refresh();
}

async function sendTo(slug, text, images, ta) {
  if (sending) return;
  sending = true;
  deliveries.set(slug, { st: "sending", text, at: Date.now() });
  renderDeliveries();
  try {
    const r = await api(apiBase(slug) + "/send", sendBody(text, images));
    if (ta === $("d-text")) drafts.delete(slug);
    dropAttached(slug, images);
    if (ta.value.trim() === text) ta.value = "";
    deliveries.set(slug, { st: "delivered", text: text || imagesNote(images.length).trim(), at: Date.now() });
    // ● until it shows up in the transcript (✓)
    const typed = (r && typeof r.text === "string" && r.text) || text;
    if (slug === TAPI && view === "dashboard") tchat.addPending(typed);
    if (view === "detail" && slug === current && paneWant === "chat") dchat.addPending(typed);
    setTimeout(refreshScreen, 600);
  } catch (e) {
    deliveries.set(slug, { st: "failed", text, at: Date.now(), why: e.message });
    toast((slug === TAPI ? "tapi" : slug) + ": " + e.message);
  }
  sending = false;
  renderDeliveries();
  if (current) renderTarget(detailTarget());
  renderChatHead();
}

function renderDeliveries() {
  if (view === "detail") renderDelivery($("d-delivery"), current);
  renderDelivery($("c-delivery"), TAPI);
}

// ---- tapi's chat on the Dashboard: the transcript as bubbles (chatpane.js) ----

const tchat = chatPane({
  log: $("c-log"), head: $("c-earlier"), body: $("c-items"), jump: $("c-jump"),
  url: () => "/api/tapi/transcript", name: () => "tapi", shown: () => view === "dashboard",
});
tchat.reset(TAPI);
let chatTailSig = "", spinTimer = 0;

function renderChatHead() {
  const st = $("c-state");
  st.replaceChildren(...tapiState());
  const ok = tapiUp() && tapi.sendable && !tapi.answerable;
  $("c-send-btn").disabled = !ok || sending;
  $("c-shell").disabled = !ok;
  $("c-text").placeholder = !tapiUp() ? "tapi is not running" : !tapi.sendable ? "pekoed does not take a send to tapi here" : tapi.answerable ? "tapi waits for a permission: answer it in the terminal first" : "ask tapi";
  renderChatTail();
}

// renderChatTail: what tapi does now, under the last bubble: working (a
// spinner), a permission prompt (answered at the terminal) or a question
// (answered in the tapi tab, with the keypad).
function renderChatTail() {
  const tail = $("c-tail");
  const t = tapiUp() ? tapi : null;
  const sig = t ? [t.state, t.wait_kind, t.answerable, t.state_since].join("|") : "-";
  const stick = tchat.atBottom();
  if (sig === chatTailSig) return;
  chatTailSig = sig;
  tail.replaceChildren();
  clearInterval(spinTimer);
  if (t && t.state === "waiting") {
    const card = el("div", "chat-wait");
    const head = el("div", "wc-head");
    head.append(colored("warn", "◉", "sym"), el("span", "wc-title", t.answerable ? "tapi asks for a permission" : "tapi asks a question"), el("span", "dim", "wait " + ago(t.state_since)));
    card.append(head);
    if (t.answerable) card.append(el("div", "wc-msg", "Answer it in the terminal (pekoe TUI). Send is off until then."));
    else {
      const a = el("a", "lnk", "Answer it in the tapi tab ›");
      a.href = "#/tapi";
      card.append(a);
    }
    tail.append(card);
  } else if (t && t.state === "running") {
    const row = el("div", "turn agent");
    const typing = el("span", "typing");
    typing.append(el("span", "spin r-ok", SPIN[0]), el("span", "r-dim think", "thinking · " + ago(t.state_since)));
    row.append(bubbleLine(typing, null));
    tail.append(row);
    let i = 0;
    const spin = typing.querySelector(".spin");
    spinTimer = setInterval(() => {
      spin.textContent = SPIN[++i % SPIN.length];
      if (i % 12 === 0) typing.querySelector(".think").textContent = "thinking · " + ago(t.state_since);
    }, 80);
  }
  if (stick) tchat.toBottom();
}

// ---- the status line: the mode, the target, the counts, the keys ----

function hint(key, action, keyRole) {
  const h = el("span", "hint");
  h.append(el("span", "k" + (keyRole ? " r-" + keyRole : ""), key), txt(" " + action));
  return h;
}

function renderStatus() {
  const isTapi = current === TAPI;
  $("st-mode").textContent = view === "dashboard" ? "DASHBOARD" : isTapi ? "TAPI" : "SESSION";
  const seg = $("st-seg");
  const s = view === "detail" ? detailTarget() : null;
  if (view === "detail") seg.textContent = (isTapi ? "tapi" : (s && (s.branch || s.slug)) || current) + (s ? " · " + (s.look ? s.look.word : s.state) : " · gone");
  else seg.textContent = "tapi · " + (!tapi ? "not readable" : !tapi.started ? "not started" : !tapi.alive ? "exited" : (tapi.look && tapi.look.word) || tapi.state);
  seg.hidden = false;
  const info = $("st-info");
  info.replaceChildren();
  const waiting = sessions.filter((x) => x.state === "waiting").length;
  if (waiting) {
    const w = el("span");
    w.append(colored("warn", "◉", "sym"), txt(" " + waiting + " waiting"));
    info.append(w);
  }
  const inb = tapi ? tapi.inbox_open : 0;
  if (inb) info.append(el("span", null, inb + " in the inbox"));
  info.append(el("span", null, sessions.length + " sessions"));
  if (listFail && listOk) {
    const f = el("span");
    f.append(colored("err", "✗", "sym"), txt(" not updated · last " + ago(new Date(listOk).toISOString()) + " ago"));
    info.append(f);
  } else if (listOk) info.append(el("span", null, "updated " + ago(new Date(listOk).toISOString()) + " ago"));
  // the keys of the view now, at most six; no global keys (docs/gui-design.md)
  const hints = $("st-hints");
  hints.replaceChildren();
  if (view === "dashboard") hints.append(hint("⌘↵", "send to tapi"));
  else if (s && s.answerable && !isTapi) hints.append(hint("y", "allow"), hint("A", "always"), hint("n", "deny", "err"));
  else if (questionUp(s)) hints.append(hint("1–9", "choose"), hint("⌘↵", "send words"));
  else hints.append(hint("⌘↵", "send"));
  if (view === "detail" && !interruptWhy(s, isTapi)) hints.append(hint("esc", "interrupt"));
}

// ---- New session, tapi's Reset, Interrupt (actions.js): each through #csheet ----

let csheet = null; // {run, back}: what OK does, what Cancel goes back to

// openConfirm shows w (actions.js's words) in #csheet. run() does it; it
// may return a function to call once the sheet is closed. back: Cancel
// goes back there (the New session form) instead of closing.
function openConfirm(w, run, back) {
  csheet = { run, back };
  busy = true;
  $("csheet").querySelector(".sheet").classList.toggle("danger", !!w.danger);
  $("csheet-title").textContent = w.title;
  $("csheet-to-cap").textContent = w.toCap;
  $("csheet-to").textContent = w.to;
  const box = $("csheet-facts");
  box.replaceChildren();
  for (const f of w.facts) box.append(el("div", "sheet-cap", f.cap), el(f.pre ? "pre" : "div", f.pre ? "input whole" : "sheet-fact", f.text));
  $("csheet-warn").textContent = w.warn;
  $("csheet-warn").hidden = !w.warn;
  $("csheet-cancel").textContent = back ? "Back" : "Cancel";
  const ok = $("csheet-ok");
  ok.textContent = w.ok;
  ok.className = "btn" + (w.danger ? "" : " primary"); // a destructive one is never the main button
  ok.disabled = false;
  $("csheet").hidden = false;
  (w.focusOk ? ok : $("csheet-cancel")).focus();
}

function closeConfirm() {
  const c = csheet;
  $("csheet").hidden = true;
  csheet = null;
  busy = false;
  if (c && c.back) c.back();
}

async function confirmRun() {
  const c = csheet;
  if (!c || $("csheet-ok").disabled) return;
  $("csheet-ok").disabled = true;
  let after = null;
  try {
    after = await c.run();
  } catch (e) {
    toast(e.message);
  }
  $("csheet").hidden = true;
  csheet = null;
  busy = false;
  if (typeof after === "function") after();
  refresh();
}

// New session: the form (#nsheet), then the confirm sheet. The fields keep
// what was typed until a session is made.
let repos = [];

function newErr(text) {
  $("n-err").textContent = text ? "⚠ " + text : "";
  $("n-err").hidden = !text;
}

function showNewForm(err) {
  busy = true;
  newErr(err || "");
  $("nsheet").hidden = false;
  $(err ? "n-branch" : $("n-branch").value ? "n-prompt" : "n-branch").focus();
}

async function openNew() {
  if (sheetOpen()) return;
  showNewForm("");
  $("n-branch").focus();
  const sel = $("n-repo");
  const was = sel.value;
  sel.disabled = true;
  sel.replaceChildren(el("option", null, "loading…"));
  try {
    repos = (await api("/api/repos")).repos || [];
  } catch (e) {
    repos = [];
    newErr("cannot list the repositories: " + e.message);
  }
  sel.replaceChildren(...repos.map((r) => {
    const o = el("option", null, r.name + " · " + r.dir);
    o.value = r.repo_id;
    return o;
  }));
  sel.disabled = !repos.length;
  if (!repos.length && $("n-err").hidden) newErr("pekoe knows no repository yet: make its first session from the TUI or with pekoe new");
  // what was picked before, else the repository of the session in view
  const cur = current && current !== TAPI ? sessions.find((x) => x.slug === current) : null;
  const pick = repos.find((r) => r.repo_id === was) || (cur && repos.find((r) => r.name === cur.repo)) || repos[0];
  if (pick) sel.value = pick.repo_id;
}

function closeNew() {
  $("nsheet").hidden = true;
  busy = false;
}

$("nform").addEventListener("submit", (e) => {
  e.preventDefault();
  const repo = repos.find((r) => r.repo_id === $("n-repo").value);
  const branch = $("n-branch").value.trim();
  const prompt = $("n-prompt").value.trim() ? $("n-prompt").value : "";
  const why = newWhy(repo, branch);
  if (why) return newErr(why);
  $("nsheet").hidden = true;
  openConfirm(newSheet(repo, branch, prompt), async () => {
    try {
      const s = await api("/api/sessions", { repo_id: repo.repo_id, branch, prompt });
      $("n-branch").value = "";
      $("n-prompt").value = "";
      toast("created " + s.slug, true);
      return () => refresh().then(() => go(s.slug));
    } catch (e) {
      return () => showNewForm(e.message);
    }
  }, () => showNewForm(""));
});
$("n-cancel").addEventListener("click", closeNew);
$("nsheet").addEventListener("click", (e) => { if (e.target === $("nsheet")) closeNew(); });
$("new-btn").addEventListener("click", openNew);

// tapi's Reset (the TUI's T, pekoe tapi reset)
function openReset() {
  if (sheetOpen()) return;
  openConfirm(resetSheet(tapi), async () => {
    tapi = await api("/api/tapi/reset", {});
    tchat.reset(TAPI);
    if (current === TAPI) dchat.reset(TAPI);
    chatTailSig = "";
    toast("tapi chat reset", true);
    return () => {
      tchat.render(true);
      renderChatHead();
      renderPane();
    };
  });
}
$("c-reset").addEventListener("click", openReset);

// Interrupt: Escape once into the agent in view (the TUI's esc while typing)
function interruptTarget() {
  const isTapi = current === TAPI;
  return { isTapi, t: isTapi ? tapi : detailTarget() };
}

function renderInterrupt(s) {
  const b = $("d-interrupt");
  const why = interruptWhy(s, current === TAPI);
  b.disabled = !!why;
  b.title = why ? "Interrupt: " + why : "stop the agent's turn: Escape once, after a confirmation";
}

function openInterrupt() {
  if (view !== "detail" || sheetOpen()) return;
  const { isTapi, t } = interruptTarget();
  const slug = current;
  const why = interruptWhy(t, isTapi);
  if (why) return toast((isTapi ? "tapi" : slug) + ": " + why);
  const w = interruptSheet(isTapi ? "tapi" : t.branch || slug, isTapi);
  if (!isTapi) w.to = targetLabel(slug);
  w.focusOk = true; // esc, then ↵: the sheet is the confirmation
  openConfirm(w, async () => {
    await api(apiBase(slug) + "/interrupt", {});
    toast("interrupted " + (isTapi ? "tapi" : slug), true);
    setTimeout(refreshScreen, 300);
  });
}
$("d-interrupt").addEventListener("click", openInterrupt);
$("csheet-cancel").addEventListener("click", closeConfirm);
$("csheet-ok").addEventListener("click", confirmRun);
$("csheet").addEventListener("click", (e) => { if (e.target === $("csheet")) closeConfirm(); });

// ---- routing & polling ----

function go(slug) {
  location.hash = slug === TAPI ? "#/tapi" : "#/s/" + encodeURIComponent(slug);
}

function route() {
  const m = location.hash.match(/^#\/s\/(.+)$/);
  const ta = $("d-text");
  if (current) {
    if (ta.value) drafts.set(current, ta.value);
    else drafts.delete(current);
  }
  current = location.hash === "#/tapi" ? TAPI : m ? decodeURIComponent(m[1]) : null;
  view = current ? "detail" : "dashboard";
  if (current && current !== TAPI && !openTabs.includes(current)) {
    openTabs.push(current);
    if (openTabs.length > 8) openTabs.splice(openTabs.findIndex((x) => x !== current), 1);
  }
  inboxTyping = false;
  ta.value = (current && drafts.get(current)) || "";
  $("dashboard").hidden = view !== "dashboard";
  $("detail").hidden = view !== "detail";
  renderSide();
  renderTabs();
  pick.close();
  if (view === "detail") {
    dchat.reset(current);
    if (paneWant === "chat") dchat.refresh().then(() => {
      renderPane();
      dchat.toBottom();
    });
    $("d-screen").textContent = "";
    screenSig = "";
    scr.ok = 0;
    scr.fail = "";
    screenPaused = screenHeld = false;
    $("d-follow").hidden = true;
    renderThumbs();
    renderDetail();
    renderFresh();
    refreshScreen();
    if (!overview) api("/api/overview").then((o) => {
      overview = o;
      if (view === "detail") renderDetail();
    }).catch(() => {});
  } else {
    dashSig = inboxSig = "";
    tchat.c.sig = chatTailSig = "";
    refreshDashboard().then(renderSide);
    tchat.refresh().then(() => view === "dashboard" && tchat.toBottom());
    renderChatHead();
    if (location.hash === "#/inbox") $("inbox").scrollIntoView();
  }
  renderStatus();
  schedule();
}

function schedule() {
  clearInterval(listTimer);
  clearInterval(screenTimer);
  clearInterval(chatTimer);
  clearInterval(tickTimer);
  if (document.hidden) return;
  listTimer = setInterval(refresh, 3000);
  if (view === "detail") screenTimer = setInterval(refreshScreen, 2500);
  if (view === "dashboard") chatTimer = setInterval(tchat.refresh, 2000);
  if (view === "detail" && paneWant === "chat") chatTimer = setInterval(refreshPaneChat, 2000);
  // the "… ago" words move with the clock, not with the polls
  tickTimer = setInterval(() => {
    renderFresh();
    renderStatus();
    renderDaemon();
  }, 1000);
}

// ---- keys: only while no text box has the focus (docs/gui-design.md) ----
// ":" the filter; on a permission card y / A / n open the confirm sheet
// (never answer by themselves); on a question card 1–9 go to its menu.
// n New session (unless n is a permission card's deny), T tapi's Reset, esc
// Interrupt on a session that works: each opens its confirm sheet.

const typing = (e) => {
  const t = e.target;
  return t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
};
const sheetOpen = () => ["sheet", "isheet", "tsheet", "xsheet", "nsheet", "csheet"].some((id) => !$(id).hidden);

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") {
    // Escape closes a sheet (cancel); it never answers
    if (!$("sheet").hidden) answers.close();
    else if (!$("isheet").hidden) isheet.close();
    else if (!$("tsheet").hidden || !$("xsheet").hidden) closeSheets();
    else if (!$("csheet").hidden) closeConfirm();
    else if (!$("nsheet").hidden) closeNew();
    else if (!typing(e) && !e.isComposing && view === "detail") {
      const { isTapi, t } = interruptTarget();
      if (!interruptWhy(t, isTapi)) {
        e.preventDefault();
        openInterrupt();
      }
    }
    return;
  }
  if (typing(e) || sheetOpen() || e.metaKey || e.ctrlKey || e.altKey || e.isComposing) return;
  if (e.key === ":") {
    e.preventDefault();
    $("q").focus();
    return;
  }
  if (e.key === "T") {
    e.preventDefault();
    openReset();
    return;
  }
  const s = view === "detail" ? detailTarget() : null;
  if (s && current !== TAPI && s.answerable && !busy) {
    const answer = { y: "allow", A: "always", n: "deny" }[e.key];
    if (answer) {
      e.preventDefault();
      confirmAnswer(s, answer);
      return;
    }
  } else if (questionUp(s) && /^[1-9]$/.test(e.key)) {
    e.preventDefault();
    pressKey(e.key);
    return;
  }
  if (e.key === "n") {
    e.preventDefault();
    openNew();
  }
});

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) {
    refresh();
    if (view === "detail") {
      refreshScreen();
      if (paneWant === "chat") refreshPaneChat();
    } else tchat.refresh();
  }
  schedule();
});
window.addEventListener("hashchange", route);
$("d-send").addEventListener("submit", (e) => {
  e.preventDefault();
  doSend("detail");
});
$("c-send").addEventListener("submit", (e) => {
  e.preventDefault();
  doSend("tapi");
});
for (const id of ["d-text", "c-text"]) {
  $(id).addEventListener("keydown", (e) => {
    if (id === "d-text" && pick.key(e)) return; // the / and @ list takes ↑ ↓ ↵ Tab esc while open
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.isComposing) {
      e.preventDefault();
      doSend(id === "c-text" ? "tapi" : "detail");
    }
  });
}
// ! Shell: the line starts with "!" (Claude Code's shell mode); the sheet
// shows it again before it goes
for (const [btn, ta] of [["d-shell", "d-text"], ["c-shell", "c-text"]]) {
  $(btn).addEventListener("click", () => {
    const t = $(ta);
    if (!shellBang(t.value)) t.value = "!" + t.value.replace(/^\s+/, "");
    t.focus();
    t.setSelectionRange(t.value.length, t.value.length);
  });
}
$("d-text").addEventListener("input", () => renderTarget(detailTarget()));
// images: chosen, pasted (a screenshot on the clipboard) or dropped
$("d-file").addEventListener("change", (e) => {
  const files = [...(e.target.files || [])];
  e.target.value = "";
  addImages(files);
});
$("d-text").addEventListener("paste", (e) => {
  const files = [...((e.clipboardData && e.clipboardData.files) || [])];
  if (!files.length) return;
  e.preventDefault();
  addImages(files);
});
$("d-send").addEventListener("dragover", (e) => {
  e.preventDefault();
  $("d-send").classList.add("is-drop-target");
});
$("d-send").addEventListener("dragleave", () => $("d-send").classList.remove("is-drop-target"));
$("d-send").addEventListener("drop", (e) => {
  e.preventDefault();
  $("d-send").classList.remove("is-drop-target");
  addImages([...((e.dataTransfer && e.dataTransfer.files) || [])]);
});
// a file dropped anywhere else: not opened by the browser (drop.js)
guardPageDrops(window);
$("tsheet-cancel").addEventListener("click", closeSheets);
$("tsheet-ok").addEventListener("click", () => sheetSend("tsheet-ok"));
$("tsheet").addEventListener("click", (e) => { if (e.target === $("tsheet")) closeSheets(); });
$("xsheet-cancel").addEventListener("click", closeSheets);
$("xsheet-ok").addEventListener("click", () => sheetSend("xsheet-ok"));
$("xsheet").addEventListener("click", (e) => { if (e.target === $("xsheet")) closeSheets(); });

guiSignIn().then(() => {
  refresh().then(route);
}).catch(() => {});
