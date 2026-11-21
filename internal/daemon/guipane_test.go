package daemon

import (
	"io/fs"
	"os/exec"
	"strings"
	"testing"
)

// paneFake is a small fake of the page for gui/panewidth.js in node: a
// store over a Map, the window's resize, the body's classes, and page(o,
// room) that wires a pane and reads back its width, the grip's aria and
// the kept width.
const paneFake = `import { SIDE_PANE, TAPI_PANE, MAIN_MIN, clampWidth, maxWidth, readWidth, saveWidth, paneWidth } from "gui/panewidth.js";
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
const page = (o, room, changed) => {
  const props = {};
  const pane = { style: { setProperty: (k, v) => (props[k] = v) } };
  const grip = target();
  const cls = new Set();
  grip.classList = { add: (c) => cls.add(c), remove: (c) => cls.delete(c) };
  grip.setAttribute = (k, v) => (grip.attrs[k] = v);
  grip.setPointerCapture = () => {};
  const w = paneWidth({ pane, grip, o, store, room, changed });
  return { w, props, grip, held: () => cls.has("held") && body.has("pane-resizing"),
    at: () => [props[o.prop], grip.attrs["aria-valuenow"], grip.attrs["aria-valuemax"], m.has(o.key) ? m.get(o.key) : null] };
};
`

// One pane (gui/panewidth.js, docs/gui.md 「サイドバーのビュー」): dragging the grip moves it (the sidebar widens to the
// right, the tapi panel to the left) and stops at the narrowest and the
// widest (the main column keeps MAIN_MIN), a double click gives back the
// default, the arrow keys move the edge the way they point and Home / End
// go to the bounds, and a width is kept in the storage (the default
// forgets it) and read back on a reload; odd, missing or blocked storage
// reads as the default. The width is always a whole number of px (the one
// style a gui/ script sets, TestGUIStylesOnlyInCSS). Runs in node (skipped
// without node).
func TestGUIPaneWidth(t *testing.T) {
	out := runWebModule(t, paneFake+`
const run = (o, x, d) => {
  // x: where the drag starts; d: px the pointer moves the way that widens
  let room = 1600;
  const seen = {};
  let p = page(o, () => room);
  seen.start = p.at();
  p.grip.fire("pointerdown", { clientX: x });
  p.grip.fire("pointermove", { clientX: x + d * 80.4 });
  seen.dragging = [...p.at(), p.held()];
  p.grip.fire("pointerup", { clientX: x + d * 80 });
  seen.dropped = [...p.at(), p.held()];
  p = page(o, () => room); // reloaded
  seen.reload = p.at();
  p.grip.fire("pointerdown", { clientX: 0 });
  p.grip.fire("pointermove", { clientX: d * 5000 });
  p.grip.fire("pointerup");
  seen.widest = p.at();
  p.grip.fire("pointerdown", { clientX: 0 });
  p.grip.fire("pointermove", { clientX: -d * 5000 });
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
  room = 900; win.fire("resize"); // the window narrows: the pane too, the choice stays
  seen.narrow = p.at();
  room = 1600; win.fire("resize");
  seen.wide = p.at();
  p.grip.fire("keydown", { key: "Home" });
  seen.home = p.at();
  p.grip.fire("keydown", { key: "a" });
  seen.other = p.at();
  p.grip.fire("dblclick");
  win.on.resize = [];
  return seen;
};
const odd = (o) => ["abc", "-40", "1e3", "12.5", "", "99999"].map((v) => { m.set(o.key, v); return readWidth(store, o); });
saveWidth(blocked, SIDE_PANE, 400);
console.log(JSON.stringify({ side: run(SIDE_PANE, 300, 1), tapi: run(TAPI_PANE, 1000, -1),
  odd: [odd(SIDE_PANE), odd(TAPI_PANE)], blocked: [readWidth(blocked, SIDE_PANE), readWidth(blocked, TAPI_PANE)], main: MAIN_MIN,
  side_o: [SIDE_PANE.def, SIDE_PANE.min, SIDE_PANE.max, SIDE_PANE.key, SIDE_PANE.prop], tapi_o: [TAPI_PANE.def, TAPI_PANE.min, TAPI_PANE.max, TAPI_PANE.key, TAPI_PANE.prop],
  clamp: [clampWidth(NaN, 1600, SIDE_PANE), clampWidth("x", 1600, SIDE_PANE), clampWidth(null, 1600, TAPI_PANE), clampWidth(Infinity, 1600, TAPI_PANE),
    clampWidth(333.6, 1600, SIDE_PANE), clampWidth(400, 600, SIDE_PANE), clampWidth(400, 600, TAPI_PANE), clampWidth(400, NaN, SIDE_PANE), maxWidth(NaN, TAPI_PANE)] }));`)
	want := `{"side":{"start":["300px","300","560",null],"dragging":["380px","380","560",null,true],"dropped":["380px","380","560","380",false],` +
		`"reload":["380px","380","560","380"],"widest":["560px","560","560","560"],"narrowest":["220px","220","560","220"],"loose":["220px","220","560","220"],` +
		`"dblclick":["300px","300","560",null],"right":["316px","316","560","316"],"left":["252px","252","560","252"],"end":["560px","560","560","560"],` +
		`"narrow":["420px","420","420","560"],"wide":["560px","560","560","560"],"home":["220px","220","560","220"],"other":["220px","220","560","220"]},` +
		`"tapi":{"start":["340px","340","720",null],"dragging":["420px","420","720",null,true],"dropped":["420px","420","720","420",false],` +
		`"reload":["420px","420","720","420"],"widest":["720px","720","720","720"],"narrowest":["280px","280","720","280"],"loose":["280px","280","720","280"],` +
		`"dblclick":["340px","340","720",null],"right":["324px","324","720","324"],"left":["388px","388","720","388"],"end":["720px","720","720","720"],` +
		`"narrow":["420px","420","420","720"],"wide":["720px","720","720","720"],"home":["280px","280","720","280"],"other":["280px","280","720","280"]},` +
		`"odd":[[300,300,300,300,300,300],[340,340,340,340,340,340]],"blocked":[300,340],"main":480,` +
		`"side_o":[300,220,560,"pekoe-gui-side-w","--side-w"],"tapi_o":[340,280,720,"pekoe-gui-tapi-w","--tapi-w"],` +
		`"clamp":[300,300,340,340,334,220,280,400,720]}`
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("pane width:\n got %s\nwant %s", got, want)
	}
}

// The sidebar and the tapi panel together, wired as gui.js wires them:
// the sidebar's room is the window less the tapi panel's narrowest, the
// tapi panel's is what the sidebar leaves. Widening the sidebar narrows
// the tapi panel and lowers its bound (aria-valuemax too), the main column
// keeps MAIN_MIN, a narrower window narrows the tapi panel first, and the
// split comes back the same after a wider window or a Session (where the
// tapi panel is not shown and the sidebar does not count it) and back.
// While the tapi panel is under the main column (not beside), the sidebar
// does not count it either.
func TestGUIPaneWidthTogether(t *testing.T) {
	out := runWebModule(t, paneFake+`
let room = 1600, beside = true, dash = true;
let tapi = null;
const side = page(SIDE_PANE, () => room - (beside && dash ? TAPI_PANE.min : 0), () => tapi?.w.apply());
tapi = page(TAPI_PANE, () => room - side.w.width());
const both = () => [side.at(), tapi.at(), room - side.w.width() - tapi.w.width()];
const seen = {};
seen.start = both();
side.grip.fire("keydown", { key: "End" });
seen.sideEnd = both();
tapi.grip.fire("keydown", { key: "End" });
seen.tapiEnd = both();
side.grip.fire("keydown", { key: "Home" });
tapi.grip.fire("pointerdown", { clientX: 1000 });
tapi.grip.fire("pointermove", { clientX: 0 });
tapi.grip.fire("pointerup");
seen.tapiDrag = both();
side.grip.fire("keydown", { key: "End" }); // pushes the tapi panel, its choice kept
seen.sidePush = both();
room = 1200; win.fire("resize");
seen.narrow = both();
room = 1600; win.fire("resize");
seen.wide = both();
room = 1300; win.fire("resize");
seen.at1300 = both();
dash = false; side.w.apply(); tapi.w.apply(); // a Session: no tapi panel
seen.session = side.at();
dash = true; side.w.apply(); tapi.w.apply(); // the Dashboard again
seen.dashboard = both();
room = 1600; win.fire("resize");
beside = false; room = 1100; win.fire("resize"); // the tapi panel goes under the main column
seen.under = [side.at()[2]];
console.log(JSON.stringify(seen));`)
	want := `{"start":[["300px","300","560",null],["340px","340","720",null],960],` +
		`"sideEnd":[["560px","560","560","560"],["340px","340","560",null],700],` +
		`"tapiEnd":[["560px","560","560","560"],["560px","560","560","560"],480],` +
		`"tapiDrag":[["220px","220","560","220"],["720px","720","720","720"],660],` +
		`"sidePush":[["560px","560","560","560"],["560px","560","560","720"],480],` +
		`"narrow":[["440px","440","440","560"],["280px","280","280","720"],480],` +
		`"wide":[["560px","560","560","560"],["560px","560","560","720"],480],` +
		`"at1300":[["540px","540","540","560"],["280px","280","280","720"],480],` +
		`"session":["560px","560","560","560"],` +
		`"dashboard":[["540px","540","540","560"],["280px","280","280","720"],480],` +
		`"under":["560"]}`
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("panes together:\n got %s\nwant %s", got, want)
	}
}

// The page wires both grips (gui.js), each is a focusable vertical
// separator for its pane (index.html), the panes read their properties,
// and neither grip shows where the pane does not sit beside the main
// column (gui.css: the sidebar stacks at 760px, the tapi panel goes under
// the main column at 1180px).
func TestGUIPaneGrips(t *testing.T) {
	js, _ := webFiles.ReadFile("webui/gui/gui.js")
	for _, s := range []string{
		`pane: $("side"), grip: $("side-grip"), o: SIDE_PANE, store: () => window.localStorage`,
		`pane: $("tapi-panel"), grip: $("tapi-grip"), o: TAPI_PANE, store: () => window.localStorage`,
		`window.matchMedia("(min-width: 1181px)")`,
		`const tapiBeside = () => beside.matches && !$("dashboard").hidden;`,
		`room: () => window.innerWidth - (tapiBeside() ? TAPI_PANE.min : 0),`,
		`room: () => window.innerWidth - sideWidth.width(),`,
		"$(\"detail\").hidden = view !== \"detail\";\n  sideWidth.apply();",
	} {
		if !strings.Contains(string(js), s) {
			t.Errorf("gui.js does not wire the grips (panewidth.js): no %s", s)
		}
	}
	html, _ := webFiles.ReadFile("webui/gui/index.html")
	for _, s := range []string{
		`<div class="pane-grip side-grip" id="side-grip" role="separator" aria-orientation="vertical" aria-controls="side" aria-label="Sidebar width" title="Drag to resize · double-click to reset" tabindex="0"></div>`,
		`<aside class="tapi-panel" id="tapi-panel" aria-label="tapi">`,
		`<div class="pane-grip tapi-grip" id="tapi-grip" role="separator" aria-orientation="vertical" aria-controls="tapi-panel" aria-label="tapi panel width" title="Drag to resize · double-click to reset" tabindex="0"></div>`,
	} {
		if !strings.Contains(string(html), s) {
			t.Errorf("index.html: no %s", s)
		}
	}
	css, _ := webFiles.ReadFile("webui/gui/gui.css")
	for _, s := range []string{"width: var(--side-w, 300px)", "flex: 0 0 var(--tapi-w, 340px)", ".side-grip { display: none; }", ".tapi-grip { display: none; }"} {
		if !strings.Contains(string(css), s) {
			t.Errorf("gui.css: no %s", s)
		}
	}
	mq := func(q string) string {
		i := strings.Index(string(css), q)
		if i < 0 {
			return ""
		}
		rest := string(css)[i:]
		if j := strings.Index(rest[1:], "@media"); j >= 0 {
			rest = rest[:j+1]
		}
		return rest
	}
	if !strings.Contains(mq("@media (max-width: 1180px)"), ".tapi-grip { display: none; }") {
		t.Error("gui.css: the tapi grip shows while the panel is under the main column")
	}
	if !strings.Contains(mq("@media (max-width: 760px)"), ".side-grip { display: none; }") {
		t.Error("gui.css: the sidebar's grip shows while it is stacked")
	}
}

// Every script of the desktop page parses as a module (node --check):
// a bad one (a name imported twice, say) stops gui.js, and the page comes
// up empty with no test of a single module noticing. Skipped without node.
func TestGUIScriptsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found: the page's modules are not parsed")
	}
	n := 0
	err = fs.WalkDir(webFiles, "webui", func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".js") {
			return err
		}
		js, err := webFiles.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		cmd := exec.Command(node, "--input-type=module", "--check")
		cmd.Stdin = strings.NewReader(string(js))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s does not parse: %v\n%s", p, err, out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 10 {
		t.Fatalf("scripts parsed: %d", n)
	}
}
