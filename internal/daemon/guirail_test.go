package daemon

import (
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The icon rail (gui/rail.js, docs/gui.md 「サイドバーのビュー」): each
// button shows its view in the sidebar, one at a time (the one before is
// unmounted and its elements gone); the choice is kept for the tab
// (sessionStorage) and read back on a reload, Sessions when nothing (or
// something odd) is kept or the storage is blocked. The title bar's box
// belongs to the view shown: Sessions filters with it, a view without a
// search turns it off. Sessions draws the sidebar it drew before the rail
// (tapi's card, the repositories, OTHER REPOSITORIES, the chips) and its
// rows open what they show; a view not built yet says so. rail.js, the
// views and lib/ run in node as they are (skipped without node), on a
// small fake DOM.
func TestGUIRailViews(t *testing.T) {
	out := runWebModule(t, fakeDOM+`
N.prototype.removeAttribute = function (k) { delete this.attrs[k]; };
import { VIEWS, SIDE_KEY, sideRail, readSide } from "gui/rail.js";
globalThis.fetch = () => new Promise(() => {}); // the Worktrees view's read: never answered here
const m = new Map();
const store = () => ({ getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k) });
const went = [];
const page = () => {
  const buttons = VIEWS.map((v) => { const b = el0("button"); b.dataset = { view: v.id }; return b; });
  const side = el0("aside"), box = el0("div"), input = el0("input");
  input.value = "left over";
  const r = sideRail({ buttons, side, box, input, store, ctx: { go: (s) => went.push(s) } });
  return { r, buttons, side, box, input };
};
const at = (p) => ({ id: p.r.id(), cur: p.buttons.filter((b) => b.attrs["aria-current"] === "true").map((b) => b.dataset.view), label: p.side.attrs["aria-label"],
  off: p.input.disabled, value: p.input.value, ph: p.input.placeholder, text: p.box.textContent, kept: m.has(SIDE_KEY) ? m.get(SIDE_KEY) : null });
const S = (slug, repo, state, extra) => ({ slug, branch: slug, repo, state, state_since: new Date().toISOString(), agent: "claude", look: { icon: "◉", role: "ok", word: state }, ...extra });
const data = {
  sessions: [S("feat-a", "pekoe", "running"), S("feat-b", "pekoe", "waiting", { wait_kind: "question" }), S("main", "other", "idle")],
  tapi: { started: true, alive: true, state: "idle", look: { icon: "○", role: "dim", word: "idle" }, last_message: "line one\nline two" },
  overview: null, current: "feat-a",
};
const r = {};
const contract = VIEWS.map((v) => [v.id, v.title, typeof v.mount, typeof v.update, typeof v.unmount, v.search ? typeof v.query : "-"]);
let p = page();
r.first = at(p);
p.r.update(data);
r.sessions = at(p);
// a row is a <button>, or a div acting as one (asButton) when it can hold a PR link
const rows = find(p.box, (n) => /\bsrow\b/.test(n.className) && (n.tag === "button" || n.attrs.role === "button"));
r.rows = rows.map((b) => [b.className, b.attrs["aria-current"] || ""]);
rows[1].click();
find(p.box, (n) => /\btapi-card\b/.test(n.className))[0].click();
r.went = went.slice();
p.r.query("zzz");
r.none = p.box.textContent.includes("Nothing matches");
p.r.query("");
find(p.box, (n) => /\bchip\b/.test(n.className) && n.textContent.startsWith("Running"))[0].click();
r.running = find(p.box, (n) => /\bsrow\b/.test(n.className)).length;
find(p.box, (n) => /\bchip\b/.test(n.className) && n.textContent.startsWith("All"))[0].click();
p.buttons[1].click();
r.worktrees = at(p);
// a view without a search (one still not built) ignores the box
const plain = VIEWS.findIndex((v) => !v.search);
if (plain >= 0) {
  p.buttons[plain].click();
  const before = p.box.textContent;
  p.r.query("x");
  p.r.update(data);
  r.worktrees2 = p.input.disabled && p.box.textContent === before;
  p.buttons[1].click();
} else r.worktrees2 = true;
p = page(); // the tab reloaded
r.reload = at(p);
p.r.update(data);
p.buttons[0].click();
r.back = at(p);
r.backRows = find(p.box, (n) => /\bsrow\b/.test(n.className)).length;
for (const b of p.buttons) { b.click(); r["v_" + b.dataset.view] = at(p).text; }
r.blocked = readSide(() => { throw new Error("SecurityError"); });
m.set(SIDE_KEY, "nope");
r.odd = readSide(store);
console.log(JSON.stringify({ contract, ...r }));`)
	var got struct {
		Contract                [][]string
		First, Sessions         railAt
		Rows                    [][]string
		Went                    []string
		None                    bool
		Running                 int
		Worktrees, Reload, Back railAt
		Worktrees2              bool
		BackRows                int
		VSessions               string `json:"v_sessions"`
		VWorktrees              string `json:"v_worktrees"`
		VChanges                string `json:"v_changes"`
		VSettings               string `json:"v_settings"`
		Blocked, Odd            string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	ids := []string{}
	for _, c := range got.Contract {
		ids = append(ids, c[0])
		if c[1] == "" || c[2] != "function" || c[3] != "function" || c[4] != "function" || (c[5] != "-" && c[5] != "function") {
			t.Errorf("view %v: wants id, title, mount, update, unmount (and query with a search)", c)
		}
	}
	if !slices.Equal(ids, []string{"sessions", "worktrees", "changes", "settings"}) {
		t.Errorf("views: %v", ids)
	}
	// first load: Sessions, the box its filter, the left-over text gone
	if f := got.First; f.ID != "sessions" || !slices.Equal(f.Cur, []string{"sessions"}) || f.Label != "sessions" || f.Off || f.Value != "" ||
		f.Ph != "Filter sessions by name or repository" || f.Kept != nil {
		t.Errorf("first: %+v", f)
	}
	s := got.Sessions.Text
	for _, want := range []string{"SESSIONS3", "All 3", "Needs you 1", "Running 1", "tapiorchestrator", "line one", "pekoe2", "feat-a", "feat-b", "REPOSITORIES", "other", "1 waiting"} {
		if !strings.Contains(s, want) {
			t.Errorf("sessions view lacks %q: %s", want, s)
		}
	}
	if strings.Contains(s, "line two") {
		t.Errorf("tapi's card shows more than its first line: %s", s)
	}
	if len(got.Rows) != 3 || got.Rows[0][1] != "true" || !strings.Contains(got.Rows[0][0], "sel") || got.Rows[1][1] != "" || !strings.Contains(got.Rows[2][0], "one") {
		t.Errorf("rows: %v", got.Rows)
	}
	if !slices.Equal(got.Went, []string{"feat-b", "+tapi"}) {
		t.Errorf("rows open: %v", got.Went)
	}
	if !got.None || got.Running != 1 {
		t.Errorf("filter: none %v, running %d", got.None, got.Running)
	}
	// Worktrees: its own filter in the box, the choice kept (its rows:
	// TestGUIWorktreesView)
	if w := got.Worktrees; w.ID != "worktrees" || !slices.Equal(w.Cur, []string{"worktrees"}) || w.Label != "worktrees" || w.Off || w.Value != "" ||
		w.Ph != "Filter worktrees by branch or repository" || w.Kept == nil || *w.Kept != "worktrees" ||
		!strings.Contains(w.Text, "WORKTREES") || strings.Contains(w.Text, "Not built yet") || strings.Contains(w.Text, "SESSIONS") {
		t.Errorf("worktrees: %+v", w)
	}
	if !got.Worktrees2 {
		t.Error("a view without a search changed on query or update")
	}
	if got.Reload.ID != "worktrees" || !strings.Contains(got.Reload.Text, "WORKTREES") {
		t.Errorf("reload: %+v", got.Reload)
	}
	// back to Sessions: the storage forgets (Sessions is the default), and
	// the rows come with the data the rail already had
	if b := got.Back; b.ID != "sessions" || b.Kept != nil || b.Off || strings.Contains(b.Text, "WORKTREES") || got.BackRows != 3 {
		t.Errorf("back: %+v rows %d", b, got.BackRows)
	}
	if !strings.Contains(got.VChanges, "CHANGES") || strings.Contains(got.VChanges, "Not built yet") {
		t.Errorf("changes: %q", got.VChanges)
	}
	// Settings is built (guiconfig_test.go): its head, and it reads
	if !strings.Contains(got.VSettings, "SETTINGS") || strings.Contains(got.VSettings, "Not built yet") {
		t.Errorf("settings: %q", got.VSettings)
	}
	if got.Blocked != "sessions" || got.Odd != "sessions" {
		t.Errorf("blocked %q odd %q", got.Blocked, got.Odd)
	}

	// the page: one rail button per view, in the rail's order, named
	// (aria-label and title, docs/gui-design.md 「アイコン」); Settings at
	// the foot; every file in gui/views/ is a view of the rail (or the
	// not-yet helper), so a view's file is not left out
	html, _ := webFiles.ReadFile("webui/gui/index.html")
	btns := regexp.MustCompile(`<button type="button" class="rail-btn( foot)?" data-view="([a-z]+)" aria-label="([A-Za-z]+)" title="([A-Za-z]+)">`).FindAllStringSubmatch(string(html), -1)
	var bids []string
	for i, b := range btns {
		bids = append(bids, b[2])
		if i < len(got.Contract) && (b[3] != got.Contract[i][1] || b[4] != b[3]) {
			t.Errorf("rail button %s: label %q title %q, view %q", b[2], b[3], b[4], got.Contract[i][1])
		}
		if (b[2] == "settings") != (b[1] == " foot") {
			t.Errorf("rail button %s: foot %q", b[2], b[1])
		}
	}
	if !slices.Equal(bids, ids) || strings.Count(string(html), `class="rail-btn`) != len(ids) {
		t.Errorf("rail buttons %v, views %v", bids, ids)
	}
	ents, _ := fs.ReadDir(webFiles, "webui/gui/views")
	for _, e := range ents {
		id := strings.TrimSuffix(e.Name(), path.Ext(e.Name()))
		if !slices.Contains(ids, id) {
			t.Errorf("gui/views/%s is not a view of the rail", e.Name())
		}
	}
	// the page hands the sidebar to the rail: no sidebar of its own
	js, _ := webFiles.ReadFile("webui/gui/gui.js")
	if !strings.Contains(string(js), "sideRail({") || strings.Contains(string(js), "function sideRow") || strings.Contains(string(js), `"s-groups"`) {
		t.Error("gui.js does not leave the sidebar to rail.js")
	}
	// no number keys for the views (決まったこと 4): the rail is clicks
	rail, _ := webFiles.ReadFile("webui/gui/rail.js")
	if strings.Contains(string(rail), "keydown") {
		t.Error("rail.js takes keys")
	}
}

type railAt struct {
	ID    string
	Cur   []string
	Label string
	Off   bool
	Value string
	Ph    string
	Text  string
	Kept  *string
}
