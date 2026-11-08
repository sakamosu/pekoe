package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"boba/internal/daemon"
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
	for _, c := range []struct {
		st   daemon.RunState
		want string
	}{
		{daemon.RunState{Finished: true, Command: "make install", ExitStatus: 2}, "`make install` を実行しました(exit 2)"},
		{daemon.RunState{Finished: true, Command: "sleep 100", ExitStatus: -1, Signal: "SIGINT"}, "`sleep 100` を実行しましたが、中断されました(SIGINT)"},
		{daemon.RunState{Finished: true, Command: "npm login\nnpm publish", ExitStatus: 0}, "`npm login ⏎ npm publish` を実行しました(exit 0)"},
		{daemon.RunState{ExitStatus: -1}, "頼まれたコマンド を実行しました(終了コード不明)"},
	} {
		if got := runReport(c.st); got != c.want {
			t.Errorf("%+v: %q, want %q", c.st, got, c.want)
		}
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
	close map[string]any // run.close answer
	agent []string       // the agent's screen
	runSc *engine.Screen // the run window's screen
}

func (r *runReplies) reply(method string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch method {
	case "run.start":
		return r.run
	case "run.close":
		return r.close
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
	rr.run = map[string]any{"running": true, "started": true, "command": "read -p ok x", "exit_status": -1}
	m.sessions[0].Run = "read -p ok x" // session.updated from bobad
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
	rr.close = map[string]any{"finished": true, "command": "read -p ok x", "exit_status": 3}
	rr.mu.Unlock()
	runCmd(m, m.onKey(key("enter")))
	if rc := f.find("run.close"); len(rc) != 1 || rc[0]["slug"] != "api-1" {
		t.Fatalf("run.close: %v", rc)
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
	m, f, rr := runModel(t)
	rr.close = map[string]any{"finished": true, "command": "ls", "exit_status": 0}
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

// review #52: closing a finished run with x / Esc must not leave its dead
// screen as the agent's (Enter would then restart the running agent).
func TestRunEscDoesNotRestartAgent(t *testing.T) {
	for _, k := range []string{"esc", "x"} {
		m, f, _ := runModel(t)
		m.window, m.focus, m.typing = engine.WinRun, pAgent, true
		m.screen, m.screenOf = &engine.Screen{Lines: []string{"$ ls"}, Dead: true, Width: 100, Height: 30}, "api-1"
		runCmd(m, m.onKey(key(k)))
		if m.window != engine.WinAgent || (m.screen != nil && m.screen.Dead) {
			t.Fatalf("%s: window %s, dead screen kept %v", k, m.window, m.screen)
		}
		m.focus = pAgent
		runCmd(m, m.onKey(key("enter")))
		if r := f.find("session.restart"); len(r) != 0 {
			t.Fatalf("%s then enter restarted the agent: %v", k, r)
		}
	}
}

// review #52: "x close" on a finished run closes the run, not the session.
func TestRunXClosesRunNotSession(t *testing.T) {
	for _, focus := range []int{pAgent, pSessions} {
		m, f, _ := runModel(t)
		m.window, m.focus, m.typing = engine.WinRun, focus, false
		m.screen, m.screenOf = &engine.Screen{Lines: []string{"$ ls"}, Dead: true, Width: 100, Height: 30}, "api-1"
		runCmd(m, m.onKey(key("x")))
		if k := f.find("session.kill"); len(k) != 0 || m.modal != nil {
			t.Fatalf("focus %d: x ended the session: %v %T", focus, k, m.modal)
		}
		if m.window != engine.WinAgent {
			t.Fatalf("focus %d: run not closed: %s", focus, m.window)
		}
	}
}

// review #52: Enter answering the command's prompts must not also send the
// report: the report modal takes y / n only.
func TestRunEnterDoesNotSendReport(t *testing.T) {
	m, f, _ := runModel(t)
	m.window, m.focus, m.typing = engine.WinRun, pAgent, true
	m.screen, m.screenOf = &engine.Screen{Lines: []string{"$ ls"}, Dead: true, Width: 100, Height: 30}, "api-1"
	runCmd(m, m.onKey(key("enter")))
	runCmd(m, m.onKey(key("enter")))
	runCmd(m, m.onKey(key("enter")))
	if s := f.find("session.send"); len(s) != 0 {
		t.Fatalf("sent without y: %v", s)
	}
}

// review #52: a failed session's finished run is still the run window.
func TestRunOnFailedSessionShowsRun(t *testing.T) {
	m, _, _ := runModel(t)
	m.sessions[0].State = state.Failed
	m.sessions[0].Error = "setup failed: exit 1"
	m.window, m.focus, m.typing = engine.WinRun, pAgent, true
	m.screen, m.screenOf = &engine.Screen{Lines: []string{"$ make setup", "done"}, Dead: true, Width: 100, Height: 30}, "api-1"
	if out := ansi.Strip(m.View()); strings.Contains(out, "setup failed") || !strings.Contains(out, "back to the agent") {
		t.Fatalf("failed session's run:\n%s", out)
	}
}

// review #52: what the suggestion must not pick, and must not miss.
func TestRunSuggestReview(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"!= is not a bang", []string{"  if err != nil {", "`sudo make install` を実行してください"}, "sudo make install"},
		{"!ok is not a bang", []string{"  return !ok", "run `make test`"}, "make test"},
		{"Claude Code's shortcut list", []string{"  ! for bash mode    / for commands", "> "}, ""},
		{"one-word inline code is a file", []string{"`gh auth login` を実行して", "`main.go` を直しました"}, "gh auth login"},
		{"fence opened off screen", []string{"$ brew install tmux", "```", "done", "> "}, "brew install tmux"},
		{"url argument", []string{"⏺ Run " + "\x1b[38;2;177;185;249m" + "curl -fsSL https://example.com/install.sh | sh" + "\x1b[39m" + " to install."}, "curl -fsSL https://example.com/install.sh | sh"},
		{"url argument in backticks", []string{"then `git clone https://github.com/a/b` please"}, "git clone https://github.com/a/b"},
		{"a bare url is not a command", []string{"open `https://example.com/x y`"}, ""},
		{"underline colour is not a text colour", []string{"please \x1b[58;2;0;0;35mrun this now\x1b[59m ok"}, ""},
		{"colon-form underline keeps the code span", []string{"⏺ Run \x1b[4:3m\x1b[38;2;177;185;249mgh auth refresh -s repo\x1b[39m\x1b[4:0m to fix it."}, "gh auth refresh -s repo"},
	}
	for _, c := range cases {
		if got := runSuggest(c.lines); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// review #52: a pasted multi-line command reaches the shell as pasted.
func TestRunPasteKeepsLines(t *testing.T) {
	m, f, rr := runModel(t)
	rr.run = map[string]any{}
	runCmd(m, m.onKey(key("!")))
	m.onKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	paste := "docker run \\\r\n  -it ubuntu \\\r\n  sh -c 'echo  a'\r\nnpm publish\n"
	m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	if out := checkSize(t, m, "pasted"); !strings.Contains(out, "⏎") {
		t.Fatalf("newlines not marked:\n%s", out)
	}
	runCmd(m, m.onKey(key("enter")))
	runCmd(m, m.onKey(key("enter"))) // twice, before the modal is gone
	rs := f.find("run.start")
	want := "docker run \\\n  -it ubuntu \\\n  sh -c 'echo  a'\nnpm publish"
	if len(rs) != 2 || rs[1]["command"] != want {
		t.Fatalf("run.start: %q", rs)
	}
}

// review #52: another TUI's run is open: show it, named as bobad names it.
func TestRunShowsOtherRunsCommand(t *testing.T) {
	m, _, rr := runModel(t)
	rr.run = map[string]any{}
	runCmd(m, m.onKey(key("!")))
	rr.mu.Lock()
	rr.run = map[string]any{"running": true, "command": "make deploy", "exit_status": -1}
	rr.mu.Unlock()
	m.sessions[0].Run = "make deploy"
	runCmd(m, m.onKey(key("enter")))
	out := ansi.Strip(m.View())
	if m.window != engine.WinRun || !strings.Contains(out, "$ make deploy") || strings.Contains(out, "$ gh auth login ·") {
		t.Fatalf("header:\n%s", out)
	}
	if !strings.Contains(m.msg, "make deploy") {
		t.Fatalf("msg: %q", m.msg)
	}
}

func TestRunInterruptedLine(t *testing.T) {
	m, _, _ := runModel(t)
	m.window, m.focus, m.typing = engine.WinRun, pAgent, true
	m.screen, m.screenOf = &engine.Screen{Lines: []string{"$ sleep 100", "^C"}, Dead: true, ExitStatus: -1, Signal: "SIGINT", Width: 100, Height: 30}, "api-1"
	if out := ansi.Strip(m.View()); !strings.Contains(out, "interrupted (SIGINT) · ↵ back to the agent") {
		t.Fatalf("interrupted:\n%s", out)
	}
}
