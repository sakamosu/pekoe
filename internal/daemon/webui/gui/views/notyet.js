// notYet: a view of the rail that is not built yet (docs/gui.md
// 「サイドバーのビュー」): its name and a line saying it is not here yet,
// with what it is to show. Nothing is read, nothing can be done in it. A
// view's own file replaces its call with the real view when it is built.

import { el } from "../../lib/dom.js";

export function notYet(id, title, what) {
  return {
    id,
    title,
    mount(box) {
      const list = el("div", "side-list");
      const head = el("div", "side-head");
      head.append(el("span", "label", title.toUpperCase()));
      const empty = el("div", "side-empty");
      empty.append(el("p", "se-title", "Not built yet"), el("p", "se-what", what));
      list.append(head, empty);
      box.append(list);
    },
    update() {},
    unmount() {},
  };
}
