// A chat pane: an agent's transcript as bubbles (lib/chat.js), for tapi's
// panel on the Dashboard and a session's (or tapi's) Chat in the detail
// view. pekoed reads the transcript (transcript.go: words, tool names and
// one argument, turn times; never tool results or thinking) and hands it
// out after a cursor (?after=) or a page back (?before=). The pane keeps
// up to CHAT_KEEP items and draws the last `show` of them; "Show earlier"
// draws more of what it keeps, then reads the page before (Load earlier),
// as far as the start of the transcript (決まったこと 7).

import { el, toast } from "../lib/dom.js";
import { api } from "../lib/api.js";
import { markRewound, chatView } from "../lib/chat.js";
import { prependPage } from "./compose.js";

export const CHAT_KEEP = 3000, CHAT_PAGE = 120;

const sameWords = (s) => String(s || "").replace(/\s+/g, " ").trim();

export function selectingIn(node) {
  const sel = document.getSelection();
  if (!sel || sel.isCollapsed || !sel.rangeCount) return false;
  return node.contains(sel.anchorNode) || node.contains(sel.focusNode);
}

// chatPane: log is the scroller (role=log) holding head (the earlier
// line), body (the bubbles) and, after them, whatever the page puts under
// the last bubble; jump the "↓ Latest" button. url(key) is the transcript's
// path for the agent key names (a slug, or tapi); name is who the agent's
// bubbles say; shown() whether the pane is on screen now (no reads, no
// drawing while it is not).
export function chatPane({ log, head, body, jump, url, name, shown }) {
  const c = {
    key: "", cursor: "", items: [], earlier: false, before: "", ok: null, why: "", code: 0,
    loading: false, back: false, sig: "", held: false, open: new Set(), pending: [], show: CHAT_PAGE,
  };
  let gen = 0; // a read for another agent (or before a reset) is dropped

  const atBottom = () => log.scrollHeight - log.scrollTop - log.clientHeight < 40;
  function toBottom() {
    log.scrollTop = log.scrollHeight;
    jump.hidden = true;
  }

  // reset: another agent (key), or the same one forgotten (tapi's Reset)
  function reset(key) {
    gen++;
    Object.assign(c, {
      key, cursor: "", items: [], earlier: false, before: "", ok: null, why: "", code: 0,
      loading: false, back: false, sig: "", held: false, pending: [], show: CHAT_PAGE,
    });
    c.open = new Set();
    head.replaceChildren();
    body.replaceChildren();
  }

  function earlierLine() {
    head.replaceChildren();
    const hidden = Math.max(0, c.items.length - c.show);
    const button = (label, onClick) => {
      const b = el("button", "btn xs earlier", label);
      b.type = "button";
      b.disabled = c.back;
      b.addEventListener("click", onClick);
      head.append(b);
    };
    if (hidden) {
      button("Show earlier (" + Math.min(hidden, CHAT_PAGE) + " more of " + hidden + ")", () => {
        c.show += CHAT_PAGE;
        render(true);
      });
    } else if (c.before) {
      button(c.back ? "Loading earlier…" : "Load earlier", loadEarlier);
    } else if (c.earlier) {
      head.append(el("div", "divider", "Older messages were let go here to keep the page light: reopen the view to read back"));
    } else if (c.ok && c.items.length) {
      head.append(el("div", "divider", "start of the conversation"));
    }
  }

  function render(force) {
    if (!shown()) return;
    const sig = [c.key, c.items.length, c.pending.length, [...c.open].join(","), c.earlier, c.before, c.back, c.ok, c.show].join("|");
    if (!force && sig === c.sig) return;
    if (selectingIn(log)) {
      c.held = true;
      return;
    }
    c.held = false;
    const stick = atBottom();
    const fromBottom = log.scrollHeight - log.scrollTop;
    c.sig = sig;
    earlierLine();
    body.replaceChildren();
    if (c.ok === false) body.append(el("div", "empty", "Chat unavailable: " + c.why));
    else if (!c.items.length && !c.pending.length) body.append(el("div", "empty", c.ok === true ? "No messages yet" : "Loading…"));
    body.append(chatView(c.items.slice(-c.show), c.pending, name(), c.open, () => render(true)));
    if (stick) toBottom();
    else log.scrollTop = log.scrollHeight - fromBottom; // keep the place when earlier ones come in above
  }

  // refresh reads what is new; only while the pane is shown, unless
  // always (the detail view reads on while the screen stands in for it)
  async function refresh(always) {
    if ((!shown() && always !== true) || c.loading || !c.key) return;
    c.loading = true;
    const g = gen;
    try {
      const r = await api(url(c.key) + "?after=" + encodeURIComponent(c.cursor));
      if (g !== gen) return;
      if (r.reset) {
        if (r.new_conversation && c.items.length) {
          // /clear: the old conversation stays above the line as it is;
          // a page back would be of the old one, whose cursor pekoed no
          // longer takes
          c.items.push({ kind: "conv" });
          if (c.before) c.earlier = true;
          c.before = "";
        } else {
          c.items = [];
          c.open.clear();
          c.show = CHAT_PAGE;
          c.earlier = !!r.earlier;
          c.before = typeof r.before === "string" ? r.before : "";
        }
      }
      for (const it of r.items || []) {
        if (it && typeof it === "object" && typeof it.kind === "string") c.items.push(it);
      }
      trim();
      markRewound(c.items);
      c.cursor = String(r.cursor || "");
      const seen = new Set(c.items.filter((it) => it.kind === "human").slice(-20).map((it) => sameWords(it.text)));
      const old = Date.now() - 5 * 60 * 1000;
      c.pending = c.pending.filter((p) => !seen.has(sameWords(p.text)) && p.at > old);
      c.ok = true;
      c.why = "";
      c.code = 0;
    } catch (e) {
      if (g !== gen) return;
      c.ok = false;
      c.why = e.message;
      c.code = e.status || 0;
    } finally {
      if (g === gen) c.loading = false;
    }
    render(false);
  }

  // trim: past CHAT_KEEP the oldest go; what is before them cannot be read
  // back from here any more (the cursor was where they started)
  function trim() {
    if (c.items.length <= CHAT_KEEP) return;
    c.items = c.items.slice(-CHAT_KEEP);
    c.open.clear();
    c.earlier = true;
    c.before = "";
  }

  async function loadEarlier() {
    if (!c.before || c.back || !shown()) return;
    c.back = true;
    render(true);
    const g = gen;
    try {
      const r = await api(url(c.key) + "?before=" + encodeURIComponent(c.before));
      if (g !== gen) return;
      const older = (r.items || []).filter((it) => it && typeof it === "object" && typeof it.kind === "string");
      const p = prependPage(c.items, older, c.open);
      c.items = p.items;
      c.open = p.open;
      c.show += older.length;
      c.before = typeof r.before === "string" ? r.before : "";
      c.earlier = !!r.earlier;
      if (c.items.length > CHAT_KEEP) {
        // keep the newest; the page back just read stays partly
        c.items = c.items.slice(-CHAT_KEEP);
        c.open.clear();
        c.before = "";
        c.earlier = true;
      }
      markRewound(c.items);
    } catch (e) {
      if (g !== gen) return;
      if (e.status === 409) {
        // the conversation moved (/clear, a shorter transcript): read it
        // again from its end
        const key = c.key;
        reset(key);
        c.back = false;
        refresh();
        return;
      }
      toast("Load earlier: " + e.message);
    } finally {
      if (g === gen) c.back = false;
    }
    render(true);
  }

  // addPending: the person's words sent, ● until they show up (✓)
  function addPending(text) {
    c.pending.push({ text, at: Date.now() });
    render(true);
    toBottom();
    setTimeout(refresh, 600);
  }

  log.addEventListener("scroll", () => (jump.hidden = atBottom()), { passive: true });
  jump.addEventListener("click", () => toBottom());

  return { c, reset, refresh, render, toBottom, atBottom, addPending, loadEarlier };
}
