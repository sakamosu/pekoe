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
let listTimer = 0, screenTimer = 0;
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
    box.append(el("div", "msg", "質問です。開いて画面を見ながら、キーパッドか send で答えてください。"));
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
  $("d-text").placeholder = isTapi ? "tapi に頼む(送る前に全文を確かめます)" : "一言の指示を送る";
  if (isTapi) {
    $("d-to-slug").textContent = "tapi";
    $("d-to-repo").textContent = s ? "統括役 · 全リポジトリ" : tapi && tapi.started ? "has exited" : "is not started";
    let why = "";
    if (tapi && !tapi.sendable) why = "tapi への送信には config.toml の [web] allow_devices が要ります(端末を絞っているときだけ送れます)";
    else if (s && s.answerable) why = "tapi は許可を待っています: 端末(pekoe TUI)で答えるまで送れません";
    note.textContent = why;
    note.hidden = !why;
    to.classList.toggle("gone", !s);
    $("d-send-btn").disabled = !s || !tapi.sendable || s.answerable || sending;
    return;
  }
  note.hidden = true;
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
  l2.append(document.createTextNode("統括役 · 全リポジトリを読み、セッションを作って任せる"));
  head.append(l2);
  if (busy) return;
  ask.replaceChildren();
  ask.hidden = !t || t.state !== "waiting";
  if (ask.hidden) return;
  const body = el("div", "body");
  if (t.answerable) {
    const w = el("div", "also");
    w.append(el("span", "ic", "⚠"), document.createTextNode(" tapi は許可を待っています。allow / deny は Web には出しません: 端末(pekoe TUI)で答えてください。それまで send とキーは止まります。"));
    body.append(w);
  } else if (t.wait_kind === "question") {
    body.append(el("div", "msg", "質問です。画面を見ながら、キーパッドか send で答えてください。"));
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

// ---- send to tapi: always through a sheet with the whole text ----
// tapi acts on every repository, so its send weighs more than one
// session's: the sheet says the target is tapi and shows every character
// that will be typed. The text is fixed when the sheet opens.

let tsend = null; // the text in the sheet

function openTapiSheet(text) {
  if (!tapiUp()) {
    toast("tapi is not running");
    return;
  }
  if (!tapi.sendable) {
    toast("tapi への送信には [web] allow_devices が要ります");
    return;
  }
  busy = true;
  tsend = text;
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
  try {
    await api("/api/tapi/send", { text });
    drafts.delete(TAPI);
    const ta = $("d-text");
    if (current === TAPI && ta.value.trim() === text) ta.value = "";
    toast("sent → tapi", true);
    setTimeout(refreshScreen, 600);
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
    if (it.recommend === i) b.append(el("span", "rec", "推奨(エージェント)"));
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
  ta.placeholder = (it.options || []).length ? "note (optional; 差し戻す needs one)" : "answer in words";
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

function renderInbox() {
  const rows = $("i-rows");
  rows.replaceChildren();
  $("i-count").textContent = inbox.length ? String(inbox.length) : "";
  for (const id of [...inboxDraft.keys()]) if (!inbox.some((it) => it.id === id)) inboxDraft.delete(id);
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
    const sig = JSON.stringify(items);
    if (sig === inboxSig) return;
    inbox = items;
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
  if (d.choice !== null) a.push(d.choice + 1 + ". " + it.options[d.choice] + (it.recommend === d.choice ? "  (推奨(エージェント)と同じ)" : ""));
  if (note) a.push("note: " + note);
  $("isheet-a").textContent = a.join("\n");
  $("isheet-warn").hidden = false;
  $("isheet-warn").textContent = "as of rev " + d.rev + ": if the facts change before this reaches pekoe, it is refused and shown again";
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

const STEP_NAMES = { impl: "実装", pr: "PR", ci: "CI", review: "レビュー", merge: "マージ" };
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
      notes.append(el("div", "r-ok", "✓ 根拠が揃った · 人のマージ待ち" + (f.merge.inbox ? " (inbox #" + f.merge.inbox + ")" : "")));
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
    renderDetailHead();
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
