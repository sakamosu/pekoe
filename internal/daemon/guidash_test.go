package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// GET /api/stats is `pekoe --json stats` for 7 days, on the GUI only and
// with a session token: a merge and tokens recorded today come back as
// today's.
func TestGUIStats(t *testing.T) {
	d, _, _, h, _ := guiSetup(t)
	now := time.Now()
	today := now.Format("2006-01-02")
	merged := now.Add(-time.Minute)
	if err := d.DB.RecordSession(state.SessionRecord{Slug: "gone", Repo: "pekoe", PR: 7, CreatedAt: now.Add(-time.Hour), MergedAt: &merged, EndedAt: &merged}); err != nil {
		t.Fatal(err)
	}
	u := state.Usage{Input: 12, Output: 3400, CacheCreation: 56000, CacheRead: 7800000}
	if err := d.DB.AddUsage(state.UsageCursor{Slug: "gone", Born: state.Born(&state.Session{CreatedAt: now.Add(-time.Hour)}), Path: "/x.jsonl", Offset: 1}, "pekoe", []state.UsageMsg{{ID: "m1", Day: today, Usage: u}}); err != nil {
		t.Fatal(err)
	}
	if w := doGUI(t, h, guiReq{path: "/api/stats"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	s := guiSignIn(t, d, h)
	w := doGUI(t, h, guiReq{path: "/api/stats", bearer: s})
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("stats: %d %v %s", w.Code, w.Header(), w.Body)
	}
	var st Stats
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.To != today || st.From != now.AddDate(0, 0, -6).Format("2006-01-02") || st.Merged != 1 || st.Tokens != u {
		t.Fatalf("stats: %+v", st)
	}
	if len(st.Rows) != 1 || st.Rows[0].Day != today || st.Rows[0].Merged != 1 || st.Rows[0].Tokens != u {
		t.Fatalf("rows: %+v", st.Rows)
	}
}

// GET /api/ledger: the open requests (oldest first), then the closed ones
// closed last (newest first, 5 by default, ?closed=N up to 500) and how
// many are closed; the text as it was typed. The phone's Web UI has
// neither route.
func TestGUILedger(t *testing.T) {
	d, _, _, h, _ := guiSetup(t)
	base := time.Now().Add(-48 * time.Hour)
	evil := `<img src=x onerror=alert(1)>`
	var ids []int64
	for i := range 10 {
		e := &state.LedgerEntry{At: base.Add(time.Duration(i) * time.Hour), Text: fmt.Sprintf("req %d %s", i, evil), Status: state.LedgerOpen}
		if err := d.DB.AddLedger(e); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	// closed in an order of their own: 0 last, then 2, 4, 6, 8 earlier
	for k, i := range []int{8, 6, 4, 2, 0, 9} {
		e, _ := d.DB.LedgerGet(ids[i])
		at := base.Add(30*time.Hour + time.Duration(k)*time.Minute)
		e.ClosedAt = &at
		e.Status = state.LedgerDone
		if i == 0 {
			e.Status, e.Auto = state.LedgerChat, true
		}
		if i == 9 {
			e.Status, e.Note = state.LedgerDropped, "no"
		}
		if err := d.DB.SaveLedger(e); err != nil {
			t.Fatal(err)
		}
	}
	s := guiSignIn(t, d, h)
	get := func(q string) guiLedgerView {
		t.Helper()
		w := doGUI(t, h, guiReq{path: "/api/ledger" + q, bearer: s})
		if w.Code != 200 {
			t.Fatalf("ledger%s: %d %s", q, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "<img") {
			t.Fatalf("raw tag in the JSON: %s", w.Body)
		}
		var v guiLedgerView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	idsOf := func(es []state.LedgerEntry) []int64 {
		var out []int64
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	v := get("")
	if got, want := idsOf(v.Open), []int64{ids[1], ids[3], ids[5], ids[7]}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("open: %v, want %v", got, want)
	}
	if got, want := idsOf(v.Closed), []int64{ids[9], ids[0], ids[2], ids[4], ids[6]}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("closed: %v, want %v", got, want)
	}
	if v.ClosedTotal != 6 || v.Open[0].Text != "req 1 "+evil || !v.Closed[1].Auto || v.Closed[0].Status != state.LedgerDropped {
		t.Fatalf("view: %+v", v)
	}
	if v := get("?closed=500"); len(v.Closed) != 6 || v.Closed[5].ID != ids[8] {
		t.Fatalf("closed=500: %v", idsOf(v.Closed))
	}
	if v := get("?closed=0"); len(v.Closed) != 0 || v.ClosedTotal != 6 || len(v.Open) != 4 {
		t.Fatalf("closed=0: %+v", v)
	}
	for _, q := range []string{"?closed=501", "?closed=-1", "?closed=x"} {
		if w := doGUI(t, h, guiReq{path: "/api/ledger" + q, bearer: s}); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	if w := doGUI(t, h, guiReq{path: "/api/ledger"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	// empty: arrays, not null (the page reads .open / .closed)
	d2, _, _, h2, _ := guiSetup(t)
	w := doGUI(t, h2, guiReq{path: "/api/ledger", bearer: guiSignIn(t, d2, h2)})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"open":[]`) || !strings.Contains(w.Body.String(), `"closed":[]`) {
		t.Fatalf("empty: %d %s", w.Code, w.Body)
	}

	ph := testWebHandler(d)
	for _, p := range []string{"/api/stats", "/api/ledger"} {
		w := doWeb(t, ph, webReq{path: p, from: ipPhone})
		if w.Code == 200 && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("phone %s: %d %s", p, w.Code, w.Body)
		}
	}
}

// The tokens the Overview shows are shortened as pekoe stats and the TUI
// shorten them: gui/dash.js's shortTokens run in node (skipped without
// node) against state.ShortTokens, ties and unit edges included.
func TestGUIShortTokensMatchesPekoed(t *testing.T) {
	ins := []int64{0, 1, 999, 1000, 1049, 1050, 1149, 1150, 1250, 1350, 12345, 99949, 99950, 999949, 999950, 1000000,
		1005000, 1015000, 1234567, 999994999, 999995000, 1000000000, 1234567890, 1005000000, 98765432109}
	for i := int64(1); i < 4000; i += 37 {
		ins = append(ins, i*50, i*5000, i*5_000_000)
	}
	raw, _ := json.Marshal(ins)
	out := runWebModule(t, `import { shortTokens } from "gui/dash.js";
console.log(JSON.stringify(`+string(raw)+`.map(shortTokens)));`)
	var got []string
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(ins) {
		t.Fatalf("node: %v %s", err, out)
	}
	for i, n := range ins {
		if want := state.ShortTokens(n); got[i] != want {
			t.Errorf("%d: page %q, pekoed %q", n, got[i], want)
		}
	}
}

// The Overview's tiles and the Requests drawn from real-shaped data in a
// small fake DOM (node; skipped without node): one bar a day, oldest
// first, today's secondary, each with its number in a <title> and all of
// them in the label; today's tokens by kind; the open request first with
// its session as a button, the closed ones dim with how they ended, and
// the text in as text.
func TestGUIDashRender(t *testing.T) {
	out := runWebModule(t, fakeDOM+`
import { renderTiles, renderRequests, mergeDays } from "gui/dash.js";
const st = {
  from: "2026-11-15", to: "2026-11-21",
  rows: [
    { day: "2026-11-21", repo: "pekoe", merged: 1, tokens: { input: 1500, output: 383800, cache_creation: 2210000, cache_read: 88330000 } },
    { day: "2026-11-21", repo: "", merged: 0, tokens: { input: 0, output: 200, cache_creation: 0, cache_read: 0 } },
    { day: "2026-11-18", repo: "pekoe", merged: 4, tokens: { input: 1, output: 1, cache_creation: 1, cache_read: 1 } },
    { day: "2026-11-16", repo: "x", merged: 2, tokens: { input: 1, output: 1, cache_creation: 1, cache_read: 1 } },
  ],
  merges: [], merged: 7, tokens: {},
};
const box = el0("div");
renderTiles(box, { running: 1 }, { st, err: "" });
const tiles = box.children;
const bars = find(tiles[1], (n) => n.tag === "rect");
const res = {
  days: mergeDays(st).map((d) => d.day + ":" + d.merged + (d.today ? "*" : "")),
  names: tiles.map((t) => t.children[0].textContent),
  merged: tiles[1].children[1].children.filter((c) => c.tag !== "svg").map((c) => c.textContent),
  bars: bars.map((r) => [r.attrs.class, r.attrs.height, r.children[0].textContent]),
  label: find(tiles[1], (n) => n.tag === "svg")[0].attrs["aria-label"],
  tokens: tiles[2].children[1].children.map((kv) => kv.textContent + "|" + kv.children[1].title),
};
const err = el0("div");
renderTiles(err, {}, { st: null, err: "boom" });
res.err = err.children.map((t) => t.textContent);
const evil = "<img src=x onerror=alert(1)>";
const lv = {
  open: [{ id: 894, at: new Date(Date.now() - 39 * 60000).toISOString(), text: "マージした。" + evil, status: "open", sessions: ["old", "feat-x"] }],
  closed: [
    { id: 12, at: "2026-11-01T00:00:00Z", closed_at: new Date(Date.now() - 3 * 3600000).toISOString(), text: "hi", status: "chat", auto: true, sessions: [] },
    { id: 11, at: "2026-11-01T00:00:00Z", closed_at: new Date(Date.now() - 5 * 3600000).toISOString(), text: "fix", status: "done", sessions: ["gone"] },
  ],
  closed_total: 9,
};
const opened = [];
const rq = el0("div");
res.counts = renderRequests(rq, lv, { open: (s) => opened.push(s), has: (s) => s === "feat-x", expanded: true });
res.rows = rq.children.map((r) => [r.className, r.textContent]);
find(rq, (n) => n.tag === "button")[0].click();
res.opened = opened;
res.htmlNodes = find(rq, (n) => n.tag === "img").length;
const rq2 = el0("div");
renderRequests(rq2, lv, { open: () => {}, has: () => false, expanded: false });
res.collapsed = rq2.children.map((r) => r.textContent);
console.log(JSON.stringify(res));
`)
	var res struct {
		Days, Names, Err, Tokens, Opened, Collapsed, Merged []string
		Label                                               string
		Bars                                                [][3]string
		Counts                                              []int
		Rows                                                [][2]string
		HTMLNodes                                           int
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	want := func(what string, got, want any) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s:\n got %v\nwant %v", what, got, want)
		}
	}
	want("days", res.Days, []string{"2026-11-15:0", "2026-11-16:2", "2026-11-17:0", "2026-11-18:4", "2026-11-19:0", "2026-11-20:0", "2026-11-21:1*"})
	want("tiles", res.Names, []string{"Right now", "Merged", "Tokens today"})
	want("merged", res.Merged, []string{"1 today", "7 7 days"})
	want("bars", res.Bars, [][3]string{
		{"bar zero", "2", "Sun 2026-11-15 · 0 merged"},
		{"bar", "14", "Mon 2026-11-16 · 2 merged"},
		{"bar zero", "2", "Tue 2026-11-17 · 0 merged"},
		{"bar", "28", "Wed 2026-11-18 · 4 merged"},
		{"bar zero", "2", "Thu 2026-11-19 · 0 merged"},
		{"bar zero", "2", "Fri 2026-11-20 · 0 merged"},
		{"bar today", "7", "today 2026-11-21 · 1 merged"},
	})
	if !strings.HasPrefix(res.Label, "Merges per day: Sun 2026-11-15 0, Mon 2026-11-16 2,") || !strings.HasSuffix(res.Label, "today 2026-11-21 1") {
		t.Errorf("label: %s", res.Label)
	}
	want("tokens", res.Tokens, []string{"out384.0k|384,000 tokens", "in1.5k|1,500 tokens", "cache write2.21M|2,210,000 tokens", "cache read88.33M|88,330,000 tokens"})
	want("stats failed", res.Err[1:], []string{"Merged✗ stats unavailable · boom", "Tokens today✗ stats unavailable · boom"})
	want("counts", res.Counts, []int{1, 9})
	want("rows", res.Rows, [][2]string{
		{"lrow", "◉r894マージした。" + `<img src=x onerror=alert(1)>` + "open → feat-x +139m"},
		{"lrow past", "✓r12hiclosed by pekoe · 3h"},
		{"lrow past", "✓r11fixdone · 5h"},
		{"lrow more", "+7 older · pekoe ledger --all"},
	})
	want("opened", res.Opened, []string{"feat-x"})
	want("html nodes", res.HTMLNodes, 0)
	if len(res.Collapsed) != 3 || !strings.Contains(res.Collapsed[0], "open → feat-x +1") {
		t.Errorf("collapsed, session gone: %v", res.Collapsed)
	}
}

// The Pipeline as a tree (docs/gui-design.md「Pipeline」): a request's
// branches under its box with one trunk (a straight line for the first
// branch, a corner for the others), the newest request on top, a branch no
// request made on its own row after them; a later request as a badge (◉
// while it is at work, + once done) and "reopened by" when it sent the
// branch back to impl; the step boxes in their looks (CI as passed/all,
// the person's merge as you); waits written dim in one line, never as ✗;
// inside a request, the rows in the overview's order of states.
func TestGUIPipelineRender(t *testing.T) {
	out := runWebModule(t, fakeDOM+`
import { renderPipeline, pipelineModel, statusNote, PIPE_ROWS } from "gui/dash.js";
const at = new Date(Date.now() - 120000).toISOString();
const steps = (...st) => ["impl", "review", "pr", "ci", "merge"].map((name, i) => ({ name, status: st[i] || "todo" }));
const look = { icon: "◉", role: "ok", word: "run" };
const groups = [
  { parent: "+tapi", request: { id: 961 }, rows: [
    { slug: "a", branch: "fix/a", repo: "pekoe", state: "running", state_since: at, look, parent: "+tapi", origin: 961, steps: steps("done", "now") },
    { slug: "b", branch: "fix/b", repo: "pekoe", state: "running", state_since: at, look, parent: "+tapi", origin: 961, pr: 279,
      steps: steps("done", "done", "done", "now"), ci: { pass: 2, fail: 0, pending: 2 },
      merge: { ready: false, conds: [
        { name: "land", ok: true, detail: "pekoe land done (PR #279)" },
        { name: "ci", ok: false, detail: "2 pending" },
        { name: "merge_state", ok: false, detail: "merge_state is UNSTABLE" },
        { name: "settled", ok: false, detail: "agent is running" } ] } },
    { slug: "c", branch: "fix/c", repo: "pekoe", state: "done", state_since: at, look: { icon: "✓", role: "secondary", word: "done" }, parent: "+tapi", origin: 961, pr: 280,
      steps: steps("done", "done", "done", "done", "now"), merge: { ready: true, conds: [] } },
  ] },
  { parent: "+tapi", request: { id: 970 }, rows: [
    { slug: "d", branch: "fix/d", repo: "pekoe", state: "running", state_since: at, look, parent: "+tapi", origin: 961, later: [{ id: 965 }, { id: 970, now: true }], steps: steps("now") },
  ] },
  { parent: "", rows: [
    { slug: "e", branch: "hand/e", repo: "pekoe", state: "waiting", state_since: at, look: { icon: "◉", role: "warn", word: "wait" }, later: [{ id: 971, now: true }], steps: steps("now") },
  ] },
  { parent: "+tapi", request: { id: 962 }, rows: [
    { slug: "f", branch: "fix/f", repo: "pekoe", state: "running", state_since: at, look, parent: "+tapi", origin: 962, later: [{ id: 963 }, { id: 964 }, { id: 966, now: true }], steps: steps("done", "fail"), },
  ] },
];
const box = el0("div");
const opened = [];
const res = { moving: renderPipeline(box, groups, (s) => opened.push(s), {}) };
const cls = (n) => n.className;
const rowsOf = (n) => find(n, (x) => /\bpl-row\b/.test(x.className) && !/pl-head/.test(x.className));
res.order = rowsOf(box).map((r) => find(r, (x) => x.className === "pl-bname")[0].textContent);
res.groups = box.children.filter((c) => c.className === "pl-group").map((g) => [g.children.filter((c) => c.className === "pl-trunk").length, rowsOf(g).length]);
res.req = rowsOf(box).map((r) => r.children[0].textContent);
res.tree = rowsOf(box).map((r) => r.children[1].children.map(cls).join(""));
res.boxes = rowsOf(box).map((r) => find(r, (x) => /\bpl-box\b/.test(x.className)).map((b) => cls(b).replace("pl-box b-", "") + ":" + b.textContent).join(" "));
res.links = rowsOf(box).map((r) => find(r, (x) => /\bpl-link\b/.test(x.className)).map((l) => (/todo/.test(l.className) ? "-" : "=")).join(""));
res.badges = rowsOf(box).map((r) => find(r, (x) => /\bpl-badge\b/.test(x.className)).map((b) => b.textContent + (/now/.test(b.className) ? "*" : "")).join(","));
res.status = rowsOf(box).map((r) => r.children[r.children.length - 1].children.map((c) => c.textContent).join(" | "));
res.cross = find(box, (x) => x.tag === "#text" && x.text.includes("✗")).length;
find(box, (x) => x.tag === "button" && x.className === "pl-branch")[0].click();
res.opened = opened;
const many = [{ parent: "+tapi", rows: Array.from({ length: PIPE_ROWS + 3 }, (_, i) => ({ slug: "s" + i, branch: "b" + i, state: "running", state_since: at, look, origin: 1, steps: steps("now") })) }];
const mb = el0("div");
let toggled = 0;
renderPipeline(mb, many, () => {}, { expanded: false, toggle: () => toggled++ });
res.more = [rowsOf(mb).length, find(mb, (x) => x.className === "pl-more")[0].textContent];
find(mb, (x) => x.className === "pl-more")[0].click();
res.toggled = toggled;
renderPipeline(mb, many, () => {}, { expanded: true });
res.all = [rowsOf(mb).length, find(mb, (x) => x.className === "pl-more")[0].textContent];
res.behind = statusNote({ state: "running", steps: [], merge: { conds: [{ name: "ci", ok: false, detail: "2 pending" }, { name: "merge_state", ok: false, detail: "merge_state is BEHIND" }] } });
res.model = pipelineModel(groups).trees.map((t) => t.id + ":" + t.from + ":" + t.rows.length);
console.log(JSON.stringify(res));
`)
	var res struct {
		Moving, Cross, Toggled                                        int
		Behind                                                        string
		Order, Req, Tree, Boxes, Links, Badges, Status, Opened, Model []string
		Groups                                                        [][2]int
		More, All                                                     [2]any
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	want := func(what string, got, want any) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s:\n got %v\nwant %v", what, got, want)
		}
	}
	want("moving", res.Moving, 6)
	want("BEHIND is shown while CI runs (only UNSTABLE/BLOCKED say the same)", res.Behind, "merge waits on CI · merge state behind")
	want("model", res.Model, []string{"962:tapi:1", "961:tapi:4"})
	want("order", res.Order, []string{"fix/f", "fix/a", "fix/b", "fix/d", "fix/c", "hand/e"})
	want("groups", res.Groups, [][2]int{{0, 1}, {1, 4}})
	want("request column", res.Req, []string{"r962tapi · 1 branch", "r961tapi · 4 branches", "", "", "", "no request"})
	want("tree", res.Tree, []string{"pl-flat", "pl-flat", "pl-corner", "pl-corner", "pl-corner", ""})
	want("boxes", res.Boxes, []string{
		"done:✓done fail:✗fail todo:○ todo:○ todo:○",
		"done:✓done now:◉now todo:○ todo:○ todo:○",
		"done:✓done done:✓done done:✓#279 now:◉2/4 todo:○",
		"now:◉now todo:○ todo:○ todo:○ todo:○",
		"done:✓done done:✓done done:✓#280 done:✓done you:◉you",
		"you:◉you todo:○ todo:○ todo:○ todo:○",
	})
	want("links", res.Links, []string{"==---", "==---", "====-", "=----", "=====", "=----"})
	want("badges", res.Badges, []string{"◉ r966*,+2", "", "", "◉ r970*,+r965", "", "◉ r971*"})
	want("status", res.Status, []string{
		"◉ run · 2m | review failed",
		"◉ run · 2m",
		"◉ run · 2m | merge waits on CI · agent running",
		"◉ run · 2m | reopened by r970",
		"✓ done · 2m | ready · your merge",
		"◉ wait · 2m | working on r971",
	})
	want("✗ only in f's failed review box, never for a wait", res.Cross, 1)
	want("opened", res.Opened, []string{"f"})
	want("more", res.More, [2]any{8, "Show 3 more"})
	want("toggled", res.Toggled, 1)
	want("all", res.All, [2]any{11, "Show less"})
}

// fakeDOM: the little of the DOM dash.js's Overview and Requests use, for
// node. textContent replaces the children with one text node (never
// parses), so markup in the data stays text.
const fakeDOM = `
class N {
  constructor(tag) { this.tag = tag; this.children = []; this.attrs = {}; this.className = ""; this.title = ""; this.text = ""; this.on = {}; }
  append(...xs) { for (const x of xs) this.children.push(typeof x === "string" ? txtNode(x) : x); }
  replaceChildren(...xs) { this.children = []; this.append(...xs); }
  get lastChild() { return this.children[this.children.length - 1]; }
  get childNodes() { return this.children; }
  get firstChild() { return this.children[0]; }
  set textContent(s) { this.children = [txtNode(String(s))]; }
  get textContent() { return this.tag === "#text" ? this.text : this.children.map((c) => c.textContent).join(""); }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === "class") this.className = String(v); }
  addEventListener(k, f) { this.on[k] = f; }
  click() { this.on.click && this.on.click(); }
}
const txtNode = (s) => { const n = new N("#text"); n.text = s; return n; };
globalThis.document = {
  createElement: (t) => new N(t),
  createElementNS: (ns, t) => new N(t),
  createTextNode: (s) => txtNode(s),
};
const el0 = (t) => new N(t);
const find = (n, f) => [...(f(n) ? [n] : []), ...n.children.flatMap((c) => find(c, f))];
`
