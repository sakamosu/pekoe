package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/config"
	"pekoe/internal/engine"
	"pekoe/internal/gitx"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// ---- session bookkeeping ----

func (d *Daemon) get(slug string) (*state.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[slug]
	if !ok {
		return nil, fmt.Errorf("no such session: %s", slug)
	}
	return s, nil
}

// snapshot returns a copy safe to marshal outside the lock.
func (d *Daemon) snapshot(slug string) (*state.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[slug]
	if !ok {
		return nil, fmt.Errorf("no such session: %s", slug)
	}
	return d.copyLocked(s), nil
}

// copyLocked copies s with its live counters filled in (under mu).
func (d *Daemon) copyLocked(s *state.Session) *state.Session {
	c := *s
	c.Activity = d.activityLocked(s.Slug)
	if o := d.out[s.Slug]; o != nil {
		c.Unread = o.unread
	}
	return &c
}

// persist writes the session to state.db, but only while it is still the one
// in memory. The write happens outside mu (no disk I/O under the daemon-wide
// lock), so without this check a save that copied the session just before
// session removal could land after DeleteSession and bring a removed
// session back on the next restart. saveMu orders persist against forget:
// either the save finishes before the delete, or it sees the session gone.
// It also makes concurrent saves of one slug write the newest copy last.
func (d *Daemon) persist(s *state.Session) {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	if d.sessions[s.Slug] != s { // removed (or replaced by a new session of the same slug)
		d.mu.Unlock()
		return
	}
	c := d.copyLocked(s)
	d.mu.Unlock()
	if err := d.DB.SaveSession(c); err != nil {
		log.Printf("save %s: %v", s.Slug, err)
	}
}

// forget drops a session from memory and from state.db as one step with
// respect to persist (see there).
func (d *Daemon) forget(slug string) {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	delete(d.sessions, slug)
	delete(d.out, slug)
	delete(d.progress, slug)
	delete(d.prFetched, slug)
	d.mu.Unlock()
	if err := d.DB.DeleteSession(slug); err != nil {
		log.Printf("delete %s: %v", slug, err)
	}
}

// update mutates a session under the lock, persists it and emits events.
func (d *Daemon) update(slug string, f func(s *state.Session)) (*state.Session, error) {
	d.mu.Lock()
	s, ok := d.sessions[slug]
	if !ok {
		d.mu.Unlock()
		return nil, fmt.Errorf("no such session: %s", slug)
	}
	prev := s.State
	f(s)
	if s.State != prev {
		s.StateSince = time.Now()
	}
	c := d.copyLocked(s)
	d.mu.Unlock()
	d.persist(s)
	d.emit("session.updated", c)
	if c.State != prev {
		switch c.State {
		case state.Waiting:
			d.emit("session.waiting", c)
			go d.notify(c)
		case state.Done:
			d.emit("session.done", c)
		}
		d.previewOnState(c, prev)
	}
	return c, nil
}

func (d *Daemon) setState(slug string, st state.State, waitKind string) {
	d.update(slug, func(s *state.Session) {
		s.State = st
		if st == state.Waiting {
			s.WaitKind = waitKind
		} else {
			s.WaitKind = ""
			if st != state.Waiting {
				s.PendingTool = nil
			}
		}
	})
}

func (d *Daemon) add(s *state.Session) {
	d.mu.Lock()
	d.sessions[s.Slug] = s
	if d.out[s.Slug] == nil {
		d.out[s.Slug] = &outState{}
	}
	c := *s
	d.mu.Unlock()
	d.persist(s)
	d.emit("session.created", &c)
}

type listP struct {
	RepoID string `json:"repo_id"`
}

func (d *Daemon) hList(p listP) (any, error) {
	d.mu.Lock()
	slugs := make([]string, 0, len(d.sessions))
	for k, s := range d.sessions {
		if p.RepoID == "" || s.RepoID == p.RepoID {
			slugs = append(slugs, k)
		}
	}
	d.mu.Unlock()
	out := make([]*state.Session, 0, len(slugs))
	for _, k := range slugs {
		if s, err := d.snapshot(k); err == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

// ---- repo resolution ----

type repoInfo struct {
	Dir    string // main worktree
	ID     string
	Name   string
	Config *config.Repo
}

func resolveRepo(dir string) (*repoInfo, error) {
	if dir == "" {
		return nil, errors.New("repo path required")
	}
	top, err := gitx.Toplevel(dir)
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git repository", dir)
	}
	main, err := gitx.MainWorktree(top)
	if err != nil {
		main = top
	}
	id, err := gitx.RepoID(main)
	if err != nil {
		return nil, err
	}
	cfg, _, err := config.LoadRepo(id)
	if err != nil {
		return nil, err
	}
	return &repoInfo{Dir: main, ID: id, Name: gitx.RepoName(main), Config: cfg}, nil
}

func portBusy(p int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
	if err != nil {
		return true
	}
	l.Close()
	return false
}

// uniqueSlug picks the slug for a new session of a repository: base when it
// is free or already this repository's; when another repository has it,
// "<name>-<base>" (app-feat-x), and then -2, -3 … as before.
func (d *Daemon) uniqueSlug(base, repoID, repoName string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	free := func(slug string) bool {
		s, ok := d.sessions[slug]
		return !ok || s.RepoID == repoID
	}
	if free(base) {
		return base
	}
	first := base
	if repoName != "" {
		first = paths.Slug(repoName) + "-" + base
	}
	slug := first
	for i := 2; !free(slug); i++ {
		slug = fmt.Sprintf("%s-%d", first, i)
	}
	return slug
}

// ---- new: worktree → copy → setup → port → start ----

type Step struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Status   string `json:"status"` // pending running ok fail
	Detail   string `json:"detail"`
	Progress int    `json:"progress,omitempty"` // 0-99 while running, from the last run's duration
	Expected int64  `json:"expected_ms,omitempty"`
	Elapsed  int64  `json:"elapsed_ms"`
}

type newP struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	From   string `json:"from"` // gh#123
	Agent  string `json:"agent"`
	Prompt string `json:"prompt"`
	Base   string `json:"base"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func stepLabel(cmd string) string {
	l := strings.ToLower(cmd)
	switch {
	case strings.Contains(l, "install"):
		return "install"
	case strings.Contains(l, "migrat"):
		return "migrate"
	case strings.Contains(l, "createdb"), strings.Contains(l, "db:"):
		return "db"
	case strings.Contains(l, "direnv"):
		return "direnv"
	}
	return "setup"
}

func (d *Daemon) hNew(p newP) (any, error) {
	if p.Branch == "" {
		return nil, errors.New("branch required")
	}
	repo, err := resolveRepo(p.Repo)
	if err != nil {
		return nil, err
	}
	d.resolved(repo)
	if !gitx.HasHead(repo.Dir) {
		// a worktree needs a commit to start from
		return nil, fmt.Errorf("%s has no commits yet: make a first commit (git commit) before creating a session", repo.Dir)
	}
	slug := d.uniqueSlug(paths.Slug(p.Branch), repo.ID, repo.Name)
	if s, err := d.get(slug); err == nil && s.State != state.Landed {
		return nil, fmt.Errorf("session %s already exists", slug)
	}
	agent := p.Agent
	if agent == "" {
		agent = repo.Config.Agent
	}
	if agent == "" {
		agent = "claude"
	}
	if _, ok := d.Global.Agents[agent]; !ok {
		return nil, fmt.Errorf("unknown agent: %s", agent)
	}
	base := p.Base
	if base == "" {
		base = repo.Config.Worktree.Base
	}
	if base == "" {
		base = gitx.DefaultBase(repo.Dir)
	}
	port, err := d.DB.AllocPort(repo.ID, slug, d.Global.Ports.Base, portBusy)
	if err != nil {
		return nil, err
	}
	s := &state.Session{
		Slug: slug, Branch: p.Branch, Repo: repo.Name, RepoID: repo.ID, RepoDir: repo.Dir,
		Path: paths.Worktree(repo.Name, slug), Agent: agent, State: state.Creating,
		StateSince: time.Now(), CreatedAt: time.Now(), Port: port, Managed: true, Base: base,
		Test: state.Test{Status: "none"}, Issue: issueNumber(p.From),
	}
	d.add(s)
	steps := []Step{
		{Name: "worktree", Label: "worktree", Detail: s.Path},
		{Name: "env", Label: "env", Detail: strings.Join(append(repo.Config.Worktree.Copy, repo.Config.Worktree.Clone...), " ")},
		{Name: "port", Label: "port", Detail: fmt.Sprintf(":%d", port)},
	}
	dv := config.Vars{Slug: slug, Branch: p.Branch, Path: s.Path, Repo: repo.Name, Port: port}
	for i, c := range repo.Config.Hooks.Setup {
		steps = append(steps, Step{Name: fmt.Sprintf("setup%d", i), Label: stepLabel(c), Detail: dv.Expand(c)})
	}
	steps = append(steps, Step{Name: "start", Label: "start", Detail: agent + " · new session · snapshot #0"})
	for i := range steps {
		steps[i].Status = "pending"
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.progress[slug] = steps
	d.cancels[slug] = cancel
	d.mu.Unlock()
	go d.runNew(ctx, s.Slug, repo, p)
	return d.snapshot(slug)
}

func (d *Daemon) stepUpdate(slug string, i int, status, detail string, started time.Time) {
	d.mu.Lock()
	steps := d.progress[slug]
	if i < len(steps) {
		steps[i].Status = status
		if detail != "" {
			steps[i].Detail = detail
		}
		if !started.IsZero() {
			steps[i].Elapsed = time.Since(started).Milliseconds()
		}
	}
	cp := append([]Step(nil), steps...)
	d.mu.Unlock()
	d.emit("session.progress", map[string]any{"slug": slug, "steps": cp})
}

// gauge reports progress of a running step against its last duration
// (capped at 99% until the step really ends).
func (d *Daemon) gauge(slug string, i int, t0 time.Time, prev time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		pct := int(time.Since(t0) * 100 / prev)
		if pct > 99 {
			pct = 99
		}
		d.mu.Lock()
		if steps := d.progress[slug]; i < len(steps) && steps[i].Status == "running" {
			steps[i].Progress = pct
			steps[i].Elapsed = time.Since(t0).Milliseconds()
			steps[i].Expected = prev.Milliseconds()
		}
		cp := append([]Step(nil), d.progress[slug]...)
		d.mu.Unlock()
		d.emit("session.progress", map[string]any{"slug": slug, "steps": cp})
	}
}

func (d *Daemon) failNew(slug string, i int, err error, started time.Time) {
	d.stepUpdate(slug, i, "fail", "", started)
	d.update(slug, func(s *state.Session) {
		s.State = state.Failed
		s.Error = err.Error()
	})
}

func (d *Daemon) runNew(ctx context.Context, slug string, repo *repoInfo, p newP) {
	defer func() {
		d.mu.Lock()
		delete(d.cancels, slug)
		d.mu.Unlock()
	}()
	s, _ := d.snapshot(slug)
	vars := config.Vars{Slug: slug, Branch: s.Branch, Path: s.Path, Repo: repo.Name, Port: s.Port}
	i := 0
	t0 := time.Now()
	// durKey remembers how long a step took last time in this repository, so
	// a slow step can show a gauge based on a real value (mock 04).
	durKey := func(name string) string { return "dur:" + repo.ID + ":" + name }
	run := func(name string, f func() error) bool {
		t0 = time.Now()
		d.stepUpdate(slug, i, "running", "", time.Time{})
		if ctx.Err() != nil {
			return false
		}
		stopGauge := make(chan struct{})
		if prev, _ := time.ParseDuration(d.DB.Get(durKey(name))); prev >= 2*time.Second {
			go d.gauge(slug, i, t0, prev, stopGauge)
		}
		err := f()
		close(stopGauge)
		if err != nil {
			d.failNew(slug, i, fmt.Errorf("%s: %w", name, err), t0)
			return false
		}
		if ctx.Err() != nil {
			return false
		}
		_ = d.DB.Set(durKey(name), time.Since(t0).Round(100*time.Millisecond).String())
		d.stepUpdate(slug, i, "ok", "", t0)
		i++
		return true
	}
	if !run("worktree", func() error {
		if _, err := os.Stat(s.Path); err == nil {
			// an existing worktree for this branch: reuse it, but only when
			// it is this repository's (a fork shares ~/worktrees/<name>)
			return reusableWorktree(repo.Dir, s.Path)
		}
		// whatever is at s.Path from here on is ours, even if git stops
		// half way: rm may delete it
		d.update(slug, func(x *state.Session) { x.OwnDir = true })
		return gitx.AddWorktree(repo.Dir, s.Path, s.Branch, s.Base)
	}) {
		return
	}
	if !run("env", func() error { return copyIgnored(repo.Dir, s.Path, repo.Config.Worktree) }) {
		return
	}
	if !run("port", func() error { return nil }) {
		return
	}
	env := append(repo.Config.EnvList(vars), "PEKOE_SESSION="+slug, "PEKOE_PORT="+strconv.Itoa(s.Port))
	for _, c := range repo.Config.Hooks.Setup {
		cmd := vars.Expand(c)
		if !run("setup:"+c, func() error {
			out, err := shell(ctx, s.Path, env, cmd, 30*time.Minute)
			if err != nil {
				return fmt.Errorf("%v\n%s", err, tail(out, 20))
			}
			return nil
		}) {
			return
		}
	}
	prompt := p.Prompt
	if strings.HasPrefix(p.From, "gh#") {
		if body, err := issuePrompt(repo.Dir, strings.TrimPrefix(p.From, "gh#")); err == nil {
			if prompt != "" {
				prompt = body + "\n\n" + prompt
			} else {
				prompt = body
			}
		} else {
			log.Printf("gh issue: %v", err)
		}
	}
	run("start", func() error {
		if sha, err := gitx.SnapshotCommit(s.Path, "pekoe snapshot #0"); err == nil {
			ref := fmt.Sprintf("refs/pekoe/%s/prompt-0", slug)
			_ = gitx.UpdateRef(s.Path, ref, sha)
			_ = d.DB.AddPrompt(slug, 0, "(start)", ref)
		}
		d.update(slug, func(x *state.Session) { x.Env = env })
		return d.spawnAgent(slug, prompt, "", p.Width, p.Height)
	})
}

// reusableWorktree accepts an existing directory as a session's worktree only
// when it is a worktree of the repository at repoDir.
func reusableWorktree(repoDir, path string) error {
	switch err := gitx.CheckWorktreeOf(repoDir, path); {
	case err == nil:
		return nil
	case errors.Is(err, gitx.ErrForeignWorktree):
		return fmt.Errorf("%s already exists as a worktree of another repository; not reusing it", path)
	case errors.Is(err, gitx.ErrNotWorktree):
		return fmt.Errorf("%s exists and is not a worktree", path)
	default:
		return err
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// shell runs a command line through sh -c (design: ただのシェル).
func shell(ctx context.Context, dir string, env []string, cmdline string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	c := exec.CommandContext(ctx, sh, "-c", cmdline)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	out, err := c.CombinedOutput()
	return string(out), err
}

func issuePrompt(dir, n string) (string, error) {
	c := exec.Command("gh", "issue", "view", n, "--json", "title,body,number")
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return "", err
	}
	var is struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(out, &is); err != nil {
		return "", err
	}
	return fmt.Sprintf("GitHub issue #%d: %s\n\n%s", is.Number, is.Title, is.Body), nil
}

// copyIgnored brings ignored files (.env etc.) into the new worktree.
// copy = plain copy, clone = APFS clone (cp -c) for heavy dirs like node_modules.
func copyIgnored(from, to string, w config.Worktree) error {
	do := func(patterns []string, clone bool) error {
		for _, pat := range patterns {
			matches, _ := filepath.Glob(filepath.Join(from, pat))
			for _, m := range matches {
				rel, err := filepath.Rel(from, m)
				if err != nil {
					continue
				}
				dst := filepath.Join(to, rel)
				if _, err := os.Lstat(dst); err == nil {
					continue
				}
				_ = os.MkdirAll(filepath.Dir(dst), 0o755)
				args := []string{"-R", "-p", m, dst}
				if clone {
					args = []string{"-c", "-R", "-p", m, dst}
				}
				if out, err := exec.Command("cp", args...).CombinedOutput(); err != nil {
					if clone { // not APFS: fall back to a plain copy
						if out2, err2 := exec.Command("cp", "-R", "-p", m, dst).CombinedOutput(); err2 != nil {
							return fmt.Errorf("cp %s: %s", rel, out2)
						}
						continue
					}
					return fmt.Errorf("cp %s: %s", rel, out)
				}
			}
		}
		return nil
	}
	if err := do(w.Copy, false); err != nil {
		return err
	}
	return do(w.Clone, true)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// spawnAgent starts the agent window. resume = claude session id to resume
// ("continue" = most recent in cwd).
func (d *Daemon) spawnAgent(slug, prompt, resume string, w, h int) error {
	s, err := d.snapshot(slug)
	if err != nil {
		return err
	}
	ag := d.Global.Agents[s.Agent]
	cmd := ag.Command
	if s.Agent == "claude" && resume != "" && resume != "continue" && !claudeConversationExists(s.Path, resume) {
		// Claude Code only stores a conversation once a prompt was sent; an
		// id from SessionStart alone cannot be resumed
		resume = ""
		if len(pastClaudeSessions(s.Path)) > 0 {
			resume = "continue"
		}
	}
	switch {
	case resume == "continue" && s.Agent == "claude":
		cmd = ag.Command + " --continue"
	case resume != "" && ag.Resume != "":
		cmd = strings.ReplaceAll(ag.Resume, "{{session_id}}", resume)
	}
	pasteLater := ""
	if prompt != "" {
		if ag.Prompt == "paste" {
			pasteLater = prompt
		} else {
			cmd += " " + shellQuote(prompt)
		}
	}
	env := append([]string{}, s.Env...)
	env = append(env, "PEKOE_SESSION="+slug, "PEKOE_SOCK="+paths.Socket(), "PEKOE=1")
	if os.Getenv("PEKOE_HOME") != "" {
		env = append(env, "PEKOE_HOME="+os.Getenv("PEKOE_HOME"))
	}
	w, h = d.spawnSize(w, h)
	// Keep the window alive after the agent exits so the user sees why.
	// the pane stays after exit (remain-on-exit) and reports itself dead, so
	// the TUI can offer restart instead of typing into nothing
	full := cmd + `; printf '\n[pekoe] agent exited (%s). ↵ restart · x close\n' "$?"`
	if err := d.Eng.Spawn(engine.Spawn{Slug: slug, Window: engine.WinAgent, Dir: s.Path, Env: env, Command: full, Width: w, Height: h}); err != nil {
		return err
	}
	d.update(slug, func(x *state.Session) {
		x.Stopped = false
		x.Error = ""
		if x.State == state.Creating || x.State == state.Failed || x.State == state.Idle {
			x.State = state.Running
			if prompt == "" && resume == "" {
				x.State = state.Idle
			}
		}
	})
	if pasteLater != "" {
		go func() {
			time.Sleep(2 * time.Second)
			_ = d.Eng.SendText(slug, engine.WinAgent, pasteLater)
			_ = d.Eng.SendKeys(slug, engine.WinAgent, "Enter")
		}()
	}
	return nil
}

func (d *Daemon) hCancel(p slugP) (any, error) {
	d.mu.Lock()
	cancel := d.cancels[p.Slug]
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if s.State != state.Creating && s.State != state.Failed {
		return nil, errors.New("session is not being created")
	}
	return d.hRm(rmP{Slug: p.Slug, Force: true})
}

// ---- open / adopt ----

type openP struct {
	Path   string `json:"path"`
	Resume string `json:"resume"` // ask | resume | new
	Agent  string `json:"agent"`
	Prompt string `json:"prompt"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// claudeProjectDir mirrors how Claude Code stores transcripts per cwd.
func claudeProjectDir(path string) string {
	home, _ := os.UserHomeDir()
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		base = filepath.Join(home, ".claude")
	}
	enc := strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(path)
	return filepath.Join(base, "projects", enc)
}

type pastSession struct {
	ID       string    `json:"id"`
	Modified time.Time `json:"modified"`
}

func claudeConversationExists(path, id string) bool {
	_, err := os.Stat(filepath.Join(claudeProjectDir(path), id+".jsonl"))
	return err == nil
}

func pastClaudeSessions(path string) []pastSession {
	ents, err := os.ReadDir(claudeProjectDir(path))
	if err != nil {
		return nil
	}
	var out []pastSession
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, pastSession{ID: strings.TrimSuffix(e.Name(), ".jsonl"), Modified: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

func (d *Daemon) hOpen(p openP) (any, error) {
	top, err := gitx.Toplevel(p.Path)
	if err != nil {
		return nil, fmt.Errorf("%s is not a git worktree", p.Path)
	}
	repo, err := resolveRepo(top)
	if err != nil {
		return nil, err
	}
	d.resolved(repo)
	d.mu.Lock()
	for _, s := range d.sessions {
		if s.Path == top {
			d.mu.Unlock()
			return nil, fmt.Errorf("already managed as %s", s.Slug)
		}
	}
	d.mu.Unlock()
	agent := p.Agent
	if agent == "" {
		agent = "claude"
	}
	past := []pastSession{}
	if agent == "claude" {
		past = pastClaudeSessions(top)
	}
	if (p.Resume == "" || p.Resume == "ask") && len(past) > 0 {
		return map[string]any{"needs_choice": true, "past": past}, nil
	}
	branch := gitx.CurrentBranch(top)
	name := branch
	if name == "HEAD" || name == "" {
		name = filepath.Base(top)
	}
	slug := d.uniqueSlug(paths.Slug(name), repo.ID, repo.Name)
	if _, err := d.get(slug); err == nil {
		slug = d.uniqueSlug(paths.Slug(filepath.Base(top)), repo.ID+"#", repo.Name)
	}
	port, err := d.DB.AllocPort(repo.ID, slug, d.Global.Ports.Base, portBusy)
	if err != nil {
		return nil, err
	}
	base := repo.Config.Worktree.Base
	if base == "" {
		base = gitx.DefaultBase(repo.Dir)
	}
	vars := config.Vars{Slug: slug, Branch: branch, Path: top, Repo: repo.Name, Port: port}
	s := &state.Session{
		Slug: slug, Branch: branch, Repo: repo.Name, RepoID: repo.ID, RepoDir: repo.Dir, Path: top,
		Agent: agent, State: state.Idle, StateSince: time.Now(), CreatedAt: time.Now(), Port: port,
		Managed: paths.UnderWorktrees(top), Base: base, Test: state.Test{Status: "none"},
		Env: append(repo.Config.EnvList(vars), "PEKOE_PORT="+strconv.Itoa(port)),
	}
	d.add(s)
	if sha, err := gitx.SnapshotCommit(top, "pekoe snapshot #0"); err == nil {
		ref := fmt.Sprintf("refs/pekoe/%s/prompt-0", slug)
		_ = gitx.UpdateRef(top, ref, sha)
		_ = d.DB.AddPrompt(slug, 0, "(open)", ref)
	}
	resume := ""
	if p.Resume == "resume" && len(past) > 0 {
		resume = past[0].ID
	} else if p.Resume != "" && p.Resume != "new" && p.Resume != "ask" && p.Resume != "resume" {
		resume = p.Resume // explicit session id
	}
	if err := d.spawnAgent(slug, p.Prompt, resume, p.Width, p.Height); err != nil {
		d.update(slug, func(x *state.Session) { x.State = state.Failed; x.Error = err.Error() })
		return nil, err
	}
	return d.snapshot(slug)
}

type adoptP struct {
	Path    string `json:"path"`
	Confirm bool   `json:"confirm"` // stop the original process and resume inside pekoe
}

type adoptCandidate struct {
	PID       int    `json:"pid"`
	Cwd       string `json:"cwd"`
	Where     string `json:"where"` // tty or tmux target
	SessionID string `json:"session_id"`
}

// hAdopt finds agents started by hand in other tabs (or tmux sessions on the
// default server). A process cannot move between ptys, so adopting means
// stopping it and resuming the same Claude Code session inside pekoe.
func (d *Daemon) hAdopt(p adoptP) (any, error) {
	cands := findAgentProcesses()
	if p.Path != "" {
		top, _ := gitx.Toplevel(p.Path)
		var f []adoptCandidate
		for _, c := range cands {
			if c.Cwd == top || strings.HasPrefix(c.Cwd, top+"/") {
				f = append(f, c)
			}
		}
		cands = f
	}
	d.mu.Lock()
	managed := map[string]bool{}
	for _, s := range d.sessions {
		managed[s.Path] = true
	}
	d.mu.Unlock()
	var free []adoptCandidate
	for _, c := range cands {
		top, err := gitx.Toplevel(c.Cwd)
		if err != nil || managed[top] {
			continue
		}
		c.Cwd = top
		if ps := pastClaudeSessions(top); len(ps) > 0 {
			c.SessionID = ps[0].ID
		}
		free = append(free, c)
	}
	if !p.Confirm {
		return map[string]any{"candidates": free}, nil
	}
	var adopted []*state.Session
	for _, c := range free {
		if proc, err := os.FindProcess(c.PID); err == nil {
			_ = proc.Signal(os.Interrupt)
			time.Sleep(500 * time.Millisecond)
			_ = proc.Signal(os.Interrupt)
		}
		time.Sleep(time.Second)
		res, err := d.hOpen(openP{Path: c.Cwd, Resume: orNew(c.SessionID)})
		if err != nil {
			log.Printf("adopt %s: %v", c.Cwd, err)
			continue
		}
		if s, ok := res.(*state.Session); ok {
			adopted = append(adopted, s)
		}
	}
	return adopted, nil
}

func orNew(id string) string {
	if id == "" {
		return "new"
	}
	return id
}

func findAgentProcesses() []adoptCandidate {
	out, err := exec.Command("ps", "-axo", "pid=,tty=,comm=").Output()
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var res []adoptCandidate
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		comm := filepath.Base(f[len(f)-1])
		if pid == self || comm != "claude" {
			continue
		}
		if os.Getenv("PEKOE_TEST") == "" && f[1] == "??" {
			continue // no tty: not interactive
		}
		cwd := processCwd(pid)
		if cwd == "" {
			continue
		}
		if env := processHasPekoeEnv(pid); env {
			continue // already inside pekoe
		}
		res = append(res, adoptCandidate{PID: pid, Cwd: cwd, Where: f[1]})
	}
	return res
}

func processCwd(pid int) string {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(ln, "n") {
			return ln[1:]
		}
	}
	return ""
}

func processHasPekoeEnv(pid int) bool {
	out, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.Contains(string(out), "PEKOE_SESSION=")
}

// ---- input ----

type sendP struct {
	Slug   string `json:"slug"`
	Text   string `json:"text"`
	Submit bool   `json:"submit"`
	Window string `json:"window"`
}

func win(w string) string {
	if w == "" {
		return engine.WinAgent
	}
	return w
}

func (d *Daemon) hSend(p sendP) (any, error) {
	if _, err := d.get(p.Slug); err != nil {
		return nil, err
	}
	if err := d.Eng.SendText(p.Slug, win(p.Window), p.Text); err != nil {
		return nil, err
	}
	if p.Submit {
		// give the agent's input box a moment to take the paste
		time.Sleep(150 * time.Millisecond)
		if err := d.Eng.SendKeys(p.Slug, win(p.Window), "Enter"); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

type keysP struct {
	Slug    string   `json:"slug"`
	Window  string   `json:"window"`
	Keys    []string `json:"keys"`
	Literal string   `json:"literal"`
	Hex     []string `json:"hex"`
}

func (d *Daemon) hKeys(p keysP) (any, error) {
	if p.Slug == TapiSlug { // tapi chat has its own methods (tapi.keys)
		return nil, fmt.Errorf("no such session: %s", p.Slug)
	}
	return d.sendKeys(p)
}

func (d *Daemon) sendKeys(p keysP) (any, error) {
	w := win(p.Window)
	if p.Literal != "" {
		if err := d.Eng.SendLiteral(p.Slug, w, p.Literal); err != nil {
			return nil, err
		}
	}
	if len(p.Hex) > 0 {
		if err := d.Eng.SendHex(p.Slug, w, p.Hex); err != nil {
			return nil, err
		}
	}
	if len(p.Keys) > 0 {
		if err := d.Eng.SendKeys(p.Slug, w, p.Keys...); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

type answerP struct {
	Slug   string `json:"slug"`
	Answer string `json:"answer"` // allow | deny | always | y | n
	// Expect, when given, is the prompt the user was shown: the answer is
	// refused (errPromptChanged) unless the session still waits on that
	// tool. The web UI always sends it (web.go).
	Expect *answerExpect `json:"expect,omitempty"`
}

type answerExpect struct {
	Tool *state.Tool `json:"tool"`
}

var errPromptChanged = errors.New("the prompt changed; look again")

func (d *Daemon) sendKeySpec(slug, spec string) error {
	if spec == "" {
		return nil
	}
	if len([]rune(spec)) == 1 {
		return d.Eng.SendLiteral(slug, engine.WinAgent, spec)
	}
	return d.Eng.SendKeys(slug, engine.WinAgent, spec)
}

func (d *Daemon) hAnswer(p answerP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if s.State != state.Waiting {
		// never type a stray "1" into the agent's prompt
		return nil, fmt.Errorf("%s is not waiting (state: %s)", p.Slug, s.State)
	}
	if p.Expect != nil && !sameTool(s.PendingTool, p.Expect.Tool) {
		return nil, errPromptChanged
	}
	ag := d.Global.Agents[s.Agent]
	var spec string
	switch p.Answer {
	case "allow", "y", "yes":
		spec = ag.Allow
	case "always", "a", "A":
		spec = ag.Always
	case "deny", "n", "no":
		spec = ag.Deny
	default:
		return nil, fmt.Errorf("answer must be allow, deny or always")
	}
	if s.PendingTool != nil && s.PendingTool.Name == "Trust" {
		// folder trust menu: the cursor starts on "No, exit"
		switch spec {
		case ag.Allow, ag.Always:
			err = d.Eng.SendKeys(p.Slug, engine.WinAgent, "Down", "Enter")
		default:
			err = d.Eng.SendKeys(p.Slug, engine.WinAgent, "Escape")
		}
	} else {
		err = d.sendKeySpec(p.Slug, spec)
	}
	if err != nil {
		return nil, err
	}
	next := state.Running
	if p.Answer == "deny" || p.Answer == "n" || p.Answer == "no" {
		next = state.Idle
	}
	if s.PendingTool != nil && s.PendingTool.Name == "Trust" {
		next = state.Idle // UserPromptSubmit moves it to running if a prompt follows
	}
	return d.update(p.Slug, func(x *state.Session) {
		if x.State == state.Waiting {
			x.State = next
			x.WaitKind = ""
			x.PendingTool = nil
		}
	})
}

type screenP struct {
	Slug   string `json:"slug"`
	Window string `json:"window"`
}

func (d *Daemon) hScreen(p screenP) (any, error) {
	if _, err := d.get(p.Slug); err != nil {
		return nil, err
	}
	return d.Eng.Capture(p.Slug, win(p.Window))
}

// spawnSize picks the pty size for a new agent window. Callers that don't
// know the pane (the CLI run from a non-terminal, e.g. by an agent) pass 0;
// the TUI's last reported pane size is used then, so the agent's first
// render already wraps at the width it will be shown in.
func (d *Daemon) spawnSize(w, h int) (int, int) {
	if w > 0 && h > 0 {
		return w, h
	}
	d.mu.Lock()
	pw, ph := d.paneW, d.paneH
	d.mu.Unlock()
	if pw > 0 && ph > 0 {
		return pw, ph
	}
	return 120, 40
}

type resizeP struct {
	Slug   string `json:"slug"`
	Window string `json:"window"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func (d *Daemon) hResize(p resizeP) (any, error) {
	if p.Slug == "*" { // resize every agent window (TUI layout change)
		d.mu.Lock()
		d.paneW, d.paneH = p.Width, p.Height
		slugs := make([]string, 0, len(d.sessions))
		for k := range d.sessions {
			slugs = append(slugs, k)
		}
		d.mu.Unlock()
		for _, s := range slugs {
			for _, w := range []string{engine.WinAgent, engine.WinReview, engine.WinDev, engine.WinRun} {
				_ = d.Eng.Resize(s, w, p.Width, p.Height)
			}
		}
		_ = d.Eng.Resize(TapiSlug, engine.WinAgent, p.Width, p.Height) // tapi chat shows in the same pane
		return nil, nil
	}
	return nil, d.Eng.Resize(p.Slug, win(p.Window), p.Width, p.Height)
}

type seenP struct {
	Slug  string `json:"slug"`
	Lines int    `json:"lines"` // absolute line offset at which the user left
}

func (d *Daemon) hSeen(p seenP) (any, error) {
	d.mu.Lock()
	if o := d.out[p.Slug]; o != nil {
		o.unread = 0
	}
	d.mu.Unlock()
	return d.update(p.Slug, func(s *state.Session) {
		if p.Lines > 0 {
			s.SeenLines = p.Lines
		}
	})
}

func (d *Daemon) hKill(p slugP) (any, error) {
	if _, err := d.get(p.Slug); err != nil {
		return nil, err
	}
	_ = d.Eng.KillSession(p.Slug)
	return d.update(p.Slug, func(s *state.Session) {
		s.Stopped = true
		s.Dev = false
		if s.State == state.Running || s.State == state.Waiting {
			s.State = state.Idle
		}
	})
}

type restartP struct {
	Slug   string `json:"slug"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// hRestart starts the agent again, resuming its last Claude Code session.
func (d *Daemon) hRestart(p restartP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	resume := s.ClaudeSessionID
	if resume == "" && s.Agent == "claude" && len(pastClaudeSessions(s.Path)) > 0 {
		resume = "continue"
	}
	if err := d.spawnAgent(p.Slug, "", resume, p.Width, p.Height); err != nil {
		return nil, err
	}
	return d.snapshot(p.Slug)
}

type undoP struct {
	Slug string `json:"slug"`
	N    int    `json:"n"`
}

// hUndo restores the working tree to the snapshot taken when prompt n was
// submitted (default: the last prompt), i.e. throws that prompt's changes away.
func (d *Daemon) hUndo(p undoP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	prompts, _ := d.DB.Prompts(p.Slug)
	if len(prompts) == 0 {
		return nil, errors.New("no snapshots")
	}
	target := prompts[len(prompts)-1]
	if p.N > 0 {
		found := false
		for _, pr := range prompts {
			if pr.N == p.N {
				target, found = pr, true
			}
		}
		if !found {
			return nil, fmt.Errorf("no snapshot #%d", p.N)
		}
	}
	// keep a safety ref of the current state
	if sha, err := gitx.SnapshotCommit(s.Path, "pekoe before undo"); err == nil {
		_ = gitx.UpdateRef(s.Path, fmt.Sprintf("refs/pekoe/%s/undo-%d", p.Slug, time.Now().Unix()), sha)
	}
	if err := gitx.RestoreSnapshot(s.Path, target.Ref); err != nil {
		return nil, err
	}
	d.refreshDiffstat(p.Slug)
	return map[string]any{"restored": target.N, "prompt": target.Text}, nil
}

// ---- attention queue ----

type fromP struct {
	From string `json:"from"`
}

// hAttentionNext: sessions waiting for the user (oldest wait first), then
// sessions whose PR changed in a way that wants a look (PRStatus.Attention:
// CI failed, changes requested, new comment, mergeable; oldest first).
func (d *Daemon) hAttentionNext(p fromP) (any, error) {
	list, _ := d.hList(listP{})
	var waiting, pr []*state.Session
	for _, s := range list.([]*state.Session) {
		switch {
		case s.State == state.Waiting:
			waiting = append(waiting, s)
		case s.PRStatus != nil && len(s.PRStatus.Attention) > 0:
			pr = append(pr, s)
		}
	}
	sort.Slice(waiting, func(i, j int) bool { return waiting[i].StateSince.Before(waiting[j].StateSince) })
	sort.Slice(pr, func(i, j int) bool { return pr[i].PRStatus.AttentionAt.Before(pr[j].PRStatus.AttentionAt) })
	queue := append(waiting, pr...)
	if len(queue) == 0 {
		return nil, nil
	}
	// cycle: the first that is not the one we are on
	for _, s := range queue {
		if s.Slug != p.From {
			return s, nil
		}
	}
	return queue[0], nil
}

func (d *Daemon) hReviewNext(p fromP) (any, error) {
	list, _ := d.hList(listP{})
	var done []*state.Session
	for _, s := range list.([]*state.Session) {
		if s.State == state.Done {
			done = append(done, s)
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].StateSince.Before(done[j].StateSince) })
	for _, s := range done {
		if s.Slug != p.From {
			return s, nil
		}
	}
	if len(done) > 0 {
		return done[0], nil
	}
	return nil, nil
}

// ---- restore after daemon restart ----

func (d *Daemon) restore() {
	list, err := d.DB.Sessions()
	if err != nil {
		log.Printf("restore: %v", err)
		return
	}
	alive, _ := d.Eng.Sessions()
	d.mu.Lock()
	for _, s := range list {
		d.sessions[s.Slug] = s
		d.out[s.Slug] = &outState{}
	}
	d.mu.Unlock()
	// before anything reads per-repo config or files by repo-id
	d.realignRepoIDs()
	for _, s := range list {
		if s.State == state.Landed {
			continue
		}
		if s.State == state.Creating {
			d.update(s.Slug, func(x *state.Session) { x.State = state.Failed; x.Error = "interrupted by daemon restart" })
			continue
		}
		// Kept (as failed) rather than dropped even for managed worktrees: the
		// directory may be only temporarily unreachable (unmounted volume,
		// renamed parent), and dropping it would lose prompts and review
		// history. Since persist/forget, rm no longer leaves such rows behind.
		if _, err := os.Stat(s.Path); err != nil {
			d.update(s.Slug, func(x *state.Session) {
				x.State = state.Failed
				x.Error = "worktree missing: " + s.Path + " (pekoe rm " + s.Slug + " to forget it)"
			})
			continue
		}
		d.refreshBranch(s.Slug)
		hasAgent, hasRun := false, false
		for _, w := range alive[s.Slug] {
			if w == engine.WinAgent {
				hasAgent = true
			}
			if w == engine.WinDev {
				s.Dev = true
			}
			if w == engine.WinRun {
				hasRun = true
			}
		}
		if s.Run != "" && !hasRun { // the tmux server went away with it
			d.update(s.Slug, func(x *state.Session) { x.Run = "" })
		}
		if hasAgent || s.Stopped {
			continue
		}
		// the pty is gone (tmux server restarted): resume the agent session
		resume := s.ClaudeSessionID
		if err := d.spawnAgent(s.Slug, "", resume, 0, 0); err != nil {
			log.Printf("resume %s: %v", s.Slug, err)
			continue
		}
		d.update(s.Slug, func(x *state.Session) {
			if x.State == state.Running || x.State == state.Waiting {
				x.State = state.Idle
			}
		})
	}
}
