// A session's and tapi's state as symbol, color and word (docs/gui-design.md
// 決まったこと 3), shared by the page (the session's header) and the
// sidebar's views (views/sessions.js): the same look wherever it shows.

import { el, colored } from "../lib/dom.js";
import { ago, look } from "../lib/fmt.js";
import { cap } from "./dash.js";

// flows: the Overview's rows, each with its group (/api/overview)
export const flows = (overview) => (overview ? (overview.groups || []).flatMap((g) => (g.rows || []).map((f) => ({ f, g }))) : []);

// readySet: the slugs whose PR is ready to merge (the person's turn)
export const readySet = (overview) => new Set(flows(overview).filter(({ f }) => f.merge && f.merge.ready && f.state !== "landed").map(({ f }) => f.slug));

export const needsYou = (s, ready) => s.state === "waiting" || ready.has(s.slug);

// stateBits: symbol (its color), word (text; secondary only for the
// person's turn, "Ready to merge"). On the open row (raised) gui.css
// turns every word to text (決まったこと 12).
export function stateBits(s, ready, withAge) {
  const out = [];
  if (ready.has(s.slug)) {
    out.push(colored("secondary", "◉", "sym"), el("span", "word r-secondary", "Ready to merge"));
  } else {
    const lk = look(s);
    let word = cap(lk.word);
    if (s.state === "waiting") word += s.answerable && s.pending_tool ? " · " + s.pending_tool.name : s.wait_kind === "question" ? " · question" : "";
    out.push(colored(lk.role, lk.icon, "sym"), el("span", "word" + (s.state === "waiting" ? " r-text" : ""), word));
  }
  if (withAge && s.state !== "idle") out.push(el("span", "word", " · " + ago(s.state_since)));
  return out;
}

// tapiState: tapi's symbol, word and age (tapi: /api/tapi, or null)
export function tapiState(tapi) {
  if (!tapi) return [colored("dim", "○", "sym"), el("span", "word", " Not readable")];
  if (!tapi.started) return [colored("dim", "○", "sym"), el("span", "word", " Not started")];
  if (!tapi.alive) return [colored("dim", "○", "sym"), el("span", "word", " Exited")];
  const lk = tapi.look || { icon: "○", role: "dim", word: tapi.state };
  return [colored(lk.role, lk.icon, "sym"), el("span", "word", " " + cap(lk.word) + (tapi.state === "idle" ? "" : " · " + ago(tapi.state_since)))];
}
