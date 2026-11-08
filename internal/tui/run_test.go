package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"boba/internal/engine"
	"boba/internal/state"
)

func TestRunSuggest(t *testing.T) {
	const (
		code = "\x1b[38;2;177;185;249m" // Claude Code draws inline code in a colour, without backticks
		hl1  = "\x1b[38;5;81m"
		hl2  = "\x1b[38;5;186m"
		rst  = "\x1b[39m"
	)
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"nothing", []string{"⏺ Done! All tests pass.", "", "> "}, ""},
		{"bang", []string{"⏺ Please run this yourself:", "", "  ! gh auth login", "", "> "}, "gh auth login"},
		{"bang in backticks", []string{"Run `! sudo make install` in the prompt."}, "sudo make install"},
		{"bang beats later code", []string{"! npm login", "then `npm whoami`"}, "npm login"},
		{"last bang wins", []string{"! first cmd", "! second cmd"}, "second cmd"},
		{"exclamation in prose is not a command", []string{"Done! Next step: `make test`"}, "make test"},
		{"backtick inline code", []string{"try `go test ./...` first", "or `make test` instead"}, "make test"},
		{"fenced block", []string{"```sh", "$ brew install tmux", "```", "done"}, "brew install tmux"},
		{"dollar line", []string{"  $ docker login ghcr.io"}, "docker login ghcr.io"},
		{"coloured inline code", []string{"⏺ Run " + code + "gh auth refresh -s repo" + rst + " to fix the scope."}, "gh auth refresh -s repo"},
		{"coloured single word is not a command", []string{"⏺ Edit " + code + "main.go" + rst + " later."}, ""},
		{"coloured url is not a command", []string{"open " + code + "http://localhost 3000" + rst + " now"}, ""},
		{"highlighted code-block line", []string{"⏺ Run:", "", "  " + hl1 + "sudo" + rst + " " + hl2 + "pmset -a sleep 0" + rst}, "sudo pmset -a sleep 0"},
		{"bottom-most code wins", []string{"`old cmd`", "  " + hl1 + "new cmd" + rst}, "new cmd"},
	}
	for _, c := range cases {
		if got := runSuggest(c.lines); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRunReport(t *testing.T) {
	if got := runReport("make install", 2); got != "`make install` を実行しました(exit 2)" {
		t.Fatal(got)
	}
	if got := runReport("", -1); got != "頼まれたコマンド を実行しました" {
		t.Fatal(got)
	}
	sc := &engine.Screen{Lines: []string{"\x1b[2m$ gh auth login\x1b[0m", "? Where do you use GitHub?"}}
	if got := runCommandOnScreen(sc); got != "gh auth login" {
		t.Fatalf("command from the screen: %q", got)
	}
}

// runModel: one session selected in [1], talking to a fake bobad.
func runModel(t *testing.T) (*model, *fakeBobad, *runReplies) {
	t.Helper()
	f, cl := startFakeBobad(t)
	rr := &runReplies{agent: []string{"⏺ Please run:", "", "  ! gh auth login"}}
	f.reply = rr.reply
	t.Setenv("BOBA_COLOR", "none")
	m := newModel(cl, "/src/api")
	m.tapi.splash = false
	m.width, m.height = 140, 40
	m.sessions = []*state.Session{sess("api-1", appA, state.Idle, time.Now())}
	m.sessionsIn, m.homePicked, m.reposLoaded = true, true, true
	m.lastSel = "api-1"
	return m, f, rr
}

type runReplies struct {
	mu    sync.Mutex
	run   map[string]any // run.start answer
	agent []string       // the agent's screen
	runSc *engine.Screen // the run window's screen
}

func (r *runReplies) reply(method string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch method {
	case "run.start":
		return r.run
	case "session.screen":
		if r.runSc != nil {
			return r.runSc
		}
		return engine.Screen{Lines: r.agent, Width: 100, Height: 30}
	}
	return nil
}

func TestRunWindowFlow(t *testing.T) {
	m, f, rr := runModel(t)

	// typing into the agent: ! is the agent's (Claude Code's shell mode)
	m.focus, m.typing = pAgent, true
	runCmd(m, m.onKey(key("!")))
	if len(f.find("run.start")) != 0 {
		t.Fatal("! while typing must reach the agent, not open a run")
	}
	if ks := f.find("session.keys"); len(ks) != 1 || ks[0]["literal"] != "!" || ks[0]["window"] != engine.WinAgent {
		t.Fatalf("forwarded: %v", ks)
	}

	// ! in Sessions: the command line, pre-filled from the agent's screen
	m.typing, m.focus = false, pSessions
	runCmd(m, m.onKey(key("!")))
	rm, ok := m.modal.(*runModal)
	if !ok {
		t.Fatalf("! opened %T", m.modal)
	}
	if rm.in.String() != "gh auth login" {
		t.Fatalf("suggestion: %q", rm.in.String())
	}
	if out := checkSize(t, m, "run modal"); !strings.Contains(out, "$ gh auth login") {
		t.Fatalf("modal:\n%s", out)
	}
	// edit it, then run
	m.onKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	for _, r := range "read -p ok x" {
		m.onKey(key(string(r)))
	}
	rr.mu.Lock()
	rr.run = map[string]any{"running": true, "started": true}
	rr.runSc = &engine.Screen{Lines: []string{"$ read -p ok x", "ok"}, Width: 100, Height: 30}
	rr.mu.Unlock()
	runCmd(m, m.onKey(key("enter")))
	rs := f.find("run.start")
	if len(rs) != 2 || rs[1]["command"] != "read -p ok x" || rs[1]["slug"] != "api-1" || rs[1]["width"] == nil {
		t.Fatalf("run.start: %v", rs)
	}
	if m.modal != nil || m.window != engine.WinRun || m.focus != pAgent || !m.typing {
		t.Fatalf("after start: modal %T window %s focus %d typing %v", m.modal, m.window, m.focus, m.typing)
	}
	if out := checkSize(t, m, "run"); !strings.Contains(out, "Run api-1") || !strings.Contains(out, "$ read -p ok x") {
		t.Fatalf("run window:\n%s", out)
	}
	// keys go to the run window's pty (the prompt is answered there)
	runCmd(m, m.onKey(key("y")))
	if ks := f.find("session.keys"); ks[len(ks)-1]["window"] != engine.WinRun || ks[len(ks)-1]["literal"] != "y" {
		t.Fatalf("typed into: %v", ks[len(ks)-1])
	}

	// the command exits: exit status and the way back
	m.Update(screenMsg{slug: "api-1", window: engine.WinRun, sc: &engine.Screen{Lines: []string{"$ read -p ok x", "ok y", "", "Pane is dead (status 3, Sun Nov  8 15:39:31 2026)"}, Width: 100, Height: 30, Dead: true, ExitStatus: 3}})
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "exit 3 · ↵ back to the agent") || strings.Contains(out, "Pane is dead") {
		t.Fatalf("finished run:\n%s", out)
	}
	rr.mu.Lock()
	rr.runSc = nil
	rr.mu.Unlock()
	runCmd(m, m.onKey(key("enter")))
	if wk := f.find("window.kill"); len(wk) != 1 || wk[0]["window"] != engine.WinRun {
		t.Fatalf("window.kill: %v", wk)
	}
	if m.window != engine.WinAgent {
		t.Fatalf("back to %s", m.window)
	}
	if _, ok := m.modal.(*runReportModal); !ok {
		t.Fatalf("asks whether to tell the agent: %T", m.modal)
	}
	if out := checkSize(t, m, "report"); !strings.Contains(out, "tell the agent?") {
		t.Fatalf("report modal:\n%s", out)
	}
	runCmd(m, m.onKey(key("y")))
	ss := f.find("session.send")
	if len(ss) != 1 || ss[0]["text"] != "`read -p ok x` を実行しました(exit 3)" || ss[0]["submit"] != true || ss[0]["slug"] != "api-1" {
		t.Fatalf("session.send: %v", ss)
	}
	if _, ok := ss[0]["window"]; ok {
		t.Fatalf("the report goes to the agent window: %v", ss[0])
	}
	if m.modal != nil || !m.typing || m.window != engine.WinAgent {
		t.Fatalf("after report: modal %T typing %v window %s", m.modal, m.typing, m.window)
	}
}

func TestRunDeclineReport(t *testing.T) {
	m, f, _ := runModel(t)
	m.runs = map[string]string{"api-1": "ls"}
	m.window, m.focus, m.typing = engine.WinRun, pAgent, true
	m.screen, m.screenOf = &engine.Screen{Lines: []string{"$ ls"}, Dead: true, ExitStatus: 0, Width: 100, Height: 30}, "api-1"
	runCmd(m, m.onKey(key("enter")))
	runCmd(m, m.onKey(key("n")))
	if len(f.find("session.send")) != 0 || m.modal != nil || m.window != engine.WinAgent {
		t.Fatalf("n sends nothing: %v %T %s", f.find("session.send"), m.modal, m.window)
	}
}

// One run per session: ! while it runs (or before it was closed) shows it.
func TestRunBangShowsExisting(t *testing.T) {
	for _, st := range []map[string]any{{"running": true}, {"finished": true}} {
		m, f, rr := runModel(t)
		rr.run = st
		runCmd(m, m.onKey(key("!")))
		if m.modal != nil || m.window != engine.WinRun || !m.typing || m.focus != pAgent {
			t.Fatalf("%v: modal %T window %s", st, m.modal, m.window)
		}
		if rs := f.find("run.start"); len(rs) != 1 || rs[0]["command"] != nil {
			t.Fatalf("%v: must not start another: %v", st, rs)
		}
		// v back to the agent, ! again returns to the run
		m.typing = false
		runCmd(m, m.onKey(key("v")))
		if m.window != engine.WinAgent {
			t.Fatalf("v: %s", m.window)
		}
	}
}
