// How a session reads on both pages: the TUI's line (icon, name, state,
// age) and its dim details, pekoe's ids, times and sizes.

import { el, colored, sep } from "./dom.js";

// pekoe's own numbers carry a letter for their kind (internal/ids), so they
// are not taken for a GitHub PR or issue (#185): r651 a request, q12 an
// inbox question, p468 a report.
export const requestId = (id) => "r" + id;
export const questionId = (id) => "q" + id;
export const reportId = (id) => "p" + id;

export const short = (sha) => (sha ? String(sha).slice(0, 9) : "-");

// ago is theme.Ago: hours up to two days
export function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return Math.floor(s) + "s";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 48 * 3600) return Math.floor(s / 3600) + "h";
  return Math.floor(s / 86400) + "d";
}

export const pad2 = (n) => String(n).padStart(2, "0");
const DAYS = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];

export function hm(iso) {
  const d = new Date(iso);
  return isNaN(d) ? "" : pad2(d.getHours()) + ":" + pad2(d.getMinutes());
}
export function dayKey(iso) {
  const d = new Date(iso);
  return isNaN(d) ? "" : d.getFullYear() + "-" + d.getMonth() + "-" + d.getDate();
}
export function dayLabel(iso) {
  const d = new Date(iso);
  return d.getMonth() + 1 + "/" + d.getDate() + " " + DAYS[d.getDay()];
}
export function dur(ms) {
  const s = Math.round(ms / 1000);
  if (s < 60) return s + "s";
  if (s < 3600) return Math.floor(s / 60) + "m" + pad2(s % 60) + "s";
  return Math.floor(s / 3600) + "h" + pad2(Math.floor(s / 60) % 60) + "m";
}

export const kb = (n) => (n >= 1024 * 1024 ? (n / 1024 / 1024).toFixed(1) + " MB" : Math.max(1, Math.round(n / 1024)) + " KB");

// look is the TUI's (theme.Look, sent by pekoed): symbol, role, word
export function look(s) {
  return s.look || { icon: "○", role: "dim", word: s.state };
}

// details: the dim second line, with the colored bits the TUI colors
export function details(s, withRepo) {
  const out = [];
  const add = (...nodes) => {
    if (out.length) out.push(sep());
    out.push(...nodes);
  };
  if (withRepo) add(document.createTextNode(s.repo));
  if (s.slug !== s.branch) add(document.createTextNode(s.slug));
  const d = s.diffstat;
  if (d && d.added + d.deleted > 0) add(colored("ok", "+" + d.added), document.createTextNode(" "), colored("err", "-" + d.deleted));
  if (s.test === "pass") add(colored("ok", "✓"), document.createTextNode("test"));
  else if (s.test === "fail") add(colored("err", "✗"), document.createTextNode("test"));
  else if (s.test === "running") add(document.createTextNode("…test"));
  else if (s.test === "blocked") add(colored("warn", "!"), document.createTextNode("test not run: no sandbox"));
  else if (s.test === "error") add(colored("warn", "!"), document.createTextNode("test could not run"));
  if (s.git_blocked) add(colored("warn", "!"), document.createTextNode(".git changed: git stopped"));
  if (s.state === "queued" && s.queue) add(document.createTextNode(s.queue));
  if (s.pr) {
    const n = [document.createTextNode("#" + s.pr)];
    if (s.pr_look) n.push(document.createTextNode(" "), colored(s.pr_look.role, s.pr_look.icon));
    add(...n);
  }
  if (s.error) add(colored("err", "err"), document.createTextNode(" " + s.error.split("\n")[0]));
  else if (s.state === "running" && s.last_tool) add(colored("ok", "●"), document.createTextNode(" " + s.last_tool));
  return out;
}

// line1 is the TUI's sessionLine: icon  name  state  age (no age when idle)
export function line1(s) {
  const lk = look(s);
  const dimName = s.state === "idle" || s.state === "creating" || s.state === "queued" || s.state === "landed";
  const l1 = el("div", "line1");
  l1.append(
    colored(lk.role, lk.icon, "ic"),
    el("span", "name " + (dimName ? "r-dim" : "r-text"), s.branch || s.slug),
    colored(lk.role, lk.word, "word"),
    el("span", "age", s.state === "idle" ? "" : ago(s.state_since)),
  );
  return l1;
}

// cutNote: when the summary leaves part of the command out (cut short, or
// several lines folded into one), say how much, so a long or padded
// command is not judged by its first line.
export function cutNote(t) {
  if (!t || (!t.cut && !(t.lines > 1))) return "";
  const parts = [];
  if (t.lines > 1) parts.push(t.lines + " lines");
  if (t.size) parts.push(t.size >= 1024 ? (t.size / 1024).toFixed(1) + " KB" : t.size + " bytes");
  return (t.cut ? "summary cut" : "multi-line") + " (" + parts.join(", ") + "): read the whole command on the screen";
}

// questionUp: a question's menu (AskUserQuestion) is up: the keypad
// drives it. pekoed refuses keys in any other state (a permission menu
// stays allow / deny).
export function questionUp(s) {
  return !!s && s.state === "waiting" && s.wait_kind === "question" && !s.stopped;
}
