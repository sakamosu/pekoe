// The Session panel's agent facts: model and effort, the permission mode,
// the context's tokens, and "Update installed" (docs/gui.md 「段階 2 の一部:
// Session の右パネル」). pekoed reads them from the hooks and the
// transcript (GET /api/sessions/{slug}/panel = session.panel): the agent's
// own word, shown as text and never used to decide anything.
//
// agentRows / updateNotice turn the answer into rows (data: tested with
// node); nodes() draws a row's parts; panelFacts() keeps one answer per
// session and reads it again when it is older than FACTS_EVERY.

import { colored, txt } from "../lib/dom.js";
import { api, apiBase } from "../lib/api.js";

export const FACTS_EVERY = 5000;

// the hooks' permission_mode, as Claude Code's mode menu words it
// ("default" is its Manual). auto and bypassPermissions are warn: the
// agent acts without asking (docs/gui-design.md: warn is 確認待ち、auto mode)
const MODES = {
  default: ["manual", ""],
  plan: ["plan", ""],
  acceptEdits: ["accept edits", ""],
  auto: ["auto mode on", "warn"],
  dontAsk: ["don't ask", ""],
  bypassPermissions: ["bypass permissions", "warn"],
};

export function modeLook(mode) {
  const m = MODES[mode];
  return m ? { word: m[0], role: m[1] } : null;
}

// a part is { text } (plain) or { text, role, sym } (a theme role)
const plain = (text) => ({ text: String(text) });

// agentRows: the AGENT section's rows, [{ k, parts }]. An agent that is
// not Claude Code gets the one row saying so.
export function agentRows(p) {
  if (!p || "error" in p) return [];
  if (!p.supported) return [{ k: "", parts: [{ text: "◦", role: "dim", sym: true }, plain(" " + (p.why || "not Claude Code"))] }];
  const rows = [];
  if (p.model) rows.push({ k: "model", parts: [plain(p.model_name || p.model)], title: p.model });
  if (p.effort) rows.push({ k: "effort", parts: [plain(p.effort + (p.effort_from === "start" ? " · as started" : ""))] });
  const m = modeLook(p.mode);
  if (m) rows.push({ k: "mode", parts: [m.role ? { text: m.word, role: m.role } : plain(m.word)] });
  if (p.tokens > 0) rows.push({ k: "context", parts: [plain((p.tokens_short || String(p.tokens)) + " tokens")], title: p.tokens.toLocaleString("en-US") + " tokens in the last request" });
  return rows;
}

// updateNotice: the NOTICES row when the agent runs an older Claude Code
// than the one installed (pekoed compares them), else null
export function updateNotice(p) {
  if (!p || !p.supported || !p.update_installed || !p.running_version || !p.installed_version) return null;
  return { k: "", parts: [{ text: "✓", role: "secondary", sym: true }, plain(" Update installed · " + p.running_version + " → " + p.installed_version + " · restart the agent to apply")] };
}

export function nodes(parts) {
  return parts.map((x) => (x.role ? colored(x.role, x.text, x.sym ? "sym" : undefined) : txt(x.text)));
}

// panelFacts: the answers by slug. want(slug) reads when there is none or
// it is older than FACTS_EVERY (one read at a time per slug); changed()
// runs when an answer comes in. get(slug) is the last answer, or
// { error } when the read failed.
export function panelFacts(changed) {
  const by = new Map();
  const busy = new Set();
  return {
    get: (slug) => by.get(slug)?.data,
    want(slug) {
      if (!slug || busy.has(slug)) return;
      const e = by.get(slug);
      if (e && Date.now() - e.at < FACTS_EVERY) return;
      busy.add(slug);
      api(apiBase(slug) + "/panel")
        .then((data) => by.set(slug, { data, at: Date.now() }))
        .catch((err) => by.set(slug, { data: { error: err.status === 404 ? "" : err.message || "unavailable" }, at: Date.now() }))
        .finally(() => {
          busy.delete(slug);
          changed(slug);
        });
    },
  };
}
