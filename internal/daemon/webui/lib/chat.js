// A chat of an agent's transcript as bubbles (tapi chat; a session's
// transcript can use it later). pekoed reads the Claude Code transcript:
// the person's words, the agent's words, the tools it called (name and one
// main argument) and how long each turn took; never tool results or
// thinking. The agent's words are untrusted: they go into the DOM with
// textContent, through the markdown of md.js (no links, no HTML).

import { el, txt, colored } from "./dom.js";
import { hm, dayKey, dayLabel, dur } from "./fmt.js";
import { mdLines } from "./md.js";

export const CHAT_FOLD = 15; // lines shown before "more"
export const SPIN = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏";

// mdBody: the agent's words; past CHAT_FOLD lines folded behind "more".
// open: the keys of what is unfolded ("m<i>"), kept by the page.
function mdBody(text, key, open, onToggle) {
  const box = el("div", "md");
  const lines = String(text).split("\n");
  const long = lines.length > CHAT_FOLD;
  const isOpen = open.has(key);
  box.append(mdLines(long && !isOpen ? lines.slice(0, CHAT_FOLD) : lines));
  if (long) {
    const b = el("button", "more");
    b.type = "button";
    if (isOpen) b.append(el("span", "r-secondary", "less ‹"));
    else b.append(el("span", "r-secondary", "more ›"), txt("  +" + (lines.length - CHAT_FOLD) + " lines"));
    b.addEventListener("click", () => {
      if (open.has(key)) open.delete(key);
      else open.add(key);
      onToggle();
    });
    box.append(b);
  }
  return box;
}

export function bubbleLine(body, meta) {
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

// markRewound marks the words a person took back (rewound: true): a prompt
// interrupted (ctrl+c, esc) before the first word of the reply leaves the
// conversation and Claude Code's screen, and the prompt sent after it is
// written as another child of the same parent. The chat leaves them out
// as the screen does. Only lines typed at the prompt have a parent; one
// taken in the middle of a turn (typed while the agent worked) never is
// (docs/web.md 「取り消された発言」).
export function markRewound(items) {
  const last = new Map(); // parent -> the newest item typed after it
  for (const it of items) {
    if (it.kind !== "human" && it.kind !== "pekoe") continue;
    if (!it.parent || !it.id) continue;
    const prev = last.get(it.parent);
    if (prev && prev.id !== it.id) prev.rewound = true;
    last.set(it.parent, it);
  }
}

// comesBack: whether what was sent comes back from the transcript as the
// person's words, the bubble a "not sent yet" one (●) waits for. A ! shell
// command and a slash command (/compact, /<skill> args) do not: Claude Code
// writes them as its own records (<bash-input>, <command-name>), which the
// chat leaves out, and the ● would stay its five minutes and go without a
// word. The send's own "delivered" says they went. A path (/Users/…) is
// words as any other.
export function comesBack(text) {
  return !/^\s*(?:!|\/[A-Za-z][\w:.-]*(?:\s|$))/.test(String(text || ""));
}

// chatBlocks groups items into what is drawn: day lines, the person's
// turns, the agent's turns (tool lines and bubbles), pekoe's lines.
export function chatBlocks(items, pending) {
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
    if (it.rewound) return; // taken back (markRewound): not on the agent's screen either
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

function toolsLine(list, key, open, onToggle) {
  const frag = document.createDocumentFragment();
  const isOpen = open.has(key);
  const b = el("button", "tools");
  b.type = "button";
  b.setAttribute("aria-expanded", String(isOpen));
  const words = list.map((t) => (t.name + " " + (t.input || "")).trim()).join(" · ");
  b.append(el("span", "ic", isOpen ? "⌄" : "›"), txt(list.length + (list.length === 1 ? " tool · " : " tools · ") + words));
  b.addEventListener("click", () => {
    if (open.has(key)) open.delete(key);
    else open.add(key);
    onToggle();
  });
  frag.append(b);
  if (isOpen) {
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
// name. open: what is unfolded (a Set the page keeps); onToggle redraws
// after a fold opens or closes.
export function chatView(items, pending, name, open, onToggle) {
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
          turn.append(toolsLine(p.list, "t" + p.i, open, onToggle));
          return;
        }
        const body = mdBody(p.text, "m" + p.i, open, onToggle);
        const meta = j === lastText ? metaOf(bl.ms ? dur(bl.ms) : "", "", p.at) : null;
        turn.append(bubbleLine(body, meta));
      });
      frag.append(turn);
    }
  }
  return frag;
}
