// pekoed's /api/, for both pages.
//
// The local GUI (docs/gui.md): `pekoe gui` opens the page on
// http://127.0.0.1:<port>/#t=<one-time token>. The page trades the token
// for a session token (POST /api/gui/session) and sends it as
// Authorization: Bearer on every API request. It lives in sessionStorage
// (per origin, this tab only): never a cookie (it would ride along to an
// agent's dev server on another port of the same host) and never
// localStorage. On the phone (tailnet) there is no token and nothing here
// runs.

import { $ } from "./dom.js";

const GUI_KEY = "pekoe.gui.token";
let guiToken = null;
let guiOut = false; // 401: signed out; no more requests until `pekoe gui`
try {
  guiToken = sessionStorage.getItem(GUI_KEY);
} catch (e) {
  guiToken = null;
}

function guiSignedOut(text) {
  guiOut = true;
  guiToken = null;
  try {
    sessionStorage.removeItem(GUI_KEY);
  } catch (e) { /* storage off: nothing kept */ }
  const b = $("gui-auth");
  b.textContent = text;
  b.hidden = false;
}

// guiSignIn: the fragment's one-time token, if any, for a session token.
export async function guiSignIn() {
  const m = location.hash.match(/^#t=([0-9a-f]{64})$/);
  if (!m) return;
  // off the address bar and this history entry before anything else
  history.replaceState(null, "", location.pathname + location.search);
  const r = await fetch("/api/gui/session", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token: m[1] }),
  });
  if (!r.ok) {
    guiSignedOut(((await r.text()).trim() || r.status + " " + r.statusText));
    throw new Error("gui sign-in failed");
  }
  const j = await r.json();
  guiToken = j.token;
  try {
    sessionStorage.setItem(GUI_KEY, guiToken);
  } catch (e) { /* this tab keeps it in memory only */ }
}

// api GETs path, or POSTs body as JSON when given. An error carries the
// HTTP status (e.status) and pekoed's words.
export async function api(path, body) {
  if (guiOut) throw new Error("signed out: run pekoe gui again");
  const opt = { headers: {} };
  if (guiToken) opt.headers["Authorization"] = "Bearer " + guiToken;
  if (body !== undefined) {
    opt.method = "POST";
    opt.headers["Content-Type"] = "application/json";
    opt.headers["X-Pekoe-Web"] = "1";
    opt.body = JSON.stringify(body);
  }
  const r = await fetch(path, opt);
  if (r.status === 401) {
    // only the local GUI answers 401 (the tailnet's front door says 403)
    guiSignedOut("Signed out of the local GUI (pekoed restarted, the session token expired or was revoked, or this is a new tab). Run `pekoe gui` in a terminal to open it again.");
  }
  if (!r.ok) {
    const t = (await r.text()).trim();
    const e = new Error(t || r.status + " " + r.statusText);
    e.status = r.status;
    throw e;
  }
  return r.json();
}

// TAPI is tapi chat in a page's routes (#/tapi). No session slug starts
// with "+", so it never names a session.
export const TAPI = "+tapi";

// apiBase: where a target's screen, send and keys go
export function apiBase(slug) {
  return slug === TAPI ? "/api/tapi" : "/api/sessions/" + encodeURIComponent(slug);
}
