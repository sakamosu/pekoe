// pekoe's local GUI, the desktop page (docs/gui.md 段階 1): the sessions on
// the left (by repository, waiting first), the view on the right: the
// overview's flow as one table, a session's colored screen with send,
// allow / deny / always and the question keypad, the inbox, tapi chat.
// It draws with the phone's modules (lib/) and calls the same /api/; only
// the layout is its own. Everything goes into the DOM with textContent
// (agent output is untrusted text).

import { $, el, colored, txt, sep, toast } from "../lib/dom.js";
import { api, guiSignIn, TAPI, apiBase } from "../lib/api.js";
import { requestId, questionId, ago, details, line1, cutNote, questionUp } from "../lib/fmt.js";
import { termLines, screenRows } from "../lib/term.js";
import { IMG_MAX, prepareImage, thumbs, sendBody, imagesNote } from "../lib/images.js";
import { draftOf, inboxCard, mergeRow } from "../lib/inbox.js";
import { renderCounts, renderTapi, tapiLine, parentLabel, stepCell, missing, STEP_NAMES } from "../lib/overview.js";
import { SPIN, bubbleLine, markRewound, chatView } from "../lib/chat.js";
import { answerSheet, inboxSheet, showSendSheet } from "../lib/sheets.js";

let sessions = [];
let tapi = null; // /api/tapi
let view = "overview"; // overview | inbox | detail
let current = null; // the slug in the detail view, or TAPI
let busy = false; // a sheet is open: the cards it was opened from stay as they are
let sending = false, pressing = false, preparing = false;
let listTimer = 0, screenTimer = 0, chatTimer = 0;
const drafts = new Map(); // slug → unsent text: one session's words never ride along to another
const attachments = new Map(); // slug → images for its next send (same rule)

// ---- the sessions on the left: by repository, waiting first ----
// /api/sessions comes waiting first (oldest wait on top), then running,
// then the rest; a repository is placed where its first session is, so
// one with a session waiting comes on top.

function byRepo(list) {
  const groups = new Map();
  for (const s of list) {
    if (!groups.has(s.repo)) groups.set(s.repo, []);
    groups.get(s.repo).push(s);
  }
  return [...groups];
}

function sideRow(s) {
  const b = el("button", "row-s" + (s.slug === current ? " sel" : "") + (s.state === "waiting" ? " is-waiting" : ""));
  b.type = "button";
  const l2 = el("div", "line2");
  l2.append(...details(s, false));
  b.append(line1(s));
  if (l2.childNodes.length) b.append(l2);
  if (s.state === "waiting") {
    const w = el("div", "line2 wait");
    if (s.answerable && s.pending_tool) w.append(colored("warn", "?"), txt(" " + s.pending_tool.name + " " + (s.pending_tool.input || "")));
    else if (s.wait_kind === "question") w.append(colored("warn", "?"), txt(" a question"));
    else w.append(colored("warn", "?"), txt(" waiting"));
    b.append(w);
  }
  b.addEventListener("click", () => go(s.slug));
  b.setAttribute("aria-current", s.slug === current ? "true" : "false");
  return b;
}

function renderSide() {
  const rows = $("s-rows");
  rows.replaceChildren();
  $("s-count").textContent = sessions.length ? String(sessions.length) : "";
  if (!sessions.length) {
    rows.append(el("div", "empty", "no sessions"));
  }
  for (const [repo, list] of byRepo(sessions)) {
    const g = el("div", "session-group");
    const t = el("div", "session-group-head");
    const waits = list.filter((s) => s.state === "waiting").length;
    t.append(el("span", "r-secondary", repo), el("span", "n", String(list.length)));
    if (waits) t.append(colored("warn", "● " + waits + " waiting", "session-group-waiting"));
    g.append(t, ...list.map(sideRow));
    rows.append(g);
  }
  const waiting = sessions.filter((s) => s.state === "waiting");
  const done = sessions.filter((s) => s.state === "done").length;
  const running = sessions.filter((s) => s.state === "running").length;
  const foot = $("s-foot");
  foot.replaceChildren();
  if (waiting.length) {
    const oldest = waiting.reduce((a, s) => (s.state_since < a ? s.state_since : a), waiting[0].state_since);
    foot.append(colored("warn", "●"), txt(` ${waiting.length} waiting · oldest ${ago(oldest)}`));
  }
  if (done) {
    if (foot.childNodes.length) foot.append(el("span", "gap"));
    foot.append(colored("secondary", "✓"), txt(` ${done} to review`));
  }
  foot.hidden = !foot.childNodes.length;
  const sum = $("sum");
  sum.replaceChildren();
  const bit = (role, icon, n, word) => {
    if (!n) return;
    if (sum.childNodes.length) sum.append(txt("  "));
    sum.append(colored(role, icon), txt(` ${n} ${word}`));
  };
  bit("warn", "●", waiting.length, "wait");
  bit("ok", "●", running, "run");
  bit("secondary", "✓", done, "done");
  document.title = waiting.length ? `(${waiting.length}) pekoe` : "pekoe";
  // the nav: open inbox items, tapi's state
  const n = tapi ? tapi.inbox_open : 0;
  $("n-inbox-n").textContent = n ? String(n) : "";
  $("n-inbox-n").hidden = !n;
  const lk = tapi && tapi.started && tapi.alive ? tapi.look || { icon: "○", role: "dim" } : { icon: "○", role: "dim" };
  $("n-tapi-ic").className = "ic r-" + lk.role;
  $("n-tapi-ic").textContent = lk.icon;
}

async function refresh() {
  try {
    const [r, t] = await Promise.all([api("/api/sessions"), api("/api/tapi").catch(() => null)]);
    sessions = r.sessions || [];
    tapi = t;
    renderSide();
    if (view === "detail") renderDetailHead();
    else if (view === "inbox") await refreshInbox();
    else await refreshOverview();
  } catch (e) {
    toast(e.message);
  }
}

// ---- overview: the counts, tapi, and the flow as one table ----

const STEPS = ["impl", "review", "pr", "ci", "merge"];
let overviewSig = "";

function flowRow(f) {
  const tr = el("tr", "dashboard-row");
  const s = el("td", "col-session");
  const b = el("button", "dashboard-session");
  b.type = "button";
  b.append(colored(f.look.role, f.look.icon, "ic"), el("span", "name r-text", f.branch || f.slug));
  b.addEventListener("click", () => go(f.slug));
  s.append(b, el("div", "dashboard-session-sub", f.repo + (f.slug !== f.branch ? " · " + f.slug : "")));
  const st = el("td", "col-state");
  st.append(colored(f.look.role, f.look.word), el("span", "age", f.state === "idle" ? "" : " " + ago(f.state_since)));
  tr.append(s, st);
  for (const name of STEPS) {
    const step = (f.steps || []).find((x) => x.name === name);
    const td = el("td", "col-step");
    if (step) {
      td.append(stepCell(step, false));
      if (name === "pr" && f.pr) td.append(txt(" #" + f.pr));
      if (step.detail) td.title = (STEP_NAMES[name] || name) + ": " + step.detail;
    } else {
      td.append(colored("dim", "·"));
    }
    tr.append(td);
  }
  const miss = el("td", "col-missing");
  if (f.merge && f.merge.ready) {
    miss.append(el("div", "r-ok", "✓ evidence ready · waiting for a person to merge" + (f.merge.inbox ? " (inbox " + questionId(f.merge.inbox) + ")" : "")));
  }
  miss.append(...missing(f));
  tr.append(miss);
  return tr;
}

function renderOverview(o) {
  renderCounts($("o-counts"), o.counts || {});
  renderTapi($("o-tapi"), tapi);
  const table = $("o-flow");
  for (const b of [...table.tBodies]) b.remove();
  const groups = o.groups || [];
  $("o-empty").hidden = !!groups.length;
  table.hidden = !groups.length;
  for (const g of groups) {
    const body = el("tbody", "dashboard-group");
    const head = el("tr", "dashboard-group-head");
    const td = el("td");
    td.colSpan = 3 + STEPS.length;
    td.append(el("span", "r-secondary", parentLabel(g)));
    if (g.request) {
      td.append(sep(), txt("request " + requestId(g.request.id) + " "), colored(g.request.status === "open" ? "warn" : "dim", g.request.status));
      if (g.request.text) td.append(el("div", "req", g.request.text));
    }
    head.append(td);
    body.append(head, ...(g.rows || []).map(flowRow));
    table.append(body);
  }
}

async function refreshOverview() {
  try {
    const o = await api("/api/overview");
    if (view !== "overview") return;
    $("o-at").textContent = new Date().toLocaleTimeString();
    const sig = JSON.stringify([o.counts, o.groups, tapi]);
    if (sig === overviewSig) return;
    overviewSig = sig;
    renderOverview(o);
  } catch (e) {
    toast(e.message);
  }
}

// ---- inbox ----

let inbox = [], merges = [], inboxSig = "", inboxHeld = false, inboxTyping = false;
const inboxDraft = new Map();
const isheet = inboxSheet(inboxDraft, {
  close: () => resumeInbox(),
  done: () => {
    inboxSig = "";
    refresh();
  },
});

function renderInbox() {
  const rows = $("i-rows");
  rows.replaceChildren();
  $("i-count").textContent = inbox.length ? String(inbox.length) : "";
  for (const id of [...inboxDraft.keys()]) if (!inbox.some((it) => it.id === id)) inboxDraft.delete(id);
  if (merges.length) {
    const m = el("div", "inbox-merges");
    m.append(el("div", "sub", "merges by pekoed (on your \"merge\")"), ...merges.map(mergeRow));
    rows.append(m);
  }
  if (!inbox.length) {
    rows.append(el("div", "empty", "nothing waits for you"));
    return;
  }
  for (const it of inbox) {
    rows.append(inboxCard(it, draftOf(inboxDraft, it), {
      busy: () => isheet.isOpen(),
      focus: () => (inboxTyping = true),
      blur: () => {
        inboxTyping = false;
        setTimeout(resumeInbox, 300); // after a click on answer… has landed
      },
      answer: (x) => isheet.open(x),
    }));
  }
}

const holdInbox = () => inboxTyping || isheet.isOpen();

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

// ---- the detail view: a session, or tapi chat ----

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
}

// waitBody: what the agent asks, and the buttons. A permission prompt:
// deny / allow / always (the TUI's n / y / A), each through the sheet. A
// question: the keypad next to the screen, or send.
function waitBody(s, box) {
  const t = s.pending_tool;
  if (t) {
    const tool = el("div", "tool");
    tool.append(el("span", "ic", "?"), txt(t.name));
    box.append(tool);
    if (t.input) box.append(el("pre", "input", t.input));
    const cut = cutNote(t);
    if (cut) {
      const w = el("div", "also");
      w.append(el("span", "ic", "⚠"), txt(" " + cut));
      box.append(w);
    }
    if (t.also_in && t.also_in.length) {
      const also = el("div", "also");
      also.append(el("span", "ic", "⚠"), txt(" " + t.touches + " also changed in " + t.also_in.join(", ")));
      box.append(also);
    }
  }
  if (s.last_message) box.append(el("div", "msg", s.last_message));
  if (s.answerable) {
    const row = el("div", "row answer");
    for (const [cls, word, label] of [["deny", "deny", "✗ deny"], ["allow", "allow", "✓ allow"], ["always", "always", "✓✓ always"]]) {
      const b = el("button", "btn " + cls, label);
      b.type = "button";
      if (word === "always") b.title = "the agent's \"don't ask again\" (the TUI's A)";
      b.addEventListener("click", () => confirmAnswer(s, word));
      row.append(b);
    }
    box.append(row);
  } else if (s.wait_kind === "question") {
    box.append(el("div", "msg", "a question: answer with the keypad next to the screen, or send words, watching the screen"));
  }
}

function renderDetailHead() {
  const isTapi = current === TAPI;
  $("d-kind").textContent = isTapi ? "◆ tapi" : "◆ Session";
  applyTapiMode();
  const head = $("d-head"), ask = $("d-ask");
  head.replaceChildren();
  const s = detailTarget();
  renderTarget(s);
  renderKeypad(s);
  if (isTapi) {
    $("d-name").textContent = "tapi";
    head.append(tapiLine(tapi));
    head.append(el("div", "line2", "orchestrator · reads every repository, starts sessions and hands work to them"));
    if (busy) return;
    ask.replaceChildren();
    ask.hidden = !s || s.state !== "waiting" || tapiMode() === "chat";
    if (ask.hidden) return;
    const body = el("div", "body");
    if (s.answerable) body.append(el("div", "also r-warn", "⚠ tapi waits for a permission: answer it in the terminal (pekoe TUI). send and the keys are off until then."));
    else if (s.wait_kind === "question") body.append(el("div", "msg", "a question: answer with the keypad or send, watching the screen"));
    if (s.last_message) body.append(el("div", "msg", s.last_message));
    ask.append(body);
    return;
  }
  $("d-name").textContent = current || "";
  if (!s) {
    head.append(el("div", "empty", current + " is gone"));
    if (!busy) ask.hidden = true;
    return;
  }
  const l2 = el("div", "line2");
  const more = details(s, false);
  l2.append(txt(s.repo), sep(), txt(s.agent), ...(more.length ? [sep(), ...more] : []));
  head.append(line1(s), l2);
  if (busy) return; // the sheet is open: keep what it was opened from
  ask.replaceChildren();
  ask.hidden = s.state !== "waiting";
  if (!ask.hidden) {
    const body = el("div", "body");
    waitBody(s, body);
    ask.append(body);
  }
}

// renderTarget: where send goes, right above the box: the same `current`
// doSend posts to.
function renderTarget(s) {
  const isTapi = current === TAPI;
  const to = $("d-to"), note = $("d-send-note");
  to.classList.toggle("tapi", isTapi);
  to.classList.toggle("gone", !s);
  $("d-send").classList.toggle("chat", isTapi);
  let why = "";
  if (isTapi) {
    $("d-to-slug").textContent = "tapi";
    $("d-to-repo").textContent = s ? "orchestrator · all repositories" : tapi && tapi.started ? "has exited" : "is not started";
    if (tapi && !tapi.sendable) why = "pekoed does not take a send to tapi here";
    else if (s && s.answerable) why = "tapi waits for a permission: answer it in the terminal (pekoe TUI) before sending";
    $("d-text").placeholder = "ask tapi · ⌘⏎ to send";
  } else {
    $("d-to-slug").textContent = current || "";
    $("d-to-repo").textContent = s ? [s.repo, s.branch !== s.slug ? s.branch : ""].filter(Boolean).join(" · ") : "is gone";
    // a permission menu takes the first character as its choice: pekoed
    // refuses a send until it is answered (409)
    if (s && s.answerable) why = "waits for a permission: answer allow / deny / always above first";
    $("d-text").placeholder = "send a short instruction · ⌘⏎ to send";
  }
  note.textContent = why;
  note.hidden = !why;
  $("d-send-btn").disabled = !s || !!why || sending || preparing;
  $("d-attach").classList.toggle("off", preparing || attached(current).length >= IMG_MAX || !!why);
  $("d-send").classList.toggle("ready", $("d-text").value.trim() !== "" || attached(current).length > 0);
}

// ---- the screen, kept still while text in it is selected or paused ----

let screenSig = "", screenPaused = false, screenHeld = false;

function selectingIn(node) {
  const sel = document.getSelection();
  if (!sel || sel.isCollapsed || !sel.rangeCount) return false;
  return node.contains(sel.anchorNode) || node.contains(sel.focusNode);
}

function renderScreenState() {
  const btn = $("d-pause"), st = $("d-live");
  btn.textContent = screenPaused ? "▶ resume" : "❚❚ pause";
  btn.setAttribute("aria-pressed", String(screenPaused));
  st.replaceChildren();
  if (screenPaused) st.append(colored("warn", "❚❚"), txt(" paused"));
  else if (screenHeld) st.append(colored("dim", "○"), txt(" hold · selecting"));
  else st.append(colored("ok", "●"), txt(" live"));
}

async function refreshScreen() {
  if (!current || screenPaused) return;
  if (current === TAPI && tapiMode() === "chat") return; // the chat reads the transcript
  const pre = $("d-screen");
  if (selectingIn(pre)) {
    if (!screenHeld) {
      screenHeld = true;
      renderScreenState();
    }
    return;
  }
  const slug = current;
  try {
    const r = await api(apiBase(slug) + "/screen");
    if (slug !== current || screenPaused) return;
    if (selectingIn(pre)) return;
    $("d-at").textContent = new Date().toLocaleTimeString();
    const sig = slug + "\n" + JSON.stringify([r.rows, r.lines, r.dead]);
    if (sig === screenSig) return;
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

document.addEventListener("selectionchange", () => {
  if (screenHeld && !screenPaused && !selectingIn($("d-screen"))) {
    screenHeld = false;
    renderScreenState();
    refreshScreen();
  }
  if (chat.held && !selectingIn($("c-log"))) renderChat(true);
});

// ---- keypad ----

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
  // one key at a time: no second Down (or Enter) before pekoed sent the first
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
  setTimeout(() => {
    pressing = false;
    if (current) renderKeypad(detailTarget());
  }, 200);
}

// ---- send: words and images ----

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

let tsend = null; // {slug, text, images} in the send sheet

function doSend(ev) {
  if (ev) ev.preventDefault();
  const text = $("d-text").value.trim();
  const slug = current; // fixed here, from the same `current` the label shows
  const images = attached(slug);
  if ((!text && !images.length) || !slug || sending || preparing || $("d-send-btn").disabled) return;
  if (slug === TAPI) {
    if (!tapiUp()) return toast("tapi is not running");
    if (tapi.confirm_send !== false) return openSendSheet(slug, text, images);
    return sendTo(slug, text, images);
  }
  if (!sessions.some((x) => x.slug === slug)) return toast(slug + " is gone");
  // images go through the sheet: the target, the text and each image seen
  // once more before they leave
  if (images.length) return openSendSheet(slug, text, images);
  sendTo(slug, text, []);
}

function openSendSheet(slug, text, images) {
  tsend = { slug, text, images };
  busy = true;
  const s = sessions.find((x) => x.slug === slug);
  showSendSheet(slug === TAPI, slug, s && s.repo, text, images);
}

function closeSendSheet() {
  $("tsheet").hidden = true;
  tsend = null;
  busy = false;
}

async function sheetSend() {
  if (!tsend || sending) return;
  const { slug, text, images } = tsend;
  $("tsheet-ok").disabled = true;
  await sendTo(slug, text, images);
  closeSendSheet();
  refresh();
}

async function sendTo(slug, text, images) {
  if (sending) return;
  sending = true;
  $("d-send-btn").disabled = true;
  try {
    const r = await api(apiBase(slug) + "/send", sendBody(text, images));
    drafts.delete(slug);
    dropAttached(slug, images);
    const ta = $("d-text");
    if (current === slug && ta.value.trim() === text) ta.value = "";
    toast("sent → " + (slug === TAPI ? "tapi" : slug) + imagesNote(images.length), true);
    if (slug === TAPI) {
      // ● until it shows up in the transcript (✓)
      chat.pending.push({ text: (r && typeof r.text === "string" && r.text) || text, at: Date.now() });
      if (current === TAPI && tapiMode() === "chat") {
        renderChat(true);
        toBottom();
      }
      setTimeout(refreshChat, 600);
    }
    setTimeout(refreshScreen, 600);
  } catch (e) {
    toast((slug === TAPI ? "tapi" : slug) + ": " + e.message);
  }
  sending = false;
  if (current) renderTarget(detailTarget());
}

// ---- tapi chat ----

const CHAT_KEEP = 1500;
const chat = {
  cursor: "", items: [], earlier: false, ok: null, why: "", loading: false,
  sig: "", tailSig: "", held: false, open: new Set(), pending: [],
};
let chatChoice = "chat";
try {
  if (localStorage.getItem("pekoe.gui.tapiView") === "screen") chatChoice = "screen";
} catch (e) { /* no storage: chat */ }
let spinTimer = 0;

const tapiMode = () => (chatChoice === "chat" && chat.ok !== false && tapiUp() ? "chat" : "screen");
const atBottom = (log) => log.scrollHeight - log.scrollTop - log.clientHeight < 40;

function toBottom() {
  const log = $("c-log");
  log.scrollTop = log.scrollHeight;
  $("c-jump").hidden = true;
}

function renderChat(force) {
  if (current !== TAPI) return;
  const log = $("c-log");
  const sig = chat.items.length + "|" + chat.pending.length + "|" + [...chat.open].join(",") + "|" + chat.earlier + "|" + chat.ok;
  if (!force && sig === chat.sig) return;
  if (selectingIn(log)) {
    chat.held = true;
    return;
  }
  chat.held = false;
  const stick = atBottom(log);
  const top = log.scrollTop;
  chat.sig = sig;
  const items = $("c-items");
  items.replaceChildren();
  if (chat.earlier) items.append(el("div", "divider", "earlier messages: see screen"));
  if (!chat.items.length && !chat.pending.length) items.append(el("div", "empty", chat.ok === true ? "no messages yet" : "loading…"));
  items.append(chatView(chat.items, chat.pending, "tapi", chat.open, () => renderChat(true)));
  if (stick) toBottom();
  else log.scrollTop = top;
}

// renderChatTail: what tapi does now, under the last bubble: working (a
// spinner), a permission prompt (answered at the terminal) or a question
// (the keypad moves here).
function renderChatTail() {
  const tail = $("c-tail");
  const t = detailTarget();
  const pad = $("d-keys");
  const sig = t ? [t.state, t.wait_kind, t.answerable, t.state_since].join("|") : "-";
  const stick = atBottom($("c-log"));
  if (sig !== chat.tailSig) {
    chat.tailSig = sig;
    tail.replaceChildren();
    if (t && t.answerable) {
      const card = el("div", "card wait-card");
      const head = el("div", "chead");
      head.append(el("span", "ic r-secondary", "◆"), el("b", null, "tapi asks for a permission"), colored("warn", "● wait " + ago(t.state_since), "st"));
      card.append(head, el("div", "foot", "answer it in the terminal (pekoe TUI). send and the keys are off until then."));
      tail.append(card);
    } else if (t && t.state === "running") {
      const row = el("div", "turn agent");
      const typing = el("span", "typing");
      typing.append(el("span", "spin r-ok", SPIN[0]), el("span", "r-dim think", "thinking · " + ago(t.state_since)));
      row.append(bubbleLine(typing, null));
      tail.append(row);
    }
  }
  if (tapiMode() === "chat" && questionUp(t)) {
    if (pad.parentNode !== tail) tail.append(pad);
    pad.hidden = false;
  } else if (pad.parentNode === tail) {
    $("d-panel").after(pad);
  }
  if (stick) toBottom();
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

function applyTapiMode() {
  const isTapi = current === TAPI;
  const chatOn = isTapi && tapiMode() === "chat";
  $("c-ttl").hidden = !isTapi;
  $("d-ttl").hidden = isTapi;
  $("c-chat").hidden = !chatOn;
  $("d-screen-box").hidden = chatOn;
  $("d-panel").classList.toggle("chat-panel", chatOn);
  const why = $("c-why");
  why.hidden = !isTapi || chatOn || chatChoice !== "chat" || chat.ok !== false;
  why.textContent = why.hidden ? "" : "chat unavailable: " + chat.why;
  if (isTapi) {
    const word = !tapi ? "not readable" : !tapi.started ? "not started" : !tapi.alive ? "exited" : (tapi.look && tapi.look.word) || tapi.state;
    $("c-state").textContent = "· " + word;
    const eyes = !tapiUp() ? "- -" : tapi.state === "running" ? "● ●" : tapi.state === "waiting" ? "◕ ◕" : "• •";
    const [a, , b] = [...eyes];
    const cell = (g) => el("i", { "●": "e-dot", "◕": "e-puppy" }[g] || null, g);
    $("c-face").replaceChildren(cell(a), el("i", null, " "), cell(b));
    $("c-v-chat").setAttribute("aria-pressed", String(tapiMode() === "chat"));
    $("c-v-screen").setAttribute("aria-pressed", String(tapiMode() === "screen"));
    renderChatTail();
  } else {
    clearInterval(spinTimer);
    if ($("d-keys").parentNode !== $("d-panel").parentNode) $("d-panel").after($("d-keys"));
  }
  if (chatOn) renderChat(false);
}

function chooseTapiView(v) {
  chatChoice = v;
  try {
    localStorage.setItem("pekoe.gui.tapiView", v);
  } catch (e) { /* not kept */ }
  renderDetailHead();
  if (v === "chat") refreshChat().then(toBottom);
  else {
    screenSig = "";
    refreshScreen();
  }
}

const sameWords = (s) => String(s || "").replace(/\s+/g, " ").trim();

async function refreshChat() {
  if (current !== TAPI || chat.loading) return;
  chat.loading = true;
  const wasMode = tapiMode();
  try {
    const r = await api("/api/tapi/transcript?after=" + encodeURIComponent(chat.cursor));
    if (current !== TAPI) return;
    if (r.reset) {
      if (r.new_conversation && chat.items.length) chat.items.push({ kind: "conv" });
      else {
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
    markRewound(chat.items);
    chat.cursor = String(r.cursor || "");
    const seen = new Set(chat.items.filter((it) => it.kind === "human").slice(-20).map((it) => sameWords(it.text)));
    const old = Date.now() - 5 * 60 * 1000;
    chat.pending = chat.pending.filter((p) => !seen.has(sameWords(p.text)) && p.at > old);
    chat.ok = true;
    chat.why = "";
  } catch (e) {
    chat.ok = false;
    chat.why = e.message;
  } finally {
    chat.loading = false;
  }
  if (current !== TAPI) return;
  if (tapiMode() !== wasMode) {
    renderDetailHead();
    if (tapiMode() === "chat") toBottom();
    else {
      screenSig = "";
      refreshScreen();
    }
  } else if (tapiMode() === "chat") renderChat(false);
  else applyTapiMode();
}

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
  view = current ? "detail" : location.hash === "#/inbox" ? "inbox" : "overview";
  inboxTyping = false;
  ta.value = (current && drafts.get(current)) || "";
  for (const v of ["overview", "inbox", "detail"]) $(v).hidden = view !== v;
  for (const [id, on] of [["n-overview", view === "overview"], ["n-inbox", view === "inbox"], ["n-tapi", current === TAPI]]) {
    if (on) $(id).setAttribute("aria-current", "page");
    else $(id).removeAttribute("aria-current");
  }
  renderSide();
  if (view === "detail") {
    $("d-screen").textContent = "";
    screenSig = "";
    screenPaused = screenHeld = false;
    renderScreenState();
    chat.sig = chat.tailSig = "";
    renderThumbs();
    renderDetailHead();
    if (current === TAPI) refreshChat().then(() => current === TAPI && toBottom());
    refreshScreen();
  } else if (view === "inbox") {
    inboxSig = "";
    refreshInbox();
  } else {
    overviewSig = "";
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
$("d-send").addEventListener("submit", doSend);
$("d-text").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.isComposing) doSend(e);
});
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
$("tsheet-cancel").addEventListener("click", closeSendSheet);
$("tsheet-ok").addEventListener("click", sheetSend);
$("tsheet").addEventListener("click", (e) => { if (e.target === $("tsheet")) closeSendSheet(); });
// Escape closes a sheet (cancel); it never answers
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  if (!$("sheet").hidden) answers.close();
  else if (!$("isheet").hidden) isheet.close();
  else if (!$("tsheet").hidden) closeSendSheet();
});
$("d-pause").addEventListener("click", () => {
  screenPaused = !screenPaused;
  screenHeld = false;
  renderScreenState();
  if (!screenPaused) refreshScreen();
});
for (const b of $("d-keys").querySelectorAll("button[data-key]")) b.addEventListener("click", () => pressKey(b.dataset.key));
$("c-v-chat").addEventListener("click", () => chooseTapiView("chat"));
$("c-v-screen").addEventListener("click", () => chooseTapiView("screen"));
$("c-log").addEventListener("scroll", () => ($("c-jump").hidden = atBottom($("c-log"))), { passive: true });
$("c-jump").addEventListener("click", () => $("c-log").scrollTo({ top: $("c-log").scrollHeight, behavior: "smooth" }));

guiSignIn().then(() => {
  api("/api/me").then((me) => {
    if (me) $("who").textContent = me.login + (me.device ? " · " + me.device : "");
  }).catch(() => {});
  refresh().then(route);
}).catch(() => {});
