// The send box's pick list: "/" lists the session's slash commands, "@"
// the files of its worktree (compose.js has the rules). Typing "/" at the
// start or "@" after a blank opens it, as the buttons do; ↑ ↓ move, ↵ or
// Tab puts the pick into the box, esc closes it. Nothing is sent: the
// person reads the text and sends it as any other (the ! sheet included).
// Every name is drawn with textContent (they are the repository's).

import { el } from "../lib/dom.js";
import { api, apiBase } from "../lib/api.js";
import { tokenAt, insertPick, filterCommands, pickFiles, commandText, mention } from "./compose.js";

const CMDS_EVERY = 30000, FILES_WAIT = 120;

// picker: ta the send box, box the list's element; slug() the session
// the box sends to ("" when it is no session: no list).
export function picker({ ta, box, slug }) {
  const p = { tok: null, list: [], sel: 0, note: "", dismissed: "", seq: 0 };
  const cmds = new Map(); // slug -> {at, supported, list}
  let wait = 0;

  const isOpen = () => !box.hidden;

  function close() {
    clearTimeout(wait);
    p.seq++;
    p.tok = null;
    p.list = [];
    box.hidden = true;
    box.replaceChildren();
    ta.removeAttribute("aria-activedescendant");
    ta.setAttribute("aria-expanded", "false");
  }

  function draw() {
    box.replaceChildren();
    const kind = p.tok && p.tok.kind;
    const cap = el("div", "pk-cap");
    cap.append(el("span", "k", kind === "cmd" ? "/" : "@"), el("span", null, kind === "cmd" ? " Commands" : " Files"), el("span", "dim pk-keys", "↑↓ move · ↵ insert · esc close"));
    box.append(cap);
    p.list.forEach((it, i) => {
      const row = el("button", "pk-row" + (i === p.sel ? " sel" : ""));
      row.type = "button";
      row.id = "pk-" + i;
      row.setAttribute("role", "option");
      row.setAttribute("aria-selected", String(i === p.sel));
      row.tabIndex = -1;
      if (kind === "cmd") {
        row.append(el("span", "pk-name", "/" + it.name), el("span", "pk-desc", it.description || ""), el("span", "pk-src", it.source || ""));
        row.title = "/" + it.name + (it.description ? " — " + it.description : "");
      } else {
        const slash = it.lastIndexOf("/");
        row.append(el("span", "pk-name", slash < 0 ? it : it.slice(slash + 1)), el("span", "pk-desc", slash < 0 ? "" : it.slice(0, slash)));
        row.title = it;
      }
      // keep the focus in the box: a click picks, the caret stays
      row.addEventListener("mousedown", (e) => e.preventDefault());
      row.addEventListener("click", () => choose(i));
      box.append(row);
    });
    if (!p.list.length) box.append(el("div", "pk-empty", p.note || (kind === "cmd" ? "No command matches" : "No file matches")));
    else if (p.note) box.append(el("div", "pk-note", p.note));
    box.hidden = false;
    ta.setAttribute("aria-expanded", "true");
    if (p.list.length) {
      ta.setAttribute("aria-activedescendant", "pk-" + p.sel);
      const r = box.querySelector(".pk-row.sel");
      if (r && r.scrollIntoView) r.scrollIntoView({ block: "nearest" });
    } else ta.removeAttribute("aria-activedescendant");
  }

  async function commandsOf(s) {
    const had = cmds.get(s);
    if (had && Date.now() - had.at < CMDS_EVERY) return had;
    const r = await api(apiBase(s) + "/commands");
    const v = { at: Date.now(), supported: !!r.supported, list: Array.isArray(r.commands) ? r.commands : [] };
    cmds.set(s, v);
    return v;
  }

  // update: after each keystroke and caret move
  async function update() {
    const s = slug();
    const tok = s ? tokenAt(ta.value, ta.selectionStart) : null;
    const key = tok ? tok.kind + ":" + tok.start : "";
    if (!tok || key === p.dismissed) {
      if (!tok) p.dismissed = "";
      if (isOpen()) close();
      return;
    }
    p.dismissed = "";
    const same = p.tok && p.tok.kind === tok.kind && p.tok.q === tok.q;
    p.tok = tok;
    if (same && isOpen()) return;
    const seq = ++p.seq;
    if (tok.kind === "cmd") {
      try {
        const c = await commandsOf(s);
        if (seq !== p.seq) return;
        p.list = c.supported ? filterCommands(c.list, tok.q) : [];
        p.note = c.supported ? "" : "Commands are listed for Claude Code agents only";
      } catch (e) {
        if (seq !== p.seq) return;
        p.list = [];
        p.note = "Commands unavailable: " + e.message;
      }
      p.sel = 0;
      draw();
      return;
    }
    clearTimeout(wait);
    wait = setTimeout(async () => {
      try {
        const r = await api(apiBase(s) + "/files?q=" + encodeURIComponent(tok.q));
        if (seq !== p.seq) return;
        p.list = pickFiles(r.files);
        const more = [];
        if (r.matches > p.list.length) more.push(p.list.length + " of " + r.matches + " matches: type more of the name");
        if (r.truncated) more.push("the worktree has more files than pekoe lists");
        p.note = more.join(" · ");
      } catch (e) {
        if (seq !== p.seq) return;
        p.list = [];
        p.note = "Files unavailable: " + e.message;
      }
      p.sel = 0;
      draw();
    }, FILES_WAIT);
  }

  function choose(i) {
    const it = p.list[i];
    if (!it || !p.tok) return;
    const value = p.tok.kind === "cmd" ? commandText(it.name) : mention(it);
    const out = insertPick(ta.value, p.tok, value);
    close();
    if (!out) return;
    ta.value = out.text;
    ta.focus();
    ta.setSelectionRange(out.caret, out.caret);
    ta.dispatchEvent(new Event("input", { bubbles: true }));
  }

  // key: the box's keydown while the list is open; true when it took it
  function key(e) {
    if (!isOpen() || e.isComposing) return false;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      if (!p.list.length) return false;
      e.preventDefault();
      p.sel = (p.sel + (e.key === "ArrowDown" ? 1 : p.list.length - 1)) % p.list.length;
      draw();
      return true;
    }
    if ((e.key === "Enter" && !e.metaKey && !e.ctrlKey && !e.shiftKey) || (e.key === "Tab" && !e.shiftKey)) {
      if (!p.list.length) return false;
      e.preventDefault();
      choose(p.sel);
      return true;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      p.dismissed = p.tok ? p.tok.kind + ":" + p.tok.start : "";
      close();
      return true;
    }
    return false;
  }

  // start: the buttons. "/" goes at the start of the text (Claude Code
  // reads a command only there), "@" at the caret, after a blank.
  function start(kind) {
    if (!slug()) return;
    const v = ta.value;
    p.dismissed = "";
    if (kind === "cmd") {
      const lead = v.match(/^\s*/)[0].length;
      if (v[lead] !== "/") {
        const rest = v.slice(lead);
        ta.value = v.slice(0, lead) + "/" + (rest && !/^\s/.test(rest) ? " " : "") + rest;
      }
      ta.focus();
      ta.setSelectionRange(lead + 1, lead + 1);
    } else {
      const at = ta.selectionStart == null ? v.length : ta.selectionStart;
      const pre = at > 0 && !/\s/.test(v[at - 1]) ? " " : "";
      ta.value = v.slice(0, at) + pre + "@" + v.slice(at);
      ta.focus();
      const c = at + pre.length + 1;
      ta.setSelectionRange(c, c);
    }
    ta.dispatchEvent(new Event("input", { bubbles: true }));
  }

  ta.setAttribute("aria-controls", box.id);
  ta.setAttribute("aria-expanded", "false");
  ta.addEventListener("input", update);
  ta.addEventListener("click", update);
  ta.addEventListener("keyup", (e) => {
    if (e.key === "ArrowLeft" || e.key === "ArrowRight" || e.key === "Home" || e.key === "End") update();
  });
  ta.addEventListener("blur", () => setTimeout(() => document.activeElement !== ta && close(), 150));

  return { key, start, close, isOpen, forget: (s) => cmds.delete(s) };
}
