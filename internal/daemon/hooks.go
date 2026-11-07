package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"boba/internal/config"
	"boba/internal/gitx"
	"boba/internal/paths"
	"boba/internal/state"
)

// Hook payloads come from Claude Code on stdin of `boba event` (session_id,
// cwd, hook_event_name, …). State is taken from hooks, never from the screen.
type eventP struct {
	Hook    string          `json:"hook"`
	Session string          `json:"session"` // $BOBA_SESSION of the hook process
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

// resolveSession maps a hook to a session: $BOBA_SESSION first, then the
// longest worktree path containing cwd.
func (d *Daemon) resolveSession(env, cwd string) string {
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

func toolSummary(name string, input json.RawMessage) string {
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
	arg := pick("command", "file_path", "path", "pattern", "url", "query", "description", "prompt")
	arg = strings.ReplaceAll(arg, "\n", " ")
	if len(arg) > 200 {
		arg = arg[:200] + "…"
	}
	return arg
}

func (d *Daemon) hEvent(p eventP) (any, error) {
	var hp hookPayload
	_ = json.Unmarshal(p.Payload, &hp)
	hook := p.Hook
	if hook == "" {
		hook = hp.HookEventName
	}
	slug := d.resolveSession(p.Session, hp.Cwd)
	if slug == "" {
		return map[string]any{"ignored": true}, nil // not a boba session
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
		sum := toolSummary(hp.ToolName, hp.ToolInput)
		d.update(slug, func(s *state.Session) {
			s.LastTool = strings.TrimSpace(hp.ToolName + " " + sum)
			t := touches(hp.ToolName, hp.ToolInput, s.Path)
			s.PendingTool = &state.Tool{Name: hp.ToolName, Input: sum, Touches: t, AlsoIn: d.alsoChangedLocked(s, t)}
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

// classifyNotification separates permission prompts from questions.
// Claude Code sends notification_type (permission_prompt, idle_prompt,
// elicitation_dialog); older versions only a message.
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
	}
	m := strings.ToLower(hp.Message)
	switch {
	case strings.Contains(m, "permission"), strings.Contains(m, "approve"), strings.Contains(m, "allow"):
		return "permission"
	case strings.Contains(m, "waiting for your input"):
		return "idle"
	}
	return "question"
}

func (d *Daemon) snapshotPrompt(slug, prompt string) {
	s, err := d.snapshot(slug)
	if err != nil {
		return
	}
	n := s.Prompts + 1
	sha, err := gitx.SnapshotCommit(s.Path, fmt.Sprintf("boba prompt #%d\n\n%s", n, prompt))
	if err != nil {
		log.Printf("snapshot %s: %v", slug, err)
		return
	}
	ref := fmt.Sprintf("refs/boba/%s/prompt-%d", slug, n)
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

func tailFile(path string, n int64) []byte {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	off := fi.Size() - n
	if off < 0 {
		off = 0
	}
	f.Seek(off, io.SeekStart)
	b, _ := io.ReadAll(f)
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
	s, err := d.snapshot(slug)
	if err != nil {
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

func (d *Daemon) runTest(slug string) {
	s, err := d.snapshot(slug)
	if err != nil {
		return
	}
	cfg := d.repoConfig(s)
	if cfg.Hooks.Test == "" {
		return
	}
	cmd := d.vars(s).Expand(cfg.Hooks.Test)
	d.update(slug, func(x *state.Session) { x.Test = state.Test{Status: "running", At: time.Now()} })
	out, err := shell(context.Background(), s.Path, s.Env, cmd, 20*time.Minute)
	logDir := filepath.Join(paths.DataDir(), s.RepoID, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	_ = os.WriteFile(filepath.Join(logDir, slug+"-test.log"), []byte(out), 0o644)
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
	diff, err := gitx.RunRaw(context.Background(), s.Path, nil, nil, "diff", "--no-color", base, tree)
	if err != nil || len(diff) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	c := execCommand(ctx, sh, "-c", d.vars(s).Expand(cfg.Review.Gatekeeper))
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

// ---- notifications (ntfy) ----

func (d *Daemon) notify(s *state.Session) {
	url := d.Global.Notify.Ntfy
	if url == "" {
		return
	}
	body := s.Slug + " is waiting"
	if s.PendingTool != nil {
		body += ": " + s.PendingTool.Name + " " + s.PendingTool.Input
	} else if s.LastMessage != "" {
		body += ": " + truncate(s.LastMessage, 200)
	}
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Title", "boba · "+s.WaitKind)
	req.Header.Set("Tags", "hourglass")
	cl := &http.Client{Timeout: 5 * time.Second}
	if resp, err := cl.Do(req); err == nil {
		resp.Body.Close()
	}
}
