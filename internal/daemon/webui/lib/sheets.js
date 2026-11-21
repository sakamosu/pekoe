// The confirm sheets both pages share (their markup is in each page's
// index.html, with the same ids).
//
// answer: always through a confirmation that shows what is allowed.
// pending_tool is pekoe's one-line summary of the last tool the agent asked
// for (cut at 200 bytes); the sheet also shows the screen (the last 80
// lines, scrolled to the bottom, scrollable up), where the agent's own
// permission dialog says exactly what it will run.

import { $, toast } from "./dom.js";
import { api } from "./api.js";
import { cutNote, questionId } from "./fmt.js";
import { termLines, screenRows } from "./term.js";
import { thumbs } from "./images.js";
import { canAnswer } from "./inbox.js";

// ALWAYS_NOTE: what "always" does, in the sheet (the local GUI only)
const ALWAYS_NOTE = "always picks the agent's \"don't ask again\": later prompts like this one in this session are allowed without asking. Check what the screen says it covers.";

// answerSheet wires #sheet: open(s, answer) shows it; answer… sends the
// answer with the tool the person saw (pekoed refuses it with 409 if the
// agent has moved on). on: {close() after it went, done() after an
// answer was sent}.
export function answerSheet(on) {
  let pending = null;
  const close = () => {
    $("sheet").hidden = true;
    pending = null;
    on.close();
  };
  const open = (s, answer) => {
    pending = { slug: s.slug, answer, tool: s.pending_tool || null };
    const sheet = $("sheet").querySelector(".sheet");
    sheet.classList.toggle("danger", answer === "deny");
    sheet.classList.toggle("always", answer === "always");
    $("sheet-title").textContent = `${answer} · ${s.slug}`;
    $("sheet-tool").textContent = s.pending_tool ? s.pending_tool.name : "(no tool detail)";
    $("sheet-input").textContent = s.pending_tool ? s.pending_tool.input || "" : s.last_message || "";
    const cut = cutNote(s.pending_tool);
    const warn = [cut ? "⚠ " + cut : "", answer === "always" ? "⚠ " + ALWAYS_NOTE : ""].filter(Boolean).join("\n");
    $("sheet-warn").hidden = !warn;
    $("sheet-warn").textContent = warn;
    sheet.classList.toggle("long", !!cut);
    const ok = $("sheet-ok");
    ok.textContent = answer;
    ok.className = "btn " + answer;
    ok.disabled = false;
    const scr = $("sheet-screen");
    scr.textContent = "…";
    $("sheet").hidden = false;
    api("/api/sessions/" + encodeURIComponent(s.slug) + "/screen").then((r) => {
      if (!pending || pending.slug !== s.slug) return;
      // every line the screen call returns, not just the bottom: a command
      // padded with blank lines still has its start in reach by scrolling
      scr.replaceChildren(termLines(screenRows(r)));
      scr.scrollTop = scr.scrollHeight;
    }).catch((e) => { scr.textContent = "(" + e.message + ")"; });
  };
  const confirm = async () => {
    if (!pending) return;
    const p = pending;
    $("sheet-ok").disabled = true;
    try {
      await api("/api/sessions/" + encodeURIComponent(p.slug) + "/answer", { answer: p.answer, tool: p.tool });
      toast(`${p.answer}: ${p.slug}`, true);
    } catch (e) {
      toast(e.message);
    }
    close();
    on.done();
  };
  $("sheet-cancel").addEventListener("click", close);
  $("sheet-ok").addEventListener("click", confirm);
  $("sheet").addEventListener("click", (e) => { if (e.target === $("sheet")) close(); });
  return { open, close, isOpen: () => !!pending };
}

// inboxSheet wires #isheet: what will be sent, to whom, before it is.
// drafts is the page's id → draft map (inbox.js draftOf). on: {close(),
// done()}.
export function inboxSheet(drafts, on) {
  let ianswer = null;
  const close = () => {
    $("isheet").hidden = true;
    ianswer = null;
    on.close();
  };
  const open = (it) => {
    const d = drafts.get(it.id);
    if (!d || !canAnswer(it, d)) return;
    const note = d.note.trim();
    ianswer = { id: it.id, rev: d.rev, choice: d.choice, note };
    $("isheet-title").textContent = "answer inbox " + questionId(it.id);
    $("isheet-to").textContent = it.from + (it.about && it.about !== it.from ? " · about " + it.about : "");
    $("isheet-q").textContent = it.question;
    const a = [];
    if (d.choice !== null) a.push(d.choice + 1 + ". " + it.options[d.choice] + (it.recommend === d.choice ? "  (same as the agent recommended)" : ""));
    else if ((it.options || []).length) a.push("none of the options (the note alone)");
    if (note) a.push("note: " + note);
    $("isheet-a").textContent = a.join("\n");
    $("isheet-warn").hidden = false;
    let warn = "as of rev " + d.rev + ": if the facts change before this reaches pekoe, it is refused and shown again";
    if (it.from === "pekoed" && it.kind === "approve" && d.choice === 0 && it.options[0] === "merge") {
      warn = "pekoed checks the conditions again and runs gh pr merge on PR #" + ((it.facts && it.facts.pr) || "?") + " at the HEAD it asked about. " + warn;
    }
    $("isheet-warn").textContent = warn;
    $("isheet-ok").disabled = false;
    $("isheet").hidden = false;
  };
  const confirm = async () => {
    const a = ianswer;
    if (!a) return;
    $("isheet-ok").disabled = true;
    const body = { rev: a.rev, note: a.note };
    if (a.choice !== null) body.choice = a.choice;
    try {
      await api("/api/inbox/" + encodeURIComponent(String(a.id)) + "/answer", body);
      drafts.delete(a.id);
      toast("answered " + questionId(a.id), true);
    } catch (e) {
      const d = drafts.get(a.id);
      if (e.status === 409 && d) {
        d.choice = null;
        d.changed = true;
        toast(questionId(a.id) + " changed or was closed: look again");
      } else {
        toast(questionId(a.id) + ": " + e.message);
      }
    }
    close();
    on.done();
  };
  $("isheet-cancel").addEventListener("click", close);
  $("isheet-ok").addEventListener("click", confirm);
  $("isheet").addEventListener("click", (e) => { if (e.target === $("isheet")) close(); });
  return { open, close, isOpen: () => !!ianswer };
}

// showSendSheet fills #tsheet for a send: to tapi (the whole text, every
// time unless the page sends to it without one) or to a session when
// images go along. The text and the images are fixed when it opens; the
// page binds send / cancel. repo: the session's repository, if known.
export function showSendSheet(isTapi, slug, repo, text, images) {
  $("tsheet-title").textContent = isTapi ? "send to tapi" : "send to " + slug;
  $("tsheet-to-cap").textContent = isTapi ? "to — the orchestrator, not one session" : "to — the session";
  $("tsheet-to").textContent = isTapi ? "tapi (orchestrator)" : slug + (repo ? " · " + repo : "");
  $("tsheet-warn").hidden = !isTapi;
  $("tsheet-cap").textContent = text ? "text — all of it, as it will be typed (" + [...text].length + " chars, " + text.split("\n").length + " lines)" : "text — none (the images alone)";
  $("tsheet-text").textContent = text;
  $("tsheet-text").hidden = !text;
  $("tsheet-text").scrollTop = 0;
  $("tsheet-imgs-cap").textContent = images.length + " image" + (images.length > 1 ? "s" : "") + " — pekoed saves them outside the worktree and adds a line with each one's path, for the agent to open";
  $("tsheet-imgs-cap").hidden = !images.length;
  thumbs($("tsheet-imgs"), images, null);
  $("tsheet-ok").textContent = isTapi ? "send to tapi" : "send";
  $("tsheet-ok").disabled = false;
  $("tsheet").hidden = false;
}
