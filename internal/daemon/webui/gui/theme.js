// The theme choice of the local GUI (docs/gui-design.md 決まったこと 11):
// "system" follows the OS's appearance (no data-theme: /theme.css's
// prefers-color-scheme), "dark" and "light" are picked by hand (the <html>
// data-theme attribute). The title bar's button goes system → dark →
// light → system, so a hand-picked theme can always be given back to the
// OS. A hand pick is kept in sessionStorage for this tab (the origin's
// port changes with every listen, so localStorage would not find it next
// time anyway); "system" removes it.

export const THEME_KEY = "pekoe.gui.theme";
export const THEMES = ["system", "dark", "light"];
export const THEME_WORDS = { system: "System", dark: "Dark", light: "Light" };

const known = (c) => (c === "dark" || c === "light" ? c : "system");

// nextTheme: the button's next choice; anything unknown counts as system.
export const nextTheme = (c) => THEMES[(THEMES.indexOf(known(c)) + 1) % THEMES.length];

// readTheme: the tab's choice, "system" when none (or storage is blocked).
// store returns the storage: reading window.sessionStorage itself throws
// when the browser blocks it.
export function readTheme(store) {
  try {
    return known(store().getItem(THEME_KEY));
  } catch (e) {
    return "system";
  }
}

// saveTheme: keep a hand pick; "system" forgets it.
export function saveTheme(store, c) {
  try {
    if (known(c) === "system") store().removeItem(THEME_KEY);
    else store().setItem(THEME_KEY, c);
  } catch (e) { /* this page only */ }
}

// applyTheme: the choice on <html> (root.dataset.theme), which /theme.css
// switches the variables on. No style is written.
export function applyTheme(root, c) {
  const k = known(c);
  if (k === "system") delete root.dataset.theme;
  else root.dataset.theme = k;
  return k;
}
