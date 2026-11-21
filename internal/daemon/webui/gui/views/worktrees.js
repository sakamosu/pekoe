// The sidebar's Worktrees view (docs/gui.md 「サイドバーのビュー」の
// Worktrees): the TUI's [2] Worktrees for every repository pekoe knows.
// Each repository is a group (its name and how many worktrees), the main
// checkout first; a row is the worktree's branch (its directory's name when
// the HEAD is detached), main / outside ~/worktrees, the port, and the
// state of the session in it with the same symbol, color and word as the
// Sessions view (states.js). A row with a session opens it; a worktree
// without one only says so (opening one there is the TUI's ↵ or
// `pekoe open`): this view reads and opens, it removes nothing. The title
// bar's box filters by branch, directory, session or repository.
//
// GET /api/worktrees (the GUI's mux only) runs git worktree list for each
// repository, so the view reads it on its first update after mount and
// then at most every minGap ms on the page's refresh (sooner when the
// sessions change: one made or gone), and never while it is hidden.

import { el, colored } from "../../lib/dom.js";
import { api } from "../../lib/api.js";
import { diffBits } from "../dash.js";
import { readySet, stateBits } from "../states.js";

export const minGap = 5000;

let ctx = null; // {go}
let ui = null; // the elements mount built
let st = { sessions: [], tapi: null, overview: null, current: null };
let query = "";
let repos = null; // GET /api/worktrees's repos, null before the first answer
let err = ""; // the last read's error
let gen = 0; // bumped on unmount: an answer for an older mount is dropped
let busy = false, lastAt = 0, lastKey = "";
const now = () => Date.now();

// base: the last element of a path
const base = (p) => String(p || "").replace(/\/+$/, "").split("/").pop() || String(p || "");

// sessionsKey: what of the session list changes the worktrees (one made,
// one gone)
const sessionsKey = (ss) => (ss || []).map((s) => s.slug).sort().join("\n");

async function load() {
  if (busy) return;
  busy = true;
  const g = gen;
  lastAt = now();
  lastKey = sessionsKey(st.sessions);
  try {
    const j = await api("/api/worktrees");
    if (g !== gen) return;
    repos = Array.isArray(j && j.repos) ? j.repos : [];
    err = "";
  } catch (e) {
    if (g !== gen) return;
    err = String((e && e.message) || e);
  } finally {
    if (g === gen) busy = false;
  }
  render();
  // the sessions changed while it read: once more
  if (sessionsKey(st.sessions) !== lastKey) load();
}

function shown(repo, w, s) {
  if (!query) return true;
  const q = query.toLowerCase();
  return [w.branch, base(w.path), w.slug, repo.name, s && s.branch].some((x) => String(x || "").toLowerCase().includes(q));
}

function row(w, s, ready) {
  const name = w.branch || base(w.path);
  const has = !!w.slug;
  const sel = has && w.slug === st.current;
  const r = el(has ? "button" : "div", "srow view-worktrees-row" + (sel ? " sel" : "") + (has ? "" : " view-worktrees-bare"));
  r.title = w.path;
  const l1 = el("span", "sr-1");
  l1.append(el("span", "sr-name", name));
  if (!w.branch) l1.append(el("span", "view-worktrees-tag", "detached"));
  if (w.main) l1.append(el("span", "view-worktrees-tag", "main"));
  else if (!w.managed) l1.append(el("span", "view-worktrees-tag", "outside ~/worktrees"));
  if (w.port) l1.append(el("span", "sr-age", ":" + w.port));
  const l2 = el("span", "sr-2");
  if (s) {
    l2.append(...stateBits(s, ready, true));
    const d = diffBits(s.diffstat);
    if (d) l2.append(d);
  } else if (has) {
    // a session /api/sessions does not list (yet): its slug and state as
    // worktree.list has them
    l2.append(colored("dim", "◦", "sym"), el("span", "word", (w.state || "session") + " · " + w.slug));
  } else {
    l2.append(colored("dim", "○", "sym"), el("span", "word", "No session"));
  }
  r.append(l1, l2);
  if (has) {
    r.type = "button";
    r.addEventListener("click", () => ctx.go(w.slug));
    if (sel) r.setAttribute("aria-current", "true");
  }
  return r;
}

function render() {
  if (!ui) return;
  const box = ui.groups;
  box.replaceChildren();
  ui.note.hidden = !err;
  if (err) ui.note.replaceChildren(colored("err", "✗", "sym"), el("span", "word", " Could not read the worktrees · " + err));
  if (!repos) {
    ui.count.textContent = "";
    if (!err) box.append(el("div", "empty", "Reading worktrees…"));
    return;
  }
  const ready = readySet(st.overview);
  const bySlug = new Map((st.sessions || []).map((s) => [s.slug, s]));
  let total = 0, withS = 0;
  for (const repo of repos) {
    const ws = repo.worktrees || [];
    total += ws.length;
    withS += ws.filter((w) => w.slug).length;
    const list = ws.filter((w) => shown(repo, w, bySlug.get(w.slug)));
    if (!list.length) continue;
    const g = el("div", "sgroup");
    const h = el("div", "sgroup-head");
    const nm = el("span", "sg-name", repo.name || base(repo.dir));
    nm.title = repo.dir;
    h.append(nm, el("span", "sg-n", String(ws.length)));
    g.append(h, ...list.map((w) => row(w, bySlug.get(w.slug), ready)));
    box.append(g);
  }
  ui.count.textContent = String(total);
  if (!box.childNodes.length) box.append(el("div", "empty", total ? "Nothing matches" : repos.length ? "No worktrees" : "No repositories · pekoe new in one"));
  const foot = ui.foot;
  foot.replaceChildren();
  if (total) foot.append(el("span", "", withS + " with a session · " + (total - withS) + " without"));
  foot.hidden = !total;
}

export default {
  id: "worktrees",
  title: "Worktrees",
  search: "Filter worktrees by branch or repository",

  mount(box, c) {
    ctx = c;
    const list = el("div", "side-list view-worktrees");
    const head = el("div", "side-head");
    const count = el("span", "label-n");
    head.append(el("span", "label", "WORKTREES"), count);
    const note = el("div", "view-worktrees-note");
    note.hidden = true;
    const groups = el("div", "view-worktrees-rows");
    const foot = el("div", "side-foot");
    foot.hidden = true;
    list.append(head, note, groups, foot);
    box.append(list);
    ui = { count, note, groups, foot };
    lastAt = 0;
    render(); // the rows of the last time it was shown, if any, until the read
  },

  update(state) {
    st = state;
    render();
    if (now() - lastAt >= minGap || sessionsKey(st.sessions) !== lastKey) load();
  },

  query(text) {
    query = text;
    render();
  },

  unmount() {
    ui = null;
    gen++;
    busy = false;
  },
};
