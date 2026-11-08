// boba web: a display layer over bobad's /api/. Everything the page shows is
// put into the DOM with textContent (agent output is untrusted text).
"use strict";

const $ = (id) => document.getElementById(id);
const LOOK = {
  running: ["●", "run", "s-run"],
  waiting: ["●", "wait", "s-wait"],
  idle: ["○", "idle", "s-idle"],
  creating: ["◌", "creating", "s-idle"],
  done: ["✓", "done", "s-done"],
  reviewing: ["●", "review", "s-run"],
  approved: ["✓", "approved", "s-run"],
  landed: ["✓", "landed", "s-idle"],
  failed: ["✗", "fail", "s-fail"],
};

let sessions = [];
let current = null; // slug shown in the detail view
let listTimer = 0, screenTimer = 0;
let busy = false;

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

async function api(path, body) {
  const opt = { headers: {} };
  if (body !== undefined) {
    opt.method = "POST";
    opt.headers["Content-Type"] = "application/json";
    opt.headers["X-Boba-Web"] = "1";
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
  t.textContent = text;
  t.className = "toast" + (ok ? " ok" : "");
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.hidden = true), ok ? 1800 : 4000);
}

function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return Math.floor(s) + "s";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h";
  return Math.floor(s / 86400) + "d";
}

function look(s) {
  return LOOK[s.state] || ["○", s.state, "s-idle"];
}

// ---- list ----

function sessionRow(s) {
  const [ic, word, cls] = look(s);
  const b = el("button", "row-s " + cls);
  b.type = "button";
  const l1 = el("div", "line1");
  l1.append(el("span", "ic", ic), el("span", "slug", s.slug), el("span", "st", word + " " + ago(s.state_since)));
  const bits = [s.repo, s.branch !== s.slug ? s.branch : ""];
  if (s.diffstat && s.diffstat.files) bits.push(`+${s.diffstat.added} -${s.diffstat.deleted}`);
  if (s.pr) bits.push("#" + s.pr);
  if (s.error) bits.push(s.error);
  else if (s.state !== "waiting" && s.last_tool) bits.push(s.last_tool);
  b.append(l1, el("div", "line2", bits.filter(Boolean).join(" · ")));
  b.addEventListener("click", () => go(s.slug));
  return b;
}

// waitBody: what the agent is asking, and the buttons (allow/deny only for
// a permission prompt; a question is answered by sending words).
function waitBody(s, box) {
  const t = s.pending_tool;
  if (t) {
    box.append(el("div", "tool", t.name));
    if (t.input) box.append(el("pre", "input", t.input));
    if (t.also_in && t.also_in.length) box.append(el("div", "also", "⚠ " + t.touches + " also changed in " + t.also_in.join(", ")));
  }
  if (s.last_message) box.append(el("div", "msg", s.last_message));
  if (s.answerable) {
    const row = el("div", "row");
    const deny = el("button", "btn deny", "deny");
    const allow = el("button", "btn allow", "allow");
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
  const list = $("list");
  list.replaceChildren();
  if (!sessions.length) {
    list.append(el("div", "empty", "no sessions"));
    return;
  }
  for (const s of sessions) {
    if (s.state === "waiting") {
      const card = el("div", "wait-card");
      card.append(sessionRow(s));
      waitBody(s, card);
      list.append(card);
    } else {
      list.append(sessionRow(s));
    }
  }
}

function renderSummary() {
  const n = (st) => sessions.filter((s) => s.state === st).length;
  const parts = [];
  if (n("waiting")) parts.push(n("waiting") + " wait");
  if (n("running")) parts.push(n("running") + " run");
  if (n("done")) parts.push(n("done") + " done");
  $("sum").textContent = parts.join(" · ");
  document.title = n("waiting") ? `(${n("waiting")}) boba` : "boba";
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
  ask.replaceChildren();
  if (!s) {
    head.append(el("div", "empty", current + " is gone"));
    ask.hidden = true;
    return;
  }
  const [ic, word, cls] = look(s);
  const l1 = el("div", "line1 " + cls);
  l1.append(el("span", "ic", ic), el("span", "slug", s.slug), el("span", "st", word + " " + ago(s.state_since)));
  const bits = [s.repo + " · " + s.branch, s.agent];
  if (s.diffstat && s.diffstat.files) bits.push(`${s.diffstat.files} files +${s.diffstat.added} -${s.diffstat.deleted}`);
  if (s.error) bits.push(s.error);
  head.append(l1, el("div", "line2", bits.filter(Boolean).join(" · ")));
  ask.hidden = s.state !== "waiting";
  if (!ask.hidden && !busy) waitBody(s, ask);
}

async function refreshScreen() {
  if (!current) return;
  const pre = $("d-screen");
  try {
    const r = await api("/api/sessions/" + encodeURIComponent(current) + "/screen");
    const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 24;
    pre.textContent = (r.lines || []).join("\n") + (r.dead ? "\n[agent exited]" : "");
    if (atBottom) pre.scrollTop = pre.scrollHeight;
    $("d-at").textContent = new Date().toLocaleTimeString();
  } catch (e) {
    pre.textContent = "(" + e.message + ")";
  }
}

// ---- answer: always through a confirmation that shows what is allowed ----

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
  $("sheet").hidden = false;
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
  if (!text || !current) return;
  const btn = $("d-send-btn");
  btn.disabled = true;
  try {
    await api("/api/sessions/" + encodeURIComponent(current) + "/send", { text });
    ta.value = "";
    toast("sent", true);
    setTimeout(refreshScreen, 600);
  } catch (e) {
    toast(e.message);
  }
  btn.disabled = false;
}

// ---- routing & polling ----

function go(slug) {
  location.hash = slug ? "#/s/" + encodeURIComponent(slug) : "";
}

function route() {
  const m = location.hash.match(/^#\/s\/(.+)$/);
  current = m ? decodeURIComponent(m[1]) : null;
  $("list").hidden = !!current;
  $("detail").hidden = !current;
  $("back").hidden = !current;
  $("title").hidden = !!current;
  if (current) {
    $("d-screen").textContent = "";
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

api("/api/me").then((me) => {
  if (me) $("who").textContent = me.login + (me.device ? " · " + me.device : "");
}).catch(() => {});
refresh().then(route);
