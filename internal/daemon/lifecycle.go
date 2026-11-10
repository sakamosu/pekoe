package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/gitx"
	"pekoe/internal/initdetect"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// ---- worktree list ----

type WorktreeView struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Slug    string `json:"slug,omitempty"`
	Port    int    `json:"port,omitempty"`
	Managed bool   `json:"managed"`
	Main    bool   `json:"main"`
	State   string `json:"state,omitempty"`
}

type wtListP struct {
	RepoID string `json:"repo_id"`
	Repo   string `json:"repo"` // a path inside the repository
}

func (d *Daemon) hWorktreeList(p wtListP) (any, error) {
	repoDirs := map[string]bool{}
	if p.Repo != "" {
		if r, err := resolveRepo(p.Repo); err == nil {
			repoDirs[r.Dir] = true
		}
	}
	d.mu.Lock()
	bySlugPath := map[string]*state.Session{}
	for _, s := range d.sessions {
		if p.RepoID == "" || s.RepoID == p.RepoID {
			if p.Repo == "" {
				repoDirs[s.RepoDir] = true
			}
			c := *s
			bySlugPath[s.Path] = &c
		}
	}
	d.mu.Unlock()
	out := []WorktreeView{}
	for dir := range repoDirs {
		wts, err := gitx.Worktrees(dir)
		if err != nil {
			continue
		}
		for i, w := range wts {
			if w.Bare {
				continue
			}
			v := WorktreeView{Path: w.Path, Branch: w.Branch, Main: i == 0, Managed: paths.UnderWorktrees(w.Path)}
			if s := bySlugPath[w.Path]; s != nil {
				v.Slug, v.Port, v.State = s.Slug, s.Port, string(s.State)
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// ---- rm: teardown → remove worktree (only under ~/worktrees) ----

type rmP struct {
	Slug  string `json:"slug"`
	Force bool   `json:"force"`
}

func (d *Daemon) teardown(s *state.Session) error {
	cfg := d.repoConfig(s)
	v := d.vars(s)
	for _, c := range cfg.Hooks.Teardown {
		cmd := v.ExpandShell(c)
		if out, err := shell(context.Background(), s.Path, s.Env, cmd, 10*time.Minute); err != nil {
			return fmt.Errorf("teardown %q: %v\n%s", cmd, err, tail(out, 10))
		}
	}
	return nil
}

func (d *Daemon) hRm(p rmP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	_, statErr := os.Stat(s.Path)
	exists := statErr == nil
	// the directory may belong to another repository of the same name (a
	// fork shares ~/worktrees/<name>): then it is not ours to run hooks in or
	// to delete
	owner := error(nil)
	if exists {
		owner = gitx.CheckWorktreeOf(s.RepoDir, s.Path)
	}
	foreign := errors.Is(owner, gitx.ErrForeignWorktree)
	if !p.Force && s.Managed && !foreign {
		if out, _ := gitx.Run(s.Path, "status", "--porcelain"); out != "" {
			return nil, fmt.Errorf("%s has uncommitted changes (use force)", s.Path)
		}
	}
	_ = d.Eng.KillSession(p.Slug)
	deleted := false
	if exists && foreign {
		log.Printf("rm %s: %s is a worktree of another repository; left as is", p.Slug, s.Path)
	} else if exists {
		if err := d.teardown(s); err != nil && !p.Force {
			return nil, err
		}
		gitx.DeleteRefs(s.Path, "refs/pekoe/"+p.Slug+"/")
		if s.Managed && paths.UnderWorktrees(s.Path) {
			if owner == nil {
				deleted = gitx.RemoveWorktree(s.RepoDir, s.Path, true) == nil
			}
			if !deleted && s.OwnDir {
				// not registered (half-created): delete the directory this
				// session made, and nothing else
				deleted = os.RemoveAll(s.Path) == nil
				_, _ = gitx.Run(s.RepoDir, "worktree", "prune")
			}
		}
	}
	// out of memory and state.db together, before anything else can save it
	// again (an update racing with rm used to write it back: see persist)
	d.forget(p.Slug)
	// picks addressed to this session go with it (no leftovers)
	if picks, err := d.DB.Picks(1000); err == nil {
		for _, pk := range picks {
			if pk.Slug == p.Slug && pk.RepoID == s.RepoID {
				os.Remove(pk.File)
				if pk.Image != "" {
					os.Remove(pk.Image)
				}
				_ = d.DB.DeletePick(pk.ID)
			}
		}
	}
	d.catch.Forget(p.Slug)
	removeAttachments(s.RepoID, p.Slug)
	d.dropPreview(s)
	d.emit("session.removed", s)
	return map[string]any{"removed": p.Slug, "worktree_deleted": deleted}, nil
}

// ---- dev server ----

func (d *Daemon) hDevToggle(p slugP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if d.Eng.Alive(p.Slug, engine.WinDev) {
		_ = d.Eng.Kill(p.Slug, engine.WinDev)
		return d.update(p.Slug, func(x *state.Session) { x.Dev = false })
	}
	cfg := d.repoConfig(s)
	if cfg.Hooks.Dev == "" {
		return nil, fmt.Errorf("no [hooks] dev command in %s", paths.RepoConfig(s.RepoID))
	}
	cmd := d.vars(s).ExpandShell(cfg.Hooks.Dev)
	full := cmd + `; printf '\n[pekoe] dev server exited (%s)\n' "$?"`
	if err := d.Eng.Spawn(engine.Spawn{Slug: p.Slug, Window: engine.WinDev, Dir: s.Path, Env: append(s.Env, "PEKOE_SESSION="+p.Slug), Command: full, Width: 120, Height: 40}); err != nil {
		return nil, err
	}
	return d.update(p.Slug, func(x *state.Session) { x.Dev = true })
}

// ---- run: one command in the worktree (TUI `!`) ----

type runP struct {
	Slug    string `json:"slug"`
	Command string `json:"command"` // empty: only report the run window's state
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

// RunState is what run.start and run.close answer: whether the session's
// run window is still running, or holds a finished command waiting to be
// closed, which command it is (the daemon keeps it: every TUI, and one
// restarted meanwhile, names the same) and how it ended. Started is set
// when this call spawned it.
type RunState struct {
	Running    bool   `json:"running"`
	Finished   bool   `json:"finished"`
	Started    bool   `json:"started"`
	Command    string `json:"command,omitempty"`
	ExitStatus int    `json:"exit_status"`      // -1: still running, unknown, or killed by Signal
	Signal     string `json:"signal,omitempty"` // "SIGINT" when Ctrl-C ended it
}

// runState reads the session's run window. Callers hold runMu.
func (d *Daemon) runState(s *state.Session) RunState {
	st := RunState{Command: s.Run, ExitStatus: -1}
	if d.Eng.Alive(s.Slug, engine.WinRun) {
		st.Running = true
		return st
	}
	if sc, err := d.Eng.Capture(s.Slug, engine.WinRun); err == nil && sc.Dead {
		st.Finished = true
		st.ExitStatus, st.Signal = sc.ExitStatus, sc.Signal
	}
	return st
}

// hRunStart runs one command in a pty of its own, in the worktree with the
// dev server's environment, through the user's shell. A session has at most
// one run window: while one is running (or finished but not closed), the
// call starts nothing and says so, and the TUI shows that window instead.
// Calls are serialized (runMu): Enter pressed twice must not let the second
// Spawn kill the first run and start the command again.
func (d *Daemon) hRunStart(p runP) (any, error) {
	d.runMu.Lock()
	defer d.runMu.Unlock()
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if st := d.runState(s); st.Running || st.Finished {
		return st, nil
	}
	cmd := strings.TrimSpace(p.Command)
	if cmd == "" {
		return RunState{ExitStatus: -1}, nil
	}
	// never outside the worktree: tmux starts in $HOME when -c is missing
	switch s.State {
	case state.Creating:
		return nil, fmt.Errorf("%s is still being created", p.Slug)
	case state.Landed:
		return nil, fmt.Errorf("%s has landed; its worktree is gone", p.Slug)
	}
	if fi, err := os.Stat(s.Path); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("worktree missing: %s", s.Path)
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	w, h := d.spawnSize(p.Width, p.Height)
	full := "/bin/sh -c " + shellQuote(runScript(sh, cmd, w, h))
	env := append(append([]string{}, s.Env...), "PEKOE_SESSION="+p.Slug)
	if err := d.Eng.Spawn(engine.Spawn{Slug: p.Slug, Window: engine.WinRun, Dir: s.Path, Env: env, Command: full, Width: w, Height: h, Quiet: true}); err != nil {
		return nil, err
	}
	d.update(p.Slug, func(x *state.Session) { x.Run = cmd })
	return RunState{Running: true, Started: true, Command: cmd, ExitStatus: -1}, nil
}

// hRunClose closes the run window (finished or not) and answers how it
// ended, for the TUI's "tell the agent?".
func (d *Daemon) hRunClose(p slugP) (any, error) {
	d.runMu.Lock()
	defer d.runMu.Unlock()
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	st := d.runState(s)
	_ = d.Eng.Kill(p.Slug, engine.WinRun)
	if s.Run != "" {
		d.update(p.Slug, func(x *state.Session) { x.Run = "" })
	}
	return st, nil
}

// runScript is the run window's sh script. It waits (up to 1 s) for the pty
// to get its size: tmux makes the window at the session's size and resizes
// it (and sets Quiet's option) right after, and a command over before that
// (echo) would have its output pushed into the scrollback by the shrink.
// Then it echoes the command (dim) so the window says what ran, and hands
// the pty to the user's shell, whose exit status becomes the pane's.
func runScript(sh, cmd string, w, h int) string {
	return fmt.Sprintf(`i=0; while [ "$(stty size 2>/dev/null)" != "%d %d" ] && [ $i -lt 20 ]; do sleep 0.05; i=$((i+1)); done; `, h, w) +
		`printf '\033[2m$ %s\033[0m\n' ` + shellQuote(cmd) + "; exec " + shellQuote(sh) + " -c " + shellQuote(cmd)
}

// ---- land: rebase → test → push → gh pr create; teardown after merge ----

func (d *Daemon) hLand(p slugP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if s.PRReview {
		return nil, errors.New("this is someone else's PR; nothing to land")
	}
	if out, _ := gitx.Run(s.Path, "status", "--porcelain"); out != "" {
		return nil, errors.New("uncommitted changes in the worktree: ask the agent to commit first")
	}
	base := s.Base
	if base == "" {
		base = gitx.DefaultBase(s.Path)
	}
	if err := gitx.CheckRefArg("base", base); err != nil {
		return nil, err
	}
	if err := gitx.CheckRefArg("branch", s.Branch); err != nil {
		return nil, err
	}
	hasOrigin := gitx.Origin(s.Path) != ""
	steps := []string{}
	onto := base
	if hasOrigin {
		if _, err := gitx.Run(s.Path, "fetch", "--end-of-options", "origin", base); err == nil {
			onto = "origin/" + base
		}
	}
	if _, err := gitx.Run(s.Path, "rebase", "--end-of-options", onto); err != nil {
		_, _ = gitx.Run(s.Path, "rebase", "--abort")
		return nil, fmt.Errorf("rebase onto %s failed (aborted): %v", onto, err)
	}
	steps = append(steps, "rebased onto "+onto)
	cfg := d.repoConfig(s)
	if cfg.Hooks.Test != "" {
		cmd := d.vars(s).ExpandShell(cfg.Hooks.Test)
		out, err := shell(context.Background(), s.Path, s.Env, cmd, 30*time.Minute)
		t := state.Test{Status: "pass", Summary: summarize(out, err == nil), At: time.Now()}
		if err != nil {
			t.Status = "fail"
		}
		d.update(p.Slug, func(x *state.Session) { x.Test = t })
		if err != nil {
			return nil, fmt.Errorf("tests failed: %s", t.Summary)
		}
		steps = append(steps, "tests passed")
	}
	if !hasOrigin {
		// local-only repository: fast-forward the base branch in the main worktree
		mainBranch := gitx.CurrentBranch(s.RepoDir)
		if mainBranch == base {
			if _, err := gitx.Run(s.RepoDir, "merge", "--ff-only", "--end-of-options", s.Branch); err != nil {
				return nil, err
			}
		} else if _, err := gitx.Run(s.RepoDir, "branch", "-f", "--end-of-options", base, s.Branch); err != nil {
			return nil, err
		}
		steps = append(steps, "fast-forwarded "+base)
		if err := d.finishLanding(p.Slug); err != nil {
			return nil, err
		}
		return map[string]any{"steps": steps, "landed": true}, nil
	}
	if _, err := gitx.Run(s.Path, "push", "-u", "--force-with-lease", "--end-of-options", "origin", "HEAD:"+s.Branch); err != nil {
		return nil, err
	}
	steps = append(steps, "pushed "+s.Branch)
	pr := s.PR
	if pr == 0 {
		c := exec.Command("gh", "pr", "create", "--fill", "--head", s.Branch, "--base", base)
		c.Dir = s.Path
		out, err := c.CombinedOutput()
		if err != nil && !strings.Contains(string(out), "already exists") {
			return nil, fmt.Errorf("gh pr create: %s", strings.TrimSpace(string(out)))
		}
		pr = prNumber(string(out))
		if pr == 0 {
			pr = prForBranch(s.Path, s.Branch)
		}
		steps = append(steps, fmt.Sprintf("PR #%d", pr))
	}
	d.update(p.Slug, func(x *state.Session) { x.PR = pr; x.Landing = true })
	return map[string]any{"steps": steps, "pr": pr}, nil
}

var prURLRe = regexp.MustCompile(`/pull/(\d+)`)

func prNumber(s string) int {
	if m := prURLRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func prForBranch(dir, branch string) int {
	c := exec.Command("gh", "pr", "view", branch, "--json", "number")
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return 0
	}
	var v struct {
		Number int `json:"number"`
	}
	json.Unmarshal(out, &v)
	return v.Number
}

func (d *Daemon) finishLanding(slug string) error {
	s, err := d.snapshot(slug)
	if err != nil {
		return err
	}
	_ = d.Eng.KillSession(slug)
	if errors.Is(gitx.CheckWorktreeOf(s.RepoDir, s.Path), gitx.ErrForeignWorktree) {
		log.Printf("land %s: %s is a worktree of another repository; left as is", slug, s.Path)
	} else {
		if err := d.teardown(s); err != nil {
			log.Printf("teardown %s: %v", slug, err)
		}
		gitx.DeleteRefs(s.Path, "refs/pekoe/"+slug+"/")
	}
	if s.Managed && paths.UnderWorktrees(s.Path) {
		if err := gitx.RemoveWorktree(s.RepoDir, s.Path, false); err != nil {
			log.Printf("remove worktree %s: %v", s.Path, err)
		}
	}
	d.update(slug, func(x *state.Session) {
		x.State, x.Landing, x.Dev, x.Stopped = state.Landed, false, false, true
	})
	return nil
}

// watchPRs fills in the PR number of sessions whose branch got a PR outside
// pekoe (e.g. the agent ran `gh pr create`), so the list can show it.
func (d *Daemon) watchPRs() {
	d.mu.Lock()
	var check []state.Session
	for _, s := range d.sessions {
		if s.PR == 0 && !s.PRReview && s.State != state.Landed &&
			s.Branch != "" && s.Branch != s.Base && s.Path != "" {
			check = append(check, *s)
		}
	}
	d.mu.Unlock()
	for _, s := range check {
		if pr := prForBranch(s.Path, s.Branch); pr > 0 {
			d.update(s.Slug, func(x *state.Session) {
				if x.PR == 0 {
					x.PR = pr
				}
			})
		}
	}
}

// watchMerges detects merged PRs of landing sessions and tears them down.
func (d *Daemon) watchMerges() {
	d.mu.Lock()
	var landing []*state.Session
	for _, s := range d.sessions {
		if s.Landing && s.PR > 0 {
			c := *s
			landing = append(landing, &c)
		}
	}
	d.mu.Unlock()
	for _, s := range landing {
		c := exec.Command("gh", "pr", "view", strconv.Itoa(s.PR), "--json", "state")
		c.Dir = s.RepoDir
		out, err := c.Output()
		if err != nil {
			continue
		}
		var v struct {
			State string `json:"state"`
		}
		json.Unmarshal(out, &v)
		if v.State == "MERGED" {
			if err := d.finishLanding(s.Slug); err != nil {
				log.Printf("land %s: %v", s.Slug, err)
			}
		}
	}
}

// ---- editor.open (MCP → nvim) ----

type editorP struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Note string `json:"note"`
	Slug string `json:"slug"`
}

func (d *Daemon) hEditorOpen(p editorP) (any, error) {
	if p.Path == "" {
		return nil, errors.New("path required")
	}
	if !filepath.IsAbs(p.Path) && p.Slug != "" {
		if s, err := d.snapshot(p.Slug); err == nil {
			p.Path = filepath.Join(s.Path, p.Path)
		}
	}
	d.emit("editor.open", p)
	return map[string]any{"ok": true}, nil
}

// ---- pekoe init ----

type initP struct {
	Path string `json:"path"`
}

func (d *Daemon) hInitDetect(p initP) (any, error) {
	repo, err := resolveRepo(p.Path)
	if err != nil {
		return nil, err
	}
	d.resolved(repo)
	return initdetect.Detect(repo.Dir, repo.ID)
}

type initWriteP struct {
	Path string `json:"path"`
	TOML string `json:"toml"`
	Over bool   `json:"overwrite"`
}

func (d *Daemon) hInitWrite(p initWriteP) (any, error) {
	repo, err := resolveRepo(p.Path)
	if err != nil {
		return nil, err
	}
	d.resolved(repo)
	dst := paths.RepoConfig(repo.ID)
	if _, err := os.Stat(dst); err == nil && !p.Over {
		return nil, fmt.Errorf("%s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), paths.DirPerm); err != nil {
		return nil, err
	}
	return map[string]any{"path": dst}, os.WriteFile(dst, []byte(p.TOML), paths.FilePerm)
}

// ---- review surface (nvim in the session's review window) ----

type surfaceP struct {
	Slug   string `json:"slug"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// hReviewSurface swaps the right pane to nvim (claude keeps running in its
// own window). nvim gets PEKOE_SESSION and opens :PekoeReview by itself.
func (d *Daemon) hReviewSurface(p surfaceP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("nvim"); err != nil {
		return nil, errors.New("nvim not found; set [review] surface = \"tui\"")
	}
	d.hReviewStart(slugP{Slug: p.Slug})
	if d.Eng.Alive(p.Slug, engine.WinReview) {
		return nil, nil
	}
	cmd := "nvim"
	env := append(append([]string{}, s.Env...), "PEKOE_SESSION="+p.Slug, "PEKOE_REVIEW=1", "PEKOE_SOCK="+paths.Socket())
	// The plugin directory is pekoed's own, never the caller's: nvim reads
	// "|" in an Ex command as a separator ("set rtp+=/x | !cmd" would run
	// cmd outside the agent's sandbox). It reaches nvim through the
	// environment, so no path is spliced into the command. After the
	// user's config: plugin managers like lazy.nvim reset rtp at startup.
	if rtp := nvimPluginDir(); rtp != "" {
		env = append(env, "PEKOE_NVIM_PLUGIN="+rtp)
		cmd += " -c " + shellQuote(`lua vim.opt.rtp:append(vim.env.PEKOE_NVIM_PLUGIN); vim.cmd.runtime("plugin/pekoe.lua")`)
	}
	return nil, d.Eng.Spawn(engine.Spawn{Slug: p.Slug, Window: engine.WinReview, Dir: s.Path, Env: env, Command: cmd, Width: p.Width, Height: p.Height})
}

func nvimPluginDir() string {
	if v := os.Getenv("PEKOE_NVIM_RTP"); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	for _, c := range []string{filepath.Join(filepath.Dir(exe), "..", "nvim"), filepath.Join(filepath.Dir(exe), "..", "share", "pekoe", "nvim")} {
		if _, err := os.Stat(filepath.Join(c, "lua", "pekoe", "init.lua")); err == nil {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

type windowP struct {
	Slug   string `json:"slug"`
	Window string `json:"window"`
}

func (d *Daemon) hWindowKill(p windowP) (any, error) {
	if p.Window == engine.WinAgent {
		return nil, errors.New("use session.kill for the agent window")
	}
	_ = d.Eng.Kill(p.Slug, p.Window)
	if p.Window == engine.WinReview {
		// leaving the review surface: back to done unless comments are pending
		_, _ = d.hReviewEnd(slugP{Slug: p.Slug})
	}
	if p.Window == engine.WinDev {
		d.update(p.Slug, func(s *state.Session) { s.Dev = false })
	}
	if p.Window == engine.WinRun {
		_, err := d.hRunClose(slugP{Slug: p.Slug})
		return nil, err
	}
	return nil, nil
}
