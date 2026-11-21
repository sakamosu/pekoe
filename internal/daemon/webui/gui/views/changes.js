// The sidebar's Changes view (docs/gui.md 「段階 2 の一部: Changes」, the
// TUI's [3] Changes): the files the session open in the main column
// changed since its base, each with its status (A / M / D), the lines
// added and deleted, and the sessions touching it too (⚠, the conflict
// forecast); the forecast's sum at the foot. A file opens its diff, to
// read only, in the same place (← Files goes back; not Escape, the page's
// interrupt): no comment, no
// approval, no review here (決まったこと 7: that is the TUI's). With no
// session open, the sessions that changed something, to open one. The
// title bar's box filters the files by path.
//
// GET /api/sessions/{slug}/changes and /diff?path= (guichanges.go), read
// again when the session's diffstat moves. Drawn again only when what is
// shown changed (not on every 3 s refresh: a selection in the diff, the
// focus on a row would be lost), and a diff with new text waits while
// text in the old one is selected. Paths and the diff's lines are the
// agent's: text only (el / txt), and color is only on the symbols and the
// + / − tokens.

import { el, colored, txt } from "../../lib/dom.js";
import { api, TAPI } from "../../lib/api.js";
import { diffBits } from "../dash.js";

const STATUS = { A: ["ok", "added"], M: ["warn", "modified"], D: ["err", "deleted"], T: ["warn", "type changed"] };

let ctx = null; // {go}
let ui = null; // the elements mount built
let st = { sessions: [], current: null };
let query = "";
let slug = null; // the session shown
let sig = ""; // its diffstat when the list was read: read again when it moves
let files = null; // the list read (null: not yet)
let overlaps = [];
let failed = ""; // why the list could not be read
let open = null; // the file whose diff is shown (its path)
let diff = null; // that diff (null: being read)
let diffFailed = "";
let seq = 0; // drops answers to list reads that are no longer wanted
let dseq = 0; // the same for diff reads
let drawn = ""; // what render last drew from the page's data (shownSig)
let held = false; // a draw waits for the selection in the diff to go

const statSig = (s) => (s && s.diffstat ? [s.diffstat.files, s.diffstat.added, s.diffstat.deleted].join(",") : "") + "|" + (s ? s.state : "");

function current() {
  if (!st.current || st.current === TAPI) return null;
  return st.sessions.find((s) => s.slug === st.current) || null;
}

// shownSig: what the drawing takes from the page's data (the rest it
// draws is the view's own reads, which draw when they change); the open
// diff takes nothing from it
function shownSig() {
  if (open !== null) return "diff";
  const s = current();
  if (s) return JSON.stringify([s.slug, s.branch, s.diffstat]);
  return JSON.stringify(st.sessions.filter((x) => x.diffstat && x.diffstat.files > 0 && x.state !== "landed").map((x) => [x.slug, x.branch, x.repo, x.diffstat]));
}

function selecting(node) {
  const sel = document.getSelection ? document.getSelection() : null;
  if (!sel || sel.isCollapsed || !sel.rangeCount || !node.contains) return false;
  return node.contains(sel.anchorNode) || node.contains(sel.focusNode);
}

function reason(e) {
  return String((e && e.message) || e).split("\n")[0];
}

// ---- reading ----

async function readList() {
  const my = ++seq;
  let r = null, why = "";
  try {
    r = await api("/api/sessions/" + encodeURIComponent(slug) + "/changes");
  } catch (e) {
    why = reason(e);
  }
  if (my !== seq || !ui) return;
  const was = JSON.stringify([files, overlaps, failed]);
  if (r) {
    files = r.files || [];
    overlaps = r.overlaps || [];
  } else files = files || [];
  failed = why;
  // the list is drawn when it changed (the open diff draws on its read)
  if (open === null && JSON.stringify([files, overlaps, failed]) !== was) render();
  if (open) readDiff();
}

async function readDiff() {
  const my = ++dseq;
  const path = open;
  try {
    const r = await api("/api/sessions/" + encodeURIComponent(slug) + "/diff?path=" + encodeURIComponent(path));
    if (my !== dseq || !ui || open !== path) return;
    if (!diffFailed && JSON.stringify(r) === JSON.stringify(diff)) return; // the same text: kept as drawn
    diff = r;
    diffFailed = "";
  } catch (e) {
    if (my !== dseq || !ui || open !== path) return;
    const why = e && e.status === 404 ? "No longer changed in this session" : reason(e);
    if (!diff && why === diffFailed) return;
    diff = null;
    diffFailed = why;
  }
  render();
}

// follow: the session open in the main column; read again when it is
// another, or its diffstat or state moved
function follow() {
  const s = current();
  const next = s ? s.slug : null;
  if (next !== slug) {
    slug = next;
    files = null;
    overlaps = [];
    failed = "";
    open = null;
    diff = null;
    seq++;
    dseq++;
    sig = statSig(s);
    if (slug) readList();
    return;
  }
  if (slug && statSig(s) !== sig) {
    sig = statSig(s);
    readList();
  }
}

// ---- drawing ----

function statusMark(c) {
  const [role, word] = STATUS[c.status] || ["dim", "changed"];
  const m = colored(role, c.status, "view-changes-st");
  m.setAttribute("aria-label", word);
  m.title = word;
  return m;
}

function counts(c) {
  if (c.binary) return el("span", "view-changes-n", "binary");
  const n = el("span", "view-changes-n");
  if (c.added) n.append(colored("ok", "+" + c.added));
  if (c.added && c.deleted) n.append(txt(" "));
  if (c.deleted) n.append(colored("err", "−" + c.deleted));
  return n;
}

function peerBits(peers) {
  if (!peers || !peers.length) return null;
  const p = el("span", "view-changes-peers");
  const conflict = peers.some((x) => x.conflict);
  p.append(colored(conflict ? "err" : "warn", "⚠", "sym"), txt(" " + peers.map((x) => x.slug).join(", ")));
  p.title = peers.map((x) => x.slug + (x.conflict ? " conflicts" : " also changes it")).join(" · ");
  return p;
}

function fileRow(c) {
  const b = el("button", "srow view-changes-row");
  b.type = "button";
  const slash = c.path.lastIndexOf("/");
  const l1 = el("span", "sr-1");
  l1.append(statusMark(c), el("span", "sr-name view-changes-name", c.path.slice(slash + 1)), counts(c));
  b.append(l1);
  const dir = slash >= 0 ? c.path.slice(0, slash + 1) : "";
  const pb = peerBits(c.peers);
  if (dir || pb) {
    const l2 = el("span", "sr-2 view-changes-dir");
    if (dir) l2.append(el("span", "word", dir));
    if (pb) l2.append(pb);
    b.append(l2);
  }
  b.title = c.path;
  b.addEventListener("click", () => {
    open = c.path;
    diff = null;
    diffFailed = "";
    render();
    readDiff();
    if (ui.back.focus) ui.back.focus();
  });
  return b;
}

function sessionRow(s) {
  const b = el("button", "srow view-changes-row");
  b.type = "button";
  const l1 = el("span", "sr-1");
  l1.append(el("span", "sr-name", s.branch || s.slug));
  const d = diffBits(s.diffstat);
  if (d) l1.append(d);
  b.append(l1, el("span", "sr-2", s.repo + " · " + s.diffstat.files + " file" + (s.diffstat.files === 1 ? "" : "s")));
  b.addEventListener("click", () => ctx.go(s.slug));
  return b;
}

function renderNone(box) {
  const changed = st.sessions.filter((s) => s.diffstat && s.diffstat.files > 0 && s.state !== "landed");
  const e = el("div", "side-empty");
  e.append(el("p", "se-title", "No session open"), el("p", "se-what", "Open a session to see the files it changed."));
  box.append(e);
  if (changed.length) {
    box.append(el("div", "label sgroup-label", "SESSIONS WITH CHANGES"), ...changed.map(sessionRow));
  }
}

function renderList() {
  const box = ui.rows;
  // a row with the focus has it again after the rows are made anew
  const had = document.activeElement && box.contains && box.contains(document.activeElement) ? document.activeElement.title : null;
  box.replaceChildren();
  ui.count.textContent = files ? String(files.length) : "";
  const s = current();
  ui.who.replaceChildren();
  ui.who.hidden = !s;
  if (s) {
    ui.who.append(el("span", "sr-name", s.branch || s.slug));
    const d = diffBits(s.diffstat);
    if (d) ui.who.append(d);
  }
  if (!slug) {
    renderNone(box);
  } else if (files === null) {
    box.append(el("div", "empty", "Reading…"));
  } else {
    if (failed) box.append(el("div", "empty view-changes-err", "✗ changes unavailable · " + failed));
    const q = query.toLowerCase();
    const shown = files.filter((c) => !q || c.path.toLowerCase().includes(q));
    box.append(...shown.map(fileRow));
    if (!shown.length && !failed) box.append(el("div", "empty", files.length ? "Nothing matches" : "No changes yet"));
    if (had) {
      const row = [...box.childNodes].find((b) => b.title === had);
      if (row && row.focus) row.focus();
    }
  }
  const foot = ui.foot;
  foot.replaceChildren();
  for (const o of slug ? overlaps : []) {
    if (foot.childNodes.length) foot.append(el("span", "gap"));
    foot.append(colored(o.conflict ? "err" : "warn", "⚠", "sym"), txt(" " + o.slug + " " + (o.conflict ? "conflict" : "overlap")));
  }
  foot.hidden = !foot.childNodes.length;
}

function lineEl(l) {
  const row = el("div", "view-changes-line");
  const mark = l.kind === "+" ? colored("ok", "+", "view-changes-mark") : l.kind === "-" ? colored("err", "−", "view-changes-mark") : el("span", "view-changes-mark", " ");
  const t = el("span", "view-changes-text", l.text);
  row.append(mark, t);
  if (l.cut) {
    const c = el("span", "view-changes-cut", " …");
    c.title = "the rest of this line is not shown";
    row.append(c);
  }
  return row;
}

function renderDiff() {
  const c = (files || []).find((x) => x.path === open) || diff || { path: open, status: "" };
  ui.path.replaceChildren(statusMark(c), el("span", "view-changes-path", open), counts(diff || c));
  ui.path.title = open;
  const box = ui.diff;
  const top = box.scrollTop || 0;
  box.replaceChildren();
  if (diffFailed) {
    box.append(el("div", "empty view-changes-err", "✗ " + diffFailed));
  } else if (!diff) {
    box.append(el("div", "empty", "Reading…"));
  } else if (diff.binary) {
    box.append(el("div", "empty", "Binary file: no text to show"));
  } else if (!diff.hunks.length && diff.truncated) {
    box.append(el("div", "empty", "Too long to show here (" + diff.total + " lines) · read it in the TUI's review"));
  } else if (!diff.hunks.length) {
    box.append(el("div", "empty", "No text changes (mode only)"));
  } else {
    for (const h of diff.hunks) {
      const sec = el("div", "view-changes-hunk");
      sec.append(el("div", "view-changes-hh", h.header), ...h.lines.map(lineEl));
      box.append(sec);
    }
    if (diff.truncated) box.append(el("div", "view-changes-more", diff.lines + " of " + diff.total + " lines shown · the rest in the TUI's review"));
  }
  box.scrollTop = top;
}

function render() {
  if (!ui) return;
  drawn = shownSig();
  const inDiff = open !== null;
  // new text for the open diff while some of it is selected: drawn when
  // the selection is gone (update looks again)
  held = inDiff && selecting(ui.diff);
  if (held) return;
  ui.list.hidden = inDiff;
  ui.file.hidden = !inDiff;
  if (inDiff) renderDiff();
  else {
    ui.path.replaceChildren();
    ui.diff.replaceChildren();
    renderList();
  }
}

function back() {
  const was = open;
  open = null;
  diff = null;
  diffFailed = "";
  dseq++;
  render();
  // the row that was opened takes the focus again
  const row = [...ui.rows.childNodes].find((b) => b.title === was);
  if (row && row.focus) row.focus();
}

export default {
  id: "changes",
  title: "Changes",
  search: "Filter changed files by path",

  mount(box, c) {
    ctx = c;
    const root = el("div", "side-list view-changes");
    // the list
    const list = el("div", "view-changes-list");
    const head = el("div", "side-head");
    const count = el("span", "label-n");
    head.append(el("span", "label", "CHANGES"), count);
    const who = el("div", "view-changes-who");
    who.hidden = true;
    const rows = el("div", "view-changes-rows");
    const foot = el("div", "side-foot");
    foot.hidden = true;
    list.append(head, who, rows, foot);
    // one file's diff
    const file = el("div", "view-changes-file");
    file.hidden = true;
    const bar = el("div", "view-changes-bar");
    const backBtn = el("button", "btn sm", "← Files");
    backBtn.type = "button";
    backBtn.setAttribute("aria-label", "back to the changed files");
    backBtn.addEventListener("click", back);
    bar.append(backBtn, el("span", "view-changes-ro", "read only"));
    const path = el("div", "view-changes-head");
    const diffBox = el("div", "view-changes-diff");
    diffBox.setAttribute("tabindex", "0");
    diffBox.setAttribute("aria-label", "diff");
    file.append(bar, path, diffBox);
    root.append(list, file);
    box.append(root);
    ui = { list, count, who, rows, foot, file, back: backBtn, path, diff: diffBox };
    slug = null;
    sig = "";
    drawn = "";
    held = false;
    // nothing is read yet: the rail's update right after names the
    // session open now (not the one open the last time it was shown)
  },

  update(state) {
    st = state;
    if (!ui) return;
    const was = slug;
    follow();
    if (slug !== was || held || shownSig() !== drawn) render();
  },

  query(text) {
    query = text;
    if (open === null) render();
  },

  unmount() {
    ui = null;
    st = { sessions: [], current: null };
    seq++;
    dseq++;
    slug = null;
    files = null;
    open = null;
    diff = null;
  },
};
