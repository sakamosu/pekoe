// The confirm sheet (#csheet) that New session, tapi's Reset and Interrupt
// go through (actions.js has their words; docs/gui.md 「操作」).
//
// While OK's request runs, the sheet stays as it is: Cancel / Back, Esc and
// a click on the backdrop do nothing (the request is already on its way to
// pekoed, so going back would only look like it was called off), and when
// the request ends it closes this sheet only (never one opened after it).

import { $, el } from "../lib/dom.js";

// confirmSheet: open / close / run for the page. setBusy: the page's "a
// sheet is open" flag; toast: an error's words; refresh: called once a run
// is over.
export function confirmSheet({ setBusy, toast, refresh }) {
  let c = null; // {run, back, running}: what OK does, what Cancel goes back to

  // open shows w (actions.js's words). run() does it; it may return a
  // function to call once the sheet is closed. back: Cancel goes back there
  // (the New session form) instead of closing.
  function open(w, run, back) {
    c = { run, back, running: false };
    setBusy(true);
    $("csheet").querySelector(".sheet").classList.toggle("danger", !!w.danger);
    $("csheet-title").textContent = w.title;
    $("csheet-to-cap").textContent = w.toCap;
    $("csheet-to").textContent = w.to;
    const box = $("csheet-facts");
    box.replaceChildren();
    for (const f of w.facts) box.append(el("div", "sheet-cap", f.cap), el(f.pre ? "pre" : "div", f.pre ? "input whole" : "sheet-fact", f.text));
    $("csheet-warn").textContent = w.warn;
    $("csheet-warn").hidden = !w.warn;
    const cancel = $("csheet-cancel");
    cancel.textContent = back ? "Back" : "Cancel";
    cancel.disabled = false;
    const ok = $("csheet-ok");
    ok.textContent = w.ok;
    ok.className = "btn" + (w.danger ? "" : " primary"); // a destructive one is never the main button
    ok.disabled = false;
    $("csheet").hidden = false;
    (w.focusOk ? ok : cancel).focus();
  }

  // close: Cancel / Back, Esc, the backdrop; nothing while OK's request runs
  function close() {
    const was = c;
    if (was && was.running) return;
    $("csheet").hidden = true;
    c = null;
    setBusy(false);
    if (was && was.back) was.back();
  }

  async function run() {
    const me = c;
    if (!me || me.running) return;
    me.running = true;
    $("csheet-ok").disabled = true;
    $("csheet-cancel").disabled = true;
    let after = null;
    try {
      after = await me.run();
    } catch (e) {
      toast(e.message);
    }
    if (c === me) {
      $("csheet").hidden = true;
      $("csheet-cancel").disabled = false;
      c = null;
      setBusy(false);
    }
    if (typeof after === "function") after();
    refresh();
  }

  return { open, close, run };
}
