// The send box's "/ Commands" and "@ Files" (docs/gui.md 「段階 2 の一部:
// セッションの吹き出しと送信欄の / @」): what is being typed at the caret,
// the list filtered, and the pick put into the text. They only write into
// the box; nothing is sent, and a send goes through the same checks as one
// typed by hand (the ! sheet included). Pure functions: gui.js holds the
// DOM, the tests run these in node.
//
// The names come from the repository and the user's .claude (commands,
// skills, file names): untrusted. The page draws them with textContent;
// a name with a control character never goes into the box (pekoed leaves
// such file names out already, the send would refuse them).

export const PICK_MAX = 50;

// controls: what pekoed's send refuses (C0, DEL, C1) and the line breaks
// that are no newline to a textarea but break a line on screen
const CONTROLS = /[\u0000-\u001f\u007f-\u009f\u2028\u2029]/;

export const insertable = (s) => typeof s === "string" && s !== "" && !CONTROLS.test(s);

// tokenAt is the "/command" or "@path" the caret is in, or null:
//   - cmd: a "/" at the start of the text (after blanks): Claude Code
//     reads a slash command only there
//   - file: an "@" at the start or after a blank, up to the caret
// start..end is what a pick replaces (the rest of the word after the
// caret too); q is what was typed after the sign, up to the caret.
export function tokenAt(text, caret) {
  text = String(text || "");
  caret = Math.max(0, Math.min(caret == null ? text.length : caret, text.length));
  const before = text.slice(0, caret);
  const tail = text.slice(caret).match(/^\S*/)[0].length;
  let m = before.match(/^(\s*)\/(\S*)$/);
  if (m) return { kind: "cmd", q: m[2], start: m[1].length, end: caret + tail };
  m = before.match(/(^|\s)@("?)([^\s"]*)$/);
  if (m) return { kind: "file", q: m[3], start: before.length - m[2].length - m[3].length - 1, end: caret + tail };
  return null;
}

// commandText / mention: what a pick writes. A path with a blank is
// quoted (@"a b.md"): Claude Code ends an @ mention at the first blank
// (要確認: the quoted form, docs/gui.md).
export const commandText = (name) => "/" + name;
export function mention(path) {
  return /\s/.test(path) ? '@"' + path + '"' : "@" + path;
}

// insertPick puts value in place of tok and one blank after it (none
// added when one follows already): the new text and where the caret goes.
// null when value may not go into the box.
export function insertPick(text, tok, value) {
  text = String(text || "");
  if (!tok || !insertable(value)) return null;
  const rest = text.slice(tok.end);
  const gap = /^\s/.test(rest) ? "" : " ";
  const out = text.slice(0, tok.start) + value + gap + rest;
  return { text: out, caret: tok.start + value.length + gap.length };
}

// filterCommands: the commands whose name starts with q first (in the
// order pekoed gave: built in, the worktree's, the user's), then those
// whose name holds it, then those whose description does; at most
// PICK_MAX, control characters left out.
export function filterCommands(list, q) {
  q = String(q || "").toLowerCase();
  const ok = (list || []).filter((c) => c && insertable(c.name));
  if (!q) return ok.slice(0, PICK_MAX);
  const by = [[], [], []];
  for (const c of ok) {
    const n = c.name.toLowerCase();
    if (n.startsWith(q)) by[0].push(c);
    else if (n.includes(q)) by[1].push(c);
    else if (String(c.description || "").toLowerCase().includes(q)) by[2].push(c);
  }
  return by.flat().slice(0, PICK_MAX);
}

// pickFiles: pekoed's ranked paths, the ones that may go into the box.
export const pickFiles = (files) => (files || []).filter(insertable).slice(0, PICK_MAX);

// ---- a chat's pages back (chatpane.js) ----

// prependPage puts an older page in front of the items a chat holds and
// moves the keys of what is unfolded ("m<i>", "t<i>": item indices) along.
export function prependPage(items, older, open) {
  const n = older.length;
  const moved = new Set();
  for (const k of open) {
    const m = /^([a-z])(\d+)$/.exec(k);
    if (m) moved.add(m[1] + (Number(m[2]) + n));
  }
  return { items: older.concat(items), open: moved };
}
