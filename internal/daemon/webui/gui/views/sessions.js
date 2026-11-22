// The sidebar's Sessions view (docs/gui-design.md の「左サイドバー」):
// SESSIONS and the count, the chips (All / Needs you / Running), tapi's
// card, the repositories with two or more sessions open, the rest folded
// under OTHER REPOSITORIES, and the counts at the foot. The title bar's box
// filters it by name, branch or repository. A view of the rail
// (gui/rail.js; the contract is in docs/gui.md 「サイドバーのビュー」).

import { el, colored, txt, prLink, asButton } from "../../lib/dom.js";
import { ago } from "../../lib/fmt.js";
import { TAPI } from "../../lib/api.js";
import { diffBits } from "../dash.js";
import { readySet, needsYou, stateBits, tapiState } from "../states.js";

let ctx = null; // {go}
let ui = null; // the elements mount built
let st = { sessions: [], tapi: null, overview: null, current: null };
let filter = "all", query = "";

const CHIPS = [["all", "All"], ["needs", "Needs you"], ["running", "Running"]];

function shown(s, ready) {
  if (filter === "needs" && !needsYou(s, ready)) return false;
  if (filter === "running" && s.state !== "running") return false;
  if (!query) return true;
  const q = query.toLowerCase();
  return [s.slug, s.branch, s.repo].some((x) => String(x || "").toLowerCase().includes(q));
}

function sideRow(s, ready) {
  const sel = s.slug === st.current;
  // a div acting as a button: the PR's number in it is a link (asButton)
  const b = el("div", "srow" + (sel ? " sel" : "") + (s.state === "landed" ? " past" : ""));
  const l1 = el("span", "sr-1");
  l1.append(el("span", "sr-name", s.branch || s.slug), el("span", "sr-age", s.state === "idle" ? "" : ago(s.state_since)));
  const l2 = el("span", "sr-2");
  l2.append(...stateBits(s, ready, false));
  if (s.pr) {
    const pr = el("span", "sr-pr");
    if (s.pr_look) pr.append(colored(s.pr_look.role, s.pr_look.icon, "sym"), txt(" "));
    pr.append(prLink(s.pr_url, "#" + s.pr));
    l2.append(pr);
  }
  const d = diffBits(s.diffstat);
  if (d) l2.append(d);
  b.append(l1, l2);
  asButton(b, () => ctx.go(s.slug));
  if (sel) b.setAttribute("aria-current", "true");
  return b;
}

// oneRow: a repository with one session, folded to one line
function oneRow(s, ready) {
  const sel = s.slug === st.current;
  const b = el("button", "srow one" + (sel ? " sel" : "") + (s.state === "landed" ? " past" : ""));
  b.type = "button";
  b.append(el("span", "sr-repo", s.repo));
  if (s.branch && s.branch !== s.repo) b.append(el("span", "sr-branch", s.branch));
  const d = diffBits(s.diffstat);
  if (d) b.append(d);
  const sts = el("span", "sr-st");
  sts.append(...stateBits(s, ready, true));
  b.append(sts);
  b.addEventListener("click", () => ctx.go(s.slug));
  if (sel) b.setAttribute("aria-current", "true");
  return b;
}

function renderTapiCard() {
  const c = ui.tapi;
  const sel = st.current === TAPI;
  c.replaceChildren();
  c.className = "tapi-card" + (sel ? " sel" : "");
  const l1 = el("span", "sr-1");
  l1.append(el("span", "sr-name", "tapi"), el("span", "sr-role", "orchestrator"));
  const sts = el("span", "sr-st");
  sts.append(...tapiState(st.tapi));
  l1.append(sts);
  c.append(l1);
  if (st.tapi && st.tapi.last_message) c.append(el("span", "sr-last", st.tapi.last_message.split("\n")[0]));
  if (sel) c.setAttribute("aria-current", "true");
  else c.removeAttribute("aria-current");
}

function byRepo(list) {
  const groups = new Map();
  for (const s of list) {
    if (!groups.has(s.repo)) groups.set(s.repo, []);
    groups.get(s.repo).push(s);
  }
  return [...groups];
}

function render() {
  if (!ui) return;
  const { sessions } = st;
  const ready = readySet(st.overview);
  const box = ui.groups;
  box.replaceChildren();
  ui.count.textContent = String(sessions.length);
  ui.n.all.textContent = String(sessions.length);
  ui.n.needs.textContent = String(sessions.filter((s) => needsYou(s, ready)).length);
  ui.n.running.textContent = String(sessions.filter((s) => s.state === "running").length);
  for (const [k, b] of ui.chips) b.setAttribute("aria-pressed", String(k === filter));
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
  const foot = ui.foot;
  foot.replaceChildren();
  if (waiting) foot.append(colored("warn", "◉", "sym"), txt(" " + waiting + " waiting"));
  if (done) {
    if (waiting) foot.append(el("span", "gap"));
    foot.append(colored("secondary", "✓", "sym"), txt(" " + done + " to review"));
  }
  foot.hidden = !foot.childNodes.length;
}

export default {
  id: "sessions",
  title: "Sessions",
  search: "Filter sessions by name or repository",

  mount(box, c) {
    ctx = c;
    const list = el("div", "session-list");
    const head = el("div", "side-head");
    const count = el("span", "label-n");
    head.append(el("span", "label", "SESSIONS"), count);
    const chipBox = el("div", "chips");
    chipBox.setAttribute("role", "group");
    chipBox.setAttribute("aria-label", "show");
    const chips = [], n = {};
    for (const [k, word] of CHIPS) {
      const b = el("button", "chip", word + " ");
      b.type = "button";
      n[k] = el("span", "n");
      b.append(n[k]);
      b.addEventListener("click", () => {
        filter = k;
        render();
      });
      chips.push([k, b]);
      chipBox.append(b);
    }
    const rows = el("div", "session-rows");
    const tapi = el("button", "tapi-card");
    tapi.type = "button";
    tapi.addEventListener("click", () => ctx.go(TAPI));
    const groups = el("div");
    rows.append(tapi, groups);
    const foot = el("div", "side-foot");
    foot.hidden = true;
    list.append(head, chipBox, rows, foot);
    box.append(list);
    ui = { count, chips, n, tapi, groups, foot };
  },

  update(state) {
    st = state;
    render();
  },

  query(text) {
    query = text;
    render();
  },

  unmount() {
    ui = null;
  },
};
