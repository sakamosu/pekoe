// pekoe web: a display layer over pekoed's /api/, the phone's page (the
// local GUI's desktop page is gui/gui.js). What both pages draw with is in
// lib/ (ES modules, no bundler). Everything the page shows is put into the
// DOM with textContent (agent output is untrusted text).

import { $, el, colored, txt, sep, toast } from "./lib/dom.js";
import { api, guiSignIn, TAPI, apiBase } from "./lib/api.js";
import { requestId, questionId, ago, details, line1, cutNote, questionUp } from "./lib/fmt.js";
import { termLines, screenRows } from "./lib/term.js";
import { IMG_MAX, prepareImage, thumbs, sendBody, imagesNote } from "./lib/images.js";
import { draftOf, inboxCard, mergeRow } from "./lib/inbox.js";
import { renderCounts, renderTapi, tapiLine, parentLabel, stepCell, missing } from "./lib/overview.js";
import { SPIN, bubbleLine, markRewound, chatView } from "./lib/chat.js";
import { answerSheet, inboxSheet, showSendSheet } from "./lib/sheets.js";

let sessions = [];
let view = "list"; // list | inbox | overview | detail
let current = null; // slug shown in the detail view
let backTo = ""; // the hash the detail view's back button returns to
let tapi = null; // /api/tapi: tapi's state and the inbox / ledger counts
let listTimer = 0, screenTimer = 0, chatTimer = 0;
let busy = false;
let sending = false; // a send is in flight
let pressing = false; // a keypad key is in flight
let touching = false; // a finger is on the list: do not move rows under it
const drafts = new Map(); // slug → unsent text, so one session's words never ride along to another
const attachments = new Map(); // slug → images for the next send (same rule): [{data (base64), thumb (data URL), w, h, bytes, type}]
let preparing = false; // images being read and re-encoded

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
    $("d-send-btn").disabled = !s || !tapi.sendable || s.answerable || sending || preparing;
    $("d-attach").hidden = !!tapi && !tapi.sendable;
    renderComposer(); // no attach, nothing to fold
    return;
  }
  note.hidden = true;
  to.hidden = false;
  $("d-to-slug").textContent = current || "";
  $("d-to-repo").textContent = s ? [s.repo, s.branch !== s.slug ? s.branch : ""].filter(Boolean).join(" · ") : "is gone";
  to.classList.toggle("gone", !s);
  $("d-send-btn").disabled = !s || sending || preparing;
  $("d-attach").hidden = false;
  renderComposer();
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
  head.append(tapiLine(tapi));
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

// ---- answer: always through the confirm sheet (lib/sheets.js) ----

const answers = answerSheet({
  close: () => (busy = false),
  done: () => refresh(),
});

function confirmAnswer(s, answer) {
  busy = true;
  answers.open(s, answer);
}

// ---- send ----

async function doSend(ev) {
  ev.preventDefault();
  const ta = $("d-text");
  const text = ta.value.trim();
  // the target is fixed here, from the same `current` the label shows
  const slug = current;
  const images = attached(slug);
  if ((!text && !images.length) || !slug || sending || preparing) return;
  if (slug === TAPI) {
    openTapiSheet(text, images);
    return;
  }
  if (!sessions.some((x) => x.slug === slug)) {
    toast(slug + " is gone");
    return;
  }
  // images go through the sheet: the target, the text and each image
  // seen once more before they leave the phone
  if (images.length) {
    openSendSheet(slug, text, images);
    return;
  }
  sendTo(slug, text, []);
}

// sendTo posts a send to a session. images: the prepared attachments.
async function sendTo(slug, text, images) {
  if (sending) return;
  sending = true;
  $("d-send-btn").disabled = true;
  const ta = $("d-text");
  try {
    await api("/api/sessions/" + encodeURIComponent(slug) + "/send", sendBody(text, images));
    drafts.delete(slug);
    dropAttached(slug, images);
    if (current === slug && ta.value.trim() === text) {
      ta.value = "";
      fitComposer();
    }
    toast("sent → " + slug + imagesNote(images.length), true);
    setTimeout(refreshScreen, 600);
  } catch (e) {
    toast(slug + ": " + e.message);
  }
  sending = false;
  if (current) renderTarget(detailTarget());
}

// ---- images: attached to the next send (lib/images.js) ----
// Kept per target, like the drafts: one session's images never ride along
// to another.

function attached(slug) {
  return (slug && attachments.get(slug)) || [];
}

function dropAttached(slug, images) {
  const left = attached(slug).filter((im) => !images.includes(im));
  if (left.length) attachments.set(slug, left);
  else attachments.delete(slug);
  if (current === slug) renderThumbs();
}

async function addImages(files) {
  const slug = current;
  if (!slug || !files.length) return;
  const have = attached(slug);
  const room = IMG_MAX - have.length;
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
  if (current === slug) renderThumbs();
  if (current) renderTarget(detailTarget());
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
  $("d-attach").classList.toggle("off", preparing || images.length >= IMG_MAX);
  renderComposer();
}

// ---- the send sheet: tapi's (the whole text, every time unless
// [web] confirm_tapi_send = false) and a session's when images go along ----
// tapi acts on every repository, so its send weighs more than one
// session's: the sheet says the target is tapi and shows every character
// that will be typed. The text and the images are fixed when the sheet
// opens. [web] confirm_tapi_send = false (tapi.confirm_send) leaves the
// sheet out and sends at once; pekoed checks the send the same way either
// way. An older pekoed without the field gets the sheet.

let tsend = null; // {slug, text, images} in the sheet (or being sent without one)

function tapiConfirm() {
  return !tapi || tapi.confirm_send !== false;
}

function openTapiSheet(text, images) {
  if (!tapiUp()) {
    toast("tapi is not running");
    return;
  }
  if (!tapi.sendable) {
    toast("sending to tapi needs [web] allow_devices");
    return;
  }
  tsend = { slug: TAPI, text, images };
  if (!tapiConfirm()) {
    doSheetSend();
    return;
  }
  showSheet();
}

function openSendSheet(slug, text, images) {
  tsend = { slug, text, images };
  showSheet();
}

function showSheet() {
  const { slug, text, images } = tsend;
  busy = true;
  const s = sessions.find((x) => x.slug === slug);
  showSendSheet(slug === TAPI, slug, s && s.repo, text, images);
}

function closeTapiSheet() {
  $("tsheet").hidden = true;
  tsend = null;
  busy = false;
}

async function doSheetSend() {
  if (!tsend || sending) return;
  if (tsend.slug !== TAPI) {
    const { slug, text, images } = tsend;
    $("tsheet-ok").disabled = true;
    await sendTo(slug, text, images);
    closeTapiSheet();
    refresh();
    return;
  }
  const { text, images } = tsend;
  $("tsheet-ok").disabled = true;
  sending = true;
  $("d-send-btn").disabled = true; // no second send while this one is out
  try {
    const r = await api("/api/tapi/send", sendBody(text, images));
    drafts.delete(TAPI);
    dropAttached(TAPI, images);
    const ta = $("d-text");
    if (current === TAPI && ta.value.trim() === text) {
      ta.value = "";
      fitComposer();
    }
    toast("sent → tapi" + imagesNote(images.length), true);
    // ● until it shows up in the transcript (✓): what was typed, with
    // the image lines pekoed added
    chat.pending.push({ text: (r && typeof r.text === "string" && r.text) || text, at: Date.now() });
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
// Shown only while the session waits on a question (questionUp); pekoed
// refuses keys in any other state (a permission menu stays allow/deny only).

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
const isheet = inboxSheet(inboxDraft, {
  close: () => resumeInbox(),
  done: () => {
    inboxSig = "";
    refresh();
  },
});

function openInboxSheet(it) {
  isheet.open(it);
}

function card(it) {
  return inboxCard(it, draftOf(inboxDraft, it), {
    busy: () => isheet.isOpen(),
    focus: () => (inboxTyping = true),
    blur: () => {
      inboxTyping = false;
      setTimeout(resumeInbox, 300); // after a tap on answer… has landed
    },
    answer: openInboxSheet,
  });
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
  for (const it of inbox) rows.append(card(it));
}

function holdInbox() {
  return inboxTyping || isheet.isOpen();
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

// ---- overview: counts, tapi, and every session's flow (read only) ----

let overviewSig = "";

function flowRow(f) {
  const wrap = el("div", "flow");
  const b = el("button", "row-s");
  b.type = "button";
  b.append(line1(f));
  b.addEventListener("click", () => go(f.slug));
  const steps = el("div", "steps");
  f.steps.forEach((s, i) => {
    if (i) steps.append(el("span", "arrow", "→"));
    steps.append(stepCell(s, true));
  });
  wrap.append(b, steps);
  const notes = el("div", "miss");
  if (f.merge && f.merge.ready) {
    notes.append(el("div", "r-ok", "✓ evidence ready · waiting for a person to merge" + (f.merge.inbox ? " (inbox " + questionId(f.merge.inbox) + ")" : "")));
  }
  notes.append(...missing(f));
  if (notes.childNodes.length) wrap.append(notes);
  return wrap;
}

function renderOverview(o) {
  renderCounts($("o-counts"), o.counts || {});
  renderTapi($("o-tapi"), tapi);
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
      t.append(sep(), txt("request " + requestId(g.request.id) + " "), colored(g.request.status === "open" ? "warn" : "dim", g.request.status));
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

// ---- tapi chat: the conversation as bubbles (lib/chat.js) ----

const CHAT_KEEP = 1500; // items kept in the page
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

// fitComposer: the box grows with its lines (one line to start, up to its
// max-height), and the row shows what can be done now (renderComposer).
function fitComposer() {
  const ta = $("d-text");
  ta.style.height = "";
  ta.style.height = Math.max(ta.scrollHeight + 2, 40) + "px"; // + the border
  renderComposer();
}

// renderComposer: send is quiet with nothing to send and stands out with
// something (words or images). While typing (focus and words in the box)
// the attach icon folds into ›, as in a messenger; › (or leaving the box)
// brings it back until the next keystroke.
let unfolded = false;
function renderComposer() {
  const ta = $("d-text"), form = $("d-send");
  const words = ta.value.trim() !== "";
  form.classList.toggle("ready", words || attached(current).length > 0);
  const typing = document.activeElement === ta && words;
  if (!typing) unfolded = false;
  form.classList.toggle("folded", typing && !unfolded && !$("d-attach").hidden);
}

// fitViewport: the detail view fills what can be seen (body.pinned). On iOS
// the software keyboard does not shrink the layout viewport (nor 100dvh); it
// shrinks the visual viewport and pans it. So the page takes the visual
// viewport's height and offset while the keyboard is up, and the composer
// stays right above it; without the keyboard it takes the layout viewport's
// height. The heights the browser reports are what it paints: a home-screen
// app on iOS can come back from the keyboard with innerHeight,
// visualViewport.height and 100dvh all short by the status bar (a WebKit
// bug), and then the band under them is dead (the system draws it, the page
// never shows there). The page does not reach past them (that put the
// composer into the dead band, and under the keyboard); it drops the bottom
// inset instead (shortBelow) and tries to get the full height back
// (healViewport).
function fitViewport() {
  if (!document.body.classList.contains("pinned")) return;
  const vv = window.visualViewport;
  if (vv && vv.scale > 1.01) return; // pinch-zoomed: keep the page as it was, let it pan
  const h = Math.max(window.innerHeight, document.documentElement.clientHeight);
  const seen = vv ? vv.height : h;
  const kb = h - seen > 120; // the keyboard covers the bottom
  const r = document.documentElement.style;
  r.setProperty("--vvh", (kb ? seen : h) + "px");
  r.setProperty("--vvt", (kb && vv ? vv.offsetTop : 0) + "px");
  // the keyboard covers the home indicator, or the dead band does: no
  // bottom inset above it
  document.body.classList.toggle("kb", kb);
  document.body.classList.toggle("cut", !kb && shortBelow(h) > 0);
}

// shortBelow: by how much the page stops above the screen's bottom in a
// home-screen app (the WebKit bug above; 0 when it reaches it). Such an app
// (black-translucent status bar) runs from the screen's top to its bottom,
// under the status bar (a top inset). It is counted when the app is full
// width, the top inset is there (the page still starts at the top, so what
// is missing is at the bottom) and the shortfall is about a status bar.
function shortBelow(h) {
  const standalone = navigator.standalone === true || (window.matchMedia && matchMedia("(display-mode: standalone)").matches);
  if (!standalone || !window.screen) return 0;
  const land = window.innerWidth > h;
  const sw = land ? Math.max(screen.width, screen.height) : Math.min(screen.width, screen.height);
  const sh = land ? Math.min(screen.width, screen.height) : Math.max(screen.width, screen.height);
  const short = sh - h;
  if (Math.abs(window.innerWidth - sw) > 1 || short <= 0 || short > 80) return 0;
  const top = parseFloat(getComputedStyle(document.body).paddingTop) || 0; // env(safe-area-inset-top), app.css
  return top > 0 ? short : 0;
}

// healViewport: after the keyboard goes, a home-screen app on iOS may keep
// the shrunken height (shortBelow). Taking the full-height page out of the
// layout and back (with a reflow between) makes WebKit measure the viewport
// again; the places scrolled to are kept. Only when the height is short.
function healViewport() {
  if (!document.body.classList.contains("pinned")) return;
  const h = Math.max(window.innerHeight, document.documentElement.clientHeight);
  if (shortBelow(h) <= 0) return;
  if (document.activeElement === $("d-text")) return; // back in the box: taking the page out would close the keyboard
  const kept = ["c-log", "detail"].map((id) => [$(id), $(id).scrollTop]);
  const b = document.body;
  b.style.display = "none";
  void b.offsetHeight;
  b.style.display = "";
  for (const [e, top] of kept) e.scrollTop = top;
  fitViewport();
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
  items.append(chatView(chat.items, chat.pending, "tapi", chat.open, () => renderChat(true)));
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

// sameWords: a text as compared between a send and the transcript
const sameWords = (s) => String(s || "").replace(/\s+/g, " ").trim();

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
    markRewound(chat.items);
    chat.cursor = String(r.cursor || "");
    // the person's words that came through are no longer "not sent". A
    // send of more than one line comes back as a paste (pekoed takes the
    // wrapping off); the blank around and between the words is not
    // compared, so the same words never show twice
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
  renderThumbs(); // the images attached for this target (kept per target, like the drafts)
  for (const v of ["list", "inbox", "overview", "detail"]) $(v).hidden = view !== v;
  if (view !== "detail") document.body.classList.remove("chat-on");
  document.body.classList.toggle("pinned", view === "detail");
  fitViewport();
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
$("d-send").addEventListener("submit", doSend);
$("tsheet-cancel").addEventListener("click", closeTapiSheet);
$("tsheet-ok").addEventListener("click", doSheetSend);
$("d-file").addEventListener("change", (e) => {
  const files = [...(e.target.files || [])];
  e.target.value = ""; // the same photo can be chosen again
  addImages(files);
});
$("tsheet").addEventListener("click", (e) => { if (e.target === $("tsheet")) closeTapiSheet(); });
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
$("d-text").addEventListener("input", () => {
  unfolded = false; // the next keystroke folds the icons again
  fitComposer();
});
$("d-text").addEventListener("focus", renderComposer);
// leaving the box: a moment later, so a tap on › or send lands first
$("d-text").addEventListener("blur", () => {
  setTimeout(renderComposer, 0);
  setTimeout(healViewport, 150); // once the keyboard has gone
});
// › : the icons back, the keyboard stays up (no focus leaves the box)
$("d-unfold").addEventListener("pointerdown", (e) => e.preventDefault());
$("d-unfold").addEventListener("click", () => {
  unfolded = true;
  renderComposer();
  $("d-text").focus();
});
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

guiSignIn().then(() => {
  api("/api/me").then((me) => {
    if (me) $("who").textContent = me.login + (me.device ? " · " + me.device : "");
  }).catch(() => {});
  refresh().then(route);
}).catch(() => {});
