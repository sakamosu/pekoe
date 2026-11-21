// The sidebar's width (docs/gui.md 「サイドバーのビュー」): the grip on
// its right edge (gui/index.html #side-grip, role="separator") drags it,
// a double click gives back the default, and the arrow keys (Home / End)
// move it from the keyboard. The width is the one style this page sets
// from a script: the custom property --side-w on the sidebar, through
// CSSOM, always a whole number of px that clampWidth made (gui.css reads
// it; TestGUIStylesOnlyInCSS allows that one line and no other).
//
// The width is kept in localStorage (no secret in it): it lasts while the
// GUI listens on the same port (every tab, every reopen), and for good
// with a fixed [gui] port. With the default port 0 the next listen is a
// new origin, and the width starts at the default again.

export const WIDTH_KEY = "pekoe-gui-side-w";
export const DEFAULT_W = 300;
export const MIN_W = 220;
export const MAX_W = 560;
export const MAIN_MIN = 480; // the main column keeps at least this much
export const STEP = 16;

// maxWidth: the widest the sidebar may be in a window this wide
export function maxWidth(room) {
  const r = Number(room);
  if (!Number.isFinite(r)) return MAX_W;
  return Math.max(MIN_W, Math.min(MAX_W, Math.floor(r - MAIN_MIN)));
}

// clampWidth: a whole number of px between MIN_W and maxWidth(room);
// anything that is not a number reads as the default
export function clampWidth(w, room) {
  let n = Number(w);
  if (w === null || w === "" || !Number.isFinite(n)) n = DEFAULT_W;
  return Math.min(maxWidth(room), Math.max(MIN_W, Math.round(n)));
}

// readWidth: the kept width, the default when none, odd or unreadable
export function readWidth(store) {
  try {
    const v = store().getItem(WIDTH_KEY);
    if (v !== null && /^\d{1,4}$/.test(v)) return Number(v);
  } catch {}
  return DEFAULT_W;
}

// saveWidth: keep a width; the default forgets it
export function saveWidth(store, w) {
  try {
    if (w === DEFAULT_W) store().removeItem(WIDTH_KEY);
    else store().setItem(WIDTH_KEY, String(w));
  } catch {}
}

// sideWidth wires the grip. side: the sidebar; grip: the separator;
// store: () => localStorage; room: () => the window's width.
export function sideWidth({ side, grip, store, room }) {
  let want = readWidth(store); // what the person chose; shown clamped
  let shown = 0;

  function apply() {
    shown = clampWidth(want, room());
    side.style.setProperty("--side-w", shown + "px");
    grip.setAttribute("aria-valuemin", String(MIN_W));
    grip.setAttribute("aria-valuemax", String(maxWidth(room())));
    grip.setAttribute("aria-valuenow", String(shown));
  }
  function set(w, keep) {
    want = clampWidth(w, room());
    apply();
    if (keep) saveWidth(store, want);
  }

  let drag = null; // { id, x, w } while a pointer holds the grip
  grip.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    drag = { id: e.pointerId, x: e.clientX, w: shown };
    grip.setPointerCapture(e.pointerId);
    document.body.classList.add("side-resizing");
  });
  grip.addEventListener("pointermove", (e) => {
    if (drag && e.pointerId === drag.id) set(drag.w + e.clientX - drag.x, false);
  });
  const end = (e) => {
    if (!drag || e.pointerId !== drag.id) return;
    drag = null;
    document.body.classList.remove("side-resizing");
    saveWidth(store, want);
  };
  grip.addEventListener("pointerup", end);
  grip.addEventListener("pointercancel", end);
  grip.addEventListener("dblclick", () => set(DEFAULT_W, true));
  grip.addEventListener("keydown", (e) => {
    const step = e.shiftKey ? STEP * 4 : STEP;
    const to = { ArrowLeft: shown - step, ArrowRight: shown + step, Home: MIN_W, End: maxWidth(room()) }[e.key];
    if (to === undefined) return;
    e.preventDefault();
    set(to, true);
  });
  // a narrower window narrows the sidebar; widening it again gives back
  // the chosen width (want is not changed by the window)
  window.addEventListener("resize", apply);
  apply();
}
