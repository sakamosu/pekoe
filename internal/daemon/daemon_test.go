package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

// fakeEngine records what would be written to the pty.
type fakeEngine struct {
	mu    sync.Mutex
	sent  []string
	wins  map[string]bool
	sizes map[string][2]int // window -> pty size: spawn, then resizes
	out   chan engine.Output
	lines []string          // what Capture returns (nil: "x")
	dead  map[string]bool   // window -> its process exited (Capture says Dead)
	exits map[string][2]int // window -> exit status, signal (after exit)
	delay time.Duration     // Spawn takes this long (races)
	spawn map[string]engine.Spawn
}

func newFake() *fakeEngine {
	return &fakeEngine{wins: map[string]bool{}, sizes: map[string][2]int{}, dead: map[string]bool{}, exits: map[string][2]int{}, spawn: map[string]engine.Spawn{}, out: make(chan engine.Output, 16)}
}

func (f *fakeEngine) Start() error { return nil }
func (f *fakeEngine) Spawn(s engine.Spawn) error {
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wins[s.Slug+"~"+s.Window] = true
	delete(f.dead, s.Slug+"~"+s.Window)
	f.spawn[s.Slug+"~"+s.Window] = s
	f.sizes[s.Slug+"~"+s.Window] = [2]int{s.Width, s.Height}
	f.sent = append(f.sent, "spawn:"+s.Window+":"+s.Command)
	return nil
}
func (f *fakeEngine) Alive(slug, w string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wins[slug+"~"+w]
}
func (f *fakeEngine) rec(s string) error {
	f.mu.Lock()
	f.sent = append(f.sent, s)
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) SendText(slug, w, t string) error { return f.rec("text:" + t) }
func (f *fakeEngine) SendKeys(slug, w string, k ...string) error {
	return f.rec("keys:" + strings.Join(k, ","))
}
func (f *fakeEngine) SendLiteral(slug, w, t string) error { return f.rec("lit:" + t) }
func (f *fakeEngine) SendHex(slug, w string, h []string) error {
	b := make([]byte, 0, len(h))
	for _, x := range h {
		var v byte
		fmt.Sscanf(x, "%02x", &v)
		b = append(b, v)
	}
	return f.rec("hex:" + string(b))
}
func (f *fakeEngine) Capture(slug, w string) (*engine.Screen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines := f.lines
	if lines == nil {
		lines = []string{"x"}
	}
	sc := &engine.Screen{Lines: append([]string(nil), lines...), Width: 80, Height: 24, Dead: f.dead[slug+"~"+w], ExitStatus: -1}
	if e, ok := f.exits[slug+"~"+w]; ok {
		sc.ExitStatus, sc.Signal = e[0], engine.SignalName(strconv.Itoa(e[1]))
	}
	return sc, nil
}
func (f *fakeEngine) Resize(slug, win string, w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.wins[slug+"~"+win] {
		f.sizes[slug+"~"+win] = [2]int{w, h}
	}
	return nil
}
func (f *fakeEngine) size(slug, win string) [2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sizes[slug+"~"+win]
}
func (f *fakeEngine) Kill(slug, w string) error {
	f.mu.Lock()
	delete(f.wins, slug+"~"+w)
	delete(f.dead, slug+"~"+w)
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) KillSession(slug string) error {
	f.mu.Lock()
	for k := range f.wins {
		if strings.HasPrefix(k, slug+"~") {
			delete(f.wins, k)
		}
	}
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) Sessions() (map[string][]string, error) { return map[string][]string{}, nil }
func (f *fakeEngine) Output() <-chan engine.Output           { return f.out }
func (f *fakeEngine) Close()                                 {}
func (f *fakeEngine) KillServer() error                      { return f.rec("killserver") }
func (f *fakeEngine) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.sent, "\n")
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command(args[0], args[1:]...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", args, out)
	}
}

func setup(t *testing.T) (*Daemon, *fakeEngine, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PEKOE_HOME", home)
	t.Setenv("PEKOE_WORKTREES", filepath.Join(home, "wt"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	repo := filepath.Join(t.TempDir(), "app")
	os.MkdirAll(repo, 0o755)
	run(t, repo, "git", "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\n2\n3\n"), 0o644)
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "init")
	d, err := New()
	if err != nil {
		t.Fatal(err)
	}
	fe := newFake()
	d.Eng = fe
	t.Cleanup(func() { d.DB.Close(); d.releaseLock() })
	return d, fe, repo
}

func call[T any](t *testing.T, d *Daemon, method string, params any) T {
	t.Helper()
	b, _ := json.Marshal(params)
	res, err := d.Call(method, b)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var out T
	rb, _ := json.Marshal(res)
	json.Unmarshal(rb, &out)
	return out
}

func event(t *testing.T, d *Daemon, hook, slug string, payload map[string]any) {
	t.Helper()
	call[map[string]any](t, d, "session.event", map[string]any{"hook": hook, "session": slug, "payload": payload})
}

func TestSessionLifecycleAndReview(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	if s.Slug != "main" || s.Port == 0 || s.Managed {
		t.Fatalf("open: %+v", s)
	}
	if !strings.Contains(fe.last(), "spawn:agent:claude") {
		t.Fatalf("agent not spawned: %s", fe.last())
	}
	// hooks drive the state machine
	event(t, d, "SessionStart", "", map[string]any{"session_id": "abc", "cwd": repo})
	event(t, d, "UserPromptSubmit", "", map[string]any{"cwd": repo + "/sub", "prompt": "change line two"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\nTWO\n3\n"), 0o644)
	event(t, d, "PreToolUse", "main", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "pnpm migrate"}})
	event(t, d, "Notification", "main", map[string]any{"notification_type": "permission_prompt", "message": "needs permission"})
	got := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if got.State != state.Waiting || got.WaitKind != "permission" || got.PendingTool.Name != "Bash" || got.ClaudeSessionID != "abc" || got.Prompts != 1 {
		t.Fatalf("waiting: %+v", got)
	}
	call[any](t, d, "session.answer", map[string]any{"slug": "main", "answer": "allow"})
	if !strings.Contains(fe.last(), "lit:1") {
		t.Fatal("allow should type 1 into claude's permission menu")
	}
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(tr, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"changed it"}],"usage":{"input_tokens":10,"cache_read_input_tokens":41000,"output_tokens":5}}}`+"\n"), 0o644)
	stop(t, d, "main", map[string]any{"transcript_path": tr})
	got = call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if got.State != state.Done || got.LastMessage != "changed it" || got.Diffstat.Added != 1 {
		t.Fatalf("done: %+v", got)
	}

	// review: one hunk, attributed to prompt #1
	rv := call[ReviewView](t, d, "review.files", map[string]any{"slug": "main"})
	if rv.Total != 1 || rv.Files[0].Hunks[0].Prompt == nil || rv.Files[0].Hunks[0].Prompt.N != 1 {
		t.Fatalf("review: %+v", rv)
	}
	hid := rv.Files[0].Hunks[0].ID
	call[any](t, d, "review.comment", map[string]any{"slug": "main", "path": "a.txt", "line": 2, "hunk_id": hid, "text": "lowercase please"})
	res := call[map[string]any](t, d, "review.flush", map[string]any{"slug": "main"})
	if res["sent"].(float64) != 1 || !strings.Contains(fe.last(), "a.txt:2") || !strings.Contains(fe.last(), "lowercase please") {
		t.Fatalf("flush: %v / %s", res, fe.last())
	}
	// next round: only changes after review #1
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "fix review"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\ntwo!\n3\n4\n"), 0o644)
	stop(t, d, "main", map[string]any{})
	rv = call[ReviewView](t, d, "review.files", map[string]any{"slug": "main"})
	if rv.BaseLabel != "review #1" || rv.Total != 1 {
		t.Fatalf("diff of diff: %+v", rv)
	}
	for _, f := range rv.Files {
		for _, h := range f.Hunks {
			call[any](t, d, "review.approve", map[string]any{"slug": "main", "hunk_id": h.ID})
		}
	}
	got = call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if got.State != state.Approved {
		t.Fatalf("expected approved, got %s", got.State)
	}
	// undo the last prompt
	call[any](t, d, "session.undo", map[string]any{"slug": "main"})
	b, _ := os.ReadFile(filepath.Join(repo, "a.txt"))
	if string(b) != "1\nTWO\n3\n" {
		t.Fatalf("undo: %q", b)
	}
	// rm never deletes a worktree outside ~/worktrees
	call[any](t, d, "worktree.rm", map[string]any{"slug": "main", "force": true})
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal("repository outside ~/worktrees was deleted")
	}
}

func TestNewRunsSetupAndFailsVisibly(t *testing.T) {
	d, _, repo := setup(t)
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	os.MkdirAll(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos"), 0o755)
	cfg := "[hooks]\nsetup = [\"echo {{slug}}:{{port}} > marker\", \"false\"]\n"
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte(cfg), 0o644)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	var s state.Session
	for i := 0; i < 100; i++ {
		s = call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"})
		if s.State != state.Creating {
			break
		}
		sleep()
	}
	if s.State != state.Failed || !strings.Contains(s.Error, "false") {
		t.Fatalf("setup failure must stop and show: %+v", s)
	}
	b, _ := os.ReadFile(filepath.Join(s.Path, "marker"))
	if strings.TrimSpace(string(b)) != fmt.Sprintf("feat-x:%d", s.Port) {
		t.Fatalf("vars not expanded: %q", b)
	}
	call[any](t, d, "session.cancel", map[string]any{"slug": "feat-x"})
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatal("cancel should remove the worktree")
	}
}

func TestHTTPGuardAndPick(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.token = "secret"
	h := d.guard(d.httpPick)
	body := fmt.Sprintf(`{"url":"http://localhost:%d/x","instruction":"wider","outerHTML":"<b>x</b>","component":{"name":"Card","framework":"react"}}`, s.Port)
	try := func(origin, tok string) int {
		r := httptest.NewRequest("POST", "/pick", strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}
	if try("https://evil.example", "secret") != http.StatusForbidden {
		t.Fatal("web origin accepted")
	}
	if try("chrome-extension://x", "wrong") != http.StatusUnauthorized {
		t.Fatal("bad token accepted")
	}
	if try("chrome-extension://x", "secret") != http.StatusOK {
		t.Fatal("extension rejected")
	}
	if !strings.Contains(fe.last(), "picks/0001.md wider") {
		t.Fatalf("pick not pasted as a short @ line: %s", fe.last())
	}
	pk, _ := d.DB.PickByID(1)
	call[any](t, d, "worktree.rm", map[string]any{"slug": s.Slug, "force": true})
	if left, _ := d.DB.Picks(10); len(left) != 0 {
		t.Fatal("rm must remove the session's picks")
	}
	if _, err := os.Stat(pk.File); !os.IsNotExist(err) {
		t.Fatal("pick file left behind")
	}
}

func TestTouches(t *testing.T) {
	root := t.TempDir()
	in := json.RawMessage(`{"file_path":"` + filepath.Join(root, "src", "a.ts") + `"}`)
	if got := touches("Edit", in, root); got != "src/a.ts" {
		t.Fatalf("touches = %q", got)
	}
	if touches("Bash", json.RawMessage(`{"command":"ls"}`), root) != "" {
		t.Fatal("Bash has no file to show")
	}
}

func TestClassifyNotification(t *testing.T) {
	cases := []struct {
		in   hookPayload
		want string
	}{
		{hookPayload{NotificationType: "permission_prompt"}, "permission"},
		{hookPayload{NotificationType: "idle_prompt"}, "idle"},
		{hookPayload{NotificationType: "elicitation_dialog"}, "question"},
		{hookPayload{Message: "Claude needs your permission to use Bash"}, "permission"},
		{hookPayload{Message: "Claude is waiting for your input"}, "idle"},
	}
	for _, c := range cases {
		if got := classifyNotification(c.in); got != c.want {
			t.Errorf("%+v → %s, want %s", c.in, got, c.want)
		}
	}
}

func sleep() { timeSleep(50) }

// review.ask sends the hunk to the agent; the answer is cut out of the
// transcript at the next Stop hook and emitted as review.answer, and the
// session stays in review.
func TestReviewAsk(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "edit"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\nX\n3\n"), 0o644)
	stop(t, d, "main", map[string]any{})
	rv := call[ReviewView](t, d, "review.files", map[string]any{"slug": "main"})
	hid := rv.Files[0].Hunks[0].ID
	call[any](t, d, "review.start", map[string]any{"slug": "main"})

	// capture events emitted by the daemon
	got := make(chan Event, 8)
	d.testHook = func(ev string, data any) {
		if ev == "review.answer" {
			got <- Event{Event: ev, Data: data}
		}
	}
	call[any](t, d, "review.ask", map[string]any{"slug": "main", "hunk_id": hid})
	if !strings.Contains(fe.last(), "ここの意図は?") || !strings.Contains(fe.last(), "a.txt:L") {
		t.Fatalf("question not sent to the pty: %s", fe.last())
	}
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(tr, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"空行を区切りとして扱うためです"}]}}`+"\n"), 0o644)
	stop(t, d, "main", map[string]any{"transcript_path": tr})
	select {
	case ev := <-got:
		m := ev.Data.(map[string]any)
		if m["hunk_id"] != hid || m["text"] != "空行を区切りとして扱うためです" {
			t.Fatalf("answer: %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no review.answer event")
	}
	s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if s.State != state.Reviewing {
		t.Fatalf("answering a question must keep the session in review, got %s", s.State)
	}
}

// Closing the review surface without sending anything leaves reviewing;
// unsent comments keep the session in review.
func TestReviewEnd(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "edit"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\nX\n3\n"), 0o644)
	stop(t, d, "main", map[string]any{})

	call[any](t, d, "review.start", map[string]any{"slug": "main"})
	call[any](t, d, "review.end", map[string]any{"slug": "main"})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.State != state.Done {
		t.Fatalf("closing an untouched review must go back to done, got %s", s.State)
	}

	call[any](t, d, "review.start", map[string]any{"slug": "main"})
	call[any](t, d, "review.comment", map[string]any{"slug": "main", "path": "a.txt", "line": 2, "text": "why?"})
	call[any](t, d, "review.end", map[string]any{"slug": "main"})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.State != state.Reviewing {
		t.Fatalf("unsent comments must keep the session in review, got %s", s.State)
	}

	// leaving the nvim surface ends the review the same way
	rv := call[ReviewView](t, d, "review.files", map[string]any{"slug": "main"})
	call[any](t, d, "review.comment.delete", map[string]any{"slug": "main", "id": rv.Comments[0].ID})
	call[any](t, d, "window.kill", map[string]any{"slug": "main", "window": "review"})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.State != state.Done {
		t.Fatalf("killing the review window must end the review, got %s", s.State)
	}
}

// stop sends a Stop hook and waits for its (asynchronous) handling.
func stop(t *testing.T, d *Daemon, slug string, payload map[string]any) {
	t.Helper()
	before := call[state.Session](t, d, "session.get", map[string]any{"slug": slug})
	event(t, d, "Stop", slug, payload)
	for i := 0; i < 100; i++ {
		s := call[state.Session](t, d, "session.get", map[string]any{"slug": slug})
		if s.State != before.State || s.State == state.Done || s.State == state.Reviewing && i > 10 {
			if s.State == state.Done || s.State == state.Reviewing {
				time.Sleep(50 * time.Millisecond) // let diffstat settle
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A session id that Claude Code never stored (no prompt was sent) must not
// be passed to --resume: restart falls back to a fresh start.
func TestRestartSkipsUnknownResumeID(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	event(t, d, "SessionStart", "main", map[string]any{"session_id": "never-saved"})
	call[state.Session](t, d, "session.restart", map[string]any{"slug": "main"})
	if strings.Contains(fe.last(), "--resume") {
		t.Fatalf("resumed an id with no conversation: %s", fe.last())
	}
	// with a stored conversation it resumes that id
	dir := claudeProjectDir(realpath(repo)) // pekoe records the resolved worktree path
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "saved-1.jsonl"), []byte("{}\n"), 0o644)
	event(t, d, "SessionStart", "main", map[string]any{"session_id": "saved-1"})
	call[state.Session](t, d, "session.restart", map[string]any{"slug": "main"})
	if !strings.Contains(fe.last(), "--resume saved-1") {
		t.Fatalf("expected resume of the saved conversation: %s", fe.last())
	}
}

func TestNewRefusesRepoWithoutCommits(t *testing.T) {
	d, _, _ := setup(t)
	empty := filepath.Join(t.TempDir(), "empty")
	os.MkdirAll(empty, 0o755)
	run(t, empty, "git", "init", "-q", "-b", "main")
	_, err := d.Call("session.new", mustJSON(map[string]any{"repo": empty, "branch": "feat/x"}))
	if err == nil || !strings.Contains(err.Error(), "no commits yet") {
		t.Fatalf("expected a clear error, got %v", err)
	}
	if ss, _ := d.DB.Sessions(); len(ss) != 0 {
		t.Fatal("no session must be created")
	}
}

// The CLI run by an agent has no terminal and sends 0x0: the agent must then
// start at the TUI's pane size, not a guess the pane would show it narrower
// than (or overflowing) its border.
func TestSpawnSizeFollowsTUIPane(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	if got := fe.size(s.Slug, engine.WinAgent); got != [2]int{120, 40} {
		t.Fatalf("no TUI yet: spawned %v, want the 120x40 fallback", got)
	}
	call[any](t, d, "session.resize", map[string]any{"slug": "*", "width": 161, "height": 57})
	if got := fe.size(s.Slug, engine.WinAgent); got != [2]int{161, 57} {
		t.Fatalf("resize *: %v", got)
	}
	call[any](t, d, "session.restart", map[string]any{"slug": s.Slug})
	if got := fe.size(s.Slug, engine.WinAgent); got != [2]int{161, 57} {
		t.Fatalf("size-less spawn after the TUI reported its pane: %v, want 161x57", got)
	}
	call[any](t, d, "session.restart", map[string]any{"slug": s.Slug, "width": 90, "height": 30})
	if got := fe.size(s.Slug, engine.WinAgent); got != [2]int{90, 30} {
		t.Fatalf("an explicit size wins: %v", got)
	}
}

// exit makes a window's process exit (tmux keeps the pane: remain-on-exit).
func (f *fakeEngine) exit(slug, w string) { f.exitWith(slug, w, 0, 0) }

// exitWith: status -1 with a signal number is how tmux reports a kill.
func (f *fakeEngine) exitWith(slug, w string, status, signal int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.wins, slug+"~"+w)
	f.dead[slug+"~"+w] = true
	f.exits[slug+"~"+w] = [2]int{status, signal}
}

// `!` in the TUI: one command in the worktree with the dev server's
// environment, through the user's shell; one run window per session.
func TestRunWindow(t *testing.T) {
	d, fe, repo := setup(t)
	t.Setenv("SHELL", "/bin/zsh")
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})

	if st := call[RunState](t, d, "run.start", map[string]any{"slug": s.Slug}); st.Running || st.Finished || st.Started || st.Command != "" {
		t.Fatalf("nothing running yet: %+v", st)
	}
	st := call[RunState](t, d, "run.start", map[string]any{"slug": s.Slug, "command": "read -p 'ok? ' x; echo $x", "width": 100, "height": 30})
	if !st.Running || !st.Started {
		t.Fatalf("start: %+v", st)
	}
	sp := fe.spawn[s.Slug+"~"+engine.WinRun]
	if sp.Dir != s.Path || sp.Width != 100 || sp.Height != 30 || !sp.Quiet {
		t.Fatalf("spawn: %+v", sp)
	}
	script := runScript("/bin/zsh", "read -p 'ok? ' x; echo $x", 100, 30)
	if sp.Command != "/bin/sh -c "+shellQuote(script) || !strings.Contains(script, `!= "30 100"`) ||
		!strings.Contains(script, `exec '/bin/zsh' -c 'read -p '\''ok? '\'' x; echo $x'`) {
		t.Fatalf("not through the user's shell: %s", sp.Command)
	}
	env := strings.Join(sp.Env, "\n")
	if !strings.Contains(env, "PEKOE_SESSION="+s.Slug) || !strings.Contains(env, fmt.Sprintf("PEKOE_PORT=%d", s.Port)) {
		t.Fatalf("dev's environment expected: %v", sp.Env)
	}

	// a second ! while it runs starts nothing: the TUI shows the running one
	st = call[RunState](t, d, "run.start", map[string]any{"slug": s.Slug, "command": "echo other"})
	if !st.Running || st.Started || strings.Contains(fe.last(), "echo other") {
		t.Fatalf("second run: %+v\n%s", st, fe.last())
	}
	// finished but not closed yet: still that one (its exit is on screen)
	fe.exit(s.Slug, engine.WinRun)
	st = call[RunState](t, d, "run.start", map[string]any{"slug": s.Slug, "command": "echo other"})
	if !st.Finished || st.Started {
		t.Fatalf("finished run: %+v", st)
	}
	// closed: the next ! runs again
	call[any](t, d, "window.kill", map[string]any{"slug": s.Slug, "window": engine.WinRun})
	if st = call[RunState](t, d, "run.start", map[string]any{"slug": s.Slug, "command": "echo other"}); !st.Started {
		t.Fatalf("after close: %+v", st)
	}
	if _, err := d.Call("run.start", []byte(`{"slug":"nope","command":"ls"}`)); err == nil {
		t.Fatal("unknown session must fail")
	}
}

// review #52: the daemon keeps the command and how it ended, so every TUI
// (and one restarted meanwhile) names the same command and status.
func TestRunStateCarriesCommandAndExit(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	call[RunState](t, d, "run.start", map[string]any{"slug": s.Slug, "command": "make deploy"})
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); got.Run != "make deploy" {
		t.Fatalf("session.run: %q", got.Run)
	}
	// another TUI asks to run ls meanwhile: it gets make deploy back
	st := call[RunState](t, d, "run.start", map[string]any{"slug": s.Slug, "command": "ls"})
	if st.Started || !st.Running || st.Command != "make deploy" || st.ExitStatus != -1 {
		t.Fatalf("running: %+v", st)
	}
	fe.exitWith(s.Slug, engine.WinRun, 3, 0)
	if st = call[RunState](t, d, "run.start", map[string]any{"slug": s.Slug}); !st.Finished || st.Command != "make deploy" || st.ExitStatus != 3 || st.Signal != "" {
		t.Fatalf("finished: %+v", st)
	}
	st = call[RunState](t, d, "run.close", map[string]any{"slug": s.Slug})
	if !st.Finished || st.Command != "make deploy" || st.ExitStatus != 3 || fe.Alive(s.Slug, engine.WinRun) {
		t.Fatalf("close: %+v", st)
	}
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); got.Run != "" {
		t.Fatalf("closed run still recorded: %q", got.Run)
	}
	// Ctrl-C: tmux has no exit status, a signal instead
	call[RunState](t, d, "run.start", map[string]any{"slug": s.Slug, "command": "sleep 100"})
	fe.exitWith(s.Slug, engine.WinRun, -1, 2)
	if st = call[RunState](t, d, "run.close", map[string]any{"slug": s.Slug}); st.Signal != "SIGINT" || st.ExitStatus != -1 || st.Command != "sleep 100" {
		t.Fatalf("interrupted: %+v", st)
	}
	// window.kill of the run window forgets it too
	call[RunState](t, d, "run.start", map[string]any{"slug": s.Slug, "command": "top"})
	call[any](t, d, "window.kill", map[string]any{"slug": s.Slug, "window": engine.WinRun})
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); got.Run != "" {
		t.Fatalf("window.kill kept %q", got.Run)
	}
}

// review #52: never run outside the worktree (tmux starts in $HOME when
// the directory is gone).
func TestRunRefusesWithoutWorktree(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	for _, st := range []state.State{state.Creating, state.Landed} {
		d.update(s.Slug, func(x *state.Session) { x.State = st })
		if _, err := d.Call("run.start", []byte(`{"slug":"`+s.Slug+`","command":"rm -rf build"}`)); err == nil {
			t.Fatalf("%s: run.start accepted", st)
		}
	}
	d.update(s.Slug, func(x *state.Session) { x.State = state.Failed; x.Path = filepath.Join(repo, "gone") })
	if _, err := d.Call("run.start", []byte(`{"slug":"`+s.Slug+`","command":"rm -rf build"}`)); err == nil {
		t.Fatal("missing worktree: run.start accepted")
	}
	if strings.Contains(fe.last(), "spawn:run") {
		t.Fatalf("spawned: %s", fe.last())
	}
}

// review #52: two run.start at once (Enter twice) start one run; the
// second must not kill the first and run the command again.
func TestRunStartSerialized(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	fe.delay = 50 * time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Call("run.start", []byte(`{"slug":"`+s.Slug+`","command":"make install"}`))
		}()
	}
	wg.Wait()
	if n := strings.Count(fe.last(), "spawn:run:"); n != 1 {
		t.Fatalf("%d run windows spawned", n)
	}
}
