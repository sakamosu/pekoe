package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

func TestAnsiStrip(t *testing.T) {
	a := &ansiStrip{}
	var got strings.Builder
	// colours, an OSC title, a sequence cut between chunks, \r\n, a
	// progress line drawn over with a lone \r, a bell
	for _, chunk := range []string{
		"\x1b[32mready\x1b[0m in 3s\r\n",
		"\x1b]0;title\x07compiling\x1b[",
		"1;31m 10%\r 50%\r100%\r",
		"\ndone\x07\ttab\x1b(B\n",
	} {
		got.Write(a.feed([]byte(chunk)))
	}
	want := "ready in 3s\ncompiling 10%\n 50%\n100%\ndone\ttab\n"
	if got.String() != want {
		t.Fatalf("got %q\nwant %q", got.String(), want)
	}
}

func sessGet(t *testing.T, d *Daemon, slug string) state.Session {
	t.Helper()
	return call[state.Session](t, d, "session.get", map[string]any{"slug": slug})
}

// A: no [hooks] dev says what the file holds.
func TestDevToggleExplainsMissingDev(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	cases := []struct{ cfg, want string }{
		{"", "the file does not exist"},
		{"[worktree]\ncopy = [\".env\"]\n", "no [hooks] table (it has: worktree)"},
		{"[hooks]\nsetup = [\"x\"]\ntest = \"make test\"\n", "[hooks] has setup, test, no dev"},
		{"[hooks]\ndev = \"\"\n", "[hooks] dev is there but empty"},
		{"[hooks\ndev = 1", "does not parse"},
	}
	for _, c := range cases {
		if c.cfg != "" {
			writeRepoConfig(t, d, repo, c.cfg)
		}
		_, err := d.Call("dev.toggle", []byte(`{"slug":"main"}`))
		if err == nil || !strings.Contains(err.Error(), "no [hooks] dev command in ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v (want %q)", c.cfg, err, c.want)
		}
	}
}

// D, G, E: the hook's output goes to its log; a hook that ends by itself
// turns dev false with its exit status; stopping a running one runs
// dev_stop in the worktree with the session's environment.
func TestDevLogExitAndStop(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	out := filepath.Join(t.TempDir(), "down")
	writeRepoConfig(t, d, repo, "[hooks]\ndev = \"make dev\"\ndev_stop = \"echo down $PEKOE_SESSION $PWD >> "+out+"; echo stop-out\"\n")

	s := call[state.Session](t, d, "dev.toggle", map[string]any{"slug": "main"})
	if !s.Dev || s.DevLog != devLogPath("main") || s.DevExit != nil {
		t.Fatalf("started: %+v", s)
	}
	if sp := fe.spawn["main~dev"]; !strings.Contains(sp.Command, `exit "$pekoe_st"`) {
		t.Fatalf("the hook's exit status is not kept: %s", sp.Command)
	}
	d.onOutput(engine.Output{Slug: "main", Window: engine.WinDev, Data: []byte("\x1b[1mlistening on :3000\x1b[0m\r\nGET / 200\r\n")})
	lg := call[DevLog](t, d, "dev.log", map[string]any{"slug": "main"})
	if !lg.Running || lg.Exit != nil || len(lg.Lines) < 3 ||
		!strings.Contains(lg.Lines[0], "dev hook started") || lg.Lines[1] != "listening on :3000" || lg.Lines[2] != "GET / 200" {
		t.Fatalf("log: %+v", lg)
	}
	if lg := call[DevLog](t, d, "dev.log", map[string]any{"slug": "main", "lines": 1}); len(lg.Lines) != 1 || lg.Lines[0] != "GET / 200" {
		t.Fatalf("lines 1: %+v", lg.Lines)
	}

	// it ends by itself: exit status 2
	fe.mu.Lock()
	fe.dead["main~dev"] = true
	fe.exits["main~dev"] = [2]int{2, 0}
	fe.mu.Unlock()
	d.checkDevExits()
	s = sessGet(t, d, "main")
	if s.Dev || s.DevExit == nil || s.DevExit.Status != 2 || s.DevExit.Stopped || s.DevExit.At.IsZero() {
		t.Fatalf("after exit: dev=%v %+v", s.Dev, s.DevExit)
	}
	lg = call[DevLog](t, d, "dev.log", map[string]any{"slug": "main"})
	if lg.Running || lg.Exit == nil || lg.Exit.Status != 2 || !strings.Contains(lg.Lines[len(lg.Lines)-1], "ended by itself: exit status 2") {
		t.Fatalf("log after exit: %+v", lg)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("dev_stop ran for a hook that ended by itself")
	}

	// started again, then stopped with pekoe dev: dev_stop runs once
	s = call[state.Session](t, d, "dev.toggle", map[string]any{"slug": "main"})
	if !s.Dev || s.DevExit != nil {
		t.Fatalf("restart: %+v", s)
	}
	s = call[state.Session](t, d, "dev.toggle", map[string]any{"slug": "main"})
	if s.Dev || s.DevExit == nil || !s.DevExit.Stopped || s.DevExit.DevStop == nil || s.DevExit.DevStop.Error != "" || fe.Alive("main", "dev") {
		t.Fatalf("stop: %+v %+v", s, s.DevExit)
	}
	b, _ := os.ReadFile(out)
	if got := strings.TrimSpace(string(b)); got != "down main "+s.Path && got != "down main "+realpath(s.Path) {
		t.Fatalf("dev_stop ran as %q", got)
	}
	text := strings.Join(call[DevLog](t, d, "dev.log", map[string]any{"slug": "main"}).Lines, "\n")
	for _, w := range []string{"dev hook stopped by pekoe", "[hooks] dev_stop: echo down", "stop-out", "[hooks] dev_stop done"} {
		if !strings.Contains(text, w) {
			t.Errorf("log lacks %q:\n%s", w, text)
		}
	}

	// rm after that: dev_stop has run since, so not again
	call[any](t, d, "worktree.rm", map[string]any{"slug": "main", "force": true})
	if b, _ := os.ReadFile(out); strings.Count(string(b), "down") != 1 {
		t.Fatalf("dev_stop ran again on rm: %q", b)
	}
	if _, err := os.Stat(devLogPath("main")); !os.IsNotExist(err) {
		t.Fatal("rm left the dev log")
	}
}

// E: rm runs dev_stop for a running hook, and for one that ended by
// itself with no dev_stop since; a failing dev_stop does not stop rm.
func TestDevStopOnRm(t *testing.T) {
	for _, ended := range []bool{false, true} {
		d, fe, repo := setup(t)
		call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
		out := filepath.Join(t.TempDir(), "down")
		writeRepoConfig(t, d, repo, "[hooks]\ndev = \"make dev\"\ndev_stop = \"echo down >> "+out+"; exit 3\"\n")
		call[state.Session](t, d, "dev.toggle", map[string]any{"slug": "main"})
		if ended {
			fe.mu.Lock()
			fe.dead["main~dev"] = true
			fe.mu.Unlock()
			d.checkDevExits()
		}
		call[any](t, d, "worktree.rm", map[string]any{"slug": "main", "force": true})
		if b, _ := os.ReadFile(out); strings.Count(string(b), "down") != 1 {
			t.Fatalf("ended=%v: dev_stop on rm: %q", ended, b)
		}
		if _, err := d.get("main"); err == nil {
			t.Fatalf("ended=%v: rm did not go through", ended)
		}
	}
}

// E: pekoed stopping its agents (daemon stop --kill-agents) runs every
// running dev hook's dev_stop before it goes.
func TestDevStopOnKillAgents(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	out := filepath.Join(t.TempDir(), "down")
	writeRepoConfig(t, d, repo, "[hooks]\ndev = \"make dev\"\ndev_stop = \"echo down >> "+out+"\"\n")
	call[state.Session](t, d, "dev.toggle", map[string]any{"slug": "main"})
	d.stopAllDevs()
	if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != "down" {
		t.Fatalf("dev_stop: %q", b)
	}
	if s := sessGet(t, d, "main"); s.Dev || s.DevExit == nil || !s.DevExit.Stopped || fe.Alive("main", "dev") {
		t.Fatalf("after: %+v", s)
	}
	d.stopAllDevs() // nothing left to stop
	if b, _ := os.ReadFile(out); strings.Count(string(b), "down") != 1 {
		t.Fatalf("ran twice: %q", b)
	}
}

// D: without the token, dev.log is the caller's own session's only.
func TestAgentDevLogOnlyOwnSession(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	call[state.Session](t, d, "session.open", map[string]any{"path": gitRepo(t, "x", "other"), "resume": "new"})
	d.devLogNote("other", "secret")
	d.devLogNote("main", "mine")
	lg := callAs[DevLog](t, d, agentAs("main"), "dev.log", map[string]any{})
	if lg.Slug != "main" || len(lg.Lines) != 1 || !strings.Contains(lg.Lines[0], "mine") {
		t.Fatalf("own, no slug: %+v", lg)
	}
	if err := callAsErr(d, agentAs("main"), "dev.log", map[string]any{"slug": "other"}); err == nil || !strings.Contains(err.Error(), "only its own") {
		t.Fatalf("other: %v", err)
	}
	if err := callAsErr(d, agentAs(TapiSlug), "dev.log", map[string]any{}); err == nil {
		t.Fatal("tapi read a dev log")
	}
}

// G: a dev window that is there but dead when pekoed starts does not turn
// dev back on.
func TestDevDeadWindowAtStart(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	fe.mu.Lock()
	fe.wins["main~dev"] = true
	fe.dead["main~dev"] = true
	fe.mu.Unlock()
	d.restore()
	if s := sessGet(t, d, "main"); s.Dev {
		t.Fatal("dead dev window taken as running")
	}
}

// B: [worktree] copy / clone sources missing or empty in the main
// checkout are warnings on the env step and the session; the session is
// made all the same.
func TestNewWarnsAboutCopySources(t *testing.T) {
	d, _, repo := setup(t)
	os.WriteFile(filepath.Join(repo, ".envrc.local"), nil, 0o644)
	os.WriteFile(filepath.Join(repo, ".env"), []byte("A=1\n"), 0o644)
	writeRepoConfig(t, d, repo, "[worktree]\ncopy = [\".env\", \".envrc.local\", \".env.missing\"]\nclone = [\"node_modules\"]\n")
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/w"})
	var s state.Session
	waitFor(t, func() bool { s = sessGet(t, d, "feat-w"); return s.State != state.Creating })
	if s.State == state.Failed {
		t.Fatalf("failed: %s", s.Error)
	}
	got := strings.Join(s.Warnings, "\n")
	for _, w := range []string{`[worktree] copy ".env.missing": not found in `, "[worktree] copy .envrc.local: empty (0 bytes)", `[worktree] clone "node_modules": not found`} {
		if !strings.Contains(got, w) {
			t.Errorf("warnings lack %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, `".env"`) || strings.Contains(got, "copy .env:") {
		t.Errorf("a good source warned about:\n%s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(s.Path, ".env")); string(b) != "A=1\n" {
		t.Fatalf(".env not copied: %q", b)
	}
	p := call[struct {
		Steps []Step `json:"steps"`
	}](t, d, "session.progress", map[string]any{"slug": "feat-w"})
	for _, st := range p.Steps {
		if st.Name == "env" && len(st.Warnings) != 3 {
			t.Errorf("env step warnings: %v", st.Warnings)
		}
	}
}
