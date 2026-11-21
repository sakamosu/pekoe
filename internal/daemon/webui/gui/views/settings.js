// The sidebar's Settings view (docs/gui.md 「Settings」, at the rail's
// foot): the settings pekoed uses (GET /api/config = config.show, the same
// as `pekoe config show`: defaults filled in, secrets hidden), section by
// section, each key with what it does, whether config.toml has it, and
// whether a change to it waits for pekoe daemon restart. Reading only:
// config.toml is changed with `pekoe config edit` in a terminal. The title
// bar's box filters by key, section, value or description. A view of the
// rail (gui/rail.js; the contract is in docs/gui.md 「サイドバーのビュー」).

import { el, colored, txt } from "../../lib/dom.js";
import { api } from "../../lib/api.js";

const EVERY = 5000; // read again at most this often (update comes every 3 s)
const MAX_PROBLEMS = 5;

let ui = null; // {box}
let data = null, err = "", query = "";
let readAt = 0, reading = false, gen = 0;

// fmtValue: a setting as config.toml would spell it
export function fmtValue(v) {
  if (v === null || v === undefined) return "(unset)";
  if (typeof v === "string") return v === "(hidden)" ? v : JSON.stringify(v);
  if (Array.isArray(v)) return "[" + v.map((x) => JSON.stringify(x)).join(", ") + "]";
  if (typeof v === "object") {
    const ks = Object.keys(v).sort();
    return ks.length ? "{ " + ks.map((k) => k + " = " + JSON.stringify(v[k])).join(", ") + " }" : "{}";
  }
  return String(v);
}

// full: a key's dotted name ("web.hostname"; a bare key is its section)
const full = (s, k) => (s.name === k.key ? k.key : s.name + "." + k.key);

// waits: config.toml changed it, and it keeps its running value until
// pekoed restarts. waiting names the keys ("web.allow_devices"); the
// restart list's "web" names the section, not which of its keys changed
const waits = (waiting, s, k) => k.restart && waiting.includes(full(s, k));

// matches: the box's text in the key, section, value or doc; "restart"
// also finds the keys whose change waits for a restart
function matches(s, k, q) {
  return [s.name, full(s, k), k.doc, fmtValue(k.value), k.restart ? "restart" : ""].some((x) => String(x || "").toLowerCase().includes(q));
}

function keyRow(s, k, waiting) {
  const r = el("div", "view-settings-key" + (k.set ? " set" : ""));
  const l1 = el("div", "view-settings-k1");
  l1.append(el("span", "view-settings-name", k.key));
  if (waits(waiting, s, k)) {
    const w = el("span", "view-settings-tag");
    w.append(colored("warn", "!", "sym"), txt(" waits for restart"));
    w.title = "config.toml changed this; pekoed uses the value it started with until pekoe daemon restart";
    l1.append(w);
  } else if (k.restart) {
    const w = el("span", "view-settings-tag", "restart");
    w.title = "A change takes effect after pekoe daemon restart";
    l1.append(w);
  }
  if (k.set) {
    const t = el("span", "view-settings-tag view-settings-in", "set");
    t.title = "config.toml has this key; the others are at their default";
    l1.append(t);
  }
  const v = el("div", "view-settings-value", fmtValue(k.value));
  v.title = full(s, k) + " = " + fmtValue(k.value);
  r.append(l1, v);
  if (k.doc) r.append(el("div", "view-settings-doc", k.doc));
  return r;
}

function problemsBox(ps) {
  const b = el("div", "view-settings-problems");
  const fatal = ps.find((p) => p.fatal);
  if (fatal) {
    const p = el("p", "view-settings-note");
    p.append(colored("err", "✗", "sym"), txt(" config.toml does not read · pekoed keeps the settings below"));
    b.append(p, el("p", "view-settings-msg", fatal.message));
    return b;
  }
  for (const x of ps.slice(0, MAX_PROBLEMS)) {
    const p = el("p", "view-settings-note");
    p.append(colored("warn", "!", "sym"), txt(" " + (x.key ? x.key + ": " : "") + x.message));
    b.append(p);
  }
  if (ps.length > MAX_PROBLEMS) b.append(el("p", "view-settings-msg", "+" + (ps.length - MAX_PROBLEMS) + " more · pekoe config check"));
  return b;
}

// render fills the rows; the list and its head stay (mount), so the
// scroll position survives a read
function render() {
  if (!ui) return;
  const out = [];
  if (!data) {
    const p = el("p", "view-settings-note");
    if (err) p.append(colored("err", "✗", "sym"), txt(" settings unavailable · " + err));
    else p.append(txt("Loading…"));
    out.push(p);
    ui.rows.replaceChildren(...out);
    return;
  }
  const where = el("div", "view-settings-where");
  const path = el("p", "view-settings-path", data.path + (data.exists ? "" : " (none yet: every setting at its default)"));
  path.title = data.path;
  const how = el("p", "view-settings-msg");
  how.append(txt("Change them with "), el("span", "view-settings-cmd", "pekoe config edit"), txt(" in a terminal"));
  where.append(path, how);
  out.push(where);
  if (err) { // the last read again failed: what shows is from before
    const p = el("p", "view-settings-note");
    p.append(colored("err", "✗", "sym"), txt(" not read again · " + err + " · showing the last read"));
    out.push(p);
  }
  const restart = data.restart || [];
  if (restart.length) {
    const p = el("p", "view-settings-note");
    p.append(colored("warn", "!", "sym"), txt(" " + restart.join(", ") + " changed · applies after pekoe daemon restart"));
    out.push(p);
  }
  if ((data.problems || []).length) out.push(problemsBox(data.problems));
  const waiting = data.waiting || [];

  const q = query.toLowerCase();
  let shown = 0;
  for (const s of data.sections || []) {
    const keys = (s.keys || []).filter((k) => !q || matches(s, k, q));
    if (!keys.length) continue;
    shown += keys.length;
    const sec = el("section", "view-settings-sec");
    sec.setAttribute("aria-label", s.name);
    const h = el("div", "view-settings-head");
    h.append(el("span", "view-settings-sname", s.name === "animate" ? "animate" : "[" + s.name + "]")); // as config.toml spells it
    const set = s.keys.filter((k) => k.set).length;
    if (set) h.append(el("span", "label-n", set + " set"));
    sec.append(h);
    if (s.doc) sec.append(el("p", "view-settings-sdoc", s.doc));
    for (const k of keys) sec.append(keyRow(s, k, waiting));
    out.push(sec);
  }
  if (q && !shown) out.push(el("div", "empty", "Nothing matches “" + query + "”"));

  const tab = el("section", "view-settings-sec");
  tab.setAttribute("aria-label", "this tab");
  const th = el("div", "view-settings-head");
  th.append(el("span", "label", "THIS TAB"));
  tab.append(th, el("p", "view-settings-sdoc", "The theme (the title bar's button), Chat or Screen and this sidebar's view are kept for this tab only, not in config.toml."));
  out.push(tab);
  ui.rows.replaceChildren(...out);
}

async function read() {
  if (reading || !ui) return;
  reading = true;
  readAt = Date.now();
  const my = gen;
  try {
    const d = await api("/api/config");
    if (my !== gen) return;
    data = d;
    err = "";
  } catch (e) {
    if (my !== gen) return;
    err = e.message || String(e);
  } finally {
    if (my === gen) reading = false;
  }
  render();
}

export default {
  id: "settings",
  title: "Settings",
  search: "Filter settings by key, value or description",
  mount(box) {
    const list = el("div", "side-list view-settings");
    const head = el("div", "side-head");
    head.append(el("span", "label", "SETTINGS"), el("span", "label-n", "read only"));
    ui = { rows: el("div", "view-settings-rows") };
    list.append(head, ui.rows);
    box.append(list);
    gen++;
    reading = false;
    data = null; // read afresh each time the view shows
    err = "";
    render();
    read();
  },
  update() {
    if (ui && Date.now() - readAt >= EVERY) read();
  },
  query(text) {
    query = text || "";
    render();
  },
  unmount() {
    ui = null;
    gen++; // a read on its way is dropped
    reading = false;
    query = "";
  },
};
