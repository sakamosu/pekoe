package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"pekoe/internal/config"
	"pekoe/internal/gitx"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// Hook payloads come from Claude Code on stdin of `pekoe event` (session_id,
// cwd, hook_event_name, …). State is taken from hooks, never from the screen.
type eventP struct {
	Hook    string          `json:"hook"`
	Session string          `json:"session"` // $PEKOE_SESSION of the hook process
	Payload json.RawMessage `json:"payload"`
}

type hookPayload struct {
	SessionID        string          `json:"session_id"`
	TranscriptPath   string          `json:"transcript_path"`
	Cwd              string          `json:"cwd"`
	HookEventName    string          `json:"hook_event_name"`
	Prompt           string          `json:"prompt"`
	Message          string          `json:"message"`
	NotificationType string          `json:"notification_type"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	Source           string          `json:"source"`
	StopHookActive   bool            `json:"stop_hook_active"`
	LastAssistant    string          `json:"last_assistant_message"`
}

// resolveSession maps a hook to a session: $PEKOE_SESSION first, then the
// longest worktree path containing cwd.
// tapi chat (TapiSlug) is matched the same way: by its reserved name, or
// by a cwd inside its working directory.
func (d *Daemon) resolveSession(env, cwd string) string {
	if env == TapiSlug {
		return TapiSlug
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if env != "" {
		if _, ok := d.sessions[env]; ok {
			return env
		}
	}
	if cwd == "" {
		return ""
	}
	cwd = realpath(cwd)
	if inTapiDir(cwd) {
		return TapiSlug
	}
	best, bestLen := "", 0
	for slug, s := range d.sessions {
		p := realpath(s.Path)
		if (cwd == p || strings.HasPrefix(cwd, p+"/")) && len(p) > bestLen {
			best, bestLen = slug, len(p)
		}
	}
	return best
}

// realpath resolves symlinks (macOS /var → /private/var) even when the tail
// of the path does not exist, by resolving the longest existing parent.
func realpath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// touches names the file an editing tool will change, relative to the worktree.
func touches(name string, input json.RawMessage, root string) string {
	switch name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
	default:
		return ""
	}
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	p, _ := m["file_path"].(string)
	if p == "" {
		p, _ = m["notebook_path"].(string)
	}
	if rel, err := filepath.Rel(realpath(root), realpath(p)); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

var (
	summaryBreaks = regexp.MustCompile(`[ \t]*[\r\n][\s]*`)
	summarySpaces = regexp.MustCompile(`[ \t]{3,}`)
)

// summarizeTool is the one-line summary of a tool call that pending_tool
// and last_tool show: the argument that says what it does, with each run
// of line breaks shown as " ⏎ " and long runs of spaces folded, so blank
// lines or padding cannot push the rest out of the first 200 bytes, then
// cut there. Lines / Size / Cut say how much the summary leaves out.
func summarizeTool(name string, input json.RawMessage) state.Tool {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	raw := pick("command", "file_path", "path", "pattern", "url", "query", "description", "prompt")
	t := state.Tool{Name: name}
	if n := strings.Count(strings.TrimRight(raw, "\r\n"), "\n") + 1; n > 1 {
		t.Lines = n
	}
	arg := summaryBreaks.ReplaceAllString(strings.TrimSpace(raw), " ⏎ ")
	arg = summarySpaces.ReplaceAllString(arg, " ")
	if len(arg) > 200 {
		// on a rune boundary: half a character is invalid UTF-8, which JSON
		// turns into U+FFFD and the web UI's prompt check (sameTool) then
		// never matches
		cut := 200
		for cut > 0 && !utf8.RuneStart(arg[cut]) {
			cut--
		}
		arg = arg[:cut] + "…"
		t.Cut, t.Size = true, len(raw)
	}
	t.Input = arg
	return t
}

// hEventAs is session.event: a hook that comes from a pekoe window is that
// window's session, whatever its $PEKOE_SESSION and cwd say. Claude Code
// starts its hooks itself, outside its sandbox, as children of the agent
// process, which is in the window's process tree (caller.go). A hook from
// no pekoe window (an agent pekoe adopted, which runs outside tmux) falls
// back to $PEKOE_SESSION and the cwd, as before.
func (d *Daemon) hEventAs(who *Caller, p eventP) (any, error) {
	pane := who.Pane()
	if pane != "" {
		p.Session = pane
	}
	res, err := d.hEvent(p)
	if pane == TapiSlug && err == nil {
		// a person's request to tapi (ledger.go): only from tapi's own
		// window, never from a caller that just names it
		var hp hookPayload
		_ = json.Unmarshal(p.Payload, &hp)
		if hook := p.Hook; hook == "UserPromptSubmit" || hook == "" && hp.HookEventName == "UserPromptSubmit" {
			d.recordRequest(hp.Prompt)
		}
	}
	return res, err
}

func (d *Daemon) hEvent(p eventP) (any, error) {
	var hp hookPayload
	_ = json.Unmarshal(p.Payload, &hp)
	if !validSessionID(hp.SessionID) {
		// it ends up on a command line (resume): keep only plain ids
		hp.SessionID = ""
	}
	hook := p.Hook
	if hook == "" {
		hook = hp.HookEventName
	}
	slug := d.resolveSession(p.Session, hp.Cwd)
	if slug == "" {
		return map[string]any{"ignored": true}, nil // not a pekoe session
	}
	if slug == TapiSlug {
		d.tapiEvent(hook, hp)
		return map[string]any{"slug": slug}, nil
	}
	switch hook {
	case "SessionStart":
		d.update(slug, func(s *state.Session) {
			if hp.SessionID != "" {
				s.ClaudeSessionID = hp.SessionID
			}
			if s.State == state.Creating || s.State == state.Failed {
				s.State = state.Idle
			}
		})
	case "UserPromptSubmit":
		d.changes.notePrompt(slug)
		d.snapshotPrompt(slug, hp.Prompt)
		d.update(slug, func(s *state.Session) {
			if hp.SessionID != "" {
				s.ClaudeSessionID = hp.SessionID
			}
			if s.State != state.Reviewing {
				s.State = state.Running
			}
			s.WaitKind, s.PendingTool = "", nil
		})
	case "PreToolUse":
		tool := summarizeTool(hp.ToolName, hp.ToolInput)
		d.update(slug, func(s *state.Session) {
			s.LastTool = strings.TrimSpace(hp.ToolName + " " + tool.Input)
			pt := tool
			pt.Touches = touches(hp.ToolName, hp.ToolInput, s.Path)
			pt.AlsoIn = d.alsoChangedLocked(s, pt.Touches)
			s.PendingTool = &pt
			if hp.ToolName == "AskUserQuestion" {
				s.State, s.WaitKind = state.Waiting, "question"
			} else if s.State == state.Waiting || s.State == state.Idle || s.State == state.Done {
				s.State, s.WaitKind = state.Running, ""
			}
		})
	case "PostToolUse":
		d.update(slug, func(s *state.Session) {
			if s.State == state.Waiting {
				s.State, s.WaitKind = state.Running, ""
			}
			s.PendingTool = nil
		})
		if strings.HasPrefix(hp.ToolName, "Edit") || strings.HasPrefix(hp.ToolName, "Write") || hp.ToolName == "MultiEdit" || hp.ToolName == "NotebookEdit" {
			go d.refreshDiffstat(slug)
			d.kickConflicts()
		}
		go d.readTokens(slug, hp.TranscriptPath)
	case "Notification":
		kind := classifyNotification(hp)
		if kind == "" {
			break
		}
		d.update(slug, func(s *state.Session) {
			if kind == "idle" {
				if s.State == state.Running {
					s.State = state.Idle
				}
				return
			}
			if kind == "permission" && askingQuestion(s) {
				// Claude Code shows AskUserQuestion through its permission
				// dialog, so a permission_prompt follows the PreToolUse.
				// Keep it a question so the web keypad can answer it.
				kind = "question"
			}
			s.State, s.WaitKind = state.Waiting, kind
			s.LastMessage = hp.Message
		})
	case "Stop", "SubagentStop":
		if hook == "SubagentStop" {
			break
		}
		go d.onStop(slug, hp)
	case "SessionEnd":
		d.update(slug, func(s *state.Session) {
			if s.State == state.Running || s.State == state.Waiting {
				s.State = state.Idle
			}
		})
	}
	return map[string]any{"slug": slug}, nil
}

// askingQuestion reports whether s is still in the AskUserQuestion that its
// last PreToolUse started. It is deliberately narrow: a "question" lets the
// web keypad type digits and arrows into the agent, and on a real permission
// menu that could pick "Yes, and don't ask again". Any other tool's
// PreToolUse replaces PendingTool (and leaves the Waiting state), so its
// permission prompt stays a permission.
func askingQuestion(s *state.Session) bool {
	return s.State == state.Waiting && s.WaitKind == "question" &&
		s.PendingTool != nil && s.PendingTool.Name == "AskUserQuestion"
}

// classifyNotification separates permission prompts from questions.
// Claude Code sends notification_type (permission_prompt, idle_prompt,
// elicitation_dialog); older versions only a message.
//
// Anything it does not recognize is a permission: a "question" lets the web
// keypad press digits and arrows, which on a permission menu could pick
// "don't ask again", while a permission only takes allow/deny. The one
// question a permission_prompt stands for (AskUserQuestion) is kept a
// question by askingQuestion, which also covers unknown types.
func classifyNotification(hp hookPayload) string {
	switch hp.NotificationType {
	case "permission_prompt":
		return "permission"
	case "idle_prompt":
		return "idle"
	case "elicitation_dialog":
		return "question"
	case "auth_success":
		return ""
	case "":
	default:
		return "permission"
	}
	m := strings.ToLower(hp.Message)
	switch {
	case strings.Contains(m, "permission"), strings.Contains(m, "approve"), strings.Contains(m, "allow"):
		return "permission"
	case strings.Contains(m, "waiting for your input"):
		return "idle"
	}
	return "permission"
}

func (d *Daemon) snapshotPrompt(slug, prompt string) {
	s, err := d.snapshot(slug)
	if err != nil {
		return
	}
	n := s.Prompts + 1
	sha, err := gitx.SnapshotCommit(s.Path, fmt.Sprintf("pekoe prompt #%d\n\n%s", n, prompt))
	if err != nil {
		log.Printf("snapshot %s: %v", slug, err)
		return
	}
	ref := fmt.Sprintf("refs/pekoe/%s/prompt-%d", slug, n)
	if err := gitx.UpdateRef(s.Path, ref, sha); err != nil {
		log.Printf("update-ref: %v", err)
		return
	}
	_ = d.DB.AddPrompt(slug, n, prompt, ref)
	d.update(slug, func(x *state.Session) { x.Prompts = n })
}

// stopMessage gets the final reply: from the hook input when Claude Code
// provides it, else from the transcript, which may lag the Stop hook a bit.
func stopMessage(hp hookPayload) string {
	if hp.LastAssistant != "" {
		return strings.TrimSpace(hp.LastAssistant)
	}
	for i := 0; i < 8; i++ {
		if m := lastAssistantText(hp.TranscriptPath); m != "" {
			return m
		}
		if hp.TranscriptPath == "" {
			return ""
		}
		time.Sleep(250 * time.Millisecond)
	}
	return ""
}

func (d *Daemon) onStop(slug string, hp hookPayload) {
	msg := stopMessage(hp)
	d.mu.Lock()
	hunk, asking := d.asks[slug]
	delete(d.asks, slug)
	d.mu.Unlock()
	if asking {
		d.emit("review.answer", map[string]any{"slug": slug, "hunk_id": hunk, "text": msg})
	}
	s, _ := d.update(slug, func(s *state.Session) {
		if hp.SessionID != "" {
			s.ClaudeSessionID = hp.SessionID
		}
		if msg != "" {
			s.LastMessage = msg
		}
		if asking && s.State == state.Reviewing {
			return // answering a review question keeps us in review
		}
		s.State, s.WaitKind, s.PendingTool = state.Done, "", nil
	})
	go d.readTokens(slug, hp.TranscriptPath)
	d.refreshDiffstat(slug)
	if s != nil && !asking {
		go d.runTest(slug)
		go d.runGatekeeper(slug)
	}
}

// lastAssistantText reads the final assistant message from a transcript.
func lastAssistantText(path string) string {
	b := tailFile(path, 512*1024)
	if b == nil {
		return ""
	}
	lines := bytes.Split(b, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var e struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &e) != nil || e.Type != "assistant" {
			continue
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(e.Message.Content, &parts) == nil {
			var sb strings.Builder
			for _, p := range parts {
				if p.Type == "text" {
					sb.WriteString(p.Text)
				}
			}
			if sb.Len() > 0 {
				return strings.TrimSpace(sb.String())
			}
			continue
		}
		var s string
		if json.Unmarshal(e.Message.Content, &s) == nil && s != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// tailFile reads the last n bytes of a transcript. The path comes from a
// hook payload, which any process of the user can send (session.event
// needs no token), so only a regular file is read, and at most n bytes:
// /dev/zero would never end, and a FIFO would block the open (hence
// O_NONBLOCK, which a regular file ignores).
func tailFile(path string, n int64) []byte {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	off := fi.Size() - n
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(f, n))
	if off > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return b
}

// readTokens takes the context size from the last assistant usage record.
func (d *Daemon) readTokens(slug, path string) {
	b := tailFile(path, 256*1024)
	if b == nil {
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	tokens := 0
	for sc.Scan() {
		var e struct {
			Message struct {
				Usage struct {
					In     int `json:"input_tokens"`
					Out    int `json:"output_tokens"`
					CacheR int `json:"cache_read_input_tokens"`
					CacheC int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			u := e.Message.Usage
			if t := u.In + u.Out + u.CacheR + u.CacheC; t > 0 {
				tokens = t
			}
		}
	}
	if tokens > 0 {
		d.update(slug, func(s *state.Session) { s.Tokens = tokens })
	}
}

// ---- base / diffstat ----

// baseCommit is where review starts: the last review point, else merge-base.
func (d *Daemon) baseCommit(s *state.Session, since int) (sha, label string) {
	if since < 0 {
		since = s.ReviewN
	}
	if since > 0 {
		if ref, ok := d.DB.ReviewRef(s.Slug, since); ok {
			if sha, err := gitx.RevParse(s.Path, ref); err == nil {
				return sha, fmt.Sprintf("review #%d", since)
			}
		}
	}
	base := s.Base
	if base == "" {
		base = gitx.DefaultBase(s.Path)
	}
	for _, b := range []string{"origin/" + base, base} {
		if s.PRReview && !strings.HasPrefix(b, "origin/") {
			continue
		}
		if mb, err := gitx.MergeBase(s.Path, b, "HEAD"); err == nil && mb != "" {
			return mb, base
		}
	}
	if h, err := gitx.RevParse(s.Path, "HEAD"); err == nil {
		return h, "HEAD"
	}
	return "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "empty" // empty tree
}

func (d *Daemon) refreshDiffstat(slug string) {
	d.refreshBranch(slug)
	s, err := d.snapshot(slug)
	if err != nil {
		return
	}
	if !d.checkGitDir(s) {
		return
	}
	base, _ := d.baseCommit(s, 0)
	tree, err := gitx.SnapshotTree(s.Path)
	if err != nil {
		return
	}
	files, err := gitx.Diff(s.Path, base, tree)
	if err != nil {
		return
	}
	st := gitx.DiffStat(files)
	d.update(slug, func(x *state.Session) {
		x.Diffstat = state.Stat{Added: st.Added, Deleted: st.Deleted, Files: st.Files}
	})
}

// checkGitDir looks at the session's worktree's .git file (gitx.CheckPin)
// and keeps git_blocked in step: set while it points somewhere else than
// the repository's entry for the worktree (pekoed runs no git there then),
// cleared once it points back. It runs with every diffstat (each edit and
// Stop) and on restore. false: git is blocked.
func (d *Daemon) checkGitDir(s *state.Session) bool {
	msg := ""
	if err := gitx.CheckPin(s.Path); errors.Is(err, gitx.ErrTampered) {
		msg = err.Error()
	}
	if msg != s.GitBlocked {
		if msg != "" {
			log.Printf("%s: %s", s.Slug, msg)
		}
		d.update(s.Slug, func(x *state.Session) { x.GitBlocked = msg })
	}
	return msg == ""
}

// refreshBranch re-reads the branch name of a session whose stored name is
// not a real branch. Sessions adopted on an unborn branch (before this was
// fixed) or on a detached HEAD were saved as "HEAD"; once the worktree is on a
// branch, show that. A real name is never replaced, so a detached HEAD in the
// middle of a rebase cannot clobber it.
func (d *Daemon) refreshBranch(slug string) {
	s, err := d.snapshot(slug)
	if err != nil || (s.Branch != "HEAD" && s.Branch != "") {
		return
	}
	b := gitx.CurrentBranch(s.Path)
	if b == "" || b == "HEAD" || gitx.CheckBranchName(b) != nil {
		// a name the agent picked in the worktree goes into [hooks]
		// commands as {{branch}}: the same rule as session.new
		return
	}
	d.update(slug, func(x *state.Session) {
		if x.Branch == "HEAD" || x.Branch == "" {
			x.Branch = b
		}
	})
}

// ---- gate before the human: tests and the risk lister ----

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*(\x07|\x1b\\)`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

var countRe = regexp.MustCompile(`(?i)(\d+)\s+(passed|passing|failed|failing|tests?)`)

func summarize(out string, ok bool) string {
	out = stripANSI(out)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-30; i-- {
		if countRe.MatchString(lines[i]) {
			return truncate(strings.TrimSpace(lines[i]), 60)
		}
	}
	if ok {
		return "passed"
	}
	if len(lines) > 0 {
		return truncate(strings.TrimSpace(lines[len(lines)-1]), 60)
	}
	return "failed"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (d *Daemon) repoConfig(s *state.Session) *config.Repo {
	cfg, _, err := config.LoadRepo(s.RepoID)
	if err != nil || cfg == nil {
		return &config.Repo{}
	}
	return cfg
}

func (d *Daemon) vars(s *state.Session) config.Vars {
	return config.Vars{Slug: s.Slug, Branch: s.Branch, Path: s.Path, Repo: s.Repo, Port: s.Port}
}

// testRuns tracks the [hooks] test run of each session.
type testRuns struct {
	mu sync.Mutex
	m  map[string]*testRun
}

type testRun struct{ cancel context.CancelFunc }

// start registers a new run for slug, cancelling the one before it.
func (r *testRuns) start(slug string) (context.Context, *testRun) {
	ctx, cancel := context.WithCancel(context.Background())
	run := &testRun{cancel: cancel}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*testRun{}
	}
	if old := r.m[slug]; old != nil {
		old.cancel()
	}
	r.m[slug] = run
	return ctx, run
}

// finish ends a run and reports whether it is still the newest one.
func (r *testRuns) finish(slug string, run *testRun) bool {
	run.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m[slug] != run {
		return false
	}
	delete(r.m, slug)
	return true
}

func (d *Daemon) runTest(slug string) {
	s, err := d.snapshot(slug)
	if err != nil {
		return
	}
	cfg := d.repoConfig(s)
	if cfg.Hooks.Test == "" {
		return
	}
	cmd := d.vars(s).ExpandShell(cfg.Hooks.Test)
	// no sandbox: not run at all, and the session says why (anyone can
	// send a Stop, so this would run the worktree's code at will)
	if err := d.sbx.Check(); err != nil {
		log.Printf("test %s: %v", slug, err)
		d.update(slug, func(x *state.Session) {
			x.Test = state.Test{Status: "blocked", Summary: err.Error(), At: time.Now()}
		})
		return
	}
	// a newer Stop's run supersedes this one: stop the older run, and drop
	// its result (it tested code that has changed since)
	ctx, run := d.tests.start(slug)
	d.update(slug, func(x *state.Session) { x.Test = state.Test{Status: "running", At: time.Now()} })
	out, err := d.shell(ctx, s.Path, s.Env, cmd, 20*time.Minute)
	if !d.tests.finish(slug, run) {
		return
	}
	logDir := filepath.Join(paths.DataDir(), s.RepoID, "logs")
	_ = os.MkdirAll(logDir, paths.DirPerm)
	_ = os.WriteFile(filepath.Join(logDir, slug+"-test.log"), []byte(out), paths.FilePerm)
	t := state.Test{Status: "pass", Summary: summarize(out, err == nil), At: time.Now()}
	if err != nil {
		t.Status = "fail"
	}
	d.update(slug, func(x *state.Session) { x.Test = t })
}

var riskRe = regexp.MustCompile(`^\s*[-*]?\s*([^\s:]+):(\d+):?\s*(.*)$`)

func (d *Daemon) runGatekeeper(slug string) {
	s, err := d.snapshot(slug)
	if err != nil {
		return
	}
	cfg := d.repoConfig(s)
	if cfg.Review.Gatekeeper == "" {
		return
	}
	base, _ := d.baseCommit(s, 0)
	tree, err := gitx.SnapshotTree(s.Path)
	if err != nil {
		return
	}
	diff, err := gitx.RunRaw(context.Background(), s.Path, nil, nil, "diff", "--no-color", "--end-of-options", base, tree)
	if err != nil || len(diff) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// in the sandbox: the gatekeeper runs in the worktree, where an agent
	// can leave files that it reads or runs (CLAUDE.md, .claude/settings.json)
	argv, err := d.sbx.Wrap([]string{userShell(), "-c", d.vars(s).ExpandShell(cfg.Review.Gatekeeper)})
	if err != nil {
		log.Printf("gatekeeper %s: %v", slug, err)
		_ = d.DB.SetRisks(slug, []state.Risk{{Text: "gatekeeper " + err.Error()}})
		d.emit("review.updated", map[string]any{"slug": slug})
		return
	}
	c := execCommand(ctx, argv[0], argv[1:]...)
	c.Dir = s.Path
	c.Stdin = bytes.NewReader(diff)
	out, err := c.Output()
	if err != nil {
		log.Printf("gatekeeper %s: %v", slug, err)
	}
	var risks []state.Risk
	for _, ln := range strings.Split(string(out), "\n") {
		if m := riskRe.FindStringSubmatch(ln); m != nil {
			n, _ := strconv.Atoi(m[2])
			risks = append(risks, state.Risk{Path: m[1], Line: n, Text: m[3]})
		}
	}
	_ = d.DB.SetRisks(slug, risks)
	d.emit("review.updated", map[string]any{"slug": slug})
}
