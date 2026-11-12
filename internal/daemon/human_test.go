package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/config"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// sockClient talks to d.serve over an in-memory pipe, like a client of the
// unix socket.
type sockClient struct {
	t  *testing.T
	nc net.Conn
	sc *bufio.Scanner
	id int
}

func dialPipe(t *testing.T, d *Daemon) *sockClient {
	t.Helper()
	a, b := net.Pipe()
	go d.serve(b)
	t.Cleanup(func() { a.Close() })
	return &sockClient{t: t, nc: a, sc: bufio.NewScanner(a)}
}

// call sends one request (token may be "") and returns its error string.
func (c *sockClient) call(method string, params any, token string) string {
	c.t.Helper()
	c.id++
	req := map[string]any{"id": c.id, "method": method, "params": params}
	if token != "" {
		req["token"] = token
	}
	b, _ := json.Marshal(req)
	if _, err := c.nc.Write(append(b, '\n')); err != nil {
		c.t.Fatal(err)
	}
	for c.sc.Scan() {
		var r struct {
			ID    int    `json:"id"`
			Error string `json:"error"`
		}
		if json.Unmarshal(c.sc.Bytes(), &r) == nil && r.ID == c.id {
			return r.Error
		}
	}
	c.t.Fatalf("%s: connection closed", method)
	return ""
}

// sentSince returns what reached the engine after the first n records.
func sentSince(fe *fakeEngine, n int) []string {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return append([]string{}, fe.sent[n:]...)
}

func TestHumanTokenFile(t *testing.T) {
	d, _, _ := setup(t)
	fi, err := os.Stat(paths.HumanTokenFile())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("human-token mode = %v, want 0600", fi.Mode().Perm())
	}
	if len(d.humanToken) < 32 {
		t.Fatalf("token too short: %q", d.humanToken)
	}
	// kept across restarts: a running TUI stays valid
	tok, err := loadOrCreateHumanToken()
	if err != nil || tok != d.humanToken {
		t.Fatalf("reload = %q, %v; want %q", tok, err, d.humanToken)
	}
}

// The human-only methods are refused over the socket without the token,
// before they reach the engine, and work with it.
func TestHumanOnlyMethodsNeedToken(t *testing.T) {
	d, fe, repo := setup(t)
	waitingSession(t, d, repo)
	c := dialPipe(t, d)
	before := sentCount(fe)
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"session.answer", map[string]any{"slug": "main", "answer": "always"}},
		{"session.keys", map[string]any{"slug": "main", "keys": []string{"2"}}},
		{"tapi.send", map[string]any{"text": "hi", "submit": true}},
		{"tapi.keys", map[string]any{"keys": []string{"Enter"}}},
		// tapi's words and the tools it called
		{"tapi.transcript", map[string]any{"after": ""}},
		{"init.write", map[string]any{"path": repo, "toml": "[hooks]\nsetup = \"true\"\n"}},
		{"run.start", map[string]any{"slug": "main", "command": "true"}},
		{"daemon.stop", map[string]any{"kill_agents": true}},
		// added after the deny list and missed by it: run nvim, read
		// excerpts, return the login URL or the extension's token
		{"review.surface", map[string]any{"slug": "main"}},
		{"error.get", map[string]any{"id": 1}},
		{"error.send", map[string]any{"id": 1}},
		{"web.status", nil},
		{"ext.info", nil},
		{"session.screen", map[string]any{"slug": "main"}},
		{"session.open", map[string]any{"path": repo, "resume": "new"}},
		{"daemon.status", nil},
		// still human-only now that land is open to agents
		{"worktree.rm", map[string]any{"slug": "main", "force": true}},
		{"session.kill", map[string]any{"slug": "main"}},
		{"session.undo", map[string]any{"slug": "main"}},
		{"review.pr", map[string]any{"repo": repo, "number": 7}},
	} {
		for _, tok := range []string{"", "wrong-" + d.humanToken} {
			if e := c.call(tc.method, tc.params, tok); !strings.Contains(e, "only a person can do this") {
				t.Errorf("%s with token %q: error = %q, want the human-only refusal", tc.method, tok, e)
			}
		}
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("refused calls reached the engine: %v", got)
	}
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	if _, err := os.Stat(paths.RepoConfig(id)); err == nil {
		t.Fatal("init.write wrote the repo config without the token")
	}
	if d.killAgents.Load() {
		t.Fatal("daemon.stop ran without the token")
	}

	if e := c.call("session.answer", map[string]any{"slug": "main", "answer": "allow"}, d.humanToken); e != "" {
		t.Fatalf("session.answer with the token: %s", e)
	}
	if !strings.Contains(fe.last(), "lit:1") {
		t.Fatalf("allow not typed: %s", fe.last())
	}
}

// What a hub agent uses keeps working without the token; session.send is
// limited to the agent's input box and refused on a permission prompt.
func TestAgentMethodsWithoutToken(t *testing.T) {
	d, fe, repo := setup(t)
	waitingSession(t, d, repo)
	c := dialPipe(t, d)
	for _, m := range []string{"session.list", "repo.resolve"} {
		params := map[string]any{}
		if m == "repo.resolve" {
			params = map[string]any{"repo": repo, "cwd": repo}
		}
		if e := c.call(m, params, ""); e != "" {
			t.Errorf("%s: %s", m, e)
		}
	}
	if e := c.call("session.get", map[string]any{"slug": "main"}, ""); e != "" {
		t.Errorf("session.get: %s", e)
	}
	if e := c.call("no.such", nil, ""); !strings.Contains(e, "unknown method") {
		t.Errorf("unknown method: error = %q", e)
	}
	before := sentCount(fe)
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "2", "submit": true}, ""); !strings.Contains(e, "waiting for a permission answer") {
		t.Fatalf("send on a permission prompt: error = %q", e)
	}
	if e := c.call("session.send", map[string]any{"slug": "app/main", "text": "2"}, ""); !strings.Contains(e, "waiting for a permission answer") {
		t.Fatalf("send by repo/slug on a permission prompt: error = %q", e)
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("refused send reached the engine: %v", got)
	}
	// a person may type there (the TUI's own input goes through keys, but
	// send is theirs too)
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "2"}, d.humanToken); e != "" {
		t.Fatalf("send with the token: %s", e)
	}

	// once the prompt is answered, the hub can hand over more work
	call[any](t, d, "session.answer", map[string]any{"slug": "main", "answer": "deny"})
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "next, add tests", "submit": true}, ""); e != "" {
		t.Fatalf("send to an idle agent: %s", e)
	}
	if !strings.Contains(strings.Join(sentSince(fe, before), "\n"), "next, add tests") {
		t.Fatalf("text not sent: %v", sentSince(fe, before))
	}
	// other windows are shells
	for _, w := range []string{"dev", "run"} {
		if e := c.call("session.send", map[string]any{"slug": "main", "window": w, "text": "echo hi"}, ""); !strings.Contains(e, "only a person can do this") {
			t.Errorf("send to the %s window: error = %q", w, e)
		}
	}
	if e := c.call("session.send", map[string]any{"slug": "nope", "text": "x"}, ""); !strings.Contains(e, "nope") {
		t.Errorf("send to an unknown slug: error = %q", e)
	}

	// a dialog on the screen that no hook has reported yet holds the send
	// too, with or without submit; with the token only a submit is held
	fe.mu.Lock()
	fe.lines = []string{"Do you want to proceed?", "❯ 1. Yes", "  2. No"}
	fe.mu.Unlock()
	before = sentCount(fe)
	for _, submit := range []bool{true, false} {
		e := c.call("session.send", map[string]any{"slug": "main", "text": "2", "submit": submit}, "")
		if !strings.Contains(e, "main is waiting for a permission answer: answer it in the pekoe TUI first") {
			t.Fatalf("send (submit %v) over an on-screen prompt: error = %q", submit, e)
		}
	}
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "go", "submit": true}, d.humanToken); !strings.Contains(e, "main is waiting for a permission answer: answer it first") {
		t.Fatalf("submit with the token over an on-screen prompt: error = %q", e)
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("held sends reached the engine: %v", got)
	}
}

// land is open to agents: an agent whose sandbox allows github.com can push
// and open the PR itself, and merging (which starts the teardown) stays a
// person's. In a repository without origin land would fast-forward the base
// branch and remove the worktree at once, so that stays human-only.
func TestAgentLand(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	gh := fakeGH(t, "OPEN")
	s := readySession(t, d, repo, "feat/agent-land")
	os.WriteFile(filepath.Join(s.Path, "b.txt"), []byte("b\n"), 0o644)
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "b")
	c := dialPipe(t, d)
	if e := c.call("land", map[string]any{"slug": s.Slug}, ""); e != "" {
		t.Fatalf("land without the token: %s", e)
	}
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	if !strings.Contains(string(out), "pr create --fill --head feat/agent-land --base main") {
		t.Fatalf("gh calls: %s", out)
	}
	run(t, repo, "git", "--git-dir", bare, "rev-parse", "--verify", "refs/heads/feat/agent-land")
	// not merged: nothing is torn down
	d.watchMerges()
	got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	if got.State == state.Landed || !got.Landing || got.PR != 42 {
		t.Fatalf("after land: state %s landing %v pr %d", got.State, got.Landing, got.PR)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("worktree removed before the merge: %v", err)
	}
}

func TestAgentLandWithoutOriginRefused(t *testing.T) {
	d, _, repo := setup(t)
	s := readySession(t, d, repo, "feat/local")
	os.WriteFile(filepath.Join(s.Path, "b.txt"), []byte("b\n"), 0o644)
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "b")
	rev := func() string {
		out, err := exec.Command("git", "-C", repo, "rev-parse", "main").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	mainBefore := rev()
	c := dialPipe(t, d)
	if e := c.call("land", map[string]any{"slug": s.Slug}, ""); !strings.Contains(e, "only a person can do this") || !strings.Contains(e, "without origin") {
		t.Fatalf("land without origin and token: error = %q", e)
	}
	if got := rev(); got != mainBefore {
		t.Fatalf("main moved: %s → %s", mainBefore, got)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("worktree removed: %v", err)
	}
	if e := c.call("land", map[string]any{"slug": s.Slug}, d.humanToken); e != "" {
		t.Fatalf("land with the token: %s", e)
	}
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); got.State != state.Landed {
		t.Fatalf("with the token: state %s, want landed", got.State)
	}
}

// readySession starts a session on branch and waits until it is created.
func readySession(t *testing.T, d *Daemon, repo, branch string) state.Session {
	t.Helper()
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": branch})
	return waitCreated(t, d, s.Slug)
}

// Every registered method is either open to agents or human-only, on
// purpose: a new method fails here until someone puts it in one of the two
// lists (and authorize refuses it to agents meanwhile).
func TestEveryMethodClassified(t *testing.T) {
	d, _, _ := setup(t)
	for m := range d.handlers {
		_, a := agentMethods[m]
		_, h := humanMethods[m]
		switch {
		case a && h:
			t.Errorf("%s is in both agentMethods and humanMethods", m)
		case !a && !h:
			t.Errorf("%s is in neither agentMethods nor humanMethods (human.go): decide whether an agent may call it without the human token", m)
		}
	}
	for _, list := range []map[string]string{agentMethods, humanMethods} {
		for m := range list {
			if _, ok := d.handlers[m]; !ok {
				t.Errorf("%s is classified but not registered", m)
			}
		}
	}
}

// A subscription without the token gets only the session events: not
// other sessions' clipboard (term.passthrough), picks, errors or review
// text.
func TestSubscribeWithoutTokenGetsSessionEvents(t *testing.T) {
	d, _, _ := setup(t)
	conns := func() map[*conn]bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		out := map[*conn]bool{}
		for c := range d.conns {
			out[c] = true
		}
		return out
	}
	newConn := func(tok string) *conn {
		before := conns()
		c := dialPipe(t, d)
		if e := c.call("subscribe", map[string]any{"events": []string{"*"}}, tok); e != "" {
			t.Fatal(e)
		}
		for k := range conns() {
			if !before[k] {
				return k
			}
		}
		t.Fatal("no connection")
		return nil
	}
	agent := newConn("")
	for _, ev := range []string{"term.passthrough", "pick.received", "error.updated", "review.answer", "editor.open", "tapi.output"} {
		if agent.wants(ev) {
			t.Errorf("agent subscription receives %s", ev)
		}
	}
	for _, ev := range []string{"session.updated", "session.progress"} {
		if !agent.wants(ev) {
			t.Errorf("agent subscription misses %s", ev)
		}
	}
	human := newConn(d.humanToken)
	if !human.wants("term.passthrough") || !human.wants("editor.open") {
		t.Error("human subscription misses events")
	}
}

// An agent sends "2" while timing its own permission dialog: the dialog
// comes up after authorizeSend looked at the screen. The send looks again
// right before typing, and text starting with a digit is refused anyway.
func TestAgentSendRechecksBeforeTyping(t *testing.T) {
	d, fe, repo := setup(t)
	call[any](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	c := dialPipe(t, d)
	dialog := []string{"Do you want to proceed?", "❯ 1. Yes", "  2. Yes, and don't ask again", "  3. No"}
	// the first look (authorizeSend) sees an idle box, every later one the dialog
	arm := func() {
		n := 0
		fe.mu.Lock()
		fe.lines = nil
		fe.onCap = func() {
			fe.mu.Lock()
			defer fe.mu.Unlock()
			if n++; n > 1 {
				fe.lines = dialog
			}
		}
		fe.mu.Unlock()
	}
	before := sentCount(fe)
	for _, submit := range []bool{false, true} {
		arm()
		e := c.call("session.send", map[string]any{"slug": "main", "text": "ok go", "submit": submit}, "")
		if !strings.Contains(e, "main is waiting for a permission answer") {
			t.Fatalf("dialog after the first check (submit %v): error = %q", submit, e)
		}
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("typed into a dialog that came up after the first check: %v", got)
	}

	// no dialog at all: a leading digit is still refused without the token
	fe.mu.Lock()
	fe.onCap, fe.lines = nil, nil
	fe.mu.Unlock()
	for _, text := range []string{"2", " 2", "\n3. do it", "1 more thing"} {
		for _, submit := range []bool{false, true} {
			if e := c.call("session.send", map[string]any{"slug": "main", "text": text, "submit": submit}, ""); !strings.Contains(e, "may not start with a digit") {
				t.Errorf("send %q (submit %v): error = %q", text, submit, e)
			}
		}
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("a send starting with a digit reached the agent: %v", got)
	}
	// words are fine, and a person may type digits
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "step 2 next"}, ""); e != "" {
		t.Fatalf("send of words: %s", e)
	}
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "2"}, d.humanToken); e != "" {
		t.Fatalf("send with the token: %s", e)
	}
}

// Without the token, session.new works only in a repository pekoe knows,
// at the directory pekoe knows it by: not in a new one, and not in a clone
// that copied a known repository's origin (and so its id).
func TestAgentNewOnlyInKnownRepos(t *testing.T) {
	d, _, repo := setup(t)
	run(t, repo, "git", "remote", "add", "origin", "https://example.com/me/app.git")
	c := dialPipe(t, d)
	if e := c.call("session.new", map[string]any{"repo": repo, "branch": "feat/a"}, ""); !strings.Contains(e, "does not know yet") || !strings.Contains(e, "only a person can do this") {
		t.Fatalf("new repository without the token: error = %q", e)
	}
	if e := c.call("session.new", map[string]any{"repo": repo, "branch": "feat/a"}, d.humanToken); e != "" {
		t.Fatalf("with the token: %s", e)
	}
	if e := c.call("session.new", map[string]any{"repo": repo, "branch": "feat/b"}, ""); e != "" {
		t.Fatalf("known repository without the token: %s", e)
	}
	// a subdirectory of the known repository is the same repository
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if e := c.call("session.new", map[string]any{"repo": sub, "branch": "feat/c"}, ""); e != "" {
		t.Fatalf("subdirectory of a known repository: %s", e)
	}

	twin := gitRepo(t, "other", "app")
	run(t, twin, "git", "remote", "add", "origin", "https://example.com/me/app.git")
	if e := c.call("session.new", map[string]any{"repo": twin, "branch": "feat/d"}, ""); !strings.Contains(e, "does not know yet") {
		t.Fatalf("clone with a known origin: error = %q", e)
	}
}

// rm without the token clears a landed session (its worktree is already
// gone) and nothing else: other states, force, and a landed session whose
// worktree is still on disk need a person.
func TestAgentRmOnlyLanded(t *testing.T) {
	d, _, repo := setup(t)
	c := dialPipe(t, d)
	s := readySession(t, d, repo, "feat/live")
	for _, params := range []map[string]any{
		{"slug": s.Slug},
		{"slug": s.Slug, "force": true},
	} {
		if e := c.call("worktree.rm", params, ""); !strings.Contains(e, "only a person can do this") {
			t.Fatalf("rm %v of a %s session without the token: error = %q", params, s.State, e)
		}
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("worktree removed: %v", err)
	}
	if _, err := d.get(s.Slug); err != nil {
		t.Fatalf("session removed: %v", err)
	}

	// landed, but the worktree removal failed: refused, directory kept
	d.update(s.Slug, func(x *state.Session) { x.State = state.Landed })
	if e := c.call("worktree.rm", map[string]any{"slug": s.Slug}, ""); !strings.Contains(e, "still there") || !strings.Contains(e, "only a person can do this") {
		t.Fatalf("rm of a landed session with its worktree: error = %q", e)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("worktree removed: %v", err)
	}

	// landed the usual way: the worktree is gone, rm clears the record
	d.update(s.Slug, func(x *state.Session) { x.State = state.Running })
	if err := d.finishLanding(s.Slug); err != nil {
		t.Fatal(err)
	}
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); got.State != state.Landed {
		t.Fatalf("state %s, want landed", got.State)
	}
	if _, err := os.Stat(s.Path); err == nil {
		t.Fatal("finishLanding left the worktree")
	}
	if e := c.call("worktree.rm", map[string]any{"slug": s.Slug, "force": true}, ""); !strings.Contains(e, "only a person can do this") {
		t.Fatalf("rm --force of a landed session without the token: error = %q", e)
	}
	if e := c.call("worktree.rm", map[string]any{"slug": s.Slug}, ""); e != "" {
		t.Fatalf("rm of a landed session without the token: %s", e)
	}
	if _, err := d.get(s.Slug); err == nil {
		t.Fatal("landed session still listed")
	}
	l, err := d.DB.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range l {
		if x.Slug == s.Slug {
			t.Fatal("landed session still in state.db")
		}
	}
}

// The landed check sits where rm removes the session: a session.new of the
// same slug that replaced the landed one is not removed by an rm that
// looked earlier.
func TestAgentRmRechecksUnderLock(t *testing.T) {
	d, _, repo := setup(t)
	s := readySession(t, d, repo, "feat/again")
	d.finishLanding(s.Slug)
	landed, _ := d.get(s.Slug)
	// what a racing session.new does: a new session under the same slug
	d.mu.Lock()
	n := *landed
	n.State = state.Running
	d.sessions[s.Slug] = &n
	d.mu.Unlock()
	if d.forgetIf(s.Slug, func(x *state.Session) bool { return x == landed && x.State == state.Landed }) {
		t.Fatal("forgetIf removed the session that replaced the landed one")
	}
	if got, err := d.get(s.Slug); err != nil || got != &n {
		t.Fatalf("replacement gone: %v", err)
	}
}

// Without the token, session.new starts only an agent pekoe sandboxes (a
// Claude Code agent with sandbox on), or the agent the caller runs itself;
// with the token any agent starts.
func TestAgentNewOnlySandboxedAgents(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/known"}) // pekoe knows the repository
	off := false
	d.Global.Agents["codex"] = config.Agent{Command: "codex", Prompt: "arg"}
	d.Global.Agents["claude-raw"] = config.Agent{Command: "claude --model x", Prompt: "arg", Sandbox: &off}
	d.Global.Agents["claude-fast"] = config.Agent{Command: "/usr/local/bin/claude --model y", Prompt: "arg"}

	hub := agentAs("feat-known") // a claude session
	for _, name := range []string{"codex", "claude-raw"} {
		err := callAsErr(d, hub, "session.new", map[string]any{"repo": repo, "branch": "feat/" + name, "agent": name})
		if err == nil || !errors.Is(err, ErrHumanOnly) || !strings.Contains(err.Error(), "does not sandbox") || !strings.Contains(err.Error(), "TUI") {
			t.Fatalf("%s without the token: err = %v", name, err)
		}
	}
	for _, name := range []string{"", "claude", "claude-fast"} {
		callAs[state.Session](t, d, hub, "session.new", map[string]any{"repo": repo, "branch": "feat/ok-" + name + "x", "agent": name})
	}
	// an unknown agent is hNew's error, as before
	if err := callAsErr(d, hub, "session.new", map[string]any{"repo": repo, "branch": "feat/nope", "agent": "nope"}); err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("unknown agent: %v", err)
	}
	// the repository's default agent counts as asked for
	writeRepoConfig(t, d, repo, "agent = \"codex\"\n")
	if err := callAsErr(d, hub, "session.new", map[string]any{"repo": repo, "branch": "feat/default"}); !errors.Is(err, ErrHumanOnly) {
		t.Fatalf("repository default codex without the token: err = %v", err)
	}
	writeRepoConfig(t, d, repo, "")

	// a codex session may start another codex session: nothing it cannot do already
	cx := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/codex", "agent": "codex"})
	callAs[state.Session](t, d, agentAs(cx.Slug), "session.new", map[string]any{"repo": repo, "branch": "feat/codex2", "agent": "codex"})
	if err := callAsErr(d, agentAs(cx.Slug), "session.new", map[string]any{"repo": repo, "branch": "feat/raw", "agent": "claude-raw"}); !errors.Is(err, ErrHumanOnly) {
		t.Fatalf("codex starting claude-raw: %v", err)
	}
	// a caller in no window gets only sandboxed agents
	if err := callAsErr(d, agentAs(""), "session.new", map[string]any{"repo": repo, "branch": "feat/anon", "agent": "codex"}); !errors.Is(err, ErrHumanOnly) {
		t.Fatalf("unknown caller starting codex: %v", err)
	}
	// tapi runs the agent [tapi] names
	d.Global.Tapi.Agent = "codex"
	callAs[state.Session](t, d, agentAs(TapiSlug), "session.new", map[string]any{"repo": repo, "branch": "feat/tapi-codex", "agent": "codex"})

	// a person starts any agent
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/raw", "agent": "claude-raw"})
}

// session.new's from is gh#N, N an issue number; anything else (a URL
// pekoed's gh would follow to another repository) is refused.
func TestNewFromOnlyIssueNumber(t *testing.T) {
	d, _, repo := setup(t)
	for _, from := range []string{"gh#https://github.com/o/private/issues/1", "gh#0", "gh#01", "gh#12345678901", "gh#-1", "gh#1 2", "12", "#12", "gh#"} {
		if err := callErr(d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "from": from}); err == nil || !strings.Contains(err.Error(), "gh#N") {
			t.Fatalf("from %q: err = %v", from, err)
		}
	}
	if !fromIssueRe.MatchString("gh#1234567890") || !fromIssueRe.MatchString("gh#7") {
		t.Fatal("gh#N refused")
	}
}
