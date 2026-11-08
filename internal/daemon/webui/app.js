// pekoe web: a display layer over pekoed's /api/. Everything the page shows is
// put into the DOM with textContent (agent output is untrusted text).
"use strict";

const $ = (id) => document.getElementById(id);
const ROLES = new Set(["ok", "warn", "err", "primary", "secondary", "dim", "text"]);

let sessions = [];
let current = null; // slug shown in the detail view
let listTimer = 0, screenTimer = 0;
let busy = false;
let sending = false; // a send is in flight
let touching = false; // a finger is on the list: do not move rows under it
const drafts = new Map(); // slug → unsent text, so one session's words never ride along to another

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

// colored: text in a theme role (a class, never a style from the wire)
function colored(role, text, cls) {
  return el("span", (cls ? cls + " " : "") + "r-" + (ROLES.has(role) ? role : "dim"), text);
}

async function api(path, body) {
  const opt = { headers: {} };
  if (body !== undefined) {
    opt.method = "POST";
    opt.headers["Content-Type"] = "application/json";
    opt.headers["X-Pekoe-Web"] = "1";
    opt.body = JSON.stringify(body);
  }
  const r = await fetch(path, opt);
  if (!r.ok) {
    const t = (await r.text()).trim();
    const e = new Error(t || r.status + " " + r.statusText);
    e.status = r.status;
    throw e;
  }
  return r.json();
}

function toast(text, ok) {
  const t = $("toast");
  t.replaceChildren(el("span", "ic", ok ? "✓" : "✗"), document.createTextNode(text));
  t.className = "toast" + (ok ? " ok" : "");
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.hidden = true), ok ? 1800 : 4000);
}

// ago is theme.Ago: hours up to two days
function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return Math.floor(s) + "s";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 48 * 3600) return Math.floor(s / 3600) + "h";
  return Math.floor(s / 86400) + "d";
}

// look is the TUI's (theme.Look, sent by pekoed): symbol, role, word
function look(s) {
  return s.look || { icon: "○", role: "dim", word: s.state };
}

const sep = () => el("span", "sep", "·");

// details: the dim second line, with the colored bits the TUI colors
function details(s, withRepo) {
  const out = [];
  const add = (...nodes) => {
    if (out.length) out.push(sep());
    out.push(...nodes);
  };
  if (withRepo) add(document.createTextNode(s.repo));
  if (s.slug !== s.branch) add(document.createTextNode(s.slug));
  const d = s.diffstat;
  if (d && d.added + d.deleted > 0) add(colored("ok", "+" + d.added), document.createTextNode(" "), colored("err", "-" + d.deleted));
  if (s.test === "pass") add(colored("ok", "✓"), document.createTextNode("test"));
  else if (s.test === "fail") add(colored("err", "✗"), document.createTextNode("test"));
  else if (s.test === "running") add(document.createTextNode("…test"));
  if (s.pr) {
    const n = [document.createTextNode("#" + s.pr)];
    if (s.pr_look) n.push(document.createTextNode(" "), colored(s.pr_look.role, s.pr_look.icon));
    add(...n);
  }
  if (s.error) add(colored("err", "err"), document.createTextNode(" " + s.error.split("\n")[0]));
  else if (s.state === "running" && s.last_tool) add(colored("ok", "●"), document.createTextNode(" " + s.last_tool));
  return out;
}

// line1 is the TUI's sessionLine: icon  name  state  age (no age when idle)
function line1(s) {
  const lk = look(s);
  const dimName = s.state === "idle" || s.state === "creating" || s.state === "landed";
  const l1 = el("div", "line1");
  l1.append(
    colored(lk.role, lk.icon, "ic"),
    el("span", "name " + (dimName ? "r-dim" : "r-text"), s.branch || s.slug),
    colored(lk.role, lk.word, "word"),
    el("span", "age", s.state === "idle" ? "" : ago(s.state_since)),
  );
  return l1;
}

// ---- list ----

function sessionRow(s) {
  const b = el("button", "row-s");
  b.type = "button";
  const l2 = el("div", "line2");
  l2.append(...details(s, true));
  b.append(line1(s), l2);
  b.addEventListener("click", () => go(s.slug));
  return b;
}

// waitBody: what the agent is asking, and the buttons (allow/deny only for
// a permission prompt; a question is answered by sending words).
function waitBody(s, box) {
  const t = s.pending_tool;
  if (t) {
    const tool = el("div", "tool");
    tool.append(el("span", "ic", "?"), document.createTextNode(t.name));
    box.append(tool);
    if (t.input) box.append(el("pre", "input", t.input));
    if (t.also_in && t.also_in.length) {
      const also = el("div", "also");
      also.append(el("span", "ic", "⚠"), document.createTextNode(" " + t.touches + " also changed in " + t.also_in.join(", ")));
      box.append(also);
    }
  }
  if (s.last_message) box.append(el("div", "msg", s.last_message));
  if (s.answerable) {
    const row = el("div", "row");
    const deny = el("button", "btn deny", "✗ deny");
    const allow = el("button", "btn allow", "✓ allow");
    deny.type = allow.type = "button";
    deny.addEventListener("click", () => confirmAnswer(s, "deny"));
    allow.addEventListener("click", () => confirmAnswer(s, "allow"));
    row.append(deny, allow);
    box.append(row);
  } else if (s.wait_kind === "question") {
    box.append(el("div", "msg", "質問です。画面を見て、答えを send で送ってください。"));
  }
}

function renderList() {
  if (touching) return; // redrawn on release
  const rows = $("l-rows");
  rows.replaceChildren();
  $("l-count").textContent = sessions.length ? String(sessions.length) : "";
  if (!sessions.length) {
    rows.append(el("div", "empty", "no sessions"));
    return;
  }
  for (const s of sessions) {
    if (s.state === "waiting") {
      const card = el("div", "card");
      const body = el("div", "body");
      waitBody(s, body);
      card.append(sessionRow(s), body);
      rows.append(card);
    } else {
      rows.append(sessionRow(s));
    }
  }
}

// renderSummary: the Sessions footer (TUI: icon in its state color, words
// dim) and the count in the status line.
function renderSummary() {
  const waiting = sessions.filter((s) => s.state === "waiting");
  const done = sessions.filter((s) => s.state === "done").length;
  const running = sessions.filter((s) => s.state === "running").length;
  const foot = $("l-foot");
  foot.replaceChildren();
  if (waiting.length) {
    const oldest = waiting.reduce((a, s) => (s.state_since < a ? s.state_since : a), waiting[0].state_since);
    foot.append(colored("warn", "●"), document.createTextNode(` ${waiting.length} waiting · oldest ${ago(oldest)}`));
  }
  if (done) {
    if (foot.childNodes.length) foot.append(el("span", "gap"));
    foot.append(colored("secondary", "✓"), document.createTextNode(` ${done} to review`));
  }
  foot.hidden = !foot.childNodes.length;
  const sum = $("sum");
  sum.replaceChildren();
  const bit = (role, icon, n, word) => {
    if (!n) return;
    if (sum.childNodes.length) sum.append(document.createTextNode("  "));
    sum.append(colored(role, icon), document.createTextNode(` ${n} ${word}`));
  };
  bit("warn", "●", waiting.length, "wait");
  bit("ok", "●", running, "run");
  bit("secondary", "✓", done, "done");
  document.title = waiting.length ? `(${waiting.length}) pekoe` : "pekoe";
}

async function refresh() {
  try {
    const r = await api("/api/sessions");
    sessions = r.sessions || [];
    renderSummary();
    if (current) renderDetailHead();
    else if (!busy) renderList();
  } catch (e) {
    toast(e.message);
  }
}

// ---- detail ----

function renderDetailHead() {
  const s = sessions.find((x) => x.slug === current);
  const head = $("d-head"), ask = $("d-ask");
  head.replaceChildren();
  $("d-name").textContent = current || "";
  renderTarget(s);
  if (!s) {
    head.append(el("div", "empty", current + " is gone"));
    if (!busy) ask.hidden = true;
    return;
  }
  const l2 = el("div", "line2");
  l2.append(document.createTextNode(s.repo), sep(), document.createTextNode(s.agent), ...(details(s, false).length ? [sep(), ...details(s, false)] : []));
  head.append(line1(s), l2);
  if (busy) return; // the confirm sheet is open: keep what it was opened from
  ask.replaceChildren();
  ask.hidden = s.state !== "waiting";
  if (!ask.hidden) {
    const body = el("div", "body");
    waitBody(s, body);
    ask.append(body);
  }
}

// renderTarget: the session send goes to, next to the box. It is the same
// `current` doSend posts to, so what is shown is what is hit.
function renderTarget(s) {
  const to = $("d-to");
  $("d-to-slug").textContent = current || "";
  $("d-to-repo").textContent = s ? [s.repo, s.branch !== s.slug ? s.branch : ""].filter(Boolean).join(" · ") : "is gone";
  to.classList.toggle("gone", !s);
  $("d-send-btn").disabled = !s || sending;
}

// ---- the agent screen, with its colors ----
// pekoed sends each line as runs {t, fg, bg, b, d, i, u, s} (internal/termstyle).
// The text goes in with textContent; a color is a theme role (a class from
// this fixed list) or #rrggbb, checked here and set through CSSOM.

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
function termLines(rows) {
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
function screenRows(r) {
  if (Array.isArray(r.rows)) return r.rows;
  return (r.lines || []).map((l) => [{ t: l }]);
}

// ---- keeping the screen still while it is being copied ----
// The screen is redrawn only when its content changed, and not at all while
// text in it is selected (iOS: also while the long-press menu is up, which
// keeps a selection) or a finger is on it, or while the user paused it.
// A skipped redraw is caught up as soon as the selection goes away.

let screenSig = "";      // the slug and content now on screen
let screenPaused = false; // the pause button
let screenHeld = false;   // a redraw was skipped for a selection or a touch
let screenTouch = false;

function selectingScreen() {
  const sel = document.getSelection();
  if (!sel || sel.isCollapsed || !sel.rangeCount) return false;
  const pre = $("d-screen");
  return pre.contains(sel.anchorNode) || pre.contains(sel.focusNode);
}

function holdScreen() {
  return screenTouch || selectingScreen();
}

function renderScreenState() {
  const btn = $("d-pause"), st = $("d-live");
  btn.textContent = screenPaused ? "▶ resume" : "❚❚ pause";
  btn.setAttribute("aria-pressed", String(screenPaused));
  st.replaceChildren();
  if (screenPaused) st.append(colored("warn", "❚❚"), document.createTextNode(" paused"));
  else if (screenHeld) st.append(colored("dim", "○"), document.createTextNode(" hold · selecting"));
  else st.append(colored("ok", "●"), document.createTextNode(" live"));
}

function resumeIfHeld() {
  if (screenHeld && !screenPaused && !holdScreen()) {
    screenHeld = false;
    renderScreenState();
    refreshScreen();
  }
}

async function refreshScreen() {
  if (!current || screenPaused) return;
  if (holdScreen()) {
    if (!screenHeld) {
      screenHeld = true;
      renderScreenState();
    }
    return;
  }
  const slug = current;
  const pre = $("d-screen");
  try {
    const r = await api("/api/sessions/" + encodeURIComponent(slug) + "/screen");
    if (slug !== current || screenPaused) return; // answered after a switch or a pause
    if (holdScreen()) { // a selection started while it was on the way
      screenHeld = true;
      renderScreenState();
      return;
    }
    $("d-at").textContent = new Date().toLocaleTimeString();
    const sig = slug + "\n" + JSON.stringify([r.rows, r.lines, r.dead]);
    if (sig === screenSig) return; // nothing new: leave the DOM (and any selection) alone
    screenSig = sig;
    const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 24;
    pre.replaceChildren(termLines(screenRows(r)));
    if (r.dead) pre.append("\n", el("span", "f-dim", "[agent exited]"));
    if (atBottom) pre.scrollTop = pre.scrollHeight;
  } catch (e) {
    if (slug === current) {
      screenSig = "";
      pre.textContent = "(" + e.message + ")";
    }
  }
}

function togglePause() {
  screenPaused = !screenPaused;
  screenHeld = false;
  renderScreenState();
  if (!screenPaused) refreshScreen();
}

// ---- answer: always through a confirmation that shows what is allowed ----
// pending_tool is pekoe's one-line summary of the last tool the agent asked
// for (cut at 200 bytes); the sheet also shows the bottom of the screen,
// where the agent's own permission dialog says exactly what it will run.

let pending = null;

function confirmAnswer(s, answer) {
  busy = true;
  pending = { slug: s.slug, answer, tool: s.pending_tool || null };
  const sheet = $("sheet").querySelector(".sheet");
  sheet.classList.toggle("danger", answer === "deny");
  $("sheet-title").textContent = `${answer} · ${s.slug}`;
  $("sheet-tool").textContent = s.pending_tool ? s.pending_tool.name : "(no tool detail)";
  $("sheet-input").textContent = s.pending_tool ? s.pending_tool.input || "" : s.last_message || "";
  const ok = $("sheet-ok");
  ok.textContent = answer;
  ok.className = "btn " + answer;
  ok.disabled = false;
  const scr = $("sheet-screen");
  scr.textContent = "…";
  $("sheet").hidden = false;
  api("/api/sessions/" + encodeURIComponent(s.slug) + "/screen").then((r) => {
    if (!pending || pending.slug !== s.slug) return;
    scr.replaceChildren(termLines(screenRows(r).slice(-18)));
    scr.scrollTop = scr.scrollHeight;
  }).catch((e) => { scr.textContent = "(" + e.message + ")"; });
}

function closeSheet() {
  $("sheet").hidden = true;
  pending = null;
  busy = false;
}

async function doAnswer() {
  if (!pending) return;
  const p = pending;
  $("sheet-ok").disabled = true;
  try {
    await api("/api/sessions/" + encodeURIComponent(p.slug) + "/answer", { answer: p.answer, tool: p.tool });
    toast(`${p.answer}: ${p.slug}`, true);
  } catch (e) {
    toast(e.message);
  }
  closeSheet();
  refresh();
}

// ---- send ----

async function doSend(ev) {
  ev.preventDefault();
  const ta = $("d-text");
  const text = ta.value.trim();
  // the target is fixed here, from the same `current` the label shows
  const slug = current;
  if (!text || !slug || sending) return;
  if (!sessions.some((x) => x.slug === slug)) {
    toast(slug + " is gone");
    return;
  }
  sending = true;
  $("d-send-btn").disabled = true;
  try {
    await api("/api/sessions/" + encodeURIComponent(slug) + "/send", { text });
    drafts.delete(slug);
    if (current === slug && ta.value.trim() === text) ta.value = "";
    toast("sent → " + slug, true);
    setTimeout(refreshScreen, 600);
  } catch (e) {
    toast(slug + ": " + e.message);
  }
  sending = false;
  if (current) renderTarget(sessions.find((x) => x.slug === current));
}

// ---- routing & polling ----

function go(slug) {
  location.hash = slug ? "#/s/" + encodeURIComponent(slug) : "";
}

function route() {
  const m = location.hash.match(/^#\/s\/(.+)$/);
  const ta = $("d-text");
  if (current) {
    if (ta.value) drafts.set(current, ta.value);
    else drafts.delete(current);
  }
  current = m ? decodeURIComponent(m[1]) : null;
  ta.value = (current && drafts.get(current)) || "";
  $("list").hidden = !!current;
  $("detail").hidden = !current;
  $("back").hidden = !current;
  if (current) {
    $("d-screen").textContent = "";
    screenSig = "";
    screenPaused = screenHeld = false;
    renderScreenState();
    renderDetailHead();
    refreshScreen();
  } else {
    renderList();
  }
  schedule();
}

function schedule() {
  clearInterval(listTimer);
  clearInterval(screenTimer);
  if (document.hidden) return;
  listTimer = setInterval(refresh, 3000);
  if (current) screenTimer = setInterval(refreshScreen, 2500);
}

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) {
    refresh();
    if (current) refreshScreen();
  }
  schedule();
});
window.addEventListener("hashchange", route);
$("back").addEventListener("click", () => go(null));
$("sheet-cancel").addEventListener("click", closeSheet);
$("sheet-ok").addEventListener("click", doAnswer);
$("sheet").addEventListener("click", (e) => { if (e.target === $("sheet")) closeSheet(); });
$("d-send").addEventListener("submit", doSend);
$("d-pause").addEventListener("click", togglePause);
document.addEventListener("selectionchange", resumeIfHeld);
{
  const pre = $("d-screen");
  pre.addEventListener("pointerdown", () => (screenTouch = true));
  // a long press turns into a selection: hold on until that has settled
  const up = () => setTimeout(() => {
    screenTouch = false;
    resumeIfHeld();
  }, 600);
  document.addEventListener("pointerup", () => screenTouch && up());
  document.addEventListener("pointercancel", () => screenTouch && up());
}
// hold the list still while a finger is on it: a redraw between touch and
// click could put another session's row under the finger
const list = $("list");
const release = () => {
  if (!touching) return;
  touching = false;
  if (!current && !busy) renderList();
};
list.addEventListener("pointerdown", () => (touching = true));
// released a bit later: the click that follows pointerup must still land on
// the row the finger was on
const releaseSoon = () => setTimeout(release, 400);
document.addEventListener("pointerup", releaseSoon);
document.addEventListener("pointercancel", releaseSoon);

api("/api/me").then((me) => {
  if (me) $("who").textContent = me.login + (me.device ? " · " + me.device : "");
}).catch(() => {});
refresh().then(route);
