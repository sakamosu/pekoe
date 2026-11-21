package daemon

import (
	"strings"
	"testing"
)

// The sidebar's width (gui/sidewidth.js, docs/gui.md 「サイドバーのビュー」):
// dragging the grip moves it and stops at the narrowest and the widest
// (the main column keeps MAIN_MIN), a double click gives back the default,
// the arrow keys / Home / End move it, and a width is kept in the storage
// (the default forgets it) and read back on a reload; odd, missing or
// blocked storage reads as the default. The width is always a whole number
// of px (the one style a gui/ script sets, TestGUIStylesOnlyInCSS). Runs in
// node (skipped without node) on a small fake of the page.
func TestGUISideWidth(t *testing.T) {
	out := runWebModule(t, `import { WIDTH_KEY, DEFAULT_W, MIN_W, MAX_W, clampWidth, readWidth, saveWidth, sideWidth } from "gui/sidewidth.js";
const m = new Map();
const store = () => ({ getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k) });
const blocked = () => { throw new Error("SecurityError"); };
const target = () => {
  const on = {};
  return { on, attrs: {}, addEventListener(k, f) { (on[k] ||= []).push(f); }, fire(k, e = {}) { for (const f of on[k] || []) f({ preventDefault() {}, button: 0, pointerId: 1, ...e }); } };
};
const win = target();
globalThis.window = win;
const body = new Set();
globalThis.document = { body: { classList: { add: (c) => body.add(c), remove: (c) => body.delete(c) } } };
let room = 1600;
const page = () => {
  const props = {};
  const side = { style: { setProperty: (k, v) => (props[k] = v) } };
  const grip = target();
  grip.setAttribute = (k, v) => (grip.attrs[k] = v);
  grip.setPointerCapture = () => {};
  sideWidth({ side, grip, store, room: () => room });
  return { props, grip, at: () => [props["--side-w"], grip.attrs["aria-valuenow"], grip.attrs["aria-valuemax"], m.has(WIDTH_KEY) ? m.get(WIDTH_KEY) : null] };
};
const seen = {};
let p = page();
seen.start = p.at();
p.grip.fire("pointerdown", { clientX: 300 });
p.grip.fire("pointermove", { clientX: 380.4 });
seen.dragging = [...p.at(), body.has("side-resizing")];
p.grip.fire("pointerup", { clientX: 380 });
seen.dropped = [...p.at(), body.has("side-resizing")];
p = page(); // reloaded
seen.reload = p.at();
p.grip.fire("pointerdown", { clientX: 0 });
p.grip.fire("pointermove", { clientX: 5000 });
p.grip.fire("pointerup");
seen.widest = p.at();
p.grip.fire("pointerdown", { clientX: 0 });
p.grip.fire("pointermove", { clientX: -5000 });
p.grip.fire("pointerup");
seen.narrowest = p.at();
p.grip.fire("pointermove", { clientX: 900 }); // no pointer holds it
seen.loose = p.at();
p.grip.fire("dblclick");
seen.dblclick = p.at();
p.grip.fire("keydown", { key: "ArrowRight" });
seen.right = p.at();
p.grip.fire("keydown", { key: "ArrowLeft", shiftKey: true });
seen.left = p.at();
p.grip.fire("keydown", { key: "End" });
seen.end = p.at();
room = 900; win.fire("resize"); // the window narrows: the sidebar too, the choice stays
seen.narrow = p.at();
room = 1600; win.fire("resize");
seen.wide = p.at();
p.grip.fire("keydown", { key: "Home" });
seen.home = p.at();
p.grip.fire("keydown", { key: "a" });
seen.other = p.at();
const odd = ["abc", "-40", "1e3", "12.5", "", "99999"].map((v) => { m.set(WIDTH_KEY, v); return readWidth(store); });
saveWidth(blocked, 400);
console.log(JSON.stringify({ seen, odd, blocked: readWidth(blocked), def: DEFAULT_W, min: MIN_W, max: MAX_W,
  clamp: [clampWidth(NaN, 1600), clampWidth("x", 1600), clampWidth(null, 1600), clampWidth(Infinity, 1600), clampWidth(333.6, 1600), clampWidth(400, 600), clampWidth(400, NaN)] }));`)
	want := `{"seen":{"start":["300px","300","560",null],"dragging":["380px","380","560",null,true],"dropped":["380px","380","560","380",false],` +
		`"reload":["380px","380","560","380"],"widest":["560px","560","560","560"],"narrowest":["220px","220","560","220"],"loose":["220px","220","560","220"],` +
		`"dblclick":["300px","300","560",null],"right":["316px","316","560","316"],"left":["252px","252","560","252"],"end":["560px","560","560","560"],` +
		`"narrow":["420px","420","420","560"],"wide":["560px","560","560","560"],"home":["220px","220","560","220"],"other":["220px","220","560","220"]},` +
		`"odd":[300,300,300,300,300,300],"blocked":300,"def":300,"min":220,"max":560,"clamp":[300,300,300,300,334,220,400]}`
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("sidebar width:\n got %s\nwant %s", got, want)
	}
	js, _ := webFiles.ReadFile("webui/gui/gui.js")
	if !strings.Contains(string(js), `sideWidth({ side: $("side"), grip: $("side-grip"), store: () => window.localStorage`) {
		t.Error("gui.js does not wire the sidebar's grip (sidewidth.js)")
	}
	html, _ := webFiles.ReadFile("webui/gui/index.html")
	for _, s := range []string{`id="side-grip" role="separator" aria-orientation="vertical" aria-controls="side"`, `tabindex="0"`} {
		if !strings.Contains(string(html), s) {
			t.Errorf("index.html: the grip lacks %s", s)
		}
	}
	css, _ := webFiles.ReadFile("webui/gui/gui.css")
	if !strings.Contains(string(css), "width: var(--side-w, 300px)") || !strings.Contains(string(css), ".side-grip { display: none; }") {
		t.Error("gui.css: the sidebar does not read --side-w, or the grip shows when stacked")
	}
}
