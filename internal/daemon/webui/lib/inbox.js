// The inbox: what agents ask a person.
// pekoed's facts and the links first, the agent's words after (its
// opinion, not pekoe's). The recommendation is labelled as the agent's and
// never chosen for the person. Every string goes in with textContent; a
// link becomes an anchor only when it is http(s) (dom.js link).

import { el, txt, sep, colored, link } from "./dom.js";
import { ago, short, questionId, reportId } from "./fmt.js";

const KINDS = {
  decision: ["?", "secondary", "decision"],
  approve: ["!", "warn", "approve"],
  login: ["@", "primary", "login"],
  fyi: ["i", "dim", "fyi"],
};

function factRow(label, ...nodes) {
  const r = el("div", "fact");
  r.append(el("span", "fk", label), ...nodes);
  return r;
}

export function factsBlock(it) {
  const box = el("div", "facts");
  box.append(el("div", "sub", "pekoe read"));
  const f = it.facts;
  if (!f) {
    box.append(el("div", "fact r-dim", "(not about a session)"));
  } else if (f.gone) {
    box.append(factRow("session", colored("warn", f.slug + " is gone")));
  } else {
    const ses = [txt(f.slug), sep(), txt(f.state)];
    if (f.branch) ses.push(sep(), txt(f.branch + (f.base ? " → " + f.base : "")));
    box.append(factRow("session", ...ses));
    const d = f.diffstat || { added: 0, deleted: 0, files: 0 };
    box.append(factRow("HEAD", txt(short(f.head)), sep(), colored("ok", "+" + d.added), txt(" "), colored("err", "−" + d.deleted), txt(" · " + d.files + " files")));
    if (f.pr) {
      const n = [f.pr_url ? link(f.pr_url, "#" + f.pr) : txt("#" + f.pr)];
      const ps = f.pr_status;
      if (ps) {
        n.push(sep(), txt(ps.state || "?"), sep(), txt("CI "), colored(ps.fail ? "dim" : "ok", ps.pass + " pass"), txt(" "),
          colored(ps.fail ? "err" : "dim", ps.fail + " fail"), txt(" " + ps.pending + " pending"));
        if (ps.review_decision) n.push(sep(), txt(ps.review_decision));
        if (ps.merge_state) n.push(sep(), txt(ps.merge_state));
        if (ps.unresolved) n.push(sep(), colored("warn", ps.unresolved + " unresolved"));
      }
      box.append(factRow("PR", ...n));
    }
    if (f.pr_repo) box.append(factRow("fork", colored("warn", "PR in " + f.pr_repo), txt(" · pushed to " + f.push_repo + " (origin)")));
    const ts = f.test && f.test.status;
    if (ts && ts !== "none") box.append(factRow("test", colored(ts === "pass" ? "ok" : ts === "fail" ? "err" : ts === "error" || ts === "blocked" ? "warn" : "dim", ts)));
    for (const o of f.overlaps || []) {
      box.append(factRow("forecast", o.conflict ? colored("err", "conflicts") : txt("overlaps"), txt(" with " + o.slug)));
    }
    if (f.report) box.append(factRow("report", txt(reportId(f.report.id) + " " + f.report.status + " · " + ago(f.report.at) + " ago")));
    if (f.git_blocked) box.append(factRow("git", colored("warn", ".git changed: git stopped")));
    if (f.merge) {
      for (const c of f.merge.conds || []) box.append(factRow(c.name, colored(c.ok ? "ok" : "err", c.ok ? "✓" : "✗"), txt(" " + c.detail)));
    }
  }
  for (const l of it.links || []) box.append(factRow("link", link(l)));
  return box;
}

// draftOf: the person's pick for it (drafts: id → {rev, choice, note,
// changed}, kept by the page). When the item changed since it was drawn
// (its rev went up: HEAD moved, CI turned), the choice is dropped and the
// card says so, as the TUI's modal does.
export function draftOf(drafts, it) {
  let d = drafts.get(it.id);
  if (!d) {
    d = { rev: it.rev, choice: null, note: "", changed: false };
    drafts.set(it.id, d);
  } else if (d.rev !== it.rev) {
    d.changed = d.changed || d.choice !== null;
    d.choice = null;
    d.rev = it.rev;
  }
  return d;
}

// pekoedItem: pekoed's own item (merge / wait / send back): answered by
// choosing only, never by a note alone (pekoed refuses that too).
function pekoedItem(it) {
  return it.from === "pekoed" && it.kind === "approve";
}

// canAnswer: an option chosen, or a note written (with options and none
// chosen, the note alone is the answer: none of them; not pekoed's item).
export function canAnswer(it, d) {
  if (d.choice !== null) return true;
  if ((it.options || []).length && pekoedItem(it)) return false;
  return d.note.trim() !== "";
}

// inboxCard: one open item, with its options, a note and answer… (which
// opens the confirm sheet). on: {busy() (a sheet is open: answer… off),
// focus(), blur() (the note box), answer(it)}.
export function inboxCard(it, d, on) {
  const card = el("div", "card ibox");
  const [icon, role, word] = KINDS[it.kind] || ["?", "dim", it.kind];
  const head = el("div", "line1 ihead");
  head.append(colored(role, icon, "ic"), el("span", "name r-text", questionId(it.id)), colored(role, word, "word"), el("span", "age", ago(it.created_at)));
  const body = el("div", "body");
  body.append(factsBlock(it));
  if (d.changed) body.append(el("div", "also r-warn", "⚠ changed while you looked: check the facts again"));

  const says = el("div", "says");
  says.append(el("div", "sub", "the agent says (its words, not pekoe's)"));
  const from = el("div", "from");
  from.append(txt("from " + it.from));
  if (it.claimed) from.append(txt(" "), colored("warn", "⚠ claims to be " + it.claimed));
  says.append(from, el("div", "q", it.question));
  if (it.context) says.append(el("pre", "input ctx", it.context));
  body.append(says);

  const btn = el("button", "btn key", "answer…");
  btn.type = "button";
  const update = () => (btn.disabled = !canAnswer(it, d) || on.busy());
  const opts = el("div", "opts");
  opts.setAttribute("role", "radiogroup");
  (it.options || []).forEach((o, i) => {
    const b = el("button", "opt");
    b.type = "button";
    b.setAttribute("role", "radio");
    b.append(el("span", "num", String(i + 1)), el("span", "otext", o));
    if (it.recommend === i) b.append(el("span", "rec", "recommended (agent)"));
    b.addEventListener("click", () => {
      d.choice = d.choice === i ? null : i;
      d.changed = false;
      paint();
      update();
    });
    opts.append(b);
  });
  const paint = () => opts.querySelectorAll(".opt").forEach((b, i) => {
    b.classList.toggle("sel", d.choice === i);
    b.setAttribute("aria-checked", String(d.choice === i));
  });
  paint();
  if (opts.childNodes.length) body.append(opts);

  const ta = el("textarea", "note");
  ta.rows = 2;
  ta.maxLength = 4000;
  ta.placeholder = !(it.options || []).length ? "answer in words" : pekoedItem(it) ? "note (optional; send back needs one)" : "note (optional; or, with nothing chosen, the answer on its own: none of these)";
  ta.value = d.note;
  ta.addEventListener("input", () => {
    d.note = ta.value;
    update();
  });
  ta.addEventListener("focus", on.focus);
  ta.addEventListener("blur", on.blur);
  btn.addEventListener("click", () => on.answer(it));
  const row = el("div", "row");
  row.append(btn);
  body.append(ta, row);
  update();
  card.append(head, body);
  return card;
}

const MERGE_LOOK = { running: ["●", "warn", "merging"], merged: ["✓", "ok", "merged"], failed: ["✗", "err", "not merged"] };

// mergeRow: pekoed's merge after a "merge" answer (pekoed's words only).
export function mergeRow(it) {
  const r = it.answer.merge;
  const [icon, role, word] = MERGE_LOOK[r.state] || ["?", "dim", r.state];
  const row = el("div", "card");
  const head = el("div", "line1");
  head.append(colored(role, icon, "ic"), el("span", "name r-text", "PR #" + r.pr), colored(role, word, "word"), el("span", "age", ago(r.at)));
  const body = el("div", "body");
  body.append(el("div", "also", (it.about || "") + " · HEAD " + (r.head || "").slice(0, 7) + (r.method ? " · " + r.method : "") + " · inbox " + questionId(it.id)));
  if (r.detail) body.append(el("div", "also " + (r.state === "failed" ? "r-err" : "r-dim"), r.detail));
  row.append(head, body);
  return row;
}
