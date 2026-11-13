// pekoe web: a display layer over pekoed's /api/. Everything the page shows is
// put into the DOM with textContent (agent output is untrusted text).
"use strict";

const $ = (id) => document.getElementById(id);
const ROLES = new Set(["ok", "warn", "err", "primary", "secondary", "dim", "text"]);

let sessions = [];
let view = "list"; // list | inbox | overview | detail
let current = null; // slug shown in the detail view
let backTo = ""; // the hash the detail view's back button returns to
let tapi = null; // /api/tapi: tapi's state and the inbox / ledger counts
// TAPI is tapi chat in the detail view (#/tapi). No session slug starts
// with "+", so it never names a session.
const TAPI = "+tapi";
let listTimer = 0, screenTimer = 0, chatTimer = 0;
let busy = false;
let sending = false; // a send is in flight
let pressing = false; // a keypad key is in flight
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
  else if (s.test === "blocked") add(colored("warn", "!"), document.createTextNode("test not run: no sandbox"));
  if (s.git_blocked) add(colored("warn", "!"), document.createTextNode(".git changed: git stopped"));
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
// a permission prompt; a question is answered with the keypad or by
// sending words, in the session's own view).
function waitBody(s, box) {
  const t = s.pending_tool;
  if (t) {
    const tool = el("div", "tool");
    tool.append(el("span", "ic", "?"), document.createTextNode(t.name));
    box.append(tool);
    if (t.input) box.append(el("pre", "input", t.input));
    const cut = cutNote(t);
    if (cut) {
      const w = el("div", "also");
      w.append(el("span", "ic", "⚠"), document.createTextNode(" " + cut));
      box.append(w);
    }
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
    box.append(el("div", "msg", "a question: open it and answer with the keypad or send, watching the screen"));
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
    const [r, t] = await Promise.all([api("/api/sessions"), api("/api/tapi").catch(() => null)]);
    sessions = r.sessions || [];
    tapi = t;
    renderSummary();
    renderBadge();
    if (view === "detail") renderDetailHead();
    else if (view === "list" && !busy) renderList();
    else if (view === "inbox") await refreshInbox();
    else if (view === "overview") await refreshOverview();
  } catch (e) {
    toast(e.message);
  }
}

// renderBadge: the open inbox items (everyone's) on the Inbox tab
function renderBadge() {
  const b = $("t-inbox-n");
  const n = tapi ? tapi.inbox_open : 0;
  b.textContent = n ? String(n) : "";
  b.hidden = !n;
}

// ---- detail ----

// apiBase: where the detail view's screen, send and keys go
function apiBase(slug) {
  return slug === TAPI ? "/api/tapi" : "/api/sessions/" + encodeURIComponent(slug);
}

// tapiUp: tapi chat runs (its screen, keys and send make sense)
function tapiUp() {
  return !!tapi && tapi.started && tapi.alive;
}

// detailTarget: what the detail view shows: the session, or tapi
function detailTarget() {
  if (current === TAPI) return tapiUp() ? tapi : null;
  return sessions.find((x) => x.slug === current);
}

function renderDetailHead() {
  $("d-kind").textContent = current === TAPI ? "◆ tapi" : "◆ Session";
  applyTapiMode();
  if (current === TAPI) return renderTapiHead();
  const s = sessions.find((x) => x.slug === current);
  const head = $("d-head"), ask = $("d-ask");
  head.replaceChildren();
  $("d-name").textContent = current || "";
  renderTarget(s);
  if (!s) {
    renderKeypad(null);
    head.append(el("div", "empty", current + " is gone"));
    if (!busy) ask.hidden = true;
    return;
  }
  const l2 = el("div", "line2");
  l2.append(document.createTextNode(s.repo), sep(), document.createTextNode(s.agent), ...(details(s, false).length ? [sep(), ...details(s, false)] : []));
  head.append(line1(s), l2);
  renderKeypad(s);
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
  const to = $("d-to"), note = $("d-send-note");
  const isTapi = current === TAPI;
  to.classList.toggle("tapi", isTapi);
  $("d-send").classList.toggle("chat", isTapi);
  $("d-text").placeholder = !isTapi ? "send a short instruction" : tapiConfirm() ? "ask tapi (you check the whole text before it is sent)" : "ask tapi";
  if (isTapi) {
    $("d-to-slug").textContent = "tapi";
    $("d-to-repo").textContent = s ? "orchestrator · all repositories" : tapi && tapi.started ? "has exited" : "is not started";
    let why = "";
    if (tapi && !tapi.sendable) why = "sending to tapi needs [web] allow_devices in config.toml (only when devices are restricted)";
    else if (s && s.answerable) why = "tapi is waiting for a permission: answer it in the terminal (pekoe TUI) before sending";
    note.textContent = why;
    note.hidden = !why;
    to.classList.toggle("gone", !s);
    // while tapi runs the row is left out (the log gets the room): the
    // confirm sheet (or, with confirm_tapi_send = false, the chat view
    // itself) is what shows the send goes to tapi. When tapi is not
    // running it stays, to say why nothing can be sent
    to.hidden = !!s;
    $("d-send-btn").disabled = !s || !tapi.sendable || s.answerable || sending;
    return;
  }
  note.hidden = true;
  to.hidden = false;
  $("d-to-slug").textContent = current || "";
  $("d-to-repo").textContent = s ? [s.repo, s.branch !== s.slug ? s.branch : ""].filter(Boolean).join(" · ") : "is gone";
  to.classList.toggle("gone", !s);
  $("d-send-btn").disabled = !s || sending;
}

// renderTapiHead: tapi's state in the detail view. Its permission prompts
// are not answered here (no allow/deny): the card says so.
function renderTapiHead() {
  const head = $("d-head"), ask = $("d-ask");
  const t = detailTarget();
  head.replaceChildren();
  $("d-name").textContent = "tapi";
  renderTarget(t);
  renderKeypad(t);
  head.append(tapiLine());
  const l2 = el("div", "line2");
  l2.append(document.createTextNode("orchestrator · reads every repository, starts sessions and hands work to them"));
  head.append(l2);
  if (busy) return;
  ask.replaceChildren();
  // in chat, the permission card and the keypad sit under the last bubble
  ask.hidden = !t || t.state !== "waiting" || tapiMode() === "chat";
  if (ask.hidden) return;
  const body = el("div", "body");
  if (t.answerable) {
    const w = el("div", "also");
    w.append(el("span", "ic", "⚠"), document.createTextNode(" tapi is waiting for a permission. allow / deny is not offered on the web: answer it in the terminal (pekoe TUI). send and the keys are off until then."));
    body.append(w);
  } else if (t.wait_kind === "question") {
    body.append(el("div", "msg", "a question: answer with the keypad or send, watching the screen"));
  }
  if (t.last_message) body.append(el("div", "msg", t.last_message));
  ask.append(body);
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
  if (current === TAPI && tapiMode() === "chat") return; // the chat reads the transcript
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
    const r = await api(apiBase(slug) + "/screen");
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
// for (cut at 200 bytes); the sheet also shows the screen (the last 80
// lines, scrolled to the bottom, scrollable up), where the agent's own
// permission dialog says exactly what it will run.

let pending = null;

// cutNote: when the summary leaves part of the command out (cut short, or
// several lines folded into one), say how much, so a long or padded
// command is not judged by its first line.
function cutNote(t) {
  if (!t || (!t.cut && !(t.lines > 1))) return "";
  const parts = [];
  if (t.lines > 1) parts.push(t.lines + " lines");
  if (t.size) parts.push(t.size >= 1024 ? (t.size / 1024).toFixed(1) + " KB" : t.size + " bytes");
  return (t.cut ? "summary cut" : "multi-line") + " (" + parts.join(", ") + "): read the whole command on the screen";
}

function confirmAnswer(s, answer) {
  busy = true;
  pending = { slug: s.slug, answer, tool: s.pending_tool || null };
  const sheet = $("sheet").querySelector(".sheet");
  sheet.classList.toggle("danger", answer === "deny");
  $("sheet-title").textContent = `${answer} · ${s.slug}`;
  $("sheet-tool").textContent = s.pending_tool ? s.pending_tool.name : "(no tool detail)";
  $("sheet-input").textContent = s.pending_tool ? s.pending_tool.input || "" : s.last_message || "";
  const cut = cutNote(s.pending_tool);
  $("sheet-warn").hidden = !cut;
  $("sheet-warn").textContent = cut ? "⚠ " + cut : "";
  sheet.classList.toggle("long", !!cut);
  const ok = $("sheet-ok");
  ok.textContent = answer;
  ok.className = "btn " + answer;
  ok.disabled = false;
  const scr = $("sheet-screen");
  scr.textContent = "…";
  $("sheet").hidden = false;
  api("/api/sessions/" + encodeURIComponent(s.slug) + "/screen").then((r) => {
    if (!pending || pending.slug !== s.slug) return;
    // every line the screen call returns, not just the bottom: a command
    // padded with blank lines still has its start in reach by scrolling
    scr.replaceChildren(termLines(screenRows(r)));
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
  if (slug === TAPI) {
    openTapiSheet(text);
    return;
  }
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
  if (current) renderTarget(detailTarget());
}

// ---- send to tapi: through a sheet with the whole text ----
// tapi acts on every repository, so its send weighs more than one
// session's: the sheet says the target is tapi and shows every character
// that will be typed. The text is fixed when the sheet opens.
// [web] confirm_tapi_send = false (tapi.confirm_send) leaves the sheet out
// and sends at once; pekoed checks the send the same way either way. An
// older pekoed without the field gets the sheet.

let tsend = null; // the text in the sheet (or being sent without one)

function tapiConfirm() {
  return !tapi || tapi.confirm_send !== false;
}

function openTapiSheet(text) {
  if (!tapiUp()) {
    toast("tapi is not running");
    return;
  }
  if (!tapi.sendable) {
    toast("sending to tapi needs [web] allow_devices");
    return;
  }
  tsend = text;
  if (!tapiConfirm()) {
    doTapiSend();
    return;
  }
  busy = true;
  $("tsheet-cap").textContent = "text — all of it, as it will be typed (" + [...text].length + " chars, " + text.split("\n").length + " lines)";
  $("tsheet-text").textContent = text;
  $("tsheet-text").scrollTop = 0;
  $("tsheet-ok").disabled = false;
  $("tsheet").hidden = false;
}

function closeTapiSheet() {
  $("tsheet").hidden = true;
  tsend = null;
  busy = false;
}

async function doTapiSend() {
  const text = tsend;
  if (text == null || sending) return;
  $("tsheet-ok").disabled = true;
  sending = true;
  $("d-send-btn").disabled = true; // no second send while this one is out
  try {
    await api("/api/tapi/send", { text });
    drafts.delete(TAPI);
    const ta = $("d-text");
    if (current === TAPI && ta.value.trim() === text) {
      ta.value = "";
      fitComposer();
    }
    toast("sent → tapi", true);
    // ● until it shows up in the transcript (✓)
    chat.pending.push({ text, at: Date.now() });
    if (current === TAPI && tapiMode() === "chat") {
      renderChat(true);
      toBottom();
    }
    setTimeout(refreshScreen, 600);
    setTimeout(refreshChat, 600);
  } catch (e) {
    toast("tapi: " + e.message);
  }
  sending = false;
  closeTapiSheet();
  refresh();
}

// ---- keypad: a question's menu (AskUserQuestion) by its keys ----
// Shown only while the session waits on a question; pekoed refuses keys
// in any other state (a permission menu stays allow/deny only).

function questionUp(s) {
  return !!s && s.state === "waiting" && s.wait_kind === "question" && !s.stopped;
}

function renderKeypad(s) {
  const pad = $("d-keys");
  pad.hidden = !questionUp(s);
  for (const b of pad.querySelectorAll("button")) b.disabled = pressing;
}

async function pressKey(key) {
  const slug = current;
  if (!slug || pressing) return;
  const s = detailTarget();
  if (!questionUp(s)) return;
  // one key at a time: the buttons stay off until pekoed has sent it, so a
  // double tap is not two Downs (or two Enters)
  pressing = true;
  renderKeypad(s);
  try {
    await api(apiBase(slug) + "/keys", { keys: [key] });
    if (slug === TAPI) setTimeout(refreshChat, 700);
    setTimeout(refreshScreen, 150);
    setTimeout(refreshScreen, 700);
    if (key === "Enter" || key === "Escape") setTimeout(refresh, 700);
  } catch (e) {
    toast(slug + ": " + e.message);
    refresh();
  }
  // a short pause after the reply: the menu redraws before the next key
  setTimeout(() => {
    pressing = false;
    if (current) renderKeypad(detailTarget());
  }, 200);
}

// ---- inbox: what agents ask a person ----
// pekoed's facts and the links first, the agent's words after (its
// opinion, not pekoe's). The recommendation is labelled as the agent's and
// never chosen for the person. Every string goes in with textContent; a
// link becomes an anchor only when it is http(s).

let inbox = []; // open items, oldest first
let merges = []; // pekoed's merges on a person's "merge", newest first (the last day)
let inboxSig = ""; // what is on screen
let inboxHeld = false; // a redraw was skipped (a note being typed, the sheet open)
let inboxTyping = false;
const inboxDraft = new Map(); // id → {rev, choice, note, changed}: what the person picked, for the rev shown
let ianswer = null; // the answer in the confirm sheet

const KINDS = {
  decision: ["?", "secondary", "decision"],
  approve: ["!", "warn", "approve"],
  login: ["@", "primary", "login"],
  fyi: ["i", "dim", "fyi"],
};

const txt = (s) => document.createTextNode(String(s));
const short = (sha) => (sha ? String(sha).slice(0, 9) : "-");

// link: an anchor for an http(s) URL with nothing odd in it; anything else
// (javascript:, data:, a path) stays plain text
function link(url, text) {
  const u = String(url ?? "");
  if (!/^https?:\/\//i.test(u) || /[\s"<>\\]/.test(u)) return el("span", "r-dim", text ?? u);
  const a = el("a", "lnk", text ?? u);
  a.href = u;
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  return a;
}

function factRow(label, ...nodes) {
  const r = el("div", "fact");
  r.append(el("span", "fk", label), ...nodes);
  return r;
}

function factsBlock(it) {
  const box = el("div", "facts");
  box.append(el("div", "sub", "pekoe read"));
  const f = it.facts;
  if (!f) {
    box.append(el("div", "fact r-dim", "(not about a session)"));
  } else if (f.gone) {
    box.append(factRow("session", colored("warn", f.slug + " is gone")));
  } else {
    const ses = [txt(f.slug), sep(), txt(f.state)];
    if (f.branch) ses.push(sep(), txt(f.branch + (f.base ? " → " + f.base : "")));
    box.append(factRow("session", ...ses));
    const d = f.diffstat || { added: 0, deleted: 0, files: 0 };
    box.append(factRow("HEAD", txt(short(f.head)), sep(), colored("ok", "+" + d.added), txt(" "), colored("err", "−" + d.deleted), txt(" · " + d.files + " files")));
    if (f.pr) {
      const n = [f.pr_url ? link(f.pr_url, "#" + f.pr) : txt("#" + f.pr)];
      const ps = f.pr_status;
      if (ps) {
        n.push(sep(), txt(ps.state || "?"), sep(), txt("CI "), colored(ps.fail ? "dim" : "ok", ps.pass + " pass"), txt(" "),
          colored(ps.fail ? "err" : "dim", ps.fail + " fail"), txt(" " + ps.pending + " pending"));
        if (ps.review_decision) n.push(sep(), txt(ps.review_decision));
        if (ps.merge_state) n.push(sep(), txt(ps.merge_state));
        if (ps.unresolved) n.push(sep(), colored("warn", ps.unresolved + " unresolved"));
      }
      box.append(factRow("PR", ...n));
    }
    const ts = f.test && f.test.status;
    if (ts && ts !== "none") box.append(factRow("test", colored(ts === "pass" ? "ok" : ts === "fail" ? "err" : "dim", ts)));
    for (const o of f.overlaps || []) {
      box.append(factRow("forecast", o.conflict ? colored("err", "conflicts") : txt("overlaps"), txt(" with " + o.slug)));
    }
    if (f.report) box.append(factRow("report", txt("#" + f.report.id + " " + f.report.status + " · " + ago(f.report.at) + " ago")));
    if (f.git_blocked) box.append(factRow("git", colored("warn", ".git changed: git stopped")));
    if (f.merge) {
      for (const c of f.merge.conds || []) box.append(factRow(c.name, colored(c.ok ? "ok" : "err", c.ok ? "✓" : "✗"), txt(" " + c.detail)));
    }
  }
  for (const l of it.links || []) box.append(factRow("link", link(l)));
  return box;
}

// draftOf: the person's pick for it. When the item changed since it was
// drawn (its rev went up: HEAD moved, CI turned), the choice is dropped
// and the card says so, as the TUI's modal does.
function draftOf(it) {
  let d = inboxDraft.get(it.id);
  if (!d) {
    d = { rev: it.rev, choice: null, note: "", changed: false };
    inboxDraft.set(it.id, d);
  } else if (d.rev !== it.rev) {
    d.changed = d.changed || d.choice !== null;
    d.choice = null;
    d.rev = it.rev;
  }
  return d;
}

function canAnswer(it, d) {
  return (it.options || []).length ? d.choice !== null : d.note.trim() !== "";
}

function inboxCard(it) {
  const d = draftOf(it);
  const card = el("div", "card ibox");
  const [icon, role, word] = KINDS[it.kind] || ["?", "dim", it.kind];
  const head = el("div", "line1 ihead");
  head.append(colored(role, icon, "ic"), el("span", "name r-text", "#" + it.id), colored(role, word, "word"), el("span", "age", ago(it.created_at)));
  const body = el("div", "body");
  body.append(factsBlock(it));
  if (d.changed) body.append(el("div", "also r-warn", "⚠ changed while you looked: check the facts again"));

  const says = el("div", "says");
  says.append(el("div", "sub", "the agent says (its words, not pekoe's)"));
  const from = el("div", "from");
  from.append(txt("from " + it.from));
  if (it.claimed) from.append(txt(" "), colored("warn", "⚠ claims to be " + it.claimed));
  says.append(from, el("div", "q", it.question));
  if (it.context) says.append(el("pre", "input ctx", it.context));
  body.append(says);

  const btn = el("button", "btn key", "answer…");
  btn.type = "button";
  const update = () => (btn.disabled = !canAnswer(it, d) || !!ianswer);
  const opts = el("div", "opts");
  opts.setAttribute("role", "radiogroup");
  (it.options || []).forEach((o, i) => {
    const b = el("button", "opt");
    b.type = "button";
    b.setAttribute("role", "radio");
    b.append(el("span", "num", String(i + 1)), el("span", "otext", o));
    if (it.recommend === i) b.append(el("span", "rec", "recommended (agent)"));
    b.addEventListener("click", () => {
      d.choice = d.choice === i ? null : i;
      d.changed = false;
      paint();
      update();
    });
    opts.append(b);
  });
  const paint = () => opts.querySelectorAll(".opt").forEach((b, i) => {
    b.classList.toggle("sel", d.choice === i);
    b.setAttribute("aria-checked", String(d.choice === i));
  });
  paint();
  if (opts.childNodes.length) body.append(opts);

  const ta = el("textarea", "note");
  ta.rows = 2;
  ta.maxLength = 4000;
  ta.placeholder = (it.options || []).length ? "note (optional; send back needs one)" : "answer in words";
  ta.value = d.note;
  ta.addEventListener("input", () => {
    d.note = ta.value;
    update();
  });
  ta.addEventListener("focus", () => (inboxTyping = true));
  ta.addEventListener("blur", () => {
    inboxTyping = false;
    setTimeout(resumeInbox, 300); // after a tap on answer… has landed
  });
  btn.addEventListener("click", () => openInboxSheet(it));
  const row = el("div", "row");
  row.append(btn);
  body.append(ta, row);
  update();
  card.append(head, body);
  return card;
}

const MERGE_LOOK = { running: ["●", "warn", "merging"], merged: ["✓", "ok", "merged"], failed: ["✗", "err", "not merged"] };

// mergeRow: pekoed's merge after a "merge" answer (pekoed's words only).
function mergeRow(it) {
  const r = it.answer.merge;
  const [icon, role, word] = MERGE_LOOK[r.state] || ["?", "dim", r.state];
  const row = el("div", "card");
  const head = el("div", "line1");
  head.append(colored(role, icon, "ic"), el("span", "name r-text", "PR #" + r.pr), colored(role, word, "word"), el("span", "age", ago(r.at)));
  const body = el("div", "body");
  body.append(el("div", "also", (it.about || "") + " · HEAD " + (r.head || "").slice(0, 7) + (r.method ? " · " + r.method : "") + " · inbox #" + it.id));
  if (r.detail) body.append(el("div", "also " + (r.state === "failed" ? "r-err" : "r-dim"), r.detail));
  row.append(head, body);
  return row;
}

function renderInbox() {
  const rows = $("i-rows");
  rows.replaceChildren();
  $("i-count").textContent = inbox.length ? String(inbox.length) : "";
  for (const id of [...inboxDraft.keys()]) if (!inbox.some((it) => it.id === id)) inboxDraft.delete(id);
  if (merges.length) {
    rows.append(el("div", "sub", "merges by pekoed (on your \"merge\")"));
    for (const it of merges) rows.append(mergeRow(it));
  }
  if (!inbox.length) {
    rows.append(el("div", "empty", "nothing waits for you"));
    return;
  }
  for (const it of inbox) rows.append(inboxCard(it));
}

function holdInbox() {
  return inboxTyping || !!ianswer;
}

function resumeInbox() {
  if (inboxHeld && !holdInbox()) {
    inboxHeld = false;
    renderInbox();
  }
}

async function refreshInbox() {
  try {
    const r = await api("/api/inbox");
    if (view !== "inbox") return;
    const items = r.items || [];
    const sig = JSON.stringify([items, r.merges || []]);
    if (sig === inboxSig) return;
    inbox = items;
    merges = r.merges || [];
    inboxSig = sig;
    if (holdInbox()) inboxHeld = true;
    else renderInbox();
  } catch (e) {
    toast(e.message);
  }
}

// openInboxSheet: what will be sent, to whom, before it is.
function openInboxSheet(it) {
  const d = inboxDraft.get(it.id);
  if (!d || !canAnswer(it, d)) return;
  const note = d.note.trim();
  ianswer = { id: it.id, rev: d.rev, choice: d.choice, note };
  $("isheet-title").textContent = "answer inbox #" + it.id;
  $("isheet-to").textContent = it.from + (it.about && it.about !== it.from ? " · about " + it.about : "");
  $("isheet-q").textContent = it.question;
  const a = [];
  if (d.choice !== null) a.push(d.choice + 1 + ". " + it.options[d.choice] + (it.recommend === d.choice ? "  (same as the agent recommended)" : ""));
  if (note) a.push("note: " + note);
  $("isheet-a").textContent = a.join("\n");
  $("isheet-warn").hidden = false;
  let warn = "as of rev " + d.rev + ": if the facts change before this reaches pekoe, it is refused and shown again";
  if (it.from === "pekoed" && it.kind === "approve" && d.choice === 0 && it.options[0] === "merge") {
    warn = "pekoed checks the conditions again and runs gh pr merge on PR #" + ((it.facts && it.facts.pr) || "?") + " at the HEAD it asked about. " + warn;
  }
  $("isheet-warn").textContent = warn;
  $("isheet-ok").disabled = false;
  $("isheet").hidden = false;
}

function closeInboxSheet() {
  $("isheet").hidden = true;
  ianswer = null;
  resumeInbox();
}

async function doInboxAnswer() {
  const a = ianswer;
  if (!a) return;
  $("isheet-ok").disabled = true;
  const body = { rev: a.rev, note: a.note };
  if (a.choice !== null) body.choice = a.choice;
  try {
    await api("/api/inbox/" + encodeURIComponent(String(a.id)) + "/answer", body);
    inboxDraft.delete(a.id);
    toast("answered #" + a.id, true);
  } catch (e) {
    const d = inboxDraft.get(a.id);
    if (e.status === 409 && d) {
      d.choice = null;
      d.changed = true;
      toast("#" + a.id + " changed or was closed: look again");
    } else {
      toast("#" + a.id + ": " + e.message);
    }
  }
  closeInboxSheet();
  inboxSig = "";
  refresh();
}

// ---- overview: counts, tapi, and every session's flow (read only) ----

const STEP_NAMES = { impl: "impl", pr: "PR", ci: "CI", review: "review", merge: "merge" };
const STEP_LOOK = { done: ["✓", "ok"], now: ["●", "warn"], fail: ["✗", "err"], todo: ["○", "dim"] };
let overviewSig = "";

function renderCounts(c) {
  const box = $("o-counts");
  box.replaceChildren();
  const tile = (role, icon, n, word, hash) => {
    const t = el(hash ? "a" : "div", "tile");
    if (hash) t.href = hash;
    t.append(colored(n ? role : "dim", icon), el("span", "n", String(n)), el("span", "w", word));
    box.append(t);
  };
  tile("ok", "●", c.running, "running");
  tile("warn", "●", c.waiting, "waiting");
  tile("secondary", "✓", c.review, "to review");
  tile("primary", "#", c.open_prs, "open PRs");
  tile("ok", "✓", c.ready, "ready to merge");
  tile("warn", "?", c.inbox_open, "inbox", "#/inbox");
  tile("secondary", "☰", c.ledger_open, "requests open");
}

// tapiLine: icon  tapi  state  age, as a session's line1
function tapiLine() {
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

function renderTapi() {
  const box = $("o-tapi");
  box.replaceChildren();
  if (!tapi) {
    box.append(el("div", "empty", "(tapi is not readable)"));
    return;
  }
  box.append(tapiLine());
  if (tapi.started && tapi.answerable) box.append(el("div", "also r-warn", "⚠ tapi waits for a permission: answer it at the terminal"));
  else if (tapi.started && tapi.state === "waiting") box.append(el("div", "also r-warn", "? tapi asks: open it to answer"));
  if (tapi.last_message) box.append(el("div", "msg last", tapi.last_message));
}

function parentLabel(g) {
  switch (g.parent) {
    case "+tapi": return "tapi";
    case "": return "opened directly";
    case "human": return "you";
    case "unknown": return "unknown";
  }
  return "from " + g.parent;
}

function flowRow(f) {
  const wrap = el("div", "flow");
  const b = el("button", "row-s");
  b.type = "button";
  b.append(line1(f));
  b.addEventListener("click", () => go(f.slug));
  const steps = el("div", "steps");
  f.steps.forEach((s, i) => {
    if (i) steps.append(el("span", "arrow", "→"));
    const [icon, role] = STEP_LOOK[s.status] || ["○", "dim"];
    const sp = el("span", "step st-" + (STEP_LOOK[s.status] ? s.status : "todo"));
    sp.append(colored(role, icon), txt(" " + (STEP_NAMES[s.name] || s.name)));
    steps.append(sp);
  });
  wrap.append(b, steps);
  const notes = el("div", "miss");
  if (f.merge) {
    if (f.merge.ready) {
      notes.append(el("div", "r-ok", "✓ evidence ready · waiting for a person to merge" + (f.merge.inbox ? " (inbox #" + f.merge.inbox + ")" : "")));
    }
    for (const c of f.merge.conds || []) {
      if (c.ok) continue;
      const line = el("div");
      line.append(colored("err", "✗"), txt(" " + c.name + ": " + c.detail));
      notes.append(line);
    }
  } else {
    for (const s of f.steps) {
      if (s.status !== "fail" || !s.detail) continue;
      const line = el("div");
      line.append(colored("err", "✗"), txt(" " + (STEP_NAMES[s.name] || s.name) + ": " + s.detail));
      notes.append(line);
    }
  }
  if (!f.merge && f.after && f.after.length) notes.append(el("div", null, "after " + f.after.join(", "))); // with a PR, merge.check's after says it
  if (notes.childNodes.length) wrap.append(notes);
  return wrap;
}

function renderOverview(o) {
  renderCounts(o.counts || {});
  renderTapi();
  const box = $("o-flow");
  box.replaceChildren();
  const groups = o.groups || [];
  if (!groups.length) {
    box.append(el("div", "empty", "no sessions"));
    return;
  }
  for (const g of groups) {
    const sec = el("div", "group");
    const t = el("div", "gtitle");
    t.append(el("span", "r-secondary", parentLabel(g)));
    if (g.request) {
      t.append(sep(), txt("request #" + g.request.id + " "), colored(g.request.status === "open" ? "warn" : "dim", g.request.status));
      if (g.request.text) sec.append(t, el("div", "req", g.request.text));
      else sec.append(t);
    } else {
      sec.append(t);
    }
    for (const f of g.rows || []) sec.append(flowRow(f));
    box.append(sec);
  }
}

async function refreshOverview() {
  try {
    const o = await api("/api/overview");
    if (view !== "overview") return;
    const sig = JSON.stringify([o.counts, o.groups, tapi]);
    $("o-at").textContent = new Date().toLocaleTimeString();
    if (sig === overviewSig) return;
    overviewSig = sig;
    renderOverview(o);
  } catch (e) {
    toast(e.message);
  }
}

// ---- tapi chat: the conversation as bubbles ----
// pekoed reads tapi's Claude Code transcript (/api/tapi/transcript): the
// person's words, tapi's words, the tools it called (name and one main
// argument) and how long each turn took; never tool results or thinking.
// tapi's words are untrusted (as the inbox): they go into the DOM with
// textContent, through a small markdown of our own (headings, lists,
// checkboxes, inline code; no links, no HTML). chatView takes items and a
// name, not tapi: a session's transcript can use it later.

const CHAT_FOLD = 15; // lines shown before "more"
const CHAT_KEEP = 1500; // items kept in the page
const SPIN = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏";
const DAYS = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];

const chat = {
  cursor: "",
  items: [], // ChatItem, and {kind: "conv"} where a new conversation starts
  earlier: false, // older items were left out
  ok: null, // the transcript reads (null: not asked yet)
  why: "", // why it does not
  loading: false,
  sig: "", // what is in #c-items
  tailSig: "", // what is in #c-tail
  held: false, // a redraw waits for a selection to go
  open: new Set(), // tool lists and long bubbles opened ("t<i>" / "m<i>")
  pending: [], // sent from here, not in the transcript yet: {text, at}
  stuck: true, // the log is at its bottom (kept when the log is resized)
};
let chatChoice = "chat"; // the switch: chat | screen
try {
  if (localStorage.getItem("pekoe.tapiView") === "screen") chatChoice = "screen";
} catch (e) { /* no storage: chat */ }
let spinTimer = 0, blinkTimer = 0, blinkEyes = "";

const pad2 = (n) => String(n).padStart(2, "0");
function hm(iso) {
  const d = new Date(iso);
  return isNaN(d) ? "" : pad2(d.getHours()) + ":" + pad2(d.getMinutes());
}
function dayKey(iso) {
  const d = new Date(iso);
  return isNaN(d) ? "" : d.getFullYear() + "-" + d.getMonth() + "-" + d.getDate();
}
function dayLabel(iso) {
  const d = new Date(iso);
  return d.getMonth() + 1 + "/" + d.getDate() + " " + DAYS[d.getDay()];
}
function dur(ms) {
  const s = Math.round(ms / 1000);
  if (s < 60) return s + "s";
  if (s < 3600) return Math.floor(s / 60) + "m" + pad2(s % 60) + "s";
  return Math.floor(s / 3600) + "h" + pad2(Math.floor(s / 60) % 60) + "m";
}

// mdInline: `code` as code, the rest as text
function mdInline(parent, s) {
  const re = /`([^`\n]+)`/g;
  let at = 0, m;
  while ((m = re.exec(s))) {
    if (m.index > at) parent.append(txt(s.slice(at, m.index)));
    parent.append(el("code", null, m[1]));
    at = re.lastIndex;
  }
  if (at < s.length) parent.append(txt(s.slice(at)));
}

// mdLines: a naive markdown, one element per line. Headings, list markers
// and checkboxes (dim, monospace), inline code, fenced code (monospace, as
// written). Nothing becomes a link.
function mdLines(lines) {
  const frag = document.createDocumentFragment();
  let fence = null;
  for (const ln of lines) {
    if (/^\s*```/.test(ln)) {
      if (fence) fence = null;
      else frag.append((fence = el("pre", "fence")));
      continue;
    }
    if (fence) {
      fence.append(txt((fence.childNodes.length ? "\n" : "") + ln));
      continue;
    }
    const h = ln.match(/^ {0,3}#{1,6}\s+(.*)$/);
    if (h) {
      const e = el("div", "h");
      mdInline(e, h[1].replace(/\s+#+\s*$/, ""));
      frag.append(e);
      continue;
    }
    const line = el("div", "l");
    const m = ln.match(/^(\s*)([-*+]|\d{1,3}[.)])\s+(\[[ xX]\]\s+)?(.*)$/);
    if (m) {
      if (m[1]) line.append(txt(m[1]));
      const num = /\d/.test(m[2]);
      const mark = m[3] ? (num ? m[2] + " " : "- ") + m[3].trim() : num ? m[2] : "•";
      line.append(el("span", "cb", mark), txt(" "));
      mdInline(line, m[4]);
    } else {
      mdInline(line, ln);
    }
    frag.append(line);
  }
  // a fence opened on the last line shown (folded) has nothing in it yet
  for (const f of frag.querySelectorAll(".fence")) if (!f.childNodes.length) f.remove();
  return frag;
}

// mdBody: the agent's words; past CHAT_FOLD lines folded behind "more"
function mdBody(text, key, onToggle) {
  const box = el("div", "md");
  const lines = String(text).split("\n");
  const long = lines.length > CHAT_FOLD;
  const open = chat.open.has(key);
  box.append(mdLines(long && !open ? lines.slice(0, CHAT_FOLD) : lines));
  if (long) {
    const b = el("button", "more");
    b.type = "button";
    if (open) b.append(el("span", "r-secondary", "less ‹"));
    else b.append(el("span", "r-secondary", "more ›"), txt("  +" + (lines.length - CHAT_FOLD) + " lines"));
    b.addEventListener("click", () => {
      if (chat.open.has(key)) chat.open.delete(key);
      else chat.open.add(key);
      onToggle();
    });
    box.append(b);
  }
  return box;
}

function bubbleLine(body, meta) {
  const line = el("div", "line");
  const b = el("div", "bubble");
  b.append(body);
  line.append(b);
  if (meta) line.append(meta);
  return line;
}

// metaOf: the time under a bubble, with ✓ (sent) / ● (not in the
// transcript yet) for the person's, the turn's length for the agent's
function metaOf(top, topRole, at) {
  const m = el("div", "meta");
  if (top) m.append(topRole ? colored(topRole, top) : txt(top), el("br"));
  m.append(txt(hm(at)));
  return m;
}

// chatBlocks groups items into what is drawn: day lines, the person's
// turns, the agent's turns (tool lines and bubbles), pekoe's lines.
function chatBlocks(items, pending) {
  const out = [];
  let day = "", cur = null;
  const dayOf = (at) => {
    const k = dayKey(at);
    if (k && k !== day) {
      day = k;
      out.push({ type: "day", label: dayLabel(at) });
      cur = null;
    }
  };
  items.forEach((it, i) => {
    if (it.kind === "conv") {
      out.push({ type: "conv" });
      cur = null;
      return;
    }
    if (it.kind === "turn") {
      if (cur && cur.type === "agent") cur.ms = it.ms;
      return;
    }
    dayOf(it.at);
    if (it.kind === "pekoe") {
      out.push({ type: "pekoe", text: it.text, at: it.at });
      cur = null;
    } else if (it.kind === "human") {
      if (!cur || cur.type !== "me") out.push((cur = { type: "me", msgs: [] }));
      cur.msgs.push({ text: it.text, at: it.at, sent: true });
    } else if (it.kind === "agent" || it.kind === "tool") {
      if (!cur || cur.type !== "agent" || cur.ms) out.push((cur = { type: "agent", parts: [], ms: 0 }));
      const last = cur.parts[cur.parts.length - 1];
      if (it.kind === "agent") cur.parts.push({ type: "text", text: it.text, at: it.at, i });
      else if (it.name === "AskUserQuestion" && it.question) cur.parts.push({ type: "ask", text: it.question, at: it.at, i });
      else if (last && last.type === "tools") last.list.push(it);
      else cur.parts.push({ type: "tools", list: [it], i });
    }
  });
  for (const p of pending) {
    dayOf(p.at);
    if (!cur || cur.type !== "me") out.push((cur = { type: "me", msgs: [] }));
    cur.msgs.push({ text: p.text, at: p.at, sent: false });
  }
  return out;
}

function toolsLine(list, key, onToggle) {
  const frag = document.createDocumentFragment();
  const open = chat.open.has(key);
  const b = el("button", "tools");
  b.type = "button";
  b.setAttribute("aria-expanded", String(open));
  const words = list.map((t) => (t.name + " " + (t.input || "")).trim()).join(" · ");
  b.append(el("span", "ic", open ? "⌄" : "›"), txt(list.length + (list.length === 1 ? " tool · " : " tools · ") + words));
  b.addEventListener("click", () => {
    if (chat.open.has(key)) chat.open.delete(key);
    else chat.open.add(key);
    onToggle();
  });
  frag.append(b);
  if (open) {
    const box = el("div", "tool-list");
    for (const t of list) {
      const r = el("div");
      r.append(el("span", "n", t.name), txt(t.input ? " " + t.input : ""));
      box.append(r);
    }
    frag.append(box);
  }
  return frag;
}

// chatView draws items (and the person's unsent words) for an agent called
// name. onToggle redraws after a fold opens or closes.
function chatView(items, pending, name, onToggle) {
  const frag = document.createDocumentFragment();
  for (const bl of chatBlocks(items, pending)) {
    if (bl.type === "day") {
      frag.append(el("div", "divider", bl.label));
    } else if (bl.type === "conv") {
      frag.append(el("div", "divider", "new conversation"));
    } else if (bl.type === "pekoe") {
      const p = el("div", "sysline", bl.text);
      p.title = bl.text;
      frag.append(p);
    } else if (bl.type === "me") {
      const turn = el("div", "turn me");
      bl.msgs.forEach((m, j) => {
        const last = j === bl.msgs.length - 1;
        const body = el("div", "plain", m.text);
        turn.append(bubbleLine(body, last ? metaOf(m.sent ? "✓" : "●", m.sent ? "" : "secondary", m.at) : null));
      });
      frag.append(turn);
    } else {
      const turn = el("div", "turn agent");
      const who = el("div", "who");
      who.append(el("span", "ic", "◆"), txt(name));
      turn.append(who);
      const lastText = bl.parts.map((p) => p.type !== "tools").lastIndexOf(true);
      bl.parts.forEach((p, j) => {
        if (p.type === "tools") {
          turn.append(toolsLine(p.list, "t" + p.i, onToggle));
          return;
        }
        const body = mdBody(p.text, "m" + p.i, onToggle);
        const meta = j === lastText ? metaOf(bl.ms ? dur(bl.ms) : "", "", p.at) : null;
        turn.append(bubbleLine(body, meta));
      });
      frag.append(turn);
    }
  }
  return frag;
}

// ---- tapi chat: the page around chatView ----

// tapiMode: what the panel shows now. chat when chosen and the transcript
// reads (or is still being read the first time: "loading" in the chat, not
// the screen for a moment); the screen otherwise (tapi not started, no
// transcript).
function tapiMode() {
  return chatChoice === "chat" && chat.ok !== false && tapiUp() ? "chat" : "screen";
}

function selectingIn(node) {
  const sel = document.getSelection();
  if (!sel || sel.isCollapsed || !sel.rangeCount) return false;
  return node.contains(sel.anchorNode) || node.contains(sel.focusNode);
}

function atBottom(log) {
  return log.scrollHeight - log.scrollTop - log.clientHeight < 40;
}

function updateJump() {
  const log = $("c-log");
  $("c-jump").hidden = tapiMode() !== "chat" || atBottom(log);
}

// fitComposer: in tapi chat the box grows with its lines (up to its
// max-height, 30vh). Elsewhere it keeps its own (resizable) height: only
// the height set here is taken back when leaving tapi.
function fitComposer() {
  const ta = $("d-text");
  if (current !== TAPI) {
    if (ta.dataset.fit) {
      ta.style.height = "";
      delete ta.dataset.fit;
    }
    return;
  }
  ta.style.height = "";
  ta.style.height = Math.max(ta.scrollHeight + 2, 44) + "px"; // + the border
  ta.dataset.fit = "1";
}

// fitViewport: tapi chat fills what can be seen. On iOS the software
// keyboard does not shrink the layout viewport (nor 100dvh); it shrinks the
// visual viewport and pans it. So the page takes its height and offset,
// and the composer stays right above the keyboard.
function fitViewport() {
  const vv = window.visualViewport;
  if (!vv || !document.body.classList.contains("chat-on")) return;
  if (vv.scale > 1.01) return; // pinch-zoomed: keep the page as it was, let it pan
  const r = document.documentElement.style;
  r.setProperty("--vvh", vv.height + "px");
  r.setProperty("--vvt", vv.offsetTop + "px");
  // the keyboard covers the home indicator: no bottom inset above it
  document.body.classList.toggle("kb", document.documentElement.clientHeight - vv.height > 120);
}

function toBottom() {
  const log = $("c-log");
  log.scrollTop = log.scrollHeight;
  updateJump();
}

// renderChat redraws the bubbles when something changed, keeping the place
// when scrolled up and following the newest when at the bottom.
function renderChat(force) {
  if (current !== TAPI) return;
  const log = $("c-log");
  const sig = chat.items.length + "|" + chat.pending.length + "|" + [...chat.open].join(",") + "|" + chat.earlier + "|" + chat.ok;
  if (!force && sig === chat.sig) return;
  if (selectingIn(log)) {
    chat.held = true; // copying: redraw when the selection goes
    return;
  }
  chat.held = false;
  const stick = atBottom(log);
  const top = log.scrollTop;
  chat.sig = sig;
  const items = $("c-items");
  items.replaceChildren();
  if (chat.earlier) items.append(el("div", "divider", "earlier messages: see screen"));
  if (!chat.items.length && !chat.pending.length) {
    // empty only once the transcript has been read; before that, loading
    items.append(el("div", "empty", chat.ok === true ? "no messages yet" : "loading…"));
  }
  items.append(chatView(chat.items, chat.pending, "tapi", () => renderChat(true)));
  if (stick) toBottom();
  else log.scrollTop = top;
  updateJump();
}

// renderChatTail: what tapi is doing now, under the last bubble: working
// (a spinner), a permission prompt (answered at the terminal) or a
// question (the keypad).
function renderChatTail() {
  const tail = $("c-tail");
  const t = detailTarget();
  const pad = $("d-keys");
  const sig = t ? [t.state, t.wait_kind, t.answerable, t.state_since, t.answerable ? ago(t.state_since) : ""].join("|") : "-";
  const log = $("c-log");
  const stick = atBottom(log);
  let moved = false;
  if (sig !== chat.tailSig) {
    chat.tailSig = sig;
    moved = true;
    tail.replaceChildren();
    if (t && t.answerable) {
      const card = el("div", "card wait-card");
      card.setAttribute("role", "status");
      const head = el("div", "chead");
      head.append(el("span", "ic r-secondary", "◆"), el("b", null, "tapi asks for a permission"), colored("warn", "● wait " + ago(t.state_since), "st"));
      card.append(head, el("div", "foot", "answer it in the terminal (pekoe TUI) · allow / deny is not on the web. send and the keys are off until then."));
      tail.append(card);
    } else if (t && t.state === "running") {
      const row = el("div", "turn agent");
      const typing = el("span", "typing");
      typing.append(el("span", "spin r-ok", SPIN[0]), el("span", "r-dim think", "thinking · " + ago(t.state_since)));
      row.append(bubbleLine(typing, null));
      tail.append(row);
    }
  }
  // the keypad lives under the question while in chat, else in its place
  if (tapiMode() === "chat" && questionUp(t)) {
    if (pad.parentNode !== tail) {
      tail.append(pad);
      pad.hidden = false; // renderKeypad agrees: questionUp(t)
      moved = true;
    }
  } else if (pad.parentNode !== $("detail")) {
    $("d-panel").after(pad);
  }
  if (moved && stick) toBottom();
  clearInterval(spinTimer);
  const spin = tail.querySelector(".spin");
  if (spin && t) {
    let i = 0;
    spinTimer = setInterval(() => {
      spin.textContent = SPIN[++i % SPIN.length];
      if (i % 12 === 0) tail.querySelector(".think").textContent = "thinking · " + ago(t.state_since);
    }, 80);
  }
}

// ---- the face: ▐◕ ◕▌ as the TUI's pearl1 ----
// Body and eye colors never change (theme.css: --tapi-bd / -sh / -eye);
// the state is the eyes' shape and the word after the title.

const EYE_CLASS = { "●": "e-dot", "◕": "e-puppy" };

function setFace(eyes) {
  const face = $("c-face");
  const [a, , b] = [...eyes];
  const cell = (g) => el("i", EYE_CLASS[g] || null, g);
  face.replaceChildren(cell(a), el("i", null, " "), cell(b));
}

function tapiEyes() {
  if (!tapi || !tapi.started || !tapi.alive) return "- -";
  if (tapi.state === "running") return "● ●";
  if (tapi.state === "waiting") return "◕ ◕";
  return "• •";
}

// blink (docs/mascot.md): idle / work show "- -" for one frame every 4–6 s
function faceBlink(eyes) {
  if (eyes === blinkEyes) return;
  blinkEyes = eyes;
  clearTimeout(blinkTimer);
  setFace(eyes);
  if (eyes !== "• •" && eyes !== "● ●") return;
  const next = () => {
    blinkTimer = setTimeout(() => {
      if (blinkEyes !== eyes || current !== TAPI) return;
      setFace("- -");
      blinkTimer = setTimeout(() => {
        if (blinkEyes !== eyes) return;
        setFace(eyes);
        next();
      }, 150);
    }, 4000 + Math.random() * 2000);
  };
  next();
}

function renderChatTitle() {
  faceBlink(tapiEyes());
  let word = "";
  if (!tapi) word = "not readable";
  else if (!tapi.started) word = "not started";
  else if (!tapi.alive) word = "exited";
  else word = (tapi.look && tapi.look.word) || tapi.state;
  $("c-state").textContent = "· " + word;
  const mode = tapiMode();
  $("c-v-chat").setAttribute("aria-pressed", String(mode === "chat"));
  $("c-v-screen").setAttribute("aria-pressed", String(mode === "screen"));
}

// applyTapiMode shows the chat or the screen (and, for a session, the
// screen with its own title).
function applyTapiMode() {
  const isTapi = current === TAPI;
  const chatOn = isTapi && tapiMode() === "chat";
  $("c-ttl").hidden = !isTapi;
  $("d-ttl").hidden = isTapi;
  $("c-chat").hidden = !chatOn;
  $("d-screen-box").hidden = chatOn;
  $("d-head-panel").hidden = chatOn;
  $("d-panel").classList.toggle("chat-panel", chatOn);
  document.body.classList.toggle("chat-on", chatOn);
  if (chatOn) fitViewport();
  const why = $("c-why");
  why.hidden = !isTapi || chatOn || chatChoice !== "chat" || chat.ok !== false;
  why.textContent = why.hidden ? "" : "chat unavailable: " + chat.why;
  if (isTapi) {
    renderChatTitle();
    renderChatTail();
  } else {
    clearInterval(spinTimer);
    clearTimeout(blinkTimer);
    blinkEyes = "";
    if ($("d-keys").parentNode !== $("detail")) $("d-panel").after($("d-keys"));
  }
  if (chatOn) renderChat(false);
  else if (current) updateJump();
}

function chooseTapiView(v) {
  chatChoice = v;
  try {
    localStorage.setItem("pekoe.tapiView", v);
  } catch (e) { /* not kept */ }
  const was = $("c-chat").hidden;
  renderDetailHead(); // the head and the wait card belong to the screen view
  if (v === "chat") {
    refreshChat();
    if (was && !$("c-chat").hidden) toBottom();
  } else {
    screenSig = "";
    refreshScreen();
  }
}

// refreshChat reads what is new after the cursor. A new conversation
// (/clear) keeps the old bubbles above a divider.
async function refreshChat() {
  if (current !== TAPI || chat.loading) return;
  chat.loading = true;
  const wasMode = tapiMode();
  try {
    const r = await api("/api/tapi/transcript?after=" + encodeURIComponent(chat.cursor));
    if (current !== TAPI) return;
    if (r.reset) {
      if (r.new_conversation && chat.items.length) {
        chat.items.push({ kind: "conv" });
      } else {
        chat.items = [];
        chat.open.clear();
        chat.earlier = !!r.earlier;
      }
    }
    for (const it of r.items || []) {
      if (it && typeof it === "object" && typeof it.kind === "string") chat.items.push(it);
    }
    if (chat.items.length > CHAT_KEEP) {
      chat.items = chat.items.slice(-CHAT_KEEP);
      chat.open.clear();
      chat.earlier = true;
    }
    chat.cursor = String(r.cursor || "");
    // the person's words that came through are no longer "not sent"
    const seen = new Set(chat.items.filter((it) => it.kind === "human").slice(-20).map((it) => it.text));
    const old = Date.now() - 5 * 60 * 1000;
    chat.pending = chat.pending.filter((p) => !seen.has(p.text) && p.at > old);
    chat.ok = true;
    chat.why = "";
  } catch (e) {
    chat.ok = false;
    chat.why = e.message;
  } finally {
    chat.loading = false;
  }
  if (current !== TAPI) return;
  const mode = tapiMode();
  if (mode !== wasMode) {
    applyTapiMode();
    if (mode === "chat") toBottom();
    else {
      screenSig = "";
      refreshScreen();
    }
  } else if (mode === "chat") {
    renderChat(false);
  } else {
    applyTapiMode(); // the reason next to the screen
  }
}

// ---- routing & polling ----

function go(slug) {
  location.hash = slug === TAPI ? "#/tapi" : slug ? "#/s/" + encodeURIComponent(slug) : backTo;
}

const VIEWS = { "#/inbox": "inbox", "#/overview": "overview" };

function route() {
  const m = location.hash.match(/^#\/s\/(.+)$/);
  const ta = $("d-text");
  if (current) {
    if (ta.value) drafts.set(current, ta.value);
    else drafts.delete(current);
  }
  const isTapi = location.hash === "#/tapi";
  current = isTapi ? TAPI : m ? decodeURIComponent(m[1]) : null;
  view = current ? "detail" : VIEWS[location.hash] || "list";
  inboxTyping = false; // a hidden note box may never get its blur
  if (!current) backTo = location.hash === "#/inbox" || location.hash === "#/overview" ? location.hash : "";
  ta.value = (current && drafts.get(current)) || "";
  for (const v of ["list", "inbox", "overview", "detail"]) $(v).hidden = view !== v;
  if (view !== "detail") document.body.classList.remove("chat-on");
  fitComposer();
  $("tabs").hidden = view === "detail";
  for (const v of ["list", "inbox", "overview"]) {
    const t = $("t-" + v);
    if (view === v) t.setAttribute("aria-current", "page");
    else t.removeAttribute("aria-current");
  }
  $("back").hidden = view !== "detail";
  $("back").textContent = "‹ " + ({ "#/inbox": "inbox", "#/overview": "overview" }[backTo] || "sessions");
  if (view === "detail") {
    $("d-screen").textContent = "";
    screenSig = "";
    screenPaused = screenHeld = false;
    renderScreenState();
    chat.sig = chat.tailSig = "";
    renderDetailHead();
    if (current === TAPI) {
      // renderDetailHead has drawn the bubbles read so far (chat.sig was
      // cleared above): keep them in sight while this read brings what is new
      toBottom();
      refreshChat().then(() => current === TAPI && toBottom());
    }
    refreshScreen();
  } else if (view === "list") {
    renderList();
  } else if (view === "inbox") {
    inboxSig = "";
    refreshInbox();
  } else {
    refreshOverview();
  }
  schedule();
}

function schedule() {
  clearInterval(listTimer);
  clearInterval(screenTimer);
  clearInterval(chatTimer);
  if (document.hidden) return;
  listTimer = setInterval(refresh, 3000);
  if (current) screenTimer = setInterval(refreshScreen, 2500);
  if (current === TAPI) chatTimer = setInterval(refreshChat, 2000);
}

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) {
    refresh();
    if (current) refreshScreen();
    if (current === TAPI) refreshChat();
  }
  schedule();
});
window.addEventListener("hashchange", route);
$("back").addEventListener("click", () => go(null));
$("sheet-cancel").addEventListener("click", closeSheet);
$("sheet-ok").addEventListener("click", doAnswer);
$("sheet").addEventListener("click", (e) => { if (e.target === $("sheet")) closeSheet(); });
$("d-send").addEventListener("submit", doSend);
$("tsheet-cancel").addEventListener("click", closeTapiSheet);
$("tsheet-ok").addEventListener("click", doTapiSend);
$("tsheet").addEventListener("click", (e) => { if (e.target === $("tsheet")) closeTapiSheet(); });
$("isheet-cancel").addEventListener("click", closeInboxSheet);
$("isheet-ok").addEventListener("click", doInboxAnswer);
$("isheet").addEventListener("click", (e) => { if (e.target === $("isheet")) closeInboxSheet(); });
$("d-pause").addEventListener("click", togglePause);
for (const b of $("d-keys").querySelectorAll("button[data-key]")) {
  b.addEventListener("click", () => pressKey(b.dataset.key));
}
document.addEventListener("selectionchange", resumeIfHeld);
document.addEventListener("selectionchange", () => {
  if (chat.held && !selectingIn($("c-log"))) renderChat(true);
});
$("c-v-chat").addEventListener("click", () => chooseTapiView("chat"));
$("c-v-screen").addEventListener("click", () => chooseTapiView("screen"));
$("c-log").addEventListener("scroll", () => {
  chat.stuck = atBottom($("c-log"));
  updateJump();
}, { passive: true });
// the log gets shorter when the composer grows, the note shows or the
// keyboard opens: if it was at the bottom, keep the newest in sight
if (window.ResizeObserver) {
  new ResizeObserver(() => {
    if (chat.stuck && !$("c-chat").hidden) toBottom();
  }).observe($("c-log"));
}
$("d-text").addEventListener("input", fitComposer);
if (window.visualViewport) {
  visualViewport.addEventListener("resize", fitViewport);
  visualViewport.addEventListener("scroll", fitViewport);
}
$("c-jump").addEventListener("click", () => {
  const log = $("c-log");
  log.scrollTo({ top: log.scrollHeight, behavior: "smooth" });
});
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
  if (view === "list" && !busy) renderList();
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
