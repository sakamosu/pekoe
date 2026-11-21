// The icon rail and the sidebar's views (docs/gui.md 「サイドバーのビュー」,
// docs/gui-design.md 「GUI で変えるもの」7). The rail's buttons
// (gui/index.html, one per view, data-view = the view's id) choose which
// view the sidebar shows; the choice is kept for this tab only
// (sessionStorage, as the theme): no number keys (決まったこと 4).
//
// A view is one file, gui/views/<id>.js, whose default export is
//   { id, title, search?, mount(box, ctx), update(state), query?(text), unmount() }
// mount builds the view into box (an empty element the rail owns); update
// gets the page's data after each refresh (and right after mount); query
// gets the title bar's text while the view is shown, if the view has a
// search (the box's placeholder): without one the box is off; unmount
// stops what it started, and the rail empties box after it.

import sessions from "./views/sessions.js";
import worktrees from "./views/worktrees.js";
import changes from "./views/changes.js";
import settings from "./views/settings.js";

// VIEWS: the rail's order (Settings at its foot, gui.css)
export const VIEWS = [sessions, worktrees, changes, settings];
export const SIDE_KEY = "pekoe-gui-side";

const byId = (id) => VIEWS.find((v) => v.id === id);

// readSide: the view this tab chose, else Sessions (blocked or odd storage
// reads as Sessions)
export function readSide(store) {
  try {
    const id = store().getItem(SIDE_KEY);
    if (byId(id)) return id;
  } catch {}
  return VIEWS[0].id;
}

export function saveSide(store, id) {
  try {
    if (id === VIEWS[0].id) store().removeItem(SIDE_KEY);
    else store().setItem(SIDE_KEY, id);
  } catch {}
}

// sideRail wires the rail. buttons: the rail's buttons (each data-view);
// side: the sidebar (its aria-label names the view shown); box: where the
// view goes; input: the title bar's box; store: () => the tab's storage;
// ctx: what views may call (go(slug) opens a session or tapi).
export function sideRail({ buttons, side, box, input, store, ctx }) {
  let view = null;
  let state = null; // the last data update got: a view mounted later gets it too

  function show(id) {
    const next = byId(id) || VIEWS[0];
    if (view === next) return;
    if (view) view.unmount();
    box.replaceChildren();
    view = next;
    for (const b of buttons) {
      if (b.dataset.view === view.id) b.setAttribute("aria-current", "true");
      else b.removeAttribute("aria-current");
    }
    side.setAttribute("aria-label", view.title.toLowerCase());
    // the title bar's box belongs to the view shown: its text starts empty
    input.value = "";
    input.disabled = !view.search;
    input.placeholder = view.search || "Nothing to filter in " + view.title;
    input.setAttribute("aria-label", view.search ? view.search.charAt(0).toLowerCase() + view.search.slice(1) : "nothing to filter");
    view.mount(box, ctx);
    if (view.search) view.query("");
    if (state) view.update(state);
  }

  for (const b of buttons) {
    b.addEventListener("click", () => {
      show(b.dataset.view);
      saveSide(store, view.id);
    });
  }
  show(readSide(store));

  return {
    id: () => view.id,
    update(s) {
      state = s;
      view.update(s);
    },
    query(text) {
      if (view.search) view.query(text);
    },
  };
}
