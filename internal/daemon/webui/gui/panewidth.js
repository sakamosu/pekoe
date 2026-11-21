// The widths of the panes beside the main column (docs/gui.md 「サイドバー
// のビュー」): the sidebar on the left (its grip on
// its right edge, gui/index.html #side-grip) and the dashboard's tapi
// panel on the right (its grip on its left edge, #tapi-grip). A grip is a
// role="separator": dragging it moves the edge, a double click gives back
// the default, and the arrow keys (Home / End) move it from the keyboard.
// The width is the one style this page sets from a script: a custom
// property (--side-w / --tapi-w) on the pane, through CSSOM, always a
// whole number of px that clampWidth made (gui.css reads it;
// TestGUIStylesOnlyInCSS allows that one line and no other).
//
// The width is kept in localStorage (no secret in it): it lasts while the
// GUI listens on the same port (every tab, every reopen), and for good
// with a fixed [gui] port. With the default port 0 the next listen is a
// new origin, and the width starts at the default again.

export const MAIN_MIN = 480; // the main column keeps at least this much
export const STEP = 16;

// a pane: where its width is kept, the custom property gui.css reads, the
// default and the bounds, and which of its edges holds the grip ("right":
// dragging right widens it; "left": dragging left does)
export const SIDE_PANE = { key: "pekoe-gui-side-w", prop: "--side-w", def: 300, min: 220, max: 560, edge: "right" };
export const TAPI_PANE = { key: "pekoe-gui-tapi-w", prop: "--tapi-w", def: 340, min: 280, max: 720, edge: "left" };

// maxWidth: the widest the pane may be when room px are left for it and
// the main column (the window less the other pane)
export function maxWidth(room, o) {
  const r = Number(room);
  if (!Number.isFinite(r)) return o.max;
  return Math.max(o.min, Math.min(o.max, Math.floor(r - MAIN_MIN)));
}

// clampWidth: a whole number of px between o.min and maxWidth(room, o);
// anything that is not a number reads as the default
export function clampWidth(w, room, o) {
  let n = Number(w);
  if (w === null || w === "" || !Number.isFinite(n)) n = o.def;
  return Math.min(maxWidth(room, o), Math.max(o.min, Math.round(n)));
}

// readWidth: the kept width, the default when none, odd or unreadable
export function readWidth(store, o) {
  try {
    const v = store().getItem(o.key);
    if (v !== null && /^\d{1,4}$/.test(v)) return Number(v);
  } catch {}
  return o.def;
}

// saveWidth: keep a width; the default forgets it
export function saveWidth(store, o, w) {
  try {
    if (w === o.def) store().removeItem(o.key);
    else store().setItem(o.key, String(w));
  } catch {}
}

// paneWidth wires a grip. pane: the element the property goes on; grip:
// the separator; o: SIDE_PANE or TAPI_PANE; store: () => localStorage;
// room: () => the px for this pane and the main column; changed: called
// when this pane's width moves (so the other pane's bound follows).
// Returns { width, apply }: the width shown, and redo the clamp (the room
// moved).
export function paneWidth({ pane, grip, o, store, room, changed = () => {} }) {
  const sign = o.edge === "left" ? -1 : 1; // the way that widens it
  let want = readWidth(store, o); // what the person chose; shown clamped
  let shown = 0;

  function apply() {
    shown = clampWidth(want, room(), o);
    pane.style.setProperty(o.prop, shown + "px");
    grip.setAttribute("aria-valuemin", String(o.min));
    grip.setAttribute("aria-valuemax", String(maxWidth(room(), o)));
    grip.setAttribute("aria-valuenow", String(shown));
  }
  // set: the width w, clamped to the room now (a drag, the keys: what is
  // shown is what the person chose); exact: w as it is, shown clamped
  // (the double click: the default, kept as the default, so a narrow
  // window does not keep the narrowed width in its place)
  function set(w, keep, exact) {
    want = exact ? w : clampWidth(w, room(), o);
    apply();
    changed();
    if (keep) saveWidth(store, o, want);
  }

  let drag = null; // { id, x, w } while a pointer holds the grip
  grip.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    drag = { id: e.pointerId, x: e.clientX, w: shown };
    grip.setPointerCapture(e.pointerId);
    grip.classList.add("held");
    document.body.classList.add("pane-resizing");
  });
  grip.addEventListener("pointermove", (e) => {
    if (drag && e.pointerId === drag.id) set(drag.w + sign * (e.clientX - drag.x), false);
  });
  const end = (e) => {
    if (!drag || e.pointerId !== drag.id) return;
    drag = null;
    grip.classList.remove("held");
    document.body.classList.remove("pane-resizing");
    saveWidth(store, o, want);
  };
  grip.addEventListener("pointerup", end);
  grip.addEventListener("pointercancel", end);
  grip.addEventListener("dblclick", () => set(o.def, true, true));
  grip.addEventListener("keydown", (e) => {
    // the arrows move the edge: → widens the sidebar, ← the tapi panel
    const step = (e.shiftKey ? STEP * 4 : STEP) * sign;
    const to = { ArrowLeft: shown - step, ArrowRight: shown + step, Home: o.min, End: maxWidth(room(), o) }[e.key];
    if (to === undefined) return;
    e.preventDefault();
    set(to, true);
  });
  // a narrower window narrows the pane; widening it again gives back the
  // chosen width (want is not changed by the window)
  window.addEventListener("resize", () => {
    apply();
    changed();
  });
  apply();
  return { width: () => shown, apply };
}
