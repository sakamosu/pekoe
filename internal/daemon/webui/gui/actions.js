// What the local GUI's three operations say and when they are offered
// (docs/gui.md 段階 2): New session (the TUI's n), tapi's Reset (T) and
// Interrupt (the TUI's esc while typing into an agent). Each goes through
// the confirm sheet (gui.js); pekoed checks again (guiact.go,
// interrupt.go). Plain data in, plain data out, so node can test it
// (TestGUIActions); the page draws the words with textContent.

import { shellBang } from "./shell.js";

// interruptWhy: "" when an Interrupt may go to t (a session of
// /api/sessions, or /api/tapi with isTapi), else why not. As pekoed's
// interruptCheck: only while the agent works (pekoed also looks for the
// turn's spinner on the screen, which the page does not have: a 409 then). In a permission prompt
// Escape would be the agent's "No" (deny it instead), in a question's menu
// it backs out (the keypad has esc), in an idle box it clears the input.
export function interruptWhy(t, isTapi) {
  if (!t) return isTapi ? "tapi is not running" : "the session is gone";
  if (isTapi && (!t.started || !t.alive)) return "tapi is not running";
  if (t.stopped) return "the agent is stopped";
  if (t.state === "waiting" && t.wait_kind !== "question") return "it waits for a permission: deny it instead";
  if (t.state === "waiting") return "it asks a question: use the keypad's esc";
  if (t.state !== "running") return "it is not working: nothing to interrupt";
  return "";
}

// interruptSheet: the sheet's words for an Interrupt of name.
export function interruptSheet(name, isTapi) {
  return {
    title: "Interrupt " + name,
    toCap: "to — the agent's window",
    to: isTapi ? "tapi (orchestrator)" : name,
    facts: [
      { cap: "what pekoed does", text: "sends Escape once, the key Claude Code stops its turn on (the TUI's esc while typing into the agent). Nothing else is typed." },
      { cap: "what stays", text: "what the agent did so far: files it changed stay changed, commands it started may have run. It then waits for your next instruction." },
    ],
    warn: "",
    ok: "Interrupt",
  };
}

// resetSheet: the sheet's words for tapi's Reset (pekoe tapi reset). t is
// /api/tapi (null when unreadable).
export function resetSheet(t) {
  const facts = [
    { cap: "what goes", text: "tapi's window closes and its conversation is forgotten (Claude Code's record of it is deleted). This cannot be undone." },
    {
      cap: "what stays",
      text: "the requests (ledger" + (t ? ", " + t.ledger_open + " open" : "") + "), the inbox (" + (t ? t.inbox_open + " open, " : "") +
        "answered or not), the reports, the send log and every session. tapi reads the open requests again when it starts.",
    },
    { cap: "after", text: "tapi is not started: start it again from the TUI (t), with a new conversation." },
  ];
  return {
    title: "Reset tapi chat",
    toCap: "to — the orchestrator",
    to: "tapi" + (t && t.started ? (t.state === "running" ? " · working now: its turn stops" : "") : " · not started"),
    facts,
    warn: t && t.state === "running" ? "⚠ tapi is working: the reset stops it in the middle of its turn." : "",
    ok: "Reset tapi",
    danger: true,
  };
}

// newWhy: "" when the form can go to the confirm sheet, else what to fix.
// pekoed checks the branch name itself (git check-ref-format).
export function newWhy(repo, branch) {
  if (!repo) return "pick a repository";
  const b = String(branch || "").trim();
  if (!b) return "a branch is required";
  if (/\s/.test(b)) return "a branch has no spaces";
  return "";
}

// newSheet: the sheet's words for a new session. repo: {name, dir}.
export function newSheet(repo, branch, prompt) {
  const p = String(prompt || "");
  const facts = [
    { cap: "branch — new, in a worktree of its own", text: String(branch).trim() },
    { cap: "agent", text: "the repository's agent (its config's agent, else claude), as the TUI's n makes it" },
    p.trim()
      ? { cap: "prompt — all of it, as the agent gets it (" + [...p].length + " chars, " + p.split("\n").length + " lines)", text: p, pre: true }
      : { cap: "prompt", text: "none: the agent starts with an empty input box" },
  ];
  return {
    title: "New session in " + repo.name,
    toCap: "where — the repository",
    to: repo.name + " · " + repo.dir,
    facts,
    warn: shellBang(p)
      ? "⚠ The prompt starts with !: an agent that takes its prompt by typing it into its input box (opencode) runs the rest as a shell command, right away and without a permission prompt. Claude Code gets it as its first message."
      : "",
    ok: "Create session",
  };
}
