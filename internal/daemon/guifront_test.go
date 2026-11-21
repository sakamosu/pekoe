package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// The confirm sheet (gui/confirm.js: New session, tapi's Reset, Interrupt)
// while OK's request runs: Cancel / Back (and Esc, the backdrop: the same
// close) do nothing and the Cancel button is off, so the form does not
// come back as if the request were called off; when the request ends the
// sheet closes once and "after" runs. A sheet opened after a run ended is
// its own: the next run's end never closes another. Runs in node (skipped
// without node).
func TestGUIConfirmWaitsForItsRequest(t *testing.T) {
	out := runWebModule(t, fakeDOM+`
const ids = {};
const node = (id) => (ids[id] ||= Object.assign(el0("div"), { hidden: true, disabled: false, classList: { toggle() {} }, focus() {} }));
node("csheet").querySelector = () => node("csheet.sheet");
globalThis.document.getElementById = node;
import { confirmSheet } from "gui/confirm.js";
let busy = false;
const log = [];
const c = confirmSheet({ setBusy: (b) => (busy = b), toast: (m) => log.push("toast:" + m), refresh: () => log.push("refresh") });
const w = { title: "New session in r", toCap: "", to: "", facts: [], warn: "", ok: "Create session" };
let release;
c.open(w, async () => {
  await new Promise((r) => (release = r));
  log.push("created");
  return () => log.push("after");
}, () => log.push("back"));
const p = c.run();
c.run(); // a second press: nothing
const during = { ok: node("csheet-ok").disabled, cancel: node("csheet-cancel").disabled };
c.close(); // Back / Esc / the backdrop while the request runs
during.hidden = node("csheet").hidden;
during.busy = busy;
during.log = [...log];
release();
await p;
const after = { hidden: node("csheet").hidden, busy, cancel: node("csheet-cancel").disabled, log: [...log] };
// the next sheet: Cancel goes back, and closing it twice goes back once
c.open({ ...w, title: "Reset tapi chat", ok: "Reset tapi" }, async () => log.push("RESET"), () => log.push("back2"));
const second = { title: node("csheet-title").textContent, hidden: node("csheet").hidden, cancel: node("csheet-cancel").disabled };
c.close();
c.close();
second.closed = node("csheet").hidden;
second.log = log.slice(after.log.length);
console.log(JSON.stringify({ during, after, second }));
`)
	var got struct {
		During struct {
			OK, Cancel, Hidden, Busy bool
			Log                      []string
		}
		After struct {
			Hidden, Busy, Cancel bool
			Log                  []string
		}
		Second struct {
			Title                  string
			Hidden, Cancel, Closed bool
			Log                    []string
		}
	}
	if err := json.Unmarshal(nodeJSON(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !got.During.OK || !got.During.Cancel || got.During.Hidden || !got.During.Busy || len(got.During.Log) != 0 {
		t.Errorf("while the request runs (OK and Cancel off, the sheet up, busy, nothing done): %+v", got.During)
	}
	if !got.After.Hidden || got.After.Busy || got.After.Cancel || strings.Join(got.After.Log, ",") != "created,after,refresh" {
		t.Errorf("when the request ends (closed once, not back to the form): %+v", got.After)
	}
	if got.Second.Title != "Reset tapi chat" || got.Second.Hidden || got.Second.Cancel || !got.Second.Closed || strings.Join(got.Second.Log, ",") != "back2" {
		t.Errorf("the next sheet: %+v", got.Second)
	}
	// the page keeps no sheet of its own: every confirm goes through confirm.js
	js, _ := webFiles.ReadFile("webui/gui/gui.js")
	if !strings.Contains(string(js), "confirmSheet({") || strings.Contains(string(js), "let csheet") {
		t.Error("gui.js does not leave the confirm sheet to confirm.js")
	}
}

// nodeJSON: the last non-empty line of node's output (the JSON a test
// printed).
func nodeJSON(out []byte) []byte {
	return []byte(lastLine(string(out)))
}

// The Overview's days (gui/dash.js mergeDays) in a time zone whose DST
// starts at 0:00 (America/Santiago, the first Sunday of September: that
// day has no 0:00): every day from … to is there, today last. Runs in
// node with TZ set (skipped without node).
func TestGUIMergeDaysDST(t *testing.T) {
	t.Setenv("TZ", "America/Santiago")
	out := runWebModule(t, `
import { mergeDays } from "gui/dash.js";
const res = [["2026-09-01", "2026-09-07"], ["2026-09-03", "2026-09-09"], ["2026-09-07", "2026-09-13"], ["2026-03-30", "2026-04-05"]].map(([from, to]) => {
  const d = mergeDays({ from, to, rows: [{ day: to, merged: 3 }], merged: 3 });
  return [d.length, d[0].day, d.at(-1).day, d.at(-1).today, d.at(-1).merged];
});
console.log(new Date(2026, 8, 6).getHours(), JSON.stringify(res));
`)
	s := strings.TrimSpace(string(out))
	if !strings.HasPrefix(s, "1 ") {
		t.Skipf("node's time zone is not America/Santiago (no 0:00 DST jump): %s", s)
	}
	want := `[[7,"2026-09-01","2026-09-07",true,3],[7,"2026-09-03","2026-09-09",true,3],[7,"2026-09-07","2026-09-13",true,3],[7,"2026-03-30","2026-04-05",true,3]]`
	if got := strings.TrimPrefix(s, "1 "); got != want {
		t.Errorf("mergeDays across a 0:00 DST jump:\n got %s\nwant %s", got, want)
	}
}

// A double click on a grip gives back the default (docs/gui.md
// 「サイドバーのビュー」) even while the window is too narrow to show it:
// the default is kept (the storage forgets the width), shown clamped, and
// once the window widens the pane is at its default again. Runs in node
// (skipped without node).
func TestGUIPaneDblclickNarrow(t *testing.T) {
	out := runWebModule(t, paneFake+`
const res = {};
for (const [name, o, narrow] of [["tapi", TAPI_PANE, 1250 - 440], ["side", SIDE_PANE, 770]]) {
  m.set(o.key, "600");
  let room = narrow;
  const p = page(o, () => room);
  p.grip.fire("dblclick");
  const dbl = p.at();
  room = 1600;
  p.w.apply();
  res[name] = { dbl, wide: p.at() };
}
console.log(JSON.stringify(res));
`)
	var got map[string]struct{ Dbl, Wide []*string }
	if err := json.Unmarshal(nodeJSON(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	str := func(p *string) string {
		if p == nil {
			return "<none>"
		}
		return *p
	}
	for name, want := range map[string][2]string{"tapi": {"330px", "340px"}, "side": {"290px", "300px"}} {
		g := got[name]
		if len(g.Dbl) != 4 || len(g.Wide) != 4 {
			t.Fatalf("%s: %s", name, out)
		}
		if str(g.Dbl[0]) != want[0] || g.Dbl[3] != nil {
			t.Errorf("%s after a double click in a narrow window: shown %s, kept %s (want %s shown, nothing kept)", name, str(g.Dbl[0]), str(g.Dbl[3]), want[0])
		}
		if str(g.Wide[0]) != want[1] || g.Wide[3] != nil {
			t.Errorf("%s once the window widens: shown %s, kept %s (want the default %s)", name, str(g.Wide[0]), str(g.Wide[3]), want[1])
		}
	}
}

// Off the Dashboard, the page still reads /api/overview with the list
// (every 3 s): the sidebar's "Ready to merge", Needs you, a session's
// header and LINKED come from it, and a session merged (or made ready)
// while a session's screen is open must not keep its old mark until the
// Dashboard is shown again.
func TestGUIOverviewOffDashboard(t *testing.T) {
	js, _ := webFiles.ReadFile("webui/gui/gui.js")
	src := string(js)
	i := strings.Index(src, "async function refresh() {")
	if i < 0 {
		t.Fatal("gui.js has no refresh()")
	}
	body := src[i:]
	body = body[:strings.Index(body, "\n}\n")]
	for _, want := range []string{
		`view === "dashboard" ? null : api("/api/overview")`,
		`else if (o) overview = o;`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("refresh() lacks %q: off the Dashboard the overview is not read again", want)
		}
	}
}

// The local GUI's sign-in (lib/api.js): a request sent while the one-time
// token's exchange is on its way (a refresh when the tab comes to the
// front) waits for it and goes with the session token, so the tab is not
// signed out by a 401 though the exchange succeeds. Runs in node (skipped
// without node).
func TestGUISignInRace(t *testing.T) {
	out := runWebModule(t, `
const store = new Map();
globalThis.sessionStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v), removeItem: (k) => store.delete(k) };
globalThis.location = { hash: "#t=" + "a".repeat(64), pathname: "/", search: "" };
globalThis.history = { replaceState() {} };
const banner = { hidden: true, textContent: "" };
globalThis.document = { getElementById: () => banner };
let release;
const sent = [];
globalThis.fetch = (path, opt) => {
  if (path === "/api/gui/session") return new Promise((r) => (release = () => r({ ok: true, status: 200, json: async () => ({ token: "s".repeat(64) }) })));
  const auth = opt.headers && opt.headers.Authorization;
  sent.push(path + " " + (auth ? "bearer" : "none"));
  return Promise.resolve(auth === "Bearer " + "s".repeat(64)
    ? { ok: true, status: 200, json: async () => ({ ok: 1 }), text: async () => "" }
    : { ok: false, status: 401, statusText: "Unauthorized", text: async () => "Unauthorized" });
};
import { guiSignIn, api } from "lib/api.js";
const signing = guiSignIn();
const during = api("/api/sessions").then(() => "ok", (e) => e.message);
await new Promise((r) => setTimeout(r, 10));
release();
await signing;
const res = { during: await during, next: await api("/api/tapi").then(() => "ok", (e) => e.message), banner: !banner.hidden, sent };
console.log(JSON.stringify(res));
`)
	var got struct {
		During, Next string
		Banner       bool
		Sent         []string
	}
	if err := json.Unmarshal(nodeJSON(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.During != "ok" || got.Next != "ok" || got.Banner || strings.Join(got.Sent, ",") != "/api/sessions bearer,/api/tapi bearer" {
		t.Errorf("a request during the sign-in: %+v", got)
	}
}
