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

// busyLines: Claude Code's screen in the middle of a turn.
var busyLines = []string{"⏺ Bash(pnpm test)", "  ⎿  Running…", "", "✻ Working… (12s · esc to interrupt)", "", "> "}

func showBusy(fe *fakeEngine, busy bool) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if busy {
		fe.lines = busyLines
	} else {
		fe.lines = []string{"  ⎿  Interrupted · What should Claude do instead?", "", "> "}
	}
}

// runningSession opens "main" and puts its agent to work (a prompt went in,
// its screen shows the spinner).
func runningSession(t *testing.T, d *Daemon, repo string) {
	t.Helper()
	showBusy(d.Eng.(*fakeEngine), true)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "build it"})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.State != state.Running {
		t.Fatalf("not running: %+v", s.State)
	}
}

func escapes(fe *fakeEngine, from int) int {
	n := 0
	for _, s := range sentSince(fe, from) {
		if s == "keys:Escape" {
			n++
		}
	}
	return n
}

// session.interrupt and tapi.interrupt are a person's: over the socket
// without the human token (or with a wrong one) they are refused before
// any key goes out, and with it they send one Escape.
func TestInterruptNeedsToken(t *testing.T) {
	d, fe, repo := setup(t)
	runningSession(t, d, repo)
	call[Tapi](t, d, "tapi.open", nil)
	event(t, d, "UserPromptSubmit", TapiSlug, map[string]any{"prompt": "hi"})
	c := dialPipe(t, d)
	before := sentCount(fe)
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"session.interrupt", map[string]any{"slug": "main"}},
		{"tapi.interrupt", nil},
	} {
		for _, tok := range []string{"", "wrong-" + d.humanToken} {
			if e := c.call(tc.method, tc.params, tok); !strings.Contains(e, "only a person can do this") {
				t.Errorf("%s with token %q: %q, want the human-only refusal", tc.method, tok, e)
			}
		}
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("refused calls reached the engine: %v", got)
	}
	if e := c.call("session.interrupt", map[string]any{"slug": "main"}, d.humanToken); e != "" {
		t.Fatalf("with the token: %s", e)
	}
	if e := c.call("tapi.interrupt", nil, d.humanToken); e != "" {
		t.Fatalf("tapi with the token: %s", e)
	}
	if n := escapes(fe, before); n != 2 {
		t.Fatalf("escapes sent: %d (%v)", n, sentSince(fe, before))
	}
}

// The GUI's Interrupt: one Escape while the agent works; refused (409,
// nothing typed) while a permission prompt is up (by the hooks or only on
// the screen), on a question, when idle, and a second time within
// interruptEvery. Without a session token 401; the phone has no route.
func TestGUIInterrupt(t *testing.T) {
	d, fe, repo, h, _ := guiSetup(t)
	runningSession(t, d, repo)
	path := "/api/sessions/main/interrupt"
	if w := doGUI(t, h, guiReq{method: "POST", path: path, body: map[string]any{}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("without a bearer: %d", w.Code)
	}
	ph := testWebHandler(d)
	before := sentCount(fe)
	if w := doWeb(t, ph, webReq{method: "POST", path: path, from: ipPhone, body: map[string]any{}}); w.Code == 200 {
		t.Fatalf("the phone interrupted: %d %s", w.Code, w.Body)
	}
	if n := escapes(fe, before); n != 0 {
		t.Fatalf("refused interrupts typed: %v", sentSince(fe, before))
	}
	s := guiSignIn(t, d, h)
	w := doGUI(t, h, guiReq{method: "POST", path: path, bearer: s, body: map[string]any{}})
	if w.Code != 200 {
		t.Fatalf("interrupt: %d %s", w.Code, w.Body)
	}
	if got := sentSince(fe, before); len(got) != 1 || got[0] != "keys:Escape" {
		t.Fatalf("typed: %v, want one Escape", got)
	}
	// a double press: the second Escape would open the rewind menu
	before = sentCount(fe)
	if w := doGUI(t, h, guiReq{method: "POST", path: path, bearer: s, body: map[string]any{}}); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "interrupted") {
		t.Fatalf("again at once: %d %s", w.Code, w.Body)
	}
	refused := func(why string) {
		t.Helper()
		d.interrupted = interrupts{}
		n := sentCount(fe)
		if w := doGUI(t, h, guiReq{method: "POST", path: path, bearer: s, body: map[string]any{}}); w.Code != http.StatusConflict {
			t.Fatalf("%s: %d %s", why, w.Code, w.Body)
		}
		if got := sentSince(fe, n); len(got) != 0 {
			t.Fatalf("%s: typed %v", why, got)
		}
	}
	// Claude Code sends no Stop for a turn interrupted with esc: the hooks
	// still say running, the screen says the turn is over. A second Escape
	// after the cooldown would clear the input box (two: the rewind menu)
	showBusy(fe, false)
	if st := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}).State; st != state.Running {
		t.Fatalf("state %s, want still running", st)
	}
	refused("interrupted, no Stop, the spinner gone")
	// a menu with "esc to cancel" is not a turn
	fe.mu.Lock()
	fe.lines = []string{"Select a model", "❯ 1. Opus", "  2. Sonnet", "Enter to select · esc to cancel"}
	fe.mu.Unlock()
	refused("a menu on the screen")
	// a permission prompt on the screen the hooks have not reported yet
	fe.mu.Lock()
	fe.lines = []string{"✻ Working… (esc to interrupt)", "Bash(rm -rf x)", "Do you want to proceed?", "❯ 1. Yes", "  2. No"}
	fe.mu.Unlock()
	refused("permission menu on the screen")
	showBusy(fe, true)
	// the hooks' permission prompt
	event(t, d, "PreToolUse", "main", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "pnpm migrate"}})
	event(t, d, "Notification", "main", map[string]any{"notification_type": "permission_prompt", "message": "needs permission"})
	refused("waiting for a permission")
	// a question: Escape would back out of its menu (the keypad has esc)
	event(t, d, "PreToolUse", "main", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"question": "which db?"}})
	refused("a question")
	// stop waits for onStop (its git in the worktree): the temp dir goes
	// when the test ends
	stop(t, d, "main", map[string]any{})
	if st := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}).State; st == state.Running {
		t.Fatalf("still running after Stop")
	}
	refused("not working")
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions/nope/interrupt", bearer: s, body: map[string]any{}}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown session: %d", w.Code)
	}
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions/" + TapiSlug + "/interrupt", bearer: s, body: map[string]any{}}); w.Code != http.StatusNotFound {
		t.Fatalf("+tapi as a session: %d", w.Code)
	}
	// the cooldown is the agent's: it passes
	d.interrupted = interrupts{last: map[string]time.Time{"main": time.Now().Add(-interruptEvery)}}
	showBusy(fe, true)
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "go on"})
	if w := doGUI(t, h, guiReq{method: "POST", path: path, bearer: s, body: map[string]any{}}); w.Code != 200 {
		t.Fatalf("after the cooldown: %d %s", w.Code, w.Body)
	}
}

// tapi's Interrupt and Reset on the GUI: the interrupt as a session's (only
// while it works); the reset closes tapi's window and keeps the requests
// and the inbox. Neither is on the phone's front door.
func TestGUITapiInterruptAndReset(t *testing.T) {
	d, fe, _, h, _ := guiSetup(t)
	s := guiSignIn(t, d, h)
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/tapi/interrupt", bearer: s, body: map[string]any{}}); w.Code != http.StatusConflict {
		t.Fatalf("interrupt before tapi.open: %d %s", w.Code, w.Body)
	}
	call[Tapi](t, d, "tapi.open", nil)
	showBusy(fe, true)
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/tapi/interrupt", bearer: s, body: map[string]any{}}); w.Code != http.StatusConflict {
		t.Fatalf("interrupt an idle tapi: %d %s", w.Code, w.Body)
	}
	event(t, d, "UserPromptSubmit", TapiSlug, map[string]any{"prompt": "hi"})
	ph := testWebHandler(d)
	before := sentCount(fe)
	for _, p := range []string{"/api/tapi/interrupt", "/api/tapi/reset"} {
		if w := doWeb(t, ph, webReq{method: "POST", path: p, from: ipPhone, body: map[string]any{}}); w.Code == 200 {
			t.Fatalf("the phone: %s %d", p, w.Code)
		}
		if w := doGUI(t, h, guiReq{method: "POST", path: p, body: map[string]any{}}); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s without a bearer: %d", p, w.Code)
		}
	}
	if !d.tapiStarted() || sentCount(fe) != before {
		t.Fatalf("refused calls acted: started %v, typed %v", d.tapiStarted(), sentSince(fe, before))
	}
	w := doGUI(t, h, guiReq{method: "POST", path: "/api/tapi/interrupt", bearer: s, body: map[string]any{}})
	if w.Code != 200 {
		t.Fatalf("tapi interrupt: %d %s", w.Code, w.Body)
	}
	if got := sentSince(fe, before); len(got) != 1 || got[0] != "keys:Escape" {
		t.Fatalf("typed: %v", got)
	}

	e := &state.LedgerEntry{At: time.Now(), Text: "keep me", Status: state.LedgerOpen}
	if err := d.DB.AddLedger(e); err != nil {
		t.Fatal(err)
	}
	w = doGUI(t, h, guiReq{method: "POST", path: "/api/tapi/reset", bearer: s, body: map[string]any{}})
	if w.Code != 200 {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	var v webTapi
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.Started || d.tapiStarted() {
		t.Fatalf("tapi still started after reset: %+v", v)
	}
	if got, err := d.DB.LedgerGet(e.ID); err != nil || got.Status != state.LedgerOpen {
		t.Fatalf("the request after reset: %+v %v", got, err)
	}
}

// GET /api/repos lists what repo.list knows (with a directory); POST
// /api/sessions makes a session there as the person (a prompt starting with
// '!' too: the GUI is a person, the sheet says what it does). A repository
// is named by its id: an unknown id or a path is refused, and nothing is
// made. Neither route is on the phone's front door.
func TestGUINewSession(t *testing.T) {
	d, _, repo, h, _ := guiSetup(t)
	call[[]KnownRepo](t, d, "repo.list", map[string]any{"cwd": repo})
	s := guiSignIn(t, d, h)
	if w := doGUI(t, h, guiReq{path: "/api/repos"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("repos without a bearer: %d", w.Code)
	}
	w := doGUI(t, h, guiReq{path: "/api/repos", bearer: s})
	var rs struct {
		Repos []guiRepo `json:"repos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rs); err != nil || w.Code != 200 || len(rs.Repos) != 1 || rs.Repos[0].Dir == "" {
		t.Fatalf("repos: %d %s", w.Code, w.Body)
	}
	id := rs.Repos[0].ID
	ph := testWebHandler(d)
	if w := doWeb(t, ph, webReq{path: "/api/repos", from: ipPhone}); w.Code == 200 {
		t.Fatalf("the phone lists repositories: %d %s", w.Code, w.Body)
	}
	if w := doWeb(t, ph, webReq{method: "POST", path: "/api/sessions", from: ipPhone, body: map[string]any{"repo_id": id, "branch": "feat/phone"}}); w.Code == 200 {
		t.Fatalf("the phone made a session: %d %s", w.Code, w.Body)
	}
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions", body: map[string]any{"repo_id": id, "branch": "feat/nobearer"}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("new without a bearer: %d", w.Code)
	}
	for name, body := range map[string]map[string]any{
		"unknown id":    {"repo_id": "nope", "branch": "feat/a"},
		"a path":        {"repo_id": repo, "branch": "feat/a"},
		"no branch":     {"repo_id": id, "branch": " "},
		"control chars": {"repo_id": id, "branch": "feat/a", "prompt": "hi\x1b[2~"},
		"too long":      {"repo_id": id, "branch": "feat/a", "prompt": strings.Repeat("あ", guiMaxPrompt+1)},
	} {
		if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions", bearer: s, body: body}); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions", bearer: s, body: map[string]any{"repo_id": id, "branch": "bad..branch"}}); w.Code != http.StatusConflict {
		t.Fatalf("bad branch: %d %s", w.Code, w.Body)
	}
	if got := call[[]*state.Session](t, d, "session.list", nil); len(got) != 0 {
		t.Fatalf("refused requests made sessions: %d", len(got))
	}
	w = doGUI(t, h, guiReq{method: "POST", path: "/api/sessions", bearer: s, body: map[string]any{"repo_id": id, "branch": "feat/gui", "prompt": "!ls\nthen build"}})
	if w.Code != 200 {
		t.Fatalf("new: %d %s", w.Code, w.Body)
	}
	var ws webSession
	json.Unmarshal(w.Body.Bytes(), &ws)
	if ws.Slug != "feat-gui" || ws.Branch != "feat/gui" {
		t.Fatalf("made: %+v", ws)
	}
	got := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-gui"})
	if got.Parent != callerHuman {
		t.Fatalf("parent: %q, want the person", got.Parent)
	}
	// the send log has it as the GUI's (via gui), with the whole prompt
	rs2, err := d.DB.Sends(state.SendFilter{To: "feat-gui", Limit: 10})
	if err != nil || len(rs2) != 1 || rs2[0].Kind != "new" || rs2[0].Text != "!ls\nthen build" || !strings.Contains(rs2[0].Via, "gui") {
		t.Fatalf("send log: %+v %v", rs2, err)
	}
	// the same branch again: session.new's own refusal
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions", bearer: s, body: map[string]any{"repo_id": id, "branch": "feat/gui"}}); w.Code != http.StatusConflict {
		t.Fatalf("again: %d %s", w.Code, w.Body)
	}
}

// The page's side (gui/actions.js): Interrupt is offered only where
// pekoed's interruptCheck would let it through (the agent works, no
// prompt), the reset sheet says the requests and the inbox stay, and the
// New session sheet shows the whole prompt and warns of a leading '!'.
func TestGUIActions(t *testing.T) {
	out := runWebModule(t, `import { interruptWhy, resetSheet, newWhy, newSheet, issueLine, issueMeta, pickIssueBranch, tapiStart } from "gui/actions.js";
const s = (o) => Object.assign({ state: "running", stopped: false }, o);
const why = [
  interruptWhy(s({}), false),
  interruptWhy(s({ state: "waiting" }), false),
  interruptWhy(s({ state: "waiting", wait_kind: "question" }), false),
  interruptWhy(s({ state: "idle" }), false),
  interruptWhy(s({ stopped: true }), false),
  interruptWhy(null, false),
  interruptWhy({ started: true, alive: true, state: "running" }, true),
  interruptWhy({ started: false, alive: false, state: "running" }, true),
  interruptWhy({ started: true, alive: false, state: "running" }, true),
].map((w) => w !== "");
const reset = resetSheet({ started: true, state: "running", ledger_open: 3, inbox_open: 2 });
const n1 = newSheet({ name: "pekoe", dir: "/r/pekoe" }, " feat/x ", "!ls\nmore");
const n2 = newSheet({ name: "pekoe", dir: "/r/pekoe" }, "feat/x", "  ");
const det = { number: 12, title: "<img src=x onerror=alert(1)>", author: "eve", url: "https://github.com/me/app/issues/12", text: "GitHub issue #12 に対応してください。\n~~~markdown\n# t\n~~~" };
const n3 = newSheet({ name: "pekoe", dir: "/r/pekoe" }, "fix/12", "and a test", det);
const n4 = newSheet({ name: "pekoe", dir: "/r/pekoe" }, "fix/12", " ", det);
const row = { number: 12, title: "Login", labels: ["bug", "ui"], branch: "fix/login", session: { branch: "fix/12", state: "running" } };
console.log(JSON.stringify({
  why,
  reset: [reset.danger, reset.facts.map((f) => f.text).join("\n"), reset.warn],
  new1: [n1.to, n1.facts[0].text, n1.facts[2].text, n1.facts[2].pre, n1.warn],
  new2: [n2.facts[2].pre, n2.warn],
  newWhy: [newWhy(null, "x"), newWhy({}, " "), newWhy({}, "a b"), newWhy({}, "feat/x")],
  issue3: [n3.title, n3.facts.map((f) => f.cap + "=" + f.text).join("|"), n3.facts[3].text, n3.facts[3].pre, n3.warn],
  issue4: [n4.facts[3].text],
  rows: [issueLine(row), issueMeta(row), issueMeta({ number: 1, title: "x", labels: [] })],
  branch: [pickIssueBranch("", "", row), pickIssueBranch("fix/old", "fix/old", row), pickIssueBranch("mine/x", "fix/old", row)],
  start: [tapiStart(null), tapiStart({ started: false }), tapiStart({ started: true, alive: false }), tapiStart({ started: true, alive: true })],
}));`)
	var got struct {
		Why    []bool   `json:"why"`
		Reset  []any    `json:"reset"`
		New1   []any    `json:"new1"`
		New2   []any    `json:"new2"`
		NewWhy []string `json:"newWhy"`
		Issue3 []any    `json:"issue3"`
		Issue4 []string `json:"issue4"`
		Rows   []string `json:"rows"`
		Branch []string `json:"branch"`
		Start  []string `json:"start"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	if want := []bool{false, true, true, true, true, true, false, true, true}; fmt.Sprint(got.Why) != fmt.Sprint(want) {
		t.Errorf("interruptWhy refuses %v, want %v", got.Why, want)
	}
	if got.Reset[0] != true || !strings.Contains(got.Reset[1].(string), "3 open") || !strings.Contains(got.Reset[1].(string), "inbox (2 open") || got.Reset[2] == "" {
		t.Errorf("reset sheet: %v", got.Reset)
	}
	if got.New1[0] != "pekoe · /r/pekoe" || got.New1[1] != "feat/x" || got.New1[2] != "!ls\nmore" || got.New1[3] != true || !strings.Contains(got.New1[4].(string), "starts with !") {
		t.Errorf("new sheet: %v", got.New1)
	}
	if got.New2[0] == true || got.New2[1] != "" {
		t.Errorf("new sheet without a prompt: %v", got.New2)
	}
	if got.NewWhy[0] == "" || got.NewWhy[1] == "" || got.NewWhy[2] == "" || got.NewWhy[3] != "" {
		t.Errorf("newWhy: %q", got.NewWhy)
	}
	// from an issue: the issue's text heads the prompt the sheet shows whole,
	// the issue's author and title are named, and a warning says who wrote it
	det := "GitHub issue #12 に対応してください。\n~~~markdown\n# t\n~~~"
	if got.Issue3[0] != "New session in pekoe from #12" || !strings.Contains(got.Issue3[1].(string), "#12 <img src=x onerror=alert(1)> · opened by eve") ||
		got.Issue3[2] != det+"\n\nand a test" || got.Issue3[3] != true || !strings.Contains(got.Issue3[4].(string), "whoever can open an issue") {
		t.Errorf("issue sheet: %v", got.Issue3)
	}
	if got.Issue4[0] != det {
		t.Errorf("issue sheet without a prompt: %q", got.Issue4)
	}
	if want := []string{"#12 Login", "bug, ui · session fix/12 · running", ""}; fmt.Sprint(got.Rows) != fmt.Sprint(want) {
		t.Errorf("issue rows: %q", got.Rows)
	}
	if want := []string{"fix/login", "fix/login", "mine/x"}; fmt.Sprint(got.Branch) != fmt.Sprint(want) {
		t.Errorf("pickIssueBranch: %q", got.Branch)
	}
	if want := []string{"", "start", "restart", ""}; fmt.Sprint(got.Start) != fmt.Sprint(want) {
		t.Errorf("tapiStart: %q", got.Start)
	}
	// the page wires each one to its sheet, and the keys to the sheet, never
	// to the call itself
	js, _ := webFiles.ReadFile("webui/gui/gui.js")
	for _, want := range []string{
		`openConfirm(newSheet(repo, branch, prompt, det)`, `openConfirm(resetSheet(tapi)`, `openConfirm(w, async () => {
    await api(apiBase(slug) + "/interrupt", {});`,
		`if (e.key === "T") {
    e.preventDefault();
    openReset();`,
	} {
		if !strings.Contains(string(js), want) {
			t.Errorf("gui.js lacks %q", want)
		}
	}
	// the issue's text reaches POST /api/sessions only as the digest of what
	// the sheet showed (pekoed reads the issue itself); issue rows and the
	// pick are drawn with textContent (el / textContent, never HTML)
	for _, want := range []string{`issue: det.number, issue_sig: det.sig`, `pick.textContent = is ? issueLine(is) : "none";`, `b.append(el("span", "t", issueLine(is)));`,
		`if (e.key === "t" && tapiStart(tapi)) {`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("gui.js lacks %q", want)
		}
	}
	if strings.Contains(string(js), "det.text") {
		t.Error("gui.js sends the issue's text itself")
	}
	if strings.Count(string(js), `"/api/tapi/start"`) != 1 {
		t.Error("gui.js calls /api/tapi/start from more than one place")
	}
	if strings.Count(string(js), `"/interrupt"`) != 1 || strings.Count(string(js), `"/api/tapi/reset"`) != 1 || strings.Count(string(js), `api("/api/sessions", {`) != 1 {
		t.Error("gui.js calls an operation outside its confirm sheet")
	}
}
