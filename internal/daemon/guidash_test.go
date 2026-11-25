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

// The Pipeline by request (docs/gui-design.md「Pipeline」): a request's
// heading line (rN, its words from /api/overview's requests, "tapi · N
// branches · status") over its branches, the newest request on top, the
// branches no request made in one "no request" group after them; a later request as a badge (◉
// while it is at work, + once done) and "reopened by" when it sent the
// branch back to impl; the step boxes in their looks (CI as passed/all,
// the person's merge as you); waits written dim in one line, never as ✗;
// inside a request, the rows in the overview's order of states.
func TestGUIPipelineRender(t *testing.T) {
	out := runWebModule(t, fakeDOM+`
import { renderPipeline, pipelineModel, pastRequest, statusNote, stepLook, PIPE_ROWS } from "gui/dash.js";
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
    { slug: "e", branch: "hand/e", repo: "dotfiles", state: "waiting", state_since: at, look: { icon: "◉", role: "warn", word: "wait" }, later: [{ id: 971, now: true }], steps: steps("now") },
  ] },
  { parent: "+tapi", request: { id: 962 }, rows: [
    { slug: "f", branch: "fix/f", repo: "pekoe", state: "running", state_since: at, look, parent: "+tapi", origin: 962, later: [{ id: 963 }, { id: 964 }, { id: 966, now: true }], steps: steps("done", "fail"), },
  ] },
];
const box = el0("div");
const opened = [];
const requests = [{ id: 961, text: "CSV を直して <b>x</b>", status: "open" }, { id: 962, text: "", status: "done" }];
const res = { moving: renderPipeline(box, groups, (s) => opened.push(s), {}, requests) };
const cls = (n) => n.className;
const rowsOf = (n) => find(n, (x) => /\bpl-row\b/.test(x.className) && !/pl-head/.test(x.className));
res.order = rowsOf(box).map((r) => find(r, (x) => x.className === "pl-bname")[0].textContent);
const heads = (n) => find(n, (x) => x.className === "pl-rhead").map((h) => h.children[0].children.map((c) => c.className + ":" + c.textContent + (c.title ? "|" + c.title : "")).join(" "));
res.groups = box.children.filter((c) => /\bpl-group\b/.test(c.className)).map((g) => [g.className, rowsOf(g).length]);
// the Branch box: "<repo> · <branch>" (repo dim), then the badges; the title keeps the whole
const label = (n) => find(n, (x) => x.className === "pl-label")[0].children.map((c) => c.className + ":" + c.textContent).join(" ");
// two badges or more: the full ones (pl-full) and one folded badge (pl-compact) gui.css shows when the box is narrow
res.compact = rowsOf(box).map((r) => find(r, (x) => /\bpl-badge\b/.test(x.className)).map((b) => b.className.replace("pl-badge", "").trim() + ":" + b.textContent).join(","));
res.label = rowsOf(box).map((r) => label(find(r, (x) => x.className === "pl-branch")[0]));
res.title = rowsOf(box).map((r) => find(r, (x) => x.className === "pl-branch")[0].title);
const nr = el0("div");
renderPipeline(nr, [{ rows: [{ slug: "n", branch: "n", state: "running", look, steps: steps("now") }] }], () => {});
res.label.push(label(find(nr, (x) => x.className === "pl-branch")[0]));
res.req = heads(box);
res.colspan = find(box, (x) => x.className === "pl-rcell").map((c) => c.attrs.role + ":" + c.attrs["aria-colspan"]);
res.boxes = rowsOf(box).map((r) => find(r, (x) => /\bpl-box\b/.test(x.className)).map((b) => cls(b).replace("pl-box b-", "") + ":" + b.textContent).join(" "));
res.links = rowsOf(box).map((r) => find(r, (x) => /\bpl-link\b/.test(x.className)).map((l) => (/todo/.test(l.className) ? "-" : "=")).join(""));
res.badges = rowsOf(box).map((r) => find(r, (x) => /\bpl-badge\b/.test(x.className) && !/pl-compact/.test(x.className)).map((b) => b.textContent + (/now/.test(b.className) ? "*" : "")).join(","));
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
renderPipeline(mb, many, () => {}, {});
res.reqCut = heads(mb);
const two = [
  { parent: "+tapi", rows: Array.from({ length: PIPE_ROWS - 1 }, (_, i) => ({ slug: "p" + i, branch: "p" + i, state: "running", look, origin: 2, steps: steps("now") })) },
  { parent: "+tapi", rows: Array.from({ length: 4 }, (_, i) => ({ slug: "q" + i, branch: "q" + i, state: "running", look, origin: 1, steps: steps("now") })) },
];
renderPipeline(mb, two, () => {}, {});
res.reqOne = heads(mb);
renderPipeline(mb, many, () => {}, { expanded: true });
res.reqAll = heads(mb);
// the header has as many columns (columnheader) as a body row has cells,
// and a request's heading spans them all
const head = find(box, (x) => /pl-head/.test(x.className))[0];
res.head = head.children.map((c) => (c.attrs.role || "-") + (c.attrs["aria-hidden"] ? ":hidden" : "")).join(" ");
res.cols = [head.children.filter((c) => c.attrs.role === "columnheader").length, ...rowsOf(box).map((r) => r.children.filter((c) => c.attrs.role === "cell").length)];
res.noCI = [
  statusNote({ state: "done", steps: [], merge: { conds: [{ name: "ci", ok: true, detail: "no CI checks (not required)" }, { name: "merge_state", ok: false, detail: "merge_state is BLOCKED" }] } }),
  statusNote({ state: "done", steps: [], merge: { conds: [{ name: "ci", ok: true, detail: "no CI checks (not required)" }, { name: "review", ok: false, detail: "no review" }] } }),
  statusNote({ state: "done", steps: [], merge: { ready: true, conds: [{ name: "ci", ok: true, detail: "no CI checks (not required)" }] } }),
  stepLook({ state: "done", steps: [{ name: "ci", status: "todo", detail: "no CI checks (not required)" }] }, "ci").join(""),
];
// waiting: the step it is on is the person's turn, except CI (GitHub's)
const wait = (st, ci) => { const wb = el0("div"); renderPipeline(wb, [{ rows: [{ slug: "w", branch: "w", state: "waiting", look, steps: st, ci }] }], () => {}); return find(wb, (x) => /\bpl-box\b/.test(x.className)).map((b) => cls(b).replace("pl-box b-", "") + ":" + b.textContent).join(" "); };
res.wait = [wait(steps("done", "now")), wait(steps("done", "done", "now")), wait(steps("done", "done", "done", "now"), { pass: 1, fail: 0, pending: 1 }), wait(steps("done", "done", "done", "now"))];
res.behind = statusNote({ state: "running", steps: [], merge: { conds: [{ name: "ci", ok: false, detail: "2 pending" }, { name: "merge_state", ok: false, detail: "merge_state is BEHIND" }] } });
const past = (status, ...rows) => pastRequest({ status, rows: rows.map((r) => ({ state: r[0], merge: r[1] })) });
res.past = [
  "all landed:" + past("open", ["landed"], ["landed"]),
  "open:" + past("open", ["done"]),
  "done, merge to do:" + past("done", ["done", { ready: true }]),
  "done, waiting:" + past("done", ["waiting"]),
  "done, idle:" + past("done", ["idle"], ["landed"]),
  "done, failed:" + past("dropped", ["failed"]),
  "gone:" + past("gone", ["idle"]),
];
const lb = el0("div");
renderPipeline(lb, [{ parent: "+tapi", rows: [{ slug: "z", branch: "z", state: "landed", look, origin: 5, steps: steps("done", "done", "done", "done", "done") }] }], () => {}, {}, [{ id: 5, text: "old", status: "done" }]);
res.groups.push(...lb.children.filter((c) => /\bpl-group\b/.test(c.className)).map((g) => [g.className, rowsOf(g).length]));
res.model = pipelineModel(groups).trees.map((t) => t.id + ":" + t.from + ":" + t.rows.length);
console.log(JSON.stringify(res));
`)
	var res struct {
		Moving, Cross, Toggled                                  int
		Behind, Head                                            string
		ReqCut, ReqOne, ReqAll, NoCI, Wait                      []string
		Cols                                                    []int
		Order, Req, Boxes, Links, Badges, Status, Opened, Model []string
		Label, Title, Compact                                   []string
		Groups                                                  [][2]any
		Colspan, Past                                           []string
		More, All                                               [2]any
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
	want("branch box: repo · branch, no repo no separator", res.Label, []string{
		"pl-repo:pekoe pl-sep:· pl-bname:fix/f", "pl-repo:pekoe pl-sep:· pl-bname:fix/a", "pl-repo:pekoe pl-sep:· pl-bname:fix/b",
		"pl-repo:pekoe pl-sep:· pl-bname:fix/d", "pl-repo:pekoe pl-sep:· pl-bname:fix/c", "pl-repo:dotfiles pl-sep:· pl-bname:hand/e",
		"pl-bname:n",
	})
	want("badges: full and folded when two or more", res.Compact, []string{
		"now pl-full:◉ r966,pl-full:+2,now pl-compact:◉ r966 +2", "", "", "now pl-full:◉ r970,pl-full:+r965,now pl-compact:◉ r970 +1", "", "now:◉ r971",
	})
	want("branch box title", res.Title, []string{"fix/f · pekoe · f", "fix/a · pekoe · a", "fix/b · pekoe · b", "fix/d · pekoe · d", "fix/c · pekoe · c", "hand/e · dotfiles · e"})
	want("groups", res.Groups, [][2]any{{"pl-group", 1}, {"pl-group", 4}, {"pl-group", 1}, {"pl-group past", 1}})
	want("request headings", res.Req, []string{
		"pl-rid:r962 pl-rtext: pl-rmeta:tapi · 1 branch · done",
		"pl-rid:r961 pl-rtext:CSV を直して <b>x</b>|CSV を直して <b>x</b> pl-rmeta:tapi · 4 branches · open",
		"pl-rnone:no request pl-rtext: pl-rmeta:1 branch",
	})
	want("a heading is one cell over every column", res.Colspan, []string{"cell:7", "cell:7", "cell:7"})
	want("faded: every branch landed, or closed with nothing left", res.Past, []string{"all landed:true", "open:false", "done, merge to do:false", "done, waiting:false", "done, idle:true", "done, failed:false", "gone:false"})
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
	want("a request cut by Show more: shown of all", res.ReqCut, []string{"pl-rid:r1 pl-rtext: pl-rmeta:tapi · 8 of 11 branches"})
	want("one row of a request shown", res.ReqOne, []string{"pl-rid:r2 pl-rtext: pl-rmeta:tapi · 7 branches", "pl-rid:r1 pl-rtext: pl-rmeta:tapi · 1 of 4 branches"})
	want("expanded: all of them", res.ReqAll, []string{"pl-rid:r1 pl-rtext: pl-rmeta:tapi · 11 branches"})
	want("header: one columnheader a column", res.Head, "columnheader columnheader columnheader columnheader columnheader columnheader columnheader")
	want("header and body rows have as many columns", res.Cols, []int{7, 7, 7, 7, 7, 7, 7})
	want("no CI checks is met (not required), never a wait", res.NoCI, []string{"merge waits on merge state blocked", "merge waits on review", "ready · your merge", "todo○n/a"})
	want("waiting: the step it is on is you, never CI", res.Wait, []string{
		"done:✓done you:◉you todo:○ todo:○ todo:○",
		"done:✓done done:✓done you:◉you todo:○ todo:○",
		"done:✓done done:✓done done:✓done now:◉1/2 todo:○",
		"done:✓done done:✓done done:✓done now:◉now todo:○",
	})
}

// A branch sent back to work after its PR (webinbox.go's reopenOf), as
// the Pipeline draws what pekoed decided: (a) impl now, the PR's box to do
// with its number kept, CI and the merge to do whatever the old checks
// said, "reopened" (by the later request when one is tied to it) and not
// what the merge waits on; (b) nothing changed: the merge is yours again;
// (c) work the PR lacks: impl done, review to do; (d) the review asked
// again is now.
func TestGUIPipelineReopen(t *testing.T) {
	out := runWebModule(t, fakeDOM+`
import { renderPipeline } from "gui/dash.js";
const at = new Date(Date.now() - 120000).toISOString();
const steps = (...st) => ["impl", "review", "pr", "ci", "merge"].map((name, i) => ({ name, status: st[i] || "todo" }));
const look = { icon: "◉", role: "ok", word: "run" };
const busy = { ready: false, conds: [{ name: "settled", ok: false, detail: "agent is running" }] };
const lacks = { ready: false, conds: [{ name: "settled", ok: false, detail: "HEAD has 1 commit not in PR #289 (run pekoe land)" }] };
const row = (slug, o) => ({ slug, branch: slug, state: "running", state_since: at, look, pr: 289, ci: { pass: 3, fail: 0, pending: 0 }, ...o });
const rows = [
  row("a", { reopened: true, steps: steps("now"), merge: busy }),
  row("a2", { reopened: true, later: [{ id: 975, now: true }], steps: steps("now"), merge: busy }),
  row("aw", { state: "waiting", reopened: true, steps: steps("now"), merge: busy }),
  row("b", { state: "done", steps: steps("done", "done", "done", "done", "now"), merge: { ready: true, conds: [] } }),
  row("c", { state: "done", steps: steps("done"), merge: lacks }),
  row("d", { steps: steps("done", "now"), merge: lacks }),
];
const box = el0("div");
renderPipeline(box, [{ parent: "", rows }], () => {}, { expanded: true });
const rowsOf = (n) => find(n, (x) => /\bpl-row\b/.test(x.className) && !/pl-head/.test(x.className));
const res = {};
res.boxes = rowsOf(box).map((r) => find(r, (x) => /\bpl-box\b/.test(x.className)).map((b) => b.className.replace("pl-box b-", "") + ":" + b.textContent).join(" "));
res.status = rowsOf(box).map((r) => (r.children[r.children.length - 1].children[1] || { textContent: "" }).textContent);
console.log(JSON.stringify(res));
`)
	var res struct{ Boxes, Status []string }
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	want := func(what string, got, want any) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s:\n got %v\nwant %v", what, got, want)
		}
	}
	want("boxes", res.Boxes, []string{
		"now:◉now todo:○ todo:○#289 todo:○ todo:○",
		"now:◉now todo:○ todo:○#289 todo:○ todo:○",
		"you:◉you todo:○ todo:○#289 todo:○ todo:○",
		"done:✓done done:✓done done:✓#289 done:✓done you:◉you",
		"done:✓done todo:○ todo:○#289 todo:○ todo:○",
		"done:✓done now:◉now todo:○#289 todo:○ todo:○",
	})
	want("status", res.Status, []string{
		"reopened",
		"reopened by r975",
		"reopened",
		"ready · your merge",
		"merge waits on HEAD has 1 commit not in PR #289 (run pekoe land)",
		"merge waits on HEAD has 1 commit not in PR #289 (run pekoe land)",
	})
}

// A PR's number is a link to the PR when pekoed sent its page (pr_url):
// the Pipeline's PR box (✓ #4, the box itself does nothing on a click),
// the session list's row, the details' PR row and the merges; text
// without one. The link opens in a new tab and keeps its click from the
// row around it (its keys go on to the page's own keys), which acts as a button (asButton) on a click,
// Enter or Space on the row itself.
func TestGUIPRLinks(t *testing.T) {
	out := runWebModule(t, fakeDOM+`
import { renderPipeline } from "gui/dash.js";
import { prLink, asButton } from "lib/dom.js";
import { mergePRURL } from "lib/inbox.js";
const steps = (...st) => ["impl", "review", "pr", "ci", "merge"].map((name, i) => ({ name, status: st[i] || "todo" }));
const look = { icon: "◉", role: "ok", word: "run" };
const url = "https://github.com/me/app/pull/4";
const box = el0("div");
renderPipeline(box, [{ parent: "", rows: [
  { slug: "a", branch: "a", state: "running", look, pr: 4, pr_url: url, steps: steps("done", "done", "done", "now") },
  { slug: "b", branch: "b", state: "running", look, pr: 5, steps: steps("done", "done", "done", "now") },
  { slug: "c", branch: "c", state: "running", look, pr: 6, pr_url: "https://github.com/me/app/pull/6", reopened: true, steps: steps("now") },
] }], () => {}, { expanded: true });
const prBox = (slug) => find(box, (x) => /\bpl-row\b/.test(x.className) && find(x, (y) => y.className === "pl-bname" && y.textContent === slug).length)
  .flatMap((r) => find(r, (x) => /\bpl-box\b/.test(x.className)))[2];
const word = (b) => b.children[1];
const res = {};
res.boxes = ["a", "b", "c"].map((s) => { const w = word(prBox(s)); return [w.tag, w.className, w.textContent, w.href || "", w.target || "", w.rel || ""].join("|"); });
res.box = prBox("a").textContent;
let stopped = 0;
const ev = () => ({ stopPropagation: () => stopped++ });
const a = word(prBox("a"));
a.on.click(ev());
res.stopped = stopped;
res.keyHandler = "keydown" in a.on;
res.none = [prLink("", "#7").tag, prLink("", "#7").text, prLink("javascript:alert(1)", "#7").tag];
const row = el0("div");
let went = 0;
asButton(row, () => went++);
res.row = [row.attrs.role, row.tabIndex];
const key = (target, k) => { let pd = 0; row.on.keydown({ target, key: k, preventDefault: () => pd++ }); return pd; };
res.keys = [key(row, "Enter"), key(row, " "), key(row, "a"), key(a, "Enter")];
row.click();
res.went = went;
res.merge = [
  mergePRURL({ facts: { pr: 4, pr_url: url }, answer: { merge: { pr: 4 } } }),
  mergePRURL({ facts: { pr: 3, pr_url: url }, answer: { merge: { pr: 4 } } }),
  mergePRURL({ answer: { merge: { pr: 4 } } }),
];
console.log(JSON.stringify(res));
`)
	var res struct {
		Boxes, None, Merge []string
		Box                string
		Stopped, Went      int
		KeyHandler         bool
		Row                []any
		Keys               []int
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
	want("the PR box's number", res.Boxes, []string{
		"a|lnk pl-word|#4|https://github.com/me/app/pull/4|_blank|noopener noreferrer",
		"span|pl-word|#5|||",
		"a|lnk pl-word|#6|https://github.com/me/app/pull/6|_blank|noopener noreferrer",
	})
	want("the box", res.Box, "✓#4")
	want("the link keeps its click", res.Stopped, 1)
	want("the link's keys go on to the page (the GUI's keys)", res.KeyHandler, false)
	want("no URL, or not http(s): text", res.None, []string{"#text", "#7", "span"})
	want("a row acting as a button", res.Row, []any{"button", 0})
	want("Enter and Space on the row press it; other keys and keys on the link do not", res.Keys, []int{1, 1, 0, 0})
	want("pressed", res.Went, 3)
	want("the merged PR's page from the facts about it", res.Merge, []string{"https://github.com/me/app/pull/4", "", ""})
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

// The inbox card's merge gates (docs/gui-design.md「Inbox」): one bar a
// condition (met ok, missing err) with the count in the label, over the
// table of them one line each; the CI bar one segment a check (passed,
// failed, pending in that order) with the counts beside it, and words
// with no bar when there are no checks.
func TestGUIMergeGates(t *testing.T) {
	out := runWebModule(t, fakeDOM+`
import { mergeGates, ciBar } from "gui/dash.js";
const conds = [
  { name: "pr", ok: true, detail: "PR #42 is OPEN" },
  { name: "ci", ok: false, detail: "1 failed" },
  { name: "review", ok: true, detail: "independent review passed at 3f2a9c1" },
  { name: "merge_state", ok: true, detail: "CLEAN" },
  { name: "conflicts", ok: true, detail: "no overlaps" },
  { name: "settled", ok: false, detail: "agent is running" },
];
const mg = mergeGates({ conds });
const bars = find(mg, (x) => x.className === "mg-bars")[0];
const res = {
  cap: mg.children[0].textContent,
  bars: bars.children.map((b) => b.className + "|" + b.title),
  label: bars.attrs["aria-label"] + "|" + bars.attrs.role,
  table: find(mg, (x) => x.className === "gate").map((g) => g.children.map((c) => c.textContent).join(" ")),
  none: String(mergeGates({ conds: [] })) + " " + String(mergeGates(null)),
};
const ci = ciBar({ pass: 3, fail: 1, pending: 2 });
res.ci = [ci.children[0].textContent, ...find(ci, (x) => /\bci-seg\b/.test(x.className)).map((s) => s.className.replace("ci-seg ", ""))];
res.ciLabel = find(ci, (x) => x.className === "ci-bars")[0].attrs["aria-label"];
const no = ciBar({ pass: 0, fail: 0, pending: 0 });
res.noCI = [no.textContent, find(no, (x) => x.className === "ci-bars").length];
console.log(JSON.stringify(res));
`)
	var res struct {
		Cap, Label, None, CILabel string
		Bars, Table, CI           []string
		NoCI                      []any
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
	want("caption", res.Cap, "Merge gates · 4/6")
	want("bars", res.Bars, []string{
		"mg-bar ok|pr: PR #42 is OPEN", "mg-bar fail|ci: 1 failed", "mg-bar ok|review: independent review passed at 3f2a9c1",
		"mg-bar ok|merge state: CLEAN", "mg-bar ok|conflicts: no overlaps", "mg-bar fail|settled: agent is running",
	})
	want("bars' label", res.Label, "Merge gates: 4 of 6 met, missing ci, settled|img")
	want("table", res.Table, []string{
		"✓ pr PR #42 is OPEN", "✗ ci 1 failed", "✓ review independent review passed at 3f2a9c1",
		"✓ merge state CLEAN", "✓ conflicts no overlaps", "✗ settled agent is running",
	})
	want("no conditions, no gates", res.None, "null null")
	want("ci", res.CI, []string{"CI3/6 passed · 1 failed · 2 pending", "pass", "pass", "pass", "fail", "pending", "pending"})
	want("ci's label", res.CILabel, "CI: 3 passed, 1 failed, 2 pending")
	want("no checks: words, no bar", res.NoCI, []any{"CIno checks", 0})
}
